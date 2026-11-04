//! A deliberately restricted proof gate, not an effect-family heuristic.
//! Unsupported shapes receive the original encoder and reference execution.

use super::Ctx;
use base::DefId;
use hir::{Body, Expr, ExprId, Pattern, Res};

impl Ctx<'_> {
    // Use the declaration's REAL emitted signature, exactly like lower_var.
    // Caller inference/boot values are not a proof of the Full input ABI.
    pub(super) fn spa_model_input_type(&mut self, encoder: ExprId) -> super::GoExpr {
        let real = match &self.body.exprs[encoder] {
            Expr::Var(Res::Def(d)) => self.defs.get(d).and_then(|callee| {
                let params = self.def_param_tys.get(d)?;
                if params.len() != 1 {
                    return None;
                }
                Some((params[0].clone(), callee.module_name.clone()))
            }),
            _ => None,
        };
        if let Some((param, module)) = real {
            let ty = self.goty_in(&param, &module);
            super::GoExpr::new(
                super::GoExprKind::GenericCall("rt.SpaModelInputType".into(), vec![ty], vec![]),
                super::GoTy::Any,
            )
        } else {
            // A missing invariant signature grants no input fast path.
            super::GoExpr::new(super::GoExprKind::Ident("nil".into()), super::GoTy::Any)
        }
    }

    // Preserve the actual ABI shared by Full, Prepare and Finish. A matching
    // HIR expression alone does not certify the coercions at callback entry.
    fn spa_model_callback_abi(&mut self, expr: ExprId) -> Option<(Vec<super::GoTy>, super::GoTy)> {
        let (params, result, module) = match &self.body.exprs[expr] {
            Expr::Var(Res::Def(d)) => self.defs.get(d).and_then(|callee| {
                Some((
                    self.def_param_tys.get(d)?.clone(),
                    self.def_result_tys.get(d)?.clone(),
                    callee.module_name.clone(),
                ))
            }),
            _ => None,
        }?;
        let params = params.iter().map(|t| self.goty_in(t, &module)).collect();
        let result = self.goty_in(&result, &module);
        Some((params, result))
    }

    fn validated_prepared_model_abi(
        &mut self,
        full: ExprId,
        prepare: ExprId,
        finish: ExprId,
    ) -> bool {
        let (Some(full), Some(prepare), Some(finish)) = (
            self.spa_model_callback_abi(full),
            self.spa_model_callback_abi(prepare),
            self.spa_model_callback_abi(finish),
        ) else {
            return false;
        };
        prepared_abi_eligible(&full, &prepare, &finish)
    }

    pub(super) fn validated_auto_model_encoder(&self, expr: ExprId) -> bool {
        let Some(trusted) = self.trusted_auto_codec else {
            return false;
        };
        let Expr::Var(Res::Def(def)) = &self.body.exprs[expr] else {
            return false;
        };
        let Some(encoder) = self.defs.get(def) else {
            return false;
        };
        eligible(&encoder.body, trusted, &|d| {
            self.defs.get(&d).map(|e| &e.body)
        })
    }
}

fn call(body: &Body, mut id: ExprId) -> (ExprId, Vec<ExprId>) {
    let mut args = Vec::new();
    while let Expr::Call(head, tail) = &body.exprs[id] {
        args.splice(0..0, tail.iter().copied());
        id = *head;
    }
    (id, args)
}

fn eligible<'a>(
    body: &Body,
    trusted: (DefId, DefId),
    lookup: &impl Fn(DefId) -> Option<&'a Body>,
) -> bool {
    let [param] = body.params.as_slice() else {
        return false;
    };
    let Pattern::Var(local) = &body.pats[*param] else {
        return false;
    };
    let Some(root) = body.root else {
        return false;
    };
    let (head, args) = call(body, root);
    if !matches!(&body.exprs[head], Expr::Var(Res::Def(d)) if *d == trusted.0) || args.len() != 2 {
        return false;
    }
    if !matches!(&body.exprs[args[1]], Expr::Var(Res::Local(l)) if l == local) {
        return false;
    }
    let (auto, witnesses) = call(body, args[0]);
    if !matches!(&body.exprs[auto], Expr::Var(Res::Def(d)) if *d == trusted.1)
        || witnesses.len() != 1
    {
        return false;
    }
    let Expr::Var(Res::Def(blank)) = &body.exprs[witnesses[0]] else {
        return false;
    };
    let Some(blank_body) = lookup(*blank) else {
        return false;
    };
    let mut visiting = std::collections::HashSet::from([*blank]);
    let mut budget = 4096;
    blank_body.params.is_empty()
        && blank_body
            .root
            .is_some_and(|r| literal(blank_body, r, lookup, &mut visiting, &mut budget))
}

