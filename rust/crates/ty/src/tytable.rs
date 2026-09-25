//! A hash-consed store of read-back [`Ty`] values — the compact backing of a
//! def's per-expression / per-local type table ([`crate::BodyTypes`]).
//!
//! # Why it exists
//!
//! [`Ty`] is an owned tree: an alias is expanded structurally and a record
//! carries every field inline. The typed table records one `Ty` per expression,
//! so a body whose expressions share large sub-types stores each copy again.
//! The pathological shape is a `Codec.object Ctor |> Codec.field … |> …`
//! pipeline over an N-field record: every step's type is an N-ary constructor
//! arrow over the fully expanded field types, and the pipeline has O(N) steps,
//! so the table grows as O(N × size(record)). A generated Sky.Spa `Shared`
//! module has one such pipeline per wire type. Measured on a real app's backend
//! leg: 35.8 M `Ty` nodes across 3,906 def tables, one codec def alone 645,098
//! nodes of which only 595 are distinct. The whole-program lowerer holds every
//! table at once (and the salsa `infer` memo holds them too), so the leg peaked
//! above 6 GB.
//!
//! Interning each distinct sub-tree once makes a table proportional to the
//! number of DISTINCT types in the body, not to the sum of their sizes. A read
//! rebuilds an owned `Ty` on demand, so every consumer still receives exactly
//! the value that was stored: the representation changes, the values do not.

use crate::Ty;
use base::Name;
use rustc_hash::FxHashMap;

/// An index into a [`TyTable`].
#[derive(Copy, Clone, PartialEq, Eq, Hash, Debug)]
pub struct TyRef(u32);

/// One interned node. Children are [`TyRef`]s into the same table, so a shared
/// sub-tree is stored once.
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
enum TyNode {
    Var(Name),
    Fun(TyRef, TyRef),
    App(Name, Box<[TyRef]>),
    Record(Box<[(Name, TyRef)]>, Option<Name>),
    Tuple(Box<[TyRef]>),
    Unit,
    Error,
}

/// A frozen, hash-consed set of types. Built with [`TyTableBuilder`].
#[derive(Default, Clone, Debug)]
pub struct TyTable {
    nodes: Vec<TyNode>,
}

impl TyTable {
    /// Rebuild the owned type stored at `r`.
    pub fn get(&self, r: TyRef) -> Ty {
        match &self.nodes[r.0 as usize] {
            TyNode::Var(n) => Ty::Var(n.clone()),
            TyNode::Fun(a, b) => Ty::Fun(Box::new(self.get(*a)), Box::new(self.get(*b))),
            TyNode::App(n, xs) => Ty::App(n.clone(), xs.iter().map(|x| self.get(*x)).collect()),
            TyNode::Record(fs, ext) => Ty::Record(
                fs.iter().map(|(n, x)| (n.clone(), self.get(*x))).collect(),
                ext.clone(),
            ),
            TyNode::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| self.get(*x)).collect()),
            TyNode::Unit => Ty::Unit,
            TyNode::Error => Ty::Error,
        }
    }

    /// The head constructor name when the type at `r` is a nominal
    /// application (`Ty::App(name, _)`), without rebuilding its arguments.
    pub fn app_name(&self, r: TyRef) -> Option<&Name> {
        match &self.nodes[r.0 as usize] {
            TyNode::App(n, _) => Some(n),
            _ => None,
        }
    }

    /// Number of distinct nodes stored — the table's size measure.
    pub fn node_count(&self) -> usize {
        self.nodes.len()
    }
}

/// Builds a [`TyTable`], sharing every structurally equal sub-tree.
#[derive(Default)]
pub struct TyTableBuilder {
    nodes: Vec<TyNode>,
    // Fx, not SipHash: keys are small compiler-internal nodes (no untrusted
    // input to defend against), and hashing is this builder's hot path.
    index: FxHashMap<TyNode, TyRef>,
}

impl TyTableBuilder {
    pub fn new() -> Self {
        Self::default()
    }

