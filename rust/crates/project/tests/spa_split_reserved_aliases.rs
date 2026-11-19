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

/// A codec copied into `Shared` from a sibling module (one with a server
/// effect, so `Shared` copies its codec rather than importing the module)
/// keeps the names that
/// module wrote. `Shared` used to import `Std.Codec as Codec` whatever the
/// app wrote, so a sibling's `C.object` (from `import Std.Codec as C`) named
/// nothing there, and `Codec.` collided with a project module `Codec`.
#[test]
fn a_sibling_codec_copied_into_shared_keeps_its_own_aliases() {
    let dir = fixture("sibling-codec", "import Data exposing (Item, itemCodec)\n");
    write(
        &dir,
        "src/Data.sky",
        "module Data exposing (Item, itemCodec, note)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Codec as C exposing (Codec)\nimport Std.Log as Log\n\n\nnote : String -> Task Error ()\nnote s =\n    Log.println s\n\n\ntype alias Item =\n    { id : Int, label : String }\n\n\nitemCodec : Codec Item\nitemCodec =\n    C.object Item\n        |> C.field \"id\" .id C.int\n        |> C.field \"label\" .label C.string\n        |> C.buildObject\n",
    );
    let main = std::fs::read_to_string(dir.join("src/Main.sky"))
        .unwrap()
        .replace("    {{ status : String }}", "    {{ status : String }}")
        .replace(
            "type alias Model =\n    { status : String }",
            "type alias Model =\n    { status : String, items : List Item }",
        )
        .replace("{ status = Codec.label \"ready\" }", "{ status = Codec.label \"ready\", items = [] }")
        .replace(
            "( { model | status = Server.record model.status }",
            "( { model | status = Server.record model.status, items = { id = 1, label = \"a\" } :: model.items }",
        );
    write(&dir, "src/Main.sky", &main);
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    assert!(
        shared.contains("itemCodec"),
        "the codec is copied:\n{shared}"
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

/// D1 (v0.30.0): a project whose second entry `src/Other.sky` holds the app,
/// next to an unrelated `src/Main.sky` program. The split wrote the generated
/// entry as `module Main` at `src/Main.sky` in both trees, then copied the
/// project's own `Main` over it, so the build silently produced the project
/// `Main` program. The generated entry now keeps the app entry's module name
/// and path, and the project `Main` is an ordinary sibling.
#[test]
fn a_second_entry_splits_beside_a_project_main() {
    let dir = std::env::temp_dir().join(format!("sky-spa-second-entry-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"second\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(
        &dir,
        "src/Main.sky",
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"I AM THE PROJECT MAIN\"\n",
    );
    write(
        &dir,
        "src/Other.sky",
        r#"module Other exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as T
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)


type alias Model =
    { status : String }


type Msg
    = Save
    | Saved (Result Error ())


init : () -> ( Model, Cmd Msg )
init _ =
    ( { status = "other" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( model, Cmd.perform (Log.println "saved") Saved )

        Saved _ ->
            ( { model | status = "OTHER APP" }, Cmd.none )


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
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#,
    );
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, Some("Other"), &out, None, None)
        .unwrap_or_else(|e| panic!("the second entry must split: {e}"));
    for half in ["backend", "frontend"] {
        let toml = std::fs::read_to_string(out.join(half).join("sky.toml")).unwrap();
        assert!(
            toml.contains("entry = \"src/Other.sky\""),
            "the generated {half} names the app entry's path:\n{toml}"
        );
        assert_eq!(spa_split::generated_entry(&out.join(half)), "src/Other.sky");
        let entry = std::fs::read_to_string(out.join(half).join("src/Other.sky")).unwrap();
        assert!(
            entry.starts_with("module Other exposing (main)"),
            "the generated {half} entry keeps the module name `Other`:\n{entry}"
        );
        // The project `Main` is copied only as an ordinary sibling (when the
        // loader includes it), never in place of the generated entry.
        if let Ok(project_main) = std::fs::read_to_string(out.join(half).join("src/Main.sky")) {
            assert!(
                project_main.contains("I AM THE PROJECT MAIN"),
                "src/Main.sky may only be the project `Main`:\n{project_main}"
            );
        }
        let errors = front_half_errors(&root(), &out.join(half), Some("Other"));
        assert!(
            errors.is_empty(),
            "the generated {half} must type-check: {errors:?}\n{}",
            tree_text(&out.join(half).join("src"))
        );
    }
    let back = std::fs::read_to_string(out.join("backend/src/Other.sky")).unwrap();
    assert!(back.contains("/_rpc/Save"), "{back}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// D1 (v0.30.0): a project module named `Shared` would be replaced by the
/// split's generated wire contract `Shared.sky`. It is refused by name.
#[test]
fn a_project_module_named_shared_is_refused() {
    let dir = fixture("shared", "import Shared\n");
    write(
        &dir,
        "src/Shared.sky",
        "module Shared exposing (tag)\n\nimport Sky.Core.Prelude exposing (..)\n\n\ntag : String\ntag =\n    \"t\"\n",
    );
    let out = dir.join("generated");
    let Err(err) = spa_split::generate(&root(), &dir, None, &out, None, None) else {
        panic!("a project module named Shared must be refused");
    };
    assert!(
        err.contains("`Shared`") && err.contains("Rename the module"),
        "the refusal names the module: {err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
