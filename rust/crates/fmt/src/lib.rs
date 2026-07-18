#![forbid(unsafe_code)]
//! `fmt` — `sky fmt`, the formatter over the lossless CST (doc 02, doc 04,
//! doc 10). Because it works on the lossless rowan tree (L8), formatting is
//! exact and two passes are byte-identical.
//!
//! Scope (bring-up): `format_source` is the **lossless CST reprint** — it parses
//! the source into the rowan tree (trivia + error nodes and all) and re-emits it
//! verbatim via [`syntax::Parse::reprint`]. This is the exact mechanism the M1
//! round-trip gate proves byte-exact over the whole corpus (156/156), so it is
//! *guaranteed* trivia-preserving (trailing comments included — closing the
//! `Format.hs:18` data-loss hole) and *guaranteed* idempotent:
//! `format_source(format_source(x)) == format_source(x)`.
//!
//! It is intentionally NOT yet the opinionated Wadler-Lindig re-layout (doc 04
//! §"sky fmt"). Normalising spacing/width is a strict superset of a correct
//! lossless reprint and lands on this same entry point without changing the
//! CLI/LSP contract — both call `format_source`, the CLI over the file bytes,
//! the LSP over the open-doc input.

use base::FileId;
use syntax::parse;

/// Format Sky source. Currently a lossless, trivia-preserving, idempotent CST
/// reprint (see the module doc for scope). Formatting a syntactically broken
/// file still works (L8): the CST carries error nodes and they re-emit verbatim
/// rather than throwing.
pub fn format_source(src: &str) -> String {
    parse(src, FileId(0)).reprint()
}

/// `sky fmt --check`: is `src` already formatted? True when a format pass is a
/// no-op (byte-identical). The CLI turns `false` into a non-zero exit.
pub fn is_formatted(src: &str) -> bool {
    format_source(src) == src
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn format_is_idempotent() {
        for src in [
            "module Main",
            "module Main exposing (main)\n\nmain =\n    println \"hi\"\n",
            "x = 1  -- trailing comment survives\n",
            "",
        ] {
            let once = format_source(src);
            assert_eq!(format_source(&once), once, "idempotent for {src:?}");
        }
    }

    #[test]
    fn reprint_is_lossless_for_wellformed_source() {
        let src = "module Main exposing (main)\n\nmain =\n    println \"hi\"\n";
        assert_eq!(format_source(src), src);
        assert!(is_formatted(src));
    }
}
