//! Lexer (doc 04 §3). `logos` for the regular tokens; hand-scanning callbacks
//! for the four non-regular ones — nested `{- -}` block comments, `"""…"""`
//! triple strings, single-line strings with escapes, char literals. Every byte
//! becomes a token (trivia included) — losslessness starts here (L8).
//!
//! Maximal-munch operators and identifiers are lexed as coarse runs, then
//! reclassified by exact text (keywords, operator glyphs) — matching the
//! Haskell `keyword`/`Symbol.hs` design (doc 04 §3.4, §3.5).

use crate::kind::SyntaxKind;
use logos::{Lexer, Logos};

/// A lexed token: a kind plus a half-open byte range into the source.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct LexToken {
    pub kind: SyntaxKind,
    pub start: u32,
    pub end: u32,
}

/// The coarse `logos` token classes. Keywords and operator glyphs are one class
/// each here and split by text in [`map_kind`].
#[derive(Logos, Clone, Copy, PartialEq, Eq, Debug)]
enum Lx {
    #[regex(r"[ \t\r]+")]
    Ws,
    #[token("\n")]
    Nl,
    #[regex(r"--[^\n]*", priority = 4, allow_greedy = true)]
    LineComment,
    #[token("{-", lex_block_comment)]
    BlockComment,

    #[regex(r"0x[0-9a-fA-F]+")]
    Hex,
    // float: `1.5`, `1.5e-2`, or integer-with-exponent `1e6`
    #[regex(r"[0-9]+(\.[0-9]+([eE][+-]?[0-9]+)?|[eE][+-]?[0-9]+)")]
    Float,
    #[regex(r"[0-9]+")]
    Int,

    #[token("\"\"\"", lex_multiline)]
    Multiline,
    #[token("\"", lex_string)]
    Str,
    #[token("'", lex_char)]
    Char,

    // one identifier run; lower/upper/keyword split in `map_kind`
    #[regex(r"[_\p{L}][_\p{L}\p{N}]*")]
    Ident,

    // maximal operator run (char class per Symbol.hs, sans `'` which is a char
    // literal delimiter and sans structural punctuation)
    #[regex(r"[-+*/<>=!&|^~%?@#$:.\\]+", priority = 2)]
    Op,

    #[token("(")]
    LParen,
    #[token(")")]
    RParen,
    #[token("[")]
    LBrack,
    #[token("]")]
    RBrack,
    #[token("{")]
    LBrace,
    #[token("}")]
    RBrace,
    #[token(",")]
    Comma,
    #[token("_", priority = 5)]
    Underscore,
}

fn lex_block_comment(lex: &mut Lexer<Lx>) {
    // opening `{-` already consumed; scan the remainder counting nesting.
    let rem = lex.remainder();
    let bytes = rem.as_bytes();
    let mut depth = 1usize;
    let mut i = 0usize;
    while i < bytes.len() {
        if bytes[i] == b'{' && i + 1 < bytes.len() && bytes[i + 1] == b'-' {
            depth += 1;
            i += 2;
        } else if bytes[i] == b'-' && i + 1 < bytes.len() && bytes[i + 1] == b'}' {
            depth -= 1;
            i += 2;
            if depth == 0 {
                break;
            }
        } else {
            i += 1;
        }
    }
    // unterminated: `i == len`, token still covers the rest (lossless).
    lex.bump(i);
}

fn lex_string(lex: &mut Lexer<Lx>) {
    let rem = lex.remainder();
    let mut escaped = false;
    let mut end: Option<usize> = None;
    let mut stop = rem.len();
    for (i, c) in rem.char_indices() {
        if escaped {
            escaped = false;
            continue;
        }
        match c {
            '\\' => escaped = true,
            '"' => {
                end = Some(i + 1);
                break;
            }
            '\n' => {
                stop = i;
                break;
            }
            _ => {}
        }
    }
    lex.bump(end.unwrap_or(stop));
}

fn lex_multiline(lex: &mut Lexer<Lx>) {
    // opening `"""` consumed; content is raw up to the next `"""`.
    let rem = lex.remainder();
    match rem.find("\"\"\"") {
        Some(pos) => lex.bump(pos + 3),
        None => lex.bump(rem.len()),
    }
}

fn lex_char(lex: &mut Lexer<Lx>) {
    let rem = lex.remainder();
    let mut escaped = false;
    let mut end: Option<usize> = None;
    let mut stop = rem.len();
    for (i, c) in rem.char_indices() {
        if escaped {
            escaped = false;
            continue;
        }
        match c {
            '\\' => escaped = true,
            '\'' => {
                end = Some(i + 1);
                break;
            }
            '\n' => {
                stop = i;
                break;
            }
            _ => {}
        }
    }
    lex.bump(end.unwrap_or(stop));
}

/// Lower-cased keyword set (doc 04 §3.4). `True`/`False` are upper — handled
/// separately.
fn lower_keyword(text: &str) -> Option<SyntaxKind> {
    Some(match text {
        "module" => SyntaxKind::ModuleKw,
        "exposing" => SyntaxKind::ExposingKw,
        "import" => SyntaxKind::ImportKw,
        "as" => SyntaxKind::AsKw,
        "type" => SyntaxKind::TypeKw,
        "alias" => SyntaxKind::AliasKw,
        "foreign" => SyntaxKind::ForeignKw,
        "if" => SyntaxKind::IfKw,
        "then" => SyntaxKind::ThenKw,
        "else" => SyntaxKind::ElseKw,
        "case" => SyntaxKind::CaseKw,
        "of" => SyntaxKind::OfKw,
        "let" => SyntaxKind::LetKw,
        "in" => SyntaxKind::InKw,
        _ => return None,
    })
}

