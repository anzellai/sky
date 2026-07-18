#![forbid(unsafe_code)]
//! `syntax` — lexer (`logos`) + lossless CST (`rowan`) + typed AST view, error
//! recovery, layout/indentation (doc 02, doc 04, law L8: parse always produces
//! a tree; the LSP works on broken code).
//!
//! M0 stub: the `SyntaxKind` enum is seeded and the rowan `Language` binding is
//! declared so the CST spine compiles. M1 fills in the real lexer + parser + the
//! reprint round-trip gate.

use logos::Logos;

/// Every syntactic token + node kind. The single source of truth for both the
/// lexer and the rowan tree (doc 04). Ordered; discriminants are stable.
#[derive(Logos, Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
#[repr(u16)]
pub enum SyntaxKind {
    // --- trivia (kept in the tree — L8) ---
    #[regex(r"[ \t]+")]
    Whitespace,
    #[regex(r"--[^\n]*", allow_greedy = true)]
    LineComment,

    // --- a few real tokens to prove the lexer wiring (M1 completes) ---
    #[token("module")]
    ModuleKw,
    #[token("import")]
    ImportKw,
    #[regex(r"[A-Za-z_][A-Za-z0-9_]*")]
    Ident,

    // --- sentinels ---
    /// Lexing error / unexpected byte (recovery lands here — L8).
    Error,
    /// End of input.
    Eof,

    // --- composite node kinds (produced by the parser, not the lexer) ---
    SourceFile,
    ModuleDecl,
    ImportDecl,
}

impl From<SyntaxKind> for rowan::SyntaxKind {
    fn from(k: SyntaxKind) -> Self {
        rowan::SyntaxKind(k as u16)
    }
}

/// The rowan `Language` binding for Sky's CST (doc 04).
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum SkyLang {}

impl rowan::Language for SkyLang {
    type Kind = SyntaxKind;

    fn kind_from_raw(raw: rowan::SyntaxKind) -> Self::Kind {
        // M0: identity-ish mapping over the known range; M1 makes this total +
        // exhaustive (L6). Bounded to the highest declared discriminant.
        match raw.0 {
            0 => SyntaxKind::Whitespace,
            1 => SyntaxKind::LineComment,
            2 => SyntaxKind::ModuleKw,
            3 => SyntaxKind::ImportKw,
            4 => SyntaxKind::Ident,
            5 => SyntaxKind::Error,
            6 => SyntaxKind::Eof,
            7 => SyntaxKind::SourceFile,
            8 => SyntaxKind::ModuleDecl,
            _ => SyntaxKind::ImportDecl,
        }
    }

    fn kind_to_raw(kind: Self::Kind) -> rowan::SyntaxKind {
        kind.into()
    }
}

/// A Sky syntax node in the lossless tree.
pub type SyntaxNode = rowan::SyntaxNode<SkyLang>;
/// A Sky token in the lossless tree.
pub type SyntaxToken = rowan::SyntaxToken<SkyLang>;

/// Lex a source string into `(kind, text)` spans. M0 smoke of the `logos`
/// wiring; the parser that feeds rowan is an M1 deliverable.
pub fn lex(src: &str) -> Vec<(SyntaxKind, &str)> {
    let mut out = Vec::new();
    let mut lexer = SyntaxKind::lexer(src);
    while let Some(res) = lexer.next() {
        let kind = res.unwrap_or(SyntaxKind::Error);
        out.push((kind, lexer.slice()));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lexer_recognises_keywords_and_idents() {
        let toks = lex("module Main");
        let kinds: Vec<_> = toks.iter().map(|(k, _)| *k).collect();
        assert_eq!(
            kinds,
            vec![SyntaxKind::ModuleKw, SyntaxKind::Whitespace, SyntaxKind::Ident]
        );
    }
}
