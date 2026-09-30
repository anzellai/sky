//! Regression (C-13): a recursive `type alias` is refused at its declaration
//! with `[E1016]`.
//!
//! Before the fix, `type alias Node = { name : String, next : Maybe Node }`
//! passed `sky check` when the alias was only used as a value, and the emitted
//! Go failed `go build` with `invalid recursive type Main_Node_R`. Used by a
//! recursive function it gave a confusing `record vs Node` type mismatch. Elm
//! refuses the declaration and suggests a `type` wrapper; Sky now does too.
//!
//! Every rejection is paired with an accepted twin that differs only in the
//! defect, so a resolver that rejected every alias would fail the twin.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn resolve_main(src: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Main", syntax::parse(src, FileId(0)));
    resolve(&db, db.module_by_name("Main").expect("module registered"))
}

fn e1016(r: &ResolveResult) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1016")
        .map(|d| d.message.clone())
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\n";

#[test]
fn directly_recursive_record_alias_is_refused() {
    let src = format!(
        "{HEAD}type alias Node =\n    {{ name : String, next : Maybe Node }}\n\n\
         main =\n    1\n"
    );
    let r = resolve_main(&src);
    let msgs = e1016(&r);
    assert_eq!(
        msgs.len(),
        1,
        "expected one [E1016], got {:?}",
        r.diagnostics
    );
    assert!(msgs[0].contains("`Node`"), "{}", msgs[0]);
    // The fix hint names the wrapper the user should write.
    assert!(msgs[0].contains("type Node = Node"), "{}", msgs[0]);
    assert!(
        msgs[0].ends_with("See docs/migration/v0.27.md#recursive-type-alias"),
        "{}",
        msgs[0]
    );
    // The diagnostic points at the declaration (C-16's lesson: no location-less
    // errors).
    let d = r.diagnostics.iter().find(|d| d.code.0 == "E1016").unwrap();
    assert_eq!(d.labels.len(), 1, "one label at the alias name: {d:?}");
}

#[test]
fn mutually_recursive_aliases_are_refused_with_the_chain() {
    let src = format!(
        "{HEAD}type alias A =\n    {{ b : List B }}\n\n\
         type alias B =\n    {{ a : Maybe A }}\n\n\
         main =\n    1\n"
    );
    let r = resolve_main(&src);
    let msgs = e1016(&r);
    assert_eq!(
        msgs.len(),
        2,
        "both aliases are in the cycle: {:?}",
        r.diagnostics
    );
    assert!(
        msgs.iter().any(|m| m.contains("A -> B -> A")),
        "the message names the chain: {msgs:?}"
    );
}

#[test]
fn recursion_through_a_custom_type_is_accepted() {
    // The accepted twin: the recursion goes through a `type`, which is named,
    // so the alias itself is not recursive.
    let src = format!(
        "{HEAD}type Tree =\n    Tree {{ label : String, kids : List Tree }}\n\n\
         type alias Root =\n    {{ tree : Tree, size : Int }}\n\n\
         main =\n    1\n"
    );
    let r = resolve_main(&src);
    assert!(e1016(&r).is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn an_alias_using_another_alias_is_accepted() {
    let src = format!(
        "{HEAD}type alias Point =\n    {{ x : Int, y : Int }}\n\n\
         type alias Line =\n    {{ from : Point, to : Point }}\n\n\
         main =\n    1\n"
    );
    let r = resolve_main(&src);
    assert!(e1016(&r).is_empty(), "{:?}", r.diagnostics);
}
