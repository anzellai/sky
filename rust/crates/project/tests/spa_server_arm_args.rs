//! A SERVER branch must bind each argument of its Msg to a plain name.
//!
//! The split sends the names a server arm binds and rebuilds the Msg from them
//! on the backend (`update (Report p.line) m`). An arm that matches inside an
//! argument (`Report (Ok line)`) binds the inner value, so the rebuilt Msg had
//! the wrong type and the backend failed to compile with a type error in
//! generated code (`reportHandler: Result Error String vs String`), reported
//! against the derived project. The split now refuses the arm, naming it and
//! the form that works; the plain-name form splits.

use project::spa_split;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

/// A Sky.Spa app whose `Report` branch writes a log line (a server effect).
/// `report_arms` is the text of the `Report` arm(s).
fn project(tag: &str, report_arms: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sky-spa-armargs-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"armargs\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    let src = format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Task as Task
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)


type alias Model =
    {{ status : String }}


type Msg
    = Send
    | Report (Result Error String)
    | Reported (Result Error ())


init : () -> ( Model, Cmd Msg )
init _ =
    ( {{ status = "ready" }}, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Send ->
            ( model, Cmd.perform (Task.succeed "hello") Report )

{report_arms}
        Reported _ ->
            ( model, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task Error ()
main =
    Spa.app
        (Spa.config
            {{ init = init, update = update, view = view, subscriptions = subscriptions }}
        )
"#
    );
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
    dir
}

fn split(dir: &Path) -> Result<(), String> {
    let out = dir.join(".split");
    spa_split::generate(&repo_root(), dir, None, &out, None, None).map(|_| ())
}

#[test]
fn a_server_arm_that_matches_inside_its_msg_argument_is_refused_by_name() {
    let dir = project(
        "nested",
        "        Report (Ok line) ->\n            ( model, Cmd.perform (Log.println line) Reported )\n\n\
         \x20       Report (Err _) ->\n            ( model, Cmd.none )\n\n",
    );
    let err = split(&dir).expect_err("a nested server-arm pattern must not split");
    assert!(
        err.contains("SERVER branch `Report")
            && err.contains("matches inside")
            && err.contains("Report value ->"),
        "the refusal must name the arm and the form that works; got: {err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_server_arm_that_binds_its_msg_argument_splits() {
    let dir = project(
        "plain",
        "        Report result ->\n            case result of\n                Ok line ->\n                    \
         ( model, Cmd.perform (Log.println line) Reported )\n\n                Err _ ->\n                    \
         ( model, Cmd.none )\n\n",
    );
    split(&dir).unwrap_or_else(|e| panic!("the plain-name form must split: {e}"));
    let _ = std::fs::remove_dir_all(&dir);
}
