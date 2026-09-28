//! End-to-end: a Task program (no TEA loop) using the v0.27 WebSocket and
//! embedded Sky.Live surfaces, built with the real compiler and run.
//!
//! The `task-ws-embedded-live` fixture is ONE program (one `go build`) with
//! three modes, chosen by `FLOW_MODE`:
//!
//!   - `ws` — `Task.spawn (Server.listen …)` serves a WebSocket endpoint whose
//!     handler uses `Ws.withOnFrame`; the same `main` connects with
//!     `Sky.Core.WebSocket` and reads with `receive` / `receiveWithin`. A text
//!     frame comes back as `Text`, the binary frame `00 ff` comes back as
//!     `Binary` with exactly those bytes, `receiveWithin 100` on a quiet socket
//!     is `Err Timeout`, and after `close` a receive is `Nothing`. Nothing else
//!     in the repository sends a WebSocket frame end to end.
//!   - `live` — `Task.spawn (App.run app)` with `App.withEmbedded`, next to a
//!     `Task.loop` that polls the Live app over HTTP until it answers 200; then
//!     `main` returns and the process exits 0.
//!   - `port-taken` — the embedded app's port is already held. Its Task fails
//!     with the port error and `main` reports it: the process is not exited by
//!     the runtime (a non-embedded Live app calls `ExitProcess(1)` here).
//!
//! Needs a `go` toolchain.

use std::net::TcpListener;
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
    let out_path = unique("twel-out");
    let err_path = unique("twel-err");
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

fn free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// The built binary: a `Std.App` entry builds under `.skyapp/<target>/`.
fn app_binary(project: &Path) -> PathBuf {
    let std_app = project.join(".skyapp/web/sky-out/app");
    if std_app.is_file() {
        return std_app;
    }
    project.join("sky-out/app")
}

fn run_mode(app: &Path, project: &Path, mode: &str, port: u16) -> Output {
    run_bounded(
        Command::new(app)
            .current_dir(project)
            .env("FLOW_MODE", mode)
            .env("FLOW_PORT", port.to_string())
            .env_remove("SKY_LIVE_PORT")
            .env_remove("ENV")
            .env_remove("SKY_ENV"),
        &format!("app FLOW_MODE={mode}"),
    )
}

#[test]
// A real `go build` plus three runs of the binary. Heavy for the per-commit
// T1 budget; the Go-level legs (runtime-go/rt/websocket_task_receive_test.go,
// server_websocket_frame_test.go, live_embedded_test.go) run per commit.
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit legs: runtime-go/rt websocket_task_receive_test.go + live_embedded_test.go"]
fn a_task_program_round_trips_websocket_frames_and_embeds_a_live_app() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/task-ws-embedded-live");
    let project = unique("task-ws-embedded-live");
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

    // ── ws: frame types round-trip through a server and a Task client ──
    let ws = run_mode(&app, &project, "ws", free_port());
    let out = both(&ws);
    assert!(ws.status.success(), "ws mode failed:\n{out}");
    for want in [
        "RECV Text text:hello",
        "RECV Binary 00ff",
        "TIMEOUT Timeout",
        "RECV Nothing",
        "WS_FLOW_DONE",
    ] {
        assert!(out.contains(want), "ws mode: missing {want:?} in:\n{out}");
    }

    // ── live: an embedded Live app answers next to a Task.loop ──
    let live = run_mode(&app, &project, "live", free_port());
    let out = both(&live);
    assert!(live.status.success(), "live mode failed:\n{out}");
    assert!(
        out.contains("LIVE_STATUS 200") && out.contains("LOOP_DONE"),
        "live mode: the loop did not reach the embedded app:\n{out}"
    );

    // ── port-taken: the embedded app's failure is main's Err to handle ──
    let held = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = held.local_addr().unwrap().port();
    let taken = run_mode(&app, &project, "port-taken", port);
    let out = both(&taken);
    drop(held);
    assert!(
        taken.status.success(),
        "port-taken: the process exited with {:?} (the runtime must not exit an embedded app's host):\n{out}",
        taken.status.code()
    );
    assert!(
        out.contains("EMBED_ERR") && out.contains("already in use"),
        "port-taken: main did not receive the port error:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&project);
}
