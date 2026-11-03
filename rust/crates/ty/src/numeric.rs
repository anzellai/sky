//! Numeric dictionaries retained independently of the ordinary lowering table.
//! Solver variable IDs never escape the inference run.

use crate::{Scheme, SuperType, Ty, World};
use base::{DefId, Name};
use hir::{ExprId, LocalId};
use std::collections::HashMap;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum NumericBinder {
    Top,
    Local(LocalId),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct NumericParam {
    pub binder: NumericBinder,
    pub index: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumberEvidence {
    Int,
    Float,
    Param(NumericParam),
}

/// Only paths containing a quantified numeric leaf are retained. Everything
/// else keeps the existing lowering inference's representation.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub enum NumericShape {
    #[default]
    None,
    Number(NumericParam),
    Float,
    Fun(Box<NumericShape>, Box<NumericShape>),
    App(Name, Vec<NumericShape>),
    Tuple(Vec<NumericShape>),
    Record(Vec<(Name, NumericShape)>),
}

impl NumericShape {
    pub fn is_empty(&self) -> bool {
        matches!(self, Self::None)
    }
    pub fn parameter(&self, index: usize) -> Option<&Self> {
        let mut current = self;
        for _ in 0..index {
            let Self::Fun(_, rest) = current else {
                return None;
            };
            current = rest;
        }
        if let Self::Fun(arg, _) = current {
            Some(arg)
        } else {
            None
        }
    }
    pub fn result_after(&self, count: usize) -> Option<&Self> {
        let mut current = self;
        for _ in 0..count {
            let Self::Fun(_, rest) = current else {
                return None;
            };
            current = rest;
        }
        Some(current)
    }
    pub fn apply(&self, ty: &Ty) -> Ty {
        base::ensure_stack(|| match (self, ty) {
            (Self::Number(_), _) => Ty::var("__sky_numeric"),
            (Self::Float, Ty::App(n, args)) if n.as_str() == "Int" && args.is_empty() => {
                Ty::app("Float", vec![])
            }
            (Self::Fun(a, b), Ty::Fun(x, y)) => Ty::Fun(Box::new(a.apply(x)), Box::new(b.apply(y))),
            (Self::App(n, args), Ty::App(m, old)) if n == m && args.len() == old.len() => Ty::App(
                m.clone(),
                args.iter().zip(old).map(|(a, t)| a.apply(t)).collect(),
            ),
            (Self::Tuple(args), Ty::Tuple(old)) if args.len() == old.len() => {
                Ty::Tuple(args.iter().zip(old).map(|(a, t)| a.apply(t)).collect())
            }
            (Self::Record(fields), Ty::Record(old, row)) => Ty::Record(
                old.iter()
                    .map(|(n, t)| {
                        let t = fields
                            .iter()
                            .find(|(f, _)| f == n)
                            .map_or_else(|| t.clone(), |(_, s)| s.apply(t));
                        (n.clone(), t)
                    })
                    .collect(),
                row.clone(),
            ),
            _ => ty.clone(),
        })
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NumericElaboration {
    pub top_params: usize,
    pub local_params: HashMap<LocalId, usize>,
    pub references: HashMap<ExprId, Vec<NumberEvidence>>,
    pub expressions: HashMap<ExprId, NumericShape>,
    pub locals: HashMap<LocalId, NumericShape>,
    pub signature: NumericShape,
    pub errors: Vec<String>,
}

pub(crate) fn numeric_name(name: &str) -> bool {
    SuperType::from_var_name(name).is_some_and(|b| b.has(SuperType::Number))
}

pub(crate) fn numeric_scheme(world: &World, def: DefId) -> Option<&Scheme> {
    world
        .bound_check_sigs
        .get(&def)
        .or_else(|| world.any_result_check_sigs.get(&def))
        .or_else(|| world.value_sigs.get(&def))
        .or_else(|| world.check_sigs.get(&def))
        .or_else(|| world.app_check_sigs.get(&def))
}

pub fn compute_numeric_elaboration(
    world: &World,
    db: &dyn hir::SkyDb,
    module: base::ModuleId,
    def: DefId,
) -> NumericElaboration {
    let resolved = db.resolve(module);
    let Some(body) = resolved.bodies.get(&def) else {
        return NumericElaboration::default();
    };
    let scheme = numeric_scheme(world, def);
    let has_number = |s: &Scheme| s.vars.iter().any(|n| numeric_name(n.as_str()));
    let relevant = scheme.is_some_and(has_number)
        || body.exprs.iter().any(|(_, expr)| match expr {
            hir::Expr::Var(hir::Res::Def(d)) => numeric_scheme(world, *d).is_some_and(has_number),
            hir::Expr::Let { defs, .. } => defs.iter().any(|d| {
                d.anno
                    .as_ref()
                    .and_then(|a| world.let_annotation_ty(db, module, a.span))
                    .is_some_and(|ty| ty.free_vars().iter().any(|n| numeric_name(n.as_str())))
            }),
            _ => false,
        });
    if !relevant {
        return NumericElaboration::default();
    }
    let mut infer = crate::infer::Infer::new(world, db)
        .with_self_def(Some(def))
        .with_numeric_recording();
    if let Some(scheme) = scheme {
        infer.infer_def_against(body, scheme)
    } else {
        infer.infer_def_scheme(body, false);
    }
    infer.finish_numeric(body)
}
