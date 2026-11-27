//! Exhaustiveness (doc 06 §"Exhaustiveness"; doc 03 §8). Deliberately
//! conservative-but-real: **only ADT-constructor and Bool heads force
//! coverage** — a wildcard/var/irrefutable head covers the rest, and
//! infinite-domain literal (Int/String/…) and list heads are NOT forced (we do
//! not over-report). Warns `E3001` on a genuine missing constructor, and on a
//! refutable pattern in a binder position ([`check_binder`], v0.32.0).

use crate::sig::World;
use hir::{Body, Expr, ExprId, PatId, Pattern};
use std::collections::BTreeSet;

/// Walk every `case` in a body, returning one `E3001` diagnostic per
/// non-exhaustive ADT/Bool match, and one per refutable pattern in a binder
/// position (a parameter or a `let` destructure, see [`check_binder`]).
pub fn check_body(body: &Body, world: &World, src: &str) -> Vec<diagnostics::Diagnostic> {
    let mut out = Vec::new();
    for &p in &body.params {
        check_binder(body, world, p, Binder::Parameter, &mut out);
    }
    if let Some(root) = body.root {
        walk(body, world, src, root, &mut out);
    }
    out
}

/// Where a pattern binds with no second branch to fall back on.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Binder {
    /// A parameter of a top-level or `let`-bound function.
    Parameter,
    /// A lambda parameter.
    LambdaParameter,
    /// The pattern of a `let` destructure (`let ( a, b ) = pair`).
    LetDestructure,
    /// The pattern of a bind line in a `let` block (v0.32.0).
    BindLine,
}

impl Binder {
    fn describe(self) -> &'static str {
        match self {
            Binder::Parameter => "a function parameter",
            Binder::LambdaParameter => "a lambda parameter",
            Binder::LetDestructure => "a `let` destructure",
            Binder::BindLine => "a bind line",
        }
    }
}

/// The first sub-pattern of `p` that can fail to match a value of its type,
/// or `None` when `p` matches every value (it is IRREFUTABLE).
///
/// Total: a variable, `_`, `()`, a record pattern, a tuple or an alias of
/// total patterns, and a constructor of a ONE-variant union whose arguments
/// are total. The union is found through `CtorRef::type_` (its `DefId`), never
/// by name (doc 13, D1). Refutable: a literal, a `Bool`, a list or cons
/// pattern, a constructor of a union with two or more variants, and a
/// constructor that did not resolve. `Pattern::Error` counts as total, so a
/// parse error is not reported a second time.
pub fn first_refutable(body: &Body, world: &World, p: PatId) -> Option<PatId> {
    match &body.pats[p] {
        Pattern::Anything | Pattern::Var(_) | Pattern::Unit | Pattern::Record(_) => None,
        Pattern::Error => None,
        Pattern::Alias(inner, _) => first_refutable(body, world, *inner),
        Pattern::Tuple(xs) => xs.iter().find_map(|&x| first_refutable(body, world, x)),
        Pattern::Ctor { ctor, args, .. } => {
            let one_variant = ctor
                .as_ref()
                .and_then(|cr| world.union_members_by_def.get(&cr.type_))
                .is_some_and(|members| members.len() == 1);
            if one_variant {
                args.iter().find_map(|&a| first_refutable(body, world, a))
            } else {
                Some(p)
            }
        }
        Pattern::Bool(_)
        | Pattern::Chr(_)
        | Pattern::Str(_)
        | Pattern::Int(_)
        | Pattern::Float(_)
        | Pattern::List(_)
        | Pattern::Cons(..) => Some(p),
    }
}

/// Report `[E3001]` when the pattern `p`, in the binder position `at`, can
/// fail to match. ONE check for every binder position: function parameters,
/// lambda parameters, `let` destructures and bind lines.
pub fn check_binder(
    body: &Body,
    world: &World,
    p: PatId,
    at: Binder,
    out: &mut Vec<diagnostics::Diagnostic>,
) {
    let Some(bad) = first_refutable(body, world, p) else {
        return;
    };
    let labels = body
        .pat_span(bad)
        .or_else(|| body.pat_span(p))
        .map(|span| {
            vec![diagnostics::Label {
                span,
                message: "this pattern does not match every value".to_string(),
            }]
        })
        .unwrap_or_default();
    out.push(diagnostics::Diagnostic {
        severity: diagnostics::Severity::Warning,
        code: diagnostics::Code("E3001".to_string()),
        message: format!(
            "This pattern in {} does not match every value, and there is no other branch \
             to take. Use a `case` with a branch for each shape instead.",
            at.describe()
        ),
        labels,
        suggestion: Some(
            "Bind a variable here and match it with `case … of`, which must cover every value."
                .to_string(),
        ),
    });
}

