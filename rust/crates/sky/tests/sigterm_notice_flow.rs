//! A SIGTERM from another process (a memory watchdog) used to end `sky` with
//! status 143 and no output at all. `sky` now writes one line to stderr and
//! still ends by SIGTERM, so a parent sees the same status as before.

#![cfg(unix)]

use std::io::Read;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

#[test]
fn sky_stopped_by_sigterm_says_so_and_keeps_status_143() {
    use std::os::unix::process::ExitStatusExt;
    // `sky lsp` waits on its stdin, so it is alive when the signal comes.
    let mut child = Command::new(SKY)
        .arg("lsp")
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn sky lsp");
    std::thread::sleep(Duration::from_millis(800));
    let pid = nix::unistd::Pid::from_raw(child.id() as i32);
    nix::sys::signal::kill(pid, nix::sys::signal::Signal::SIGTERM).expect("send SIGTERM");
    let deadline = Instant::now() + Duration::from_secs(30);
    let status = loop {
        if let Some(s) = child.try_wait().expect("wait") {
            break s;
        }
        if Instant::now() > deadline {
            let _ = child.kill();
            panic!("sky did not stop on SIGTERM");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    let mut stderr = String::new();
    child
        .stderr
        .take()
        .unwrap()
        .read_to_string(&mut stderr)
        .unwrap();
    assert_eq!(status.signal(), Some(15), "status: {status:?}");
    assert!(
        stderr.contains("sky: stopped by SIGTERM from another process"),
        "stderr: {stderr:?}"
    );
}
