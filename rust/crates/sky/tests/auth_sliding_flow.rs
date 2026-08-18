//! Layer-2 flow coverage for SLIDING auth tokens.
//!
//! The Go runtime tests (`runtime-go/rt/auth_sliding_test.go`) prove the
//! middleware's decision logic in isolation. What they CANNOT prove — because
//! they bypass the compiler entirely — is that a real Sky.Live app which opts
//! into the feature *compiles and emits buildable Go*: that
//! `Live.withAuthSliding`'s record (including its
//! `revokedCheck : Maybe (String -> Task Error Bool)` field), `Auth.signSlidingToken`
//! and `Auth.setSlidingCookie` all typecheck, resolve to their runtime kernels,
//! and lower to Go that `go build` accepts. That is the Layer-2 duty the auth
//! lifecycle carries and that coerce-floor / roundtrip are structurally blind to.
//!
//! When a Go toolchain is present the test goes further: it RUNS the compiled
//! binary and drives the builder-owned login setter over real HTTP, asserting
//! the sliding cookie is issued with the builder's attributes (HttpOnly,
//! Path=/, SameSite=Strict) — the same attributes the re-issue middleware uses.

use std::io::Read;
use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

// A ≥32-byte secret so `Auth.signSlidingToken` succeeds at runtime.
const SLIDING_SECRET: &str = "sliding-flow-secret-at-least-32-bytes!!";

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-sliding-{}-{}",
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
        "name = \"sliding-flow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), APP_SRC).unwrap();
    dir
}

// A minimal Sky.Live app that exercises ALL THREE new APIs: the builder
// (withAuthSliding, with a Maybe-wrapped revokedCheck), signSlidingToken, and
// the builder-owned setSlidingCookie on a GET /login api route.
const APP_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Html exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, config, route, api, withAuthSliding)
import Std.Auth as Auth
import Sky.Http.Server as Server
import Sky.Http.Server exposing (Request, Response)
import System


secret : String
secret =
    System.getenvOr "SKY_AUTH_TOKEN_SECRET" "dev-secret-at-least-32-bytes-xxxxxx"


type alias Model =
    { page : Page }


type Page
    = Home


type Msg
    = Noop


init : a -> ( Model, Cmd Msg )
init _req =
    ( { page = Home }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update _msg model =
    ( model, Cmd.none )


subscriptions : Model -> Sub Msg
subscriptions _model =
    Sub.none


view : Model -> Html Msg
view _model =
    div [] [ text "home" ]


login : Request -> Task Error Response
login req =
    case Auth.signSlidingToken secret { sub = "1" } { windowSeconds = 900, maxLifetimeSeconds = 86400 } of
        Ok token ->
            Task.succeed
                (Server.json "{\"ok\":true}"
                    |> Auth.setSlidingCookie req token
                )

        Err _ ->
            Task.succeed (Server.withStatus 500 (Server.text "sign failed"))


revoked : String -> Task Error Bool
revoked _sub =
    Task.succeed False


main =
    app
        (config
            { init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes = [ route "/" Home, api "GET /login" login ]
            , notFound = Home
            }
            |> withAuthSliding
                { cookie = "sky_auth"
                , secretEnv = "SKY_AUTH_TOKEN_SECRET"
                , sameSite = "Strict"
                , revokedCheck = Just revoked
                }
        )
"#;

#[test]
fn sliding_auth_app_builds_and_serves() {
    if !go_on_path() {
        required(Need::Go, false);
        return;
    }
    let dir = scratch_project();

    // ── Compile leg: sky build → go build on the emitted Go. This is the
    // Layer-2 assertion the Go rt tests cannot make. ──
    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build");
    assert!(
        build.status.success(),
        "sky build of the sliding-auth app failed:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr),
    );

    // ── Runtime leg: run the compiled binary and drive the login setter over
    // HTTP. Bind a fixed high port; the child inherits our SLIDING_SECRET. ──
    let port = 8477u16;
    let app_bin = dir.join("sky-out").join("app");
    let log_path = dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&dir)
        .env("SKY_LIVE_PORT", port.to_string())
        .env("SKY_AUTH_TOKEN_SECRET", SLIDING_SECRET)
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn compiled sliding-auth app");

    // Poll the log for the listening line (fieldbook's readiness pattern).
    let ready = wait_for_listening(&log_path, port, 60);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        panic!("app never reported listening on :{port}\nlog:\n{buf}");
    }

    let cookie_line = curl_header(port, "/login", "set-cookie");
    let root_code = curl_status(port, "/");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&dir);

    // GET / serves the app.
    assert_eq!(root_code.as_deref(), Some("200"), "GET / should serve 200");

    // The login route set the sliding cookie via the builder-owned setter,
    // with the builder's attributes (G4: same attributes the re-issue uses).
    let cookie = cookie_line.expect("GET /login must set a Set-Cookie header");
    assert!(
        cookie.contains("sky_auth="),
        "expected the sky_auth cookie, got: {cookie}"
    );
    assert!(
        cookie.to_lowercase().contains("httponly"),
        "sliding cookie must be HttpOnly, got: {cookie}"
    );
    assert!(
        cookie.contains("Path=/"),
        "sliding cookie must carry Path=/, got: {cookie}"
    );
    assert!(
        cookie.contains("SameSite=Strict"),
        "sliding cookie must be SameSite=Strict (builder default), got: {cookie}"
    );
}

fn wait_for_listening(log_path: &std::path::Path, port: u16, tries: u32) -> bool {
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

// Return the first value of `header` (case-insensitive) from the response
// headers of `GET http://127.0.0.1:<port><path>`, using curl -D-.
fn curl_header(port: u16, path: &str, header: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "-D", "-", "-o", "/dev/null", &url])
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&out.stdout);
    let want = format!("{}:", header.to_lowercase());
    for line in text.lines() {
        if line.to_lowercase().starts_with(&want) {
            return Some(line[line.find(':').unwrap() + 1..].trim().to_string());
        }
    }
    None
}

fn curl_status(port: u16, path: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "-o", "/dev/null", "-w", "%{http_code}", &url])
        .output()
        .ok()?;
    Some(String::from_utf8_lossy(&out.stdout).trim().to_string())
}
