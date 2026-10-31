//! Coverage for `Std.Ai.Trace` — the token + cost ledger, fully offline.
//!
//! The `ai-trace` fixture prices two model calls with known token usage against a
//! price book (input 0.15/1k, output 0.60/1k USD) and rolls the cost up per run.
//! The two calls cost 0.75 and 0.30, so the run total is exactly 1.05 — summed in
//! Sky over `Std.Decimal`, never a float. It prints:
//!
//!     trace spans=2 total=1.05
//!     progress steps=2 peak1=1089914008 eval1=2.5 peak2=1100000000
//!
//! which proves `record` persists a trace, `spanCount` counts the calls, and
//! `totalCost` sums the per-call costs exactly; and that a training step's
//! peak memory is recorded once (a replayed step adds no row and keeps the
//! first peak) while a replay fills the eval loss the first write lacked.
//! Needs a `go` toolchain. No network.

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
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
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
    assert!(
        stdout.contains("progress steps=2 peak1=1089914008 eval1=2.5 peak2=1100000000"),
        "a replayed step records its peak memory once and fills its eval loss; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
