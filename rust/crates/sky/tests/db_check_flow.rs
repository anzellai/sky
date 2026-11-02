//! End-to-end tests for `sky db check` (v0.29.2): the read-only comparison of
//! a live database with the tables the code declares, on SQLite and on
//! PostgreSQL, driven through the real `sky` binary.
//!
//! - a database the committed migrations built: exit 0;
//! - every kind of drift (missing table / column / index, a type change, an
//!   extra column / table, NOT NULL, a default, UNIQUE): exit 1, each named;
//! - the ledger: a pending and an edited migration;
//! - `--sql`: safe steps plain (rendered by the runtime's migration renderer),
//!   unsafe ones commented with the reason, the INTEGER→BIGINT notice;
//! - `--write`: refused while a migration is pending, else a migration file
//!   whose ops are EXACTLY the ops `sky db migrate --gen` writes for the same
//!   change (check and migrate cannot disagree);
//! - `--code`: the notes, and record + Codec + Store stubs that `sky check`
//!   compiles.
//!
//! `sky db check` writes nothing to the database: after every run the tests
//! check that no `_sky_migrations` ledger appeared where there was none.

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

fn unique(tag: &str) -> String {
    use std::sync::atomic::{AtomicU64, Ordering};
    static SEQ: AtomicU64 = AtomicU64::new(0);
    format!(
        "{tag}-{}-{}-{}",
        std::process::id(),
        SEQ.fetch_add(1, Ordering::Relaxed),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )
}

/// The code: `users` (serial key, a unique email, a `now` default, an Int
/// with a default) and `orders`. `extra_fields` / `extra_blank` /
/// `extra_builders` add to `users`; `extra_tables` to the project.
fn main_src(
    extra_fields: &str,
    extra_blank: &str,
    extra_builders: &str,
    extra_imports: &str,
    extra_tables: &str,
) -> String {
    format!(
        r#"module Main exposing (main, db)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Db.Store as Store
import Std.Codec as Codec
import Std.Log exposing (println)
{extra_imports}

type alias User =
    {{ id : Int
    , email : String
    , createdAt : String
    , rank : Int{extra_fields}
    }}


users : Store.Store User
users =
    Store.fromCodec "users" (Codec.auto {{ id = 0, email = "", createdAt = "", rank = 0{extra_blank} }})
        |> Store.serial "id"
        |> Store.unique "email"
        |> Store.defaultNow "created_at"
        |> Store.defaultInt "rank" 3{extra_builders}


type alias Order =
    {{ id : String
    , total : Float
    }}


orders : Store.Store Order
orders =
    Store.fromCodec "orders" (Codec.auto {{ id = "", total = 0.0 }})
        |> Store.primaryKey "id"


db : Store.Project
db =
    Store.project [ Store.toTable users, Store.toTable orders{extra_tables} ]


main : Task Error ()
main =
    let
        _ =
            println "ready"
    in
    Task.succeed ()
"#
    )
}

fn base_src() -> String {
    main_src("", "", "", "", "")
}

/// The base code plus a nullable `nick` and an index on `rank`.
fn changed_src() -> String {
    main_src(
        "\n    , nick : Maybe String",
        ", nick = Nothing",
        "\n        |> Store.index [ \"rank\" ]",
        "",
        "",
    )
}

/// A project at a fresh path. `database` is the `[database]` section body
/// ("" for none: the DSN then comes from the environment).
fn project(tag: &str, database: &str, src: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(unique(tag));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    let db = if database.is_empty() {
        String::new()
    } else {
        format!("\n[database]\n{database}\n")
    };
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"db-check\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n{db}"),
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), src).unwrap();
    dir
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for e in std::fs::read_dir(from).unwrap().flatten() {
        let p = e.path();
        let name = e.file_name();
        if name == "sky-out" || name == ".skycache" {
            continue;
        }
        if p.is_dir() {
            copy_dir(&p, &to.join(&name));
        } else {
            std::fs::copy(&p, to.join(&name)).unwrap();
        }
    }
}

