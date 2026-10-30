//! Regression: a record-update lambda at a slot whose record is an
//! ANONYMOUS record type builds (v0.28.0).
//!
//! `update : ({ name : String, rank : Int } -> { name : String, rank : Int })
//! -> Job -> Job` called as `update (\s -> { s | rank = n }) job`. The lambda's
//! update-base param is an anonymous struct, which `lower_lambda` widens to
//! `any` (the update must keep every field), so the literal is emitted
//! `func(v any) any`. Its type was still reported as the declared
//! `func(struct{…}) struct{…}`, so `coerce_if_needed` saw equal types, the
//! slot's adaptation was skipped, and `go build` rejected the call. The
//! literal now carries its emitted type whenever that type erases a position
//! the declared one fixes, and the slot retypes it (doc 14 lever §5.1).
//! With a `type alias` in the annotation the same program already built.
//!
//! Covered: a direct call, a point-free def (also curried, see
//! `curried_def_value_slot.rs`) and a `List.map` over an anonymous record list.
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


type Job
    = Job { name : String, rank : Int }


update : ({ name : String, rank : Int } -> { name : String, rank : Int }) -> Job -> Job
update f (Job j) =
    Job (f j)


withRank : Int -> Job -> Job
withRank n job =
    update (\s -> { s | rank = n }) job


rename : String -> Job -> Job
rename name =
    update (\s -> { s | name = name })


bumpAll : List { name : String, rank : Int } -> List { name : String, rank : Int }
bumpAll xs =
    List.map (\s -> { s | rank = s.rank + 1 }) xs


describe : Job -> String
describe (Job j) =
    j.name ++ " " ++ String.fromInt j.rank


main =
    let
        start =
            Job { name = "a", rank = 1 }
    in
    println
        (String.join ","
            [ describe (withRank 4 start)
            , describe (rename "b" start)
            , String.join "" (List.map (\r -> String.fromInt r.rank) (bumpAll [ { name = "x", rank = 8 } ]))
            ]
        )
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-anonupdate-{}-{}",
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
        "name = \"anonupdate\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
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
fn a_record_update_lambda_at_an_anonymous_record_slot_builds_and_runs() {
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
    // The lambda is retyped at the slot's anonymous-struct shape.
    assert!(
        go.contains(
            "Main_update(func(_e0 struct{ Name string; Rank int }) struct{ Name string; Rank int }"
        ),
        "the update lambda takes the slot's record shape:\n{go}"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(combined.contains("a 4,b 1,9"), "output:\n{combined}");
    let _ = std::fs::remove_dir_all(&dir);
}
