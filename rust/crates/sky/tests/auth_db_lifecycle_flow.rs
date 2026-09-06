//! Layer-2 flow coverage for the Std.Auth lifecycle / RBAC surface and the
//! Std.Db transaction + by-id surface, ON REAL POSTGRES.
//!
//! # Why this exists (v1 blocker B8)
//!
//! The v1 audit found these two surfaces at GENUINE ZERO test coverage. For a
//! service authenticating toward tens of millions of users that is the exact
//! silent-regression class CLAUDE.md §0.2.1 exists to prevent — and the gap was
//! not hypothetical. Standing this coverage up surfaced four shipped
//! "if it compiles it works" breaks that no gate could see because nothing
//! exercised the functions:
//!
//!   * `Db.getById` / `updateById` / `deleteById` bound the id with `AsInt`,
//!     written for an OLD `Int` id signature; the shipped signatures take the
//!     id as a `String`, so any well-typed Sky call PANICKED with
//!     `rt.AsInt: expected numeric value, got string`.
//!   * `Db.getById` additionally returned `Ok(bareDict)` / `Err(NotFound)`
//!     instead of the `Maybe` its type advertises, so even a numeric id would
//!     CoerceFailure on the caller's `case … of Just … / Nothing`.
//!   * `Auth.setRole` returned the affected-row `Int` from `updateById` where
//!     its type says `Task Error ()`, so a well-typed caller CoerceFailed
//!     ("source int cannot be cast to target struct {}").
//!
//! The kernel-level fixes are locked by `runtime-go/rt/db_by_id_test.go`. This
//! file is the Layer-2 leg: it drives the REAL `sky` binary through
//! `sky run` against an embedded PostgreSQL cluster, so the compile → emit →
//! `go build` → run → real-SQL path is what asserts the security and
//! persistence properties — the leg the Go rt tests structurally cannot make.
//!
//! # What is actually asserted
//!
//! The apps assert the ACTUAL access / persistence DECISIONS, not that calls
//! return Ok. Each prints `CHECK <name>=PASS|FAIL`; the harness fails on any
//! `=FAIL`, on a missing expected check, or on a missing done-sentinel.
//!
//!   A (auth): a wrong password is DENIED; a default-role user is DENIED the
//!   role-gated action while an admin is ALLOWED it; a token signed with the
//!   typed `Secret` verifies with the right secret and is REJECTED with a wrong
//!   one; after `revokeUser` `isRevoked` flips true; after `disableUser` a
//!   subsequent login is DENIED and `userAccessState` reports `Disabled`.
//!
//!   B (db): a transaction returning Ok COMMITS both rows; a transaction that
//!   writes a row (proven visible on the tx handle) and then returns Err ROLLS
//!   BACK — the row is gone AND previously-committed data survives; by-id
//!   update/updateFields/delete address exactly one row; `Store.insertMany`
//!   persists a batch addressable by id.
//!
//! When no PostgreSQL or no Go toolchain is discoverable the live gate FAILS
//! (naming what to install) rather than skipping — `SKY_LIVE_TESTS=skip` is the
//! one documented opt-out.

use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// A `sky run` compiles the app through a real `go build` first (minutes on a
/// cold cache) and then runs it against a freshly `initdb`'d cluster. Generous
/// on purpose; what it rules out is the UNBOUNDED case that consumes a CI job
/// rather than failing (see the longer note in `db_run_cluster_flow.rs`).
const RUN_LIMIT: Duration = Duration::from_secs(420);

// ── App A: the Auth lifecycle / RBAC flow. ───────────────────────────────
const APP_AUTH: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Task as Task
import Sky.Core.Time as Time
import Sky.Core.Jwt as Jwt
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Secret as Secret exposing (Secret)
import Std.Db as Db
import Std.Auth as Auth
import Std.Log exposing (println)


secret : Secret
secret =
    Secret.unsafeFromString "sky-b8-auth-flow-secret-key-32-bytes!!"


wrongSecret : Secret
wrongSecret =
    Secret.unsafeFromString "sky-b8-WRONG-secret-key-32-bytes-long!!"


check : String -> Bool -> Task Error ()
check name ok =
    let
        _ =
            println ("CHECK " ++ name ++ "=" ++ (if ok then "PASS" else "FAIL"))
    in
    Task.succeed ()


roleOf : Db -> Int -> Task Error String
roleOf db uid =
    Db.getById db "users" (String.fromInt uid)
        |> Task.map
            (\mrow ->
                case mrow of
                    Just row ->
                        Db.getString "role" row

                    Nothing ->
                        "<no-row>"
            )


