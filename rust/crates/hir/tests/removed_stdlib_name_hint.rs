//! v0.29.2: the Std.Money redesign removed `sumOf`, `addChecked`, the global
//! rate table and the currency lookups. A program that still uses one gets an
//! error that names the new API and the migration guide, both qualified
//! (`Money.sumOf`) and through `exposing (sumOf)`. Any other unknown member
//! keeps the plain message.
//!
//! The stdlib module here is a stand-in in the v0.29.2 shape, so the test does
//! not depend on the real Std.Money source.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

const MONEY: &str = "module Std.Money exposing (Money, sum)\n\n\
                     type Money\n    = Money Int\n\n\
                     sum : List Money -> Int\nsum xs =\n    0\n";

fn resolve_main(main: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Std.Money", syntax::parse(MONEY, FileId(0)));
    db.add_module("Main", syntax::parse(main, FileId(1)));
    resolve(&db, db.module_by_name("Main").unwrap())
}

fn messages(r: &ResolveResult) -> String {
    r.diagnostics
        .iter()
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect::<Vec<_>>()
        .join("\n")
}

const ANCHOR: &str = "docs/migration/v0.29.md#money-results";

#[test]
fn a_qualified_removed_name_names_its_replacement() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Money as Money\n\nmain =\n    Money.sumOf\n",
    );
    let m = messages(&r);
    assert!(m.contains("[E1001]"), "{m}");
    assert!(m.contains("Money.sum : Currency -> List Money"), "{m}");
    assert!(m.contains(ANCHOR), "{m}");
}

#[test]
fn an_exposed_removed_name_names_its_replacement() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Money exposing (setRate)\n\nmain =\n    1\n",
    );
    let m = messages(&r);
    assert!(m.contains("[E1011]"), "{m}");
    assert!(m.contains("Money.rates"), "{m}");
    assert!(m.contains(ANCHOR), "{m}");
}

#[test]
fn another_unknown_member_keeps_the_plain_message() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Money as Money\n\nmain =\n    Money.nothingHere\n",
    );
    let m = messages(&r);
    assert!(m.contains("[E1001]"), "{m}");
    assert!(!m.contains("migration"), "{m}");
}

/// v0.29.2 second Judge, gap 6: a removed name used bare after
/// `import Std.Money exposing (..)` gave only `Undefined name`.
#[test]
fn a_bare_removed_name_under_exposing_all_names_its_replacement() {
    for (name, want) in [
        ("setRate", "Money.rates"),
        ("sumOf", "Money.sum : Currency -> List Money"),
    ] {
        let r = resolve_main(&format!(
            "module Main exposing (main)\nimport Std.Money exposing (..)\n\nmain =\n    {name}\n"
        ));
        let m = messages(&r);
        assert!(m.contains("[E1001]"), "{m}");
        assert!(m.contains(want), "{name}: {m}");
        assert!(m.contains(ANCHOR), "{name}: {m}");
    }
}

/// Without an `exposing (..)` import of the module, a bare unknown name keeps
/// the plain message: `setRate` may be any program's own typo.
#[test]
fn a_bare_unknown_name_without_exposing_all_keeps_the_plain_message() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Money as Money\n\nmain =\n    setRate\n",
    );
    let m = messages(&r);
    assert!(m.contains("[E1001]"), "{m}");
    assert!(!m.contains("migration"), "{m}");
}
