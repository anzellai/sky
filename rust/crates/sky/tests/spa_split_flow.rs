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

fn mixed_codec_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-mixed-codec/src/Main.sky")
}

fn error_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-error-wire/src/Main.sky")
}

fn msg_with_wire_types_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-msg-with-wire-types/src/Main.sky")
}

fn union_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-union-wire/src/Main.sky")
}

fn auto_record_codec_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-auto-record-codec/src/Main.sky")
}

fn bare_adt_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-bare-adt-wire/src/Main.sky")
}

fn ssr_multimodule_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-multimodule")
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

fn server_chain_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-server-chain/src/Main.sky")
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

// The `Msg` union, the wire record `Item` and the `Model` all live in ONE module
// (`Types`), and sibling modules read `Types` via `exposing (..)`. Injecting the
// `Applied<Msg>` RPC variants into `Types` makes it import `Shared`, so `Shared`
// must NOT import `Types` back. The split resolves the would-be cycle (E1010) by
// giving `Shared` its OWN copy of `Item` + `itemCodec`; because a record
// `type alias` is structural, the model field (`Types.Item`) and the RPC response
// field (`Shared.Item`) unify, so the round-trip is sound. Regression for the
// deleted "Move `Msg` into its own module" refusal.
#[test]
fn splits_a_module_that_co_locates_msg_with_its_wire_types() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            msg_with_wire_types_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split MUST succeed when `Msg` co-locates with its wire types (the E1010 refusal is deleted)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_types = std::fs::read_to_string(out.join("frontend/src/Types.sky")).unwrap();
    let back_types = std::fs::read_to_string(out.join("backend/src/Types.sky")).unwrap();
    let front_main = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back_main = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared OWNS the wire type + codec, and never imports the Msg module. ---
    assert!(
        shared.contains("type alias Item =") && shared.contains("itemCodec"),
        "Shared must OWN a copy of the co-located `Item` + `itemCodec`:\n{shared}"
    );
    assert!(
        !shared.contains("import Types"),
        "NO CYCLE: Shared must NOT import the Msg module `Types` (Types imports Shared):\n{shared}"
    );

    // --- The Msg module imports Shared for the injected `Applied<Msg>` variants,
    //     and still resolves `Item` for its `exposing (..)` consumers. ---
    assert!(
        front_types.contains("import Shared") && front_types.contains("AppliedSave"),
        "frontend Types must import Shared and carry the injected Applied<Msg> variant:\n{front_types}"
    );
    assert!(
        front_types.contains("type alias Item ="),
        "frontend Types must keep `Item` so its `exposing (..)` consumers still resolve it:\n{front_types}"
    );
    // Neither tree may pull the wire type from BOTH Types and Shared unqualified
    // (that would be an ambiguous double-import). The entry imports Shared with an
    // explicit exposing list that excludes the copied names it already reads from
    // `Types exposing (..)`.
    assert!(
        !front_main.contains("import Shared exposing (..)"),
        "frontend Main must import Shared with an explicit list (not `..`) to avoid an ambiguous `Item`:\n{front_main}"
    );
    assert!(
        !back_main.contains("import Shared exposing (..)"),
        "backend Main must import Shared with an explicit list (not `..`) to avoid an ambiguous `Item`:\n{back_main}"
    );

    // --- SECURITY: the File effect never reaches the frontend. ---
    for needle in ["File.", "loadItems", "saveItems"] {
        assert!(
            !front_main.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front_main}"
        );
    }

    // backend Types stays a normal module (the wire type lives there for the
    // native server too).
    assert!(
        back_types.contains("type alias Item ="),
        "backend Types keeps `Item`:\n{back_types}"
    );

    // --- Both build (Go-gated). This is the type-identity proof: the model field
    //     `items : List Item` (Types.Item) and the RPC response field
    //     `items : List Item` (Shared.Item) must reconcile, or `go build` fails. ---
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
        "backend must build natively (proves Types.Item unifies with Shared.Item on the RPC fold)"
    );
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "frontend must build to wasm (proves the client-side fold unifies too)"
    );
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

