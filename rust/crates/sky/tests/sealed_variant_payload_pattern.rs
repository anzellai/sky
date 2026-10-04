//! Regression: a `case` on a SEALED app ADT reads each variant payload field
//! directly, without a runtime narrowing (v0.27.3).
//!
//! An app ADT whose variant fields all resolve is lowered as a sealed
//! interface: `type Main_Fig_Circle_V struct { V0 int }`. After the variant
//! assertion `_v0, _ok := _subj.(Main_Fig_Circle_V)`, `_v0.V0` already IS an
//! `int`. `adt_variant_binds` (lower.rs) built that selector as `any` whenever
//! the field's Go type was not `any`, and wrapped it in a `GenericErase`
//! Coerce, so the emitted Go boxed a typed field and asserted it straight back:
//!
//! ```go
//! v_2 := /* generic erase */ rt.AsInt(_v0.V0)
//! ```
//!
//! doc 14 origin R6 (§3; closeable for app ADTs, §4.5), lever §5.2 (the
//! selector carries the field's declared Go type), closeable by §1: the
//! variant struct's field type and the binder's type are both known at emit
//! time and are equal. The same rule already holds for typed Result / Maybe
//! payloads (R8, `typed_result_payload_pattern.rs`). The coerce-floor golden
//! carries the drop.
//!
//! The type names are chosen not to collide with any stdlib nominal: a
//! colliding name (`Point`, `Box`) is ambiguous and keeps the union on the
//! `rt.SkyADT` bag, where the narrowing is genuine.
//!
//! Covered shapes (the doc 13 D2/D3 neighbourhood): an `Int` payload, a
//! record-alias payload, a `Maybe Int` payload with a nested `Just n`, a
//! two-field payload, an `as` binding over a ctor nested in another sealed
//! ctor (`Wrap ((Circle r) as inner)`), and a top-level `as`.
//!
//! Two legs: the EMITTED-GO leg always runs; the RUN leg needs a Go toolchain
//! and fails, never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Std.Log exposing (println)

type alias Pt2 =
    { x : Int, y : Int }

type Fig
    = Circle Int
    | Rect Pt2
    | Tagged (Maybe Int)
    | Pair Int String

type Holder
    = Wrap Fig


area : Fig -> Int
area s =
    case s of
        Circle r ->
            r * r

        Rect p ->
            p.x * p.y

        Tagged m ->
            case m of
                Just n ->
                    n

                Nothing ->
                    0

        Pair n str ->
            n + String.length str


pick : Fig -> Int
pick s =
    case s of
        Tagged (Just n) ->
            n + 100

        (Circle r) as whole ->
            r + area whole

        _ ->
            -1


unwrap : Holder -> Int
unwrap b =
    case b of
        Wrap ((Circle r) as inner) ->
            r + area inner

        Wrap other ->
            area other


main =
    println
        (String.join " "
            (List.map String.fromInt
                [ area (Circle 3)
                , area (Rect { x = 2, y = 5 })
                , area (Tagged (Just 7))
                , area (Pair 1 "ab")
                , pick (Tagged (Just 1))
                , pick (Circle 2)
                , unwrap (Wrap (Circle 4))
                , unwrap (Wrap (Rect { x = 3, y = 3 }))
                ]
            )
        )
"#;

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-sealedpayload-{tag}-{}-{}",
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
        "name = \"sealedpayload\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
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

/// The Go body of `func <name>(` in `src`, up to the closing brace column-0.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let needle = format!("func {name}(");
    let at = src
        .find(&needle)
        .unwrap_or_else(|| panic!("emitted Go must define {name}:\n{src}"));
    let rest = &src[at..];
    let end = rest.find("\n}").map(|i| i + 2).unwrap_or(rest.len());
    &rest[..end]
}

#[test]
fn sealed_variant_payload_fields_are_read_without_a_narrowing() {
    let dir = scratch("emit");
    let log = build(&dir);
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (log:\n{log})"
    );
    let go = std::fs::read_to_string(&main_go).unwrap();
    // The premise: both ADTs are sealed, with typed variant fields.
    for decl in [
        "type Main_Fig_Circle_V struct { V0 int }",
        "type Main_Holder_Wrap_V struct { V0 Main_Fig }",
    ] {
        assert!(go.contains(decl), "the fixture's premise: `{decl}`:\n{go}");
    }
    for f in ["Main_area", "Main_pick", "Main_unwrap"] {
        let body = func_body(&go, f);
        assert!(
            !body.contains("/* generic erase */"),
            "{f}: a sealed variant's typed field is re-narrowed:\n{body}"
        );
        assert!(body.contains(".V0"), "{f} still reads the payload:\n{body}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn sealed_variant_payload_patterns_build_and_compute() {
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
    assert!(
        combined.contains("9 10 7 3 101 6 20 9"),
        "output:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
