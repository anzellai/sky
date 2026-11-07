//! Private immutable ground constructor descriptors. Node indices are NOT UF
//! IDs. Instance origin is separate layout/ownership metadata on FrozenRef.
use super::{Content, FlatTy};
use crate::{Ty, TyVarId};
use base::Name;
use std::collections::{BTreeMap, HashSet};
use std::sync::Arc;

#[derive(Debug)]
pub(super) enum GroundNode {
    App(Name, Vec<usize>),
    Fun(usize, usize),
    Record(Vec<(Name, usize)>),
    Tuple(Vec<usize>),
    Unit,
}
#[derive(Debug)]
pub(crate) struct GroundShape {
    pub(super) nodes: Vec<GroundNode>,
    pub(super) parents: Vec<Option<usize>>,
}
impl GroundShape {
    pub(crate) fn constructor(ty: &Ty) -> Option<Arc<Self>> {
        if !matches!(ty, Ty::Fun(..)) {
            return None;
        }
        let mut shape = Self {
            nodes: Vec::new(),
            parents: Vec::new(),
        };
        shape.add(ty)?;
        Some(Arc::new(shape))
    }
    fn add(&mut self, ty: &Ty) -> Option<usize> {
        base::ensure_stack(|| {
            let node = match ty {
                Ty::Var(_) | Ty::Error | Ty::Record(_, Some(_)) => return None,
                Ty::App(name, args) => GroundNode::App(
                    name.clone(),
                    args.iter()
                        .map(|t| self.add(t))
                        .collect::<Option<Vec<_>>>()?,
                ),
                Ty::Fun(a, b) => GroundNode::Fun(self.add(a)?, self.add(b)?),
                Ty::Record(fields, None) => {
                    let mut seen = HashSet::new();
                    let mut result = Vec::with_capacity(fields.len());
                    for (name, ty) in fields {
                        if !seen.insert(name) {
                            return None;
                        }
                        result.push((name.clone(), self.add(ty)?));
                    }
                    GroundNode::Record(result)
                }
                Ty::Tuple(xs) => {
                    GroundNode::Tuple(xs.iter().map(|t| self.add(t)).collect::<Option<Vec<_>>>()?)
                }
                Ty::Unit => GroundNode::Unit,
            };
            let id = self.nodes.len();
            match &node {
                GroundNode::App(_, xs) | GroundNode::Tuple(xs) => {
                    for &x in xs {
                        self.parents[x] = Some(id)
                    }
                }
                GroundNode::Fun(a, b) => {
                    self.parents[*a] = Some(id);
                    self.parents[*b] = Some(id)
                }
                GroundNode::Record(xs) => {
                    for (_, x) in xs {
                        self.parents[*x] = Some(id)
                    }
                }
                GroundNode::Unit => {}
            }
            self.nodes.push(node);
            self.parents.push(None);
            Some(id)
        })
    }
}
#[derive(Clone)]
pub(super) struct FrozenRef {
    pub(super) shape: Arc<GroundShape>,
    pub(super) origin: TyVarId,
    pub(super) node: usize,
}
impl std::fmt::Debug for FrozenRef {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("FrozenRef")
            .field("origin", &self.origin)
            .field("node", &self.node)
            .finish()
    }
}
impl FrozenRef {
    pub(super) fn id(&self, node: usize) -> TyVarId {
        TyVarId(
            self.origin
                .0
                .checked_add(u32::try_from(node).expect("ground node limit"))
                .expect("ground ID limit"),
        )
    }
    pub(super) fn content(&self) -> Content {
        let flat = match &self.shape.nodes[self.node] {
            GroundNode::App(name, args) => {
                FlatTy::App(name.clone(), args.iter().map(|&n| self.id(n)).collect())
            }
            GroundNode::Fun(a, b) => FlatTy::Fun(self.id(*a), self.id(*b)),
            GroundNode::Record(fields) => FlatTy::Record(
                fields
                    .iter()
                    .map(|(name, node)| (name.clone(), self.id(*node)))
                    .collect::<BTreeMap<_, _>>(),
                None,
            ),
            GroundNode::Tuple(xs) => FlatTy::Tuple(xs.iter().map(|&n| self.id(n)).collect()),
            GroundNode::Unit => FlatTy::Unit,
        };
        Content::Structure(flat)
    }
}
