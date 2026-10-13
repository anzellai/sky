//! Regression: a constructor name used by two types of one module is refused
//! with `[E1002]` at the second declaration.
//!
//! Found while writing `Std.Ai.Device` (v0.28.0): a `Reason` variant
//! `NotDownloaded` and a `Status` variant `NotDownloaded { … }` in the same
//! module type-checked, and the emitted Go failed `go build` with `too many
//! arguments in call to …_Reason_NotDownloaded`. In a user module the same
//! defect shows as a confusing `Int -> B vs B` type mismatch. Elm refuses the
//! second definition; Sky now does too.
//!
//! Each rejection has an accepted twin that differs only in the defect.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn resolve_main(src: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Main", syntax::parse(src, FileId(0)));
    resolve(&db, db.module_by_name("Main").expect("module registered"))
}

fn e1002(r: &ResolveResult) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1002")
        .map(|d| d.message.clone())
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\n";

#[test]
fn one_constructor_name_in_two_types_is_refused() {
    let src = format!(
        "{HEAD}type A\n    = Same\n    | Other\n\ntype B\n    = Same Int\n    | Third\n\nmain =\n    1\n"
    );
    let r = resolve_main(&src);
    let msgs = e1002(&r);
    assert_eq!(
        msgs.len(),
        1,
        "expected one [E1002], got {:?}",
        r.diagnostics
    );
    assert!(
        msgs[0].contains("`Same`") && msgs[0].contains("`A`") && msgs[0].contains("`B`"),
        "{}",
        msgs[0]
    );
    let d = r.diagnostics.iter().find(|d| d.code.0 == "E1002").unwrap();
    assert_eq!(
        d.labels.len(),
        1,
        "one label at the second constructor: {d:?}"
    );
}

#[test]
fn one_constructor_twice_in_one_type_is_refused() {
    let src = format!("{HEAD}type A\n    = Same\n    | Same Int\n\nmain =\n    1\n");
    let msgs = e1002(&resolve_main(&src));
    assert_eq!(msgs.len(), 1, "{msgs:?}");
    assert!(msgs[0].contains("more than once"), "{}", msgs[0]);
}

#[test]
fn distinct_constructor_names_are_accepted() {
    let src = format!(
        "{HEAD}type A\n    = Same\n    | Other\n\ntype B\n    = Different Int\n    | Third\n\nmain =\n    1\n"
    );
    let r = resolve_main(&src);
    assert!(e1002(&r).is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn a_constructor_named_like_its_type_is_accepted() {
    // `type Box = Box Int` is the usual single-variant wrapper.
    let src = format!(
        "{HEAD}type Box\n    = Box Int\n\ntype Pair\n    = Pair Int Int\n\nmain =\n    1\n"
    );
    let r = resolve_main(&src);
    assert!(e1002(&r).is_empty(), "{:?}", r.diagnostics);
}
