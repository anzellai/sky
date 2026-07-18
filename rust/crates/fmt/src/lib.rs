//! `fmt` — `sky fmt`, the exact formatter over the CST: idempotent,
//! trivia-preserving (doc 02, doc 04, doc 10). Because it works on the lossless
//! rowan tree (L8), formatting is exact and two passes are byte-identical.
//!
//! M0 stub: the entry point exists over the `syntax` lexer; the CST-walking
//! formatter lands with the parser in M1.

use syntax::lex;

/// M0 placeholder: proves the `fmt` → `syntax` dependency edge and the lexer
/// hand-off. Currently a no-op reprint of the token text (identity), which the
/// idempotence property trivially satisfies. M1+ replaces it with a real
/// CST-driven formatter over the lossless tree.
pub fn format_source(src: &str) -> String {
    lex(src)
        .into_iter()
        .map(|t| &src[t.start as usize..t.end as usize])
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn format_is_idempotent() {
        let src = "module Main";
        let once = format_source(src);
        assert_eq!(format_source(&once), once);
    }
}
