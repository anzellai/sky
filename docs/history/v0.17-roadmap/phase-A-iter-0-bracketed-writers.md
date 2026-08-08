# v0.17 Phase A Iter-0 Audit: Bracketed-Writer IORefs

**Audit Date:** 2026-06-24  
**Scope:** 7 `scopeStateRef` write/restore sites in Compile.hs  
**Purpose:** Assess migration path for each site to eliminate load-bearing IORef scope-discipline tricks  

## Overview

The 7 sites use `scopeStateRef` (an `IORef LC.LowerCtx`) with a write-snapshot-restore pattern to enforce scope discipline around lazy GoExpr construction. Each site:

1. Reads the previous state (`prev <- readIORef scopeStateRef`)
2. Installs new bindings (`writeIORef scopeStateRef (LC.with* additions prev)`)
3. Forces evaluation of a lazy Haskell structure (rendering to String or forcing to WHNF)
4. Restores the previous state (`writeIORef scopeStateRef prev`)

This pattern is load-bearing for preventing type-parameter leakage when lazy GoExpr thunks are forced at unexpected points in the compilation pipeline. The audit assesses whether each site can be migrated to pure functional scope handling (ReaderT.local, StateT, or a mini-architecture).

---

## Site 1: `withLambdaTypes` (line 520-523)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:520-523`

```haskell
withLambdaTypes :: Map.Map String T.Type -> a -> a
withLambdaTypes additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaTypes additions prev)
    return x
```

**Scope Discipline:** Pushes typed-lambda parameter bindings into scope, then immediately returns `x` without forcing. The binding installation is live at `x`'s construction time only, not at forced-evaluation time. This is **fundamentally impure** — the pure value `x` is evaluated at call site, not inside the bracket.

**Load-Bearing Issue:** This returns `x` to WHNF immediately (before pop), so the bindings are **already in scope** when `x` is evaluated at the call site. The bracket is live during `x`'s construction (which is actually not forced here, just returned). This is a **scope-installation-at-construction-time** trick, which differs from the other 6 sites.

**Migration Verdict:** **(c) Requires mini-architecture**

**Reasoning:**
- Unlike the other 6 sites, this doesn't force its argument before returning.
- The binding must be **live at the call site**, not at some deferred evaluation point.
- Pure ReaderT.local won't work because the caller holds the pure value and forces it later.
- Can't migrate to StateT because the caller's code (not the wrapper) does the forcing.
- **Architecture:** Create a `LambdaTypesBuilder` monad that threads `LC.LowerCtx` through pure code where lambdas are constructed. Callers of `withLambdaTypes` must lift into this monad.

**Estimated Migration Cost:** 4-5 sessions  
- Requires identifying all call sites of `withLambdaTypes` (likely 10-20 sites).
- May require refactoring caller call graphs to thread the monad upward.
- Risk: high (affects pure codegen sites).

---

## Site 2: `withScopedLambdaTypes` (line 538-549)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:538-549`

```haskell
withScopedLambdaTypes :: Map.Map String T.Type -> GoIr.GoExpr -> GoIr.GoExpr
withScopedLambdaTypes additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaTypes additions prev)
    let rendered = GoBuilder.renderExpr x
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)
```

**Scope Discipline:** Pushes bindings, forces `GoExpr` to String form (via `renderExpr`), pops bindings, wraps result as `GoRaw`. The forced-evaluation happens **inside the bracket**, so the bindings are live exactly when needed.

**Load-Bearing Issue:** GoExpr trees are lazy. Rendering forces evaluation of the entire GoExpr. If the bindings are popped before rendering, downstream renderers (called inside renderExpr) see stale scopeStateRef. If rendering is deferred (lazy GoExpr), the pop leaks into sibling scopes.

**Migration Verdict:** **(a) Maps cleanly to ReaderT.local**