// A NOMINAL `union` (`Page`) is declared in the `Msg` module (`Types`) and rides
// the RPC wire as a `Model` field (`page : Page`); a server branch writes it, and
// a view sibling reads `Types exposing (..)` and references it. A union cannot be
// duplicated copy-and-leave (two same-named unions never unify), so `Shared` must
// OWN the single definition: it declares `Page` ONCE, `Types` strips its `Page`
// declaration, and every consumer (the entry, the Msg module, the view sibling)
// imports `Page(..)` from `Shared`. Regression for the deleted "cannot auto-split:
// the wire needs the union type `Page`" refusal — the case must now SUCCEED and
// both trees build (the single-definition identity proof).
#[test]
fn splits_a_module_whose_wire_rides_a_nominal_union_by_owning_it_in_shared() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            union_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split MUST succeed when a wire-riding NOMINAL union lives in the Msg module (the union refusal is deleted; Shared OWNS the union)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_types = std::fs::read_to_string(out.join("frontend/src/Types.sky")).unwrap();
    let back_types = std::fs::read_to_string(out.join("backend/src/Types.sky")).unwrap();
    let front_view = std::fs::read_to_string(out.join("frontend/src/View.sky")).unwrap();
    let back_view = std::fs::read_to_string(out.join("backend/src/View.sky")).unwrap();
    let front_main = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back_main = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared declares `Page` ONCE (the single definition) and never imports
    //     the Msg module `Types`. ---
    assert!(
        shared.contains("type Page") && shared.contains("HomePage") && shared.contains("AccountPage"),
        "Shared must declare the `Page` union (its single definition):\n{shared}"
    );
    assert!(
        !shared.contains("import Types"),
        "NO CYCLE: Shared must NOT import the Msg module `Types`:\n{shared}"
    );

    // --- NO module declares `Page` twice: the Msg module strips its declaration,
    //     and every reference resolves to Shared's copy. ---
    for (label, src) in [
        ("frontend Types", &front_types),
        ("backend Types", &back_types),
        ("frontend View", &front_view),
        ("backend View", &back_view),
        ("frontend Main", &front_main),
        ("backend Main", &back_main),
    ] {
        assert!(
            !src.contains("type Page"),
            "{label} must NOT re-declare `Page` (Shared owns the single definition):\n{src}"
        );
    }

    // --- The Msg module imports `Page(..)` from Shared (its `Model` field + the
    //     `pathOf` helper reference it), and keeps its `Msg` union + `Model`. ---
    for (label, src) in [("frontend Types", &front_types), ("backend Types", &back_types)] {
        assert!(
            src.contains("import Shared exposing (") && src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared:\n{src}"
        );
        assert!(
            src.contains("type alias Model ="),
            "{label} must keep `Model` (a structural record):\n{src}"
        );
    }
    assert!(
        front_types.contains("AppliedDoSignIn"),
        "frontend Types must carry the injected Applied<Msg> variant:\n{front_types}"
    );

    // --- The view sibling imports `Page(..)` from Shared in BOTH trees, because
    //     `Types exposing (..)` no longer surfaces the moved union. ---
    for (label, src) in [("frontend View", &front_view), ("backend View", &back_view)] {
        assert!(
            src.contains("import Shared exposing (") && src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared (its `case`/annotation reference it):\n{src}"
        );
    }

    // --- The entry imports `Page(..)` from Shared (it writes `Page` ctors), with
    //     an explicit exposing list (not `..`). ---
    for (label, src) in [("frontend Main", &front_main), ("backend Main", &back_main)] {
        assert!(
            !src.contains("import Shared exposing (..)"),
            "{label} must import Shared with an explicit list, not `..`:\n{src}"
        );
        assert!(
            src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared:\n{src}"
        );
    }

    // --- SECURITY: the File effect never reaches the frontend. ---
    for needle in ["File.", "writeFile"] {
        assert!(
            !front_main.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front_main}"
        );
    }

    // --- Both build (Go-gated). This is the single-definition identity proof: the
    //     model field `page : Page` (now Shared.Page) and the RPC response field
    //     `page : Page` (Shared.Page) resolve to the SAME type, or `go build`
    //     fails. ---
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
        "backend must build natively (proves every `Page` reference resolves to Shared's single definition)"
    );
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "frontend must build to wasm (proves the client-side fold resolves to Shared.Page too)"
    );
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// A `Codec <T>` binding that lives in a MIXED module (one that ALSO owns a
/// server effect) is itself PURE, and the wire needs it. The generator must
/// COPY that codec + its type into `Shared` — NEVER import the tainted module
/// into `Shared` (that would drag the File effect into the wasm frontend, which
/// `Shared` compiles into). Before this fix the codec registry scanned only the
/// entry + PURE sibling modules, so a `List Item` field whose `itemCodec` lived
/// beside a `File` effect fell through to `no codec for a field of type any`.
#[test]
fn splits_a_mixed_module_codec_by_copying_it_into_shared() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            mixed_codec_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed by COPYING the mixed-module codec into Shared (was: `no codec for a field of type any`)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared COPIES the mixed-module codec + its type (never imports Data). ---
    assert!(
        shared.contains("itemCodec") && shared.contains("type alias Item ="),
        "Shared must COPY the pure `itemCodec` + `Item` type from the mixed module:\n{shared}"
    );
    assert!(
        !shared.contains("import Data"),
        "SECURITY LEAK: Shared must NOT import the server-tainted `Data` module:\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"items\" .items (Codec.list itemCodec)"),
        "the items field must wire to `Codec.list itemCodec`:\n{shared}"
    );

    // --- The server File effect must be routed backend-only; Data stays backend. ---
    assert!(
        out.join("backend/src/Data.sky").is_file(),
        "the mixed Data module must be present in the backend (it runs the File effect)"
    );

    // --- SECURITY: no server effect / tainted helper leaks into the client. ---
    for needle in ["File.", "loadItems", "saveItems", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front}"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`:\n{front_shared}"
        );
    }
    assert!(
        !front.contains("import Data"),
        "SECURITY LEAK: frontend/src/Main.sky imports the backend-only `Data` module:\n{front}"
    );
    assert!(
        !out.join("frontend/src/Data.sky").exists(),
        "SECURITY LEAK: the server-tainted `Data` module must NOT be in the frontend"
    );

    // --- The backend keeps the effect + carries the codec via Shared (no clash). ---
    assert!(
        std::fs::read_to_string(out.join("backend/src/Data.sky")).unwrap().contains("File."),
        "backend Data must keep the File effect (it runs it server-side)"
    );
    assert!(
        back.contains("import Shared exposing (") && back.contains("Item") && back.contains("itemCodec"),
        "backend Main imports Shared for the copied codec/type (explicit exposing list):\n{back}"
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
    assert!(backend_build.success(), "mixed-codec backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "mixed-codec frontend must build to wasm");
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// An `Error` value MAY cross the Sky.Spa RPC wire by DEFAULT — it must
/// serialise, not be refused. A server branch whose payload is
/// `Result Error String` (the `Cmd.perform … Sent` shape, e.g. darraghstudio's
/// `EmailSent (Result Error String)`) must wire through the stdlib
/// `Codec.result` + `Codec.error` with NO hand-written app codec. Before this fix
/// the resolver had no `Result`/`Error` arm and refused with `no codec for a
/// field of type Result Error String`. The two-level-error concern is satisfied
/// off-wire: logging stays a server effect (`Std.Log`) and an app controls
/// handling via `App.withRpcError` — so the wire itself must round-trip.
#[test]
fn wires_a_result_error_payload_through_the_stdlib_error_codec() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            error_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must SUCCEED wiring a `Result Error String` payload (was: `no codec for a field of type Result Error String`)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Shared wires the field via the stdlib Result + Error codecs. ---
    assert!(
        shared.contains("outcome : Result Error String"),
        "the RPC field keeps its `Result Error String` surface type:\n{shared}"
    );
    assert!(
        shared.contains("(Codec.result Codec.error Codec.string)"),
        "the `Result Error String` field must wire to `Codec.result Codec.error Codec.string`:\n{shared}"
    );
    // --- No hand-written codec was needed / copied (the stdlib provides it). ---
    assert!(
        !shared.contains("errorCodec") && !shared.contains("resultCodec"),
        "no app-level Error/Result codec should be copied — Shared references the stdlib `Codec.error`/`Codec.result`:\n{shared}"
    );
    // --- The Secret/Set fail-closed refusal is untouched (no false refusal here). ---
    assert!(
        !shared.contains("Secret") && !shared.contains("Set "),
        "the error-wire fixture carries no Secret/Set — none should appear:\n{shared}"
    );

    // --- SECURITY: no server effect leaks into the client Shared. ---
    for needle in ["File.", "audit.txt", "Db.", "System."] {
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`:\n{front_shared}"
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
    assert!(backend_build.success(), "error-wire backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "error-wire frontend must build to wasm");
    assert!(dist_has_wasm(&out.join("frontend/dist")), "frontend stages a hashed main.<hash>.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// A plain RECORD payload crosses the Sky.Spa wire with NO hand-written codec:
/// the split AUTO-DERIVES one (§14 #2, option B). The fixture reproduces
/// darraghstudio's `OrderFinalized (Result Error CheckoutResult)` — a
/// server-result Msg carries a `Result Error Receipt` where `Receipt` is a plain
/// record with MIXED fields (String, Int, `Maybe`, `List`, and a NESTED record
/// `Address`). Before the fix the record solved to a structural row and the split
/// failed with `no codec for a field of type any`. The fix synthesises a
/// nominally-annotated blank + `Codec.auto` and copies `Receipt` + `Address` into
/// `Shared`; the `Codec.auto` codec ROUND-TRIPS (the SSR model embed relies on
/// the same property), which the build legs prove end-to-end.
#[test]
fn auto_derives_a_record_codec_for_a_plain_record_wire_field() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            auto_record_codec_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        output.status.success(),
        "sky spa-split must SUCCEED by AUTO-DERIVING a `Codec.auto` codec for the plain record `Receipt` (was: `no codec for a field of type any`), got:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();

    // --- The nominally-annotated blank + auto codec are synthesised. ---
    assert!(
        shared.contains("blankReceipt_ : Receipt"),
        "Shared must synthesise a NOMINALLY-annotated blank `blankReceipt_ : Receipt` (an inline unannotated literal erases element types):\n{shared}"
    );
    assert!(
        shared.contains("autoReceiptCodec_ =")
            && shared.contains("Codec.auto blankReceipt_"),
        "Shared must derive `autoReceiptCodec_ = Codec.auto blankReceipt_`:\n{shared}"
    );
    // --- The record (and its NESTED record) are copied into Shared. ---
    assert!(
        shared.contains("type alias Receipt =") && shared.contains("type alias Address ="),
        "Shared must COPY `Receipt` AND the nested `Address` it references:\n{shared}"
    );
    // --- The wire field wires through the derived codec (wrapped by Result). ---
    assert!(
        shared.contains("(Codec.result Codec.error autoReceiptCodec_)"),
        "the `Result Error Receipt` field must wire `Codec.result Codec.error autoReceiptCodec_`:\n{shared}"
    );
    // --- The mixed field defaults are sound (String/Int/Maybe/List/nested). ---
    for needle in ["orderId = \"\"", "amountMinor = 0", "note = Nothing", "tags = []"] {
        assert!(
            shared.contains(needle),
            "blankReceipt_ must default `{needle}`:\n{shared}"
        );
    }
    // --- The Secret/Set fail-closed refusal is NOT falsely tripped here. ---
    assert!(
        !shared.contains("Secret") && !shared.contains("Set "),
        "this fixture carries no Secret/Set — none should appear:\n{shared}"
    );

    // --- Both trees BUILD (Go-gated). The build IS the round-trip proof: the ---
    // --- backend embeds the SSR model and the frontend decodes it symmetrically. ---
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
        "auto-record-codec backend must build natively (the derived codec must type-check)"
    );
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "auto-record-codec frontend must build to wasm");
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// The auto-derive fails CLOSED, with an actionable message, when the wire field
/// is a BARE top-level data-carrying union. `Codec.auto` has no decode/rebuild
/// path for a bare ADT (only a record's FIELDS decode an ADT), so the split must
/// refuse — telling the user to declare a top-level `Codec <T>` — rather than
/// emit a codec that will not round-trip. (A union NESTED in a record is fine.)
#[test]
fn refuses_a_bare_data_carrying_union_wire_field_with_an_actionable_error() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            bare_adt_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        !output.status.success(),
        "sky spa-split must FAIL CLOSED on a bare data-carrying union wire field"
    );
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        combined.contains("cannot DECODE a bare data-carrying union"),
        "the error must explain WHY a bare ADT cannot cross the wire:\n{combined}"
    );
    assert!(
        combined.contains("Define a top-level `Codec Outcome` binding"),
        "the error must be ACTIONABLE — naming the type + the fix:\n{combined}"
    );

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
        |> App.withOnKey (\_ -> Noop)


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
    // genuinely-uncarried `withOnKey` (terminal-only). `withRoutes`/`withNotFound`/
    // `withHead`/`withOnNavigate`/`withRequest`/`withGuard` are all CARRIED, so
    // although they may appear in the warning's explanatory prose, they must NOT be
    // in the dropped list.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    assert!(
        dropped_list.contains("withOnKey"),
        "BUG-2: `App.withOnKey` (terminal-only) must be named in the dropped list, got `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withHead"),
        "SSR-P0: `App.withHead` is now carried into the Spa entry and must NOT be in the dropped list `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withNotFound"),
        "withNotFound is carried into the Spa entry and must not be in the dropped list `{dropped_list}`"
    );
    // Fix 5: withGuard is carried (enforced server-side) — must NOT be dropped.
    assert!(
        !dropped_list.contains("withGuard"),
        "Fix 5: `App.withGuard` is now carried (enforced server-side) and must NOT be in the dropped list `{dropped_list}`:\n{log}"
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
    // lives in a `Persist` branch, NOT in `init`) AND it has no `withOnNavigate`,
    // so there is no GET-safe read to settle → NO data-resolve settle is emitted,
    // and the route renders the pure model. `Spa_ssrSettle` must be absent. (No
    // `withRoutes` either, so the pure model is `model0` directly, not a `routed`
    // binding — fix 2 emits the route-resolve binding only when routes exist.)
    assert!(
        !backend.contains("spaSsrSettle") && backend.contains("resolved =\n            model0"),
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

fn spa_guard_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-guard")
}

fn spa_onnav_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-onnav")
}

/// Fix 2. A routed content site whose PER-ROUTE data is loaded in `onNavigate`
/// (not in `init`, which is `Cmd.none`). The SSR settle must fire
/// `onNavigate page` for the resolved route and settle its GET-safe read, so a
/// direct GET of a deep route server-renders that route's REAL data — into the
/// body a crawler sees AND the embedded `#sky-model` the client boots from.
/// Before the fix the settle ran only `init`'s single (here empty) command, so a
/// deep route rendered a blank body: RED. After: the route's data is present.
///
/// Two layers of proof:
///   * emission (no Go): the backend `ssrHandler` fires `onNavigate` on the
///     resolved route, runs it through `update`, and settles that command;
///   * Go-gated e2e: `GET /a` carries "Alpha content", `GET /b` "Beta content",
///     each in the rendered body and the `#sky-model` blob; `GET /` (no per-route
///     data) carries an empty body — proving the data is per-route + server-resolved.
#[test]
fn spa_ssr_settles_per_route_onnavigate_data() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_onnav_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-ssr-onnav fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: onNavigate is carried onto the client config.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("|> Spa.withOnNavigate spaOnNavigate_"),
        "fix 2: onNavigate must be carried onto the client Spa config:\n{synth}"
    );

    // Emission: the SSR handler fires onNavigate on the resolved route and
    // settles its command (the per-route data load).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    for needle in [
        "spaOnNavigate_ preNav_.page",
        "update navMsg_ preNav_",
        "spaSsrSettle navModel_ navCmd_ update",
    ] {
        assert!(
            backend.contains(needle),
            "fix 2: the SSR handler must fire + settle onNavigate — missing `{needle}`:\n{backend}"
        );
    }

    // ── Go-gated e2e: serve and assert REAL per-route data. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "fix 2: --target web:app must build end-to-end:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the per-route data the onNavigate reads settle.
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/a.txt"), "Alpha content\n").unwrap();
    std::fs::write(backend_dir.join("data/b.txt"), "Beta content\n").unwrap();

    let port = 8977u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-ssr-onnav backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-ssr-onnav backend never reported listening on :{port}");
    }
    let a_body = curl_body_p(port, "/a");
    let b_body = curl_body_p(port, "/b");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let a_body = a_body.expect("GET /a should return a body");
    let b_body = b_body.expect("GET /b should return a body");
    let home_body = home_body.expect("GET / should return a body");

    // /a carries the SERVER-RESOLVED per-route data (the onNavigate read settled),
    // both in the rendered body and the embedded #sky-model blob.
    assert!(
        a_body.contains("data-sky-ssr")
            && a_body.contains("Route A")
            && a_body.contains("Alpha content"),
        "fix 2: GET /a must carry the onNavigate-resolved data in the body:\n{a_body}"
    );
    let a_blob_start = a_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("fix 2: the #sky-model blob must be present on /a");
    let a_blob = &a_body[a_blob_start..];
    let a_blob = &a_blob[..a_blob.find("</script>").expect("blob must close")];
    assert!(
        a_blob.contains(r#""page":"a""#) && a_blob.contains("Alpha content"),
        "fix 2: the /a #sky-model must carry the resolved page + body:\n{a_blob}"
    );
    // /b resolves to its OWN data.
    assert!(
        b_body.contains("Route B") && b_body.contains("Beta content"),
        "fix 2: GET /b must carry its own resolved data (not /a's):\n{b_body}"
    );
    // / has no per-route onNavigate data → empty body (proves per-route, not global).
    let home_app = {
        let s = home_body.find(r#"<div id="app""#).expect("home #app must exist");
        let e = home_body.find(r#"<script id="sky-model""#).unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Home page")
            && !home_app.contains("Alpha content")
            && !home_app.contains("Beta content"),
        "fix 2: GET / must render Home with no per-route data:\n{home_app}"
    );
}

/// Fix 5. `App.withGuard` / `App.withOnNavigate` / `App.withRequest` are CARRIED
/// through the App→Spa synthesis (never named in the drop warning), and the
/// per-Msg guard is enforced SERVER-side: the generated `POST /_rpc/<Msg>`
/// handler calls `spaGuard_ <msg> m` BEFORE `update`, answering 403 on `Err`
/// and never running the effect. This is the trusted authorisation point — the
/// wasm client is untrusted, so a client that skips its own guard still cannot
/// fire the effect.
///
/// Two layers of proof:
///   * synthesis + emission (no Go): the three builders reach named bindings;
///     the backend's `saveHandler` calls `spaGuard_` ahead of `update`.
///   * Go-gated e2e: a valid `POST /_rpc/Save` that the guard DENIES returns 403
///     and leaves the write target untouched (the effect never ran).
#[test]
fn spa_guard_is_enforced_server_side_on_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_guard_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-guard fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: the three builders are carried into NAMED bindings.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    for needle in [
        "spaGuard_ =",
        "spaOnNavigate_ =",
        "spaOnRequest_ =",
        "|> Spa.withOnNavigate spaOnNavigate_",
    ] {
        assert!(
            synth.contains(needle),
            "fix 5: the synthesised entry must carry `{needle}`:\n{synth}"
        );
    }
    // None of the three may appear in the drop warning's dropped LIST.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    for banned in ["withGuard", "withOnNavigate", "withRequest"] {
        assert!(
            !dropped_list.contains(banned),
            "fix 5: `{banned}` is carried and must NOT be in the dropped list `{dropped_list}`:\n{log}"
        );
    }

    // Emission: the backend enforces the guard on the `/_rpc/Save` handler,
    // ahead of `update`, and has a `forbidden` (403) responder.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // The fixture declares `withRequest`, so the guard + update run against the
    // REQUEST-SEEDED model `mReq` (`spaOnRequest_ req m`), NOT the raw client
    // payload model `m`. This closes Judge finding 5: the wasm client can forge
    // any field of `m`, so a guard reading an identity / session field off the
    // payload would trust forged data; re-applying withRequest overwrites those
    // fields from the real request before the guard runs.
    assert!(
        backend.contains("case spaGuard_ (Save p.content) mReq of"),
        "fix 5: the guard must run against the request-seeded model mReq, not the forgeable payload m:\n{backend}"
    );
    assert!(
        backend.contains("Task.succeed (forbidden"),
        "fix 5: a denied guard must answer 403 (forbidden):\n{backend}"
    );
    let reseed_at = backend
        .find("spaOnRequest_ req m")
        .expect("fix 5: the /_rpc handler must re-apply withRequest server-side");
    let guard_at = backend
        .find("case spaGuard_ (Save p.content) mReq of")
        .expect("guard check present");
    let update_at = backend
        .find("update (Save p.content) mReq")
        .expect("update present");
    assert!(
        reseed_at < guard_at && guard_at < update_at,
        "fix 5: the request re-seed must precede the guard, and the guard must precede the update:\n{backend}"
    );

    // ── Go-gated e2e: a denied Save returns 403 and never runs the write. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "fix 5: --target web:app must build end-to-end:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the write target with a known sentinel so we can prove it is UNCHANGED.
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/out.txt"), "seed\n").unwrap();

    let port = 8974u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-guard backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-guard backend never reported listening on :{port}");
    }
    // A VALID request body (decodes to SaveReq) so the handler reaches the guard,
    // not the 400 decode-error path. The guard DENIES Save → 403.
    let posted = curl_post_status_body(port, "/_rpc/Save", r#"{"content":"pwned"}"#);
    let after = std::fs::read_to_string(backend_dir.join("data/out.txt")).unwrap_or_default();
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let (code, _body) = posted.expect("POST /_rpc/Save should return");
    assert_eq!(
        code, 403,
        "fix 5: a guard-denied /_rpc/Save must return 403 (the trusted server-side enforcement)"
    );
    assert_eq!(
        after, "seed\n",
        "fix 5: the denied effect must NOT run — data/out.txt must be unchanged, was {after:?}"
    );
}

