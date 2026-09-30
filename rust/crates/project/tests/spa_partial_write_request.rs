//! NEW-1 (v0.27.0): a server branch whose helper writes a field on only SOME
//! paths must send that field in its request. The backend starts from `init`,
//! fills the request fields and answers the write-set; a written field left
//! out of the request came back with `init`'s value on the paths that do not
//! write it (a real app's "Edit" landed on `/`: `page` came back as the home
//! page). The request is `read ∪ (write − always written)`, and "always
//! written" must be what EVERY path writes (an intersection), not what some
//! path writes (the union the write-set uses).

use project::spa_partition;
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no repo root");
    }
    dir
}

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.File as File
import Sky.Core.Task as Task
import Std.Cmd as Cmd
import Std.Html exposing (Html)
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui


type alias Model =
    { page : String
    , slug : String
    , title : String
    }


type Msg
    = Load


init : () -> ( Model, Cmd Msg )
init _ =
    ( { page = "home", slug = "", title = "" }, Cmd.none )


loadPost : Model -> Model
loadPost model =
    case Task.run (File.readFile model.slug) of
        Ok text ->
            { model | title = text }

        Err _ ->
            { model | page = "home" }


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Load ->
            ( loadPost model, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.title)


main : Task Error ()
main =
    Spa.app
        (Spa.config
            { init = init
            , update = update
            , view = view
            , subscriptions = \_ -> Sub.none
            }
        )
"#;

#[test]
fn a_field_written_on_some_paths_rides_the_request() {
    let dir = std::env::temp_dir().join(format!(
        "sky-partial-write-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"partial\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), MAIN).unwrap();
    let report = spa_partition::analyze(&repo_root(), &dir, None).expect("analyze");
    let load = report
        .branches
        .iter()
        .find(|b| b.msg == "Load")
        .expect("a Load branch");
    assert!(load.server, "Load reads a file: {}", load.reason);
    let io = load.io.as_ref().expect("server io");
    assert!(
        io.write_fields.contains(&"page".to_string()),
        "page is written on one path: {io:?}"
    );
    assert!(
        io.request_fields().contains(&"page".to_string()),
        "page is written on one path only, so the client's page must ride the \
         request, or the other path answers init's page: {:?}",
        io.request_fields()
    );
    let _ = std::fs::remove_dir_all(&dir);
}
