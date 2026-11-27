//! The safety-net fallback is per top-level declaration: a declaration the
//! printer cannot lay out without losing a comment is kept exactly as
//! written, and every other declaration is still formatted.

use fmt::{format_report, format_source, is_formatted};

/// `main` has a trailing comment on a `let` line, which the printer cannot
/// keep in place; `helper` is badly laid out and formattable.
const SRC: &str = "module Main exposing (main)\n\nimport Sky.Core.Task as Task\nimport Std.Log exposing (println)\n\n\nhelper x = x+1\n\n\nmain =\n    let\n        x <- Task.succeed 1 -- trailing\n        (a,b) = (1,2)\n    in\n        println \"done\"\n";

#[test]
fn a_declaration_with_an_unplaceable_comment_is_kept_and_the_rest_formatted() {
    let r = format_report(SRC);
    // `helper` is formatted.
    assert!(r.text.contains("helper x =\n    x + 1\n"), "{}", r.text);
    // `main` is exactly as written, comment included.
    let main_src = &SRC[SRC.find("main =").unwrap()..];
    assert!(r.text.ends_with(main_src), "{}", r.text);
    // The kept declaration is named by its first line.
    assert_eq!(r.kept, vec![10]);
    // Idempotent, and the second pass keeps the same declaration.
    let again = format_report(&r.text);
    assert_eq!(again.text, r.text);
    assert_eq!(
        again.kept,
        vec![r.text.lines().position(|l| l == "main =").unwrap() + 1]
    );
}

#[test]
fn check_fails_only_when_a_formattable_declaration_would_change() {
    // `helper` would change: not formatted.
    assert!(!is_formatted(SRC));
    // After formatting, only the kept declaration differs from the printer's
    // layout: formatted.
    assert!(is_formatted(&format_source(SRC)));
    // A file whose only problem is the unplaceable comment is formatted.
    let only_main = format_source(SRC).replace("helper x =\n    x + 1\n\n\n", "");
    assert!(is_formatted(&only_main), "{only_main}");
    assert_eq!(format_report(&only_main).kept.len(), 1);
}

#[test]
fn a_kept_annotated_declaration_keeps_its_annotation_and_own_line_comments() {
    let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\n-- header\nf : Int -> Int\nf n =\n    let\n        -- inside\n        y = n -- trailing\n    in\n        y\n\n\nmain =\n    println   \"x\"\n";
    let r = format_report(src);
    let f_src = &src[src.find("f : Int").unwrap()..src.find("\n\n\nmain").unwrap()];
    assert!(
        r.text.contains(&format!("-- header\n{f_src}\n")),
        "{}",
        r.text
    );
    assert_eq!(r.text.matches("-- inside").count(), 1, "{}", r.text);
    assert!(r.text.contains("main =\n    println \"x\"\n"), "{}", r.text);
    assert_eq!(r.kept, vec![7]);
    assert_eq!(format_source(&r.text), r.text);
}

#[test]
fn a_file_with_nothing_unsafe_keeps_nothing() {
    let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\nmain =\n    println   \"x\"\n";
    let r = format_report(src);
    assert!(r.kept.is_empty());
    assert!(r.text.contains("println \"x\""));
}