fn spa_deeplink_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-deeplink")
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
    // Per-route SSR routes must precede the static fallthrough. Fix 6: the
    // catch-all is `Server.staticNotFound … ssrHandler`, so a cold unmatched path
    // SSRs the NotFound page instead of a bare file-server 404.
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "SSR-P3: per-route SSR routes must precede the static NotFound fallback:\n{backend}"
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

/// Fix 1 (cold deep-link is dark). A cold two-segment URL (`/blog/<slug>`) served
/// by the SSR backend must reference its assets by ROOT-ABSOLUTE URL, so the
/// browser fetches `/wasm_exec.js` + `/main.<hash>.wasm` at ANY route depth. With
/// a bare relative `wasm_exec.js`, a `/blog/<slug>` document resolves it to
/// `/blog/wasm_exec.js` (404 → text/html → "Go is not defined") and the wasm
/// never boots. The Go-gated leg serves the real backend and confirms the SSR
/// document's asset URLs are root-absolute AND that `/wasm_exec.js` is a 200
/// JavaScript asset.
#[test]
fn spa_deep_link_ssr_references_root_absolute_assets() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_deeplink_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the deep-link fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The two-segment param route reaches the SSR handler (a more-specific mux
    // entry than the static catch-all).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    assert!(
        backend.contains("Server.api \"GET /blog/:slug\" ssrHandler"),
        "the /blog/:slug route must be a per-route SSR GET:\n{backend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "deep-link fixture must build end-to-end:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8974u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled deep-link backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("deep-link backend never reported listening on :{port}");
    }
    let deep_body = curl_body_p(port, "/blog/hello");
    let asset = curl_status_ctype(port, "/wasm_exec.js");
    let _ = child.kill();
    let _ = child.wait();

    let deep_body = deep_body.expect("GET /blog/hello should return a body");
    // The cold deep-link SSRs the Post route (crawlable) with the SSR marker.
    assert!(
        deep_body.contains("data-sky-ssr") && deep_body.contains("hello"),
        "GET /blog/hello must SSR the Post route (marker + slug). Body was:\n{deep_body}"
    );
    // Assets are ROOT-ABSOLUTE — correct at this two-segment depth.
    assert!(
        deep_body.contains(r#"<script src="/wasm_exec.js">"#),
        "the deep-link document must load /wasm_exec.js (root-absolute):\n{deep_body}"
    );
    assert!(
        !deep_body.contains(r#"<script src="wasm_exec.js">"#),
        "the deep-link document must NOT reference a bare relative wasm_exec.js:\n{deep_body}"
    );
    assert!(
        deep_body.contains(r#"fetch("/main."#) && deep_body.contains(".wasm\")"),
        "the deep-link document must fetch the wasm by root-absolute URL:\n{deep_body}"
    );

    // /wasm_exec.js is a REAL asset served by the static mount: 200 + JavaScript.
    let (code, ctype) = asset.expect("GET /wasm_exec.js should answer");
    let _ = std::fs::remove_dir_all(&proj);
    assert_eq!(code, 200, "/wasm_exec.js must be 200, got {code} ({ctype})");
    assert!(
        ctype.contains("javascript"),
        "/wasm_exec.js must be served as JavaScript, got Content-Type {ctype}"
    );
}

/// Fix 6 (unknown deep path does not SSR the NotFound page). A cold load of an
/// unknown path used to match no mux pattern and fall through to `Server.static`,
/// returning a bare file-server 404. With the SPA NotFound fallback the catch-all
/// is `Server.staticNotFound … ssrHandler`, so an unmatched app path boots the
/// shell and SSRs the NotFound page (`data-sky-ssr` + the NotFound view) exactly
/// as a known route would, while a request that maps to a REAL asset still serves
/// the file. RED before the fix (a plain `Server.static` catch-all → 404).
#[test]
fn spa_unknown_deep_path_ssrs_the_not_found_page() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_deeplink_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the deep-link fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Backend-source: the static catch-all is the SPA NotFound fallback, NOT a
    // bare `Server.static` (which would 404 unmatched paths).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    assert!(
        backend.contains("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler"),
        "the static catch-all must be the SPA NotFound fallback:\n{backend}"
    );
    assert!(
        !backend.contains("Server.static \"/\" \"../frontend/dist\""),
        "an SSR+routed app must NOT emit a bare Server.static catch-all:\n{backend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "deep-link fixture must build end-to-end:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8975u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled deep-link backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("deep-link backend never reported listening on :{port}");
    }
    let unknown_status = curl_status_ctype(port, "/some/unknown/deep/path");
    let unknown_body = curl_body_p(port, "/some/unknown/deep/path");
    let asset = curl_status_ctype(port, "/wasm_exec.js");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    // An unmatched deep path SSRs the NotFound page — 200, not a bare 404.
    let (code, ctype) = unknown_status.expect("GET unknown path should answer");
    assert_eq!(code, 200, "unmatched path must SSR NotFound (200), got {code} ({ctype})");
    assert!(
        ctype.starts_with("text/html"),
        "the NotFound SSR must be text/html, got {ctype}"
    );
    let unknown_body = unknown_body.expect("GET unknown path should return a body");
    assert!(
        unknown_body.contains("data-sky-ssr")
            && unknown_body.contains("No such page here")
            && unknown_body.contains(r#"<script src="/wasm_exec.js">"#),
        "the unmatched path must SSR the NotFound page inside the shell:\n{unknown_body}"
    );
    assert!(
        !unknown_body.contains("404 page not found"),
        "the bare file-server 404 must be suppressed:\n{unknown_body}"
    );

    // A REAL asset is NOT shadowed by the fallback: still 200 JavaScript.
    let (acode, actype) = asset.expect("GET /wasm_exec.js should answer");
    assert_eq!(acode, 200, "/wasm_exec.js must still be 200, got {acode} ({actype})");
    assert!(
        actype.contains("javascript"),
        "/wasm_exec.js must serve as JavaScript, got {actype}"
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

fn ssr_nested_record_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-nested-record")
}

fn ssr_sibling_db_init_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-sibling-db-init")
}

/// GAP-2 (sibling-module init strip). The `db`-CAF SSR client-leg, but with
/// `init` factored into the SIBLING module `Boot` (the sky-lang.org shape),
/// resolved through the import graph — not the entry source. Before the fix the
/// frontend init-command strip read the ENTRY only, so the sibling `Boot.init`
/// was copied VERBATIM into the wasm frontend with its `Cmd.perform (Db.query db
/// …)`, leaving `Undefined name: db` + the server-only `Db.query` kernel. The fix
/// strips the sibling's init to `Cmd.none` in its frontend copy, drops the
/// dangling `Conn` (backend-only) + `Std.Db` (server-only) imports, and still
/// emits + wires the client model decoder from the sibling init's pure model.
#[test]
fn spa_ssr_sibling_db_init_is_stripped_in_the_frontend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_sibling_db_init_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the sibling-db-init fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The crux: the FRONTEND copy of the SIBLING `Boot` module is stripped to
    // `Cmd.none` and carries no `db` / `Db.*` / backend-only-module reference.
    // Holds without a Go toolchain (the `.sky` is generated before any go build). ──
    let boot = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/src/Boot.sky"),
    )
    .unwrap_or_else(|_| panic!("generated frontend Boot.sky must exist:\n{log}"));
    let boot_code = strip_line_comments(&boot);
    assert!(
        boot_code.contains("Cmd.none"),
        "GAP-2: the sibling `Boot.init` must be stripped to `Cmd.none`:\n{boot}"
    );
    assert!(
        !references_word_test(&boot_code, "db"),
        "GAP-2: the frontend `Boot` must NOT reference the `db` CAF:\n{boot}"
    );
    for needle in ["Db.query", "import Std.Db", "import Conn"] {
        assert!(
            !boot_code.contains(needle),
            "GAP-2: the frontend `Boot` must NOT contain `{needle}`:\n{boot}"
        );
    }

    // The model DECODER is still emitted + wired (derived from the SIBLING init's
    // pure model), so the client boots from `#sky-model` — symmetric with the
    // backend embed.
    let fe_main = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"),
    )
    .unwrap_or_else(|_| panic!("generated frontend Main.sky must exist:\n{log}"));
    let fe_main_code = strip_line_comments(&fe_main);
    assert!(
        fe_main_code.contains("spaModelDecoder_ jsonStr_ =")
            && fe_main_code.contains("Codec.fromJson (Codec.auto")
            && fe_main_code.contains("|> Spa.withModelDecoder spaModelDecoder_")
            && fe_main_code.contains("import Std.Codec"),
        "GAP-2: the frontend must emit + wire a model decoder for the sibling init:\n{fe_main}"
    );

    // ── The BACKEND keeps the `db` CAF (in `Conn`) + settles the read. ──
    let backend = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/backend/src/Conn.sky"),
    )
    .unwrap_or_else(|_| panic!("generated backend Conn.sky must exist:\n{log}"));
    assert!(
        backend.contains("db =") && backend.contains("Db.open"),
        "GAP-2: the `db` CAF must remain in the BACKEND `Conn` module:\n{backend}"
    );
    let backend_main = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/backend/src/Main.sky"),
    )
    .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    assert!(
        backend_main.contains("spaSsrSettle routed cmd0 update"),
        "GAP-2: the sibling init must be resolved GET-safe → a data-resolve settle:\n{backend_main}"
    );

    // ── Go-gated e2e: the whole thing builds; the wasm frontend links with no
    // `Db_*` kernel. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "GAP-2: --target web:app must build end-to-end:\n{log}"
    );
    let fe_go = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"),
    )
    .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query") && !fe_go.contains("Db_open"),
            "GAP-2: the emitted wasm frontend Go must contain no Db_* kernel"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
}

