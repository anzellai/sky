//! The arena union-find — the L3 centrepiece (doc 06 §"The arena union-find").
//!
//! Type-variable identity stops being a pointer (`UF.Point`, `UnionFind.hs:61`)
//! and becomes a dense integer index [`TyVarId`] into a `Vec` local to one
//! inference run (never a global — L1). `find` is path-compressed; `union` is by
//! rank; the occurs-check is a `HashSet<TyVarId>` of representatives.
//!
//! `Content` mirrors `Type.hs:51` **minus** the dead `_rank/_mark/_copy`
//! elm/compiler let-generalisation-pool machinery (doc 06 §"What we delete").
//! Rigid + Alias content are folded away here: aliases are expanded eagerly in
//! [`crate::sig`], and rigid annotation vars are instantiated flexibly (leniency
//! that only ever *accepts more* — safe under the accept-parity gate).

use crate::TyVarId;
use base::Name;
use std::collections::BTreeMap;

/// The four built-in super-vars (`Type.hs:62`). NOT typeclasses (doc 03 §5).
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum SuperType {
    Number,
    Comparable,
    Appendable,
    CompAppend,
}

/// The descriptor a union-find root carries.
#[derive(Clone, Debug)]
pub enum Content {
    /// An unbound inference variable (`FlexVar`).
    Flex,
    /// A super-constrained flex var (`Number`/`Comparable`/…).
    FlexSuper(SuperType),
    /// A resolved concrete shape.
    Structure(FlatTy),
    /// L7 recovery sentinel — unifies with anything, suppresses cascades.
    Error,
}

/// `Type.hs:71` FlatType, over [`TyVarId`] instead of `Variable`.
#[derive(Clone, Debug)]
pub enum FlatTy {
    /// Nominal application `Name a b …` (home folded into the name for
    /// accept-parity — see the report's honesty note).
    App(Name, Vec<TyVarId>),
    Fun(TyVarId, TyVarId),
    /// Row-polymorphic record: sorted fields + optional extension var.
    /// `ext = None` ⇒ closed; `ext = Some(v)` ⇒ open under row var `v`.
    Record(BTreeMap<Name, TyVarId>, Option<TyVarId>),
    Tuple(Vec<TyVarId>),
    Unit,
}

#[derive(Clone, Debug)]
enum Slot {
    Root { content: Content, rank: u32 },
    Link(TyVarId),
}

/// A unification mismatch — a value, never an exception (L7). Read back into a
/// diagnostic by the caller.
#[derive(Clone, Debug)]
pub struct Mismatch {
    pub message: String,
}

impl Mismatch {
    fn new(msg: impl Into<String>) -> Self {
        Mismatch {
            message: msg.into(),
        }
    }
}

/// The union-find store. Local to ONE inference run; never global (L1).
#[derive(Default)]
pub struct UnionFind {
    slots: Vec<Slot>,
    /// When set, record unification treats a closed side that lacks the other's
    /// fields as OK (skips the extra/missing-field error branches) but STILL
    /// unifies shared fields (so wrong-field-type clashes survive). The
    /// annotation gate ([`crate::infer::Infer::infer_def_against`]) flips this on
    /// only while unifying a body result against its declared type, where
    /// exact field-presence parity would false-positive on row-polymorphic TEA
    /// code the full-HM oracle accepts (accept-parity, 19-skyforum). Field-type
    /// and non-record-vs-record clashes are unaffected. Default `false`.
    pub lenient_record_presence: bool,
}

impl UnionFind {
    pub fn new() -> Self {
        UnionFind {
            slots: Vec::new(),
            lenient_record_presence: false,
        }
    }

    /// Allocate a fresh variable with the given content. Deterministic (L4):
    /// the id is the next dense index, drawn in constraint-generation order.
    pub fn fresh(&mut self, content: Content) -> TyVarId {
        let id = TyVarId(self.slots.len() as u32);
        self.slots.push(Slot::Root { content, rank: 0 });
        id
    }

    #[inline]
    pub fn fresh_flex(&mut self) -> TyVarId {
        self.fresh(Content::Flex)
    }

