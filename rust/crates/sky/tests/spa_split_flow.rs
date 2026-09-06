//! Acceptance test for `sky spa-split` (Phase B3/B4 of the Sky.Spa auto-split).
//!
//! Runs the source-to-source GENERATOR on the crafted skeleton fixture under
//! `tests/fixtures/spa-split` (Model {n,log}; Msg Bump|Persist; server helper
//! `saveN` with a File effect; `Bump` pure, `Persist` inline File effect →
//! read-set {n}, write-set {log}) and asserts:
//!
//!   1. The generator emits the three-tree split (shared / backend / frontend).
//!   2. SECURITY: no server-tainted value/function (`saveN`, `File.`, `Db.`,
//!      `System.`) leaks into the frontend source.
//!   3. Both projects BUILD — the backend natively and the frontend to wasm
//!      (`--target web`). Gated on the Go toolchain via `live_gate`.
//!
//! The build legs need Go + the wasm target, so they gate through `live_gate`;
//! the generation + leak-check legs need only the in-repo stdlib and always run.

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

// Each test in this file generates + `go build`s two projects (backend native +
// frontend wasm). Cargo runs the tests in a binary in parallel by default, so
// three of them at once means up to six concurrent `go build`s — which contend
// and time out under load, an intermittent false red (same class as the
// db_cluster flake). Serialize the build-heavy bodies through one lock: the
// generation + assertions are cheap, but only one test compiles at a time.
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
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-split/src/Main.sky")
}

fn todos_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-split-todos/src/Main.sky")
}

fn push_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-push-counter/src/Main.sky")
}

fn multimodule_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-multimodule/src/Main.sky")
}

fn clientonly_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-clientonly/src/Main.sky")
}

fn clientnative_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-clientnative/src/Main.sky")
}

fn explicit_rpc_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-explicit-rpc/src/Main.sky")
}

/// The wasm bundle is content-hashed (main.<hash>.wasm), so check for that shape
/// rather than a fixed `main.wasm`.
fn dist_has_wasm(dist: &std::path::Path) -> bool {
    std::fs::read_dir(dist)
        .map(|rd| {
            rd.filter_map(|e| e.ok()).any(|e| {
                let n = e.file_name().to_string_lossy().into_owned();
                n.starts_with("main.") && n.ends_with(".wasm")
            })
        })
        .unwrap_or(false)
}

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-spasplit-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    std::env::temp_dir().join(uniq)
}

/// Recursively copy `src` → `dst`, skipping generated build dirs (so a committed
/// fixture's stray local `.skyapp`/`sky-out`/`dist` never rides along). Used by
/// the `--target web:app` tests, which build INTO the project dir and so must
/// run on a scratch copy, never the checked-in tree.
fn copy_tree(src: &std::path::Path, dst: &std::path::Path) {
    std::fs::create_dir_all(dst).unwrap();
    for entry in std::fs::read_dir(src).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let n = name.to_string_lossy();
        if matches!(
            n.as_ref(),
            ".skyapp" | ".split" | "sky-out" | "sky-out-rust" | ".skycache" | ".skydeps" | "dist"
        ) {
            continue;
        }
        let from = entry.path();
        let to = dst.join(&name);
        if from.is_dir() {
            copy_tree(&from, &to);
        } else {
            std::fs::copy(&from, &to).unwrap();
        }
    }
}

fn web_config_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-web-config")
}

/// Write a minimal `sky.toml` + a Std.App `App.app` `src/Main.sky` from `main_sky`
/// into a fresh scratch project dir. Used by the BUG-2/BUG-3 `--target web:app`
/// synthesis tests, which need a specific `App.app` shape rather than a committed
/// fixture.
fn scratch_std_app(name: &str, main_sky: &str) -> PathBuf {
    let dir = scratch();
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"{name}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), main_sky).unwrap();
    dir
}

