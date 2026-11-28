//! A codec `Shared` copies from a sibling names types of another module that
//! `Shared` also copies from. Each such type must be copied too.
//!
//! The split copied a module's types by a closure over that module's own
//! copied declarations only. `Data.sessionCodec : Codec Session` (copied,
//! since `Data` runs a server effect) names `Types.Session`, and `Types` is a
//! module `Shared` cannot import (it holds `Msg`), so `Shared` copies from it.
//! The rewrite wrote `Session` bare, as a type `Shared` declares, but nothing
//! put `Session` in `Types`' copy set. Up to v0.32.0 the bare name still
//! resolved through the program-wide alias table. In v0.32.1 a bare kernel
//! type name (`Session`, `Request`, `Store`, …) is the kernel type in every
//! module, so the generated backend failed to type-check with
//! `Session vs record`, and every other such name (`Product`, `Order`) only
//! resolved by that lenient fallback.

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

const TYPES: &str = r#"module Types exposing (..)

import Sky.Core.Prelude exposing (..)


type alias Item =
    { id : Int, label : String }


type alias Session =
    { userId : String, email : String }


type alias Product =
    { slug : String, price : Int }


type alias Model =
    { status : String
    , items : List Item
    , session : Maybe Session
    , products : List Product
    }


type Msg
    = Save
    | Saved (Result Error ())
    | Local String
"#;

const DATA: &str = r#"module Data exposing (note, productCodec, sessionCodec)

import Sky.Core.Prelude exposing (..)
import Std.Codec as Codec exposing (Codec)
import Std.Log as Log
import Types exposing (..)


note : String -> Task Error ()
note s =
    Log.println s


sessionCodec : Codec Session
sessionCodec =
    Codec.auto { userId = "", email = "" }


productCodec : Codec Product
productCodec =
    Codec.auto { slug = "", price = 0 }
"#;

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Cmd as Cmd
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
import Types exposing (..)
import Data