fn mixed_routes_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-mixed-routes")
}

/// GAP-1 (mixed page + `App.api` routes) + GAP-2 together — the full sky-lang.org
/// shape. `withRoutes (Routes.routes ++ apiRoutes)` mixes page routes (sibling
/// `Routes`) with server `App.api` endpoints (`apiRoutes`, its handlers reaching
/// server effects), and `init` is a sibling db-read. Before the fix the
/// synthesised client `spaRoutes_` referenced the server-tainted `apiRoutes` the
/// split drops → the client entry failed to compile (`Undefined name:
/// spaRoutes_`). The fix partitions `withRoutes`: page routes drive the client
/// `spaRoutes_`, the api endpoints become a BACKEND-only `spaApiRoutes_` the
/// server mounts via `App.apiServerRoute`.
#[test]
fn splits_mixed_page_and_api_routes() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&mixed_routes_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the mixed-routes fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── GAP-1 client leg: the synthesised client `spaRoutes_` carries ONLY the
    // page routes; it does NOT reference the server `apiRoutes` binding (which the
    // split drops) nor any api handler. Holds without a Go toolchain. ──
    let fe_main = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"),
    )
    .unwrap_or_else(|_| panic!("generated frontend Main.sky must exist:\n{log}"));
    let fe_code = strip_line_comments(&fe_main);
    assert!(
        fe_code.contains("|> Spa.withRoutes spaRoutes_") && fe_code.contains("spaRoutes_ ="),
        "GAP-1: the frontend must define + wire the page-only `spaRoutes_`:\n{fe_main}"
    );
    for needle in ["apiRoutes", "spaApiRoutes_", "handleItemsApi", "handleHealthz"] {
        assert!(
            !references_word_test(&fe_code, needle),
            "GAP-1: the client `spaRoutes_`/entry must NOT reference the api binding `{needle}`:\n{fe_main}"
        );
    }

    // ── GAP-1 backend leg: the api endpoints are mounted BACKEND-ONLY, via
    // `App.apiServerRoute spaApiRoutes_`, and `spaApiRoutes_` carries the api
    // route source. ──
    let backend = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/backend/src/Main.sky"),
    )
    .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    assert!(
        backend.contains("spaApiRoutes_ =")
            && backend.contains("++ List.concatMap App.apiServerRoute spaApiRoutes_"),
        "GAP-1: the backend must mount the api routes via `App.apiServerRoute spaApiRoutes_`:\n{backend}"
    );
    // The api handlers survive into the backend (they run there).
    assert!(
        backend.contains("handleItemsApi") && backend.contains("handleHealthz"),
        "GAP-1: the api handlers must remain in the BACKEND tree:\n{backend}"
    );

    // ── GAP-1 page routes still SSR, ahead of the static NotFound fallback. ──
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "GAP-1: page routes must SSR ahead of the static NotFound fallback:\n{backend}"
    );

    // ── GAP-2 within the mixed app: the sibling `Boot.init` is stripped. ──
    let boot = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/src/Boot.sky"),
    )
    .unwrap_or_else(|_| panic!("generated frontend Boot.sky must exist:\n{log}"));
    let boot_code = strip_line_comments(&boot);
    assert!(
        boot_code.contains("Cmd.none")
            && !references_word_test(&boot_code, "db")
            && !boot_code.contains("Db.query"),
        "GAP-2: the sibling `Boot.init` must be stripped to `Cmd.none` in the frontend:\n{boot}"
    );

    // ── Go-gated e2e: the whole `--target web:app` build succeeds and the wasm
    // frontend links with no `Db_*` kernel. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "GAP-1: --target web:app must build end-to-end:\n{log}"
    );
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "GAP-1: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );
    let fe_go = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"),
    )
    .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query"),
            "GAP-2: the emitted wasm frontend Go must contain no Db_query kernel"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
}