    /// `repr` (`UnionFind.hs:47`) — path compression, by index.
    pub fn find(&mut self, mut v: TyVarId) -> TyVarId {
        let root = {
            let mut r = v;
            while let Slot::Link(p) = self.slots[r.0 as usize] {
                r = p;
            }
            r
        };
        while let Slot::Link(p) = self.slots[v.0 as usize] {
            self.slots[v.0 as usize] = Slot::Link(root);
            v = p;
        }
        root
    }

    fn rank(&self, root: TyVarId) -> u32 {
        match &self.slots[root.0 as usize] {
            Slot::Root { rank, .. } => *rank,
            Slot::Link(_) => 0,
        }
    }

    /// Content of `v`'s representative (cloned — cheap, ids inside).
    pub fn content(&mut self, v: TyVarId) -> Content {
        let r = self.find(v);
        match &self.slots[r.0 as usize] {
            Slot::Root { content, .. } => content.clone(),
            Slot::Link(_) => Content::Flex,
        }
    }

    fn set_content(&mut self, v: TyVarId, content: Content) {
        let r = self.find(v);
        if let Slot::Root { content: c, .. } = &mut self.slots[r.0 as usize] {
            *c = content;
        }
    }

    /// `union` by rank, carrying merged content (`UnionFind.hs:106`).
    fn union(&mut self, a: TyVarId, b: TyVarId, content: Content) {
        let (ra, rb) = (self.find(a), self.find(b));
        if ra == rb {
            self.set_content(ra, content);
            return;
        }
        let (rank_a, rank_b) = (self.rank(ra), self.rank(rb));
        let (root, child, mut rank) = if rank_a >= rank_b {
            (ra, rb, rank_a)
        } else {
            (rb, ra, rank_b)
        };
        if rank_a == rank_b {
            rank += 1;
        }
        self.slots[child.0 as usize] = Slot::Link(root);
        self.slots[root.0 as usize] = Slot::Root { content, rank };
    }

    // ---- occurs check (Occurs.hs) ---------------------------------------

    /// Does `target`'s representative appear inside the structure reachable
    /// from `v` (other than at `v == target`)? Gates the Flex↔Structure merge
    /// that would otherwise build an infinite type.
    fn occurs(&mut self, v: TyVarId, target: TyVarId) -> bool {
        let target = self.find(target);
        let mut seen = std::collections::HashSet::new();
        self.occurs_in(v, target, &mut seen)
    }

    fn occurs_in(
        &mut self,
        v: TyVarId,
        target: TyVarId,
        seen: &mut std::collections::HashSet<TyVarId>,
    ) -> bool {
        let r = self.find(v);
        if r == target {
            return true;
        }
        if !seen.insert(r) {
            return false;
        }
        match self.content(r) {
            Content::Structure(ft) => {
                let kids = flat_children(&ft);
                kids.into_iter().any(|k| self.occurs_in(k, target, seen))
            }
            _ => false,
        }
    }

    // ---- unification (Unify.hs actuallyUnify) ---------------------------

    /// Unify `a` and `b`. Returns `Err(Mismatch)` on a genuine clash — the
    /// caller records a diagnostic and commits both vars to `Error` (L7).
    pub fn unify(&mut self, a: TyVarId, b: TyVarId) -> Result<(), Mismatch> {
        let ra = self.find(a);
        let rb = self.find(b);
        if ra == rb {
            return Ok(());
        }
        let ca = self.content(ra);
        let cb = self.content(rb);
        match (ca, cb) {
            (Content::Error, _) | (_, Content::Error) => {
                self.union(ra, rb, Content::Error);
                Ok(())
            }
            (Content::Flex, Content::Flex) => {
                self.union(ra, rb, Content::Flex);
                Ok(())
            }
            (Content::Flex, other) => {
                if self.occurs(rb, ra) {
                    return self.infinite(ra, rb);
                }
                self.union(ra, rb, other);
                Ok(())
            }
            (other, Content::Flex) => {
                if self.occurs(ra, rb) {
                    return self.infinite(ra, rb);
                }
                self.union(ra, rb, other);
                Ok(())
            }
            (Content::FlexSuper(s1), Content::FlexSuper(s2)) => match combine_super(s1, s2) {
                Some(s) => {
                    self.union(ra, rb, Content::FlexSuper(s));
                    Ok(())
                }
                None => Err(Mismatch::new(format!(
                    "cannot unify {s1:?} with {s2:?}"
                ))),
            },
            (Content::FlexSuper(s), Content::Structure(ft))
            | (Content::Structure(ft), Content::FlexSuper(s)) => {
                if super_matches(s, &ft) {
                    self.union(ra, rb, Content::Structure(ft));
                    Ok(())
                } else {
                    Err(Mismatch::new(format!(
                        "{} is not a {s:?}",
                        flat_label(&ft)
                    )))
                }
            }
            (Content::Structure(f1), Content::Structure(f2)) => {
                self.unify_flat(ra, rb, f1, f2)
            }
        }
    }