init : () -> ( Model, Cmd Msg )
init _ =
    ( { status = "ready", items = [], session = Nothing, products = [] }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( { model | items = { id = 1, label = "a" } :: model.items, session = Just { userId = "u", email = "e" }, products = { slug = "s", price = 1 } :: model.products }
            , Cmd.perform (Data.note model.status) Saved
            )

        Saved _ ->
            ( model, Cmd.none )

        Local s ->
            ( { model | status = s }, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task.Task Error ()
main =
    Spa.app
        (Spa.config
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#;

#[test]
fn types_named_by_a_copied_sibling_codec_are_copied_into_shared() {
    let dir =
        std::env::temp_dir().join(format!("sky-spa-copied-codec-types-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"copied\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(&dir, "src/Types.sky", TYPES);
    write(&dir, "src/Data.sky", DATA);
    write(&dir, "src/Main.sky", MAIN);
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    for codec in ["sessionCodec", "productCodec"] {
        assert!(shared.contains(codec), "`{codec}` is copied:\n{shared}");
    }
    for ty in ["Session", "Product"] {
        assert!(
            shared.contains(&format!("type alias {ty} =")),
            "a type a copied codec names is copied with it (`{ty}`):\n{shared}"
        );
    }
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

/// Split `src` (the three modules, with `Data` given) and type-check both
/// generated halves.
fn split_and_check(tag: &str, types: &str, data: &str, extra: &[(&str, &str)]) -> String {
    let dir =
        std::env::temp_dir().join(format!("sky-spa-copied-codec-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"copied\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(&dir, "src/Types.sky", types);
    write(&dir, "src/Data.sky", data);
    write(&dir, "src/Main.sky", SESSION_MAIN);
    for (p, t) in extra {
        write(&dir, p, t);
    }
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    for half in ["backend", "frontend"] {
        let errors = front_half_errors(&root(), &out.join(half), None);
        assert!(
            errors.is_empty(),
            "the generated {half} must type-check: {errors:?}\n{}",
            tree_text(&out.join(half).join("src"))
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
    shared
}

const SESSION_TYPES: &str = r#"module Types exposing (..)

import Sky.Core.Prelude exposing (..)


type Role
    = Admin
    | Guest


type alias Session =
    { userId : String, email : String }


type alias Model =
    { status : String
    , session : Maybe Session
    }


type Msg
    = Save
    | Saved (Result Error ())
    | Local String
"#;

const DATA_HEAD: &str = r#"module Data exposing (note, sessionCodec)

import Sky.Core.Prelude exposing (..)
import Std.Codec as Codec exposing (Codec)
import Std.Log as Log
import Types exposing (..)
"#;

const DATA_NOTE: &str = r#"

note : String -> Task Error ()
note s =
    Log.println s
"#;

const SESSION_MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Cmd as Cmd
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
import Types exposing (..)
import Data


init : () -> ( Model, Cmd Msg )
init _ =
    ( { status = "ready", session = Nothing }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( { model | session = Just { userId = "u", email = "e" } }
            , Cmd.perform (Data.note model.status) Saved
            )

        Saved _ ->
            ( model, Cmd.none )

        Local s ->
            ( { model | status = s }, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task.Task Error ()
main =
    Spa.app
        (Spa.config
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#;

/// A copied codec names a constructor (`Admin`) of a union no copied type
/// names. The constructor is written bare, so its union is copied too.
#[test]
fn a_constructor_named_by_a_copied_codec_copies_its_union() {
    let data = format!(
        "{DATA_HEAD}{DATA_NOTE}\n\nsessionCodec : Codec Session\nsessionCodec =\n    Codec.auto {{ userId = (if Admin == Guest then \"x\" else \"\"), email = \"\" }}\n"
    );
    let shared = split_and_check("ctor", SESSION_TYPES, &data, &[]);
    assert!(
        shared.contains("type Role"),
        "the union is copied:\n{shared}"
    );
}

/// A copied codec names a value of another project module by its qualifier
/// (`Codec.auto Helpers.blankSession`). The rewrite to the generated alias
/// keeps the space before it.
#[test]
fn a_qualified_value_in_a_copied_codec_keeps_its_spacing() {
    let helpers = "module Helpers exposing (blankSession)\n\nimport Sky.Core.Prelude exposing (..)\n\n\nblankSession : { userId : String, email : String }\nblankSession =\n    { userId = \"\", email = \"\" }\n";
    let data = format!(
        "{DATA_HEAD}import Helpers\n{DATA_NOTE}\n\nsessionCodec : Codec Session\nsessionCodec =\n    Codec.auto Helpers.blankSession\n"
    );
    let shared = split_and_check(
        "qualified",
        SESSION_TYPES,
        &data,
        &[("src/Helpers.sky", helpers)],
    );
    assert!(
        !shared.contains("autoSpaTy_"),
        "the rewrite keeps the space:\n{shared}"
    );
}

/// The same codec, with a `Helpers` that imports `Types` (which imports
/// `Shared`): `Shared` cannot import `Helpers`, and the split refuses with a
/// message that names the cycle, never generated code that does not compile.
#[test]
fn a_qualified_value_on_an_import_cycle_is_refused_by_name() {
    let dir =
        std::env::temp_dir().join(format!("sky-spa-copied-codec-cycle-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"copied\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(&dir, "src/Types.sky", SESSION_TYPES);
    write(
        &dir,
        "src/Helpers.sky",
        "module Helpers exposing (blankSession)\n\nimport Sky.Core.Prelude exposing (..)\nimport Types exposing (Session)\n\n\nblankSession : Session\nblankSession =\n    { userId = \"\", email = \"\" }\n",
    );
    write(
        &dir,
        "src/Data.sky",
        &format!(
            "{DATA_HEAD}import Helpers\n{DATA_NOTE}\n\nsessionCodec : Codec Session\nsessionCodec =\n    Codec.auto Helpers.blankSession\n"
        ),
    );
    write(&dir, "src/Main.sky", SESSION_MAIN);
    let out = dir.join("generated");
    let Err(err) = spa_split::generate(&root(), &dir, None, &out, None, None) else {
        panic!("an import cycle through Shared must be refused");
    };
    assert!(
        err.contains("Helpers -> Types -> Shared") && err.contains("cycle"),
        "the refusal names the cycle: {err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A union `Shared` takes over (its constructor is named by a copied codec)
/// leaves its home module. A module that imported it from there by name
/// (`Types exposing (Session, Msg, Role(..))`) or named it qualified
/// (`Types.Admin`) is pointed at `Shared`.
#[test]
fn a_moved_union_imported_by_name_or_qualified_points_at_shared() {
    let head = DATA_HEAD.replace(
        "import Types exposing (..)\n",
        "import Types exposing (Session, Msg, Role(..))\nimport Types\n",
    );
    let data = format!(
        "{head}{DATA_NOTE}\n\nsessionCodec : Codec Session\nsessionCodec =\n    Codec.auto {{ userId = (if Types.Admin == Guest then \"x\" else \"\"), email = \"\" }}\n"
    );
    let shared = split_and_check("moved-named", SESSION_TYPES, &data, &[]);
    assert!(
        shared.contains("type Role"),
        "the union is copied:\n{shared}"
    );
}

/// A wire union with its own codec (`roleCodec : Codec Role`) moves to
/// `Shared` the same way, and its home module's `exposing (Role(..))`
/// importers are pointed at `Shared`.
#[test]
fn a_wire_union_with_its_own_codec_points_its_importers_at_shared() {
    let types = SESSION_TYPES.replace(
        "    , session : Maybe Session\n    }",
        "    , session : Maybe Session\n    , role : Role\n    }",
    );
    let head = DATA_HEAD
        .replace(
            "module Data exposing (note, sessionCodec)",
            "module Data exposing (note, roleCodec, sessionCodec)",
        )
        .replace(
            "import Types exposing (..)\n",
            "import Types exposing (Session, Msg, Role(..))\n",
        );
    let data = format!(
        "{head}{DATA_NOTE}\n\nsessionCodec : Codec Session\nsessionCodec =\n    Codec.auto {{ userId = \"\", email = \"\" }}\n\n\nroleCodec : Codec Role\nroleCodec =\n    Codec.enum [ ( Admin, \"admin\" ), ( Guest, \"guest\" ) ]\n"
    );
    let dir = std::env::temp_dir().join(format!(
        "sky-spa-copied-codec-wire-union-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    write(
        &dir,
        "sky.toml",
        "name = \"copied\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    );
    write(&dir, "src/Types.sky", &types);
    write(&dir, "src/Data.sky", &data);
    let main = SESSION_MAIN
        .replace(
            "{ status = \"ready\", session = Nothing }",
            "{ status = \"ready\", session = Nothing, role = Guest }",
        )
        .replace(
            "{ model | session = Just { userId = \"u\", email = \"e\" } }",
            "{ model | session = Just { userId = \"u\", email = \"e\" }, role = Admin }",
        );
    write(&dir, "src/Main.sky", &main);
    let errors = front_half_errors(&root(), &dir, None);
    assert!(errors.is_empty(), "the app itself must check: {errors:?}");
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the app must split: {e}"));
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    assert!(
        shared.contains("type Role") && shared.contains("roleCodec"),
        "the wire union and its codec move to Shared:\n{shared}"
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
