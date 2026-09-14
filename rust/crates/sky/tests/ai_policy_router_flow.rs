//! Coverage for `Std.Ai.Policy` (the action firewall) and `Provider.router`.
//!
//! The `ai-policy-router` fixture runs three checks and prints:
//!
//!     policy-allow: ran:ok        -- an allowed low-risk action ran
//!     policy-high: pending        -- a high-risk action was held for approval,
//!                                    the effect did NOT run (the firewall works)
//!     router: MOCKED-ANSWER       -- a Provider.router dispatched to a backend,
//!                                    mocked offline (test mode)
//!
//! Needs a `go` toolchain; the router line runs under `SKY_TEST_MODE=1` so the
//! LLM HTTP call is answered by the fixture mock.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn stage_fixture() -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-policy-router");
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-policy-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

#[test]
fn policy_gates_actions_and_router_dispatches() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&dir)
        .env("SKY_TEST_MODE", "1")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky run");
    let mut stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    stdout.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(out.status.success(), "fixture exited non-zero:\n{stdout}");
    assert!(
        stdout.contains("policy-allow: ran:ok"),
        "an allowed action should run:\n{stdout}"
    );
    assert!(
        stdout.contains("policy-high: pending"),
        "a high-risk action should be held for approval, not run:\n{stdout}"
    );
    assert!(
        stdout.contains("router: MOCKED-ANSWER"),
        "the router should dispatch to a backend (mocked):\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
