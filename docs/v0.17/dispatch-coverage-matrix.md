# v0.17 Fully-Typed Go Codegen Dispatch Coverage Matrix

## Session 1 — Phase 0 / 2 / 2.5 (empirical verify)

**Status**: Matrix is theoretical; empirical verify CONTRADICTS theory.

---

## ⚠ EMPIRICAL FINDING that overrides the matrix below

Three entry-function traces added to `Compile.hs` during Session 1 Phase 2.5
empirical verify (then reverted):

1. **`exprToGo` Call entry** (line 14050) — trace at `Can.Call` head, BEFORE any
   arm fires. Filter: `Can.VarKernel m _` where `take 3 m == "Go_"`.
2. **`exprToGoTyped`** (line 20555 wrapper) — same filter.
3. **`exprToGoExpectGo`** (line 13469) — same filter.

Then rebuilt sky-compiler + ran `examples/13-skyshop/sky build`. Observed
output:

```
FFI-CALL-ENTRY:        <ZERO HITS>
EXPR2GOTYPED-CALL:     <ZERO HITS>
EXPR2GOEXPECTGO-CALL:  <ZERO HITS>
```

Yet `examples/13-skyshop/sky-out/main.go` emits 25+ bare
`rt.Go_Firestore_*` / `rt.Go_Mux_*` / `rt.Go_Http_*` calls.

**Conclusion**: the bare-name FFI emission for examples 05/11/13 comes
from a **FOURTH code path** that bypasses ALL THREE exprToGo* entry
functions. The Phase 0 Architecture-Consult agent's identification of
12 dispatch arms is INCOMPLETE — at least one is missing from its
inventory.

### Hypothesis for the 4th path (Session 2 must verify)

The dispatch likely lives in **dep-module emission** (`generateDepDef` /
`generateAliasForDep` family in Compile.hs around line 6657 + 7256).
Lib/Db.sky is a dep module of 13-skyshop's Main. Its calls to
`Firestore.queryDocuments` would be lowered via the dep-emit codepath,
which may have its own VarKernel dispatch independent of `exprToGo*`.

Alternative candidates (less likely):
- A string-template emission that hand-builds `rt.X_y(args)` without
  going through GoIr
- `coerceCallArgsAt` family with its own VarKernel handling
- Pattern-bound case-subject lowering that calls a separate
  case-subject expr→Go function

### Why this matters

The matrix below was constructed from STATIC source analysis (Phase 2)
not empirical trace. The matrix is wrong on the question "which arm
fires for row 4 (Firestore.queryDocuments)". Sessions 2's first action
MUST be: locate the actual emission site by empirical trace, not by
arm-walking.

### Session 2 priming

1. Add `Debug.Trace.trace` at EVERY emission site in Compile.hs that
   builds a `"Go_" ++ X` or `"rt." ++ modName ++ "_" ++ funcName`
   string. Grep for: `modName \+\+ "_"`, `"Go_"`, `GoQualified "rt"`,
   `kernelName \+\+`, `_ki_goName`.
2. Build sky-compiler, run examples/13-skyshop, identify which trace
   fires.
3. That site is the 4th path. Fix at the bug site, not at theory.

The matrix below is preserved for reference but is to be treated as
HYPOTHESIS until Session 2 empirical verify completes.

---

---

## 1. DISPATCH MECHANISM INVENTORY

The Sky compiler routes every kernel-function call (`Can.VarKernel`) through one of 12 dispatch arms:

| Arm | Function | File:Lines | Condition | Emits |
|-----|----------|-----------|-----------|-------|
| **A1** | `kernelToGo` | Compile.hs:14866 | `Can.VarKernel` in `exprToGo` (direct kernel reference) | Go kernel ident (bare or typed per registry) |
| **A2** | `kernelTypedCall` | Compile.hs:21893 | List/Maybe/Result HOF with typed element type inference | `rt.List_mapT[int, any](...)` typed variant |
| **A3** | `emitPartialKernelCall` | Compile.hs:18004 | Partial application (arity < declared) | Closure with curried `func(any) any` layers |
| **A4** | FFI zero-arg typed | Compile.hs:14116 | Go_* module + all-Unit args + typed wrapper exists | `rt.Go_Uuid_newStringT()` (bare, no args) |
| **A5** | FFI N-arg typed | Compile.hs:14134 | Go_* module + non-Unit args + typed wrapper params == call args | `rt.Go_Firestore_queryDocumentsT(q, ctx)` with coerced args |
| **A6** | Literal-arg typed | Compile.hs:14157 | All Sky args are primitives + kernel in `typedKernelLiterals` | `rt.String_toUpperT("abc")` |
| **A7** | `exprToGoTyped` VarKernel | Compile.hs:20569 | Typed entry point routing to kernelToGo | Same as A1 |
| **A8** | `exprToGoTyped` Call | Compile.hs:20582 | Typed entry point with typed kernel routing | Via `kernelTypedCall` (same as A2) |
| **A9** | `exprToGoMain` entry | Compile.hs:~19800 | Program entry point for top-level let-bindings | Delegates to exprToGo or exprToGoTyped per context |
| **A10** | `kernelToGo` fallback | Compile.hs:14876 | No kernel registry match; emit bare `rt.ModName_funcName` | Generic bare Go call |
| **A11** | `kernelTypedCall` selector | Compile.hs:21974 | Determine typed HOF variant (List.map, etc.) | Branch dispatcher, not direct emitter |
| **A12** | `emitPartialKernelCall` adapter | Compile.hs:18004 | Wraps partial-app closure; routes underlying call via A1 | Closure `func(__pk0 any) any { return ... }` |

---

## 2. DISPATCH COVERAGE MATRIX

Test rows span the 10 critical call shapes from the Phase 0 architecture consult.

### Matrix Key:
- **Arm**: Which A1-A12 mechanism fires
- **Emit**: Concrete Go code emitted (from grep of `examples/*/sky-out/main.go`)
- **TypeSuffix**: T-suffix (typed) or bare name
- **Status**: ✅ correct / ❌ broken / ⚠️ unsure

### ROW 1: `Uuid.newString ()` — 0-arg, Go FFI, unit, typed wrapper exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` (main entry) | **A4** (line 14116) | `rt.Go_Uuid_newStringT()` | **T** ✅ | ✅ WORKS |
| `exprToGoTyped` (typed entry) | **A7** → `kernelToGo` line 20569 | `rt.Go_Uuid_newStringT()` | **T** ✅ | ✅ WORKS |
| `exprToGoExpectGo` (typed slot) | Via leaf fallback to A4 | `rt.Go_Uuid_newStringT()` | **T** ✅ | ✅ WORKS |
| Dep-module emission | `generateDepDef` (A1 equivalent) | `rt.Go_Uuid_newStringT()` | **T** ✅ | ✅ WORKS |

**Evidence (empirical)**:
- examples/13-skyshop/sky-out/main.go:775, 815, 879, 936, 955: **7 instances** all emit `rt.Go_Uuid_newStringT()` ✅

---

### ROW 2: `String.toUpper s` — 1-arg stdlib, typed kernel exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A6** (line 14157) if literal | `rt.String_toUpperT("abc")` | **T** ✅ | ✅ WORKS |
| `exprToGo` | **A1** → `kernelToGo` + registry | `rt.String_toUpperT` (ident, needs call arg) | **T** ✅ | ✅ WORKS |
| `exprToGoTyped` | **A7** → `kernelToGo` | `rt.String_toUpperT` (ident) | **T** ✅ | ✅ WORKS |
| Dep-module | A1 equivalent | `rt.String_toUpperT` | **T** ✅ | ✅ WORKS |

**Evidence**: Stdlib kernels in Kernel.hs registry with `_ki_typed = True` emit T suffix via kernelToGo line 14871 + genericParams.

---

