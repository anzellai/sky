//! End-to-end regression for `sky run`'s embedded-cluster integration
//! (embedded-Postgres phase 4).
//!
//! The unit tests in `db_cluster.rs` prove the ref *arithmetic* — which refs
//! survive a prune, which combination of `explicit` and `refs` means "keep".
//! They cannot prove that a real `sky run`, exiting, leaves a real postmaster
//! alone. That is the property the feature exists for, and the only way to
//! observe it is to have two `sky run`s overlap against a real PostgreSQL and
//! watch what the first one's exit does to the second one's database.
//!
//! So this file builds a real Sky app that CONNECTS through the injected DSN and
//! queries the cluster, then drives the three claims:
//!
//!   1. a second `sky run`'s database survives the first one's exit,
//!   2. a cluster started by `sky db start` survives a `sky run` exiting,
//!   3. a `SIGKILL`ed `sky run` does not pin the cluster up forever.
//!
//! When no PostgreSQL or no `go` toolchain is discoverable the live tests
//! early-return with a note rather than failing, matching `db_cluster_flow.rs`
//! and `db_flow.rs`.

use std::io::Write;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// The ceiling on any single blocking `sky` invocation in this file.
///
/// Generous on purpose: a `sky run` compiles the app through a real `go build`,
/// which on a cold CI cache is minutes, and a bound that fires on a slow-but-
/// working machine is a flake of its own. What it rules out is the UNBOUNDED
/// case — the one that does not fail, it just consumes the job.
const SKY_LIMIT: Duration = Duration::from_secs(300);

/// The app under test. It prints the DSN it was handed, opens it, runs a query,
/// and then holds the process open for `SKY_P4_HOLD` milliseconds.
///
/// The query is the point. A test that only checked the environment variable
/// would pass on a DSN no client library can parse — and the shape of this DSN
/// (a `postgresql://` URL whose host is a *directory*) is exactly the kind that
/// looks right and is not.
const APP: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Task as Task
import Sky.Core.Time as Time
import Std.Db as Db
import Std.Log exposing (println)
import Std.System as System


holdMs : Task Error Int
holdMs =
    System.getenv "SKY_P4_HOLD"
        |> Task.onError (\_ -> Task.succeed "0")
        |> Task.map (\s -> Maybe.withDefault 0 (String.toInt s))


main =
    Task.run
        (System.getenv "SKY_DB_PATH"
            |> Task.andThen
                (\dsn ->
                    let
                        _ =
                            println ("DSN=[" ++ dsn ++ "]")
                    in
                    Db.connect ()
                )
            |> Task.andThen (\db -> Db.query db "select 42 as answer" [])
            |> Task.andThen
                (\rows ->
                    let
                        _ =
                            println ("ANSWER=" ++ Maybe.withDefault "?" (Maybe.map (Db.getString "answer") (List.head rows)))
                    in
                    Task.andThen Time.sleep holdMs
                )
        )
"#;

// ---- environment discovery ----------------------------------------------

/// A PostgreSQL `bin` directory holding all three required binaries. Mirrors
/// `db_cluster_flow.rs`: Homebrew's `postgresql@N` kegs are deliberately not
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

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