fn literal<'a>(
    body: &Body,
    id: ExprId,
    lookup: &impl Fn(DefId) -> Option<&'a Body>,
    visiting: &mut std::collections::HashSet<DefId>,
    budget: &mut usize,
) -> bool {
    base::ensure_stack(|| {
        if *budget == 0 {
            return false;
        }
        *budget -= 1;
        match &body.exprs[id] {
            Expr::Int(_) | Expr::Str(_) | Expr::Chr(_) | Expr::Bool(_) | Expr::Unit => true,
            Expr::Float(n) => n.is_finite(),
            Expr::List(xs) | Expr::Tuple(xs) => xs
                .iter()
                .all(|x| literal(body, *x, lookup, visiting, budget)),
            Expr::Record(fs) => fs
                .iter()
                .all(|(_, x)| literal(body, *x, lookup, visiting, budget)),
            Expr::Negate(x) => {
                matches!(&body.exprs[*x], Expr::Int(_) | Expr::Float(_))
                    && literal(body, *x, lookup, visiting, budget)
            }
            Expr::Var(Res::Ctor(c)) => c.arity == 0,
            Expr::Var(Res::Def(d)) => {
                if !visiting.insert(*d) {
                    return false;
                }
                let proven = lookup(*d).is_some_and(|alias| {
                    alias.params.is_empty()
                        && alias
                            .root
                            .is_some_and(|root| literal(alias, root, lookup, visiting, budget))
                });
                visiting.remove(d);
                proven
            }
            Expr::Call(_, _) => {
                let (head, args) = call(body, id);
                matches!(&body.exprs[head], Expr::Var(Res::Ctor(c)) if usize::from(c.arity) == args.len())
                    && args
                        .iter()
                        .all(|x| literal(body, *x, lookup, visiting, budget))
            }
            // CAF aliases are admitted only by recursively proving literal data.
            // Function calls, lets, effects and operators retain reference execution.
            _ => false,
        }
    })
}

// The broad capsule preserves strict codec construction on EVERY invocation.
// It does not grant the independent closed/pure reuse capability.
impl Ctx<'_> {
    pub(super) fn validated_prepared_model_encoder(
        &mut self,
        full: ExprId,
        prepare: ExprId,
        finish: ExprId,
    ) -> bool {
        let Some(trusted) = self.trusted_auto_codec else {
            return false;
        };
        let proven = {
            let resolved = |expr| match &self.body.exprs[expr] {
                Expr::Var(Res::Def(d)) => self.defs.get(d).map(|v| &v.body),
                _ => None,
            };
            let (Some(full), Some(prepare), Some(finish)) =
                (resolved(full), resolved(prepare), resolved(finish))
            else {
                return false;
            };
            prepared_eligible(full, prepare, finish, trusted, &|d| {
                self.defs.get(&d).map(|v| &v.body)
            })
        };
        proven && self.validated_prepared_model_abi(full, prepare, finish)
    }
}

// Deliberately exact Go representation equality. Structural record bridges,
// reordered aliases and extra callback entry coercions retain reference Full.
fn prepared_abi_eligible(
    full: &(Vec<super::GoTy>, super::GoTy),
    prepare: &(Vec<super::GoTy>, super::GoTy),
    finish: &(Vec<super::GoTy>, super::GoTy),
) -> bool {
    use super::{GoTy, Prim};
    full.0.len() == 1
        && prepare.0.len() == 1
        && finish.0.len() == 2
        && matches!(&prepare.0[0], GoTy::Unit | GoTy::Any)
        && full.1 == GoTy::Bare(Prim::Str)
        && finish.1 == GoTy::Bare(Prim::Str)
        && (finish.0[1] == full.0[0] || finish.0[1] == GoTy::Any)
        && (finish.0[0] == prepare.1 || finish.0[0] == GoTy::Any)
}

fn local_param(body: &Body, index: usize) -> Option<hir::LocalId> {
    match body.params.get(index).map(|p| &body.pats[*p]) {
        Some(Pattern::Var(local)) => Some(*local),
        _ => None,
    }
}

fn local_is(body: &Body, expr: ExprId, local: hir::LocalId) -> bool {
    matches!(&body.exprs[expr], Expr::Var(Res::Local(l)) if *l == local)
}

