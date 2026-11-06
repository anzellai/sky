//! The arena union-find — the L3 centrepiece (doc 06 §"The arena union-find").
//!
//! Derivative work adapted from elm/compiler's `Type.UnionFind`, `Type.Type`,
//! and `Type.Occurs` (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause).
//! See NOTICE.md at the repo root for the full attribution and licence text.
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
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, OnceLock,
};

/// The bounds a type variable can carry: Elm's four built-in super-vars
/// (`Type.hs:62`) plus `Encodable` (v0.27.0, B-1). NOT typeclasses (doc 03
/// §5): a fixed set, decided structurally.
///
/// A set of flags, closed upwards by [`SuperType::closure`]: a number is
/// comparable, and a comparable is encodable (every comparable shape is data
/// `Codec.auto` round-trips). `Number` and `Appendable` together are
/// unsatisfiable (no type is both).
///
/// **Qualified bounds (v0.27.0, C-11).** A bound survives generalisation by
/// its NAME: a scheme variable read back from a bounded variable is spelt
/// `<label><id>` (`comparable12`), and instantiation maps any variable whose
/// name starts with a label back to a bounded variable
/// ([`SuperType::from_var_name`]). So does a variable the user writes:
/// `largest : List comparable -> Maybe comparable`, as in Elm.
#[derive(Copy, Clone, PartialEq, Eq, Debug, Hash, Default)]
pub struct SuperType(u8);

#[allow(non_upper_case_globals)]
impl SuperType {
    /// `Int` or `Float`.
    pub const Number: SuperType = SuperType(1);
    /// Can be ordered (`<`, `compare`, `List.sort`, `Set`, …).
    pub const Comparable: SuperType = SuperType(2);
    /// `String` or `List a` (`++`).
    pub const Appendable: SuperType = SuperType(4);
    /// Comparable and appendable: `String` or `List comparable`.
    pub const CompAppend: SuperType = SuperType(2 | 4);
    /// Round-trips through `Codec.auto` (B-1): no function, `Secret`, key,
    /// crypto state or runtime handle anywhere inside.
    pub const Encodable: SuperType = SuperType(8);
    /// A Go value (`go@…`) or still undetermined: the type a Go FFI binding's
    /// non-empty interface parameter (`goi@…`) takes. A Sky value never
    /// implements a Go interface (`[E2013]`, v0.27.0).
    pub const GoValue: SuperType = SuperType(16);
    /// An integer LITERAL on the check path (v0.29.2). It is an `Int`,
    /// except that it may stand for a RIGID annotation variable that carries
    /// the `number` bound: `isPositive : number -> Bool; isPositive x = x > 0`
    /// (Elm's rule: a literal is a `number`). Anything else it meets turns it
    /// into `Int` first ([`UnionFind::default_int_literal`]), so every other
    /// program types exactly as when a literal was a concrete `Int` (`1 + 2.5`
    /// is still refused; a `Float` slot still needs `1.0`). One that nothing
    /// resolves reads back as `Int`.
    pub const IntLit: SuperType = SuperType(32);
    /// A `number` variable of a def's OWN annotation, on the LOWERING path
    /// only (v0.29.2). The lowering view infers a body without its
    /// annotation, so in `double : number -> number; double x = x * 2` the
    /// literal pinned `x` to `Int` and `double 2.5` truncated to `4`. When an
    /// annotation names a `number` variable, the body is inferred against it
    /// with this variable: it absorbs an `Int` (in a checked program only an
    /// integer literal can meet it, the checker refuses anything else), binds
    /// like a plain variable to any other type, and reads back as the type
    /// variable `number`, so the arithmetic lowers to the generic `rt.Mul`.
    pub const NumVar: SuperType = SuperType(64);

    pub fn is_empty(self) -> bool {
        self.0 == 0
    }
    pub fn has(self, o: SuperType) -> bool {
        self.0 & o.0 == o.0 && !o.is_empty()
    }
    pub fn union(self, o: SuperType) -> SuperType {
        SuperType(self.0 | o.0)
    }
    pub fn intersect(self, o: SuperType) -> SuperType {
        SuperType(self.0 & o.0)
    }
    pub fn minus(self, o: SuperType) -> SuperType {
        SuperType(self.0 & !o.0)
    }
    /// Close the set upwards: `IntLit ⇒ Number ⇒ Comparable ⇒ Encodable`.
    pub fn closure(self) -> SuperType {
        let mut b = self.0;
        if b & 32 != 0 {
            b |= 1;
        }
        if b & 1 != 0 {
            b |= 2;
        }
        if b & 2 != 0 {
            b |= 8;
        }
        SuperType(b)
    }
    /// Some type satisfies every bound in the set.
    pub fn is_satisfiable(self) -> bool {
        let orders = SuperType(1 | 2 | 4);
        !(self.has(SuperType::Number) && self.has(SuperType::Appendable))
            && !(self.has(SuperType::GoValue) && !self.intersect(orders).is_empty())
    }
    /// What the ELEMENTS of a container must satisfy for the container to
    /// satisfy `self` (a list is comparable when its elements are).
    pub fn for_elements(self) -> SuperType {
        self.closure()
            .intersect(SuperType::Comparable.union(SuperType::Encodable))
    }
    /// The variable-name prefix that spells this (closed) set.
    pub fn label(self) -> &'static str {
        let b = self.closure();
        if b.has(SuperType::GoValue) {
            "govalue"
        } else if b.has(SuperType::Number) {
            "number"
        } else if b.has(SuperType::CompAppend) {
            "compappend"
        } else if b.has(SuperType::Comparable) {
            "comparable"
        } else if b.has(SuperType::Appendable) && b.has(SuperType::Encodable) {
            "encappend"
        } else if b.has(SuperType::Appendable) {
            "appendable"
        } else {
            "encodable"
        }
    }
    /// The bound a variable NAME carries (Elm's rule: a prefix). `None` for
    /// an ordinary variable.
    pub fn from_var_name(name: &str) -> Option<SuperType> {
        const PREFIXES: [(&str, SuperType); 7] = [
            ("govalue", SuperType::GoValue),
            ("compappend", SuperType::CompAppend),
            ("comparable", SuperType::Comparable),
            ("appendable", SuperType::Appendable),
            ("encappend", SuperType(4 | 8)),
            ("encodable", SuperType::Encodable),
            ("number", SuperType::Number),
        ];
        PREFIXES
            .iter()
            .find(|(p, _)| name.starts_with(p))
            .map(|(_, b)| b.closure())
    }
    /// Is `name` a variable the unifier minted for a bounded variable
    /// (`comparable12`: a label followed by digits only)? Such a name is an
    /// inference artefact, like `t12`, and is shown as plain `comparable`.
    pub fn minted_label(name: &str) -> Option<&'static str> {
        [
            "govalue",
            "compappend",
            "comparable",
            "appendable",
            "encappend",
            "encodable",
            "number",
        ]
        .into_iter()
        .find(|p| {
            name.strip_prefix(p)
                .is_some_and(|rest| !rest.is_empty() && rest.chars().all(|c| c.is_ascii_digit()))
        })
    }
}

/// The descriptor a union-find root carries.
#[derive(Clone, Debug)]
pub enum Content {
    /// An unbound inference variable (`FlexVar`).
    Flex,
    /// A super-constrained flex var (`Number`/`Comparable`/…).
    FlexSuper(SuperType),
    /// A skolem — a rigid annotation quantifier (`Rigid(name)`). Introduced by
    /// the annotation gate ([`crate::infer::Infer::infer_def_against`]) when it
    /// skolemizes a declared scheme's quantifiers: a rigid BINDS a flex (so the
    /// body may stay polymorphic through it) but CLASHES with a concrete
    /// Structure or a different rigid (so a body that pins the quantifier to a
    /// concrete type is rejected — audit #5/#6).
    Rigid(Name),
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

/// One interval of intentionally unreachable logical IDs. No live UF edge,
/// obligation or recorded expression refers to an ID inside this interval.
#[derive(Clone, Debug)]
struct SkippedIds {
    start: usize,
    end: usize,
    before: usize,
}

/// Logical IDs retain the original allocation sequence; only live slots occupy
/// storage. Indexing always translates a logical ID, never silently treating a
/// reserved hole as a different live variable. Most inference runs have no holes.
#[derive(Default, Clone, Debug)]
struct Slots {
    dense: Vec<Slot>,
    skipped: Vec<SkippedIds>,
    logical_len: usize,
    skipped_len: usize,
}

impl Slots {
    fn len(&self) -> usize {
        self.logical_len
    }
    fn stored_len(&self) -> usize {
        self.dense.len()
    }

    fn push(&mut self, slot: Slot) {
        assert!(
            self.logical_len <= u32::MAX as usize,
            "type-variable ID limit exceeded"
        );
        let next = self
            .logical_len
            .checked_add(1)
            .expect("type-variable ID limit exceeded");
        self.dense.push(slot);
        self.logical_len = next;
    }

    fn reserve_unreachable_ids(&mut self, count: usize) {
        if count == 0 {
            return;
        }
        let end = self
            .logical_len
            .checked_add(count)
            .expect("type-variable ID limit exceeded");
        assert!(
            (end as u64) <= u32::MAX as u64 + 1,
            "type-variable ID limit exceeded"
        );
        match self.skipped.last_mut() {
            Some(last) if last.end == self.logical_len => last.end = end,
            _ => self.skipped.push(SkippedIds {
                start: self.logical_len,
                end,
                before: self.skipped_len,
            }),
        }
        self.skipped_len += count;
        self.logical_len = end;
    }

    fn dense_index(&self, id: usize) -> usize {
        assert!(id < self.logical_len, "type-variable ID out of range");
        let Some(first) = self.skipped.first() else {
            return id;
        };
        if id < first.start {
            return id;
        }
        let last = self.skipped.last().unwrap();
        if id >= last.end {
            return id - self.skipped_len;
        }
        let after = self.skipped.partition_point(|gap| gap.end <= id);
        assert!(
            id < self.skipped[after].start,
            "reserved unreachable type-variable ID was accessed"
        );
        if after == 0 {
            id
        } else {
            let previous = &self.skipped[after - 1];
            id - previous.before - (previous.end - previous.start)
        }
    }

    #[cfg(test)]
    fn as_slice(&self) -> &[Slot] {
        &self.dense
    }
}

impl std::ops::Index<usize> for Slots {
    type Output = Slot;
    fn index(&self, logical: usize) -> &Slot {
        &self.dense[self.dense_index(logical)]
    }
}
impl std::ops::IndexMut<usize> for Slots {
    fn index_mut(&mut self, logical: usize) -> &mut Slot {
        let dense = self.dense_index(logical);
        &mut self.dense[dense]
    }
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

/// Metadata-only capability state. A whole-UF snapshot is itself a latent
/// Record export: permanently disable authority in BOTH graph copies before
/// Clone returns. Never reactivate this flag on rollback or clear.
#[derive(Default)]
struct AccessorRowGrants {
    rows: HashSet<TyVarId>,
    snapshotted: OnceLock<Arc<AtomicBool>>,
}
impl Clone for AccessorRowGrants {
    fn clone(&self) -> Self {
        let flag = self
            .snapshotted
            .get_or_init(|| Arc::new(AtomicBool::new(false)));
        flag.store(true, Ordering::Release);
        Self {
            rows: HashSet::new(),
            snapshotted: OnceLock::from(flag.clone()),
        }
    }
}
impl AccessorRowGrants {
    fn disabled(&self) -> bool {
        self.snapshotted
            .get()
            .is_some_and(|flag| flag.load(Ordering::Acquire))
    }
    fn active(&self) -> bool {
        !self.rows.is_empty() && !self.disabled()
    }
    fn contains(&self, row: &TyVarId) -> bool {
        self.active() && self.rows.contains(row)
    }
    fn grant(&mut self, row: TyVarId) {
        if !self.disabled() {
            self.rows.insert(row);
        }
    }
    fn revoke(&mut self, row: TyVarId) {
        self.rows.remove(&row);
    }
    fn clear(&mut self) {
        self.rows = HashSet::new();
    }
}

/// The union-find store. Local to ONE inference run; never global (L1).
#[derive(Default, Clone)]
pub struct UnionFind {
    slots: Slots,
    accessor_rows: AccessorRowGrants,
    /// Bound obligations the unifier cannot decide on its own (v0.27.0,
    /// C-11/B-1): a bounded variable met a nominal type (a custom type, an
    /// opaque stdlib type) or a record. Whether a custom type is comparable
    /// depends on its constructors, which only the nominal environment knows,
    /// and whether a record's row is closed is known only after solving. The
    /// checker drains these and decides them (`crate::obligations`).
    pub(crate) pending: Vec<(SuperType, TyVarId)>,
    /// Bounds a RIGID (annotation) variable was asked for that its name does
    /// not carry but that the checker infers instead of refusing: only
    /// `Encodable` (B-1). `persist : a -> Codec a; persist b = Codec.auto b`
    /// exports `encodable -> Codec encodable`, so the bound is carried to its
    /// callers. A missing `Comparable`/`Number`/`Appendable` is refused
    /// (Elm's rule).
    pub(crate) rigid_needs: Vec<(Name, SuperType)>,
    /// Bounds a RIGID variable was asked for that its name does not carry and
    /// that the checker refuses (`Comparable`/`Number`/`Appendable`). The
    /// refusal is DEFERRED to the end of the definition
    /// (`Infer::check_annotation_bounds`) and made only when the body raised
    /// no other error: `show : a -> String; show v = v ++ ">"` first meets
    /// `++` (an appendable) and then `">"` (a `String`), and the right advice
    /// is "write `String`", not "write `appendable`" (v0.29.2).
    pub(crate) rigid_missing: Vec<(Name, SuperType)>,
    /// Every integer-literal variable made since the last
    /// [`UnionFind::default_all_int_literals`] (`SuperType::IntLit`).
    int_literals: Vec<TyVarId>,
    /// Reused buffers of the occurs check (no type state: see
    /// [`OccursScratch`]).
    occurs_scratch: OccursScratch,
    /// True while unifying a function's PARAMETER (contravariant position).
    /// The unifier reads its first side as the actual value and its second as
    /// the declared type. Under a function's parameter the roles swap:
    /// `unify(callee, arg -> result)` meets the declared parameter on the
    /// first side and the argument on the second. A record clash reads this
    /// to name the missing and the unknown fields from the right side. It
    /// changes only the wording of a refusal, never what unifies.
    contravariant: bool,
    /// Record fields copied out by [`UnionFind::content`] (tests only): pins
    /// that binding a variable moves a structure instead of copying it.
    #[cfg(test)]
    record_copies: u64,
}

/// The occurs check's visited marks and work stack, kept between calls so a
/// check allocates nothing. A root is visited in the current call when its
/// mark equals `epoch`; each call bumps `epoch`, which clears every mark at
/// once. It holds no type information: a clone starts empty, and two stores
/// that differ only here behave identically.
#[derive(Default)]
struct OccursScratch {
    mark: Vec<u32>,
    epoch: u32,
    stack: Vec<TyVarId>,
}

impl Clone for OccursScratch {
    fn clone(&self) -> Self {
        OccursScratch::default()
    }
}

/// How [`UnionFind::unify_vars`] first splits a pair of roots.
#[derive(Clone, Copy)]
enum Kind {
    Error,
    Flex,
    Other,
}

impl UnionFind {
    pub fn new() -> Self {
        UnionFind::default()
    }

    /// Number of variables allocated so far.
    #[cfg(test)]
    pub(crate) fn var_count(&self) -> usize {
        self.slots.len()
    }

