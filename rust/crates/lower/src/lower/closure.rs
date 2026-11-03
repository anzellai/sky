//! Closures capture Sky values, including parameters reassigned by a tail loop.
use super::*;
use ty::numeric::NumericBinder;

/// An erased callback slot must not erase concrete information from the
/// lambda's body. Adapt at the boundary, after lowering the typed body.
pub(super) fn slot_loses_lambda_types(actual: &GoTy, slot: &GoTy, count: usize) -> bool {
    let Some((args, result)) = split_function_type(actual, count) else {
        return false;
    };
    let Some((slots, ret)) = split_function_type(slot, count) else {
        return false;
    };
    args.iter()
        .zip(&slots)
        .any(|(a, s)| *a != GoTy::Any && *s == GoTy::Any)
        || (result != GoTy::Any && ret == GoTy::Any)
}

/// Consume a proven function spine without discarding its remaining stages.
pub(super) fn split_function_type(ty: &GoTy, count: usize) -> Option<(Vec<GoTy>, GoTy)> {
    let mut current = ty.clone();
    let mut parameters = Vec::new();
    while parameters.len() < count {
        let (ps, ret) = current.into_func().ok()?;
        if ps.is_empty() {
            return None;
        }
        let take = (count - parameters.len()).min(ps.len());
        parameters.extend(ps[..take].iter().cloned());
        current = if take < ps.len() {
            GoTy::Func(ps[take..].to_vec(), Box::new(ret))
        } else {
            ret
        };
    }
    Some((parameters, current))
}

impl Ctx<'_> {
    pub(super) fn function_value_needs_capture(&self, value: &GoExpr) -> bool {
        match &value.kind {
            GoExprKind::FuncLit(..) => false,
            GoExprKind::Ident(name) => self
                .tco
                .as_ref()
                .is_some_and(|t| t.params.iter().any(|(n, _)| n == name)),
            _ => true,
        }
    }

    pub(super) fn with_frozen_values(
        &mut self,
        values: Vec<GoExpr>,
        build: impl FnOnce(&mut Self, Vec<GoExpr>) -> GoExpr,
    ) -> GoExpr {
        if values.is_empty() {
            return build(self, Vec::new());
        }
        let mut params = Vec::new();
        let mut references = Vec::new();
        for value in &values {
            let name = self.fresh_temp();
            params.push(GoParam {
                name: name.clone(),
                ty: value.ty.clone(),
            });
            references.push(GoExpr::new(GoExprKind::Ident(name), value.ty.clone()));
        }
        let body = build(self, references);
        let ret = body.ty.clone();
        let ty = GoTy::Func(
            params.iter().map(|p| p.ty.clone()).collect(),
            Box::new(ret.clone()),
        );
        let capture = GoExpr::new(
            GoExprKind::FuncLit(params, ret.clone(), vec![GoStmt::Return(Some(body))]),
            ty,
        );
        GoExpr::new(GoExprKind::Call(Box::new(capture), values), ret)
    }

    pub(super) fn freeze_closure_scope(
        &mut self,
        value: GoExpr,
        scope: &[NumericBinder],
        source: ExprId,
    ) -> GoExpr {
        let Some(tco) = self.tco.clone() else {
            return value;
        };
        let mut params = Vec::new();
        for &binder in scope {
            let count = match binder {
                NumericBinder::Top => self.numeric.top_params,
                NumericBinder::Local(l) => self.numeric.local_params.get(&l).copied().unwrap_or(0),
            };
            params.extend(self.number_params(binder, count));
        }
        let mut refs = HashSet::new();
        self.collect_local_refs(source, &mut refs);
        for (name, ty) in tco.params {
            if name != "_" && refs.iter().any(|l| self.local_names.get(l) == Some(&name)) {
                params.push(GoParam { name, ty });
            }
        }
        if params.is_empty() {
            return value;
        }
        // Reuse the source names in the capturing scope. The returned closure
        // then sees these immutable copies instead of the loop's variables.
        let args = params
            .iter()
            .map(|p| GoExpr::new(GoExprKind::Ident(p.name.clone()), p.ty.clone()))
            .collect();
        let ret = value.ty.clone();
        let ty = GoTy::Func(
            params.iter().map(|p| p.ty.clone()).collect(),
            Box::new(ret.clone()),
        );
        let capture = GoExpr::new(
            GoExprKind::FuncLit(params, ret.clone(), vec![GoStmt::Return(Some(value))]),
            ty,
        );
        GoExpr::new(GoExprKind::Call(Box::new(capture), args), ret)
    }

    pub(super) fn freeze_closure_context(&mut self, value: GoExpr, source: ExprId) -> GoExpr {
        let scope = self.numeric_scope.clone();
        self.freeze_closure_scope(value, &scope, source)
    }
}
