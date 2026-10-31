//! `sky run` passes the arguments after `--` to the program, as `go run` and
//! `cargo run` do, and `sky run --help` prints help and builds nothing.
//!
//! Before this, `sky run src/Main.sky -- a "b c"` started the program with no
//! arguments (`System.args ()` was empty) and `sky run --help` in a project
//! directory built and ran the project. Needs a `go` toolchain for the tests
//! that build; the help test is toolchain-free.

use std::path::{Path, PathBuf};
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

/// A scratch project whose program prints its arguments joined by `|`,
/// framed by `<` and `>` so an empty list is visible as `<>`.
fn stage_project(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-run-args-{tag}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"run-args\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.System as System
import Sky.Core.Task as Task
import Std.Log exposing (println)


main =
    System.args ()
        |> Task.andThen (\a -> println ("<" ++ String.join "|" a ++ ">"))
        |> Task.run
"#,
    )
    .unwrap();
    dir
}

fn sky(dir: &Path, args: &[&str]) -> (bool, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

#[test]
fn run_passes_arguments_after_double_dash() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_project("pass");
    let (ok, out) = sky(&dir, &["run", "src/Main.sky", "--", "a", "b c"]);
    assert!(ok, "sky run failed:\n{out}");
    assert!(
        out.contains("<a|b c>"),
        "the program should receive `a` and `b c`; got:\n{out}"
    );

    // A flag after `--` belongs to the program, not to `sky run`: `--help`
    // here must not print sky's help, and `--go-jobs` must not be consumed.
    let (ok, out) = sky(
        &dir,
        &["run", "src/Main.sky", "--", "--help", "--go-jobs", "x"],
    );
    assert!(ok, "sky run failed:\n{out}");
    assert!(
        out.contains("<--help|--go-jobs|x>"),
        "flags after `--` should reach the program unchanged; got:\n{out}"
    );

    // The entry may come from sky.toml: `sky run -- a` still passes `a`.
    let (ok, out) = sky(&dir, &["run", "--", "only"]);
    assert!(ok, "sky run failed:\n{out}");
    assert!(out.contains("<only>"), "got:\n{out}");

    // Without `--` the behaviour is unchanged: no arguments reach the program.
    let (ok, out) = sky(&dir, &["run", "src/Main.sky"]);
    assert!(ok, "sky run failed:\n{out}");
    assert!(
        out.contains("<>"),
        "no `--` means no arguments; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn run_help_prints_help_and_builds_nothing() {
    let dir = stage_project("help");
    for flag in ["--help", "-h"] {
        let (ok, out) = sky(&dir, &["run", flag]);
        assert!(ok, "sky run {flag} should succeed; got:\n{out}");
        assert!(
            out.contains("sky run") && out.contains("--"),
            "sky run {flag} should print the run usage; got:\n{out}"
        );
        assert!(
            !dir.join("sky-out").exists(),
            "sky run {flag} must not build the project; got:\n{out}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}
