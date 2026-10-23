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
    explain_errors(src, &toks, file, &mut errors, &done.sites, &done.units);
    let (green, depth) = event::build_tree(src, &raw, done.events);
    Parse {
        green,
        errors,
        depth,
    }
}

/// Parse `toks` as ONE unit, with no split at column 1.
fn run_grammar_one_unit(src: &str, toks: Vec<PToken>, file: FileId) -> parser::Finished {
    let mut p = parser::Parser::new(src, toks, file);
    grammar::source_file(&mut p);
    p.finish()
}

/// The message for a line at column 1 that the definition above needs.
const INDENT_MESSAGE: &str = "This line starts at column 1, so it begins a new declaration. \
     Indent it to continue the definition above.";

/// Make each unit's one syntax error say what caused it, when a trial proves
/// the cause. First, a line at column 1 that starts with a lower-case name
/// always begins a new unit (`parser::lexical_units`). When the unit before
/// it has an error, and the two units parse with no error as ONE unit, the
/// line was meant to continue the definition: the first unit's error says
/// to indent it, and the second unit's error is dropped. Otherwise a
/// misused reserved word is named ([`name_reserved_words`]).
fn explain_errors(
    src: &str,
    toks: &[PToken],
    file: FileId,
    errors: &mut Vec<Diagnostic>,
    sites: &[parser::ErrorSite],
    units: &[usize],
) {
    let mut dropped = vec![false; errors.len()];
    let mut joined_units = Vec::new();
    for site in sites {
        if dropped[site.diag] || joined_units.contains(&site.unit) {
            continue;
        }
        let Some(&next) = units.get(site.unit + 1) else {
            continue;
        };
        if toks[next].kind != SyntaxKind::LowerIdent {
            continue;
        }
        let start = units[site.unit];
        let end = units.get(site.unit + 2).copied().unwrap_or(toks.len());
        if !run_grammar_one_unit(src, toks[start..end].to_vec(), file)
            .sites
            .is_empty()
        {
            continue;
        }
        let t = toks[next];
        let span = base::Span::new(file, t.start, t.end);
        errors[site.diag] =
            Diagnostic::error("E0001", INDENT_MESSAGE).with_label(span, INDENT_MESSAGE);
        joined_units.push(site.unit + 1);
        for later in sites.iter().filter(|s| s.unit == site.unit + 1) {
            dropped[later.diag] = true;
        }
    }
    let named: Vec<parser::ErrorSite> = sites
        .iter()
        .filter(|s| !dropped[s.diag] && errors[s.diag].message != INDENT_MESSAGE)
        .copied()
        .collect();
    name_reserved_words(src, toks, file, errors, &named, units);
    let mut i = 0;
    errors.retain(|_| {
        let keep = !dropped.get(i).copied().unwrap_or(false);
        i += 1;
        keep
    });
}

fn run_grammar(src: &str, toks: Vec<PToken>, file: FileId) -> parser::Finished {
    let mut p = parser::Parser::new_file(src, toks, file);
    grammar::source_file(&mut p);
    p.finish()
}

/// The most further uses of the same word one trial follows.
const MAX_FOLLOW: usize = 4;

/// The most renames the chain search from one start word tries.
const CHAIN_BUDGET: usize = 256;

