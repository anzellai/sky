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
fn record_update_with_qualified_base() {
    // A qualified path used as the update base — `{ Mod.default | f = v }` — is
    // a valid expression form (a qualified reference as the update base). The
    // parser once rejected it with `expected LowerIdent` because update
    // detection only recognised a bare LowerIdent base.
    let src = "x =\n    { App.webDefaults | port = 9000 }\n";
    let parse = assert_clean(src);
    assert_eq!(count(&parse, SyntaxKind::RecordUpdate), 1);
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

#[test]
fn qualified_value_then_field_is_field_access() {
    // `Shape.origin.x`: the module qualifier ends at the first lower-case
    // segment, so the value is `Shape.origin` (one QualRefExpr) and `.x` is a
    // field access on it. A module path `Html.Attributes.class` stays one
    // QualRefExpr with no field access.
    let parse = assert_clean("x =\n    f Shape.origin.x Html.Attributes.class A.B.v.w.z\n");
    assert_eq!(count(&parse, SyntaxKind::QualRefExpr), 3);
    // `.x` on `Shape.origin`, then `.w` and `.z` on `A.B.v`.
    assert_eq!(count(&parse, SyntaxKind::FieldAccess), 3);
}

// ---- reserved words (v0.27.12) --------------------------------------------
//
// `foreign` was a keyword in the Rust lexer only: `docs/language/syntax.md`
// never listed it, and the one rule that used it (a top-level `foreign …`
// line) was parsed and then dropped by `hir`. So `let foreign = "x"` failed
// with `expected a pattern` and a cascade of `expected RParen` errors. It is
// an ordinary name now. Every word that IS reserved gets ONE diagnostic that
// names it, wherever a name is expected, and no follow-on errors.

fn reserved_errors(src: &str) -> Vec<std::string::String> {
    p(src).errors().iter().map(|d| d.message.clone()).collect()
}

fn assert_one_reserved(src: &str, kw: &str) {
    let errs = reserved_errors(src);
    assert_eq!(
        errs.len(),
        1,
        "`{kw}`: expected exactly ONE diagnostic (no cascade) for {src:?}, got {errs:?}"
    );
    assert!(
        errs[0].contains(&format!("`{kw}` is a reserved word")),
        "`{kw}`: the diagnostic must name the reserved word, got {:?}",
        errs[0]
    );
}

#[test]
fn foreign_is_an_ordinary_name() {
    assert_clean("x =\n    let\n        foreign =\n            \"x\"\n    in\n    foreign\n");
    assert_clean("foreign =\n    1\n");
    assert_clean("f foreign =\n    foreign\n");
    assert_clean("g r =\n    { r | foreign = 1 }.foreign\n");
    assert!(!syntax::RESERVED_WORDS.contains(&"foreign"));
}

#[test]
fn reserved_words_match_the_lexer_and_the_docs() {
    for kw in syntax::RESERVED_WORDS {
        let toks = syntax::lex(kw);
        assert!(
            toks.iter()
                .any(|t| t.kind != SyntaxKind::LowerIdent && t.kind != SyntaxKind::Eof),
            "`{kw}` is listed as reserved but lexes as a name"
        );
    }
    let docs = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../docs/language/syntax.md"
    ))
    .expect("docs/language/syntax.md");
    let section = docs
        .split("## Reserved words")
        .nth(1)
        .expect("a `## Reserved words` section")
        .split("\n## ")
        .next()
        .unwrap();
    let listed_line = section.lines().find(|l| l.starts_with('`')).unwrap();
    let listed: Vec<&str> = listed_line
        .split('`')
        .enumerate()
        .filter(|(i, _)| i % 2 == 1)
        .map(|(_, w)| w)
        .collect();
    let mut want: Vec<&str> = syntax::RESERVED_WORDS.to_vec();
    let mut got = listed.clone();
    want.sort_unstable();
    got.sort_unstable();
    assert_eq!(
        got, want,
        "docs/language/syntax.md must list exactly the lexer's reserved words"
    );
}

#[test]
fn reserved_word_as_a_let_name_is_one_named_error() {
    for kw in syntax::RESERVED_WORDS {
        if matches!(*kw, "module" | "import") {
            continue;
        }
        let src = format!("x =\n    let\n        {kw} =\n            1\n    in\n    2\n");
        assert_one_reserved(&src, kw);
    }
}

