//! End-to-end: a classified panic inside a `Task.parallel` / `Task.parallelN`
//! branch is a 500 for that request, not the end of the server (C-1).
//!
//! The branch used to run on a goroutine with no `recover`, so a division by
//! zero in one branch killed the process: the per-request recovery in
//! Sky.Http.Server runs on the REQUEST goroutine and never saw it. The runtime
//! now starts every branch with `goSky` (runtime-go/rt/task_go.go), which
//! carries the panic back and re-raises it on the waiting goroutine.
//!
//! The `task-parallel-panic` fixture serves `/seq` (the same panic in
//! sequential code: always a 500), `/par`, `/parn` and `/ok`. The test asks
//! each panicking route twice and then `/ok`: every panic is a 500 and the
//! server still answers 200. The Go-level legs run per commit
//! (`runtime-go/rt/task_go_test.go`).
//!
//! Needs a `go` toolchain.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
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

/// Run `cmd` to completion under a deadline, output to files.
fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = unique("tpp-out");
    let err_path = unique("tpp-err");
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

/// One plain HTTP/1.1 GET. Returns the status code, or None when the
/// connection failed or was dropped (the server died).
fn get_status(port: u16, path: &str) -> Option<u16> {
    let mut s = TcpStream::connect(("127.0.0.1", port)).ok()?;
    s.set_read_timeout(Some(Duration::from_secs(10))).ok()?;
    write!(
        s,
        "GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"
    )
    .ok()?;
    let mut buf = String::new();
    s.read_to_string(&mut buf).ok()?;
    let line = buf.lines().next()?;
    line.split_whitespace().nth(1)?.parse().ok()
}

struct Server {
    child: Child,
    log: PathBuf,
}

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

impl Server {
    fn log_text(&self) -> String {
        std::fs::read_to_string(&self.log).unwrap_or_default()
    }
}

#[test]
// A real `go build` plus a running server. Heavy for the per-commit T1 budget.
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit leg: runtime-go/rt/task_go_test.go"]
fn a_panic_in_a_parallel_branch_is_a_500_and_the_server_stays_up() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/task-parallel-panic");
    let project = unique("task-parallel-panic");
    copy_dir(&fixture, &project);
    let build = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&project),
        "sky build src/Main.sky",
    );
    assert!(build.status.success(), "build failed:\n{}", both(&build));
    let app = project.join("sky-out/app");
    assert!(app.is_file(), "no binary at {}", app.display());

    let port = free_port();
    let log = unique("tpp-server-log");
    let child = Command::new(&app)
        .current_dir(&project)
        .env("FLOW_PORT", port.to_string())
        .env_remove("ENV")
        .env_remove("SKY_ENV")
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&log).unwrap()))
        .stderr(Stdio::from(
            std::fs::OpenOptions::new().append(true).open(&log).unwrap(),
        ))
        .spawn()
        .expect("start the server");
    let mut server = Server { child, log };

    let deadline = Instant::now() + Duration::from_secs(60);
    while get_status(port, "/ok") != Some(200) {
        assert!(
            Instant::now() < deadline,
            "the server never answered /ok:\n{}",
            server.log_text()
        );
        if let Ok(Some(st)) = server.child.try_wait() {
            panic!(
                "the server exited ({st}) before it answered:\n{}",
                server.log_text()
            );
        }
        std::thread::sleep(Duration::from_millis(100));
    }

    for path in ["/seq", "/par", "/parn", "/par", "/parn"] {
        let got = get_status(port, path);
        assert_eq!(
            got,
            Some(500),
            "{path}: want a 500 for the classified panic, got {got:?}:\n{}",
            server.log_text()
        );
        assert!(
            server.child.try_wait().unwrap().is_none(),
            "{path}: a panic in a parallel branch ended the server:\n{}",
            server.log_text()
        );
    }
    assert_eq!(
        get_status(port, "/ok"),
        Some(200),
        "the server stopped answering after the panics:\n{}",
        server.log_text()
    );
    let log_text = server.log_text();
    assert!(
        log_text.contains("DivisionByZero") || log_text.contains("division by zero"),
        "the panics were not logged with their class:\n{log_text}"
    );
}
