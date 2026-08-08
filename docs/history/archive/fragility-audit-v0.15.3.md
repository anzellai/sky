# Fragility Audit — Sky Compiler v0.15.3

> Source: autonomous audit by Agent A (Explore) on branch
> `refactor/compiler-fragility-audit`, 2026-05-25.
>
> **Update — v0.15.5 (2026-05-25)**: items #2, #11, #15 closed by
> PR #73 (branch `refactor/v0.15.5-lower-ctx-migrate-lookups`).
> Items #1, #6, #9 partially closed by the same PR (IORef
> consolidation into `scopeStateRef`); the v0.15.6 cascade finishes
> them.  Items #8, #14 deferred to v0.15.6 (iter 3 attempt
> exposed a region-pollution bug; needs the cascade's
> single-snapshot-per-compile to land first).  See
> `docs/improvement-plan-v0.16.md` for the next stage.

## Executive Summary

Sky's v0.15.x release ships **type-directed lowering** to address parametric record alias soundness bugs, but the implementation **accumulates ad-hoc recovery points** rather than replacing the type-blind lowering architecture. The compiler now has **18 NOINLINE global IORefs** managing implicit state, **97+ call sites emitting runtime coercions**, and **multiple "lazy-evaluation race" mitigations** that create new fragility vectors. The recent v0.15.2-v0.15.3 panic fixes closed **specific surface instances** but left the **underlying divergence between HM types and emitted Go IR** unaddressed.

---

## Critical (Panic-Class Risk, Not Yet Exercised)

### 1. Race Between Lambda-Types IORef Push/Pop and GoIR Lazy Rendering

**File:** `src/Sky/Build/Compile.hs:322-334, 368-374`

**Functions:** `withScopedLambdaTypes`, `withScopedLambdaGoStrings`

**Issue:**
The `withScopedLambdaTypes` mechanism uses `unsafePerformIO` + manual IORef state management to register parameter types in scope. The lambda-body expression is emitted as a `GoIr.GoExpr` (a lazy data structure), but the IORef is **popped immediately after rendering to string**. If a sibling expression in the same statement-sequence references a lambda param but the expression evaluation defers its type lookup until AFTER the pop, the lookup returns `Nothing`.

**Concrete scenario that would trigger it:**
```haskell
-- Pseudo-code
let f = \x -> someFunc x        -- x registered in globalLambdaTypes
    g = h (f)                    -- if g's codegen is lazy-deferred,
                                 -- by the time it runs exprToGo on (f),
                                 -- x is no longer in the IORef
```

**Why it's not caught today:**
- Most lambda uses occur in inline positions where forcing is immediate.
- Sibling-reference cases (like the recent v0.15.3 editor panic) happen at field-access sites where `lookupLambdaGoStr` provides a fallback via a second IORef (`globalLambdaGoStrings`).
- No systematic lazy-deferral testing across the entire lowering pipeline.

**Soundness impact:** Runtime panic with `interface conversion: <actual> vs <expected>` when the deferred codegen uses `any` instead of the cached type.

---

### 2. `inferExprType` Returns `Nothing` for Unhandled Expression Forms

