//! Bind lines in `let` (`p <- e`, docs/design/let-bind-lines.md §1, §7):
//! the `<-` token, the `BindBinding` node, and the `[E0001]` refusals.

use base::FileId;
use syntax::ast::{AstNode, BindBinding, Pattern};
use syntax::{parse, Parse, SyntaxKind, SyntaxNode};

fn p(src: &str) -> Parse {
    parse(src, FileId(0))
}

fn assert_clean(src: &str) -> Parse {
    let parse = p(src);
    assert_eq!(parse.reprint(), src, "round-trip must be byte-exact");
    assert_eq!(parse.error_node_count(), 0, "must be zero error nodes");
    assert_eq!(
        parse.errors().len(),
        0,
        "must be zero diagnostics: {:?}",
        parse
            .errors()
            .iter()
            .map(|d| &d.message)
            .collect::<Vec<_>>()
    );
    parse
}

fn binds(parse: &Parse) -> Vec<BindBinding> {
    parse
        .syntax()
        .descendants()
        .filter_map(BindBinding::cast)
        .collect()
}

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

/// `src` gives exactly one `[E0001]` whose message starts with `prefix`, and
/// the tree still reprints byte for byte.
fn assert_one_error(src: &str, prefix: &str) {
    let parsed = p(src);
    let msgs: Vec<_> = parsed
        .errors()
        .iter()
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect();
    assert_eq!(parsed.reprint(), src, "round-trip must be byte-exact");
    assert!(
        msgs.len() == 1 && msgs[0].starts_with(&format!("[E0001] {prefix}")),
        "expected one [E0001] starting {prefix:?} for {src:?}, got {msgs:?}"
    );
}

const BIND_HINT: &str = "`<-` is a bind line; write it as a line of a `let` block";

#[test]
fn larrow_is_its_own_token_kind() {
    let parse = assert_clean("x =\n    let\n        a <- t\n    in\n    a\n");
    let n = parse
        .syntax()
        .descendants_with_tokens()
        .filter(|e| e.kind() == SyntaxKind::LArrow)
        .count();
    assert_eq!(n, 1);
}

#[test]
fn design_example_parses_into_bind_bindings() {
    let src = "\
step app =
    let
        state <- Sync.get app.ref
        now <- nowOf state
        ctx = { now = now }
        _ <- Sync.set (f ctx state) app.ref
    in
    Task.succeed ctx
";
    let parse = assert_clean(src);
    let bs = binds(&parse);
    assert_eq!(bs.len(), 3);
    assert!(matches!(bs[0].pattern(), Some(Pattern::Var(_))));
    assert!(matches!(bs[2].pattern(), Some(Pattern::Wildcard(_))));
    assert_eq!(
        bs[0].expr().unwrap().syntax().text().to_string().trim(),
        "Sync.get app.ref"
    );
    assert!(bs[0].larrow().is_some());
    insta::assert_snapshot!("bind_lines_design_example", tree_dump(&parse));
}

#[test]
fn every_irrefutable_pattern_shape_parses() {
    for line in [
        "_ <- t",
        "() <- t",
        "( a, b ) <- t",
        "{ x, y } <- t",
        "(( a, b ) as pair) <- t",
        "Wrap inner <- t",
        "M.Wrap inner <- t",
        "( Wrap a, { b } ) <- t",
    ] {
        let src = format!("x =\n    let\n        {line}\n    in\n    1\n");
        let parse = assert_clean(&src);
        assert_eq!(binds(&parse).len(), 1, "{line}");
        assert!(binds(&parse)[0].pattern().is_some(), "{line}");
    }
}

#[test]
fn bind_line_may_be_the_last_line_and_mix_with_equals_lines() {
    let src = "\
x =
    let
        a = 1
        b <- t a
    in
    b
";
    let parse = assert_clean(src);
    assert_eq!(binds(&parse).len(), 1);
}