-- A role-gated action: only an "admin" may perform it. Returns the access
-- decision as a Result so the caller asserts on ALLOW vs DENY, not on Ok.
adminOnly : String -> Result Error String
adminOnly role =
    if role == "admin" then
        Ok "sensitive-admin-operation-performed"

    else
        Err (Error.permissionDenied |> Error.withMessage "forbidden: not an admin")


isAllowed : Result Error String -> Bool
isAllowed r =
    case r of
        Ok _ ->
            True

        Err _ ->
            False


verifyOk : Result Error String -> Bool
verifyOk r =
    case r of
        Ok _ ->
            True

        Err _ ->
            False


loginSucceeds : Db -> String -> String -> Task Error Bool
loginSucceeds db email pw =
    Auth.login db email pw
        |> Task.map (\_ -> True)
        |> Task.onError (\_ -> Task.succeed False)


tokenChecks : Int -> Task Error ()
tokenChecks uid =
    Time.now ()
        |> Task.andThen
            (\nowMs ->
                let
                    now =
                        nowMs // 1000
                in
                case Auth.signToken secret { sub = String.fromInt uid } 3600 of
                    Ok tok ->
                        check "token_signed" (String.length tok > 0)
                            |> Task.andThen
                                (\_ ->
                                    check "token_verified_with_correct_secret"
                                        (verifyOk (Auth.verifyTokenWithAlgorithm (Jwt.hs256 secret) now tok))
                                )
                            |> Task.andThen
                                (\_ ->
                                    check "token_rejected_with_wrong_secret"
                                        (not (verifyOk (Auth.verifyTokenWithAlgorithm (Jwt.hs256 wrongSecret) now tok)))
                                )

                    Err _ ->
                        check "token_signed" False
            )


runChecks : Db -> Int -> Int -> Task Error ()
runChecks db adminId memberId =
    loginSucceeds db "admin@example.com" "correct-horse-battery-staple"
        |> Task.andThen (\ok -> check "login_correct_password_granted" ok)
        |> Task.andThen (\_ -> loginSucceeds db "admin@example.com" "wrong-password")
        |> Task.andThen (\ok -> check "login_wrong_password_denied" (not ok))
        |> Task.andThen (\_ -> roleOf db memberId)
        |> Task.andThen (\r -> check "member_default_role_denied_admin_action" (not (isAllowed (adminOnly r))))
        |> Task.andThen (\_ -> Auth.setRole db adminId "admin")
        |> Task.andThen (\_ -> roleOf db adminId)
        |> Task.andThen
            (\r ->
                check "admin_role_persisted" (r == "admin")
                    |> Task.andThen (\_ -> check "admin_role_allows_admin_action" (isAllowed (adminOnly r)))
            )
        |> Task.andThen (\_ -> tokenChecks adminId)
        |> Task.andThen (\_ -> Auth.isRevoked db (String.fromInt adminId) 1)
        |> Task.andThen (\rev -> check "not_revoked_before_revokeUser" (not rev))
        |> Task.andThen (\_ -> Auth.revokeUser db (String.fromInt adminId))
        |> Task.andThen (\_ -> Auth.isRevoked db (String.fromInt adminId) 1)
        |> Task.andThen (\rev -> check "isRevoked_true_after_revokeUser" rev)
        |> Task.andThen (\_ -> Auth.disableUser db (String.fromInt adminId))
        |> Task.andThen (\_ -> Auth.isDisabled db (String.fromInt adminId))
        |> Task.andThen (\dis -> check "isDisabled_true_after_disableUser" dis)
        |> Task.andThen (\_ -> loginSucceeds db "admin@example.com" "correct-horse-battery-staple")
        |> Task.andThen (\ok -> check "disabled_user_login_denied" (not ok))
        |> Task.andThen (\_ -> Auth.userAccessState db (String.fromInt adminId) 1)
        |> Task.andThen (\st -> check "access_state_disabled" (st == Auth.Disabled))
        |> Task.andThen
            (\_ ->
                let
                    _ =
                        println "ALL-AUTH-CHECKS-DONE"
                in
                Task.succeed ()
            )


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen
            (\db ->
                Auth.register db "admin@example.com" "correct-horse-battery-staple"
                    |> Task.andThen
                        (\adminId ->
                            Auth.register db "member@example.com" "another-solid-passphrase"
                                |> Task.andThen (\memberId -> runChecks db adminId memberId)
                        )
            )
