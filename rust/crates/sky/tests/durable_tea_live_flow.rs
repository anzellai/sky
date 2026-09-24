//! Behavioural coverage for zero-annotation durable TEA on the Sky.Live backend
//! — `App.withDurable` on an `App.app` web app, across a process restart, over
//! HTTP, with only sqlite (no Postgres).
//!
//! # The scenario this proves
//!
//! A Live session's Model normally lives in the session store. With the DEFAULT
//! memory store that store is in-process, so a process restart loses it. The
//! zero-annotation durability layer (`App.withDurable db modelCodec`) snapshots
//! the Model to the database (keyed by the session id) after every update, and
//! restores it on a fresh mount. So even with a memory session store, a Model
//! survives a restart whenever the `sky_sid` cookie survives.
//!
//!   1. Process 1: `GET /` establishes an `sky_sid` session (count 0), then 3
//!      dispatched events mutate the Model to a distinctive value (3). Each
//!      update snapshots the Model to the sqlite durable table.
//!   2. Process 1 is KILLED. A fresh process 2 starts against the SAME project
//!      dir (same sqlite db) with a memory session store, so it has NEVER seen
//!      the sid — its session store is cold.
//!   3. Process 2: `GET /` with the SAME cookie renders 3, not 0 — proving the
//!      Model was restored from the durable snapshot, not re-`init`ed.
//!   4. Negative control: a FRESH cookie on process 2 renders 0 — so the
//!      assertion distinguishes "restored the snapshot" from "always renders 3".
//!
//! There is NO durable code in the app's model / msg / update — durability is
//! the single `App.withDurable` builder line. Needs a `go` toolchain + `curl`.

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const SKY_LIMIT: Duration = Duration::from_secs(420);

/// A minimal durable Sky.Live counter. `App.withDurableId "live"` makes it
/// durable with NO change to model / msg / update. The view renders the count as
/// `DCOUNT=<n>` so `curl` can read it.
const APP_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)
import Std.Db as Db exposing (Db)
import Std.Codec as Codec exposing (Codec)


type alias Model =
    { count : Int }


modelCodec : Codec Model
modelCodec =
    Codec.auto { count = 0 }


type Msg
    = Increment


init : a -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column
        []
        [ Ui.text ("DCOUNT=" ++ String.fromInt model.count)
        , Ui.button [] { onPress = Just Increment, label = Ui.text "inc" }
        ]


mkApp db =
    App.app { init = init, update = update, view = view, subscriptions = \_ -> Sub.none }
        |> App.withNotFound ()
        |> App.withDurable db modelCodec


main : Task Error ()
main =
    Db.connect () |> Task.andThen (\db -> App.run (mkApp db))