**Reasoning:**
- The forced-evaluation is **within the bracket**.
- Rendering is deterministic: `GoBuilder.renderExpr` is a pure function that forces the thunk completely.
- Can replace with: `local (LC.withLambdaTypes additions) (GoBuilder.renderExpr x)` in a ReaderT env.
- All deferred IORef-reading thunks inside renderExpr see the bracketed scope.

**Estimated Migration Cost:** 1-2 sessions  
- Audit call sites (likely 5-10).
- Refactor to thread ReaderT through GoBuilder.renderExpr and its callers.
- Straightforward; no call-graph restructuring needed.

---

## Site 3: `withScopedLambdaGoStrings` (line 606-612)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:606-612`

```haskell
withScopedLambdaGoStrings :: Map.Map String String -> GoIr.GoExpr -> GoIr.GoExpr
withScopedLambdaGoStrings additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaGoStrs additions prev)
    let rendered = GoBuilder.renderExpr x
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)
```

**Scope Discipline:** Identical to Site 2, but for Go-type-string bindings instead of Sky type bindings. Pushes bindings, renders GoExpr (forcing), pops, wraps.

**Load-Bearing Issue:** Same as Site 2: lazy GoExpr thunks inside renderExpr must see live bindings.

**Migration Verdict:** **(a) Maps cleanly to ReaderT.local**

**Reasoning:** Identical to Site 2. Can migrate in the same refactor session.

**Estimated Migration Cost:** Bundled with Site 2 (included in 1-2 sessions)

---

## Site 4: `withScopedEnclosingTypeParams` (line 644-650)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:644-650`

```haskell
withScopedEnclosingTypeParams :: [String] -> GoIr.GoExpr -> GoIr.GoExpr
withScopedEnclosingTypeParams tps x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withEnclosingTypeParams tps prev)
    let rendered = GoBuilder.renderExpr x
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)
```

**Scope Discipline:** Pushes enclosing-function generic type-param names (`["T1", "T2"]`), renders GoExpr, pops. Used by type-arg erasure logic: if `Cfg_R[T2]` is in scope, keep `T2`; else erase to `any`.

**Load-Bearing Issue:** Same as Sites 2-3: lazy GoExpr thunks (esp. inside coerceArg / coerceToFieldType) must see live T-params during rendering.

**Migration Verdict:** **(a) Maps cleanly to ReaderT.local**

**Reasoning:** Identical pattern to Sites 2-3. Can migrate together.

**Estimated Migration Cost:** Bundled with Sites 2-3 (included in 1-2 sessions)

---

## Site 5: `withScopedEnclosingTypeParamsStmts` (line 683-693)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:683-693`

```haskell
withScopedEnclosingTypeParamsStmts :: [String] -> [GoIr.GoStmt] -> [GoIr.GoStmt]
withScopedEnclosingTypeParamsStmts tps stmts = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withEnclosingTypeParams tps prev)
    let rendered = unlines (concatMap GoBuilder.renderStmt stmts)
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return [GoIr.GoExprStmt (GoIr.GoRaw rendered)]
```

**Scope Discipline:** Mirrors Site 4 but operates on statement lists (`[GoStmt]`). Used by TCO emission path: the continue-block's inner coerce logic must see the function's declared T-vars in scope. Critical for avoiding `rt.MaybeCoerce[any]` mismatches against `rt.SkyMaybe[T1]`.

**Load-Bearing Issue:** Same as Sites 2-4: lazy statement thunks must see live scope during rendering.

**Migration Verdict:** **(a) Maps cleanly to ReaderT.local**

**Reasoning:** Same pattern. Can migrate together with Sites 2-4.

**Estimated Migration Cost:** Bundled with Sites 2-4 (included in 1-2 sessions)

---

## Site 6: `lowerExpr` (line 12706-12721)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:12706-12721`