    /// The new row never escapes its birth scope: only the complete function
    /// root is returned. No annotation, callback or owned content copy occurs
    /// between allocation and minting this singleton's authority.
    pub(crate) fn fresh_accessor(&mut self, field: Name) -> TyVarId {
        self.fresh_accessor_impl(field, true)
    }
    #[cfg(test)]
    pub(crate) fn fresh_accessor_reference(&mut self, field: Name) -> TyVarId {
        self.fresh_accessor_impl(field, false)
    }
    fn fresh_accessor_impl(&mut self, field: Name, grant: bool) -> TyVarId {
        let value = self.fresh_flex();
        let row = self.fresh_flex();
        let rec = self.fresh(Content::Structure(FlatTy::Record(
            BTreeMap::from([(field, value)]),
            Some(row),
        )));
        let function = self.fresh(Content::Structure(FlatTy::Fun(rec, value)));
        if grant {
            self.accessor_rows.grant(row);
        }
        function
    }
    pub(crate) fn revoke_accessor_rows(&mut self) {
        self.accessor_rows.clear();
    }
    fn revoke_accessor_root(&mut self, id: TyVarId) {
        if !self.accessor_rows.active() {
            return;
        }
        let root = self.find(id);
        self.accessor_rows.revoke(id);
        self.accessor_rows.revoke(root);
    }
    fn revoke_accessor_owner(&mut self, id: TyVarId) {
        if !self.accessor_rows.active() {
            return;
        }
        let root = self.find(id);
        let row = match &self.slots[root.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::Record(_, Some(row))),
                ..
            } => Some(*row),
            _ => None,
        };
        if let Some(row) = row {
            self.revoke_accessor_root(row);
        }
    }
    /// New direct structural edges revoke before publication. A granted row
    /// cannot be a link: every link/mutation of that representative revokes it.
    fn revoke_accessor_children(&mut self, content: &Content) {
        if !self.accessor_rows.active() {
            return;
        }
        let Content::Structure(flat) = content else {
            return;
        };
        let rows = &mut self.accessor_rows;
        match flat {
            FlatTy::App(_, xs) | FlatTy::Tuple(xs) => {
                for x in xs {
                    rows.revoke(*x);
                }
            }
            FlatTy::Fun(a, b) => {
                rows.revoke(*a);
                rows.revoke(*b);
            }
            FlatTy::Record(fs, ext) => {
                for x in fs.values() {
                    rows.revoke(*x);
                }
                if let Some(ext) = ext {
                    rows.revoke(*ext);
                }
            }
            FlatTy::Unit => {}
        }
    }
    #[cfg(test)]
    pub(crate) fn record_copies_for_test(&self) -> u64 {
        self.record_copies
    }

    /// Allocate a fresh variable with the given content. Deterministic (L4):
    /// the id is the next logical index, drawn in constraint-generation order.
    pub fn fresh(&mut self, content: Content) -> TyVarId {
        assert!(
            self.slots.len() <= u32::MAX as usize,
            "type-variable ID limit exceeded"
        );
        self.slots
            .len()
            .checked_add(1)
            .expect("type-variable ID limit exceeded");
        self.revoke_accessor_children(&content);
        let id = TyVarId(self.slots.len() as u32);
        let is_int_literal = matches!(&content, Content::FlexSuper(s) if s.has(SuperType::IntLit));
        self.slots.push(Slot::Root { content, rank: 0 });
        if is_int_literal {
            self.int_literals.push(id);
        }
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
        // Revoke BEFORE an owned Record/row copy can be held across later
        // unification, even when this reader is internal to the unifier.
        self.revoke_accessor_owner(r);
        match &self.slots[r.0 as usize] {
            Slot::Root { content, .. } => {
                if let Content::Structure(FlatTy::Record(fs, _)) = content {
                    crate::counters::RECORD_FIELDS.add(fs.len() as u64);
                    #[cfg(test)]
                    {
                        self.record_copies += fs.len() as u64;
                    }
                }
                content.clone()
            }
            Slot::Link(_) => Content::Flex,
        }
    }