fn dup_route_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-dup-route")
}

/// FINDING A. A path that is BOTH an `App.route` PAGE route (`/admin/login`) AND
/// an `App.api "GET /admin/login"` server endpoint (the sky-lang.org OAuth-entry
/// shape). The App→Spa synthesis mounts the page route as a per-route SSR
/// `Server.api "GET /admin/login" ssrHandler` AND the api endpoint via
/// `App.apiServerRoute spaApiRoutes_`; both register `GET /admin/login` on Go's
/// mux, which PANICS at boot on the duplicate pattern (`http.ServeMux`),
/// crash-looping the backend. The api endpoint must WIN — the SSR page mount for
/// the same METHOD+PATH is suppressed — so the backend boots. (The Live build
/// tolerates the mix via its single `/` dispatcher, so this is split-only.)
#[test]
fn mixed_page_and_get_api_route_on_same_path_does_not_double_register() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&dup_route_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the dup-route fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let backend_raw = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/backend/src/Main.sky"),
    )
    .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    // Strip `--` comments: the fixture's own doc comment is copied verbatim into
    // the backend and mentions `Server.api "GET /admin/login" ssrHandler` in
    // prose — a `.contains` on the raw source would match THAT, not a real
    // registration. Assert against the code only.
    let backend = strip_line_comments(&backend_raw);

    // ── The api endpoints are mounted (the api handler is the winner). ──
    assert!(
        backend.contains("spaApiRoutes_ =")
            && backend.contains("++ List.concatMap App.apiServerRoute spaApiRoutes_"),
        "FINDING A: the api endpoints must still mount via `App.apiServerRoute`:\n{backend}"
    );

    // ── The colliding page's SSR GET mount is SUPPRESSED — the mux registers
    // `GET /admin/login` exactly once (from the api side), so the backend boots.
    // Before the fix the SSR block ALSO emitted this line → duplicate → panic. ──
    assert!(
        !backend.contains("Server.api \"GET /admin/login\" ssrHandler"),
        "FINDING A: the SSR page mount for `/admin/login` must be suppressed \
         (it collides with `App.api \"GET /admin/login\"`), else the mux \
         double-registers and boot-panics:\n{backend}"
    );

    // ── Non-colliding page routes STILL SSR-mount (the dedupe is scoped to the
    // exact method+path collision, not a blanket suppression). ──
    assert!(
        backend.contains("Server.api \"GET /items\" ssrHandler")
            && backend.contains("Server.api \"GET /{$}\" ssrHandler"),
        "FINDING A: non-colliding page routes must keep their SSR GET mounts:\n{backend}"
    );

    // ── Go-gated real proof: the built backend BOOTS without a
    // duplicate-pattern panic (the actual failure this fixes). ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "FINDING A: --target web:app must build end-to-end:\n{log}"
    );
    let app_bin = proj.join(".skyapp/web-app/.split/backend/sky-out/app");
    assert!(app_bin.is_file(), "backend binary must exist:\n{log}");

    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let boot_log = backend_dir.join("boot.log");
    let logf = std::fs::File::create(&boot_log).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", "8973")
        .stdout(logf.try_clone().unwrap())
        .stderr(logf)
        .spawn()
        .expect("spawn the split backend");
    // Give the mux setup (where the duplicate-pattern panic fires, before the
    // listen loop) time to run, then check the process is still alive.
    std::thread::sleep(std::time::Duration::from_millis(1500));
    let status = child.try_wait().expect("poll the backend process");
    let mut boot = String::new();
    use std::io::Read as _;
    let _ = std::fs::File::open(&boot_log).and_then(|mut f| f.read_to_string(&mut boot));
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    assert!(
        status.is_none(),
        "FINDING A: the split backend must BOOT, not exit at mux setup with a \
         duplicate-registration panic. It exited early ({status:?}); boot log:\n{boot}"
    );
    assert!(
        !boot.contains("multiple registrations") && !boot.contains("panic:"),
        "FINDING A: the backend logged a mux/panic error at boot:\n{boot}"
    );
}

fn static_assets_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-static-assets")
}

