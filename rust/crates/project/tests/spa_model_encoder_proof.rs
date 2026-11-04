//! The real build path must prove the callable, not trust editable [spa] flags.
use std::path::PathBuf;

fn root() -> PathBuf {
    let mut p = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !p.join("sky-stdlib").is_dir() {
        assert!(p.pop());
    }
    p
}

const SOURCE: &str = r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Ffi as Plumbing
import Std.Codec as C
import Std.Spa as S
import Std.Html as H exposing (Html)

type alias Model = { session : Maybe String, text : String }
type Msg = Ignored

blank : Model
blank = { session = Nothing, text = "" }

makeBlank : () -> Model
makeBlank _ = { session = Nothing, text = "" }

encoder : Model -> String
encoder m = C.toJson (C.auto blank) m

custom : Model -> String
custom m = C.toJson (C.auto blank) m

install : (Model -> String) -> S.AppConfig Model Msg -> S.AppConfig Model Msg
install = Plumbing.kernel "Spa_withGeneratedModelEncoder"

init : () -> (Model, Cmd Msg)
init _ = (blank, Cmd.none)

update : Msg -> Model -> (Model, Cmd Msg)
update _ m = (m, Cmd.none)

view : Model -> Html Msg
view m = H.text m.text

main = S.app (S.config { init = init, update = update, view = view, subscriptions = \_ -> Sub.none } |> install encoder)
"#;

#[test]
fn real_build_grants_only_proven_encoders() {
    let dir = std::env::temp_dir().join(format!("sky-model-encoder-proof-{}", std::process::id()));
    struct Cleanup(PathBuf);
    impl Drop for Cleanup {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    let _clean = Cleanup(dir.clone());
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "[project]\nname=\"model-proof\"\n[spa]\ngenerated = true\nrole=\"frontend\"\n",
    )
    .unwrap();
    for (name, source, extra, eligible) in [
        ("canonical", SOURCE.to_string(), 0, true),
        (
            "literal CAF alias",
            SOURCE.replace(
                "blank = { session = Nothing, text = \"\" }",
                "blank = aliasBlank\naliasBlank : Model\naliasBlank = { session = Nothing, text = \"\" }",
            ),
            0,
            true,
        ),
        (
            "CAF alias with a function call",
            SOURCE.replace(
                "blank = { session = Nothing, text = \"\" }",
                "blank = aliasBlank\naliasBlank : Model\naliasBlank = makeBlank ()",
            ),
            0,
            false,
        ),
        (
            "arbitrary callable",
            SOURCE.replace(
                "encoder m = C.toJson (C.auto blank) m",
                "encoder m = custom m",
            ),
            0,
            false,
        ),
        (
            "nonliteral witness",
            SOURCE.replace(
                "blank = { session = Nothing, text = \"\" }",
                "blank = makeBlank ()",
            ),
            0,
            false,
        ),
        ("added namespace", SOURCE.to_string(), 1, false),
        ("shadowed canonical source", SOURCE.to_string(), 2, false),
    ] {
        std::fs::write(dir.join("src/Main.sky"), source).unwrap();
        if dir.join("src/Std").exists() {
            std::fs::remove_dir_all(dir.join("src/Std")).unwrap();
        }
        if extra == 1 {
            std::fs::create_dir_all(dir.join("src/Std")).unwrap();
            std::fs::write(
                dir.join("src/Std/Extra.sky"),
                "module Std.Extra exposing (marker)\nmarker = 0\n",
            )
            .unwrap();
        }
        if extra == 2 {
            std::fs::create_dir_all(dir.join("src/Std/Office")).unwrap();
            std::fs::write(
                dir.join("src/Std/Office/Docx.sky"),
                "module Std.Office.Docx exposing (marker)\nmarker = 0\n",
            )
            .unwrap();
        }
        let result = project::emit_example_source(&root(), &dir);
        if extra == 2 {
            let error = result.expect_err("a canonical stdlib source shadow must be refused");
            assert!(error.contains("standard-library module") && error.contains("replace"));
            continue;
        }
        let emitted = result.unwrap_or_else(|e| panic!("{name}: {e}"));
        assert_eq!(
            emitted.contains("rt.SpaAutoModelEncoder{"),
            eligible,
            "{name}: capsule eligibility differs"
        );
    }
}
