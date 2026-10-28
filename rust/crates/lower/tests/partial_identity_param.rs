//! Regression: a partially applied top-level def passed into a func slot whose
//! param is the def's own Go param type declares that param at the type and
//! passes it straight through (`lower.rs::make_partial`). Before, the closure
//! declared it `any`, `func_shape_eta` re-bound it from the slot's typed
//! param, and the call narrowed it back with `rt.Coerce[T]`: an identity
//! narrowing, since both shapes are known at emit time and equal (doc 14 §1,
//! R1). A slot whose param is `any` keeps the `any` form.

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
            _ => None,
        })
        .unwrap_or_else(|| panic!("{name} is emitted"))
}

const SRC: &str = "module Main exposing (main)\n\n\
                   type alias Step =\n    { n : Int, label : String }\n\n\
                   tag : String -> Int -> Step -> Step\ntag prefix k s =\n    { s | label = prefix ++ String.fromInt (s.n + k) }\n\n\
                   apply : (Step -> Step) -> Step -> Step\napply f s =\n    f s\n\n\
                   run : Step -> Step\nrun s =\n    apply (tag \"x\" 1) s\n\n\
                   tagAll : List Step -> List Step\ntagAll xs =\n    List.map (tag \"y\" 2) xs\n\n\
                   main =\n    (run { n = 1, label = \"a\" }).label ++ String.fromInt (List.length (tagAll []))\n";

#[test]
fn a_partial_into_a_typed_slot_declares_the_param_at_its_type() {
    let f = func_debug(SRC, "Main_run");
    assert!(f.contains("Main_tag"), "{f}");
    assert!(
        !f.contains("Coerce"),
        "an identity narrowing of the partial's param: {f}"
    );
}

#[test]
fn a_partial_into_a_typed_hof_slot_narrows_nothing() {
    let f = func_debug(SRC, "Main_tagAll");
    assert!(
        !f.contains("Coerce"),
        "an identity narrowing of the partial's param: {f}"
    );
}