#[test]
fn reserved_word_as_a_parameter_is_one_named_error() {
    for kw in syntax::RESERVED_WORDS {
        let src = format!("f {kw} =\n    1\n");
        assert_one_reserved(&src, kw);
        let src = format!("g =\n    \\{kw} -> 1\n");
        assert_one_reserved(&src, kw);
    }
}

#[test]
fn reserved_word_as_a_top_level_name_is_one_named_error() {
    for kw in syntax::RESERVED_WORDS {
        if matches!(*kw, "module" | "import") {
            continue;
        }
        let src = format!("module Main exposing (main)\n\n{kw} =\n    1\n\n\nmain =\n    2\n");
        assert_one_reserved(&src, kw);
    }
}

#[test]
fn reserved_word_as_a_field_name_is_one_named_error() {
    assert_one_reserved("r =\n    { type = 1 }\n", "type");
    assert_one_reserved("g r =\n    r.type\n", "type");
    assert_one_reserved("g r =\n    { r | of = 1 }\n", "of");
}

// A reserved word used as a VALUE (an argument, an operand, a constructor
// argument in a pattern) is the same single named error. Before the fix the
// argument position stopped at the word, so `Log.println type)` also gave an
// unnamed `expected RParen`.
const NOT_AN_EXPRESSION: &[&str] = &["module", "exposing", "import", "as", "type", "alias"];

#[test]
fn reserved_word_used_as_a_value_is_one_named_error() {
    let repro = "module Main exposing (main)\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Std.Log as Log\nmain =\n    let\n        type = \"x\"\n    in\n    Task.run (Log.println type)\n";
    let errs = reserved_errors(repro);
    assert_eq!(
        errs.len(),
        1,
        "the repro gives ONE diagnostic, got {errs:?}"
    );
    assert!(errs[0].contains("`type` is a reserved word"), "{errs:?}");
    // the use site is marked on that one diagnostic, not reported again
    let parsed = p(repro);
    let labels: Vec<&str> = parsed.errors()[0]
        .labels
        .iter()
        .map(|l| l.message.as_str())
        .collect();
    assert_eq!(labels.len(), 2, "binding + use site labels, got {labels:?}");
    assert_eq!(labels[1], "`type` is also used here");
    for kw in NOT_AN_EXPRESSION {
        assert_one_reserved(&format!("f x =\n    g {kw}\n"), kw);
        assert_one_reserved(&format!("f x =\n    h (g x {kw})\n"), kw);
        assert_one_reserved(&format!("f x =\n    g x {kw} x\n"), kw);
        assert_one_reserved(&format!("f x =\n    x ++ {kw}\n"), kw);
        assert_one_reserved(&format!("f x =\n    [ x, {kw} ]\n"), kw);
    }
}

#[test]
fn reserved_word_in_a_pattern_is_one_named_error() {
    for kw in syntax::RESERVED_WORDS {
        // a case-arm pattern, bare and as a constructor argument
        assert_one_reserved(
            &format!("f x =\n    case x of\n        {kw} ->\n            1\n"),
            kw,
        );
        // a lambda argument and a function argument
        assert_one_reserved(&format!("g =\n    \\{kw} -> 1\n"), kw);
        assert_one_reserved(&format!("f {kw} =\n    1\n"), kw);
    }
    for kw in ["module", "exposing", "import", "type", "alias"] {
        assert_one_reserved(
            &format!("f x =\n    case x of\n        Just {kw} ->\n            1\n\n        Nothing ->\n            0\n"),
            kw,
        );
        assert_one_reserved(&format!("g =\n    \\(Just {kw}) -> 1\n"), kw);
        assert_one_reserved(&format!("f (Just {kw}) =\n    1\n"), kw);
        assert_one_reserved(&format!("g =\n    \\( a, {kw} ) -> 1\n"), kw);
    }
}

#[test]
fn port_is_an_ordinary_name_and_expression_keywords_still_work() {
    assert_clean("f port =\n    g port\n");
    assert_clean("f x =\n    case x of\n        Just port ->\n            port\n\n        Nothing ->\n            0\n");
    assert_clean("g =\n    \\port -> port\n");
    assert_clean(
        "f x =\n    g (if x then 1 else 2) (case x of\n        _ ->\n            1\n    )\n",
    );
    assert_clean("f x =\n    let\n        y =\n            1\n    in\n    g y\n");
    assert_clean("f x =\n    case x of\n        Just y as z ->\n            z\n\n        _ ->\n            x\n");
}
