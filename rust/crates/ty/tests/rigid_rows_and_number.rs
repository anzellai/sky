//! Regression (v0.29.2, Judge round 7): the annotation gate's rigid
//! variables cover ROW variables and the `number` bound.
//!
//! * A row variable in an annotation (`{ r | name : String }`) was not
//!   rigid: `f : { r | name : String } -> { s | name : String }; f rec = rec`
//!   checked, and `(f { name = "x" }).age` then failed at run time with a
//!   `CoerceFailure`. The row links of `unify_records` were dropped.
//! * `+ - *`, `negate` and `abs` carried no `number` bound on the check path,
//!   so `f : a -> a; f x = x + x` checked and `f "s"` panicked in `rt.AsInt`.
//! * An integer literal was a concrete `Int`, so a correct
//!   `isPositive : number -> Bool; isPositive x = x > 0` was refused once
//!   argument variables became rigid.
//! * The advice "write `List _` instead of `a`" did not parse, and a record
//!   was shown as `record`.
//!
//! Every rejection has an accepted twin, and the row cases are generated over
//! every container shape a record can sit in.

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
    import Sky.Core.List as List\n\
    import Std.Log exposing (println)\n\n";

fn errors(body: &str) -> Vec<String> {
    errors_with(&format!("{HEAD}{body}\n\nmain =\n    println \"x\"\n"), &[])
}

#[track_caller]
fn rejected(body: &str, needles: &[&str]) -> String {
    let errs = errors(body);
    assert!(
        !errs.is_empty(),
        "accepted, but the annotation is too general:\n{body}"
    );
    let all = errs.join("\n");
    assert!(all.contains("[E2001]"), "{all}");
    for n in needles {
        assert!(all.contains(n), "missing {n:?} in:\n{all}");
    }
    all
}

#[track_caller]
fn accepted(body: &str) {
    let errs = errors(body);
    assert!(
        errs.is_empty(),
        "rejected a sound annotation:\n{body}\n{}",
        errs.join("\n")
    );
}

// ---- rows -----------------------------------------------------------------

#[test]
fn two_rigid_rows_made_one_are_rejected() {
    let msg = rejected(
        "f : { r | name : String } -> { s | name : String }\nf rec =\n    rec\n",
        &["`r`", "`s`", "different sets of record fields"],
    );
    assert!(msg.contains("Fix:"), "{msg}");
}

#[test]
fn the_same_rigid_row_on_both_sides_is_accepted() {
    accepted("f : { r | name : String } -> { r | name : String }\nf rec =\n    rec\n");
    accepted(
        "f : { r | name : String } -> { r | name : String }\nf rec =\n    { rec | name = rec.name ++ \"!\" }\n",
    );
    accepted("f : { r | name : String } -> String\nf rec =\n    rec.name\n");
}

#[test]
fn a_closed_record_returned_for_a_rigid_row_is_rejected() {
    rejected(
        "f : { r | name : String } -> { r | name : String }\nf rec =\n    { name = rec.name ++ \"!\" }\n",
        &["`r`", "`name`", "would be lost"],
    );
}

#[test]
fn a_field_the_rigid_row_does_not_list_is_rejected() {
    rejected(
        "f : { r | name : String } -> Int\nf rec =\n    rec.age\n",
        &["`r`", "`age`", "add `age`"],
    );
    accepted("f : { r | name : String, age : Int } -> Int\nf rec =\n    rec.age\n");
}

#[test]
fn a_rigid_row_passed_to_a_closed_record_function_is_rejected() {
    rejected(
        "g : { name : String } -> String\ng x =\n    x.name\n\nf : { r | name : String } -> String\nf rec =\n    g rec\n",
        &["`r`"],
    );
    accepted(
        "g : { s | name : String } -> String\ng x =\n    x.name\n\nf : { r | name : String } -> String\nf rec =\n    g rec\n",
    );
}

/// Every shape a record can sit in: the row variable is still rigid there.
const WRAPPERS: &[&str] = &[
    "{}",
    "List {}",
    "Maybe {}",
    "Dict String {}",
    "Result String {}",
    "( {}, Int )",
    "{ inner : {} }",
    "List (Maybe {})",
    "Box {}",
    "(Int -> {})",
];

