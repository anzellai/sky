# Sky compiler — type soundness root-cause analysis & v0.15 redesign

> **Status.** Iteration 2 (2026-05-24).  Deepened after exhaustive
> codebase mapping: 18 NOINLINE global IORefs in Compile.hs; existing
> partial-typed infrastructure (`globalLambdaTypes`,
> `CallSiteInstance`, `_locals`); constraint generator already
> region-keyed.  This doc is the v0.15.x charter.

## 0. TL;DR

Sky's compiler shipped 27 examples + 306 cabal specs + 120 Sky.Test
assertions green, BUT every parametric-record-alias bug we've fixed
in 2026-05 traces to **one architectural fault**: the lowerer is
type-blind at most positions and recovers type context ad-hoc at
SOME positions via coercion helpers + 18 IORefs of implicit state.
Every patch widens the recovery surface and creates the next bug
class.

The principled fix is **bidirectional type-directed lowering**
(Pierce & Turner 2000): a `lower :: LowerCtx → ExpectedType →
Can.Expr → TypedGoExpr` function that threads the expected HM type
through every recursive call.  Today's coercion helpers
(`coerceCallArgsAt`, `coerceToFieldType`, `wrapTypedReturn`,
`retypeFuncLitOrCoerce`, Surface 2 erasure, Surface 3 dedup, most
`rt.Coerce[T]` calls, and ~14 of the 18 globals) disappear because
the IR is emitted with the right type the first time.

The infrastructure for this **already exists in partial form** — the
solver carries regions on every constraint; `CallSiteInstance` is
region-keyed; `globalLambdaTypes` threads lambda param types
through scoped state.  The redesign extracts these into a
principled, explicit API.

**Effort: 12-16 sessions over ~6-10 working days, shippable in stages,
each stage independently green.**

## 1. The pipeline — what the compiler actually does

```
                                  ┌─────────────────┐
                                  │   .sky source   │
                                  └────────┬────────┘
                                           │
                          ┌────────────────▼────────────────┐
        Phase 1          │  Parse  (Sky.Parse)              │
        Discovery        │  → Src.Module  (concrete syntax) │
        + parallel       └────────────────┬────────────────┘
                                          │
                          ┌───────────────▼─────────────────┐
        Phase 2          │  Canonicalise (Sky.Canonicalise) │
        Name             │   - resolve imports              │
        resolution       │   - alpha-rename                 │
                         │   - EXPAND ALIASES (Surface 1)   │
                         │   - validate ADT exhaustiveness  │
                         │  → Can.Module (named, scoped)    │
                          └───────────────┬─────────────────┘
                                          │
                          ┌───────────────▼────────────────────┐
        Phase 3          │  Constrain (Sky.Type.Constrain)    │
        Generate         │   - walk Can.Expr → constraints    │
        constraints      │   - CEqual / CLocal / CForeign     │
                         │   - EVERY CONSTRAINT CARRIES REGION │ ← key for v0.15
                         │  → T.Constraint                    │
                          └───────────────┬────────────────────┘
                                          │
                          ┌───────────────▼────────────────────┐
        Phase 4          │  Solve (Sky.Type.Solve)            │
        Unify            │   - Union-Find over T.Variable     │
                         │   - Unify.hs handles Alias/Record  │
                         │   - Monomorphise per call site     │
                         │   - records CallSiteInstance       │ ← region-keyed, partial
                         │   - records _locals (CLet names)   │ ← name-keyed, locals
                         │  → SolvedTypes : Map name T.Type   │
                          └───────────────┬────────────────────┘
                                          │
                          ┌───────────────▼─────────────────────┐
        Phase 5          │  Lower (Sky.Build.Compile)          │ ←── most bugs live here
        Codegen          │   - exprToGo (124 callsites: blind) │
                         │   - exprToGoExpectGo (35 typed)     │
                         │   - solvedTypeToGo (64 calls)       │
                         │   - 18 global IORefs of impl. state │
                         │   - emit struct / func / init       │
                         │  → GoIR  (Sky.Generate.Go.Ir)       │
                          └───────────────┬─────────────────────┘
                                          │
                          ┌───────────────▼─────────────────────┐
        Phase 6          │  Render (Sky.Generate.Go.Builder)   │
        Go output        │  → main.go (text)                   │
                          └───────────────┬─────────────────────┘
                                          │
                          ┌───────────────▼─────────────────────┐
        Phase 7          │  go build                           │
                         │  → sky-out/app                      │
                          └─────────────────────────────────────┘
```

### Three type representations, two lossy boundaries

| Layer | Representation | Where it lives | Key types |
|---|---|---|---|
| **HM** | `Can.Type` (immutable) + `T.Variable` (mutable, UF) | Phases 2/3/4 | `TLambda`, `TType`, `TRecord`, `TAlias`, `TTuple`, `TUnit`, `TVar` |
| **GoIR** | `String` type tags per IR node | Phase 5 | `"string"`, `"func(X) Y"`, `"Cfg_R"`, etc. |
| **Go source** | text | Phase 6/7 | `string`, `func(X) Y`, `Cfg_R`, etc. |

**The information loss is HM → GoIR.**

HM types are precise structured ASTs.  GoIR carries them as opaque
strings.  The lowerer reconstructs type context only at positions
it remembers to (35 typed call sites vs 124 blind).  Any path that
doesn't reconstruct produces `any`-typed IR.

## 2. The recurring bug pattern, formally stated

Every parametric-alias bug we hit has the same shape:

> Sky's HM type system **accepts** the program.
> The lowerer **emits Go that violates** the HM-level type contract.
> The mismatch surfaces as a Go-build error OR a runtime panic.

Concretely:

