#![forbid(unsafe_code)]
//! `fmt` — `sky fmt`, the opinionated formatter over the lossless CST
//! (doc 02, doc 04, doc 10).
//!
//! [`format_source`] re-lays-out code to a canonical, Elm-format-compatible
//! shape that mirrors the Haskell oracle (`src/Sky/Format/Format.hs`) — see
//! [`pretty`] for the layout rules. It is guarded by a **safety net** that
//! guarantees the two hard properties unconditionally:
//!
//! * **Idempotent** — `format_source(format_source(x)) == format_source(x)`
//!   byte-for-byte. The opinionated path only ships an output when it re-formats
//!   to itself; otherwise the file falls back to the lossless CST reprint, which
//!   is the identity on any well-formed source (the M1 round-trip invariant).
//! * **Semantics-preserving + no data loss** — the opinionated output is
//!   accepted only when it (a) re-parses with zero error nodes, (b) preserves
//!   every comment verbatim (own-line *and* trailing — closing the oracle's
//!   `Format.hs:18` data-loss hole) and in place (each comment keeps the
//!   significant token just before it and just after it, named by text and
//!   occurrence, so a moved comment fails the net), and (c) preserves the multiset of
//!   significant tokens (no dropped name / literal / operator). On a failure
//!   the fallback is per top-level declaration: every declaration that
//!   formats safely is formatted, and a declaration the printer cannot lay
//!   out without losing a comment is kept exactly as written
//!   ([`format_report`] names it). Only when that mix is not safe either does
//!   the whole file fall back to the lossless reprint.
//!
//! Broken input (error nodes) always takes the lossless path (L8).

mod pretty;

use base::FileId;
use std::collections::{BTreeMap, BTreeSet};
use syntax::{parse, Parse, SyntaxKind};

/// Format Sky source into canonical, opinionated layout — keeping a
/// declaration exactly as written when the opinionated pass cannot be proven
/// safe + idempotent for it (see the module docs).
pub fn format_source(src: &str) -> String {
    format_report(src).text
}

/// The result of [`format_report`].
pub struct Formatted {
    /// The formatted source.
    pub text: String,
    /// The 1-based first line of each top-level declaration kept exactly as
    /// written, because formatting it would lose or move a comment.
    pub kept: Vec<usize>,
}

/// Format Sky source, and say which declarations were kept as written.
pub fn format_report(src: &str) -> Formatted {
    let parsed = parse(src, FileId(0));
    // Broken input: never re-lay-out; reprint verbatim (L8).
    if parsed.error_node_count() > 0 {
        return Formatted {
            text: parsed.reprint(),
            kept: Vec::new(),
        };
    }
    // The source's comments and significant tokens, read once: every safety
    // check below compares an output against them.
    let want = Multisets::of(&parsed);
    let none = BTreeSet::new();
    let (out, units) = opinionated(src, &parsed, &none);
    if is_safe(&want, &out, &none) {
        return Formatted {
            text: out,
            kept: Vec::new(),
        };
    }
    // Per declaration. A unit can only fail the net by losing or moving a
    // comment, so first try each unit that HOLDS a comment on its own, every
    // other unit formatted. The mix is then checked as a whole; only if that
    // fails is every unit tried one at a time (the exact, slower search).
    let all: BTreeSet<usize> = units.iter().map(|u| u.0).collect();
    let comment_lines = comment_lines(&parsed);
    let with_comments: Vec<usize> = units
        .iter()
        .enumerate()
        .filter(|(i, u)| {
            let end = units.get(i + 1).map_or(usize::MAX, |n| n.1);
            comment_lines.iter().any(|&l| l >= u.1 && l < end)
        })
        .map(|(_, u)| u.0)
        .collect();
    let keep_of = |candidates: &mut dyn Iterator<Item = usize>| {
        let mut keep = BTreeSet::new();
        for unit in candidates {
            let mut others = all.clone();
            others.remove(&unit);
            let (one, _) = opinionated(src, &parsed, &others);
            if !is_safe(&want, &one, &others) {
                keep.insert(unit);
            }
        }
        keep
    };
    // Each unit that holds a comment is checked ON ITS OWN: its source lines
    // are formatted as a file by themselves and put through the same net. A
    // top-level declaration is self-contained, so this decides it in time
    // that grows with the file's size, not with size × units (a 10,000-line
    // commented file took tens of seconds with one whole-file pass per unit).
    // The mix is still checked as a whole below.
    let lines: Vec<&str> = src.split_inclusive('\n').collect();
    let unsafe_alone = || -> BTreeSet<usize> {
        let mut found = BTreeSet::new();
        for (i, u) in units.iter().enumerate() {
            if !with_comments.contains(&u.0) {
                continue;
            }
            let end = units
                .get(i + 1)
                .map_or(lines.len(), |n| n.1)
                .min(lines.len());
            let slice: String = lines[u.1.min(end)..end].concat();
            let p = parse(&slice, FileId(0));
            let alone_safe = p.error_node_count() == 0 && {
                let want_alone = Multisets::of(&p);
                let (out, _) = opinionated(&slice, &p, &BTreeSet::new());
                is_safe(&want_alone, &out, &BTreeSet::new())
            };
            if !alone_safe {
                found.insert(u.0);
            }
        }
        found
    };
    // The slow exact search (one whole-file pass per unit) runs only when the
    // fast choice does not give a safe file. Each candidate is built lazily.
    let candidates: [&dyn Fn() -> BTreeSet<usize>; 2] =
        [&unsafe_alone, &|| keep_of(&mut all.iter().copied())];
    for candidate in candidates {
        let keep = candidate();
        let (mixed, _) = opinionated(src, &parsed, &keep);
        if !keep.is_empty() && is_safe(&want, &mixed, &keep) {
            let kept = units
                .iter()
                .filter(|u| keep.contains(&u.0))
                .map(|u| u.1 + 1)
                .collect();
            return Formatted { text: mixed, kept };
        }
    }
    Formatted {
        text: parsed.reprint(),
        kept: Vec::new(),
    }
}