```haskell
lowerExpr :: LC.LowerCtx -> Can.Expr -> GoIr.GoExpr
lowerExpr ctx e = unsafePerformIO $ do
    ctx `seq` return ()
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef ctx
    let rendered = GoBuilder.renderExpr (exprToGo ctx e)
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)
```

**Scope Discipline:** This is a **context installation** wrapper (not a patch/merge). Takes explicit `ctx` as parameter, installs it into scopeStateRef, renders, pops back to `prev`. The WHNF gate (`ctx `seq` return ()`) is critical: forces the incoming ctx before writing to prevent circular thunk references.

**Load-Bearing Issue:** If the incoming `ctx` is itself a thunk fetched from scopeStateRef (via `ctxFromIORef ()`), writing the thunk creates an IORef cell that loops on itself. The `seq` forces it first. The rendered result must be forced **before the pop** to snap all deferred IORef-reading thunks inside exprToGo.

**Migration Verdict:** **(b) Maps cleanly to StateT in emitPhase**

**Reasoning:**
- This is not a **patch** (merge) operation; it's an **install** operation.
- The explicit `ctx` parameter is already available; no reader from IORef needed.
- Can be rewritten as a local state mutation in StateT: `put ctx >> ... >> put prev`.
- The forcing (rendering) is guaranteed to be inside the bracket.
- All call sites of `lowerExpr` can thread through a StateT (or explicit WriterT for cascade).
- No call-graph restructuring: just add state threading.

**Estimated Migration Cost:** 2-3 sessions  
- Identify all call sites of `lowerExpr` (likely 20-50 high-call-volume sites).
- Introduce StateT context (or threading parameter) at each caller.
- Verify that rendering happens within the bracket (likely already true).

---

## Site 7: `lowerExprExpectGo` (line 12728-12736)

**Location:** `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs:12728-12736`

```haskell
lowerExprExpectGo :: LC.LowerCtx -> String -> Can.Expr -> GoIr.GoExpr
lowerExprExpectGo ctx goRendering e = unsafePerformIO $ do
    ctx `seq` return ()
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef ctx
    let rendered = GoBuilder.renderExpr (exprToGoExpectGo ctx goRendering e)
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)
```

**Scope Discipline:** Identical to Site 6, but for the `exprToGoExpectGo` entry point (which provides a Go-type expectation hint).

**Load-Bearing Issue:** Same as Site 6.

**Migration Verdict:** **(b) Maps cleanly to StateT in emitPhase**

**Reasoning:** Identical to Site 6. Can migrate together.

**Estimated Migration Cost:** Bundled with Site 6 (included in 2-3 sessions)

---

## Summary Table

| Site | Function | Location | Pattern | Verdict | Session Cost | Coupling |
|------|----------|----------|---------|---------|--------------|----------|
| 1 | `withLambdaTypes` | 520-523 | Return before force | (c) Mini-arch | 4-5 | High (caller-side) |
| 2 | `withScopedLambdaTypes` | 538-549 | Force inside bracket | (a) ReaderT.local | 1-2 | Low (local) |
| 3 | `withScopedLambdaGoStrings` | 606-612 | Force inside bracket | (a) ReaderT.local | (Bundle S2) | Low (local) |
| 4 | `withScopedEnclosingTypeParams` | 644-650 | Force inside bracket | (a) ReaderT.local | (Bundle S2-S3) | Low (local) |
| 5 | `withScopedEnclosingTypeParamsStmts` | 683-693 | Force inside bracket | (a) ReaderT.local | (Bundle S2-S4) | Low (local) |
| 6 | `lowerExpr` | 12706-12721 | Install + force | (b) StateT | 2-3 | Medium (cascade) |
| 7 | `lowerExprExpectGo` | 12728-12736 | Install + force | (b) StateT | (Bundle S6) | Medium (cascade) |

**Bundled Totals:**
- **Group A (ReaderT.local):** Sites 2-5 → 1-2 sessions
- **Group B (StateT):** Sites 6-7 → 2-3 sessions
- **Group C (Mini-arch):** Site 1 → 4-5 sessions
- **Total Estimate:** 7-10 sessions (iter-0 Phase A commitment)

