//! Regression: every type variable in a top-level annotation is RIGID
//! (v0.29.2).
//!
//! Before v0.29.2 the annotation gate made only the variables of the RESULT
//! rigid. A variable that appears only in argument position stayed flexible,
//! so the body could pin it to a concrete type, or merge it with another
//! variable, and the annotation still checked:
//!
//! * `show : a -> String` / `show v = "<" ++ v ++ ">"` printed `<1>` for
//!   `show (Just 1)`;
//! * `coerce : a -> b` / `coerce x = x` turned a `String` into an `Int`, and
//!   `n + 1` then panicked at run time (`rt.AsInt … got string`), a runtime
//!   panic from well-typed code.
//!
//! Now each variable is a skolem in its definition: the body may keep it
//! polymorphic, but not pin it to a type or join it to another variable.
//! Each rejection below has an accepted twin, so a checker that rejected
//! every polymorphic annotation would fail.

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

#[test]
fn an_argument_variable_pinned_to_a_type_is_rejected_with_the_fix() {
    let msg = rejected(
        "show : a -> String\nshow v =\n    \"<\" ++ v ++ \">\"\n",
        &["`a`", "String"],
    );
    assert!(msg.contains("any type"), "{msg}");
    assert!(msg.contains("Fix:"), "{msg}");
    assert!(
        !msg.contains("write `appendable`"),
        "the body pins `a` to String, so `appendable` is the wrong advice:\n{msg}"
    );
    assert_eq!(msg.lines().count(), 1, "one definition, one error:\n{msg}");
    accepted("show : String -> String\nshow v =\n    \"<\" ++ v ++ \">\"\n");
    accepted("show : appendable -> appendable -> appendable\nshow a b =\n    a ++ b\n");
}

#[test]
fn two_variables_made_one_type_are_rejected() {
    let msg = rejected("coerce : a -> b\ncoerce x =\n    x\n", &["`a`", "`b`"]);
    assert!(msg.contains("Fix:"), "{msg}");
    rejected(
        "pairUp : a -> b -> List a\npairUp x y =\n    [ x, y ]\n",
        &["`a`", "`b`"],
    );
    accepted("pairUp : a -> a -> List a\npairUp x y =\n    [ x, y ]\n");
    accepted("coerce : a -> a\ncoerce x =\n    x\n");
}

#[test]
fn an_argument_variable_used_as_a_number_is_rejected() {
    // The body pins `a` to the annotated result `Int`: write `Int`.
    rejected("bad3 : a -> Int\nbad3 v =\n    v + 1\n", &["`a`", "`Int`"]);
    // The body needs only an ordering: write `comparable` (a bound the body
    // needs is refused at the end of the definition, so a later clash with a
    // type gives the clearer "write `Int`" instead).
    rejected(
        "bad4 : a -> a -> Bool\nbad4 x y =\n    x < y\n",
        &["`a`", "`comparable`"],
    );
    accepted("ok4 : comparable -> comparable -> Bool\nok4 x y =\n    x < y\n");
    accepted("ok3 : number -> number\nok3 v =\n    v + v\n");
}

#[test]
fn polymorphic_annotations_still_check() {
    accepted("apply : (a -> b) -> a -> b\napply f x =\n    f x\n");
    accepted("ignore : a -> ()\nignore _ =\n    ()\n");
    accepted("firstOr : a -> List a -> a\nfirstOr d xs =\n    case xs of\n        x :: _ ->\n            x\n\n        [] ->\n            d\n");
    accepted("largest : comparable -> comparable -> comparable\nlargest a b =\n    if a < b then\n        b\n\n    else\n        a\n");
    accepted(
        "mapBoth : (a -> b) -> ( a, a ) -> ( b, b )\nmapBoth f ( x, y ) =\n    ( f x, f y )\n",
    );
    accepted("count : List a -> Int\ncount xs =\n    List.length xs\n");
}

#[test]
fn row_polymorphic_records() {
    accepted("getName : { r | name : String } -> String\ngetName rec =\n    rec.name\n");
    accepted(
        "rename : String -> { r | name : String } -> { r | name : String }\nrename n rec =\n    { rec | name = n }\n",
    );
    accepted("field : { r | name : a } -> a\nfield rec =\n    rec.name\n");
    rejected(
        "getName : { r | name : a } -> String\ngetName rec =\n    rec.name\n",
        &["`a`", "String"],
    );
}

#[test]
fn mutual_recursion() {
    accepted(
        "walkA : a -> Int -> Int\nwalkA x n =\n    if n <= 0 then\n        0\n\n    else\n        walkB x (n - 1)\n\n\n\
         walkB : b -> Int -> Int\nwalkB y n =\n    walkA y n\n",
    );
    rejected(
        "walkA : a -> Int -> Int\nwalkA x n =\n    if n <= 0 then\n        0\n\n    else\n        walkB x (n - 1)\n\n\n\
         walkB : Int -> Int -> Int\nwalkB y n =\n    walkA y (n + y)\n",
        &["`a`", "Int"],
    );
}

#[test]
fn let_annotations_and_scoped_type_variables() {
    // A let annotation's own variable is rigid in its definition.
    rejected(
        "f : Int -> String\nf n =\n    let\n        show2 : a -> String\n        show2 v =\n            \"<\" ++ v\n    in\n    show2 \"x\"\n",
        &["`a`"],
    );
    // A let annotation may name the enclosing annotation's variable (Elm's
    // scoped type variables): it is the same, rigid, variable.
    accepted(
        "wrap : a -> List a\nwrap x =\n    let\n        one : a\n        one =\n            x\n    in\n    [ one ]\n",
    );
    accepted(
        "twice : a -> List a\ntwice x =\n    let\n        dup : b -> List b\n        dup y =\n            [ y, y ]\n    in\n    dup x\n",
    );
}