    fn infinite(&mut self, a: TyVarId, b: TyVarId) -> Result<(), Mismatch> {
        self.union(a, b, Content::Error);
        Err(Mismatch::new("infinite type (occurs check)"))
    }

    fn unify_flat(
        &mut self,
        ra: TyVarId,
        rb: TyVarId,
        f1: FlatTy,
        f2: FlatTy,
    ) -> Result<(), Mismatch> {
        match (&f1, &f2) {
            (FlatTy::Unit, FlatTy::Unit) => {
                self.union(ra, rb, Content::Structure(FlatTy::Unit));
                Ok(())
            }
            (FlatTy::Fun(a1, r1), FlatTy::Fun(a2, r2)) => {
                let (a1, r1, a2, r2) = (*a1, *r1, *a2, *r2);
                self.union(ra, rb, Content::Structure(f1.clone()));
                self.unify(a1, a2)?;
                self.unify(r1, r2)
            }
            (FlatTy::Tuple(xs), FlatTy::Tuple(ys)) => {
                if xs.len() != ys.len() {
                    return Err(Mismatch::new(format!(
                        "tuple arity {} vs {}",
                        xs.len(),
                        ys.len()
                    )));
                }
                let pairs: Vec<(TyVarId, TyVarId)> =
                    xs.iter().copied().zip(ys.iter().copied()).collect();
                self.union(ra, rb, Content::Structure(f1.clone()));
                for (x, y) in pairs {
                    self.unify(x, y)?;
                }
                Ok(())
            }
            (FlatTy::App(n1, a1), FlatTy::App(n2, a2)) => {
                if n1 == n2 && a1.len() == a2.len() {
                    let pairs: Vec<(TyVarId, TyVarId)> =
                        a1.iter().copied().zip(a2.iter().copied()).collect();
                    self.union(ra, rb, Content::Structure(f1.clone()));
                    for (x, y) in pairs {
                        self.unify(x, y)?;
                    }
                    Ok(())
                } else {
                    Err(Mismatch::new(format!(
                        "type mismatch: `{}` vs `{}`",
                        n1.as_str(),
                        n2.as_str()
                    )))
                }
            }
            (FlatTy::Record(fs1, e1), FlatTy::Record(fs2, e2)) => {
                self.unify_records(ra, rb, fs1.clone(), *e1, fs2.clone(), *e2)
            }
            _ => Err(Mismatch::new(format!(
                "type mismatch: {} vs {}",
                flat_label(&f1),
                flat_label(&f2)
            ))),
        }
    }