/// `[E3001]` for a refutable bind-line pattern ([`Binder::BindLine`]), with
/// a message that names the bind line and the values it does not match:
/// "`Just x <- …` does not match `Nothing`; use `case`".
fn check_bind_line(
    body: &Body,
    world: &World,
    src: &str,
    p: PatId,
    out: &mut Vec<diagnostics::Diagnostic>,
) {
    let mut found = Vec::new();
    check_binder(body, world, p, Binder::BindLine, &mut found);
    let Some(mut d) = found.pop() else { return };
    let text = body
        .pat_span(p)
        .and_then(|s| src.get(s.range.0 as usize..s.range.1 as usize))
        .map(str::trim)
        .filter(|t| !t.is_empty() && t.len() <= 40 && !t.contains('\n'))
        .unwrap_or("…");
    let bad = first_refutable(body, world, p).unwrap_or(p);
    let missing: Vec<String> = match &body.pats[bad] {
        Pattern::Ctor { ctor, name, .. } => ctor
            .as_ref()
            .and_then(|cr| world.union_members_by_def.get(&cr.type_))
            // A builtin constructor (`Just`, `Ok`) is known by name only.
            .or_else(|| {
                world
                    .ctor_union
                    .get(name.as_str())
                    .and_then(|u| world.union_ctors.get(u))
            })
            .map(|ms| {
                ms.iter()
                    .filter(|m| m.as_str() != name.as_str())
                    .map(|m| format!("`{m}`"))
                    .collect()
            })
            .unwrap_or_default(),
        Pattern::Bool(b) => vec![format!("`{}`", if *b { "False" } else { "True" })],
        _ => Vec::new(),
    };
    let what = if missing.is_empty() {
        "every value".to_string()
    } else {
        missing.join(" or ")
    };
    d.message = format!(
        "The bind line `{text} <- …` does not match {what}, and a bind line has no other \
         branch to take. Use `case` to handle each shape."
    );
    out.push(d);
}

fn walk(body: &Body, world: &World, src: &str, e: ExprId, out: &mut Vec<diagnostics::Diagnostic>) {
    // A body is as deep as its longest spine: a left-nested `|>` pipeline or a
    // generated record codec puts one level here per step. Grow the stack on
    // demand rather than overflow it (the other expression walkers do the same).
    base::ensure_stack(|| walk_guarded(body, world, src, e, out))
}

fn walk_guarded(
    body: &Body,
    world: &World,
    src: &str,
    e: ExprId,
    out: &mut Vec<diagnostics::Diagnostic>,
) {
    match &body.exprs[e] {
        Expr::Case { subject, branches } => {
            walk(body, world, src, *subject, out);
            check_case(body, world, *subject, branches, out);
            for br in branches {
                walk(body, world, src, br.body, out);
            }
        }
        Expr::List(xs) | Expr::Tuple(xs) => {
            for &x in xs {
                walk(body, world, src, x, out);
            }
        }
        Expr::Record(fs) => {
            for (_, v) in fs {
                walk(body, world, src, *v, out);
            }
        }
        Expr::Update { base, fields } => {
            walk(body, world, src, *base, out);
            for (_, v) in fields {
                walk(body, world, src, *v, out);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => walk(body, world, src, *x, out),
        Expr::Lambda { params, body: b } => {
            for &p in params {
                check_binder(body, world, p, Binder::LambdaParameter, out);
            }
            walk(body, world, src, *b, out)
        }
        // A bind line `p <- e`: the lambda's parameter is the bind pattern.
        Expr::Call(f, args)
            if matches!(body.exprs[*f], Expr::Var(hir::Res::BindAndThen)) && args.len() == 2 =>
        {
            match &body.exprs[args[0]] {
                Expr::Lambda { params, body: b } if params.len() == 1 => {
                    check_bind_line(body, world, src, params[0], out);
                    walk(body, world, src, *b, out);
                }
                _ => walk(body, world, src, args[0], out),
            }
            walk(body, world, src, args[1], out);
        }
        Expr::Call(f, args) => {
            walk(body, world, src, *f, out);
            for &a in args {
                walk(body, world, src, a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            walk(body, world, src, *lhs, out);
            walk(body, world, src, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk(body, world, src, *c, out);
                walk(body, world, src, *t, out);
            }
            walk(body, world, src, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                if let Some(p) = d.pat {
                    check_binder(body, world, p, Binder::LetDestructure, out);
                }
                for &p in &d.params {
                    check_binder(body, world, p, Binder::Parameter, out);
                }
                walk(body, world, src, d.body, out);
            }
            walk(body, world, src, *b, out);
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
            suggestion: open_union_hint(all, &missing),
        });
    }
}

/// The constructor members of `Sky.Core.Error.ErrorDetails`, before `Domain`.
/// Matched as a set (with `Domain`) so a same-named user union is not taken for
/// the stdlib one.
const ERROR_DETAILS_CORE: &[&str] = &[
    "FfiPanic",
    "TypeMismatch",
    "HttpStatus",
    "JsonDecode",
    "Custom",
];

/// A hint for a match that misses `Sky.Core.Error.Domain`. `ErrorDetails` is
/// the one stdlib union designed to stay open to domains (v0.28.0): a program
/// that matched every variant before `Domain` existed stops compiling, and the
/// fix that never breaks again is a generic helper or a `_ ->` branch, not a
/// `Domain` arm that a later domain's failures then reach unhandled.
fn open_union_hint(all: &[String], missing: &[&String]) -> Option<String> {
    let is_error_details = all.iter().any(|c| c == "Domain")
        && ERROR_DETAILS_CORE
            .iter()
            .all(|c| all.iter().any(|a| a == c));
    if !is_error_details || !missing.iter().any(|m| m.as_str() == "Domain") {
        return None;
    }
    Some(
        "`Sky.Core.Error.ErrorDetails` gained `Domain DomainFailure` in v0.28.0: the failures of a \
         stdlib domain (for example Std.Ai) or of your app. Add a `_ ->` branch, or use the \
         generic helpers `Error.domain`, `Error.isRetryable` and `Error.retryAfter` (or a \
         domain's typed view, such as `Std.Ai.Failure.fromError`), so later domains need no \
         change here. See docs/migration/v0.28.md#a-new-errordetails-variant."
            .to_string(),
    )
}