/// The 0-based line of every comment token in `parsed`.
fn comment_lines(parsed: &Parse) -> Vec<usize> {
    let mut lines = Vec::new();
    let text = parsed.syntax().to_string();
    for e in parsed.syntax().descendants_with_tokens() {
        if let Some(t) = e.into_token() {
            if matches!(t.kind(), SyntaxKind::LineComment | SyntaxKind::BlockComment) {
                let at: usize = t.text_range().start().into();
                lines.push(text[..at].matches('\n').count());
            }
        }
    }
    lines
}

/// `sky fmt --check`: is `src` already formatted? True when a format pass is a
/// no-op (byte-identical). A declaration kept as written does not count, so
/// only a declaration that formats and would change makes it false. The CLI
/// turns `false` into a non-zero exit.
pub fn is_formatted(src: &str) -> bool {
    format_source(src) == src
}

/// Run the opinionated printer once, with the units in `verbatim` (by the
/// index of their first decl) kept as written. Returns the text and every
/// unit printed (its first decl index and 0-based first line).
fn opinionated(
    src: &str,
    parsed: &Parse,
    verbatim: &BTreeSet<usize>,
) -> (String, Vec<(usize, usize)>) {
    let root = parsed.syntax();
    let file = parsed.tree();
    let mut printer = pretty::Printer::new(src, &root).with_verbatim(verbatim.clone());
    let out = printer.format(&file);
    (out, printer.units)
}

/// The safety net: accept the opinionated output only when it is provably a
/// semantics-preserving, idempotent reformat.
fn is_safe(want: &Multisets, out: &str, verbatim: &BTreeSet<usize>) -> bool {
    let out_parse = parse(out, FileId(0));
    // (a) re-parses cleanly.
    if out_parse.error_node_count() > 0 {
        return false;
    }
    // (b) comments preserved verbatim (own-line AND trailing), each between
    //     the same two significant tokens as in the source (its anchor), and
    //     (c) significant tokens preserved (parens excluded — the printer may
    //     add grouping parens around complex call arguments, never drop a
    //     name).
    if Multisets::of(&out_parse) != *want {
        return false;
    }
    // (d) idempotent: a second opinionated pass (keeping the same units as
    //     written) reproduces `out` exactly.
    opinionated(out, &out_parse, verbatim).0 == out
}

/// The comment multiset, the significant-token multiset and the comment
/// anchors of one parse.
#[derive(PartialEq, Eq)]
struct Multisets {
    comments: BTreeMap<String, usize>,
    tokens: BTreeMap<String, usize>,
    anchors: Vec<Anchor>,
}

