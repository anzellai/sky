//! End-to-end: `Process.events` and `Watch.changes` delivered to `update` in a
//! TEA app, and torn down when the model stops asking for them.
//!
//! The `process-watch-tea` fixture is a Sky.Cli app (`App.cli`, no input
//! handler). `init` spawns `sh -c 'echo one; echo two 1>&2; …; exit 4'`; the
//! `events` Sub delivers its stdout, its stderr and then `Exited`; `update`
//! then drops that Sub and starts a watcher on `FLOW_DIR`, writes a file there,
//! receives the `Created` batch through the `changes` Sub, drops that Sub and
//! closes the watcher.
//!
//! A Sky.Cli app without input exits only when nothing is left to happen,
//! and a subscription that is still running counts as something. So the exit
//! itself is the teardown assertion: a runner that outlived its dropped Sub
//! would keep the program alive past the deadline.
//!
//! Needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const BUILD_LIMIT: Duration = Duration::from_secs(420);
const RUN_LIMIT: Duration = Duration::from_secs(60);

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn unique(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), &dest).unwrap();
        }
    }
}

/// Run `cmd` to completion under a deadline; output goes to files so a chatty
/// child cannot block on a full pipe.
fn run_bounded(cmd: &mut Command, what: &str, limit: Duration) -> Output {
    let out_path = unique("pwt-out");
    let err_path = unique("pwt-err");
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
        .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
        .spawn()
        .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + limit;
    let status = loop {
        match child.try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                let out = std::fs::read_to_string(&out_path).unwrap_or_default();
                panic!(
                    "`{what}` did not finish within {}s (a subscription outlived its Sub?)\n{out}",
                    limit.as_secs()
                );
            }
            None => std::thread::sleep(Duration::from_millis(50)),
        }
    };
    let out = Output {
        status,
        stdout: std::fs::read(&out_path).unwrap_or_default(),
        stderr: std::fs::read(&err_path).unwrap_or_default(),
    };
    let _ = std::fs::remove_file(&out_path);
    let _ = std::fs::remove_file(&err_path);
    out
}

fn both(o: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&o.stdout),
        String::from_utf8_lossy(&o.stderr)
    )
}

#[test]
// A real `go build` plus one run of the binary: heavy for the per-commit T1
// budget. The per-commit legs are the Go tests in runtime-go/rt
// (process_spawn_test.go, watch_test.go: the same subscription manager this
// app runs on) and the ProcessWatch conformance suite.
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit legs: runtime-go/rt process_spawn_test.go + watch_test.go"]
fn events_and_changes_reach_update_and_tear_down() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/process-watch-tea");
    let project = unique("process-watch-tea");
    copy_dir(&fixture, &project);
    let watched = unique("process-watch-dir");
    std::fs::create_dir_all(&watched).unwrap();
    // macOS temp dirs sit behind a symlink; report paths as the app sees them.
    let watched = watched.canonicalize().unwrap();

    let build = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&project),
        "sky build src/Main.sky",
        BUILD_LIMIT,
    );
    assert!(build.status.success(), "build failed:\n{}", both(&build));
    let app = project.join(".skyapp/terminal-cli/sky-out/app");
    assert!(
        app.is_file(),
        "no binary at {}\n{}",
        app.display(),
        both(&build)
    );

    let run = run_bounded(
        Command::new(&app)
            .current_dir(&project)
            .env("FLOW_DIR", &watched),
        "process-watch-tea binary",
        RUN_LIMIT,
    );
    let out = both(&run);
    assert!(run.status.success(), "the app failed:\n{out}");
    let created = format!("changes=Created {}/flow.txt", watched.display());
    for want in [
        "out=one",
        "err=two",
        "exit=code 4",
        created.as_str(),
        "log=closed",
    ] {
        assert!(out.contains(want), "missing {want:?} in:\n{out}");
    }

    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&watched);
}
