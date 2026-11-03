use super::*;
use crate::numeric::{
    NumberEvidence, NumericBinder, NumericElaboration, NumericParam, NumericShape,
};
use std::collections::HashSet;

#[derive(Default)]
pub(super) struct RawNumeric {
    pub reference: Option<ExprId>,
    pub owner: Option<LocalId>,
    pub parents: HashMap<LocalId, Option<LocalId>>,
    pub expr_owners: HashMap<ExprId, Option<LocalId>>,
    pub local_owners: HashMap<LocalId, Option<LocalId>>,
    pub top_roots: Vec<TyVarId>,
    pub local_roots: HashMap<LocalId, Vec<TyVarId>>,
    pub arguments: HashMap<ExprId, Vec<TyVarId>>,
    pub resolutions: HashMap<ExprId, Res>,
    pub params: Vec<TyVarId>,
}

impl Infer<'_> {
    pub(crate) fn with_numeric_recording(mut self) -> Self {
        self.numeric = Some(RawNumeric::default());
        self.record_exprs = true;
        self
    }

    fn numeric_param(&mut self, v: TyVarId, mut owner: Option<LocalId>) -> Option<NumericParam> {
        let root = self.uf.find(v);
        while let Some(local) = owner {
            let roots = self
                .numeric
                .as_ref()?
                .local_roots
                .get(&local)
                .cloned()
                .unwrap_or_default();
            for (index, r) in roots.into_iter().enumerate() {
                if self.uf.find(r) == root {
                    return Some(NumericParam {
                        binder: NumericBinder::Local(local),
                        index,
                    });
                }
            }
            owner = self
                .numeric
                .as_ref()?
                .parents
                .get(&local)
                .copied()
                .flatten();
        }
        let roots = self.numeric.as_ref()?.top_roots.clone();
        for (index, r) in roots.into_iter().enumerate() {
            if self.uf.find(r) == root {
                return Some(NumericParam {
                    binder: NumericBinder::Top,
                    index,
                });
            }
        }
        None
    }

    fn numeric_shape(
        &mut self,
        v: TyVarId,
        owner: Option<LocalId>,
        seen: &mut HashSet<TyVarId>,
    ) -> NumericShape {
        base::ensure_stack(|| {
            let root = self.uf.find(v);
            if let Some(p) = self.numeric_param(root, owner) {
                return NumericShape::Number(p);
            }
            if !seen.insert(root) {
                return NumericShape::None;
            }
            let shape = match self.uf.content(root) {
                Content::Structure(FlatTy::App(ref n, ref args))
                    if n.as_str() == "Float" && args.is_empty() =>
                {
                    NumericShape::Float
                }
                Content::Structure(FlatTy::Fun(a, b)) => NumericShape::Fun(
                    Box::new(self.numeric_shape(a, owner, seen)),
                    Box::new(self.numeric_shape(b, owner, seen)),
                ),
                Content::Structure(FlatTy::App(n, args)) => NumericShape::App(
                    n,
                    args.into_iter()
                        .map(|a| self.numeric_shape(a, owner, seen))
                        .collect(),
                ),
                Content::Structure(FlatTy::Tuple(args)) => NumericShape::Tuple(
                    args.into_iter()
                        .map(|a| self.numeric_shape(a, owner, seen))
                        .collect(),
                ),
                Content::Structure(FlatTy::Record(fields, _)) => NumericShape::Record(
                    fields
                        .into_iter()
                        .map(|(n, a)| (n, self.numeric_shape(a, owner, seen)))
                        .collect(),
                ),
                _ => NumericShape::None,
            };
            seen.remove(&root);
            let empty = match &shape {
                NumericShape::Fun(a, b) => a.is_empty() && b.is_empty(),
                NumericShape::App(_, xs) | NumericShape::Tuple(xs) => {
                    xs.iter().all(NumericShape::is_empty)
                }
                NumericShape::Record(fs) => fs.iter().all(|(_, s)| s.is_empty()),
                _ => shape.is_empty(),
            };
            if empty {
                NumericShape::None
            } else {
                shape
            }
        })
    }

    fn number_evidence(&mut self, v: TyVarId, owner: Option<LocalId>) -> NumberEvidence {
        if let Some(p) = self.numeric_param(v, owner) {
            return NumberEvidence::Param(p);
        }
        match self.uf.content(v) {
            Content::Structure(FlatTy::App(n, _)) if n.as_str() == "Float" => NumberEvidence::Float,
            _ => NumberEvidence::Int,
        }
    }

    pub(crate) fn finish_numeric(&mut self, body: &Body) -> NumericElaboration {
        let Some(raw) = self.numeric.as_ref() else {
            return NumericElaboration::default();
        };
        let mut out = NumericElaboration {
            top_params: raw.top_roots.len(),
            local_params: raw
                .local_roots
                .iter()
                .filter(|(_, v)| !v.is_empty())
                .map(|(k, v)| (*k, v.len()))
                .collect(),
            errors: self.errors.iter().map(|e| e.message.clone()).collect(),
            ..NumericElaboration::default()
        };
        for (e, v) in self.expr_vars.clone() {
            let owner = self
                .numeric
                .as_ref()
                .unwrap()
                .expr_owners
                .get(&e)
                .copied()
                .flatten();
            let shape = self.numeric_shape(v, owner, &mut HashSet::new());
            if !shape.is_empty() {
                out.expressions.insert(e, shape);
            }
        }
        for (l, v) in self.local_vars.clone() {
            let owner = self
                .numeric
                .as_ref()
                .unwrap()
                .local_owners
                .get(&l)
                .copied()
                .flatten();
            let shape = self.numeric_shape(v, owner, &mut HashSet::new());
            if !shape.is_empty() {
                out.locals.insert(l, shape);
            }
        }
        if let Some(rv) = body.root.and_then(|root| {
            self.expr_vars
                .iter()
                .rev()
                .find(|(e, _)| *e == root)
                .map(|(_, v)| *v)
        }) {
            let params = self.numeric.as_ref().unwrap().params.clone();
            let full = params
                .into_iter()
                .rev()
                .fold(rv, |rest, p| self.fun(p, rest));
            out.signature = self.numeric_shape(full, None, &mut HashSet::new());
        }
        let refs = self.numeric.as_ref().unwrap().resolutions.clone();
        for (e, res) in refs {
            let owner = self
                .numeric
                .as_ref()
                .unwrap()
                .expr_owners
                .get(&e)
                .copied()
                .flatten();
            let mut args = self
                .numeric
                .as_ref()
                .unwrap()
                .arguments
                .get(&e)
                .cloned()
                .unwrap_or_default();
            // Recursive local references are monomorphic inside their SCC,
            // before the group's quantifiers are generalised.
            if args.is_empty() {
                if let Res::Local(l) = res {
                    args = self
                        .numeric
                        .as_ref()
                        .unwrap()
                        .local_roots
                        .get(&l)
                        .cloned()
                        .unwrap_or_default();
                }
                if let Res::Def(def) = res {
                    if self.self_def == Some(def) {
                        args = self.numeric.as_ref().unwrap().top_roots.clone();
                    }
                }
            }
            if !args.is_empty() {
                out.references.insert(
                    e,
                    args.into_iter()
                        .map(|v| self.number_evidence(v, owner))
                        .collect(),
                );
            }
        }
        out
    }

    pub(super) fn record_local_type(&mut self, local: LocalId, var: TyVarId) {
        self.local_vars.push((local, var));
        if let Some(raw) = &mut self.numeric {
            raw.local_owners.insert(local, raw.owner);
        }
    }
}
