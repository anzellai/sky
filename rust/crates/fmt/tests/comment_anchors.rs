//! A comment never moves. Every comment keeps the significant token just
//! before it and just after it: the safety net checks this anchor pair (a
//! moved comment makes the output unsafe, so the declaration is kept as
//! written), and the printer keeps a comment in place in the three gaps that
//! used to move it: between an annotation and its definition, between union
//! variants, and between imports.

use fmt::{format_report, format_source, is_formatted};

/// `expected` is canonical; the source given to the formatter is the same
/// text with two spaces after `module`, so a whole-file lossless
/// fallback cannot pass for a format.
fn check(expected: &str) {
    let src = unformat(expected);
    assert_ne!(src, expected);
    let r = format_report(&src);
    assert_eq!(r.text, expected, "\n--- got ---\n{}", r.text);
    assert!(r.kept.is_empty(), "kept {:?}:\n{}", r.kept, r.text);
    assert_eq!(format_source(&r.text), r.text, "not idempotent");
    assert!(is_formatted(&r.text));
}

/// `src` with two spaces after `module`.
fn unformat(src: &str) -> String {
    src.replacen("module Main", "module  Main", 1)
}

#[test]
fn a_comment_between_an_annotation_and_its_definition_stays_there() {
    let src = "module Main exposing (b, main)\n\nimport Std.Log exposing (println)\n\n\nb : Int\n-- note\nb =\n    2\n\n\nmain =\n    println \"x\"\n";
    check(src);
}

#[test]
fn an_indented_comment_between_an_annotation_and_its_definition_stays_there() {
    let src = "module Main exposing (b)\n\n\nb : Int\n    -- note\nb =\n    2\n";
    let r = format_report(&unformat(src));
    assert!(r.text.contains("b : Int\n    -- note\nb =\n"), "{}", r.text);
    assert!(r.kept.is_empty(), "{}", r.text);
    assert_eq!(format_source(&r.text), r.text);
}

#[test]
fn a_comment_between_variants_stays_at_its_variant() {
    let src = "module Main exposing (Msg(..))\n\ntype Msg\n    = A\n    -- c\n    | B\n";
    check(src);
}

#[test]
fn a_comment_before_the_first_variant_stays_there() {
    let src = "module Main exposing (Msg(..))\n\ntype Msg\n    -- c\n    = A\n    | B\n";
    check(src);
}

#[test]
fn a_comment_between_imports_stays_at_its_import() {
    let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n-- c\nimport Sky.Core.List as List\n\n\nmain =\n    println \"x\"\n";
    check(src);
}

