//! The Sky.Spa client's first-paint model codec (`spaModelBlank_`) must name
//! nothing the generated entry module cannot see.
//!
//! Before v0.29.9 the split copied the TEXT of `init`'s returned model into the
//! client entry as the codec's blank. The text kept its references but lost
//! their scope: a name bound by a `let` around the returned tuple, a parameter
//! of `init`, and a private top-level binding of the module that declares a
//! sibling `init` all named nothing there. `sky check` passed, and
//! `sky build --target web:app` failed with `Undefined name: seedComments`
//! (`examples/19-skyforum`). The blank now comes from `init ()` itself, which
//! the client tree already holds and which resolves every name in its own
//! scope, so no reference in `init` can leave its scope.

use project::{front_half_errors, spa_split};
use std::path::{Path, PathBuf};

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .unwrap()
        .to_path_buf()
}

fn write(dir: &Path, rel: &str, text: &str) {
    let p = dir.join(rel);
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, text).unwrap();
}

fn tree_text(dir: &Path) -> String {
    let mut out = String::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_some_and(|x| x == "sky") {
                out.push_str(&format!("==== {}\n", p.display()));
                out.push_str(&std::fs::read_to_string(&p).unwrap());
                out.push('\n');
            }
        }
    }
    out
}

const TOML: &str =
    "name = \"blank\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n";

const REST: &str = r#"

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( model, Cmd.perform (Log.println model.status) Saved )

        Saved _ ->
            ( { model | status = "saved" }, Cmd.none )

        Add n ->
            ( { model | items = n :: model.items }, Cmd.none )


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
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#;

const HEADER: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
"#;

const TYPES: &str = r#"

type alias Model =
    { status : String, items : List Int, owner : String }


type Msg
    = Save
    | Saved (Result Error ())
    | Add Int
"#;

fn split_and_check(tag: &str, files: &[(&str, String)]) {
    let dir =
        std::env::temp_dir().join(format!("sky-spa-blank-scope-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    write(&dir, "sky.toml", TOML);
    for (rel, text) in files {
        write(&dir, rel, text);
    }
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("spaModelBlank_ :") && front.contains("SpaLib_.withModelDecoder"),
        "the client must keep its first-paint model codec:\n{front}"
    );
    for half in ["backend", "frontend"] {
        let errors = front_half_errors(&root(), &out.join(half), None);
        assert!(
            errors.is_empty(),
            "the generated {half} must type-check: {errors:?}\n{}",
            tree_text(&out.join(half).join("src"))
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// `init`'s model names `let` bindings (one of them through another) and the
/// parameter of `init`.
#[test]
fn a_model_built_from_let_bindings_and_the_init_argument_splits_and_checks() {
    let main = format!(
        "{HEADER}{TYPES}

init : a -> ( Model, Cmd Msg )
init flags =
    let
        seedItems =
            [ 1, 2, 3 ]

        moreItems =
            0 :: seedItems

        owner =
            ownerOf flags
    in
    ( {{ status = \"ready\", items = moreItems, owner = owner }}, Cmd.none )


ownerOf : a -> String
ownerOf _ =
    \"me\"
{REST}"
    );
    split_and_check("let", &[("src/Main.sky", main)]);
}

/// A sibling module declares `init`, and its model names a private top-level
/// binding of that module and a module the entry does not import.
#[test]
fn a_sibling_init_whose_model_names_private_bindings_splits_and_checks() {
    let state = r#"module State exposing (init)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as L
import Std.Cmd as Cmd
import Types exposing (Model, Msg)


seedItems : List Int
seedItems =
    L.range 1 3


init : () -> ( Model, Cmd Msg )
init _ =
    ( { status = "ready", items = seedItems, owner = "me" }, Cmd.none )
"#
    .to_string();
    let types = format!(
        "module Types exposing (Model, Msg(..))\n\nimport Sky.Core.Prelude exposing (..)\n{TYPES}"
    );
    let main = format!(
        "{HEADER}import Types exposing (Model, Msg(..))\nimport State exposing (init)\n{REST}"
    );
    split_and_check(
        "sibling",
        &[
            ("src/Main.sky", main),
            ("src/State.sky", state),
            ("src/Types.sky", types),
        ],
    );
}
