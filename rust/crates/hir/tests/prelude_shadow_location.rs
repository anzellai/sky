//! Regression (C-16): `[E1004]` (a declared type or constructor shadows a
//! Prelude name) carries the declaration's location.
//!
//! Before the fix the three `[E1004]` sites pushed a diagnostic with no label,
//! so `sky check` printed `-- SHADOWED NAME ---- [E1004]` with no file, line or
//! source excerpt. In a project with many modules the user had to search for
//! the offending `type Error` by hand.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn resolve_named(modules: &[(&str, &str)], name: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    for (i, (m, src)) in modules.iter().enumerate() {
        db.add_module(m, syntax::parse(src, FileId(i as u32)));
    }
    resolve(&db, db.module_by_name(name).expect("module registered"))
}

fn e1004_labels(r: &ResolveResult, src: &str) -> Vec<String> {
    let ds: Vec<_> = r
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1004")
        .collect();
    assert!(!ds.is_empty(), "expected [E1004], got {:?}", r.diagnostics);
    ds.iter()
        .map(|d| {
            assert_eq!(
                d.labels.len(),
                1,
                "[E1004] must point at the declaration: {d:?}"
            );
            let (s, e) = d.labels[0].span.range;
            src[s as usize..e as usize].to_string()
        })
        .collect()
}

const API: &str = "module Github.Api exposing (..)\n\n\
                   type Error\n    = NotFound\n    | Nothing\n\n\
                   type alias Maybe =\n    { x : Int }\n";

#[test]
fn shadowing_type_ctor_and_alias_each_point_at_their_name() {
    let r = resolve_named(&[("Github.Api", API)], "Github.Api");
    let mut got = e1004_labels(&r, API);
    got.sort();
    assert_eq!(
        got,
        vec!["Error", "Maybe", "Nothing"],
        "{:?}",
        r.diagnostics
    );
}

#[test]
fn a_non_shadowing_declaration_raises_nothing() {
    let src = "module Github.Api exposing (..)\n\ntype ApiError\n    = NotFound\n";
    let r = resolve_named(&[("Github.Api", src)], "Github.Api");
    assert!(
        r.diagnostics.iter().all(|d| d.code.0 != "E1004"),
        "{:?}",
        r.diagnostics
    );
}