fn wrap(w: &str, rec: &str) -> String {
    if w == "{}" {
        rec.to_string()
    } else if w.ends_with(" {}") {
        format!("{} ({rec})", &w[..w.len() - 3])
    } else {
        w.replace("{}", rec)
    }
}

#[test]
fn rigid_rows_are_rigid_in_every_shape() {
    for w in WRAPPERS {
        let a = wrap(w, "{ r | name : String }");
        let b = wrap(w, "{ s | name : String }");
        let prelude = "type Box x\n    = Box x\n\n";
        let bad = format!("{prelude}f : {a} -> {b}\nf x =\n    x\n");
        let errs = errors(&bad);
        assert!(
            errs.iter().any(|e| e.contains("[E2001]")),
            "accepted two rigid rows made one inside `{w}`:\n{bad}"
        );
        let good = format!("{prelude}f : {a} -> {a}\nf x =\n    x\n");
        let errs = errors(&good);
        assert!(
            errs.is_empty(),
            "rejected `{w}`:\n{good}\n{}",
            errs.join("\n")
        );
    }
}

// ---- the number bound ------------------------------------------------------

#[test]
fn arithmetic_on_an_unbounded_variable_is_rejected() {
    for body in [
        "f : a -> a\nf x =\n    x + x\n",
        "f : a -> a -> a\nf x y =\n    x * y\n",
        "f : a -> a -> a\nf x y =\n    x - y\n",
        "f : a -> a\nf x =\n    negate x\n",
        "f : a -> a\nf x =\n    abs x\n",
        "f : List a -> List a\nf xs =\n    List.map (\\v -> v + v) xs\n",
    ] {
        rejected(body, &["`a`", "number"]);
    }
}

#[test]
fn arithmetic_on_a_number_variable_is_accepted() {
    for body in [
        "f : number -> number\nf x =\n    x + x\n",
        "f : number -> number -> number\nf x y =\n    x * y - y\n",
        "f : number -> number\nf x =\n    negate (abs x)\n",
        "f : List number -> List number\nf xs =\n    List.map (\\v -> v + v) xs\n",
    ] {
        accepted(body);
    }
}

#[test]
fn an_unannotated_helper_needs_numbers() {
    let bad = errors("add a b =\n    a + b\n\nx =\n    add \"s\" \"t\"\n");
    assert!(bad.iter().any(|e| e.contains("number")), "{bad:?}");
    accepted("add a b =\n    a + b\n\nx =\n    add 1.5 2.5\n\ny =\n    add 1 2\n");
    let bad = errors("x =\n    negate \"s\"\n");
    assert!(!bad.is_empty(), "negate \"s\" accepted");
}

#[test]
fn an_integer_literal_stands_for_a_number_variable() {
    accepted("isPositive : number -> Bool\nisPositive x =\n    x > 0\n");
    accepted("double : number -> number\ndouble x =\n    x * 2\n");
    accepted("inc : number -> number\ninc x =\n    x + 1\n");
    accepted("zero : number -> number\nzero _ =\n    0\n");
    accepted(
        "sumAll : List number -> number\nsumAll xs =\n    List.foldl (\\x acc -> x + acc) 0 xs\n",
    );
    // A literal is still an `Int` everywhere else.
    rejected("f : a -> a\nf x =\n    x + 1\n", &["`a`", "Int"]);
    let errs = errors("x : Float\nx =\n    1\n");
    assert!(!errs.is_empty(), "an Int literal checked as a Float");
    let errs = errors("x =\n    1 + 2.5\n");
    assert!(!errs.is_empty(), "`1 + 2.5` checked");
    let errs = errors("f : Float -> Float\nf x =\n    x + 1\n");
    assert!(!errs.is_empty(), "`x + 1` on a Float checked");
}

// ---- advice the user can write ---------------------------------------------

#[test]
fn the_suggested_type_parses() {
    let msg = rejected("f : a -> Int\nf x =\n    List.length x\n", &["`a`"]);
    assert!(!msg.contains('_'), "advice shows `_`: {msg}");
    assert!(msg.contains("`List x1`"), "{msg}");
    let msg = rejected(
        "g : { r | name : a } -> Int\ng rec =\n    rec.name.count\n",
        &["`a`"],
    );
    assert!(!msg.contains("`record`"), "advice shows `record`: {msg}");
    assert!(msg.contains("count : "), "{msg}");
}
