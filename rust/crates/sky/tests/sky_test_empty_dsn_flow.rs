//! `sky test` gives a project that declares a database but is given no DSN a
//! throwaway database (docs/tooling/testing.md). An EMPTY `DATABASE_URL` is no
//! DSN: a CI job or a shell that clears it with `DATABASE_URL=` must still get
//! the throwaway database. Before the fix the runner tested only whether the
//! variable was SET, so an empty value started no database.
//!
//! The project here declares a SQLite database (`[database] driver =
//! "sqlite"`), whose throwaway database is a scratch file the runner names in
//! `SKY_DB_PATH`. The suite asserts it sees that path.

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn project() -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-test-empty-dsn-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"emptydsn\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n\n[database]\ndriver = \"sqlite\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"ok\"\n",
    )
    .unwrap();
    // Test mode (and with it the throwaway database) is opted into by a
    // committed `.env.test`.
    std::fs::write(dir.join(".env.test"), "# test mode\n").unwrap();
    std::fs::write(
        dir.join("tests/DbPathTest.sky"),
        "module DbPathTest exposing (tests)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Core.System as System\n\
         import Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test\n          \
         \"the runner gave the suite a throwaway database\"\n          \
         (\\_ -> Test.isTrue (String.endsWith \"test.db\" (System.getenvOr \"SKY_DB_PATH\" \"\")))\n    \
         ]\n",
    )
    .unwrap();
    dir
}

fn run_sky_test(dir: &PathBuf, database_url: Option<&str>) -> (bool, String) {
    let mut cmd = Command::new(SKY);
    cmd.args(["test", "tests/DbPathTest.sky"])
        .current_dir(dir)
        .env_remove("SKY_DB_PATH")
        .stdin(std::process::Stdio::null());
    match database_url {
        Some(v) => {
            cmd.env("DATABASE_URL", v);
        }
        None => {
            cmd.env_remove("DATABASE_URL");
        }
    }
    let out = cmd.output().expect("run sky test");
    (
        out.status.success(),
        format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        ),
    )
}

#[test]
fn an_empty_database_url_still_gets_the_throwaway_database() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project();
    let (unset_ok, unset_out) = run_sky_test(&dir, None);
    let (empty_ok, empty_out) = run_sky_test(&dir, Some(""));
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        unset_ok,
        "with DATABASE_URL unset the suite must get its throwaway database:\n{unset_out}"
    );
    assert!(
        empty_ok,
        "an EMPTY DATABASE_URL is no DSN: the suite must still get its throwaway database:\n{empty_out}"
    );
}
