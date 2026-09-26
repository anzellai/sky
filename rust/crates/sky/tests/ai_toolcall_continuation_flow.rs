//! Coverage for `Provider.ToolCall.continuation` — provider-owned state a native
//! tool loop carries between model turns (the OpenAI Responses API's
//! `previous_response_id`, Gemini's per-call `thoughtSignature`). The
//! `ai-toolcall-continuation` fixture runs `Agent.nativeToolLoop` against a
//! `customTools` test double, fully offline. It prints:
//!
//!     legacy continuation=none
//!     continuation answer=FINAL cont=resp_1 args={"q":"code"} status=done
//!
//! The first line proves a `ToolResponse` journalled before the field existed
//! still decodes, so a durable run started by an older Sky resumes. The second
//! proves the value set on turn 1 reaches the provider on turn 2 through the
//! `Returned` turn and the durable journal, and that the tool received only the
//! arguments. Needs a `go` toolchain. No network.

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-toolcall-continuation");
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-continuation-{}-{}",
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
// T1 tier-budget: a go-build + run of the fixture, like `ai_native_tools_flow`.
// Runs in the release gate and nightly via `cargo test -p sky -- --ignored`.
#[ignore = "heavy go-build+run e2e leg; runs nightly (--ignored)"]
fn tool_call_continuation_reaches_the_next_turn() {
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
        "ai-toolcall-continuation fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("legacy continuation=none"),
        "a ToolResponse journalled without `continuation` must decode to Nothing; got:\n{stdout}"
    );
    assert!(
        stdout.contains(r#"continuation answer=FINAL cont=resp_1 args={"q":"code"} status=done"#),
        "the continuation set on turn 1 must reach the provider on turn 2; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
