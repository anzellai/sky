//! Regression: three runtime narrowings the lowering could already prove
//! unnecessary are gone (v0.28.0, doc 14 §1: both Go shapes known at emit
//! time).
//!
//! 1. A union whose variant names a type its OWN module declares stays a
//!    sealed interface even when another module declares the same name.
//!    `ty_refs_ambiguous` counted the name program-wide, so `Other.Item` (or
//!    `Std.Ai.Train.Snapshot`, for `examples/18-job-queue`) put `Main.Msg` on
//!    the `rt.SkyADT` bag and every payload read narrowed (R6). The field is
//!    resolved with the declaring module as `cur_mod`, so a bare local name
//!    is not ambiguous.
//! 2. `String.fromInt`, `modBy` and `Math.round` over typed arguments call
//!    the runtime's typed twins (`rt.String_fromIntT`, `rt.Basics_modByT`,
//!    `rt.Math_roundT`), not the `any` kernels whose result was narrowed back
//!    (R5, lever §5.3).
//! 3. An empty list literal at a typed slot is built as `[]T{}`, not
//!    `[]any{}` narrowed with `rt.AsListT[T]` (R1, lever §5.2).
//!
//! The program's output is unchanged. The test needs a Go toolchain
//! (`sky build` runs `go build`) and fails, never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Math as Math
import Std.Log exposing (println)
import Other


type alias Item =
    { name : String, size : Int }


type alias Store =
    { items : List Item, tags : List String }


type Msg
    = Loaded (List Item)
    | Cleared


emptyStore : Store
emptyStore =
    { items = [], tags = [] }


apply : Msg -> Store -> Store
apply msg store =
    case msg of
        Loaded items ->
            { store | items = items }

        Cleared ->
            emptyStore


describe : Float -> Int -> String
describe f n =
    String.fromInt (Math.round f) ++ "/" ++ String.fromInt (modBy 3 n)


main =
    let
        s =
            apply (Loaded [ { name = "a", size = 2 } ]) emptyStore

        sizes =
            List.map (\i -> String.fromInt i.size) s.items
    in
    println
        (String.join ","
            (sizes
                ++ [ describe 2.6 7
                   , String.fromInt (List.length (apply Cleared s).items)
                   , Other.label { code = 5 }
                   ]
            )
        )
"#;

const OTHER: &str = r#"module Other exposing (Item, label)


type alias Item =
    { code : Int }


label : Item -> String
label i =
    "other " ++ String.fromInt i.code
"#;

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-provable-{}-{}",
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
        "name = \"provable\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), MAIN).unwrap();
    std::fs::write(dir.join("src").join("Other.sky"), OTHER).unwrap();
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
fn provably_unneeded_narrowings_are_not_emitted() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch();
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "the program must build (log:\n{log})");
    let go = std::fs::read_to_string(dir.join("sky-out").join("main.go")).unwrap();
    assert!(
        go.contains("type Main_Msg interface"),
        "a union over its own module's `Item` stays sealed:\n{go}"
    );
    assert!(
        go.contains("type Main_Msg_Loaded_V struct { V0 []Main_Item_R }"),
        "the sealed variant field is the declaring module's own `Item`:\n{go}"
    );
    for twin in [
        "rt.String_fromIntT(",
        "rt.Math_roundT(",
        "rt.Basics_modByT(",
    ] {
        assert!(go.contains(twin), "a typed call uses `{twin}`:\n{go}");
    }
    assert!(
        !go.contains("([]any{})"),
        "an empty list at a typed slot is built at the slot's type:\n{go}"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(combined.contains("2,3/1,0,other 5"), "output:\n{combined}");
    let _ = std::fs::remove_dir_all(&dir);
}