// ---- fixture -------------------------------------------------------------

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
    fn new(tag: &str) -> Option<Fixture> {
        if !have_go() {
            return None;
        }
        let pg_bin = find_pg_bin()?;
        let project = std::env::temp_dir().join(unique(tag));
        std::fs::create_dir_all(project.join("src")).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "name = \"p4-run\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
             [database]\nembedded = true\n",
        )
        .unwrap();
        std::fs::write(project.join("src").join("Main.sky"), APP).unwrap();
        let logs = std::env::temp_dir().join(unique(&format!("{tag}-logs")));
        std::fs::create_dir_all(&logs).unwrap();
        Some(Fixture {
            project,
            sky_home: std::env::temp_dir().join(unique(&format!("{tag}-home"))),
            pg_bin,
            logs,
        })
    }

    fn cmd(&self, args: &[&str]) -> Command {
        let mut c = Command::new(SKY);
        c.args(args)
            .current_dir(&self.project)
            // An isolated registry: never the developer's ~/.sky/clusters.json.
            .env("SKY_HOME", &self.sky_home)
            .env("SKY_POSTGRES_BIN", &self.pg_bin)
            .env_remove("XDG_RUNTIME_DIR")
            // A DSN inherited from the developer's shell would be read as the
            // ambiguity this feature refuses, and every test here would fail for
            // a reason that has nothing to do with the code.
            .env_remove("SKY_DB_PATH")
            .env_remove("DATABASE_URL");
        c
    }

    fn sky(&self, args: &[&str]) -> Output {
        self.sky_within(args, SKY_LIMIT)
    }

    /// Run `sky` to completion — but never for longer than `limit`.
    ///
    /// `Command::output()`, which this replaces, has NO BOUND. It returns when
    /// the child has exited *and* every inherited copy of its stdout pipe is
    /// closed, so one wedged descendant turns one test into an unbounded wait.
    /// In CI that is not a failing test, it is a failing JOB: on 2026-08-15
    /// `test-rest` ran `sky_watch_hands_the_same_cluster_to_the_app_it_spawns`
    /// for 22 minutes until the 30-minute budget expired and the run was
    /// cancelled. The cancellation is what makes an unbounded wait so expensive
    /// to diagnose: libtest prints captured output only once the binary
    /// finishes, so the evidence — including the assertion message of the OTHER
    /// test that had already failed — went with it. Every blocking `sky` call
    /// here is bounded now, whether or not the original wedge recurs; the point
    /// is that the next one fails in minutes carrying its output, rather than
    /// consuming a job and saying nothing.
    ///
    /// Output goes to FILES rather than pipes: nothing then depends on a reader
    /// keeping up, and whatever the command managed to say is still readable
    /// after a timeout. On expiry the whole process GROUP goes down (`sky run`
    /// and the app it spawned), and the panic carries the partial output.
    fn sky_within(&self, args: &[&str], limit: Duration) -> Output {
        let tag = unique("cmd");
        let out_path = self.logs.join(format!("{tag}.out"));
        let err_path = self.logs.join(format!("{tag}.err"));
        let mut child = self
            .cmd(args)
            // `Command::output()` nulls stdin for you; `spawn()` INHERITS it.
            // Keeping the null is not tidiness — a verb that ever reads from a
            // terminal would otherwise block on the harness's own stdin, and
            // that is a hang with no output at all to explain itself.
            .stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
            // Its own group, so a timeout can take down the tree rather than
            // just the process that happens to be holding the handle.
            .process_group(0)
            .spawn()
            .expect("failed to spawn sky");
        let deadline = Instant::now() + limit;
        let status = loop {
            match child.try_wait().unwrap() {
                Some(s) => break s,
                None if Instant::now() >= deadline => {
                    kill_process_group(child.id() as i32);
                    let _ = child.wait();
                    panic!(
                        "`sky {}` did not finish within {}s — killed.\n\
                         --- stdout ---\n{}\n--- stderr ---\n{}",
                        args.join(" "),
                        limit.as_secs(),
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

    /// Start `sky run` in the background, holding the app open for `hold_ms`,
    /// with its output captured to a file the test can poll.
    ///
    /// Its own process group, so the test can take down the whole tree
    /// (`sky run` *and* the app it spawned) with one signal — and, in the
    /// stale-ref test, take down only the `sky run` and leave a real orphan.
    fn spawn_run(&self, name: &str, hold_ms: u64) -> Run {
        let log = self.logs.join(format!("{name}.log"));
        let file = std::fs::File::create(&log).unwrap();
        let err = file.try_clone().unwrap();
        let child = self
            .cmd(&["run", "src/Main.sky"])
            .env("SKY_P4_HOLD", hold_ms.to_string())
            .stdout(Stdio::from(file))
            .stderr(Stdio::from(err))
            .process_group(0)
            .spawn()
            .expect("failed to spawn sky run");
        Run { child, log }
    }

    /// `sky watch`, backgrounded the same way. It never exits on its own, so the
    /// caller always takes it down by group.
    fn spawn_watch(&self, name: &str, hold_ms: u64) -> Run {
        let log = self.logs.join(format!("{name}.log"));
        let file = std::fs::File::create(&log).unwrap();
        let err = file.try_clone().unwrap();
        let child = self
            .cmd(&["watch", "src/Main.sky"])
            .env("SKY_P4_HOLD", hold_ms.to_string())
            .stdout(Stdio::from(file))
            .stderr(Stdio::from(err))
            .process_group(0)
            .spawn()
            .expect("failed to spawn sky watch");
        Run { child, log }
    }

    fn data_dir(&self) -> PathBuf {
        self.project.join(".skydata").join("pg")
    }

    fn postmaster_pid(&self) -> Option<i32> {
        std::fs::read_to_string(self.data_dir().join("postmaster.pid"))
            .ok()?
            .lines()
            .next()?
            .trim()
            .parse()
            .ok()
    }

    /// Is a postmaster ACTUALLY serving this data dir? The pid file alone is not
    /// an answer — a `SIGKILL`ed cluster leaves one behind.
    fn cluster_running(&self) -> bool {
        self.postmaster_pid().is_some_and(pid_alive)
    }

    fn registry(&self) -> serde_json::Value {
        let text = std::fs::read_to_string(self.sky_home.join("clusters.json"))
            .expect("registry was never written");
        serde_json::from_str(&text).expect("registry is not valid JSON")
    }

    fn entry(&self) -> serde_json::Value {
        let key = self.project.canonicalize().unwrap().display().to_string();
        let reg = self.registry();
        reg["clusters"][&key].clone()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = Command::new(self.pg_bin.join("pg_ctl"))
            .arg("-D")
            .arg(self.data_dir())
            .args(["-m", "immediate", "-w", "-t", "20", "stop"])
            .output();
        let _ = std::fs::remove_dir_all(&self.project);
        let _ = std::fs::remove_dir_all(&self.sky_home);
        let _ = std::fs::remove_dir_all(&self.logs);
    }
}

/// A backgrounded `sky run`, plus the file its output is going to.
struct Run {
    child: Child,
    log: PathBuf,
}

impl Run {
    fn output(&self) -> String {
        std::fs::read_to_string(&self.log).unwrap_or_default()
    }

    /// Block until the app has printed the line proving it connected, or fail
    /// with everything it did print — a timeout with no context is a second
    /// investigation.
    fn wait_for_connected(&self, what: &str) {
        wait_until(
            Duration::from_secs(180),
            || self.output().contains("ANSWER=42"),
            || format!("{what} never connected to the cluster. Its output:\n{}", self.output()),
        );
    }

    fn wait_for_exit(&mut self, what: &str) -> std::process::ExitStatus {
        let deadline = Instant::now() + Duration::from_secs(180);
        loop {
            match self.child.try_wait().unwrap() {
                Some(s) => return s,
                None => {
                    assert!(
                        Instant::now() < deadline,
                        "{what} never exited. Its output:\n{}",
                        self.output()
                    );
                    std::thread::sleep(Duration::from_millis(50));
                }
            }
        }
    }

    fn is_running(&mut self) -> bool {
        self.child.try_wait().unwrap().is_none()
    }

    /// Take down the whole tree — `sky run` and the app it spawned.
    fn kill_group(&mut self) {
        if self.child.try_wait().map(|s| s.is_some()).unwrap_or(true) {
            // Already reaped. Signalling the group now could reach a recycled
            // pgid, which is a worse outcome than leaving it alone.
            return;
        }
        kill_process_group(self.child.id() as i32);
        // BOUNDED. Reaping a process we have just SIGKILLed is immediate, so
        // this loop is normally one iteration — but `sky watch` never exits on
        // its own, and an unbounded `wait()` on a process that (for whatever
        // reason) was not signalled is an unbounded TEST. Give up loudly rather
        // than hold the harness open: this runs from `Drop` too, where a panic
        // during unwinding would abort the process.
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            match self.child.try_wait() {
                Ok(Some(_)) | Err(_) => return,
                Ok(None) if Instant::now() >= deadline => {
                    let _ = writeln!(
                        std::io::stderr(),
                        "warning: pid {} outlived a SIGKILL to its process group",
                        self.child.id()
                    );
                    let _ = self.child.kill();
                    let _ = self.child.try_wait();
                    return;
                }
                Ok(None) => std::thread::sleep(Duration::from_millis(20)),
            }
        }
    }
}

/// A test that fails at an assertion never reaches its own cleanup, and a
/// backgrounded `sky run` holding an app open for two minutes is exactly the
/// kind of orphan that then outlives the whole session — and, on the next run,
/// looks like a live reference to a cluster nobody is using.
impl Drop for Run {
    fn drop(&mut self) {
        self.kill_group();
    }
}

/// SIGKILL an entire process group, without shelling out.
///
/// The `kill` this used to spawn is a BINARY, resolved on PATH: procps-ng on
/// Linux, BSD kill on macOS, and on a slim image not present at all. Its spawn
/// error was discarded, so "there is no kill binary here" and "the group is
/// dead" were the same observation — followed by a `wait()` on a process that
/// had never been signalled. One `kill(2)`, no PATH, no package set, and the
/// two implementations cannot disagree about what a negative pid means.
fn kill_process_group(pid: i32) {
    if pid <= 0 {
        return;
    }
    let _ = nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(-pid),
        nix::sys::signal::Signal::SIGKILL,
    );
}

fn pid_alive(pid: i32) -> bool {
    // Signal 0 tests for existence + permission without delivering anything.
    pid > 0 && nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), None).is_ok()
}

