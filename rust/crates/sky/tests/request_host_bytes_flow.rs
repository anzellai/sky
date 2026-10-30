//! End-to-end, with the real compiler and a real HTTP client (`curl`):
//!
//! 1. `Server.Request` carries the `host` a request used. A user app serves
//!    two domains and must tell them apart. Go's net/http moves the `Host`
//!    header out of `r.Header` into `r.Host`, so before v0.27.13 `Request` had
//!    no host and `Dict.get "Host" req.headers` was always `Nothing`. Both a
//!    `Sky.Http.Server` route handler and the `App.withRequest` hook of a
//!    Sky.Live app must now see `a.localhost:8000` for a request sent with
//!    `Host: a.localhost:8000`, in `req.host` and in `req.headers`.
//! 2. `Server.bytes` answers binary data byte for byte (a 1x1 PNG from
//!    `Bytes.fromBase64`), and `File.writeBytes` round-trips non-UTF-8 data.
//! 3. `List.repeat` checks, builds and runs with Elm semantics.
//!
//! The Go-level legs (runtime-go/rt/request_host_bytes_test.go) and the
//! conformance cases (ListConformanceTest, SyncFileConformanceTest) run per
//! commit. Needs a `go` toolchain and `curl`.

#![cfg(unix)]

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

#[path = "support/child_group.rs"]
mod child_group;
use child_group::ChildGroup;

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const SKY_LIMIT: Duration = Duration::from_secs(420);

/// The 1x1 PNG the server answers, as base64 (in the Sky source) and as bytes.
const PNG_B64: &str = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";
const PNG: &[u8] = &[
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
    0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
    0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0x64, 0x60, 0xf8, 0x5f,
    0x0f, 0x00, 0x02, 0x87, 0x01, 0x80, 0xeb, 0x47, 0xba, 0x92, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
    0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
];

const SERVER_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Bytes as Bytes
import Sky.Core.Dict as Dict
import Sky.Core.File as File
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.System as System
import Sky.Core.Task as Task
import Sky.Core.Error exposing (Error)
import Sky.Http.Server as Server exposing (Request, Response)
import Std.Log exposing (println)


pngB64 : String
pngB64 =
    "__PNG_B64__"


hostRoute : Request -> Task Error Response
hostRoute req =
    Task.succeed
        (Server.text
            ("HOST=" ++ req.host ++ ";HDR=" ++ Maybe.withDefault "none" (Dict.get "Host" req.headers) ++ ";"))


pixelRoute : Request -> Task Error Response
pixelRoute _ =
    case Bytes.fromBase64 pngB64 of
        Just png ->
            Task.succeed (Server.bytes "image/png" png)

        Nothing ->
            Task.succeed (Server.text "bad base64" |> Server.withStatus 500)


repeatLine : String
repeatLine =
    "REPEAT="
        ++ String.join "," (List.repeat 3 "ab")
        ++ "|"
        ++ String.fromInt (List.length (List.repeat 0 1))
        ++ "|"
        ++ String.fromInt (List.length (List.repeat -4 1))
        ++ "|"
        ++ String.fromInt (List.foldl (\x acc -> x + acc) 0 (List.repeat 4 5))


writeBytesCheck : Task Error ()
writeBytesCheck =
    case Bytes.fromBase64 pngB64 of
        Nothing ->
            println "WRITEBYTES=nobase64"

        Just png ->
            File.tempDir "sky-wb-"
                |> Task.andThen
                    (\dir ->
                        File.writeBytes (dir ++ "/p.png") png
                            |> Task.andThen (\_ -> File.readFileBytes (dir ++ "/p.png"))
                            |> Task.andThen
                                (\ints ->
                                    println
                                        ("WRITEBYTES="
                                            ++ String.fromInt (List.length ints)
                                            ++ ":"
                                            ++ String.join "," (List.map String.fromInt (List.take 4 ints))
                                        )
                                )
                    )


main : Task Error ()
main =
    println repeatLine
        |> Task.andThen (\_ -> writeBytesCheck)
        |> Task.andThen (\_ -> System.getenvInt "HB_PORT")
        |> Task.andThen
            (\port ->
                Server.listen port
                    [ Server.get "/host" hostRoute
                    , Server.get "/pixel.png" pixelRoute
                    ]
            )
