#![forbid(unsafe_code)]
//! `syntax` — lexer (`logos`) + layout pass + lossless CST (`rowan`) + typed AST
//! view, with error recovery and byte-exact round-trip (doc 04). Law L8: parse
//! always produces a tree; the LSP works on broken code. Law L7: errors are
//! `Diagnostic` values, never exceptions. Law L4: `lex`/`layout`/`parse` are
//! pure functions of the source bytes.

mod event;
mod grammar;
mod interp;
mod kind;
pub mod kind_class;
mod layout;
mod lexer;
mod parser;

pub mod ast;

pub use kind::{SkyLang, SyntaxElement, SyntaxKind, SyntaxNode, SyntaxToken};
pub use layout::{layout, PToken};
pub use lexer::{lex, LexToken, RESERVED_WORDS};
/// Byte-range primitives from rowan — re-exported so downstream crates (the
/// resolver's LSP occurrence recording) can name a token/node range without a
/// direct rowan dependency.
pub use rowan::{TextRange, TextSize};

use base::FileId;
use diagnostics::Diagnostic;
use rowan::GreenNode;

/// The result of parsing a file: a lossless green tree + the diagnostics
/// collected along the way (doc 04 §5.3). Cheap to clone (`GreenNode` is Arc'd).
#[derive(Clone)]
pub struct Parse {
    green: GreenNode,
    errors: Vec<Diagnostic>,
    /// The tree's depth in nodes (see the `Drop` impl).
    depth: u32,
}

/// A tree deeper than this is dropped by [`drop_green_flat`].
const FLAT_DROP_DEPTH: u32 = 512;

// rowan frees a green node's children from inside the parent's drop, so
// dropping a tree takes a few native frames per level. A left-nested `|>`
// pipeline or a generated record codec nests one node per step, and dropping
// the parse of a 20,000-step pipeline overflowed a 2 MiB thread. A deep tree is
// dropped without that recursion; an ordinary one by rowan as before.
impl Drop for Parse {
    fn drop(&mut self) {
        if self.depth > FLAT_DROP_DEPTH {
            use rowan::Language;
            let empty = GreenNode::new(SkyLang::kind_to_raw(SyntaxKind::SourceFile), []);
            drop_green_flat(std::mem::replace(&mut self.green, empty));
        }
    }
}

/// Drop a green tree with no recursion. Every node is first held in a list,
/// parents before children; the list is then dropped in that order, so each
/// node that is freed finds its children still held and frees nothing below
/// itself. A node shared with a tree that is still alive is only released.
pub(crate) fn drop_green_flat(root: GreenNode) {
    let mut held: Vec<GreenNode> = vec![root];
    let mut i = 0;
    while i < held.len() {
        let children: Vec<GreenNode> = held[i]
            .children()
            .filter_map(|c| c.into_node())
            .map(|n| n.to_owned())
            .collect();
        held.extend(children);
        i += 1;
    }
    // `Vec` drops its elements front to back: parents first.
    drop(held);
}

impl Parse {
    /// The untyped root node.
    pub fn syntax(&self) -> SyntaxNode {
        SyntaxNode::new_root(self.green.clone())
    }

    /// The typed root view.
    pub fn tree(&self) -> ast::SourceFile {
        use ast::AstNode;
        ast::SourceFile::cast(self.syntax()).expect("root is always SOURCE_FILE")
    }

    /// The interned green tree.
    pub fn green(&self) -> &GreenNode {
        &self.green
    }

    /// Parse diagnostics (L7 — values, not exceptions).
    pub fn errors(&self) -> &[Diagnostic] {
        &self.errors
    }

    /// Reconstruct the source by concatenating every leaf's text in order — the
    /// operational definition of L8 losslessness (doc 04 §14).
    pub fn reprint(&self) -> String {
        self.syntax().text().to_string()
    }

    /// Count `ERROR` nodes — the parser structured every construct iff this is
    /// zero (the M1 gate, doc 04 §11).
    pub fn error_node_count(&self) -> usize {
        self.syntax()
            .descendants()
            .filter(|n| n.kind() == SyntaxKind::Error)
            .count()
    }
}

/// Parse `src` into a lossless CST. Never panics; broken input yields `ERROR`
/// nodes + diagnostics (L7, L8).
pub fn parse(src: &str, file: FileId) -> Parse {
    let raw = lexer::lex(src);
    let toks = layout::layout(src, &raw);
    let done = run_grammar(src, toks.clone(), file);
    let mut errors = done.diags;
    name_reserved_words(src, &toks, file, &mut errors, &done.sites, &done.units);
    let (green, depth) = event::build_tree(src, &raw, done.events);
    Parse {
        green,
        errors,
        depth,
    }
}

fn run_grammar(src: &str, toks: Vec<PToken>, file: FileId) -> parser::Finished {
    let mut p = parser::Parser::new(src, toks, file);
    grammar::source_file(&mut p);
    p.finish()
}

/// The most reserved words [`name_reserved_words`] tries for one error.
const MAX_CANDIDATES: usize = 8;

/// The most further uses of the same word one trial follows.
const MAX_FOLLOW: usize = 4;