#[test]
fn bind_line_with_multi_line_expression() {
    let src = "\
x =
    let
        user <-
            fetch id
                |> Task.map .user
        y = user
    in
    y
";
    let parse = assert_clean(src);
    let bs = binds(&parse);
    assert_eq!(bs.len(), 1);
    assert!(bs[0]
        .expr()
        .unwrap()
        .syntax()
        .text()
        .to_string()
        .contains("Task.map"));
}

#[test]
fn larrow_in_an_expression_is_a_parse_error() {
    assert_one_error("x =\n    a <- b\n", BIND_HINT);
    assert_one_error("x =\n    f (a <- b)\n", BIND_HINT);
    assert_one_error("x =\n    \\a -> a <- b\n", BIND_HINT);
    assert_one_error("x =\n    case m of\n        _ -> a <- b\n", BIND_HINT);
}

#[test]
fn larrow_after_a_function_binding_head_is_a_parse_error() {
    assert_one_error("x =\n    let\n        f a <- t\n    in\n    f\n", BIND_HINT);
}

#[test]
fn larrow_at_top_level_is_a_parse_error() {
    assert_one_error("x <- t\n", BIND_HINT);
}

#[test]
fn annotation_directly_before_a_bind_line_is_refused() {
    assert_one_error(
        "x =\n    let\n        a : Int\n        a <- t\n    in\n    a\n",
        "An annotation cannot go on a bind line",
    );
    // An annotation before an `=` line, then a bind line, is fine.
    assert_clean("x =\n    let\n        a : Int\n        a = 1\n        b <- t\n    in\n    b\n");
}

#[test]
fn larrow_in_a_record_field_is_a_parse_error() {
    assert_one_error("r =\n    { a <- Just 1 }\n", BIND_HINT);
    assert_one_error("r =\n    { a = 1, b <- Just 2 }\n", BIND_HINT);
    assert_one_error("r =\n    { m | a <- Just 1 }\n", BIND_HINT);
}

#[test]
fn larrow_in_a_case_arm_pattern_is_a_parse_error() {
    assert_one_error("x =\n    case m of\n        Just x <- m -> x\n", BIND_HINT);
    assert_one_error(
        "x =\n    case m of\n        Just x <- m -> x\n        Nothing -> 0\n",
        BIND_HINT,
    );
}

#[test]
fn larrow_in_a_lambda_parameter_is_a_parse_error() {
    assert_one_error("f =\n    \\x <- Just 1\n", BIND_HINT);
    assert_one_error("f =\n    \\x <- Just 1 -> x\n", BIND_HINT);
}

#[test]
fn larrow_in_a_function_parameter_is_a_parse_error() {
    assert_one_error("f (x <- m) = x\n", BIND_HINT);
    assert_one_error(
        "x =\n    let\n        f (y <- m) = y\n    in\n    f\n",
        BIND_HINT,
    );
}

#[test]
fn larrow_in_a_type_annotation_is_a_parse_error() {
    assert_one_error("f : Int <- Int\nf x =\n    x\n", BIND_HINT);
    assert_one_error("f : Int -> Int <- Int\nf x =\n    x\n", BIND_HINT);
    assert_one_error(
        "x =\n    let\n        g : Int <- Int\n        g = 1\n    in\n    g\n",
        BIND_HINT,
    );
}

#[test]
fn larrow_in_an_import_is_a_parse_error() {
    assert_one_error(
        "module Main exposing (main)\n\nimport Sky.Core.Task as Task <- x\n\nmain =\n    ()\n",
        BIND_HINT,
    );
    assert_one_error(
        "module Main exposing (main)\n\nimport Sky.Core.Task as Task exposing (andThen <- x)\n\nmain =\n    ()\n",
        BIND_HINT,
    );
    assert_one_error(
        "module Main exposing (main)\n\nimport Sky.Core.Task <- x\n\nmain =\n    ()\n",
        BIND_HINT,
    );
}

#[test]
fn larrow_in_the_module_header_is_a_parse_error() {
    assert_one_error(
        "module Main exposing (main) <- x\n\nmain =\n    ()\n",
        BIND_HINT,
    );
    assert_one_error(
        "module Main exposing (main <- x)\n\nmain =\n    ()\n",
        BIND_HINT,
    );
}