/// FINDING C. A `Std.App` web app that DECLARES a served static-file dir
/// (`static = "brand"`, mounted at `staticUrl = "/brand"`) with a real asset at
/// `brand/screenshots/spa-web.png`. The Live runtime mounts that dir; the split
/// backend serves only `frontend/dist`, so before the fix every `/brand/…` asset
/// 404'd under `--target web:app`. The split must copy the declared dir into
/// `frontend/dist/brand/` (at the mount prefix, structure preserved) so the
/// generated backend's `Server.static "/" "../frontend/dist"` serves it. The
/// dir is written by the split GENERATOR (before any build), so this holds
/// without a Go toolchain.
#[test]
fn declared_static_dir_is_propagated_into_the_frontend_dist() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&static_assets_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the static-assets fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The declared static dir lands in the frontend dist at the mount prefix,
    // structure preserved — so the backend serves `/brand/screenshots/spa-web.png`
    // same-origin. Written by the generator, so it holds even without Go.
    let asset = proj.join(".skyapp/web-app/.split/frontend/dist/brand/screenshots/spa-web.png");
    assert!(
        asset.is_file(),
        "FINDING C: the declared static dir must be copied into the frontend \
         dist at its mount prefix (expected {} — split log:\n{log})",
        asset.display()
    );
    // Byte-identical to the source asset (a real copy, not a stub).
    let want = std::fs::read(
        static_assets_fixture_dir().join("brand/screenshots/spa-web.png"),
    )
    .unwrap();
    let got = std::fs::read(&asset).unwrap();
    assert_eq!(
        got, want,
        "FINDING C: the propagated asset must be byte-identical to the source"
    );
    // Directory structure is preserved (the top-level asset too).
    assert!(
        proj.join(".skyapp/web-app/.split/frontend/dist/brand/logo.png").is_file(),
        "FINDING C: nested + top-level assets under the static dir must be copied"
    );

    // ── Go-gated e2e: the whole `--target web:app` build succeeds and the wasm
    // frontend is staged ALONGSIDE the propagated static dir (stage_web_bundle
    // must not clobber it). ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "FINDING C: --target web:app must build end-to-end:\n{log}"
    );
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "FINDING C: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );
    assert!(
        dist.join("brand/screenshots/spa-web.png").is_file(),
        "FINDING C: the propagated static asset must survive the frontend build \
         (stage_web_bundle must not wipe dist/):\n{log}"
    );
    let _ = std::fs::remove_dir_all(&proj);
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
    // The model DECODER (blocker #1/#2) is emitted + wired onto the config, so the
    // client can boot from `#sky-model` — symmetric with the backend embed.
    assert!(
        frontend_code.contains("spaModelDecoder_ jsonStr_ =")
            && frontend_code.contains("Codec.fromJson (Codec.auto")
            && frontend_code.contains("|> Spa.withModelDecoder spaModelDecoder_")
            && frontend_code.contains("import Std.Codec"),
        "SSR client-leg: the frontend must emit + wire a model decoder:\n{frontend}"
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

/// HYDRATION-LOSS regression (the sky-lang.org blog bug). A model with a NESTED
/// RECORD collection (`posts : List Post`) must survive the client hydration
/// decoder. The decoder is `Codec.fromJson (Codec.auto blank)`; if `blank` is an
/// INLINE literal (`Codec.auto ({ page = Home, posts = [] })`), the constrained
/// split-frontend module type-checks it to a STRUCTURAL row and lowers the empty
/// `posts = []` with its element type ERASED to `[]any`. `Codec.auto` then
/// reflects `kind interface`, `Codec.fromJson` returns `Err`, and the boot path
/// SILENTLY falls back to `init` — so every `Post` the server embedded in
/// `#sky-model` is dropped on hydration, with no error (the home page, whose
/// model needs no nested record, hydrated fine; the blog list collapsed to empty).
/// The fix hoists the blank to a top-level ANNOTATED binding
/// (`spaModelBlank_ : Model`) so codegen pins the nominal `List Post` element
/// type and the decoder's `Codec.auto` matches the encoder's byte-for-byte.
///
/// `spa-ssr-db` never caught this: its model is `items : List String`, and a
/// `String` element round-trips through `[]any` unharmed.
#[test]
fn spa_ssr_nested_record_model_blank_pins_the_nominal_type_so_hydration_is_lossless() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_nested_record_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR nested-record fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The FIX (no Go toolchain needed): the client model decoder must derive
    // its `Codec.auto` from a top-level ANNOTATED blank binding, NOT an inline
    // literal that erases nested element types to `[]any`. This is the
    // deterministic RED→GREEN gate: pre-fix the frontend had no `spaModelBlank_`
    // binding and used the inline `Codec.auto ({ … })` form. ──
    let frontend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated frontend entry must exist:\n{log}"));
    let frontend_code = strip_line_comments(&frontend);
    assert!(
        frontend_code.contains("spaModelBlank_ :") && frontend_code.contains("spaModelBlank_ ="),
        "SSR hydration: the client model blank must be a top-level ANNOTATED \
         binding (`spaModelBlank_ : Model`) so its nested collection element types \
         survive lowering:\n{frontend}"
    );
    assert!(
        frontend_code.contains("Codec.fromJson (Codec.auto spaModelBlank_)"),
        "SSR hydration: the decoder must derive `Codec.auto` from the annotated \
         `spaModelBlank_` binding:\n{frontend}"
    );
    assert!(
        !frontend_code.contains("Codec.fromJson (Codec.auto ({"),
        "SSR hydration: the decoder must NOT use the inline blank literal that \
         erases nested element types to `[]any`:\n{frontend}"
    );

    // ── Go-gated: the annotated blank type-checks in the constrained frontend
    // module (the risk the fix takes on) and the frontend links, and the emitted
    // Go pins the model's `posts` field to the nominal `Post` record — NOT `[]any`.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR nested-record: --target web:app must build end-to-end (the annotated \
         blank must type-check in the constrained frontend module):\n{log}"
    );
    let fe_go = std::fs::read_to_string(
        proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"),
    )
    .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            fe_go.contains("Posts []Main_Post_R"),
            "SSR nested-record: the emitted frontend model must pin `posts` to the \
             nominal `Post` record slice (`[]Main_Post_R`); an erased `[]any` field \
             is what `codec_auto` cannot decode → silent hydration loss"
        );
    }

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

// POST a JSON `body` to `path` and return (status_code, response_body). Used by
// the server-side guard test (fix 5): a denied `/_rpc/<Msg>` must answer 403.
fn curl_post_status_body(port: u16, path: &str, body: &str) -> Option<(u32, String)> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args([
            "-s",
            "-w",
            "\n%{http_code}",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-d",
            body,
            &url,
        ])
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let idx = s.rfind('\n')?;
    let (resp_body, code) = s.split_at(idx);
    let code = code.trim().parse::<u32>().ok()?;
    Some((code, resp_body.to_string()))
}

// HTTP status code + Content-Type of `GET http://127.0.0.1:<port><path>`.
fn curl_status_ctype(port: u16, path: &str) -> Option<(u32, String)> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "-o", "/dev/null", "-w", "%{http_code} %{content_type}", &url])
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let mut it = s.splitn(2, ' ');
    let code = it.next()?.trim().parse::<u32>().ok()?;
    let ctype = it.next().unwrap_or("").trim().to_string();
    Some((code, ctype))
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