/// A comment at every placement the printer handles. Each source is
/// formatted; the output must keep every comment between the same two
/// significant tokens (by text and occurrence) and be idempotent. The net
/// is the guarantee: a placement the printer cannot keep is kept as written.
#[test]
fn every_comment_keeps_its_anchor_pair() {
    let cases: &[&str] = &[
        // top level: header, imports, before / after items, annotation gap
        "-- top\nmodule Main exposing (main)\n\n-- before imports\nimport Std.Log exposing (println)\n-- between imports\nimport Sky.Core.List as List\n-- after imports\n\n\nmain =\n    println \"x\"\n-- end\n",
        "module Main exposing (a, b)\n\n\na : Int\n-- gap\na =\n    1\n\n\n-- before b\nb : Int\nb =\n    2\n    -- trailer\n",
        // exposing lists
        "module Main exposing\n    ( a\n    -- c\n    , b\n    )\n\n\na =\n    1\n\n\nb =\n    2\n",
        "module Main exposing (main)\n\nimport Std.Log exposing\n    ( println\n    -- c\n    )\n\n\nmain =\n    println \"x\"\n",
        // type variants and aliases
        "module Main exposing (T(..))\n\n\ntype T\n    = A\n    -- one\n    | B Int\n    -- two\n    | C\n",
        "module Main exposing (R)\n\n\ntype alias R =\n    { a : Int\n    -- c\n    , b : Int\n    }\n",
        // records and lists
        "module Main exposing (r)\n\n\nr =\n    { a = 1\n    -- c\n    , b = 2\n    }\n",
        "module Main exposing (l)\n\n\nl =\n    [ 1\n    -- c\n    , 2\n    ]\n",
        // case arms
        "module Main exposing (f)\n\n\nf x =\n    case x of\n        -- first\n        0 ->\n            1\n\n        -- other\n        _ ->\n            -- body\n            2\n",
        // let lines and bind lines
        "module Main exposing (f)\n\n\nf x =\n    let\n        -- a\n        y =\n            x\n\n        -- b\n        z =\n            y\n    in\n    -- body\n    z\n",
        "module Main exposing (f)\n\nimport Sky.Core.Task as Task\n\n\nf t =\n    let\n        -- first\n        a <- t\n        -- second\n        b <- t\n    in\n    Task.succeed (a + b)\n",
        // if and pipelines
        "module Main exposing (f)\n\n\nf x =\n    if x then\n        -- yes\n        1\n\n    else\n        -- no\n        2\n",
        "module Main exposing (f)\n\n\nf x =\n    x\n        -- one\n        |> g\n        -- two\n        |> h\n",
        // trailing comments
        "module Main exposing (a)\n\n\na =\n    1 -- one\n",
        "module Main exposing (main)\n\nimport Std.Log exposing (println) -- log\n\n\nmain =\n    println \"x\"\n",
    ];
    for src in cases {
        let out = format_source(src);
        assert_eq!(
            anchors(src),
            anchors(&out),
            "a comment moved:\n--- src ---\n{src}\n--- out ---\n{out}"
        );
        assert_eq!(format_source(&out), out, "not idempotent:\n{out}");
    }
}

/// The comment anchors of `src`, computed here independently of the crate:
/// for each comment, the non-paren significant token before and after it,
/// each named by text and occurrence index.
fn anchors(src: &str) -> Vec<(Option<(String, usize)>, Option<(String, usize)>, String)> {
    use std::collections::HashMap;
    use syntax::SyntaxKind;
    let p = syntax::parse(src, base::FileId(0));
    assert_eq!(p.error_node_count(), 0, "does not parse:\n{src}");
    let mut seen: HashMap<String, usize> = HashMap::new();
    let mut prev = None;
    let mut pending = Vec::new();
    let mut out = Vec::new();
    for t in p
        .syntax()
        .descendants_with_tokens()
        .filter_map(|e| e.into_token())
    {
        let k = t.kind();
        if matches!(k, SyntaxKind::LineComment | SyntaxKind::BlockComment) {
            pending.push(t.text().to_string());
        } else if !k.is_trivia() && !matches!(k, SyntaxKind::LParen | SyntaxKind::RParen) {
            let n = seen.entry(t.text().to_string()).or_insert(0);
            let id = (t.text().to_string(), *n);
            *n += 1;
            for c in pending.drain(..) {
                out.push((prev.clone(), Some(id.clone()), c));
            }
            prev = Some(id);
        }
    }
    for c in pending {
        out.push((prev.clone(), None, c));
    }
    out.sort();
    out
}

/// The net on its own: the printer has no place for a comment inside a
/// variant's arguments and would print it below the type. The comment
/// multiset is the same, but the anchor pair is not, so the declaration is
/// kept as written and the rest of the file is formatted.
#[test]
fn a_comment_the_printer_would_move_keeps_its_declaration_as_written() {
    let src = "module  Main exposing (T(..), a)\n\n\ntype T\n    = A\n        -- inside\n        Int\n    | B\n\n\na =\n    1\n";
    let r = format_report(src);
    assert_eq!(r.kept, vec![4], "{}", r.text);
    assert!(r.text.starts_with("module Main exposing"), "{}", r.text);
    assert!(
        r.text
            .contains("type T\n    = A\n        -- inside\n        Int\n    | B\n"),
        "{}",
        r.text
    );
    assert_eq!(anchors(src), anchors(&r.text));
    assert_eq!(format_source(&r.text), r.text);
}
