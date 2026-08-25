//! Acceptance test for `Std.App` — the unified app builder (Phase 2a).
//!
//! The fragile guarantees this locks:
//!   * ONE `App fallback seed page model msg` value feeds ALL FIVE backend
//!     runners (`runLive`/`runSpa`/`runTui`/`runCli`/`runWebview`) — the
//!     `std-app` fixture lists all five in `allBackends`, so a break in the
//!     shared type, a view adapter, or a runner's backend-config construction
//!     fails the type-check here (grill G1 regression).
//!   * The phantom capability flag: `web` (Live) requires `withNotFound`
//!     (`HasFallback`) at compile time, while terminal-only apps (`NoFallback`)
//!     are NOT forced to add one — verified target-scoped below.
//!
//! `sky check` type-checks AND runs `go build` on the emitted Go, so it gates on
//! the Go toolchain via `live_gate` (loud skip, never silent).

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

// Every test here `go build`s (some build a full spa split). Cargo runs them in
// parallel by default; several concurrent `go build`s contend and intermittently
// fail under load (same class as the db_cluster / spa_split flakes). Serialize the
// build bodies through one lock — only one compiles at a time.
static BUILD_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

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

fn terminal_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-terminal")
}

/// Copy a fixture to a fresh temp dir so per-target derived build trees
/// (`.skyapp/`) never land in the repo. Returns the temp project dir.
fn copy_fixture_to_temp(fixture: PathBuf, tag: &str) -> PathBuf {
    let dst = std::env::temp_dir().join(format!("sky-stdapp-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dst);
    let status = Command::new("cp")
        .arg("-R")
        .arg(&fixture)
        .arg(&dst)
        .status()
        .expect("cp -R fixture");
    assert!(status.success(), "failed to stage fixture to {}", dst.display());
    dst
}

#[test]
fn all_five_runners_typecheck_and_build_off_one_app_value() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
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
fn a_dispatched_entry_checks_target_scoped() {
    // Bare `sky check` on a dispatched entry (exposes `app`, no `main`) checks
    // the three view-adapter runners (runTui/runCli/runSpa) — none of which force
    // a capability — so a well-formed app passes without a fallback page.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(dispatch_fixture_dir(), "check");
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
        "sky check on a dispatched Std.App entry should verify the core:\n{stdout}\n{stderr}"
    );
}

#[test]
fn a_terminal_only_app_checks_and_builds_without_a_fallback() {
    // The phantom capability model must NOT force `notFound` on an app that never
    // targets web. A NoFallback app: bare check passes; terminal:cli builds.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(terminal_fixture_dir(), "term");
    let checked = Command::new(SKY)
        .arg("check")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky check terminal-only");
    assert!(
        checked.status.success(),
        "terminal-only (NoFallback) app must pass bare `sky check`:\n{}\n{}",
        String::from_utf8_lossy(&checked.stdout),
        String::from_utf8_lossy(&checked.stderr)
    );
    let built = Command::new(SKY)
        .arg("build")
        .arg("--target")
        .arg("terminal:cli")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build terminal-only");
    let ok = built.status.success();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        ok,
        "terminal-only (NoFallback) app must build for terminal:cli:\n{}\n{}",
        String::from_utf8_lossy(&built.stdout),
        String::from_utf8_lossy(&built.stderr)
    );
}

#[test]
fn web_without_a_fallback_gives_a_clean_error_not_a_phantom_leak() {
    // `--target web` on an app with no `withNotFound` must reprint the actionable
    // hint and SUPPRESS the raw `HasFallback vs NoFallback` from generated code.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(terminal_fixture_dir(), "webfail");
    let out = Command::new(SKY)
        .arg("build")
        .arg("--target")
        .arg("web")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build --target web terminal-only");
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(!out.status.success(), "web build without a fallback must fail");
    assert!(
        combined.contains("requires a fallback page") && combined.contains("withNotFound"),
        "expected the clean fallback hint:\n{combined}"
    );
    assert!(
        !combined.contains("HasFallback") && !combined.contains("NoFallback"),
        "the raw phantom-type error must be suppressed (points at generated code):\n{combined}"
    );
}

#[test]
fn a_std_app_entry_builds_web_app_via_synthesized_spa() {
    // Spa subsumption: `--target web:app` on a Std.App entry synthesises a Spa.app
    // (init/update/view/subscriptions referenced directly) and feeds the EXISTING
    // auto-split — so the client target builds from the ONE source, no Std.Spa
    // entry. Produces a backend binary + a wasm frontend.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(dispatch_fixture_dir(), "webapp");
    let out = Command::new(SKY)
        .arg("build")
        .arg("--target")
        .arg("web:app")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build --target web:app");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    let backend = dir.join(".skyapp/web-app/.split/backend/sky-out/app");
    let wasm = dir.join(".skyapp/web-app/.split/frontend/sky-out/main.wasm");
    let ok = out.status.success() && backend.exists() && wasm.exists();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        ok,
        "Std.App web:app synthesis must build a backend + wasm frontend:\n{stdout}\n{stderr}"
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
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(dispatch_fixture_dir(), "cli");
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
fn a_dispatched_entry_defaults_to_web_without_a_target() {
    // `--target` is optional: `main = App.run app` with no target builds `web`
    // (Sky.Live). The dispatch fixture is HasFallback (it calls withNotFound), so
    // the default web build succeeds.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(dispatch_fixture_dir(), "default");
    let out = Command::new(SKY)
        .arg("build")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build dispatched entry without --target");
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success() && combined.contains("--target web"),
        "a dispatched entry with no --target should default to web:\n{combined}"
    );
}
