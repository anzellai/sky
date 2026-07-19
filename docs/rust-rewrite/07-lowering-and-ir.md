# 07 — Lowering & the Typed Go-IR

The `lower` crate turns **typed Sky** (name-resolved HIR + the `infer` type
table) into a **typed Go-IR** — a Rust data structure where *every node already
carries its Go type*. Codegen ([`08`](08-go-codegen.md)) then walks that IR and
prints Go without re-deriving a single type.

This doc is the concrete design of law **L9 — a typed lowering IR; coercion is
the exception**. It is also where **L4** (deterministic fresh-name generation),
**L3** (interned ids drive iteration order), and the TCO/DCE memory story live.

> **Implementation status (as of `rewrite/rust-compiler`).** The typed IR is
> **partly built**: `rust/crates/lower/src/ir.rs` does carry a structural `GoTy`
> on every `GoExpr` (the L9 spine is real, and it is why the corpus builds+runs).
> But the target's stronger claims below — *"coercion is the exception"*, *"no
> `Raw(String)`"*, *"no widen-to-`any`"*, structural Go-generics on record
> aliases, and sealed-interface ADT emission that deletes residual coerce classes
> 1/2/3/4/6/8 — describe the **destination**, not the current milestone. What the
> code does today:
>
> - **A `Widen` node exists** (`ir.rs`, `GoExprKind::Widen`) and codegen renders
>   it as `any(x)` (`rust/crates/codegen/src/lib.rs`). So `Coerce` is *not* the
>   only narrowing/widening node yet.
> - **User ADTs are erased, not structurally generic.** `sky_ty_to_go`
>   (`rust/crates/lower/src/goty.rs`) emits user nominal types **non-generic**: a
>   sealed ADT becomes a `rt.SkyADT` bag (`type X = rt.SkyADT`, `{Fields []any}`,
>   codegen `emit_type`), an iota enum becomes `int`, a parametric record alias
>   becomes a plain struct whose type-var fields are erased to `any`. A parametric
>   application like `Cfg Msg` renders as the bare Go name with its args dropped —
>   the file calls this the *"generic-erase floor, doc 07 §6 class 8"* in a code
>   comment. The `Cfg_R[Msg]` structural-generic emission in §3 below is target.
> - **Tuples erase to `rt.T2[any,any]`** (`goty.rs` / codegen `render_tuple_ty`) —
>   the runtime's reflection paths standardise on the `any`-element shape — rather
>   than the `rt.T2[A,B]` structural form §1/§6 describe.
> - **Names are `String`, not interned `GoName`/`TyParamId`.** `GoTy::Named(String, …)`
>   and `GoTy::TyVar(String)` in `ir.rs`; the interned-`GoName`/`Name` listing
>   in §1 is target.
>
> This interim erase-based representation is **verified to build+run+match** the
> Haskell oracle across the corpus (including `13-skyshop`, 76k FFI symbols), so
> it is a working simplification, not a bug — but the residual-`any` surface it
> carries is exactly what the structural typed IR below is designed to remove, and
> that removal is remaining work ([`12`](12-migration-and-milestones.md)). Read the
> rest of this doc as the target design; the §6 "Fate under the typed IR" column
> is target-state, not a description of current emission.

## The scar this fixes

In the Haskell backend the Go IR carries **types as `String`s**. Look at the
node definitions in `src/Sky/Generate/Go/Ir.hs:13-36`: `GoSliceLit !String`,
`GoStructLit !String`, `GoGenericCall !String [String]`, `GoTypeAssert !GoExpr
!String`, `GoFuncDecl._gf_returnType :: !String`. The type of every value is a
free-form string produced by `typeToGo :: T.Type -> String`
(`src/Sky/Generate/Go/Type.hs:39`) — and once it is a string, nothing
downstream can reason about it structurally. Three consequences, all scars:

