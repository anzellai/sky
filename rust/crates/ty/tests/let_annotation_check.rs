//! Regression: a type annotation on a `let` binding is checked.
//!
//! Before v0.27.14 the resolver resolved a let annotation's type names and then
//! dropped it, so `let n : Int` / `n = "not an int"` checked clean. A let
//! definition is now checked against its annotation the way a top-level one
//! is: the annotation is a scheme whose variables are rigid inside the
//! definition (the body must be at least as general), and the binder has the
//! annotated type in the `let` body, generalised under the value restriction.
//!
//! Each rejection has an accepted twin, so a checker that rejected every
//! annotated let would fail. Covers the doc 13 D3 rows that apply: annotated
//! vs unannotated, polymorphic vs monomorphic, an alias, a record row, a
//! cross-module alias, and a nested let.

use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

/// Error messages (`[code] message`) of `main` plus any extra modules.
fn errors_with(main: &str, extra: &[(&str, &str)]) -> Vec<String> {
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    let mut db = hir::SourceDb::new();
    for path in files {
        let src = std::fs::read_to_string(&path).unwrap();
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .unwrap_or_default();
        if !name.is_empty() {
            db.add_module(&name, parse);
        }
    }
    let mut ids = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, base::FileId(2 + i as u32))));
    }
    let mid = db.add_module("Main", syntax::parse(main, base::FileId(1)));
    ids.push(mid);
    ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Std.Log exposing (println)\n\n\
    type alias P =\n    { a : String, b : String }\n\n";

fn errors(body: &str) -> Vec<String> {
    errors_with(&format!("{HEAD}{body}"), &[])
}

fn clean(body: &str) {
    let errs = errors(body);
    assert!(errs.is_empty(), "must check clean: {errs:#?}");
}

fn rejected(body: &str) -> Vec<String> {
    let errs = errors(body);
    assert!(
        errs.iter().any(|e| e.starts_with("[E2001]")),
        "must be rejected with [E2001]: {errs:#?}"
    );
    errs
}

/// Drop the `[def] ` prefix the checker puts before a message.
fn core(msg: &str) -> String {
    let rest = msg.strip_prefix("[E2001] ").unwrap_or(msg);
    match rest.strip_prefix('[') {
        Some(r) => r
            .split_once("] ")
            .map(|(_, m)| m.to_string())
            .unwrap_or(rest.to_string()),
        None => rest.to_string(),
    }
}

// ---- monomorphic ------------------------------------------------------------

#[test]
fn a_monomorphic_let_annotation_mismatch_is_rejected_like_a_top_level_one() {
    let errs = rejected(
        "main =\n    let\n        n : Int\n        n =\n            \"not an int\"\n    in\n    println (String.fromInt n)\n",
    );
    let top =
        rejected("n : Int\nn =\n    \"not an int\"\n\nmain =\n    println (String.fromInt n)\n");
    assert_eq!(errs.len(), 1, "one wrong annotation, one error: {errs:#?}");
    assert_eq!(core(&errs[0]), core(&top[0]), "same wording as top level");
}

#[test]
fn a_correct_monomorphic_let_annotation_checks_clean() {
    clean("main =\n    let\n        n : Int\n        n =\n            3\n    in\n    println (String.fromInt n)\n");
}

#[test]
fn an_annotated_let_function_is_used_at_its_annotated_type() {
    clean(
        "main =\n    let\n        g : Int -> String\n        g n =\n            String.fromInt n\n    in\n    println (g 1)\n",
    );
    rejected(
        "main =\n    let\n        g : Int -> String\n        g n =\n            String.fromInt n\n    in\n    println (g \"x\")\n",
    );
}

#[test]
fn a_let_function_whose_body_contradicts_its_annotation_is_rejected() {
    rejected(
        "main =\n    let\n        g : Int -> Int\n        g n =\n            String.fromInt n\n    in\n    println \"unused\"\n",
    );
}

// ---- polymorphic ------------------------------------------------------------

#[test]
fn a_polymorphic_let_function_is_usable_at_two_types() {
    clean(
        "main =\n    let\n        ident : a -> a\n        ident x =\n            x\n    in\n    println (ident \"s\" ++ String.fromInt (ident 1))\n",
    );
}