#[test]
fn generates_a_buildable_split_with_no_server_leak_into_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    // 1. Generate.
    let status = Command::new(SKY)
        .args([
            "spa-split",
            fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    // The three-tree split exists.
    for rel in [
        "shared/Shared.sky",
        "backend/src/Main.sky",
        "backend/src/Shared.sky",
        "backend/sky.toml",
        "frontend/src/Main.sky",
        "frontend/src/Shared.sky",
        "frontend/sky.toml",
    ] {
        assert!(out.join(rel).is_file(), "generator must write {rel}");
    }

    // 2. SECURITY — the frontend source must not contain any server-tainted
    // value or effect kernel. This is the spine of the split.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();
    for needle in ["File.", "saveN", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
    }
    // The backend, by contrast, MUST carry the effect (it runs it server-side).
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(back.contains("saveN"), "backend must keep the server effect saveN");
    assert!(
        back.contains("Server.api \"POST /_rpc/Persist\""),
        "backend must expose the generated RPC endpoint"
    );
    // The frontend must reach the effect through the typed RPC boundary instead.
    assert!(
        front.contains("Spa.postJson") && front.contains("/_rpc/Persist"),
        "frontend must call the RPC boundary for the server branch"
    );

    // 3. Both projects build (Go-gated). Backend native, frontend wasm.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "backend must build natively");
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend build must produce sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "frontend must build to wasm");
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );
    assert!(
        out.join("frontend/dist/index.html").is_file(),
        "frontend build must stage dist/index.html"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A client-ONLY app (every `update` branch pure, no effect kernels) still
/// spa-splits into a buildable backend. Regression for the empty-route-list bug:
/// with no RPC/push routes the generated `Server.listen` list opened
/// `[ , Server.static …]` — a leading comma the parser rejected — so `sky build`
/// on the generated backend failed with a PARSE ERROR. The static-asset route is
/// now a normal list entry, so the `[` always has a first element to attach to.
#[test]
fn client_only_app_generates_a_buildable_static_only_backend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientonly_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on a client-only app");

    // The backend has NO RPC routes (nothing was server-tainted) but MUST still
    // serve static assets — and the generated list must be well-formed.
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("Server.static \"/\" \"../frontend/dist\""),
        "backend must still serve the frontend's static assets"
    );
    assert!(
        !back.contains("/_rpc/"),
        "a client-only app must generate no RPC endpoints"
    );
    // The exact defect: a list opening with a leading comma.
    assert!(
        !back.contains("[\n        , Server.static") && !back.contains("[ , Server.static"),
        "backend Server.listen list must not open with a leading comma"
    );

    // The real proof: the generated backend BUILDS (it used to fail to parse).
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "client-only backend must build (regression: leading-comma parse error)"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend build must produce sky-out/app"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A `Std.Native.*` effect is a CLIENT effect: it must stay in the wasm frontend,
/// never become a server RPC. Native capabilities (`clipboardWrite`, `share`, …)
/// reach a browser/webview-only platform API whose `//go:build !js` counterpart is
/// an `Err` stub, so routing them server-side — the fail-closed default for an
/// unknown effect — would make every call fail (this fixture's kernels once
/// generated `/_rpc/Copy` + `/_rpc/Share`, and the round-trip hit the Err stubs).
/// `classify_kernel` now maps the `Native_` family to `ClientEffect`, so the
/// frontend keeps the kernel call and the backend generates NO RPC for it.
#[test]
fn native_effects_stay_client_side_not_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientnative_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on a client-native app");

    // The frontend KEEPS the native kernel calls in its `update` — they run in the
    // wasm client. (Both are inside a `Cmd.perform (Native.… ) Done`.)
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Native.clipboardWrite") && front.contains("Native.share"),
        "frontend must keep the Std.Native kernel calls (they run client-side)"
    );

    // The definitive proof it was NOT server-routed: the backend generates NO RPC
    // ENDPOINT for the native effects (a server-routed effect emits
    // `Server.api "POST /_rpc/<Msg>"`). It must still serve the static assets.
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        !back.contains("POST /_rpc/"),
        "a client-native effect must NOT generate an RPC endpoint on the backend"
    );
    assert!(
        back.contains("Server.static \"/\" \"../frontend/dist\""),
        "backend must still serve the frontend's static assets"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// The generated backend's DEFAULT port must match the port the generated
/// desktop / iOS / Android shells load, or a user who starts the backend bare
/// (`./app`, no PORT) and launches a shell lands on a dead port. Regression: the
/// backend defaulted to 8971 while every shell baked 8951, so the mobile shells
/// could not reach the backend on its own default. Both sides now default 8951.
#[test]
fn backend_default_port_matches_the_generated_shell() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientnative_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("getenvOr \"PORT\" \"8951\""),
        "backend serverPort must default to 8951 (the shells' port), got:\n{}",
        back.lines().filter(|l| l.contains("PORT")).collect::<Vec<_>>().join("\n")
    );

    // The shell generator (this crate's main.rs) must bake the SAME default, or
    // the two drift apart again. Pin them together.
    let main_rs = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src/main.rs"),
    )
    .unwrap();
    assert!(
        main_rs.contains("getenvOr \"PORT\" \"8951\"") && main_rs.contains("localhost:8951"),
        "the generated shell (main.rs) must load the same 8951 the backend serves"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// An app that imports an EXTERNAL Sky library (a `.skydeps/` package) must
/// survive spa-split: the generated frontend/backend need the `[dependencies]`
/// section AND a copy of the `.skydeps/` tree, or they can't rebuild the import.
/// Regression: before this, spa-split emitted a fixed manifest and copied only the
/// project's own src/, so a third-party import analysed fine but the generated
/// projects failed to resolve it. `.skydeps/` is gitignored, so the lib is
/// constructed here rather than checked in.
#[test]
fn spa_split_carries_external_sky_dependencies() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    let slug = "github.com_test_sky-greet";

    // The fetched library (as `sky add --sky` would leave it under .skydeps/).
    let lib_src = proj.join(".skydeps").join(slug).join("src/Ext");
    std::fs::create_dir_all(&lib_src).unwrap();
    std::fs::write(
        lib_src.join("Greet.sky"),
        "module Ext.Greet exposing (greet)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\
         greet : String -> String\ngreet name =\n    \"Hi from the lib, \" ++ name\n",
    )
    .unwrap();

    // The consumer project declaring the dependency + importing it.
    std::fs::write(
        proj.join("sky.toml"),
        "name = \"extdep\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n\n[dependencies]\n\"github.com/test/sky-greet\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::create_dir_all(proj.join("src")).unwrap();
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.Spa as Spa
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
import Ext.Greet exposing (greet)


type alias Model =
    { who : String }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { who = "Sky" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text (greet model.who))


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task Error ()
main =
    Spa.app
        (Spa.config
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#;
    std::fs::write(proj.join("src/Main.sky"), main_sky).unwrap();

    let out = proj.join("dist");
    let status = Command::new(SKY)
        .args(["spa-split", proj.join("src/Main.sky").to_str().unwrap(), "--out", out.to_str().unwrap()])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "spa-split should succeed on an app with an external dep");

    // The generated frontend must declare the dep AND carry its .skydeps source.
    let front_toml = std::fs::read_to_string(out.join("frontend/sky.toml")).unwrap();
    assert!(
        front_toml.contains("[dependencies]") && front_toml.contains("github.com/test/sky-greet"),
        "generated frontend manifest must carry [dependencies], got:\n{front_toml}"
    );
    assert!(
        out.join("frontend/.skydeps").join(slug).join("src/Ext/Greet.sky").is_file(),
        "generated frontend must carry the .skydeps source tree"
    );
    // And the same for the backend.
    let back_toml = std::fs::read_to_string(out.join("backend/sky.toml")).unwrap();
    assert!(
        back_toml.contains("github.com/test/sky-greet"),
        "generated backend manifest must carry [dependencies] too"
    );

    // The real proof: the generated frontend BUILDS (resolves the import).
    if required(Need::Go, have_go()) {
        let build = Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(out.join("frontend"))
            .status()
            .expect("run sky build (frontend)");
        assert!(build.success(), "generated frontend must build with the external import resolved");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// A REAL one-project app: the todos app (Model `{ todos : List Todo, draft }`,
/// Msg `DraftChanged String | Add | Toggle Int | Remove Int`, user-defined
/// `todoCodec`/`todoListCodec`). Exercises the generalised generator:
///   * **Msg-arg Req fields** — `Toggle Int` / `Remove Int` put a typed `id :
///     Int` into the request; the backend reconstructs `update (Toggle p.id) m`;
///     the frontend sends `{ id = id }`.
///   * **Non-primitive field codecs** — `todos : List Todo` wires to the user's
///     `todoListCodec`, which (with `Todo` + `todoCodec`) is COPIED into Shared.
#[test]
fn generalises_to_a_real_app_with_msg_args_and_nonprimitive_codecs() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            todos_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on the todos app");

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Msg-arg Req fields: `Toggle Int` → `ToggleReq { id : Int }` + codec ---
    assert!(
        shared.contains("type alias ToggleReq") && shared.contains("id : Int"),
        "ToggleReq must carry the typed Msg arg `id : Int`:\n{shared}"
    );
    assert!(
        shared.contains("toggleReqCodec")
            && shared.contains("Codec.field \"id\" .id Codec.int"),
        "toggleReqCodec must encode the Msg arg with a real codec"
    );
    // Backend RECONSTRUCTS the Msg with the wire arg, not a bare ctor.
    assert!(
        back.contains("update (Toggle p.id) m"),
        "backend must reconstruct `update (Toggle p.id) m`:\n{back}"
    );
    // Frontend SENDS the Msg arg.
    assert!(
        front.contains("Spa.postJson toggleReqCodec toggleRespCodec \"/_rpc/Toggle\" { id = id } AppliedToggle"),
        "frontend must send the Msg arg to the RPC:\n{front}"
    );

    // --- Non-primitive field codecs: `todos : List Todo` → user codec, copied ---
    assert!(
        shared.contains("todos : List Todo"),
        "the response field keeps its surface type `List Todo` (not `List any`):\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"todos\" .todos todoListCodec"),
        "the todos field must wire to the user's `todoListCodec`, not a placeholder"
    );
    // The user's type + codecs are COPIED into Shared (so both projects share one).
    assert!(
        shared.contains("type alias Todo =") && shared.contains("todoCodec =") && shared.contains("todoListCodec ="),
        "Shared must copy the user's Todo type + todoCodec + todoListCodec"
    );
    // …and therefore NOT be re-declared in either project's Main (duplicate def).
    assert!(
        !back.contains("type alias Todo ="),
        "backend Main must NOT re-declare Todo (it comes from Shared)"
    );
    assert!(
        !front.contains("todoListCodec ="),
        "frontend Main must NOT re-declare todoListCodec (it comes from Shared)"
    );

    // --- SECURITY: no server effect / tainted helper in the client ---
    for needle in ["File.", "loadTodos", "saveTodos", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
    }

    // --- Both build (Go-gated). Backend native, frontend wasm. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "todos backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "todos frontend must build to wasm");
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// A MULTI-MODULE app (docs/skyspa/auto-split.md §17): the todos app split
/// across `Main` (Model/Msg/TEA loop) + a PURE `Domain` (Todo type + codecs) +
/// an EFFECTFUL `Store` (File load/save). The generator must:
///   * copy the pure `Domain` module into BOTH trees (frontend + backend);
///   * route the server-tainted `Store` module to the BACKEND ONLY, and NEVER
///     emit it — or an import of it — into the wasm frontend (the security spine);
///   * have `Shared` reference the sibling codec (`todoListCodec`) by IMPORTING
///     `Domain` rather than re-copying it;
///   * still wire the RPCs (Msg-arg Req fields, non-primitive codecs) as it does
///     for a single-module app.
#[test]
fn splits_a_multi_module_app_routing_pure_and_effectful_modules() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            multimodule_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on a multi-module app (no longer refused)"
    );

    // --- Module routing: pure `Domain` → both trees, effectful `Store` → backend only. ---
    assert!(
        out.join("backend/src/Domain.sky").is_file() && out.join("frontend/src/Domain.sky").is_file(),
        "the PURE Domain module must be copied into BOTH trees"
    );
    assert!(
        out.join("backend/src/Store.sky").is_file(),
        "the effectful Store module must be present in the backend"
    );
    assert!(
        !out.join("frontend/src/Store.sky").exists(),
        "SECURITY LEAK: the server-tainted Store module must NOT be in the frontend"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Shared IMPORTS the sibling codec's module (Domain), does NOT re-copy it. ---
    assert!(
        shared.contains("import Domain"),
        "Shared must import the sibling Domain module for the codec/type:\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"todos\" .todos todoListCodec"),
        "the todos field must wire to the sibling `todoListCodec`:\n{shared}"
    );
    assert!(
        !shared.contains("type alias Todo ="),
        "Shared must NOT re-declare Todo — it comes from the imported Domain module"
    );

    // --- Frontend must NOT import the backend-only Store module. ---
    assert!(
        !front.contains("import Store"),
        "SECURITY LEAK: frontend/src/Main.sky imports the backend-only Store module:\n{front}"
    );

    // --- SECURITY: no server effect / tainted helper / effectful module in the client. ---
    for needle in ["File.", "loadTodos", "saveTodos", "Store.", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
        assert!(
            !std::fs::read_to_string(out.join("frontend/src/Domain.sky"))
                .unwrap()
                .contains(needle),
            "SECURITY LEAK: frontend/src/Domain.sky contains `{needle}`"
        );
    }

    // --- The backend keeps the effects + reconstructs the Msg-arg RPCs. ---
    assert!(
        std::fs::read_to_string(out.join("backend/src/Store.sky")).unwrap().contains("File."),
        "backend Store must keep the File effect (it runs it server-side)"
    );
    assert!(
        back.contains("update (Toggle p.id) m"),
        "backend must reconstruct `update (Toggle p.id) m`:\n{back}"
    );
    assert!(
        front.contains("Spa.postJson toggleReqCodec toggleRespCodec \"/_rpc/Toggle\" { id = id } AppliedToggle"),
        "frontend must send the Msg arg to the RPC:\n{front}"
    );

    // --- Both build (Go-gated). Backend native, frontend wasm. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "multi-module backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "multi-module frontend must build to wasm");
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// Server→client PUSH (SSE): the shared-counter fixture uses `Cmd.publish` +
/// `Sub.subscribeTopic`, so the generator must turn on push mode — a shared
/// broker, publish-interpreting RPC handlers, and the `GET /_sky/sub` SSE
/// endpoint — while the frontend keeps `subscriptions` verbatim and leaks no
/// server effect. Both projects must build. (docs/skyspa/auto-split.md §16.)
#[test]
fn wires_server_to_client_push_when_the_app_uses_publish_and_subscribe_topic() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            push_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on the push fixture");

    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();

    // --- Backend push wiring: shared broker + interpret + the SSE endpoint. ---
    assert!(
        back.contains("Ffi.kernel \"Spa_newBroker\"") && back.contains("spaBroker ="),
        "backend must construct the shared broker CAF:\n{back}"
    );
    assert!(
        back.contains("Ffi.kernel \"Spa_interpretPublish\""),
        "backend must wire the Cmd-publish interpreter"
    );
    assert!(
        back.contains("spaInterpretPublish spaBroker cmd"),
        "the RPC handler must feed its returned Cmd to the broker (not discard it):\n{back}"
    );
    assert!(
        back.contains("Server.api \"GET /_sky/sub\" subHandler")
            && back.contains("Ffi.kernel \"Spa_streamTopic\""),
        "backend must mount the SSE push endpoint:\n{back}"
    );

    // --- Frontend keeps the subscription verbatim; no server effect leaks. ---
    assert!(
        front.contains("Sub.subscribeTopic \"count\" GotCount"),
        "frontend must keep `subscriptions` (the EventSource client wires it):\n{front}"
    );
    for needle in ["File.", "saveCount", "Db.", "System.", "Cmd.publish"] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
    }
    // The server branch still routes through the RPC boundary.
    assert!(
        front.contains("Spa.postJson") && front.contains("/_rpc/Increment"),
        "frontend must call the RPC boundary for the Increment server branch"
    );

    // --- Both build (Go-gated). Backend native, frontend wasm. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "push backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "push frontend must build to wasm");
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// Regression for issue #195. A branch whose ONLY server contact is an EXPLICIT
/// `Spa.postJson` / `Spa.getJson` (over a user-provided codec) is a CLIENT branch
/// — the explicit RPC IS the boundary, not a server effect to lift into a
/// synthesized whole-model RPC. Before the fix, `spa_partition` followed
/// `Std.Spa.postJson` into its internal `Http.*` and marked `AddItem` SERVER;
/// because `AddItem` returns the whole model opaquely (`setUi (…) model`), the
/// synthesized RPC request carried EVERY Model field, including the record-alias
/// field `data : Data` — which no codec could wire, so `sky spa-split` failed with
/// `branch \`AddItem\`, field \`data\`: no codec for a field of type \`any\``.
///
/// The fix classifies `Std.Spa`'s client-boundary helpers as CLIENT (pure leaves
/// in the taint graph), so every branch stays client, no `<Msg>Req`/`<Msg>Resp`
/// is synthesized, and the `Spa.postJson` call is copied verbatim into the wasm
/// frontend. The generated static-only backend still builds (it copies none of
/// the app's TEA decls, which reference the client-only `Std.Spa` framework).
#[test]
fn explicit_spa_rpc_branch_stays_client_not_a_synthesized_model_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            explicit_rpc_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        output.status.success(),
        "sky spa-split must SUCCEED on an explicit-RPC client (issue #195), got:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );
    // The exact #195 symptom must be gone.
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        !combined.contains("no codec for a field of type"),
        "the spurious whole-model codec error must not appear:\n{combined}"
    );
    // The split report classifies AddItem CLIENT (local), with NO server branches.
    assert!(
        combined.contains("server branches (→ RPC): (none)"),
        "an explicit-RPC client must have NO server branches:\n{combined}"
    );

    // The frontend keeps the explicit `Spa.postJson` with the user's own codec —
    // it is NOT rewritten into a synthesized `Spa.postJson … "/_rpc/AddItem" …`.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Spa.postJson newItemCodec itemListCodec \"/api/items\""),
        "frontend must keep the author's explicit Spa.postJson verbatim"
    );
    assert!(
        !front.contains("/_rpc/AddItem"),
        "AddItem must NOT be rewritten into a synthesized RPC"
    );

    // Nothing anywhere synthesized an `AddItemReq` / `AddItemResp` wire record.
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    for (name, text) in [("shared", &shared), ("backend", &back), ("frontend", &front)] {
        assert!(
            !text.contains("AddItemReq") && !text.contains("AddItemResp"),
            "no synthesized AddItemReq/AddItemResp wire record should exist in {name}"
        );
        assert!(
            !text.contains("/_rpc/"),
            "no /_rpc endpoint should be generated for a client-only app ({name})"
        );
    }

    // The real proof: both trees BUILD (backend native, frontend wasm). Gated on
    // the Go toolchain, exactly like the other build-leg tests here.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "explicit-RPC static-only backend must build"
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "explicit-RPC frontend must build to wasm");
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// BUG-1 (headline). `sky build --target web:app` on a Std.App `App.app`
/// (Std.Ui `Element`-view) app that configures itself through `App.withConfig
/// (App.WebConfig { App.webDefaults | port = … })` MUST succeed end-to-end.
///
/// The App→Spa synthesis chooses whether to wrap the user's `view` in
/// `Ui.layout []` (Element → Html) by detecting the view family. The old guard
/// was `src.contains("App.web")`, which ALSO matched the `App.webDefaults`
/// opts helper that a NORMAL `App.app` web app uses — so the app was
/// misclassified as an `App.web` (Std.Html) app, the wrap was skipped, and the
/// synthesised `Spa.config` failed to type-check with `Html Msg vs Element Msg`.
/// The fix detects the `App.web` BUILDER at a call boundary (not the
/// `App.webDefaults`/`App.webConfig` idents, and not inside `--` comments).
#[test]
fn web_app_target_wraps_ui_element_view_despite_webdefaults() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&web_config_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The exact pre-fix symptom must be gone — regardless of the Go toolchain,
    // because it is a type error the synthesis produced BEFORE any `go build`.
    assert!(
        !log.contains("Html Msg` vs `Element Msg") && !log.contains("Html Msg vs Element Msg"),
        "BUG-1: the Html/Element mismatch must be gone (App.webDefaults must not be read as App.web):\n{log}"
    );
    // The synthesised entry must wrap the Element view in `Ui.layout []` (not
    // pass it through as an already-Html view). This is the direct proof and
    // needs no Go toolchain.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("Ui.layout [] (view model_)"),
        "BUG-1: the Element view must be wrapped in `Ui.layout []`:\n{synth}"
    );
    // Synthesis + the split's type-check passed (this line prints only after
    // `generate` type-checks clean).
    assert!(
        log.contains("client/server split"),
        "the split must run (synthesis + type-check passed):\n{log}"
    );

    // Full end-to-end proof (Go-gated): the whole `--target web:app` build
    // produces the native backend + the wasm frontend.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "BUG-1: --target web:app must build end-to-end:\n{log}");
    assert!(
        proj.join(".skyapp/web-app/.split/backend/sky-out/app").is_file(),
        "backend binary must be built:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// BUG-2. A `|> App.withX` builder step the synthesis does NOT carry into the
/// derived Spa entry (here `withHead`, an SEO hook) must be reported by name,
/// never dropped silently. Runs during synthesis, so no Go toolchain is needed.
#[test]
fn web_app_synthesis_warns_about_dropped_builder_steps() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)
import Std.Html as Html exposing (Html)


