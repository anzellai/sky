//! Coverage for `Agent.nativeToolLoop` + `Provider.chatTools` — the OpenAI NATIVE
//! function-calling loop, fully offline. The `ai-native-tools` fixture drives one
//! run: the mocked model returns a `tool_calls` request (turn 1), the loop runs
//! the `lookup` tool as a journalled durable step and feeds the result back, and
//! the follow-up request (carrying the tool result — matched by the mock's
//! `bodyContains:"tool_call_id"`) gets the final answer (turn 2). It prints:
//!
//!     native answer=FINAL: the code is 4231 status=done
//!
//! which proves the native tools request/response wire format round-trips, the
//! two-turn loop discriminates via the request body, and the durable workflow
//! completes. Needs a `go` toolchain. No network.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-native-tools");
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-native-{}-{}",
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
fn native_tool_loop_round_trips_offline() {
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
        "ai-native-tools fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("native answer=FINAL: the code is 4231 status=done"),
        "the native tool loop should call the tool then return the final answer; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