"#;

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn have_curl() -> bool {
    Command::new("curl")
        .arg("--version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn unique(tag: &str) -> String {
    format!(
        "sky-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )
}

fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = std::env::temp_dir().join(unique("dl-cmd-out"));
    let err_path = std::env::temp_dir().join(unique("dl-cmd-err"));
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
        .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
        .spawn()
        .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + SKY_LIMIT;
    let status = loop {
        match child.try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
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

struct Server {
    child: Child,
    port: u16,
    log_path: PathBuf,
}

impl Server {
    fn launch(project: &Path, app_bin: &Path, port: u16) -> Server {
        let log_path = std::env::temp_dir().join(unique("dl-log"));
        let log = std::fs::File::create(&log_path).unwrap();
        let child = Command::new(app_bin)
            .current_dir(project)
            .env("SKY_LIVE_PORT", port.to_string())
            // Default memory session store: cold on every fresh process, which
            // is exactly the case durable restore must cover.
            .env_remove("SKY_LIVE_STORE")
            .stdin(Stdio::null())
            .stdout(log.try_clone().unwrap())
            .stderr(log)
            .spawn()
            .unwrap_or_else(|e| panic!("failed to spawn server on :{port}: {e}"));
        let mut s = Server {
            child,
            port,
            log_path,
        };
        if !s.wait_for_log(&format!("Sky.Live listening on :{port}"), 60) {
            let log = s.read_log();
            panic!("server never reported listening on :{port}\nlog:\n{log}");
        }
        s
    }

    fn wait_for_log(&mut self, needle: &str, tries: u32) -> bool {
        for _ in 0..tries {
            if let Ok(Some(status)) = self.child.try_wait() {
                panic!(
                    "server on :{} exited early ({status}) before logging {needle:?}\nlog:\n{}",
                    self.port,
                    self.read_log()
                );
            }
            if self.read_log().contains(needle) {
                return true;
            }
            std::thread::sleep(Duration::from_millis(250));
        }
        false
    }

    fn read_log(&self) -> String {
        let mut buf = String::new();
        let _ = std::fs::File::open(&self.log_path).and_then(|mut f| f.read_to_string(&mut buf));
        buf
    }

    fn stop(mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_file(&self.log_path);
    }
}

// A failed assertion unwinds past `stop`; without this the server process
// outlives the test and holds its port, so the next run cannot start.
impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn curl_get(port: u16, path: &str, jar: &Path, save: bool) -> String {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> = vec![
        "-s".into(),
        "--max-time".into(),
        "30".into(),
        "-b".into(),
        jar.display().to_string(),
    ];
    if save {
        args.push("-c".into());
        args.push(jar.display().to_string());
    }
    args.push(url);
    let out = Command::new("curl")
        .args(&args)
        .output()
        .expect("run curl GET");
    String::from_utf8_lossy(&out.stdout).to_string()
}

fn curl_get_fresh(port: u16, path: &str) -> String {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "--max-time", "30", &url])
        .output()
        .expect("run curl GET (fresh)");
    String::from_utf8_lossy(&out.stdout).to_string()
}

fn curl_post_event(port: u16, jar: &Path, csrf: &str, handler_id: &str) -> String {
    let url = format!("http://127.0.0.1:{port}/_sky/event");
    let body =
        format!("{{\"sessionId\":\"\",\"msg\":\"\",\"args\":[],\"handlerId\":\"{handler_id}\"}}");
    let out = Command::new("curl")
        .args([
            "-s",
            "--max-time",
            "30",
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "-b",
            &jar.display().to_string(),
            "-H",
            "Content-Type: application/json",
            "-H",
            &format!("X-Sky-Csrf: {csrf}"),
            "-X",
            "POST",
            &url,
            "-d",
            &body,
        ])
        .output()
        .expect("run curl POST");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

fn cookie_from_jar(jar: &Path, name: &str) -> Option<String> {
    let text = std::fs::read_to_string(jar).ok()?;
    let mut found = None;
    for line in text.lines() {
        let cols: Vec<&str> = line.split('\t').collect();
        if cols.len() >= 7 && cols[5] == name {
            found = Some(cols[6].to_string());
        }
    }
    found
}

fn rendered_count(body: &str) -> Option<i64> {
    let idx = body.find("DCOUNT=")?;
    let rest = &body[idx + "DCOUNT=".len()..];
    let digits: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    digits.parse().ok()
}

fn button_handler_id(body: &str) -> Option<String> {
    let key = "data-sky-hid=\"";
    let idx = body.find(key)?;
    let rest = &body[idx + key.len()..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

#[test]
// T1 tier-budget: a real `go build` + two server processes + an HTTP restart
// handoff. It shares the boot/persist mechanism with the lighter, per-commit
// `durable_tea_cli_flow.rs` (both go through `durable_tea.go`), so the Live-only
// glue (fresh-session restore + post-update persist) is the delta this proves.
// Runs nightly via `--ignored`; remove `#[ignore]` to re-arm per-commit.
#[ignore = "heavy HTTP restart e2e leg; runs nightly (--ignored). Lighter leg: durable_tea_cli_flow.rs"]
fn a_live_model_survives_a_process_restart_via_durable_snapshot() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    if !have_curl() {
        panic!("curl is required to drive the Sky.Live HTTP restart flow");
    }

    // Build the durable app once. The project dir (holding the sqlite db) is
    // shared by both server processes.
    let project = std::env::temp_dir().join(unique("dlive"));
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"dlive\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\
         [database]\ndriver = \"sqlite\"\npath = \"dlive.db\"\n",
    )
    .unwrap();
    std::fs::write(project.join("src").join("Main.sky"), APP_SRC).unwrap();

    let build = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&project),
        "sky build src/Main.sky",
    );
    assert!(
        build.status.success(),
        "durable Live app build failed:\n{}",
        both(&build)
    );
    let app_bin = project.join("sky-out").join("app");
    assert!(
        app_bin.is_file(),
        "no binary at {}\n{}",
        app_bin.display(),
        both(&build)
    );

    // ── Process 1: establish a session and mutate the Model to 3. ──
    let server1 = Server::launch(&project, &app_bin, 8821);
    let jar = std::env::temp_dir().join(unique("dl-jar"));
    let initial = curl_get(server1.port, "/", &jar, true);
    assert_eq!(
        rendered_count(&initial),
        Some(0),
        "a fresh session should start at the init value (0):\n{initial}"
    );
    let sid = cookie_from_jar(&jar, "sky_sid").expect("process 1 set no sky_sid cookie");
    let csrf = cookie_from_jar(&jar, "__sky_csrf").expect("process 1 set no __sky_csrf cookie");
    let handler_id = button_handler_id(&initial).expect("could not find the button data-sky-hid");

    const TARGET: i64 = 3;
    for i in 1..=TARGET {
        let status = curl_post_event(server1.port, &jar, &csrf, &handler_id);
        assert_eq!(
            status,
            "200",
            "event #{i} returned HTTP {status}, not 200\nlog:\n{}",
            server1.read_log()
        );
    }
    let on1 = curl_get(server1.port, "/", &jar, true);
    assert_eq!(
        rendered_count(&on1),
        Some(TARGET),
        "process 1 did not reflect its own {TARGET} dispatches:\n{on1}"
    );

    // ── Restart: kill process 1, start a fresh process 2 (cold memory store). ──
    server1.stop();
    // Give the OS a moment to release the port.
    std::thread::sleep(Duration::from_millis(500));
    let server2 = Server::launch(&project, &app_bin, 8822);

    // Process 2 has never seen this sid (fresh memory store). GET / with the
    // SAME cookie must RESTORE the snapshot -> DCOUNT=3, not 0.
    let restored = curl_get(server2.port, "/", &jar, true);
    assert_eq!(
        rendered_count(&restored),
        Some(TARGET),
        "process 2 should RESTORE the durable snapshot for sid {sid} (DCOUNT={TARGET}), \
         not re-init (0); got:\n{restored}\nlog:\n{}",
        server2.read_log()
    );

    // Negative control: a fresh browser (no cookie) starts from init (0), so the
    // assertion above distinguishes restore from "always renders 3".
    let fresh = curl_get_fresh(server2.port, "/");
    assert_eq!(
        rendered_count(&fresh),
        Some(0),
        "a fresh cookie-less session on process 2 must start at 0, not the restored value:\n{fresh}"
    );

    server2.stop();
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_file(&jar);
}