/// `sky <args>` in `dir` with `env`; (exit code, stdout, stderr).
fn sky(dir: &Path, args: &[&str], env: &[(&str, &str)]) -> (i32, String, String) {
    let mut cmd = Command::new(SKY);
    cmd.args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .env_remove("DATABASE_URL")
        .env_remove("SKY_DB_PATH");
    for (k, v) in env {
        cmd.env(k, v);
    }
    let out = cmd.output().expect("spawn sky");
    (
        out.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

fn ok(dir: &Path, args: &[&str], env: &[(&str, &str)]) -> String {
    let (code, out, err) = sky(dir, args, env);
    assert_eq!(code, 0, "sky {args:?} exited {code}:\n{out}\n{err}");
    out
}

fn sqlite3(db: &Path, sql: &str) -> String {
    let out = Command::new("sqlite3")
        .arg(db)
        .arg(sql)
        .output()
        .expect("spawn sqlite3");
    assert!(
        out.status.success(),
        "sqlite3 {sql}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

fn migration_files(dir: &Path) -> Vec<PathBuf> {
    let mut v: Vec<PathBuf> = std::fs::read_dir(dir.join("db").join("migrations"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    v.sort();
    v
}

fn file_ops(p: &Path) -> (serde_json::Value, serde_json::Value) {
    let v: serde_json::Value = serde_json::from_str(&std::fs::read_to_string(p).unwrap()).unwrap();
    (
        v.get("ops").cloned().unwrap_or_default(),
        v.get("destructive")
            .cloned()
            .unwrap_or(serde_json::json!([])),
    )
}

/// The Sky stub block of a `--code` report.
fn stub_block(code_out: &str) -> String {
    let begin = code_out
        .find("-- ---- begin Sky stubs ----")
        .expect("no stub block");
    let end = code_out
        .find("-- ---- end Sky stubs ----")
        .expect("no stub end");
    code_out[begin..end].to_string()
}

/// Compile the `--code` stubs: a `Stubs` module the project's `db` uses.
fn stubs_compile(dir: &Path, code_out: &str, store: &str, env: &[(&str, &str)]) {
    std::fs::write(
        dir.join("src").join("Stubs.sky"),
        format!("module Stubs exposing (..)\n\n{}", stub_block(code_out)),
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        main_src(
            "\n    , nick : Maybe String",
            ", nick = Nothing",
            "\n        |> Store.index [ \"rank\" ]",
            "import Stubs",
            &format!(", Store.toTable Stubs.{store}"),
        ),
    )
    .unwrap();
    let (code, out, err) = sky(dir, &["check"], env);
    assert_eq!(
        code,
        0,
        "the --code stubs do not compile:\n{}\n{out}\n{err}",
        std::fs::read_to_string(dir.join("src").join("Stubs.sky")).unwrap()
    );
}

/// Clean → 0; edited → 1; pending → 1 and `--write` refused; `--write` on a
/// copy writes the SAME ops `--gen` writes; after migrate → 0.
#[test]
fn sqlite_ledger_states_and_write_matches_gen() {
    if !required(Need::Go, tool_on_path("go")) || !required(Need::Sqlite3, tool_on_path("sqlite3"))
    {
        return;
    }
    let a = project(
        "sky-dbcheck-a",
        "driver = \"sqlite\"\npath = \"app.db\"",
        &base_src(),
    );
    ok(&a, &["db", "init"], &[]);
    ok(&a, &["db", "migrate", "--gen", "init"], &[]);
    ok(&a, &["db", "migrate"], &[]);

    // A database the committed migrations built matches the code.
    let out = ok(&a, &["db", "check"], &[]);
    assert!(
        out.contains("schema: the database matches the code."),
        "{out}"
    );
    assert!(out.contains("1 applied, 0 pending, 0 edited"), "{out}");
    assert!(out.contains("up to date."), "{out}");

    // Edit the applied file: the ledger checksum no longer matches.
    let init = migration_files(&a).remove(0);
    let original = std::fs::read_to_string(&init).unwrap();
    let edited = original.replacen(
        "\"ops\": [",
        "\"ops\": [\n    { \"kind\": \"raw\", \"sql\": \"SELECT 1\" },",
        1,
    );
    assert_ne!(edited, original);
    std::fs::write(&init, &edited).unwrap();
    let (code, out, _) = sky(&a, &["db", "check"], &[]);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("EDITED"), "{out}");
    assert!(out.contains("1 edited"), "{out}");
    // `sky db status` reads the same ledger check: EDITED, exit 1 (it said
    // "applied" and "up to date." before v0.29.2).
    let (code, out, err) = sky(&a, &["db", "status"], &[]);
    assert_eq!(code, 1, "{out}\n{err}");
    assert!(out.contains("EDITED"), "{out}");
    assert!(!out.contains("up to date."), "{out}");
    std::fs::write(&init, &original).unwrap();
    let out = ok(&a, &["db", "status"], &[]);
    assert!(
        out.contains("applied") && out.contains("up to date."),
        "{out}"
    );

    // The code changes. B is a copy of A: same database, same files.
    std::fs::write(a.join("src").join("Main.sky"), changed_src()).unwrap();
    let b = std::env::temp_dir().join(unique("sky-dbcheck-b"));
    copy_dir(&a, &b);

    // A: --gen writes the next file (not applied yet).
    ok(&a, &["db", "migrate", "--gen", "next"], &[]);
    let (code, out, _) = sky(&a, &["db", "check"], &[]);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("PENDING"), "{out}");
    assert!(out.contains("column users.nick is missing"), "{out}");
    assert!(
        out.contains("`sky db migrate` applies the pending"),
        "{out}"
    );
    let (code, out, err) = sky(&a, &["db", "check", "--sql", "--write"], &[]);
    assert_eq!(
        code, 2,
        "--write ran with a pending migration:\n{out}\n{err}"
    );
    assert!(err.contains("nothing written"), "{err}");
    assert_eq!(migration_files(&a).len(), 2);

    // B: check --sql --write writes the same ops --gen wrote in A.
    let (code, out, err) = sky(&b, &["db", "check", "--sql", "--write"], &[]);
    assert_eq!(code, 1, "{out}\n{err}");
    assert!(
        out.contains("\nALTER TABLE users ADD COLUMN nick TEXT;\n"),
        "the safe step is not plain SQL:\n{out}"
    );
    assert!(
        out.contains("\nCREATE INDEX IF NOT EXISTS ix_users_"),
        "{out}"
    );
    assert!(
        out.contains("-- sky db check --write: wrote db/migrations/"),
        "{out}"
    );
    let gen = migration_files(&a).pop().unwrap();
    let written = migration_files(&b).pop().unwrap();
    assert!(written.to_string_lossy().ends_with("_db_check.json"));
    assert_eq!(
        file_ops(&written),
        file_ops(&gen),
        "check --write and migrate --gen disagree:\ncheck: {}\ngen:   {}",
        std::fs::read_to_string(&written).unwrap(),
        std::fs::read_to_string(&gen).unwrap()
    );
    assert_eq!(
        std::fs::read_to_string(b.join("db/schema.json")).unwrap(),
        std::fs::read_to_string(a.join("db/schema.json")).unwrap(),
        "the snapshot does not follow the written file"
    );
    // A second --gen in B finds nothing: the snapshot moved with the file.
    let out = ok(&b, &["db", "migrate", "--gen", "again"], &[]);
    assert!(out.contains("no schema changes"), "{out}");

    // Applied, both match.
    for dir in [&a, &b] {
        ok(dir, &["db", "migrate"], &[]);
        let out = ok(dir, &["db", "check"], &[]);
        assert!(out.contains("up to date."), "{out}");
    }
    let _ = std::fs::remove_dir_all(&a);
    let _ = std::fs::remove_dir_all(&b);
}

/// Every kind of drift on SQLite, the `--sql` and `--code` outputs, and stubs
/// that compile. The database is written by hand with `sqlite3`.
#[test]
fn sqlite_every_drift_kind_sql_and_code() {
    if !required(Need::Go, tool_on_path("go")) || !required(Need::Sqlite3, tool_on_path("sqlite3"))
    {
        return;
    }
    let dir = project("sky-dbcheck-drift", "", &changed_src());
    let dbf = dir.join("drift.db");
    sqlite3(
        &dbf,
        "CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT, \
         created_at INTEGER NOT NULL DEFAULT 0, rank INTEGER NOT NULL DEFAULT 9, legacy TEXT NOT NULL);\
         CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, note_text TEXT NOT NULL, amount REAL);\
         CREATE TABLE sky_sessions (id TEXT);",
    );
    let path = dbf.to_string_lossy().into_owned();
    let env = [("SKY_DB_PATH", path.as_str())];

    let (code, out, err) = sky(&dir, &["db", "check"], &env);
    assert_eq!(code, 1, "drift must exit 1:\n{out}\n{err}");
    for want in [
        "table orders is missing",
        "column users.nick is missing",
        "on users (rank) is missing",
        "column users.created_at is INTEGER in the database; the code reads it as text",
        "column users.legacy (TEXT) is in the database, and the code has no field for it. It is NOT NULL with no default",
        "table audit_log is in the database, and the code declares no Store",
        "column users.email is nullable in the database",
        "column users.rank has DEFAULT 9 in the database; the code declares DEFAULT 3",
        "column users.email is not UNIQUE in the database",
        "migrations: no files in db/migrations",
        "drift: the database does not match the code.",
    ] {
        assert!(out.contains(want), "missing {want:?} in:\n{out}");
    }
    assert!(
        !out.contains("sky_sessions"),
        "a runtime table was reported:\n{out}"
    );

    let (code, sql, _) = sky(&dir, &["db", "check", "--sql"], &env);
    assert_eq!(code, 1);
    for want in [
        "\nCREATE TABLE IF NOT EXISTS orders (",
        "\nALTER TABLE users ADD COLUMN nick TEXT;\n",
        "\nCREATE INDEX IF NOT EXISTS ix_users_",
        "\nCREATE UNIQUE INDEX IF NOT EXISTS ux_users_",
        "-- ALTER TABLE users DROP COLUMN legacy;\n",
        "-- DROP TABLE audit_log;\n",
        "-- UNSAFE: dropping a column deletes its data",
        "-- UNSAFE: SQLite cannot change a column's type in place",
        "-- UNSAFE: SQLite cannot add NOT NULL to a column in place",
    ] {
        assert!(sql.contains(want), "missing {want:?} in --sql:\n{sql}");
    }
    assert!(
        !sql.contains("\nDROP"),
        "a drop is not commented out:\n{sql}"
    );

    // --write needs db/migrations.
    let (code, _, err) = sky(&dir, &["db", "check", "--sql", "--write"], &env);
    assert_eq!(code, 2);
    assert!(err.contains("sky db init"), "{err}");

    let (code, code_out, _) = sky(&dir, &["db", "check", "--code"], &env);
    assert_eq!(code, 1);
    for want in [
        "column users.legacy (TEXT, NOT NULL, no default) has no field in the code: every insert fails",
        "column users.email may hold NULL",
        "column users.created_at is INTEGER",
        "type alias AuditLog =",
        "noteText : String",
        "amount : Maybe Float",
        "Store.serial \"id\"",
    ] {
        assert!(code_out.contains(want), "missing {want:?} in --code:\n{code_out}");
    }
    // Read-only: nothing created the ledger, and the stray table is untouched.
    assert_eq!(
        sqlite3(
            &dbf,
            "SELECT COUNT(*) FROM sqlite_master WHERE name = '_sky_migrations'"
        ),
        "0"
    );

    // The stubs compile, and once the code declares the table it is no
    // longer an extra table.
    stubs_compile(&dir, &code_out, "auditLogStore", &env);
    let (_, out, _) = sky(&dir, &["db", "check"], &env);
    assert!(
        !out.contains("audit_log"),
        "the stub's Store does not match its table:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---- PostgreSQL ------------------------------------------------------------

fn find_pg_bin() -> Option<PathBuf> {
    let complete = |d: &Path| {
        ["initdb", "pg_ctl", "postgres", "psql"]
            .iter()
            .all(|b| d.join(b).is_file())
    };
    if let Ok(v) = std::env::var("SKY_POSTGRES_BIN") {
        let d = PathBuf::from(v);
        if complete(&d) {
            return Some(d);
        }
    }
    if let Ok(path) = std::env::var("PATH") {
        for d in std::env::split_paths(&path) {
            if complete(&d) {
                return Some(d);
            }
        }
    }
    let mut roots: Vec<PathBuf> = Vec::new();
    for prefix in ["/opt/homebrew/opt", "/usr/local/opt"] {
        if let Ok(rd) = std::fs::read_dir(prefix) {
            for e in rd.filter_map(Result::ok) {
                if e.file_name().to_string_lossy().starts_with("postgresql") {
                    roots.push(e.path().join("bin"));
                }
            }
        }
    }
    for v in (9..=20).rev() {
        roots.push(PathBuf::from(format!("/usr/lib/postgresql/{v}/bin")));
    }
    roots.sort();
    roots.reverse();
    roots.into_iter().find(|d| complete(d))
}

/// A PostgreSQL database for one test: a fresh schema on the database
/// `SKY_TEST_POSTGRES_DSN` names, else a throwaway cluster (`initdb`) on a
/// unix socket. Dropping it removes the schema / stops the cluster.
struct Pg {
    dsn: String,
    psql: PathBuf,
    psql_dsn: String,
    cleanup: Option<String>,
    cluster: Option<(PathBuf, PathBuf)>,
}

impl Pg {
    fn new() -> Option<Pg> {
        let pg_bin = find_pg_bin();
        let psql = pg_bin
            .as_ref()
            .map(|d| d.join("psql"))
            .unwrap_or_else(|| PathBuf::from("psql"));
        if let Ok(dsn) = std::env::var("SKY_TEST_POSTGRES_DSN") {
            if !dsn.is_empty() {
                let schema = unique("sky_check").replace('-', "_");
                let pg = Pg {
                    dsn: format!(
                        "{dsn}{}search_path={schema}",
                        if dsn.contains('?') { "&" } else { "?" }
                    ),
                    psql,
                    psql_dsn: dsn,
                    cleanup: Some(format!("DROP SCHEMA IF EXISTS {schema} CASCADE")),
                    cluster: None,
                };
                pg.psql(&format!("CREATE SCHEMA {schema}"), false);
                return Some(pg);
            }
        }
        let bin = pg_bin?;
        let root = PathBuf::from("/tmp").join(unique("skc"));
        let data = root.join("d");
        std::fs::create_dir_all(&root).unwrap();
        let init = Command::new(bin.join("initdb"))
            .arg("-D")
            .arg(&data)
            .args(["-A", "trust", "-U", "sky", "--no-sync"])
            .output()
            .expect("initdb");
        assert!(
            init.status.success(),
            "initdb: {}",
            String::from_utf8_lossy(&init.stderr)
        );
        let start = Command::new(bin.join("pg_ctl"))
            .arg("-D")
            .arg(&data)
            .arg("-o")
            .arg(format!(
                "-k {} -c listen_addresses='' -c fsync=off -p 5432",
                root.display()
            ))
            .arg("-l")
            .arg(root.join("log"))
            .args(["-w", "start"])
            .output()
            .expect("pg_ctl start");
        assert!(
            start.status.success(),
            "pg_ctl start: {}\n{}",
            String::from_utf8_lossy(&start.stderr),
            std::fs::read_to_string(root.join("log")).unwrap_or_default()
        );
        let dsn = format!(
            "postgresql://sky@/postgres?host={}&port=5432",
            root.display()
        );
        Some(Pg {
            dsn: dsn.clone(),
            psql,
            psql_dsn: dsn,
            cleanup: None,
            cluster: Some((bin, data)),
        })
    }

    fn psql(&self, sql: &str, in_schema: bool) -> String {
        let dsn = if in_schema { &self.dsn } else { &self.psql_dsn };
        // libpq does not take `search_path` as a URI parameter: pass it as
        // `options` instead.
        let dsn = match dsn.split_once("search_path=") {
            Some((head, schema)) => format!("{head}options=-csearch_path%3D{schema}"),
            None => dsn.clone(),
        };
        let out = Command::new(&self.psql)
            .arg(&dsn)
            .args(["-v", "ON_ERROR_STOP=1", "-tAc", sql])
            .output()
            .expect("spawn psql");
        assert!(
            out.status.success(),
            "psql {sql}: {}",
            String::from_utf8_lossy(&out.stderr)
        );
        String::from_utf8_lossy(&out.stdout).trim().to_string()
    }
}

impl Drop for Pg {
    fn drop(&mut self) {
        if let Some(sql) = self.cleanup.take() {
            let _ = Command::new(&self.psql)
                .arg(&self.psql_dsn)
                .args(["-tAc", &sql])
                .output();
        }
        if let Some((bin, data)) = self.cluster.take() {
            let _ = Command::new(bin.join("pg_ctl"))
                .arg("-D")
                .arg(&data)
                .args(["-m", "immediate", "-w", "stop"])
                .output();
            if let Some(root) = data.parent() {
                let _ = std::fs::remove_dir_all(root);
            }
        }
    }
}

/// PostgreSQL: clean → 0; an INTEGER Int column is a notice, not drift; then
/// drift by hand (an extra NOT NULL column, a dropped NOT NULL, a changed
/// default, an extra table with a NUMERIC column) → 1, the `--sql` statements,
/// and `--code` stubs (with `Decimal`) that compile.
#[test]
fn postgres_clean_notice_drift_sql_and_code() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let pg = Pg::new();
    if !required(Need::Postgres, pg.is_some()) {
        return;
    }
    let pg = pg.unwrap();
    let dir = project("sky-dbcheck-pg", "", &base_src());
    let env = [("DATABASE_URL", pg.dsn.as_str())];
    ok(&dir, &["db", "init"], &env);
    ok(&dir, &["db", "migrate", "--gen", "init"], &env);
    ok(&dir, &["db", "migrate"], &env);
    let out = ok(&dir, &["db", "check"], &env);
    assert!(out.contains("PostgreSQL database"), "{out}");
    assert!(
        out.contains("schema: the database matches the code."),
        "{out}"
    );
    assert!(out.contains("up to date."), "{out}");

    // An Int column an older Schema.createSchema made INTEGER: accepted.
    pg.psql("ALTER TABLE users ALTER COLUMN rank TYPE INTEGER", true);
    let out = ok(&dir, &["db", "check"], &env);
    assert!(
        out.contains("notice: column users.rank is INTEGER on PostgreSQL"),
        "{out}"
    );
    let sql = ok(&dir, &["db", "check", "--sql"], &env);
    assert!(
        sql.contains("-- ALTER TABLE users ALTER COLUMN rank TYPE BIGINT;"),
        "{sql}"
    );
    assert!(sql.contains("integer out of range"), "{sql}");

    pg.psql(
        "ALTER TABLE users ADD COLUMN legacy TEXT NOT NULL DEFAULT 'x';\
         ALTER TABLE users ALTER COLUMN legacy DROP DEFAULT;\
         ALTER TABLE users ALTER COLUMN email DROP NOT NULL;\
         ALTER TABLE users ALTER COLUMN rank SET DEFAULT 9;\
         ALTER TABLE orders ALTER COLUMN total TYPE TEXT;\
         CREATE TABLE ledger_entries (id BIGSERIAL PRIMARY KEY, amount NUMERIC, posted BOOLEAN NOT NULL)",
        true,
    );
    let (code, out, err) = sky(&dir, &["db", "check"], &env);
    assert_eq!(code, 1, "{out}\n{err}");
    for want in [
        "column users.legacy (text) is in the database, and the code has no field for it. It is NOT NULL with no default",
        "column users.email is nullable in the database",
        "column users.rank has DEFAULT 9 in the database; the code declares DEFAULT 3",
        "column orders.total is text in the database; the code reads it as real (DOUBLE PRECISION)",
        "table ledger_entries is in the database",
    ] {
        assert!(out.contains(want), "missing {want:?} in:\n{out}");
    }
    let (_, sql, _) = sky(&dir, &["db", "check", "--sql"], &env);
    for want in [
        "-- UPDATE users SET email = '' WHERE email IS NULL;\n-- ALTER TABLE users ALTER COLUMN email SET NOT NULL;\n",
        "\nALTER TABLE users ALTER COLUMN rank SET DEFAULT 3;\n",
        "-- ALTER TABLE orders ALTER COLUMN total TYPE DOUBLE PRECISION USING total::DOUBLE PRECISION;\n",
        "-- ALTER TABLE users DROP COLUMN legacy;\n",
        "-- DROP TABLE ledger_entries;\n",
    ] {
        assert!(sql.contains(want), "missing {want:?} in --sql:\n{sql}");
    }
    let (_, code_out, _) = sky(&dir, &["db", "check", "--code"], &env);
    for want in [
        "type alias LedgerEntries =",
        "amount : Maybe Decimal",
        "posted : Bool",
        "import Std.Decimal as Decimal exposing (Decimal)",
    ] {
        assert!(
            code_out.contains(want),
            "missing {want:?} in --code:\n{code_out}"
        );
    }
    assert_eq!(
        pg.psql(
            "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'ledger_entries'",
            true
        ),
        "1",
        "check changed the database"
    );
    stubs_compile(&dir, &code_out, "ledgerEntriesStore", &env);
    // The stub's Store matches its table: NUMERIC through Codec.decimal, the
    // BIGSERIAL key through Store.serial.
    let (_, out, _) = sky(&dir, &["db", "check"], &env);
    assert!(
        !out.contains("ledger_entries"),
        "the stub's Store does not match its table:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A Store builder problem stops the helper before any database is read: the
/// message names the problem (the builder's own text), and does not blame the
/// database.
#[test]
fn a_store_builder_problem_is_named_not_blamed_on_the_database() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let src = main_src(
        "",
        "",
        "\n        |> Store.unique \"no_such_column\"",
        "",
        "",
    );
    let dir = project(
        "sky-dbcheck-problem",
        "driver = \"sqlite\"\npath = \"app.db\"",
        &src,
    );
    let (code, out, err) = sky(&dir, &["db", "check"], &[]);
    assert_eq!(code, 2, "{out}\n{err}");
    assert!(
        err.contains("the project's `db` was refused") && err.contains("no_such_column"),
        "the builder problem is not named:\n{err}"
    );
    assert!(
        !err.contains("reachable") && !err.contains("could not read the database"),
        "a builder problem was blamed on the database:\n{err}"
    );
    // Nothing was read, so nothing was created.
    assert!(
        !dir.join("app.db").exists() || std::fs::metadata(dir.join("app.db")).unwrap().len() == 0
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A database that cannot be reached is named as that, after the code's
/// schema was read.
#[test]
fn an_unreachable_database_is_named_as_the_cause() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let dir = project("sky-dbcheck-unreachable", "", &base_src());
    let (code, out, err) = sky(
        &dir,
        &["db", "check"],
        &[(
            "DATABASE_URL",
            "postgres://sky@127.0.0.1:1/none?sslmode=disable&connect_timeout=2",
        )],
    );
    assert_eq!(code, 2, "{out}\n{err}");
    assert!(
        err.contains("could not read the database"),
        "a connect failure is not named:\n{err}"
    );
    assert!(!err.contains("was refused"), "{err}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// The tables of the test's own schema (`current_schema()`).
fn pg_tables(pg: &Pg) -> String {
    pg.psql(
        "SELECT COALESCE(string_agg(table_name, ',' ORDER BY table_name), '') \
         FROM information_schema.tables WHERE table_schema = current_schema()",
        true,
    )
}

/// `sky db check` and `sky db status` write nothing to the database (v0.29.2
/// second Judge, gap 2): on PostgreSQL the helper's runtime created
/// `telemetry_log` / `telemetry_metric` / `telemetry_span` in the user's
/// database. An empty database stays empty after check, `--code`, `--sql` and
/// status.
#[test]
fn postgres_check_and_status_leave_an_empty_database_empty() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let pg = Pg::new();
    if !required(Need::Postgres, pg.is_some()) {
        return;
    }
    let pg = pg.unwrap();
    let dir = project("sky-dbcheck-pg-empty", "", &base_src());
    let env = [("DATABASE_URL", pg.dsn.as_str())];
    assert_eq!(pg_tables(&pg), "", "the schema is not empty to start with");
    for args in [
        &["db", "check"][..],
        &["db", "check", "--code"][..],
        &["db", "check", "--sql"][..],
        &["db", "status"][..],
    ] {
        let (code, out, err) = sky(&dir, args, &env);
        assert!(
            code == 0 || code == 1,
            "sky {args:?} exited {code}:\n{out}\n{err}"
        );
        assert_eq!(
            pg_tables(&pg),
            "",
            "sky {args:?} wrote to the database:\n{out}\n{err}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// A missing SQLite file is refused, not created: `sky db check` exits 2
/// naming the file, and neither it nor `sky db status` leaves `app.db`,
/// `app.db-wal` or `app.db-shm` behind (v0.29.2 second Judge, gap 2).
#[test]
fn sqlite_check_and_status_do_not_create_a_missing_database() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let dir = project(
        "sky-dbcheck-nofile",
        "driver = \"sqlite\"\npath = \"app.db\"",
        &base_src(),
    );
    let (code, out, err) = sky(&dir, &["db", "check"], &[]);
    assert_eq!(code, 2, "{out}\n{err}");
    assert!(
        err.contains("app.db does not exist"),
        "the missing file is not named:\n{err}"
    );
    // Without db/migrations, `sky db status` runs the app with SKY_DB_OP;
    // with them, the ledger helper. Neither creates the file.
    let _ = sky(&dir, &["db", "status"], &[]);
    std::fs::create_dir_all(dir.join("db").join("migrations")).unwrap();
    let (code, out, err) = sky(&dir, &["db", "status"], &[]);
    assert_ne!(code, 0, "{out}\n{err}");
    assert!(err.contains("app.db does not exist"), "{out}\n{err}");
    for f in ["app.db", "app.db-wal", "app.db-shm"] {
        assert!(!dir.join(f).exists(), "{f} was created");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// A Codec.moneyIn GBP field on a Codec.auto Money column (TEXT holding
/// `{"amount":…,"currency":…}`): `--sql` gives the check query for rows in
/// another currency, then the conversion that fails on such a row, and the
/// conversion run on PostgreSQL refuses a USD row instead of relabelling it
/// GBP (v0.29.2 second Judge, gap 1).
#[test]
fn postgres_money_column_conversion_never_relabels_a_currency() {
    if !required(Need::Go, tool_on_path("go")) {
        return;
    }
    let pg = Pg::new();
    if !required(Need::Postgres, pg.is_some()) {
        return;
    }
    let pg = pg.unwrap();
    let src = r#"module Main exposing (main, db)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Db.Store as Store
import Std.Codec as Codec
import Std.Money as Money exposing (Money)


type alias Price =
    { id : String
    , cost : Money
    }


prices : Store.Store Price
prices =
    Store.fromCodec "prices"
        (Codec.object Price
            |> Codec.field "id" .id Codec.string
            |> Codec.field "cost" .cost (Codec.moneyIn Money.GBP)
            |> Codec.buildObject
        )
        |> Store.primaryKey "id"


db : Store.Project
db =
    Store.project [ Store.toTable prices ]


main : Task Error ()
main =
    Task.succeed ()
"#;
    let dir = project("sky-dbcheck-money", "", src);
    let env = [("DATABASE_URL", pg.dsn.as_str())];
    pg.psql(
        "CREATE TABLE prices (id TEXT PRIMARY KEY NOT NULL, cost TEXT NOT NULL);\
         INSERT INTO prices VALUES ('a', '{\"amount\":\"1.50\",\"currency\":\"GBP\"}'),\
         ('b', '{\"amount\":\"5.00\",\"currency\":\"USD\"}'), ('c', '\"3\"'), ('d', '4')",
        true,
    );
    let (code, sql, err) = sky(&dir, &["db", "check", "--sql"], &env);
    assert_eq!(code, 1, "{sql}\n{err}");
    let check = "SELECT cost FROM prices WHERE cost::text LIKE '{%' AND \
                 upper(cost::json->>'currency') IS DISTINCT FROM 'GBP'";
    assert!(sql.contains(check), "no check query in:\n{sql}");
    let alter = sql
        .lines()
        .find(|l| l.contains("ALTER TABLE prices ALTER COLUMN cost TYPE NUMERIC"))
        .unwrap_or_else(|| panic!("no conversion in:\n{sql}"))
        .trim_start_matches("-- ")
        .trim_end_matches(';')
        .to_string();
    assert!(sql.find(check) < sql.find(&alter), "{sql}");
    assert_eq!(
        pg.psql(check, true),
        "{\"amount\":\"5.00\",\"currency\":\"USD\"}"
    );
    // The conversion fails on the USD row and changes nothing.
    let dsn = match pg.dsn.split_once("search_path=") {
        Some((head, schema)) => format!("{head}options=-csearch_path%3D{schema}"),
        None => pg.dsn.clone(),
    };
    let out = Command::new(&pg.psql)
        .arg(&dsn)
        .args(["-v", "ON_ERROR_STOP=1", "-tAc", &alter])
        .output()
        .expect("spawn psql");
    assert!(!out.status.success(), "the conversion took a USD row");
    assert!(
        String::from_utf8_lossy(&out.stderr).contains("not in GBP"),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(
        pg.psql("SELECT cost FROM prices WHERE id = 'b'", true),
        "{\"amount\":\"5.00\",\"currency\":\"USD\"}"
    );
    // Once the USD row is moved out, the conversion runs.
    pg.psql("DELETE FROM prices WHERE id = 'b'", true);
    pg.psql(&alter, true);
    assert_eq!(
        pg.psql(
            "SELECT string_agg(id || '=' || cost::text, ' ' ORDER BY id) FROM prices",
            true
        ),
        "a=1.50 c=3 d=4"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