"#;

/// A Sky.Live app whose `App.withRequest` hook copies the request's host
/// (field and header) into the model; the view prints both.
const LIVE_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Dict as Dict
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


type alias Model =
    { host : String, hostHeader : String }


type Msg
    = Noop


init : a -> ( Model, Cmd Msg )
init _ =
    ( { host = "", hostHeader = "" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update _ model =
    ( model, Cmd.none )


fromRequest req model =
    ( { model | host = req.host, hostHeader = Maybe.withDefault "none" (Dict.get "Host" req.headers) }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column
        []
        [ Ui.text ("HOST=" ++ model.host ++ ";")
        , Ui.text ("HDR=" ++ model.hostHeader ++ ";")
        ]


main : Task Error ()
main =
    App.app { init = init, update = update, view = view, subscriptions = \_ -> Sub.none }
        |> App.withRequest fromRequest
        |> App.withNotFound ()
        |> App.run
"#;

fn have_tool(name: &str, arg: &str) -> bool {
    Command::new(name)
        .arg(arg)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn unique(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-{tag}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn free_port() -> u16 {
    let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    l.local_addr().unwrap().port()
}

fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = unique("rhb-out");
    let err_path = unique("rhb-err");
    let mut child = ChildGroup::spawn(
        cmd.stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap())),
    )
    .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + SKY_LIMIT;
    let status = loop {
        match child.child().try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                child.kill();
                panic!("`{what}` did not finish within {}s", SKY_LIMIT.as_secs());
            }
            None => std::thread::sleep(Duration::from_millis(50)),
        }
    };
    let out = Output {
        status,
        stdout: std::fs::read(&out_path).unwrap_or_default(),
        stderr: std::fs::read(&err_path).unwrap_or_default(),
    };
    let _ = std::fs::remove_file(&out_path);
    let _ = std::fs::remove_file(&err_path);
    out
}

fn both(o: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&o.stdout),
        String::from_utf8_lossy(&o.stderr)
    )
}

fn project_with(tag: &str, src: &str) -> PathBuf {
    let project = unique(tag);
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        format!("name = \"{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n"),
    )
    .unwrap();
    std::fs::write(project.join("src/Main.sky"), src).unwrap();
    project
}

fn build(project: &Path) -> PathBuf {
    let check = run_bounded(
        Command::new(SKY)
            .args(["check", "src/Main.sky"])
            .current_dir(project),
        "sky check src/Main.sky",
    );
    assert!(
        check.status.success(),
        "sky check failed:\n{}",
        both(&check)
    );
    let out = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(project),
        "sky build src/Main.sky",
    );
    assert!(out.status.success(), "sky build failed:\n{}", both(&out));
    let std_app = project.join(".skyapp/web/sky-out/app");
    if std_app.is_file() {
        return std_app;
    }
    project.join("sky-out/app")
}

struct Running {
    child: ChildGroup,
    log_path: PathBuf,
}

impl Running {
    fn launch(project: &Path, bin: &Path, envs: &[(&str, String)]) -> Running {
        let log_path = unique("rhb-log");
        let log = std::fs::File::create(&log_path).unwrap();
        let mut cmd = Command::new(bin);
        cmd.current_dir(project)
            .env_remove("ENV")
            .env_remove("SKY_ENV")
            .env_remove("SKY_LIVE_STORE")
            .env_remove("SKY_HOST")
            .env_remove("SKY_ALLOWED_HOSTS")
            .stdin(Stdio::null())
            .stdout(log.try_clone().unwrap())
            .stderr(log);
        for (k, v) in envs {
            cmd.env(k, v);
        }
        let child = ChildGroup::spawn(&mut cmd).expect("spawn the built app");
        Running { child, log_path }
    }

    fn read_log(&self) -> String {
        let mut buf = String::new();
        let _ = std::fs::File::open(&self.log_path).and_then(|mut f| f.read_to_string(&mut buf));
        buf
    }