1. **Type-directed lowering had to be bolted on.** Because the IR node did not
   know its own type, the solver was retrofitted to publish a *side table* of
   per-source-region types (`Solve.lookupSolvedRegion`, snapshotted into
   `LowerCtx._lc_solved`, `src/Sky/Build/LowerCtx.hs:87`), and the lowerer
   threads an "expected type for this slot" through a 15-field `LowerCtx`
   (`_lc_lambdaGoTypes`, `_lc_enclosingTypeParams`, `_lc_ffiTypedWrapperParams`,
   …). The type is *reconstructed at emit time* instead of *carried by the node*.

2. **Monomorphisation degraded to a string rewrite.** `substTypeParamsInString`
   (`src/Sky/Build/Monomorphise.hs:481`) walks emitted Go *type strings* token by
   token replacing `T1`→`int`. A structural substitution
   (`substTVarsInGoTypeStructural`, `src/Sky/Generate/Go/Type.hs:290`) was later
   added over a *half-migrated* `GoType` ADT (`Go/Type.hs:237`) that now coexists
   with the String IR — a migration frozen mid-flight.

3. **`rt.Coerce`/`any` became a pervasive surface.** When a slot's type is a
   string you cannot cheaply prove "these two types are already equal", so the
   safe default is to wrap. The result is documented in
   `docs/v0.17/rt-coerce-residual-surface.md`: **8 safety classes, 200–700 coerce
   sites per UI-heavy example**. A real emitted line from
   `examples/07-todo-cli/sky-out/main.go:864` reads:

   ```go
   rt.TaskCoerceT[Sky_Core_Error_Error, struct{}](rt.Task_map(func(_ any) any {
       … _ = rt.AnyTaskRun(rt.Db_exec(any(conn).(*rt.SkyDb), …, []any{todoTitle})) … },
       /* PROOF: FFI: SkyTask[any,any] → SkyTask[E,A] (typed instantiation) */
       rt.TaskCoerceT[any, any](rt.Db_exec(any(conn).(*rt.SkyDb), …))))
   ```

   Nested `TaskCoerceT[any, any]` wraps with `/* PROOF */` comments are the
   *symptom* of a lowerer that lost the type and is defensively re-narrowing.

The rewrite makes the type non-optional: `GoTy` is a field of every IR node, set
once at lowering from the `infer` result, never re-derived. A coercion is then a
**distinct, explicit IR node** the lowerer emits *only* when it can prove the
source and target Go types genuinely differ — which, as the residual-surface
analysis shows, is a small, enumerable set.

## Position in the query graph

`lower` is a set of salsa queries (L2). It never reads a global; it asks the db.

```mermaid
flowchart LR
    RES["resolve(ModuleId)"] --> THIR
    INFER["infer(DefId)\n(types + per-region type map)"] --> THIR["typed_hir(DefId)\n→ typed HIR (TCO-normalised)"]
    THIR --> MONO["mono_instances(project)\n→ (DefId, [GoTy]) worklist"]
    MONO --> GOIR["go_items(ModuleId)\n→ Vec<GoItem> (typed Go-IR)"]
    DCE["reachable(project)\n→ Set<Ref>"] --> GOIR
    THIR --> DCE
    GOIR --> CODEGEN["codegen: go_module(ModuleId)"]
```

Every edge is memoised; editing one def reruns `typed_hir` for that def and the
`go_items` of its module only. `reachable` and `mono_instances` are
whole-program queries keyed on the project revision.

## 1. The typed Go-IR (`GoTy` + `GoExpr` + `GoStmt` + `GoItem`)

The center of the crate. Contrast with `Ir.hs`: **there are no `String` type
fields**. Types are the `GoTy` enum, names are interned `Name`s (L3), and every
expression is `(node, GoTy)`.