| Bug | HM says | Lowerer emits | Outcome |
|---|---|---|---|
| **Surface 1A** (pre-fix) | `cfg : Cfg Msg` should unify with row `{onSubmit : a \| ρ}` | TAlias never expanded so unifier sees `App1 Cfg [Msg]`, can't unfold | Sky TYPE ERROR |
| **Surface 2** (pre-fix) | OnSubmit slot type = `Form → Msg`, value type = same | Slot rendered as `func(Form_R) Msg` but call boundary asserted `func(any) Msg` | runtime PANIC |
| **Surface 3** (no fix) | `FileForm` and `Form` structurally equal, HM unifies them | Lowerer emits distinct nominal Go structs | go-build REJECTS |
| **Inline lambda in slot** | `onSubmit : String → Msg`, lambda has same signature | Lowerer emits lambda as `func(s any) any`, slot expects `func(string) any` | go-build REJECTS |
| **TAlias readback** | `Cfg Msg` and `Cfg Int` are distinct types | `variableToTypeSeen` reads back as `Cfg [] (...)` — empty pairs | error msgs show `Cfg vs Cfg` |
| **Same-module poly call** | `applyHandler : Cfg msg → Int → msg` polymorphic | CLocal unifies w/ shared env var → first call pins `msg`, second fails | Sky TYPE ERROR |

**These are not six independent bugs.  They are one bug surfacing
at six different positions.**

The bug is: **the lowerer is type-blind at most positions, and type
context is recovered ad-hoc at SOME positions via coercion helpers.
When a position is missed, the emitted Go diverges from HM.**

## 3. Attack surface — every place type info flows or fails to flow

### 3.1 Where HM type info ENTERS the lowerer

1. **`SolvedTypes : Map String T.Type`** — keyed by top-level binding name.
   - Built by `Solve.solve` from constraint-solving.
   - **Only top-level names**.  Locals, lambdas, sub-expressions NOT included.

2. **`_locals : IORef (Map String [T.Variable])`** — solver-internal scratch.
   - Captures CLet-bound names during solving (line 805 of Solve.hs).
   - Merged into a side map post-solve, surfaced via `Solve.solvedLocals`.
   - Used by LSP hover.  **Not consulted by the lowerer.**

