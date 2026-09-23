//! Coverage for `Durable.compact` — history compaction (v2), fully offline.
//!
//! The `durable-compaction` fixture runs a one-step workflow to `done` (so its
//! journal has a row), then calls `compact db 0` and re-inspects. It prints:
//!
//!     compact before=1 after=0 removed=1 status=done output="ok"
//!
//! which proves compaction removes a terminal run's journal rows while the run
//! row and its output (the retained summary) survive. Needs a `go` toolchain.

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-compaction");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-compact-{}-{}",
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
// T1 tier-budget: a go-build + run of the fixture on the per-commit test-sky
// tier, which sits at ~96% of its 990s ceiling. Runs NIGHTLY via
// `cargo test -p sky -- --ignored`. Per-commit durable behaviour stays covered by
// the pre-existing v1 durable_flow.rs. Remove #[ignore] only with a matching T1
// budget cut.
#[ignore = "heavy go-build+run e2e leg; runs nightly (--ignored)"]
fn compact_collapses_terminal_journal_and_keeps_the_summary() {
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
        "durable-compaction fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("compact before=1 after=0 removed=1 status=done"),
        "compaction should remove the terminal run's journal (before=1 after=0 removed=1) \
         while keeping status=done; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