### ROW 3: `List.map fn xs` — 2-arg, stdlib HOF, lambda arg, typed-T exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A2** (line 14069) if list-elem-type known | `rt.List_mapT[int, any](fn, xs)` | **T** ✅ | ✅ WORKS |
| `exprToGo` | **A1** + recovery σ (line 14196) | `rt.Sky_Core_List_map_(fn, xs)` with coercion | **bare** | ⚠️ FALLBACK |
| `exprToGoTyped` | **A8** (line 20582) | Via `kernelTypedCall`, same as A2 | **T** ✅ | ✅ WORKS |
| Dep-module | A2 equivalent | `rt.List_mapT[elemT, outT](...)` | **T** ✅ | ✅ WORKS |

**Evidence**: examples/13-skyshop/sky-out/main.go line 775 filters list → `Sky_Core_List_filter(func(...) bool { ... }, rt.AsListAny(existingItems))`

---

### ROW 4: `Firestore.queryDocuments q ctx` — 2-arg, Go FFI, both non-Unit, typed wrapper exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A5** gate FAILS (see below) | `rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoTyped` | **A7** → A1 → A5 gate FAILS | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoExpectGo` (case subject) | **A7** path via case arm | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |
| Dep-module (Lib/Db.sky) | A1 → A5 gate FAILS | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |

**Evidence (empirical)**:
- examples/13-skyshop/sky-out/main.go:541: **BARE** `rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())`
- examples/13-skyshop/sky-out/main.go:775: Same bare call within nested closures
- NO instance found of `rt.Go_Firestore_queryDocumentsT(...)` in entire codebase

**Arm A5 gate analysis (line 14134-14143)**:
```haskell
Can.VarKernel modName funcName
    | take 3 modName == "Go_"
    , not (null args)
    , not (all isUnitArg args)             -- ← PASSES (args are non-Unit)
    , let typedName = modName ++ "_" ++ funcName ++ "T"
    , Set.member typedName (..._lc_ffiTypedWrapperNames ctx)  -- ← PASSES (registered)
    , Just paramTys <- Map.lookup typedName (..._lc_ffiTypedWrapperParams ctx)
    , length paramTys == length args       -- ← FAILS: paramTys length != 2
```

**Root cause**: `_lc_ffiTypedWrapperParams` likely missing the entry OR entry has wrong arity.

---

### ROW 5: `Http.listenAndServe addr handler` — 2-arg, Go FFI, second arg is func, typed wrapper exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A5** gate FAILS | `rt.Go_Http_listenAndServe(":8000", router)` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoTyped` | **A7** → A5 gate FAILS | `rt.Go_Http_listenAndServe(...)` | **BARE** ❌ | ❌ BROKEN |
| Dep-module | A1 → A5 gate FAILS | `rt.Go_Http_listenAndServe(...)` | **BARE** ❌ | ❌ BROKEN |
| Let RHS (row 8) | Via exprToGo | `rt.Go_Http_listenAndServe(...)` | **BARE** ❌ | ❌ BROKEN |

**Evidence (empirical)**:
- examples/05-mux-server/sky-out/main.go:155: **BARE** `rt.Go_Http_listenAndServe(":8000", router)`
- NO instance of `rt.Go_Http_listenAndServeT(...)` found

**Same A5 gate failure as row 4**: param-count mismatch in `_lc_ffiTypedWrapperParams`.

---

### ROW 6: `Firestore.documentSnapshotData snap` — 1-arg, Go FFI, typed wrapper exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A4** (line 14116) if zero-arg OR **A5** if 1+ args | `rt.Go_Firestore_documentSnapshotData(snap)` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoTyped` | **A7** → same as above | `rt.Go_Firestore_documentSnapshotData(snap)` | **BARE** ❌ | ❌ BROKEN |
| Dep-module | A1 → A5 gate FAILS | `rt.Go_Firestore_documentSnapshotData(...)` | **BARE** ❌ | ❌ BROKEN |

**Root cause**: Similar to row 4/5 — A5 param-count gate fails.

---

