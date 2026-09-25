//! Regression: lowering never emits `nil` for a referenced top-level value it
//! cannot find (v0.25.19).
//!
//! A module listed `decode` in `exposing (…)` without defining it, and a caller
//! wrote `Resp.decode "hi"`. The reference reached lowering as a `Res::Def` with
//! no definition behind it, and `lower_var` fell back to `nil`. `go build`
//! accepted it (`_s := any(nil)`), and the program panicked (`NilDereference`)
//! the first time the call ran.
//!
//! The resolver now rejects that program ([E1015] / [E1001]), so the reference
//! arrives as `Res::Error` instead. This test drives lowering DIRECTLY, without
//! the build driver's diagnostic halt, to pin the second line of defence: any
//! unresolved reference that reaches lowering, by whatever future path, is a
//! hard lowering error (an internal compiler error), never a silent `nil`.

use base::FileId;
use hir::SourceDb;

#[test]
fn unresolved_reference_is_an_ice_not_a_nil() {
    let resp = "module Lib.Resp exposing (decode, other)\n\n\
                other : Int -> Int\nother x =\n    x + 1\n";
    let main = "module Main exposing (main)\n\
                import Lib.Resp as Resp\n\n\
                main =\n    Resp.decode \"hi\"\n";
    let mut db = SourceDb::new();
    db.add_module("Lib.Resp", syntax::parse(resp, FileId(0)));
    let mid = db.add_module("Main", syntax::parse(main, FileId(1)));

    let out = lower::lower_program(&db, mid);
    assert!(
        out.errors
            .iter()
            .any(|e| e.contains("internal compiler error") && e.contains("refuses to emit")),
        "an unresolved reference must be a hard lowering error, got errors {:?} \
         and warnings {:?}",
        out.errors,
        out.warnings
    );
}