```rust
/// Structural Go type — the ONLY representation of a Go type in the IR.
/// Supersedes `typeToGo :: T.Type -> String` (Go/Type.hs:39) and the
/// half-migrated `GoType` ADT (Go/Type.hs:237). Rendered to source once,
/// in codegen (08), never parsed back.
#[derive(Clone, PartialEq, Eq, Hash)]
pub enum GoTy {
    Bare(Prim),                       // int | string | bool | float64 | rune | []byte
    Unit,                             // struct{}   (Sky's `()`)
    Any,                              // any        — the JUSTIFIED wildcard (see §6)
    Func(Vec<GoTy>, Box<GoTy>),       // func(A, B, …) R   (N-ary; no curry chain)
    Named(GoName, Vec<GoTy>),         // Module_Name  or  rt.SkyList[T] etc.
    Struct(Vec<(Name, GoTy)>),        // anonymous struct{ … } (fields in _fieldIndex order)
    Tuple(Vec<GoTy>),                 // rt.T2[A,B] / rt.T3[…] / rt.SkyTupleN (arity ≥ 4)
    TyVar(TyParamId),                 // T1, T2 — a Go generic type parameter, interned
    // NOTE: no `Raw(String)` escape hatch. `Go/Type.hs:237`'s `GoRaw` is the
    // seam through which untyped strings leaked; the rewrite forbids it. Any
    // shape the constructors can't model is a design gap to be fixed, logged,
    // never string-patched.
}

/// `GoName` is an interned, already-mangled Go identifier (L3). Mangling
/// (module prefix, reserved-name rewrite — see 08 §reserved) happens once at
/// interning time so no emit path re-mangles.
pub struct GoName(Name);

pub struct GoExpr {
    pub kind: GoExprKind,
    pub ty:   GoTy,          // ← the invariant: every expression knows its type
}

pub enum GoExprKind {
    Ident(GoName),
    Qualified(GoName, Name),                 // pkg.name
    IntLit(i64), FloatLit(f64), StrLit(Text), RuneLit(char), BoolLit(bool), Nil,
    Call(Box<GoExpr>, Vec<GoExpr>),
    GenericCall(GoName, Vec<GoTy>, Vec<GoExpr>),  // f[T1,T2](a,b) — type args are GoTy, not String
    Selector(Box<GoExpr>, Name),
    Index(Box<GoExpr>, Box<GoExpr>),
    SliceLit(GoTy, Vec<GoExpr>),             // []T{…} — element type is structural
    MapLit(GoTy, GoTy, Vec<(GoExpr, GoExpr)>),
    StructLit(GoName, Vec<(Name, GoExpr)>),
    FuncLit(Vec<GoParam>, GoTy, Vec<GoStmt>),
    Binary(BinOp, Box<GoExpr>, Box<GoExpr>),
    Unary(UnOp, Box<GoExpr>),
    Block(Vec<GoStmt>, Box<GoExpr>),         // typed IIFE; ty field is the return type
    /// The ONLY narrowing node. `from`/`to` are both known; codegen picks the
    /// exact runtime helper (Coerce / AsListT / MaybeCoerce / …) or, when
    /// `from == to`, emits nothing (the coercion elides — see §6, cf.
    /// `coerceToFieldType` in the Haskell backend).
    Coerce { inner: Box<GoExpr>, from: GoTy, to: GoTy, reason: CoerceReason },
}

pub enum GoStmt {
    Expr(GoExpr),
    Short(GoName, GoExpr),                    // name := expr
    Assign(GoName, GoExpr),
    Var(GoName, GoTy, Option<GoExpr>),
    Return(Option<GoExpr>),
    If(GoExpr, Vec<GoStmt>, Vec<GoStmt>),
    Switch(GoExpr, Vec<(GoExpr, Vec<GoStmt>)>, Option<Vec<GoStmt>>),
    TypeSwitch(GoName, GoExpr, Vec<(GoTy, Vec<GoStmt>)>, Option<Vec<GoStmt>>),
    ForRange(GoName, GoExpr, Vec<GoStmt>),
    Loop(Vec<GoStmt>),                        // for { … } — the TCO target (§4)
    Continue,
    Comment(Text), Blank,
}

pub enum GoItem {
    Func(GoFuncDecl),
    Method(GoName, GoTy, GoFuncDecl),
    Type(GoName, GoTypeDef),                  // struct / iota-enum / alias / sealed iface
    Var(GoName, GoTy, Option<GoExpr>),
    Const(GoName, GoTy, GoExpr),
}

pub struct GoFuncDecl {
    pub name:        GoName,
    pub type_params: Vec<(TyParamId, GoTy)>,  // [(T1, any), (E, error)] — constraints are GoTy
    pub params:      Vec<GoParam>,
    pub ret:         GoTy,
    pub body:        Vec<GoStmt>,
}
```