### ROW 7: `case Firestore.queryDocuments q ctx of …` — 2-arg, subject position, same as row 4

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| Case subject (exprToGoExpectGo) | **A7** path enters exprToGoExpectGo line 13487-13488 | `rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())` | **BARE** ❌ | ❌ BROKEN |
| Case subject (exprToGo fallback) | **A5** gate FAILS | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |
| Case body binding | Via `patternBindings` + structured match | N/A (subject result only) | N/A | ⚠️ INDIRECT |

**Evidence (empirical)**:
- examples/13-skyshop/src/Lib/Db.sky line 104: `case Firestore.queryDocuments q ctx of`
- examples/13-skyshop/sky-out/main.go:541: Emitted as **BARE** `rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())`

**Arm flow**: Case subject at line 13487 routes through `caseToGo ctx (Just goRendering) subject branches` → subject lowers via `exprToGo` → A5 gate fails → bare emission.

---

### ROW 8: `let result = Http.listenAndServe addr h` — Same as row 5, let RHS context

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| Let RHS (exprToGoExpectGo) | **A7** if type-directed OR **A5** fallback | `rt.Go_Http_listenAndServe(...)` | **BARE** ❌ | ❌ BROKEN |
| Let RHS (exprToGo) | **A5** gate FAILS | `rt.Go_Http_listenAndServe(...)` | **BARE** ❌ | ❌ BROKEN |

**Arm flow**: Let binding lowers via `letToGo phaseACtxB ctx (Just goRendering) def body` line 13486 → RHS lowers via `exprToGoExpectGo` → falls back to `exprToGo` when type not emittable → A5 gate fails.

---

### ROW 9: `Cmd.perform (Firestore.queryDocuments q ctx) GotIter` — Row 4 wrapped in Cmd.perform

| Entry Point | Arm | Emit (inner Firestore call) | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` → coerceCallArgsAt | At **typed-param-slot** context | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoExpectGo` (coerced slot) | Line 16281: slot-shape arm, routes inner call via **A5** | `rt.Go_Firestore_queryDocuments(...)` | **BARE** ❌ | ❌ BROKEN |
| Cmd.perform kernel routing | Via `coerceCallArgsAt` σ-recovery (line 14196) | Inner call lowered via A5 gate FAILS | **BARE** ❌ | ❌ BROKEN |

**Evidence (empirical)**:
- `Cmd.perform` routes callees through σ-recovery + coercion path, but inner FFI call still fails A5 gate.
- No examples in codebase show `rt.Go_Firestore_queryDocumentsT(...)` at Cmd.perform call sites.

**Arm flow**: `Cmd.perform` arg at typed-param slot → `coerceCallArgsAt` line 16278-16281 → routes via `exprToGoExpectGo` when emittable Go type exists → but inner kernel still fails A5.

---

### ROW 10: `Mux.routerHandleFunc r path h` — 3-arg, Go FFI, third arg is func, typed wrapper exists

| Entry Point | Arm | Emit | TypeSuffix | Status |
|---|---|---|---|---|
| `exprToGo` | **A5** gate FAILS | `rt.Go_Mux_routerHandleFunc(r, path, h)` | **BARE** ❌ | ❌ BROKEN |
| `exprToGoTyped` | **A7** → A5 gate FAILS | `rt.Go_Mux_routerHandleFunc(...)` | **BARE** ❌ | ❌ BROKEN |
| Dep-module | A1 → A5 gate FAILS | `rt.Go_Mux_routerHandleFunc(...)` | **BARE** ❌ | ❌ BROKEN |

**Evidence (empirical)**:
- examples/05-mux-server/sky-out/main.go: NO instances found (Mux calls likely wrapped in stdlib helpers)
- Pattern inference from rows 4/5 suggests same A5 gate failure.

---

## 3. DIVERGENCE ANALYSIS

### Pattern Summary

**Rows 1-3 (Stdlib Kernels)**: All fire typed arms (A2, A4, A6, A7) → **T-suffix emitted** ✅
- These kernels are registered in `Kernel.hs` with `_ki_typed = True`
- `kernelToGo` line 14871 adds suffix via `Kernel._ki_goName ki ++ genericParams`