fn classify_ident(text: &str) -> SyntaxKind {
    if let Some(kw) = lower_keyword(text) {
        return kw;
    }
    match text.chars().next() {
        Some(c) if c.is_uppercase() => match text {
            "True" => SyntaxKind::TrueKw,
            "False" => SyntaxKind::FalseKw,
            _ => SyntaxKind::UpperIdent,
        },
        _ => SyntaxKind::LowerIdent,
    }
}

fn classify_op(text: &str) -> SyntaxKind {
    match text {
        "=" => SyntaxKind::Eq,
        ":" => SyntaxKind::Colon,
        "::" => SyntaxKind::Colon2,
        "." => SyntaxKind::Dot,
        ".." => SyntaxKind::DotDot,
        "|" => SyntaxKind::Pipe,
        "->" => SyntaxKind::Arrow,
        "\\" => SyntaxKind::Backslash,
        _ => SyntaxKind::Op,
    }
}

fn map_kind(lx: Lx, text: &str) -> SyntaxKind {
    match lx {
        Lx::Ws => SyntaxKind::Whitespace,
        Lx::Nl => SyntaxKind::Newline,
        Lx::LineComment => SyntaxKind::LineComment,
        Lx::BlockComment => SyntaxKind::BlockComment,
        Lx::Hex => SyntaxKind::HexInt,
        Lx::Float => SyntaxKind::Float,
        Lx::Int => SyntaxKind::Int,
        Lx::Multiline => SyntaxKind::MultilineString,
        Lx::Str => SyntaxKind::String,
        Lx::Char => SyntaxKind::Char,
        Lx::Ident => classify_ident(text),
        Lx::Op => classify_op(text),
        Lx::LParen => SyntaxKind::LParen,
        Lx::RParen => SyntaxKind::RParen,
        Lx::LBrack => SyntaxKind::LBrack,
        Lx::RBrack => SyntaxKind::RBrack,
        Lx::LBrace => SyntaxKind::LBrace,
        Lx::RBrace => SyntaxKind::RBrace,
        Lx::Comma => SyntaxKind::Comma,
        Lx::Underscore => SyntaxKind::Underscore,
    }
}

/// Lex `src` into a lossless token vector: every byte lands in exactly one
/// token, in source order (L8, L4).
pub fn lex(src: &str) -> Vec<LexToken> {
    let mut out = Vec::new();
    let mut lx = Lx::lexer(src);
    while let Some(res) = lx.next() {
        let span = lx.span();
        let kind = match res {
            Ok(k) => map_kind(k, lx.slice()),
            Err(()) => SyntaxKind::Error,
        };
        out.push(LexToken {
            kind,
            start: span.start as u32,
            end: span.end as u32,
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn kinds(src: &str) -> Vec<SyntaxKind> {
        lex(src).into_iter().map(|t| t.kind).collect()
    }

    #[test]
    fn losslessness_bytes_covered() {
        let src = "module Main exposing (main)\n\nmain =\n    println \"hi\"\n";
        let toks = lex(src);
        let mut reconstructed = String::new();
        for t in &toks {
            reconstructed.push_str(&src[t.start as usize..t.end as usize]);
        }
        assert_eq!(reconstructed, src);
    }

    #[test]
    fn keywords_and_idents() {
        use SyntaxKind::*;
        assert_eq!(kinds("module Main"), vec![ModuleKw, Whitespace, UpperIdent]);
        assert_eq!(kinds("let in case of"), {
            use SyntaxKind::*;
            vec![
                LetKw, Whitespace, InKw, Whitespace, CaseKw, Whitespace, OfKw,
            ]
        });
    }

    #[test]
    fn operators_maximal_munch() {
        use SyntaxKind::*;
        assert_eq!(kinds("a<|b"), vec![LowerIdent, Op, LowerIdent]);
        assert_eq!(
            kinds("x :: xs"),
            vec![LowerIdent, Whitespace, Colon2, Whitespace, LowerIdent]
        );
        assert_eq!(
            kinds("a -> b"),
            vec![LowerIdent, Whitespace, Arrow, Whitespace, LowerIdent]
        );
        assert_eq!(kinds(".."), vec![DotDot]);
        assert_eq!(kinds("xs.field"), vec![LowerIdent, Dot, LowerIdent]);
    }

    #[test]
    fn line_comment_beats_operator() {
        use SyntaxKind::*;
        assert_eq!(kinds("Int-- c"), vec![UpperIdent, LineComment]);
        assert_eq!(kinds("x -- c"), vec![LowerIdent, Whitespace, LineComment]);
    }

    #[test]
    fn numbers() {
        use SyntaxKind::*;
        assert_eq!(kinds("123"), vec![Int]);
        assert_eq!(kinds("0xFF"), vec![HexInt]);
        assert_eq!(kinds("1.5"), vec![Float]);
        assert_eq!(kinds("1e6"), vec![Float]);
        assert_eq!(kinds("1.5e-2"), vec![Float]);
    }

    #[test]
    fn nested_block_comment() {
        use SyntaxKind::*;
        assert_eq!(kinds("{- a {- b -} c -}x"), vec![BlockComment, LowerIdent]);
    }

    #[test]
    fn strings_and_multiline() {
        use SyntaxKind::*;
        assert_eq!(kinds(r#""he\"llo""#), vec![String]);
        assert_eq!(kinds("\"\"\"a\nb\"\"\""), vec![MultilineString]);
        assert_eq!(kinds("'c'"), vec![Char]);
        assert_eq!(kinds(r"'\n'"), vec![Char]);
    }

    #[test]
    fn underscore_vs_ident() {
        use SyntaxKind::*;
        assert_eq!(kinds("_"), vec![Underscore]);
        assert_eq!(kinds("_foo"), vec![LowerIdent]);
    }
}
