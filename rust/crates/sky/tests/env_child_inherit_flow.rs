//! End-to-end: a Sky program's own defaults do not leak into the Sky programs
//! it starts (v0.27.7).
//!
//! Generated `init()` seeds `<PREFIX>_LIVE_PORT` from sky.toml for EVERY
//! program (`rt.SetPortDefault`). It used `os.Setenv`, so a child process
//! inherited the parent's port, and a child Sky program ranked that inherited
//! value as an OPERATOR override (`configLayers`: operator env > builder >
//! seeded default): its own `[live] port` and its own builder port both lost,
//! and it bound the PARENT's port. The program's own values now live in an
//! in-process table (`runtime-go/rt/procenv`) that children cannot see; what
//! the operator set is still in the environment and still inherited.
//!
//! The parent is a Task program with `[live] port = <P_parent>`. It runs the
//! child with `Process.run`. The child has `[live] port = <P_toml>`; it serves
//! one app on the sky.toml port (`port = -1`) and one with a builder port
//! (`port = <P_builder>`), and prints the port each one bound:
//!
//!   leg 1, no operator env:          CHILD_TOML <P_toml>, CHILD_BUILDER <P_builder>
//!   leg 2, operator SKY_LIVE_PORT=X: CHILD_TOML X,        CHILD_BUILDER X
//!
//! The parent also prints the value IT sees (`PARENT_SEES <P_parent>` in leg 1),
//! so the seed still works inside the program that made it.
//!
//! Needs a `go` toolchain. The Go-level leg
//! (runtime-go/rt/env_child_inherit_test.go) runs per commit.

use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const LIMIT: Duration = Duration::from_secs(420);

const CHILD: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.App as App exposing (Config(..), webDefaults)
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)

type alias Model =
    { count : Int }

type Msg
    = Noop

init : a -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ model =
    ( model, Cmd.none )

view : Model -> Element Msg
view _ =
    Ui.text "child"

appOn port =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        }
        |> App.withNotFound ()
        |> App.withConfig (WebConfig { webDefaults | port = port })

portOf : App.Running -> String
portOf running =
    App.address running |> String.split ":" |> List.reverse |> List.head
        |> Maybe.withDefault "?"

serveAndReport : String -> Int -> Task Error ()
serveAndReport label port =
    App.serve (appOn port)
        |> Task.andThen
               (\running ->
                   Log.println (label ++ " " ++ portOf running)
                       |> Task.andThen (\_ -> App.stop running))

main : Task Error ()
main =
    serveAndReport "CHILD_TOML" (-1)
        |> Task.andThen (\_ -> serveAndReport "CHILD_BUILDER" __BUILDER_PORT__)
        |> Task.andThen (\_ -> Log.println "CHILD_DONE")
"#;

const PARENT: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Process as Process
import Sky.Core.System as System
import Sky.Core.Task as Task
import Std.Log as Log

main : Task Error ()
main =
    Log.println ("PARENT_SEES " ++ System.getenvOr "SKY_LIVE_PORT" "none")
        |> Task.andThen (\_ -> Process.run (System.getenvOr "CHILD_BIN" "") [])
        |> Task.andThen (\out -> Log.println out)
        |> Task.andThen (\_ -> Log.println "PARENT_DONE")
"#;

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn unique(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

/// A port nothing listens on right now: bind port 0, read it, release it.
fn free_port() -> u16 {
    let l = std::net::TcpListener::bind("127.0.0.1:0").expect("bind 127.0.0.1:0");
    l.local_addr().unwrap().port()
}

fn project(tag: &str, port: u16, src: &str) -> PathBuf {
    let dir = unique(tag);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!(
            "name = \"{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
             [source]\nroot = \"src\"\n\n[live]\nport = {port}\n"
        ),
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), src).unwrap();
    dir
}

