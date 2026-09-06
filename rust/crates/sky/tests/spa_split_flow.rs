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
/// derived Spa entry must be reported by name, never dropped silently. Runs
/// during synthesis, so no Go toolchain is needed.
///
/// SSR-P0 update: `withHead` is now CARRIED (it becomes `|> Spa.withHead` in the
/// synthesised entry — the SSR per-route `<head>` channel), so it must NOT be in
/// the dropped list any more. A genuinely server-only builder (`withGuard`) is
/// added here to keep the never-drop-silently invariant under test.
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
        |> App.withGuard (\_ _ -> Ok ())


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
    // The dropped LIST (the segment after `client entry: `) must name the
    // server-only `withGuard`. `withRoutes`/`withNotFound`/`withHead` are all
    // CARRIED, so although they may appear in the warning's explanatory prose,
    // they must NOT be in the dropped list.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    assert!(
        dropped_list.contains("withGuard"),
        "BUG-2: `App.withGuard` (server-only) must be named in the dropped list, got `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withHead"),
        "SSR-P0: `App.withHead` is now carried into the Spa entry and must NOT be in the dropped list `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withNotFound"),
        "withNotFound is carried into the Spa entry and must not be in the dropped list `{dropped_list}`"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// SSR-P0. `App.withHead` is CARRIED through the App→Spa synthesis into a
/// `|> Spa.withHead` step (the SSR per-route `<head>` channel, design §4.3 /
/// §7-P0). The argument may be a `sky fmt`-wrapped MULTI-LINE lambda; the
/// line-based `extract_app_fields` must gather the whole lambda by bracket
/// balancing, not truncate it to its first physical line (§7-P0(c)). This proves
/// the multi-line capture + the carry, and that the drop-warning no longer names
/// `withHead`. Synthesis-only assertions need no Go toolchain.
#[test]
fn web_app_carries_multiline_withhead_into_spa_entry() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)
import Std.Live.Head as Head


type alias Model =
    { title : String }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { title = "Home" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Element Msg
view _ =
    Ui.text "hi"


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
        |> App.withHead
            (\m ->
                [ Head.title ("SSR-HEAD-MARKER: " ++ m.title)
                , Head.meta "description" "a spa ssr page"
                ]
            )


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("multilinehead", main_sky);

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

    // The synthesised entry must carry the head as a `Spa.withHead` builder step
    // AND contain the FULL multi-line lambda body — not a first-line truncation.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("Spa.withHead"),
        "SSR-P0: the synthesised entry must carry `withHead` as `Spa.withHead`:\n{synth}"
    );
    assert!(
        synth.contains("SSR-HEAD-MARKER") && synth.contains("Head.meta") && synth.contains("description"),
        "SSR-P0: the FULL multi-line withHead lambda must be captured (all lines), not truncated:\n{synth}"
    );

    // The drop-warning, if any fired, must NOT name withHead (it is carried).
    if let Some(after) = log.split("client entry: ").nth(1) {
        let dropped_list = after.split('.').next().unwrap_or("");
        assert!(
            !dropped_list.contains("withHead"),
            "SSR-P0: withHead is carried and must not appear in the dropped list `{dropped_list}`:\n{log}"
        );
    }

    // Synthesis + the split's type-check passed (only prints after `generate`
    // type-checks the derived entry clean — so `Spa.withHead pageHead` is well
    // typed against the new `Std.Spa.withHead` signature).
    assert!(
        log.contains("client/server split"),
        "SSR-P0: the split must run (synthesis + type-check of the head-carrying entry passed):\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_app_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-app")
}

