//! Regression: a record value passed to `Just` / `Ok` / `Err` at a slot of a
//! SAME-SHAPE record alias from another module builds (v0.28.0).
//!
//! Sky record aliases are structural: `Who.Who` and `Main.Ident`, both
//! `{ email : String, subject : String }`, are one Sky type. The lowering can
//! carry them as two Go structs: a sealed variant reads its payload by the
//! alias NAME (`Who_Who_R`), while the `Model` field `Maybe Ident` resolves
//! its expanded record STRUCTURALLY to the first alias with that shape
//! (`Main_Ident_R`). `ctor_emit` narrowed a builtin constructor's argument
//! only when it held `any`, so `rt.Just[Main_Ident_R](v)` with
//! `v : Who_Who_R` failed `go build`. The bundled console hit it
//! (`State.Identity` against `Std.Live.Console.Identity`) once its `Msg`
//! stayed sealed. Two different record nominals now take the R1 narrowing,
//! as a user constructor or function argument already does.
//!
//! The test needs a Go toolchain (`sky build` runs `go build`) and fails,
//! never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Maybe as Maybe exposing (Maybe(..))
import Std.Log exposing (println)
import Who exposing (Who)


type alias Ident =
    { email : String, subject : String }


type alias Model =
    { who : Maybe Ident, seen : Maybe Ident, n : Int }


type Msg
    = Got (Result Error Who)
    | Seen (Result Error Ident)
    | Bump


update : Msg -> Model -> Model
update msg m =
    case msg of
        Got (Ok w) ->
            { m | who = Just w }

        Got (Err _) ->
            m

        Seen (Ok i) ->
            { m | seen = Just i }

        Seen (Err _) ->
            m

        Bump ->
            { m | n = m.n + 1 }


show : Maybe Ident -> String
show mi =
    case mi of
        Just i ->
            Who.render i

        Nothing ->
            "none"


main =
    let
        m0 =
            { who = Nothing, seen = Nothing, n = 0 }

        m1 =
            update (Got (Ok { email = "a@b", subject = "ada" })) m0

        m2 =
            update (Seen (Ok { email = "c@d", subject = "bob" })) m1
    in
    println (show m2.who ++ " " ++ show m2.seen)
"#;

const WHO: &str = r#"module Who exposing (Ident, Who, render)

import Sky.Core.Prelude exposing (..)


type alias Who =
    { email : String, subject : String }


type alias Ident =
    { email : String, subject : String }


render : Who -> String
render w =
    w.subject ++ " <" ++ w.email ++ ">"
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-samealias-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"samealias\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), MAIN).unwrap();
    std::fs::write(dir.join("src").join("Who.sky"), WHO).unwrap();
    dir
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn build(dir: &Path) -> String {
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    s
}

#[test]
fn a_same_shape_record_alias_from_another_module_passes_through_just() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch();
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(
        bin.is_file(),
        "a well-typed program must pass go build (log:\n{log})"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(
        combined.contains("ada <a@b> bob <c@d>"),
        "output:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