type alias Model =
    { count : Int }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Element Msg
view _ =
    Ui.text "hi"


pageHead : Model -> List (Html Msg)
pageHead _ =
    [ Html.node "title" [] [ Html.text "My App" ] ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()
        |> App.withHead pageHead


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("withheaddrop", main_sky);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert!(
        log.contains("NOT carried") || log.contains("not carried"),
        "BUG-2: a dropped builder step must be reported, not dropped silently:\n{log}"
    );
    // The dropped LIST (the segment after `client entry: `) must name withHead
    // — and only withHead. `withNotFound` IS carried, so although it appears in
    // the warning's explanatory prose, it must NOT be in the dropped list.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    assert!(
        dropped_list.contains("withHead"),
        "BUG-2: `App.withHead` must be named in the dropped list, got `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withNotFound"),
        "withNotFound is carried into the Spa entry and must not be in the dropped list `{dropped_list}`"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// BUG-3. When the SYNTHESISED client entry fails to type-check, the failure
/// must surface the actual diagnostic (file:line + caret), plus a pointer to the
/// staged entry — not a bare `1 type error(s)` count that discards where the
/// error is. Type-checking happens before any `go build`, so no Go is needed.
#[test]
fn web_app_type_error_reports_file_line_not_just_a_count() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    // `view` returns an `Int`, so the synthesised `Ui.layout [] (view model_)`
    // cannot type-check — a deterministic single type error in the derived entry.
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui


type alias Model =
    { count : Int }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Int
view model =
    model.count


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("brokenentry", main_sky);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert!(!output.status.success(), "a broken synthesised entry must fail the build");
    // The rendered diagnostic — an Elm-style TYPE ERROR block with a file:line
    // header — must be present (BUG-3: the count alone used to be all we got).
    assert!(
        log.contains("TYPE ERROR") && log.contains("[E2"),
        "BUG-3: the actual type diagnostic (file:line + code) must be shown, not just a count:\n{log}"
    );
    // …and the user must be told WHERE the synthesised entry is, so the
    // file:line resolves to a real path they can open.
    assert!(
        log.contains(".skyapp/web-app") && log.contains("sky check"),
        "BUG-3: the staged synthesised-entry path + a `sky check` hint must be printed:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}
