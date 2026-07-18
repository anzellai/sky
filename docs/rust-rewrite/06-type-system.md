# 06 — Type System (the `ty` crate)

Hindley-Milner inference for Sky, rebuilt on the arena + integer-id idioms. This
is where L3 pays off most concretely: **type-variable identity stops being a
pointer and becomes a `TyVarId`** — "the one real design task" the self-host
analysis named (`docs/self-host/00-feasibility-and-architecture.md:112`), solved
for free by the arena. The crate owns HM inference, arena union-find,
generalisation/instantiation, records + row polymorphism, exhaustiveness, and the
**per-region type table** that drives type-directed lowering (`07`).

Depends on `base` (ids, interners, arena), `hir` (name-resolved IR), and
`diagnostics`. Depended on by `skydb` (which exposes `infer` as a query) and
`lower`. `#![forbid(unsafe_code)]`.

## What the current design gets right (and we keep)

The Haskell `Sky.Type.*` tree is a competent elm/compiler-derived HM engine. Four
behaviours are load-bearing soundness and we reproduce them *exactly*, then make
them cheaper or safer by construction:

1. **The wildcard-`any` per-occurrence gate** — `any (/= "any") freeVars`
   (`Constrain/Expression.hs:472,1122,1201`; `Instantiate.hs:43`). Mis-gating
   silently accepts wrong return types (CLAUDE.md "Wildcard-`any` soundness gate").
