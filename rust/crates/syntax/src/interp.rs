//! Multiline-string interpolation interior split (doc 04 §9).
//!
//! The `MultilineString` token is lossless raw bytes. Here we re-cover those
//! bytes with `STRING_CHUNK` leaves and `INTERPOLATION` nodes — the latter
//! carrying a real sub-expression with correct file-offset spans, so the LSP
//! can hover/goto inside `{{…}}`. Every byte of the original token is
//! re-emitted, so the round-trip stays exact (L8).
//!
//! Splitting mirrors `splitInterpolation` (doc 03 §1.6): `\{{` is a literal
//! `{{`, `{{` with no closing `}}` is literal, single braces are literal. Note
//! the CST keeps every byte verbatim (the backslash of `\{{` is *not* dropped);
//! escape decoding is an AST-value concern.

use crate::event::build_tree;
use crate::kind::SyntaxKind::*;
use crate::kind::SyntaxNode;
use crate::layout::layout;
use crate::lexer::lex;
use crate::parser::Parser;
use rowan::NodeOrToken;

/// Emit the interior of the current `MultilineString` token. Consumes the raw
/// token (without emitting it) and re-emits its bytes split into chunks +
/// interpolations.
pub(crate) fn multiline(p: &mut Parser) {
    let (start, end) = match p.cur_range() {
        Some(r) => r,
        None => return,
    };
    let text = p.src_slice(start, end).to_string();
    p.skip_raw(); // consume the raw token; interior leaves re-cover its bytes

    let bytes = text.as_bytes();
    let n = bytes.len();
    let open = 3.min(n);
    let close = if n >= 6 && text.ends_with("\"\"\"") {
        n - 3
    } else {
        n
    };

    // opening `"""`
    if open > 0 {
        p.emit_slice(StringChunk, start, start + open as u32);
    }

    let mut i = open;
    let mut chunk_start = open;
    while i < close {
        // escaped brace `\{` — verbatim literal, never an interpolation intro
        if bytes[i] == b'\\' && i + 1 < close && bytes[i + 1] == b'{' {
            i += 2;
            continue;
        }
        if bytes[i] == b'{' && i + 1 < close && bytes[i + 1] == b'{' {
            if let Some(rel) = find_close(&bytes[i + 2..close]) {
                let expr_start = i + 2;
                let expr_end = i + 2 + rel;
                let interp_end = expr_end + 2; // past `}}`
                if chunk_start < i {
                    p.emit_slice(StringChunk, start + chunk_start as u32, start + i as u32);
                }
                emit_interpolation(
                    p,
                    start + i as u32,
                    start + expr_start as u32,
                    start + expr_end as u32,
                    start + interp_end as u32,
                );
                i = interp_end;
                chunk_start = i;
                continue;
            }
        }
        i += 1;
    }

    if chunk_start < close {
        p.emit_slice(
            StringChunk,
            start + chunk_start as u32,
            start + close as u32,
        );
    }
    if close < n {
        p.emit_slice(StringChunk, start + close as u32, end);
    }
}

fn find_close(hay: &[u8]) -> Option<usize> {
    let mut i = 0;
    while i + 1 < hay.len() {
        if hay[i] == b'}' && hay[i + 1] == b'}' {
            return Some(i);
        }
        i += 1;
    }
    None
}

fn emit_interpolation(
    p: &mut Parser,
    open_at: u32,
    expr_start: u32,
    expr_end: u32,
    close_end: u32,
) {
    let m = p.start();
    p.emit_slice(InterpOpen, open_at, expr_start); // `{{`
    p.parse_interp_expr(expr_start, expr_end);
    p.emit_slice(InterpClose, expr_end, close_end); // `}}`
    m.complete(p, Interpolation);
}

/// Parse an interpolation body (`src[start..end]`) as a sub-expression through
/// the *real* Pratt expression grammar (`grammar::expr`), so rich forms —
/// `String.fromInt n`, `record.field.sub`, `f x y` — resolve to the correct
/// HIR expression instead of the hand-rolled mini-classifier's flat leaf /
/// wrong-node fallback (the pre-fix silent miscompile, doc 03 §1.6).
///
/// Mechanism (the trivia-interleave remap): the body is lexed + laid out at
/// *local* offsets, a sub-`Parser` runs `expr` over it, and its events are
/// replayed into a standalone green sub-tree via [`build_tree`]. That green
/// tree resolves all Pratt `forward_parent` re-parenting into plain nested
/// nodes; we then walk it and re-emit every leaf as an offset-shifted
/// `RawSlice` (absolute = `base + local`) plus a `Start`/`Finish` per node.
/// Because the sub-tree is itself lossless (L8), every body byte — including
/// interior whitespace — re-emits exactly once, in order, keeping the outer
/// round-trip byte-exact.
///
/// If the body does not parse as a single, fully-consumed, error-free
/// expression we fall back to flat leaf emission (still lossless, no expr
/// node — same degradation as an unparseable body before this change).
pub(crate) fn body(p: &mut Parser, start: u32, end: u32) {
    let text = p.src_slice(start, end).to_string();
    let base = start;

    let raw = lex(&text);

    // Sub-parse the body as an expression at local offsets.
    let toks = layout(&text, &raw);
    let mut sub = Parser::new(&text, toks, p.file());
    crate::grammar::expr(&mut sub);
    // Good path only when `expr` consumed the whole body with no diagnostics —
    // a partial parse would drop the un-consumed significant tokens from the
    // sub green tree (non-lossless), so route those to the flat fallback.
    let clean = sub.at_end() && sub.diags.is_empty();
    let (events, _diags) = sub.finish();

    if clean {
        let green = build_tree(&text, &raw, events);
        let root = SyntaxNode::new_root(green);
        // Defensive: only take the structured path if the sub-tree covers the
        // body byte-for-byte (guarantees the offset walk lands exactly on
        // `end`); otherwise the flat fallback keeps losslessness.
        if u32::from(root.text_range().len()) == end - base {
            let mut cursor = base;
            emit_green_node(p, &root, &mut cursor);
            debug_assert_eq!(cursor, end, "interp expr walk must cover the body");
            return;
        }
    }

    // Fallback: emit flat leaves (still lossless; no expr node).
    emit_flat(p, &raw, base);
}

/// Re-emit a sub-tree node's leaves as absolute-offset `RawSlice`s, preserving
/// the node structure via `Start`/`Finish`. `cursor` tracks the running
/// absolute byte offset (the sub-tree is contiguous + lossless, so token
/// lengths reconstruct exact spans).
fn emit_green_node(p: &mut Parser, node: &SyntaxNode, cursor: &mut u32) {
    let m = p.start();
    for child in node.children_with_tokens() {
        match child {
            NodeOrToken::Node(n) => emit_green_node(p, &n, cursor),
            NodeOrToken::Token(t) => {
                let len = t.text().len() as u32;
                p.emit_slice(t.kind(), *cursor, *cursor + len);
                *cursor += len;
            }
        }
    }
    m.complete(p, node.kind());
}

/// Flat-leaf fallback: every lexed token re-emitted at its absolute offset. No
/// expression node is produced (an unparseable body contributes no interpolated
/// value — same behaviour as before this change).
fn emit_flat(p: &mut Parser, raw: &[crate::lexer::LexToken], base: u32) {
    for t in raw {
        p.emit_slice(t.kind, base + t.start, base + t.end);
    }
}
