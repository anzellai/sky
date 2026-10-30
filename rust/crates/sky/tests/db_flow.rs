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

/// A fresh project directory that no other caller holds.
///
/// The name used to be pid + wall-clock nanoseconds alone. The tests in this
/// file run on parallel threads of ONE process, so the pid is shared, and the
/// macOS wall clock ticks in whole microseconds, so two threads that reach this
/// line together read the same "nanoseconds". They then shared a project and
/// broke each other (see `concurrent_scratch_projects_are_distinct`). A
/// process-wide counter makes the name unique within the process, and
/// `create_dir` (not `create_dir_all`) claims it exclusively, so a directory
/// left by an earlier process with a reused pid is skipped, never shared.
fn scratch_project() -> PathBuf {
    use std::sync::atomic::{AtomicU64, Ordering};
    static SEQ: AtomicU64 = AtomicU64::new(0);
    let dir = loop {
        let uniq = format!(
            "sky-db-flow-{}-{}-{}",
            std::process::id(),
            SEQ.fetch_add(1, Ordering::Relaxed),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        );
        let dir = std::env::temp_dir().join(uniq);
        match std::fs::create_dir(&dir) {
            Ok(()) => break dir,
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(e) => panic!("create {}: {e}", dir.display()),
        }
    };
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
    , createdAt : String
    }


users : Store.Store User
users =
    Store.fromCodec "users" (Codec.auto { id = 0, email = "", createdAt = "" })
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