    /// Read only an application's head without cloning its child IDs or a
    /// different shape's record fields. Finding the root may path-compress.
    pub(crate) fn app_name(&mut self, v: TyVarId) -> Option<&Name> {
        let root = self.find(v);
        match &self.slots[root.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::App(name, _)),
                ..
            } => Some(name),
            _ => None,
        }
    }

    /// Overwrite a root's content with no checks — builds the cyclic graphs
    /// the read-back tests need (the unifier only makes them through paths
    /// that are hard to drive from a test).
    #[cfg(test)]
    pub(crate) fn set_content_for_test(&mut self, v: TyVarId, content: Content) {
        self.set_content(v, content);
    }

    /// Which of the three cases [`Self::unify_vars`] splits on a ROOT's
    /// content falls in, read in place (no copy of the content).
    fn kind(&self, root: TyVarId) -> Kind {
        match &self.slots[root.0 as usize] {
            Slot::Root {
                content: Content::Error,
                ..
            } => Kind::Error,
            Slot::Root {
                content: Content::Flex,
                ..
            }
            | Slot::Link(_) => Kind::Flex,
            Slot::Root { .. } => Kind::Other,
        }
    }

    /// Move a ROOT's content out, leaving `Flex` behind. Only for a root that
    /// [`Self::union`] overwrites next, before anything reads it.
    fn take_content(&mut self, root: TyVarId) -> Content {
        self.revoke_accessor_root(root);
        match &mut self.slots[root.0 as usize] {
            Slot::Root { content, .. } => std::mem::replace(content, Content::Flex),
            Slot::Link(_) => Content::Flex,
        }
    }

    fn set_content(&mut self, v: TyVarId, content: Content) {
        self.revoke_accessor_owner(v);
        self.revoke_accessor_root(v);
        self.revoke_accessor_children(&content);
        self.set_moved_record_content(v, content);
    }

    /// Only the direct fresh Access path uses this: move already-published
    /// fields plus its new fresh value/rest, with no new raw row aliases.
    fn set_moved_record_content(&mut self, v: TyVarId, content: Content) {
        self.revoke_accessor_owner(v);
        self.revoke_accessor_root(v);
        let r = self.find(v);
        if let Slot::Root { content: c, .. } = &mut self.slots[r.0 as usize] {
            *c = content;
        }
    }

    /// `union` by rank, carrying merged content (`UnionFind.hs:106`).
    fn union(&mut self, a: TyVarId, b: TyVarId, content: Content) {
        self.revoke_accessor_owner(a);
        self.revoke_accessor_owner(b);
        self.union_moved_content(a, b, content);
    }

    /// The two Flex/Structure arms call this immediately after take_content:
    /// the same edge moves within one logical class, without an owned copy or
    /// intervening callback. Other union callers revoke owners first.
    fn union_moved_content(&mut self, a: TyVarId, b: TyVarId, content: Content) {
        self.revoke_accessor_root(a);
        self.revoke_accessor_root(b);
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
    ///
    /// A reachability question, so the walk order does not matter: it runs on
    /// an explicit stack and reads each node's children in place (no copy of a
    /// record's field map per node, no recursion depth bound by the type's).
    ///
    /// The visited set and the stack are the reused [`OccursScratch`]: a mark
    /// per variable, valid for one call through the epoch. The walk visits
    /// the same nodes in the same order as a fresh set per call would.
    fn occurs(&mut self, v: TyVarId, target: TyVarId) -> bool {
        let target = self.find(target);
        let mut sc = std::mem::take(&mut self.occurs_scratch);
        sc.epoch = sc.epoch.wrapping_add(1);
        if sc.epoch == 0 {
            // The epoch wrapped: old marks could equal a new one.
            sc.mark.iter_mut().for_each(|m| *m = 0);
            sc.epoch = 1;
        }
        // The walk allocates no variable, so marks sized now cover it.
        if sc.mark.len() < self.slots.stored_len() {
            sc.mark.resize(self.slots.stored_len(), 0);
        }
        sc.stack.clear();
        sc.stack.push(v);
        let mut found = false;
        while let Some(x) = sc.stack.pop() {
            crate::counters::OCCURS_NODES.bump();
            let r = self.find(x);
            if r == target {
                found = true;
                break;
            }
            let mark = &mut sc.mark[self.slots.dense_index(r.0 as usize)];
            if *mark == sc.epoch {
                continue;
            }
            *mark = sc.epoch;
            if let Slot::Root {
                content: Content::Structure(ft),
                ..
            } = &self.slots[r.0 as usize]
            {
                match ft {
                    FlatTy::App(_, args) => sc.stack.extend(args.iter().copied()),
                    FlatTy::Fun(a, b) => sc.stack.extend([*a, *b]),
                    FlatTy::Record(fs, e) => {
                        sc.stack.extend(fs.values().copied());
                        sc.stack.extend(e.iter().copied());
                    }
                    FlatTy::Tuple(xs) => sc.stack.extend(xs.iter().copied()),
                    FlatTy::Unit => {}
                }
            }
        }
        self.occurs_scratch = sc;
        found
    }

    // ---- unification (Unify.hs actuallyUnify) ---------------------------

    /// Unify `a` and `b`. Returns `Err(Mismatch)` on a genuine clash — the
    /// caller records a diagnostic and commits both vars to `Error` (L7).
    ///
    /// A curried function type is as deep as its arity (a record constructor
    /// with one argument per field), so the recursion through [`Self::unify_flat`]
    /// grows the stack on demand ([`base::ensure_stack`]).
    pub fn unify(&mut self, a: TyVarId, b: TyVarId) -> Result<(), Mismatch> {
        base::ensure_stack(|| self.unify_vars(a, b))
    }

    fn unify_vars(&mut self, a: TyVarId, b: TyVarId) -> Result<(), Mismatch> {
        self.revoke_accessor_root(a);
        self.revoke_accessor_root(b);
        let ra = self.find(a);
        let rb = self.find(b);
        if ra == rb {
            return Ok(());
        }
        // The `Error` and `Flex` cases need no copy of either side: they read
        // the two contents by kind only, and a `Flex` side's partner content
        // MOVES into the merged root (`union` overwrites both slots, so the
        // emptied one is never read). Binding a variable to a large record
        // used to clone the record's field map, and the same map again for
        // the `Flex` side's copy of nothing.
        match (self.kind(ra), self.kind(rb)) {
            (Kind::Error, _) | (_, Kind::Error) => {
                self.union(ra, rb, Content::Error);
                return Ok(());
            }
            (Kind::Flex, Kind::Flex) => {
                self.union(ra, rb, Content::Flex);
                return Ok(());
            }
            (Kind::Flex, Kind::Other) => {
                if self.occurs(rb, ra) {
                    return self.infinite(ra, rb);
                }
                let other = self.take_content(rb);
                self.union_moved_content(ra, rb, other);
                return Ok(());
            }
            (Kind::Other, Kind::Flex) => {
                if self.occurs(ra, rb) {
                    return self.infinite(ra, rb);
                }
                let other = self.take_content(ra);
                self.union_moved_content(ra, rb, other);
                return Ok(());
            }
            (Kind::Other, Kind::Other) => {}
        }
        // Consume before generic owned Record copies revoke the authority.
        if let Some(result) = self.unify_consumable_accessor_record(ra, rb) {
            return result;
        }
        // Both sides are rigid, bounded or structured: the arms below consume
        // owned copies (`unify_flat` / `satisfy` may read either root while
        // they run, so neither can be emptied first).
        let ca = self.content(ra);
        let cb = self.content(rb);
        match (ca, cb) {
            (Content::Error, _) | (_, Content::Error) | (Content::Flex, _) | (_, Content::Flex) => {
                unreachable!("Error and Flex roots are decided above")
            }
            // Two skolems unify only when they are the SAME quantifier
            // (`identity : a -> a` — the body's `x` and the declared result are
            // the same rigid `a`). Distinct skolems clash: the body makes two
            // variables one type (`coerce : a -> b; coerce x = x`). Every
            // quantifier of a top-level annotation is a skolem (v0.29.2).
            (Content::Rigid(n1), Content::Rigid(n2)) => {
                if n1 == n2 {
                    self.union(ra, rb, Content::Rigid(n1));
                    Ok(())
                } else {
                    Err(Mismatch::new(rigid_pair_message(n1.as_str(), n2.as_str())))
                }
            }
            // A skolem vs a BOUNDED flex (v0.27.0 qualified bounds): legal when
            // the skolem's own name carries the bound (`comparable` against
            // `<`), or when the only missing bound is `Encodable`, which the
            // checker infers and adds to the exported scheme. A missing
            // `Comparable`/`Number`/`Appendable` is Elm's "the annotation is
            // too general" error, worded with the fix.
            (Content::Rigid(n), Content::FlexSuper(s))
            | (Content::FlexSuper(s), Content::Rigid(n)) => {
                let have = SuperType::from_var_name(n.as_str()).unwrap_or_default();
                // An integer literal stands for a `number` variable (Elm's
                // rule). Against any other variable it is the `Int` it always
                // was, so the refusal names `Int`.
                if s.has(SuperType::IntLit) && !have.has(SuperType::Number) {
                    self.default_int_literal(ra);
                    self.default_int_literal(rb);
                    return self.unify_vars(ra, rb);
                }
                let s = s.minus(SuperType::IntLit);
                // A type variable given to a Go interface slot is left to the
                // wrapper's run-time assertion (`ffi_iface`): not a bound the
                // annotation must name.
                let missing = s.closure().minus(have.closure()).minus(SuperType::GoValue);
                if missing.is_empty() {
                    self.union(ra, rb, Content::Rigid(n));
                    Ok(())
                } else if missing == SuperType::Encodable {
                    self.rigid_needs.push((n.clone(), missing));
                    self.union(ra, rb, Content::Rigid(n));
                    Ok(())
                } else {
                    // Refused at the end of the definition, unless a clearer
                    // error (the body pins the variable to a type) comes first.
                    if missing.has(SuperType::Encodable) {
                        self.rigid_needs.push((n.clone(), SuperType::Encodable));
                    }
                    self.rigid_missing
                        .push((n.clone(), missing.minus(SuperType::Encodable)));
                    self.union(ra, rb, Content::Rigid(n));
                    Ok(())
                }
            }
            // A skolem vs ANYTHING concrete (Structure / FlexSuper) is a clash:
            // the body forced the declared quantifier to a specific type, so the
            // annotation is strictly more general than the body (audit #5/#6).
            // NOTE: this sits AFTER the Flex arms so a rigid still BINDS a plain
            // flex (the body may keep the quantifier polymorphic).
            (Content::Rigid(n), _) | (_, Content::Rigid(n)) => {
                let other = if matches!(self.content(ra), Content::Rigid(_)) {
                    rb
                } else {
                    ra
                };
                let shown = self.describe_advice(other);
                Err(Mismatch::new(rigid_concrete_message(n.as_str(), &shown)))
            }
            (Content::FlexSuper(s1), Content::FlexSuper(s2)) => {
                let s = s1.union(s2).closure();
                if s.is_satisfiable() {
                    self.union(ra, rb, Content::FlexSuper(s));
                    Ok(())
                } else if s.has(SuperType::IntLit) {
                    // A literal meets a bound no number has: it is the `Int`
                    // it always was, and the refusal names `Int`.
                    self.default_int_literal(ra);
                    self.default_int_literal(rb);
                    self.unify_vars(ra, rb)
                } else if s.has(SuperType::GoValue) {
                    Err(Mismatch::new(go_value_failure(
                        "a value that must be a number, appendable or comparable",
                    )))
                } else {
                    Err(Mismatch::new(
                        "no type is both a number and appendable (`++` needs a `String` or a `List`, \
                         arithmetic needs an `Int` or a `Float`)",
                    ))
                }
            }
            // Lowering only: an annotation's `number` variable absorbs the
            // `Int` of a literal and binds like a plain variable otherwise.
            (Content::FlexSuper(s), Content::Structure(ft))
            | (Content::Structure(ft), Content::FlexSuper(s))
                if s.has(SuperType::NumVar) =>
            {
                let is_int =
                    matches!(&ft, FlatTy::App(n, a) if n.as_str() == "Int" && a.is_empty());
                if is_int {
                    self.union(ra, rb, Content::FlexSuper(s));
                } else {
                    self.union(ra, rb, Content::Structure(ft));
                }
                Ok(())
            }
            // A literal meets a concrete type: it is an `Int`, and the
            // structural unify words any clash exactly as it always did.
            (Content::FlexSuper(s), Content::Structure(_))
            | (Content::Structure(_), Content::FlexSuper(s))
                if s.has(SuperType::IntLit) =>
            {
                self.default_int_literal(ra);
                self.default_int_literal(rb);
                self.unify_vars(ra, rb)
            }
            (Content::FlexSuper(s), Content::Structure(ft))
            | (Content::Structure(ft), Content::FlexSuper(s)) => self.satisfy(ra, rb, s, ft),
            (Content::Structure(f1), Content::Structure(f2)) => self.unify_flat(ra, rb, f1, f2),
        }
    }

    /// A bounded variable meets a concrete shape (`superMatches`, `Unify.hs:529`,
    /// made structural in v0.27.0). Containers pass the bound to their
    /// elements (a list is comparable when its elements are), so the check is
    /// complete for every shape the unifier can see. A nominal type other than
    /// the builtins, and a record (whose row is known closed only after
    /// solving), are recorded in [`UnionFind::pending`] for the checker.
    fn satisfy(
        &mut self,
        ra: TyVarId,
        rb: TyVarId,
        s: SuperType,
        ft: FlatTy,
    ) -> Result<(), Mismatch> {
        let s = s.closure();
        // A Go value: only a `go@` type (or a weak variable, one fixed type
        // chosen elsewhere) passes; every Sky shape fails. Any other bound on
        // the same variable is then decided as usual.
        if s.has(SuperType::GoValue) {
            let go = matches!(&ft, FlatTy::App(n, _)
                if crate::nominal::is_go_type(n.as_str())
                    || n.as_str().starts_with(crate::variance::WEAK_PREFIX));
            if !go {
                let what = self.describe_flat(&ft, 0);
                let shown = match &ft {
                    FlatTy::Fun(..) => format!("a function (`{what}`)"),
                    FlatTy::Record(..) => "a record".to_string(),
                    _ => format!("a Sky `{what}`"),
                };
                return Err(Mismatch::new(go_value_failure(&shown)));
            }
            let rest = s.minus(SuperType::GoValue);
            if rest.is_empty() {
                self.union(ra, rb, Content::Structure(ft));
                return Ok(());
            }
            return self.satisfy(ra, rb, rest, ft);
        }
        let elems = s.for_elements();
        let num = s.has(SuperType::Number);
        let app = s.has(SuperType::Appendable);
        let fail = |this: &mut Self, ft: &FlatTy| -> Result<(), Mismatch> {
            let what = this.describe_flat(ft, 0);
            Err(Mismatch::new(bound_failure(s, &what, ft)))
        };
        // Which children must carry `elems`, and whether the node itself is a
        // pending (nominal / record) obligation.
        let (children, pend): (Vec<TyVarId>, bool) = match &ft {
            FlatTy::Fun(..) => return fail(self, &ft),
            FlatTy::Unit => {
                if num || app {
                    return fail(self, &ft);
                }
                (Vec::new(), false)
            }
            FlatTy::Tuple(xs) => {
                if num || app {
                    return fail(self, &ft);
                }
                (xs.clone(), false)
            }
            FlatTy::Record(fs, _) => {
                if num || app {
                    return fail(self, &ft);
                }
                (fs.values().copied().collect(), true)
            }
            FlatTy::App(n, args) => match (n.as_str(), args.len()) {
                ("Int" | "Float", 0) => {
                    if app {
                        return fail(self, &ft);
                    }
                    (Vec::new(), false)
                }
                ("String", 0) => {
                    if num {
                        return fail(self, &ft);
                    }
                    (Vec::new(), false)
                }
                ("Char" | "Bool", 0) => {
                    if num || app {
                        return fail(self, &ft);
                    }
                    (Vec::new(), false)
                }
                ("List", 1) => {
                    if num {
                        return fail(self, &ft);
                    }
                    (args.clone(), false)
                }
                ("Maybe", 1) | ("Result", 2) => {
                    if num || app {
                        return fail(self, &ft);
                    }
                    (args.clone(), false)
                }
                // A `Dict` or a `Set` has no order (Elm's rule); both encode.
                ("Dict", 2) | ("Set", 1) => {
                    if num || app || s.has(SuperType::Comparable) {
                        return fail(self, &ft);
                    }
                    (args.clone(), false)
                }
                // A weak variable (C-2) is one fixed type chosen elsewhere.
                (w, _) if w.starts_with(crate::variance::WEAK_PREFIX) => (Vec::new(), false),
                _ => {
                    if num || app {
                        return fail(self, &ft);
                    }
                    (Vec::new(), true)
                }
            },
        };
        self.union(ra, rb, Content::Structure(ft));
        let root = self.find(ra);
        if pend && !elems.is_empty() {
            self.pending.push((elems, root));
        }
        if !elems.is_empty() {
            for c in children {
                let b = self.fresh(Content::FlexSuper(elems));
                self.unify(c, b)?;
            }
        }
        Ok(())
    }

    /// Turn an integer-literal variable ([`SuperType::IntLit`]) into the
    /// `Int` it is. A no-op on any other root. Its other bounds (`Number`,
    /// `Comparable`, `Encodable`) all hold for `Int`; a bound `Int` does not
    /// meet (`Appendable`, a Go value) fails the satisfiability check first,
    /// and the caller then refuses it against `Int`.
    pub(crate) fn default_int_literal(&mut self, v: TyVarId) {
        let r = self.find(v);
        let is_lit = matches!(
            &self.slots[r.0 as usize],
            Slot::Root { content: Content::FlexSuper(s), .. } if s.has(SuperType::IntLit)
        );
        if is_lit {
            self.set_content(
                r,
                Content::Structure(FlatTy::App(Name::new("Int"), Vec::new())),
            );
        }
    }

    /// A validated local `number` annotation generalises its literal root as
    /// Number. Keep the bound, but remove the literal's default-to-Int marker
    /// before each call gets a fresh instance. Concrete Int results never
    /// enter this branch, and unannotated literals still default to Int.
    pub(crate) fn generalise_annotated_number_literal(&mut self, v: TyVarId) {
        let root = self.find(v);
        if let Content::FlexSuper(bound) = self.content(root) {
            if bound.has(SuperType::Number) && bound.has(SuperType::IntLit) {
                self.set_content(root, Content::FlexSuper(bound.minus(SuperType::IntLit)));
            }
        }
    }

    /// Default every integer literal that no rigid `number` variable
    /// absorbed to `Int`. Run at the end of a definition, before its types
    /// are read back.
    pub(crate) fn default_all_int_literals(&mut self) {
        for v in std::mem::take(&mut self.int_literals) {
            self.default_int_literal(v);
        }
    }

    /// Render `v` as a type the user can WRITE in an annotation (v0.29.2), for
    /// the "write `T` instead of `a`" advice. An unresolved variable gets a
    /// name of its own (`List x1`, not `List _`, which does not parse), a
    /// bounded one its bound (`number`), and a record its fields
    /// (`{ r1 | count : x1 }`, not `record`).
    pub(crate) fn describe_advice(&mut self, v: TyVarId) -> String {
        let mut names: HashMap<TyVarId, String> = HashMap::new();
        let mut counters = (0u32, 0u32);
        self.advice_var(v, 0, &mut names, &mut counters)
    }

    fn advice_var(
        &mut self,
        v: TyVarId,
        depth: u32,
        names: &mut HashMap<TyVarId, String>,
        counters: &mut (u32, u32),
    ) -> String {
        if depth > 8 {
            return "x0".to_string();
        }
        let r = self.find(v);
        match self.content(r) {
            Content::Rigid(n) => n.as_str().to_string(),
            Content::FlexSuper(s) if s.has(SuperType::IntLit) => "Int".to_string(),
            Content::FlexSuper(s) => names
                .entry(r)
                .or_insert_with(|| {
                    counters.0 += 1;
                    format!("{}{}", s.label(), counters.0)
                })
                .clone(),
            Content::Flex | Content::Error => names
                .entry(r)
                .or_insert_with(|| {
                    counters.0 += 1;
                    format!("x{}", counters.0)
                })
                .clone(),
            Content::Structure(ft) => {
                let paren = |s: String| {
                    if s.contains(' ') && !s.starts_with('{') && !s.starts_with('(') {
                        format!("({s})")
                    } else {
                        s
                    }
                };
                match ft {
                    FlatTy::App(n, args) => {
                        let head = crate::nominal::strip(n.as_str()).to_string();
                        let mut out = head;
                        for a in args {
                            let s = self.advice_var(a, depth + 1, names, counters);
                            out.push(' ');
                            out.push_str(&paren(s));
                        }
                        out
                    }
                    FlatTy::Fun(a, b) => {
                        let sa = self.advice_var(a, depth + 1, names, counters);
                        let sb = self.advice_var(b, depth + 1, names, counters);
                        let sa = if sa.contains("->") && !sa.starts_with('{') {
                            format!("({sa})")
                        } else {
                            sa
                        };
                        format!("{sa} -> {sb}")
                    }
                    FlatTy::Tuple(xs) => {
                        let parts: Vec<String> = xs
                            .into_iter()
                            .map(|x| self.advice_var(x, depth + 1, names, counters))
                            .collect();
                        format!("( {} )", parts.join(", "))
                    }
                    FlatTy::Unit => "()".to_string(),
                    FlatTy::Record(fs, ext) => {
                        let (fs, ext) = self.normalize_record(fs, ext);
                        let fields: Vec<String> = fs
                            .into_iter()
                            .map(|(k, t)| {
                                let s = self.advice_var(t, depth + 1, names, counters);
                                format!("{} : {s}", k.as_str())
                            })
                            .collect();
                        let row = ext.map(|e| {
                            let re = self.find(e);
                            match self.content(re) {
                                Content::Rigid(n) => n.as_str().to_string(),
                                _ => names
                                    .entry(re)
                                    .or_insert_with(|| {
                                        counters.1 += 1;
                                        format!("r{}", counters.1)
                                    })
                                    .clone(),
                            }
                        });
                        match (row, fields.is_empty()) {
                            (None, true) => "{}".to_string(),
                            (None, false) => format!("{{ {} }}", fields.join(", ")),
                            (Some(r), true) => format!("{{ {r} | }}"),
                            (Some(r), false) => format!("{{ {r} | {} }}", fields.join(", ")),
                        }
                    }
                }
            }
        }
    }

    /// Describe a variable for a bound diagnostic (`describe_var`, public to
    /// the checker's obligation pass).
    pub(crate) fn describe(&mut self, v: TyVarId) -> String {
        self.describe_var(v, 0)
    }

    fn infinite(&mut self, a: TyVarId, b: TyVarId) -> Result<(), Mismatch> {
        self.union(a, b, Content::Error);
        Err(Mismatch::new("infinite type (occurs check)"))
    }

    /// Render a resolved variable to a display string for diagnostics, walking
    /// the union-find so a nominal application shows its arguments (`Maybe Int`,
    /// not the head-only `Maybe`). Unresolved / flex vars render as `_`; nested
    /// multi-word applications parenthesise. Depth-bounded so a cyclic residue
    /// can't loop.
    fn describe_var(&mut self, v: TyVarId, depth: u32) -> String {
        if depth > 6 {
            return "…".to_string();
        }
        match self.content(v) {
            Content::Structure(ft) => self.describe_flat(&ft, depth),
            Content::Rigid(n) => n.as_str().to_string(),
            Content::Flex | Content::FlexSuper(_) | Content::Error => "_".to_string(),
        }
    }

    /// Like [`Self::describe_flat`] but keeps the module qualifier on the HEAD
    /// name. Used for one case only: a mismatch whose two sides share a base
    /// name, where the stripped form would read "`Shape` vs `Shape`" and the
    /// declaring module IS the error.
    fn describe_flat_qualified(&mut self, ft: &FlatTy, depth: u32) -> String {
        match ft {
            FlatTy::App(n, args) if args.is_empty() => n.as_str().to_string(),
            FlatTy::App(n, _) => {
                let bare = self.describe_flat(ft, depth);
                // Swap the (stripped) head for the qualified one, arguments
                // untouched — they are a separate question.
                let head = crate::nominal::strip(n.as_str());
                match bare.strip_prefix(head) {
                    Some(rest) => format!("{}{}", n.as_str(), rest),
                    None => bare,
                }
            }
            other => self.describe_flat(other, depth),
        }
    }

    fn describe_flat(&mut self, ft: &FlatTy, depth: u32) -> String {
        match ft {
            FlatTy::App(n, args) if args.is_empty() => {
                crate::nominal::strip(n.as_str()).to_string()
            }
            FlatTy::App(n, args) => {
                let parts: Vec<String> = args
                    .iter()
                    .map(|a| {
                        let s = self.describe_var(*a, depth + 1);
                        if s.contains(' ') {
                            format!("({s})")
                        } else {
                            s
                        }
                    })
                    .collect();
                format!("{} {}", crate::nominal::strip(n.as_str()), parts.join(" "))
            }
            FlatTy::Fun(a, r) => {
                let sa = self.describe_var(*a, depth + 1);
                let sr = self.describe_var(*r, depth + 1);
                format!("{sa} -> {sr}")
            }
            FlatTy::Tuple(xs) => {
                let parts: Vec<String> = xs
                    .iter()
                    .map(|x| self.describe_var(*x, depth + 1))
                    .collect();
                format!("({})", parts.join(", "))
            }
            FlatTy::Record(_, _) => "record".to_string(),
            FlatTy::Unit => "()".to_string(),
        }
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
                // The parameter is contravariant: flip which side is the
                // declared type for the record wording, and restore it before
                // the result (covariant) or an early return.
                self.contravariant = !self.contravariant;
                let params = self.unify(a1, a2);
                self.contravariant = !self.contravariant;
                params?;
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
                // Nominal identity is `crate::nominal`'s call, not `==`. Two
                // same-named unions in DIFFERENT modules are different types —
                // the cross-module conflation that made `sky check` accept a
                // program which then panicked with `sky.Unreachable(case)`. A
                // BARE name means "declaring module unknown" and still matches,
                // so a resolution gap can never manufacture a rejection.
                if crate::nominal::same(n1.as_str(), n2.as_str()) && a1.len() == a2.len() {
                    let pairs: Vec<(TyVarId, TyVarId)> =
                        a1.iter().copied().zip(a2.iter().copied()).collect();
                    // Keep the QUALIFIED side as the representative so a
                    // confident identity propagates through inference variables
                    // instead of being erased by the first bare name it meets.
                    let winner =
                        if crate::nominal::most_specific(n1.as_str(), n2.as_str()) == n1.as_str() {
                            f1.clone()
                        } else {
                            f2.clone()
                        };
                    self.union(ra, rb, Content::Structure(winner));
                    for (x, y) in pairs {
                        self.unify(x, y)?;
                    }
                    Ok(())
                } else {
                    // Render the FULL applications (args included) — a head-only
                    // `Maybe` vs `String` hides that the mismatch is `Maybe Int`.
                    // Names print BARE (`nominal::strip`) so every pre-existing
                    // message is byte-identical — except when the two sides share
                    // a base, where the bare form would say the useless
                    // "`Shape` vs `Shape`" and the module is the whole point.
                    let same_base =
                        crate::nominal::base(n1.as_str()) == crate::nominal::base(n2.as_str());
                    let (d1, d2) = if same_base {
                        (
                            self.describe_flat_qualified(&f1, 0),
                            self.describe_flat_qualified(&f2, 0),
                        )
                    } else {
                        (self.describe_flat(&f1, 0), self.describe_flat(&f2, 0))
                    };
                    Err(Mismatch::new(format!("type mismatch: `{d1}` vs `{d2}`")))
                }
            }
            (FlatTy::Record(..), FlatTy::Record(..)) => {
                let (FlatTy::Record(fs1, e1), FlatTy::Record(fs2, e2)) = (f1, f2) else {
                    unreachable!("matched as two records above")
                };
                self.unify_records(ra, rb, fs1, e1, fs2, e2)
            }
            _ => {
                let (d1, d2) = (self.describe_flat(&f1, 0), self.describe_flat(&f2, 0));
                Err(Mismatch::new(format!("type mismatch: {d1} vs {d2}")))
            }
        }
    }

    /// Follow a record's extension chain, merging any ext var that has already
    /// resolved to a concrete `Record` into the field map. Returns the fully
    /// flattened field set plus the FINAL extension (`None` = genuinely closed;
    /// `Some(v)` = still open under a flex/unresolved row var `v`).
    ///
    /// The row-poly result-access bug: a generalised row-poly function like
    /// `bump : {age|ρ} -> {age|ρ}` shares ρ between its param and result. At a
    /// call `bump {name, age}`, unifying the param binds ρ to `Record({name})`,
    /// so the RESULT node reads as the NESTED `{age | {name}}`. Without
    /// flattening, `{age | {name}}` is treated as `{age | flex}` for the
    /// closed/open discipline (the ext var is `Some`, so "open"), and the
    /// row-carried `name` is invisible — field access on it, and the eventual
    /// generalised read-back, drop `name`. Flattening makes `{age | {name}}`
    /// behave as the closed `{age, name}` it really is.
    ///
    /// Bounded by a visited set so a recursive row (`ρ` resolving to a record
    /// whose ext is `ρ` again) terminates. Outer fields win over an inner
    /// ext-carried field of the same name (they are more specific).
    pub(crate) fn normalize_record(
        &mut self,
        mut fields: BTreeMap<Name, TyVarId>,
        ext: Option<TyVarId>,
    ) -> (BTreeMap<Name, TyVarId>, Option<TyVarId>) {
        let mut cur = ext;
        let mut seen = std::collections::HashSet::new();
        loop {
            match cur {
                None => return (fields, None),
                Some(e) => {
                    let r = self.find(e);
                    // The returned row/map is a raw-ID export, not just a read.
                    self.revoke_accessor_root(e);
                    if !seen.insert(r) {
                        return (fields, Some(r));
                    }
                    match &self.slots[r.0 as usize] {
                        Slot::Root {
                            content: Content::Structure(FlatTy::Record(fs_inner, ext_inner)),
                            ..
                        } => {
                            crate::counters::RECORD_FIELDS.add(fs_inner.len() as u64);
                            for (k, v) in fs_inner {
                                fields.entry(k.clone()).or_insert(*v);
                            }
                            cur = *ext_inner;
                        }
                        // Flex/super/rigid/error/non-record structure: the row is
                        // still open under `r` (or malformed — leave it to the
                        // caller's unify to clash). This preserves the exact
                        // pre-fix open/closed status for every case except a
                        // fully-resolved Record chain.
                        _ => return (fields, Some(r)),
                    }
                }
            }
        }
    }

    /// `unify(base, rec)` where `rec` is a constraint node built for a field
    /// access or a record update — `{ fields | row }` — and `rec` and `row`
    /// are FRESH: nothing but `rec` refers to `row`, and nothing refers to
    /// `rec`. For a singleton field access, the narrow open-record form below
    /// is tried first. Otherwise the base must be a closed record holding every
    /// field. Returns `None` when neither proof applies; the caller then runs
    /// the general `unify`.
    ///
    /// When it applies, the union-find ends in the state the general path
    /// leaves on every node reachable from a live variable, with the same
    /// variables allocated in the same order, and the same result:
    ///
    /// * `unify_records(base, rec)` links `rec` under `base`'s root and keeps
    ///   the base's content (a closed record whose fields include all of
    ///   `fields` is its own merge): [`UnionFind::link_fresh`];
    /// * it unifies each shared field pair in field order, stopping at the
    ///   first clash. With `fresh_values` (a field ACCESS: each value is a
    ///   fresh flex var referred to only by `rec`), that unify is a union in
    ///   which the field keeps its own content and the occurs check cannot
    ///   fire, so it is a link too; otherwise the general `unify` runs;
    /// * when the base has fields beyond `fields`, it binds `row` to a fresh
    ///   record of them. Nothing live refers to `row` (only `rec`'s content
    ///   did, and `rec` is now a link), so that record is never read: the fast
    ///   form allocates its variable, to keep later variable ids (and so the
    ///   `t<id>` names of read-back) identical, without copying the base's
    ///   field map into it or walking it for the occurs check.
    ///
    /// This turns a field access from O(size of the record) into O(log
    /// fields), and an update into O(updated fields) plus their unifications.
    pub(crate) fn unify_closed_record_fresh_row(
        &mut self,
        base: TyVarId,
        rec: TyVarId,
        row: TyVarId,
        fields: &BTreeMap<Name, TyVarId>,
        fresh_values: bool,
    ) -> Option<Result<(), Mismatch>> {
        let ra = self.find(base);
        let rb = self.find(rec);
        if ra == rb {
            return None;
        }
        if fresh_values {
            if let Some(result) = self.unify_open_record_fresh_access(ra, rb, row, fields) {
                return Some(result);
            }
        }
        let (field_vars, has_rest) = match &self.slots[ra.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::Record(fs, None)),
                ..
            } => {
                let mut vs = Vec::with_capacity(fields.len());
                for n in fields.keys() {
                    vs.push(*fs.get(n)?);
                }
                (vs, fs.len() > fields.len())
            }
            _ => return None,
        };
        crate::counters::RECORD_FIELDS.add(fields.len() as u64);
        self.link_fresh(ra, rb);
        for (fv, (_, val)) in field_vars.into_iter().zip(fields.iter()) {
            if fresh_values {
                let r = self.find(fv);
                self.link_fresh(r, *val);
            } else if let Err(m) = self.unify(fv, *val) {
                return Some(Err(m));
            }
        }
        if has_rest {
            let rest = self.fresh(Content::Structure(FlatTy::Record(BTreeMap::new(), None)));
            // `union(row, rest, …)`: two rank-0 roots, `row` first.
            self.link_fresh(row, rest);
            self.set_content(
                row,
                Content::Structure(FlatTy::Record(BTreeMap::new(), None)),
            );
        }
        Some(Ok(()))
    }

    /// The caller proves that `rec`, `row`, and every value in `fields`
    /// were created after inferring `base` and have no other references. This
    /// narrower form admits only one fresh Access field and a direct, plain
    /// flexible base extension; bounded, rigid, resolved or malformed rows
    /// keep the general path. In particular this is not an arbitrary record
    /// merge or a first-class accessor application.
    ///
    /// The old extension remains live: a missing field binds it to that
    /// singleton over the fresh shared rest, exactly as `unify_records` does.
    /// Only the NEW constraint's extension loses its sole incoming edge when
    /// `rec` links to `base`; its potentially wide complement is unreachable.
    /// Preserve its allocation/link/rank sequence without copying that map.
    fn unify_open_record_fresh_access(
        &mut self,
        base: TyVarId,
        rec: TyVarId,
        row: TyVarId,
        fields: &BTreeMap<Name, TyVarId>,
    ) -> Option<Result<(), Mismatch>> {
        if fields.len() != 1 {
            return None;
        }
        let (field, &value) = fields.first_key_value()?;
        let (old_ext, existing, has_rest) = match &self.slots[base.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::Record(fs, Some(ext))),
                ..
            } => {
                let existing = fs.get(field).copied();
                (*ext, existing, fs.len() > usize::from(existing.is_some()))
            }
            _ => return None,
        };
        let old_ext = self.find(old_ext);
        if !matches!(self.kind(old_ext), Kind::Flex) {
            return None;
        }
        // Replacing this owner record's old extension ends any earlier grant.
        self.revoke_accessor_root(old_ext);
        // Both open rows allocate their shared rest before any child unifies.
        let rest = self.fresh_flex();
        let Content::Structure(FlatTy::Record(mut all, _)) = self.take_content(base) else {
            unreachable!("the base was inspected above, without an intervening unify")
        };
        if existing.is_none() {
            all.insert(field.clone(), value);
        }
        self.set_moved_record_content(base, Content::Structure(FlatTy::Record(all, Some(rest))));
        self.link_fresh(base, rec);
        crate::counters::RECORD_FIELDS.bump();
        if let Some(existing) = existing {
            // The value is fresh, so it cannot occur in an existing field.
            self.link_fresh(existing, value);
        } else {
            // This old row can have external aliases: preserve its constraint.
            let singleton = self.fresh(Content::Structure(FlatTy::Record(
                fields.clone(),
                Some(rest),
            )));
            let _ = self.unify(old_ext, singleton);
        }
        if has_rest {
            let complement = self.fresh(Content::Structure(FlatTy::Record(
                BTreeMap::new(),
                Some(rest),
            )));
            self.link_fresh(row, complement);
            self.set_content(
                row,
                Content::Structure(FlatTy::Record(BTreeMap::new(), Some(rest))),
            );
        }
        Some(Ok(()))
    }

    /// `union(root, child, <root's own content>)` for a `child` that is a
    /// rank-0 root distinct from `root`: `child` links under `root`, whose
    /// rank rises to 1 if it was 0 (union by rank). The content stays.
    fn link_fresh(&mut self, root: TyVarId, child: TyVarId) {
        // Root content is preserved; child content is discarded.
        self.revoke_accessor_owner(child);
        self.revoke_accessor_root(root);
        self.revoke_accessor_root(child);
        let root = self.find(root);
        let child = self.find(child);
        debug_assert_ne!(root, child);
        debug_assert_eq!(self.rank(child), 0, "link_fresh: child must be rank 0");
        if self.rank(root) == 0 {
            if let Slot::Root { rank, .. } = &mut self.slots[root.0 as usize] {
                *rank = 1;
            }
        }
        self.slots[child.0 as usize] = Slot::Link(root);
    }

    /// Consume only the sole incoming row edge of a compiler-created
    /// singleton accessor. The partner is closed or has a literal plain Flex
    /// extension; all meaningful old-row constraints still run ordinarily.
    fn unify_consumable_accessor_record(
        &mut self,
        ra: TyVarId,
        rb: TyVarId,
    ) -> Option<Result<(), Mismatch>> {
        if !self.accessor_rows.active() {
            return None;
        }
        let owned = |root: TyVarId| -> Option<(Name, TyVarId, TyVarId)> {
            let Slot::Root {
                content: Content::Structure(FlatTy::Record(fields, Some(row))),
                ..
            } = &self.slots[root.0 as usize]
            else {
                return None;
            };
            if fields.len() != 1 || !self.accessor_rows.contains(row) {
                return None;
            }
            if !matches!(
                &self.slots[row.0 as usize],
                Slot::Root {
                    content: Content::Flex,
                    rank: 0
                }
            ) {
                return None;
            }
            let (field, value) = fields.first_key_value()?;
            Some((field.clone(), *value, *row))
        };
        let (accessor, base, field, value, row) = if let Some((f, v, r)) = owned(ra) {
            (ra, rb, f, v, r)
        } else if let Some((f, v, r)) = owned(rb) {
            (rb, ra, f, v, r)
        } else {
            return None;
        };
        let (base_value, base_only, old_ext) = match &self.slots[base.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::Record(fields, ext)),
                ..
            } => {
                let shared = fields.get(&field).copied();
                match ext {
                    None if shared.is_none() => return None,
                    Some(old)
                        if *old == row
                            || !matches!(
                                &self.slots[old.0 as usize],
                                Slot::Root {
                                    content: Content::Flex,
                                    ..
                                }
                            ) =>
                    {
                        return None
                    }
                    _ => {}
                }
                (shared, fields.len() - usize::from(shared.is_some()), *ext)
            }
            _ => return None,
        };
        // Consume before any fallible/reentrant work or owned Record copy.
        // The base's old extension remains semantically live, so revoke its
        // authority and retain its ordinary complement constraint below.
        self.accessor_rows.revoke(row);
        if let Some(old) = old_ext {
            self.revoke_accessor_root(old);
        }
        // General record merging allocates the shared rest BEFORE union and
        // all shared-field/complement constraints, even for existing fields.
        let rest = old_ext.map(|_| self.fresh_flex());
        let Content::Structure(FlatTy::Record(mut all, _)) = self.take_content(base) else {
            unreachable!()
        };
        if accessor == ra {
            all.insert(field.clone(), value);
        } else {
            all.entry(field.clone()).or_insert(value);
        }
        self.union(ra, rb, Content::Structure(FlatTy::Record(all, rest)));
        crate::counters::RECORD_FIELDS.bump();
        if let Some(other) = base_value {
            let pair = if accessor == ra {
                (value, other)
            } else {
                (other, value)
            };
            if let Err(error) = self.unify(pair.0, pair.1) {
                return Some(Err(error));
            }
        }
        // Mirror e1 then e2 exactly. Only the consumed row's complement is
        // dead; a new field's one-field old-row complement is meaningful and
        // must use ordinary unify, including its ignored-error graph state.
        if accessor == ra {
            self.bind_consumed_accessor_complement(row, base_only, rest);
            self.bind_accessor_base_complement(old_ext, base_value.is_none(), &field, value, rest);
        } else {
            self.bind_accessor_base_complement(old_ext, base_value.is_none(), &field, value, rest);
            self.bind_consumed_accessor_complement(row, base_only, rest);
        }
        Some(Ok(()))
    }

    /// Private post-consumption continuation only. No live graph edge reaches
    /// row; preserve the allocation/link/rank with an empty placeholder map.
    fn bind_consumed_accessor_complement(
        &mut self,
        row: TyVarId,
        fields: usize,
        rest: Option<TyVarId>,
    ) {
        if fields == 0 {
            return;
        }
        let complement = self.fresh(Content::Structure(FlatTy::Record(BTreeMap::new(), rest)));
        self.link_fresh(row, complement);
        self.set_content(
            row,
            Content::Structure(FlatTy::Record(BTreeMap::new(), rest)),
        );
    }

    /// This is intentionally NOT a fresh-link shortcut. The accessor's value
    /// can already alias the old row; occurs checks and ignored failures of
    /// the original generic record merge must still happen in original order.
    fn bind_accessor_base_complement(
        &mut self,
        old: Option<TyVarId>,
        missing: bool,
        field: &Name,
        value: TyVarId,
        rest: Option<TyVarId>,
    ) {
        if !missing {
            return;
        }
        if let Some(old) = old {
            let complement = self.fresh(Content::Structure(FlatTy::Record(
                BTreeMap::from([(field.clone(), value)]),
                rest,
            )));
            let _ = self.unify(old, complement);
        }
    }

    /// The number of nodes of `ty` when `v`'s graph, unfolded along `ty`, IS
    /// `ty`: at every node of `ty` the class there is a structure with the
    /// same shape (an identical nominal name, a closed record with the same
    /// field names, a tuple of the same width, a function, unit). `None` when
    /// any node differs, or when `ty` holds a type variable, an `Error` or an
    /// open record — the cases where unifying a fresh instance of `ty` with
    /// `v` could bind, rename, widen or allocate beyond one node per node of
    /// `ty`. The walk only reads (and compresses paths).
    ///
    /// A match is finite, as `ty` is: every leaf of `ty` matches only a class
    /// with no children, so a cyclic graph never matches.
    pub(crate) fn ground_instance_nodes(&mut self, v: TyVarId, ty: &crate::Ty) -> Option<usize> {
        use crate::Ty;
        let mut nodes = 0usize;
        let mut stack: Vec<(TyVarId, &Ty)> = vec![(v, ty)];
        while let Some((x, t)) = stack.pop() {
            nodes += 1;
            let r = self.find(x);
            let Slot::Root {
                content: Content::Structure(ft),
                ..
            } = &self.slots[r.0 as usize]
            else {
                return None;
            };
            match (ft, t) {
                (FlatTy::App(n, args), Ty::App(m, targs))
                    if n == m && args.len() == targs.len() =>
                {
                    stack.extend(args.iter().copied().zip(targs.iter()));
                }
                (FlatTy::Fun(a, b), Ty::Fun(ta, tb)) => {
                    stack.push((*a, &**ta));
                    stack.push((*b, &**tb));
                }
                (FlatTy::Tuple(xs), Ty::Tuple(ts)) if xs.len() == ts.len() => {
                    stack.extend(xs.iter().copied().zip(ts.iter()));
                }
                (FlatTy::Unit, Ty::Unit) => {}
                (FlatTy::Record(fs, None), Ty::Record(tfs, None)) if fs.len() == tfs.len() => {
                    // A `Ty` record lists its fields in declaration order.
                    // Every name must be a field of the class, once: with
                    // equal lengths that makes the two name sets equal. (A
                    // repeated name would collapse in `ty_to_var`'s map and
                    // the unify would then clash, so it never matches.)
                    if !tfs.windows(2).all(|w| w[0].0 < w[1].0) {
                        let mut names: Vec<&Name> = tfs.iter().map(|(n, _)| n).collect();
                        names.sort_unstable();
                        if names.windows(2).any(|w| w[0] == w[1]) {
                            return None;
                        }
                    }
                    for (tk, tt) in tfs {
                        stack.push((*fs.get(tk)?, tt));
                    }
                }
                _ => return None,
            }
        }
        Some(nodes)
    }

    /// Reserve `n` logical variables nothing refers to, preserving every later
    /// ID/name without allocating phantom slots or occurs-check marks. The sole
    /// caller has proved that these are the elided ground instance's dead nodes.
    pub(crate) fn reserve_unreachable(&mut self, n: usize) {
        self.slots.reserve_unreachable_ids(n);
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
        if let Some(row) = e1 {
            self.revoke_accessor_root(row);
        }
        if let Some(row) = e2 {
            self.revoke_accessor_root(row);
        }
        // Flatten any resolved-Record extension so the closed/open discipline
        // sees the record's true field set + true final extension. A no-op when
        // both ext vars are still flex (the common case at call time).
        let (fs1, e1) = self.normalize_record(fs1, e1);
        let (fs2, e2) = self.normalize_record(fs2, e2);
        crate::counters::RECORD_FIELDS.add((fs1.len() + fs2.len()) as u64);
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
        // (extras1Illegal / extras2Illegal, Unify.hs:486). Runs unconditionally:
        // a closed record that lacks a field the other side carries is a genuine
        // field-presence clash the Haskell oracle rejects.
        // The first unify arg is the ACTUAL (body/literal), the second the
        // EXPECTED (declared) — `unify(v, expected)` threads a→side1, b→side2 —
        // except under a function's parameter, where the roles swap
        // (`contravariant`). So, with the sides put in actual/expected order:
        //   actual closed + fields only the expected has
        //     ⇒ the actual is MISSING those fields.
        //   expected closed + fields only the actual has
        //     ⇒ the actual has UNKNOWN fields not in the declared type.
        // When both hold, one message names both.
        let (act_only, act_closed, exp_only, exp_closed) = if self.contravariant {
            (&only2, e2.is_none(), &only1, e1.is_none())
        } else {
            (&only1, e1.is_none(), &only2, e2.is_none())
        };
        let missing = act_closed && !exp_only.is_empty();
        let unknown = exp_closed && !act_only.is_empty();
        match (missing, unknown) {
            (true, true) => {
                return Err(Mismatch::new(format!(
                    "{}, and has {}",
                    record_missing_msg(exp_only),
                    unknown_fields_phrase(act_only)
                )))
            }
            (true, false) => return Err(Mismatch::new(record_missing_msg(exp_only))),
            (false, true) => return Err(Mismatch::new(record_unknown_msg(act_only))),
            (false, false) => {}
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
        let rigid1 = e1.and_then(|e| self.rigid_name(e));
        let rigid2 = e2.and_then(|e| self.rigid_name(e));
        let field_names: Vec<Name> = all.keys().cloned().collect();
        self.union(ra, rb, Content::Structure(FlatTy::Record(all, ext)));
        for (a, b) in shared {
            self.unify(a, b)?;
        }
        // A RIGID row (an annotation's `{ r | name : String }`, v0.29.2)
        // stands for whatever other fields a caller's record has, so it is
        // unified like any other rigid variable: each open side's row must
        // be exactly the other side's extra fields plus the shared rest, and a
        // closed side pins the row to nothing. A rigid row may not take a
        // field (`rec.age` on `{ r | name : String }`), become empty (a
        // closed record returned where `{ r | … }` is promised), or become a
        // different rigid row (`{ r | … } -> { s | … }` returning its
        // argument). Before v0.29.2 those links were dropped, and each such
        // program compiled and then failed at run time.
        if rigid1.is_some() || rigid2.is_some() {
            let rigid = (rigid1, rigid2);
            self.link_rigid_row(e1, &only2, ext, &rigid, &field_names)?;
            return self.link_rigid_row(e2, &only1, ext, &rigid, &field_names);
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

    /// The name of `v`'s root when it is a rigid (annotation) variable.
    fn rigid_name(&mut self, v: TyVarId) -> Option<Name> {
        let r = self.find(v);
        match &self.slots[r.0 as usize] {
            Slot::Root {
                content: Content::Rigid(n),
                ..
            } => Some(n.clone()),
            _ => None,
        }
    }

    /// Link one open side's row to what it must hold when a rigid row takes
    /// part (see [`Self::unify_records`]): the other side's `extras` over the
    /// shared rest `ext`, or the empty closed row when the merged record is
    /// closed. A refusal is worded for the rigid row.
    fn link_rigid_row(
        &mut self,
        row: Option<TyVarId>,
        extras: &BTreeMap<Name, TyVarId>,
        ext: Option<TyVarId>,
        rigid: &(Option<Name>, Option<Name>),
        fields: &[Name],
    ) -> Result<(), Mismatch> {
        let Some(row) = row else {
            // A closed side: the presence check above already compared its
            // fields with the other side's.
            return Ok(());
        };
        let target = match (extras.is_empty(), ext) {
            (true, Some(e)) => e,
            _ => self.fresh(Content::Structure(FlatTy::Record(extras.clone(), ext))),
        };
        if self.unify(row, target).is_ok() {
            return Ok(());
        }
        let names = |ns: &[&Name]| {
            ns.iter()
                .map(|n| format!("`{}`", n.as_str()))
                .collect::<Vec<_>>()
                .join(", ")
        };
        let msg = match (rigid, ext) {
            // The merged record is closed: the rigid row would be nothing.
            ((Some(r), _), None) | ((None, Some(r)), None) => {
                let fs: Vec<&Name> = fields.iter().collect();
                rigid_row_closed_message(r.as_str(), &names(&fs))
            }
            // Two different rigid rows made one.
            ((Some(r1), Some(r2)), Some(_)) if r1 != r2 && extras.is_empty() => {
                rigid_row_pair_message(r1.as_str(), r2.as_str())
            }
            ((Some(r), _), Some(_)) | ((None, Some(r)), Some(_)) => {
                if extras.is_empty() {
                    // The row met another rigid row through a flexible one.
                    let other = match rigid {
                        (Some(a), Some(b)) if a == r => b.as_str().to_string(),
                        (Some(a), _) => a.as_str().to_string(),
                        (None, Some(b)) => b.as_str().to_string(),
                        _ => "another row".to_string(),
                    };
                    rigid_row_pair_message(r.as_str(), &other)
                } else {
                    let fs: Vec<&Name> = extras.keys().collect();
                    rigid_row_fields_message(r.as_str(), &names(&fs))
                }
            }
            ((None, None), _) => unreachable!("called only when a row is rigid"),
        };
        Err(Mismatch::new(msg))
    }
}

/// A rigid row variable was given fields its annotation does not list.
pub(crate) fn rigid_row_fields_message(row: &str, fields: &str) -> String {
    format!(
        "the type annotation says `{row}` stands for any other fields a record may have, but this \
         code needs the record to have {fields}, which the annotation does not list. A record type \
         `{{ {row} | … }}` promises only the fields it names: a caller may pass a record without \
         {fields}. Fix: add {fields} to the record type in the type annotation"
    )
}

/// A rigid row variable was made empty: a closed record where the
/// annotation promised `{ r | … }`.
pub(crate) fn rigid_row_closed_message(row: &str, fields: &str) -> String {
    format!(
        "the type annotation says `{row}` stands for any other fields a record may have, but this \
         code uses a record with exactly the fields {fields} there, so the other fields a caller's \
         record has would be lost. Fix: keep the caller's record and change it with an update \
         (`{{ rec | name = … }}`), or write a closed record type (no `{row} |`) in the type \
         annotation"
    )
}

/// Two different rigid row variables were made one set of fields.
pub(crate) fn rigid_row_pair_message(a: &str, b: &str) -> String {
    format!(
        "the type annotation says `{a}` and `{b}` can be different sets of record fields, but this \
         code makes them the same. Fix: write the same row variable in both record types of the \
         type annotation (for example `{{ {a} | … }}` twice), or change the code so it does not \
         pass one record where the other is promised"
    )
}

pub(crate) fn flat_children(ft: &FlatTy) -> Vec<TyVarId> {
    match ft {
        FlatTy::App(_, args) => args.clone(),
        FlatTy::Fun(a, b) => vec![*a, *b],
        FlatTy::Record(fs, e) => fs.values().copied().chain(e.iter().copied()).collect(),
        FlatTy::Tuple(xs) => xs.clone(),
        FlatTy::Unit => Vec::new(),
    }
}

fn record_missing_msg(missing: &BTreeMap<Name, TyVarId>) -> String {
    let names: Vec<&str> = missing.keys().map(Name::as_str).collect();
    format!("record is missing field(s): {}", names.join(", "))
}

fn record_unknown_msg(unknown: &BTreeMap<Name, TyVarId>) -> String {
    format!("record has {}", unknown_fields_phrase(unknown))
}

fn unknown_fields_phrase(unknown: &BTreeMap<Name, TyVarId>) -> String {
    let names: Vec<&str> = unknown.keys().map(Name::as_str).collect();
    format!(
        "unknown field(s) not in the declared type: {}",
        names.join(", ")
    )
}

/// The migration link for an ordering refused since v0.27.0.
pub(crate) const COMPARABLE_ANCHOR: &str = "docs/migration/v0.27.md#comparable-bound";
/// The migration link for a persisted value refused since v0.27.0.
pub(crate) const ENCODABLE_ANCHOR: &str = "docs/migration/v0.27.md#encodable-bound";

/// Why a shape fails a bound, worded for the user. `what` renders the shape.
pub(crate) fn bound_failure(s: SuperType, what: &str, ft: &FlatTy) -> String {
    let s = s.closure();
    let shown = match ft {
        FlatTy::Fun(..) => format!("a function (`{what}`)"),
        FlatTy::Record(..) => "a record".to_string(),
        _ => format!("`{what}`"),
    };
    if s.has(SuperType::Number) {
        format!("{shown} is not a number: this needs an `Int` or a `Float`")
    } else if s.has(SuperType::Appendable) {
        format!("{shown} cannot be appended with `++`: only a `String` or a `List` can")
    } else if s.has(SuperType::Comparable) {
        comparable_failure(&shown)
    } else {
        encodable_failure(&shown)
    }
}

/// The anchor every `[E2013]` message ends with; the checker keys the code on it.
pub(crate) const GO_INTERFACE_ANCHOR: &str = "docs/migration/v0.27.md#ffi-go-interface-params";

/// The Go-interface refusal (`[E2013]`), ending with the migration link.
pub(crate) fn go_value_failure(shown: &str) -> String {
    format!(
        "{shown} is given where a Go FFI binding needs a Go value that implements a Go \
         interface. Since v0.27.0 a Go interface parameter is checked: a Sky value never \
         implements a Go interface. Fix: pass a Go value that implements it (one a binding \
         returns). see {GO_INTERFACE_ANCHOR}"
    )
}

/// The `Comparable` refusal, ending with the migration link.
pub(crate) fn comparable_failure(shown: &str) -> String {
    format!(
        "{shown} cannot be ordered, so it cannot be used with `<`, `>`, `<=`, `>=`, \
         `compare`, `min`, `max`, `List.sort`, `List.sortBy` or a `Set`. Since v0.27.0 an \
         ordering needs a comparable type: `Int`, `Float`, `String`, `Char`, `Bool`, a `List`, \
         `Maybe`, `Result` or tuple of comparables, a record whose fields are comparable, or a \
         custom type whose constructors hold only comparable values. A function, a `Dict`, a \
         `Set`, a `Secret`, a key, a runtime handle and an opaque stdlib type have no order. \
         Fix: order by a comparable key instead, for example `List.sortBy .name items`. \
         See {COMPARABLE_ANCHOR}"
    )
}

/// The `Encodable` refusal, ending with the migration link.
pub(crate) fn encodable_failure(shown: &str) -> String {
    format!(
        "{shown} cannot be encoded, so it cannot be stored by `Codec.auto`, \
         `App.withDurable`, a `Std.Db.Table`, `Jobs.define`/`enqueue` or `Auth.signToken`. \
         Since v0.27.0 these need a type that round-trips as data: no function, `Secret`, \
         crypto key or state, or runtime handle (`Process`, `Watcher`, `Sync.Ref`, `Mutex`, \
         `Queue`, `WebSocket`, `WebSocketServer`, `StreamId`, `StreamWriter`, `Cache`, `Regex`) anywhere \
         inside, in either direction (a handle decoded from JSON would be forged). Fix: keep \
         that value out of the stored type, for example in a separate field of the model that \
         is not persisted, or store the data it is made from. See {ENCODABLE_ANCHOR}"
    )
}

/// The annotation is too general: its variable `var` stands for any type,
/// and the body uses it as the concrete type `shown` (v0.29.2: every variable
/// of a top-level annotation is rigid, argument-only ones included).
pub(crate) fn rigid_concrete_message(var: &str, shown: &str) -> String {
    format!(
        "the type annotation says `{var}` can be any type, but this code uses it as `{shown}`. \
         A type variable in an annotation stands for every type a caller may choose, so the \
         code must work for all of them. Fix: write `{shown}` instead of `{var}` in the type \
         annotation, or change the code so it does not depend on the type"
    )
}

/// The annotation is too general: two of its variables can be different
/// types, and the body makes them one type (`coerce : a -> b; coerce x = x`).
pub(crate) fn rigid_pair_message(a: &str, b: &str) -> String {
    format!(
        "the type annotation says `{a}` and `{b}` can be two different types, but this code \
         uses them as one type. A caller could choose two different types, and the value would \
         not be the type the annotation promises. Fix: write one variable for both (`{a}` in \
         place of `{b}`), or change the code so the two stay apart"
    )
}

/// Elm's "the annotation is too general" error for a rigid variable that the
/// body needs to be bounded (`largest : List a -> Maybe a` using `List.sort`).
pub(crate) fn rigid_bound_message(var: &str, missing: SuperType) -> String {
    let missing = missing.closure();
    if missing.has(SuperType::Comparable) && !missing.has(SuperType::Number) {
        format!(
            "the type annotation says `{var}` can be any type, but this code orders it \
             (`<`, `compare`, `min`, `max`, `List.sort`, `List.sortBy` or a `Set`), so it must \
             be a comparable type. Since v0.27.0 an ordering needs a comparable type, and a \
             type variable that is ordered must say so. Fix: write `comparable` instead of \
             `{var}` in the type annotation, for example \
             `largest : List comparable -> Maybe comparable`. See {COMPARABLE_ANCHOR}"
        )
    } else if missing.has(SuperType::Number) {
        format!(
            "the type annotation says `{var}` can be any type, but this code uses it as a \
             number. Fix: write `number` instead of `{var}` in the type annotation, or a \
             concrete `Int` or `Float`"
        )
    } else {
        format!(
            "the type annotation says `{var}` can be any type, but this code appends it with \
             `++`. Fix: write `appendable` instead of `{var}` in the type annotation, or a \
             concrete `String` or `List`"
        )
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
        assert!(
            matches!(uf.content(a), Content::Structure(FlatTy::App(n, _)) if n.as_str() == "Int")
        );
    }

    #[test]
    fn rejects_mismatched_apps() {
        let mut uf = UnionFind::new();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let s = uf.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        assert!(uf.unify(int, s).is_err());
    }

    /// Mismatch messages render the FULL application (arguments included), not
    /// the head-only constructor — `Maybe Int` vs `String`, and the nested
    /// `List (Maybe Int)` parenthesises.
    #[test]
    fn mismatch_message_keeps_type_arguments() {
        let mut uf = UnionFind::new();
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let maybe_int = uf.fresh(Content::Structure(FlatTy::App(
            Name::new("Maybe"),
            vec![int],
        )));
        let s = uf.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        let err = uf.unify(maybe_int, s).unwrap_err();
        assert!(
            err.message.contains("Maybe Int") && err.message.contains("String"),
            "want full args, got: {}",
            err.message
        );

        // Nested application parenthesises: List (Maybe Int).
        let mut uf2 = UnionFind::new();
        let int2 = uf2.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let maybe2 = uf2.fresh(Content::Structure(FlatTy::App(
            Name::new("Maybe"),
            vec![int2],
        )));
        let list_maybe = uf2.fresh(Content::Structure(FlatTy::App(
            Name::new("List"),
            vec![maybe2],
        )));
        let plain_int = uf2.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let err2 = uf2.unify(list_maybe, plain_int).unwrap_err();
        assert!(
            err2.message.contains("List (Maybe Int)"),
            "want parenthesised nested app, got: {}",
            err2.message
        );
    }

    /// The self-host killer guard (self-host §7 R1-D2): two DISTINCT
    /// nominal/opaque types must NOT unify. The legacy `Unify.sky:99-100`
    /// `isOpaqueFfiType a && isOpaqueFfiType b -> Ok emptySub` made every pair
    /// of unrelated FFI types unify (`Customer` ≡ `Widget`). This unifier has NO
    /// such rule — `App(n1,..)` unifies with `App(n2,..)` only when `n1 == n2`.
    #[test]
    fn distinct_nominal_types_do_not_unify() {
        let mut uf = UnionFind::new();
        let customer = uf.fresh(Content::Structure(FlatTy::App(
            Name::new("Customer"),
            vec![],
        )));
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

    #[test]
    fn rigid_binds_flex_but_clashes_concrete() {
        // A skolem BINDS a plain flex (body keeps the quantifier polymorphic:
        // `identity : a -> a`).
        let mut uf = UnionFind::new();
        let rigid = uf.fresh(Content::Rigid(Name::new("a")));
        let flex = uf.fresh_flex();
        assert!(uf.unify(rigid, flex).is_ok());
        assert!(matches!(uf.content(flex), Content::Rigid(n) if n.as_str() == "a"));

        // A skolem CLASHES with a concrete Structure (body pins the quantifier:
        // `f : a -> a; f n = n + 1` — audit #5).
        let mut uf = UnionFind::new();
        let rigid = uf.fresh(Content::Rigid(Name::new("a")));
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        assert!(uf.unify(rigid, int).is_err());

        // A skolem against a super-constrained flex whose bound its name does
        // not spell (`x = 5` forces Number — audit #6): the refusal is
        // DEFERRED to the end of the definition (`rigid_missing`), so a later,
        // clearer clash with a type can be reported instead (v0.29.2).
        let mut uf = UnionFind::new();
        let rigid = uf.fresh(Content::Rigid(Name::new("a")));
        let num = uf.fresh(Content::FlexSuper(SuperType::Number));
        assert!(uf.unify(rigid, num).is_ok());
        assert_eq!(uf.rigid_missing.len(), 1);
        assert!(uf.rigid_missing[0].1.has(SuperType::Number));
        // A skolem whose name spells the bound needs nothing.
        let mut uf = UnionFind::new();
        let rigid = uf.fresh(Content::Rigid(Name::new("number")));
        let num = uf.fresh(Content::FlexSuper(SuperType::Number));
        assert!(uf.unify(rigid, num).is_ok());
        assert!(uf.rigid_missing.is_empty());
    }

    #[test]
    fn distinct_rigids_clash_same_rigid_ok() {
        // Same skolem name unifies (both occurrences of `a` in `a -> a`).
        let mut uf = UnionFind::new();
        let a1 = uf.fresh(Content::Rigid(Name::new("a")));
        let a2 = uf.fresh(Content::Rigid(Name::new("a")));
        assert!(uf.unify(a1, a2).is_ok());
        // Distinct skolems clash at the primitive level (two distinct
        // result-position quantifiers forced together by the body).
        let mut uf = UnionFind::new();
        let a = uf.fresh(Content::Rigid(Name::new("a")));
        let b = uf.fresh(Content::Rigid(Name::new("b")));
        assert!(uf.unify(a, b).is_err());
    }
    fn record_of(uf: &mut UnionFind, n: usize) -> TyVarId {
        let int = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        let fields: BTreeMap<Name, TyVarId> =
            (0..n).map(|i| (Name::new(&format!("f{i}")), int)).collect();
        uf.fresh(Content::Structure(FlatTy::Record(fields, None)))
    }

    /// Binding a variable to a record moves the record into the merged root:
    /// no field map is copied, on either side of the pair, and the variable
    /// reads back the record.
    #[test]
    fn binding_a_variable_moves_the_structure() {
        for flex_first in [true, false] {
            let mut uf = UnionFind::new();
            let rec = record_of(&mut uf, 500);
            let a = uf.fresh_flex();
            let before = uf.record_copies;
            let res = if flex_first {
                uf.unify(a, rec)
            } else {
                uf.unify(rec, a)
            };
            assert!(res.is_ok());
            assert_eq!(uf.record_copies, before, "a record field map was copied");
            assert!(matches!(
                uf.content(a),
                Content::Structure(FlatTy::Record(fs, None)) if fs.len() == 500
            ));
            assert_eq!(uf.find(a), uf.find(rec));
        }
    }

    /// The occurs check still refuses an infinite type through the moving
    /// path, and commits both sides to `Error`.
    #[test]
    fn occurs_failure_still_commits_error() {
        let mut uf = UnionFind::new();
        let a = uf.fresh_flex();
        let list = uf.fresh(Content::Structure(FlatTy::App(Name::new("List"), vec![a])));
        assert!(uf.unify(a, list).is_err());
        assert!(matches!(uf.content(a), Content::Error));
        assert!(matches!(uf.content(list), Content::Error));
    }

    /// The occurs check's reused marks: each call sees only its own visits,
    /// across many calls and across a wrap of the epoch counter.
    #[test]
    fn occurs_marks_are_per_call() {
        let mut uf = UnionFind::new();
        let a = uf.fresh_flex();
        let b = uf.fresh_flex();
        let pair = uf.fresh(Content::Structure(FlatTy::Tuple(vec![a, a])));
        for _ in 0..3 {
            assert!(uf.occurs(pair, a));
            assert!(!uf.occurs(pair, b));
        }
        uf.occurs_scratch.epoch = u32::MAX - 1;
        for _ in 0..4 {
            assert!(uf.occurs(pair, a));
            assert!(!uf.occurs(pair, b));
        }
        assert!(uf.occurs_scratch.epoch >= 1);
        // A clone carries no marks and still answers the same.
        let mut c = uf.clone();
        assert!(c.occurs(pair, a));
        assert!(!c.occurs(pair, b));
    }
}

#[cfg(test)]
mod sparse_reservation_storage_tests {
    use super::*;

    /// The real D2 pattern reserves a wide ground record at each update.
    /// Count physical slots, independently of inference work counters.
    #[test]
    fn ground_reservations_preserve_ids_without_phantom_storage() {
        for width in [128usize, 256] {
            let mut uf = UnionFind::new();
            let reserved_per_update = 2 * width + 1;
            for update in 0..width {
                uf.reserve_unreachable(reserved_per_update);
                let actual = uf.fresh_flex();
                assert_eq!(
                    actual.0 as usize,
                    update * (reserved_per_update + 1) + reserved_per_update
                );
            }
            assert_eq!(uf.var_count(), width * (reserved_per_update + 1));
            assert_eq!(
                uf.slots.as_slice().len(),
                width,
                "unreachable reservations consumed physical UF slots"
            );
        }
    }
}

#[cfg(test)]
mod sparse_reservation_equivalence_tests {
    use super::*;

    // Allocate the holes physically in the oracle, just as the original UF did.
    fn gap(sparse: &mut UnionFind, dense: &mut UnionFind, count: usize) {
        sparse.reserve_unreachable(count);
        for _ in 0..count {
            dense.fresh_flex();
        }
        assert_eq!(sparse.var_count(), dense.var_count());
    }
    fn fresh_pair(sparse: &mut UnionFind, dense: &mut UnionFind, content: Content) -> TyVarId {
        let id = sparse.fresh(content.clone());
        assert_eq!(id, dense.fresh(content));
        id
    }
    fn compare(sparse: &mut UnionFind, dense: &mut UnionFind, ids: &[TyVarId]) {
        assert_eq!(sparse.var_count(), dense.var_count());
        for &id in ids {
            let sr = sparse.find(id);
            let dr = dense.find(id);
            assert_eq!(sr, dr);
            assert_eq!(sparse.rank(sr), dense.rank(dr));
            assert_eq!(
                format!("{:?}", sparse.content(id)),
                format!("{:?}", dense.content(id))
            );
        }
    }
    #[test]
    fn reserved_ids_preserve_dense_unification_and_clone_results() {
        let mut sparse = UnionFind::new();
        let mut dense = UnionFind::new();
        let int = fresh_pair(
            &mut sparse,
            &mut dense,
            Content::Structure(FlatTy::App(Name::new("Int"), vec![])),
        );
        gap(&mut sparse, &mut dense, 4);
        gap(&mut sparse, &mut dense, 0);
        gap(&mut sparse, &mut dense, 7); // adjacent reservations coalesce
        let a = fresh_pair(&mut sparse, &mut dense, Content::Flex);
        let b = fresh_pair(&mut sparse, &mut dense, Content::Flex);
        gap(&mut sparse, &mut dense, 3);
        let tuple = fresh_pair(
            &mut sparse,
            &mut dense,
            Content::Structure(FlatTy::Tuple(vec![a, int])),
        );
        let row = fresh_pair(&mut sparse, &mut dense, Content::Flex);
        let open = fresh_pair(
            &mut sparse,
            &mut dense,
            Content::Structure(FlatTy::Record(
                BTreeMap::from([(Name::new("x"), a)]),
                Some(row),
            )),
        );
        gap(&mut sparse, &mut dense, 19);
        let closed = fresh_pair(
            &mut sparse,
            &mut dense,
            Content::Structure(FlatTy::Record(
                BTreeMap::from([(Name::new("x"), int), (Name::new("y"), b)]),
                None,
            )),
        );
        let fun = fresh_pair(
            &mut sparse,
            &mut dense,
            Content::Structure(FlatTy::Fun(tuple, open)),
        );
        let f = fresh_pair(&mut sparse, &mut dense, Content::Flex);
        let ids = [int, a, b, tuple, row, open, closed, fun, f];
        for (left, right) in [(a, int), (open, closed), (f, fun)] {
            assert_eq!(
                sparse.unify(left, right).is_ok(),
                dense.unify(left, right).is_ok()
            );
            compare(&mut sparse, &mut dense, &ids);
        }
        // Clones retain the exact sparse ID space and independent mutations.
        let mut sc = sparse.clone();
        let mut dc = dense.clone();
        gap(&mut sc, &mut dc, 11);
        let infinite = fresh_pair(&mut sc, &mut dc, Content::Flex);
        let list = fresh_pair(
            &mut sc,
            &mut dc,
            Content::Structure(FlatTy::App(Name::new("List"), vec![infinite])),
        );
        assert!(sc.unify(infinite, list).is_err());
        assert!(dc.unify(infinite, list).is_err());
        compare(&mut sc, &mut dc, &[infinite, list]);
        compare(&mut sparse, &mut dense, &ids);
    }

    #[test]
    fn sparse_occurs_marks_follow_live_slots_and_epoch_wrap() {
        let mut uf = UnionFind::new();
        uf.reserve_unreachable(1000);
        let a = uf.fresh_flex();
        uf.reserve_unreachable(2000);
        let b = uf.fresh_flex();
        let pair = uf.fresh(Content::Structure(FlatTy::Tuple(vec![a, a])));
        uf.occurs_scratch.epoch = u32::MAX - 1;
        for _ in 0..4 {
            assert!(uf.occurs(pair, a));
            assert!(!uf.occurs(pair, b));
        }
        assert_eq!(uf.occurs_scratch.mark.len(), 3);
        let mut clone = uf.clone();
        assert!(clone.occurs(pair, a));
        assert!(!clone.occurs(pair, b));
        assert_eq!(clone.occurs_scratch.mark.len(), 3);
    }

    #[test]
    fn logical_id_exhaustion_is_checked_before_storage_or_literal_publication() {
        let mut uf = UnionFind::new();
        uf.reserve_unreachable(u32::MAX as usize);
        if usize::BITS > 32 {
            // The final representable ID is valid on hosts that can store its
            // one-past count. This allocates exactly one physical slot.
            assert_eq!(uf.fresh_flex(), TyVarId(u32::MAX));
            assert_eq!(uf.slots.as_slice().len(), 1);
        }
        let count = uf.var_count();
        let stored = uf.slots.as_slice().len();
        let literals = uf.int_literals.len();
        let rejected = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            uf.fresh(Content::FlexSuper(SuperType::IntLit));
        }));
        assert!(rejected.is_err());
        assert_eq!(uf.var_count(), count);
        assert_eq!(uf.slots.as_slice().len(), stored);
        assert_eq!(uf.int_literals.len(), literals);
    }

    #[test]
    #[should_panic(expected = "reserved unreachable type-variable ID was accessed")]
    fn reserved_holes_cannot_alias_live_slots() {
        let mut uf = UnionFind::new();
        uf.fresh_flex();
        uf.reserve_unreachable(5);
        uf.fresh_flex();
        uf.content(TyVarId(3));
    }
}