2. **The FFI interface-satisfaction axiom** — `isFfiInterfacePair` /
   `implementsInterface` (`Unify.hs:108-121,349-381`). This is the soundness the
   `any`-boxed self-host *lacked* (CLAUDE.md Limitation #6).
3. **Strict-HM arity gate (E2007)** — `declaredArity` + `SlotShape` +
   `maybeEmit{Arity,ValueSlot}Mismatch` (`Constrain/Expression.hs:1114-1208,4127`).
4. **Exhaustiveness stronger than GHC-as-configured** — `Exhaustiveness.hs`. A
   real win; kept and, per L6, applied to the compiler's *own* IR.

Two things the Haskell code does that violate our laws, fixed here:

- **`rowExtCounter` is a global `unsafePerformIO` IORef** (`Unify.hs:42-50`)
  naming fresh row-extension vars. That is L1 (global mutable) *and* L4
  (nondeterministic across runs / interleavings). It becomes a counter on the
  inference context, seeded deterministically (L4).
- **`Descriptor._rank/_mark/_copy`** (`Type.hs:42-47`) are vestigial elm/compiler
  let-generalisation-pool machinery — **unused** in Sky, which generalises via
  annotations, not by rank (`docs/self-host/00-feasibility-and-architecture.md:112-114`).
  We drop them. The *only* rank we keep is the union-by-rank weight
  (`UnionFind.hs:33`, `Word32` in `PointInfo`).

## Two levels of type, exactly as today

The Haskell code has two representations and we mirror the split — it is the
right one:

| Level | Haskell | Rust | Mutability |
|---|---|---|---|
| Canonical / immutable | `T.Type` (`Sky.AST.Canonical`) | `Ty` (interned) | frozen, `Copy` id |
| Solver / inference | `Variable = UF.Point Descriptor` (`Type.hs:38`) | `TyVarId` (arena slot) | in-place union-find |

`Ty` is the interned HM type — what flows on constraints, what a region maps to,
what `sky doc`/hover render. `TyVarId` is the transient union-find variable used
*only* during one `infer` query. "Read back" (`variableToType`, `Solve.hs:1428`)
walks the settled arena into an interned `Ty`.

### `Ty` — interned canonical type (`base`/`ty`)

Structural, hash-consed once, compared by 32-bit id (L3). Mirrors
`Sky.AST.Canonical.Type`.

```rust
/// Interned handle. `==` is an integer compare.
#[derive(Copy, Clone, PartialEq, Eq, Hash)]
pub struct Ty(InternId);

/// The interned payload (one per distinct structural type).
#[derive(PartialEq, Eq, Hash)]
pub enum TyData {
    /// Rigid / flexible type variable *name* at the canonical level
    /// (`T.TVar name`). `"any"` is the wildcard — see the gate below.
    Var(Name),
    /// `a -> b`  (`T.TLambda`)
    Fun(Ty, Ty),
    /// Nominal application `Home.Name a b …` (`T.TType home name args`).
    /// Home is a `ModuleId`; the empty-home sentinel (`Canonical ""`,
    /// Unify.hs:341) becomes `ModuleId::EMPTY`.
    App { home: ModuleId, name: Name, args: Box<[Ty]> },
    /// Row-polymorphic record: sorted fields + optional extension var.
    /// Fields carry `_fieldIndex` for deterministic emission (L4).
    Record { fields: Box<[(Name, Ty)]>, ext: Option<Name> },
    Unit,
    /// 2- or 3-tuple (`T.Tuple1` is 2-or-3; keep the shape).
    Tuple(Ty, Ty, Option<Ty>),
    /// Transparent alias carrying its arg bindings + expansion
    /// (`T.TAlias home name pairs real`).
    Alias { home: ModuleId, name: Name, args: Box<[(Name, Ty)]>, real: Ty },
    /// L7 error-recovery sentinel. Unifies with anything, suppresses
    /// cascades. Replaces the scattered `T.TVar "_lit"/"_ambig"/…`
    /// magic strings (`Solve.hs:379` `Unresolved` scaffold) with ONE
    /// typed node (L6).
    Error,
}
```

`fields` is a **sorted boxed slice**, not a `HashMap` — the Haskell `Record1`
uses `Map String Variable` (`Type.hs:76`) and every emission path already has to
"sort by `_fieldIndex`". Sorting at the interning site makes L4 structural.

## The arena union-find (the L3 centrepiece)

The Haskell `UnionFind.hs` is `Point a = Pt (IORef (PointInfo a))` with **pointer
identity** for `Eq` (`UnionFind.hs:61-62`) and weighted-union + path-compression
over `IORef` links. Every soundness argument that says "these two variables are
the same" is a pointer comparison today. We replace pointer identity with a
dense integer index into an arena `Vec`, and the links become indices.

```rust
/// Type-variable identity. Replaces `UF.Point` pointer-eq (UnionFind.hs:61).
/// Allocation order is deterministic (L4) because it is drawn from the
/// InferCtx counter, not from hashmap order.
#[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct TyVarId(u32);

enum Slot {
    /// Root: carries the descriptor + union-by-rank weight.
    Root { content: Content, rank: u32 },
    /// Non-root: points at its parent (was `PointInfo::Link`, UnionFind.hs:34).
    Link(TyVarId),
}

/// The union-find store. Local to ONE `infer` query; never global (L1).
/// This is the "honest scoped-mutation sweet spot" of 01-architecture.
pub struct UnionFind {
    slots: Vec<Slot>,
}
```

`Content` mirrors `Type.hs:51` — **minus** the dead `_rank/_mark/_copy`:

```rust
enum Content {
    Flex(Option<Name>),                 // FlexVar
    FlexSuper(SuperType, Option<Name>), // Number/Comparable/Appendable/CompAppend
    Rigid(Name),                        // from a user annotation; won't unify w/ concrete
    RigidSuper(SuperType, Name),
    Structure(FlatTy),                  // resolved concrete shape
    Alias { home: ModuleId, name: Name, args: Vec<(Name, TyVarId)>, real: TyVarId },
    Error,                              // recovery
}

/// `Type.hs:71` FlatType, over TyVarId instead of Variable.
enum FlatTy {
    App { home: ModuleId, name: Name, args: Vec<TyVarId> },
    Fun(TyVarId, TyVarId),
    EmptyRecord,
    Record { fields: BTreeMap<Name, TyVarId>, ext: TyVarId },
    Unit,
    Tuple(TyVarId, TyVarId, Option<TyVarId>),
}

enum SuperType { Number, Comparable, Appendable, CompAppend } // Type.hs:62
```

### `find` — path compression, by index

Direct translation of `repr` (`UnionFind.hs:47-58`); the only change is
`IORef`-write → `Vec`-write:

```rust
impl UnionFind {
    fn find(&mut self, mut v: TyVarId) -> TyVarId {
        // Collect the chain, then point every node at the root (compression).
        let root = {
            let mut r = v;
            while let Slot::Link(p) = self.slots[r.0 as usize] { r = p; }
            r
        };
        while let Slot::Link(p) = self.slots[v.0 as usize] {
            self.slots[v.0 as usize] = Slot::Link(root);
            v = p;
        }
        root
    }
}
```

### `union` — by rank, carrying merged content

`merge` (`Unify.hs:520-525`) unions with `newRank = min(rank1, rank2)`; the
weighted link itself is `UnionFind.hs:106-129`. In the arena the higher-rank root
wins the parent slot; equal ranks bump the survivor (union-by-rank):

```rust
fn union(&mut self, a: TyVarId, b: TyVarId, content: Content) {
    let (ra, rb) = (self.find(a), self.find(b));
    if ra == rb { self.set_content(ra, content); return; }
    let (rank_a, rank_b) = (self.rank(ra), self.rank(rb));
    let (root, child, mut rank) =
        if rank_a >= rank_b { (ra, rb, rank_a) } else { (rb, ra, rank_b) };
    if rank_a == rank_b { rank += 1; }
    self.slots[child.0 as usize] = Slot::Link(root);
    self.slots[root.0 as usize]  = Slot::Root { content, rank };
}
```

### occurs-check — a `TyVarId` set, not `UF.equivalent` scans

`Occurs.hs:90-94` walks a `[Variable]` seen-list calling `UF.equivalent`
(O(n) representative compares per step). With integer ids the seen-set is a
`FxHashSet<TyVarId>` of **representatives** (internal cache; never emitted, so
`HashSet` is fine per L4's carve-out). Membership is O(1); the whole check is
linear in the visited graph. The check gates the `Flex ↔ Structure` merge
(`Unify.hs:192-223`) that otherwise builds an infinite type and OOMs the host
(the mini-notion >3 GB blow-up documented there).

```rust
fn occurs(&mut self, target: TyVarId) -> bool {
    let root = self.find(target);
    let mut seen = FxHashSet::default();
    self.occurs_in(root, root, &mut seen)   // reject if root reachable from its own structure
}
```

## `infer(DefId)` — inference as a salsa query

Today constraint generation is a whole-module `IO` pass (`constrainModule`,
`Constrain/Module.hs:17`) feeding one `solve` (`Solve.hs:803`); the LSP had to
bolt a **5-round canonicalise+solve fixpoint** on top to see cross-module types
(CLAUDE.md v0.17.3 note). In the query core that fixpoint disappears: `infer` is
per-`DefId`, and cross-module signatures arrive by *depending on the callee's
`infer`*, memoised by salsa (L2). No manual rounds, no 8 LSP IORefs.

```rust
/// skydb query. Returns partial results + diagnostics — never throws (L7).
#[salsa::tracked]
pub fn infer(db: &dyn Db, def: DefId) -> InferResult {
    let mut cx = InferCtx::new(db, def);
    let hir = db.def_body(def);
    let want = cx.instantiate_signature(def);   // annotation → fresh vars
    cx.constrain(&hir, want);                   // generate + solve, interleaved
    cx.finish()                                 // read back env + region table + diags
}

pub struct InferResult {
    pub scheme: Ty,                             // generalised type of `def`
    pub regions: RegionTable,                   // per-region types → lowering (07)
    pub instances: Vec<CallInstance>,           // monomorphisation keys
    pub diagnostics: Vec<Diagnostic>,
}
```

The current solver already *is* value-threaded — `solveHelp :: SolverState ->
Constraint -> IO (Maybe String, SolverState)` (`Solve.hs:1217`) returns errors as
values and threads state. That is exactly the shape L1/L7 want; we drop the `IO`
and the residual global IORefs (`rowExtCounter`) into `InferCtx`.

```rust
struct InferCtx<'db> {
    db: &'db dyn Db,
    uf: UnionFind,
    /// L4 deterministic fresh supply. Seeded from a pre-order traversal
    /// index of `def`, NOT from allocation happenstance. Replaces the
    /// `Counter = IORef Int` (Constrain/Expression.hs:133) AND the global
    /// `rowExtCounter` (Unify.hs:42). `fresh_row_ext()` draws from the
    /// same counter so merged records read back OPEN with a unique name
    /// (Unify.hs:499) — now deterministic.
    next_var: u32,
    /// name → var, the solver `_env` (Solve.hs:506). Scoped push/pop for
    /// let/lambda/case shadowing (Solve.hs:1375-1401), not a global leak.
    env: ScopedMap<Name, TyVarId>,
    /// region → var; frozen to `RegionTable` at the end (Solve.hs:762).
    region_vars: FxHashMap<Span, TyVarId>,
    /// FFI interface-satisfaction registry (see below). A pinned salsa
    /// input, not a mutable global (replaces `_ffiImplements`, Solve.hs:509
    /// + the deleted `ffiImplementsRef`).
    ffi_implements: &'db ImplementsMap,
    /// Defensive solver-step budget (Solve.hs:544). `steps > budget` →
    /// one diagnostic, bail (not an OOM).
    steps: u64,
    budget: u64,
    diagnostics: Vec<Diagnostic>,
}
```

### Fresh-var determinism (L4)

`fresh()` returns `TyVarId(self.next_var); self.next_var += 1;` and pushes a
`Slot::Root { Flex(name), rank: 0 }`. The **critical** rule, straight from
`01-architecture-overview.md:88-93`: the counter is advanced at the *collection*
(constraint-generation) site in a fixed pre-order walk of the HIR, never at an
emission site keyed by hashmap order. Same source ⇒ same `TyVarId`s ⇒ same read-
back ⇒ byte-identical Go (the reproducibility gate).

### Budget

Keep the structural budget verbatim (`Solve.hs:708-746`): `max(5_000_000,
constraint_count × 200)`, env-overridable via `SKY_SOLVER_BUDGET` /
`SKY_SOLVER_BUDGET_FACTOR`, `0` disables. It is the fence against constraint-
explosion OOM (Limitation #17). `bump_step()` returns `Err(budget_msg)` past the
cap; the caller short-circuits with a diagnostic. Per-`DefId` inference makes the
per-query budget naturally smaller than the old whole-module one.

## Unification (the soundness core)

`unify(a, b) -> Result<(), TypeError>` mirrors `actuallyUnify`
(`Unify.hs:171-435`) arm-for-arm. The arms that pin soundness:

- **Flex ↔ Structure/Alias** guard with `occurs` before merging
  (`Unify.hs:192-223`) — infinite-type rejection.
- **Rigid** unifies only with Flex or Error (`Unify.hs:263-267`) — a user-annotated
  rigid var never silently unifies with a concrete type.
- **`FlexSuper`** (`Number`/`Comparable`/`Appendable`/`CompAppend`) matched via
  `superMatches`/`combineSuper` (`Unify.hs:529-554`) — keep the `isSkyCore` home
  guard so `(5).field` is still rejected.
- **App ↔ App** exact + the two axioms below (`Unify.hs:330-381`).
- **App ↔ Alias same-name** unfold-and-match (`Unify.hs:282-308`) for recursive
  parametric aliases.

### Wildcard-`any`, per occurrence — preserve EXACTLY

Sky's `"any"` is a *per-occurrence* wildcard: each textual `any` gets its own
fresh var so destructuring one occurrence never constrains another (the
`AttrA String | AttrB any` cross-branch example, `Instantiate.hs:58-66`). Two
halves, both mandatory:

1. **Instantiation drops `"any"` from the shared env** so each occurrence is
   fresh (`Instantiate.hs:43` `filter (/= "any")`; `buildEnv` returns the env
   unchanged for `TVar "any"`, `Instantiate.hs:69-72`). In Rust,
   `instantiate(scheme)` builds the shared substitution over
   `free_vars \ {"any"}` and calls `fresh()` at *every* `Ty::Var("any")` leaf.
2. **The polymorphism gate is `free.iter().any(|v| v != "any")`, never
   `!free.is_empty()`** — reproduced at all three sites: the same-module
   CForeign/CLocal choice (`Constrain/Expression.hs:472`), the arity gate
   (`:1122`), the value-slot gate (`:1201`). A `Cfg -> msg` with only `any` free
   is **not** polymorphic; treating it as polymorphic would diverge the body↔caller
   vars under per-call-site re-instantiation and accept wrong return types.

```rust
/// The one true polymorphism predicate. Do NOT replace with `!free.is_empty()`.
fn is_polymorphic(free: &[Name]) -> bool {
    free.iter().any(|n| n.as_str() != "any")
}
```

A `ty` unit test asserts `is_polymorphic(&["any"]) == false` and
`is_polymorphic(&["msg"]) == true`, mirroring
`Sky.Type.StrictHmArityGateSpec`'s `wa-a` / `wp-a` cases.

### FFI interface-satisfaction axiom — the self-host's missing soundness

When two nullary `App`s have *different* names and the pinned FFI registry says
one implements the other (Go structural interface satisfaction), unify succeeds
as a **widening** (`Unify.hs:358` inside the App↔App arm). This closes the Fyne
`Label ↔ CanvasObject` and Stripe `Iter ↔ Token` cases (CLAUDE.md Limitation #6)
and is precisely the check the `any`-boxed self-host could not make.

```rust
// Unify.hs:108-121 — one-way relation, symmetric probe.
fn implements(&self, q: &Name, iface: &Name) -> bool {
    self.ffi_implements.get(q).map_or(false, |is| is.contains(iface))
}
fn ffi_interface_pair(&self, n1: &Name, n2: &Name) -> bool {
    self.implements(n1, n2) || self.implements(n2, n1)   // isFfiInterfacePair
}

// inside unify's App↔App arm, AFTER exact-match fails (Unify.hs:358):
if a.args.is_empty() && b.args.is_empty() && self.ffi_interface_pair(&a.name, &b.name) {
    return self.commit(va, vb, FlatTy::App(a));          // widen; principal type preserved
}
```

`ImplementsMap` is a **pinned salsa input** built once from the deterministic FFI
surface (`09`), replacing both the `_us_ffiImplements` thread and the deleted
`ffiImplementsRef` global (`Unify.hs:53-60`). Empty map ⇒ strict nominal equality
(the safe default). Keep the sibling `Value ↔ qualified` bridge
(`Unify.hs:377-380`) for hand-coded kernel sigs, one-way (`hasQualifiedMarker`
guards the `_at_` mangled form only).

### Records + row polymorphism

`unify_records` reproduces `Unify.hs:468-502` — the closed/open discipline is a
real soundness fix (the `takesRecord { wrong fields }` panic class,
`Unify.hs:449-460`):

- shared fields unify pairwise;
- a **closed** side forbids extras on the other side (`extras{1,2}Illegal`,
  `Unify.hs:486-489`) — reject;
- both closed ⇒ exact field-set match;
- both open ⇒ row-poly merge under a **freshly-named** extension var
  (`Unify.hs:499`) — now `cx.fresh_row_ext()`, deterministic (L4).

Keep the open-record ↔ empty-home FFI-opaque arm (`Unify.hs:421-433`): an
`expr.field` open-row pattern satisfies an opaque FFI nominal (getter-backed) but
a *closed* literal does not.

## Generalisation & instantiation

Sky does **not** generalise by rank (why the Elm `_rank/_mark/_copy` pool is
dead). Generalisation is: solve the def, read back its var, then rename residual
flex vars to `Forall` quantifiers — `generaliseToAnnotation :: Ty -> Annotation`
(`Compile.hs:11292`). Instantiation is `fromAnnotation` (`Instantiate.hs:39`):
fresh flex var per quantifier except `"any"`.

```rust
fn generalize(&mut self, root: TyVarId) -> Scheme {
    let ty = self.read_back(root);                 // variableToType (Solve.hs:1428)
    let free = free_vars(ty).into_iter()
        .filter(|v| v.as_str() != "any")           // Instantiate.hs:43
        .collect::<Vec<_>>();
    Scheme { vars: free, ty }
}

fn instantiate(&mut self, s: &Scheme) -> TyVarId {
    // one shared fresh var per non-`any` quantifier …
    let sub: FxHashMap<Name, TyVarId> =
        s.vars.iter().map(|v| (*v, self.fresh(Some(*v)))).collect();
    // … but `Ty::Var("any")` leaves call `fresh()` fresh EACH time (buildEnv, Instantiate.hs:69)
    self.ty_to_var(&s.ty, &sub)
}
```

### Same-module polymorphic re-instantiation

A sibling reference to a *polymorphic annotated* same-module def emits `CForeign`
and α-renames per call site, so `f : Cfg msg -> msg` used at `msg=Int` and
`msg=Bool` in one module both work (`Constrain/Expression.hs:466-526`). The choice
is: `home == current_module && same_mod_annots[name]` is polymorphic (the gate
above) ⇒ **`CForeign` + fresh instantiation**; else ⇒ **`CLocal` (shared env
var)**. Non-polymorphic / wildcard-only sigs *must* stay on the shared path —
identity-based unification on nominal aliases and wildcard-`any` binding both need
the shared body↔caller var chain (CLAUDE.md "Same-module polymorphic
re-instantiation"). In the query world, `same_mod_annots` is just
`db.module_signatures(module)`; cross-module externals are
`db.def_scheme(callee)` — both memoised, no fixpoint.

Each polymorphic def's quantifiers are α-renamed with `fresh_name` per def
(`Constrain/Expression.hs:1337-1346`) so sibling defs' `a`s don't unify.

## Strict-HM arity gate (E2007, Limitation #7) — keep

Two surfaces, one diagnostic. `declared_arity` counts leading arrows
(`Constrain/Expression.hs:4127-4131`):

```rust
fn declared_arity(scheme: &Scheme) -> u32 {
    let mut n = 0; let mut t = scheme.ty;
    while let TyData::Fun(_, to) = db.lookup(t) { n += 1; t = to; }
    n
}
```

- **Call site** (`arityGateCall`, `:1066`): calling a `0`-arg binding with `()`
  fires `CArityMismatch{declared:0, supplied:1}` — but only when the arg is unit
  and the binding is non-polymorphic (`maybeEmitArityMismatch`, `:1114-1131`).
- **Value slot** (`valueSlotGate*`, `:1171`): a bare reference of a `() -> X`
  binding into a *concrete* (non-arrow, non-var) slot fires
  `CArityMismatch{declared:d, supplied:0}` (`:1206-1207`). Classification by
  `SlotShape` (`:1233`): `Var → skip` (defer to normal unify), `Arrow → skip`
  (slot wants a function), `Value → fire when d ≥ 1`.

Both gates are **guarded by the wildcard-polymorphism predicate** (`:1122,:1201`)
so real polymorphism is never mis-flagged. Represent the mismatch as its own
constraint (`Type.hs:104` `CArityMismatch`), solved to a `Diagnostic` with code
`E2007` (`Diagnostic.hs:206`) — no exception, continue.

## Exhaustiveness — keep it strong (L6)

`Exhaustiveness.hs` is deliberately conservative-but-real: for an ADT `case` it
requires every constructor of the subject's union to appear (or a
wildcard/var/alias head), warning `E3001` (`Diagnostic.hs:211`) on any miss
(`Exhaustiveness.hs:100-119`). Bool needs both `True`/`False`; unit is trivially
covered; infinite-domain literals (Int/String/Float/Char) require a wildcard arm.
This is **stronger than GHC-as-configured** and it is a keeper.

Port `checkBranches` (`:100`) + `classify` (`:129`) verbatim into `ty` as a query
`exhaustiveness(DefId) -> Vec<Diagnostic>` fed by resolved constructor sets from
`hir`. Two upgrades the Rust setting affords:

- Run it over a **column matrix** (Maranget-style) so nested patterns and
  literal ranges get the same rigor without false positives — the current checker
  analyses only top-level arm heads (`Exhaustiveness.hs:8-13`); widening it is
  additive and never *weakens* the guarantee.
- **L6 turned inward:** the compiler's own IR/AST enums carry
  `#![deny(non_exhaustive_omitted_patterns)]`; no `_ =>` catch-alls on our own
  types. The self-host's `getDeclName` panic (33-gap audit) is unrepresentable.

## The per-region type table (the output that drives lowering)

Every solved `CEqual`/`CLocal`/`CForeign` records its actual var against the
constraint's source region (`recordRegionVar`, `Solve.hs:762-766`, skipping the
`(0,0)` synthetic sentinel); at solve-end the map is frozen to concrete types
(`freezeRegionTypes`, `:772`). This `RegionTypes = Map Region Ty` (`Solve.hs:358`)
is what type-directed lowering reads to pick a typed Go shape per sub-expression
(`07`; CLAUDE.md "Type-directed lowering").

```rust
/// Frozen at the end of `infer`. Consumed by `lower` (07). Keyed by Span
/// (interned FileId + range), iterated in id/BTree order (L4).
pub struct RegionTable(BTreeMap<Span, Ty>);
```

This replaces the historical `scopeStateRef`/`_lc_regionTypes` IORef read that
was "the load-bearing reason the LowerCtx cascade couldn't migrate"
(`Solve.hs:88-108`). Here it is a **pure value** returned by the query — the
per-module ledger machinery (`_stPerModuleRegions`, `Solve.hs:143`) that patched
cross-module region collisions is unnecessary: regions are `(FileId, range)` and
each `DefId`'s table is its own, keyed by an interned `FileId` that cannot
collide across modules.

## Error recovery — type error → `Error` node + diagnostic, continue (L7)

`unify` never aborts the query. On a genuine mismatch it: (1) pushes a structured
`Diagnostic` (rich record-diff for TEA-cfg shapes, `Solve.hs:1276-1298`), and (2)
commits **both** vars to `Content::Error`, which unifies with anything
(`Unify.hs:236-242`) and reads back as the `Ty::Error` sentinel
(`variableToTypeSeen … Error`, `Solve.hs:1474`). Cascades are suppressed; one
mistake yields one diagnostic, and inference produces a *partial* `InferResult`
the LSP can still hover over. The read-back's own cycle guard
(`anyEquivSeen`, `Solve.hs:1449-1451`) returns an `Error`-class sentinel rather
than looping on a pre-existing cyclic graph.

```rust
fn unify(&mut self, a: TyVarId, b: TyVarId) -> Result<(), ()> {
    match self.try_unify(a, b) {
        Ok(()) => Ok(()),
        Err(mismatch) => {
            self.diagnostics.push(mismatch.into_diagnostic());
            self.commit(a, b, Content::Error);   // suppress cascade (Unify.hs:236)
            Err(())
        }
    }
}
```

## What we delete (and why it is safe)

| Dropped | Why | Cite |
|---|---|---|
| `Descriptor._rank / _mark / _copy` | Elm let-generalisation pool; Sky generalises via annotations, never used | `Type.hs:42-47`; self-host `:112-114` |
| `rowExtCounter` global IORef | L1 + L4 violation; becomes `InferCtx.next_var` | `Unify.hs:42-50` |
| `ffiImplementsRef` / `_ffiImplements` thread | pinned salsa input instead | `Unify.hs:53-60`; `Solve.hs:509` |
| `_stPerModule{Env,Regions}` + `_stCurrentModule` | per-`DefId` queries + interned `FileId` regions can't collide | `Solve.hs:125-165` |
| LSP 5-round canonicalise+solve fixpoint | salsa memoisation of `infer` dependencies | CLAUDE.md v0.17.3 |
| `Unresolved` magic-string sentinels (18 strings) | one typed `Ty::Error` (L6) | `Solve.hs:361-399` |

## Test obligations (differential + property)

- **Wildcard gate**: `is_polymorphic(["any"]) == false`; port
  `StrictHmArityGateSpec` (`wa-a`,`wp-a`,`k-a`,`k-b`,`u-a`,`u-b`).
- **FFI axiom**: Fyne `Label→CanvasObject` and Stripe `Iter→Token` accept;
  unrelated nullary pair rejects.
- **Records**: `takesRecord { wrong fields }` rejects (closed-record extras);
  open-record merge accepts.
- **Occurs**: `a = List a` shape rejects, no hang.
- **Exhaustiveness**: missing-`Nothing` arm warns `E3001`; wildcard suppresses.
- **Determinism (L4)**: infer a def twice, assert identical `TyVarId` allocation
  and identical `RegionTable` (the reproducibility gate, `xtask`).
- **Oracle**: for every corpus def, the read-back `Ty` matches the Haskell
  compiler's `generaliseToAnnotation` output (accept/reject parity, `11`).
