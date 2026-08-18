//! Smoke coverage for the `sky doc` verb paths.
//!
//! `sky doc`'s rendering helpers (project::render_module / render_doc_site) are
//! unit-tested inside the `project` crate, but the CLI verb paths had no
//! end-to-end coverage. Two cases here:
//!   (a) `sky doc <Module>` — terminal render → exit 0 + the module's real
//!       signatures in the output.
//!   (b) `sky doc --serve --port N` — build + spawn the bundled doc server,
//!       poll the port, GET `/` + `/api/symbols.json`, assert HTTP 200 with the
//!       expected content, then tear the whole process group down.
//!
//! The serve case needs a `go` toolchain (it compiles the bundled Sky.Http.Server
//! doc app). When go is absent it early-returns with a note. Every spawned
//! server is bounded by a poll deadline and killed via its process group so no
//! orphan survives the test.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::Command;
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_project(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-doc-{tag}-{}-{}",
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
        "name = \"doc-smoke\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\n\nmain : Task Error ()\nmain =\n    Task.succeed ()\n",
    )
    .unwrap();
    dir
}

#[test]
fn doc_module_prints_signatures() {
    let dir = scratch_project("module");
    let out = Command::new(SKY)
        .args(["doc", "Sky.Core.List"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "sky doc Sky.Core.List failed:\n{stdout}{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        stdout.contains("Sky.Core.List"),
        "doc output missing the module header:\n{stdout}"
    );
    // A couple of real signatures the module documents — pins that the terminal
    // renderer actually emitted typed signatures, not just the header.
    assert!(
        stdout.contains("map : (a -> b) -> List a -> List b"),
        "doc output missing `map` signature:\n{stdout}"
    );
    assert!(
        stdout.contains("filter :"),
        "doc output missing `filter` signature:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Minimal HTTP/1.0 GET over a raw TcpStream (avoids a curl / reqwest
/// dependency). Returns (status_code, body) or None if the connection failed.
fn http_get(port: u16, path: &str) -> Option<(u16, String)> {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).ok()?;
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .ok()?;
    let req = format!(
        "GET {path} HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(req.as_bytes()).ok()?;
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).ok()?;
    let text = String::from_utf8_lossy(&raw).into_owned();
    let status = text
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|c| c.parse::<u16>().ok())?;
    let body = text
        .split_once("\r\n\r\n")
        .map(|(_, b)| b.to_string())
        .unwrap_or_default();
    Some((status, body))
}

/// SIGKILL the child's entire process group (the bundled server is a grandchild
/// of the spawned `sky` process; they share the group we created).
fn kill_group(child: &std::process::Child) {
    #[cfg(unix)]
    {
        use nix::sys::signal::{killpg, Signal};
        use nix::unistd::Pid;
        let _ = killpg(Pid::from_raw(child.id() as i32), Signal::SIGTERM);
        std::thread::sleep(Duration::from_millis(300));
        let _ = killpg(Pid::from_raw(child.id() as i32), Signal::SIGKILL);
    }
}

#[test]
fn doc_serve_answers_http_200() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch_project("serve");
    // A port unlikely to collide (derived from pid, high range).
    let port: u16 = 20000 + (std::process::id() % 20000) as u16;

    let mut cmd = Command::new(SKY);
    cmd.args(["doc", "--serve", "--port", &port.to_string()])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped());
    #[cfg(unix)]
    {
        // New process group so we can reap the whole server tree.
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    let mut child = cmd.spawn().expect("spawn sky doc --serve");

    // Poll the port until it answers 200 (or the deadline elapses). First run may
    // build the bundled Sky.Http.Server doc app, so the window is generous.
    let deadline = Instant::now() + Duration::from_secs(180);
    let mut got: Option<(u16, String)> = None;
    while Instant::now() < deadline {
        // If the server crashed early, stop waiting.
        if let Ok(Some(status)) = child.try_wait() {
            kill_group(&child);
            panic!("doc --serve exited early with {status:?} before binding port {port}");
        }
        if let Some((code, body)) = http_get(port, "/") {
            if code == 200 {
                got = Some((code, body));
                break;
            }
        }
        std::thread::sleep(Duration::from_millis(500));
    }

    let index = match got {
        Some((code, body)) => {
            assert_eq!(code, 200, "index did not return 200");
            body
        }
        None => {
            kill_group(&child);
            let _ = std::fs::remove_dir_all(&dir);
            panic!("doc --serve never answered 200 on port {port} within 180s");
        }
    };

    // The index is the doc SPA shell; assert it renders the doc site.
    assert!(
        index.contains("Sky API docs") || index.contains("Sky.Core"),
        "index HTML missing expected doc content:\n{}",
        &index[..index.len().min(600)]
    );

    // The symbols index must be served and carry stdlib entries.
    let symbols = http_get(port, "/api/symbols.json");
    if let Some((code, body)) = symbols {
        assert_eq!(code, 200, "/api/symbols.json did not return 200");
        assert!(
            body.contains("Sky.Core.List") && body.contains("\"module\""),
            "symbols.json missing expected entries:\n{}",
            &body[..body.len().min(400)]
        );
    } else {
        kill_group(&child);
        let _ = std::fs::remove_dir_all(&dir);
        panic!("/api/symbols.json connection failed");
    }

    kill_group(&child);
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&dir);
}
