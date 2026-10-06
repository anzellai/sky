//! Regression: `==` on custom types compares the CONSTRUCTOR, then the fields
//! (v0.27.7).
//!
//! `type T = A | B | C Int` lowers to a sealed interface with one Go struct per
//! constructor: `Main_T_A_V{}`, `Main_T_B_V{}`, `Main_T_C_V{V0: 1}`. `==`
//! lowers to `rt.Eq`, whose `deepEq` compared two structs of DIFFERENT types
//! "field by name" (a fallback meant for two record structs with the same
//! fields) and never looked at the constructor. `A{}` and `B{}` have no fields,
//! so `A == B` was True; `C 1` and `D 1` both carry `V0 = 1`, so `C 1 == D 1`
//! was True. The Set / Cache / `Std.Ui.Lazy` identity key had the same fault
//! (every nullary variant keyed `R0;`), so `Set.fromList [ A, B ]` held one
//! element. Both now ask one recogniser, `runtime-go/rt/union_value.go`.
//!
//! The program covers the doc 13 D2 neighbourhood: nullary vs nullary, nullary
//! vs fields, equal and unequal payloads, a runtime-built value, the values
//! nested in a record / list / Maybe / Result / tuple / Dict, a custom type
//! from another module, a generic custom type (sealed) and a generic one whose
//! name collides with a stdlib type (kept on the `rt.SkyADT` bag), `/=`,
//! `List.member`, `Set`, and `List.sort` (constructor order).
//!
//! Two legs: the EMITTED-GO leg checks the premise (the type is sealed); the
//! RUN leg needs a Go toolchain and fails, never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict
import Sky.Core.List as List
import Sky.Core.Set as Set
import Sky.Core.String as String
import Std.Log exposing (println)
import Shapes exposing (Box(..), Shape(..))

type T
    = A
    | B
    | C Int
    | D Int

type Keep a
    = Hollow
    | Hold a

type alias Holder =
    { kind : T, count : Int }

pick : Int -> T
pick n =
    if n == 0 then
        A

    else
        B

show : String -> Bool -> String
show label b =
    label
        ++ "="
        ++ (if b then
                "True"

            else
                "False"
           )

emptyBox : Box Int
emptyBox =
    Empty

main =
    println
        (String.join "\n"
            [ show "A==B" (A == B)
            , show "B==A" (B == A)
            , show "A==A" (A == A)
            , show "A/=B" (A /= B)
            , show "C1==C1" (C 1 == C 1)
            , show "C1==C2" (C 1 == C 2)
            , show "C1==D1" (C 1 == D 1)
            , show "A==C0" (A == C 0)
            , show "pick0==B" (pick 0 == B)
            , show "pick1==B" (pick 1 == B)
            , show "recA==recB" ({ kind = A, count = 1 } == { kind = B, count = 1 })
            , show "recA==recA" (Holder A 1 == Holder A 1)
            , show "listAB" ([ A, C 1 ] == [ B, C 1 ])
            , show "listAA" ([ A, C 1 ] == [ A, C 1 ])
            , show "justAB" (Just A == Just B)
            , show "okAB" (Ok A == Ok B)
            , show "tupleAB" (( A, 1 ) == ( B, 1 ))
            , show "dictAB" (Dict.fromList [ ( "k", A ) ] == Dict.fromList [ ( "k", B ) ])
            , show "dotBlank" (Dot == Blank)
            , show "circle2" (Circle 2 == Shapes.circle 2)
            , show "circleRect" (Circle 2 == Rect 2 2)
            , show "hollowHold" (Hollow == Hold 0)
            , show "holdAB" (Hold A == Hold B)
            , show "holdC3" (Hold (C 3) == Hold (C 3))
            , show "emptyFull" (emptyBox == Full 0)
            , show "fullAB" (Full A == Full B)
            , show "memberB" (List.member B [ A, C 1 ])
            , show "set2" (Set.size (Set.fromList [ A, B, A ]) == 2)
            , show "sorted" (List.sort [ C 2, B, C 1, A ] == [ A, B, C 1, C 2 ])
            ]
        )
"#;

const SHAPES: &str = r#"module Shapes exposing (Shape(..), Box(..), circle)

type Shape
    = Dot
    | Blank
    | Circle Int
    | Rect Int Int

type Box a
    = Empty
    | Full a

circle : Int -> Shape
circle r =
    Circle r
"#;

const EXPECTED: &[&str] = &[
    "A==B=False",
    "B==A=False",
    "A==A=True",
    "A/=B=True",
    "C1==C1=True",
    "C1==C2=False",
    "C1==D1=False",
    "A==C0=False",
    "pick0==B=False",
    "pick1==B=True",
    "recA==recB=False",
    "recA==recA=True",
    "listAB=False",
    "listAA=True",
    "justAB=False",
    "okAB=False",
    "tupleAB=False",
    "dictAB=False",
    "dotBlank=False",
    "circle2=True",
    "circleRect=False",
    "hollowHold=False",
    "holdAB=False",
    "holdC3=True",
    "emptyFull=False",
    "fullAB=False",
    "memberB=False",
    "set2=True",
    "sorted=True",
];

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-adteq-{tag}-{}-{}",
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
        "name = \"adteq\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), MAIN).unwrap();
    std::fs::write(dir.join("src").join("Shapes.sky"), SHAPES).unwrap();
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
fn adt_equality_fixture_uses_distinct_variant_structs() {
    let dir = scratch("emit");
    let log = build(&dir);
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (log:\n{log})"
    );
    let go = std::fs::read_to_string(&main_go).unwrap();
    // The premise: `T` is sealed, so `A` and `B` are two field-less Go structs
    // of different types — the exact shape the defect compared as equal.
    for decl in [
        "type Main_T_A_V struct {}",
        "type Main_T_B_V struct {}",
        "type Main_T_C_V struct { V0 int }",
    ] {
        assert!(go.contains(decl), "the fixture's premise: `{decl}`:\n{go}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn adt_equality_compares_the_constructor() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("run");
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log:\n{log})");
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    let mut wrong = Vec::new();
    for want in EXPECTED {
        if !combined.lines().any(|l| l.trim() == *want) {
            wrong.push(*want);
        }
    }
    assert!(
        wrong.is_empty(),
        "{} of {} equality checks are wrong; expected lines missing: {wrong:?}\noutput:\n{combined}",
        wrong.len(),
        EXPECTED.len()
    );
    let _ = std::fs::remove_dir_all(&dir);
}