#[cfg(test)]
mod open_access_consumption_tests {
    use super::*;

    fn access(uf: &mut UnionFind, base: TyVarId, field: &str, fast: bool) -> TyVarId {
        let value = uf.fresh_flex();
        let row = uf.fresh_flex();
        let fields = BTreeMap::from([(Name::new(field), value)]);
        let rec = uf.fresh(Content::Structure(FlatTy::Record(
            fields.clone(),
            Some(row),
        )));
        if fast {
            let result = uf
                .unify_closed_record_fresh_row(base, rec, row, &fields, true)
                .unwrap_or_else(|| uf.unify(base, rec));
            assert!(result.is_ok());
        } else {
            assert!(uf.unify(base, rec).is_ok());
        }
        // As in Infer::infer_expr_inner Access, only the extracted value is
        // published. The constraint and its new extension never escape.
        value
    }

    fn open(uf: &mut UnionFind, row_content: Content) -> (TyVarId, TyVarId) {
        let row = uf.fresh(row_content);
        let base = uf.fresh(Content::Structure(FlatTy::Record(
            BTreeMap::new(),
            Some(row),
        )));
        (base, row)
    }

    fn same(fast: &UnionFind, slow: &UnionFind, live: &[TyVarId]) {
        // Snapshot inspection must not change either tested inference run.
        let (mut fast, mut slow) = (fast.clone(), slow.clone());
        assert_eq!(fast.var_count(), slow.var_count());
        assert_eq!(format!("{:?}", fast.pending), format!("{:?}", slow.pending));
        assert_eq!(
            format!("{:?}", fast.rigid_needs),
            format!("{:?}", slow.rigid_needs)
        );
        assert_eq!(
            format!("{:?}", fast.rigid_missing),
            format!("{:?}", slow.rigid_missing)
        );
        for &id in live {
            let fr = fast.find(id);
            let sr = slow.find(id);
            assert_eq!(fr, sr, "representative of {id:?}");
            assert_eq!(fast.rank(fr), slow.rank(sr), "rank of {id:?}");
            assert_eq!(
                format!("{:?}", fast.content(id)),
                format!("{:?}", slow.content(id)),
                "content of {id:?}"
            );
        }
    }