/// SSR-P1. A `Std.App` app with ≥1 SERVER branch (a `File` effect) auto-splits
/// into a backend that carries `view`/`init`, and `gen_backend` must emit an SSR
/// `GET /{$}` route that server-renders the root's first paint (design §4.1) —
/// the SEO enabler that replaces the empty `#app` static shell with real,
/// crawlable HTML + a per-route `<head>`.
///
/// Two layers of proof:
///  - synthesis: `App.withHead` is carried into a NAMED `spaHead_` binding (so it
///    reaches the backend, where the SSR route calls it — an inline lambda would
///    live only in the dropped `main`);
///  - emission: the backend source carries the `ssrHandler` + the `GET /{$}`
///    route (registered AHEAD of `Server.static`) that renders
///    `spaSsrRenderBody (spaView_ …)` with the `spaHead_` head, and the whole
///    split type-checks (`generate` type-checks the emitted backend — a malformed
///    SSR route would fail HERE, before any Go).
/// The Go-gated leg proves the whole thing builds end-to-end. The SERVED HTML
/// (body inside a `data-sky-ssr` `#app` + the `<head>`) is asserted by the Go
/// runtime tests (spa_ssr_notjs_test.go / spa_ssr_test.go); the browser hydration
/// leg is manual (a wasm/browser attach cannot run here).
#[test]
fn spa_ssr_app_emits_a_server_render_route_for_the_root() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_app_fixture_dir(), &proj);

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

    // Synthesis carried `withHead` into a NAMED binding referenced by the config.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("spaHead_ model_") && synth.contains("|> Spa.withHead spaHead_"),
        "SSR-P1: withHead must be a NAMED `spaHead_` binding (reaches the backend), \
         referenced by the config:\n{synth}"
    );

    // The split ran → the emitted backend (incl. the SSR route) TYPE-CHECKED.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "SSR-P1: the split must run (the emitted backend SSR route type-checked):\n{log}"
    );

    // The backend carries the SSR route, ahead of the static route.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // P3 renamed the handler's rendered model to `resolved` (the per-route,
    // optionally data-settled model). This fixture is route-less with a
    // `Cmd.none` init, so `resolved` folds to the pure init model (chrome-only) —
    // the SSR route + render kernels are still exactly what P1 asserted.
    for needle in [
        "ssrHandler",
        "Server.api \"GET /{$}\" ssrHandler",
        "spaSsrPage",
        "spaSsrRenderBody (spaView_ resolved)",
        "spaSsrRenderHead spaHead_ resolved",
        "spaSsrWasmName \"../frontend/dist\"",
    ] {
        assert!(
            backend.contains(needle),
            "SSR-P1: the generated backend must carry the SSR route piece `{needle}`:\n{backend}"
        );
    }
    // The SSR route must be registered BEFORE the static fallthrough so asset
    // GETs (main.<hash>.wasm, wasm_exec.js) still reach the file server.
    let ssr_at = backend.find("Server.api \"GET /{$}\" ssrHandler");
    let static_at = backend.find("Server.static \"/\"");
    assert!(
        ssr_at.is_some() && static_at.is_some() && ssr_at < static_at,
        "SSR-P1: the SSR route must precede the static route:\n{backend}"
    );
    // P3 fail-closed: this fixture's `init` is `Cmd.none` (its `File.writeFile`
    // lives in a `Persist` branch, NOT in `init`), so the GET-safe allowlist scan
    // finds no safe read → NO data-resolve settle is emitted, and the route
    // renders the pure model. `Spa_ssrSettle` must be absent.
    assert!(
        !backend.contains("spaSsrSettle") && backend.contains("resolved =\n            routed"),
        "SSR-P3 fail-closed: a `Cmd.none` init must NOT get a data-resolve settle:\n{backend}"
    );

    // Full end-to-end proof (Go-gated): the whole thing builds — a broken kernel
    // reference (`Spa_ssr*`) would fail the backend `go build` here.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "SSR-P1: --target web:app must build end-to-end:\n{log}");
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

fn ssr_p3_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-p3")
}

