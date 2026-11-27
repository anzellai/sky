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
    assert_one_error(
        "x =\n    let\n        f a <- t\n    in\n    f\n",
        BIND_LINE_FORM,
    );
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
    assert_one_error(
        "x =\n    case m of\n        Just x <- m -> x\n",
        CASE_ARM_HINT,
    );
    assert_one_error(
        "x =\n    case m of\n        Just x <- m -> x\n        Nothing -> 0\n",
        CASE_ARM_HINT,
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
        BIND_LINE_FORM,
    );
}

#[test]
fn larrow_in_a_type_annotation_is_a_parse_error() {
    assert_one_error("f : Int <- Int\nf x =\n    x\n", BIND_HINT);
    assert_one_error("f : Int -> Int <- Int\nf x =\n    x\n", BIND_HINT);
    assert_one_error(
        "x =\n    let\n        g : Int <- Int\n        g = 1\n    in\n    g\n",
        BIND_LINE_FORM,
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
            let hinted = msgs.iter().filter(|m| is_bind_hint(m)).count();
            assert!(!msgs.is_empty(), "no diagnostic for {mutated:?}");
            assert!(hinted <= 1, "a cascade of hints in {mutated:?}: {msgs:?}");
            for d in parsed.errors() {
                let on_arrow = d
                    .labels
                    .iter()
                    .any(|l| l.span.range == (arrow_at, arrow_at + 2));
                if d.code.0 == "E0001" && on_arrow {
                    assert!(
                        is_bind_hint(&d.message),
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

/// The one diagnostic for a `let` line with a `<-` whose left side is not a
/// bind-line pattern (design §1), and its variant for an empty left side.
const BIND_LINE_FORM: &str = "a bind line is `pattern <- expression`. The pattern is a name, `_`, `()`, or a pattern in parentheses, e.g. `( a, b ) <- e`, `(x as y) <- e`. A function goes on an `=` line.";
const NEEDS_PATTERN: &str =
    "a bind line needs a pattern: write `_ <- e` to run `e` and drop its result";
const CASE_ARM_HINT: &str = "a case arm uses `->`";

const RESERVED: [&str; 13] = [
    "module", "exposing", "import", "as", "type", "alias", "if", "then", "else", "case", "of",
    "let", "in",
];

fn let_line(lhs: &str, op: &str) -> String {
    format!("x =\n    let\n        {lhs} {op} e\n        y = 1\n    in\n    y\n")
}

fn is_clean(src: &str) -> bool {
    let parsed = p(src);
    parsed.errors().is_empty() && parsed.error_node_count() == 0 && parsed.reprint() == src
}

/// `lhs` is a pattern in a position that takes any pattern (a case arm).
fn parses_as_pattern(lhs: &str) -> bool {
    is_clean(&format!(
        "x =\n    case m of\n        {lhs} ->\n            1\n"
    ))
}

/// Property, exhaustive over left sides: a `let` line `L <- e` is either a
/// clean bind line (L is a pattern, and a lowercase start is a name alone, as
/// on an `=` line), or exactly one `[E0001]` with the bind-line form whose
/// fix, applied, parses: `( L ) <- e` when L is a pattern, `L = e` when it is
/// a function head, else a pattern such as `_` in its place.
#[test]
fn every_let_line_left_side_gets_one_error_with_a_fix_that_parses() {
    let mut sides: Vec<String> = vec![
        "",
        "x",
        "_",
        "()",
        "(a)",
        "( a, b )",
        "{ a, b }",
        "[ a, b ]",
        "(x as y)",
        "x as y",
        "_ as y",
        "( a, b ) as p",
        "x :: xs",
        "(x :: xs)",
        "a, b",
        "a, b, c",
        "_, b",
        "( a, b ), c",
        "g x",
        "go a b",
        "g (Just x)",
        "g _",
        "x y z",
        "_ x",
        "x.y",
        ".y",
        "M.x",
        "a + b",
        "a - b",
        "-1",
        "1",
        "1.5",
        "\"s\"",
        "'c'",
        "True",
        "\"\"\"m\"\"\"",
        "Just x",
        "(Just x)",
        "Just (x)",
        "Wrap a b",
        "(Wrap a b)",
        "M.Wrap a",
        "(M.Wrap a)",
        "Nothing",
        "Wrap",
        "M.Wrap",
        "{ r | a }",
        "{ a = 1 }",
        "(",
        ")",
        "[",
        "]",
        "{",
        "}",
        "( a",
        "a )",
        "\\x",
        "\\x -> x",
        "x -> y",
        "x : Int",
        "f x : Int",
        "(x <- y)",
        "[ x <- y ]",
    ]
    .into_iter()
    .map(String::from)
    .collect();
    for op in [
        "+", "-", "*", "/", "//", "%", "++", "::", "==", "/=", "<", ">", "<=", ">=", "&&", "||",
        "|>", "<|", ">>", "<<", "^", ".", ",", ":", "->", "\\", "|", "..",
    ] {
        sides.push(op.to_string());
        sides.push(format!("a {op}"));
        sides.push(format!("{op} a"));
        sides.push(format!("a {op} b"));
        sides.push(format!("({op})"));
    }
    for kw in [
        "if", "then", "else", "case", "of", "let", "type", "alias", "module", "import", "exposing",
        "as", "foreign", "where", "port",
    ] {
        sides.push(kw.to_string());
        sides.push(format!("x {kw}"));
        sides.push(format!("({kw})"));
    }
    let mut errors = 0;
    for lhs in &sides {
        let src = let_line(lhs, "<-");
        let parsed = p(&src);
        assert_eq!(parsed.reprint(), src, "round-trip for {src:?}");
        let lower_compound =
            lhs.starts_with(|c: char| c.is_ascii_lowercase()) && lhs.split_whitespace().count() > 1;
        let lower_kw = ["foreign", "where", "port"].contains(&lhs.as_str());
        if !lhs.is_empty() && parses_as_pattern(lhs) && !lower_compound && !lower_kw {
            assert!(
                is_clean(&src),
                "{lhs:?} is a pattern, so a bind line: {src:?}"
            );
            continue;
        }
        if lower_kw && is_clean(&src) {
            continue; // `foreign` / `where` / `port` are ordinary names
        }
        errors += 1;
        let msgs: Vec<_> = parsed
            .errors()
            .iter()
            .map(|d| (d.code.0.clone(), d.message.clone()))
            .collect();
        let want = if lhs.is_empty() {
            NEEDS_PATTERN
        } else {
            BIND_LINE_FORM
        };
        // A reserved word used as the name is named as such (its fix is
        // another name), by the reserved-word pass over the same one error.
        let reserved = lhs
            .split(|c: char| !c.is_ascii_alphanumeric())
            .find(|w| RESERVED.contains(w));
        let named_reserved =
            |m: &str| reserved.is_some_and(|w| m.starts_with(&format!("`{w}` is a reserved word")));
        assert!(
            msgs.len() == 1
                && msgs[0].0 == "E0001"
                && (msgs[0].1 == want || named_reserved(&msgs[0].1)),
            "{lhs:?}: expected one [E0001] {want:?}, got {msgs:?}"
        );
        // The fix the message names, applied, parses.
        let fixed = [
            let_line(&format!("({lhs})"), "<-"),
            let_line(lhs, "="),
            let_line("_", "<-"),
        ];
        let fix = fixed.iter().position(|f| is_clean(f));
        assert!(fix.is_some(), "no named fix for {lhs:?} parses");
        if parses_as_pattern(&format!("({lhs})")) {
            assert!(is_clean(&fixed[0]), "`( {lhs} ) <- e` must parse");
        }
    }
    assert!(
        sides.len() > 200 && errors > 150,
        "{} sides, {errors} errors",
        sides.len()
    );
    // The named shapes take the fix the message gives them.
    for (lhs, fix) in [
        ("a, b", "( a, b )"),
        ("x as y", "(x as y)"),
        ("x :: xs", "(x :: xs)"),
    ] {
        assert!(!is_clean(&let_line(lhs, "<-")), "{lhs}");
        assert!(is_clean(&let_line(fix, "<-")), "{fix}");
    }
    for head in ["g x", "go a b", "g (Just x)"] {
        assert!(is_clean(&let_line(head, "=")), "{head}");
    }
}

/// The error sits on the `<-`, the line before and after still parse, and a
/// whole program around it gives that one diagnostic.
#[test]
fn a_bind_line_with_a_wrong_left_side_reports_on_its_arrow_only() {
    let src =
        "x =\n    let\n        a = 1\n        g a <- step a\n        b <- Just a\n    in\n    b\n";
    let parsed = p(src);
    assert_eq!(parsed.errors().len(), 1);
    let arrow = src.find("<-").unwrap() as u32;
    assert!(parsed.errors()[0]
        .labels
        .iter()
        .any(|l| l.span.range == (arrow, arrow + 2)));
    assert_eq!(binds(&parsed).len(), 2);
    assert_one_error(
        "x =\n    let\n        <- println \"x\"\n    in\n    1\n",
        NEEDS_PATTERN,
    );
}

#[test]
fn larrow_in_a_case_arm_pattern_says_case_arms_use_arrow() {
    assert_one_error("x =\n    case m of\n        v <- x -> 1\n", CASE_ARM_HINT);
    let msg = p("x =\n    case m of\n        v <- x -> 1\n").errors()[0]
        .message
        .clone();
    assert!(msg.contains("goes in a `let` block"), "{msg}");
}

/// The bind-line hint, or one of the hints for a position that is already a
/// `let` line or a case arm.
fn is_bind_hint(m: &str) -> bool {
    m == BIND_HINT || m == BIND_LINE_FORM || m == NEEDS_PATTERN || m.starts_with(CASE_ARM_HINT)
}

/// The one diagnostic for a `let` line with neither `=` nor `<-`.
fn bare_line_msg(shown: &str) -> String {
    format!(
        "a line in a `let` block needs `=` or `<-`. To run an effect and drop its result, \
         write `_ <- {shown}`; to name a value, write `name = {shown}`"
    )
}

/// A bare expression line in a `let` block (`println "a"` with no `=` or
/// `<-`) is ONE `[E0001]` on that line, never "expected `=`" at the `in`,
/// in a block with bind lines and in one without.
#[test]
fn a_bare_expression_line_in_a_let_block_is_one_error_on_that_line() {
    let src = "x =\n    let\n        a <- fetch 1\n        println (String.fromInt a)\n    in\n    Task.succeed a\n";
    let parsed = p(src);
    assert_eq!(parsed.reprint(), src);
    let errs = parsed.errors();
    assert_eq!(
        errs.len(),
        1,
        "{:?}",
        errs.iter().map(|d| &d.message).collect::<Vec<_>>()
    );
    assert_eq!(errs[0].code.0, "E0001");
    assert_eq!(errs[0].message, bare_line_msg("println (String.fromInt a)"));
    let at = src.find("println").unwrap() as u32;
    assert!(errs[0].labels.iter().any(|l| l.span.range.0 == at));
    // No bind line in the block.
    let src = "x =\n    let\n        a = 1\n        println \"a\"\n    in\n    a\n";
    assert_one_error(src, "a line in a `let` block needs `=` or `<-`");
    // The first line, and a long line names its first word.
    let src = "x =\n    let\n        println (String.fromInt (List.length [ 1, 2, 3, 4, 5, 6, 7 ]))\n        a = 1\n    in\n    a\n";
    let parsed = p(src);
    assert_eq!(parsed.errors().len(), 1);
    assert_eq!(parsed.errors()[0].message, bare_line_msg("println …"));
}

/// Property: every bare expression line, at every position of a block with
/// and without bind lines, gives exactly one `[E0001]` with the bare-line
/// message on its own first token, the tree reprints byte for byte, and
/// both fixes the message names (`_ <- e`, `name = e`) parse clean.
#[test]
fn every_bare_let_line_gets_one_error_and_both_fixes_parse() {
    let exprs = [
        "println \"a\"",
        "println (String.fromInt a)",
        "f a b",
        "a + 1",
        "a |> f",
        "Task.succeed ()",
        "[ 1, 2 ]",
        "{ r | a = 1 }",
        "( a, b )",
        "\\x -> x",
        "if c then a else b",
        "case m of\n            Just v ->\n                v\n\n            Nothing ->\n                0",
        "let\n            q = 1\n        in q",
        "f\n            a\n            b",
        "1",
        "\"s\"",
        "Just a",
        "a == b",
    ];
    let blocks: [&[&str]; 3] = [
        &["a = 1"],
        &["a <- t", "b = 2"],
        &["a = 1", "_ <- t", "c <- u"],
    ];
    let mut checked = 0;
    for e in exprs {
        for lines in blocks {
            for at in 0..=lines.len() {
                let mk = |line: &str| {
                    let mut ls: Vec<String> = lines.iter().map(|s| s.to_string()).collect();
                    ls.insert(at, line.to_string());
                    format!(
                        "x =\n    let\n        {}\n    in\n    a\n",
                        ls.join("\n        ")
                    )
                };
                let src = mk(e);
                let parsed = p(&src);
                assert_eq!(parsed.reprint(), src, "round-trip for {src:?}");
                let msgs: Vec<_> = parsed
                    .errors()
                    .iter()
                    .map(|d| format!("[{}] {}", d.code.0, d.message))
                    .collect();
                assert!(
                    msgs.len() == 1
                        && msgs[0].starts_with("[E0001] a line in a `let` block needs `=` or `<-`"),
                    "{src:?}: {msgs:?}"
                );
                let line_at = {
                    let mut off = 0;
                    let head = "x =\n    let\n        ";
                    off += head.len();
                    for l in &lines[..at] {
                        off += l.len() + "\n        ".len();
                    }
                    off as u32
                };
                assert!(
                    parsed.errors()[0]
                        .labels
                        .iter()
                        .any(|l| l.span.range.0 == line_at),
                    "the error must sit on the line's first token: {src:?}"
                );
                assert!(is_clean(&mk(&format!("_ <- {e}"))), "`_ <- {e}`");
                assert!(is_clean(&mk(&format!("name = {e}"))), "`name = {e}`");
                checked += 1;
            }
        }
    }
    assert!(checked > 100, "{checked}");
}
