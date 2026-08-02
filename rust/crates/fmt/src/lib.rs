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
//!   `Format.hs:18` data-loss hole), and (c) preserves the multiset of
//!   significant tokens (no dropped name / literal / operator). Any failure
//!   falls the whole file back to the lossless reprint.
//!
//! Broken input (error nodes) always takes the lossless path (L8).

mod pretty;

use base::FileId;
use std::collections::BTreeMap;
use syntax::{parse, Parse, SyntaxKind};

/// Format Sky source into canonical, opinionated layout — falling back to a
/// lossless, trivia-preserving reprint whenever the opinionated pass cannot be
/// proven safe + idempotent for this file (see the module docs).
pub fn format_source(src: &str) -> String {
    let parsed = parse(src, FileId(0));
    // Broken input: never re-lay-out; reprint verbatim (L8).
    if parsed.error_node_count() > 0 {
        return parsed.reprint();
    }
    match opinionated(src, &parsed) {
        Some(out) if is_safe(src, &out) => out,
        _ => parsed.reprint(),
    }
}

/// `sky fmt --check`: is `src` already formatted? True when a format pass is a
/// no-op (byte-identical). The CLI turns `false` into a non-zero exit.
pub fn is_formatted(src: &str) -> bool {
    format_source(src) == src
}

/// Run the opinionated printer once. Returns `None` if the file has no module
/// content to lay out (defensive — the reprint path handles it).
fn opinionated(src: &str, parsed: &Parse) -> Option<String> {
    let root = parsed.syntax();
    let file = parsed.tree();
    let mut printer = pretty::Printer::new(src, &root);
    Some(printer.format(&file))
}

/// The safety net: accept the opinionated output only when it is provably a
/// semantics-preserving, idempotent reformat.
fn is_safe(src: &str, out: &str) -> bool {
    let out_parse = parse(out, FileId(0));
    // (a) re-parses cleanly.
    if out_parse.error_node_count() > 0 {
        return false;
    }
    // (b) comments preserved verbatim (own-line AND trailing).
    if comment_multiset(src) != comment_multiset(out) {
        return false;
    }
    // (c) significant tokens preserved (parens excluded — the printer may add
    //     grouping parens around complex call arguments, never drop a name).
    if sig_token_multiset(src) != sig_token_multiset(out) {
        return false;
    }
    // (d) idempotent: a second opinionated pass reproduces `out` exactly.
    match opinionated(out, &out_parse) {
        Some(twice) => twice == out,
        None => false,
    }
}

fn comment_multiset(src: &str) -> BTreeMap<String, usize> {
    let parsed = parse(src, FileId(0));
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

fn sig_token_multiset(src: &str) -> BTreeMap<String, usize> {
    let parsed = parse(src, FileId(0));
    let mut m = BTreeMap::new();
    for e in parsed.syntax().descendants_with_tokens() {
        if let Some(t) = e.into_token() {
            let k = t.kind();
            if k.is_trivia() || matches!(k, SyntaxKind::LParen | SyntaxKind::RParen) {
                continue;
            }
            // Float literals are re-rendered in Haskell `show` notation
            // (`0.05` -> `5.0e-2`); compare by numeric value so the (value-
            // preserving) reformat is accepted while a dropped literal is not.
            let key = if k == SyntaxKind::Float {
                match t.text().parse::<f64>() {
                    Ok(v) => format!("f64:{}", v.to_bits()),
                    Err(_) => t.text().to_string(),
                }
            } else {
                t.text().to_string()
            };
            *m.entry(key).or_insert(0) += 1;
        }
    }
    m
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
        let src = "greet a =\n    \"\"\"n={{String.fromInt (a + 1)}} r={{(a)}} lit=\\{{keep}}\"\"\"\n";
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
}
