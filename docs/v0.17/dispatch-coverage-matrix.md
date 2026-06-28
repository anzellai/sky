# v0.17 Fully-Typed Go Codegen Dispatch Coverage Matrix

## Session 3d — secondary bug located (`resolveOrErase` over-erasure)

**Status**: Session 3c shipped commit `7e634194` — A5 dispatch now fires. New build error surfaced: `cannot use rt.Coerce[any](q) ... as firestore.Query`. Root cause located after deeper trace.

### Architecture is correct in 2 of 3 places

1. **FFI generator (FfiGen.hs:1546 `emitFfiTAliases`)** — emits `type FfiT_<wrapper>_P<idx> = <paramType>` correctly. Verified: 1762 FfiT_ aliases in `.skycache/go/firestore_bindings.go`.
2. **DCE pruner (Compile.hs:5859 `pruneOrphanFfiTypes`)** — correctly keeps aliases referenced in main.go. Verified: removes aliases when zero `rt.FfiT_*` references found in main.go.
3. **❌ Compiler coercer (`resolveOrErase` at Compile.hs:15702 + `eraseScopedCtx`)** — converts the FfiT_ alias name → "any" via HM-substitution of source's TVar type. THIS is the bug.

### Mechanism

For `Firestore.queryDocuments q ctx`:
1. A5 fires (Session 3c fix). Calls `coerceFfiArgViaAlias ctx "Go_Firestore_queryDocuments" 0 "pkg.Query" q`.
2. `isCallerVisibleGoType "pkg.Query"` = False → branch 3.
3. `aliasName = "rt.FfiT_Go_Firestore_queryDocuments_P0"` → `coerceVia ctx (Just q) aliasName goArg`.
4. coerceVia line 15818: `rt.Coerce[resolveOrErase ctx (Just q) CoerceTHint aliasName](goArg)`.
5. `resolveOrErase`:
   - `lookupSolvedRegionScoped (region q) ...` → Just solvedT
   - `pickSubstArg CoerceTHint solvedT` returns Just t (for TType/TAlias/TRecord/TTuple)
   - `not (hasTVarT t)` passes
   - Returns `solvedTypeToGo t`
6. `solvedTypeToGo` for q's unknown FFI type → "any"
7. Final emit: `rt.Coerce[any](q)` → Go build fails on the typed call site

### The fix (Session 3d)

Add an "any guard" to `resolveOrErase` (Compile.hs:15702). When `solvedTypeToGo t == "any"` (i.e. the HM substitution loses type information), DON'T substitute — fall back to `eraseScopedCtx ctx fallback` so the alias name survives.

```haskell
resolveOrErase ctx mSrc kindHint fallback =
    case mSrc of
        Just src
            | Just solvedT <- Solve.lookupSolvedRegionScoped (A.toRegion src) (LC._lc_solved ctx)
            , Just substArg <- pickSubstArg kindHint solvedT
            , not (hasTVarT substArg)
            , let substStr = solvedTypeToGo substArg
            , substStr /= "any"   -- NEW guard: don't lose alias info
                -> substStr
        _   -> eraseScopedCtx ctx fallback
```

One-line addition. The fallback path with the FfiT_ alias name has the necessary type info (the alias points to the real Go type); the substitution path with "any" loses it.

### Verification gates (Session 3d close)

Same as Session 3c:
1. Rebuild compiler
2. 13-skyshop builds clean (expect `rt.Coerce[rt.FfiT_Go_Firestore_queryDocuments_P0](q)` emit)
3. 05 + 11 build clean
4. 01 + 26 + 00 still build (no regression)
5. cabal test zero new regressions

### Architectural verdict

NOT in §8 floor. §7 lever: "resolveOrErase: prefer alias when HM-substitution loses type info". Tiny surface change with high leverage.

---

## ✅ Session 3b — ROOT CAUSE LOCATED + precise fix plan

**Status**: Empirical A5 probe identified the exact bug. Fix is bounded (2 files, ~6 line edits).

### A5 probe (forcing pattern, then reverted)

Added an always-False guard arm BEFORE A4 at Compile.hs:14134 that fires `Debug.Trace.trace` for every Go_* Can.VarKernel call, prints all gate values, then `seq marker False` falls through to the real dispatch.

Run on examples/13-skyshop:
```
A5-PROBE: Go_Firestore_queryDocumentsT inSet=True  paramTysLen=Nothing nargs=2 allUnit=False
A5-PROBE: Go_GoV4_appAuthT             inSet=True  paramTysLen=Nothing nargs=2 allUnit=False
A5-PROBE: Go_Stripe_goV84_*T           inSet=True  paramTysLen=Nothing nargs=2 allUnit=False
... (30+ entries; every Go_*T name in set, every paramTysLen=Nothing)
```