/// Multi-module TEA auto-split (the sky-lang.org SPA-SSR shape). The TEA core is
/// factored into SHARED modules — `Msg`/`Model`/`Page` in `State`, `init` in
/// `Data`, the route table in `Routes` — not the entry. `sky build --target
/// web:app` must resolve each through the module/import graph, not the entry
/// source text:
///
///   * GAP-A: inject the `Applied<Msg>` RPC-response variants into `State`'s
///     (imported) `Msg` union so the wasm FRONTEND compiles — the generated
///     frontend `update` references `AppliedSaveItem`, undefined before the fix
///     (`E1001`). And `Shared` must not re-import the TEA siblings (they reach
///     `Msg`), or the frontend cycles (`E1010`).
///   * GAP-B: resolve `init`'s DECLARING module (`Data`) for the GET-safe scan so
///     the SSR settle runs and `#sky-model` carries REAL resolved data.
///   * GAP-C: SSR the `/items` route whose literal lives in the sibling `Routes`
///     module, not inline in `spaRoutes_` — a non-root route must 200 with
///     server-rendered content, not fall through to `Server.static` (404).
///
/// The security spine still holds: the effectful `Store` module is backend-only
/// and never reaches the wasm frontend.
#[test]
fn splits_a_multi_module_app_with_tea_core_in_imported_modules() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_multimodule_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the multi-module SSR fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The split ran + the emitted trees type-checked.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "the split must run (emitted frontend + backend type-checked):\n{log}"
    );

    // GAP-A: the FRONTEND copy of the imported `Msg` module (`State`) carries the
    // generated `Applied<Msg>` variants + the `Shared` import they need.
    let fe_state = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/State.sky"))
        .expect("generated frontend State.sky must exist");
    assert!(
        fe_state.contains("| AppliedSaveItem (Result Error SaveItemResp)"),
        "GAP-A: the `Applied<Msg>` variant must be injected into the imported `Msg` union:\n{fe_state}"
    );
    assert!(
        fe_state.contains("import Shared"),
        "GAP-A: the injected Msg module must import `Shared` for the `<Msg>Resp` payload types:\n{fe_state}"
    );

    // GAP-A security spine: the effectful `Store` module never reaches the
    // frontend, and no server effect (`writeFile`/`saveItems`) leaks into it.
    assert!(
        !proj.join(".skyapp/web-app/.split/frontend/src/Store.sky").exists(),
        "the backend-only `Store` module must NOT be emitted into the wasm frontend"
    );
    let fe_main = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend Main.sky must exist");
    // The `SaveItem` server branch is rewritten to an RPC (proving the effect
    // stays server-side); the frontend never calls `Store.saveItems` directly.
    assert!(
        fe_main.contains("Spa.postJson") && fe_main.contains("/_rpc/SaveItem"),
        "GAP-A: the server branch must be rewritten to an RPC in the frontend (effect stays server-side):\n{fe_main}"
    );

    // GAP-A cycle guard: `Shared` must not re-import the TEA sibling modules
    // (they reach `Msg`, which imports `Shared`) — that would be an `E1010`.
    let fe_shared = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Shared.sky"))
        .expect("generated frontend Shared.sky must exist");
    for sib in ["import State", "import Data", "import Routes"] {
        assert!(
            !fe_shared.contains(sib),
            "GAP-A: `Shared` must not import the TEA sibling `{sib}` (cycle):\n{fe_shared}"
        );
    }

    // GAP-B + GAP-C at the backend-source level.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend Main.sky must exist");
    assert!(
        backend.contains("spaSsrSettle routed cmd0 update"),
        "GAP-B: `init` (in the sibling `Data`) must be resolved GET-safe → a data-resolve settle:\n{backend}"
    );
    assert!(
        backend.contains("Server.api \"GET /items\" ssrHandler"),
        "GAP-C: the `/items` route (literal in the sibling `Routes`) must be SSR-registered:\n{backend}"
    );
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "GAP-C: per-route SSR routes must precede the static NotFound fallback:\n{backend}"
    );

    // ── Go-gated: the wasm FRONTEND built + the backend serves REAL per-route
    // data. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(output.status.success(), "--target web:app must build end-to-end:\n{log}");

    // GAP-A: the wasm frontend built to a hashed bundle (no `E1001`).
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "GAP-A: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );

    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the data the settle reads (init: File.readFile "data/items.json").
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::copy(proj.join("data/items.json"), backend_dir.join("data/items.json")).unwrap();

    let port = 8976u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled multi-module SSR backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&proj);
        panic!("multi-module SSR backend never reported listening on :{port}\nlog:\n{buf}");
    }
    let items_body = curl_body_p(port, "/items");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let items_body = items_body.expect("GET /items should return a body");
    let home_body = home_body.expect("GET / should return a body");

    // GAP-C + GAP-B: `/items` server-renders the RESOLVED item list (not a 404 /
    // loading state).
    assert!(
        items_body.contains("data-sky-ssr")
            && items_body.contains("Item list:")
            && items_body.contains("Alpha Widget")
            && items_body.contains("Beta Gadget")
            && items_body.contains("Gamma Gizmo"),
        "GAP-B/C: GET /items must carry the SERVER-RESOLVED item list. Body was:\n{items_body}"
    );
    // GAP-B: the embedded #sky-model blob carries the resolved model.
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("GAP-B: the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"ItemsPage""#) && blob.contains("Alpha Widget"),
        "GAP-B: the #sky-model blob must decode to the RESOLVED model. Blob was:\n{blob}"
    );
    // Per-route body: `/` renders Home, not the Items view.
    let home_app = {
        let s = home_body.find(r#"<div id="app""#).expect("home #app must exist");
        let e = home_body.find(r#"<script id="sky-model""#).unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Welcome home") && !home_app.contains("Item list:"),
        "GAP-C: GET / must render Home's own view, not the Items view:\n{home_app}"
    );
}

// ---------------------------------------------------------------------------
// GAP-1 + GAP-2: `update` in a sibling module, and per-binding subset of a
// MIXED module (pure helper next to server effects). Both are GENERATOR-side.
// ---------------------------------------------------------------------------

fn sibling_update_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-sibling-update/src/Main.sky")
}

fn sibling_update_msg_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-sibling-update-msg/src/Main.sky")
}

fn mixed_purity_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-mixed-purity/src/Main.sky")
}

/// Strip Sky comments (`--` line, `{- -}` block) from `src` so the leak-grep
/// tests CODE, never a docstring. A fixture's own doc-comment legitimately names
/// the server helper it drops (`persist`, `Db`), and a comment can neither run an
/// effect nor import a module — so a name that survives ONLY in a comment is not
/// a leak. Best-effort (does not special-case string literals), sufficient for
/// the generated split sources, which carry no `--` inside string literals.
fn strip_sky_comments(src: &str) -> String {
    // Block comments first (nestable), then line comments.
    let mut no_block = String::with_capacity(src.len());
    let bytes: Vec<char> = src.chars().collect();
    let mut i = 0usize;
    let mut depth = 0usize;
    while i < bytes.len() {
        let c = bytes[i];
        let c2 = bytes.get(i + 1).copied();
        if c == '{' && c2 == Some('-') {
            depth += 1;
            i += 2;
            continue;
        }
        if depth > 0 && c == '-' && c2 == Some('}') {
            depth -= 1;
            i += 2;
            continue;
        }
        if depth == 0 {
            no_block.push(c);
        } else if c == '\n' {
            no_block.push('\n');
        }
        i += 1;
    }
    no_block
        .lines()
        .map(|l| l.split("--").next().unwrap_or(""))
        .collect::<Vec<_>>()
        .join("\n")
}

/// Concatenate every `*.sky` file under `dir` (recursively) into one string —
/// the whole-tree leak-grep surface, COMMENTS STRIPPED. A server kernel /
/// tainted-helper name found ANYWHERE under `frontend/` is a security leak, so
/// the check must see the whole subtree, not just `Main.sky`.
fn concat_sky_tree(dir: &std::path::Path) -> String {
    let mut out = String::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let rd = match std::fs::read_dir(&d) {
            Ok(rd) => rd,
            Err(_) => continue,
        };
        for e in rd.filter_map(|e| e.ok()) {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().and_then(|s| s.to_str()) == Some("sky") {
                out.push_str(&format!("\n----- {} -----\n", p.display()));
                out.push_str(&strip_sky_comments(&std::fs::read_to_string(&p).unwrap_or_default()));
            }
        }
    }
    out
}

/// Build the generated backend (native) + frontend (wasm) of a split, Go-gated.
fn build_both_legs(out: &std::path::Path) {
    if !required(Need::Go, have_go()) {
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
}

/// GAP-1: the TEA `update` lives in a SIBLING module (`Update`), its `Msg` in a
/// THIRD module (`Msgs`). The split must regenerate the partitioned `update` IN
/// its own frontend module copy — the pure arm client-local, the server arm an
/// RPC — and NEVER leak the server helper (`persist` -> `Db`) or the backend-only
/// `Conn` connection into the wasm frontend. Was refused before this fix.
#[test]
fn sibling_module_update_regenerates_in_its_own_frontend_copy() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            sibling_update_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed when `update` lives in a sibling module (GAP-1)"
    );

    // The sibling `update` is regenerated IN its own frontend module copy.
    let front_update = std::fs::read_to_string(out.join("frontend/src/Update.sky"))
        .expect("the frontend Update module must exist");
    assert!(
        front_update.contains("cleanDraft"),
        "the pure client helper `cleanDraft` must reach the frontend Update copy:\n{front_update}"
    );
    assert!(
        front_update.contains("Spa.postJson") && front_update.contains("/_rpc/Save"),
        "the server arm `Save` must become an RPC in the frontend Update copy:\n{front_update}"
    );

    // SECURITY — no server kernel / tainted helper / backend-only module anywhere
    // in the frontend tree.
    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in ["Db.", "persist", "loadTodos", "saveTodos", "import Conn", "Conn.", "System.getenv", "File."] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }
    assert!(
        !out.join("frontend/src/Conn.sky").exists(),
        "SECURITY LEAK: the backend-only Conn module must NOT be in the frontend"
    );

    // The backend keeps the effect + exposes the RPC endpoint.
    let back_update = std::fs::read_to_string(out.join("backend/src/Update.sky")).unwrap();
    assert!(
        back_update.contains("persist") && back_update.contains("Db.query"),
        "backend Update must keep the server helper `persist` -> `Db` (it runs it):\n{back_update}"
    );
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("Server.api \"POST /_rpc/Save\""),
        "backend must expose the generated RPC endpoint for `Save`:\n{back}"
    );

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