impl Multisets {
    fn of(parsed: &Parse) -> Self {
        Multisets {
            comments: comment_multiset_of(parsed),
            tokens: sig_token_multiset_of(parsed),
            anchors: anchors_of(parsed),
        }
    }
}

/// A significant token named so that the name survives re-layout: its
/// comparison key (see [`sig_key`]) and how many tokens with that key come
/// before it in the file.
type TokenId = (String, usize);

/// Where one comment sits: the significant token just before it and just
/// after it (`None` at the start or end of the file), its position among the
/// comments in that same gap, and its text. A formatted output must give
/// every comment the same anchor, so a comment that the printer moves to
/// another place fails the safety net even though the comment multiset is
/// the same.
#[derive(PartialEq, Eq, PartialOrd, Ord)]
struct Anchor {
    before: Option<TokenId>,
    after: Option<TokenId>,
    in_gap: usize,
    text: String,
}

/// The anchor of every comment in `parsed`, sorted. One walk over the tokens.
/// Parentheses are not anchors: the printer adds and drops grouping parens.
fn anchors_of(parsed: &Parse) -> Vec<Anchor> {
    let mut seen: std::collections::HashMap<String, usize> = std::collections::HashMap::new();
    let mut before: Option<TokenId> = None;
    // Comments since the last significant token, waiting for their `after`.
    let mut pending: Vec<String> = Vec::new();
    let mut out = Vec::new();
    for e in parsed.syntax().descendants_with_tokens() {
        let Some(t) = e.into_token() else { continue };
        let k = t.kind();
        if matches!(k, SyntaxKind::LineComment | SyntaxKind::BlockComment) {
            pending.push(t.text().to_string());
            continue;
        }
        if k.is_trivia() || matches!(k, SyntaxKind::LParen | SyntaxKind::RParen) {
            continue;
        }
        let key = sig_key(&t);
        let n = seen.entry(key.clone()).or_insert(0);
        let id = (key, *n);
        *n += 1;
        for (in_gap, text) in pending.drain(..).enumerate() {
            out.push(Anchor {
                before: before.clone(),
                after: Some(id.clone()),
                in_gap,
                text,
            });
        }
        before = Some(id);
    }
    for (in_gap, text) in pending.drain(..).enumerate() {
        out.push(Anchor {
            before: before.clone(),
            after: None,
            in_gap,
            text,
        });
    }
    out.sort();
    out
}

fn comment_multiset_of(parsed: &Parse) -> BTreeMap<String, usize> {
    let mut m = BTreeMap::new();
    for e in parsed.syntax().descendants_with_tokens() {
        if let Some(t) = e.into_token() {
            if matches!(t.kind(), SyntaxKind::LineComment | SyntaxKind::BlockComment) {
                *m.entry(t.text().to_string()).or_insert(0) += 1;
            }
        }
    }
    m
}

fn sig_token_multiset_of(parsed: &Parse) -> BTreeMap<String, usize> {
    let mut m = BTreeMap::new();
    for e in parsed.syntax().descendants_with_tokens() {
        if let Some(t) = e.into_token() {
            let k = t.kind();
            if k.is_trivia() || matches!(k, SyntaxKind::LParen | SyntaxKind::RParen) {
                continue;
            }
            *m.entry(sig_key(&t)).or_insert(0) += 1;
        }
    }
    m
}

