//! `toFloat : Int -> Float` (Elm's Basics, added in v0.27.12) lowers to a
//! typed Go call over a proven `int`: `rt.Basics_toFloatT(n)`, a `float64`,
//! with no `any` box on the argument and no runtime narrowing of the result.
//!
//! Doc 14: origin R5 (the kernel return), lever §5.3 (typed kernel entry
//! point), floor check §1: the argument shape (`int`) and the result shape
//! (`float64`) are both known at emit time. If the twin routing regresses, the
//! call falls back to `rt.Basics_toFloat(any(n))` under an `rt.AsFloat`
//! narrowing, and this test goes red.

use base::FileId;
use codegen::emit_program;
use hir::SourceDb;

fn go_func<'a>(src: &'a str, name: &str) -> Option<&'a str> {
    let head = format!("\nfunc {name}(");
    let start = src.find(&head)? + 1;
    let rest = &src[start..];
    let end = rest.find("\n}\n")? + 3;
    Some(&rest[..end])
}

#[test]
fn to_float_over_a_proven_int_is_a_typed_call() {
    let main = "module Main exposing (main)\n\n\
                import Sky.Core.Prelude exposing (..)\n\n\
                half : Int -> Float\nhalf n =\n    toFloat n\n\n\
                qualified : Int -> Float\nqualified n =\n    Basics.toFloat (n + 1)\n\n\
                main =\n    ( half 3, qualified 4 )\n";
    let mut db = SourceDb::new();
    let mid = db.add_module("Main", syntax::parse(main, FileId(0)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    let go = emit_program(&out.items, false);

    for name in ["Main_half", "Main_qualified"] {
        let f = go_func(&go, name).unwrap_or_else(|| panic!("no `{name}` in:\n{go}"));
        assert!(
            f.contains("rt.Basics_toFloatT("),
            "`toFloat` over an `int` must call the typed twin:\n{f}"
        );
        assert!(
            !f.contains("rt.Basics_toFloat(")
                && !f.contains("rt.AsFloat(")
                && !f.contains("rt.Coerce"),
            "no erased call and no runtime narrowing:\n{f}"
        );
    }
}
