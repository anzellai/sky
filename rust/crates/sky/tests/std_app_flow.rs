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

fn ui_layout_reject_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/std-app-ui-layout-reject/src/Main.sky")
}

fn ui_layout_any_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-ui-layout-any")
}

fn web_any_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-web-any")
}

/// REGRESSION GATE for the silent "compiles but renders an empty page" break:
/// an `App.app` (Std.Ui family, whose `view` must return `Element msg`) whose
/// view ROOTS at `Ui.layout` / `Ui.layoutWith`. Those produce a `Std.Html.Html`
/// DOCUMENT, and while they were typed `-> any` the mismatch was erased: the
/// `Html` was coerced into the `Element` view slot at runtime, yielding an empty
/// element — `sky check` passed, `go build` passed, the browser showed only the
/// root `<div>` + `<style>` with ZERO event attributes.
///
/// The fix gives `Ui.layout`/`layoutWith` their real return type (`Html msg`),
/// so this shape is now a COMPILE error. This gate runs on EVERY commit (the
/// browser check that originally caught it — `scripts/verify-live-app.mjs` — is
/// nightly-only). It is type-check only (rejection precedes `go build`), so it
/// needs no Go toolchain. PROVEN both directions: with `Ui.layout : … -> any`
/// (pre-fix) this fixture type-checks + `go build`s clean (gate would be RED);
/// with `-> Html msg` (post-fix) it is rejected with `Element _ vs Html _`
/// (gate GREEN).
#[test]
fn app_ui_view_rooted_at_layout_is_rejected_not_silently_emptied() {
    // No Go gate: a type mismatch is reported before the Go backend runs.
    let out = Command::new(SKY)
        .arg("check")
        .arg(ui_layout_reject_entry())
        .output()
        .expect("failed to run sky check on the ui-layout reject fixture");
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !out.status.success(),
        "an App.app (Std.Ui) view rooted at Ui.layout returns an Html document, \
         not an Element — it MUST be rejected, never silently coerced to an empty \
         page. It type-checked:\n{combined}"
    );
    // Pin the SHAPE of the rejection: the App.app boundary sees an Html result
    // where an Element view is required. Guards against the error degrading into
    // an unrelated failure that would pass the `!success` check vacuously.
    assert!(
        combined.contains("Element") && combined.contains("Html"),
        "expected an Element-vs-Html view type mismatch at the App.app boundary:\n{combined}"
    );
}

/// REGRESSION GATE for the confirmed "compiles but renders a SILENTLY EMPTY
/// page" soundness break that the reject fixture above does NOT catch.
///
/// The reject fixture pins the CONCRETE-msg shape (an event handler pins `msg`,
/// so `Ui.layout`'s `Html msg` collides with the `Element msg` view slot and is
/// rejected at type-check). THIS fixture pins the shape that escapes that gate:
/// the view is annotated `Model -> any` AND `msg` is left polymorphic (no
/// handler). `any` + polymorphic `msg` lets `Html a` unify with `Element msg`,
/// so `sky check` (and `go build`) accept it — and because `Std.Ui.Element` and
/// `Std.Html.Html` are BOTH the `rt.SkyADT` alias, the runtime `rt.Coerce` at
/// the config boundary is a no-op: the raw `Html` document reached the runner
/// unchanged, `Ui.layout` re-wrapped it, and Html's Tag-0 `HElement` was read as
/// Element's Tag-0 `Empty` → a blank page (`curl` body carried the root `<div>`
/// only, zero `count=`).
///
/// The fix (`Std_App_htmlDocOrDefault`, runtime-go/rt/std_app_view.go) routes on
/// the runtime constructor NAME, making the escape HARMLESS — the document
/// renders. So this gate BUILDS + RUNS the app and asserts the served HTML
/// carries the view's `count=` content. PROVEN both directions: reverting the
/// fix (renderer back to `Ui.layout [] (v model)`) rebuilds a binary whose `GET
/// /` body has zero `count=` (gate RED); with the fix the body contains `count=`
/// (gate GREEN). Needs a Go toolchain to build + run, so it gates via
/// `live_gate` (loud skip, never silent).
#[test]
fn app_ui_view_annotated_any_rooted_at_layout_renders_not_silently_empty() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(ui_layout_any_fixture_dir(), "uilayoutany");

    // ── Compile leg: this shape type-checks + go-builds (that is the whole
    // point — it slips past the type-level reject gate above). ──
    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build on the ui-layout-any fixture");
    assert!(
        build.status.success(),
        "the `view : Model -> any` + Ui.layout + polymorphic-msg app must still \
         build (it is accepted by design; the fix makes it RENDER, not reject):\n\
         --- stdout ---\n{}\n--- stderr ---\n{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr),
    );

    // ── Runtime leg: run the web binary and assert the served body is NOT the
    // empty root — it must carry the view's `count=` text. ──
    let port = 8479u16;
    let app_bin = dir.join(".skyapp").join("web").join("sky-out").join("app");
    assert!(
        app_bin.exists(),
        "expected the web app binary at {}",
        app_bin.display()
    );
    let log_path = dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&dir)
        .env("SKY_LIVE_PORT", port.to_string())
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn compiled ui-layout-any app");

    let ready = wait_for_listening(&log_path, port, 60);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        panic!("app never reported listening on :{port}\nlog:\n{buf}");
    }

    let body = curl_body(port, "/");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&dir);

    let body = body.expect("GET / should return a body");
    assert!(
        body.contains("count="),
        "the `-> any` + Ui.layout view must render its content, not a blank page \
         — expected `count=` in the served HTML but the body was:\n{body}"
    );
}

