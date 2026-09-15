//! Compile-guard for the durable-TEA wiring (`Durable.restoreCmd` / `snapshotCmd`
//! / `applyRestore` / `SnapshotEvent`) in a real `Std.App` app.
//!
//! The `durable-tea-app` fixture is a counter `App.app` that restores its model on
//! the first tick (`restoreCmd` in `init`) and snapshots it after each update
//! (`snapshotCmd`), routing the snapshot events through one added `Msg` variant.
//! It exercises the durable-TEA API against the actual `Std.App` builder and
//! `Cmd` types. This runs `sky check` (type-check + `go build`) and asserts it is
//! clean — the behaviour of the snapshot substrate itself is covered separately by
//! `durable_snapshot_flow.rs`. Needs a `go` toolchain.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-tea-app");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-tea-{}-{}",
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
// T1 tier-budget: a `sky check` (go build) of the fixture on the per-commit
// test-sky tier, which sits at ~96% of its 990s ceiling. Runs NIGHTLY via
// `cargo test -p sky -- --ignored`. The App.withDurable compile path stays
// covered PER-COMMIT by examples/67-durable-counter, which the build-corpus job
// go-builds every push. Remove #[ignore] only with a matching T1 budget cut.
#[ignore = "heavy go-build leg; runs nightly (--ignored). Per-commit compile: examples/67-durable-counter in build-corpus"]
fn durable_tea_wiring_type_checks_and_go_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky check");
    let mut stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    stdout.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(
        out.status.success() && stdout.contains("No errors found"),
        "the durable-TEA wiring should type-check and go-build clean in a real App.app; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
