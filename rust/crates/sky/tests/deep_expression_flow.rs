//! A deep expression compiles end to end; it does not overflow the compiler's
//! stack.
//!
//! Parsing, name resolution, inference, lowering and Go emission recurse over
//! the expression tree. A left-nested `|>` pipeline is as deep as it is long,
//! and a generated record codec (`Codec.object Ctor |> Codec.field … |> …`) has
//! one step per field. A 1,600-step pipeline overflowed the 8 MiB main-thread
//! stack in lowering and aborted `sky check` ("thread 'main' has overflowed its
//! stack"). Each recursive walker now grows its stack on demand
//! (`base::ensure_stack`).
//!
//! This test is the end-to-end leg: `sky check` and `sky build` on the real
//! binary, then run the app. The binary also runs the compiler on a thread with
//! a large stack (`main.rs`, `COMPILER_STACK_BYTES`), so the guards themselves
//! are falsified on a 2 MiB thread by `project/tests/deep_expression_emit.rs`
//! (parse to Go emission) and `sky-lsp/tests/deep_expression.rs` (diagnostics
//! and hover).

use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn project(name: &str, main_sky: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-deep-{name}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"{name}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), main_sky).unwrap();
    dir
}

#[test]
fn a_four_thousand_step_pipeline_compiles() {
    let steps = "        |> inc\n".repeat(4000);
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         inc : Int -> Int\n\
         inc x =\n    x + 1\n\n\n\
         chain : Int\n\
         chain =\n    0\n{steps}\n\n\
         main =\n    println (String.fromInt chain)\n"
    );
    let dir = project("pipe", &src);
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&dir);
        return;
    }
    // `sky check` and `sky build` share the front end, and `check` also
    // lowers and emits Go before `go build`: both must get through.
    let check = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky check");
    let check_log = format!(
        "{}{}",
        String::from_utf8_lossy(&check.stdout),
        String::from_utf8_lossy(&check.stderr)
    );
    assert!(
        !check_log.contains("overflowed its stack"),
        "sky check overflowed its stack on a deep pipeline:\n{check_log}"
    );
    assert!(
        check.status.success(),
        "sky check must succeed:\n{check_log}"
    );
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !log.contains("overflowed its stack"),
        "the compiler overflowed its stack on a deep pipeline:\n{log}"
    );
    assert!(out.status.success(), "sky build must succeed:\n{log}");
    let run = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .output()
        .expect("run the built app");
    let _ = std::fs::remove_dir_all(&dir);
    assert_eq!(
        String::from_utf8_lossy(&run.stdout).trim(),
        "4000",
        "the pipeline must evaluate every step"
    );
}