#[test]
fn a_too_general_let_annotation_is_rejected() {
    rejected(
        "main =\n    let\n        f : a -> a\n        f x =\n            x + 1\n    in\n    println (String.fromInt (f 1))\n",
    );
}

#[test]
fn a_let_annotation_that_pins_a_result_variable_is_rejected() {
    rejected(
        "main =\n    let\n        mk : String -> a\n        mk s =\n            s\n    in\n    println (mk \"x\")\n",
    );
}

#[test]
fn an_unannotated_polymorphic_let_function_still_generalises() {
    clean(
        "main =\n    let\n        ident x =\n            x\n    in\n    println (ident \"s\" ++ String.fromInt (ident 1))\n",
    );
}

// ---- alias and record row ---------------------------------------------------

#[test]
fn an_alias_annotation_missing_a_field_is_rejected() {
    let errs = rejected(
        "main =\n    let\n        p : P\n        p =\n            { a = \"x\" }\n    in\n    println p.a\n",
    );
    assert!(
        errs.iter()
            .any(|e| e.contains("record is missing field(s): b")),
        "{errs:#?}"
    );
}

#[test]
fn a_correct_alias_annotation_checks_clean() {
    clean("main =\n    let\n        p : P\n        p =\n            { a = \"x\", b = \"y\" }\n    in\n    println (p.a ++ p.b)\n");
}

#[test]
fn a_record_annotation_with_an_unknown_field_is_rejected() {
    let errs = rejected(
        "main =\n    let\n        r : { a : String }\n        r =\n            { a = \"x\", b = \"y\" }\n    in\n    println r.a\n",
    );
    assert!(
        errs.iter()
            .any(|e| e.contains("record has unknown field(s) not in the declared type: b")),
        "{errs:#?}"
    );
}

#[test]
fn a_record_annotation_closes_the_binder() {
    // The accepted twin of the unknown-field case.
    clean("main =\n    let\n        r : { a : String }\n        r =\n            { a = \"x\" }\n    in\n    println r.a\n");
}

// ---- nested let -------------------------------------------------------------

#[test]
fn a_wrong_annotation_in_a_nested_let_is_rejected() {
    rejected(
        "main =\n    let\n        outer : Int -> String\n        outer k =\n            let\n                inner : String\n                inner =\n                    k + 1\n            in\n            String.fromInt k\n    in\n    println (outer 1)\n",
    );
}

#[test]
fn a_correct_nested_let_annotation_checks_clean() {
    clean(
        "main =\n    let\n        outer : Int -> String\n        outer k =\n            let\n                inner : Int\n                inner =\n                    k + 1\n            in\n            String.fromInt inner\n    in\n    println (outer 1)\n",
    );
}

// ---- cross-module alias -----------------------------------------------------

const SHAPES: &str = "module Shapes exposing (Pt)\n\n\
    type alias Pt =\n    { px : String, py : String }\n";

#[test]
fn a_cross_module_alias_annotation_is_checked() {
    let bad = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Std.Log exposing (println)\n\
        import Shapes exposing (Pt)\n\n\
        main =\n    let\n        p : Pt\n        p =\n            { px = \"x\" }\n    in\n    println p.px\n";
    let errs = errors_with(bad, &[("Shapes", SHAPES)]);
    assert!(
        errs.iter()
            .any(|e| e.contains("record is missing field(s): py")),
        "{errs:#?}"
    );
    let good = bad.replace("{ px = \"x\" }", "{ px = \"x\", py = \"y\" }");
    let errs = errors_with(&good, &[("Shapes", SHAPES)]);
    assert!(errs.is_empty(), "{errs:#?}");
}

// ---- value restriction ------------------------------------------------------

#[test]
fn an_annotated_non_value_stays_monomorphic_like_its_unannotated_twin() {
    let annotated = errors(
        "main =\n    let\n        xs : List a\n        xs =\n            List.reverse []\n    in\n    println (String.fromInt (List.length (1 :: xs)) ++ String.fromInt (List.length (\"a\" :: xs)))\n",
    );
    let bare = errors(
        "main =\n    let\n        xs =\n            List.reverse []\n    in\n    println (String.fromInt (List.length (1 :: xs)) ++ String.fromInt (List.length (\"a\" :: xs)))\n",
    );
    assert_eq!(
        annotated.is_empty(),
        bare.is_empty(),
        "annotated: {annotated:#?}\nbare: {bare:#?}"
    );
}
