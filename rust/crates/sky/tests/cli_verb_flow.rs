//! Member G of the Layer-2 corpus — the CLI verbs (`docs/ci-test-architecture-v2.md` §6, row G).
//!
//! **Why these are flow tests and not an app.** v2 §6: "Making a project
//! responsible for `sky doctor` couples a CLI verb's coverage to an app's build
//! health. Flow tests own the verbs directly, in-process, in seconds."
//!
//! **Why every assertion here is toolchain-free.** The existing `*_flow.rs`
//! tests early-`return` when `go` is absent from `PATH`, and CI's `test-rest`
//! job has no `actions/setup-go` step — so `db_flow`, `ffi_verb_flow`,
//! `profile_flow` and `doc_serve` all take that branch and report green having
//! asserted nothing. That is the "SKIP counted as pass" class living inside the
//! test suite itself. Everything below asserts on exit codes, usage text and
//! scaffolded files, so it has the same value on a bare CI runner as it does
//! locally.
//!
//! Verbs covered here: `init`, `clean`, `watch` (argument validation),
//! `install`, `update`, `upgrade`, `db` (dispatch + `init`), and unknown-verb
//! dispatch. `doctor` is owned by `doctor_flow.rs`, `doc` by `doc_flow.rs`,
//! `add`/`remove` by `ffi_verb_flow.rs`, `db migrate/push` by `db_flow.rs`.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Run `sky <args...>` in `dir` with stdin closed, so any interactive prompt
/// takes its non-TTY default. Returns (exit_code, stdout+stderr).
fn run_sky(dir: &Path, args: &[&str]) -> (i32, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

/// A unique empty scratch directory. Tests must never inherit the runner's cwd:
/// `sky clean` is `remove_dir_all` on `sky-out`/`.skycache`/`.skydeps`/`dist`
/// relative to cwd, with no project-root check.
fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-cli-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

#[test]
fn init_scaffolds_a_buildable_project_layout() {
    let dir = scratch("init");
    let (code, out) = run_sky(&dir, &["init", "myapp"]);
    assert_eq!(code, 0, "sky init should succeed; output:\n{out}");

    // The three files the verb's own help text promises unconditionally. The
    // rest of the scaffold (docker-compose.yml, .env.example, AGENTS.md,
    // CLAUDE.md) is deliberately NOT asserted: those are template-sourced and
    // documented as best-effort, and asserting them would make this test fail
    // for a reason that is not the verb's contract.
    for f in ["sky.toml", "src/Main.sky", ".gitignore"] {
        assert!(
            dir.join("myapp").join(f).is_file(),
            "sky init did not create {f}; output:\n{out}"
        );
    }

    // Whitespace-tolerant: the template aligns its `=` signs, so an exact
    // `entry = "..."` match is a test bug, not a scaffold bug.
    let toml = std::fs::read_to_string(dir.join("myapp/sky.toml")).unwrap();
    let declares_entry = toml.lines().any(|l| {
        let l = l.trim();
        !l.starts_with('#')
            && l.starts_with("entry")
            && l.contains('=')
            && l.contains("src/Main.sky")
    });
    assert!(
        declares_entry,
        "scaffolded sky.toml must point at the scaffolded entry; got:\n{toml}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn init_help_does_not_scaffold() {
    // Regression: `sky init --help` used to treat `--help` as the project name
    // and scaffold `./--help`.
    let dir = scratch("inithelp");
    let (code, out) = run_sky(&dir, &["init", "--help"]);
    assert_eq!(code, 0, "sky init --help should exit 0; output:\n{out}");
    assert!(
        out.contains("sky init"),
        "help text expected; got:\n{out}"
    );

    let entries: Vec<_> = std::fs::read_dir(&dir).unwrap().flatten().collect();
    assert!(
        entries.is_empty(),
        "sky init --help must not scaffold anything, found: {:?}",
        entries.iter().map(|e| e.file_name()).collect::<Vec<_>>()
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// clean
// ---------------------------------------------------------------------------

#[test]
fn clean_reports_and_removes_only_generated_dirs() {
    let dir = scratch("clean");

    // Nothing to do — and it must say so rather than claiming a removal.
    let (code, out) = run_sky(&dir, &["clean"]);
    assert_eq!(code, 0, "clean on an empty dir should succeed; got:\n{out}");
    assert!(
        out.contains("nothing to remove"),
        "expected the no-op message; got:\n{out}"
    );

    // Generated dirs go; a source dir and a file must survive. `sky clean` has
    // no project-root guard, so "removes ONLY the generated set" is the whole
    // safety property.
    std::fs::create_dir_all(dir.join("sky-out")).unwrap();
    std::fs::create_dir_all(dir.join(".skycache")).unwrap();
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n").unwrap();
    std::fs::write(dir.join("sky.toml"), "name = \"x\"\n").unwrap();

    let (code, out) = run_sky(&dir, &["clean"]);
    assert_eq!(code, 0, "clean should succeed; got:\n{out}");
    assert!(
        out.contains("removed") && out.contains("sky-out") && out.contains(".skycache"),
        "clean must name what it removed; got:\n{out}"
    );
    assert!(!dir.join("sky-out").exists(), "sky-out survived clean");
    assert!(!dir.join(".skycache").exists(), ".skycache survived clean");
    assert!(
        dir.join("src/Main.sky").is_file(),
        "clean deleted source — it must only remove generated trees"
    );
    assert!(dir.join("sky.toml").is_file(), "clean deleted sky.toml");

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// watch — argument validation only
// ---------------------------------------------------------------------------

#[test]
fn watch_rejects_bad_invocations_without_starting() {
    // `watch` is long-running by design (it exits only on Ctrl-C), so its
    // testable, non-daemon surface is argument validation. Both paths must exit
    // 2 — a usage error that exits 0 is how a mistyped CI step becomes a
    // permanently green no-op.
    let dir = scratch("watch");

    let (code, out) = run_sky(&dir, &["watch"]);
    assert_eq!(code, 2, "watch with no file must exit 2; got:\n{out}");
    assert!(
        out.contains("usage: sky watch"),
        "expected usage text; got:\n{out}"
    );

    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n").unwrap();
    std::fs::write(dir.join("sky.toml"), "name = \"w\"\nentry = \"src/Main.sky\"\n").unwrap();

    let (code, out) = run_sky(&dir, &["watch", "src/Main.sky", "--kill-timeout=abc"]);
    assert_eq!(
        code, 2,
        "a non-numeric --kill-timeout must exit 2 rather than silently defaulting; got:\n{out}"
    );
    assert!(
        out.contains("--kill-timeout"),
        "the diagnostic must name the offending flag; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// db — dispatch + the DB-free scaffold
// ---------------------------------------------------------------------------

#[test]
fn db_rejects_an_unknown_subcommand() {
    // `xtask` exiting 0 on an unknown subcommand made a typo'd CI gate a
    // permanently green no-op. The same property is asserted here for `sky db`.
    let dir = scratch("dbbad");
    let (code, out) = run_sky(&dir, &["db", "bogus"]);
    assert_eq!(code, 2, "unknown `sky db` subcommand must exit 2; got:\n{out}");
    assert!(
        out.contains("usage: sky db"),
        "expected usage text; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn db_init_scaffolds_the_file_based_migration_layout() {
    let dir = scratch("dbinit");
    let (code, out) = run_sky(&dir, &["init", "app"]);
    assert_eq!(code, 0, "sky init failed:\n{out}");
    let proj = dir.join("app");

    let (code, out) = run_sky(&proj, &["db", "init"]);
    assert_eq!(code, 0, "sky db init should succeed; got:\n{out}");
    assert!(
        proj.join("db/migrations").is_dir(),
        "sky db init must create db/migrations/; output:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// install / update / upgrade — the network-free paths
// ---------------------------------------------------------------------------

#[test]
fn install_and_update_are_clean_no_ops_without_dependencies() {
    // With no `["go.dependencies"]` both verbs must be network-free no-ops that
    // SAY they did nothing. Exiting 0 silently would be indistinguishable from
    // having installed something.
    let dir = scratch("install");
    let (code, out) = run_sky(&dir, &["init", "app"]);
    assert_eq!(code, 0, "sky init failed:\n{out}");
    let proj = dir.join("app");

    let (code, out) = run_sky(&proj, &["install"]);
    assert_eq!(code, 0, "install on an empty dep set should succeed; got:\n{out}");
    assert!(
        out.contains("nothing to do"),
        "install must say it did nothing; got:\n{out}"
    );

    let (code, out) = run_sky(&proj, &["update"]);
    assert_eq!(code, 0, "update on an empty dep set should succeed; got:\n{out}");
    assert!(
        out.contains("no declared surfaces"),
        "update must say it had nothing to regenerate; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn upgrade_refuses_to_replace_a_dev_build() {
    // `sky upgrade` self-replaces the running binary, so the ONLY safe thing to
    // assert is the refusal path — which is also the one that matters: a dev
    // build silently overwriting itself with a published release mid-session
    // would swap the compiler under a running verification.
    let dir = scratch("upgrade");
    let (code, out) = run_sky(&dir, &["upgrade"]);

    if out.contains("dial tcp")
        || out.contains("no such host")
        || out.contains("network is unreachable")
        || out.contains("Temporary failure in name resolution")
    {
        eprintln!("skipping upgrade assertions — network unavailable:\n{out}");
        let _ = std::fs::remove_dir_all(&dir);
        return;
    }

    assert_eq!(code, 0, "upgrade's refusal path should exit 0; got:\n{out}");
    assert!(
        out.contains("--force"),
        "the refusal must tell the operator how to override it; got:\n{out}"
    );
    assert!(
        out.contains("dev build") || out.contains("not a published release"),
        "the refusal must say WHY it refused; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

#[test]
fn unknown_verb_exits_two() {
    let dir = scratch("unknown");
    let (code, out) = run_sky(&dir, &["bogusverb"]);
    assert_eq!(
        code, 2,
        "an unknown verb must exit non-zero — a CI step with a typo'd verb that \
         exits 0 is a permanently green no-op; got:\n{out}"
    );
    assert!(
        out.contains("unknown command"),
        "expected an unknown-command diagnostic; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// `--embed` is a build flag
// ---------------------------------------------------------------------------

/// `parse_out` swallows every flag it does not recognise, "for forward
/// compatibility". That makes a misplaced `--embed` on `sky run` a silent
/// no-op — the user asks for a self-contained database and gets an ordinary
/// build, with nothing said. Silently ignoring `--embed` is the precise failure
/// mode the flag exists to refuse, so `sky run` names the two things that do
/// work instead.
#[test]
fn embed_on_run_is_refused_and_points_at_what_does_work() {
    let dir = scratch("embed-on-run");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("sky.toml"), "name = \"x\"\nentry = \"src/Main.sky\"\n").unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nmain = ()\n",
    )
    .unwrap();

    let (code, out) = run_sky(&dir, &["run", "--embed", "src/Main.sky"]);
    assert_eq!(code, 2, "a misplaced --embed must not be swallowed; got:\n{out}");
    assert!(
        out.contains("embedded = true"),
        "the refusal must name the sky.toml key that does work; got:\n{out}"
    );
    assert!(
        out.contains("sky build --embed"),
        "the refusal must name the verb that does take --embed; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
