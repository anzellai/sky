//! End-to-end coverage for the `Std.Ai` durable TOOL-CALLING loop, fully offline.
//!
//! The `durable-agent-tools` fixture registers an `Agent.toolLoop` with a
//! `record` tool that inserts a row. Under test mode the mock makes the model
//! reply `{"tool":"record","args":"…"}`, so the agent calls the tool (as a
//! journalled `Durable.step`) then hits the 1-turn limit and completes. It prints:
//!
//!     tools records=1 status=done
//!
//! which proves the loop parses the model's tool request, runs the tool through a
//! durable step, and the workflow completes. Needs a `go` toolchain.

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
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-agent-tools");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-tools-{}-{}",
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
fn durable_tool_calling_agent_runs_a_tool_offline() {
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

    assert!(
        out.status.success(),
        "durable-agent-tools fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("tools records=1 status=done"),
        "the tool-calling loop should have run the `record` tool once and completed; \
         got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
