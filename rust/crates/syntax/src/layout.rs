//! Layout pre-pass (doc 04 §4). A pure function of the lex vector: for every
//! *significant* (non-trivia) token it computes `newline_before`, the indent
//! `col` (tab = 4, per `Space.hs`), and `ws_before` (the negative-literal-arg
//! signal, §7). The Sep/Continue/Close *decisions* are made by the parser,
//! which knows the grammar and drives the context stack — here we produce the
//! column facts those decisions read (doc 04 §4.2, §4.3).

use crate::kind::SyntaxKind;
use crate::lexer::LexToken;

/// A significant (non-trivia) token annotated with the layout facts the parser
/// consults. Byte ranges point into the original source (L3).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct PToken {
    pub kind: SyntaxKind,
    pub start: u32,
    pub end: u32,
    /// First significant token on its line (a `Newline` trivia intervened).
    pub newline_before: bool,
    /// 1-based indent column of this token (tab counts as 4). Meaningful mainly
    /// when `newline_before`.
    pub col: u32,
    /// Any trivia (whitespace/newline/comment) immediately precedes this token.
    pub ws_before: bool,
}

fn advance_col(text: &str, kind: SyntaxKind, col: u32) -> u32 {
    if kind == SyntaxKind::Newline {
        return 1;
    }
    if kind == SyntaxKind::Whitespace {
        let mut c = col;
        for ch in text.chars() {
            match ch {
                ' ' => c += 1,
                '\t' => c += 4,
                '\r' => {}
                _ => c += 1,
            }
        }
        return c;
    }
    // A token spanning newlines (multiline string / block comment) resets the
    // column to the tail of its last line.
    if let Some(nl) = text.rfind('\n') {
        return text[nl + 1..].chars().count() as u32 + 1;
    }
    col + text.chars().count() as u32
}

/// Compute the significant-token stream with layout facts.
pub fn layout(src: &str, raw: &[LexToken]) -> Vec<PToken> {
    let mut out = Vec::new();
    let mut col = 1u32;
    let mut newline_since = false;
    let mut prev_was_trivia = false;

    for tok in raw {
        let text = &src[tok.start as usize..tok.end as usize];
        if tok.kind.is_trivia() {
            if tok.kind == SyntaxKind::Newline {
                newline_since = true;
            }
            prev_was_trivia = true;
        } else {
            out.push(PToken {
                kind: tok.kind,
                start: tok.start,
                end: tok.end,
                newline_before: newline_since,
                col,
                ws_before: prev_was_trivia,
            });
            newline_since = false;
            prev_was_trivia = false;
        }
        col = advance_col(text, tok.kind, col);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::lexer::lex;

    fn ptoks(src: &str) -> Vec<PToken> {
        layout(src, &lex(src))
    }

    #[test]
    fn columns_and_newline_flags() {
        let src = "let\n    a = 10\n    b = 20\nin";
        let p = ptoks(src);
        // let(1,1)  a(col5,newline)  =  10   b(col5,newline)  =  20  in(col1,newline)
        let a = p.iter().find(|t| t.kind == SyntaxKind::LowerIdent).unwrap();
        assert_eq!(a.col, 5);
        assert!(a.newline_before);
        let inkw = p.iter().find(|t| t.kind == SyntaxKind::InKw).unwrap();
        assert_eq!(inkw.col, 1);
        assert!(inkw.newline_before);
    }

    #[test]
    fn tab_counts_as_four() {
        let src = "x =\n\ty";
        let p = ptoks(src);
        let y = p
            .iter()
            .find(|t| t.kind == SyntaxKind::LowerIdent && t.newline_before);
        assert_eq!(y.unwrap().col, 5); // 1 + 4
    }

    #[test]
    fn ws_before_for_negative_literal() {
        // `f -1`: space before `-`, no space before `1`
        let src = "f -1";
        let p = ptoks(src);
        let minus = p.iter().find(|t| t.kind == SyntaxKind::Op).unwrap();
        assert!(minus.ws_before);
        let one = p.iter().find(|t| t.kind == SyntaxKind::Int).unwrap();
        assert!(!one.ws_before);
    }
}