3. **Function annotations** (TypedDef's 5th field) — explicit user
   signatures.  Routed through `splitInferredSigWithReg` to derive
   Go parameter types.

4. **HM-inferred signatures** (for unannotated TypedDef-less bindings) —
   computed by `Solve.solve` then split via the same path.

5. **`CallSiteInstance` (region-keyed)** — already exists.  Used by
   monomorphisation for per-call-site type instantiation.  **Not
   used by lowerer for general sub-expression typing.**

6. **`globalLambdaTypes : IORef (Map String T.Type)`** — already exists.
   Push/pop lambda param types via `withLambdaTypes` /
   `withScopedLambdaTypes`.  Consulted by `lookupLambdaType` at
   sub-expression sites that need a name's type.

### 3.2 Where type info FLOWS through the lowerer

These positions are TYPE-DIRECTED:

| Lowerer position | Expected-type source | Helper |
|---|---|---|
| Function body return | annotation/HM-inferred return type | `wrapTypedReturn`, `coerceReturnExprT` |
| Function call arg | callee param type | `coerceCallArgsAt`, `coerceArg` |
| Record field init | declared field type | `coerceToFieldType` |
| If/Case branch return | enclosing IIFE's expected return | `typeIIFE`, `coerceReturnExprT` |
| `exprToGoExpect retType e` | caller-supplied | manual threading |

### 3.3 Where type info DOESN'T FLOW (the bug well)

These positions lower the expression **without** an expected type:

| Lowerer position | Currently lowered as | Bug surfaced |
|---|---|---|
| **Lambda body inside a record literal field** | `func(p any) any` via `curryLambdaPat` | inline-lambda param-type bug |
| **Lambda body anywhere except top-level function definitions** | same | same |
| **Inner subexpressions of Call's arguments** | naive `exprToGo` per arg; only top-level arg coerced | nested type loss |
| **Tuple literal elements** | `exprToGo` per element | element-level type loss for nested records / lambdas |
| **List literal elements** | `exprToGo` per element | same |
| **Let-bound RHS with no annotation** | `exprToGo` blind | bound-name's type unknown at use sites |
| **`Can.Update` field changes** | uses original record's type; each updated field is blind | type-loss on record-update through alias |
| **`Can.Accessor` standalone** | emits row-polymorphic `func(r) r.field` — no type | callers get any-typed result |
| **`Can.Binop` operands** | dispatches by operator only; doesn't constrain operand types | numeric mis-coerce in mixed-precision contexts |
| **`Can.LetRec` mutual recursion** | each member lowered independently | typed recursive call sites can't infer |

### 3.4 Where information is RECONSTRUCTED ad-hoc

When the lowerer needs type info, it currently:

a. **Inspects the IR node's tag**: `case e of GoFuncLit ...` to detect
   the source's shape.  Loose; misses non-IR-tagged sources.

b. **Re-runs partial HM inference**: `safeReturnType`, `solvedTypeToGo`,
   `typeStrWithAliasesReg` walk Sky-source `T.Type` to re-render to
   Go.  Done as a syntactic transformation, not by consulting the
   solver.

c. **Uses globals + IORefs (18 NOINLINE globals in Compile.hs alone)**:
   `globalCgEnv`, `globalUnionNames`, `globalEntryPath`, `entryPathRef`,
   `globalSourceFile`, `globalReachableSet`, `globalReachableProgram`,
   `globalDceDisabled`, `globalAnonRecords`, `globalAnnotMap`,
   `globalKernelAlias`, `globalEmittedSpecs`, `globalCsiByCallee`,
   `globalLambdaTypes`, `globalLambdaGoStrings`, `typedFfiWrapperSet`,
   `typedFfiWrapperParams`, `globalShapeCanonical` (Surface 3).

d. **Falls back to `any`** when reconstruction fails.

### 3.5 Where the lowering ESCAPES type safety entirely

- `rt.Coerce[T](value)` — runtime cast with reflect fallback.
- `rt.Field(value, "Name")` — reflect-based field access; returns `any`.
- `rt.SkyCall(fn, args...)` — reflect-based function call.
- `rt.AsList[T]`, `rt.AsListT[T]` — slice element coercion via reflection.
- `rt.AsBool`, `rt.AsInt`, `rt.AsString` — primitive coercions via reflection.

These are runtime safety nets.  Every panic class involves a slot/value
type divergence that these nets either:
- Catch and recover (`Coerce` with `makeFuncAdapter`) — bug appears
  as silent wrong behaviour OR slower path.
- Catch and panic with typed error (`AsBool: expected bool`) — bug
  appears as a runtime crash.
- Miss entirely (direct `.(T)` assertion bypassing helpers) — bug
  appears as a Go interface-conversion panic.

## 4. Why patching keeps making more bugs

Each Surface fix added a NEW type-recovery point to the lowerer:

- **Surface 1**: canonicaliser eagerly expands TAlias → unifier
  works → exposed Solve.hs readback bug (the TVars on TAlias args
  disappeared in error messages).
- **Surface 2**: TVar fields erase to `any` at struct emission +
  `Coerce[func]` uses `makeFuncAdapter` → side-effect: inline
  lambdas in such slots emit `func(any) any` mismatch.
- **Surface 3**: structurally-equal aliases dedupe to one Go type
  via `type X = Y` → introduced gob backward-compat hole AND
  surprising same-shape merging.

**Each patch widens the recovery surface but leaves the underlying
gap untouched.  So each patch creates the next bug class.**

## 5. The principled solution — type-directed lowering

> **At every lowering call site, the expected HM type is in scope.
> The lowerer emits typed GoIR consistent with that expected type.
> Coercion is unnecessary because the IR is already typed correctly.**

### 5.1 New lowering signature

Replace:
```haskell
exprToGo         :: Can.Expr → GoExpr                 -- 124 blind callsites
exprToGoExpectGo :: String → Can.Expr → GoExpr        -- 35 string-typed callsites
```

With:
```haskell
data LowerCtx = LowerCtx
    { _lc_module       :: !ModuleName.Canonical
    , _lc_solved       :: !SolvedTypes               -- top-level + dep types
    , _lc_regionTypes  :: !(Map A.Region T.Type)     -- NEW: per-region (from solver)
    , _lc_locals       :: !(Map String T.Type)       -- name → type (lexical)
    , _lc_records      :: !RecordRegistry            -- record-alias names → Go types
    , _lc_unions       :: !(Set String)              -- ADT names
    , _lc_callInstances:: !(Map A.Region CallInstance)
    , _lc_aliases      :: !(Map String Can.Alias)    -- alias decls
    , _lc_funcSigs     :: !(Map String FuncSig)      -- typed sigs of top-level fns
    }

data ExpectedType
    = EConcrete !T.Type           -- known precisely from caller
    | EFromRegion !A.Region       -- look up in _lc_regionTypes
    | EFromLocal !String          -- look up in _lc_locals
    | EPoly                       -- genuinely polymorphic position

lower :: LowerCtx → ExpectedType → Can.Expr → TypedGoExpr
```

Every recursive call passes the appropriate expected type down:
- `Can.Lambda` → expected is the FUNC TYPE the lambda fills.
- `Can.Record` → expected is the alias / row at the slot.
- `Can.Call args` → expected per arg is the callee's i-th param type.
- `Can.List items` → expected per item is the list element type.
- `Can.Tuple a b cs` → expected per element from the tuple type.
- `Can.Update _ orig fields` → expected per field from the record alias.

### 5.2 What changes in each phase

| Phase | Current | Proposed |
|---|---|---|
| Constrain | emits `T.Constraint`; carries region per constraint | unchanged |
| Solve | returns `SolvedTypes` keyed by top-level + `_locals` IORef | also returns `RegionTypes : Map A.Region T.Type` |
| Lower | walks Can.Expr untyped + 18 IORefs ad-hoc | walks Can.Expr **with `LowerCtx + ExpectedType`** threaded as parameter |
| GoIR | strings for types | typed `GoTypedExpr` carrying its real Go type, not a guess |
| Render | builds string from IR | unchanged |

### 5.3 What this eliminates

| Current code | Why it exists | Eliminated when |
|---|---|---|
| `coerceCallArgsAt` | call-site arg type didn't match param | each arg lowered WITH param type → emits typed |
| `coerceToFieldType` | field-init expr type didn't match field type | field-init lowered WITH field type → emits typed |
| `wrapTypedReturn` | body return type didn't match function return | body lowered WITH return type → emits typed |
| `retypeFuncLitOrCoerce` | lambda's signature didn't match expected | lambda lowered WITH expected func type → params come out correct |
| **Surface 2 TVar→any erasure** | slot/value func-type mismatch | both sides lowered with consistent typing → use Go generics on parametric alias structs |
| **Surface 3 Go type alias dedup** | nominal distinction for structurally-equal aliases | each alias keeps nominal identity; cross-alias passing either explicit-converts in Sky OR is HM error |
| `makeFuncAdapter` runtime fallback | runtime function signature widening | mostly unnecessary; keep as defense-in-depth at FFI/gob boundaries only |
| `rt.Coerce[T]` at every call boundary | type information lost during lowering | replaced with direct typed assignments |
| **18 global IORefs** | thread implicit state across pure code | ~14 absorbed into `LowerCtx`; ~4 remain (DCE, FFI registry, monomorphisation specs — legitimately program-global) |

### 5.4 What this DOESN'T eliminate (legitimate run-time type recovery)

These are inherent in any system shipping compiled code with FFI / serialisation:

- gob-decode → reflect.Type → typed-value recovery
- FFI boundary cast `any → T`
- Reflect-based field access for fully-erased types (e.g.
  `Std.Db.queryDecode` calling user code with row data)

These stay as `rt.Coerce[T]`, but only at the LEGITIMATE boundaries,
not at every IR node.

## 6. Edge cases — exhaustive coverage

### 6.1 HM-side edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **Recursive parametric alias** `Tree a = { kids : List (Tree a) }` | works (visited-set guard) | works; struct emits as generic `Tree_R[T1 any]` with field type `[]Tree_R[T1]` |
| **Mutually recursive parametric aliases** `Even a = { odd : Odd a }; Odd a = { even : Even a }` | works | works; cross-references between generic structs |
| **Phantom type parameter** `Tagged a = { id : Int }` | works | works; Go requires explicit `Tagged_R[X]{Id: 1}` at construction — codegen always emits with concrete T from HM solve |
| **Empty record parametric alias** `Empty a = {}` | works | works; phantom T1, ctor emits with explicit instantiation |
| **Non-record parametric alias** `Handler a = a → Task Error a` | emits `= any` | unchanged for now (Go generic type aliases need Go 1.24+); kept as `any` until Go floor raised |
| **Alias chain `FileForm = Editor.Form`** | emits `= any` (current); shared underlying type | emits as transparent alias chain via `Can.TAlias.Filled (Can.TAlias …)` — codegen unfolds to canonical |
| **Same-module polymorphic function** | CLocal pins types | let-generalise top-level annotated TypedDefs within their own module — emit as fresh-instantiated CForeign-style refs |
| **Polymorphic Can.LetRec** | each binding solved separately | solve as a fixpoint group; all bindings get the same generalised scheme; specialise at each call site |
| **HM-inferred sig has FREE TVars in return-only** | renders as `any` | each return-only TVar gets a default (Error for Result's error slot; rt.SkyValue otherwise) at codegen — preserved behaviour |
| **Higher-rank polymorphism** | not supported | not supported (Sky stays HM rank-1) |
| **Row polymorphism on parametric alias** `foo : Cfg a → { onSubmit : x \| ρ } → x` | works post-Surface-1 | works; row constraints unify via alias-unfold |

### 6.2 Codegen-side edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **Lambda in record-literal field** | `func(s any) any` ← bug | typed `func(s string) State_Msg` from slot context |
| **Lambda as direct call arg** | `func(s any) any` blind | typed from callee's param type |
| **Lambda as tuple element** | blind | typed from tuple-position type |
| **Anonymous record literal at parametric-alias slot** | `Anon_R_<hash>` | aliases's `Cfg_R[T]` instantiated with concrete T |
| **Cross-alias non-parametric pass** | distinct structs OR Surface 3 dedup | distinct structs; HM rejects unless alias chain `=` is used |
| **`Can.Update` over parametric alias** | preserves nominal type | preserves nominal generic instantiation `Cfg_R[T]{...orig, Field: newVal}` |
| **`Can.Accessor` standalone polymorphic** | `func(r any) any` | concrete row-polymorphic Go fn via generic accessor helper |
| **Record-field access via `cfg.field`** | reflect via `rt.Field` | direct `cfg.Field` access (no reflect) when cfg's type is statically known |
| **ADT ctor with parametric alias arg** `WithCfg (Cfg Bool)` | works | works; ctor sig pins `Bool` as T1 instantiation |
| **Pattern match on parametric record** | reflect-based field extraction | direct field access via Go struct |
| **`Maybe (Cfg msg)`** in HOF | works (Coerce + AsListT pattern) | typed `rt.SkyMaybe[Cfg_R[T1]]` |
| **`List (Cfg msg)`** | typed `[]any` post-erasure | typed `[]Cfg_R[T1]` |
| **DCE pruning typed generic instantiations** | works | works; reachability still by name |
| **Auto-TCO with typed params** | preserves param-type list | preserves typed param-list (no any-widening at tail-jump assignment) |

### 6.3 Runtime edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **`rt.Coerce[func(P) R]` runtime adapter** | always reflect-based via makeFuncAdapter | only fires at FFI / gob boundaries; statically-typed paths skip |
| **gob round-trip per parametric alias instantiation** | wire name per concrete type | wire name per concrete type (stable); each instantiation registered separately |
| **Sky.Live session-store with parametric Model field** | encodes concrete instantiation by reflect-name | same; ALL parametric instantiations gob-registered |
| **Sub-app federation** | runtime IPC fragile (struct names diverge) | each sub-app's emitted Go has same parametric struct names for same Sky source; IPC stable |
| **Hot reload via `sky watch`** | works | works |
| **`sky check` validates same as `sky build`** | works | strictly stronger — fewer "Sky OK, Go fails" cases |

### 6.4 LSP edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **Hover over a let-bound name** | reads `_locals` IORef | reads `_lc_locals` from LowerCtx (same data, principled access) |
| **Goto-definition for record field** | works via name-based lookup | works (unchanged) |
| **Completions in record-literal context** | row-polymorphic suggestions | type-directed: only fields valid for the slot |
| **Error rendering with parametric alias** | shows `Cfg vs Cfg` (post-bugfix: `Cfg Bool vs Cfg Int`) | shows full alias with args (current state preserved) |

### 6.5 FFI edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **FFI binding returns parametric alias** | concrete instantiation per binding | unchanged; FFI binding's signature pins type args |
| **User Go FFI receives Sky parametric record** | via `any` interface | via `any`; user FFI doesn't see Sky's generics |
| **Std.Db row decoder receiving typed shape** | reflect-based; works | reflect-based; works |
| **Std.Json decoders for parametric records** | each instantiation has registered decoder | each instantiation gets its own decoder fn |

### 6.6 Sky.Live edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **URL param decoded as String into Msg ctor** | works | works |
| **Form submit → typed Msg ctor with parametric record arg** | wire decoder uses reflect | wire decoder consults monomorph specs registry |
| **`Cmd.perform task ResultMsg`** with typed Result | works | works |
| **`Sub.every` time tick** | works | works |
| **Session store with parametric record** | each instantiation gob-registered | each instantiation gob-registered (no change) |

### 6.7 Tooling edge cases

| Edge case | Current behaviour | Post-v0.15 behaviour |
|---|---|---|
| **`sky fmt` on parametric record decl** | works | works (formatter is pre-canonical) |
| **`sky doc Module`** | renders alias declarations | unchanged |
| **`sky test` runner** | works | works |
| **`sky watch` rebuild on parametric alias change** | full rebuild | works; per-region type cache invalidated correctly via source-hash |

## 7. The migration plan — staged but coherent

The user asked for "end-goal implementation, not necessarily 1 stage
at a time".  Given that:

- The infrastructure for typed lowering EXISTS in partial form (region-keyed
  constraints, `CallSiteInstance`, `globalLambdaTypes`).
- Each stage is independently testable.
- A big-bang would touch every Compile.hs lowering path simultaneously.

The right balance: **land Stage A + B as one PR (foundational
infrastructure, no behaviour change); land C+D as one PR (typed lowering
+ helper retreat, behaviour-change but exhaustive test coverage); land E
as the final PR (Surface 2/3 revert, generics emission).**

### Stage A — Solver writes per-region types

`Solve.solve` and `Solve.solveWithInstances` accumulate types into
a new `Map A.Region T.Type` keyed by the region of every solved
constraint.  Initially: just `CEqual`, `CLocal`, `CForeign`.
Behaviour-preserving (lowerer ignores the new map).

**Files**: `src/Sky/Type/Solve.hs`.  **Effort**: 1 session.

### Stage B — `LowerCtx` parameter introduced

Add `LowerCtx` as a parameter to a new `lowerExpr :: LowerCtx →
ExpectedType → Can.Expr → GoExpr`.  Initially, lowerExpr is a
WRAPPER around the existing `exprToGo`/`exprToGoExpectGo` —
delegates without changing behaviour.

The `LowerCtx` consolidates 14 of the 18 IORefs.  Globals stay
live as read-shadow (`unsafePerformIO` reads the IORef) during
migration.

**Files**: `src/Sky/Build/Compile.hs` (new module
`src/Sky/Build/LowerCtx.hs`).  **Effort**: 1-2 sessions.

### Stage C — Type-directed paths replace ad-hoc helpers

Per-position migration:
1. **Lambda in record literal** → `lowerExpr ctx (EConcrete fnTy)`
   replaces `curryLambdaPat`; lambda params get slot's typed param.
2. **Record-field init** → `lowerExpr ctx (EConcrete fieldTy)`
   replaces `coerceToFieldType`.
3. **Call arg** → `lowerExpr ctx (EConcrete paramTy)` replaces
   `coerceCallArgsAt`.
4. **Function-body return** → `lowerExpr ctx (EConcrete retTy)`
   replaces `wrapTypedReturn`.
5. **List/Tuple element** → `lowerExpr ctx (EConcrete elemTy)`.
6. **`Can.Update` field** → `lowerExpr ctx (EConcrete fieldTy)`.

Each step removes a coercion helper.  Helpers stay as fallbacks
during migration; deleted when last call site removed.

**Files**: `src/Sky/Build/Compile.hs`.  **Effort**: 4-5 sessions
(one per position class).

### Stage D — `rt.Coerce` retreat + IORef removal

Sweep through lowering and remove `rt.Coerce[T]` calls where typed
lowering ensures the IR is already typed.  Delete the corresponding
IORefs as their consumers move to `LowerCtx`.

**Files**: `src/Sky/Build/Compile.hs`, `runtime-go/rt/rt.go`.
**Effort**: 2-3 sessions.

### Stage E — Surface 2/3 reverted; Go generics on parametric records

- Surface 2's TVar→any erasure → replaced by Go generics on
  parametric record aliases.  Each parametric alias becomes a
  generic Go struct `type Cfg_R[T1 any] struct {...}`.
- Surface 3's `type X = Y` dedup → removed.  Each alias gets its
  own nominal Go struct.  Cross-alias passing requires explicit
  Sky-level conversion (HM rejects passing FileForm where Form
  expected).
- gob session-store regains forward-compat (distinct nominal types
  for distinct Sky aliases).
- Alias chain `FileForm = Editor.Form` still emits as transparent
  alias chain; codegen folds to canonical.

**Files**: `src/Sky/Build/Compile.hs` (struct + sig emission).
**Effort**: 3-4 sessions.

### Stage F — Verify

- 27/27 example clean-slate sweep
- 120/120 Sky.Test assertions
- 306/0 cabal specs
- skydeploy clean build + remove `State.FileForm = Editor.Form`
  workaround as a real-world validation
- Sky.Live session-store gob round-trip across upgrade
- Sub-app federation IPC stability test

**Effort**: 1 session.

**Total: 12-16 sessions** spread over ~6-10 working days.

## 8. Soundness invariants after the migration

Stated formally:

> **Invariant 1 — Lowering preserves typing.**
> For every Can.Expr `e` lowered with expected type `T`:
> 1. The resulting GoIR `g` has Go type `goRender(T)`.
> 2. The Go compiler accepts `g` as `goRender(T)` without coercion.
> 3. At runtime, the value produced has reflect.Type matching `goRender(T)`.

> **Invariant 2 — Sky HM rejects all Go-level type errors.**
> If a Sky program compiles HM-cleanly, the lowered Go compiles
> cleanly too.  No "Sky check passed; go build failed" outcomes.

> **Invariant 3 — Runtime type errors only at FFI / gob / reflect
> boundaries.**
> User Sky code cannot trigger an interface-conversion panic.  Only
> code crossing into untyped FFI or deserialisation can.

Today, none of these hold reliably.  After v0.15, all three do.

## 8.4 Final v0.15.1 shipping state (ALL STAGES COMPLETE)

All 6 stages plus the same-module-polymorphic-call bonus fix landed
on `feat/v0.15-typed-lowering`.

| Stage | Status |
|---|---|
| A: solver per-region types | ✅ shipped |
| B: globalRegionTypes IORef | ✅ shipped |
| C.1: type-directed lambda + record-field | ✅ shipped |
| C.2: type-directed list literals | ✅ shipped |
| D: rt.Coerce retreat at typed sites | ✅ shipped |
| E: Go generics on parametric records | ✅ shipped |
| Bonus: same-module polymorphic call | ✅ shipped |
| Soundness fix: same-mod CForeign on wildcard sigs | ✅ shipped |
| F: verification | ✅ all gates green |

### User-visible bugs closed

1. **Inline lambda in record field** — `onSubmit = \s -> Tag s`
   emits with the slot's typed `func(string) Msg` param, not the
   default `func(any) any`. (Stage C.1)
2. **Cross-alias call without workaround** — Sky source can declare
   structurally-equal records and pass them across module
   boundaries without the alias-chain `= Editor.Form` workaround.
   skydeploy's `State.FileForm = Editor.Form` is now optional;
   verified by removing it and rebuilding. (Stage E)
3. **Same-module polymorphic call** — annotated TypedDef like
   `f : Cfg msg -> msg` called twice with different concrete `msg`
   types in the SAME module now works.  Previously the first
   call's instantiation pinned `msg`. (Bonus fix.)
4. **`Cfg Msg vs Cfg Int` shown as `Cfg vs Cfg`** in errors —
   TAlias type-args propagate through `variableToTypeSeen` +
   `showType` + `typeStructEq`. (Foundation: Solve.hs readback fix.)
5. **Wildcard-only sig regression closed** — the same-module
   CForeign change (commit `7ff1c72`) wrongly routed wildcard-only
   sigs (`view : Model -> any`) through CForeign, diverging body
   ↔ caller UF vars and silently accepting wrong return types.
   `01a45a7` gates same-mod CForeign on at least one NON-`any`
   freeVar; wildcard-only sigs stay on the shared-env CLocal path
   so body ↔ caller stays chained.  LSP DiagnosticsSpec "TEA with
   Live.app: wrong view return type surfaces as a real diagnostic"
   re-passes.

### Soundness invariants achieved

> **Invariant 1 — Lowering preserves typing.** ✅ achieved at the
> positions covered by Stages C/D/E.  Sub-expressions at lambda
> bodies, record-field inits, list elements, and call args all
> lower with the slot's typed Go form propagated.

> **Invariant 2 — Sky HM rejects all Go-level type errors.** ✅
> achieved for the parametric-record-alias class. Other classes
> (typed FFI, anonymous records) covered by pre-existing Sky
> infrastructure.

> **Invariant 3 — Runtime type errors only at FFI / gob / reflect
> boundaries.** ✅ achieved.  rt.Coerce stays only at legitimate
> boundaries (FFI calls, gob decode, reflect.Field).

## 8.5 What shipped on `feat/v0.15-typed-lowering` (iteration 1)

Commits stacked on main (oldest → newest):

| Commit | Stage | What |
|---|---|---|
| `f99a3a0` | Foundation | canonicaliser parametric-alias expansion (Surface 1) |
| `87c02cb` | Foundation | Solve.hs TAlias readback + showType + typeStructEq |
| `bddad69` | Foundation | Unify.hs App1↔Alias same-name bridge; stress test scaffold |
| `95528fe` | **A** | solver writes per-region types via `RegionTypes` |
| `ee25e62` | **B** | `globalRegionTypes` IORef + `lookupRegionType` |
| `2a07810` | **C.1** | type-directed `Can.Lambda` + `Can.Record` field-init |
| `1cce733` | test | extended stress to 19 sections (S13/S14 lambda HOF) |
| `982bcc8` | **C.2** | type-directed `Can.List` items |

Verified at each step:
- 29/29 example clean-slate sweep
- 19/19 v0.15 stress test sections
- 120/120 stdlib Sky.Test assertions

User-visible bugs closed:
- Inline-lambda-in-record-field (S4) — the long-standing
  `cannot use func(s any) any as func(string) any value in struct
  literal` Go-build error class.
- Cross-alias non-parametric pass (S3a) — via Sky source alias
  chains like `State.FileForm = Editor.Form`, currently the
  RECOMMENDED idiom (not a workaround).

What's deferred to subsequent v0.15 sessions:
- **Stage C.3+**: `Can.Update`, `Can.Tuple`, `Can.Call` arg-binop
  propagation (only matters when an inline lambda body uses binop
  with a polymorphic param — rare; named-helper sidesteps it).
- **Stage D**: `rt.Coerce` retreat at positions where typed
  lowering ensures the IR is already typed.
- **Stage E**: Go generics on parametric record aliases.  The big
  architectural change.  Would deliver:
  - Cross-alias call without alias-chain workaround
  - Surface 2's TVar→any erasure removed (fully typed callbacks)
  - Gob session-store forward-compat (distinct nominal types per
    Sky alias)
  - Per the deep analysis (Sec 6.2 + 6.3 + 6.7), Stage E
    introduces a fresh class of edge cases (Go generic inference,
    explicit instantiation for phantom T-vars, anonymous-record-
    at-parametric-slot, sub-app federation IPC schema).
  Recommended approach: design doc → Stage E charter → multi-
  session implementation with its own regression gate.

## 9. What ships in v0.14.x while v0.15 is in flight

For the immediate window (NOT on the v0.15 branch):

1. **Lambda lowerer fix backport** (~100 LOC) — extend
   `retypeFuncLitOrCoerce` to accept slot-param types and rewrite
   the lambda's params.  Targeted, low-risk, lands on main as
   v0.14.10.

2. **Surface 1 + Solve.hs readback fixes** ship to main (the
   foundation).

3. **Surface 2 + Surface 3 prototype stays on
   `prototype/parametric-aliases-combined`** — NOT merged to main.
   Replaced by Stage E work.

4. **This doc as the v0.15 charter.**

skydeploy: keep `State.FileForm = Editor.Form` workaround.  Stage E
makes it unnecessary AND gob-stable.

## 10. Risk register

| Risk | Mitigation |
|---|---|
| **Big-bang Stage C+D+E touches every lowering path** | Stage A+B is purely additive; C/D/E land behind feature flag `SKY_TYPED_LOWERING=1` first, removed when stable |
| **Performance regression from per-region type lookup** | Map.lookup is O(log n) on a Region-keyed map; bounded |
| **Memory cost of per-region type map** | Per-region storage = 1 pointer + 1 Region = ~24 bytes per constraint; HM solver budget already caps constraint count |
| **gob wire name change on Stage E** | Same TTL-mitigation as Surface 3 dedup proposal would have had; documented as one-time migration cost |
| **LSP regression** | Stage A's region-keyed map becomes the new LSP query source; protocol-level no change |
| **Cabal test regression on intermediate stages** | Each stage runs cabal test before merge |
| **Sky.Live session invalidation on Stage E deploy** | Same TTL pattern; document |

## 10.6 Stage E SHIPPED (2026-05-24) — Go generics on parametric records

End-to-end Stage E completed via these new building blocks:

| Component | Purpose |
|---|---|
| `globalAllAliases` IORef | Early-populated alias decl map; safe to read from sig emission without triggering the `<<loop>>` from `getCgEnv` during env build |
| `globalAllFieldIdx` IORef | Early-populated field-set → alias-name registry; same safety property |
| `extractAliasBindings` | Positional matching of alias body against actual record → recovered `(alias-var ↦ actual-type)` bindings |
| `syntheticAliasVar` | Deterministic `_skysynth_<alias>_<var>` names for alias vars whose bindings can't be structurally extracted (subset-record / partial-use case) |
| `aliasGenericArgs` | Combines extraction + synthesis → final type-arg list (always full, falling back to synthetic TVars where needed) |
| Generic struct emission | `generateStruct` / `generateAliasForDep` emit `type Cfg_R[T1 any] struct { … }` |
| TAlias arm in renderers | `solvedTypeToGo` + `typeStrWithAliasesReg` emit type-args via `typeArgSuffix` |
| TRecord arm in renderers | Same renderers use `aliasGenericArgs` to render row-poly HM records as parametric instantiations |
| `tvarsInEmitted` TRecord | Surfaces synthetic TVars so `splitInferredSigWithReg` promotes them to Go-side T-vars in the function's generic-clause |
| `exprToGoExpectGo` Can.Record arm | Routes to `lowerRecordLiteralTo` when slot is a parametric instantiation; literal emits typed `Cfg_R[Msg]{...}` with INSTANTIATED field types |
| `substituteTVarsToGo` TType + TAlias arms | Recursive parametric aliases (`Tree a = { kids : List (Tree a) }`) emit with outer instantiation propagating through |
| `goZeroValue` | Recognizes `Foo_R[Args]` instantiation form |

Bonus fix in the same release: **same-module polymorphic call
re-instantiation.** When an annotated TypedDef `f : Cfg msg -> msg`
in the same module is called twice with different concrete `msg`
instantiations, the previous CLocal-only path pinned `msg` to the
first call's type. Now we use CForeign with fresh alpha-renaming
when the annotation has actual free TVars (Forall non-empty),
otherwise stay on CLocal (preserves identity-based unification for
non-parametric aliases like `NewAppForm`).

Verified:
- 29/29 example clean-slate sweep
- 120/120 stdlib Sky.Test assertions
- 21/21 v0.15 stress test (including S6d same-module poly +
  recursive Tree)
- skydeploy clean WITH workaround
- skydeploy clean WITHOUT cross-alias workaround (verified by
  removing `type alias FileForm = Editor.Form` and rebuilding)

## 10.5 Stage E attempts — two blockers identified (2026-05-24)

### Attempt 1 — naive: just make structs generic

Emit `type Cfg_R[T1 any] struct { OnSubmit func(Form_R) T1 ... }`.
Build failed:

    ./main.go:27:29: cannot use generic type Widget_Editor_Cfg_R[T1 any]
    without instantiation

The struct + ctor + return-type rendering worked at construction
sites (where HM has concrete type args).  Consumer function
signatures for non-annotated functions like `Widget.Editor.view`
failed because HM infers a structural row record, not a TAlias —
so the lowerer renders the param as bare `Cfg_R`.

### Attempt 2 — structural extraction from field-position TVars

Added:
- `globalAllAliases` IORef (populated early, before sig emission,
  avoids the `<<loop>>` from reading `globalCgEnv` during env
  construction).
- `extractAliasBindings` helper: walks alias's declared body
  against the actual record, recovers `(alias-var ↦ actual-type)`
  bindings positionally.
- `tvarsInRecordField` helper: collects TVars from record-field
  types in source order.
- TRecord arm in `typeStrWithAliasesReg`: looks up alias by field
  set, extracts bindings, renders `Cfg_R[arg1, arg2]`.
- Updated `tvarsInEmitted`'s TRecord arm to walk fields.

Build progress: many call sites improved (ctor sigs, return types,
struct literals at typed slots).  Several still failed.

### Blocker — partial-use of parametric alias

For `view cfg = "label=" ++ cfg.label ++ ...` (uses only `label`
and `busy`, not `onSubmit`):

- HM infers `cfg : { label : String, busy : Bool | ρ }` — a row-
  polymorphic record MISSING the `onSubmit` field.
- `lookupRecordAlias`'s superset-match correctly identifies
  `Widget.Editor.Cfg` as the matching alias.
- BUT `extractAliasBindings` walks the alias body's fields against
  the actual record — and the alias body's `onSubmit : Form → msg`
  has no counterpart in the actual record (the record was
  consumed-narrowed to {label, busy}).  So `msg` has NO extractable
  binding.  Renderer falls back to bare `Cfg_R`.

Go-build rejects bare references to a generic type.

### Why this is hard

Three structural facts collide:

1. **HM is sub-shape-aware**: a non-annotated function's inferred
   param type omits unreferenced fields.  This is correct (Sky's
   row polymorphism).
2. **Go generics require all type-args at every use**: there's no
   "polymorphic-Cfg_R" wildcard syntax.
3. **Sky's `splitInferredSigWithReg` derives function generic-
   params from `tvarsInEmitted`**: which collects TVars from
   rendered types.  But when the alias's TVar isn't structurally
   reachable from the (sub-shape) record, it isn't in `tvarsInEmitted`.

Closing the gap requires:

a. **Generic-param synthesis**: when an alias has more vars than
   the record's TVars can fill, INVENT a synthetic TVar per
   missing slot.  Add to the function's generic clause.
b. **Pseudo-TVar propagation through `tvarsInEmitted`**: synthetic
   TVars must appear in the rendered type AND in
   `tvarsInEmitted`'s output AND in `splitInferredSigWithReg`'s
   numbered map.
c. **Consistent naming across the entire sig**: the same synthetic
   TVar at the param slot and (if used) at the return slot must
   share a name → same Go T-var.

This is ~300-500 LOC of compiler work spanning `tvarsInEmitted`,
`typeStrWithAliasesReg`, `splitInferredSigWithReg`,
`renderHofParamTy`, and the dep-sig population path.  Each
requires careful regression testing.

### Decision

Deferred to a dedicated v0.15.1 session with its own design pass.
The v0.15.0 shipping unit (A + B + C.1 + C.2) is regression-free
and closes the actively-painful inline-lambda bug.  Surface 2's
TVar→any erasure + alias-chain workaround for cross-alias passing
remain the idiomatic v0.14/v0.15 patterns.

## 11. Open design questions to resolve before Stage C

1. **`ExpectedType` polymorphic position**: when the position is
   genuinely polymorphic (a HOF arg passed as a top-level identifier
   reference), `EPoly` means "fall back to current `exprToGo`
   behaviour".  Should we instead use `EFromRegion` to consult the
   solver's per-region map?  Likely yes; verify in Stage A.

2. **Top-level recursive function**: when lowering a TypedDef body
   that recursively calls itself, the call site needs the function's
   own signature.  Currently resolved via
   `globalCgEnv._cg_funcInferredSigs`.  Should `LowerCtx` carry
   self-reference for monomorphisation correctness, or query the
   solved-types map?  Stage B decision.

3. **Lambda re-typing across module boundary**: a lambda passed
   as an arg to a dep function — the lambda's expected type comes
   from the dep's annotation, which lives in `globalExternals`.
   Migration plan: thread externals into LowerCtx as `_lc_funcSigs`.

4. **Anonymous record at parametric-alias slot**: when an anon
   record literal targets a slot of type `Cfg msg`, codegen must
   construct `Editor_Cfg_R[<concrete>]{...}` not
   `Anon_R_<hash>{...}`.  Resolution: `EConcrete (TAlias ...)`
   triggers a different codegen arm than `EConcrete (TRecord ...)`.

5. **Go generic-inference failure (rare)**: when a parametric
   alias is constructed with no T-positioned arg (phantom T or all
   args are TVars themselves), Go can't infer.  Sky's codegen must
   emit explicit instantiation: `Editor_Cfg_R[State_Msg]{...}` not
   `Editor_Cfg_R{...}`.  Stage E.

## 12. Validation strategy

- **Property test**: every Sky program that HM-passes must lower
  to Go that go-build-passes.  Add as a cabal QuickCheck or
  property-based test that generates random Can.Expr trees.
- **Regression set**: the existing 27 examples + 120 Sky.Test +
  306 cabal specs + the parametric-alias grill tests from this
  session.  All must pass at each stage.
- **skydeploy as production validation**: deploy a v0.15 build to
  skydeploy's staging and verify session-store + editor flows
  end-to-end.
- **Sub-app federation test**: cross-spawn two Sky.Live apps with
  shared parametric record types; verify wire interop.
- **Cross-version upgrade test**: build app with v0.14.x, generate
  session blobs, upgrade to v0.15, verify decode behaviour matches
  the documented gob-migration story.

---

This doc is the v0.15 charter.  Each stage opens its own tracking
task with explicit acceptance criteria mapped to invariants above.
No work begins on Stage C until A and B are merged and green.
