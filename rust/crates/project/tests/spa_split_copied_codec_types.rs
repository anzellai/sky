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