**Every typed wrapper name IS in `_lc_ffiTypedWrapperNames` (Set).
NONE has an entry in `_lc_ffiTypedWrapperParams` (Map).**

The A5 gate fails at the params lookup (line 14142), not the wrapper-set membership (line 14141).

### Root cause

`seedTypedFfiNames` (Compile.hs:2990) correctly returns BOTH `twNames :: Set` AND `twParams :: Map` from the same `allEntries` list — both should be populated identically.

But the **`EmitCompileCtx` struct** (`CompileCtx.hs:347`) has only `_cc_ffiTypedWrappers :: !(Set.Set String)` — NO companion `_cc_ffiTypedWrapperParams` field. The Map dies at the EmitCompileCtx boundary.

Then `buildLowerCtxFromEmitCtx` (Compile.hs:13368) sets `_lc_ffiTypedWrapperNames = lookupFfiTypedWrappersFromCtx ecc` but leaves `_lc_ffiTypedWrapperParams = Map.empty` (from `emptyLowerCtx`). So every LowerCtx constructed via this helper has the Set populated and the Map empty.

This is the IORef-defusing migration from v0.17 iter 5 — names were migrated but params were left behind.

### The fix (exact mechanical edits)

**File 1: `src/Sky/Build/CompileCtx.hs`**

1. After line 352 (after `_cc_ffiTypedWrappers` field), add:
```haskell
    , _cc_ffiTypedWrapperParams :: !(Map.Map String [String])
        -- ^ Typed-FFI wrapper name → param Go types.  Mirrors
        -- 'LowerCtx._lc_ffiTypedWrapperParams'.  v0.17 close iter 5
        -- companion to '_cc_ffiTypedWrappers' (was missed during the
        -- names-only IORef→ctx migration; restored Session 3b).
```

2. After `_cc_ffiTypedWrappers  = Set.empty` in `emptyEmitCompileCtx` (line 370), add:
```haskell
    , _cc_ffiTypedWrapperParams = Map.empty
```

3. After `lookupFfiTypedWrappersFromCtx` definition (line 489), add:
```haskell
-- | Read the typed-FFI wrapper param Go types.
lookupFfiTypedWrapperParamsFromCtx :: EmitCompileCtx -> Map.Map String [String]
lookupFfiTypedWrapperParamsFromCtx = _cc_ffiTypedWrapperParams
```

