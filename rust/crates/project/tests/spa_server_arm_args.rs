//! A SERVER branch may match inside the arguments of its Msg.
//!
//! The split sends a server arm's message to the backend, which runs the app's
//! own `update` on it. Before v0.27.0 the split sent the names an arm binds and
//! rebuilt the Msg from them (`update (Report p.line) m`). An arm that matches
//! inside an argument (`Report (Ok line)`) binds the inner value, so the rebuilt
//! Msg had the wrong type and the backend failed to compile with a type error
//! in generated code (`reportHandler: Result Error String vs String`). The
//! first fix refused such an arm. The split now supports it: a constructor
//! whose server arm matches inside an argument, or that has more than one
//! server arm, sends each WHOLE argument under a positional name
//! (`Report ((Ok line) as spaArg0_)` on the client, `update (Report
//! p.spaArg0_) m` on the backend). Each arm keeps its own side: a client arm
//! of the same constructor stays in the client, and the backend takes the arm
//! the client took, because Sky patterns are pure and the arms keep their
//! order. The route's I/O is the union over the constructor's server arms.
//!
//! The build-and-run proof (every pattern shape over RPC against the
//! monolithic Sky.Live build) is `spa_split_flow.rs`
//! (`server_arms_that_match_inside_their_msg_arguments_behave_as_the_live_app`).

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

/// A Sky.Spa app whose `update` arms are `arms` (a server arm writes a log
/// line, a server effect).
fn project(tag: &str, arms: &str) -> PathBuf {
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


type alias Item =
    {{ id : Int, label : String }}


type alias Model =
    {{ status : String }}


type Msg
    = Send
    | Report (Result Error String)
    | Pick Int
    | Named String
    | Pair ( Int, String )
    | Take Item
    | Wrap (Maybe Int)
    | Any (Maybe Int)
    | Reported (Result Error ())


init : () -> ( Model, Cmd Msg )
init _ =
    ( {{ status = "ready" }}, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Send ->
            ( model, Cmd.perform (Task.succeed "hello") Report )

{arms}
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

fn split(dir: &Path) -> Result<PathBuf, String> {
    let out = dir.join(".split");
    spa_split::generate(&repo_root(), dir, None, &out, None, None).map(|_| out)
}

fn read_tree(dir: &Path) -> String {
    let mut out = String::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&d) else {
            continue;
        };
        let mut paths: Vec<PathBuf> = rd.flatten().map(|e| e.path()).collect();
        paths.sort();
        for p in paths {
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_some_and(|e| e == "sky") {
                out.push_str(&std::fs::read_to_string(&p).unwrap_or_default());
                out.push('\n');
            }
        }
    }
    out
}

/// Every pattern shape a server arm can take inside its Msg argument: a nested
/// constructor, Int and String literals, a tuple, a record, an `as` binding
/// and a wildcard, with client and server arms of the same constructor mixed.
const ALL_SHAPES: &str = r#"        Report (Ok line) ->
            ( { model | status = "report " ++ line }, Cmd.perform (Log.println ("ARM report " ++ line)) Reported )

        Report (Err _) ->
            ( { model | status = "report failed" }, Cmd.none )

        Pick 0 ->
            ( { model | status = "pick zero" }, Cmd.perform (Log.println "ARM pick zero") Reported )

        Pick n ->
            ( { model | status = "pick " ++ String.fromInt n }, Cmd.perform (Log.println "ARM pick") Reported )

        Named "admin" ->
            ( { model | status = "named admin" }, Cmd.perform (Log.println "ARM named admin") Reported )

        Named "guest" ->
            ( { model | status = "named guest" }, Cmd.none )

        Named other ->
            ( { model | status = "named " ++ other }, Cmd.perform (Log.println "ARM named") Reported )

        Pair ( 0, s ) ->
            ( { model | status = "pair zero " ++ s }, Cmd.perform (Log.println "ARM pair zero") Reported )

        Pair ( n, _ ) ->
            ( { model | status = "pair " ++ String.fromInt n }, Cmd.none )

        Take { id, label } ->
            ( { model | status = "take " ++ String.fromInt id ++ " " ++ label }, Cmd.perform (Log.println "ARM take") Reported )

        Wrap ((Just n) as whole) ->
            ( { model | status = "wrap " ++ String.fromInt n ++ " " ++ Maybe.withDefault "" (Maybe.map String.fromInt whole) }, Cmd.perform (Log.println "ARM wrap") Reported )

        Wrap Nothing ->
            ( { model | status = "wrap nothing" }, Cmd.none )

        Any _ ->
            ( { model | status = "any" }, Cmd.perform (Log.println "ARM any") Reported )