#[test]
fn larrow_in_a_type_declaration_is_a_parse_error() {
    assert_one_error("type alias R = { a <- Int }\n\nmain =\n    ()\n", BIND_HINT);
    assert_one_error("type alias R = Int <- Int\n\nmain =\n    ()\n", BIND_HINT);
    assert_one_error("type T = A <- Int\n\nmain =\n    ()\n", BIND_HINT);
    assert_one_error("type T = A Int | B <- Int\n\nmain =\n    ()\n", BIND_HINT);
}

/// Property: `<- x` inserted after any token of a sample program gives
/// diagnostics where every `[E0001]` that sits on the `<-` carries the
/// bind-line hint, and the tree still reprints byte for byte.
#[test]
fn larrow_inserted_anywhere_carries_the_bind_line_hint() {
    let samples = [
        "module Main exposing (main, R, T(..))\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task exposing (andThen)\n\ntype alias R =\n    { a : Int, b : List String }\n\ntype T\n    = A Int\n    | B (Maybe R)\n\nf : Int -> Maybe Int\nf n =\n    let\n        y = n + 1\n        z <- Just y\n    in\n    Just (z * 2)\n\nmain =\n    case f 1 of\n        Just v ->\n            v\n\n        Nothing ->\n            0\n",
        "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\ng : { x : Int } -> ( Int, String )\ng { x } =\n    ( x, \"s\" )\n\nmain =\n    println (String.fromInt (Tuple.first (g { x = 1 })) ++ \"!\")\n        |> (\\t -> t)\n",
    ];
    fn texts(src: &str) -> Vec<(String, usize)> {
        p(src)
            .syntax()
            .descendants_with_tokens()
            .filter_map(|e| e.into_token())
            .filter(|t| !t.kind().is_trivia())
            .map(|t| (t.text().to_string(), usize::from(t.text_range().end())))
            .collect()
    }
    let mut checked = 0;
    let mut hits = 0;
    for src in samples {
        let tokens = texts(src);
        for (i, (_, end)) in tokens.iter().enumerate() {
            let end = *end;
            let mutated = format!("{} <- x{}", &src[..end], &src[end..]);
            // Only an insertion that keeps every original token whole (not
            // one inside a dotted name or a string).
            let mut want: Vec<String> = tokens.iter().map(|t| t.0.clone()).collect();
            want.insert(i + 1, "x".into());
            want.insert(i + 1, "<-".into());
            let got: Vec<String> = texts(&mutated).into_iter().map(|t| t.0).collect();
            if got != want {
                continue;
            }
            checked += 1;
            let arrow_at = (end + 1) as u32;
            let parsed = p(&mutated);
            assert_eq!(parsed.reprint(), mutated, "round-trip for {mutated:?}");
            let msgs: Vec<_> = parsed.errors().iter().map(|d| &d.message).collect();
            let hinted = msgs.iter().filter(|m| m.as_str() == BIND_HINT).count();
            assert!(!msgs.is_empty(), "no diagnostic for {mutated:?}");
            assert!(hinted <= 1, "a cascade of hints in {mutated:?}: {msgs:?}");
            for d in parsed.errors() {
                let on_arrow = d
                    .labels
                    .iter()
                    .any(|l| l.span.range == (arrow_at, arrow_at + 2));
                if d.code.0 == "E0001" && on_arrow {
                    assert_eq!(
                        d.message, BIND_HINT,
                        "an [E0001] on the `<-` without the hint in {mutated:?}"
                    );
                    hits += 1;
                    assert_eq!(
                        msgs.len(),
                        1,
                        "a cascade after the hint in {mutated:?}: {msgs:?}"
                    );
                }
            }
        }
    }
    assert!(checked > 100, "only {checked} insertions checked");
    assert!(
        hits * 2 > checked,
        "only {hits} of {checked} errors sit on the `<-`"
    );
}
