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
use std::collections::BTreeMap;

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
    /// Close the set upwards: `Number ⇒ Comparable ⇒ Encodable`.
    pub fn closure(self) -> SuperType {
        let mut b = self.0;
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
#[derive(Default, Clone)]
pub struct UnionFind {
    slots: Vec<Slot>,
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
        match &mut self.slots[root.0 as usize] {
            Slot::Root { content, .. } => std::mem::replace(content, Content::Flex),
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
        if sc.mark.len() < self.slots.len() {
            sc.mark.resize(self.slots.len(), 0);
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
            let mark = &mut sc.mark[r.0 as usize];
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
                self.union(ra, rb, other);
                return Ok(());
            }
            (Kind::Other, Kind::Flex) => {
                if self.occurs(ra, rb) {
                    return self.infinite(ra, rb);
                }
                let other = self.take_content(ra);
                self.union(ra, rb, other);
                return Ok(());
            }
            (Kind::Other, Kind::Other) => {}
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
            // the same rigid `a`). Distinct skolems clash. (Note: the def-level
            // gate only skolemizes RESULT-position quantifiers; an argument-only
            // quantifier stays flex and BINDS a result rigid via the `(Flex,
            // other)` arm above — accept-parity with the flexible-instantiation
            // oracle. This arm therefore fires only when two *distinct
            // result-position* skolems are forced together by the body.)
            (Content::Rigid(n1), Content::Rigid(n2)) => {
                if n1 == n2 {
                    self.union(ra, rb, Content::Rigid(n1));
                    Ok(())
                } else {
                    Err(Mismatch::new(format!(
                        "rigid type variable `{}` cannot unify with `{}`",
                        n1.as_str(),
                        n2.as_str()
                    )))
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
                    Err(Mismatch::new(rigid_bound_message(n.as_str(), missing)))
                }
            }
            // A skolem vs ANYTHING concrete (Structure / FlexSuper) is a clash:
            // the body forced the declared quantifier to a specific type, so the
            // annotation is strictly more general than the body (audit #5/#6).
            // NOTE: this sits AFTER the Flex arms so a rigid still BINDS a plain
            // flex (the body may keep the quantifier polymorphic).
            (Content::Rigid(n), _) | (_, Content::Rigid(n)) => Err(Mismatch::new(format!(
                "rigid type variable `{}` cannot be unified with a concrete type",
                n.as_str()
            ))),
            (Content::FlexSuper(s1), Content::FlexSuper(s2)) => {
                let s = s1.union(s2).closure();
                if s.is_satisfiable() {
                    self.union(ra, rb, Content::FlexSuper(s));
                    Ok(())
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
    /// `rec`. Returns `None` when the fast form does not apply (the base is not
    /// a closed record holding every field); the caller then runs the general
    /// `unify`.
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

    /// `union(root, child, <root's own content>)` for a `child` that is a
    /// rank-0 root distinct from `root`: `child` links under `root`, whose
    /// rank rises to 1 if it was 0 (union by rank). The content stays.
    fn link_fresh(&mut self, root: TyVarId, child: TyVarId) {
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

    /// Allocate `n` variables nothing refers to: they keep every later id
    /// where an elided allocation of `n` fresh nodes would have put it.
    pub(crate) fn reserve_unreachable(&mut self, n: usize) {
        self.slots.reserve(n);
        for _ in 0..n {
            self.fresh_flex();
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
         `Queue`, `WebSocket`, `WebSocketServer`, `StreamId`, `StreamWriter`, `Cache`) anywhere \
         inside, in either direction (a handle decoded from JSON would be forged). Fix: keep \
         that value out of the stored type, for example in a separate field of the model that \
         is not persisted, or store the data it is made from. See {ENCODABLE_ANCHOR}"
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

        // A skolem CLASHES with a super-constrained flex (`x = 5` forces Number
        // — audit #6).
        let mut uf = UnionFind::new();
        let rigid = uf.fresh(Content::Rigid(Name::new("a")));
        let num = uf.fresh(Content::FlexSuper(SuperType::Number));
        assert!(uf.unify(rigid, num).is_err());
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