    /// Test-only RED on the original implementation: all accesses succeed,
    /// but copying the accumulated open row causes this final assertion to fail.
    #[test]
    fn repeated_open_singleton_access_does_not_copy_accumulated_fields() {
        let mut uf = UnionFind::new();
        let (base, _) = open(&mut uf, Content::Flex);
        for i in 0..128 {
            access(&mut uf, base, &format!("f{i}"), true);
        }
        for i in 0..128 {
            access(&mut uf, base, &format!("f{i}"), true);
        }
        assert_eq!(uf.record_copies, 0, "open Access copied accumulated fields");
    }

    #[test]
    fn consumed_access_matches_live_dense_roots_aliases_and_late_resolution() {
        let mut fast = UnionFind::new();
        let mut slow = UnionFind::new();
        let (base, old_row) = open(&mut fast, Content::Flex);
        assert_eq!((base, old_row), open(&mut slow, Content::Flex));
        let alias = fast.fresh_flex();
        assert_eq!(alias, slow.fresh_flex());
        assert!(fast.unify(old_row, alias).is_ok());
        assert!(slow.unify(old_row, alias).is_ok());
        let mut live = vec![base, old_row, alias];
        // Missing and existing fields; the initial old extension has a live
        // external alias and must retain its singleton constraints and rank.
        for field in ["a", "b", "a", "c", "b", "d"] {
            let f = access(&mut fast, base, field, true);
            assert_eq!(f, access(&mut slow, base, field, false));
            live.push(f);
            let Content::Structure(FlatTy::Record(_, Some(ext))) = fast.content(base) else {
                panic!("open base")
            };
            live.push(ext);
            same(&fast, &slow, &live);
        }
        let ext = *live.last().unwrap();
        let int = fast.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
        assert_eq!(
            int,
            slow.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])))
        );
        let tail = fast.fresh(Content::Structure(FlatTy::Record(
            BTreeMap::from([(Name::new("tail"), int)]),
            None,
        )));
        assert_eq!(
            tail,
            slow.fresh(Content::Structure(FlatTy::Record(
                BTreeMap::from([(Name::new("tail"), int)]),
                None
            )))
        );
        assert!(fast.unify(ext, tail).is_ok());
        assert!(slow.unify(ext, tail).is_ok());
        live.extend([int, tail]);
        same(&fast, &slow, &live);
        // A resolved extension must fall back through normalization, retaining
        // the newly supplied field and eventually closing the full record.
        let f = access(&mut fast, base, "tail", true);
        assert_eq!(f, access(&mut slow, base, "tail", false));
        live.push(f);
        same(&fast, &slow, &live);
    }

    #[test]
    fn consumed_access_preserves_later_infinite_rigid_and_mismatch_errors() {
        for kind in 0..3 {
            let mut fast = UnionFind::new();
            let mut slow = UnionFind::new();
            let (base, row) = open(&mut fast, Content::Flex);
            assert_eq!((base, row), open(&mut slow, Content::Flex));
            let f = access(&mut fast, base, "a", true);
            assert_eq!(f, access(&mut slow, base, "a", false));
            let g = access(&mut fast, base, "a", true);
            assert_eq!(g, access(&mut slow, base, "a", false));
            let (left, right) = match kind {
                0 => (f, base), // {a : itself} must still fail occurs.
                1 => {
                    let rigid = fast.fresh(Content::Rigid(Name::new("r")));
                    assert_eq!(rigid, slow.fresh(Content::Rigid(Name::new("r"))));
                    (base, rigid)
                }
                _ => {
                    let int = fast.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
                    assert_eq!(
                        int,
                        slow.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])))
                    );
                    assert!(fast.unify(f, int).is_ok());
                    assert!(slow.unify(f, int).is_ok());
                    let string =
                        fast.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
                    assert_eq!(
                        string,
                        slow.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])))
                    );
                    (g, string)
                }
            };
            let fe = fast.unify(left, right).expect_err("same refusal").message;
            let se = slow.unify(left, right).expect_err("same refusal").message;
            assert_eq!(fe, se);
            same(&fast, &slow, &[base, row, f, g, left, right]);
        }
    }

    #[test]
    fn non_plain_and_resolved_rows_keep_the_general_path() {
        for content in [
            Content::Rigid(Name::new("r")),
            Content::FlexSuper(SuperType::Number),
            Content::Error,
            Content::Structure(FlatTy::Record(BTreeMap::new(), None)),
        ] {
            let mut uf = UnionFind::new();
            let (base, _) = open(&mut uf, content);
            let value = uf.fresh_flex();
            let row = uf.fresh_flex();
            let fields = BTreeMap::from([(Name::new("a"), value)]);
            let rec = uf.fresh(Content::Structure(FlatTy::Record(
                fields.clone(),
                Some(row),
            )));
            assert!(uf
                .unify_closed_record_fresh_row(base, rec, row, &fields, true)
                .is_none());
        }
    }
    #[test]
    fn held_old_content_and_raw_row_republication_preserve_late_constraints() {
        for existing in [false, true] {
            let (mut fast, mut slow) = (UnionFind::new(), UnionFind::new());
            let int = fast.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
            assert_eq!(
                int,
                slow.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])))
            );
            let old_row = fast.fresh_flex();
            assert_eq!(old_row, slow.fresh_flex());
            let base = fast.fresh(Content::Structure(FlatTy::Record(
                BTreeMap::from([(Name::new("a"), int)]),
                Some(old_row),
            )));
            assert_eq!(
                base,
                slow.fresh(Content::Structure(FlatTy::Record(
                    BTreeMap::from([(Name::new("a"), int)]),
                    Some(old_row)
                )))
            );
            let mut live = vec![base, old_row, int];
            // Give the base external aliases and rank2 using real unions.
            for _ in 0..2 {
                let a = fast.fresh_flex();
                assert_eq!(a, slow.fresh_flex());
                let b = fast.fresh_flex();
                assert_eq!(b, slow.fresh_flex());
                assert!(fast.unify(a, b).is_ok());
                assert!(slow.unify(a, b).is_ok());
                assert!(fast.unify(base, a).is_ok());
                assert!(slow.unify(base, a).is_ok());
                live.extend([a, b]);
            }
            // Hold an unpublished owned Content copy and a raw OLD row ID
            // across consumption. Neither may lose a later constraint.
            let held_fast = fast.content(base);
            let held_slow = slow.content(base);
            let value = access(&mut fast, base, if existing { "a" } else { "b" }, true);
            assert_eq!(
                value,
                access(&mut slow, base, if existing { "a" } else { "b" }, false)
            );
            live.push(value);
            same(&fast, &slow, &live);
            let published = fast.fresh(held_fast);
            assert_eq!(published, slow.fresh(held_slow));
            // Publish a SECOND raw incoming old-row edge only AFTER the fast
            // path. The new Access row is not this externally retained row.
            let raw = fast.fresh(Content::Structure(FlatTy::Record(
                BTreeMap::new(),
                Some(old_row),
            )));
            assert_eq!(
                raw,
                slow.fresh(Content::Structure(FlatTy::Record(
                    BTreeMap::new(),
                    Some(old_row)
                )))
            );
            let string = fast.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
            assert_eq!(
                string,
                slow.fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])))
            );
            let mut tail_fields = BTreeMap::from([(Name::new("z"), string)]);
            if !existing {
                tail_fields.insert(Name::new("b"), string);
            }
            let tail = fast.fresh(Content::Structure(FlatTy::Record(
                tail_fields.clone(),
                None,
            )));
            assert_eq!(
                tail,
                slow.fresh(Content::Structure(FlatTy::Record(tail_fields, None)))
            );
            assert!(fast.unify(old_row, tail).is_ok());
            assert!(slow.unify(old_row, tail).is_ok());
            live.extend([published, raw, string, tail]);
            same(&fast, &slow, &live);
            for record in [published, raw] {
                let z = access(&mut fast, record, "z", true);
                assert_eq!(z, access(&mut slow, record, "z", false));
                live.push(z);
                assert_eq!(fast.find(z), fast.find(string));
                same(&fast, &slow, &live);
            }
            let boolean = fast.fresh(Content::Structure(FlatTy::App(Name::new("Bool"), vec![])));
            assert_eq!(
                boolean,
                slow.fresh(Content::Structure(FlatTy::App(Name::new("Bool"), vec![])))
            );
            let fe = fast
                .unify(value, boolean)
                .expect_err("late mismatch")
                .message;
            let se = slow
                .unify(value, boolean)
                .expect_err("late mismatch")
                .message;
            assert_eq!(fe, se);
            live.push(boolean);
            same(&fast, &slow, &live);
        }
    }

    #[test]
    fn multi_field_and_update_constraints_are_not_consumed_as_singleton_accesses() {
        for (count, fresh_values) in [(1usize, false), (2, true)] {
            let mut uf = UnionFind::new();
            let (base, _) = open(&mut uf, Content::Flex);
            let mut fields = BTreeMap::new();
            for i in 0..count {
                fields.insert(Name::new(&format!("f{i}")), uf.fresh_flex());
            }
            let row = uf.fresh_flex();
            let rec = uf.fresh(Content::Structure(FlatTy::Record(
                fields.clone(),
                Some(row),
            )));
            let before = uf.var_count();
            assert!(uf
                .unify_closed_record_fresh_row(base, rec, row, &fields, fresh_values)
                .is_none());
            assert_eq!(
                uf.var_count(),
                before,
                "refusal must not allocate shared rest/complements"
            );
            assert!(uf.unify(base, rec).is_ok());
        }
    }
}