/// The SYMMETRIC twin of the test above: `App.web` (Std.Html family, view slot
/// `Html msg`) with `view : Model -> any` returning a Std.Ui `Element`.
/// `Element`/`Html` share `rt.SkyADT`, so the `any` slot accepts the Element and
/// `sky check` + `go build` pass — but the ViewHtml runner would render the
/// Element as Html, its constructors dispatching to nothing, and the page is
/// silently blank. The fix routes the ViewHtml root through `renderHtmlRoot_`,
/// wrapping a crossed-in Element in `Ui.layout []`. Reverting that route makes
/// this gate RED (served body is the empty `sky-root`, no `webcount=`).
#[test]
fn app_web_view_annotated_any_returning_element_renders_not_silently_empty() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(web_any_fixture_dir(), "webany");

    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build on the web-any fixture");
    assert!(
        build.status.success(),
        "the `App.web` + `view : Model -> any` returning an Element must still \
         build (accepted by design; the fix makes it RENDER, not reject):\n\
         --- stdout ---\n{}\n--- stderr ---\n{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr),
    );

    let port = 8481u16;
    let app_bin = dir.join(".skyapp").join("web").join("sky-out").join("app");
    assert!(
        app_bin.exists(),
        "expected the web app binary at {}",
        app_bin.display()
    );
    let log_path = dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&dir)
        .env("SKY_LIVE_PORT", port.to_string())
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn compiled web-any app");

    let ready = wait_for_listening(&log_path, port, 60);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        panic!("app never reported listening on :{port}\nlog:\n{buf}");
    }

    let body = curl_body(port, "/");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&dir);

    let body = body.expect("GET / should return a body");
    assert!(
        body.contains("webcount="),
        "the `App.web` + `-> any` Element view must render its content, not a \
         blank page — expected `webcount=` in the served HTML but the body \
         was:\n{body}"
    );
}

fn wait_for_listening(log_path: &std::path::Path, port: u16, tries: u32) -> bool {
    use std::io::Read as _;
    let needle = format!("Sky.Live listening on :{port}");
    for _ in 0..tries {
        if let Ok(mut f) = std::fs::File::open(log_path) {
            let mut buf = String::new();
            if f.read_to_string(&mut buf).is_ok() && buf.contains(&needle) {
                return true;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

// Full response BODY of `GET http://127.0.0.1:<port><path>`.
fn curl_body(port: u16, path: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl").args(["-s", &url]).output().ok()?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
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
    // The build also exposes the binary at the STANDARD `<project>/sky-out/app`,
    // not only under `.skyapp/<target>/`, so tooling that expects a direct
    // build's output path (example-sweep, the build-run gate, deploy) finds a
    // Std.App-built binary. A regression here silently breaks the sweep.
    assert!(
        dir.join("sky-out/app").is_file(),
        "Std.App build must copy the binary to the standard sky-out/app location"
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