4. Export `lookupFfiTypedWrapperParamsFromCtx` (next to line 68's existing `lookupFfiTypedWrappersFromCtx` export).

**File 2: `src/Sky/Build/Compile.hs`**

1. Find `buildEmitCompileCtx` (the real builder, not the empty one). Populate `_cc_ffiTypedWrapperParams` from `CompileCtx._ctx_typedWrapperParams` (which already exists per line 119).

2. At line 13376 in `buildLowerCtxFromEmitCtx`, after `_lc_ffiTypedWrapperNames = lookupFfiTypedWrappersFromCtx ecc`, add:
```haskell
    , _lc_ffiTypedWrapperParams = lookupFfiTypedWrapperParamsFromCtx ecc
```

3. Import the new accessor at the top of Compile.hs (next to existing `lookupFfiTypedWrappersFromCtx` import at line 49).

### Verification gates for Session 3b close

- `cd examples/13-skyshop && rm -rf sky-out && sky build src/Main.sky` succeeds (currently fails with `undefined: rt.Go_<Pkg>_<method>`)
- Examples 05 + 11 same
- `bundled console regenerate` succeeds
- `cabal test` zero regressions
- Byte-diff: examples that DON'T use Go_* FFI should be byte-identical (no collateral)

### Architectural verdict

**Closes Problem B** from `docs/v0.17/stabilization-postmortem.md` (3 examples fail with `undefined: rt.Go_<Pkg>_<method>`). Likely partially closes Problem A too (bundled console regenerate may share this gap).

Mechanism is **§7 architectural lever** "complete the IORef→ctx migration (defuse leaks left behind by partial defusing)". NOT in §8 floor. NO scaffolding survives the close — net cleaner.

### Session 4 implication

If the fix is byte-clean and gates pass, Sessions 4-5 (env-thread `solvedTypeToGoViaPipelineFlat` + spec retarget) can proceed unmodified — the 4th-path investigation was a SEPARATE bug from Problem A's renderer divergence.

---

## ⚠ Session 3 — CORRECTION to Session 2 (Haskell laziness bug)

**Status**: Session 2's conclusion (`exprToGoTyped` line 20586 is the 4th path) was **WRONG**. The traces Session 2 added used `let _ = if X then trace ... else ()` which never forces `_`, so the trace expressions were never evaluated. Their "ZERO HITS" results were artifacts of Haskell laziness, not proof of unreached arms.

### Re-traced with forcing (`trace ... $ result`)

| Trace tag | Site | Function | Result for Go_Firestore.* |
|---|---|---|---|
| `BARE-VK` | line 14027 (in `exprToGo`) | bare `Can.VarKernel` arm | **15 HITS** ← real 4th path |
| `ALIAS-TOP` | line 13998 | `Can.VarTopLevel` Ffi.kernel alias arm | 0 |
| `TYPED-VK` | line 20575 (in `exprToGoTyped`) | bare `Can.VarKernel` arm | 0 |

`exprToGoTyped` is **NOT** the path for `examples/13-skyshop`. Session 2 was wrong.

### The actual 4th path

`exprToGo`'s bare `Can.VarKernel` arm at line **14027** is the immediate caller of `kernelToGo` for Go_Firestore.* calls (15 hits empirically).

Reached via `exprToGo`'s Can.Call dispatch (line 14056+):
1. Sky source: `Firestore.queryDocuments dbConn coll` → `Can.Call (Can.VarKernel "Go_Firestore" "queryDocuments") [dbConn, coll]`
2. Can.Call arm tries typed dispatch arms A1-A8 (lines 14068-14260)
3. **None fire for Go_Firestore.queryDocuments** (gate failures — paramTys/wrapper-set mismatch)
4. Falls through to `_ ->` at line 14356 → does `let goFunc = exprToGo phaseACtxA ctx func`
5. Re-enters `exprToGo` with bare `func` (which is `Can.VarKernel ...`)
6. **Bare `Can.VarKernel` arm at line 14027 fires** → `kernelToGo` emits bare `rt.Go_Firestore_queryDocuments`
7. **But `runtime-go/rt/firestore_bindings.go` only exports `Go_Firestore_queryDocumentsT`** — the bare-name reference fails Go build with `undefined: rt.Go_<Pkg>_<method>`

### Verified: only typed-T variants exist in runtime

```
$ grep "^func Go_Firestore_queryDocuments" examples/13-skyshop/sky-out/rt/firestore_bindings.go
908:func Go_Firestore_queryDocumentsT(arg0 pkg.Query, arg1 context.Context) ...
```

No bare `Go_Firestore_queryDocuments` exists. Same shape for every Go_* FFI binding — only `T` variants. The compiler's fallback to bare-name emission is the gap.

### Session 3 fix direction (CORRECTED)

Three options:

1. **Option A — Fix the gates at A4/A5** so they fire for Go_* kernels (find which condition fails — paramTys lookup vs wrapper-set lookup). Smallest surface; requires further investigation per gate.
2. **Option B — Add typed-T fallback at `kernelToGo` itself** (line 14882): when modName starts with `Go_` AND only the `T` variant exists in `_lc_ffiTypedWrapperNames`, emit `GoQualified "rt" (modName ++ "_" ++ funcName ++ "T")` instead of bare. This is a one-line change but might emit T-variants in positions that can't accept them (curried HOF refs, partial app).
3. **Option C — Pre-flight: never emit bare for Go_* kernels that have no bare runtime export**. Compiler error at emit-time pointing the user to investigate the typed wrapper.

**Recommendation**: investigate Option A first (gate failure root cause). If the gate fails due to a registry-population race (typed wrappers exist in the runtime but aren't in `_lc_ffiTypedWrapperNames` when the dispatch fires), Option A fixes everything cleanly. If gates fail intentionally (paramTys length mismatch on FFI shapes that ARE valid), Option B becomes the safety net.

### Session 3 next steps

1. Trace inside A5 (line 14134) with forcing pattern + print gate values (typedName + Set membership + paramTys lookup + length comparison)
2. If gate fails on Set lookup: investigate `_lc_ffiTypedWrapperNames` population — is the Go_* kernel registered late?
3. If gate fails on paramTys length: investigate the FFI generator (`tools/sky-ffi-inspect`) — does it emit paramTys with the right arity?
4. Architecture-Consult agent verifies before any fix

### Methodology lesson (logged for future sessions)

**Haskell tracing requires `trace ... $` forcing**. The patterns:
- `Debug.Trace.trace "X" $ result` — fires (result is demanded)
- `trace "X" result` — fires (same as above without `$`)
- `let _ = trace "X" () in result` — DOES NOT FIRE (the `_` binding is never demanded)
- `let m = trace "X" () in seq m result` — fires (seq forces m)

This bug invalidated Session 2's elimination methodology. Future tracing in Compile.hs MUST use the `trace ... $` pattern or explicit `seq`.

---

## ✅ Session 2 — 4th path LOCATED (`exprToGoTyped` line 20586) — INVALIDATED

**Status**: ~~Conclusive~~. The 4th FFI emission path is ~~`exprToGoTyped`'s `Can.VarKernel` arm~~. **REJECTED — see Session 3 correction above.** Session 2's traces used a lazy `let _ = ...` pattern that never fires.

### Empirical method

Four Debug.Trace.trace instrumentations were added to `Compile.hs` (then reverted), tagging the four possible callers of `kernelToGo`:

| Trace tag | Site | Function | Result for Go_Firestore.* |
|---|---|---|---|
| `KTG-ENTRY` | line 14868 | `kernelToGo` entry | **HITS** (15+ Firestore calls fire) |
| `BARE-VARKERNEL-ARM` | line 14027 | `exprToGo` bare `Can.VarKernel` arm | **ZERO HITS** |
| `FALLBACK-VARKERNEL` | line 14356 | `exprToGo` Can.Call `_ ->` fallback | **ZERO HITS** |
| `ALIAS-VARTOPLEVEL-ARM` | line 13998 | `Can.VarTopLevel` Ffi.kernel alias arm | **ZERO HITS** |

### Conclusion by elimination

The callers of `kernelToGo`:
1. Line 13999 — `Can.VarTopLevel` Ffi.kernel alias arm (in `exprToGo`)
2. Line 14031 — bare `Can.VarKernel` arm (in `exprToGo`)
3. Line 14882 — `kernelToGo` itself (self-loop, not relevant)
4. **Line 20586 — `exprToGoTyped` Can.VarKernel arm** ← THE 4TH PATH

Three of the four traced cleanly with zero hits for Firestore. By elimination, `exprToGoTyped` is the only viable source. **The bare-rt.Go_X_y emission for examples 05/11/13 originates from `exprToGoTyped`** — Sky.Live's typed-emission path.

### The architectural problem

`exprToGoTyped` is Sky.Live's typed-IIFE entry. Its `Can.VarKernel` arm at line 20586 is one line:

```haskell
Can.VarKernel modName funcName -> kernelToGo modName funcName
```

It does **NOT**:
- Check `_lc_ffiTypedWrapperNames` (typed-T wrapper registry)
- Check `_lc_ffiTypedWrapperParams` (param-type registry)
- Dispatch via A4 zero-arg (`exprToGo:14116`), A5 N-arg (`exprToGo:14134`), P8 typed-kernel (`exprToGo:14157, 14175`)

It just calls `kernelToGo` → bare-name `rt.Go_Firestore_queryDocuments` — but runtime-go only exports the typed-T variant `rt.Go_Firestore_queryDocumentsT`.

Likewise, `exprToGoTyped`'s Can.Call arm at line 20591:
```haskell
Can.Call func args ->
    let goFunc = exprToGoTyped ctx types retType func
        goArgs = map (exprToGoTyped ctx types retType) args
```
recursively lowers `func` via `exprToGoTyped` (NOT `exprToGo`), so kernel `func` heads bypass the typed-T arms entirely.

### Session 3 fix direction (PROPOSED — Architecture-Consult must verify)

Three options for the architectural close:

1. **Option A — Mirror** the A4/A5/P8 arms inside `exprToGoTyped`'s Can.Call. Smallest surface, divergence risk over time.
2. **Option B — Extract** the typed-FFI dispatch into a shared helper `dispatchTypedFfiCall :: LowerCtx -> String -> String -> [Can.Expr] -> Maybe GoExpr` callable from BOTH entry points. Cleanest, single source of truth.
3. **Option C — Route** `exprToGoTyped`'s VarKernel arm through `exprToGo` (but exprToGo doesn't know retType — type-context loss).

**Recommendation**: Option B. The shared helper takes `LowerCtx` + names + args, returns `Maybe GoExpr` (Just when typed dispatch fired; Nothing falls through to `kernelToGo` bare). Both entry points consume.

### Spec gates needed for Session 3

- `Sky.Build.TypedFfiDispatchInTypedEmitSpec` — fixture: Sky.Live `view` that calls a Go_* FFI kernel; asserts emitted Go uses `rt.Go_<X>_<y>T(...)` not `rt.Go_<X>_<y>(...)`.
- Regression: `examples/13-skyshop` + `examples/05` + `examples/11` clean-build.
- Byte-diff: pre-fix vs post-fix Go for examples that DON'T use Go_* FFI from typed-emit context — should be byte-identical (no collateral).

### Architectural verdict

This is **NOT** in the irreducible floor (§8 of `docs/architecture/sky-compiler-architecture.md`). It's a **dispatch-symmetry gap** between `exprToGo` and `exprToGoTyped` — they evolved independently and only `exprToGo` ever received the typed-FFI dispatch arms (A4/A5/P8). The §7 architectural lever is "Per-instance kernel σ + typed dispatch parity across emission entry points". Closure mechanism: shared dispatch helper.

---

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