    /// `unify_records` (`Unify.hs:468`) — the closed/open discipline.
    fn unify_records(
        &mut self,
        ra: TyVarId,
        rb: TyVarId,
        fs1: BTreeMap<Name, TyVarId>,
        e1: Option<TyVarId>,
        fs2: BTreeMap<Name, TyVarId>,
        e2: Option<TyVarId>,
    ) -> Result<(), Mismatch> {
        // shared fields unify pairwise
        let shared: Vec<(TyVarId, TyVarId)> = fs1
            .iter()
            .filter_map(|(k, v1)| fs2.get(k).map(|v2| (*v1, *v2)))
            .collect();
        let only1: BTreeMap<Name, TyVarId> = fs1
            .iter()
            .filter(|(k, _)| !fs2.contains_key(*k))
            .map(|(k, v)| (k.clone(), *v))
            .collect();
        let only2: BTreeMap<Name, TyVarId> = fs2
            .iter()
            .filter(|(k, _)| !fs1.contains_key(*k))
            .map(|(k, v)| (k.clone(), *v))
            .collect();

        // A closed side forbids the other side introducing extras it lacks.
        // (extras1Illegal / extras2Illegal, Unify.hs:486). Suppressed under the
        // annotation gate's presence-lenient mode — shared fields are still
        // unified below, so field-TYPE clashes survive.
        if !self.lenient_record_presence {
            if e1.is_none() && !only2.is_empty() {
                return Err(Mismatch::new(record_extra_msg(&only2)));
            }
            if e2.is_none() && !only1.is_empty() {
                return Err(Mismatch::new(record_extra_msg(&only1)));
            }
        }

        // resolved combined field set + extension
        let mut all = fs1.clone();
        for (k, v) in &fs2 {
            all.entry(k.clone()).or_insert(*v);
        }
        let ext = match (e1, e2) {
            (None, _) | (_, None) => None,
            (Some(_), Some(_)) => Some(self.fresh_flex()),
        };
        self.union(ra, rb, Content::Structure(FlatTy::Record(all, ext)));
        for (a, b) in shared {
            self.unify(a, b)?;
        }
        // connect an open side's row var to the extras it must absorb
        if let Some(row1) = e1 {
            if !only2.is_empty() {
                let rec = self.fresh(Content::Structure(FlatTy::Record(only2, ext)));
                let _ = self.unify(row1, rec);
            }
        }
        if let Some(row2) = e2 {
            if !only1.is_empty() {
                let rec = self.fresh(Content::Structure(FlatTy::Record(only1, ext)));
                let _ = self.unify(row2, rec);
            }
        }
        Ok(())
    }
}

fn flat_children(ft: &FlatTy) -> Vec<TyVarId> {
    match ft {
        FlatTy::App(_, args) => args.clone(),
        FlatTy::Fun(a, b) => vec![*a, *b],
        FlatTy::Record(fs, e) => fs.values().copied().chain(e.iter().copied()).collect(),
        FlatTy::Tuple(xs) => xs.clone(),
        FlatTy::Unit => Vec::new(),
    }
}

fn flat_label(ft: &FlatTy) -> String {
    match ft {
        FlatTy::App(n, _) => n.as_str().to_string(),
        FlatTy::Fun(_, _) => "function".to_string(),
        FlatTy::Record(_, _) => "record".to_string(),
        FlatTy::Tuple(_) => "tuple".to_string(),
        FlatTy::Unit => "()".to_string(),
    }
}

fn record_extra_msg(extra: &BTreeMap<Name, TyVarId>) -> String {
    let names: Vec<&str> = extra.keys().map(Name::as_str).collect();
    format!("record is missing field(s): {}", names.join(", "))
}

/// `combineSuper` (`Unify.hs:546`). CompAppend is the meet of Comparable and
/// Appendable; Number meets only itself/Comparable-ish per Sky's tables.
fn combine_super(a: SuperType, b: SuperType) -> Option<SuperType> {
    use SuperType::*;
    if a == b {
        return Some(a);
    }
    match (a, b) {
        (Number, Comparable) | (Comparable, Number) => Some(Number),
        (Comparable, Appendable) | (Appendable, Comparable) => Some(CompAppend),
        (CompAppend, Comparable) | (Comparable, CompAppend) => Some(CompAppend),
        (CompAppend, Appendable) | (Appendable, CompAppend) => Some(CompAppend),
        _ => None,
    }
}

