//! An own-line comment between two top-level declarations (or between the
//! module header / imports and the first declaration) stays in that gap,
//! before the next declaration, whatever its column. An indented comment
//! keeps its column, so a commented-out code block keeps its shape. It never
//! moves into the body of the next declaration.

use fmt::{format_report, format_source, is_formatted};

fn check(src: &str, expected: &str) {
    let r = format_report(src);
    assert_eq!(r.text, expected, "\n--- got ---\n{}", r.text);
    assert!(r.kept.is_empty(), "kept {:?}:\n{}", r.kept, r.text);
    assert_eq!(format_source(&r.text), r.text, "not idempotent");
    assert!(is_formatted(&r.text));
}

#[test]
fn an_indented_comment_before_the_first_declaration_stays_in_the_gap() {
    let src = "module Main exposing (total)\n\n\n-- oldTotal =\n    -- 1 + 2\n\n\ntotal : Int\ntotal =\n    3\n";
    let want = "module Main exposing (total)\n\n\n-- oldTotal =\n    -- 1 + 2\ntotal : Int\ntotal =\n    3\n";
    check(src, want);
}

#[test]
fn an_indented_comment_below_the_module_header_stays_above_main() {
    let src = "module Main exposing (main)\n\n    -- note\n\nmain =\n    1\n";
    let want = "module Main exposing (main)\n\n\n    -- note\nmain =\n    1\n";
    check(src, want);
}

#[test]
fn an_indented_comment_between_imports_and_main_stays_above_main() {
    let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n    -- note\n\nmain =\n    println \"x\"\n";
    let want = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\n    -- note\nmain =\n    println \"x\"\n";
    check(src, want);
}

#[test]
fn several_indented_lines_between_declarations_keep_their_order_and_shape() {
    let src = "module Main exposing (a, b)\n\n\na =\n    1\n\n\n-- old =\n    -- let\n        -- x = 1\n    -- in\n        -- x\nb =\n    2\n";
    let r = format_report(src);
    let block = "-- old =\n    -- let\n        -- x = 1\n    -- in\n        -- x\nb =\n    2\n";
    assert!(r.text.ends_with(block), "{}", r.text);
    assert!(r.text.contains("a =\n    1\n"), "{}", r.text);
    assert!(r.kept.is_empty());
    assert_eq!(format_source(&r.text), r.text);
}

#[test]
fn an_indented_block_comment_before_a_declaration_stays_in_the_gap() {
    // Before the first declaration: it stays above it, at its column.
    check(
        "module Main exposing (b)\n\n\n    {- old\n       b = 0\n    -}\nb =\n    2\n",
        "module Main exposing (b)\n\n\n    {- old\n       b = 0\n    -}\nb =\n    2\n",
    );
    // Straight after a body it is that body's trailer (the established rule
    // for an indented comment below a body): still in the gap, never in `b`.
    let src = "module Main exposing (a, b)\n\n\na =\n    1\n\n\n    {- old\n       b = 0\n    -}\nb =\n    2\n";
    let r = format_report(src);
    assert!(
        r.text
            .ends_with("a =\n    1\n    {- old\n       b = 0\n    -}\n\n\nb =\n    2\n"),
        "{}",
        r.text
    );
    assert!(r.kept.is_empty());
    assert_eq!(format_source(&r.text), r.text);
}

#[test]
fn a_comment_after_the_last_declaration_stays_after_it() {
    let src = "module Main exposing (a)\n\n\na =\n    1\n\n\n-- tail =\n    -- 2\n";
    let r = format_report(src);
    assert!(
        r.text.ends_with("a =\n    1\n\n-- tail =\n    -- 2\n")
            || r.text.ends_with("a =\n    1\n-- tail =\n    -- 2\n"),
        "{}",
        r.text
    );
    assert!(r.kept.is_empty());
    assert_eq!(format_source(&r.text), r.text);
}