`CoerceReason` is a small enum (`FfiReturn`, `WireDecode`, `TeaDispatch`,
`PrimitiveJoin`, `GenericErase`) that turns the Haskell backend's freeform
`/* PROOF: … */` comment string into typed data (L7). Codegen renders it as the
comment; a CI lint asserts no `CoerceReason` outside the §6 allowlist reaches
emission — the mechanised version of "the residual surface is enumerated".

## 2. Type-directed lowering

Lowering is a fold over the typed HIR carrying an `expected: GoTy` for the
current slot (the honest, structural version of `LowerCtx`'s "expected type"
thread). At each node the lowerer:

1. reads the node's Sky type from the `infer` region map,
2. maps it to `GoTy` via `sky_ty_to_go` (§3), producing the node's **actual** type,
3. lowers children with *their* expected `GoTy` (record-field type, call-arg
   param type, list-element type — computed from the parent's `GoTy`),
4. reconciles actual vs expected by **inserting a `Coerce` node iff they
   differ** — otherwise the child value flows through unwrapped.

```rust
fn lower_expr(db, cx: &LowerCx, e: &hir::Expr, expected: &GoTy) -> GoExpr {
    let sky_ty = db.region_ty(e.region);          // from infer(DefId)
    let actual = sky_ty_to_go(db, cx, &sky_ty);
    let node = match &e.kind {
        hir::ExprKind::List(items) => {
            let elem = actual.elem_ty();           // structural: []T -> T
            let lowered = items.iter().map(|it| lower_expr(db, cx, it, &elem)).collect();
            GoExprKind::SliceLit(elem, lowered)
        }
        hir::ExprKind::Record(fields) => {         // fields walked in _fieldIndex order (08)
            /* each field lowered with its declared field GoTy as `expected` */
        }
        hir::ExprKind::Call(f, args) => {
            let param_tys = actual_param_tys(db, cx, f);  // structural, from callee sig
            /* each arg lowered with its param GoTy as `expected` */
        }
        /* … */
    };
    coerce_if_needed(GoExpr { kind: node, ty: actual }, expected)
}

/// The whole point of L9. Because both types are structural, "already equal" is
/// a cheap `==` (L3 interned) and the coercion vanishes. Contrast the Haskell
/// `coerceToFieldType`, which had to string-compare rendered Go and often lost.
fn coerce_if_needed(x: GoExpr, expected: &GoTy) -> GoExpr {
    if &x.ty == expected || *expected == GoTy::Any {
        return x;                                   // no wrap — the common case
    }
    let reason = classify_coercion(&x.ty, expected); // §6; panics via bug!() if unjustified
    GoExpr { ty: expected.clone(),
             kind: GoExprKind::Coerce { from: x.ty.clone(), to: expected.clone(),
                                        inner: Box::new(x), reason } }
}
```

Because the "expected type" is a real `GoTy` threaded structurally, the
subset-record synth-var gymnastics (the Haskell `_skysynth_<alias>_<var>` TVar
minting) and the `_lc_enclosingTypeParams` scope set both disappear: a generic
parameter in scope is simply a `GoTy::TyVar(id)` that unifies by `==`.

## 3. `sky_ty_to_go` and Go-generics on parametric record aliases

`sky_ty_to_go` is the single Sky→Go type map (replacing `typeToGo` +
`mapSkyTypeToGo` + `goNamedType`, `Go/Type.hs:39/1018/88`). It is total and
returns `GoTy`, never a string.

A parametric record alias emits a Go-generic struct **from its type scheme**,
cleanly — no alias-chain workaround:

```elm
type alias Cfg msg = { onSubmit : msg, label : String }
```

```rust
// scheme: Cfg has one type param `msg`. Emit:
//   type Cfg_R[T1 any] struct { OnSubmit T1; Label string }
GoItem::Type(cfg_r, GoTypeDef::Struct {
    type_params: vec![(t1, GoTy::Any)],
    fields: vec![(onSubmit, GoTy::TyVar(t1)), (label, GoTy::Bare(Prim::Str))],  // _fieldIndex order
})
```

An instantiation at `msg = Msg` is `GoTy::Named(cfg_r, vec![GoTy::Named(msg,
[])])` → renders `Cfg_R[Msg]`. Because the instantiation is structural, a
callback field keeps its typed callee parameter (`func(Msg) …`, never
`func(any) any`) and cross-alias passing needs no coercion — the two `GoTy`s are
`==`. This is the clean form of what CLAUDE.md calls "Go generics on parametric
record aliases", with the subset-record case falling out of ordinary
`GoTy::TyVar` unification rather than synthesised TVars.

Structs (data records), sealed-interface ADTs, and iota enums are all chosen
here from the alias/union classification (the Rust port of `classifyAlias`,
`Go/Record.hs:458`, and `shouldEmitSealedIface`). **Sealed-interface ADT
emission is designed in from day one** — see §6, it is the lever that deletes
residual classes 1/3/6.

## 4. Auto-TCO

Identical strategy to the Haskell `Sky.Build.TailCallOpt`, run as a **HIR→HIR
normalisation** inside `typed_hir` before Go-IR construction — so the Go-IR is
already loop-shaped and codegen stays dumb.

- `is_tail_recursive(def)` — a self-reference exists AND every self-reference is
  in tail position with matching arity (port of `isTailRecursive`,
  `TailCallOpt.hs:52`; tail-position propagators = `Case`/`If`/`Let` bodies,
  `TailCallOpt.hs:301`).
- If so, the body lowers to `GoStmt::Loop(body')` where each tail self-call
  becomes *param reassignments + `Continue`* and every other tail position
  becomes `Return` (port of `rewriteTailCalls` + the `GoForever` target,
  `TailCallOpt.hs:163`, `Ir.hs:63`). Reassignment uses fresh temporaries drawn
  from the L4 deterministic counter to avoid the read-before-write hazard.

The pure-Sky CPS-rewritten list ops (`map`/`filter`/`foldr`/… per CLAUDE.md)
are stdlib source, not compiler passes — they land as ordinary tail-recursive
defs that this pass turns into loops. Result: constant Go stack, the arena/no-GC
memory story of L3 continued into the runtime.

## 5. Monomorphisation & DCE

**Monomorphisation** — polymorphic *annotated* defs are specialised per call-site
type instance (the CLAUDE.md "same-module polymorphic re-instantiation" +
alpha-rename), but over the **typed IR**:

- Call instances are collected at inference time (the salsa analogue of
  `Solve.solveWithInstances`) into `mono_instances`: a set of `(DefId, Vec<GoTy>)`.
- Each instance mangles to a deterministic Go name via a **structural** mangle
  over `GoTy` (port of `mangleType`/`mangleInstance`, `Monomorphise.hs:83/132` —
  same `MaybeOf_Int` grammar, but folding the `GoTy` tree, not re-parsing a
  string).
- Specialisation substitutes `GoTy::TyVar(id) → concrete GoTy` **structurally**
  (`subst_tyvars`, the honest version of `substTVarsInGoTypeStructural`,
  `Go/Type.hs:290`) — deleting the token-level `substTypeParamsInString` string
  rewrite (`Monomorphise.hs:481`) entirely. Wildcard `any` keeps its
  per-occurrence fresh-var semantics via the `any (/= "any")` gate (CLAUDE.md
  "wildcard-any soundness gate"), expressed as: a `GoTy::Any` param is never a
  monomorphisation target.

Determinism (L4): the instance worklist is a `BTreeSet<(DefId, Vec<GoTy>)>` and
emission walks it in sorted order; equal instances mangle to equal names so no
duplicate emission and a stable LSP symbol index.

**DCE** — a whole-program reachability query (port of `Dce.reachableWholeProgram`,
`src/Sky/Build/Dce.hs`). The typed `Ref` ADT is preserved:

```rust
enum Ref { Top(ModuleId, Name), Ffi(Name, Name), Ctor(ModuleId, Name) }  // Dce.hs:63
```

`reachable(project)` walks the call graph from roots `(entry, "main")` (+ test
lists under `sky test`), keeping ctor-closures alive on pattern matches
(`expandCtorClosure`) and treating module-init side-effect discards as roots
(`sideEffectRoots`). `go_items` consults it and skips unreachable defs + FFI
sigs. As a salsa query it is incremental for free — the LSP gets accurate
"unused" diagnostics without the batch re-walk the Haskell side needed.

## 6. The central goal — coercion is the exception, enumerated

The typed IR makes `Coerce` a node the lowerer must *justify*. Every insertion
site is one of a small allowlist; `classify_coercion` returns the `CoerceReason`
and `bug!()`s on anything else (L6/L7). Mapping the 8 documented residual classes
(`docs/v0.17/rt-coerce-residual-surface.md`) onto the rewrite:

| # | Haskell residual class | Sites (26-ui) | Fate under the typed IR |
|---|---|---|---|
| 1 | Sealed-iface ctor narrowing | 80+ | **Deleted.** Sealed-iface ADT emission (Haskell #677, deferred) is the default here: a ctor already returns the interface type, so `from == to`, no wrap. |
| 2 | Parametric record-alias | 63+ | **Deleted** for typed paths — `Cfg_R[Msg]` flows structurally (§3). Survives *only* at a genuine `any` source (JSON/DB row) as reason `WireDecode`. |
| 3 | Typed list narrowing `AsListT[T]` | 458+ (dominant) | **Deleted** for typed literals — `SliceLit(GoTy, …)` is born typed. Survives only wrapping a genuinely-`any` runtime slice (`WireDecode`). |
| 4 | Container Maybe/Result/Task | 15+ | **Deleted** for typed paths — containers are `Named("rt.SkyMaybe",[T])`; the payload type is carried, not re-narrowed. |
| 5 | Primitive narrowing | 6 | **Irreducible floor.** HM-structural ↔ Go-nominal join. Kept as `PrimitiveJoin`; caught by the panic floor (08). |
| 6 | Tuple narrowing | 57+ | **Deleted** for typed tuples — `Tuple(vec![A,B])` renders `rt.T2[A,B]` directly. |
| 7 | Map/Dict narrowing | 5+ | Survives only at genuine `any` source (`WireDecode`). |
| 8 | Generic-param erasure | 3+ | **Deleted** — `GoTy::TyVar(id)` in scope unifies by `==`; no widen-to-`any`-then-recover dance (the whole point of the enclosing-scope T-var hack). |

What remains is exactly the **irreducible floor** named by L10 §8: values that
enter Sky as genuine `any` — a **Go FFI return**, a **gob/JSON wire decode**, a
**TEA `reflect.MakeFunc` dispatch**. Those are the only `CoerceReason`s that
legitimately survive, and each is a small, countable set rather than a
per-expression reflex. The reproducibility + soundness gates (L4/L6, tested in
[`11`](11-testing-and-verification.md)) assert the coerce-site count on the
corpus stays at the floor and never regresses toward the Haskell surface.

## What this crate hands to codegen

A `Vec<GoItem>` per module in which **every `GoExpr` carries a `GoTy`, every type
argument is a `GoTy`, every name is an interned already-mangled `GoName`, and
every `Coerce` is justified**. Codegen ([`08`](08-go-codegen.md)) never asks "what
type is this?" — it only renders. That separation is L9 realised: the *lowering*
owns types; the *codegen* owns bytes.
