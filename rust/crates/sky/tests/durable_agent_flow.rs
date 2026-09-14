//! End-to-end coverage for `Std.Ai` running on `Std.Durable`, fully offline.
//!
//! The `durable-agent` fixture registers an `Agent.oneShot` whose model call is a
//! journalled `Durable.step`, starts it, and polls it. Under test mode
//! (`SKY_TEST_MODE=1`) the outbound OpenAI-compatible HTTP call is answered by
//! `tests/mocks/00-chat.json` (content `MOCKED-ANSWER`) — no network, no API key.
//! The fixture prints:
//!
//!     agent answer=MOCKED-ANSWER status=done
//!
//! which proves the whole chain: `Std.Ai.Provider` builds and sends the request,
//! the mock answers it, `Std.Ai.Agent` runs it as a durable step, and the
//! `Std.Durable` workflow completes. It needs a `go` toolchain (it builds the
//! emitted Go); when absent the live gate FAILS naming what to install.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-agent");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-agent-{}-{}",
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
fn durable_agent_runs_offline_against_a_mocked_provider() {
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
        "durable-agent fixture exited non-zero:\n{stdout}"
    );
    // The mocked LLM call was made through Std.Ai.Provider, journalled as a
    // Std.Durable step, and the workflow completed.
    assert!(
        stdout.contains("agent answer=MOCKED-ANSWER status=done"),
        "expected the durable agent to return the mocked answer and reach `done`; \
         got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