#[cfg(test)]
mod accessor_export_tests {
    use super::*;

    fn accessor(uf: &mut UnionFind, fast: bool) -> (TyVarId, TyVarId, TyVarId) {
        let f = if fast {
            uf.fresh_accessor(Name::new("a"))
        } else {
            uf.fresh_accessor_reference(Name::new("a"))
        };
        let Content::Structure(FlatTy::Fun(rec, value)) = uf.content(f) else {
            panic!("function")
        };
        (f, rec, value)
    }
    fn closed(uf: &mut UnionFind, width: usize) -> TyVarId {
        let mut fields = BTreeMap::new();
        for i in 0..width {
            let value = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
            fields.insert(
                Name::new(&if i == 0 {
                    "a".to_string()
                } else {
                    format!("extra{i}")
                }),
                value,
            );
        }
        uf.fresh(Content::Structure(FlatTy::Record(fields, None)))
    }
    fn close(
        uf: &mut UnionFind,
        rec: TyVarId,
        wide: TyVarId,
        reverse: bool,
    ) -> Result<(), Mismatch> {
        if reverse {
            uf.unify(wide, rec)
        } else {
            uf.unify(rec, wide)
        }
    }
    fn later_extra(uf: &mut UnionFind, record: TyVarId) -> Result<(), Mismatch> {
        let value = uf.fresh_flex();
        let row = uf.fresh_flex();
        let need = uf.fresh(Content::Structure(FlatTy::Record(
            BTreeMap::from([(Name::new("extra1"), value)]),
            Some(row),
        )));
        uf.unify(record, need)
    }
    fn state(uf: &mut UnionFind, roots: &[TyVarId], all_content: bool) -> String {
        let mut out = format!(
            "count={} pending={:?} needs={:?} missing={:?} literals={:?}",
            uf.var_count(),
            uf.pending,
            uf.rigid_needs,
            uf.rigid_missing,
            uf.int_literals
        );
        for i in 0..uf.var_count() {
            let id = TyVarId(i as u32);
            let root = uf.find(id);
            out.push_str(&format!("|{id:?}:{root:?}:{}", uf.rank(root)));
            if all_content || roots.contains(&id) {
                // Borrowed formatting exports no Content or row IDs.
                out.push_str(&format!(":{:?}", uf.slots[root.0 as usize]));
            }
        }
        out
    }
    fn outcome(r: Result<(), Mismatch>) -> Result<(), String> {
        r.map_err(|e| e.message)
    }

