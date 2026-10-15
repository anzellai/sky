//! A deep expression compiles; it does not overflow the compiler's stack.
//!
//! Parsing, inference, lowering and Go emission recurse over the expression
//! tree. A left-nested `|>` pipeline is as deep as it is long, and a generated
//! record codec (`Codec.object Ctor |> Codec.field … |> …`) has one step per
//! field. A 1,600-step pipeline overflowed the 8 MiB main-thread stack in
//! lowering and aborted `sky check` ("thread 'main' has overflowed its stack").
//! The compiler now runs on a thread with a 512 MiB stack (`main.rs`,
//! `COMPILER_STACK_BYTES`).
//!
//! Falsifier: run `sky_main` on the main thread again (or give the thread the
//! default stack) and this test aborts with the stack overflow.

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