/// Name the reserved word behind each unit's one syntax error, when a trial
/// proves it.
///
/// The grammar reports only the first error in each lexical top-level unit
/// (the diagnostic budget, `parser::lexical_units`). That error is raised
/// where the parse first fails, which may be the misused word itself
/// (`g then`), the token after a word that opened a construct (`x ++ if`
/// fails at the end of the unit), or a valid keyword after the misused one
/// (`if if then` fails at `then`). So the misused word is found by trial,
/// not by position.
///
/// A trial reads one reserved word of the unit as a fresh name (a value
/// name, or a capitalised name where only a type or module name fits) and
/// parses the unit again. When the renamed word is used again later in the
/// unit (a binding and its use), the trial follows: a new error at another
/// use of the same word renames that use too.
///
/// A word W is eligible only when the plain parse's first error is at W or
/// at the token directly after W (the construct W starts could not begin).
/// When the parse accepted W and read more of its construct before failing
/// (`if y` with no `then`), W is in its valid place and is never named. An
/// eligible word W is named when:
///
/// - its trial parses the WHOLE unit with no error, or
/// - (the chain rule, for two or more misused words in one unit) its trial
///   moves the first error strictly past W, and a later reserved word W2 is
///   eligible on that parse (the error is at W2, or at the token after W2
///   when that token is not a reserved word, or W2 is before an enclosing
///   block item), and W2 qualifies by the same rule with W still renamed.
///   A chain may start at any eligible word, before or after the plain
///   parse's first error (`1 -> if` and then `2 -> then` as two `case` arms
///   names `if`). A word whose only eligibility is a reserved word after it
///   (`if then 1 else 2`) starts no chain: its construct began validly, so
///   it is named only when renaming it alone parses clean. A keyword in its
///   valid place is never eligible, so no chain passes through it. The
///   chain is at most as long as the number of reserved words in the unit.
///
/// The earliest such word in the unit is named. When no word qualifies,
/// the parser's own diagnostic stays as it is, so a valid keyword
/// (`exposing` in `module Main exposing main`) is never named as misused.
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
        let start = units[site.unit];
        let end = units.get(site.unit + 1).copied().unwrap_or(toks.len());
        let unit = &toks[start..end];
        if let Some(i) = misused_word(src, unit, file) {
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

/// The token directly after `unit[c]` starts a later line at a column less
/// than or equal to the indentation (first-token column) of `unit[c]`'s own
/// line. That token belongs to an enclosing block (a new arm, binding or
/// declaration), so the construct the word at `c` starts could not begin.
/// A validly started construct is not caught: in `if y` the next token is on
/// the same line, and after `let` the binding is indented further.
fn before_enclosing_item(unit: &[PToken], c: usize) -> bool {
    let Some(next) = unit.get(c + 1) else {
        return false;
    };
    let mut first = c;
    while first > 0 && !unit[first].newline_before {
        first -= 1;
    }
    next.newline_before && next.col <= unit[first].col
}

/// A token of a trial read as a fresh name: its index in the unit and the
/// name kind it is read as.
type Rename = (usize, SyntaxKind);

/// A trial: the renames on top of a base, and the first error left.
type RenameFn<'a> = dyn Fn(&[Rename], usize, SyntaxKind) -> (Vec<Rename>, Option<usize>) + 'a;

/// The next links of a chain after a word, given the renames and the error.
type NextWordsFn<'a> = dyn Fn(&[Rename], usize, usize) -> Vec<usize> + 'a;

/// The index in `unit` of the earliest reserved word that qualifies (see
/// [`name_reserved_words`]), or `None`.
fn misused_word(src: &str, unit: &[PToken], file: FileId) -> Option<usize> {
    let text = |t: &PToken| &src[t.start as usize..t.end as usize];
    let reserved = |i: usize| {
        unit.get(i)
            .is_some_and(|t| parser::is_reserved_word(t.kind))
    };
    // The first error of `unit` with the tokens in `renamed` read as names,
    // or `None` when the unit parses clean.
    let first_error = |renamed: &[Rename]| {
        let mut trial = unit.to_vec();
        for &(i, name) in renamed {
            trial[i].kind = name;
        }
        let done = run_grammar(src, trial, file);
        done.sites.iter().map(|s| s.at).min()
    };
    // Read the word at `c` (and its later uses) as a fresh name of kind
    // `name`, on top of the renames in `base`. Returns all the renames and
    // the first error that is left.
    let rename = |base: &[Rename], c: usize, name: SyntaxKind| {
        let mut renamed = base.to_vec();
        renamed.push((c, name));
        let mut reached = first_error(&renamed);
        for _ in 0..MAX_FOLLOW {
            let Some(at) = reached else { break };
            // A later use of the same word stands at (or just before) the new
            // error: it is the same name, so read it as a name too.
            let again = [at, at.wrapping_sub(1)].into_iter().find(|&i| {
                reserved(i)
                    && !renamed.iter().any(|&(r, _)| r == i)
                    && text(&unit[i]) == text(&unit[c])
            });
            match again {
                Some(i) => {
                    renamed.push((i, name));
                    reached = first_error(&renamed);
                }
                None => break,
            }
        }
        (renamed, reached)
    };
    // A unit that parses clean on its own errs only in its context (an
    // import after a declaration): no rename is the cause.
    let plain = first_error(&[])?;
    let chain_limit = (0..unit.len()).filter(|&i| reserved(i)).count();
    // A fresh name is a value name or, where only a type or module name fits
    // (`type if = A`, `import Std.Log as if`), a capitalised one.
    const NAMES: [SyntaxKind; 2] = [SyntaxKind::LowerIdent, SyntaxKind::UpperIdent];
    // A word may start or continue a chain when the parse (with the earlier
    // words of the chain renamed) fails at the word, or at the token
    // directly after it when that token is not itself a reserved word, or
    // when the next token belongs to an enclosing block (see
    // [`before_enclosing_item`]). After a word, a separator (`,`), a new arm
    // or a new binding shows the word's construct could not begin. A reserved
    // word after it does not: in `if then 1 else 2` the `if` is started
    // validly and only its condition is missing, so renaming `if`, `then` and
    // `else` into an application proves nothing.
    let chains_at = |i: usize, err: usize| {
        i == err || (i + 1 == err && !reserved(err)) || before_enclosing_item(unit, i)
    };
    // Renames left for the chain search from one start word. Each rename
    // costs at most `MAX_FOLLOW + 1` grammar runs. The bound keeps a unit
    // with many reserved words from a long search, and the result stays
    // deterministic.
    let budget = std::cell::Cell::new(CHAIN_BUDGET);
    // The chain from word `w` with the renames in `base` already made: true
    // when some sequence of eligible words, starting at `w`, makes the unit
    // parse clean within `depth` more links.
    fn chain(
        rename: &RenameFn,
        next_words: &NextWordsFn,
        budget: &std::cell::Cell<usize>,
        base: &[Rename],
        w: usize,
        depth: usize,
    ) -> bool {
        for name in NAMES {
            if budget.get() == 0 {
                return false;
            }
            budget.set(budget.get() - 1);
            let (renamed, reached) = rename(base, w, name);
            let Some(at) = reached else { return true };
            // The rename must move the first error strictly past `w`.
            if depth == 0 || at <= w {
                continue;
            }
            for w2 in next_words(&renamed, w, at) {
                if chain(rename, next_words, budget, &renamed, w2, depth - 1) {
                    return true;
                }
            }
        }
        false
    }
    // The next links after `w`, when the first error is at `at`: reserved
    // words after `w`, at or before the error, not yet renamed, that may
    // continue a chain on this parse. The word at the error comes first, then
    // the word before it, then the others in order.
    let next_words = |renamed: &[Rename], w: usize, at: usize| {
        let mut out: Vec<usize> = (w + 1..=at.min(unit.len().saturating_sub(1)))
            .filter(|&i| reserved(i) && !renamed.iter().any(|&(r, _)| r == i) && chains_at(i, at))
            .collect();
        out.sort_by_key(|&i| (i != at, i + 1 != at, i));
        out
    };
    let qualifies = |c: usize| {
        // Eligibility: the plain parse fails AT the word, or at the token
        // directly after it (the construct the word starts could not begin).
        // When the parse accepted the word and read more of its construct
        // before it failed (`if y` cut off before `then`), the word is in
        // its valid place: renaming it would turn the unit into a function
        // call, which is no proof that the word is misused. A word is also
        // eligible when the next token starts a later line at or left of the
        // word's line indentation (see [`before_enclosing_item`]): a bare
        // `if` as a case arm body, before the next arm.
        if c != plain && c + 1 != plain && !before_enclosing_item(unit, c) {
            return false;
        }
        // An eligible word that cannot start a chain (`if then 1 else 2`)
        // is named only when renaming it alone parses clean.
        let depth = if chains_at(c, plain) { chain_limit } else { 0 };
        budget.set(CHAIN_BUDGET);
        chain(&rename, &next_words, &budget, &[], c, depth)
    };
    (0..unit.len())
        .filter(|&i| reserved(i))
        .find(|&c| qualifies(c))
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
