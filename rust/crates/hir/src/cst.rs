//! CST navigation helpers. The `syntax` typed AST view exposes the enum layers
//! (`Expr`/`Pattern`/`Type`/`Decl`) but not every sub-field; this module reads
//! the remaining leaves/children directly off the lossless tree. All navigation
//! that `hir` needs beyond the published accessors lives here, so the coupling
//! to CST shape is contained to one file.

use syntax::ast::{self, AstNode};
use syntax::{SyntaxKind, SyntaxNode, SyntaxToken};

/// Significant (non-trivia) tokens directly under `n`.
fn sig_tokens(n: &SyntaxNode) -> impl Iterator<Item = SyntaxToken> + '_ {
    n.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| !t.kind().is_trivia())
}

/// First direct token of `kind` under `n`.
fn first_token(n: &SyntaxNode, kind: SyntaxKind) -> Option<String> {
    sig_tokens(n)
        .find(|t| t.kind() == kind)
        .map(|t| t.text().to_string())
}

/// Direct child expression nodes, in order.
pub fn child_exprs(n: &SyntaxNode) -> Vec<ast::Expr> {
    n.children().filter_map(ast::Expr::cast).collect()
}

/// Direct child pattern nodes, in order.
pub fn child_pats(n: &SyntaxNode) -> Vec<ast::Pattern> {
    n.children().filter_map(ast::Pattern::cast).collect()
}

/// Direct child type nodes, in order.
pub fn child_types(n: &SyntaxNode) -> Vec<ast::Type> {
    n.children().filter_map(ast::Type::cast).collect()
}

/// Split a dotted qualified name node (`QualRefExpr` / `TypeQual` /
/// `PatCtorQual`) into `(qualifier, final-name)`. Joins the significant
/// ident tokens with `.` and splits at the last dot (doc 03 §1.3).
pub fn dotted_parts(n: &SyntaxNode) -> (String, String) {
    let idents: Vec<String> = sig_tokens(n)
        .filter(|t| matches!(t.kind(), SyntaxKind::UpperIdent | SyntaxKind::LowerIdent))
        .map(|t| t.text().to_string())
        .collect();
    match idents.split_last() {
        Some((last, rest)) if !rest.is_empty() => (rest.join("."), last.clone()),
        Some((last, _)) => (String::new(), last.clone()),
        None => (String::new(), String::new()),
    }
}

/// The first lowercase ident token directly under `n`.
pub fn first_lower(n: &SyntaxNode) -> Option<String> {
    first_token(n, SyntaxKind::LowerIdent)
}

/// The first LowerIdent TOKEN directly under `n`. Its range (NOT the enclosing
/// node's, which includes leading whitespace trivia) is the binder span LSP
/// rename/goto/references edit — using the node range corrupts source on rename
/// (`pick maybeVal` → `pickmv`).
pub fn first_lower_tok(n: &SyntaxNode) -> Option<SyntaxToken> {
    sig_tokens(n).find(|t| t.kind() == SyntaxKind::LowerIdent)
}

/// True when the first boolean keyword token under `n` is `True` (a `PatBool`).
pub fn first_token_is_true(n: &SyntaxNode) -> bool {
    sig_tokens(n)
        .find(|t| matches!(t.kind(), SyntaxKind::TrueKw | SyntaxKind::FalseKw))
        .map(|t| t.kind() == SyntaxKind::TrueKw)
        .unwrap_or(false)
}

/// The first uppercase ident token directly under `n`.
pub fn first_upper(n: &SyntaxNode) -> Option<String> {
    first_token(n, SyntaxKind::UpperIdent)
}

/// The first UpperIdent TOKEN directly under `n` (its range powers LSP ref
/// recording on constructor patterns — hover/goto/rename/semantic-tokens).
pub fn first_upper_tok(n: &SyntaxNode) -> Option<SyntaxToken> {
    sig_tokens(n).find(|t| t.kind() == SyntaxKind::UpperIdent)
}

/// The LAST UpperIdent token directly under `n` — for a QUALIFIED constructor
/// pattern (`Db.SetField`) the ctor name is the last upper segment (the earlier
/// ones are the module qualifier).
pub fn last_upper_tok(n: &SyntaxNode) -> Option<SyntaxToken> {
    sig_tokens(n)
        .filter(|t| t.kind() == SyntaxKind::UpperIdent)
        .last()
}

/// All lowercase ident tokens directly under `n` (record pattern fields, type
/// var lists).
pub fn lower_idents(n: &SyntaxNode) -> Vec<String> {
    sig_tokens(n)
        .filter(|t| t.kind() == SyntaxKind::LowerIdent)
        .map(|t| t.text().to_string())
        .collect()
}

/// All uppercase ident tokens directly under `n`.
pub fn upper_idents(n: &SyntaxNode) -> Vec<String> {
    sig_tokens(n)
        .filter(|t| t.kind() == SyntaxKind::UpperIdent)
        .map(|t| t.text().to_string())
        .collect()
}

/// Type-var names of an alias / union decl (`TypeVarList` child).
pub fn decl_type_vars(n: &SyntaxNode) -> Vec<String> {
    n.children()
        .find(|c| c.kind() == SyntaxKind::TypeVarList)
        .map(|tvl| lower_idents(&tvl))
        .unwrap_or_default()
}

// ---- exposing clauses ----------------------------------------------------

/// How a type's constructors are exposed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CtorExposure {
    /// `Type` — none.
    None,
    /// `Type(..)` — all.
    All,
    /// `Type(A, B)` — the listed ones.
    Some(Vec<String>),
}

/// One item in an `exposing (…)` list.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ExposedItem {
    Value(String),
    Type { name: String, ctors: CtorExposure },
    Operator,
}

/// A parsed `exposing (…)` clause.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct ExposingClause {
    /// `exposing (..)`.
    pub all: bool,
    pub items: Vec<ExposedItem>,
}

/// Read an `ExposingList` / `ImportExposing` node into a structured clause.
pub fn read_exposing(n: &SyntaxNode) -> ExposingClause {
    // expose-all: a direct `..` token (a nested `Type(..)` ctor list keeps its
    // `..` inside an ExposedCtorList child, never a direct token here).
    let all = n
        .children_with_tokens()
        .filter_map(|e| e.into_token())
        .any(|t| t.kind() == SyntaxKind::DotDot);
    let mut items = Vec::new();
    for c in n.children() {
        match c.kind() {
            SyntaxKind::ExposedValue => {
                if let Some(v) = first_lower(&c) {
                    items.push(ExposedItem::Value(v));
                }
            }
            SyntaxKind::ExposedType => {
                if let Some(name) = first_upper(&c) {
                    let ctors = match c
                        .children()
                        .find(|k| k.kind() == SyntaxKind::ExposedCtorList)
                    {
                        None => CtorExposure::None,
                        Some(cl) => {
                            let dotdot = cl
                                .children_with_tokens()
                                .filter_map(|e| e.into_token())
                                .any(|t| t.kind() == SyntaxKind::DotDot);
                            if dotdot {
                                CtorExposure::All
                            } else {
                                CtorExposure::Some(upper_idents(&cl))
                            }
                        }
                    };
                    items.push(ExposedItem::Type { name, ctors });
                }
            }
            SyntaxKind::ExposedOperator => items.push(ExposedItem::Operator),
            _ => {}
        }
    }
    ExposingClause { all, items }
}

/// The `ExposingList` node under a module header, if present.
pub fn header_exposing(header: &ast::ModuleHeader) -> Option<SyntaxNode> {
    header
        .syntax()
        .children()
        .find(|c| c.kind() == SyntaxKind::ExposingList)
}
