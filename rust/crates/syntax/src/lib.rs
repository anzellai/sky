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
mod layout;
mod lexer;
mod parser;

pub mod ast;

pub use kind::{SkyLang, SyntaxElement, SyntaxKind, SyntaxNode, SyntaxToken};
pub use layout::{layout, PToken};
pub use lexer::{lex, LexToken};

use base::FileId;
use diagnostics::Diagnostic;
use rowan::GreenNode;

/// The result of parsing a file: a lossless green tree + the diagnostics
/// collected along the way (doc 04 §5.3). Cheap to clone (`GreenNode` is Arc'd).
#[derive(Clone)]
pub struct Parse {
    green: GreenNode,
    errors: Vec<Diagnostic>,
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
    let mut p = parser::Parser::new(src, toks, file);
    grammar::source_file(&mut p);
    let (events, errors) = p.finish();
    let green = event::build_tree(src, &raw, events);
    Parse { green, errors }
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
