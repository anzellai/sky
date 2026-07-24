//! Exhaustiveness (doc 06 §"Exhaustiveness"; doc 03 §8). Deliberately
//! conservative-but-real: **only ADT-constructor and Bool heads force
//! coverage** — a wildcard/var/irrefutable head covers the rest, and
//! infinite-domain literal (Int/String/…) and list heads are NOT forced (we do
//! not over-report). Warns `E3001` on a genuine missing constructor.

use crate::sig::World;
use hir::{Body, Expr, ExprId, PatId, Pattern};
use std::collections::BTreeSet;

/// Walk every `case` in a body, returning one `E3001` diagnostic per
/// non-exhaustive ADT/Bool match.
pub fn check_body(body: &Body, world: &World) -> Vec<diagnostics::Diagnostic> {
    let mut out = Vec::new();
    if let Some(root) = body.root {
        walk(body, world, root, &mut out);
    }
    out
}

fn walk(body: &Body, world: &World, e: ExprId, out: &mut Vec<diagnostics::Diagnostic>) {
    match &body.exprs[e] {
        Expr::Case { subject, branches } => {
            walk(body, world, *subject, out);
            check_case(body, world, *subject, branches, out);
            for br in branches {
                walk(body, world, br.body, out);
            }
        }
        Expr::List(xs) | Expr::Tuple(xs) => {
            for &x in xs {
                walk(body, world, x, out);
            }
        }
        Expr::Record(fs) => {
            for (_, v) in fs {
                walk(body, world, *v, out);
            }
        }
        Expr::Update { base, fields } => {
            walk(body, world, *base, out);
            for (_, v) in fields {
                walk(body, world, *v, out);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => walk(body, world, *x, out),
        Expr::Lambda { body: b, .. } => walk(body, world, *b, out),
        Expr::Call(f, args) => {
            walk(body, world, *f, out);
            for &a in args {
                walk(body, world, a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            walk(body, world, *lhs, out);
            walk(body, world, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk(body, world, *c, out);
                walk(body, world, *t, out);
            }
            walk(body, world, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                walk(body, world, d.body, out);
            }
            walk(body, world, *b, out);
        }
        // Leaves — no sub-expression to descend into. Listed EXPLICITLY (not a
        // `_` catch-all) so that adding a NEW child-bearing `Expr` variant is a
        // compile error HERE, forcing it to be walked — rather than silently
        // falling through and leaving every `case` nested inside it unchecked for
        // exhaustiveness (the §8/L6 "don't rely on `_ -> []` catchalls" hazard).
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Var(_)
        | Expr::Accessor(_)
        | Expr::Error => {}
    }
}

/// A branch's head classified for exhaustiveness (`classify`, Exhaustiveness.hs).
enum Head {
    /// Covers everything: var / wildcard / alias-of-cover / irrefutable shape.
    Cover,
    /// A named constructor (ADT or Bool True/False) + the union's `DefId` when
    /// the ctor resolved (disambiguates same-named unions across modules).
    Ctor(String, Option<base::DefId>),
    /// An infinite-domain literal head (Int / String / Char / Float). A match
    /// built only from these — with no covering `_`/var arm — can NEVER be
    /// exhaustive (the domain is unbounded), so the oracle rejects it
    /// ("Non-exhaustive patterns"). We force coverage the same way.
    Literal,
    /// A list/cons head (or anything else) that does NOT force coverage here —
    /// `[] | x :: xs` is a legitimately-exhaustive shape we don't over-report.
    Other,
}

fn classify(body: &Body, p: PatId) -> Head {
    match &body.pats[p] {
        Pattern::Anything | Pattern::Var(_) => Head::Cover,
        Pattern::Record(_) | Pattern::Tuple(_) | Pattern::Unit => Head::Cover,
        Pattern::Alias(inner, _) => classify(body, *inner),
        Pattern::Ctor { name, ctor, .. } => {
            Head::Ctor(name.as_str().to_string(), ctor.as_ref().map(|cr| cr.type_))
        }
        Pattern::Bool(b) => Head::Ctor(if *b { "True".into() } else { "False".into() }, None),
        Pattern::Int(_) | Pattern::Str(_) | Pattern::Chr(_) => Head::Literal,
        _ => Head::Other,
    }
}

fn check_case(
    body: &Body,
    world: &World,
    subject: ExprId,
    branches: &[hir::CaseBranch],
    out: &mut Vec<diagnostics::Diagnostic>,
) {
    // The `case` subject's span anchors any non-exhaustiveness diagnostic —
    // `body.expr_span` is populated by the resolver (Phase 1). `None` (recovery
    // node) → empty labels, matching the pre-span behaviour.
    let subject_label = |msg: &str| {
        body.expr_span(subject)
            .map(|s| {
                vec![diagnostics::Label {
                    span: s,
                    message: msg.to_string(),
                }]
            })
            .unwrap_or_default()
    };
    let mut covered: BTreeSet<String> = BTreeSet::new();
    let mut union_def: Option<base::DefId> = None;
    let mut has_literal = false;
    for br in branches {
        match classify(body, br.pat) {
            Head::Cover => return, // a covering head makes the match exhaustive
            Head::Ctor(c, def) => {
                covered.insert(c);
                union_def = union_def.or(def);
            }
            Head::Literal => has_literal = true,
            Head::Other => {}
        }
    }
    // A match built only from infinite-domain literals (no ADT/Bool head, no
    // covering arm — we'd have returned above) can never be exhaustive: the
    // oracle rejects it, so we must too. Requires a catch-all `_`/var arm.
    if covered.is_empty() && has_literal {
        out.push(diagnostics::Diagnostic {
            severity: diagnostics::Severity::Warning,
            code: diagnostics::Code("E3001".to_string()),
            message:
                "This `case` does not cover all cases — literal patterns need a catch-all `_` arm"
                    .to_string(),
            labels: subject_label("this `case` is not exhaustive"),
            suggestion: None,
        });
        return;
    }
    if covered.is_empty() {
        return; // no ADT/Bool heads to force coverage
    }
    // Prefer the DefId-identified union's member set (robust to same-named
    // unions across modules); fall back to the by-name set for builtins.
    let all: Option<&Vec<String>> = union_def
        .and_then(|d| world.union_members_by_def.get(&d))
        .or_else(|| {
            covered
                .iter()
                .next()
                .and_then(|first| world.ctor_union.get(first))
                .and_then(|u| world.union_ctors.get(u))
        });
    let Some(all) = all else {
        return; // unknown union — don't over-report (lenient)
    };
    let missing: Vec<&String> = all.iter().filter(|c| !covered.contains(*c)).collect();
    if !missing.is_empty() {
        let names: Vec<&str> = missing.iter().map(|s| s.as_str()).collect();
        out.push(diagnostics::Diagnostic {
            severity: diagnostics::Severity::Warning,
            code: diagnostics::Code("E3001".to_string()),
            message: format!(
                "This `case` does not cover all cases — missing: {}",
                names.join(", ")
            ),
            labels: subject_label("this `case` is not exhaustive"),
            suggestion: None,
        });
    }
}