/// GAP-1 compose case: `update` AND `Msg` both live in the SIBLING module. The
/// module's frontend copy composes TWO per-module transforms — inject the
/// `Applied<Msg>` RPC variants into the `Msg` union, THEN regenerate `update` —
/// and still leaks no server binding.
#[test]
fn sibling_module_update_and_msg_compose_in_one_frontend_copy() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            sibling_update_msg_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed when `update` + `Msg` share a sibling module (GAP-1 compose)"
    );

    let front_update = std::fs::read_to_string(out.join("frontend/src/Update.sky"))
        .expect("the frontend Update module must exist");
    // GAP-A: the Applied<Msg> variant is injected into the Msg union here.
    assert!(
        front_update.contains("AppliedSave"),
        "the `AppliedSave` RPC variant must be injected into the sibling `Msg` union:\n{front_update}"
    );
    // GAP-1: update regenerated with the RPC arm + the pure helper.
    assert!(
        front_update.contains("cleanDraft")
            && front_update.contains("Spa.postJson")
            && front_update.contains("/_rpc/Save"),
        "the sibling `update` must be regenerated (pure arm + RPC arm):\n{front_update}"
    );

    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in ["Db.", "persist", "import Conn", "Conn.", "System.getenv", "File."] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

/// GAP-2: a MIXED module (`Store`) holds a PURE helper (`formatTotal`, called by
/// a CLIENT `update` arm) ALONGSIDE server effects (File / env / a 3-hop server
/// chain / a higher-order server pass). The split must emit a FRONTEND copy of
/// `Store` containing ONLY `formatTotal` — dropping every server binding — while
/// the backend copy stays the FULL module. Was refused before this fix.
#[test]
fn mixed_module_emits_pure_subset_to_frontend_effects_stay_backend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            mixed_purity_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed on a mixed-purity module (GAP-2)"
    );

    // The frontend Store subset exists and carries ONLY the pure helper.
    let front_store = std::fs::read_to_string(out.join("frontend/src/Store.sky"))
        .expect("the frontend Store subset must exist");
    assert!(
        front_store.contains("formatTotal"),
        "the pure helper `formatTotal` must reach the frontend Store subset:\n{front_store}"
    );

    // SECURITY — no server binding anywhere in the frontend tree.
    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in [
        "File.", "System.getenv", "loadTodos", "saveTodos", "deepLoad", "midLoad",
        "leafLoad", "dataDir", "loadEach", "Db.",
    ] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }

    // The frontend `update` keeps the client arm (via `formatTotal`) and RPCs the
    // server arm; the backend keeps the File effect.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("formatTotal") && front.contains("/_rpc/Add"),
        "frontend Main must call `formatTotal` (client) and RPC `Add` (server):\n{front}"
    );
    let back_store = std::fs::read_to_string(out.join("backend/src/Store.sky")).unwrap();
    assert!(
        back_store.contains("File.") && back_store.contains("loadTodos"),
        "backend Store must keep the FULL module (File effects):\n{back_store}"
    );

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

// ---------------------------------------------------------------------------
// G5: `init`'s returned MODEL embeds a server read the wasm client cannot
// reproduce — the split must REFUSE with actionable guidance, not emit a
// frontend that references the backend-only read or silently drop the data.
// ---------------------------------------------------------------------------

fn repo_root() -> PathBuf {
    // CARGO_MANIFEST_DIR = <repo>/rust/crates/sky
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("..")
        .canonicalize()
        .expect("repo root")
}

fn init_model_read_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-init-model-read")
}

/// G5 refusal: `init` bakes a server read (`loadAll` -> `Db`) into its returned
/// MODEL. The split must return `Err` naming the read + the deferral fix, and
/// must NOT have written a frontend that references `loadAll` / `Db.` / `db`.
#[test]
fn init_model_embedding_a_server_read_is_refused_with_guidance() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let res = project::spa_split::generate(
        &repo_root(),
        &init_model_read_fixture_dir(),
        Some("Main"),
        &out,
        None,
    );

    let err = match res {
        Err(e) => e,
        Ok(_) => panic!(
            "a server read baked into `init`'s returned model must REFUSE the split (the wasm client cannot reproduce it)"
        ),
    };
    // Names the offending read and points at the deferral fix.
    assert!(
        err.contains("init") && err.contains("model"),
        "the refusal must be about `init`'s returned model, got:\n{err}"
    );
    assert!(
        err.contains("loadAll") || err.contains("Db"),
        "the refusal must NAME the offending server read (`loadAll` / `Db`), got:\n{err}"
    );
    assert!(
        err.contains("command") && err.contains("Got"),
        "the refusal must give the fix (defer to `init`'s command + a `Got<Field>` arm), got:\n{err}"
    );

    // Because it refused, NO frontend that references the backend-only read may
    // have been written — no silent leak, no dropped data.
    let front_dir = out.join("frontend");
    if front_dir.exists() {
        let front_tree = concat_sky_tree(&front_dir);
        for needle in ["loadAll", "Db.", "db "] {
            assert!(
                !front_tree.contains(needle),
                "REFUSED split must not have written a frontend referencing `{needle}`:\n{front_tree}"
            );
        }
    }

    let _ = std::fs::remove_dir_all(&out);
}

/// No false positive: the SUPPORTED deferred pattern — a server read placed in
/// `init`'s COMMAND (`Cmd.perform (Db.query …) GotItems`) with a PURE returned
/// model (`{ page = Home, items = [] }`) — must NOT trip the G5 refusal. Uses the
/// existing `spa-ssr-db` fixture, whose model is pure and read is in the command.
#[test]
fn server_read_deferred_to_init_command_is_not_refused() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let res = project::spa_split::generate(
        &repo_root(),
        &ssr_db_fixture_dir(),
        Some("Main"),
        &out,
        None,
    );

    // It may succeed, or fail for an UNRELATED reason, but it must NEVER fail
    // with the init-model refusal — the read is deferred to the command.
    if let Err(e) = &res {
        assert!(
            !e.contains("returned model embeds server read"),
            "the deferred-command pattern must NOT trip the init-model refusal (false positive):\n{e}"
        );
    }

    let _ = std::fs::remove_dir_all(&out);
}

/// Server-internal effect chaining — the end-to-end behaviour gate. `Reload`
/// returns `Cmd.perform (File.readFile "data/note.txt") Reloaded`, and
/// `Reloaded (Ok raw)` writes `raw` into `note`. Before this feature the
/// generated handler DISCARDED the command, so `POST /_rpc/Reload` answered with
/// an empty `note`. Now the whole chain settles server-side inside the RPC, so
/// the response carries the file's contents. Builds + RUNS the backend, then
/// POSTs — Go-gated (needs the toolchain + curl).
#[test]
fn server_internal_chain_e2e_post_reload_returns_file_note() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    // 1. Generate the split.
    let status = Command::new(SKY)
        .args([
            "spa-split",
            server_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    // 2. Build the backend natively.
    let backend_dir = out.join("backend");
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&backend_dir)
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "backend must build:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );

    // 3. The file the chain reads. `File.readFile "data/note.txt"` resolves
    // relative to the process CWD, so plant it under the backend dir.
    let note_body = "hello-from-the-server-chain";
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/note.txt"), note_body).unwrap();

    // 4. Run the backend, wait for it to listen.
    let port = 8953u16;
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend build must produce sky-out/app");
    let log_path = backend_dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("ENV", "production")
        .env("SKY_CONSOLE_AUTH", "off")
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn the compiled backend");

    let ready = wait_for_listening_substr(&log_path, port, 120);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&out);
        panic!("backend never reported listening on :{port}\nlog:\n{buf}");
    }

    // 5. POST /_rpc/Reload with an empty read-set body; the response must carry
    // the file's contents in `note`.
    let body = curl_post(port, "/_rpc/Reload", "{}");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&out);

    let body = body.expect("POST /_rpc/Reload should return a body");
    assert!(
        body.contains(note_body),
        "POST /_rpc/Reload must settle the File read chain server-side and return \
         `note` = the file contents (`{note_body}`), but the response was:\n{body}"
    );
}

// Wait until the server's log carries a line containing "listening" and `:port`.
fn wait_for_listening_substr(log_path: &std::path::Path, port: u16, tries: u32) -> bool {
    use std::io::Read as _;
    let needle = format!(":{port}");
    for _ in 0..tries {
        if let Ok(mut f) = std::fs::File::open(log_path) {
            let mut buf = String::new();
            if f.read_to_string(&mut buf).is_ok() {
                if buf.lines().any(|l| l.to_lowercase().contains("listening") && l.contains(&needle)) {
                    return true;
                }
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

// POST `data` as JSON to `http://127.0.0.1:<port><path>`, returning the body.
fn curl_post(port: u16, path: &str, data: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "-X", "POST", "-H", "Content-Type: application/json", "-d", data, &url])
        .output()
        .ok()?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
}