---

## Detailed Architecture: Group C (Site 1 Mini-Architecture)

**Mini-Architecture: `LambdaTypesBuilder` Monad**

Site 1 (`withLambdaTypes`) cannot use ReaderT.local or StateT because:
- The bindings must be live **at the caller's forcing site**, not inside the bracket.
- The bracket returns a **pure value** that the caller forces later.
- By the time the caller forces, the bracket has long since popped the scope.

**Solution:**
Create a monad that threads `LC.LowerCtx` through pure code where lambdas are constructed:

```haskell
newtype LambdaTypesBuilder a = LTB (LC.LowerCtx -> (a, LC.LowerCtx))

instance Monad LambdaTypesBuilder where
  return x = LTB (\ctx -> (x, ctx))
  m >>= k = LTB (\ctx -> 
    let (x, ctx') = unLTB m ctx
        (y, ctx'') = unLTB (k x) ctx'
    in (y, ctx''))

withLambdaTypesM :: Map.Map String T.Type -> LambdaTypesBuilder a -> LambdaTypesBuilder a
withLambdaTypesM additions action = LTB (\ctx ->
  let ctx' = LC.withLambdaTypes additions ctx
      (result, ctx'') = unLTB action ctx'
  in (result, ctx''))
```

**Refactoring Scope:**
- Audit call sites of `withLambdaTypes` and identify pure-code regions (e.g., typed-lambda parameter registration).
- Lift those regions into `LambdaTypesBuilder` monad.
- At the end of the monad computation, install the final ctx into scopeStateRef (single write, at entry point).
- If call graph is deep, thread monad upward incrementally (bottom-up).

**Risk Mitigation:**
- Start with 1-2 call sites to validate the monad shape.
- Use property tests to ensure lifted code behaves identically.
- Consider staged rollout: lift high-call-volume sites first, collect long-tail sites in a follow-up batch.

---

## Phase A Iter-0 Recommendations

1. **Execute Group A (ReaderT.local) immediately:** Sites 2-5 are straightforward and unblock Phase A timeline. 1-2 sessions.

2. **Parallelize Group B (StateT) with Group A:** Sites 6-7 require threading state through the lowering pipeline, which can proceed independently. 2-3 sessions, parallel start.

3. **Defer Group C (Mini-arch) to Phase A iter-1:** Site 1's refactoring is high-risk and affects pure codegen sites. Validate Groups A and B first; Group C becomes a follow-on commit after cabal test + verification gates pass.

4. **Criterion #3 Closure:** All 7 sites eliminated means:
   - `scopeStateRef` IORef deleted entirely.
   - No more `unsafePerformIO` in scope-binding paths.
   - Pure functional scope discipline.
   - Locks Phase A completion gate: "No load-bearing IORefs."

---

## Affected Files

- `/Users/anzel/works/playground/sky/src/Sky/Build/Compile.hs` (all 7 sites)
- `/Users/anzel/works/playground/sky/src/Sky/Build/GoBuilder.hs` (renderExpr caller chain for Sites 2-5)
- `/Users/anzel/works/playground/sky/src/Sky/Build/Lower/*.hs` (lowerExpr call sites for Sites 6-7)
- Likely: `src/Sky/Type/Canonicalise/Module.hs`, `src/Sky/Type/Solve.hs` (emit-phase threading)

---

## Post-Audit Next Steps

1. Assign Group A (ReaderT) refactoring to iter-0 final commit.
2. Assess Group B (StateT) threading scope; may require EmitPhase monad refactor (orthogonal to iter-0, possible iter-1 pre-req).
3. Sketch Group C (LambdaTypesBuilder) on whiteboard; defer execution pending iter-0 validation.
4. Tag audit completion + verdicts in commit message (referenceable in Phase A close doc).
