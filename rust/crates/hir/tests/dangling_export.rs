//! Regression: a module that lists a name in `exposing (…)` but does not
//! define it (v0.25.19).
//!
//! The reported shape: a contributor deleted `decodeResponse` from a module but
//! left it in the `exposing` list, and a test module still called
//! `Responses.decodeResponse body`. `sky check` accepted both. The dangling name
//! was published by `compute_exports`, so the qualified reference resolved to a
//! `DefId` with no declaration: the checker gave it a fresh type variable (the
//! use site then fixed it), lowering found no def and emitted `any(nil)` for the
//! function value, and the program panicked with `NilDereference` at run time.
//!
//! The contract pinned here:
//! * the exporter reports `[E1015]` at the dangling `exposing` entry, for values,
//!   types and constructor lists;
//! * independently, every importer form (`M.x`, `exposing (x)`, `exposing (..)`)
//!   is an unknown-name error at the use site, never a silent binding;
//! * the same holds for types (`M.Foo` in an annotation, `exposing (Foo)`);
//! * legitimate shapes (a defined export, a re-exported imported type) stay clean.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn db_with(modules: &[(&str, &str)]) -> SourceDb {
    let mut db = SourceDb::new();
    for (name, src) in modules {
        db.add_module(name, syntax::parse(src, FileId(0)));
    }
    db
}

fn resolve_named(db: &SourceDb, name: &str) -> ResolveResult {
    resolve(db, db.module_by_name(name).expect("module registered"))
}

fn codes(r: &ResolveResult) -> Vec<(String, String)> {
    r.diagnostics
        .iter()
        .map(|d| (d.code.0.to_string(), d.message.clone()))
        .collect()
}

fn has(r: &ResolveResult, code: &str, needle: &str) -> bool {
    r.diagnostics
        .iter()
        .any(|d| d.code.0 == code && d.message.contains(needle))
}

/// The exporter: lists `decode` but only defines `other`.
const RESP: &str = "module Lib.Resp exposing (decode, other)\n\n\
                    other : Int -> Int\nother x =\n    x + 1\n";

#[test]
fn exporter_reports_the_dangling_value_export() {
    let db = db_with(&[("Lib.Resp", RESP)]);
    let r = resolve_named(&db, "Lib.Resp");
    assert!(
        has(
            &r,
            "E1015",
            "module `Lib.Resp` exposes `decode`, but does not define it"
        ),
        "expected [E1015] naming `decode` and `Lib.Resp`, got {:?}",
        codes(&r)
    );
    // Exactly one: `other` is defined and must not be reported.
    let n = r.diagnostics.iter().filter(|d| d.code.0 == "E1015").count();
    assert_eq!(n, 1, "{:?}", codes(&r));
}

#[test]
fn qualified_use_of_a_dangling_export_is_an_unknown_name() {
    let main = "module Main exposing (main)\n\
                import Lib.Resp as Resp\n\n\
                main =\n    Resp.decode \"hi\"\n";
    let db = db_with(&[("Lib.Resp", RESP), ("Main", main)]);
    let r = resolve_named(&db, "Main");
    assert!(
        has(&r, "E1001", "Undefined name: Resp.decode")
            && has(&r, "E1001", "lists `decode` in its `exposing` clause"),
        "expected [E1001] at `Resp.decode` naming the dangling export, got {:?}",
        codes(&r)
    );
    assert!(
        r.class_a.iter().any(|c| c.name == "decode"),
        "the reference must be a class-(a) miss, never a silent binding"
    );
}

#[test]
fn explicit_import_of_a_dangling_export_is_not_exposed() {
    let main = "module Main exposing (main)\n\
                import Lib.Resp exposing (decode)\n\n\
                main =\n    decode \"hi\"\n";
    let db = db_with(&[("Lib.Resp", RESP), ("Main", main)]);
    let r = resolve_named(&db, "Main");
    assert!(
        has(&r, "E1011", "does not expose `decode`") && has(&r, "E1011", "does not define it"),
        "{:?}",
        codes(&r)
    );
}

