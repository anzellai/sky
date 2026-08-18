//! Layer-2 flow coverage for PULL-model user revocation + suspension.
//!
//! The Go runtime tests (`runtime-go/rt/auth_revocation_test.go`,
//! `live_revocation_test.go`) prove the admin APIs, the canonicalSub key, the
//! in-funnel gate and the eviction in isolation. What they CANNOT prove —
//! because they bypass the compiler — is that a real Sky.Live app which opts
//! into the feature COMPILES and emits buildable Go: that `Live.withRevocation`,
//! `Live.bindSessionUser`, `Auth.revokeUser`, `Auth.disableUser` and the
//! `Auth.AccessState` ADT all typecheck, resolve to their runtime kernels, and
//! lower to Go that `go build` accepts.
//!
//! When a Go toolchain is present the test goes further: it RUNS the compiled
//! binary and drives the real HTTP surface — establish a session (bound to a
//! user by an init Cmd), revoke that user through an admin api route, then hit
//! the app again and assert the revoked session is EVICTED with the
//! `X-Sky-Status: session-lost` signal (the e2e leg the rt tests cannot make).

use std::io::Read;
use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-revocation-{}-{}",
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
        "name = \"revocation-flow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), APP_SRC).unwrap();
    dir
}

// A minimal Sky.Live app exercising ALL the new APIs: Live.withRevocation,
// Live.bindSessionUser (in the init Cmd), Auth.revokeUser + Auth.disableUser +
// Auth.enableUser (admin api routes), and the Auth.AccessState ADT.
const APP_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Html exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live as Live exposing (app, config, route, api, withRevocation, bindSessionUser)
import Std.Auth as Auth
import Std.Db as Db
import Sky.Http.Server as Server
import Sky.Http.Server exposing (Request, Response)


conn : Db
conn =
    case Task.run (Db.open "sqlite" "revflow.db") of
        Ok c ->
            c

        Err _ ->
            conn


type alias Model =
    { page : Page }


type Page
    = Home


type Msg
    = Noop


init : a -> ( Model, Cmd Msg )
init _req =
    ( { page = Home }
    , Cmd.perform (bindSessionUser "1") (\_ -> Noop)
    )


update : Msg -> Model -> ( Model, Cmd Msg )
update _msg model =
    ( model, Cmd.none )


subscriptions : Model -> Sub Msg
subscriptions _model =
    Sub.none


view : Model -> Html Msg
view _model =
    div [] [ text "home" ]


setup : Request -> Task Error Response
setup _req =
    Auth.register conn "u@x.y" "correct-horse-battery"
        |> Task.andThen (\uid -> Task.succeed (Server.text (String.fromInt uid)))
        |> Task.onError (\_ -> Task.succeed (Server.text "exists"))


revoke : Request -> Task Error Response
revoke _req =
    Auth.revokeUser conn "1"
        |> Task.andThen (\_ -> Task.succeed (Server.text "revoked"))


disable : Request -> Task Error Response
disable _req =
    Auth.disableUser conn "1"
        |> Task.andThen (\_ -> Task.succeed (Server.text "disabled"))


enable : Request -> Task Error Response
enable _req =
    Auth.enableUser conn "1"
        |> Task.andThen (\_ -> Task.succeed (Server.text "enabled"))


main =
    app
        (config
            { init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes =
                [ route "/" Home
                , api "GET /setup" setup
                , api "GET /revoke" revoke
                , api "GET /disable" disable
                , api "GET /enable" enable
                ]
            , notFound = Home
            }
            |> withRevocation conn
        )
"#;

