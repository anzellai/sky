//! The code the Sky.Spa split generates must not depend on the app's names.
//!
//! Before v0.29.7 the split added framework imports under fixed short aliases
//! (`import Sky.Http.Server as Server`, `Std.Codec as Codec`, `Sky.Core.Error
//! as Error`, `Sky.Core.Task as Task`, …) and its generated code qualified
//! with those names. Two failures had that one root:
//!
//! * a project module (or an import alias) with one of those names collided:
//!   `import Server` (the app's own module) next to the added `import
//!   Sky.Http.Server as Server` left the app's `Server.record` naming nothing,
//!   so `sky check` passed and `sky build --target web:app` failed;
//! * the import was skipped when the app imported the module under another
//!   alias (`import Sky.Core.Task as T`), so the generated `Task.succeed`
//!   named nothing.
//!
//! The split now names every framework module through an alias it reserves
//! (`SpaServer_`, `SpaCodec_`, `SpaTask_`, …) and refuses an app that uses one.

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

/// A Sky.Spa app whose project modules are named `Server`, `Codec` and
/// `Error`, and which imports framework modules under its own aliases.
fn fixture(tag: &str, extra_import: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-spa-reserved-aliases-{tag}-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"aliases\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(
        &dir,
        "src/Server.sky",
        "module Server exposing (record)\n\nimport Sky.Core.Prelude exposing (..)\n\n\nrecord : String -> String\nrecord s =\n    \"saved \" ++ s\n",
    );
    write(
        &dir,
        "src/Codec.sky",
        "module Codec exposing (label)\n\nimport Sky.Core.Prelude exposing (..)\n\n\nlabel : String -> String\nlabel s =\n    \"[\" ++ s ++ \"]\"\n",
    );
    write(
        &dir,
        "src/Error.sky",
        "module Error exposing (describe)\n\nimport Sky.Core.Prelude exposing (..)\n\n\ndescribe : String -> String\ndescribe s =\n    \"error: \" ++ s\n",
    );
    let main = format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as T
import Sky.Http.Server as Http
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
import Server
import Codec
import Error
{extra_import}

type alias Model =
    {{ status : String }}


type Msg
    = Save
    | Saved (Result Error ())
    | Local String


init : () -> ( Model, Cmd Msg )
init _ =
    ( {{ status = Codec.label "ready" }}, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( {{ model | status = Server.record model.status }}, Cmd.perform (Log.println (Error.describe model.status)) Saved )

        Saved _ ->
            ( model, Cmd.perform (T.succeed ()) (\_ -> Local "done") )

        Local s ->
            ( {{ model | status = Codec.label s }}, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : T.Task Error ()
main =
    Spa.app
        (Spa.config
            {{ init = init, update = update, view = view, subscriptions = subscriptions }}
        )
"#
    );
    write(&dir, "src/Main.sky", &main);
    dir
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
                out.push_str(&std::fs::read_to_string(&p).unwrap());
                out.push('\n');
            }
        }
    }
    out
}

#[test]
fn project_modules_named_like_framework_aliases_split_and_typecheck() {
    let dir = fixture("names", "");
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    for half in ["backend", "frontend"] {
        let errors = front_half_errors(&root(), &out.join(half), None);
        assert!(
            errors.is_empty(),
            "the generated {half} must type-check whatever the app names its modules \
             and aliases: {errors:?}\n{}",
            tree_text(&out.join(half).join("src"))
        );
    }
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(back.contains("Server.record model.status"), "{back}");
    assert!(
        back.contains("import Sky.Http.Server as SpaServer_"),
        "{back}"
    );
    for short in [
        "import Sky.Http.Server as Server",
        "import Std.Codec as Codec",
        "import Sky.Core.Error as Error",
        "import Sky.Core.Task as Task",
    ] {
        assert!(
            !back.contains(short),
            "the split must not bind the app's names (`{short}`):\n{back}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn an_app_that_uses_a_reserved_alias_is_refused() {
    let dir = fixture("refused", "import Std.Codec as SpaCodec_\n");
    let out = dir.join("generated");
    let Err(err) = spa_split::generate(&root(), &dir, None, &out, None, None) else {
        panic!("a reserved alias in app code must be refused");
    };
    assert!(
        err.contains("SpaCodec_") && err.contains("reserves"),
        "the refusal names the alias: {err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
