//! A comment the printer cannot place in the module header or the imports (a
//! trailing comment on the `module` or an `import` line, or a comment inside a
//! multi-line exposing list) keeps the header region as written, and every
//! declaration below it is still formatted. The kept region is reported, so
//! `sky fmt` warns, and `--check` still fails on an unformatted declaration.

use fmt::{format_report, format_source, is_formatted, KeptPart};

const BODY: &str = "\nf x =\n  x + 2\n\n\nmain =\n  println \"x\"\n";
const FORMATTED_BODY: &str = "f x =\n    x + 2\n\n\nmain =\n    println \"x\"\n";

fn check_header_kept(header: &str, comment_line: usize, trailing: bool) {
    let src = format!("{header}{BODY}");
    let r = format_report(&src);
    // The header region is exactly as written.
    assert!(r.text.starts_with(header.trim_end()), "{}", r.text);
    // Every declaration below it is formatted.
    assert!(r.text.ends_with(FORMATTED_BODY), "{}", r.text);
    // The header is reported by its first line, with the comment's line.
    assert_eq!(r.kept, vec![1], "{}", r.text);
    assert_eq!(r.details.len(), 1);
    let d = &r.details[0];
    assert_eq!(d.part, KeptPart::Header);
    let c = d.comment.as_ref().expect("the comment is named");
    assert_eq!(c.line, comment_line);
    assert_eq!(c.trailing, trailing);
    let msg = d.message();
    assert!(msg.contains(&format!("line {comment_line}")), "{msg}");
    if trailing {
        assert!(msg.contains("own line"), "{msg}");
    } else {
        assert!(!msg.contains("Move the comment to its own line"), "{msg}");
        assert!(msg.contains("cannot be placed"), "{msg}");
    }
    // `--check` fails while a declaration below would change ...
    assert!(!is_formatted(&src));
    // ... and passes once it is formatted; the output is idempotent.
    assert!(is_formatted(&r.text), "{}", r.text);
    assert_eq!(format_source(&r.text), r.text);
}

#[test]
fn a_trailing_comment_on_the_module_line_keeps_the_header_only() {
    check_header_kept(
        "module Main exposing (main) -- hdr\n\nimport Std.Log exposing (println)\n\n",
        1,
        true,
    );
}

#[test]
fn a_trailing_comment_on_an_import_line_keeps_the_header_only() {
    check_header_kept(
        "module Main exposing (main)\n\nimport Std.Log exposing (println) -- prelude\nimport Sky.Core.List as List\n\n",
        3,
        true,
    );
}

#[test]
fn a_comment_inside_a_module_exposing_list_keeps_the_header_only() {
    check_header_kept(
        "module Main exposing\n    ( main\n    -- the helper\n    , f\n    )\n\nimport Std.Log exposing (println)\n\n",
        3,
        false,
    );
}

#[test]
fn a_comment_inside_an_import_exposing_list_keeps_the_header_only() {
    check_header_kept(
        "module Main exposing (main)\n\nimport Std.Log\n    exposing\n        ( println -- print a line\n        )\n\n",
        5,
        true,
    );
}

#[test]
fn a_kept_declaration_with_an_own_line_comment_says_so() {
    // A trailing comment in a declaration: the advice is to move it.
    let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"x\" -- why\n";
    let r = format_report(src);
    assert_eq!(r.kept, vec![6]);
    let d = &r.details[0];
    assert_eq!(d.part, KeptPart::Declaration);
    assert!(d.message().contains("line 7"), "{}", d.message());
    assert!(d.message().contains("own line"), "{}", d.message());
}

#[test]
fn an_own_line_comment_inside_an_import_exposing_list_stays_inside_it() {
    // Parentheses are not anchors, so moving this comment below the import
    // would keep its anchor pair. The header is kept instead.
    check_header_kept(
        "module Main exposing (main)\n\nimport Std.Log\n    exposing\n        ( println\n        -- print a line\n        )\n\n",
        6,
        false,
    );
}

#[test]
fn an_import_kept_for_its_side_effects_keeps_its_underscore_alias() {
    // `as _` used to be dropped by the printer, so the whole file fell back
    // to a silent verbatim reprint.
    let src = "module Main exposing (main)\n\n-- a\n\nimport Std.Log exposing (println)\nimport Modernc.Org.Sqlite as _\n\n\nf x =\n  x + 2\n";
    let r = format_report(src);
    assert!(r.kept.is_empty(), "{:?}\n{}", r.kept, r.text);
    assert!(
        r.text.contains("import Modernc.Org.Sqlite as _\n"),
        "{}",
        r.text
    );
    assert!(r.text.ends_with("f x =\n    x + 2\n"), "{}", r.text);
}
