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

fn guard_helper_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-guard-helper")
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
#[ignore = "heavy build+run of a web app; runs in the erasure-fuzz CI job (both are erasure-boundary soundness checks) so test-sky stays off the T1 critical path"]
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
#[ignore = "heavy build+run of a web app; runs in the erasure-fuzz CI job (both are erasure-boundary soundness checks) so test-sky stays off the T1 critical path"]
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
    assert!(
        status.success(),
        "failed to stage fixture to {}",
        dst.display()
    );
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
    // Bare `sky check` on a dispatched entry checks the target a bare `sky
    // build` builds (`web` here); the fixture declares `withNotFound`, so it
    // passes.
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

/// Pin the persisted `[app] target` of a staged fixture copy.
fn pin_app_target(dir: &std::path::Path, target: &str) {
    let toml = dir.join("sky.toml");
    let mut s = std::fs::read_to_string(&toml).expect("read sky.toml");
    s.push_str(&format!("\n[app]\ntarget = \"{target}\"\n"));
    std::fs::write(&toml, s).expect("write sky.toml");
}

#[test]
fn a_terminal_only_app_checks_and_builds_without_a_fallback() {
    // The phantom capability model must NOT force `notFound` on an app that never
    // targets web. A NoFallback app that pins its terminal backend in sky.toml:
    // bare check passes (it checks the pinned target); terminal:cli builds.
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(terminal_fixture_dir(), "term");
    pin_app_target(&dir, "terminal:cli");
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

/// SA-13 — `sky check` ≡ `sky build`. A bare `sky check` used to verify the
/// any-capability `runTui` adapter, so an app with no `withNotFound` (and no
/// pinned target) PASSED a bare check while a bare `sky build` (which builds
/// `web`) rejected it. A bare check must verify the same target a bare build
/// builds, and fail the same way.
#[test]
fn bare_check_verifies_the_target_a_bare_build_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(terminal_fixture_dir(), "checkeqbuild");
    let run = |verb: &str| {
        let out = Command::new(SKY)
            .arg(verb)
            .arg(dir.join("src/Main.sky"))
            .output()
            .expect("run sky");
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        (out.status.success(), text)
    };
    let (check_ok, check_text) = run("check");
    let (build_ok, build_text) = run("build");
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        !build_ok,
        "precondition: a bare build (web) of a no-fallback app fails:\n{build_text}"
    );
    assert!(
        !check_ok,
        "a bare `sky check` must fail where a bare `sky build` fails (check ≡ build):\n{check_text}"
    );
    assert!(
        check_text.contains("requires a fallback page"),
        "check must report the same fallback hint as the build:\n{check_text}"
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
    assert!(
        !out.status.success(),
        "web build without a fallback must fail"
    );
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
    assert!(
        go.contains("rt.Cli_program"),
        "terminal:cli must link runCli"
    );
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

/// SA-1 (security) — a guard attached through a LOCAL HELPER (`|> secured`,
/// `secured a = a |> App.withGuard guard`) must be enforced by the generated
/// `web:app` backend. The line-based App→Spa reader only saw `|> App.withGuard`
/// written inline in the pipeline, so the helper's guard was silently dropped
/// and `POST /_rpc/Reveal` ran the guarded effect and returned its result. The
/// structural reader follows the helper; the backend must answer 403 and never
/// run the effect.
#[test]
fn a_guard_attached_via_a_local_helper_is_enforced_on_web_app() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = copy_fixture_to_temp(guard_helper_fixture_dir(), "guardhelper");
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app"])
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky build --target web:app");
    let backend_dir = dir.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    if !(out.status.success() && app_bin.is_file()) {
        let _ = std::fs::remove_dir_all(&dir);
        panic!(
            "web:app build of the guard-helper fixture failed:\n{}\n{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
    }
    let port = 9231u16;
    let log_path = backend_dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("ENV", "production")
        .env("SKY_CONSOLE_AUTH", "off")
        .env("HOME", "/sky-guard-secret-home")
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn the web:app backend");
    let mut ready = false;
    for _ in 0..240 {
        let probe = Command::new("curl")
            .args(["-s", "-o", "/dev/null", "-w", "%{http_code}"])
            .arg(format!("http://127.0.0.1:{port}/"))
            .output();
        if let Ok(o) = probe {
            let code = String::from_utf8_lossy(&o.stdout).to_string();
            if !code.is_empty() && code != "000" {
                ready = true;
                break;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    let resp = if ready {
        Command::new("curl")
            .args([
                "-s",
                "-w",
                "\nHTTP %{http_code}",
                "-X",
                "POST",
                "-H",
                "Content-Type: application/json",
                "-H",
                "Authorization: Bearer test",
                "-d",
                "{\"flag\":\"none\",\"secret\":\"hidden\",\"log\":\"\"}",
            ])
            .arg(format!("http://127.0.0.1:{port}/_rpc/Reveal"))
            .output()
            .ok()
            .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
    } else {
        None
    };
    let _ = child.kill();
    let _ = child.wait();
    let log_text = std::fs::read_to_string(&log_path).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&dir);
    let resp =
        resp.unwrap_or_else(|| panic!("backend never answered on :{port}\nlog:\n{log_text}"));
    assert!(
        resp.contains("HTTP 403"),
        "the helper-attached guard must reject Reveal with 403:\n{resp}\nlog:\n{log_text}"
    );
    assert!(
        !resp.contains("sky-guard-secret-home"),
        "the guarded effect must not run:\n{resp}"
    );
}

fn desktop_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-desktop")
}

/// SA-9. `--target desktop` runs the Sky.Live server on a spawned task and
/// opens the native window once that server answers. `Task.spawn` drops the
/// spawned task's result, so a server that FAILED to start (here: the bind
/// address is not on this machine, which is not the port-in-use case that
/// already exits) was lost: the window probe polled a dead port for about 50 s
/// and then failed with no cause. It must fail at once, name the cause, and
/// open no window. (A headless test cannot cover the success path, where a
/// native window opens; the port the window and the probe use is covered by
/// `TestStdAppLivePort_FollowsEnvOverride`.)
#[cfg(target_os = "macos")]
#[test]
fn a_desktop_window_whose_live_server_fails_to_start_exits_at_once_naming_the_cause() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = copy_fixture_to_temp(desktop_fixture_dir(), "desktop-fail");
    {
        let _build_guard = BUILD_LOCK.lock().unwrap();
        let out = Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&dir)
            .output()
            .expect("run sky build");
        assert!(
            out.status.success(),
            "desktop fixture build failed:\n{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
    }
    let bin = [".skyapp/desktop/sky-out/app", "sky-out/app"]
        .iter()
        .map(|p| dir.join(p))
        .find(|p| p.is_file())
        .expect("no desktop binary built");
    let log_path = dir.join("run.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let started = std::time::Instant::now();
    let mut child = Command::new(&bin)
        .current_dir(&dir)
        // 192.0.2.1 is TEST-NET-1: never an address of this machine, so the
        // listener fails with "can't assign requested address".
        .env("SKY_HOST", "192.0.2.1")
        .env("SKY_LIVE_PORT", "9334")
        .stdin(std::process::Stdio::null())
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn desktop app");
    let status = loop {
        if let Some(s) = child.try_wait().unwrap() {
            break Some(s);
        }
        if started.elapsed() > std::time::Duration::from_secs(20) {
            let _ = child.kill();
            let _ = child.wait();
            break None;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    };
    let out = std::fs::read_to_string(&log_path).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&dir);
    let status = status.unwrap_or_else(|| {
        panic!(
            "the desktop app was still running 20 s after its Live server failed to \
             start (the failure was lost; the window probe kept polling):\n{out}"
        )
    });
    assert!(
        !status.success(),
        "a desktop app whose server cannot start must exit non-zero:\n{out}"
    );
    assert!(
        out.contains("failed to start") && out.contains("192.0.2.1"),
        "the exit must name the server's start failure and its cause:\n{out}"
    );
}

// ---- App.withAppUrl: the backend address a native shell loads ----

/// Stage the dispatch fixture with `step` added to its App value (and `extra`
/// top-level definitions appended). Returns the temp project dir.
fn app_url_fixture(tag: &str, step: &str, extra: &str) -> PathBuf {
    let dir = copy_fixture_to_temp(dispatch_fixture_dir(), tag);
    let main = dir.join("src/Main.sky");
    let src = std::fs::read_to_string(&main).expect("read fixture entry");
    let src = src.replace(
        "        |> App.withInput Line\n",
        &format!("        |> App.withInput Line\n{step}\n"),
    );
    assert!(src.contains(step), "the fixture step was not added");
    std::fs::write(&main, format!("{src}\n\n{extra}")).expect("write fixture entry");
    dir
}

fn build_output(dir: &std::path::Path, target: &str, env: &[(&str, &str)]) -> (bool, String) {
    let mut cmd = Command::new(SKY);
    cmd.arg("build")
        .arg("--target")
        .arg(target)
        .arg(dir.join("src/Main.sky"))
        .env_remove("SKY_APP_URL");
    for (k, v) in env {
        cmd.env(k, v);
    }
    let out = cmd.output().expect("sky build");
    (
        out.status.success(),
        format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        ),
    )
}

/// A builder argument the build cannot evaluate statically is a build error
/// that names the builder and says why — the phone shells bake the address in
/// at build time. It fails before any Go build, so no toolchain is needed.
#[test]
fn app_url_from_a_run_time_value_is_a_build_error_naming_the_builder() {
    let dir = app_url_fixture(
        "appurl-dynamic",
        "        |> App.withAppUrl (urlFor ())",
        "urlFor : () -> String\nurlFor _ =\n    \"https://example.test/\"\n",
    );
    let (ok, out) = build_output(&dir, "mobile:android", &[]);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        !ok,
        "a non-static App.withAppUrl must fail the build:\n{out}"
    );
    assert!(
        out.contains("App.withAppUrl") && out.contains("run time"),
        "the error must name the builder and say why:\n{out}"
    );
}

