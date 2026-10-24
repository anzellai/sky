//! `Tuple.first` / `Tuple.second` resolve with no `import`, as in Elm.
//!
//! Before the fix a module that used `Tuple.first ( "a", "b" )` with no
//! `import Sky.Core.Tuple as Tuple` failed `sky check` with
//! `Undefined name: Tuple.first`. `Maybe.withDefault` worked with no import
//! only because `Maybe` is a kernel module. `Sky.Core.Tuple` is pure Sky, so
//! `hir::DEFAULT_QUALIFIED_IMPORTS` now binds the qualifier. An import that
//! binds `Tuple` to another module still wins. Needs a `go` toolchain.

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

fn project(tag: &str, files: &[(&str, &str)]) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-tuple-default-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"tupledefault\"\nentry = \"src/Main.sky\"\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    for (rel, text) in files {
        std::fs::write(dir.join(rel), text).unwrap();
    }
    dir
}

fn sky(dir: &Path, args: &[&str]) -> (bool, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), log)
}

/// `sky check`, `sky build`, then run the binary; returns its stdout.
fn check_build_run(dir: &Path) -> String {
    let (ok, log) = sky(dir, &["check", "src/Main.sky"]);
    assert!(ok, "sky check must pass:\n{log}");
    let (ok, log) = sky(dir, &["build", "src/Main.sky"]);
    assert!(ok, "sky build must pass:\n{log}");
    let out = Command::new(dir.join("sky-out/app"))
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky-out/app");
    assert!(
        out.status.success(),
        "the app must exit 0:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn tuple_first_and_second_need_no_import() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project(
        "noimport",
        &[(
            "src/Main.sky",
            "module Main exposing (main)\n\
             \n\
             import Std.Log exposing (println)\n\
             \n\
             \n\
             main =\n    \
                 let\n        \
                     pair =\n            \
                         ( \"left-value\", 42 )\n    \
                 in\n    \
                 println (Tuple.first pair ++ \" \" ++ String.fromInt (Tuple.second pair))\n",
        )],
    );
    let stdout = check_build_run(&dir);
    assert!(
        stdout.contains("left-value 42"),
        "expected `left-value 42`, got:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_user_alias_named_tuple_wins_over_the_default() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project(
        "alias",
        &[
            (
                "src/Pair.sky",
                "module Pair exposing (first)\n\
                 \n\
                 \n\
                 first : ( String, String ) -> String\n\
                 first _ =\n    \
                     \"from-user-module\"\n",
            ),
            (
                "src/Main.sky",
                "module Main exposing (main)\n\
                 \n\
                 import Pair as Tuple\n\
                 import Std.Log exposing (println)\n\
                 \n\
                 \n\
                 main =\n    \
                     println (Tuple.first ( \"a\", \"b\" ))\n",
            ),
        ],
    );
    let stdout = check_build_run(&dir);
    assert!(
        stdout.contains("from-user-module"),
        "the user alias `Tuple` must win, got:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