    /// These use only the original UF API after factory construction. On the
    /// parked unsound prototype they lose the late extra field after closure.
    #[test]
    fn held_record_copy_republished_after_close_preserves_late_fields() {
        for reverse in [false, true] {
            let mut results = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (_, rec, _) = accessor(&mut uf, fast);
                let held = uf.content(rec);
                let wide = closed(&mut uf, 4);
                close(&mut uf, rec, wide, reverse).unwrap();
                let published = uf.fresh(held);
                later_extra(&mut uf, published).unwrap();
                results.push(state(&mut uf, &[], true));
            }
            assert_eq!(results[0], results[1]);
        }
    }
    #[test]
    fn snapshot_held_before_close_protects_source_and_snapshot() {
        for reverse in [false, true] {
            let mut results = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (_, rec, _) = accessor(&mut uf, fast);
                let mut snapshot = uf.clone();
                let wide = closed(&mut uf, 4);
                close(&mut uf, rec, wide, reverse).unwrap();
                let published = uf.fresh(snapshot.content(rec));
                later_extra(&mut uf, published).unwrap();
                results.push(state(&mut uf, &[], true));
                // Independently use the snapshot, not just original's side.
                let wide = closed(&mut snapshot, 4);
                close(&mut snapshot, rec, wide, reverse).unwrap();
                assert!(later_extra(&mut snapshot, rec).is_ok());
            }
            assert_eq!(results[0], results[1]);
        }
    }
    #[test]
    fn normalized_raw_row_republished_after_close_preserves_late_fields() {
        let mut results = Vec::new();
        for fast in [true, false] {
            let mut uf = UnionFind::new();
            let (_, rec, _) = accessor(&mut uf, fast);
            let (fields, row) = uf.normalize_record(BTreeMap::new(), Some(rec));
            assert!(row.is_some());
            let wide = closed(&mut uf, 4);
            close(&mut uf, rec, wide, false).unwrap();
            let published = uf.fresh(Content::Structure(FlatTy::Record(fields, row)));
            later_extra(&mut uf, published).unwrap();
            results.push(state(&mut uf, &[], true));
        }
        assert_eq!(results[0], results[1]);
    }
    #[test]
    fn unexported_accessor_preserves_all_ids_ranks_live_roots_and_failure_order() {
        for reverse in [false, true] {
            for field in [
                Content::Flex,
                Content::Rigid(Name::new("r")),
                Content::FlexSuper(SuperType::Number),
                Content::Error,
                Content::Structure(FlatTy::App(Name::new("String"), vec![])),
            ] {
                let mut runs = Vec::new();
                for fast in [true, false] {
                    let mut uf = UnionFind::new();
                    let (f, rec, value) = accessor(&mut uf, fast);
                    uf.set_content(value, field.clone());
                    // Structure-preserving owner movement through a Flex root.
                    let moved = uf.fresh_flex();
                    uf.unify(moved, rec).unwrap();
                    let wide = closed(&mut uf, 8);
                    let result = outcome(close(&mut uf, moved, wide, reverse));
                    let live = [f, rec, value, moved, wide];
                    runs.push((result, state(&mut uf, &live, false)));
                }
                assert_eq!(runs[0], runs[1], "reverse={reverse}, field={field:?}");
            }
        }
    }
    #[test]
    fn row_publication_mutation_and_generic_merge_revoke_before_late_constraints() {
        for mode in 0..4 {
            let mut runs = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (_, rec, _) = accessor(&mut uf, fast);
                // Real raw export, followed by each otherwise independent
                // mutation/publication route. Export itself must revoke first.
                let Content::Structure(FlatTy::Record(_, Some(row))) = uf.content(rec) else {
                    panic!("row")
                };
                let alias = match mode {
                    0 => uf.fresh(Content::Structure(FlatTy::Record(
                        BTreeMap::new(),
                        Some(row),
                    ))),
                    1 => {
                        let x = uf.fresh_flex();
                        uf.set_content(
                            x,
                            Content::Structure(FlatTy::Record(BTreeMap::new(), Some(row))),
                        );
                        x
                    }
                    2 => {
                        let x = uf.fresh_flex();
                        uf.unify(row, x).unwrap();
                        uf.fresh(Content::Structure(FlatTy::Record(BTreeMap::new(), Some(x))))
                    }
                    _ => {
                        let x = uf.fresh_flex();
                        uf.unify(x, row).unwrap();
                        uf.fresh(Content::Structure(FlatTy::Record(BTreeMap::new(), Some(x))))
                    }
                };
                let wide = closed(&mut uf, 4);
                close(&mut uf, rec, wide, false).unwrap();
                later_extra(&mut uf, alias).unwrap();
                let bad = uf.fresh(Content::Structure(FlatTy::Fun(row, row)));
                let result = outcome(uf.unify(row, bad));
                assert!(result.is_err());
                runs.push((result, state(&mut uf, &[], true)));
            }
            assert_eq!(runs[0], runs[1], "mode={mode}");
        }
    }
    #[test]
    fn snapshot_disable_survives_clear_rollback_and_new_factories() {
        fn send_sync<T: Send + Sync>() {}
        send_sync::<UnionFind>();
        let mut uf = UnionFind::new();
        let _ = accessor(&mut uf, true);
        let snapshot = uf.clone();
        for mut graph in [uf, snapshot] {
            graph.revoke_accessor_rows();
            let (_, rec, _) = accessor(&mut graph, true);
            assert!(
                graph.accessor_rows.disabled(),
                "snapshot disable survives clear/rollback"
            );
            assert!(
                !graph.accessor_rows.active(),
                "a new factory cannot mint after snapshot"
            );
            let held = graph.content(rec);
            let wide = closed(&mut graph, 4);
            close(&mut graph, rec, wide, false).unwrap();
            let alias = graph.fresh(held);
            later_extra(&mut graph, alias).unwrap();
            assert!(!graph.accessor_rows.active());
        }
    }
    #[test]
    fn unexported_wide_accessor_does_not_copy_dead_complement() {
        let mut copies = Vec::new();
        for fast in [true, false] {
            let mut uf = UnionFind::new();
            let (_, rec, _) = accessor(&mut uf, fast);
            let wide = closed(&mut uf, 128);
            close(&mut uf, rec, wide, false).unwrap();
            copies.push(uf.record_copies_for_test());
        }
        assert!(
            copies[0] * 2 < copies[1],
            "fast={} reference={}",
            copies[0],
            copies[1]
        );
    }
    #[test]
    fn raw_mutation_and_publication_each_withdraw_factory_authority() {
        for mode in 0..6 {
            let mut uf = UnionFind::new();
            let (_, rec, _) = accessor(&mut uf, true);
            // Test-only borrowed inspection isolates individual revocation
            // boundaries. No production factory exposes this raw row.
            let row = match &uf.slots[rec.0 as usize] {
                Slot::Root {
                    content: Content::Structure(FlatTy::Record(_, Some(row))),
                    ..
                } => *row,
                _ => panic!("singleton"),
            };
            assert!(uf.accessor_rows.contains(&row));
            match mode {
                0 => {
                    uf.fresh(Content::Structure(FlatTy::Tuple(vec![row])));
                }
                1 => {
                    let x = uf.fresh_flex();
                    uf.set_content(x, Content::Structure(FlatTy::Fun(row, row)));
                }
                2 => {
                    uf.unify(row, row).unwrap();
                }
                3 => {
                    uf.set_content(row, Content::Flex);
                }
                4 => {
                    let x = uf.fresh_flex();
                    uf.unify(row, x).unwrap();
                }
                _ => {
                    let x = uf.fresh_flex();
                    uf.unify(x, row).unwrap();
                }
            }
            assert!(!uf.accessor_rows.contains(&row), "boundary {mode}");
        }
    }
    #[test]
    fn shared_field_occurs_failure_keeps_dense_publication_and_allocation_order() {
        for reverse in [false, true] {
            let mut runs = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (f, rec, value) = accessor(&mut uf, fast);
                let recursive = uf.fresh(Content::Structure(FlatTy::Fun(rec, value)));
                let other = uf.fresh_flex();
                let wide = uf.fresh(Content::Structure(FlatTy::Record(
                    BTreeMap::from([(Name::new("a"), recursive), (Name::new("extra1"), other)]),
                    None,
                )));
                let before = uf.var_count();
                let err = outcome(close(&mut uf, rec, wide, reverse));
                assert!(err.as_ref().is_err_and(|s| s.contains("infinite type")));
                assert_eq!(
                    uf.var_count(),
                    before,
                    "fallible shared field precedes complement allocation"
                );
                runs.push((
                    err,
                    state(&mut uf, &[f, rec, value, wide, recursive, other], true),
                ));
            }
            assert_eq!(runs[0], runs[1]);
        }
    }
    fn open_base(uf: &mut UnionFind, existing: bool) -> (TyVarId, TyVarId) {
        let row = uf.fresh_flex();
        let mut fields = BTreeMap::new();
        for name in if existing {
            vec!["a", "extra1", "other"]
        } else {
            vec!["extra1", "other"]
        } {
            let v = uf.fresh(Content::Structure(FlatTy::App(Name::new("Int"), vec![])));
            fields.insert(Name::new(name), v);
        }
        (
            uf.fresh(Content::Structure(FlatTy::Record(fields, Some(row)))),
            row,
        )
    }
    fn born_row(uf: &UnionFind, rec: TyVarId) -> TyVarId {
        match &uf.slots[rec.0 as usize] {
            Slot::Root {
                content: Content::Structure(FlatTy::Record(_, Some(row))),
                ..
            } => *row,
            _ => panic!("new accessor record"),
        }
    }
    fn live_except(uf: &mut UnionFind, dead: &[TyVarId]) -> String {
        let dead: Vec<_> = dead.iter().map(|id| uf.find(*id)).collect();
        let live: Vec<_> = (0..uf.var_count())
            .map(|i| TyVarId(i as u32))
            .filter(|id| !dead.contains(&uf.find(*id)))
            .collect();
        state(uf, &live, false)
    }
    fn request(uf: &mut UnionFind, base: TyVarId, field: &str) -> Result<(), Mismatch> {
        let v = uf.fresh_flex();
        let row = uf.fresh_flex();
        let rec = uf.fresh(Content::Structure(FlatTy::Record(
            BTreeMap::from([(Name::new(field), v)]),
            Some(row),
        )));
        uf.unify(base, rec)
    }
    #[test]
    fn open_consumption_preserves_every_live_node_old_row_and_late_constraints() {
        for reverse in [false, true] {
            for existing in [false, true] {
                for ranked in [false, true] {
                    let mut runs = Vec::new();
                    for fast in [true, false] {
                        let mut uf = UnionFind::new();
                        let (base, old) = open_base(&mut uf, existing);
                        if ranked {
                            let alias = uf.fresh_flex();
                            uf.unify(old, alias).unwrap();
                        }
                        // This copy is deliberately held before the new accessor is
                        // born. Its OLD extension must retain ordinary constraints.
                        let held = uf.content(base);
                        let (_, rec, _) = accessor(&mut uf, fast);
                        let dead = born_row(&uf, rec);
                        let result = outcome(close(&mut uf, rec, base, reverse));
                        assert!(result.is_ok());
                        let before_late = live_except(&mut uf, &[dead]);
                        let alias = uf.fresh(held);
                        if !existing {
                            request(&mut uf, alias, "a").unwrap();
                        }
                        let need =
                            uf.fresh(Content::Structure(FlatTy::Record(BTreeMap::new(), None)));
                        let late = outcome(uf.unify(old, need));
                        runs.push((result, before_late, late, live_except(&mut uf, &[dead])));
                    }
                    assert_eq!(
                        runs[0], runs[1],
                        "reverse={reverse} existing={existing} ranked={ranked}"
                    );
                }
            }
        }
    }
    #[test]
    fn exported_open_accessor_rows_keep_held_copy_snapshot_and_normalized_aliases() {
        for mode in 0..3 {
            for reverse in [false, true] {
                let mut runs = Vec::new();
                for fast in [true, false] {
                    let mut uf = UnionFind::new();
                    let (base, _) = open_base(&mut uf, false);
                    let (_, rec, _) = accessor(&mut uf, fast);
                    let held = match mode {
                        0 => uf.content(rec),
                        1 => {
                            let mut snapshot = uf.clone();
                            snapshot.content(rec)
                        }
                        _ => {
                            let (fs, ext) = uf.normalize_record(BTreeMap::new(), Some(rec));
                            Content::Structure(FlatTy::Record(fs, ext))
                        }
                    };
                    close(&mut uf, rec, base, reverse).unwrap();
                    let alias = uf.fresh(held);
                    later_extra(&mut uf, alias).unwrap();
                    runs.push(state(&mut uf, &[], true));
                }
                assert_eq!(runs[0], runs[1], "mode={mode} reverse={reverse}");
            }
        }
    }
    #[test]
    fn open_shared_field_failure_publishes_rest_but_no_complements() {
        for reverse in [false, true] {
            let mut runs = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (base, _) = open_base(&mut uf, true);
                let (_, rec, value) = accessor(&mut uf, fast);
                uf.set_content(
                    value,
                    Content::Structure(FlatTy::App(Name::new("String"), vec![])),
                );
                let before = uf.var_count();
                let result = outcome(close(&mut uf, rec, base, reverse));
                assert!(result.is_err());
                assert_eq!(
                    uf.var_count(),
                    before + 1,
                    "only shared rest allocated before field failure"
                );
                runs.push((result, state(&mut uf, &[], true)));
            }
            assert_eq!(runs[0], runs[1]);
        }
    }
    #[test]
    fn open_old_row_occurs_failure_is_ignored_in_exact_complement_order() {
        for reverse in [false, true] {
            let mut runs = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (base, old) = open_base(&mut uf, false);
                let (_, rec, value) = accessor(&mut uf, fast);
                let dead = born_row(&uf, rec);
                // Preserve OLD as a literal plain-Flex root (rank1), while
                // making the meaningful old complement actually recursive.
                uf.unify(old, value).unwrap();
                let before = uf.var_count();
                let result = outcome(close(&mut uf, rec, base, reverse));
                assert!(
                    result.is_ok(),
                    "generic record merging ignores row complement errors"
                );
                assert_eq!(
                    uf.var_count(),
                    before + 3,
                    "rest and BOTH complements in e1/e2 order"
                );
                assert!(matches!(uf.content(old), Content::Error));
                runs.push((result, live_except(&mut uf, &[dead])));
            }
            assert_eq!(runs[0], runs[1], "reverse={reverse}");
        }
    }
    #[test]
    fn unsupported_open_extension_kinds_and_linked_roots_use_full_dense_fallback() {
        for mode in 0..5 {
            for reverse in [false, true] {
                let mut runs = Vec::new();
                for fast in [true, false] {
                    let mut uf = UnionFind::new();
                    let (base, old) = open_base(&mut uf, false);
                    match mode {
                        0 => uf.set_content(old, Content::Rigid(Name::new("r"))),
                        1 => uf.set_content(old, Content::FlexSuper(SuperType::Encodable)),
                        2 => uf.set_content(old, Content::Error),
                        3 => uf.set_content(
                            old,
                            Content::Structure(FlatTy::Record(BTreeMap::new(), None)),
                        ),
                        _ => {
                            let alias = uf.fresh_flex();
                            uf.unify(alias, old).unwrap();
                        }
                    }
                    let (_, rec, _) = accessor(&mut uf, fast);
                    let result = outcome(close(&mut uf, rec, base, reverse));
                    runs.push((result, state(&mut uf, &[], true)));
                }
                assert_eq!(runs[0], runs[1], "mode={mode} reverse={reverse}");
            }
        }
    }
    #[test]
    fn two_unexported_singleton_owners_keep_both_function_aliases_exact() {
        for reverse in [false, true] {
            let mut runs = Vec::new();
            for fast in [true, false] {
                let mut uf = UnionFind::new();
                let (f1, r1, _) = accessor(&mut uf, fast);
                let row1 = born_row(&uf, r1);
                let f2 = if fast {
                    uf.fresh_accessor(Name::new("other"))
                } else {
                    uf.fresh_accessor_reference(Name::new("other"))
                };
                let Content::Structure(FlatTy::Fun(r2, _)) = uf.content(f2) else {
                    panic!("function")
                };
                let row2 = born_row(&uf, r2);
                close(&mut uf, r1, r2, reverse).unwrap();
                // ra's singleton grant is selected first; the OTHER old row
                // stays in the full live-content oracle (never hide both).
                let dead = if reverse { row2 } else { row1 };
                runs.push(live_except(&mut uf, &[dead]));
                assert!(matches!(
                    uf.content(f1),
                    Content::Structure(FlatTy::Fun(_, _))
                ));
            }
            assert_eq!(runs[0], runs[1]);
        }
    }
}