/// An invalid address is refused, naming where it came from: the builder, or
/// SKY_APP_URL (which overrides the builder).
#[test]
fn an_invalid_app_url_is_refused_naming_its_source() {
    let dir = app_url_fixture("appurl-ftp", "        |> App.withAppUrl \"ftp://x\"", "");
    let (ok, out) = build_output(&dir, "mobile:ios", &[]);
    assert!(!ok, "ftp:// must be refused:\n{out}");
    assert!(
        out.contains("App.withAppUrl") && out.contains("ftp"),
        "the error must name the builder value:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);

    let dir = app_url_fixture(
        "appurl-env",
        "        |> App.withAppUrl backendUrl",
        "backendUrl : String\nbackendUrl =\n    \"https://example.test/\"\n",
    );
    let (ok, out) = build_output(&dir, "mobile:android", &[("SKY_APP_URL", "not a url")]);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(!ok, "a bad SKY_APP_URL must be refused:\n{out}");
    assert!(
        out.contains("SKY_APP_URL") && out.contains("not a url"),
        "the error must name SKY_APP_URL:\n{out}"
    );
}

/// `sky check` ≡ `sky build`: the builder type-checks on the web target (where
/// it does nothing), and a client-target check rejects a non-static argument.
#[test]
fn app_url_checks_on_web_and_is_read_statically_by_a_client_check() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_guard = BUILD_LOCK.lock().unwrap();
    let dir = app_url_fixture(
        "appurl-check",
        "        |> App.withAppUrl \"https://example.test/\"",
        "",
    );
    let out = Command::new(SKY)
        .arg("check")
        .arg(dir.join("src/Main.sky"))
        .output()
        .expect("sky check");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success(),
        "App.withAppUrl must type-check on web:\n{text}"
    );

    let dir = app_url_fixture(
        "appurl-check-dyn",
        "        |> App.withAppUrl (urlFor ())",
        "urlFor : () -> String\nurlFor _ =\n    \"https://example.test/\"\n",
    );
    let out = Command::new(SKY)
        .arg("check")
        .arg("--target")
        .arg("mobile:ios")
        .arg(dir.join("src/Main.sky"))
        .env_remove("SKY_APP_URL")
        .output()
        .expect("sky check --target mobile:ios");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        !out.status.success() && text.contains("App.withAppUrl"),
        "a client-target check must reject a non-static App.withAppUrl:\n{text}"
    );
}
