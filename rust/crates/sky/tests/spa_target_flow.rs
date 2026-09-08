//! `sky build --target <t>` — the Sky.Spa multi-target build pipeline.
//!
//! The `web` target is the one every platform's shell wraps and the only one
//! buildable with no native SDK, so it is the portable regression: it must
//! compile the client to wasm and stage a servable bundle (index.html +
//! main.wasm + wasm_exec.js) under `dist/`. The desktop/ios/android shells are
//! covered end-to-end by the example scaffolds + the earlier manual verification
//! (they need cgo-WebKit / the Android SDK / full Xcode, gated elsewhere); here
//! we pin the shape the whole pipeline rests on.

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

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-spatarget-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"spatarget\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    // A minimal client — the pipeline shape does not depend on the app being a
    // full Sky.Spa TEA loop, only on it compiling to wasm.
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\nmain =\n    println \"spa\"\n",
    )
    .unwrap();
    dir
}

#[test]
fn target_web_stages_a_servable_wasm_bundle() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("web");
    let out = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "build --target web failed:\n{log}");

    // The wasm client + its JS bootstrap land in sky-out/.
    assert!(dir.join("sky-out").join("main.wasm").is_file(), "no sky-out/main.wasm:\n{log}");
    assert!(
        dir.join("sky-out").join("wasm_exec.js").is_file(),
        "no sky-out/wasm_exec.js:\n{log}"
    );
    // …and the servable bundle is staged under dist/ (the wasm is CONTENT-HASHED,
    // main.<hash>.wasm, so a redeploy is never served a stale cached copy).
    let dist = dir.join("dist");
    for f in ["index.html", "wasm_exec.js"] {
        assert!(dist.join(f).is_file(), "dist/{f} missing:\n{log}");
    }
    let hashed = std::fs::read_dir(&dist)
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .find(|n| n.starts_with("main.") && n.ends_with(".wasm"))
        .unwrap_or_else(|| panic!("no content-hashed main.<hash>.wasm in dist:\n{log}"));
    // index.html must actually bootstrap THAT wasm (not an empty placeholder).
    let index = std::fs::read_to_string(dist.join("index.html")).unwrap();
    assert!(
        index.contains("wasm_exec.js") && index.contains(&hashed),
        "index.html does not bootstrap the hashed wasm ({hashed}):\n{index}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn unknown_target_is_rejected_before_building() {
    let dir = scratch("badtarget");
    let out = Command::new(SKY)
        .args(["build", "--target", "frobnicate", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target frobnicate");
    assert!(!out.status.success(), "an unknown target must fail");
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("unknown target") && err.contains("frobnicate"),
        "the error must name the bad target and the supported set:\n{err}"
    );
    // It must fail FAST — before writing any wasm.
    assert!(
        !dir.join("sky-out").join("main.wasm").is_file(),
        "an unknown target must not have run the wasm build"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A minimal Std.App `App.app` app whose model carries a `Secret` field and
/// whose `Persist` server branch touches only `count` (so the Secret is NOT on
/// any RPC wire — a wire Secret is already a hard build error in build_wire).
/// Building this `--target web:app` synthesises the SSR handler, which embeds
/// the WHOLE model via `Codec.auto` — the path a Secret silently diverges on.
const SECRET_MODEL_APP: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.File as File
import Sky.Core.Secret as Secret exposing (Secret)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)


type alias Model =
    { token : Secret
    , count : Int
    }


type Msg
    = Increment
    | Persist


init : () -> ( Model, Cmd Msg )
init _ =
    ( { token = Secret.unsafeFromString "hunter2", count = 0 }, Cmd.none )


persist : Int -> Task Error ()
persist k =
    File.writeFile "count.txt" (String.fromInt k)


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Persist ->
            let
                _ =
                    Task.run (persist model.count)
            in
            ( { model | count = model.count + 1 }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column []
        [ Ui.text ("count: " ++ String.fromInt model.count)
        , Ui.el [ Ui.onClick Increment ] (Ui.text "+")
        , Ui.el [ Ui.onClick Persist ] (Ui.text "save")
        ]


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

/// Fix 7 — a model field whose type `Codec.auto` cannot round-trip through the
/// SSR model embed (the opaque `Secret`) must be caught at BUILD time, naming
/// the field + its type, not left to a runtime console.error + a silent
/// fall-back to `init`. Build the Secret-model app `--target web:app` and assert
/// the build-time warning names the field. RED before the fix: silent.
#[test]
fn web_app_warns_on_a_secret_model_field_the_ssr_embed_cannot_round_trip() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("secretmodel");
    std::fs::write(dir.join("src").join("Main.sky"), SECRET_MODEL_APP).unwrap();
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        log.contains("warning [sky.spa]") && log.contains("`token`") && log.contains("Secret"),
        "web:app build must warn about the Secret model field the SSR embed cannot round-trip:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// Mirrors SECRET_MODEL_APP exactly (so the SSR model embed IS emitted — a
// server branch makes emit_ssr true), swapping the un-round-trippable field
// from `token : Secret` to `tags : Set String`. This isolates the Set field as
// the only variable.
const SET_MODEL_APP: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.File as File
import Sky.Core.Set as Set exposing (Set)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)


type alias Model =
    { tags : Set String
    , count : Int
    }


type Msg
    = Increment
    | Persist


init : () -> ( Model, Cmd Msg )
init _ =
    ( { tags = Set.fromList [ "a", "b" ], count = 0 }, Cmd.none )


persist : Int -> Task Error ()
persist k =
    File.writeFile "count.txt" (String.fromInt k)


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Persist ->
            let
                _ =
                    Task.run (persist model.count)
            in
            ( { model | count = model.count + 1 }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column []
        [ Ui.text ("count: " ++ String.fromInt model.count)
        , Ui.el [ Ui.onClick Increment ] (Ui.text "+")
        , Ui.el [ Ui.onClick Persist ] (Ui.text "save")
        ]


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

const RPC_ERROR_APP: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.File as File
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)


type alias Model =
    { count : Int
    , lastError : String
    }


type Msg
    = Increment
    | Persist
    | RpcFailed Error


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0, lastError = "" }, Cmd.none )