fn wait_until(limit: Duration, cond: impl Fn() -> bool, msg: impl Fn() -> String) {
    let deadline = Instant::now() + limit;
    while !cond() {
        assert!(Instant::now() < deadline, "{}", msg());
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// Say — VISIBLY — that a live test did not run.
///
/// Deliberately NOT `eprintln!`: that goes through libtest's output capture,
/// and capture is only ever printed for a test that FAILED. A skipped live test
/// would report `... ok` and say nothing, which is how a green job can contain
/// no live coverage at all. The process's own stderr bypasses the capture.
fn skip(reason: &str) {
    let mut e = std::io::stderr();
    let _ = writeln!(e, "SKIPPED (live): {reason}");
}

fn stdout(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).to_string()
}
fn stderr(o: &Output) -> String {
    String::from_utf8_lossy(&o.stderr).to_string()
}
fn both(o: &Output) -> String {
    format!("{}{}", stdout(o), stderr(o))
}

// ---- the live claims -----------------------------------------------------

/// The baseline: `sky run` provisions the DSN and takes the cluster back down.
///
/// The app never reads any configuration of its own — it asks for `SKY_DB_PATH`,
/// connects, and queries. That is the "the binary never knows which tier it is
/// in" principle, observed rather than asserted.
#[test]
fn sky_run_starts_a_cluster_injects_the_dsn_and_stops_it_on_exit() {
    let Some(fx) = Fixture::new("p4-basic") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };

    let mut run = fx.spawn_run("basic", 0);
    run.wait_for_connected("sky run");
    let status = run.wait_for_exit("sky run");
    assert!(status.success(), "sky run failed:\n{}", run.output());

    let out = run.output();
    assert!(out.contains("embedded PostgreSQL"), "the run never announced the cluster:\n{out}");
    let socket_dir = PathBuf::from(fx.entry()["socket_dir"].as_str().unwrap().to_string());
    assert!(
        out.contains(&format!("DSN=[postgresql:///postgres?host={}]", socket_dir.display())),
        "the app was handed a different DSN than the cluster listens on:\n{out}"
    );

    // Ephemeral: nothing is left running, and the registry says so.
    assert!(!fx.cluster_running(), "sky run left a postmaster behind");
    assert_eq!(fx.entry()["pid"].as_i64(), Some(0));
    assert_eq!(fx.entry()["refs"].as_array().map(Vec::len), Some(0));
    assert_eq!(fx.entry()["explicit"].as_bool(), Some(false));
    assert!(
        !socket_dir.exists(),
        "an empty socket directory was left behind: {}",
        socket_dir.display()
    );
}

