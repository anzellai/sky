//! A server-returned Cmd must stay in the scope/control flow that created it.
use project::{front_half_errors, spa_split};
use std::path::{Path, PathBuf};

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .unwrap()
        .to_path_buf()
}

fn fixture(tag: &str, source: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-returned-navigation-{tag}-{}",
        std::process::id()
    ));
    assert!(!dir.exists(), "fixture output must be exclusive");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "[project]\nname=\"returned-navigation\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), source).unwrap();
    dir
}

fn original() -> &'static str {
    r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.File as File
import Std.Spa as Spa
import Std.Html as Html
import Std.Nav as Nav

type alias Model = { page : String, error : String, allowed : Bool }
type alias Form = { id : String }
type Msg = Open String | Go

init : () -> ( Model, Cmd Msg )
init _ =
    ( { page = "home", error = "", allowed = True }, Cmd.none )

requireOwner : Model -> (() -> ( Model, Cmd Msg )) -> ( Model, Cmd Msg )
requireOwner model proceed =
    if model.allowed then
        proceed ()
    else
        ( model, Cmd.none )

readForm : String -> Result Error Form
readForm key =
    Task.run (File.readFile ("public-fixture/" ++ key) |> Task.map (\value -> { id = value }))

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Open key ->
            requireOwner model
                (\_ ->
                    case readForm key of
                        Ok f ->
                            ( model, Nav.pushUrl ("/forms/edit/" ++ f.id) )
                        Err _ ->
                            ( { model | error = "failed" }, Cmd.none ))
        Go ->
            ( model, Nav.pushUrl "/about" )

view : Model -> Html.Html Msg
view model =
    Html.div [] [ Html.text model.page ]

subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none

main : Task Error ()
main =
    Spa.app (Spa.config { init = init, update = update, view = view, subscriptions = subscriptions })
"#
}

#[test]
fn server_case_bound_navigation_keeps_its_scope_and_generated_frontend_typechecks() {
    let dir = fixture("case-bound", original());
    let errors = front_half_errors(&root(), &dir, None);
    assert!(
        errors.is_empty(),
        "original legal source rejected: {errors:?}"
    );
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None).unwrap();
    let frontend_errors = front_half_errors(&root(), &out.join("frontend"), None);
    assert!(
        frontend_errors.is_empty(),
        "the split must not lift a server case-bound navigation argument into the client: {frontend_errors:?}"
    );
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn pure_client_navigation_control_still_typechecks_after_split() {
    let source = original().replace("case readForm key of", "case Ok { id = key } of");
    let dir = fixture("pure-control", &source);
    assert!(front_half_errors(&root(), &dir, None).is_empty());
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None).unwrap();
    let errors = front_half_errors(&root(), &out.join("frontend"), None);
    assert!(
        errors.is_empty(),
        "pure client control rejected: {errors:?}"
    );
    std::fs::remove_dir_all(dir).unwrap();
}

fn generated_text(dir: &Path, half: &str) -> String {
    let mut out = String::new();
    let mut stack = vec![dir.join(half).join("src")];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_some_and(|x| x == "sky") {
                out.push_str(&std::fs::read_to_string(&p).unwrap());
            }
        }
    }
    out
}

/// Split `source`, require both halves to type-check, and return the
/// generated frontend and backend source.
fn split_both(tag: &str, source: &str) -> (PathBuf, String, String) {
    let dir = fixture(tag, source);
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "legal source rejected: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None).unwrap();
    for half in ["frontend", "backend"] {
        let errors = front_half_errors(&root(), &out.join(half), None);
        assert!(
            errors.is_empty(),
            "generated {half} does not type-check: {errors:?}"
        );
    }
    let front = generated_text(&out, "frontend");
    let back = generated_text(&out, "backend");
    (dir, front, back)
}

#[test]
fn server_navigation_is_returned_command_data_not_copied_source() {
    let (dir, front, back) = split_both("returned-data", original());
    assert!(
        !front.contains("f.id"),
        "server case-bound value escaped into the client"
    );
    assert!(
        !front.contains("\"/forms/edit/\""),
        "server navigation source must not be copied into the client"
    );
    assert!(
        front.contains("SpaLib_.rpcScrollHold"),
        "a server navigation holds later Msgs"
    );
    assert!(
        back.contains("spaCollectScrolls_ cmd"),
        "backend returns the actual command"
    );
    assert!(
        front.contains("Nav.pushUrl \"/about\""),
        "a pure client navigation stays in the client"
    );
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn helper_nested_and_replace_navigations_return_from_the_server() {
    let source = original()
        .replace(
            "                        Ok f ->\n                            ( model, Nav.pushUrl (\"/forms/edit/\" ++ f.id) )",
            "                        Ok f ->\n                            ( model, Cmd.batch [ editAt f, Cmd.batch [ Nav.replaceUrl (\"/r/\" ++ f.id), Nav.clearFragment ] ] )",
        )
        + "\n\neditAt : Form -> Cmd Msg\neditAt f =\n    if f.id == \"\" then\n        Cmd.none\n    else\n        Nav.pushUrl (\"/forms/edit/\" ++ f.id)\n";
    assert!(source.contains("editAt f"), "fixture premise");
    let (dir, front, back) = split_both("returned-helper", &source);
    assert!(
        !front.contains("\"/r/\" ++ f.id"),
        "server case-bound value escaped into the client"
    );
    assert!(
        !front.contains("[ editAt f,"),
        "server navigation helper call copied into the client"
    );
    assert!(back.contains("spaCollectScrolls_ cmd"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn server_run_init_navigation_returns_with_the_init_answer() {
    // No scroll anywhere and no navigating server `update` arm: only `init`
    // navigates, on the outcome of a direct server kernel call.
    let source = original()
        .replace(
            "    ( { page = \"home\", error = \"\", allowed = True }, Cmd.none )",
            "    ( { page = \"home\", error = \"\", allowed = True }\n    , case Task.run (File.readFile \"start.txt\") of\n        Ok p ->\n            Nav.replaceUrl (\"/start/\" ++ p)\n\n        Err _ ->\n            Cmd.none\n    )",
        )
        .replace(
            "                    case readForm key of",
            "                    case Ok { id = key } of",
        );
    assert!(
        source.contains("File.readFile \"start.txt\""),
        "fixture premise"
    );
    let (dir, front, back) = split_both("returned-init", &source);
    assert!(front.contains("/_rpc/SpaInit_"), "init runs on the server");
    assert!(
        !front.contains("\"/start/\" ++ p") && !front.contains("File.readFile"),
        "init navigation source must not be copied into the client"
    );
    assert!(front.contains("SpaInitRpc_.rpcScrollHold"));
    assert!(back.contains("spaInitScrollResponse_ spaInitCmd_"));
    std::fs::remove_dir_all(dir).unwrap();
}
