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
// `foreign` was a keyword in the Rust lexer only and is an ordinary name now,
// as is `port`. Every word that IS reserved, misused as a name in ANY
// position, gives exactly ONE diagnostic: `[E0001]`, naming the word.
//
// This is not checked position by position in the grammar. Two structural
// rules give it: the diagnostic budget (only the first syntax error of a
// top-level unit is reported) and naming by trial (the reserved word that,
// read as a name, makes the unit parse is the one the error names). So the
// table below is a cross product, and a new position needs only a new row.

fn diag_lines(parse: &Parse) -> Vec<std::string::String> {
    parse
        .errors()
        .iter()
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect()
}

/// `src` gives exactly one diagnostic: `[E0001]`, naming `kw`.
fn one_named(src: &str, kw: &str) -> Result<(), std::string::String> {
    let parsed = p(src);
    let diags = parsed.errors();
    let ok = diags.len() == 1
        && diags[0].code.0 == "E0001"
        && diags[0]
            .message
            .starts_with(&format!("`{kw}` is a reserved word"));
    if ok && parsed.reprint() == src {
        Ok(())
    } else {
        Err(format!(
            "`{kw}`: {src:?}\n    got {:?}",
            diag_lines(&parsed)
        ))
    }
}

fn assert_one_named(src: &str, kw: &str) {
    if let Err(e) = one_named(src, kw) {
        panic!("expected exactly one named [E0001]: {e}");
    }
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
fn port_is_an_ordinary_name() {
    assert_clean("f port =\n    g port\n");
    assert_clean("port =\n    1\n");
    assert_clean("f x =\n    case x of\n        Just port ->\n            port\n\n        Nothing ->\n            0\n");
    assert_clean("g =\n    \\port -> port\n");
    assert_clean("r =\n    { port = 1 }.port\n");
    assert!(!syntax::RESERVED_WORDS.contains(&"port"));
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
    assert_eq!(syntax::RESERVED_WORDS.len(), 13);
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

// Every name position, as a source template. `KW` stands where a name is
// expected. Each template, with every reserved word in place of `KW`, must
// give exactly ONE diagnostic: `[E0001]`, naming the word.
const RESERVED_POSITIONS: &[(&str, &str)] = &[
    // a value / an atom
    ("value: a whole body", "f x =\n    KW\n"),
    ("value: a body before the next declaration", "f x =\n    KW\n\n\nmain =\n    1\n"),
    ("value: a let value", "x =\n    let\n        y =\n            KW\n    in\n    y\n"),
    ("value: a case arm body", "f x =\n    case x of\n        _ ->\n            KW\n"),
    ("value: an operand", "f x =\n    x ++ KW\n"),
    ("value: an operand before the next declaration", "f x =\n    x ++ KW\n\n\nmain =\n    1\n"),
    ("value: before a pipe", "f x =\n    KW |> g\n"),
    ("value: a list item", "f x =\n    [ x, KW ]\n"),
    ("value: a tuple item", "f x =\n    ( x, KW )\n"),
    ("value: in parentheses", "f x =\n    g (KW)\n"),
    ("value: a record field value", "f x =\n    { a = KW }\n"),
    ("value: the repro", "main =\n    Task.run (Log.println KW)\n"),
    ("value: an if condition", "f x =\n    if KW then \"a\" else \"b\"\n"),
    ("value: an if branch", "f x =\n    if x then KW else \"b\"\n"),
    ("value: an else branch", "f x =\n    if x then \"a\" else KW\n"),
    ("value: a case subject", "f x =\n    case KW of\n        _ ->\n            1\n"),
    ("value: a lambda body", "f x =\n    \\y -> KW\n"),
    // a function argument (an application argument)
    ("argument: the last", "f x =\n    g KW\n"),
    ("argument: before another", "f x =\n    g KW 1\n"),
    ("argument: before a pipe", "f x =\n    g KW |> h\n"),
    ("argument: before `)`", "f x =\n    h (g x KW)\n"),
    ("argument: before the next declaration", "f x =\n    g KW\n\n\nmain =\n    1\n"),
    // a parameter of a definition
    ("parameter", "f KW =\n    1\n"),
    ("second parameter", "f x KW =\n    1\n"),
    ("let function parameter", "x =\n    let\n        g KW =\n            1\n    in\n    g\n"),
    // a lambda parameter
    ("lambda parameter", "g =\n    \\KW -> 1\n"),
    ("second lambda parameter", "g =\n    \\x KW -> 1\n"),
    // patterns
    ("bare case pattern", "f x =\n    case x of\n        KW ->\n            1\n"),
    (
        "constructor argument in a case arm",
        "f w =\n    case w of\n        Just KW ->\n            \"y\"\n\n        Nothing ->\n            \"n\"\n",
    ),
    ("constructor argument, own type", "f w =\n    case w of\n        W KW ->\n            \"y\"\n"),
    ("constructor argument in a lambda", "g =\n    \\(Just KW) -> 1\n"),
    ("constructor argument in a parameter", "f (Just KW) =\n    1\n"),
    ("tuple pattern item", "g =\n    \\( a, KW ) -> 1\n"),
    ("list pattern item", "f x =\n    case x of\n        [ KW ] ->\n            1\n"),
    ("record pattern field", "f { KW } =\n    1\n"),
    ("alias after `as`", "f x =\n    case x of\n        Just y as KW ->\n            1\n"),
    // a let name
    ("let name", "x =\n    let\n        KW =\n            1\n    in\n    2\n"),
    (
        "let name and its use",
        "main =\n    let\n        KW =\n            \"x\"\n    in\n    Task.run (Log.println KW)\n",
    ),
    ("one-line let name and its use", "main =\n    let KW = \"x\" in g KW\n"),
    (
        "let annotation and name",
        "x =\n    let\n        KW : Int\n        KW =\n            1\n    in\n    2\n",
    ),
    ("let function name", "x =\n    let\n        KW y =\n            y\n    in\n    2\n"),
    // a top-level name
    ("top-level name", "module Main exposing (main)\n\nKW =\n    1\n\n\nmain =\n    2\n"),
    ("top-level string value", "module Main exposing (main)\n\nKW = \"x\"\n"),
    ("top-level annotation", "module Main exposing (main)\n\nKW : String\n"),
    ("top-level function name", "module Main exposing (main)\n\nKW x =\n    x\n\n\nmain =\n    2\n"),
    ("top-level annotation and name", "module Main exposing (main)\n\nKW : Int\nKW =\n    1\n"),
    ("first declaration name, no header", "KW x =\n    x\n"),
    ("string value, no header", "KW = \"x\"\n"),
    ("annotation, no header", "KW : String\n"),
    // a record field
    ("record field", "r =\n    { KW = 1 }\n"),
    ("second record field", "r =\n    { a = 1, KW = 2 }\n"),
    ("type alias field", "type alias R =\n    { KW : Int }\n"),
    ("second type alias field", "type alias R =\n    { a : Int, KW : Int }\n"),
    ("record type field in an annotation", "f : { KW : Int } -> Int\nf r =\n    1\n"),
    // a record update
    ("record update field", "g r =\n    { r | KW = 1 }\n"),
    ("record update base", "g r =\n    { KW | a = 1 }\n"),
    // a field access
    ("field access", "g r =\n    r.KW\n"),
    ("field access as an argument", "g r =\n    h r.KW 1\n"),
    ("accessor function", "g xs =\n    List.map .KW xs\n"),
    // types
    ("type variable in an annotation", "f : KW -> String\nf x =\n    \"a\"\n"),
    ("constructor argument type", "type T\n    = A KW\n"),
    ("type alias variable", "type alias Box KW =\n    { v : Int }\n"),
    ("union type variable", "type T KW\n    = A\n"),
    // module header and imports
    ("exposed value", "module Main exposing (main, KW)\n\n\nmain =\n    1\n"),
    ("import alias", "module Main exposing (main)\n\nimport Std.Log as KW\n\n\nmain =\n    1\n"),
    ("imported module name", "module Main exposing (main)\n\nimport KW\n\n\nmain =\n    1\n"),
    ("type name", "type KW\n    = A\n"),
    ("type alias name", "type alias KW =\n    { a : Int }\n"),
    ("imported value", "module Main exposing (main)\n\nimport Std.Log exposing (KW)\n\n\nmain =\n    1\n"),
];

#[test]
fn reserved_word_in_every_name_position_is_one_named_error() {
    let mut failures = Vec::new();
    for kw in syntax::RESERVED_WORDS {
        for (position, template) in RESERVED_POSITIONS {
            if let Err(e) = one_named(&template.replace("KW", kw), kw) {
                failures.push(format!("{position}: {e}"));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} cases do not give exactly one named [E0001]:\n{}",
        failures.len(),
        syntax::RESERVED_WORDS.len() * RESERVED_POSITIONS.len(),
        failures.join("\n")
    );
}

#[test]
fn misused_keyword_next_to_a_valid_one_names_the_misused_one() {
    // `then` / `else` are VALID here: only the misused word is named.
    assert_one_named("f x =\n    if of then \"a\" else \"b\"\n", "of");
    assert_one_named("f x =\n    if then then \"a\" else \"b\"\n", "then");
    assert_one_named("f x =\n    if if then \"a\" else \"b\"\n", "if");
    assert_one_named("f x =\n    if case then \"a\" else \"b\"\n", "case");
    assert_one_named("f x =\n    if let then \"a\" else \"b\"\n", "let");
    assert_one_named(
        "f x =\n    case case of\n        _ ->\n            1\n",
        "case",
    );
    assert_one_named("f x =\n    case if of\n        _ ->\n            1\n", "if");
    assert_one_named("f x =\n    case in of\n        _ ->\n            1\n", "in");
    assert_one_named("f x =\n    let y = in in y\n", "in");
    // a branch left out is a missing expression, not a misused word: no
    // trial parses clean, so the parser's own message stays
    assert_one_plain(
        "f x =\n    if True then else \"b\"\n",
        "expected an expression",
    );
    // the function-argument repro: atoms are the only arguments
    for kw in ["if", "case", "let"] {
        assert_one_named(&format!("f x =\n    g {kw} 1\n"), kw);
    }
}

#[test]
fn errors_in_two_declarations_give_one_each() {
    let src = "f x =\n    g type 1\n\n\ny =\n    h (case)\n\n\nmain =\n    1\n";
    let parsed = p(src);
    let got = diag_lines(&parsed);
    assert_eq!(got.len(), 2, "one per broken declaration, got {got:?}");
    assert!(got[0].contains("`type` is a reserved word"), "{got:?}");
    assert!(got[1].contains("`case` is a reserved word"), "{got:?}");
    assert_eq!(parsed.reprint(), src);
    // a broken declaration and an unrelated syntax error elsewhere
    let src = "f x =\n    g then\n\n\ny =\n    ( 1\n";
    let got = diag_lines(&p(src));
    assert_eq!(got.len(), 2, "{got:?}");
    assert!(got[0].contains("`then` is a reserved word"), "{got:?}");
    assert!(!got[1].contains("reserved word"), "{got:?}");
    // a declaration with a misused word AND another error: renaming the word
    // does not make it parse, so its own message stays (one per declaration)
    let src = "f x =\n    g type 1 ( ]\n\n\ny =\n    h (case)\n";
    let got = diag_lines(&p(src));
    assert_eq!(got.len(), 2, "{got:?}");
    assert!(!got[0].contains("reserved word"), "{got:?}");
    assert!(got[1].contains("`case` is a reserved word"), "{got:?}");
}

#[test]
fn reserved_word_repro_is_one_named_error() {
    let repro = "module Main exposing (main)\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Std.Log as Log\nmain =\n    let\n        type = \"x\"\n    in\n    Task.run (Log.println type)\n";
    assert_one_named(repro, "type");
    assert_one_named("main =\n    Task.run (Log.println then)\n", "then");
}

#[test]
fn keywords_in_their_valid_places_still_parse() {
    // every nesting of if / case / let inside each other's slots
    let leaf = "x";
    let wrap = |kind: &str, slot: usize, inner: &str| -> std::string::String {
        let s = |i: usize| {
            if i == slot {
                format!("({inner})")
            } else {
                leaf.to_string()
            }
        };
        match kind {
            "if" => format!("if {} then {} else {}", s(0), s(1), s(2)),
            "case" => format!(
                "case {} of\n            _ ->\n                {}\n",
                s(0),
                s(1)
            ),
            _ => format!("let y = {} in {}", s(0), s(1)),
        }
    };
    let slots = |kind: &str| if kind == "if" { 3 } else { 2 };
    for outer in ["if", "case", "let"] {
        for inner in ["if", "case", "let"] {
            let inner_src = match inner {
                "if" => "if x then 1 else 2".to_string(),
                "case" => "case x of\n            _ ->\n                1\n        ".to_string(),
                _ => "let z = 1 in z".to_string(),
            };
            for slot in 0..slots(outer) {
                let body = wrap(outer, slot, &inner_src);
                let src = format!("f x =\n    {body}\n");
                let src = src.replace("\n\n", "\n");
                let parsed = p(&src);
                assert!(
                    parsed.errors().is_empty() && parsed.error_node_count() == 0,
                    "{inner} inside {outer} slot {slot} must parse clean: {src:?} {:?}",
                    diag_lines(&parsed)
                );
            }
        }
    }
    assert_clean(
        "f x =\n    g (if x then h y else k z) (case f x of\n        _ ->\n            1\n    )\n",
    );
    assert_clean("f x =\n    if g x then\n        h x\n\n    else if k x then\n        1\n\n    else\n        2\n");
    assert_clean("f x =\n    case g x of\n        Just y ->\n            y\n\n        Nothing ->\n            0\n");
    assert_clean("f x =\n    let\n        y =\n            g x\n    in\n    h y\n");
    assert_clean("f x =\n    let y = g x in h y\n");
    assert_clean("f x =\n    g (let y = 1 in y) x\n");
    assert_clean("f x =\n    if case x of\n        _ ->\n            True\n    then\n        1\n\n    else\n        2\n");
    assert_clean("f x =\n    case (if x then 1 else 2) of\n        _ ->\n            x\n");
    assert_clean("f x =\n    x |> \\y -> if y then 1 else 2\n");
    assert_clean(
        "f x =\n    [ if x then 1 else 2, case x of\n        _ ->\n            3\n    ]\n",
    );
    assert_clean("f x =\n    case x of\n        Just y as z ->\n            z\n\n        _ ->\n            x\n");
    assert_clean("f (Just x as y) =\n    y\n");
    assert_clean("f x =\n    t\n        |> Task.andThen (\\y -> case y of\n            Just z ->\n                Task.succeed z\n\n            Nothing ->\n                Task.succeed 0\n        )\n");
    assert_clean("f x =\n    t\n        |> Task.andThen\n            (\\y ->\n                case y of\n                    Just z ->\n                        Task.succeed z\n\n                    Nothing ->\n                        Task.succeed 0\n            )\n");
    assert_clean("module Main exposing (main)\n\nimport Std.Log as Log exposing (println)\n\n\ntype alias R =\n    { a : Int }\n\n\ntype T\n    = A\n    | B Int\n\n\nmain =\n    1\n");
}

/// The byte offset where the first label of `d` starts.
fn diag_start(d: &diagnostics::Diagnostic) -> u32 {
    d.labels.first().map(|l| l.span.range.0).unwrap_or(0)
}

// A second, separate broken declaration after each broken first one. The
// diagnostic budget is per LEXICAL unit (a line starting at column 1, with an
// annotation and its definition as one unit), so recovery in the first unit
// can never swallow the second unit's diagnostic. `V` is a reserved word that
// differs from the first unit's word.
const SECOND_UNITS: &[(&str, &str)] = &[
    ("field access", "zz y =\n    y.V\n"),
    ("unclosed paren", "zz y =\n    ( y\n"),
    (
        "unclosed paren before a valid declaration",
        "zz y =\n    ( y\n\n\nmain =\n    1\n",
    ),
    ("missing operand", "zz y =\n    y +\n"),
    ("missing body", "zz y =\n"),
];

fn two_errors(src: &str, second_at: u32) -> Result<(), std::string::String> {
    let parsed = p(src);
    let diags = parsed.errors();
    let valid_end = src[second_at as usize..]
        .find("\nmain =")
        .map(|i| second_at + i as u32)
        .unwrap_or(src.len() as u32);
    let ok = diags.len() == 2
        && diag_start(&diags[0]) < second_at
        && diag_start(&diags[1]) >= second_at
        && diag_start(&diags[1]) <= valid_end;
    if ok && parsed.reprint() == src {
        Ok(())
    } else {
        Err(format!("{src:?}\n    got {:?}", diag_lines(&parsed)))
    }
}

#[test]
fn a_second_broken_declaration_keeps_its_own_error() {
    let mut failures = Vec::new();
    let mut total = 0;
    for kw in syntax::RESERVED_WORDS {
        let v = if *kw == "of" { "type" } else { "of" };
        for (position, template) in RESERVED_POSITIONS {
            let first = template.replace("KW", kw);
            for (kind, second) in SECOND_UNITS {
                total += 1;
                let src = format!("{first}\n\n{}", second.replace('V', v));
                let at = (first.len() + 2) as u32;
                if let Err(e) = two_errors(&src, at) {
                    failures.push(format!("{position} + {kind}: {e}"));
                }
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {total} cases do not give one error per broken declaration:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

#[test]
fn closing_punctuation_at_column_one_continues_its_declaration() {
    // The formatter writes a multi-line record type with `,` and `}` at
    // column 1. They continue the unit before them; they never start one.
    assert_clean(
        "wrap : a -> { val : a\n, tag : String\n}\nwrap v =\n    { val = v, tag = \"\" }\n",
    );
    assert_clean("type alias R =\n    { a : Int\n, b : Int\n}\n\n\nmain =\n    1\n");
}

#[test]
fn a_body_at_column_one_starts_a_new_unit() {
    // A body is indented past column 1 (the layout rule). A body token at
    // column 1 begins the next unit, so the definition has no body.
    let got = diag_lines(&p("answer =\n42\n"));
    assert_eq!(got.len(), 2, "{got:?}");
}

#[test]
fn the_judge_repros_give_two_errors() {
    let tail = "\n\n\nzz y =\n    y.of\n";
    for first in [
        "g if =\n    1\n",
        "type T\n    = T type\n",
        "of : Int\n",
        "f a type b = a\n",
        "let g type = 1\n",
    ] {
        let src = format!("{first}{tail}");
        let at = (first.len() + 3) as u32;
        if let Err(e) = two_errors(&src, at) {
            panic!("expected two errors: {e}");
        }
        let got = p(&src);
        assert!(
            got.errors()[1]
                .message
                .starts_with("`of` is a reserved word"),
            "{:?}",
            diag_lines(&got)
        );
    }
}

/// `src` gives exactly one diagnostic, with the message `want`, and names no
/// reserved word: the keyword in it is used in its valid place.
fn assert_one_plain(src: &str, want: &str) {
    let parsed = p(src);
    let got = diag_lines(&parsed);
    assert_eq!(got.len(), 1, "{src:?}: {got:?}");
    assert!(!got[0].contains("reserved word"), "{src:?}: {got:?}");
    assert_eq!(parsed.errors()[0].message, want, "{src:?}");
}

#[test]
fn a_valid_keyword_is_never_named_as_misused() {
    // The messages are the parser's own, as v0.27.11 reports them.
    assert_one_plain(
        "module Main exposing main\n\n\nmain =\n    1\n",
        "expected LParen",
    );
    assert_one_plain("f x =\n    if x 1 else 2\n", "expected ThenKw");
    assert_one_plain("f x =\n    if then 1 else 2\n", "expected an expression");
    assert_one_plain(
        "f x =\n    case of\n        _ ->\n            1\n",
        "expected an expression",
    );
}

#[test]
fn a_case_with_no_branch_is_an_error() {
    // A `case` needs at least one branch. With none, the checker saw no
    // pattern to test, and the program panicked at run time.
    assert_one_plain("f x =\n    case x of\n", "expected a case branch");
    assert_one_plain(
        "f x =\n    case x of\n\n\nmain =\n    1\n",
        "expected a case branch",
    );
}
