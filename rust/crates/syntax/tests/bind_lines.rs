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
