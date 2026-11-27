//! v0.32.0: a pattern in a binder position must match every value.
//!
//! A lambda parameter, a function parameter (top level or `let`) and a `let`
//! destructure have no second branch, so a pattern that can fail there has no
//! defined answer. Before v0.32.0 `\(Just x) -> x` applied to `Nothing`
//! returned `0` silently, and `let (Just x) = m in x` panicked. Both are now
//! `[E3001]` with the hint to use `case`.

use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

/// Every error and every `[E3001]` the checker reports for `body`.
fn diags(body: &str) -> Vec<diagnostics::Diagnostic> {
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         type Wrap\n    = Wrap Int\n\n\n\
         type Pair a\n    = Pair a a\n\n\n\
         type Shape\n    = Circle Int\n    | Square Int\n\n\n\
         {body}\n\n\nmain =\n    println \"x\"\n"
    );
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    ty::check_modules(&db, &[m])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error || d.code.0 == "E3001")
        .collect()
}

fn codes(body: &str) -> Vec<String> {
    diags(body).into_iter().map(|d| d.code.0).collect()
}

#[test]
fn a_refutable_lambda_parameter_is_e3001() {
    for body in [
        "f : Maybe Int -> Int\nf =\n    \\(Just x) -> x",
        "f : List Int -> Int\nf =\n    \\(x :: _) -> x",
        "f : List Int -> Int\nf =\n    \\[ x ] -> x",
        "f : Int -> Int\nf =\n    \\0 -> 1",
        "f : String -> Int\nf =\n    \\\"a\" -> 1",
        "f : Shape -> Int\nf =\n    \\(Circle r) -> r",
        "f : ( Int, Maybe Int ) -> Int\nf =\n    \\( a, Just b ) -> a + b",
        "f : Bool -> Int\nf =\n    \\True -> 1",
        "f : Wrap -> Int\nf =\n    \\(Wrap 0) -> 1",
    ] {
        assert_eq!(codes(body), ["E3001"], "{body}");
    }
}

#[test]
fn a_refutable_function_parameter_is_e3001() {
    assert_eq!(
        codes("f : Maybe Int -> Int\nf (Just x) =\n    x"),
        ["E3001"]
    );
    assert_eq!(
        codes("f : Int -> Int\nf n =\n    let\n        g (Just x) =\n            x\n    in\n    g (Just n)"),
        ["E3001"]
    );
}

#[test]
fn a_refutable_let_destructure_is_e3001() {
    let ds = diags(
        "g : Maybe Int -> Int\ng m =\n    let\n        (Just x) =\n            m\n    in\n    x",
    );
    assert_eq!(ds.len(), 1, "{ds:?}");
    assert_eq!(ds[0].code.0, "E3001");
    assert!(ds[0].message.contains("`case`"), "{}", ds[0].message);
    assert!(!ds[0].labels.is_empty(), "the pattern is labelled");
    assert_eq!(
        codes("g : List Int -> Int\ng xs =\n    let\n        ( a, 1 ) =\n            ( 2, 3 )\n    in\n    a"),
        ["E3001"]
    );
}

#[test]
fn a_total_binder_pattern_is_accepted() {
    for body in [
        "f : Int -> Int\nf =\n    \\x -> x",
        "f : Int -> Int\nf =\n    \\_ -> 1",
        "f : () -> Int\nf =\n    \\() -> 1",
        "f : ( Int, Int ) -> Int\nf =\n    \\( a, b ) -> a + b",
        "f : { x : Int } -> Int\nf =\n    \\{ x } -> x",
        "f : Wrap -> Int\nf =\n    \\(Wrap n) -> n",
        "f : Pair Wrap -> Int\nf =\n    \\(Pair (Wrap a) (Wrap b)) -> a + b",
        "f : ( Wrap, { x : Int } ) -> Int\nf =\n    \\( (Wrap n) as w, { x } ) -> n + x",
        "f : Wrap -> Int\nf (Wrap n) =\n    n",
        "f : ( Int, Int ) -> Int\nf p =\n    let\n        ( a, b ) =\n            p\n\n        (Wrap c) =\n            Wrap a\n    in\n    a + b + c",
    ] {
        assert_eq!(codes(body), Vec::<String>::new(), "{body}");
    }
}

#[test]
fn a_case_still_takes_refutable_patterns() {
    let body = "f : Maybe Int -> Int\nf m =\n    case m of\n        Just x ->\n            x\n\n        Nothing ->\n            0";
    assert_eq!(codes(body), Vec::<String>::new());
}
