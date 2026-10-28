//! Regression: the wider-nominal check on an OPEN record row looks only at
//! record aliases the program can actually hold (doc 14 §9.8, origin R10/R11).
//!
//! An unannotated `viewCounter c = c.count + 1` lowers its parameter with the
//! open row `{ count : Int | r }`. `goty` resolves such a row to the program's
//! own alias `Counter { count }` unless some OTHER record alias collects
//! `count` with more fields: a value of that wider alias could then reach the
//! row, and converting it to `Counter` would drop its other fields.
//!
//! The build loads the whole stdlib, so `record_templates` held every record
//! alias of every stdlib module. A new stdlib alias with a `count` field
//! (`Std.Ai.Eval.ScorerScore { scorer, mean, count }`) then widened the
//! `09-live-counter` view to `any` + `rt.Field`, although that program never
//! loads `Std.Ai.Eval`: no value of that alias can exist in it.
//!
//! The check now considers only aliases declared in modules the program can
//! reach (its transitive import closure, plus the modules behind kernel
//! qualifiers and the bare kernel-implicit type names). The reverse case is
//! pinned too: when the program DOES import the wider alias's module, the row
//! still takes the reflective path.

use base::FileId;
use hir::SourceDb;
use lower::ir::{GoItem, GoTy};

const LIB: &str = "module Lib.Score exposing (..)\n\n\
                   type alias ScorerScore =\n    { scorer : String, mean : Float, count : Int }\n\n\
                   zero : ScorerScore\nzero =\n    { scorer = \"x\", mean = 0.0, count = 0 }\n";

fn view_counter_param(main: &str) -> GoTy {
    let mut db = SourceDb::new();
    db.add_module("Lib.Score", syntax::parse(LIB, FileId(0)));
    let mid = db.add_module("Main", syntax::parse(main, FileId(1)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    out.items
        .iter()
        .find_map(|it| match it {
            GoItem::Func(f) if f.name == "Main_viewCounter" => Some(f.params[0].ty.clone()),
            _ => None,
        })
        .expect("Main_viewCounter is emitted")
}

#[test]
fn an_unloaded_modules_wider_alias_does_not_widen_the_row() {
    let main = "module Main exposing (main)\n\n\
                type alias Counter =\n    { count : Int }\n\n\
                viewCounter c =\n    c.count + 1\n\n\
                main =\n    viewCounter { count = 1 }\n";
    let ty = view_counter_param(main);
    assert_eq!(
        ty,
        GoTy::Named("Main_Counter_R".into(), vec![]),
        "`Lib.Score` is never loaded by this program, so its `ScorerScore` \
         cannot reach `viewCounter`; the row must resolve to `Counter`"
    );
}

#[test]
fn an_imported_wider_alias_still_widens_the_row() {
    let main = "module Main exposing (main)\n\n\
                import Lib.Score as Score\n\n\
                type alias Counter =\n    { count : Int }\n\n\
                viewCounter c =\n    c.count + 1\n\n\
                main =\n    viewCounter Score.zero\n";
    let ty = view_counter_param(main);
    assert_eq!(
        ty,
        GoTy::Any,
        "`Score.zero` is a `ScorerScore` and flows into `viewCounter`; resolving \
         the row to `Counter` would drop `scorer` and `mean`"
    );
}

#[test]
fn a_transitively_loaded_wider_alias_still_widens_the_row() {
    // Main never names `Lib.Score`, but `Lib.Mid` does and hands its value on.
    let mid_src = "module Lib.Mid exposing (..)\n\n\
                   import Lib.Score as Score\n\n\
                   pick x =\n    x\n\n\
                   value =\n    pick Score.zero\n";
    let main = "module Main exposing (main)\n\n\
                import Lib.Mid as Mid\n\n\
                type alias Counter =\n    { count : Int }\n\n\
                viewCounter c =\n    c.count + 1\n\n\
                main =\n    viewCounter Mid.value\n";
    let mut db = SourceDb::new();
    db.add_module("Lib.Score", syntax::parse(LIB, FileId(0)));
    db.add_module("Lib.Mid", syntax::parse(mid_src, FileId(2)));
    let mid = db.add_module("Main", syntax::parse(main, FileId(1)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    let ty = out
        .items
        .iter()
        .find_map(|it| match it {
            GoItem::Func(f) if f.name == "Main_viewCounter" => Some(f.params[0].ty.clone()),
            _ => None,
        })
        .expect("Main_viewCounter is emitted");
    assert_eq!(
        ty,
        GoTy::Any,
        "a module reached through `Lib.Mid` is loaded"
    );
}

/// Lower `MAIN_NO_IMPORT` next to one extra module `name` whose source is
/// `src`, and return `viewCounter`'s parameter type. `Main` imports nothing.
fn param_with_unimported(name: &str, src: &str) -> GoTy {
    const MAIN_NO_IMPORT: &str = "module Main exposing (main)\n\n\
                                  type alias Counter =\n    { count : Int }\n\n\
                                  viewCounter c =\n    c.count + 1\n\n\
                                  main =\n    viewCounter { count = 1 }\n";
    let mut db = SourceDb::new();
    db.add_module(name, syntax::parse(src, FileId(0)));
    let mid = db.add_module("Main", syntax::parse(MAIN_NO_IMPORT, FileId(1)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    out.items
        .iter()
        .find_map(|it| match it {
            GoItem::Func(f) if f.name == "Main_viewCounter" => Some(f.params[0].ty.clone()),
            _ => None,
        })
        .expect("Main_viewCounter is emitted")
}

#[test]
fn a_kernel_modules_wider_alias_widens_without_an_import() {
    // `Std.Db` backs the kernel qualifier `Db`: `Db.someCall` resolves with no
    // `import` line, and its result type comes from this module's signatures.
    let src = "module Std.Db exposing (..)\n\n\
               type alias Row =\n    { count : Int, table : String }\n";
    assert_eq!(param_with_unimported("Std.Db", src), GoTy::Any);
}

#[test]
fn a_kernel_implicit_named_alias_widens_without_an_import() {
    // A bare `Request` is a kernel-implicit type name: it expands through the
    // last-writer bare alias table, which can name an alias in any module.
    let src = "module Lib.Web exposing (..)\n\n\
               type alias Request =\n    { count : Int, path : String }\n";
    assert_eq!(param_with_unimported("Lib.Web", src), GoTy::Any);
}

#[test]
fn an_ordinary_unimported_alias_does_not_widen() {
    let src = "module Lib.Web exposing (..)\n\n\
               type alias Tally =\n    { count : Int, path : String }\n";
    assert_eq!(
        param_with_unimported("Lib.Web", src),
        GoTy::Named("Main_Counter_R".into(), vec![])
    );
}
