//! Task effect classification must follow logical signatures without changing
//! the lowerer's deliberately body-only representation/coercion type tables.
use super::*;

fn task_head(types: &BodyTypes, expr: ExprId) -> bool {
    types
        .expr_app_name(expr)
        .is_some_and(|name| ty::nominal::base(name.as_str()) == "Task")
}

pub(super) fn logical_task_discards(
    typer: &Typer<'_>,
    def: DefId,
    body: &Body,
    representation: &BodyTypes,
) -> BTreeSet<ExprId> {
    let mut pending = BTreeSet::new();
    for (_, expr) in body.exprs.iter() {
        let Expr::Let { defs, .. } = expr else {
            continue;
        };
        for local in defs {
            if !local.binders.is_empty() {
                continue;
            }
            // Match lower_let_def's early-return path exactly. A wildcard
            // INSIDE a destructure discards data; only a root discard can run
            // a Task. Never reinterpret tuple slots as effect boundaries.
            let destructure = local.params.is_empty()
                && local.pat.is_some_and(|pat| {
                    matches!(
                        &body.pats[pat],
                        Pattern::Tuple(_)
                            | Pattern::Record(_)
                            | Pattern::Ctor { .. }
                            | Pattern::List(_)
                            | Pattern::Cons(_, _)
                            | Pattern::Alias(_, _)
                    )
                });
            if !destructure && !task_head(representation, local.body) {
                pending.insert(local.body);
            }
        }
    }
    if pending.is_empty() || typer.value_sig(def).is_none() {
        return BTreeSet::new();
    }
    // Exactly one signature-seeded inference for an affected definition. Read
    // only these sites' solved heads; never build annotated representation
    // tables, result/signature types or unrelated expression/local readbacks.
    typer.task_typed_expressions(def, body, &pending)
}