"#;

// ── App B: the Db transaction + by-id flow. ──────────────────────────────
const APP_DB: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Task as Task
import Sky.Core.Dict as Dict
import Sky.Core.Error as Error exposing (Error)
import Std.Db as Db
import Std.Db.Store as Store
import Std.Codec as Codec
import Std.Log exposing (println)


type alias Item =
    { id : Int
    , name : String
    , qty : Int
    }


items : Store.Store Item
items =
    Store.fromCodec "items" (Codec.auto { id = 0, name = "", qty = 0 })


check : String -> Bool -> Task Error ()
check name ok =
    let
        _ =
            println ("CHECK " ++ name ++ "=" ++ (if ok then "PASS" else "FAIL"))
    in
    Task.succeed ()


isJust : Maybe a -> Bool
isJust m =
    case m of
        Just _ ->
            True

        Nothing ->
            False


qtyOf : Db -> String -> Task Error Int
qtyOf db id =
    Db.getById db "items" id
        |> Task.map
            (\mrow ->
                case mrow of
                    Just row ->
                        Db.getInt "qty" row

                    Nothing ->
                        -1
            )


insertItem : Db -> Int -> String -> Int -> Task Error Int
insertItem db id name qty =
    Db.insertFields db
        "items"
        [ ( "id", Db.SetField (Db.SqlInt id) )
        , ( "name", Db.SetField (Db.SqlString name) )
        , ( "qty", Db.SetField (Db.SqlInt qty) )
        ]


-- A transaction body that writes a row and then fails, so the whole
-- transaction MUST roll back. We read the row back on the tx-scoped handle
-- first, to prove the write really happened inside the transaction (the
-- rollback is discarding a real write, not a no-op).
rollbackBody : Db -> Task Error Int
rollbackBody tx =
    insertItem tx 3 "ghost" 30
        |> Task.andThen (\_ -> Db.getById tx "items" "3")
        |> Task.andThen
            (\m ->
                check "tx_write_visible_inside_before_rollback" (isJust m)
                    |> Task.andThen (\_ -> Task.fail (Error.conflict "intentional rollback"))
            )


commitBody : Db -> Task Error Int
commitBody tx =
    insertItem tx 1 "widget" 10
        |> Task.andThen (\_ -> insertItem tx 2 "gadget" 20)
        |> Task.andThen (\_ -> Task.succeed 2)


runChecks : Db -> Task Error ()
runChecks db =
    -- 1. commit path: two inserts inside a transaction that returns Ok persist.
    Db.withTransaction db commitBody
        |> Task.andThen (\_ -> Db.getById db "items" "1")
        |> Task.andThen (\m1 -> check "tx_commit_persisted_row1" (isJust m1))
        |> Task.andThen (\_ -> Db.getById db "items" "2")
        |> Task.andThen (\m2 -> check "tx_commit_persisted_row2" (isJust m2))
        -- 2. by-id mutations.
        |> Task.andThen (\_ -> Db.updateById db "items" "1" (Dict.fromList [ ( "qty", "99" ) ]))
        |> Task.andThen (\_ -> qtyOf db "1")
        |> Task.andThen (\q -> check "updateById_applied" (q == 99))
        |> Task.andThen
            (\_ ->
                Db.updateFields db
                    "items"
                    [ ( "id", Db.SqlInt 2 ) ]
                    [ ( "qty", Db.SetField (Db.SqlInt 55) ) ]
            )
        |> Task.andThen (\_ -> qtyOf db "2")
        |> Task.andThen (\q -> check "updateFields_applied" (q == 55))
        -- 3. rollback path: a transaction that returns Err discards ALL its writes.
        |> Task.andThen
            (\_ ->
                Db.withTransaction db rollbackBody
                    |> Task.map (\_ -> False)
                    |> Task.onError (\_ -> Task.succeed True)
            )
        |> Task.andThen (\rolledBack -> check "tx_returned_err_and_rolled_back" rolledBack)
        |> Task.andThen (\_ -> Db.getById db "items" "3")
        |> Task.andThen (\m3 -> check "tx_rollback_discarded_write" (not (isJust m3)))
        -- committed data from before the failed transaction must survive it.
        |> Task.andThen (\_ -> Db.getById db "items" "1")
        |> Task.andThen (\m1 -> check "tx_rollback_preserved_committed_data" (isJust m1))
        -- 4. deleteById removes exactly the addressed row.
        |> Task.andThen (\_ -> Db.deleteById db "items" "1")
        |> Task.andThen (\_ -> Db.getById db "items" "1")
        |> Task.andThen (\m1 -> check "deleteById_removed" (not (isJust m1)))
        |> Task.andThen (\_ -> Db.getById db "items" "2")
        |> Task.andThen (\m2 -> check "deleteById_left_other_rows" (isJust m2))
        -- 5. insertMany (Store) batch-inserts, and the rows are addressable by id.
        |> Task.andThen (\_ -> Store.insertMany db items [ { id = 10, name = "a", qty = 1 }, { id = 11, name = "b", qty = 2 } ])
        |> Task.andThen (\n -> check "insertMany_reported_two" (n == 2))
        |> Task.andThen (\_ -> Db.getById db "items" "10")
        |> Task.andThen (\m10 -> check "insertMany_persisted_row" (isJust m10))
        |> Task.andThen
            (\_ ->
                let
                    _ =
                        println "ALL-DB-CHECKS-DONE"
                in
                Task.succeed ()
            )


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen
            (\db ->
                Db.execRaw db "CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, qty INTEGER NOT NULL)"
                    |> Task.andThen (\_ -> runChecks db)
            )
