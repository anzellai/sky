use super::*;
use ty::numeric::{NumberEvidence, NumericBinder, NumericParam};

pub(super) fn number_ty() -> GoTy {
    GoTy::Named("rt.NumberKind".into(), Vec::new())
}
pub(super) fn number_name(p: NumericParam) -> String {
    match p.binder {
        NumericBinder::Top => format!("__num_top_{}", p.index),
        NumericBinder::Local(l) => format!("__num_local_{}_{}", l.0, p.index),
    }
}

impl Ctx<'_> {
    pub(super) fn number_expr(&self, evidence: NumberEvidence) -> GoExpr {
        match evidence {
            NumberEvidence::Param(p) => GoExpr::new(GoExprKind::Ident(number_name(p)), number_ty()),
            NumberEvidence::Int | NumberEvidence::Float => {
                let ordinal = if evidence == NumberEvidence::Float {
                    1
                } else {
                    0
                };
                call_rt(
                    "rt.NumberKind",
                    vec![GoExpr::new(
                        GoExprKind::IntLit(ordinal),
                        GoTy::Bare(Prim::Int),
                    )],
                    number_ty(),
                )
            }
        }
    }

    pub(super) fn number_params(&self, binder: NumericBinder, count: usize) -> Vec<GoParam> {
        (0..count)
            .map(|index| GoParam {
                name: number_name(NumericParam { binder, index }),
                ty: number_ty(),
            })
            .collect()
    }

    pub(super) fn number_args(&mut self, reference: ExprId, count: usize) -> Vec<GoExpr> {
        let args = self
            .numeric
            .references
            .get(&reference)
            .cloned()
            .unwrap_or_default();
        if args.len() != count {
            self.error(format!(
                "numeric instantiation evidence has {} argument(s), expected {count}",
                args.len()
            ));
            return (0..count)
                .map(|_| self.number_expr(NumberEvidence::Int))
                .collect();
        }
        args.into_iter().map(|e| self.number_expr(e)).collect()
    }

    pub(super) fn numeric_reference_count(&self, e: ExprId) -> usize {
        match &self.body.exprs[e] {
            Expr::Var(Res::Def(d)) if !self.kernel_alias.contains_key(d) => {
                self.defs.get(d).map_or(0, |d| d.numeric.top_params)
            }
            Expr::Var(Res::Local(l)) => self.numeric.local_params.get(l).copied().unwrap_or(0),
            _ => 0,
        }
    }

    pub(super) fn numeric_raw_reference(&mut self, e: ExprId) -> Option<GoExpr> {
        match self.body.exprs[e].clone() {
            Expr::Var(Res::Def(d)) if !self.kernel_alias.contains_key(&d) => {
                let entry = self.defs.get(&d)?;
                let name = top_go_name(&entry.module_name, &entry.name);
                let module = entry.module_name.clone();
                let count = entry.numeric.top_params;
                let mut params = vec![number_ty(); count];
                for ty in self.def_param_tys.get(&d).cloned().unwrap_or_default() {
                    params.push(self.goty_in(&ty, &module));
                }
                let ret = self
                    .def_result_tys
                    .get(&d)
                    .cloned()
                    .map(|t| self.goty_in(&t, &module))
                    .unwrap_or(GoTy::Any);
                self.discovered.push(d);
                Some(GoExpr::new(
                    GoExprKind::Ident(name),
                    GoTy::Func(params, Box::new(ret)),
                ))
            }
            Expr::Var(Res::Local(l)) => {
                let name = self
                    .local_names
                    .get(&l)
                    .cloned()
                    .unwrap_or_else(|| format!("v_{}", l.0));
                let ty = self.local_tys.get(&l).cloned()?;
                Some(GoExpr::new(GoExprKind::Ident(name), ty))
            }
            _ => None,
        }
    }

    /// Freeze witnesses before the first-class function escapes its current
    /// invocation. A TCO iteration may subsequently assign a new witness.
    pub(super) fn lower_numeric_reference(&mut self, e: ExprId) -> Option<GoExpr> {
        let count = self.numeric_reference_count(e);
        if count == 0 {
            return None;
        }
        let raw = self.numeric_raw_reference(e)?;
        let (params, ret) = match &raw.ty {
            GoTy::Func(params, ret) => (params.clone(), ret.clone()),
            _ => {
                self.error("numeric binding is not a function".into());
                return None;
            }
        };
        if params.len() < count {
            self.error("numeric binding is missing hidden parameters".into());
            return None;
        }
        let args = self.number_args(e, count);
        let visible = params[count..].to_vec();
        if visible.is_empty() {
            return Some(GoExpr::new(GoExprKind::Call(Box::new(raw), args), *ret));
        }
        let mut captured = Vec::new();
        let mut call_args = Vec::new();
        for _ in 0..count {
            let name = self.fresh_temp();
            captured.push(GoParam {
                name: name.clone(),
                ty: number_ty(),
            });
            call_args.push(GoExpr::new(GoExprKind::Ident(name), number_ty()));
        }
        let mut visible_params = Vec::new();
        for ty in &visible {
            let name = self.fresh_temp();
            visible_params.push(GoParam {
                name: name.clone(),
                ty: ty.clone(),
            });
            call_args.push(GoExpr::new(GoExprKind::Ident(name), ty.clone()));
        }
        let call = GoExpr::new(GoExprKind::Call(Box::new(raw), call_args), (*ret).clone());
        let visible_ty = GoTy::Func(visible, ret.clone());
        let closure = GoExpr::new(
            GoExprKind::FuncLit(
                visible_params,
                (*ret).clone(),
                vec![GoStmt::Return(Some(call))],
            ),
            visible_ty.clone(),
        );
        let capture_ty = GoTy::Func(vec![number_ty(); count], Box::new(visible_ty.clone()));
        let capture = GoExpr::new(
            GoExprKind::FuncLit(
                captured,
                visible_ty.clone(),
                vec![GoStmt::Return(Some(closure))],
            ),
            capture_ty,
        );
        Some(GoExpr::new(
            GoExprKind::Call(Box::new(capture), args),
            visible_ty,
        ))
    }

    pub(super) fn lower_numeric_call(
        &mut self,
        callee: ExprId,
        args: &[ExprId],
        actual: &GoTy,
    ) -> Option<GoExpr> {
        let count = self.numeric_reference_count(callee);
        if count == 0 {
            return None;
        }
        // Saturated calls pass their dictionaries directly. Only escaping
        // references and partial calls need a capturing closure.
        let raw = self.numeric_raw_reference(callee)?;
        if let GoTy::Func(params, ret) = &raw.ty {
            let visible = params.len().saturating_sub(count);
            if visible > 0 && args.len() >= visible {
                let mut given = self.number_args(callee, count);
                for (i, arg) in args.iter().enumerate() {
                    given.push(self.lower_expr(*arg, params.get(count + i).unwrap_or(&GoTy::Any)));
                }
                let ret = (**ret).clone();
                if args.len() > visible {
                    return Some(self.over_apply(raw, given, count + visible, &ret));
                }
                return Some(GoExpr::new(GoExprKind::Call(Box::new(raw), given), ret));
            }
        }
        let value = self.lower_numeric_reference(callee)?;
        let (params, ret) = match &value.ty {
            GoTy::Func(params, ret) => (params.clone(), ret.clone()),
            _ => {
                self.error("numeric value is not callable".into());
                return Some(value);
            }
        };
        let given: Vec<_> = args
            .iter()
            .enumerate()
            .map(|(i, e)| self.lower_expr(*e, params.get(i).unwrap_or(&GoTy::Any)))
            .collect();
        if given.len() < params.len() {
            return Some(self.make_partial(
                value,
                given,
                &Some(params.clone()),
                &ret,
                params.len(),
                actual,
            ));
        }
        if given.len() > params.len() {
            return Some(self.over_apply(value, given, params.len(), &ret));
        }
        Some(GoExpr::new(GoExprKind::Call(Box::new(value), given), *ret))
    }

    pub(super) fn register_local_function(
        &mut self,
        d: &hir::LocalDef,
        binder: LocalId,
        count: usize,
    ) {
        let (rows, row_result) = self.local_fn_row_poly(&d.params, d.body);
        let mut types = vec![number_ty(); count];
        for (i, p) in d.params.iter().enumerate() {
            let (_, ty, _) = self.bind_param(*p, None);
            types.push(if rows.get(i).copied().unwrap_or(false) {
                GoTy::Any
            } else {
                ty
            });
        }
        let ret = if row_result {
            GoTy::Any
        } else {
            self.expr_ty(d.body)
        };
        self.local_tys
            .insert(binder, GoTy::Func(types, Box::new(ret)));
    }

    pub(super) fn lower_numeric_local(
        &mut self,
        d: &hir::LocalDef,
        binder: LocalId,
        count: usize,
    ) -> GoExpr {
        let scope = self.numeric_scope.clone();
        self.numeric_scope.push(NumericBinder::Local(binder));
        let hidden = self.number_params(NumericBinder::Local(binder), count);
        let value = if d.params.is_empty() {
            let ty = self.expr_ty(d.body);
            let value = self.lower_expr(d.body, &ty);
            let ret = value.ty.clone();
            let ty = GoTy::Func(
                hidden.iter().map(|p| p.ty.clone()).collect(),
                Box::new(ret.clone()),
            );
            GoExpr::new(
                GoExprKind::FuncLit(hidden, ret, vec![GoStmt::Return(Some(value))]),
                ty,
            )
        } else {
            let mut value = self.lower_local_fn(&d.params, d.body);
            if let GoExprKind::FuncLit(params, _, _) = &mut value.kind {
                let mut all = hidden;
                all.append(params);
                *params = all;
            }
            if let GoTy::Func(params, _) = &mut value.ty {
                let mut all = vec![number_ty(); count];
                all.append(params);
                *params = all;
            }
            value
        };
        self.numeric_scope.pop();
        self.freeze_closure_scope(value, &scope, d.body)
    }
}