/// `superMatches` (`Unify.hs:529`): does a concrete shape satisfy a super-var?
fn super_matches(s: SuperType, ft: &FlatTy) -> bool {
    let name = match ft {
        FlatTy::App(n, args) if args.is_empty() => Some(n.as_str()),
        FlatTy::App(n, _) => Some(n.as_str()),
        _ => None,
    };
    match s {
        SuperType::Number => matches!(name, Some("Int") | Some("Float")),
        SuperType::Comparable => {
            matches!(name, Some("Int") | Some("Float") | Some("String") | Some("Char"))
                || matches!(ft, FlatTy::App(n, _) if n.as_str() == "List")
                || matches!(ft, FlatTy::Tuple(_))
        }
        SuperType::Appendable => {
            matches!(name, Some("String")) || matches!(ft, FlatTy::App(n, _) if n.as_str() == "List")
        }
        SuperType::CompAppend => {
            matches!(name, Some("String")) || matches!(ft, FlatTy::App(n, _) if n.as_str() == "List")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unifies_flex_with_structure() {
        let mut uf = UnionFind::new();
        let a = uf.fresh_flex();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        assert!(uf.unify(a, int).is_ok());
        assert!(matches!(uf.content(a), Content::Structure(FlatTy::App(n, _)) if n.as_str() == "Int"));
    }

    #[test]
    fn rejects_mismatched_apps() {
        let mut uf = UnionFind::new();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let s = uf.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        assert!(uf.unify(int, s).is_err());
    }

    /// The v0.7 self-host killer guard (self-host §7 R1-D2): two DISTINCT
    /// nominal/opaque types must NOT unify. The legacy `Unify.sky:99-100`
    /// `isOpaqueFfiType a && isOpaqueFfiType b -> Ok emptySub` made every pair
    /// of unrelated FFI types unify (`Customer` ≡ `Widget`). This unifier has NO
    /// such rule — `App(n1,..)` unifies with `App(n2,..)` only when `n1 == n2`.
    #[test]
    fn distinct_nominal_types_do_not_unify() {
        let mut uf = UnionFind::new();
        let customer = uf.fresh(Content::Structure(FlatTy::App(Name::new("Customer"), vec![])));
        let widget = uf.fresh(Content::Structure(FlatTy::App(Name::new("Widget"), vec![])));
        assert!(
            uf.unify(customer, widget).is_err(),
            "distinct nominal types Customer and Widget must NOT unify (v0.7 hole)"
        );
    }

    #[test]
    fn occurs_check_rejects_infinite() {
        let mut uf = UnionFind::new();
        let a = uf.fresh_flex();
        let list_a = uf.fresh(Content::Structure(FlatTy::App(Name::new("List"), vec![a])));
        // a = List a  →  infinite
        assert!(uf.unify(a, list_a).is_err());
    }

    #[test]
    fn number_super_accepts_int_not_string() {
        let mut uf = UnionFind::new();
        let n = uf.fresh(Content::FlexSuper(SuperType::Number));
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        assert!(uf.unify(n, int).is_ok());
        let n2 = uf.fresh(Content::FlexSuper(SuperType::Number));
        let s = uf.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        assert!(uf.unify(n2, s).is_err());
    }

    #[test]
    fn closed_record_rejects_extra_field() {
        let mut uf = UnionFind::new();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let mut f1 = BTreeMap::new();
        f1.insert(Name::new("count"), int);
        let closed = uf.fresh(Content::Structure(FlatTy::Record(f1, None)));
        let int2 = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let mut f2 = BTreeMap::new();
        f2.insert(Name::new("count"), int2);
        f2.insert(Name::new("extra"), int2);
        let other = uf.fresh(Content::Structure(FlatTy::Record(f2, None)));
        assert!(uf.unify(closed, other).is_err());
    }

    #[test]
    fn open_record_accepts_superset() {
        let mut uf = UnionFind::new();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        // open {count: Int | rho}
        let row = uf.fresh_flex();
        let mut f1 = BTreeMap::new();
        f1.insert(Name::new("count"), int);
        let open = uf.fresh(Content::Structure(FlatTy::Record(f1, Some(row))));
        // closed {count: Int, name: String}
        let s = uf.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        let mut f2 = BTreeMap::new();
        f2.insert(Name::new("count"), int);
        f2.insert(Name::new("name"), s);
        let closed = uf.fresh(Content::Structure(FlatTy::Record(f2, None)));
        assert!(uf.unify(open, closed).is_ok());
    }
}
