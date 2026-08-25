//! Acceptance test for `Std.App` — the unified app builder (Phase 2a).
//!
//! The fragile guarantee this locks: ONE `App seed page model msg` value must
//! feed ALL FIVE backend runners (`runLive`/`runSpa`/`runTui`/`runCli`/
//! `runWebview`). The fixture `tests/fixtures/std-app` builds that one value and
//! lists all five runners in `allBackends` — so if a stdlib or compiler change
//! breaks the shared type, a view adapter, or a runner's backend-config
//! construction, the type-check fails here (grill G1 regression).
//!
//! `sky check` type-checks AND runs `go build` on the emitted Go, so it gates on
//! the Go toolchain via `live_gate` (loud skip, never silent).

use std::path::PathBuf;
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

fn fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app/src/Main.sky")
}

#[test]
fn all_five_runners_typecheck_and_build_off_one_app_value() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let out = Command::new(SKY)
        .arg("check")
        .arg(fixture_entry())
        .output()
        .expect("failed to run sky check");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "sky check on the Std.App all-runners fixture failed \
         (a runner no longer typechecks off the shared App value):\n--- stdout ---\n{stdout}\n--- stderr ---\n{stderr}"
    );
    assert!(
        stdout.contains("Types OK") || stdout.contains("No errors"),
        "expected a clean type-check + go build:\n{stdout}"
    );
}
