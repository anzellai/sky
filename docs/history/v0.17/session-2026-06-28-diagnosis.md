# v0.17 — Class A + B unified root-cause diagnosis (session 2026-06-28)

## TL;DR

Both Class A (`Sky_Test_ok` mono-slot) and Class B (`Maybe.map` Go-generic) failures share **one root cause**: `resolveWrapParams` (`Compile.hs:786–855`) **unconditionally overrides** the wrap's fallback (slot-derived) params with the SOURCE expression's HM-solved type when `mSrc` is `Just`. The override is correct for ONE original case (enclosing-T-var leak class — its original motivation) and is harmful for two others. A single 1-line guard fixes all three.

## Code trace — how the wrap is emitted

**Entry**: `coerceArg :: EmitCompileCtx → LowerCtx → Maybe Can.Expr → GoExpr → String → GoExpr` (`Compile.hs:~16800`).

**Caller**: `coerceCallArgsAt` (`Compile.hs:15913`):
```haskell
paramTypes = Rec._cg_funcParamTypes env ! qualName    -- callee's Go param types (CALLEE-SOURCED, authoritative)
σ          = from CallInstance.concreteTys + quants    -- Sky-side HM-resolved concrete types
substituted = map (substTVarsInGoType σ) paramTypes    -- substitute T-vars in slot
... coerceArg ... (Just expr) (exprToGo expr) subbed   -- subbed = slot-derived target type
```

**Wrap arm (lines 16899–16908)**:
```haskell
| Just params <- stripParametric "rt.SkyResult" ty =
    GoIr.GoCall (GoIr.GoIdent
        ("rt.ResultCoerce[" ++ resolveWrapParams ctx mSrc "Result" params ++ "]")) [e]
| Just inner  <- stripParametric "rt.SkyMaybe"  ty =
    GoIr.GoCall (GoIr.GoIdent
        ("rt.MaybeCoerce["  ++ resolveWrapParams ctx mSrc "Maybe"  inner  ++ "]")) [e]
```

`params`/`inner` here is the slot-derived inner (post σ-substitution, post `stripParametric`).

**`resolveWrapParams` (lines 786–835)**:
```haskell
resolveWrapParams ctx mSrc kind fallback =
    case mSrc of
        Just src ->
            let region = A.toRegion src
                solved = LC._lc_solved ctx
                enclosingParams = LC._lc_enclosingTypeParams ctx
                substMap = ...  -- enclosing-T-var → Go-T-var map
                hasTVar t      = ...   -- HM T-var not in substMap and not in enclosingParams
                renderTy ty    = ...   -- HM-type → Go-type via mapping context
            in case Solve.lookupSolvedRegionScoped region solved of
                Just (T.TType _ k args)
                    | k == kind
                    , all (not . hasTVar) args ->                  -- ← HARMFUL ARM
                        List.intercalate ", " (map renderTy args)  -- ← OVERRIDES SLOT WITH HM TYPE
                _ -> wrapDebug region $ eraseScopedCtx ctx fallback
        Nothing -> eraseScopedCtx ctx fallback
```

## The four flow cases

For each case, the wrap target depends on `fallback` (slot-derived) AND `mSrc`'s HM type.

