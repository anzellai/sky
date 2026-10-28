//! Regression: a destructuring pattern whose names the body does not use
//! builds (v0.28.0).
//!
//! A lambda or function parameter that is a tuple / record / constructor
//! pattern binds each inner name with a Go short declaration (`j := _t0.V0`)
//! at the head of the body. A case arm and a `let` destructure already follow
//! every such binding with a `_ = j` discard, but the parameter paths
//! (`bind_param` for top-level and `let`-bound functions, `lower_lambda` for
//! lambdas) did not, so `\( j, s, a ) ( st, k, ( sm, rs ) ) -> j ++ s` failed
//! `go build` with "declared and not used". Every shape is covered here with
//! unused names, and the program output is checked.
//!
//! The test needs a Go toolchain (`sky build` runs `go build`) and fails,
//! never skips, without one.

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
import Sky.Core.Json.Decode as JsonD
import Std.Log exposing (println)


type Box
    = Box Int String


type alias Pt =
    { x : Int, y : Int, label : String }


pairFirst : ( Int, String ) -> Int
pairFirst ( a, b ) =
    a


tripleLast : ( Int, String, ( Bool, Int ) ) -> Int
tripleLast ( a, b, ( c, d ) ) =
    d


boxLabel : Box -> String
boxLabel (Box n s) =
    s


ptLabel : Pt -> String
ptLabel { x, y, label } =
    label


decoded : String
decoded =
    case
        JsonD.decodeString
            (JsonD.map2
                (\( j, s, a ) ( st, k, ( sm, rs ) ) -> j ++ " " ++ s ++ " " ++ String.fromInt rs)
                (JsonD.map3 (\j s a -> ( j, s, a )) (JsonD.field "job" JsonD.string) (JsonD.field "state" JsonD.string) (JsonD.field "attempt" JsonD.int))
                (JsonD.map3
                    (\st k sm -> ( st, k, sm ))
                    (JsonD.field "staging" JsonD.string)
                    (JsonD.field "runKey" JsonD.string)
                    (JsonD.map2 (\sm rs -> ( sm, rs ))
                        (JsonD.field "spentMinor" JsonD.int)
                        (JsonD.field "runningSinceMs" JsonD.int)
                    )
                )
            )
            """{"job":"J","state":"S","attempt":1,"staging":"T","runKey":"K","spentMinor":2,"runningSinceMs":77}"""
    of
        Ok v ->
            v

        Err _ ->
            "err"


main =
    let
        tup2 =
            List.map (\( a, b ) -> b) [ ( 1, "p" ), ( 2, "q" ) ]

        tup3 =
            List.map (\( a, b, c ) -> a) [ ( 3, "r", True ) ]

        nested =
            List.map (\( a, ( b, c ) ) -> c) [ ( 4, ( "s", 5 ) ) ]

        ignoredAll =
            List.map (\( a, b ) -> 0) [ ( 6, "t" ) ]

        rec =
            List.map (\{ x, y, label } -> x) [ { x = 7, y = 8, label = "u" } ]

        ctor =
            List.map (\(Box n s) -> n) [ Box 9 "v" ]

        ( la, lb ) =
            ( 10, "w" )

        ( ( lc, ld ), le ) =
            ( ( 11, "x" ), 12 )

        localFn ( a, b ) =
            b ++ "!"

        localRec { x, y, label } =
            y

        caseArm =
            case ( 13, ( "y", 14 ) ) of
                ( a, ( b, c ) ) ->
                    c

        parts =
            [ String.join "," tup2
            , String.join "," (List.map String.fromInt tup3)
            , String.join "," (List.map String.fromInt nested)
            , String.join "," (List.map String.fromInt ignoredAll)
            , String.join "," (List.map String.fromInt rec)
            , String.join "," (List.map String.fromInt ctor)
            , String.fromInt la
            , String.fromInt le
            , localFn ( 15, "z" )
            , String.fromInt (localRec { x = 16, y = 17, label = "l" })
            , String.fromInt caseArm
            , String.fromInt (pairFirst ( 18, "m" ))
            , String.fromInt (tripleLast ( 19, "n", ( True, 20 ) ))
            , boxLabel (Box 21 "o")
            , ptLabel { x = 22, y = 23, label = "pt" }
            , decoded
            ]
    in
    println (String.join "|" parts)
"#;

const EXPECTED: &str = "p,q|3|5|0|7|9|10|12|z!|17|14|18|20|o|pt|J S 77";

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-unusedpat-{}-{}",
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
        "name = \"unusedpat\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), MAIN).unwrap();
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
fn unused_names_in_destructuring_patterns_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch();
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(
        bin.is_file(),
        "a well-typed program with unused pattern names must pass go build (log:\n{log})"
    );
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(combined.contains(EXPECTED), "output:\n{combined}");
    let _ = std::fs::remove_dir_all(&dir);
}