/// Wait until the generated Sky.Spa backend logs its `Sky server listening` line
/// (the auto-split backend is a `Sky.Http.Server`, not a Sky.Live one, so its
/// ready line differs from `wait_for_listening`'s). Returns true once seen.
fn wait_for_spa_backend(log_path: &std::path::Path, tries: u32) -> bool {
    use std::io::Read as _;
    for _ in 0..tries {
        if let Ok(mut f) = std::fs::File::open(log_path) {
            let mut buf = String::new();
            if f.read_to_string(&mut buf).is_ok() && buf.contains("Sky server listening") {
                return true;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

/// SSR-P3 (design §4.1 + §4.2). A ROUTED `Std.App` app whose `init` reads real
/// data via a curated GET-safe kernel (`File.readFile`) must:
///
///   * synthesise NAMED `spaRoutes_` / `spaNotFound_` bindings (so the route
///     table reaches the backend, mirroring `spaHead_`);
///   * per-route emit `GET /{$}` + `GET /items` (each a more-specific mux entry
///     than `Server.static "/"`, so asset GETs still fall through) all pointing
///     at an `ssrHandler` that resolves the request path (Spa_ssrResolveModel);
///   * data-resolve: because `init`'s command is GET-safe, emit `Spa_ssrSettle`
///     and render `resolved` (not the pure init model);
///   * (Go-gated e2e) serve REAL per-route content: `GET /items` carries the
///     resolved item list a crawler sees; `GET /` carries the Home content and
///     NOT the items — proving per-route + data-resolved SSR end to end.
#[test]
fn spa_ssr_p3_resolves_real_per_route_data_for_a_get_safe_init() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_p3_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR-P3 fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: named route + notFound bindings reach the backend.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("spaRoutes_ =") && synth.contains("|> Spa.withRoutes spaRoutes_"),
        "SSR-P3: routes must be a NAMED `spaRoutes_` binding referenced by the config:\n{synth}"
    );
    assert!(
        synth.contains("spaNotFound_ =") && synth.contains("|> Spa.withNotFound spaNotFound_"),
        "SSR-P3: notFound must be a NAMED `spaNotFound_` binding:\n{synth}"
    );

    // The split ran → the emitted backend (per-route + settle) TYPE-CHECKED.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "SSR-P3: the split must run (the emitted backend type-checked):\n{log}"
    );

    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    for needle in [
        // per-route resolver + registrations
        "spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path",
        "Server.api \"GET /{$}\" ssrHandler",
        "Server.api \"GET /items\" ssrHandler",
        // data-resolved settle (init IS get-safe)
        "spaSsrSettle routed cmd0 update",
        "resolved =\n            spaSsrSettle routed cmd0 update",
    ] {
        assert!(
            backend.contains(needle),
            "SSR-P3: the generated backend must carry `{needle}`:\n{backend}"
        );
    }
    // Per-route SSR routes must precede the static fallthrough.
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.static \"/\"");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "SSR-P3: per-route SSR routes must precede the static route:\n{backend}"
    );

    // ── Go-gated e2e: run the backend, curl each route, assert REAL per-route
    // content. The backend reads `data/items.json` relative to its cwd, so run it
    // from the backend dir with the data file staged there. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "SSR-P3: --target web:app must build end-to-end:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the data the settle reads (init: File.readFile "data/items.json").
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::copy(proj.join("data/items.json"), backend_dir.join("data/items.json")).unwrap();

    let port = 8973u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled SSR-P3 backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&proj);
        panic!("SSR-P3 backend never reported listening on :{port}\nlog:\n{buf}");
    }
    let items_body = curl_body_p(port, "/items");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let items_body = items_body.expect("GET /items should return a body");
    let home_body = home_body.expect("GET / should return a body");
    // /items carries the RESOLVED, crawlable data + the SSR marker.
    assert!(
        items_body.contains("data-sky-ssr")
            && items_body.contains("Item list:")
            && items_body.contains("Alpha Widget")
            && items_body.contains("Beta Gadget")
            && items_body.contains("Gamma Gizmo"),
        "SSR-P3: GET /items must carry the SERVER-RESOLVED item list (crawlable), \
         not a loading state. Body was:\n{items_body}"
    );
    // The embedded #sky-model blob (design §4.5) carries the RESOLVED model as
    // JSON — the route's page + the settled items — so the client can boot from
    // it. It must be present and decode to the resolved data.
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("SSR-P3: the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"Items""#)
            && blob.contains("Alpha Widget")
            && blob.contains("Gamma Gizmo"),
        "SSR-P3: the #sky-model blob must decode to the RESOLVED model \
         (page=Items + the settled items). Blob was:\n{blob}"
    );
    // / renders its OWN (Home) VIEW — "Welcome home", and NOT the Items view's
    // "Item list:" header — proving per-route BODY rendering. (The embedded model
    // blob DOES carry the settled items for every route, which is correct: the
    // data is resolved once and the Home *view* simply does not display it.) So
    // assert on the rendered body region, not the whole document.
    let home_app = {
        let s = home_body.find(r#"<div id="app""#).expect("home #app must exist");
        let e = home_body.find(r#"<script id="sky-model""#).unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Welcome home") && !home_app.contains("Item list:"),
        "SSR-P3: GET / must render Home's own view, not the Items view:\n{home_app}"
    );
}

/// SSR-P3 fail-closed (design §4.2). An `init` whose command is NOT a curated
/// GET-safe read — here `Time.now` (non-deterministic) — must NOT get a
/// data-resolve settle: the allowlist scan errs toward chrome-only. Per-route
/// resolution still works; only the settle is withheld, so the route renders the
/// pure init model (exactly P1). This is the "a GET must never run a
/// non-deterministic effect / mutate" boundary.
#[test]
fn spa_ssr_p3_fail_closed_when_init_is_not_get_safe() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Time as Time
import Std.App as App
import Std.Ui as Ui exposing (Element)


type Page
    = Home
    | Stamp


type alias Model =
    { page : Page, stamp : Int }


type Msg
    = Load
    | Got (Result Error Int)


init : () -> ( Model, Cmd Msg )
init () =
    ( { page = Home, stamp = 0 }, Cmd.perform (Time.now ()) Got )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Load ->
            ( model, Cmd.perform (Time.now ()) Got )

        Got (Ok t) ->
            ( { model | stamp = t }, Cmd.none )

        Got (Err _) ->
            ( { model | stamp = 0 }, Cmd.none )


view : Model -> Element Msg
view model =
    case model.page of
        Home ->
            Ui.column [] [ Ui.text "home" ]

        Stamp ->
            Ui.column [] [ Ui.text ("stamp " ++ String.fromInt model.stamp) ]


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
        |> App.withRoutes [ App.route "/" Home, App.route "/stamp" Stamp ]
        |> App.withNotFound Home


main =
    App.run app
"#;
    let proj = scratch_std_app("ssr-failclosed", main_sky);
    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the fail-closed fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated backend must exist:\n{log}"));

    // Per-route resolution STILL happens (routes are unaffected by the allowlist).
    assert!(
        backend.contains("spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path")
            && backend.contains("Server.api \"GET /stamp\" ssrHandler"),
        "SSR-P3 fail-closed: per-route SSR must still be emitted:\n{backend}"
    );
    // …but NO data-resolve settle for a Time.now init (fail-closed → chrome-only).
    assert!(
        !backend.contains("spaSsrSettle") && backend.contains("resolved =\n            routed"),
        "SSR-P3 fail-closed: a non-deterministic (Time.now) init must NOT get a \
         data-resolve settle — it must render the pure init model:\n{backend}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_db_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-db")
}

