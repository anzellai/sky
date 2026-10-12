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
