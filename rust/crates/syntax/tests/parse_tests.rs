//! Integration tests for the `syntax` crate — round-trip + structural
//! assertions on representative constructs, and `insta` CST snapshots.

use base::FileId;
use syntax::{parse, Parse, SyntaxKind, SyntaxNode};

fn p(src: &str) -> Parse {
    parse(src, FileId(0))
}

/// Assert byte-exact round-trip + zero error nodes for `src`.
fn assert_clean(src: &str) -> Parse {
    let parse = p(src);
    assert_eq!(parse.reprint(), src, "round-trip must be byte-exact");
    assert_eq!(parse.error_node_count(), 0, "must be zero error nodes");
    assert_eq!(parse.errors().len(), 0, "must be zero diagnostics");
    parse
}

fn count(parse: &Parse, kind: SyntaxKind) -> usize {
    parse
        .syntax()
        .descendants()
        .filter(|n| n.kind() == kind)
        .count()
}

/// S-expression-ish dump of the tree (nodes only), for snapshotting structure.
fn dump(node: &SyntaxNode, indent: usize, out: &mut String) {
    out.push_str(&"  ".repeat(indent));
    out.push_str(&format!("{:?}\n", node.kind()));
    for child in node.children() {
        dump(&child, indent + 1, out);
    }
}

fn tree_dump(parse: &Parse) -> String {
    let mut s = String::new();
    dump(&parse.syntax(), 0, &mut s);
    s
}

#[test]
fn interpolation_produces_nodes_and_roundtrips() {
    let src = "x =\n    div [] [text \"\"\"#{{jid}} {{jname}} - {{status}}\"\"\"]\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 3);
    assert_eq!(count(&parse, SyntaxKind::MultilineLiteral), 1);
}

#[test]
fn interpolation_rich_expr_shapes_produce_correct_nodes() {
    // Rich interpolation bodies route through the real `expr` grammar, so a
    // qualified call, a nested field access, and a multi-arg application each
    // resolve to the correct expression node — not the pre-fix flat-leaf /
    // wrong-node fallback that silently miscompiled.
    let src = "x =\n    \"\"\"a {{String.fromInt n}} b {{record.field.sub}} c {{f x y}}\"\"\"\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 3);
    // `String.fromInt n` -> CallExpr whose callee is a QualRefExpr.
    assert_eq!(count(&parse, SyntaxKind::QualRefExpr), 1);
    // `record.field.sub` -> chained FieldAccess (two `.` steps).
    assert_eq!(count(&parse, SyntaxKind::FieldAccess), 2);
    // `String.fromInt n` and `f x y` -> two CallExpr nodes.
    assert_eq!(count(&parse, SyntaxKind::CallExpr), 2);
}

#[test]
fn interpolation_qualified_call_roundtrips_with_interior_whitespace() {
    // Interior whitespace inside `{{ … }}` must re-emit byte-for-byte.
    let src = "x =\n    \"\"\"v={{ String.fromInt  n }}!\"\"\"\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 1);
    assert_eq!(count(&parse, SyntaxKind::QualRefExpr), 1);
    assert_eq!(count(&parse, SyntaxKind::CallExpr), 1);
}

#[test]
fn interpolation_arithmetic_body_roundtrips() {
    // A binary-operator body parses as a BinExpr and round-trips.
    let src = "x =\n    \"\"\"sum={{a + b}}\"\"\"\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 1);
    assert_eq!(count(&parse, SyntaxKind::BinExpr), 1);
}

#[test]
fn escaped_interpolation_stays_literal() {
    // `\{{` must NOT open an interpolation; bytes are kept verbatim.
    let src = "x =\n    \"\"\"literal \\{{ braces }}\"\"\"\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 0);
}

#[test]
fn multiline_html_without_interpolation() {
    let src = "x =\n    \"\"\"<h1>Hi</h1>\n<ul><li>a</li></ul>\"\"\"\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::MultilineLiteral), 1);
    assert_eq!(count(&parse, SyntaxKind::Interpolation), 0);
}

#[test]
fn record_update_and_field_access() {
    let src = "x model =\n    { model | count = model.count + 1 }\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::RecordUpdate), 1);
    assert_eq!(count(&parse, SyntaxKind::FieldAccess), 1);
}