**Status: CLOSED (v0.15.5 — PR #73 iter 2, commit 89cfcf6)** — Added arms for `Can.Lambda`, `Can.Update`, `Can.Accessor`, `Can.LetRec`, plus pipe/composition binops (`|>`, `<|`, `>>`, `<<`).

**File:** `src/Sky/Build/Compile.hs:11028-11177`

**Functions:** `inferExprType`

**Issue:**
The `inferExprType` helper reconstructs HM type info at codegen time by walking `Can.Expr`. It handles literals, variable lookups, function calls (with special cases for polymorphic list operations), records, tuples, if/case, and field access. **It does NOT handle:**

- `Can.Lambda` → returns `Nothing` (no way to reconstruct the lambda's signature from the body alone)
- `Can.Update` → returns `Nothing` (would need to walk the original record type + unify with field changes)
- `Can.BinOp`, `Can.Negate` → returns `Nothing` (would need operator-level type rules)
- `Can.Let`, `Can.LetRec` (partially — case arms are handled, but let-bound names inside a call's arg are not)
- `Can.Accessor` (standalone field-access function) → returns `Nothing`

**Soundness impact:** Silent loss of type precision; downstream panics if the actual runtime value's type diverges from the nominal `any` assumption.

---

### 3. `containsGenericTypeParam` Gate Applied Backward

**File:** `src/Sky/Build/Compile.hs:8734-8750, 8390-8410`

**Issue:**
The `coerceArg` short-circuit fires ONLY when **both source and target are parametric aliases with the SAME base name**. But what if:
- Target is `Cfg_R[T1]` (generic context)
- Source is `Cfg_R[Msg]` (concrete context)
- They have the same base `Cfg_R`

The short-circuit will NOT fire because `containsGenericTypeParam "Cfg_R[T1]"` is true, so we skip the entire parametric-alias arm and fall through to the `any(arg).(Cfg_R[any])` assertion, which **panics at runtime** when the actual value is `Cfg_R[Msg]`.

**Soundness impact:** Go nominal-type assertion panic: `interface conversion: Cfg_R[Msg] vs Cfg_R[any]`.

---

### 4. `eraseTypeParams` Loses Information At Container Boundaries

**File:** `src/Sky/Build/Compile.hs:8878-8891`

**Issue:**
`eraseTypeParams` walks a Go-type string and replaces all `T\d+` with `any`. When the string contains **nested parametric types** that should preserve their structure:

```go
rt.Coerce[[]rt.SkyResult[T1, String]](x)  // should stay as-is for type-pinning
// but eraseTypeParams produces:
rt.Coerce[[]rt.SkyResult[any, String]](x) // T1 erased → loses type info
```

**Soundness impact:** Go build error (`undefined: T1`) or runtime panic from type mismatch.

---

### 5. Wildcard-`any` Soundness Gate Can Be Mis-gated

**File:** `Sky.Canonicalise.Type.freeTypeVars` + `Instantiate.fromAnnotation`

**Issue:**
If new code is added that checks `not (null (freeTypeVars sig))` instead of `not (null (filter (/= "any") (freeTypeVars sig)))`, it will treat `view : Model -> any` as polymorphic and route it through fresh-per-call CForeign instantiation. This diverges body ↔ caller UF vars and silently accepts type mismatches.

**Soundness impact:** Silent type-mismatch acceptance; the solver unifies the body and caller with DIFFERENT UF-var chains.

---

### 6. `lookupLambdaGoStr` Can Serve Stale Entries Across Module Boundaries

**File:** `src/Sky/Build/Compile.hs:362-390, 6935-6957`

**Issue:**
`globalLambdaGoStrings` is a **module-global IORef** that accumulates Go-type-string registrations. Unlike `globalLambdaTypes` which uses scoped push/pop, the Go-strings registry has **weaker isolation**. If the lowerer later re-enters (e.g., during DCE-pass codegen or re-lowering), old registrations are still in the map.

**Soundness impact:** Type mismatches when stale type-strings from one function instantiation are used in another function's codegen.

---

## High (Incorrect-Codegen-With-Workaround)

### 7. `coerceArg` Has 10+ Special-Case Branches

**File:** `src/Sky/Build/Compile.hs:8371-8510`

10 distinct branches, each handling a different panic scenario:
1. `ty == "any"` → skip
2. Generic type param + `any`-typed arg → `rt.Coerce[T]`
3. Parametric record alias with matching base → pass raw
4. `rt.SkyResult` → `rt.ResultCoerce`
5. `rt.SkyMaybe` → `rt.MaybeCoerce`
6. Sky-uncurried constructor at curried HOF slot → curry adapter
7. Already-correct type → skip
8. Parametric `rt.SkyTask` → `rt.TaskCoerceT`
9. (Disabled) HOF-kernel-variant routing
10. Plus more in downstream path

**Each branch is a special case bolted on for a specific panic class.** No evidence that they are complete — they cover OBSERVED panic instances but not all possible shapes.

**What's missing:**
- Nested parametric types: `[]rt.SkyResult[T1, T2]`?
- Partial application of generic constructors stored in records?
- Closure-captured polymorphic function refs?
- Cross-module polymorphic-call re-instantiation in non-sibling contexts?

---

### 8. `inferExprType` Cache Inconsistency With Solver's `solvedTypes`

**Status: DEFERRED to v0.15.6** — Iter 3 of v0.15.5 PR #73 attempted to remove the `canRouteTyped` body-shape whitelist (turning `letBindingType` into a single-axis type-emittability gate).  This exposed a CROSS-BINDING REGION-MAP POLLUTION bug: `lookupRegionType` returned a type from an unrelated sibling-region binding for `Sky_Core_Jwt_urlToStandard`'s `let rem = modBy 4 (length std)`, mis-typing `rem` as `Sky_Test_TestResult` instead of `int`.  2/120 stdlib assertions failed.  The fix requires the v0.15.6 cascade's single-snapshot-per-compile, where region entries are frozen at compile entry and cannot be polluted by sibling bindings.

**File:** `src/Sky/Build/Compile.hs:9510-9538, 11028-11177`

**Issue:**
`letBindingType` tries to infer a let-binding's type using two sources (region lookup vs `inferExprType`). These can diverge when:
- The solver produced a region type (in `globalRegionTypes`)
- But `inferExprType`'s reconstruction is different (incomplete or wrong)
- The code picks one source over the other and they disagree

**Soundness impact:** Silent type-mismatch acceptance.

---

### 9. `lookupLambdaType` Can Return Wrong Type After Sibling-Scope Bindings Register

**File:** `src/Sky/Build/Compile.hs:349-352, 304-312`

**Issue:**
`withLambdaTypes` permanently mutates `globalLambdaTypes` without scoping. If function A registers `x : T1`, then function B has a parameter named `x`, the inner `x` will find A's `T1` in the global map — classic variable-capture bug at the global level.

---

### 10. `splitCurriedFuncStr` Can Mis-parse Nested Generics

**File:** `src/Sky/Build/Compile.hs:8800-8814`

**Issue:**
`splitToplevelCommas` doesn't track BRACKET depth (only paren depth). A HOF parameter whose arg type is a generic container `func([]rt.SkyResult[T1, T2]) func(T1) any` causes the `int, string` comma (inside `SkyResult[...]`) to be treated as a top-level separator.

**Soundness impact:** Malformed parameter-type list → curry adapter generated with wrong arity → runtime panic.

---

## Medium (Architectural-Debt-With-Tests-Pinning-Current-Behavior)

### 11. `globalRegionTypes` Not Populated for All Expression Regions

**Status: CLOSED (v0.15.5 — PR #73, commit 4d71a55)** — The `globalRegionTypes` IORef is retired.  The region map now lives in `scopeStateRef`'s `_lc_regionTypes` field, populated identically by the Solve pass at codegen entry.  The underlying gap (some sub-expression regions still missing) is a Solve-pass concern, not a Compile-pass one; tracked separately.

Sub-expressions inside let-bindings, case arms, and deeply-nested calls often have regions NOT in `globalRegionTypes`. Type-directed lowering doesn't activate for them.

### 12. Monomorphisation Doesn't Account For Type-Alias Equivalence

`mangleType` mangles type aliases by **name only**. Two aliases that point to the same underlying type but have different names mangle to two separate Go types.

### 13. `rt.Coerce` Reflect Fallback Can Silently Succeed With Wrong Values

The reflect fallback uses **type.Kind() matching** rather than **exact structural equality**. `map[string]int` vs `map[string]interface{}` are kind-compatible but semantically different.

### 14. `defToStmts` Zero-Param Let-Binding Routing Assumes `canRouteTyped` Completeness

**Status: DEFERRED to v0.15.6** — Same blocker as #8.  Removing the `canRouteTyped` whitelist requires the v0.15.6 cascade's single-snapshot-per-compile to land first.

If a zero-param let-binding's body is an unhandled shape (e.g., `Can.Call` or `Can.Access`), `letBindingType` returns `Nothing` and the binding emits as untyped.

---

## Low (Cosmetic/Clarity)

### 15. `globalLambdaGoStrings` Never Cleaned Up Between Compilation Phases

**Status: CLOSED (v0.15.5 — PR #73, commit 7fa51bd)** — The `globalLambdaGoStrings` IORef is retired; its map now lives in `scopeStateRef`'s `_lc_lambdaGoStr` field, freshly populated per compile.

### 16. `eraseTypeParams` Removes Useful Information Even When It Shouldn't
### 17. `parametricAliasBase` Uses String Heuristics Instead of Structural Analysis

---

## Hot Spots

1. **`src/Sky/Build/Compile.hs:8371-8510`** — `coerceArg` (10+ special-case branches)
2. **`src/Sky/Build/Compile.hs:9474-9538`** — `letBindingType` (two-axis gate, discovered empirically)
3. **`src/Sky/Build/Compile.hs:6900-6960`** — `Can.Access` field-access with dual-IORef fallback
4. **`src/Sky/Build/Compile.hs:322-390`** — Lambda-type registration + dual-IORef management
5. **`src/Sky/Build/Monomorphise.hs:280-520`** — Per-call-site type instantiation

---

## Patterns That Signal Fragility

**A. "Special Case Added on $DATE for $PANIC"** — Each panic was closed by adding a NEW code path, not by fixing the underlying divergence. The architecture still has type-blind lowering; we're just recovering type info at more call sites.

**B. "Regression Test Shows Issue, But Root Cause Remains"** — v0.15.3's regression test (`test-files/v0.15-stress/src/Widget/Form.sky`) exists because the real code panicked. Fixing the test didn't fix the root cause.

**C. "Soundness Fix Widens Recovery Surface But Leaves Underlying Gap Untouched"** — explicitly documented as a design problem in `docs/v1-rfc/type-soundness-deep-analysis.md`.

**D. "Global IORef State With Manual Push/Pop Instead of Monadic Threading"** — 18 global IORefs in Compile.hs alone. `unsafePerformIO` with global state is inherently fragile.

---

## Likelihood of Undiscovered Panics

### High-Risk Shapes (Not Systematically Tested)

- Polymorphic functions returning polymorphic-return-type-var values in lists
- Generic containers storing typed-record-alias instances
- Wildcard-only signatures (e.g., `view : Model -> any`) re-instantiated per call with different concrete returns
- Func-typed fields in parametric records passed across module boundaries
- Same-callee polymorphic calls where type args share unification variables

---

## Conclusion

Sky v0.15.3 is **sound for the tested subset**. But the architecture — mixing type-blind lowering with ad-hoc type recovery — **creates new fragility vectors with each fix**.

1. **No off-ramp:** Each panic is closed by adding a special case to `coerceArg` or `letBindingType`.
2. **Implicit state races:** IORef-based lambda-type registration has lazy-evaluation races worked around (dual-IORef fallback) rather than fixed.
3. **Incomplete gates:** Both `letBindingType` (body shapes) and `containsGenericTypeParam` (TVar detection) have documented gaps.
4. **Test-pinning:** The regression test suite is good, but it only pins the bugs we've hit.

The principled solution documented in the RFC is **full type-directed lowering** (replace 124 `exprToGo` (blind) callsites with `lower :: LowerCtx -> ExpectedType -> Can.Expr -> GoExpr`). Until that's done, **fragility will accumulate** with each new feature.

---

## Closed in v0.15.x

### #19 (NEW, closed in v0.15.8): three-way σ consensus invariant — coordination caveat

The σ-recovery / TVar-erasure / coerceArg-skip-check three-way
consensus is FRAGILE — any consumer of `goExprGoType`'s positive
type info must be audited against the other two voters.  Pre-P2,
all three voters voted "any" uniformly (consistent if lossy).
The original P2 attempt added a structural fallback that broke
the consensus in ONE specific branch (coerceArg's skip-check),
causing `examples/13-skyshop` to regress with
`type []string ... does not match inferred type []any for []T1`.

The P2-followup landed in v0.15.8 with the gated skip-check
(IR-shape classifier alone), restoring the consensus.  Lock
tests:

- `test/Sky/Build/CoerceArgListMapInterplaySpec.hs` — drives
  the canonical `List.map fn (List.take 6 xs)` shape.
- `test/Sky/Build/SkyshopCompilesSpec.hs` — standing
  examples/13-skyshop clean-build canary.

Recorded in
`docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md`.
