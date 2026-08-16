//! Where the four embedded-PostgreSQL calls are emitted, and in what order.
//!
//! Every one of them is a whole-program property that only holds if generated
//! `main` says so, and each has a way of going wrong that no other gate sees:
//!
//! - `rt.MaybeStartEmbeddedPostgres()` must run FROM `main`. `[database] path` /
//!   `url` reach the runtime as `rt.SetSkyDefault("DB_PATH", …)` in the prologue
//!   `init()`, and Go runs every `init()` before `main` — so from a second
//!   `init()` the `--embed` ambiguity check would stop seeing them and an app
//!   configured with a DSN could start a cluster anyway.
//!   (`runtime-go/rt/pg_embed_test.go`'s
//!   `TestASkyTomlDatabasePathIsSeenAsAConflict` is the runtime half.)
//! - `rt.MaybeApplyEmbeddedMigrationsAndExit()` must run AFTER it, and it too
//!   must be in `main`. It shipped inside the generated `embedded_migrations.go`
//!   `init()`, where — by the same rule — it ran before the cluster existed, so
//!   `SKY_DB_OP=migrate ./app --embed` could not work at all.
//!
//! Those two constraints pull in opposite directions and `main` is the only
//! placement that satisfies both, which is precisely why the order needs pinning
//! rather than documenting. This drives the real emitter (`emit_example_source`,
//! the same path `sky build` takes) and reads the Go it produced; it needs no Go
//! toolchain, because nothing is built.

use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

fn scratch_project(tag: &str, main_src: &str) -> PathBuf {
    let uniq = format!(
        "sky-embed-prologue-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"embed-prologue\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

const HELLO: &str = "module Main exposing (main)\n\
                     \n\
                     import Std.Log exposing (println)\n\
                     \n\
                     main =\n\
                     \x20   println \"hi\"\n";

/// The body of `func main()` in the emitted Go.
fn emitted_main_body(source: &str) -> String {
    let start = source
        .find("\nfunc main() {")
        .unwrap_or_else(|| panic!("the emitted Go has no `func main()`:\n{source}"));
    let rest = &source[start + 1..];
    let end = rest
        .find("\n}\n")
        .unwrap_or_else(|| panic!("`func main()` is unterminated:\n{rest}"));
    rest[..end].to_string()
}

#[test]
fn the_four_embedded_postgres_calls_are_emitted_into_main_in_order() {
    let repo = repo_root();
    let project = scratch_project("order", HELLO);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    let body = emitted_main_body(&source);

    let want = [
        "defer rt.LogPanicAndExit()",
        "rt.MaybeStartEmbeddedPostgres()",
        "defer rt.StopEmbeddedPostgres()",
        "rt.MaybeApplyEmbeddedMigrationsAndExit()",
    ];
    let mut at = Vec::new();
    for call in want {
        let i = body
            .find(call)
            .unwrap_or_else(|| panic!("`func main()` never calls {call}:\n{body}"));
        at.push(i);
    }
    assert!(
        at.windows(2).all(|w| w[0] < w[1]),
        "the embedded-PostgreSQL calls are out of order in `func main()`.\n\
         Expected, top to bottom: {want:?}\n\
         Got:\n{body}"
    );

    // The migration cannot run before the cluster it migrates. `main` is a
    // straight-line prologue, so "after" is a text position — but only because
    // the start call is NOT itself deferred. Assert that too, or moving one word
    // would invert the runtime order while leaving the text order intact.
    assert!(
        body.contains("\n\trt.MaybeStartEmbeddedPostgres()"),
        "the start call must run inline, not deferred — a deferred start would \
         run AFTER the migration despite appearing before it:\n{body}"
    );
    assert!(
        body.contains("\n\trt.MaybeApplyEmbeddedMigrationsAndExit()"),
        "the migration call must run inline, not deferred:\n{body}"
    );
}

/// The counterpart to the ordering gate: neither call may be emitted into an
/// `init()`, whatever else the program contains. Go orders `init()`s by filename
/// and runs all of them before `main`, so an `init()` placement is not a
/// different-but-equivalent choice — it is the bug, for the start call and for
/// the migration call in turn.
#[test]
fn no_init_in_the_emitted_go_starts_a_cluster_or_migrates() {
    let repo = repo_root();
    let project = scratch_project("noinit", HELLO);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    let main_at = source.find("\nfunc main() {").expect("no `func main()`");
    let before_main = &source[..main_at];
    for call in [
        "MaybeStartEmbeddedPostgres",
        "MaybeApplyEmbeddedMigrationsAndExit",
    ] {
        // Exactly once, in the whole file. "Not before main" alone would be
        // satisfied by a call that is not emitted at all, which is the other way
        // this feature stops working.
        assert_eq!(
            source.matches(call).count(),
            1,
            "rt.{call}() must be emitted exactly once:\n{source}"
        );
        assert!(
            !before_main.contains(call),
            "{call} is emitted above `func main()` — i.e. into an init(), which runs \
             before main:\n{before_main}"
        );
    }
}

/// Guard against the gate above passing on a project that emitted nothing
/// interesting: assert the emitter really produced this program.
#[test]
fn the_scratch_project_actually_compiles_to_go() {
    let repo = repo_root();
    let project = scratch_project("sanity", HELLO);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);
    assert!(source.contains("package main"), "{source}");
    assert!(
        source.contains("\"hi\""),
        "the program's own body is missing:\n{source}"
    );
}
