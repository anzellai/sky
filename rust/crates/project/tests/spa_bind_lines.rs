//! Bind lines (`p <- e`) in a Sky.Spa `update` (docs/design/let-bind-lines.md
//! §5, §6).
//!
//! The partition reads each body as `ty::TyDb::elaborated_body_of` gives it:
//! a bind line's synthesised `andThen` call carries the `andThen` the type
//! checker chose. So an arm whose command is written with bind lines must get
//! the SAME partition plan as the two hand-written shapes, `e |> Task.andThen
//! (\p -> …)` and `Task.andThen (\p -> …) e`: the same server / client
//! verdicts, the same effect families, the same read and write sets, the same
//! chains. A Db call must stay on the server, and the model write of the
//! result arm must stay on the client.

use project::spa_partition;
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

/// The task the `Save` arm performs, in one of three shapes.
const BIND: &str = r#"saveTask : String -> Task Error Int
saveTask note =
    let
        db <- Db.connect ()
        rows <- Db.query db "SELECT id FROM notes WHERE body = ?" [ note ]
        n = List.length rows
        _ <- Db.exec db "INSERT INTO notes (body) VALUES (?)" [ note ]
    in
    Task.succeed n
"#;

const PIPED: &str = r#"saveTask : String -> Task Error Int
saveTask note =
    Db.connect ()
        |> Task.andThen
            (\db ->
                Db.query db "SELECT id FROM notes WHERE body = ?" [ note ]
                    |> Task.andThen
                        (\rows ->
                            let
                                n = List.length rows
                            in
                            Db.exec db "INSERT INTO notes (body) VALUES (?)" [ note ]
                                |> Task.andThen (\_ -> Task.succeed n)
                        )
            )
"#;

const PREFIX: &str = r#"saveTask : String -> Task Error Int
saveTask note =
    Task.andThen
        (\db ->
            Task.andThen
                (\rows ->
                    let
                        n = List.length rows
                    in
                    Task.andThen (\_ -> Task.succeed n)
                        (Db.exec db "INSERT INTO notes (body) VALUES (?)" [ note ])
                )
                (Db.query db "SELECT id FROM notes WHERE body = ?" [ note ])
        )
        (Db.connect ())
"#;

/// The same arm with an inline bind block in its command.
const INLINE_BIND: &str = r#"        Touch ->
            ( model
            , Cmd.perform
                (let
                    db <- Db.connect ()
                    n <- Db.exec db "UPDATE notes SET seen = 1" []
                 in
                 Task.succeed n
                )
                Saved
            )
"#;

const INLINE_PIPED: &str = r#"        Touch ->
            ( model
            , Cmd.perform
                (Db.connect ()
                    |> Task.andThen (\db -> Db.exec db "UPDATE notes SET seen = 1" [])
                    |> Task.andThen (\n -> Task.succeed n)
                )
                Saved
            )
"#;

fn main_sky(save_task: &str, touch_arm: &str) -> String {
    format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.Db as Db
import Std.Spa as Spa
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)

type alias Model =
    {{ note : String
    , count : Int
    }}

type Msg
    = Edit String
    | Save
    | Saved (Result Error Int)
    | Touch

init : () -> ( Model, Cmd Msg )
init _ =
    ( {{ note = "", count = 0 }}, Cmd.none )

{save_task}
update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Edit s ->
            ( {{ model | note = s }}, Cmd.none )

        Save ->
            ( model, Cmd.perform (saveTask model.note) Saved )

        Saved (Ok n) ->
            ( {{ model | count = n }}, Cmd.none )

        Saved (Err _) ->
            ( {{ model | count = 0 }}, Cmd.none )

{touch_arm}

view : Model -> Html Msg
view model =
    Ui.layout
        []
        (Ui.column
            []
            [ Ui.text ("count=" ++ String.fromInt model.count)
            , Ui.el [ Ui.onClick Save ] (Ui.text "Save")
            , Ui.el [ Ui.onClick Touch ] (Ui.text "Touch")
            ])

subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none

main : Task Error ()
main =
    Spa.app
        (Spa.config
            {{ init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            }})
"#
    )
}

fn project(tag: &str, save_task: &str, touch_arm: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sky-spa-bind-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"spa-bind-{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), main_sky(save_task, touch_arm)).unwrap();
    dir
}

fn analyze(dir: &Path) -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), dir, None)
        .unwrap_or_else(|e| panic!("analyze {} failed: {e}", dir.display()))
}

/// Everything the plan decides, without the project name and paths.
fn plan(r: &spa_partition::SpaPartitionReport) -> String {
    let branches: Vec<String> = r
        .branches
        .iter()
        .map(|b| {
            format!(
                "{} server={} families={:?} io={:?}",
                b.msg, b.server, b.effect_families, b.io
            )
        })
        .collect();
    format!(
        "{}\ninternal={:?}\nchaining={:?}\nclient_result={:?}\ntainted={}",
        branches.join("\n"),
        r.server_internal,
        r.chaining_branches,
        r.client_result,
        r.tainted.len()
    )
}

fn server_of(r: &spa_partition::SpaPartitionReport, msg: &str) -> bool {
    r.branches
        .iter()
        .find(|b| b.msg == msg)
        .unwrap_or_else(|| panic!("no `{msg}` branch"))
        .server
}

#[test]
fn bind_lines_give_the_same_partition_plan_as_hand_written_and_then() {
    let bind = analyze(&project("bind", BIND, INLINE_BIND));
    let piped = analyze(&project("piped", PIPED, INLINE_PIPED));
    let prefix = analyze(&project("prefix", PREFIX, INLINE_PIPED));

    // The Db calls run on the server, the result arm writes the model on the
    // client, and editing the note never leaves the client.
    assert!(server_of(&bind, "Save"), "{}", plan(&bind));
    assert!(server_of(&bind, "Touch"), "{}", plan(&bind));
    assert!(!server_of(&bind, "Saved (Ok …)"), "{}", plan(&bind));
    assert!(!server_of(&bind, "Edit _"), "{}", plan(&bind));
    let save = bind.branches.iter().find(|b| b.msg == "Save").unwrap();
    assert!(
        save.effect_families.iter().any(|f| f == "Db"),
        "the bind block reaches Std.Db through its chosen `Task.andThen`: {:?}",
        save.effect_families
    );

    assert_eq!(
        plan(&bind),
        plan(&piped),
        "bind lines vs `e |> Task.andThen`"
    );
    assert_eq!(
        plan(&bind),
        plan(&prefix),
        "bind lines vs `Task.andThen f e`"
    );
}
