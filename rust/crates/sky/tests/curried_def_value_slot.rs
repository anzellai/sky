//! Regression: a function DEFINED with fewer parameters than its type has,
//! passed as a value to a slot of its full type, is applied with every
//! argument (v0.28.0).
//!
//! `withRank : Int -> Job -> Job` defined `withRank n = update (…)` emits
//! `func Main_withRank(v_0 int) func(Main_Job) Main_Job`, but every slot of
//! that Sky type is the flat `func(int, Main_Job) Main_Job`. The lowering left
//! the arity mismatch to the runtime `rt.CoerceFuncSlot` adapter, which only
//! adapts uncurried to curried: it called `withRank` with the first argument,
//! dropped the second and returned the zero `Job`, so the next pattern match
//! panicked (`IndexOutOfRange`). The lowering now bridges the currying
//! statically at the slot's shape (doc 14 lever §5.1, `curry_bridge` in
//! lower.rs): `func(_e0 int, _e1 Main_Job) Main_Job { return
//! Main_withRank(_e0)(_e1) }`.
//!
//! Covered slots: a user function parameter, a `List.foldl` callback, a
//! record field, a three-argument type defined with two parameters, and a
//! let-bound function defined with one parameter.
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
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Log exposing (println)


type alias Settings =
    { name : String, rank : Int }


type Job
    = Job Settings


type alias Setter =
    { set : Int -> Job -> Job }


update : (Settings -> Settings) -> Job -> Job
update f (Job j) =
    Job (f j)


-- One declared parameter: the second arrives through `update`.
withRank : Int -> Job -> Job
withRank n =
    update (\s -> { s | rank = n })


-- Two declared parameters, the third through `update`.
withBoth : String -> Int -> Job -> Job
withBoth name n =
    update (\s -> { s | name = name, rank = n })


apply : (Int -> Job -> Job) -> Int -> Job -> Job
apply set n job =
    set n job


apply3 : (String -> Int -> Job -> Job) -> Job -> Job
apply3 set job =
    set "b" 7 job


describe : Job -> String
describe (Job j) =
    j.name ++ " " ++ String.fromInt j.rank


main =
    let
        start =
            Job { name = "a", rank = 1 }

        setter =
            { set = withRank }

        local n =
            update (\s -> { s | rank = n * 10 })
    in
    println
        (String.join ","
            [ describe (apply withRank 4 start)
            , describe (List.foldl withRank start [ 2, 5 ])
            , describe (setter.set 6 start)
            , describe (apply3 withBoth start)
            , describe (apply local 3 start)
            ]
        )
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-curriedslot-{}-{}",
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
        "name = \"curriedslot\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
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
fn a_curried_def_value_is_applied_with_every_argument() {
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
    assert!(
        !go.contains("CoerceFuncSlot[func(int, Main_Job) Main_Job](_s)"),
        "a curried def must be bridged statically, not by the runtime adapter:\n{go}"
    );
    assert!(
        go.contains("Main_Job) Main_Job { return Main_withRank(_e"),
        "the bridge applies each parameter group in turn:\n{go}"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(
        combined.contains("a 4,a 5,a 6,b 7,a 30"),
        "output:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