/// THE ref-count claim. Two `sky run`s overlap; the first one exits; the second
/// one's database must still be there.
///
/// Without the ref count this is a data-loss bug in the most literal sense: the
/// second app is mid-transaction when its server is shut down underneath it.
#[test]
fn a_second_concurrent_run_keeps_its_database_when_the_first_one_exits() {
    let Some(fx) = Fixture::new("p4-refs") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };
    // Warm the build cache so the timing below is about process lifetimes, not
    // about how long `go build` takes.
    let warm = fx.sky(&["build", "src/Main.sky"]);
    assert!(warm.status.success(), "warm-up build failed:\n{}", both(&warm));

    // First run: short-lived. Second: long enough to outlive it comfortably.
    let mut first = fx.spawn_run("first", 4_000);
    first.wait_for_connected("the first sky run");
    let started_pid = fx.postmaster_pid().expect("no postmaster after the first run connected");

    let mut second = fx.spawn_run("second", 30_000);
    second.wait_for_connected("the second sky run");
    assert_eq!(
        fx.postmaster_pid(),
        Some(started_pid),
        "the second run started a SECOND postmaster on the same data directory"
    );
    assert_eq!(
        fx.entry()["refs"].as_array().map(Vec::len),
        Some(2),
        "two concurrent runs did not produce two references: {}",
        fx.entry()
    );

    // The first one exits. This is the moment the bug would happen.
    let status = first.wait_for_exit("the first sky run");
    assert!(status.success(), "the first run failed:\n{}", first.output());
    assert!(
        second.is_running(),
        "the second run died before the assertion could be made:\n{}",
        second.output()
    );
    assert!(
        fx.cluster_running(),
        "the first run's exit stopped the second run's database (pid {started_pid} is gone)"
    );
    assert_eq!(
        fx.postmaster_pid(),
        Some(started_pid),
        "the cluster was restarted rather than kept"
    );
    assert_eq!(
        fx.entry()["refs"].as_array().map(Vec::len),
        Some(1),
        "the first run did not release its own reference: {}",
        fx.entry()
    );

    // And the last one out does take it down.
    second.kill_group();
    let after = fx.sky(&["run", "src/Main.sky"]);
    assert!(after.status.success(), "{}", both(&after));
    assert!(
        !fx.cluster_running(),
        "with every run gone the cluster must be stopped:\n{}",
        stdout(&after)
    );
}