/// Run `cmd` to completion under a deadline; stdout and stderr go to files so
/// a chatty child cannot block on a full pipe.
fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = unique("eci-out");
    let err_path = unique("eci-err");
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
        .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
        .spawn()
        .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + LIMIT;
    let status = loop {
        match child.try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                panic!("`{what}` did not finish within {}s", LIMIT.as_secs());
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

/// The built binary: a `Std.App` entry builds under `.skyapp/<target>/`.
fn app_binary(project: &Path) -> PathBuf {
    let std_app = project.join(".skyapp/web/sky-out/app");
    if std_app.is_file() {
        return std_app;
    }
    project.join("sky-out/app")
}

fn build(dir: &Path, what: &str) -> PathBuf {
    let out = run_bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(dir),
        what,
    );
    assert!(out.status.success(), "{what} failed:\n{}", both(&out));
    let bin = app_binary(dir);
    assert!(
        bin.is_file(),
        "no binary at {}\n{}",
        bin.display(),
        both(&out)
    );
    bin
}

/// The parent's environment: none of the Sky settings an outer shell might
/// carry, and the child binary's path.
fn parent_cmd(parent_bin: &Path, parent_dir: &Path, child_bin: &Path) -> Command {
    let mut cmd = Command::new(parent_bin);
    cmd.current_dir(parent_dir)
        .env("CHILD_BIN", child_bin)
        .env_remove("SKY_LIVE_PORT")
        .env_remove("SKY_LIVE_STORE")
        .env_remove("SKY_LIVE_STORE_PATH")
        .env_remove("SKY_LIVE_SESSION_TRANSPORT")
        .env_remove("SKY_ENV")
        .env_remove("ENV")
        .env_remove("SKY_HOST");
    cmd
}

#[test]
#[ignore = "heavy e2e leg: two go builds and two served apps; runs nightly and in the release suite (--ignored). Per-commit leg: runtime-go/rt/env_child_inherit_test.go"]
fn a_child_sky_program_keeps_its_own_port_and_inherits_the_operators() {
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let p_parent = free_port();
    let p_toml = free_port();
    let p_builder = free_port();
    let p_operator = free_port();
    assert!(
        p_parent != p_toml && p_toml != p_builder && p_builder != p_operator,
        "free_port returned a duplicate: {p_parent} {p_toml} {p_builder} {p_operator}"
    );

    let child_dir = project(
        "eci-child",
        p_toml,
        &CHILD.replace("__BUILDER_PORT__", &p_builder.to_string()),
    );
    let parent_dir = project("eci-parent", p_parent, PARENT);
    let child_bin = build(&child_dir, "sky build (child)");
    let parent_bin = build(&parent_dir, "sky build (parent)");

    // Leg 1: no operator env. The parent sees its own seeded port; the child
    // binds its sky.toml port and its builder port, not the parent's.
    let run = run_bounded(
        &mut parent_cmd(&parent_bin, &parent_dir, &child_bin),
        "parent (no operator env)",
    );
    let out = both(&run);
    assert!(run.status.success(), "the parent failed:\n{out}");
    for want in [
        format!("PARENT_SEES {p_parent}"),
        format!("CHILD_TOML {p_toml}"),
        format!("CHILD_BUILDER {p_builder}"),
        "CHILD_DONE".to_string(),
        "PARENT_DONE".to_string(),
    ] {
        assert!(
            out.lines().any(|l| l.trim() == want),
            "missing {want:?} (the parent's seeded port {p_parent} must not reach the child):\n{out}"
        );
    }

    // Leg 2: the operator sets SKY_LIVE_PORT for the parent. Operator intent is
    // inherited: it beats the child's sky.toml AND its builder.
    let run = run_bounded(
        parent_cmd(&parent_bin, &parent_dir, &child_bin)
            .env("SKY_LIVE_PORT", p_operator.to_string()),
        "parent (operator SKY_LIVE_PORT)",
    );
    let out = both(&run);
    assert!(run.status.success(), "the parent failed:\n{out}");
    for want in [
        format!("PARENT_SEES {p_operator}"),
        format!("CHILD_TOML {p_operator}"),
        format!("CHILD_BUILDER {p_operator}"),
        "CHILD_DONE".to_string(),
    ] {
        assert!(
            out.lines().any(|l| l.trim() == want),
            "missing {want:?} (the operator's SKY_LIVE_PORT must reach the child):\n{out}"
        );
    }

    let _ = std::fs::remove_dir_all(&child_dir);
    let _ = std::fs::remove_dir_all(&parent_dir);
}
