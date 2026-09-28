//! Regression: ordinary recursive Task code runs at a constant Go stack
//! (v0.27.0, Phase 7 part B — the Task trampoline).
//!
//! Before the trampoline, every `Task.andThen` step forced the continuation's
//! task inside the Go frame that forced its source, and every typed boundary
//! (`rt.TaskCoerceT`) added a closure frame. A Sky program recursing two
//! million steps through `andThen` died with Go's `fatal error: stack
//! overflow` — not a panic, so no handler saw it and the process exited.
//!
//! This test compiles and RUNS a real Sky program, so it exercises the exact
//! Go the compiler emits (`rt.TaskCoerceT[E, A](rt.AnyTaskAndThen(...))`), not
//! a hand-written imitation. It needs a Go toolchain and fails, never skips,
//! without one (`live_gate`).

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.List as List
import Sky.Core.Error as Error
import Sky.Core.String as String
import Std.Log exposing (println)


-- Tail recursion through the success continuation.
count : Int -> Int -> Task Error Int
count limit n =
    if n >= limit then
        Task.succeed n

    else
        Task.succeed n |> Task.andThen (\_ -> count limit (n + 1))


-- Tail recursion through the error continuation.
retry : Int -> Int -> Task Error Int
retry limit n =
    if n >= limit then
        Task.succeed n

    else
        Task.fail (Error.invalidInput "again")
            |> Task.onError (\_ -> retry limit (n + 1))


-- Recursion whose continuation maps the recursive result: the pending maps
-- are heap frames of the interpreter, never Go frames.
depth : Int -> Int -> Task Error Int
depth limit n =
    if n >= limit then
        Task.succeed 0

    else
        Task.succeed n
            |> Task.andThen (\_ -> depth limit (n + 1) |> Task.map (\d -> d + 1))


report : String -> Task Error Int -> String
report label t =
    case Task.run t of
        Ok n ->
            label ++ " " ++ String.fromInt n

        Err e ->
            label ++ " failed: " ++ Error.toString e


main =
    let
        seqTotal =
            List.range 1 100000
                |> List.map Task.succeed
                |> Task.sequence
                |> Task.map (List.foldl (\x acc -> x + acc) 0)
    in
    println
        (String.join " | "
            [ report "andThen" (count 2000000 0)
            , report "onError" (retry 2000000 0)
            , report "map" (depth 1000000 0)
            , report "sequence" seqTotal
            ]
        )
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-trampoline-{}-{}",
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
        "name = \"trampoline\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
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

#[test]
fn recursive_task_code_runs_two_million_steps() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch();
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log:\n{log})");

    let run = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&run.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&run.stderr));
    let tail: String = combined
        .lines()
        .rev()
        .take(20)
        .collect::<Vec<_>>()
        .into_iter()
        .rev()
        .collect::<Vec<_>>()
        .join("\n");
    assert_eq!(
        run.status.code(),
        Some(0),
        "the recursive Task program must finish (a fatal stack overflow exits 2). \
         Output tail:\n{tail}"
    );
    assert!(
        combined.contains(
            "andThen 2000000 | onError 2000000 | map 1000000 | sequence 5000050000"
        ),
        "output:\n{tail}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
