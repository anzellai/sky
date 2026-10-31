//! Regression: a builtin container constructor (`Just` / `Ok` / `Err`) whose
//! payload slot is concrete narrows an argument whose Go type is an ERASED
//! instance of that payload (v0.28.0).
//!
//! `Result.andThen` / `Result.map` are polymorphic Sky defs, so their Go
//! signature is erased (doc 14 origin R12) and a call returns
//! `rt.SkyResult[any, any]`. Fed to `Just` at a `Maybe (Result Error Rec)`
//! slot, `ctor_emit` (lower.rs) instantiated `rt.Just[rt.SkyResult[
//! Sky_Core_Error_Error, Main_Rec_R]]` but narrowed the argument only when its
//! Go type was exactly `any`, so the erased `SkyResult[any, any]` went in
//! unconverted and `go build` rejected the program `sky check` accepted:
//!
//! ```text
//! cannot use Sky_Core_Result_andThen(...) (value of struct type
//! rt.SkyResult[any, any]) as rt.SkyResult[Sky_Core_Error_Error, Main_Rec_R]
//! value in argument to rt.Just[...]
//! ```
//!
//! A user constructor or a user function at the same slot already narrowed it
//! (the R1 `coerce_if_needed` fall-through). The builtin constructors now take
//! the same R1 narrowing whenever the argument's Go type erases a position the
//! payload type fixes.
//!
//! Covered: `|> Just`, `Just (…)`, `Just <| …`, a nested `Just (Just …)`, a
//! `Maybe` payload, an `Ok` payload and an `Err` payload, each with the
//! erased result of `Result.andThen` or `Result.map`.
//!
//! The test needs a Go toolchain (`sky build` runs `go build`) and fails,
//! never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Result as Result
import Sky.Core.Maybe as Maybe exposing (Maybe(..))
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Json.Decode as JsonD
import Std.Log exposing (println)


type alias Rec =
    { n : Int }


raw : String -> Result Error Int
raw text =
    JsonD.decodeString (JsonD.field "n" JsonD.int) text


viaPipe : String -> Maybe (Result Error Rec)
viaPipe text =
    raw text
        |> Result.andThen (\v -> Ok { n = v })
        |> Just


viaParens : String -> Maybe (Result Error Rec)
viaParens text =
    Just (Result.andThen (\v -> Ok { n = v }) (raw text))


viaBackPipe : String -> Maybe (Result Error Rec)
viaBackPipe text =
    Just <| Result.map (\v -> { n = v }) (raw text)


nested : String -> Maybe (Maybe (Result Error Rec))
nested text =
    Just (Just (Result.andThen (\v -> Ok { n = v + 1 }) (raw text)))


maybePayload : String -> Maybe (Maybe Rec)
maybePayload text =
    Just (Maybe.map (\v -> { n = v + 2 }) (Result.toMaybe (raw text)))


okPayload : String -> Result String (Result Error Rec)
okPayload text =
    Ok (Result.andThen (\v -> Ok { n = v + 3 }) (raw text))


errPayload : String -> Result (Result Error Rec) Int
errPayload text =
    Err (Result.map (\v -> { n = v + 4 }) (raw text))


showR : Result Error Rec -> String
showR r =
    case r of
        Ok x ->
            String.fromInt x.n

        Err _ ->
            "err"


showM : Maybe (Result Error Rec) -> String
showM m =
    case m of
        Just r ->
            showR r

        Nothing ->
            "none"


main =
    let
        t =
            "{\"n\":3}"

        n1 =
            case nested t of
                Just inner ->
                    showM inner

                Nothing ->
                    "none"

        m1 =
            case maybePayload t of
                Just (Just x) ->
                    String.fromInt x.n

                _ ->
                    "none"

        o1 =
            case okPayload t of
                Ok r ->
                    showR r

                Err _ ->
                    "err"

        e1 =
            case errPayload t of
                Err r ->
                    showR r

                Ok _ ->
                    "ok"
    in
    println
        (String.join " "
            [ showM (viaPipe t), showM (viaParens t), showM (viaBackPipe t), n1, m1, o1, e1 ]
        )
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-ctorerased-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ctorerased\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), SRC).unwrap();
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
fn builtin_ctor_narrows_an_erased_payload_and_runs() {
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
    let go = std::fs::read_to_string(dir.join("sky-out").join("main.go")).unwrap();
    // The narrowing lands on the constructor's argument, typed to the payload.
    assert!(
        go.contains(
            "rt.Just[rt.SkyResult[Sky_Core_Error_Error, Main_Rec_R]](/* primitive join */ rt.ResultCoerceOk[Sky_Core_Error_Error, Main_Rec_R]("
        ),
        "Just's erased argument is narrowed to the payload type:\n{go}"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(combined.contains("3 3 3 4 5 6 7"), "output:\n{combined}");
    let _ = std::fs::remove_dir_all(&dir);
}