"#;

// ── environment discovery ────────────────────────────────────────────────

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// A PostgreSQL `bin` directory holding `initdb` + `pg_ctl` + `postgres`.
/// Mirrors `db_run_cluster_flow.rs`: Homebrew's `postgresql@N` kegs are not
/// symlinked onto PATH, so PATH alone misses the most common macOS install.
fn find_pg_bin() -> Option<PathBuf> {
    let complete =
        |d: &Path| ["initdb", "pg_ctl", "postgres"].iter().all(|b| d.join(b).is_file());
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
                let name = e.file_name().to_string_lossy().to_string();
                if name.starts_with("postgresql") {
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

fn unique(tag: &str) -> String {
    format!(
        "sky-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )
}

struct Fixture {
    project: PathBuf,
    sky_home: PathBuf,
    pg_bin: PathBuf,
    logs: PathBuf,
}

impl Fixture {
    fn new(tag: &str, app_src: &str) -> Option<Fixture> {
        if !have_go() {
            return None;
        }
        let pg_bin = find_pg_bin()?;
        let project = std::env::temp_dir().join(unique(tag));
        std::fs::create_dir_all(project.join("src")).unwrap();
        // `[database] embedded = true` makes `sky run` supervise a per-project
        // PostgreSQL cluster and inject its DSN — the app never names a tier.
        std::fs::write(
            project.join("sky.toml"),
            format!(
                "name = \"{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
                 [database]\nembedded = true\n"
            ),
        )
        .unwrap();
        std::fs::write(project.join("src").join("Main.sky"), app_src).unwrap();
        let logs = std::env::temp_dir().join(unique(&format!("{tag}-logs")));
        std::fs::create_dir_all(&logs).unwrap();
        Some(Fixture {
            project,
            sky_home: std::env::temp_dir().join(unique(&format!("{tag}-home"))),
            pg_bin,
            logs,
        })
    }

    /// `sky run src/Main.sky`, bounded, with output captured to files. Its own
    /// process group so a timeout takes down `sky run` AND the cluster + app it
    /// spawned, not just the handle we hold.
    fn run(&self) -> Output {
        let out_path = self.logs.join("run.out");
        let err_path = self.logs.join("run.err");
        let mut child = Command::new(SKY)
            .args(["run", "src/Main.sky"])
            .current_dir(&self.project)
            .env("SKY_HOME", &self.sky_home)
            .env("SKY_POSTGRES_BIN", &self.pg_bin)
            .env_remove("XDG_RUNTIME_DIR")
            // An inherited DSN would be read as the ambiguity `embedded` refuses.
            .env_remove("SKY_DB_PATH")
            .env_remove("DATABASE_URL")
            .stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
            .process_group(0)
            .spawn()
            .expect("failed to spawn sky run");
        let deadline = Instant::now() + RUN_LIMIT;
        let status = loop {
            match child.try_wait().unwrap() {
                Some(s) => break s,
                None if Instant::now() >= deadline => {
                    let pid = child.id() as i32;
                    let _ = nix::sys::signal::kill(
                        nix::unistd::Pid::from_raw(-pid),
                        nix::sys::signal::Signal::SIGKILL,
                    );
                    let _ = child.wait();
                    panic!(
                        "`sky run` did not finish within {}s — killed.\n\
                         --- stdout ---\n{}\n--- stderr ---\n{}",
                        RUN_LIMIT.as_secs(),
                        std::fs::read_to_string(&out_path).unwrap_or_default(),
                        std::fs::read_to_string(&err_path).unwrap_or_default(),
                    );
                }
                None => std::thread::sleep(Duration::from_millis(50)),
            }
        };
        Output {
            status,
            stdout: std::fs::read(&out_path).unwrap_or_default(),
            stderr: std::fs::read(&err_path).unwrap_or_default(),
        }
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        // Never leave a postmaster (or its data dir / socket) behind.
        let data_dir = self.project.join(".skydata").join("pg");
        let _ = Command::new(self.pg_bin.join("pg_ctl"))
            .arg("-D")
            .arg(&data_dir)
            .args(["-m", "immediate", "-w", "-t", "20", "stop"])
            .output();
        let _ = std::fs::remove_dir_all(&self.project);
        let _ = std::fs::remove_dir_all(&self.sky_home);
        let _ = std::fs::remove_dir_all(&self.logs);
    }
}

/// Assert every expected check printed `=PASS`, no check printed `=FAIL`, and
/// the done-sentinel was reached. `combined` is stdout+stderr of the run.
fn assert_all_checks(combined: &str, expected: &[&str], done_marker: &str) {
    // A `=FAIL` anywhere is a real access/persistence property that did not
    // hold — surface it directly.
    let fails: Vec<&str> = combined
        .lines()
        .filter(|l| l.contains("=FAIL"))
        .collect();
    assert!(
        fails.is_empty(),
        "flow reported failing checks:\n{}\n\n--- full output ---\n{combined}",
        fails.join("\n"),
    );
    for name in expected {
        let want = format!("CHECK {name}=PASS");
        assert!(
            combined.contains(&want),
            "expected check `{name}` was not reported as PASS.\n\
             (a missing check means the flow aborted before reaching it — often a \
             runtime panic upstream.)\n\n--- full output ---\n{combined}",
        );
    }
    assert!(
        combined.contains(done_marker),
        "the flow never reached its done sentinel `{done_marker}` — it aborted \
         partway.\n\n--- full output ---\n{combined}",
    );
}

fn combined(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

#[test]
fn auth_lifecycle_and_rbac_on_real_postgres() {
    let Some(fx) = Fixture::new("b8auth", APP_AUTH) else {
        // No Go (or no PostgreSQL when SKY_POSTGRES_BIN/PATH is empty) — gate
        // loudly. `required` panics under the default mode; `SKY_LIVE_TESTS=skip`
        // is the one documented opt-out.
        required(Need::Go, have_go());
        required(Need::Postgres, false);
        return;
    };
    let out = fx.run();
    let log = combined(&out);
    assert!(
        out.status.success(),
        "`sky run` (auth flow) exited non-zero:\n{log}",
    );
    assert_all_checks(
        &log,
        &[
            "login_correct_password_granted",
            "login_wrong_password_denied",
            "member_default_role_denied_admin_action",
            "admin_role_persisted",
            "admin_role_allows_admin_action",
            "token_signed",
            "token_verified_with_correct_secret",
            "token_rejected_with_wrong_secret",
            "not_revoked_before_revokeUser",
            "isRevoked_true_after_revokeUser",
            "isDisabled_true_after_disableUser",
            "disabled_user_login_denied",
            "access_state_disabled",
        ],
        "ALL-AUTH-CHECKS-DONE",
    );
}

#[test]
fn db_transaction_rollback_and_by_id_on_real_postgres() {
    let Some(fx) = Fixture::new("b8db", APP_DB) else {
        required(Need::Go, have_go());
        required(Need::Postgres, false);
        return;
    };
    let out = fx.run();
    let log = combined(&out);
    assert!(
        out.status.success(),
        "`sky run` (db flow) exited non-zero:\n{log}",
    );
    assert_all_checks(
        &log,
        &[
            "tx_commit_persisted_row1",
            "tx_commit_persisted_row2",
            "updateById_applied",
            "updateFields_applied",
            "tx_write_visible_inside_before_rollback",
            "tx_returned_err_and_rolled_back",
            "tx_rollback_discarded_write",
            "tx_rollback_preserved_committed_data",
            "deleteById_removed",
            "deleteById_left_other_rows",
            "insertMany_reported_two",
            "insertMany_persisted_row",
        ],
        "ALL-DB-CHECKS-DONE",
    );
}
