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
    assert!(
        dir.join("sky-out").join("main.wasm").is_file(),
        "no sky-out/main.wasm:\n{log}"
    );
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
///
/// Heavy full `sky build --target web:app` (wasm + Go cross-compile) — #[ignore]d
/// to stay OFF the T1 tier budget (docs/ci-test-architecture-v2.md §8.2); runs
/// nightly via `--ignored`. Per-commit codegen coverage is the fast unit test
/// `rpc_error_arm_routes_into_update` in crates/project/src/spa_split.rs.
#[ignore = "heavy web:app build; nightly via --ignored; per-commit leg: project spa_split rpc_error_arm_routes_into_update"]
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
    assert!(
        out.status.success(),
        "withRpcError web:app build must succeed end-to-end:\n{log}"
    );

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
///
/// Heavy full web:app build — #[ignore]d for the T1 budget; nightly via
/// `--ignored`. Per-commit codegen coverage: the same
/// `rpc_error_arm_routes_into_update` unit test asserts the floor arm too.
#[ignore = "heavy web:app build; nightly via --ignored; per-commit leg: project spa_split rpc_error_arm_routes_into_update"]
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
    assert!(
        out.status.success(),
        "control web:app build must succeed:\n{log}\n---\n{app}"
    );
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
///
/// Heavy full web:app build — #[ignore]d for the T1 budget; nightly via
/// `--ignored`. Per-commit coverage of the detector is the fast unit test
/// `secret_and_set_are_flagged_others_are_not` in crates/project/src/spa_split.rs.
#[ignore = "heavy web:app build; nightly via --ignored; per-commit leg: project spa_split secret_and_set_are_flagged_others_are_not"]
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

/// Build `dir` with `sky build <args>` under a private cache root and the phase
/// report on; returns the combined log (asserting success).
fn build_timed(dir: &std::path::Path, args: &[&str]) -> String {
    let out = Command::new(SKY)
        .arg("build")
        .args(args)
        .current_dir(dir)
        .env("XDG_CACHE_HOME", dir.join("xdg"))
        .env("SKY_TIMINGS", "1")
        // A Go build cache no other `sky` touches. Sky's own `~/.sky/go-build` is
        // cleaned whenever a `sky` with a different embedded runtime builds (a
        // sibling worktree, an installed release), and a clean between the two
        // builds here would force the re-link this test asserts does not happen.
        .env("GOCACHE", test_gocache())
        .output()
        .expect("run sky build");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "sky build {args:?} failed:\n{log}");
    log
}

fn test_gocache() -> std::path::PathBuf {
    match std::env::var("GOCACHE") {
        Ok(v) if !v.trim().is_empty() => std::path::PathBuf::from(v),
        _ => std::env::temp_dir().join("sky-spatarget-gocache"),
    }
}

/// The file's inode. `go build` on an up-to-date target only touches it (same
/// inode, new mtime); a re-link writes a new file, so a new inode.
#[cfg(unix)]
fn inode(p: &std::path::Path) -> u64 {
    use std::os::unix::fs::MetadataExt;
    std::fs::metadata(p)
        .map(|m| m.ino())
        .unwrap_or_else(|e| panic!("{}: {e}", p.display()))
}