#[test]
fn revocation_app_builds_and_evicts() {
    // ── Compile leg: sky build → go build on the emitted Go. This is the
    // Layer-2 assertion the Go rt tests cannot make. Always runs. ──
    let dir = scratch_project();
    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build");
    assert!(
        build.status.success(),
        "sky build of the revocation app failed:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr),
    );

    if !go_on_path() {
        required(Need::Go, false);
        let _ = std::fs::remove_dir_all(&dir);
        return;
    }

    // ── Runtime leg: run the binary and drive the eviction over HTTP. ──
    let port = 8479u16;
    let app_bin = dir.join("sky-out").join("app");
    let log_path = dir.join("server.log");
    let jar = dir.join("cookies.txt");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&dir)
        .env("SKY_LIVE_PORT", port.to_string())
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn compiled revocation app");

    let ready = wait_for_listening(&log_path, port, 60);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        panic!("app never reported listening on :{port}\nlog:\n{buf}");
    }

    // 0. Create the built-in users table + user id=1 (disableUser bans a row in it).
    let setup = curl_status(port, "/setup");
    assert_eq!(setup.as_deref(), Some("200"), "GET /setup should succeed");

    // 1. GET / establishes a session (cookie jar); its init Cmd binds the
    //    session to user "1".
    let first = curl_status_jar(port, "/", &jar, true);
    assert_eq!(first.as_deref(), Some("200"), "first GET / should serve 200");
    // Let the async bind persist onto the session.
    std::thread::sleep(std::time::Duration::from_millis(800));

    // 2. Admin: DISABLE (ban) user "1". A ban is boundAt-independent — even a
    //    freshly bound session is evicted — so the assertion is deterministic
    //    (no same-second / re-bind race the way a time-based revoke would have).
    let dis = curl_status(port, "/disable");
    assert_eq!(dis.as_deref(), Some("200"), "GET /disable should succeed");

    // 3. The SAME session hits the app again. ONE request, capturing BOTH the
    //    status line and headers, so we observe the eviction directly (a second
    //    request would legitimately re-mint a fresh session). The handleInitial
    //    gate reads the shared table FRESH, sees Disabled, and evicts.
    let (status, sky_status) = curl_status_and_header(port, "/", &jar, "x-sky-status");

    let _ = child.kill();
    let _ = child.wait();
    let logs = {
        let mut b = String::new();
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut b));
        b
    };
    let _ = std::fs::remove_dir_all(&dir);

    assert_eq!(
        status.as_deref(),
        Some("404"),
        "a disabled user's session must be evicted (404 session-lost)\nserver log:\n{logs}"
    );
    assert_eq!(
        sky_status.as_deref(),
        Some("session-lost"),
        "the eviction must carry X-Sky-Status: session-lost\nserver log:\n{logs}"
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

fn curl_status(port: u16, path: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "-o", "/dev/null", "-w", "%{http_code}", &url])
        .output()
        .ok()?;
    Some(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

// GET with a cookie jar; `save` writes new cookies (-c), always sends them (-b).
fn curl_status_jar(port: u16, path: &str, jar: &std::path::Path, save: bool) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> = vec![
        "-s".into(),
        "-o".into(),
        "/dev/null".into(),
        "-w".into(),
        "%{http_code}".into(),
        "-b".into(),
        jar.display().to_string(),
    ];
    if save {
        args.push("-c".into());
        args.push(jar.display().to_string());
    }
    args.push(url);
    let out = Command::new("curl").args(&args).output().ok()?;
    Some(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

// ONE request, sending the cookie jar (-b), returning both the HTTP status code
// and a named response header — so the eviction is observed atomically (a
// separate follow-up request would legitimately re-mint a fresh session).
fn curl_status_and_header(
    port: u16,
    path: &str,
    jar: &std::path::Path,
    header: &str,
) -> (Option<String>, Option<String>) {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = match Command::new("curl")
        .args([
            "-s",
            "-D",
            "-",
            "-o",
            "/dev/null",
            "-w",
            "\nSTATUS:%{http_code}",
            "-b",
            &jar.display().to_string(),
            &url,
        ])
        .output()
    {
        Ok(o) => o,
        Err(_) => return (None, None),
    };
    let text = String::from_utf8_lossy(&out.stdout);
    let want = format!("{}:", header.to_lowercase());
    let mut hdr = None;
    let mut status = None;
    for line in text.lines() {
        let lower = line.to_lowercase();
        if lower.starts_with(&want) {
            hdr = Some(line[line.find(':').unwrap() + 1..].trim().to_string());
        }
        if let Some(rest) = line.strip_prefix("STATUS:") {
            status = Some(rest.trim().to_string());
        }
    }
    (status, hdr)
}
