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

use crate::kind::SyntaxKind::{self, *};
use crate::lexer::lex;
use crate::parser::Parser;

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
    let close = if n >= 6 && text.ends_with("\"\"\"") { n - 3 } else { n };

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
        p.emit_slice(StringChunk, start + chunk_start as u32, start + close as u32);
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

fn emit_interpolation(p: &mut Parser, open_at: u32, expr_start: u32, expr_end: u32, close_end: u32) {
    let m = p.start();
    p.emit_slice(InterpOpen, open_at, expr_start); // `{{`
    p.parse_interp_expr(expr_start, expr_end);
    p.emit_slice(InterpClose, expr_end, close_end); // `}}`
    m.complete(p, Interpolation);
}

/// Parse an interpolation body (`src[start..end]`) as a sub-expression. Lossless
/// — every byte (including interior whitespace) is re-emitted with an absolute
/// offset; the recognised shapes get a real expression node (doc 03 §1.6).
pub(crate) fn body(p: &mut Parser, start: u32, end: u32) {
    let text = p.src_slice(start, end).to_string();
    let raw = lex(&text);
    let base = start;

    let sig: Vec<usize> = (0..raw.len())
        .filter(|&k| !raw[k].kind.is_trivia())
        .collect();
    let kinds: Vec<SyntaxKind> = sig.iter().map(|&k| raw[k].kind).collect();

    match classify(&kinds) {
        None => {
            // literal-fallback shape: emit flat leaves (still lossless).
            for t in &raw {
                p.emit_slice(t.kind, base + t.start, base + t.end);
            }
        }
        Some(node) => {
            let first = sig[0];
            let last = *sig.last().unwrap();
            for t in &raw[..first] {
                p.emit_slice(t.kind, base + t.start, base + t.end);
            }
            let m = p.start();
            for t in &raw[first..=last] {
                p.emit_slice(t.kind, base + t.start, base + t.end);
            }
            m.complete(p, node);
            for t in &raw[last + 1..] {
                p.emit_slice(t.kind, base + t.start, base + t.end);
            }
        }
    }
}

fn classify(kinds: &[SyntaxKind]) -> Option<SyntaxKind> {
    match kinds.first()? {
        UpperIdent => {
            if kinds.len() >= 3 && kinds[1] == Dot {
                Some(QualRefExpr)
            } else if kinds.len() == 1 {
                Some(RefExpr)
            } else {
                None
            }
        }
        LowerIdent => {
            if kinds.len() == 1 {
                Some(RefExpr)
            } else if kinds[1] == Dot {
                Some(FieldAccess)
            } else {
                Some(CallExpr)
            }
        }
        _ => None,
    }
}
