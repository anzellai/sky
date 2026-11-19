//! The Sky.Spa split refusals of v0.27.0. A server arm the split cannot read
//! still ends with its migration link,
//! `docs/migration/v0.27.md#spa-split-refusals`. The other two (two wire
//! records with one name, one module under several import aliases) were
//! lifted in v0.30.0: `Shared` names every type of another module through a
//! generated alias (`SpaTy_Shape_.Point`) and imports that module once under
//! it, so neither shape is ambiguous any more. Their tests now assert that
//! the split succeeds and names each type by its module.
//!
//! Each case writes a small project to a temp dir and runs the real split
//! generator over it (no Go build).

use project::spa_split;
use std::path::{Path, PathBuf};

const LINK: &str = "docs/migration/v0.27.md#spa-split-refusals";

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

fn project(tag: &str, files: &[(&str, &str)]) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sky-spa-refusal-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"),
    )
    .unwrap();
    for (name, src) in files {
        std::fs::write(dir.join("src").join(name), src).unwrap();
    }
    dir
}

/// Run the split over `dir` and return the generated `Shared` source; the
/// split must succeed.
fn split_shared(dir: &Path, tag: &str) -> String {
    let out =
        std::env::temp_dir().join(format!("sky-spa-refusal-out-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    let r = spa_split::generate(&repo_root(), dir, None, &out, None, None);
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&out);
    let _ = std::fs::remove_dir_all(dir);
    if let Err(e) = r {
        panic!("`{tag}`: the split must succeed, got:\n{e}");
    }
    shared
}

fn refusal(dir: &Path, tag: &str) -> String {
    let out =
        std::env::temp_dir().join(format!("sky-spa-refusal-out-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    let r = spa_split::generate(&repo_root(), dir, None, &out, None, None);
    let _ = std::fs::remove_dir_all(&out);
    let _ = std::fs::remove_dir_all(dir);
    match r {
        Ok(_) => panic!("`{tag}`: the split must refuse this project"),
        Err(e) => e,
    }
}

const SHAPE: &str = "module Shape exposing (Point, origin)\n\nimport Sky.Core.Prelude exposing (..)\n\n\ntype alias Point =\n    { x : Int\n    , y : Int\n    }\n\n\norigin : Point\norigin =\n    { x = 0, y = 0 }\n";

/// A Main whose `Got` Msg carries `Tagged` across the wire. `{decls}` and
/// `{imports}` are spliced in; `{arms}` replaces the `Got` arms.
fn main_src(imports: &str, decls: &str, tagged: &str, build: &str) -> String {
    format!(
        "module Main exposing (main)\n\n{imports}\nimport Sky.Core.Http as Http\nimport Sky.Core.Prelude exposing (..)\nimport Std.App as App\nimport Std.Ui as Ui\n\n\n{decls}\n\ntype alias Tagged =\n    {tagged}\n\n\ntype alias Model =\n    {{ out : String }}\n\n\ntype Msg\n    = Go\n    | Got (Result Error Tagged)\n\n\ninit : () -> ( Model, Cmd Msg )\ninit _ =\n    ( {{ out = \"\" }}, Cmd.none )\n\n\nupdate : Msg -> Model -> ( Model, Cmd Msg )\nupdate msg model =\n    case msg of\n        Go ->\n            ( model\n            , Cmd.perform\n                (Http.get \"http://127.0.0.1:1/\" |> Task.map (\\r -> {build}))\n                Got\n            )\n\n        Got (Ok _) ->\n            ( {{ model | out = \"ok\" }}, Cmd.none )\n\n        Got (Err _) ->\n            ( {{ model | out = \"failed\" }}, Cmd.none )\n\n\nmain =\n    App.run\n        (App.app\n            {{ init = init\n            , update = update\n            , view = \\m -> Ui.column [] [ Ui.text m.out, Ui.button [] {{ onPress = Just Go, label = Ui.text \"go\" }} ]\n            , subscriptions = \\_ -> Sub.none\n            }}\n            |> App.withNotFound ()\n        )\n"
    )
}

const TAINTED_WIRE: &str = "module Wire exposing (Other, fetch)\n\nimport Shape as Sh\nimport Sky.Core.Http as Http\nimport Sky.Core.Prelude exposing (..)\n\n\ntype alias Other =\n    { p : Sh.Point }\n\n\nfetch : String -> Task Error Int\nfetch url =\n    Http.get url |> Task.map (\\r -> r.status)\n";

fn assert_linked(err: &str, names: &[&str]) {
    for n in names {
        assert!(err.contains(n), "the refusal must name `{n}`, got:\n{err}");
    }
    assert!(
        err.trim_end().ends_with(LINK),
        "the refusal must end with `{LINK}`, got:\n{err}"
    );
}

/// Two project records named `Point` cross the wire (the entry's own and
/// `Shape.Point`). Before v0.30.0 the split refused them, because `Shared`
/// named a project type by its bare name. Now the entry's `Point` is copied
/// into `Shared` and named bare, and `Shape.Point` is named through its
/// module, each with its own codec.
#[test]
fn two_wire_records_with_one_name_split_with_one_codec_each() {
    let dir = project(
        "dup",
        &[
            ("Shape.sky", SHAPE),
            ("Main.sky", &DUP_MAIN.replace("WIRE", "Shape")),
        ],
    );
    let shared = split_shared(&dir, "dup");
    for want in [
        "import Shape as SpaTy_Shape_",
        "blankShape_Point_ : SpaTy_Shape_.Point",
        "blankPoint_ : Point",
        "type alias Point =",
    ] {
        assert!(
            shared.contains(want),
            "Shared must hold `{want}`:\n{shared}"
        );
    }
}

/// The wire types name `Shape` as `S` in one module and as `Sh` in another.
/// Before v0.30.0 the split refused this, because it copied `Wire.Other` into
/// `Shared` and had to import `Shape` under the one alias the copy wrote.
/// Now `Shared` imports `Wire` and `Shape` under generated aliases and copies
/// neither type.
#[test]
fn one_module_under_several_aliases_splits_through_generated_aliases() {
    let main = main_src(
        "import Shape as S\nimport Wire",
        "",
        "{ at : S.Point, other : Wire.Other }",
        "{ at = S.origin, other = { p = S.origin } }",
    );
    let dir = project(
        "alias",
        &[
            ("Shape.sky", SHAPE),
            ("Wire.sky", TAINTED_WIRE),
            ("Main.sky", &main),
        ],
    );
    let shared = split_shared(&dir, "alias");
    for want in [
        "import Shape as SpaTy_Shape_",
        "import Wire as SpaTy_Wire_",
        "at : SpaTy_Shape_.Point",
        "other : SpaTy_Wire_.Other",
    ] {
        assert!(
            shared.contains(want),
            "Shared must hold `{want}`:\n{shared}"
        );
    }
    assert!(
        !shared.contains("type alias Other") && !shared.contains("import Shape as S\n"),
        "Shared copies no type of another module and keeps no user alias:\n{shared}"
    );
}

/// A server arm of a polymorphic `Msg a` matches inside its argument, so the
/// split sends the argument whole, but its type is still a variable.
#[test]
fn a_server_arm_the_split_cannot_read_is_refused_with_the_migration_link() {
    let dir = project("arm", &[("Main.sky", POLY_MAIN)]);
    let err = refusal(&dir, "arm");
    assert_linked(
        &err,
        &["the SERVER branch `Save (Just …)`", "could not read"],
    );
}

const DUP_MAIN: &str = "module Main exposing (main)\n\nimport WIRE\nimport Sky.Core.Http as Http\nimport Sky.Core.Prelude exposing (..)\nimport Std.App as App\nimport Std.Ui as Ui\n\n\ntype alias Point =\n    { z : Int }\n\n\ntype alias Model =\n    { out : String }\n\n\ntype Msg\n    = Go\n    | GotMine (Result Error Point)\n    | GotTheirs (Result Error WIRE.Point)\n\n\ninit : () -> ( Model, Cmd Msg )\ninit _ =\n    ( { out = \"\" }, Cmd.none )\n\n\nupdate : Msg -> Model -> ( Model, Cmd Msg )\nupdate msg model =\n    case msg of\n        Go ->\n            ( model\n            , Cmd.batch\n                [ Cmd.perform (Http.get \"http://127.0.0.1:1/\" |> Task.map (\\r -> { z = r.status })) GotMine\n                , Cmd.perform (Http.get \"http://127.0.0.1:1/\" |> Task.map (\\r -> { x = r.status, y = 0 })) GotTheirs\n                ]\n            )\n\n        GotMine _ ->\n            ( { model | out = \"mine\" }, Cmd.none )\n\n        GotTheirs _ ->\n            ( { model | out = \"theirs\" }, Cmd.none )\n\n\nmain =\n    App.run\n        (App.app\n            { init = init\n            , update = update\n            , view = \\m -> Ui.column [] [ Ui.text m.out, Ui.button [] { onPress = Just Go, label = Ui.text \"go\" } ]\n            , subscriptions = \\_ -> Sub.none\n            }\n            |> App.withNotFound ()\n        )\n";

const POLY_MAIN: &str = "module Main exposing (main)\n\nimport Sky.Core.Http as Http\nimport Sky.Core.Prelude exposing (..)\nimport Std.App as App\nimport Std.Ui as Ui\n\n\ntype alias Model =\n    { out : String }\n\n\ntype Msg a\n    = Go\n    | Save (Maybe a)\n    | Saved (Result Error Int)\n\n\ninit : () -> ( Model, Cmd (Msg a) )\ninit _ =\n    ( { out = \"\" }, Cmd.none )\n\n\nupdate : Msg a -> Model -> ( Model, Cmd (Msg a) )\nupdate msg model =\n    case msg of\n        Go ->\n            ( model, Cmd.none )\n\n        Save (Just _) ->\n            ( model, Cmd.perform (Http.get \"http://127.0.0.1:1/\" |> Task.map (\\r -> r.status)) Saved )\n\n        Save Nothing ->\n            ( model, Cmd.none )\n\n        Saved _ ->\n            ( { model | out = \"saved\" }, Cmd.none )\n\n\nmain =\n    App.run\n        (App.app\n            { init = init\n            , update = update\n            , view = \\m -> Ui.column [] [ Ui.text m.out, Ui.button [] { onPress = Just Go, label = Ui.text \"go\" } ]\n            , subscriptions = \\_ -> Sub.none\n            }\n            |> App.withNotFound ()\n        )\n";