    /// GET `path` with `Host: a.localhost:8000` until the server answers.
    fn get(&mut self, port: u16, path: &str, out_file: Option<&Path>) -> String {
        let url = format!("http://127.0.0.1:{port}{path}");
        for _ in 0..240 {
            if let Ok(Some(status)) = self.child.child().try_wait() {
                panic!("app exited early ({status}):\n{}", self.read_log());
            }
            let mut args: Vec<String> = vec![
                "-s".into(),
                "-f".into(),
                "--max-time".into(),
                "20".into(),
                "-H".into(),
                "Host: a.localhost:8000".into(),
            ];
            if let Some(f) = out_file {
                args.push("-o".into());
                args.push(f.display().to_string());
                args.push("-w".into());
                args.push("%{content_type}".into());
            }
            args.push(url.clone());
            let out = Command::new("curl").args(&args).output().expect("run curl");
            if out.status.success() {
                return String::from_utf8_lossy(&out.stdout).to_string();
            }
            std::thread::sleep(Duration::from_millis(250));
        }
        panic!("{url} never answered:\n{}", self.read_log());
    }

    fn stop(mut self) {
        self.child.kill();
        let _ = std::fs::remove_file(&self.log_path);
    }
}

fn between<'a>(body: &'a str, start: &str) -> Option<&'a str> {
    let i = body.find(start)? + start.len();
    let rest = &body[i..];
    Some(&rest[..rest.find(';')?])
}

#[test]
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit legs: runtime-go/rt request_host_bytes_test.go + conformance"]
fn a_server_route_sees_the_host_and_answers_exact_bytes() {
    if !have_tool("go", "version") {
        required(Need::Go, false);
        return;
    }
    assert!(have_tool("curl", "--version"), "curl is required");
    let src = SERVER_SRC.replace("__PNG_B64__", PNG_B64);
    let project = project_with("hostbytes", &src);
    let bin = build(&project);
    let port = free_port();
    let mut app = Running::launch(&project, &bin, &[("HB_PORT", port.to_string())]);

    let host = app.get(port, "/host", None);
    assert_eq!(
        between(&host, "HOST="),
        Some("a.localhost:8000"),
        "req.host:\n{host}"
    );
    assert_eq!(
        between(&host, "HDR="),
        Some("a.localhost:8000"),
        "Dict.get \"Host\" req.headers:\n{host}"
    );

    let png_path = unique("rhb-png");
    let content_type = app.get(port, "/pixel.png", Some(&png_path));
    assert_eq!(content_type, "image/png");
    let got = std::fs::read(&png_path).unwrap();
    assert_eq!(got, PNG, "Server.bytes changed the bytes on the wire");
    let _ = std::fs::remove_file(&png_path);

    let log = app.read_log();
    assert!(
        log.contains("REPEAT=ab,ab,ab|0|0|20"),
        "List.repeat:\n{log}"
    );
    assert!(
        log.contains(&format!("WRITEBYTES={}:137,80,78,71", PNG.len())),
        "File.writeBytes round trip:\n{log}"
    );
    app.stop();
    let _ = std::fs::remove_dir_all(&project);
}

#[test]
#[ignore = "heavy go-build+run e2e leg; runs nightly and in the release suite (--ignored). Per-commit legs: runtime-go/rt request_host_bytes_test.go"]
fn the_with_request_hook_sees_the_host() {
    if !have_tool("go", "version") {
        required(Need::Go, false);
        return;
    }
    assert!(have_tool("curl", "--version"), "curl is required");
    let project = project_with("livehost", LIVE_SRC);
    let bin = build(&project);
    let port = free_port();
    let mut app = Running::launch(&project, &bin, &[("SKY_LIVE_PORT", port.to_string())]);
    let page = app.get(port, "/", None);
    assert_eq!(
        between(&page, "HOST="),
        Some("a.localhost:8000"),
        "withRequest req.host:\n{page}"
    );
    assert_eq!(
        between(&page, "HDR="),
        Some("a.localhost:8000"),
        "withRequest Dict.get \"Host\" req.headers:\n{page}"
    );
    app.stop();
    let _ = std::fs::remove_dir_all(&project);
}