/// The two flow tests run on parallel threads of one process and each takes a
/// `scratch_project()`. When two calls returned the same directory, the tests
/// shared one project: one `sky db migrate` removed `db/_apply.json` while the
/// other's apply binary was about to read it, and the second test rewrote
/// `src/Main.sky` under the first. Seen as a rare
/// `open db/_apply.json: no such file or directory` at the apply step.
#[test]
fn concurrent_scratch_projects_are_distinct() {
    const N: usize = 16;
    let barrier = std::sync::Arc::new(std::sync::Barrier::new(N));
    let handles: Vec<_> = (0..N)
        .map(|_| {
            let b = barrier.clone();
            std::thread::spawn(move || {
                b.wait();
                scratch_project()
            })
        })
        .collect();
    let dirs: Vec<PathBuf> = handles.into_iter().map(|h| h.join().unwrap()).collect();
    let distinct: std::collections::BTreeSet<&PathBuf> = dirs.iter().collect();
    for d in &distinct {
        let _ = std::fs::remove_dir_all(d);
    }
    assert_eq!(
        distinct.len(),
        N,
        "{N} concurrent scratch_project() calls shared a directory"
    );
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
    if !required(Need::Go, tool_on_path("go")) || !required(Need::Sqlite3, tool_on_path("sqlite3"))
    {
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
    assert!(
        mig_body.contains(r#""autoinc": true"#),
        "migration lost serial autoinc:\n{mig_body}"
    );
    assert!(
        mig_body.contains(r#""unique": true"#),
        "migration lost UNIQUE:\n{mig_body}"
    );
    assert!(
        mig_body.contains(r#""now": true"#),
        "migration lost DEFAULT now:\n{mig_body}"
    );

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

/// v0.29.0, user decision D1: after a field is added, `--gen` writes an
/// `addColumn` that keeps the declared default and an `addIndex`, with no op
/// version and no header; the ledger records the file by its ops checksum
/// (kind `ops`); a second `sky db migrate` shows no drift; and `sky db push`
/// on a copy of the old database runs the same ALTER, so both paths leave the
/// same DDL.
#[test]
fn added_field_follows_one_migrate_rule_on_both_paths() {
    if !required(Need::Go, tool_on_path("go")) || !required(Need::Sqlite3, tool_on_path("sqlite3"))
    {
        return;
    }
    let dir = scratch_project();
    for args in [
        vec!["db", "init"],
        vec!["db", "migrate", "--gen", "init"],
        vec!["db", "migrate"],
    ] {
        let (ok, log) = run_sky(&dir, &args);
        assert!(ok, "sky {args:?} failed:\n{log}");
    }
    // A row before the change, and a copy of the old database for `push`.
    let ins = Command::new("sqlite3")
        .arg(dir.join("app.db"))
        .arg("INSERT INTO users (email) VALUES ('a@x')")
        .output()
        .expect("spawn sqlite3");
    assert!(ins.status.success());
    std::fs::copy(dir.join("app.db"), dir.join("push.db")).unwrap();

    let main = dir.join("src").join("Main.sky");
    let src = std::fs::read_to_string(&main).unwrap();
    let src = src
        .replace(
            "    , createdAt : String\n    }",
            "    , createdAt : String\n    , rank : Int\n    , nick : Maybe String\n    , tags : List String\n    }",
        )
        .replace(
            "createdAt = \"\" })",
            "createdAt = \"\", rank = 0, nick = Nothing, tags = [ \"it's\" ] })",
        )
        .replace(
            "        |> Store.defaultNow \"created_at\"\n",
            "        |> Store.defaultNow \"created_at\"\n        |> Store.defaultInt \"rank\" 7\n        |> Store.index [ \"rank\" ]\n",
        );
    std::fs::write(&main, src).unwrap();

    let (ok, log) = run_sky(&dir, &["db", "migrate", "--gen", "add_rank"]);
    assert!(ok, "sky db migrate --gen add_rank failed:\n{log}");
    let mig = std::fs::read_dir(dir.join("db").join("migrations"))
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .find(|p| p.to_string_lossy().contains("add_rank"))
        .expect("the add_rank migration file");
    let body = std::fs::read_to_string(&mig).unwrap();
    assert!(
        !body.contains(r#""v":"#) && !body.contains("sky:migration"),
        "--gen wrote an op version or a header:\n{body}"
    );
    assert!(
        body.contains(r#""int": 7"#),
        "the declared default was lost (B9):\n{body}"
    );
    assert!(
        body.contains(r#""kind": "addIndex""#),
        "no addIndex:\n{body}"
    );
    // Gap A: a required JSON (blob) field is NOT NULL, and its DEFAULT is the
    // JSON of the codec's blank value (here `[ "it's" ]`).
    assert!(
        body.contains(r#""text": "[\"it's\"]""#),
        "the required blob field did not take the blank's JSON as its default:\n{body}"
    );
    assert!(
        !log.contains("NULLABLE"),
        "--gen still adds a required field nullable:\n{log}"
    );

    let (ok, log) = run_sky(&dir, &["db", "migrate"]);
    assert!(ok, "applying add_rank failed:\n{log}");
    let (ok, log) = run_sky(&dir, &["db", "migrate"]);
    assert!(
        ok && !log.contains("mismatch"),
        "second apply drifted:\n{log}"
    );

    let rank = Command::new("sqlite3")
        .arg(dir.join("app.db"))
        .arg("SELECT rank FROM users")
        .output()
        .expect("spawn sqlite3");
    assert_eq!(String::from_utf8_lossy(&rank.stdout).trim(), "7", "old row");
    let tags = Command::new("sqlite3")
        .arg(dir.join("app.db"))
        .arg("SELECT tags FROM users")
        .output()
        .expect("spawn sqlite3");
    assert_eq!(
        String::from_utf8_lossy(&tags.stdout).trim(),
        r#"["it's"]"#,
        "the old row does not read the blank's JSON"
    );
    let notnull = Command::new("sqlite3")
        .arg(dir.join("app.db"))
        .arg("SELECT \"notnull\" FROM pragma_table_info('users') WHERE name = 'tags'")
        .output()
        .expect("spawn sqlite3");
    assert_eq!(
        String::from_utf8_lossy(&notnull.stdout).trim(),
        "1",
        "the required blob column is nullable"
    );
    let kind = Command::new("sqlite3")
        .arg(dir.join("app.db"))
        .arg("SELECT kind FROM _sky_migrations WHERE name LIKE '%add_rank%'")
        .output()
        .expect("spawn sqlite3");
    assert_eq!(
        String::from_utf8_lossy(&kind.stdout).trim(),
        "ops",
        "the ledger did not record the file by its ops checksum"
    );

    let out = Command::new(SKY)
        .args(["db", "push"])
        .current_dir(&dir)
        .env("SKY_DB_PATH", dir.join("push.db"))
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky db push");
    assert!(out.status.success(), "sky db push failed");
    assert_eq!(
        users_ddl(&dir.join("push.db")),
        users_ddl(&dir.join("app.db")),
        "push and the committed migration left different DDL"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