/// `sky db start` is the persistent verb. A `sky run` may lean on the cluster it
/// started, but the run's exit must not take it away — that distinction is the
/// only reason the two verbs are separate.
#[test]
fn a_cluster_started_by_sky_db_start_survives_a_sky_run_exiting() {
    let Some(fx) = Fixture::new("p4-explicit") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };

    let start = fx.sky(&["db", "start"]);
    assert!(start.status.success(), "sky db start failed:\n{}", both(&start));
    let pid = fx.postmaster_pid().expect("no postmaster after sky db start");
    assert_eq!(fx.entry()["explicit"].as_bool(), Some(true));

    let mut run = fx.spawn_run("explicit", 0);
    run.wait_for_connected("sky run");
    let status = run.wait_for_exit("sky run");
    assert!(status.success(), "sky run failed:\n{}", run.output());
    assert!(
        run.output().contains("already running"),
        "sky run did not adopt the explicitly-started cluster:\n{}",
        run.output()
    );

    assert!(
        fx.cluster_running(),
        "`sky run` stopped a cluster it did not start — `sky db start` is supposed to be persistent"
    );
    assert_eq!(fx.postmaster_pid(), Some(pid), "the cluster was restarted, not kept");
    assert_eq!(fx.entry()["explicit"].as_bool(), Some(true), "persistence was cleared");

    // And the explicit verb still takes it down.
    let stop = fx.sky(&["db", "stop"]);
    assert!(stop.status.success(), "{}", both(&stop));
    assert!(!fx.cluster_running());
    assert_eq!(fx.entry()["explicit"].as_bool(), Some(false));
}