/// Name the reserved word behind each unit's one syntax error.
///
/// The grammar reports only the first error in a top-level unit (the
/// diagnostic budget, `Parser::begin_unit`). That error is raised where the
/// parse first fails, which is the misused word itself (`g then`), the token
/// after a word that opened a construct (`x ++ if` fails at the end of
/// input), or a valid keyword after the misused one (`if if then` fails at
/// `then`). So the misused word is found by trial, not by position.
///
/// Each reserved word of the unit near the error (the error token, the token
/// after it, then backwards) is read as a plain name, and the unit is parsed
/// again. When the renamed word is used again later in the unit (a binding
/// and its use), the trial follows: a new error at another use of the same
/// word renames that use too. The word whose trial gets the parse furthest
/// (no error at all is furthest) is the misused one. A trial must get past
/// the original error to count. When none does, a reserved word standing at
/// the error is named. Either way the one diagnostic of the unit becomes the
/// named `[E0001]`. A unit whose error involves no reserved word keeps its
/// message.
///
/// Only a unit that already has an error is tried, and only that unit's
/// tokens are parsed again, so valid code costs nothing.
fn name_reserved_words(
    src: &str,
    toks: &[PToken],
    file: FileId,
    errors: &mut [Diagnostic],
    sites: &[parser::ErrorSite],
    units: &[usize],
) {
    for site in sites {
        let end = units
            .iter()
            .copied()
            .find(|&u| u > site.unit)
            .unwrap_or(toks.len());
        let unit = &toks[site.unit..end];
        let at = site.at - site.unit; // may be `unit.len()` (end of the unit)
        if let Some(i) = misused_word(src, unit, file, at) {
            let t = unit[i];
            let word = &src[t.start as usize..t.end as usize];
            let msg = format!(
                "`{word}` is a reserved word and cannot be used as a name. Pick another name. \
                 The reserved words are: {}.",
                lexer::RESERVED_WORDS.join(", ")
            );
            let span = base::Span::new(file, t.start, t.end);
            errors[site.diag] = Diagnostic::error("E0001", msg.clone()).with_label(span, msg);
        }
    }
}

/// The index in `unit` of the reserved word misused as a name, for a unit
/// whose first error is at `at` (see [`name_reserved_words`]).
fn misused_word(src: &str, unit: &[PToken], file: FileId, at: usize) -> Option<usize> {
    let text = |t: &PToken| &src[t.start as usize..t.end as usize];
    let reserved = |i: usize| {
        unit.get(i)
            .is_some_and(|t| parser::is_reserved_word(t.kind))
    };
    let mut order = vec![at, at + 1];
    order.extend((0..at.min(unit.len())).rev());
    let candidates = order
        .into_iter()
        .filter(|&i| reserved(i))
        .take(MAX_CANDIDATES);
    // The first error of `unit` with the tokens in `renamed` read as names,
    // or `usize::MAX` when there is none.
    let first_error = |renamed: &[usize]| {
        let mut trial = unit.to_vec();
        for &i in renamed {
            trial[i].kind = SyntaxKind::LowerIdent;
        }
        let done = run_grammar(src, trial, file);
        done.sites.iter().map(|s| s.at).min().unwrap_or(usize::MAX)
    };
    let mut best: Option<(usize, usize)> = None; // (candidate, how far)
    for c in candidates {
        let mut renamed = vec![c];
        let mut reached = first_error(&renamed);
        for _ in 0..MAX_FOLLOW {
            // A later use of the same word stands at (or just before) the new
            // error: it is the same name, so read it as a name too.
            let again = [reached, reached.wrapping_sub(1)].into_iter().find(|&i| {
                reserved(i) && !renamed.contains(&i) && text(&unit[i]) == text(&unit[c])
            });
            match again {
                Some(i) => {
                    renamed.push(i);
                    reached = first_error(&renamed);
                }
                None => break,
            }
        }
        if reached > at && best.is_none_or(|(_, far)| reached > far) {
            best = Some((c, reached));
        }
        if reached == usize::MAX {
            break;
        }
    }
    best.map(|(c, _)| c).or_else(|| reserved(at).then_some(at))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse_str(src: &str) -> Parse {
        parse(src, FileId(0))
    }

    #[test]
    fn roundtrip_and_no_errors_hello() {
        let src = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"Hello from Sky!\"\n";
        let p = parse_str(src);
        assert_eq!(p.reprint(), src);
        assert_eq!(p.error_node_count(), 0);
    }

    #[test]
    fn roundtrip_let_case_lambda_pipeline() {
        let src = "\
f x =
    let
        a = 10
        b = 20
    in
    case x of
        Just v ->
            v
                |> add a
                |> add b

        Nothing ->
            0
";
        let p = parse_str(src);
        assert_eq!(p.reprint(), src, "byte-exact round-trip");
        assert_eq!(p.error_node_count(), 0, "no error nodes");
    }

    #[test]
    fn roundtrip_types_and_records() {
        let src = "\
type alias Model = { count : Int, name : String }


type Msg
    = Increment
    | Decrement
    | SetName String


update : Msg -> Model -> Model
update msg model =
    case msg of
        Increment ->
            { model | count = model.count + 1 }

        Decrement ->
            { model | count = model.count - 1 }

        SetName n ->
            { model | name = n }
";
        let p = parse_str(src);
        assert_eq!(p.reprint(), src);
        assert_eq!(p.error_node_count(), 0);
    }

    #[test]
    fn recovers_and_keeps_bytes_on_broken_input() {
        let src = "main =\n    @@@ let\n";
        let p = parse_str(src);
        assert_eq!(p.reprint(), src);
        assert!(!p.errors().is_empty() || p.error_node_count() > 0);
    }

    #[test]
    fn negative_literal_argument() {
        let src = "x =\n    atan2 0 -1\n";
        let p = parse_str(src);
        assert_eq!(p.reprint(), src);
        assert_eq!(p.error_node_count(), 0);
        let has_negate = p
            .syntax()
            .descendants()
            .any(|n| n.kind() == SyntaxKind::NegateExpr);
        assert!(has_negate, "expected NEGATE_EXPR for `-1` arg");
    }
}
