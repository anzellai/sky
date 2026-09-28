//! End-to-end: `App.serve` / `App.address` / `App.stop` from a Task program,
//! built with the real compiler and run.
//!
//! The `app-serve` fixture is a Task program with no TEA loop of its own. It
//! starts two Sky.Live apps with `App.serve` on port 0, reads each with
//! `Sky.Core.Http`, stops one with `App.stop` (the port stops answering, the
//! other app keeps serving), restarts it on the SAME port, stops twice, and
//! starts a third app with `App.withSessionTransport HeaderToken` whose page
//! load hands out a session token and sets no session cookie. The Go-level
//! legs (runtime-go/rt/live_serve_test.go, live_session_header_test.go) run
//! per commit.
//!
//! Needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const LIMIT: Duration = Duration::from_secs(420);

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

/// Run `cmd` to completion under a deadline; stdout and stderr go to files so
/// a chatty child cannot block on a full pipe.
fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = unique("asf-out");
    let err_path = unique("asf-err");
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
        .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
        .spawn()
        .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + LIMIT;
    let status = loop {
        match child.try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                panic!("`{what}` did not finish within {}s", LIMIT.as_secs());
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

/// The built binary: a `Std.App` entry builds under `.skyapp/<target>/`.
fn app_binary(project: &Path) -> PathBuf {
    let std_app = project.join(".skyapp/web/sky-out/app");
    if std_app.is_file() {
        return std_app;
    }
    project.join("sky-out/app")
}

#[test]
// A real `go build` plus one run of the binary. Heavy for the per-commit T1
// budget; the Go-level legs run per commit.
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit legs: runtime-go/rt live_serve_test.go + live_session_header_test.go"]
fn a_task_program_serves_stops_and_restarts_live_apps() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/app-serve");
    let project = unique("app-serve");
    copy_dir(&fixture, &project);

    let build = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&project),
        "sky build src/Main.sky",
    );
    assert!(build.status.success(), "build failed:\n{}", both(&build));
    let app = app_binary(&project);
    assert!(
        app.is_file(),
        "no binary at {}\n{}",
        app.display(),
        both(&build)
    );

    let run = run_bounded(
        Command::new(&app)
            .current_dir(&project)
            .env_remove("SKY_LIVE_PORT")
            .env_remove("SKY_LIVE_SESSION_TRANSPORT")
            .env_remove("ENV")
            .env_remove("SKY_ENV"),
        "app-serve binary",
    );
    let out = both(&run);
    assert!(run.status.success(), "the Task program failed:\n{out}");
    for want in [
        "SERVE_A 200 shown",
        "SERVE_B 200",
        "DISTINCT_PORTS True",
        "STOPPED_A Ok",
        "STILL_B 200",
        "RESTART_A 200",
        "STOP_TWICE Ok",
        "HEADER_TOKEN True",
        "HEADER_NO_COOKIE True",
        "SERVE_FLOW_DONE",
    ] {
        assert!(out.contains(want), "missing {want:?} in:\n{out}");
    }

    let _ = std::fs::remove_dir_all(&project);
}