/// A `SIGKILL`ed `sky run` never releases its reference. If a recorded pid alone
/// counted as a reference, that cluster would be pinned up for the rest of the
/// session — a database `sky run` created and nothing can close.
///
/// The proof is the stronger one: not merely that `sky db stop` still works, but
/// that the NEXT ordinary `sky run` prunes the corpse, finds itself alone, and
/// stops the cluster on its own way out.
#[test]
fn a_sigkilled_run_leaves_a_stale_reference_that_does_not_pin_the_cluster() {
    let Some(fx) = Fixture::new("p4-stale") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };

    // The hold only has to outlive the SIGKILL below and the two assertions
    // after it — a couple of milliseconds. It used to be two MINUTES, which
    // bought nothing and set the blast radius: any run in which the group kill
    // failed to land left an app holding a cluster open for two minutes, inside
    // a 30-minute job budget shared with the whole Rust suite.
    let mut doomed = fx.spawn_run("doomed", 30_000);
    doomed.wait_for_connected("the doomed sky run");
    let pid = fx.postmaster_pid().expect("no postmaster after the doomed run connected");
    assert_eq!(fx.entry()["refs"].as_array().map(Vec::len), Some(1));

    // SIGKILL the whole tree: no release runs, and the reference is left on disk
    // naming a pid that will never come back.
    doomed.kill_group();
    assert!(
        fx.cluster_running(),
        "fixture invalid: the cluster went down with the killed run, so nothing is being pinned"
    );
    assert_eq!(
        fx.entry()["refs"].as_array().map(Vec::len),
        Some(1),
        "fixture invalid: the killed run released its reference, so this test proves nothing"
    );

    // A later ordinary run must not be blocked by the corpse — it prunes it,
    // adopts the running cluster, and takes it down on the way out.
    let next = fx.sky(&["run", "src/Main.sky"]);
    assert!(next.status.success(), "the next run failed:\n{}", both(&next));
    assert!(
        stdout(&next).contains("already running"),
        "the next run did not adopt the running cluster:\n{}",
        stdout(&next)
    );
    assert!(
        !fx.cluster_running(),
        "the stale reference pinned the cluster (pid {pid} is still serving)"
    );
    assert_eq!(
        fx.entry()["refs"].as_array().map(Vec::len),
        Some(0),
        "the stale reference survived: {}",
        fx.entry()
    );
}

/// `sky watch` respawns the app on every rebuild, so the DSN has to be attached
/// to the SPAWN, not to the process `sky watch` inherited. It takes one lease
/// for the whole session: a rebuild must not cycle the database underneath the
/// app it is replacing.
#[test]
fn sky_watch_hands_the_same_cluster_to_the_app_it_spawns() {
    let Some(fx) = Fixture::new("p4-watch") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };

    // As in the SIGKILL case: long enough to still be running when the session
    // is killed, no longer. See the note there.
    let mut watch = fx.spawn_watch("watch", 30_000);
    watch.wait_for_connected("the app sky watch spawned");
    let pid = fx.postmaster_pid().expect("no postmaster after the watched app connected");
    let socket_dir = fx.entry()["socket_dir"].as_str().unwrap().to_string();
    assert!(
        watch.output().contains(&format!("DSN=[postgresql:///postgres?host={socket_dir}]")),
        "the watched app was not handed the cluster's DSN:\n{}",
        watch.output()
    );
    assert_eq!(fx.entry()["refs"].as_array().map(Vec::len), Some(1));

    watch.kill_group();
    // The killed session's reference is stale, and an ordinary run must still be
    // able to adopt the cluster and put it away.
    let next = fx.sky(&["run", "src/Main.sky"]);
    assert!(next.status.success(), "{}", both(&next));
    assert!(!fx.cluster_running(), "the killed watch session pinned the cluster (pid {pid})");
}

// ---- configuration paths (no server needed) ------------------------------

/// An explicit DSN alongside `embedded = true` must stop the run, before any
/// build work, and name both ways out. Silently preferring either one writes
/// data somewhere the author did not intend.
#[test]
fn an_explicit_dsn_alongside_embedded_refuses_to_run() {
    let project = std::env::temp_dir().join(unique("p4-ambiguous"));
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"p4-amb\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [database]\nembedded = true\nurl = \"postgres://user:pw@example.invalid/app\"\n",
    )
    .unwrap();
    std::fs::write(project.join("src").join("Main.sky"), APP).unwrap();
    let home = std::env::temp_dir().join(unique("p4-amb-home"));

    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .env_remove("SKY_DB_PATH")
        .env_remove("DATABASE_URL")
        .output()
        .unwrap();
    // The environment is just as capable of pointing the app elsewhere.
    let from_env = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .env("SKY_DB_PATH", "postgres://from-the-shell/app")
        .output()
        .unwrap();

    // Clean up BEFORE asserting. If this test ever goes red because a run that
    // should have been refused went ahead, it has left a live postmaster on a
    // temp data directory, and an assertion that panics first would leak it for
    // the rest of the session.
    let built = project.join("sky-out").exists();
    if let Some(pg) = find_pg_bin() {
        let _ = Command::new(pg.join("pg_ctl"))
            .arg("-D")
            .arg(project.join(".skydata").join("pg"))
            .args(["-m", "immediate", "-w", "-t", "20", "stop"])
            .output();
    }
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);

    assert!(!out.status.success(), "an ambiguous configuration ran anyway:\n{}", both(&out));
    let msg = stderr(&out);
    assert!(msg.contains("sky.toml [database] url"), "{msg}");
    assert!(msg.contains("postgres://user:pw@example.invalid/app"), "{msg}");
    assert!(msg.contains("remove `embedded = true`"), "{msg}");
    // Refused BEFORE the build: no compiler output, no `sky-out`.
    assert!(!built, "the project was built before the configuration was rejected");

    assert!(!from_env.status.success(), "{}", both(&from_env));
    // The environment is checked FIRST: it is the more surprising of the two,
    // because nothing in the repository records it.
    assert!(
        stderr(&from_env).contains("SKY_DB_PATH = postgres://from-the-shell/app"),
        "{}",
        stderr(&from_env)
    );
}

