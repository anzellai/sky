//! Regression (C-14): `import Sky.Core.Prelude exposing (String, Int)` names the
//! builtin types, so it is the SAME binding as the ambient builtin.
//!
//! Before the fix the kernel pseudo-module path keyed the binding
//! `kernel-implicit:String` while the ambient builtin was keyed by its `DefId`.
//! Both entered the ambient layer, the keys differed, and every use of `String`
//! was `[E1012] Ambiguous type String — brought into scope by Main and
//! Sky.Core.Prelude`, although both named one interned `DefId`.
//!
//! The twin keeps the ambiguity rule honest: two genuinely different types
//! bound in one layer are still `[E1012]`.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn resolve_main(modules: &[(&str, &str)]) -> ResolveResult {
    let mut db = SourceDb::new();
    for (name, src) in modules {
        db.add_module(name, syntax::parse(src, FileId(0)));
    }
    resolve(&db, db.module_by_name("Main").expect("module registered"))
}

fn e1012(r: &ResolveResult) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1012")
        .map(|d| d.message.clone())
        .collect()
}

#[test]
fn prelude_exposing_builtin_types_is_not_ambiguous() {
    let main = "module Main exposing (main)\n\n\
                import Sky.Core.Prelude exposing (Result(..), String, Int)\n\n\
                type alias R =\n    { name : String, n : Int }\n\n\
                r : R\nr =\n    { name = \"ok\", n = 1 }\n\n\
                main =\n    r.n\n";
    let r = resolve_main(&[("Main", main)]);
    assert!(e1012(&r).is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn two_different_types_in_one_layer_stay_ambiguous() {
    let a = "module Lib.A exposing (..)\n\ntype Thing\n    = A\n";
    let b = "module Lib.B exposing (..)\n\ntype Thing\n    = B\n";
    let main = "module Main exposing (main)\n\n\
                import Lib.A exposing (..)\n\
                import Lib.B exposing (..)\n\n\
                f : Thing -> Int\nf _ =\n    1\n\n\
                main =\n    1\n";
    let r = resolve_main(&[("Lib.A", a), ("Lib.B", b), ("Main", main)]);
    let msgs = e1012(&r);
    assert_eq!(msgs.len(), 1, "{:?}", r.diagnostics);
    assert!(msgs[0].contains("`Thing`"), "{}", msgs[0]);
}
