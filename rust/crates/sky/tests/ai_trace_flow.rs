//! Coverage for `Std.Ai.Trace` — the token + cost ledger, fully offline.
//!
//! The `ai-trace` fixture prices two model calls with known token usage against a
//! price book (input 0.15/1k, output 0.60/1k USD) and rolls the cost up per run.
//! The two calls cost 0.75 and 0.30, so the run total is exactly 1.05 — summed in
//! Sky over `Std.Decimal`, never a float. It prints:
//!
//!     trace spans=2 total=1.05
//!
//! which proves `record` persists a trace, `spanCount` counts the calls, and
//! `totalCost` sums the per-call costs exactly. Needs a `go` toolchain. No network.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-trace");
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-trace-{}-{}",
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
fn trace_records_and_rolls_up_cost_exactly() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky run");
    let mut stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    stdout.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(
        out.status.success(),
        "ai-trace fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("trace spans=2 total=1.05"),
        "two traces should record and roll up to an exact 1.05 total; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