fn prepared_eligible<'a>(
    full: &Body,
    prepare: &Body,
    finish: &Body,
    trusted: (DefId, DefId),
    lookup: &impl Fn(DefId) -> Option<&'a Body>,
) -> bool {
    if full.params.len() != 1 || prepare.params.len() != 1 || finish.params.len() != 2 {
        return false;
    }
    let (Some(model), Some(_unit), Some(codec), Some(finish_model)) = (
        local_param(full, 0),
        local_param(prepare, 0),
        local_param(finish, 0),
        local_param(finish, 1),
    ) else {
        return false;
    };
    let (Some(full_root), Some(prepare_root), Some(finish_root)) =
        (full.root, prepare.root, finish.root)
    else {
        return false;
    };
    let (to_json, full_args) = call(full, full_root);
    if !matches!(&full.exprs[to_json], Expr::Var(Res::Def(d)) if *d == trusted.0)
        || full_args.len() != 2
        || !local_is(full, full_args[1], model)
    {
        return false;
    }
    let (auto, auto_args) = call(full, full_args[0]);
    if !matches!(&full.exprs[auto], Expr::Var(Res::Def(d)) if *d == trusted.1)
        || auto_args.len() != 1
    {
        return false;
    }
    let Expr::Var(Res::Def(blank)) = &full.exprs[auto_args[0]] else {
        return false;
    };
    // A nonliteral CAF is allowed ONLY because Prepare evaluates it in place.
    if !lookup(*blank).is_some_and(|b| b.params.is_empty()) {
        return false;
    }
    let (auto, prepare_args) = call(prepare, prepare_root);
    if !matches!(&prepare.exprs[auto], Expr::Var(Res::Def(d)) if *d == trusted.1)
        || prepare_args.len() != 1
        || !matches!(&prepare.exprs[prepare_args[0]], Expr::Var(Res::Def(d)) if d == blank)
    {
        return false;
    }
    let (to_json, finish_args) = call(finish, finish_root);
    matches!(&finish.exprs[to_json], Expr::Var(Res::Def(d)) if *d == trusted.0)
        && finish_args.len() == 2
        && local_is(finish, finish_args[0], codec)
        && local_is(finish, finish_args[1], finish_model)
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::{SkyDb, SourceDb};
    use std::collections::BTreeMap;

    fn check(blank: &str, encoder: &str, grant: bool) -> bool {
        let mut db = SourceDb::new();
        let codec = db.add_module(
            "Codec",
            syntax::parse(
                "module Codec exposing (..)\nauto x = x\ntoJson c m = m\n",
                FileId(0),
            ),
        );
        let source = format!("module Main exposing (..)\nimport Codec as C\nrecordAlias = {{ text = \"\" }}\nsecondAlias = recordAlias\nimpureAlias = helper ()\ncycleA = cycleB\ncycleB = cycleA\nblank =\n    {blank}\n\nencode m =\n    {encoder}\n");
        let main = db.add_module("Main", syntax::parse(&source, FileId(1)));
        let codec = db.resolve(codec);
        let resolved = db.resolve(main);
        let def = |r: &hir::ResolveResult, name: &str| {
            r.top_defs
                .iter()
                .find(|d| d.name.as_str() == name)
                .unwrap()
                .def
        };
        let trusted = (def(&codec, "toJson"), def(&codec, "auto"));
        let bodies: BTreeMap<_, _> = resolved.bodies.iter().map(|(d, b)| (*d, b)).collect();
        grant
            && eligible(bodies[&def(&resolved, "encode")], trusted, &|d| {
                bodies.get(&d).copied()
            })
    }

    #[test]
    fn accepts_only_direct_canonical_encoder_with_literal_blank() {
        assert!(check(
            "{ text = \"\", items = [], count = -1 }",
            "C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check(
            "{ text = \"\" }",
            "C.toJson (C.auto blank) m",
            false
        ));
        assert!(!check("helper ()", "C.toJson (C.auto blank) m", true));
        assert!(!check(
            "{ text = \"\" }",
            "C.toJson (C.auto blank) { m | text = \"changed\" }",
            true
        ));
        assert!(!check(
            "{ text = \"\" }",
            "let x = helper () in C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check("{ text = \"\" }", "C.toJson (custom blank) m", true));
        assert!(!check("{ text = \"\" }", "helper m", true));
        assert!(check("secondAlias", "C.toJson (C.auto blank) m", true));
        assert!(check(
            "[ secondAlias, secondAlias ]",
            "C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check("impureAlias", "C.toJson (C.auto blank) m", true));
        assert!(!check("cycleA", "C.toJson (C.auto blank) m", true));
        let excessive = format!("[ {} ]", vec!["secondAlias"; 4096].join(", "));
        assert!(!check(&excessive, "C.toJson (C.auto blank) m", true));
    }

    fn prepared_check(blank: &str, full: &str, prepare: &str, finish: &str, grant: bool) -> bool {
        let mut db = SourceDb::new();
        let codec = db.add_module(
            "Codec",
            syntax::parse(
                "module Codec exposing (..)\nauto x = x\ntoJson c m = m\n",
                FileId(0),
            ),
        );
        let source = format!("module Main exposing (..)\nimport Codec as C\nblank = {blank}\nother = {{ n = 2 }}\nhelper x = x\nfakeAuto x = x\nfakeToJson c x = x\nauto x = x\ntoJson c x = x\nfull m = {full}\nprepare unit = {prepare}\nfinish c m = {finish}\n");
        let main = db.add_module("Main", syntax::parse(&source, FileId(1)));
        let cr = db.resolve(codec);
        let mr = db.resolve(main);
        let find = |r: &hir::ResolveResult, name: &str| {
            r.top_defs
                .iter()
                .find(|d| d.name.as_str() == name)
                .unwrap()
                .def
        };
        let bodies: BTreeMap<_, _> = mr.bodies.iter().map(|(d, b)| (*d, b)).collect();
        grant
            && prepared_eligible(
                bodies[&find(&mr, "full")],
                bodies[&find(&mr, "prepare")],
                bodies[&find(&mr, "finish")],
                (find(&cr, "toJson"), find(&cr, "auto")),
                &|d| bodies.get(&d).copied(),
            )
    }

    #[test]
    fn prepared_proof_requires_exact_resolved_spines_and_same_caf() {
        let full = "C.toJson (C.auto blank) m";
        let prepare = "C.auto blank";
        let finish = "C.toJson c m";
        assert!(prepared_check(
            "helper { n = 1 }",
            full,
            prepare,
            finish,
            true
        ));
        assert!(!prepared_check("{ n = 1 }", full, prepare, finish, false));
        assert!(!prepared_check(
            "{ n = 1 }",
            full,
            "C.auto other",
            finish,
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            full,
            "fakeAuto blank",
            finish,
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            "fakeToJson (C.auto blank) m",
            prepare,
            finish,
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            full,
            prepare,
            "fakeToJson c m",
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            "toJson (auto blank) m",
            prepare,
            finish,
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            full,
            prepare,
            "C.toJson c { m | n = 2 }",
            true
        ));
        assert!(!prepared_check(
            "{ n = 1 }",
            full,
            "let x = helper () in C.auto blank",
            finish,
            true
        ));
    }

    #[test]
    fn prepared_abi_rejects_extra_entry_coercions_and_nonstring_results() {
        use super::super::{GoTy, Prim};
        let model = GoTy::Named("Main_Model_R".into(), vec![]);
        let other = GoTy::Named("Main_OtherModel_R".into(), vec![]);
        let codec = GoTy::Named("rt.SkyADT".into(), vec![]);
        let string = GoTy::Bare(Prim::Str);
        let full = (vec![model.clone()], string.clone());
        let prepare = (vec![GoTy::Unit], codec.clone());
        let finish = (vec![codec.clone(), model.clone()], string.clone());
        assert!(prepared_abi_eligible(&full, &prepare, &finish));
        assert!(prepared_abi_eligible(
            &full,
            &(vec![GoTy::Any], codec.clone()),
            &finish
        ));
        assert!(prepared_abi_eligible(
            &full,
            &prepare,
            &(vec![GoTy::Any, GoTy::Any], string.clone())
        ));
        assert!(!prepared_abi_eligible(
            &full,
            &(vec![GoTy::Bare(Prim::Int)], codec.clone()),
            &finish
        ));
        assert!(!prepared_abi_eligible(
            &full,
            &prepare,
            &(vec![codec.clone(), other.clone()], string.clone())
        ));
        assert!(!prepared_abi_eligible(
            &full,
            &prepare,
            &(vec![other, model], string.clone())
        ));
        assert!(!prepared_abi_eligible(
            &(full.0.clone(), GoTy::Any),
            &prepare,
            &finish
        ));
        assert!(!prepared_abi_eligible(
            &full,
            &prepare,
            &(finish.0.clone(), GoTy::Any)
        ));
        assert!(!prepared_abi_eligible(&full, &(vec![], codec), &finish));
    }
}