**Rows 4-10 (Go FFI Kernels)**: All fire A5 gate BUT gate fails → **BARE emission** ❌
- Arm A5 line 14134 condition: `Set.member typedName (LC._lc_ffiTypedWrapperNames ctx)` PASSES
- **But** line 14142: `Just paramTys <- Map.lookup typedName (LC._lc_ffiTypedWrapperParams ctx)` FAILS OR arity mismatch
- **Result**: Falls through to `kernelToGo` default case (A1) → bare name emission

### The A5 Parameter Mismatch

Line 14143 gate: `length paramTys == length args`

**Hypothesis**: The `_lc_ffiTypedWrapperParams` map is populated INCORRECTLY:
1. **Missing entries**: FFI kernel wrappers not registered at all
2. **Wrong arity**: Entry registered but with wrong parameter count
3. **Stale cache**: Wrapper registry built at compile-time but user FFI binding declares different arity

---

## 4. THE THIRD EMISSION PATH — ROOT CAUSE ANALYSIS

**Critical finding**: Rows 4-10 never emit via A5 because the gate fails. They fall through to:

```haskell
kernelToGo :: String -> String -> GoIr.GoExpr
kernelToGo modName funcName =
    case Kernel.lookup modName funcName of
        Just ki -> ... (Kernel registry — for Sky built-in kernels only)
        Nothing ->
            case (modName, funcName) of
                ...
                _ -> GoIr.GoQualified "rt" (modName ++ "_" ++ funcName)  -- ← FALLBACK
```

**Line 14882 is the actual third path**: When modName starts with `Go_` but is NOT in the built-in `Kernel.lookup` registry (which only contains Sky_Core, Std, etc.), the compiler falls through to **bare qualification** `rt.Go_Firestore_queryDocuments`.

**Why this is broken**:
- A5 was designed to route FFI calls to their typed T wrappers
- A5 gate's param-count check (`length paramTys == length args`) is the **gatekeeper**
- When gate fails, **there is no backup path** — it falls through to A1 which emits bare
- The bare emission bypasses typed coercion entirely

---

## 5. VERIFICATION: _lc_ffiTypedWrapperParams POPULATION

Search for where `_lc_ffiTypedWrapperParams` is written:

```bash
grep -n "_lc_ffiTypedWrapperParams\|ffiTypedWrapperParams" src/Sky/Build/LowerCtx.hs
```

**Finding** (from Phase 1 architecture consult):
- `LowerCtx` field `_lc_ffiTypedWrapperParams :: Map.Map String [String]`
- Populated during `continueCompile` → `solvePhase` via reading from FfiGen's wrapper registry
- **Issue**: The registry may be keyed by bare `"queryDocuments"` but A5 looks up `"Go_Firestore_queryDocuments" ++ "T"` = `"Go_Firestore_queryDocumentsT"`
- **Mismatch**: Qualified name vs bare name in registry!

---

## 6. EVIDENCE: NO T-WRAPPER IN LC CONTEXT FOR GO_ KERNELS

**Grep result** from examples/13-skyshop/sky-out/main.go:
- Line 541: Call is `rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())` **BARE**
- If typed routing worked, expected: `rt.Go_Firestore_queryDocumentsT(any(q).(???), any(ctx).(???))`
- **Actual**: Bare call with no type assertion or coercion

**Conclusion**: `_lc_ffiTypedWrapperParams` does NOT contain an entry for `"Go_Firestore_queryDocumentsT"` at the time A5 gate runs.

---

## 7. SINGLE-SENTENCE CONCLUSION

**The bug lives in the FfiGen → LowerCtx bridge: typed FFI wrapper registries are keyed by bare function names (e.g., "queryDocuments") but arm A5 (line 14142) looks them up using qualified Go names (e.g., "Go_Firestore_queryDocumentsT"), causing the gate to fail silently and all N-arg Go FFI calls to fall through to bare emission at line 14882.**

---

## APPENDIX A: ARM-BY-ARM GATE FLOW FOR ROW 4

