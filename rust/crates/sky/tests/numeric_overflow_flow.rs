//! End-to-end: Decimal.toInt / Decimal.toMinor / Decimal.toFloat and
//! Money.toMinor past the Int or Float range fail as the classified
//! `NumericOverflow`: exit 1 in a CLI, a 500 in a server that stays up.
//!
//! v0.29.2 seventh Judge: the four functions reach their kernels through
//! `Ffi.callPure`. Its recover turned the kernel's NumericOverflow panic into
//! an `Err`, and `rt.AsInt` / `rt.AsFloat` then called themselves on that
//! `Err` until the Go stack ran out: `fatal error: stack overflow`, exit 2,
//! which no recover catches, so one request ended the whole server. The
//! runtime now re-raises a loud-by-design panic at the Ffi boundary
//! (`isLoudByDesignPanic`) and no As* helper recurses on an `Err` or a
//! `Nothing` (`unwrapSkyContainer`). The Go-level legs run per commit
//! (`runtime-go/rt/numeric_overflow_callpure_test.go`).
//!
//! The `numeric-overflow` fixture runs one case as a CLI when `FLOW_CASE` is
//! set, and otherwise serves `/ok` and one route per case.
//!
//! Needs a `go` toolchain.

#![cfg(unix)]

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

#[path = "support/child_group.rs"]
mod child_group;
use child_group::ChildGroup;

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const LIMIT: Duration = Duration::from_secs(420);
const CASES: &[(&str, &str)] = &[
    ("toint", "NumericOverflow"),
    ("tominor", "NumericOverflow"),
    ("money", "NumericOverflow"),
    ("tofloat", "NumericOverflow"),
    ("places", "NumericOverflow"),
    ("fromminor", "NumericOverflow"),
    ("fixed", "NumericOverflow"),
    ("exponent", "NumericOverflow"),
    ("parts", "NumericOverflow"),
    ("noparts", "DivisionByZero"),
];

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
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
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
    let out_path = unique("nof-out");
    let err_path = unique("nof-err");
    let mut child = ChildGroup::spawn(
        cmd.stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap())),
    )
    .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + LIMIT;
    let status = loop {
        match child.child().try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                child.kill();
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

/// The server under test. `ChildGroup` ends it on every path, a failed
/// assertion included.
struct Server {
    child: ChildGroup,
    log: PathBuf,
}

impl Server {
    fn log_text(&self) -> String {
        std::fs::read_to_string(&self.log).unwrap_or_default()
    }
}

/// Build the fixture once, in a fresh directory. Returns the app binary.
fn build_fixture() -> (PathBuf, PathBuf) {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/numeric-overflow");
    let project = unique("numeric-overflow");
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
    (project, app)
}

#[test]
// A real `go build` plus a running server. Heavy for the per-commit T1 budget.
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit leg: runtime-go/rt/numeric_overflow_callpure_test.go"]
fn a_numeric_overflow_is_exit_1_in_a_cli_and_a_500_in_a_server() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let (project, app) = build_fixture();

    // CLI: each case exits 1 with the classified NumericOverflow, never the
    // `fatal error: stack overflow` (exit 2) or a wrapped number.
    for &(case, kind) in CASES {
        let out = run_bounded(
            Command::new(&app)
                .current_dir(&project)
                .env("FLOW_CASE", case)
                .env_remove("ENV")
                .env_remove("SKY_ENV"),
            case,
        );
        let text = both(&out);
        assert_eq!(
            out.status.code(),
            Some(1),
            "{case}: want exit 1 for the classified panic:\n{text}"
        );
        assert!(
            text.contains(kind),
            "{case}: the panic was not classified {kind}:\n{text}"
        );
        assert!(
            !text.contains("stack overflow"),
            "{case}: the process died of a stack overflow:\n{text}"
        );
    }
    // The bound itself still works: 9 ETH is 9e18 wei.
    let ok = run_bounded(
        Command::new(&app)
            .current_dir(&project)
            .env("FLOW_CASE", "ok")
            .env_remove("ENV")
            .env_remove("SKY_ENV"),
        "ok",
    );
    assert!(
        ok.status.success() && both(&ok).contains("ok: 9000000000000000000"),
        "the in-range case failed:\n{}",
        both(&ok)
    );

    // Server: each case is a 500 for its request, and the server stays up.
    let port = free_port();
    let log = unique("nof-server-log");
    let child = ChildGroup::spawn(
        Command::new(&app)
            .current_dir(&project)
            .env("FLOW_PORT", port.to_string())
            .env_remove("FLOW_CASE")
            .env_remove("ENV")
            .env_remove("SKY_ENV")
            .stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&log).unwrap()))
            .stderr(Stdio::from(
                std::fs::OpenOptions::new().append(true).open(&log).unwrap(),
            )),
    )
    .expect("start the server");
    let mut server = Server { child, log };

    let deadline = Instant::now() + Duration::from_secs(60);
    while get_status(port, "/ok") != Some(200) {
        assert!(
            Instant::now() < deadline,
            "the server never answered /ok:\n{}",
            server.log_text()
        );
        if let Ok(Some(st)) = server.child.child().try_wait() {
            panic!(
                "the server exited ({st}) before it answered:\n{}",
                server.log_text()
            );
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    for &(case, _) in CASES.iter().chain(CASES.iter()) {
        let path = format!("/{case}");
        let got = get_status(port, &path);
        assert_eq!(
            got,
            Some(500),
            "{path}: want a 500 for the classified panic, got {got:?}:\n{}",
            server.log_text()
        );
        assert!(
            server.child.child().try_wait().unwrap().is_none(),
            "{path}: a numeric overflow ended the server:\n{}",
            server.log_text()
        );
    }
    assert_eq!(
        get_status(port, "/ok"),
        Some(200),
        "the server stopped answering after the overflows:\n{}",
        server.log_text()
    );
    let log_text = server.log_text();
    assert!(
        log_text.contains("NumericOverflow")
            || (log_text.contains("outside the Int range")
                && log_text.contains("outside the Float range")),
        "the overflows were not logged with their class or message:\n{log_text}"
    );
    assert!(
        !log_text.contains("stack overflow"),
        "a stack overflow in the server log:\n{log_text}"
    );
}

/// An `Err` that reaches a value conversion fails as a classified panic; it is
/// never printed as text. The test replaces the `Decimal_toString` kernel with
/// one that panics with an unclassified message (no stdlib kernel does, so the
/// fixture injects one into the generated Go and rebuilds it). `Ffi.callPure`
/// turns that panic into an `Err`, and the bare-typed `Decimal.toString` hands
/// it to `rt.AsString`, which printed `{1 <nil> {0 Error [2 {Ffi
/// "Decimal_toString" panicked: …}]}}` and exited 0 before v0.29.2. It is now
/// the classified `CoerceFailure`: exit 1 in a CLI. The Go-level legs run per
/// commit (`runtime-go/rt/as_err_value_test.go`).
#[test]
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit leg: runtime-go/rt/as_err_value_test.go"]
fn an_err_reaching_a_string_conversion_is_a_classified_failure_not_text() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let (project, app) = build_fixture();
    let inject = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/numeric-overflow-inject/zz_kernel_panic.go");
    let out_dir = project.join("sky-out");
    std::fs::copy(&inject, out_dir.join("zz_kernel_panic.go")).unwrap();
    let rebuild = run_bounded(
        Command::new("go")
            .args(["build", "-o", "app", "."])
            .current_dir(&out_dir),
        "go build (kernel panic injected)",
    );
    assert!(
        rebuild.status.success(),
        "rebuild failed:\n{}",
        both(&rebuild)
    );
    let out = run_bounded(
        Command::new(&app)
            .current_dir(&project)
            .env("FLOW_CASE", "kernelpanic")
            .env_remove("ENV")
            .env_remove("SKY_ENV"),
        "kernelpanic",
    );
    let text = both(&out);
    assert_eq!(
        out.status.code(),
        Some(1),
        "want exit 1 for the classified panic:\n{text}"
    );
    assert!(
        text.contains("CoerceFailure"),
        "the panic was not classified CoerceFailure:\n{text}"
    );
    assert!(
        !text.contains("kernelpanic: "),
        "the Err was printed as text:\n{text}"
    );
}
