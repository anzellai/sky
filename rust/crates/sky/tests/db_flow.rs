//! End-to-end regression for the `sky db` committed-migration flow (bug #9).
//!
//! The bug: `sky db migrate --gen` → apply SILENTLY DROPPED column-level
//! constraints — serial AUTOINCREMENT/BIGSERIAL PK, UNIQUE, and DEFAULT — that
//! the direct `sky db push` path preserves. Same `db : Store.Project`, two
//! divergent DDL renderings. Consequences: SQLite accepted duplicate values on a
//! UNIQUE column, and on Postgres a serial PK rendered as plain `BIGINT` (no
//! sequence) so `Store.insert` (which omits the generated PK) failed with a
//! null-PK violation — the app was BROKEN on Postgres via committed migrations.
//!
//! This test drives the REAL `sky` binary through `init → migrate --gen →
//! migrate` on a scratch project whose store declares `serial "id"` +
//! `unique "email"` + `defaultNow "created_at"`, then asserts:
//!   (a) the applied SQLite `users` DDL contains AUTOINCREMENT and UNIQUE, and
//!   (b) the committed-migration DDL BYTE-MATCHES the `sky db push` DDL.
//!
//! It needs a `go` toolchain (the gen/apply/push paths compile a temp Sky entry)
//! and the `sqlite3` CLI (to read back the applied DDL). When either is absent the
//! test early-returns with a note rather than failing — matching the example-sweep
//! convention for toolchain-gated checks.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn tool_on_path(name: &str) -> bool {
    Command::new(name)
        .arg("--version")
        .output()
        .map(|o| o.status.success() || !o.stdout.is_empty() || !o.stderr.is_empty())
        .unwrap_or(false)
}

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-db-flow-{}-{}",
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
        "name = \"db-flow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[database]\ndriver = \"sqlite\"\npath = \"app.db\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        r#"module Main exposing (main, db)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Db as Db
import Std.Db.Store as Store
import Std.Codec as Codec
import Std.Log exposing (println)


type alias User =
    { id : Int
    , email : String
    , createdAt : Int
    }


users : Store.Store User
users =
    Store.fromCodec "users" (Codec.auto { id = 0, email = "", createdAt = 0 })
        |> Store.serial "id"
        |> Store.unique "email"
        |> Store.defaultNow "created_at"


db : Store.Project
db =
    Store.project [ Store.toTable users ]


main : Task Error ()
main =
    let
        _ =
            println "ready"
    in
    Task.succeed ()
"#,
    )
    .unwrap();
    dir
}

/// Run `sky <args...>` in `dir` with an empty stdin (so any interactive prompt
/// takes its non-TTY default). Returns (success, stdout+stderr).
fn run_sky(dir: &Path, args: &[&str]) -> (bool, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

/// `sqlite3 <db> ".schema users"` → the stored CREATE TABLE text.
fn users_ddl(db_path: &Path) -> String {
    let out = Command::new("sqlite3")
        .arg(db_path)
        .arg(".schema users")
        .output()
        .expect("spawn sqlite3");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

#[test]
fn committed_migration_preserves_column_constraints_and_matches_push() {
    if !required(Need::Go, tool_on_path("go")) || !required(Need::Sqlite3, tool_on_path("sqlite3")) {
        return;
    }

    let dir = scratch_project();

    // init → gen → migrate (the committed-migration path).
    let (ok, log) = run_sky(&dir, &["db", "init"]);
    assert!(ok, "sky db init failed:\n{log}");
    let (ok, log) = run_sky(&dir, &["db", "migrate", "--gen", "init"]);
    assert!(ok, "sky db migrate --gen failed:\n{log}");

    // The generated migration JSON must carry the constraints (the layer the bug
    // dropped) — assert before applying so a regression is pinpointed here.
    let mig_dir = dir.join("db").join("migrations");
    let mig_body = std::fs::read_dir(&mig_dir)
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| std::fs::read_to_string(e.path()).unwrap_or_default())
        .collect::<String>();
    assert!(mig_body.contains(r#""autoinc": true"#), "migration lost serial autoinc:\n{mig_body}");
    assert!(mig_body.contains(r#""unique": true"#), "migration lost UNIQUE:\n{mig_body}");
    assert!(mig_body.contains(r#""now": true"#), "migration lost DEFAULT now:\n{mig_body}");

    let (ok, log) = run_sky(&dir, &["db", "migrate"]);
    assert!(ok, "sky db migrate (apply) failed:\n{log}");

    // (a) The applied SQLite DDL enforces the constraints.
    let migrate_ddl = users_ddl(&dir.join("app.db"));
    assert!(
        migrate_ddl.contains("AUTOINCREMENT"),
        "applied migration DDL missing AUTOINCREMENT:\n{migrate_ddl}"
    );
    assert!(
        migrate_ddl.contains("UNIQUE"),
        "applied migration DDL missing UNIQUE:\n{migrate_ddl}"
    );

    // (b) The committed-migration DDL byte-matches the `sky db push` DDL. Push into
    //     a separate DB via SKY_DB_PATH so we compare two independently-rendered
    //     CREATE TABLE statements for the same Store.Project.
    let push_db = dir.join("push.db");
    let out = Command::new(SKY)
        .args(["db", "push"])
        .current_dir(&dir)
        .env("SKY_DB_PATH", &push_db)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky db push");
    assert!(
        out.status.success(),
        "sky db push failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let push_ddl = users_ddl(&push_db);
    assert_eq!(
        push_ddl, migrate_ddl,
        "committed-migration DDL must BYTE-MATCH `sky db push` DDL\npush:    {push_ddl:?}\nmigrate: {migrate_ddl:?}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