/// The comparison key of a significant token. Float literals are re-rendered
/// in Haskell `show` notation (`0.05` -> `5.0e-2`); compare by numeric value
/// so the (value-preserving) reformat is accepted while a dropped literal is
/// not.
fn sig_key(t: &syntax::SyntaxToken) -> String {
    if t.kind() == SyntaxKind::Float {
        match t.text().parse::<f64>() {
            Ok(v) => format!("f64:{}", v.to_bits()),
            Err(_) => t.text().to_string(),
        }
    } else {
        t.text().to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn idempotent(src: &str) {
        let once = format_source(src);
        assert_eq!(format_source(&once), once, "not idempotent for {src:?}");
    }

    #[test]
    fn format_is_idempotent_small() {
        for src in [
            "module Main",
            "module Main exposing (main)\n\nmain =\n    println \"hi\"\n",
            "x = 1  -- trailing comment survives\n",
            "",
        ] {
            idempotent(src);
        }
    }

    #[test]
    fn normalises_intra_line_whitespace() {
        let src = "module Main exposing (main)\n\nmain =\n    println   \"hi\"\n";
        let out = format_source(src);
        assert!(out.contains("println \"hi\""), "got: {out:?}");
        idempotent(src);
    }

    #[test]
    fn two_blank_lines_between_values() {
        let src = "module M exposing (a, b)\n\na =\n    1\nb =\n    2\n";
        let out = format_source(src);
        assert_eq!(
            out, "module M exposing (a, b)\n\n\na =\n    1\n\n\nb =\n    2\n",
            "got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn trailing_comment_file_falls_back_lossless() {
        // Trailing (inline) comment: the printer would drop it, so the safety
        // net falls back to the lossless reprint — data preserved.
        let src = "x =\n    1  -- keep me\n";
        let out = format_source(src);
        assert!(out.contains("-- keep me"), "trailing comment lost: {out:?}");
        idempotent(src);
    }

    #[test]
    fn own_line_comment_preserved() {
        let src = "-- header\nx =\n    1\n";
        let out = format_source(src);
        assert!(out.contains("-- header"), "own-line comment lost: {out:?}");
        idempotent(src);
    }

    #[test]
    fn broken_input_reprints_verbatim() {
        let src = "main =\n    @@@ let\n";
        assert_eq!(format_source(src), src);
    }

    #[test]
    fn type_record_two_fields_break_multiline() {
        let src =
            "module M exposing (Model)\n\ntype alias Model = { count : Int, name : String }\n";
        let out = format_source(src);
        assert!(
            out.contains("type alias Model =\n    { count : Int\n    , name : String\n    }\n"),
            "got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn redundant_type_parens_dropped_in_tail() {
        let src = "f : Model -> (Html Msg)\nf m =\n    x\n";
        let out = format_source(src);
        assert!(out.contains("f : Model -> Html Msg\n"), "got: {out:?}");
        idempotent(src);
    }

    #[test]
    fn float_uses_haskell_show_notation() {
        let src = "x =\n    0.05\n";
        let out = format_source(src);
        assert!(out.contains("5.0e-2"), "got: {out:?}");
        idempotent(src);
    }

    #[test]
    fn pipeline_greedy_fill_then_one_per_line() {
        let src = "baz z =\n    z |> add 1 |> add 2 |> add 3 |> add 4 |> add 5 |> add 6 |> add 7 |> add 8 |> add 9\n";
        let out = format_source(src);
        assert!(
            out.contains(
                "z |> add 1 |> add 2 |> add 3 |> add 4 |> add 5 |> add 6 |> add 7 |> add 8\n"
            ),
            "first line should greedy-fill; got: {out:?}"
        );
        assert!(
            out.contains("        |> add 9\n"),
            "overflow wraps at op-col; got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn call_args_break_all_or_nothing() {
        let src = "a a1 =\n    reallyLongFunctionName argumentOne argumentTwo argumentThree argumentFour argFive argSix\n";
        let out = format_source(src);
        assert!(
            out.contains("reallyLongFunctionName\n        argumentOne\n"),
            "got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn section_comment_stays_above_its_decl() {
        // A block-comment section header must attach to the following decl and
        // remain stable across passes (the leading-trivia / lambda-slurp bug).
        let src = "module M exposing (Msg)\n\n\n-- SECTION\ntype Msg\n    = A\n    | B\n";
        let out = format_source(src);
        assert!(out.contains("-- SECTION\ntype Msg\n"), "got: {out:?}");
        idempotent(src);
    }

    #[test]
    fn multiline_string_arg_stable() {
        let src = "run =\n    exec conn \"\"\"CREATE TABLE t (\n    id INT\n)\"\"\"\n";
        // Must be idempotent — the multiline node's leading trivia must not
        // accumulate blank lines across passes.
        idempotent(src);
        let _ = format_source(src);
    }

    #[test]
    fn interpolation_parens_preserved() {
        // Regression guard: `sky fmt` must NOT strip parentheses inside {{...}}
        // string interpolation. The formatter renders a multiline string as a raw
        // source slice (verbatim), so this holds by construction — but the
        // safety-net token multiset deliberately EXCLUDES parens (the printer may
        // add grouping parens on call args), so a future refactor that re-rendered
        // interpolation expressions could drop a paren UNDETECTED by the gate. A
        // dropped paren changes meaning (`String.fromInt (a + 1)` vs
        // `String.fromInt a + 1`) or breaks compilation, so assert it directly.
        let src =
            "greet a =\n    \"\"\"n={{String.fromInt (a + 1)}} r={{(a)}} lit=\\{{keep}}\"\"\"\n";
        let out = format_source(src);
        assert!(
            out.contains("{{String.fromInt (a + 1)}}"),
            "parenthesized interpolation must survive formatting verbatim; got: {out:?}"
        );
        assert!(
            out.contains("{{(a)}}"),
            "redundant interpolation parens must survive; got: {out:?}"
        );
        assert!(
            out.contains("\\{{keep}}"),
            "escaped literal braces must survive (not treated as interpolation); got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn header_comment_stays_above_imports() {
        // A file-header comment placed before the imports must remain ABOVE the
        // import block — not fall through to the first decl (below the imports).
        let src = "module M exposing (main)\n\n-- header doc\nimport A\n\nmain =\n    1\n";
        let out = format_source(src);
        assert!(
            out.find("-- header doc").unwrap() < out.find("import A").unwrap(),
            "header comment must stay above imports; got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn module_doc_comment_stays_above_module() {
        // A module-doc comment above the `module` keyword (the stdlib convention,
        // read by `sky doc`) must stay above the module header — not get swept
        // below it to the first import/decl.
        let src =
            "-- | M — a module.\n--\n-- Details.\nmodule M exposing (main)\n\nmain =\n    1\n";
        let out = format_source(src);
        assert!(
            out.find("-- | M — a module.").unwrap() < out.find("module M").unwrap(),
            "module-doc comment must stay above the module header; got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn let_leading_comment_stays_above_first_binding() {
        // A comment between `let` and its first binding (even mis-indented) must
        // render above that binding — not get deferred below the `in` body.
        let src = "main =\n    let\n    -- note about x\n        x = 1\n    in\n        x\n";
        let out = format_source(src);
        let ci = out.find("-- note about x").unwrap();
        assert!(
            ci > out.find("let").unwrap() && ci < out.find("in\n").unwrap(),
            "let-leading comment must stay inside the let above the binding; got: {out:?}"
        );
        idempotent(src);
    }

    #[test]
    fn case_arm_body_comment_stays_with_its_arm() {
        // A comment between an arm's `->` and its body (at the arm's own column)
        // must stay with that arm — not get slurped onto the following arm.
        let src =
            "f x =\n    case x of\n        A ->\n        -- note about A\n            1\n\n        B ->\n            2\n";
        let out = format_source(src);
        assert!(
            out.find("-- note about A").unwrap() < out.find("B ->").unwrap(),
            "arm-body comment must stay with its own arm; got: {out:?}"
        );
        idempotent(src);
    }

    /// Assert `src` is already in canonical form (formatting is the identity,
    /// so every comment stays exactly where it was) and is idempotent.
    fn stays_put(src: &str) {
        // The opinionated printer itself, not the lossless fallback, must keep
        // the layout: a fallback would hide a moved comment.
        let parsed = parse(src, FileId(0));
        let printed = opinionated(src, &parsed, &BTreeSet::new()).0;
        assert_eq!(printed, src, "the printer moved code or a comment");
        assert_eq!(
            format_source(src),
            src,
            "formatting moved code or a comment"
        );
        idempotent(src);
    }

    #[test]
    fn then_branch_comment_stays_above_the_then_branch() {
        // Reduced from examples/73-ai-train (the resume branch of `update`):
        // `sky fmt` moved a comment above the then-branch into the
        // `Task.map (\_ -> ..)` lambda body. The if had no drain before its
        // then-branch, and the lambda's body drain was unbounded below.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    if x then\n        -- note\n        a |> Task.map (\\_ -> b)\n\n    else\n        c\n",
        );
        // Without a lambda the else-branch drain claimed it (it moved below
        // `else`).
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    if x then\n        -- note\n        a |> g\n\n    else\n        c\n",
        );
        // The original shape: a parenthesised if, piped on.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    (if x then\n        -- note\n        a |> Task.map (\\_ -> ())\n\n    else\n        Task.succeed ())\n        |> Task.andThen (\\_ -> b)\n",
        );
    }

    #[test]
    fn comment_before_else_stays_above_else() {
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    if x then\n        a\n\n    -- note\n    else\n        c\n",
        );
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    if x then\n        a\n\n    else\n        -- note\n        c\n",
        );
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    if x then\n        a\n\n    -- note\n    else if y then\n        b\n\n    else\n        c\n",
        );
    }

    #[test]
    fn pipeline_comment_stays_above_its_operator() {
        // A comment above a `|> Task.andThen (\a -> ..)` line moved into the
        // lambda body.
        stays_put(
            "module M exposing (f)\n\n\nf t =\n    t\n        -- note\n        |> Task.andThen (\\a -> b a)\n",
        );
        stays_put(
            "module M exposing (f)\n\n\nf t =\n    t |> List.map (\\a -> b a)\n        -- note\n        |> List.filter (\\a -> b a)\n",
        );
        // A comment inside the lambda body stays there.
        stays_put(
            "module M exposing (f)\n\n\nf t =\n    t\n        |> Task.andThen\n               (\\a ->\n                   -- note\n                   b a)\n",
        );
    }

    #[test]
    fn call_argument_comment_stays_above_its_argument() {
        // Above a lambda argument: it moved into the lambda body.
        stays_put(
            "module M exposing (f)\n\n\nf xs =\n    List.map\n        -- note\n        (\\x -> x)\n        xs\n",
        );
        // Above the last argument: it moved below the whole decl.
        stays_put(
            "module M exposing (f)\n\n\nf xs =\n    List.map\n        (\\x -> x)\n        -- note\n        xs\n",
        );
    }

    #[test]
    fn let_binding_body_comment_stays_above_the_body() {
        // It moved below the `in` body.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    let\n        y =\n            -- note\n            a |> g\n    in\n        y\n",
        );
    }

    #[test]
    fn let_in_comments_stay_on_their_side_of_in() {
        // A comment between `in` and the body moved above `in`, into the
        // bindings.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    let\n        y = x\n    in\n        -- note\n        y\n",
        );
        // A comment between the last binding and `in` stays above `in`.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    let\n        y = x\n        -- note\n    in\n        y\n",
        );
        // A comment between two bindings stays above the second one.
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    let\n        y =\n            if x then\n                1\n\n            else\n                2\n        -- note\n        z = y\n    in\n        z\n",
        );
    }

    #[test]
    fn record_field_value_comment_stays_above_the_value() {
        stays_put(
            "module M exposing (f)\n\n\nf x =\n    { a =\n        -- note\n        x |> g\n    , b = 1\n    }\n",
        );
        stays_put(
            "module M exposing (f)\n\n\nf m =\n    { m\n        | a =\n            -- note\n            m.a |> g\n    }\n",
        );
    }

    #[test]
    fn list_pattern_keeps_spaces_inside_its_brackets() {
        // `[ only ]` became `[only]`. A list pattern has the same spacing as a
        // list expression: `[ a, b ]`, and `[]` when empty.
        stays_put(
            "module M exposing (f)\n\n\nf xs =\n    case xs of\n\n        [ only ] ->\n            only\n\n        [ a, b ] ->\n            a\n\n        [] ->\n            0\n\n        _ ->\n            1\n",
        );
        let out = format_source(
            "module M exposing (f)\n\n\nf xs =\n    case xs of\n\n        [only] ->\n            only\n\n        _ ->\n            1\n",
        );
        assert!(out.contains("        [ only ] ->\n"), "got: {out:?}");
    }

    #[test]
    fn annotation_record_type_stays_on_one_line_when_it_fits() {
        // Every record type with two or more fields broke, which in an
        // annotation put the commas and the closing brace at column 0.
        stays_put(
            "module M exposing (island)\n\n\nisland : { name : String, id : String, props : Value } -> List (Attribute msg) -> Element msg\nisland r attrs =\n    r\n",
        );
        // A record the source had broken, or one that does not fit, breaks
        // the annotation into the standard form.
        stays_put(
            "module M exposing (g)\n\n\ng :\n    { name : String\n    , id : String\n    }\n    -> Int\ng r =\n    1\n",
        );
        let long = "module M exposing (h)\n\n\nh : { aaaaaaaaaaaaaaa : String, bbbbbbbbbbbbbbbbbbbb : String, cccccccccccccccccccc : Int } -> Int\nh r =\n    1\n";
        assert_eq!(
            format_source(long),
            "module M exposing (h)\n\n\nh :\n    { aaaaaaaaaaaaaaa : String\n    , bbbbbbbbbbbbbbbbbbbb : String\n    , cccccccccccccccccccc : Int\n    }\n    -> Int\nh r =\n    1\n"
        );
        idempotent(long);
        // A record inside another type joins when it fits: its broken source
        // layout is what the old rule printed, not a choice of the author.
        let nested = "module M exposing (stats)\n\n\nstats : Cache k v -> Task Error { hits : Int\n, misses : Int\n, evictions : Int\n}\nstats c =\n    c\n";
        assert_eq!(
            format_source(nested),
            "module M exposing (stats)\n\n\nstats : Cache k v -> Task Error { hits : Int, misses : Int, evictions : Int }\nstats c =\n    c\n"
        );
        idempotent(nested);
        // A record alias body keeps its rule: two or more fields break.
        stays_put(
            "module M exposing (Model)\n\ntype alias Model =\n    { count : Int\n    , name : String\n    }\n",
        );
    }

    #[test]
    fn mixed_precedence_chain_keeps_its_groups() {
        // The printer flattened `==` into the `||` chain and broke between
        // `t` and `== "main"`. A tighter-binding operand is one group.
        stays_put(
            "module M exposing (f)\n\n\nf t =\n    t == \"div\" || t == \"p\" || isHeadingTag t || t == \"section\" || t == \"form\"\n        || t == \"main\"\n        || t == \"nav\"\n",
        );
        let out = format_source(
            "module M exposing (f)\n\n\nf t =\n    t == \"div\"\n        || t == \"p\"\n        || isHeadingTag t\n        || t == \"section\"\n        || t == \"form\"\n        || t == \"main\"\n        || t == \"nav\"\n",
        );
        assert!(!out.contains("|| t\n"), "got: {out:?}");
        idempotent(&out);
    }

    #[test]
    fn exposing_and_annotation_one_line_rules() {
        // The intended rules, unchanged: an exposing list breaks only past
        // 100 columns, and an annotation with no record in it stays on one
        // line.
        let src = "module M exposing\n    ( a\n    , b\n    )\n\n\na : Int\n    -> Int\na x =\n    x\n\n\nb : Int\nb =\n    1\n";
        assert_eq!(
            format_source(src),
            "module M exposing (a, b)\n\n\na : Int -> Int\na x =\n    x\n\n\nb : Int\nb =\n    1\n"
        );
    }

    #[test]
    fn multiline_record_update_puts_base_alone_and_indents_fields() {
        // A record update that does not fit on one line keeps `{ base` alone on
        // the first line and every field one step in, led by `|` then `,`. The
        // old layout put the first field on the `{ base |` line and the rest at
        // the outer indent.
        let src = "module M exposing (f)\n\n\nf row =\n    { emptyItem | id = dictInt \"id\" row, title = dictGet \"title\" row, body = dictGet \"body\" row }\n";
        let out = format_source(src);
        assert_eq!(
            out,
            "module M exposing (f)\n\n\nf row =\n    { emptyItem\n        | id = dictInt \"id\" row\n        , title = dictGet \"title\" row\n        , body = dictGet \"body\" row\n    }\n",
            "got: {out:?}"
        );
        idempotent(src);
        // The old layout reformats to the new one.
        let old = "module M exposing (f)\n\n\nf row =\n    { emptyItem | id = dictInt \"id\" row\n    , title = dictGet \"title\" row\n    , body = dictGet \"body\" row\n    }\n";
        let out = format_source(old);
        assert_eq!(
            out,
            "module M exposing (f)\n\n\nf row =\n    { emptyItem\n        | id = dictInt \"id\" row\n        , title = dictGet \"title\" row\n        , body = dictGet \"body\" row\n    }\n",
            "got: {out:?}"
        );
        idempotent(old);
        // A multi-line value breaks one step in from its field, and a comment
        // before a field stays above that field.
        let nested = "module M exposing (f)\n\n\nf m =\n    { m\n        | a = 1\n        -- the list\n        , items =\n            [ \"one one one one one one\"\n            , \"two two two two two two two\"\n            , \"three\"\n            ]\n    }\n";
        assert_eq!(format_source(nested), nested);
        idempotent(nested);
        // An update that fits stays on one line.
        let short = "module M exposing (f)\n\n\nf m =\n    { m | a = 1, b = 2 }\n";
        assert_eq!(format_source(short), short);
    }
}
