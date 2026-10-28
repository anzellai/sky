//! Regression: a typed `x :: xs` lowers to the typed twin `rt.List_consT[T]`
//! (amortised O(1) on the cons blocks of `runtime-go/rt/list_block.go`)
//! instead of `rt.AsListT[T](rt.List_cons(any(x), any(xs)))`, which
//! reflect-widened the whole tail on every cons: O(n) per cons, O(n²) for an
//! accumulator loop. Doc 14 R5, lever §5.3; the `List.cons` value form goes through
//! `kernel_value_eta` (§5.1). `List.reverse` over a proven slice uses
//! `rt.List_reverseT[T]`.

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
                   build : Int -> List Int -> List Int\nbuild n acc =\n    if n <= 0 then\n        acc\n\n    else\n        build (n - 1) (n :: acc)\n\n\
                   units : Int -> List () -> List ()\nunits n acc =\n    if n <= 0 then\n        acc\n\n    else\n        units (n - 1) (() :: acc)\n\n\
                   back : List Int -> List Int\nback xs =\n    List.reverse xs\n\n\
                   ends : List Int -> List Int\nends xs =\n    List.drop 1 (List.take 2 xs)\n\n\
                   myFold : (String -> List String -> List String) -> List String -> List String -> List String\nmyFold f acc xs =\n    case xs of\n        [] ->\n            acc\n\n        x :: rest ->\n            myFold f (f x acc) rest\n\n\
                   viaFold : List String -> List String\nviaFold xs =\n    myFold List.cons [] xs\n\n\
                   main =\n    ( build 3 [], units 2 [], ( back [ 1 ], viaFold [ \"a\" ], ends [ 1 ] ) )\n";

#[test]
fn a_typed_cons_uses_the_typed_twin_and_narrows_nothing() {
    let f = func_debug(SRC, "Main_build");
    assert!(f.contains("rt.List_consT"), "{f}");
    assert!(!f.contains("rt.List_cons\""), "no erased cons: {f}");
    assert!(
        !f.contains("Coerce"),
        "no AsListT narrow of the result: {f}"
    );
}

#[test]
fn a_cons_over_unit_elements_is_typed_too() {
    let f = func_debug(SRC, "Main_units");
    assert!(f.contains("rt.List_consT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn reverse_over_a_proven_slice_uses_its_typed_twin() {
    let f = func_debug(SRC, "Main_back");
    assert!(f.contains("rt.List_reverseT"), "{f}");
    assert!(!f.contains("List_reverseAny"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn the_cons_value_form_in_a_typed_slot_is_the_typed_twin() {
    let f = func_debug(SRC, "Main_viaFold");
    assert!(f.contains("rt.List_consT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn the_cons_value_form_in_an_erased_slot_keeps_the_bare_symbol() {
    // The kernel `List.foldl` takes an `any` callback: `rt.List_cons` is
    // already valid there, and over its `[]any` accumulator it is the block
    // cons (amortised O(1)) without any reflect widen.
    let src = "module Main exposing (main)\n\nf : List String -> List String\nf xs =\n    List.foldl List.cons [] xs\n\nmain =\n    f [ \"a\" ]\n";
    let f = func_debug(src, "Main_f");
    assert!(f.contains("Ident(\"rt.List_cons\")"), "{f}");
    assert!(!f.contains("FuncLit"), "{f}");
}

#[test]
fn take_and_drop_over_a_proven_slice_are_typed_reslices() {
    let f = func_debug(SRC, "Main_ends");
    assert!(
        f.contains("rt.List_takeT") && f.contains("rt.List_dropT"),
        "{f}"
    );
    assert!(!f.contains("Coerce"), "{f}");
}