"#;

#[test]
fn server_arms_that_match_inside_their_msg_arguments_split_positionally() {
    let dir = project("shapes", ALL_SHAPES);
    let out = split(&dir).unwrap_or_else(|e| panic!("every pattern shape must split: {e}"));
    let front = read_tree(&out.join("frontend/src"));
    let back = read_tree(&out.join("backend/src"));
    // A server arm keeps its own pattern and holds each whole argument under a
    // positional name; a client arm of the same constructor stays verbatim.
    for want in [
        "Report ((Ok line) as spaArg0_) ->",
        "Pick (0 as spaArg0_) ->",
        "Pick (n as spaArg0_) ->",
        "Named (\"admin\" as spaArg0_) ->",
        "Named (other as spaArg0_) ->",
        "Pair (( 0, s ) as spaArg0_) ->",
        "Take ({ id, label } as spaArg0_) ->",
        "Wrap (((Just n) as whole) as spaArg0_) ->",
        "Any (_ as spaArg0_) ->",
    ] {
        assert!(
            front.contains(want),
            "the client must route `{want}` with its whole argument:\n{front}"
        );
    }
    for client in [
        "Report (Err _) ->",
        "Named \"guest\" ->",
        "Pair ( n, _ ) ->",
        "Wrap Nothing ->",
    ] {
        assert!(
            front.contains(client),
            "the client arm `{client}` stays in the client:\n{front}"
        );
    }
    // The client arms run locally: `Report (Err _)` sets its status in the
    // client and sends nothing.
    let err_arm = front
        .split("Report (Err _) ->")
        .nth(1)
        .and_then(|r| r.split("\n\n").next())
        .unwrap_or_default();
    assert!(
        err_arm.contains("report failed") && !err_arm.contains("Spa.rpc"),
        "`Report (Err _)` is a client arm: {err_arm}"
    );
    // One route per constructor, rebuilt from the whole argument.
    for ctor in ["Report", "Pick", "Named", "Pair", "Take", "Wrap", "Any"] {
        assert!(
            back.contains(&format!("update ({ctor} p.spaArg0_)")),
            "the backend rebuilds `{ctor}` from its whole argument:\n{back}"
        );
        assert_eq!(
            back.matches(&format!("\"POST /_rpc/{ctor}\"")).count(),
            1,
            "one route for `{ctor}`:\n{back}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// A constructor with a single server arm that binds a plain name keeps the
/// named wire form (`update (Report p.result) m`): the output of an app that
/// split before is unchanged.
#[test]
fn a_server_arm_that_binds_its_msg_argument_keeps_the_named_wire() {
    let dir = project(
        "plain",
        "        Report result ->\n            case result of\n                Ok line ->\n                    \
         ( model, Cmd.perform (Log.println line) Reported )\n\n                Err _ ->\n                    \
         ( model, Cmd.none )\n\n\
         \x20       Pick _ ->\n            ( model, Cmd.none )\n\n\
         \x20       Named _ ->\n            ( model, Cmd.none )\n\n\
         \x20       Pair _ ->\n            ( model, Cmd.none )\n\n\
         \x20       Take _ ->\n            ( model, Cmd.none )\n\n\
         \x20       Wrap _ ->\n            ( model, Cmd.none )\n\n\
         \x20       Any _ ->\n            ( model, Cmd.none )\n\n",
    );
    let out = split(&dir).unwrap_or_else(|e| panic!("the plain-name form must split: {e}"));
    let back = read_tree(&out.join("backend/src"));
    assert!(back.contains("update (Report p.result)"), "{back}");
    assert!(!back.contains("spaArg0_"), "{back}");
    let _ = std::fs::remove_dir_all(&dir);
}