/// A no-change rebuild of a `--target web:app` app does no avoidable work:
///   * the emitted Go of both legs is byte-identical (deterministic codegen,
///     so Go's content-addressed cache hits),
///   * neither the native backend nor the wasm client is re-linked (the
///     restage keeps the legs' `sky-out/`, so `go build` sees them up to date),
///   * the `.gz` / `.br` bundle variants come from the content-hash cache
///     instead of re-running gzip / brotli-11,
///   * the backend binary is not bloated by the console's inlined closure names
///     (it was 218-229 MB; since v0.25.18 the console's generated Go has no
///     nested immediately-called closures, so with inlining on it is ~62 MB and
///     its longest symbol name ~200 bytes).
/// Before the fix every rebuild wiped `.skyapp/web-app/`, re-linked both legs
/// and re-ran brotli-11 on the multi-MB wasm (measured: 19 s warm, most of it
/// brotli). A second part pins the internal `--no-precompress` flag the
/// `sky run` path passes to its frontend leg.
///
/// Heavy (two full web:app builds + a wasm build) — #[ignore]d for the T1
/// budget; nightly via `--ignored`. Per-commit legs: the `precompress::tests`
/// cache tests, `tests::restage_keeps_only_the_go_build_outputs` and
/// `go_compile_shape` (the emitted-Go shape and compile-memory budget).
#[cfg(unix)]
#[ignore = "heavy web:app build; nightly via --ignored; per-commit legs: sky precompress::tests + restage_keeps_only_the_go_build_outputs"]
#[test]
fn web_app_no_change_rebuild_reuses_outputs() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("rebuild");
    std::fs::write(dir.join("src").join("Main.sky"), RPC_ERROR_APP).unwrap();
    let split = dir.join(".skyapp").join("web-app").join(".split");
    let backend_bin = split.join("backend").join("sky-out").join("app");
    let wasm = split.join("frontend").join("sky-out").join("main.wasm");
    let backend_go = split.join("backend").join("sky-out").join("main.go");
    let frontend_go = split.join("frontend").join("sky-out").join("main.go");

    let first = build_timed(&dir, &["--target", "web:app", "src/Main.sky"]);
    assert!(
        first.contains("sky timings"),
        "SKY_TIMINGS=1 must print the phase report:\n{first}"
    );
    let go_b1 = std::fs::read(&backend_go).unwrap();
    let go_f1 = std::fs::read(&frontend_go).unwrap();
    let (bin_t1, wasm_t1) = (inode(&backend_bin), inode(&wasm));
    if String::from_utf8_lossy(&go_b1).contains("sky-app/rt/console_app") {
        let size = std::fs::metadata(&backend_bin).unwrap().len();
        assert!(
            size < 120 * 1024 * 1024,
            "backend binary is {size} bytes: the console's inlined closure names are back"
        );
    }

    let second = build_timed(&dir, &["--target", "web:app", "src/Main.sky"]);
    assert_eq!(
        std::fs::read(&backend_go).unwrap(),
        go_b1,
        "backend Go must be byte-identical across a no-change rebuild"
    );
    assert_eq!(
        std::fs::read(&frontend_go).unwrap(),
        go_f1,
        "frontend Go must be byte-identical across a no-change rebuild"
    );
    assert_eq!(
        inode(&backend_bin),
        bin_t1,
        "a no-change rebuild must not re-link the backend:\n{second}"
    );
    assert_eq!(
        inode(&wasm),
        wasm_t1,
        "a no-change rebuild must not re-link the wasm client:\n{second}"
    );
    assert!(
        second.contains("precompress gzip -9 (cached)"),
        "a no-change rebuild must take the .gz from the content cache:\n{second}"
    );
    let dist = split.join("frontend").join("dist");
    let has = |suffix: &str| {
        std::fs::read_dir(&dist)
            .unwrap()
            .flatten()
            .any(|e| e.file_name().to_string_lossy().ends_with(suffix))
    };
    assert!(has(".wasm.gz"), "the cached .gz must be staged into dist/");

    // `--no-precompress` (what `sky run` passes its frontend leg) stages the
    // bundle without the `.gz` / `.br` variants.
    let web = scratch("noprecompress");
    build_timed(
        &web,
        &["--target", "web", "--no-precompress", "src/Main.sky"],
    );
    let variants: Vec<String> = std::fs::read_dir(web.join("dist"))
        .unwrap()
        .flatten()
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .filter(|n| n.ends_with(".gz") || n.ends_with(".br"))
        .collect();
    assert!(
        variants.is_empty(),
        "--no-precompress must not write .gz / .br: {variants:?}"
    );
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&web);
}