| Case | `fallback` (slot) | HM type | Current wrap | Slot expects | Verdict |
|---|---|---|---|---|---|
| **1. Enclosing-T-var leak (original motivation)** | `T1, any` (enclosing T-var from caller scope) | `Error, Decimal` (concrete) | `[Error, Decimal]` | `[T1, any]` BUT T1 ↦ Decimal at runtime, so HM-override is correct widening | ✅ correct — override needed |
| **2. Sky_Test_ok mono-slot (Class A)** | `Sky_Core_Error_Error, rt.SkyValue` (concrete, no T-vars) | `Error, Decimal` (concrete) | `[Error, Decimal]` | `[Error, rt.SkyValue]` | ❌ wrong — override harmful |
| **3. Maybe.map Go-generic (Class B)** | `T1` (CALLEE's Go-generic T-var, NOT enclosing) | `Int` (concrete) | `[int]` | `[T1]` resolved by Go inference to match callback's `func(any) any` → `T1=any` | ❌ wrong — override forces conflict |
| **4. CForeign σ-recovery success** | `string` (σ pinned T1 → string in `subbed`) | `String` | falls through (concrete fallback, HM same) | `string` | ✅ correct |

**The discriminator is right there in case 1 vs cases 2/3**: case 1 has T-vars in `fallback` that name **enclosing-scope** type-params; cases 2/3 either have NO T-vars (case 2) or have T-vars naming the **callee's** Go-generic type-params (case 3).

The current guard `all (not . hasTVar) args` checks whether the HM-solved type has unresolved HM T-vars. That's the WRONG axis. The right axis is: does `fallback` have T-vars that need HM-substitution?

## The fix (one-guard)

Add an enclosing-scope check on `fallback`. Only override slot params when `fallback` mentions an **enclosing-scope** T-var:

```haskell
in case Solve.lookupSolvedRegionScoped region solved of
    Just (T.TType _ k args)
        | k == kind
        , all (not . hasTVar) args
        , fallbackMentionsEnclosing ctx fallback ->         -- ← NEW GUARD
            List.intercalate ", " (map renderTy args)
    _ -> wrapDebug region $ eraseScopedCtx ctx fallback
```

Where `fallbackMentionsEnclosing` checks whether ANY T-var token in `fallback` (`tvarsInGoTypeStr`) is in `LC._lc_enclosingTypeParams ctx`:

```haskell
fallbackMentionsEnclosing :: LC.LowerCtx -> String -> Bool
fallbackMentionsEnclosing ctx fb =
    any (\tv -> Set.member tv (LC._lc_enclosingTypeParams ctx))
        (tvarsInGoTypeStr fb)
```

**Behavior on each case**:

- **Case 1 (enclosing leak)** — fallback `"T1, any"`, T1 ∈ enclosingParams → guard `True` → HM override fires → `[Error, Decimal]`. ✅ unchanged.
- **Case 2 (Sky_Test_ok)** — fallback `"Sky_Core_Error_Error, rt.SkyValue"`, no T-vars → guard `False` → fall through to `eraseScopedCtx ctx fallback` → `"Sky_Core_Error_Error, rt.SkyValue"`. Wrap emits `rt.ResultCoerce[Error, rt.SkyValue]` matching slot. ✅ fixed.
- **Case 3 (Maybe.map)** — fallback `"T1"`, T1 ∉ enclosingParams (it names callee's type param) → guard `False` → fall through to `eraseScopedCtx ctx "T1"` → `eraseTypeParamsExceptScope` erases T1 to `"any"` (since not in enclosing scope). Wrap emits `rt.MaybeCoerce[any](rt.Just[any](2))` — callback is `func(any) any`. Go infers T1=any from both. ✅ fixed.
- **Case 4 (CForeign σ-recovery)** — fallback `"string"`, no T-vars → guard `False` → fall through → returns `"string"`. ✅ unchanged.

## Why this is the architecturally honest fix

The current code's symptom — HM-override widening — masks a **wrong-axis check**. The check `all (not . hasTVar) args` asked "is the HM type concrete enough to use?". The right check is "does the SLOT actually need HM-substitution?". Slot needs HM only when the slot mentions enclosing T-vars; otherwise the slot IS the authoritative target type (it's the callee's actual Go signature, post σ-recovery substitution).

This restores the architectural invariant: **the wrap target ALWAYS matches the callee's Go signature**, which is the only thing Go's type checker ever sees. HM is informative ONLY for filling in enclosing-scope T-vars at the wrap site that would otherwise erase to `any`.

## Sister site

The `coerceToFieldType` (`Compile.hs:14903`) `CoerceSkyTask` arm at line 14945-14959 ALSO uses `resolveWrapParams`. The same fix applies there — same one-guard tweak inside `resolveWrapParams` benefits both sites since `resolveWrapParams` is shared. No call-site changes needed.

The `SkyResult` / `SkyMaybe` arms at `coerceToFieldType` (lines 14939–14944) currently use `eraseScoped inner` directly (no HM override). After the fix, they remain as-is — no behavior change for them — but the comment block at lines 14887–14892 documenting "leak class is empirically not yet observed" can be deleted (the analogous arm in `coerceArg` now matches their conservative behavior on cases 2/3).

## Implementation plan

| Step | Action | File | LOC | Risk |
|---|---|---|---|---|
| 1 | Add helper `fallbackMentionsEnclosing :: LowerCtx → String → Bool` | `Compile.hs` near line 743 | +4 | trivial |
| 2 | Add new guard line to the `Just (T.TType ...)` case in `resolveWrapParams` | `Compile.hs:826` | +1 | low — only narrows scope of an existing override |
| 3 | Build compiler | — | — | trivial |
| 4 | Verify 00-standard-libs clean-builds | `cd examples/00-standard-libs && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky` | — | empirical |
| 5 | Verify 26-ui-showcase `rt.Coerce` floor doesn't regress (currently 171) | `cd examples/26-ui-showcase && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky && grep -c "rt\.Coerce\[" sky-out/main.go` | — | empirical |
| 6 | Run targeted cabal specs that lock the enclosing-T-var path | `timeout 300 cabal test --test-options "--match 'CoerceArgListMapInterplay'"` + `Sky.Build.CrossModuleSet` | — | empirical |
| 7 | If 4/5/6 clean, run example sweep | `bash scripts/example-sweep.sh` | — | medium |
| 8 | If sweep clean, run full cabal test (background) + verify-cli | `bash scripts/cabal-test.sh` + `bash scripts/verify-cli.sh` | — | medium |
| 9 | If all green → commit | — | — | — |

## Reversion plan if a step fails

The change is local to `resolveWrapParams` and adds ONE narrowing guard. If step 4/5/6 fails, revert by deleting the new guard line. The function returns to current behavior byte-identically.

If a deeper regression surfaces only at step 7/8 (full sweep): keep the new guard but add an **opposite escape hatch** — when `fallback` has NO T-vars AND HM type is concrete AND they STRUCTURALLY differ, log via `wrapDebug` and use the HM type (preserves rare wide-narrowing behavior the test suite might depend on). This trades the simple one-guard for a slightly more nuanced rule, but the diagnostic data tells us which it should be.

## What I am NOT proposing

- No change to `paramTypes` source (still `_cg_funcParamTypes`).
- No change to σ-recovery in `coerceCallArgsAt`.
- No new IORef, no new `LowerCtx` field — `_lc_enclosingTypeParams` already carries the needed info.
- No reshape of `coerceToFieldType`'s Result/Maybe arms (they don't currently HM-override; they stay).
- No mono → Go-generic emission flip for stdlib functions (Class A's surface symptom).
- No "widen wrap to any" tactic (Class B's surface symptom).

All of those have known cascade risks. The proposed fix narrows ONE existing override; it cannot regress anything that isn't already failing under the current HM-override.

## Confidence assessment

- **Diagnosis confidence:** ~95%. The trace is precise: paramTypes → σ-substitute → coerceArg → resolveWrapParams → emit. The four cases are enumerated; cases 2/3 reproduce the exact failure shapes in `examples/00-standard-libs/sky-out/main.go`. The discriminator (enclosing-vs-callee T-var) is well-defined and supported by `_lc_enclosingTypeParams` already.
- **Fix-survives-sweep confidence:** ~75%. The guard logic is correct on paper. The empirical unknown is: are there other call shapes that have been silently relying on the HM-override to widen ints / strings / etc. into wraps when the slot was concrete? I cannot enumerate without running the sweep, which is the step 7 check.
- **Recommended commit threshold:** steps 4 + 5 + 6 ALL green = safe to commit + run step 7/8 background. If step 7 surfaces a regression, the reversion plan applies.

## Next session entry point

If user authorizes the implementation: start at step 1 above. The branch is `feat/v0.17-pure-sound-codegen` at `7d09b428`. Working tree currently has one fixture workaround uncommitted (`MaybeCombineSpec.hs`) + 3 docs from this session/prior — these don't conflict with the fix.

Expected total session cost: ~30-45 minutes for steps 1–6; +30 min for step 7 (sweep); +15 min for step 8 (verify-cli). Step 7 is the riskiest gate; if it surfaces a regression, the diagnostic data from `SKY_WRAP_DEBUG=1` is the next-session input.