#[test]
fn cons_and_qualified_ctor_patterns() {
    let src = "\
f xs =
    case xs of
        x :: rest ->
            x

        Db.SetField v ->
            v

        _ ->
            0
";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::PatCons), 1);
    assert_eq!(count(&parse, SyntaxKind::PatCtorQual), 1);
}

#[test]
fn multiline_type_signature() {
    let src = "\
add
    : Int
    -> Int
    -> Int
add a b c =
    a
";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::TypeAnnoDecl), 1);
    assert!(count(&parse, SyntaxKind::TypeFun) >= 1);
}

#[test]
fn row_polymorphic_record_type() {
    let src = "f : { r | count : Int } -> Int\nf r =\n    r.count\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::RowVar), 1);
    assert_eq!(count(&parse, SyntaxKind::TypeRecord), 1);
}

#[test]
fn negative_literal_arg_vs_subtraction() {
    let sub = assert_clean("y a =\n    a - 1\n");
    assert_eq!(count(&sub, SyntaxKind::BinExpr), 1);
    assert_eq!(count(&sub, SyntaxKind::NegateExpr), 0);

    let neg = assert_clean("y =\n    atan2 0 -1\n");
    assert_eq!(count(&neg, SyntaxKind::NegateExpr), 1);
    assert_eq!(count(&neg, SyntaxKind::BinExpr), 0);
}

#[test]
fn operator_precedence_nesting() {
    // `1 + 2 * 3` → `1 + (2 * 3)`: outer BIN's rhs is the `*` BIN.
    let parse = assert_clean("x =\n    1 + 2 * 3\n");
    let bins = count(&parse, SyntaxKind::BinExpr);
    assert_eq!(bins, 2);
}

#[test]
fn deep_nesting_does_not_overflow() {
    // `((((… 1 …))))` 5000 deep — the depth guard must convert would-be stack
    // overflow into ERROR nodes (doc 04 §10), and stay lossless.
    let depth = 5000;
    let src = format!("x =\n    {}1{}\n", "(".repeat(depth), ")".repeat(depth));
    let parse = p(&src);
    assert_eq!(parse.reprint(), src, "still lossless under overflow guard");
    // it must have bailed with a diagnostic rather than panicking
    assert!(!parse.errors().is_empty());
}

#[test]
fn unterminated_string_never_panics() {
    let src = "x =\n    \"unterminated";
    let parse = p(src);
    assert_eq!(parse.reprint(), src);
}

// ---- insta snapshots -----------------------------------------------------

#[test]
fn snapshot_tea_update() {
    let src = "\
module Counter exposing (update)

import Sky.Core.Prelude exposing (..)


type Msg
    = Increment
    | Decrement


update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment ->
            count + 1

        Decrement ->
            count - 1
";
    let parse = assert_clean(src);
    insta::assert_snapshot!("tea_update", tree_dump(&parse));
}

#[test]
fn snapshot_let_pipeline() {
    let src = "\
main =
    let
        xs = [ 1, 2, 3 ]
    in
    xs
        |> List.map (\\n -> n + 1)
        |> List.foldl add 0
";
    let parse = assert_clean(src);
    insta::assert_snapshot!("let_pipeline", tree_dump(&parse));
}

#[test]
fn char_literal_strictness_matches_oracle() {
    // Valid: exactly one codepoint, or backslash + exactly one codepoint.
    for ok in [
        "x =\n    'a'\n",
        "x =\n    '\\n'\n",
        "x =\n    '\\\\'\n",
        "x =\n    '\\d'\n", // backslash + one char — the oracle accepts this
    ] {
        let parse = p(ok);
        assert_eq!(
            parse.errors().len(),
            0,
            "should ACCEPT char literal {ok:?}, got {:?}",
            parse.errors()
        );
    }
    // Invalid: empty, multi-char, and multi-codepoint escapes (`\x41`, `\u{..}`)
    // — the oracle rejects all of these at parse.
    for bad in ["x =\n    ''\n", "x =\n    'ab'\n", "x =\n    '\\x41'\n"] {
        let parse = p(bad);
        assert!(
            !parse.errors().is_empty(),
            "should REJECT char literal {bad:?}"
        );
    }
    // Pattern position rejects too (the merged Char arm was split at both sites).
    let pat_bad =
        p("f c =\n    case c of\n        'ab' ->\n            1\n\n        _ ->\n            0\n");
    assert!(
        !pat_bad.errors().is_empty(),
        "should REJECT multi-char literal in a pattern"
    );
}
