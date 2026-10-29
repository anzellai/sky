//! `[E1018]`: a value defined in terms of itself ("bad recursion").
//!
//! Before v0.29.0 these programs passed `sky check`: a top-level cycle of values
//! deadlocked on its first read, and a cycle inside a `let` failed `go build`.
//! Every rejection here has an accepted twin, so a resolver that refused every
//! recursive program could not pass.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

fn resolve_main(src: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Main", syntax::parse(src, FileId(0)));
    resolve(&db, db.module_by_name("Main").expect("Main registered"))
}

fn e1018(src: &str) -> Vec<String> {
    resolve_main(src)
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1018")
        .map(|d| d.message.clone())
        .collect()
}

fn module(body: &str) -> String {
    format!("module Main exposing (main)\n\n\n{body}\n\n\nmain =\n    0\n")
}

#[test]
fn two_values_that_read_each_other_are_refused_and_the_cycle_is_named() {
    let got = e1018(&module("a =\n    b + 1\n\n\nb =\n    a * 2"));
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(got[0].contains("`a`"), "{}", got[0]);
    assert!(got[0].contains("a -> b -> a"), "{}", got[0]);
}

#[test]
fn a_value_that_reads_itself_is_refused() {
    let got = e1018(&module("xs =\n    1 :: xs"));
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(
        got[0].contains("`xs` is defined in terms of itself"),
        "{}",
        got[0]
    );
}

#[test]
fn a_value_that_calls_a_function_that_reads_it_is_refused() {
    let got = e1018(&module("a =\n    f 1\n\n\nf x =\n    a + x"));
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(got[0].contains("a -> f -> a"), "{}", got[0]);
    // Through a pipeline too.
    let got = e1018(&module("a =\n    1 |> f\n\n\nf x =\n    a + x"));
    assert_eq!(got.len(), 1, "{got:?}");
}

#[test]
fn a_cycle_inside_a_let_is_refused() {
    let got = e1018(&module(
        "go n =\n    let\n        a =\n            b + n\n\n        b =\n            a\n    in\n    a",
    ));
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(got[0].contains("a -> b -> a"), "{}", got[0]);
    // A local value that calls a local function reading it.
    let got = e1018(&module(
        "go n =\n    let\n        a =\n            f 1\n\n        f x =\n            a + x\n    in\n    a",
    ));
    assert_eq!(got.len(), 1, "{got:?}");
}

#[test]
fn recursion_that_terminates_or_is_deferred_is_accepted() {
    for ok in [
        // A recursive function.
        "count n =\n    if n <= 0 then\n        0\n\n    else\n        count (n - 1)",
        // Two functions that call each other.
        "isEven n =\n    if n == 0 then\n        True\n\n    else\n        isOdd (n - 1)\n\n\nisOdd n =\n    if n == 0 then\n        False\n\n    else\n        isEven (n - 1)",
        // A value whose body is a lambda reads itself only when called.
        "loop =\n    \\n -> if n <= 0 then 0 else loop (n - 1)",
        // A reference under a lambda runs later.
        "a =\n    \\_ -> b\n\n\nb =\n    a",
        // A function named, not called.
        "handlers =\n    { run = f }\n\n\nf x =\n    handlers.run x",
        // A partial application runs nothing.
        "a =\n    g 1\n\n\ng x y =\n    a x y",
        // An acyclic chain of values.
        "a =\n    b + 1\n\n\nb =\n    2",
        // A local function that recurses.
        "go n =\n    let\n        f k =\n            if k <= 0 then\n                0\n\n            else\n                f (k - 1)\n    in\n    f n",
    ] {
        let got = e1018(&module(ok));
        assert!(got.is_empty(), "wrongly refused:\n{ok}\n{got:?}");
    }
}

#[test]
fn the_label_points_at_the_value() {
    let r = resolve_main(&module("a =\n    b + 1\n\n\nb =\n    a * 2"));
    let d = r
        .diagnostics
        .iter()
        .find(|d| d.code.0 == "E1018")
        .expect("an E1018");
    assert_eq!(d.labels.len(), 1);
    let src = module("a =\n    b + 1\n\n\nb =\n    a * 2");
    let sp = d.labels[0].span;
    assert_eq!(&src[sp.range.0 as usize..sp.range.1 as usize], "a");
}