/// A project that does not compile must not get a cluster.
///
/// The refusal (an ambiguous DSN) happens before the build so nobody waits
/// through a compile for it; the START happens after, so the ordinary
/// edit-compile-fail loop does not cycle a PostgreSQL up and back down on every
/// attempt — which on a first run also means an `initdb` the user never asked
/// for, in a project they cannot run yet.
#[test]
fn a_build_failure_never_starts_a_cluster() {
    let Some(fx) = Fixture::new("p4-badbuild") else {
        skip("no PostgreSQL and/or no go toolchain");
        return;
    };
    std::fs::write(
        fx.project.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nmain =\n    this is not Sky\n",
    )
    .unwrap();

    let out = fx.sky(&["run", "src/Main.sky"]);
    assert!(!out.status.success(), "a broken program ran:\n{}", both(&out));
    assert!(
        !fx.project.join(".skydata").exists(),
        "a failing build initialised a data directory:\n{}",
        both(&out)
    );
    assert!(
        !fx.sky_home.join("clusters.json").exists(),
        "a failing build registered a cluster"
    );
    assert!(!fx.cluster_running());
}

/// A project that has NOT opted in must be untouched: it keeps its own DSN, gets
/// no cluster, writes nothing to the registry, and never looks for a PostgreSQL
/// to supervise.
///
/// The project here is an ordinary SQLite one — the shipped default — and the
/// assertion is that it still WORKS, end to end, with a `SKY_POSTGRES_BIN`
/// pointing at an empty directory. A `sky run` that consulted the cluster
/// supervisor for every project would fail here, and that is the point: the
/// feature has to cost nothing to the projects that did not ask for it.
#[test]
fn a_project_without_the_opt_in_gets_no_cluster_at_all() {
    if !have_go() {
        skip("no go toolchain");
        return;
    }
    let project = std::env::temp_dir().join(unique("p4-optout"));
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"p4-out\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [database]\ndriver = \"sqlite\"\npath = \"app.db\"\n",
    )
    .unwrap();
    std::fs::write(project.join("src").join("Main.sky"), APP).unwrap();
    let home = std::env::temp_dir().join(unique("p4-optout-home"));
    let empty = std::env::temp_dir().join(unique("p4-optout-bin"));
    std::fs::create_dir_all(&empty).unwrap();

    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        // No PostgreSQL to be found under the one lookup that is an error rather
        // than a fall-through.
        .env("SKY_POSTGRES_BIN", &empty)
        .env_remove("SKY_DB_PATH")
        .env_remove("DATABASE_URL")
        .output()
        .unwrap();
    let text = both(&out);
    assert!(
        out.status.success(),
        "an un-opted-in project no longer runs on its own database:\n{text}"
    );
    assert!(text.contains("ANSWER=42"), "the app never reached its own database:\n{text}");
    assert!(
        !text.contains("embedded PostgreSQL") && !text.contains("SKY_POSTGRES_BIN"),
        "an un-opted-in project went looking for a cluster:\n{text}"
    );
    assert!(
        !home.join("clusters.json").exists(),
        "an un-opted-in project wrote to the cluster registry"
    );
    assert!(!project.join(".skydata").exists(), "an un-opted-in project got a data directory");

    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);
    let _ = std::fs::remove_dir_all(&empty);
}
