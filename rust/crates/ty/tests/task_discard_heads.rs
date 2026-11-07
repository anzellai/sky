//! Head-only effect metadata must match the existing annotated-table oracle.
use hir::{Expr, SkyDb, SourceDb};
use std::collections::BTreeSet;

const SOURCE: &str = r#"module Main exposing (..)
type alias Pending = Task Error Int
type alias Envelope = { pending : Pending }
root : Pending -> Int
root pending = let _ = pending in 7
forwardedTask : Pending -> Int
forwardedTask pending = let forwarded = pending in let _ = forwarded in 7
local : Pending -> Int
local pending =
    let
        run : Pending -> Int
        run task = let _ = task in 7
    in run pending
lambda : Pending -> Int
lambda pending = (\task -> let _ = task in 7) pending
tuple : (Pending, Int) -> Int
tuple (pending, _) = let _ = pending in 7
field : Envelope -> Int
field model = let _ = model.pending in 7
function : (() -> Pending) -> Int
function pending = let _ = pending in 7
generic : a -> Int
generic pending = let _ = pending in 7
nested : Pending -> Int
nested pending = let (kept, _) = (7, pending) in kept
main = 0
"#;

#[test]
fn selected_task_heads_match_annotated_tables_for_full_sparse_and_empty_sites() {
    let mut db = SourceDb::new();
    let parsed = syntax::parse(SOURCE, base::FileId(0));
    assert!(parsed.errors().is_empty() && parsed.error_node_count() == 0);
    let module = db.add_module("Main", parsed);
    let resolved = db.resolve(module);
    assert!(resolved.class_a.is_empty());
    let typer = ty::Typer::new(&db);
    for top in &resolved.top_defs {
        let Some(body) = resolved.bodies.get(&top.def) else {
            continue;
        };
        let all: BTreeSet<_> = body.exprs.iter().map(|(e, _)| e).collect();
        let sparse: BTreeSet<_> = body
            .exprs
            .iter()
            .filter_map(|(_, e)| match e {
                Expr::Let { defs, .. } => Some(defs.iter().map(|d| d.body)),
                _ => None,
            })
            .flatten()
            .collect();
        let oracle = typer.body_types_annotated(top.def, body);
        for selected in [&all, &sparse, &BTreeSet::new()] {
            let expected: BTreeSet<_> = selected
                .iter()
                .copied()
                .filter(|e| {
                    oracle
                        .expr_app_name(*e)
                        .is_some_and(|name| ty::nominal::base(name.as_str()) == "Task")
                })
                .collect();
            assert_eq!(
                typer.task_typed_expressions(top.def, body, selected),
                expected,
                "{}",
                top.name.as_str()
            );
        }
        if ["root", "forwardedTask", "local", "lambda", "tuple", "field"]
            .contains(&top.name.as_str())
        {
            assert!(
                !typer
                    .task_typed_expressions(top.def, body, &sparse)
                    .is_empty(),
                "{} Task sites must be selected",
                top.name.as_str()
            );
        }
        if ["function", "generic"].contains(&top.name.as_str()) {
            assert!(
                typer
                    .task_typed_expressions(top.def, body, &sparse)
                    .is_empty(),
                "{} must remain latent",
                top.name.as_str()
            );
        }
    }
}
