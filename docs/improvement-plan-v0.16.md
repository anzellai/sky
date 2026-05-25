# Improvement plan — Sky compiler v0.16+

> Status: planning artefact for branch `refactor/compiler-fragility-audit`. Author: Agent B (compiler expert review of Agent A's audit at `docs/fragility-audit-v0.15.3.md`). Date: 2026-05-25.

> The audit identifies 17 fragility items; the RFC at `docs/v1-rfc/type-soundness-deep-analysis.md` already names the principled solution (full type-directed lowering via `LowerCtx`). Stages A–E shipped in v0.15.0/.1, closing the worst Surface-1/2/3 bugs. What remains is the **fragility tail**: 18 NOINLINE IORefs (`Compile.hs:67-295`), 48 `unsafePerformIO` uses, 10+ branches in `coerceArg` (`Compile.hs:8371-8547`), and an `inferExprType` that returns `Nothing` for `Can.Lambda`/`Can.Update`/`Can.Accessor`/`Can.LetRec` (`Compile.hs:11178-11232`). This plan turns those into discrete, sequenced, low-risk PRs.

> Scope contract: no language-surface breaking changes; all 306 cabal tests + 27 examples + 120 stdlib assertions must remain green at every PR boundary; `examples/13-skyshop` build time and binary size must not regress > 3 %.

---

## 1. Triage of audit findings

Decisions: **fix** (root-cause work this cycle), **defer** (track but don't block v0.16), **accept** (documented invariant, not a bug).

| # | Audit item | Severity | Decision | Rationale |
|---|---|---|---|---|
| 1 | Lambda-types IORef push/pop race vs lazy GoIR rendering | Critical | **fix** | Root cause of every "lazy-deferral" panic class. §2. |
| 2 | `inferExprType` returns `Nothing` for Lambda/Update/Accessor/BinOp/LetRec | Critical | **fix** | Each `Nothing` collapses to `"any"` → downstream uses `any(...)` → runtime panic. §4. |
| 3 | `containsGenericTypeParam` gate applied backward at `coerceArg` | Critical | **fix** | Symptom-fix inside §3. |
| 4 | `eraseTypeParams` loses info at container boundaries | Critical | **fix** | Replace with structural walk over `GoType` ADT. §3. |
| 5 | Wildcard-`any` gate easy to mis-edit | Critical | **accept + lock** | Add explicit test (Invariant I6). |
| 6 | `lookupLambdaGoStr` stale entries | Critical | **fix** | Subsumed by §2 (LowerCtx is scoped per-module). |
| 7 | `coerceArg` 10+ branches | High | **fix** | Collapse to ≤4 cases. §3. |
| 8 | `inferExprType` ↔ `globalRegionTypes` divergence | High | **fix** | Subsumed by §4. |
| 9 | `withLambdaTypes` permanent global mutation | High | **fix** | Goes away with LowerCtx. |
| 10 | `splitCurriedFuncStr` bracket-depth bug | High | **fix** | One-line fix inside §3. |
| 11 | `globalRegionTypes` not populated for all regions | Medium | **fix-with-§2** | Snapshot at lowering entry. |
| 12 | Monomorphisation type-alias equivalence | Medium | **defer** | Track in v0.17. |
| 13 | `rt.Coerce` Kind-based reflect fallback | Medium | **defer** | Already documented safety net. |
| 14 | `defToStmts` zero-param routing on `canRouteTyped` | Medium | **fix** | Subsumed by §4. |
| 15 | `globalLambdaGoStrings` not cleaned between phases | Low | **fix-incidentally** | Goes away with §2. |
| 16 | `eraseTypeParams` over-zealous | Low | **fix-with-§3** | Same fix as #4. |
| 17 | `parametricAliasBase` string heuristics | Low | **fix-with-§3** | Replaced by structural classifier. |

**Headline**: 11 fixes, 3 defers, 2 accept/lock.

---

## 2. Priority 1 — Race-free type-context threading (`LowerCtx`)

### Problem (audit #1, #6, #9, #15)

`withScopedLambdaTypes` / `withScopedLambdaGoStrings` force the GoExpr to a string inside the IORef bracket so the pop happens after the read. This works ONLY when there is no laziness escape. The v0.15.3 editor panic was exactly such an escape. The fix added a second IORef (`globalLambdaGoStrings`) as a fallback — band-aiding the race rather than fixing it.

### Fix — introduce explicit `LowerCtx` reader monad

Replace the two `globalLambda*` IORefs with a `LowerCtx` record threaded as an explicit parameter through every `exprToGo` / `exprToGoExpectGo` / `coerceArg` / `kernelCoerceArg` / `letBindingType` etc.

**New module** `src/Sky/Build/LowerCtx.hs` (~150 LOC):

```haskell
data LowerCtx = LowerCtx
    { _lc_module       :: !ModuleName.Canonical
    , _lc_solved       :: !Solve.SolvedTypes
    , _lc_regionTypes  :: !Solve.RegionTypes
    , _lc_lambdaTypes  :: !(Map.Map String T.Type)
    , _lc_lambdaGoStr  :: !(Map.Map String String)
    , _lc_aliases      :: !(Map.Map String Can.Alias)
    , _lc_fieldIdx     :: !Rec.RecordRegistry
    , _lc_unionNames   :: !(Set.Set String)
    , _lc_aliasMap     :: !(Map.Map String String)
    , _lc_annotMap     :: !(Map.Map String T.Annotation)
    }
```

### Implementation steps

1. Add the module (no callers yet) — 1h.
2. Build `LowerCtx` at entry to `generateGoMulti` (`Compile.hs:3069`). Pass to wrapper functions. — 3h.
3. Migrate ~90 call sites bottom-up — 8-10h, mechanical.
4. Replace `withScopedLambdaTypes` calls with `(withLambdaTypes m ctx)` passed to recursive call — 2-3h.
5. Delete `globalLambdaTypes`, `globalLambdaGoStrings`, `globalRegionTypes` IORefs — 30min.
6. Keep `globalCgEnv`, `globalReachableSet`, `globalReachableProgram`, `globalDceDisabled`, `globalEntryPath`, `globalSourceFile` as legitimately program-global.

### Files affected

- **New**: `src/Sky/Build/LowerCtx.hs` (~150 LOC).
- **Edited**: `src/Sky/Build/Compile.hs` (~600 LOC of signature changes; no logic change).
- **Untouched**: `Solve.hs`, `Monomorphise.hs`, `runtime-go/rt/*.go`.

### Risk

Low — sequence-equivalent. Every IORef read becomes a `Map.lookup` of the same key against the same data.

### Estimate

**14-16 hours**, splittable into 4 hour-sized commits.

---

## 3. Priority 2 — `coerceArg` simplification

### Problem (audit #3, #4, #7, #10, #16, #17)

`coerceArg` operates on string-encoded Go types. Every classifier is a hand-rolled string parser. Audit #4 and #10 are direct consequences: any non-trivial nested generic gets mis-parsed.

### Fix — small structural `GoType` ADT

**New module** `src/Sky/Build/GoType.hs` (~120 LOC):

```haskell
data GoType
    = GoAny
    | GoPrim PrimTy
    | GoTVar Int
    | GoNamed String [GoType]
    | GoSlice GoType
    | GoMap GoType GoType
    | GoFunc [GoType] GoType
    | GoTuple Int

parseGoType :: String -> GoType
emitGoType :: GoType -> String
eraseTVars :: GoType -> GoType
classifyAlias :: LowerCtx -> GoType -> AliasKind
```

### `coerceArg` reduced to 4 cases

```haskell
coerceArg :: LowerCtx -> GoIr.GoExpr -> GoType -> GoIr.GoExpr
coerceArg ctx e target = case (target, classifyAlias ctx target, exprStaticType e) of
    (GoAny, _, _)                                      -> e
    (_, _, Just src) | src == target                   -> e
    (_, ParametricAlias base _, Just src)
        | ParametricAlias srcBase _ <- classifyAlias ctx src, base == srcBase -> e
    (_, _, _) | Just call <- runtimeContainerCoerce target e -> call
    _                                                  -> goCoerceCall target e
```

### Files affected

- **New**: `src/Sky/Build/GoType.hs` (~120 LOC).
- **Edited**: `src/Sky/Build/Compile.hs` — `coerceArg` reduced from ~177 LOC to ~50 LOC. ~15 helpers deleted. Net -350 LOC.

### Estimate

**18-22 hours**.

### Risk

Medium. Stage as 2 PRs: 3a (behaviour-preserving) + 3b (structural eraser, may flip call sites).

---

## 4. Priority 3 — Complete `inferExprType` arms

### Problem (audit #2, #8, #14)

`inferExprType` returns `Nothing` for `Can.Lambda`, `Can.Update`, `Can.Accessor`, `Can.LetRec`, and `Can.Binop` (`|>`, `<|`, `>>`, `<<`). Each `Nothing` collapses to `"any"` downstream.

### Fix — implement the missing arms

See full plan body — Lambda walks params + body; Update inherits from origExpr; Accessor returns placeholder; LetRec fixpoint; pipe/composition handle binop arity.

### Consequence

Audit #8 (`letBindingType` two-axis gate) and #14 (`canRouteTyped` completeness) drop to medium-priority cosmetic. The body-shape gate is removed entirely.

### Files affected

- **Edited**: `src/Sky/Build/Compile.hs` — `inferExprType` grows by ~70 LOC; `letBindingType` shrinks by ~30 LOC.

### Estimate

**8-10 hours**.

### Risk

Low-to-medium. Each arm is additive.

---

## 5. Priority 4 — Systematic regression test suite

### Layer A — combinatorial shape matrix (`test-files/v0.16-shapes/`)

Auto-generated by `tools/sky-shape-gen/Main.hs`. 10 Containers × 5 ParameterKinds × 4 CallShapes = **200 sections**, ~3000 LOC of Sky.

### Layer B — generated-Go grep gates (`test/Sky/Build/GeneratedGoSpec.hs`)

Forbidden patterns: `any(<typed-var>).(Foo_R[T])`, `rt.Coerce[T_R[X]](T_R{...})`, `func(any) any` inside `*_R{}`, etc.

### Layer C — property-based codegen invariants (`test/Sky/Build/CodegenInvariantSpec.hs`)

QuickCheck properties over random Sky programs.

### Layer D — LSP / sky check parity (`test/Sky/Lsp/CheckBuildParitySpec.hs`)

`sky check ≡ sky build` for every Layer A shape.

### Estimate

**24-30 hours**.

---

## 6. Priority 5 — Optional larger rewrite (full type-directed lowering)

RFC §5.1's `lower :: LowerCtx → ExpectedType → Can.Expr → TypedGoExpr`. Replaces ~169 callsites of `exprToGo`/`exprToGoExpectGo`.

**Recommendation**: DEFER to v0.17. Priorities 1-4 deliver ~70% of the benefit at ~30% of the risk.

### Estimate

**45-55 hours**.

---

## 7. Invariant gates (mechanical)

| # | Invariant |
|---|---|
| I1 | No raw `any(x).(T)` assertion where `x` is statically typed AND `T` is a parametric alias instantiation. |
| I2 | Every `rt.Coerce[T]` call site is either no-op OR crosses a documented dynamic boundary. |
| I3 | `inferExprType ctx e == Nothing` ⇒ `e`'s position NOT type-routed downstream. |
| I4 | `_lc_lambdaTypes` and `_lc_lambdaGoStr` agree under `solvedTypeToGo`. |
| I5 | `sky check ≡ sky build` for every v0.16-shapes section. |
| I6 | `freeTypeVars sig` non-`"any"` count drives polymorphism detection. |
| I7 | Parametric record struct literals always include explicit type args. |
| I8 | `Compile.hs` NOINLINE global IORefs ≤ 5. |
| I9 | `Compile.hs` `unsafePerformIO` uses ≤ 10. |
| I10 | `examples/13-skyshop` build time within ±3% of v0.15.3 baseline. |

---

## 8. Merge order

**6 atomic PRs**, sequenced, each independently green. NO big-bang.

| Order | PR | Branch | Estimate |
|---|---|---|---|
| 1 | `refactor: introduce LowerCtx module and plumbing` | `refactor/lower-ctx-intro` | 8h |
| 2 | `refactor: migrate lookups from IORef to LowerCtx` | `refactor/lower-ctx-migrate-lookups` | 8h |
| 3 | `refactor: complete inferExprType arms` | `refactor/infer-expr-completeness` | 10h |
| 4 | `refactor: replace string Go-type with GoType ADT in coerceArg` | `refactor/gotype-adt` | 12h |
| 5 | `fix: structural eraseTVars + parametricAliasBase` | `refactor/gotype-structural-eraser` | 8h |
| 6 | `test: combinatorial shape matrix + invariant gates` | `test/v0.16-shape-matrix` | 28h |

**v0.16 release** = PRs 1-6 merged. **v0.17** = optional Priority 5.

---

## 9. Acceptance criteria for v0.16

### Compiler internals

- [ ] `Compile.hs` has ≤ 5 NOINLINE global IORefs (down from 18)
- [ ] `Compile.hs` has ≤ 10 `unsafePerformIO` uses (down from 48)
- [ ] `coerceArg` has ≤ 4 branches
- [ ] `inferExprType` returns `Just t` for every `Can.Expr` constructor
- [ ] No string-level Go-type parser in `Compile.hs`
- [ ] `letBindingType` has no `canRouteTyped` body-shape gate

### Test suite

- [ ] `test-files/v0.16-shapes/` ≥ 200 sections
- [ ] `GeneratedGoSpec.hs` ≥ 6 forbidden patterns
- [ ] `CodegenInvariantSpec.hs` ≥ 5 QuickCheck properties
- [ ] `CheckBuildParitySpec.hs` covers v0.16-shapes matrix
- [ ] `AnyWildcardSpec.hs` extended with #5 gate-direction test

### Non-regression (CLAUDE.md non-negotiables)

- [ ] `cabal test` — 306+ specs all green
- [ ] All 27 examples build clean from wiped slate
- [ ] `examples/00-standard-libs` — 120 assertions still pass
- [ ] `scripts/verify-all-web.sh` Playwright sweep green
- [ ] `scripts/verify-cli.sh` green
- [ ] `examples/13-skyshop` build time within ±3% of baseline
- [ ] `examples/13-skyshop` emitted main.go size within ±3% of baseline
- [ ] skydeploy clean build

### Documentation

- [ ] `CLAUDE.md` "Current state" updated
- [ ] `docs/v1-rfc/type-soundness-deep-analysis.md` annotated with shipped sections
- [ ] `docs/fragility-audit-v0.15.3.md` superseded by `docs/fragility-audit-v0.16.md`

When this checklist is green, tag v0.16.0 and ship.