persist : Int -> Task Error ()
persist k =
    File.writeFile "count.txt" (String.fromInt k)


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Persist ->
            let
                _ =
                    Task.run (persist model.count)
            in
            ( { model | count = model.count + 1 }, Cmd.none )

        RpcFailed e ->
            ( { model | lastError = Error.toString e }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column []
        [ Ui.text ("count: " ++ String.fromInt model.count)
        , Ui.text ("err: " ++ model.lastError)
        , Ui.el [ Ui.onClick Increment ] (Ui.text "+")
        , Ui.el [ Ui.onClick Persist ] (Ui.text "save")
        ]


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
        |> App.withRpcError (\e -> RpcFailed e)


main : Task Error ()
main =
    App.run app
"#;

/// Item 4 — a failed RPC must be routable INTO the app's own `update`, not just
/// logged. `App.withRpcError (\e -> RpcFailed e)` is carried through the App→Spa
/// synthesis into a `spaRpcError_` binding, and the generated frontend
/// `Applied<Msg> (Err e)` arm dispatches `update (spaRpcError_ e) model` so the
/// app's view can show the error — parity with Sky.Live. Without the builder the
/// arm keeps the loud-log floor (`( model, Cmd.none )`). RED before item 4: the
/// Err arm was ALWAYS `( model, Cmd.none )`, with no way to reach `update`.
#[test]
fn web_app_with_rpc_error_routes_a_failed_rpc_into_update() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("rpcerror");
    std::fs::write(dir.join("src").join("Main.sky"), RPC_ERROR_APP).unwrap();
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "withRpcError web:app build must succeed end-to-end:\n{log}");

    // The synthesis carried the handler into a named binding, and the frontend
    // routes the Err arm through it into `update`.
    let front = std::fs::read_to_string(dir.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend entry must exist");
    assert!(
        front.contains("spaRpcError_ ="),
        "item 4: the frontend must carry the spaRpcError_ binding:\n{front}"
    );
    assert!(
        front.contains("update (spaRpcError_ e) model"),
        "item 4: the frontend Err arm must route the failed RPC into update:\n{front}"
    );
    assert!(
        !front.contains("AppliedPersist (Err _) ->"),
        "item 4: with the handler present the Err arm must NOT keep the swallow floor:\n{front}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Item 4 control — WITHOUT `App.withRpcError`, the same app keeps the loud-log
/// floor: the Err arm is `( model, Cmd.none )` (the transport failure is still
/// surfaced at the runtime perform site, not swallowed).
#[test]
fn web_app_without_rpc_error_keeps_the_floor() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("rpcerrorfloor");
    // Drop the withRpcError builder line + the now-unused RpcFailed machinery.
    let app = RPC_ERROR_APP
        .replace("        |> App.withRpcError (\\e -> RpcFailed e)\n", "")
        .replace("    | RpcFailed Error\n", "")
        .replace(
            "        RpcFailed e ->\n            ( { model | lastError = Error.toString e }, Cmd.none )\n\n",
            "",
        )
        .replace(", lastError : String", "")
        .replace(", lastError = \"\"", "")
        .replace("        , Ui.text (\"err: \" ++ model.lastError)\n", "");
    std::fs::write(dir.join("src").join("Main.sky"), &app).unwrap();
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "control web:app build must succeed:\n{log}\n---\n{app}");
    let front = std::fs::read_to_string(dir.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend entry must exist");
    assert!(
        !front.contains("spaRpcError_"),
        "item 4 control: no withRpcError means no spaRpcError_ binding:\n{front}"
    );
    assert!(
        front.contains("AppliedPersist (Err _) ->"),
        "item 4 control: without the handler the Err arm keeps the floor:\n{front}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Fix 7 completeness (Judge finding 2) — a `Set` model field ALSO cannot
/// round-trip the SSR embed: it erases to Go `any`, so the client decode fails
/// ("cannot decode kind interface") and the first paint falls back to `init`
/// while Sky.Live renders the Set. It must be caught at BUILD time, not left to
/// degrade at runtime. RED before the fix: the detector matched only a
/// top-level `Secret` tail, so a Set field slipped through silently.
#[test]
fn web_app_warns_on_a_set_model_field_the_ssr_embed_cannot_round_trip() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("setmodel");
    std::fs::write(dir.join("src").join("Main.sky"), SET_MODEL_APP).unwrap();
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        log.contains("warning [sky.spa]") && log.contains("`tags`") && log.contains("Set"),
        "web:app build must warn about the Set model field the SSR embed cannot round-trip:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
