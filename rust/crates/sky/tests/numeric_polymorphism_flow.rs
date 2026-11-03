//! Numeric instantiation selects representation before literals and arithmetic.
//! Result coercion alone cannot recover precision already lost in the body.

use std::path::PathBuf;
use std::process::{Command, Stdio};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

fn have_go() -> bool {
    let go = Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success());
    go
}

struct Fixture(PathBuf);

impl Fixture {
    fn new(source: &str) -> Self {
        static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let root = std::env::temp_dir().join(format!(
            "sky-numeric-{}-{}",
            std::process::id(),
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        std::fs::create_dir_all(root.join("src")).unwrap();
        std::fs::write(
            root.join("sky.toml"),
            "name = \"numeric-polymorphism\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
        )
        .unwrap();
        std::fs::write(root.join("src/Main.sky"), source).unwrap();
        Self(root)
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

#[test]
fn numeric_polymorphism_preserves_each_instantiation() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let fixture = Fixture::new(include_str!("fixtures/numeric-polymorphism/src/Main.sky"));
    let out = Command::new(env!("CARGO_BIN_EXE_sky"))
        .args(["run", "src/Main.sky"])
        .current_dir(&fixture.0)
        .stdin(Stdio::null())
        .output()
        .expect("run numeric instantiation fixture");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "{stdout}\n{stderr}");
    assert!(stdout.contains("44 passed"), "{stdout}");
}

#[test]
fn a_function_stage_fails_before_its_returned_function_is_called() {
    if !required(Need::Go, have_go()) {
        return;
    }
    assert_division_by_zero(
        r#"module Main exposing (main)

import Sky.Core.Task as Task
import Std.Log exposing (println)

maker : Float -> () -> Float
maker =
    \denominator ->
        let
            quotient = 1.0 / denominator
        in
        \_ -> quotient

main =
    let
        returned = maker 0.0
    in
    Task.run (println "UNREACHABLE")
"#,
    );
}

#[test]
fn tail_recursion_evaluates_discarded_arguments() {
    if !required(Need::Go, have_go()) {
        return;
    }
    assert_division_by_zero(
        r#"module Main exposing (main)

import Sky.Core.Task as Task
import Std.Log exposing (println)

loop : Int -> Float -> Float
loop n _ =
    if n <= 0 then
        0.0
    else
        loop (n - 1) (1.0 / 0.0)

main =
    let
        value = loop 2 1.0
    in
    Task.run (println "UNREACHABLE")
"#,
    );
}

fn assert_division_by_zero(source: &str) {
    let fixture = Fixture::new(source);
    // Build inherits the toolchain path. The built app has no need for any
    // ambient settings, so execute it with an empty environment.
    let built = Command::new(env!("CARGO_BIN_EXE_sky"))
        .args(["build", "-o", "stage-test", "src/Main.sky"])
        .current_dir(&fixture.0)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    assert!(
        built.status.success(),
        "{}",
        String::from_utf8_lossy(&built.stderr)
    );
    let out = Command::new(fixture.0.join("stage-test/app"))
        .env_clear()
        .current_dir(&fixture.0)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let output = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(!out.status.success(), "{output}");
    assert!(output.contains("DivisionByZero"), "{output}");
    assert!(!output.contains("UNREACHABLE"), "{output}");
}
