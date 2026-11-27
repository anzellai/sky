//! Names a piece of Sky syntax binds, read from the CST (never from a text
//! split at `=`). Shared by the Sky.Spa split and the `Std.App` entry
//! synthesis, which place user code in a new scope and must know which names
//! it brings (docs/design/let-bind-lines.md §6).

use std::collections::HashSet;
use syntax::ast::{self, AstNode};
use syntax::{SyntaxKind, SyntaxNode, TextRange};

/// Is `k` a node of a type (an annotation)? Its lower-case words are type
/// variables, not values.
fn is_type_kind(k: SyntaxKind) -> bool {
    matches!(
        k,
        SyntaxKind::TypeFun
            | SyntaxKind::TypeApp
            | SyntaxKind::TypeVar
            | SyntaxKind::TypeCon
            | SyntaxKind::TypeQual
            | SyntaxKind::TypeRecord
            | SyntaxKind::TypeRecordField
            | SyntaxKind::TypeTuple
            | SyntaxKind::TypeUnit
            | SyntaxKind::TypeParen
            | SyntaxKind::RowVar
    )
}

/// Every lower-case name in `n` outside `skip` and outside a type. In a
/// pattern each one is a binder: a variable, a record field (`{ a }`) or an
/// alias (`… as r`).
fn lower_idents(n: &SyntaxNode, skip: Option<TextRange>, out: &mut HashSet<String>) {
    for t in n.descendants_with_tokens().filter_map(|e| e.into_token()) {
        if t.kind() != SyntaxKind::LowerIdent
            || skip.is_some_and(|r| r.contains_range(t.text_range()))
            || t.parent_ancestors().any(|a| is_type_kind(a.kind()))
        {
            continue;
        }
        out.insert(t.text().to_string());
    }
}

/// The names one line of a `let` block binds: an `=` line's name (and, with
/// `params`, its parameters), or every name in the pattern of a destructure
/// line or a bind line (`p <- e`). A type annotation line binds nothing.
pub(crate) fn let_line_names(line: &SyntaxNode, params: bool, out: &mut HashSet<String>) {
    match line.kind() {
        SyntaxKind::LetBinding => {
            let Some(b) = ast::LetBinding::cast(line.clone()) else {
                return;
            };
            if params {
                let body = b.body().map(|e| e.syntax().text_range());
                lower_idents(line, body, out);
            } else if let Some(n) = b.name() {
                out.insert(n.text().to_string());
            }
        }
        SyntaxKind::BindBinding => {
            if let Some(p) = ast::BindBinding::cast(line.clone()).and_then(|b| b.pattern()) {
                lower_idents(p.syntax(), None, out);
            }
        }
        SyntaxKind::DestructureBinding => {
            if let Some(p) = line.children().next() {
                lower_idents(&p, None, out);
            }
        }
        _ => {}
    }
}

/// The names the lines of `let` block `le` bind (see [`let_line_names`]).
pub(crate) fn let_binder_names(le: &SyntaxNode, params: bool, out: &mut HashSet<String>) {
    for line in le.children() {
        let_line_names(&line, params, out);
    }
}

/// Every name bound anywhere inside `node`: lambda and function parameters
/// (a `ParamList`), `case` patterns, and the lines of every `let` (bind lines included).
pub(crate) fn names_bound_inside(node: &SyntaxNode) -> HashSet<String> {
    let mut out = HashSet::new();
    for d in node.descendants() {
        let k = d.kind();
        if k == SyntaxKind::LetExpr {
            let_binder_names(&d, false, &mut out);
        } else if matches!(k, SyntaxKind::ParamList | SyntaxKind::MatchArm) {
            for p in d.children().filter(|c| is_pattern_kind(c.kind())) {
                lower_idents(&p, None, &mut out);
            }
        }
    }
    out
}

fn is_pattern_kind(k: SyntaxKind) -> bool {
    matches!(
        k,
        SyntaxKind::PatWildcard
            | SyntaxKind::PatVar
            | SyntaxKind::PatCtor
            | SyntaxKind::PatCtorQual
            | SyntaxKind::PatList
            | SyntaxKind::PatCons
            | SyntaxKind::PatTuple
            | SyntaxKind::PatUnit
            | SyntaxKind::PatRecord
            | SyntaxKind::PatAlias
            | SyntaxKind::PatInt
            | SyntaxKind::PatFloat
            | SyntaxKind::PatString
            | SyntaxKind::PatChar
            | SyntaxKind::PatBool
            | SyntaxKind::PatParen
            | SyntaxKind::PatNegate
    )
}
