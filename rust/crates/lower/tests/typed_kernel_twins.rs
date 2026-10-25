//! Regression: a kernel call whose runtime twin already returns the Sky
//! result's Go type is routed to that twin, so the call site narrows nothing
//! (doc 14 R5, lever §5.3; `lower.rs::kernel_typed_return_twin`). A twin
//! parameter that takes a concrete Go type is used only when the argument
//! lowers to exactly that type (§1: both shapes known at emit time).
//!
//! And an empty list literal in a typed slot is built at the slot's slice type
//! instead of as `[]any{}` narrowed back (R1, lever §5.2).

use base::FileId;
use hir::SourceDb;
use lower::ir::GoItem;

fn func_debug(src: &str, name: &str) -> String {
    let mut db = SourceDb::new();
    let mid = db.add_module("Main", syntax::parse(src, FileId(0)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    out.items
        .iter()
        .find_map(|it| match it {
            GoItem::Func(f) if f.name == name => Some(format!("{f:?}")),
            GoItem::Var(n, _, _) if n == name => Some(format!("{it:?}")),
            _ => None,
        })
        .unwrap_or_else(|| panic!("{name} is emitted"))
}

const SRC: &str = "module Main exposing (main)\n\n\
                   label : Int -> String\nlabel n =\n    String.fromInt (n + 1)\n\n\
                   shout : String -> String\nshout s =\n    String.toUpper s\n\n\
                   tail2 : String -> String\ntail2 s =\n    String.dropRight 2 s\n\n\
                   type alias Box =\n    { items : List Int }\n\n\
                   emptyBox : Box\nemptyBox =\n    { items = [] }\n\n\
                   main =\n    label (List.length (emptyBox.items)) ++ shout \"a\" ++ tail2 \"abc\"\n";

#[test]
fn a_typed_int_argument_uses_the_int_twin() {
    let f = func_debug(SRC, "Main_label");
    assert!(f.contains("rt.String_fromIntT"), "{f}");
    assert!(!f.contains("Coerce"), "no narrowing of the result: {f}");
}

#[test]
fn a_typed_string_argument_uses_the_string_twin() {
    let f = func_debug(SRC, "Main_shout");
    assert!(f.contains("rt.String_toUpperT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn an_erased_argument_twin_returns_a_typed_string() {
    let f = func_debug(SRC, "Main_tail2");
    assert!(f.contains("rt.String_dropRightT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn an_empty_list_in_a_typed_field_is_built_at_the_field_type() {
    let f = func_debug(SRC, "Main_emptyBox");
    assert!(!f.contains("Coerce"), "`[]` must not be narrowed: {f}");
}

const MATH_SRC: &str = "module Main exposing (main)\n\n\
                        root : Float -> Float\nroot x =\n    Math.sqrt x\n\n\
                        wrap : Int -> Int\nwrap n =\n    modBy 7 n\n\n\
                        lesser : Float -> Float -> Float\nlesser a b =\n    Math.min a b\n\n\
                        fewer : Int -> Int -> Int\nfewer a b =\n    Math.min a b\n\n\
                        main =\n    ( root 2.0, wrap 3, ( lesser 1.0 2.0, fewer 1 2 ) )\n";

#[test]
fn a_typed_float_argument_uses_the_math_twin() {
    let f = func_debug(MATH_SRC, "Main_root");
    assert!(f.contains("rt.Math_sqrtT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn mod_by_on_ints_uses_its_twin() {
    let f = func_debug(MATH_SRC, "Main_wrap");
    assert!(f.contains("rt.Basics_modByT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn min_picks_the_twin_for_its_argument_types() {
    let f = func_debug(MATH_SRC, "Main_lesser");
    assert!(f.contains("rt.Math_minFloatT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
    let g = func_debug(MATH_SRC, "Main_fewer");
    assert!(g.contains("rt.Math_minT"), "{g}");
    assert!(!g.contains("Coerce"), "{g}");
}

const DB_SRC: &str = "module Main exposing (main)\n\n\
                      import Std.Db as Db\n\n\
                      name : row -> String\nname r =\n    Db.getString \"name\" r\n\n\
                      size : row -> Int\nsize r =\n    Db.getInt \"size\" r\n\n\
                      main =\n    name 1 ++ String.fromInt (size 1)\n";

#[test]
fn a_db_column_read_narrows_nothing() {
    // v0.28.0: `Db.getString` / `getInt` (and `getField`, `getFloat`,
    // `getBool`) return their Go type, so a column read is not narrowed.
    for (f, sym) in [
        ("Main_name", "rt.Db_getString"),
        ("Main_size", "rt.Db_getInt"),
    ] {
        let body = func_debug(DB_SRC, f);
        assert!(body.contains(sym), "{body}");
        assert!(
            !body.contains("Coerce"),
            "no narrowing of a column read: {body}"
        );
    }
}

const STR_SRC: &str = "module Main exposing (main)\n\n\
                       import Sky.Core.Regex as Regex\n\n\
                       size : String -> Int\nsize s =\n    String.length s\n\n\
                       tidy : String -> String\ntidy s =\n    String.replace \"a\" \"b\" (String.trim s)\n\n\
                       digits : String -> Bool\ndigits s =\n    Regex.match \"^[0-9]+$\" s\n\n\
                       main =\n    ( size \"x\", tidy \" a \", digits \"12\" )\n";

#[test]
fn length_trim_replace_and_match_use_their_twins() {
    for (f, sym) in [
        ("Main_size", "rt.String_lengthT"),
        ("Main_tidy", "rt.String_replaceT"),
        ("Main_tidy", "rt.String_trimT"),
        ("Main_digits", "rt.Regex_matchT"),
    ] {
        let body = func_debug(STR_SRC, f);
        assert!(body.contains(sym), "{sym}: {body}");
        assert!(!body.contains("Coerce"), "{body}");
    }
}

const DIV_SRC: &str = "module Main exposing (main)\n\n\
                       ratio : Float -> Float -> Float\nratio a b =\n    a / b\n\n\
                       half : Float -> Float\nhalf x =\n    x / 2.0\n\n\
                       main =\n    ( ratio 1.0 2.0, half 3.0 )\n";

// Float `/` with both operands statically `Float` is the typed `rt.DivF`
// (doc 14 R5, lever §5.3): no `any` boxing of the operands and no narrowing of
// the result. It keeps the classified divide-by-zero panic of `rt.Div`.
#[test]
fn float_division_of_typed_operands_uses_the_float_twin() {
    for name in ["Main_ratio", "Main_half"] {
        let f = func_debug(DIV_SRC, name);
        assert!(f.contains("rt.DivF"), "{name}: {f}");
        assert!(
            !f.contains("\"rt.Div\""),
            "{name}: the erased rt.Div is gone: {f}"
        );
        assert!(
            !f.contains("Coerce"),
            "{name}: no narrowing of the result: {f}"
        );
    }
}