#[test]
fn expose_all_import_does_not_bind_a_dangling_export() {
    let main = "module Main exposing (main)\n\
                import Lib.Resp exposing (..)\n\n\
                main =\n    decode \"hi\"\n";
    let db = db_with(&[("Lib.Resp", RESP), ("Main", main)]);
    let r = resolve_named(&db, "Main");
    assert!(
        has(&r, "E1001", "Undefined name: decode"),
        "{:?}",
        codes(&r)
    );
}

#[test]
fn dangling_type_and_constructor_exports_are_reported() {
    let src = "module Lib.Shapes exposing (Missing, Alias(..), Shape(Circle, Hexagon), area)\n\n\
               type alias Alias =\n    { w : Int }\n\n\
               type Shape\n    = Circle Int\n    | Square Int\n\n\
               area : Shape -> Int\narea s =\n    1\n";
    let db = db_with(&[("Lib.Shapes", src)]);
    let r = resolve_named(&db, "Lib.Shapes");
    assert!(
        has(
            &r,
            "E1015",
            "exposes the type `Missing`, but does not define or import it"
        ),
        "{:?}",
        codes(&r)
    );
    assert!(
        has(&r, "E1015", "exposes constructors of `Alias`"),
        "{:?}",
        codes(&r)
    );
    assert!(
        has(&r, "E1015", "`Hexagon` is not a constructor of `Shape`"),
        "{:?}",
        codes(&r)
    );
    // `Circle` is a real constructor and `area` a real value.
    let n = r.diagnostics.iter().filter(|d| d.code.0 == "E1015").count();
    assert_eq!(n, 3, "{:?}", codes(&r));
}

#[test]
fn qualified_type_the_module_does_not_export_is_an_unknown_name() {
    let main = "module Main exposing (main)\n\
                import Lib.Resp as Resp\n\n\
                f : Resp.Bar -> Int\nf x =\n    1\n\n\
                g : Nope.Foo -> Int\ng x =\n    2\n\n\
                main =\n    Resp.other 1\n";
    let db = db_with(&[("Lib.Resp", RESP), ("Main", main)]);
    let r = resolve_named(&db, "Main");
    assert!(
        has(&r, "E1001", "Undefined name: Resp.Bar"),
        "{:?}",
        codes(&r)
    );
    assert!(
        has(&r, "E1001", "Undefined name: Nope.Foo"),
        "{:?}",
        codes(&r)
    );
}

#[test]
fn explicit_import_of_an_unlisted_type_is_not_exposed() {
    let main = "module Main exposing (main)\n\
                import Lib.Resp exposing (Bar)\n\n\
                main =\n    1\n";
    let db = db_with(&[("Lib.Resp", RESP), ("Main", main)]);
    let r = resolve_named(&db, "Main");
    assert!(
        has(&r, "E1011", "does not expose the type `Bar`"),
        "{:?}",
        codes(&r)
    );
}

#[test]
fn defined_exports_and_reexported_types_stay_clean() {
    let base_src = "module Lib.Base exposing (Easing(..), linear)\n\n\
                    type Easing\n    = Linear\n    | Ease\n\n\
                    linear : Easing\nlinear =\n    Linear\n";
    // Re-exposes the imported `Easing` type (with `(..)`): a supported type
    // re-export; the Sky.Spa split emits it for a union `Shared` owns.
    let anim = "module Lib.Anim exposing (Easing(..), fast)\n\
                import Lib.Base exposing (Easing)\n\n\
                fast : Int\nfast =\n    1\n";
    let main = "module Main exposing (main)\n\
                import Lib.Anim as Anim\n\n\
                pick : Anim.Easing -> Int\npick e =\n    Anim.fast\n\n\
                main =\n    Anim.fast\n";
    let db = db_with(&[("Lib.Base", base_src), ("Lib.Anim", anim), ("Main", main)]);
    for m in ["Lib.Base", "Lib.Anim", "Main"] {
        let r = resolve_named(&db, m);
        assert!(r.diagnostics.is_empty(), "{m}: {:?}", codes(&r));
    }
}