/// A durable Live app whose `App.withRequest` hook copies the `X-Probe` request
/// header into the Model. The snapshot holds the header of the request that
/// was current when it was written; a restore must not bring that stale value
/// back over the header of the request that is current now.
const REQ_APP_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.Dict as Dict
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)
import Std.Db as Db exposing (Db)
import Std.Codec as Codec exposing (Codec)


type alias Model =
    { count : Int, probe : String }


modelCodec : Codec Model
modelCodec =
    Codec.auto { count = 0, probe = "" }


type Msg
    = Increment


init : a -> ( Model, Cmd Msg )
init _ =
    ( { count = 0, probe = "" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )


fromRequest req model =
    ( { model | probe = Maybe.withDefault "none" (Dict.get "X-Probe" req.headers) }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column
        []
        [ Ui.text ("DCOUNT=" ++ String.fromInt model.count)
        , Ui.text ("PROBE=" ++ model.probe ++ ";")
        , Ui.button [] { onPress = Just Increment, label = Ui.text "inc" }
        ]


mkApp db =
    App.app { init = init, update = update, view = view, subscriptions = \_ -> Sub.none }
        |> App.withRequest fromRequest
        |> App.withNotFound ()
        |> App.withDurable db modelCodec


main : Task Error ()
main =
    Db.connect () |> Task.andThen (\db -> App.run (mkApp db))
"#;

fn curl_get_probe(port: u16, jar: &Path, probe: &str) -> String {
    let url = format!("http://127.0.0.1:{port}/");
    let out = Command::new("curl")
        .args([
            "-s",
            "--max-time",
            "30",
            "-b",
            &jar.display().to_string(),
            "-c",
            &jar.display().to_string(),
            "-H",
            &format!("X-Probe: {probe}"),
            &url,
        ])
        .output()
        .expect("run curl GET (probe)");
    String::from_utf8_lossy(&out.stdout).to_string()
}

fn rendered_probe(body: &str) -> Option<String> {
    let idx = body.find("PROBE=")?;
    let rest = &body[idx + "PROBE=".len()..];
    Some(rest[..rest.find(';')?].to_string())
}

#[test]
// Same tier as the restart test above: a real `go build` + two processes.
#[ignore = "heavy HTTP restart e2e leg; runs nightly (--ignored)"]
fn a_durable_restore_keeps_the_fields_with_request_derives_from_the_current_request() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    if !have_curl() {
        panic!("curl is required to drive the Sky.Live HTTP restart flow");
    }
    let project = std::env::temp_dir().join(unique("dlive-req"));
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"dlivereq\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\
         [database]\ndriver = \"sqlite\"\npath = \"dlivereq.db\"\n",
    )
    .unwrap();
    std::fs::write(project.join("src").join("Main.sky"), REQ_APP_SRC).unwrap();
    let build = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&project),
        "sky build src/Main.sky",
    );
    assert!(
        build.status.success(),
        "durable withRequest Live app build failed:\n{}",
        both(&build)
    );
    let app_bin = project.join("sky-out").join("app");

    // Process 1: the request carries X-Probe: first; one update snapshots it.
    let server1 = Server::launch(&project, &app_bin, 9321);
    let jar = std::env::temp_dir().join(unique("dl-req-jar"));
    let initial = curl_get_probe(server1.port, &jar, "first");
    assert_eq!(
        rendered_probe(&initial).as_deref(),
        Some("first"),
        "{initial}"
    );
    let csrf = cookie_from_jar(&jar, "__sky_csrf").expect("process 1 set no __sky_csrf cookie");
    let handler_id = button_handler_id(&initial).expect("could not find the button data-sky-hid");
    let status = curl_post_event(server1.port, &jar, &csrf, &handler_id);
    assert_eq!(
        status,
        "200",
        "event returned HTTP {status}\nlog:\n{}",
        server1.read_log()
    );
    let on1 = curl_get_probe(server1.port, &jar, "first");
    assert_eq!(
        rendered_count(&on1),
        Some(1),
        "process 1 did not apply its own dispatch:\n{on1}"
    );
    // The snapshot write after an update is asynchronous: give it time to
    // land before the process is killed.
    std::thread::sleep(Duration::from_millis(1000));
    server1.stop();
    std::thread::sleep(Duration::from_millis(500));

    // Process 2: the SAME session, a new request with X-Probe: second. The
    // count comes from the snapshot; the probe comes from THIS request.
    let server2 = Server::launch(&project, &app_bin, 9322);
    let restored = curl_get_probe(server2.port, &jar, "second");
    assert_eq!(
        rendered_count(&restored),
        Some(1),
        "the snapshot was not restored:\n{restored}\nlog:\n{}",
        server2.read_log()
    );
    assert_eq!(
        rendered_probe(&restored).as_deref(),
        Some("second"),
        "the durable restore overwrote the field App.withRequest derived from the current \
         request with the snapshot's stale value:\n{restored}"
    );
    server2.stop();
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_file(&jar);
}