```
Sky source: case Firestore.queryDocuments q ctx of ...

↓ exprToGo @ line 14050
  Can.Call rawFunc [q, ctx]
  ↓
  func = rewriteAliasHead rawFunc = Can.VarKernel "Go_Firestore" "queryDocuments"
  ↓
  Can.VarKernel arm check (line 14050-14300)
  
  ↓ Try A2 @ line 14069: kernelTypedCall → False (not List.map)
  
  ↓ Try A3 @ line 14100: partial app → False (arity == args)
  
  ↓ Try A4 @ line 14116: all isUnitArg args → False (args = [q, ctx], not all Unit)
  
  ↓ Try A5 @ line 14134: Go_Firestore + non-Unit + typed wrapper exists
     PASS: take 3 "Go_Firestore" == "Go_"
     PASS: not (null [q, ctx])
     PASS: not (all isUnitArg [q, ctx])
     CHECK: Set.member "Go_Firestore_queryDocumentsT" (_lc_ffiTypedWrapperNames ctx)
            → Assume TRUE (typed wrapper is registered)
     CHECK: Map.lookup "Go_Firestore_queryDocumentsT" (_lc_ffiTypedWrapperParams ctx)
            → Returns Nothing OR Just [wrongArity]
            → If arity mismatch: length [???] != 2
            → FAILS ✗
  
  ↓ Skip A5, try A6 @ line 14157: typedKernelLiterals → False (q/ctx not literals)
  
  ↓ Try A7 (coerce via σ recovery) @ line 14196: generic type params exist?
     Maybe fires if kernelTy contains generics, but without typed A5 match,
     the Go args flow via exprToGo (any-typed) + recovery can't reconstruct
     the typed signature → emits any-typed coercion wraps, not typed call
  
  ↓ Fall through to kernelToGo @ line 14866
     Kernel.lookup "Go_Firestore" "queryDocuments" → Nothing
     ↓
     Case default → GoIr.GoQualified "rt" ("Go_Firestore" ++ "_" ++ "queryDocuments")
                  = "rt.Go_Firestore_queryDocuments"
  
  ↓ BARE EMISSION ❌
```

---

## APPENDIX B: EMPIRICAL GROUND TRUTH — FULL GREP EVIDENCE

### Uuid.newString (working) — examples/13-skyshop/sky-out/main.go
```
Line 775:   itemId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
Line 815:   orderId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
Line 879:   notificationId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
Line 936:   productId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
Line 955:   imageId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
Line 6812:  notificationId := Sky_Core_Result_withDefault("", rt.ResultCoerce[Sky_Core_Error_Error, string](rt.Go_Uuid_newStringT()))
```
**Result**: 6/6 instances use **T-suffix** ✅

### Firestore.queryDocuments (broken) — examples/13-skyshop/sky-out/main.go
```
Line 541:   __subject := rt.ResultCoerce[any, any](rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx()))
Line 775:   (complex nested context, same bare call)
```
**Result**: 2/2 instances use **BARE name** ❌

### Http.listenAndServe (broken) — examples/05-mux-server/sky-out/main.go
```
Line 155:   return func() rt.SkyResult[...] { _ = rt.AnyTaskRun(rt.Go_Http_listenAndServe(":8000", router)) }
```
**Result**: 1/1 instance uses **BARE name** ❌

---

## APPENDIX C: RECOMMENDED NEXT STEPS (FOR CLOSER PHASES)

1. **Locate FfiGen wrapper-registration code**: Find where `ffiTypedWrapperParams` or equivalent is populated from ffi/*.go files
2. **Key check**: Verify the registry key format — is it bare `"queryDocuments"` or qualified `"Go_Firestore_queryDocuments"`?
3. **Fix site**: Align the registry key format with what arm A5 line 14142 expects (likely `modName ++ "_" ++ funcName ++ "T"`)
4. **Validation**: Rebuild examples and verify all rows 4-10 emit T-suffix calls
5. **Test**: Add regression spec for N-arg FFI kernel dispatch to prevent backslide

