//! Regression: `sky db` verbs destroyed the project's built binary.
//!
//! `build_temp_db_entry` — shared by `db seed`, `db status` (file-based),
//! `db push`, `db reset` and `db drop` — and the two inline `BuildOptions` in
//! `db migrate --gen` / `db migrate` all built their synthesised helper entry
//! with `out_dir_name: "sky-out"` and `out_dir_abs: None`, i.e. straight into
//! the project's real output directory. Running any of them replaced
//! `sky-out/app` (and `sky-out/main.go`) with the helper program.
//!
//! A CI harness hits this immediately: run a db verb, and the binary you were
//! about to test is gone — or worse, still there and silently a different
//! program, so the test that follows exercises `SkyDbSeed` and passes.
//!
//! They also wrote the synthesised `.sky` into the user's own `src/`, so an
//! aborted run left `src/_skydbseed.sky` behind, where module discovery picks it
//! up on the next build.
//!
//! `sky test` already solved exactly this (`testrunner::run_test`): synthesise
//! into a private scratch dir and point `out_dir_abs` at it. The `BuildOptions`
//! field's own doc comment names that as its purpose.
//!
//! These assertions are toolchain-free: they check what the verb did to the
//! project tree, so they hold on a runner with no Go (where the helper build
//! fails — the verb must STILL not have clobbered anything).

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-dbiso-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// A project with a pre-existing `sky-out/app` + `sky-out/main.go` standing in
/// for a previously-built binary, and a `db/migrations/` dir so the file-based
/// db paths engage.
fn project(tag: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"dbiso\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n\n[database]\npath = \"app.db\"\n",
    )
    .unwrap();
    // Exposes `db` (a `Store.Project`) and `seed`, so `db push` / `migrate --gen`
    // / `seed` get PAST their "does Main expose …?" check and actually reach the
    // build. Without that they bail early and the test could not fail.
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main, db, seed)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Codec as Codec\n\
         import Std.Db as Db\n\
         import Std.Db.Store as Store exposing (Store)\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Todo =\n    { id : Int, text : String, done : Bool }\n\n\n\
         todos : Store Todo\n\
         todos =\n    \
         Store.fromCodec \"todos\" (Codec.auto { id = 0, text = \"\", done = False })\n        \
         |> Store.serial \"id\"\n\n\n\
         db : Store.Project\n\
         db =\n    Store.project [ Store.toTable todos ]\n\n\n\
         seed : Task Error ()\n\
         seed =\n    Task.succeed ()\n\n\n\
         main =\n    println \"the real app\"\n",
    )
    .unwrap();
    let out = dir.join("sky-out");
    std::fs::create_dir_all(&out).unwrap();
    std::fs::write(out.join("app"), b"SENTINEL-REAL-APP-BINARY").unwrap();
    std::fs::write(out.join("main.go"), b"// SENTINEL-REAL-APP-SOURCE").unwrap();
    std::fs::create_dir_all(dir.join("db").join("migrations")).unwrap();
    dir
}

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

/// The whole contract, per verb: the project's own build output is untouched,
/// and no synthesised source is left in `src/`.
fn assert_project_untouched(dir: &Path, verb: &str, log: &str) {
    let app = std::fs::read(dir.join("sky-out").join("app")).unwrap_or_default();
    assert_eq!(
        app,
        b"SENTINEL-REAL-APP-BINARY",
        "`sky db {verb}` overwrote the project's built binary at sky-out/app. \
         A db verb must build its helper entry into a scratch dir \
         (BuildOptions::out_dir_abs), never the project's real output. Log:\n{log}"
    );
    let main_go = std::fs::read(dir.join("sky-out").join("main.go")).unwrap_or_default();
    assert_eq!(
        main_go,
        b"// SENTINEL-REAL-APP-SOURCE",
        "`sky db {verb}` overwrote the project's emitted sky-out/main.go. Log:\n{log}"
    );

    let strays: Vec<String> = std::fs::read_dir(dir.join("src"))
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .filter(|n| n != "Main.sky")
        .collect();
    assert!(
        strays.is_empty(),
        "`sky db {verb}` left synthesised source in the project's src/: {strays:?}. \
         The synth entry belongs in a scratch dir. Log:\n{log}"
    );
}

macro_rules! db_verb_isolation {
    ($name:ident, $tag:literal, $($arg:literal),+) => {
        #[test]
        fn $name() {
            let dir = project($tag);
            let (_code, log) = run_sky(&dir, &["db", $($arg),+]);
            assert_project_untouched(&dir, concat!($($arg, " "),+), &log);
            let _ = std::fs::remove_dir_all(&dir);
        }
    };
}

db_verb_isolation!(db_status_does_not_clobber_sky_out, "status", "status");
db_verb_isolation!(db_seed_does_not_clobber_sky_out, "seed", "seed");
db_verb_isolation!(db_push_does_not_clobber_sky_out, "push", "push");
db_verb_isolation!(db_migrate_does_not_clobber_sky_out, "migrate", "migrate");
db_verb_isolation!(db_migrate_gen_does_not_clobber_sky_out, "gen", "migrate", "--gen");
db_verb_isolation!(db_reset_does_not_clobber_sky_out, "reset", "reset");
db_verb_isolation!(db_drop_does_not_clobber_sky_out, "drop", "drop");
