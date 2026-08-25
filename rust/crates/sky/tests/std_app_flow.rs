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

fn dispatch_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-dispatch")
}

/// Copy the dispatch fixture to a fresh temp dir so per-target derived build
/// trees (`.skyapp/`) never land in the repo. Returns the temp project dir.
fn copy_dispatch_fixture_to_temp(tag: &str) -> PathBuf {
    let dst = std::env::temp_dir().join(format!("sky-stdapp-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dst);
    let status = Command::new("cp")
        .arg("-R")
        .arg(dispatch_fixture_dir())
        .arg(&dst)
        .status()
        .expect("cp -R fixture");
    assert!(status.success(), "failed to stage dispatch fixture to {}", dst.display());
    dst
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

#[test]
fn a_dispatched_entry_checks_across_all_backends() {
    // `sky check` on a dispatched entry (exposes `app`, no `main`) generates the
    // all-runners check module and checks it — so a break in ANY backend fails.
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = copy_dispatch_fixture_to_temp("check");
    let out = Command::new(SKY)
        .arg("check")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky check dispatched entry");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success() && (stdout.contains("Types OK") || stdout.contains("No errors")),
        "sky check on a dispatched Std.App entry should verify all backends:\n{stdout}\n{stderr}"
    );
}

#[test]
fn a_dispatched_entry_builds_terminal_cli_and_dce_prunes_other_backends() {
    // The derived `terminal:cli` entry references only `runCli`, so DCE must keep
    // `rt.Cli_program` out of the OTHER backends — a `terminal:cli` binary that
    // linked Webview/Spa/js would be a lowering regression (grill G5/G6).
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = copy_dispatch_fixture_to_temp("cli");
    let out = Command::new(SKY)
        .arg("build")
        .arg("--target")
        .arg("terminal:cli")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build --target terminal:cli");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "dispatched terminal:cli build failed:\n{stdout}\n{stderr}"
    );
    let main_go = dir.join(".skyapp/terminal-cli/sky-out/main.go");
    let go = std::fs::read_to_string(&main_go)
        .unwrap_or_else(|e| panic!("read {}: {e}", main_go.display()));
    let _ = std::fs::remove_dir_all(&dir);
    assert!(go.contains("rt.Cli_program"), "terminal:cli must link runCli");
    for pruned in ["rt.Webview_app", "rt.Spa_app", "rt.Live_app", "syscall/js"] {
        assert!(
            !go.contains(pruned),
            "DCE regression: terminal:cli binary links `{pruned}` (should be pruned)"
        );
    }
}

#[test]
fn a_dispatched_entry_without_a_target_errors_helpfully() {
    // No Go needed — this fails before any build.
    let dir = copy_dispatch_fixture_to_temp("notarget");
    let out = Command::new(SKY)
        .arg("build")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build dispatched entry without --target");
    let stderr = String::from_utf8_lossy(&out.stderr);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(!out.status.success(), "a dispatched entry without --target must fail");
    assert!(
        stderr.contains("needs a target"),
        "expected a 'needs a target' hint, got:\n{stderr}"
    );
}