fn have_sqlite3() -> bool {
    Command::new("sqlite3")
        .arg("--version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// SSR CLIENT-LEG (design §4.4/§4.5, blocker #3). An `init` that reads through a
/// **`db` CAF** — the sky-lang.org shape. The `db` binding reaches `Db.open`, so
/// the split routes it to the BACKEND ONLY; kept verbatim, the client `init`'s
/// `Cmd.perform (Db.query db …)` would leave `Undefined name: db` in the wasm
/// frontend. The client-leg fix STRIPS `init`'s command to `Cmd.none` in the
/// frontend (the server settles the read + embeds `#sky-model`; the client boots
/// from that blob), so the client tree compiles WITHOUT the `db` CAF while the
/// backend still SSRs + embeds the resolved rows.
#[test]
fn spa_ssr_db_client_leg_excludes_the_db_caf() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_db_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR db-init fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The crux: the FRONTEND (wasm client) source compiles WITHOUT the `db`
    // CAF. `init`'s command is stripped to `Cmd.none`; no `db`/`Db.*` reference
    // survives into the client tree. This assertion holds without a Go toolchain
    // (the frontend `.sky` is generated before any `go build`). ──
    let frontend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated frontend entry must exist:\n{log}"));
    // Scan CODE only — the generated frontend carries the module doc comment,
    // which legitimately mentions `db` / `Db.query`. Strip `--` line comments so
    // the assertions test references in code, not prose.
    let frontend_code = strip_line_comments(&frontend);
    assert!(
        !references_word_test(&frontend_code, "db"),
        "SSR client-leg: the frontend tree must NOT reference the `db` CAF:\n{frontend}"
    );
    for needle in ["Db.query", "Db.open", "Std.Db"] {
        assert!(
            !frontend_code.contains(needle),
            "SSR client-leg: the frontend tree must NOT reference `{needle}`:\n{frontend}"
        );
    }
    // The strip landed: init returns `Cmd.none`, and the pure model is preserved.
    assert!(
        frontend_code.contains("init () =") && frontend_code.contains("Cmd.none"),
        "SSR client-leg: the frontend `init` must be stripped to `Cmd.none`:\n{frontend}"
    );

    // ── The BACKEND still resolves + settles the read + embeds the model. ──
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated backend entry must exist:\n{log}"));
    for needle in [
        "spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path",
        "spaSsrSettle routed cmd0 update",
        "Codec.toJson (Codec.auto resolved) resolved",
    ] {
        assert!(
            backend.contains(needle),
            "SSR client-leg: the backend must carry `{needle}`:\n{backend}"
        );
    }
    // The `db` CAF DOES survive into the backend (it is server-owned).
    assert!(
        backend.contains("db =") && backend.contains("Db.open"),
        "SSR client-leg: the `db` CAF must remain in the BACKEND tree:\n{backend}"
    );

    // ── Go-gated e2e: the whole thing builds, and (with sqlite3 to seed the DB)
    // the embedded `#sky-model` carries the SERVER-RESOLVED rows a crawler sees. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "SSR client-leg: --target web:app must build end-to-end:\n{log}");
    // The wasm frontend actually links with no `db`/`Db_*` symbol.
    let fe_go = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"),
    )
    .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query") && !fe_go.contains("Db_open"),
            "SSR client-leg: the emitted wasm frontend Go must contain no Db_* kernel"
        );
    }

    if !required(Need::Sqlite3, have_sqlite3()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let db_path = backend_dir.join("app.db");
    let seed = Command::new("sqlite3")
        .arg(&db_path)
        .arg("CREATE TABLE items(name TEXT); INSERT INTO items(name) VALUES('Alpha Widget'),('Beta Gadget'),('Gamma Gizmo');")
        .status()
        .expect("seed sqlite db");
    assert!(seed.success(), "seeding the sqlite db must succeed");

    let port = 8977u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(backend_dir.join("sky-out/app"))
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("SSR_DB_PATH", "app.db")
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled SSR db backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("SSR db backend never reported listening on :{port}");
    }
    let items_body = curl_body_p(port, "/items");
    let _ = child.kill();
    let _ = child.wait();

    let items_body = items_body.expect("GET /items should return a body");
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"ItemsPage""#)
            && blob.contains("Alpha Widget")
            && blob.contains("Gamma Gizmo"),
        "SSR client-leg: the #sky-model blob must decode to the SERVER-RESOLVED \
         rows (from the `db` read the client never runs). Blob was:\n{blob}"
    );
    assert!(
        items_body.contains("data-sky-ssr") && items_body.contains("Item list:"),
        "SSR client-leg: GET /items must carry the server-rendered, crawlable body:\n{items_body}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// Drop `--` line comments (the generated frontend carries the module doc
/// comment, which mentions `db`/`Db.*` in prose). No `--` appears inside a string
/// literal in the generated frontend's decls, so a per-line cut at the first `--`
/// is sufficient to isolate code.
fn strip_line_comments(src: &str) -> String {
    src.lines()
        .map(|l| match l.find("--") {
            Some(i) => &l[..i],
            None => l,
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Whole-word membership test mirroring `spa_split::references_word` (that helper
/// is crate-private). Used only to assert the frontend does not reference `db`.
fn references_word_test(hay: &str, needle: &str) -> bool {
    let is_ident = |b: u8| b.is_ascii_alphanumeric() || b == b'_';
    let bytes = hay.as_bytes();
    let mut from = 0;
    while let Some(rel) = hay[from..].find(needle) {
        let i = from + rel;
        let before_ok = i == 0 || !is_ident(bytes[i - 1]);
        let after = i + needle.len();
        let after_ok = after >= bytes.len() || !is_ident(bytes[after]);
        if before_ok && after_ok {
            return true;
        }
        from = i + needle.len();
    }
    false
}

// Full response BODY of `GET http://127.0.0.1:<port><path>` (P3 e2e helper).
fn curl_body_p(port: u16, path: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl").args(["-s", &url]).output().ok()?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
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
