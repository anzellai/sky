//! End-to-end coverage for the Slack agent bot (`examples/66-slack-agent`), fully
//! offline. The `slack-agent` fixture drives one @mention through the same durable
//! reply workflow the example server runs, with both HTTP boundaries mocked:
//!
//!   * the LLM (`/v1/chat/completions`) -> "MOCKED-REPLY", usage 1000+1000 tokens,
//!   * Slack (`/chat.postMessage`)      -> ts "1700000000.000100".
//!
//! It prints:
//!
//!     slack reply=MOCKED-REPLY posted=1700000000.000100 cost=0.00075 status=done
//!
//! which proves the model call and the Slack post each ran as a journalled durable
//! step, the outbound post went through the `Std.Ai.Policy` firewall (Low -> ran),
//! and the token+cost trace was captured exactly (1000/1000 tokens at
//! 0.00015/0.00060 per 1k = 0.00075). Needs a `go` toolchain. No network.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/slack-agent");
    let dir = std::env::temp_dir().join(format!(
        "sky-slack-agent-{}-{}",
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
fn slack_mention_runs_a_durable_agent_and_posts_a_reply() {
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
        "slack-agent fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout
            .contains("slack reply=MOCKED-REPLY posted=1700000000.000100 cost=0.00075 status=done"),
        "the mention should run a durable agent, post through the firewall, and capture cost; \
         got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