    /// Intern `t` (and every sub-tree of it) and return its reference.
    pub fn intern(&mut self, t: &Ty) -> TyRef {
        let node = match t {
            Ty::Var(n) => TyNode::Var(n.clone()),
            Ty::Fun(a, b) => {
                let a = self.intern(a);
                let b = self.intern(b);
                TyNode::Fun(a, b)
            }
            Ty::App(n, xs) => TyNode::App(n.clone(), xs.iter().map(|x| self.intern(x)).collect()),
            Ty::Record(fs, ext) => TyNode::Record(
                fs.iter()
                    .map(|(n, x)| (n.clone(), self.intern(x)))
                    .collect(),
                ext.clone(),
            ),
            Ty::Tuple(xs) => TyNode::Tuple(xs.iter().map(|x| self.intern(x)).collect()),
            Ty::Unit => TyNode::Unit,
            Ty::Error => TyNode::Error,
        };
        if let Some(r) = self.index.get(&node) {
            return *r;
        }
        let r = TyRef(u32::try_from(self.nodes.len()).expect("fewer than 2^32 distinct types"));
        self.nodes.push(node.clone());
        self.index.insert(node, r);
        r
    }

    /// Freeze the table. The dedup index is dropped; only the nodes are kept.
    pub fn finish(self) -> TyTable {
        let mut nodes = self.nodes;
        nodes.shrink_to_fit();
        TyTable { nodes }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(fields: &[(&str, Ty)]) -> Ty {
        Ty::Record(
            fields
                .iter()
                .map(|(n, t)| (Name::new(n), t.clone()))
                .collect(),
            None,
        )
    }

    fn sample_types() -> Vec<Ty> {
        let int = Ty::app("Int", vec![]);
        let s = Ty::app("String", vec![]);
        let row = rec(&[("id", s.clone()), ("n", int.clone())]);
        vec![
            Ty::var("a"),
            Ty::Unit,
            Ty::Error,
            Ty::Fun(Box::new(int.clone()), Box::new(s.clone())),
            Ty::app("List", vec![row.clone()]),
            Ty::Tuple(vec![int.clone(), row.clone(), Ty::Unit]),
            Ty::Record(vec![(Name::new("x"), int.clone())], Some(Name::new("r"))),
            Ty::app("Dict", vec![s, Ty::app("Maybe", vec![row])]),
        ]
    }

    #[test]
    fn round_trips_every_shape() {
        let mut b = TyTableBuilder::new();
        let tys = sample_types();
        let refs: Vec<TyRef> = tys.iter().map(|t| b.intern(t)).collect();
        let table = b.finish();
        for (t, r) in tys.iter().zip(refs) {
            assert_eq!(&table.get(r), t);
        }
    }

    #[test]
    fn equal_subtrees_are_stored_once() {
        // N copies of a big record type, and N arrows over it: without sharing
        // this is O(N × size); with it the node count stays O(size + N).
        let big = rec(&(0..50)
            .map(|i| {
                (
                    format!("f{i}"),
                    Ty::app("List", vec![Ty::app("Int", vec![])]),
                )
            })
            .collect::<Vec<_>>()
            .iter()
            .map(|(n, t)| (n.as_str(), t.clone()))
            .collect::<Vec<_>>());
        let mut b = TyTableBuilder::new();
        let mut arrow = big.clone();
        let mut refs = Vec::new();
        for _ in 0..200 {
            arrow = Ty::Fun(Box::new(big.clone()), Box::new(arrow));
            refs.push((b.intern(&arrow), arrow.clone()));
        }
        let table = b.finish();
        // Int, List Int, Name-distinct record (1), and 200 arrows.
        assert!(table.node_count() <= 3 + 200 + 1, "{}", table.node_count());
        for (r, t) in refs {
            assert_eq!(table.get(r), t);
        }
    }

    #[test]
    fn app_name_reads_the_head_only() {
        let mut b = TyTableBuilder::new();
        let r = b.intern(&Ty::app("Maybe", vec![Ty::var("a")]));
        let v = b.intern(&Ty::var("a"));
        let t = b.finish();
        assert_eq!(t.app_name(r).map(Name::as_str), Some("Maybe"));
        assert_eq!(t.app_name(v), None);
    }
}
