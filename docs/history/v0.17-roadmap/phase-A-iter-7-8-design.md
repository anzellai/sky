# v0.17 Phase A — Iter 7 + Iter 8 Design

**Date:** 2026-06-30
**Status:** DESIGN (no code yet — grounding for the next implementation session)
**Branch:** `feat/v0.17-pure-sound-codegen` @ `3362e908`
**Precedent:** iter 6d (`c5fe0aba`) shipped the transitional `phaseAFallback`
bridge that reads `scopeStateRef` + `AnonRec.readAnonRecords` to synthesise
an `EmitCompileCtx` per call.
**Authority:** `.claude/AUTONOMOUS_GOAL.md` criterion #3 (locked wording
in CLAUDE.md §0.3 rule 1).

> **Read first:**
> - `docs/v0.17-roadmap/phase-A-cgenv-reshape.md` — the master Phase A plan.
> - `docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md` — the
>   AnonRec IORef sanction (the asymmetry between cgEnv-bleed and
>   AnonRec-bleed is load-bearing here).
> - `docs/v0.17/session-8-option-F-result.md` — postmortem of the 5th
>   revert on the cgEnv-reader-bridging lever family.

---

## TL;DR

Iter 7 + Iter 8 drain TWO of the 17 `phaseAFallback ctx` call sites
to a pure construction path that does NOT read `scopeStateRef`.
Each drained site continues to read the sanctioned `AnonRec`
IORef (per the iter-0 contract).

The two sites are selected because at their lexical position the
threaded LowerCtx is structurally a SUPERSET of what
`phaseAFallback` would synthesise — i.e. the IORef hop is
self-referential and removable without changing observable
behaviour.

This is the **OPPOSITE lever** to the 4 prior reverts (iter 17 /
37 / 42 / Class-A swap / Session 8 Option F), which all
*extended* an IORef-reader to additional sites. Iter 7/8
*drains* IORef-readers at sites where the threaded ctx is
already the truth. Per CLAUDE.md §0.3 rule 3 N-strikes, this
qualifies as a fresh lever, not a 5th strike.

Both iters target criterion #3 (IORef DELETE). Neither changes
rt.Coerce counts. Neither resolves the 5 currently-failing
cabal tests directly — those close downstream of further iters
(iters 9-12 per the master plan).

---

## The asymmetry at the heart of this design

`phaseAFallback :: LC.LowerCtx -> EmitCompileCtx` at Compile.hs:13390-13411
reads TWO process-wide IORefs:

```haskell
phaseAFallback lc = unsafePerformIO $ do
    scopeSnap    <- readIORef scopeStateRef            -- channel A
    anonRecsSnap <- AnonRec.readAnonRecords            -- channel B
    let home  = LC._lc_module lc
        cgEnv = case LC.lookupCgEnv scopeSnap of
            Just env -> env
            Nothing  -> emptyCgEnv
    return $! buildEmitCompileCtx home cgEnv … anonRecsSnap …
```

These two channels have FUNDAMENTALLY DIFFERENT contracts:

### Channel A — `scopeStateRef` (cgEnv)

- **Bleed across compiles**: writes from compile N are visible to
  compile N+1 unless `resetCompileState` (Compile.hs:3304) was
  called between them. In the multi-example sweep that uses
  in-process compilation, this is the failure mode Session 8
  Option F regressed on (11/26 sweep examples broken).
- **No machine-verified contract** — CLAUDE.md criterion #3 mandates
  its DELETION.
- **Cross-compile bleed is REAL** — `Sky.Build.ExampleSweep` and the
  test harness re-use one process for many compiles; one example's
  cgEnv writes leak into the next example's emission.

### Channel B — `globalAnonRecords` (anon registry)

- **No bleed across compiles** by contract — `resetAnonRecords` is
  called at compile entry (per `phase-A-iter-0-anonrecords-contract.md`
  invariant "Reset-at-compile-entry").
- **Machine-verified contract** — `Sky.Build.AnonRecordWriterAuditSpec`
  enforces single-writer + monotonic invariant. The IORef is
  sanctioned per Option (c) of CLAUDE.md §0.3 rule 1.
- **Stays — per user-authorised iter-0 contract.**

### Therefore

The architecturally correct drain at any `phaseAFallback ctx`
call site is:

1. Replace the IORef hop on **channel A** (cgEnv) with the
   threaded-LowerCtx's cgEnv field (`LC.lookupCgEnv lc`).
2. Keep the **channel B** read (AnonRec) — it's sanctioned and
   the LowerCtx does not carry it.

The replacement value of cgEnv is `LC.lookupCgEnv lc` instead of
`LC.lookupCgEnv (unsafePerformIO (readIORef scopeStateRef))`. At
sites where `lc` carries the right cgEnv (verified per site),
this is a strict win:

- Fewer IORef hops at compile time.
- No cross-compile bleed risk on channel A.
- Closes 1/17 of the cgEnv-channel readers per drain.

---

## Iter 7 — Site Compile.hs:6888 (dep body emission)

### Site context

```haskell
-- Compile.hs:6883-6889
depBodyCtx = LC.withCurrentDepModule (Just depModuleName)
           $ LC.withEnclosingTypeParams depTypeParams
                  (buildLowerCtxFromEmitCtx (withCurrentModuleInCtx (Just depModuleName) phaseACtx))
lowerDepBody e =
    if depRetType /= "any"
        then exprToGoExpectGo (phaseAFallback depBodyCtx) depBodyCtx depRetType e
        else exprToGo        (phaseAFallback depBodyCtx) depBodyCtx e
```

### What's around the site

Immediately below at Compile.hs:6901-6906:

```haskell
typedBody = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef depBodyCtx           -- (1) write depBodyCtx
    let res = typeIIFE depBodyCtx (Just body) depRetType (lowerDepBody body)
    res `seq` writeIORef scopeStateRef prev       -- (2) restore prev
    return res
```

The IORef IS written with `depBodyCtx` BEFORE `lowerDepBody` is forced.
When `phaseAFallback depBodyCtx` fires inside `lowerDepBody body`'s
thunk, it reads back the SAME `depBodyCtx` it was passed as
argument. The IORef hop is structurally self-referential at this
site.

### Why does the cgEnv channel need depBodyCtx specifically?

`depBodyCtx`'s cgEnv field is constructed by `buildLowerCtxFromEmitCtx
phaseACtx` then carried unchanged through `withCurrentDepModule` +
`withEnclosingTypeParams`. So `LC.lookupCgEnv depBodyCtx ==
LC.lookupCgEnv (buildLowerCtxFromEmitCtx phaseACtx) ==
LC.lookupCgEnv (lcOf phaseACtx)`.

Meanwhile `phaseAFallback depBodyCtx`'s cgEnv read is
`LC.lookupCgEnv (readIORef scopeStateRef)`. After the writeIORef
at step (1), this is `LC.lookupCgEnv depBodyCtx`. Identical.

**Conclusion:** at site 6888, the cgEnv channel of `phaseAFallback`
returns `LC.lookupCgEnv depBodyCtx`. The IORef hop is removable.

### Proposed change

Introduce a pure helper that takes a `LC.LowerCtx` and produces an
`EmitCompileCtx` WITHOUT reading `scopeStateRef`:

```haskell
-- | Iter 7 — pure ctx synthesis for sites where the threaded
-- LowerCtx is the authoritative cgEnv source.  Reads only the
-- sanctioned 'globalAnonRecords' IORef (per iter-0 contract);
-- the cgEnv channel of 'scopeStateRef' is bypassed.
phaseAFallbackFromCtx :: LC.LowerCtx -> EmitCompileCtx
phaseAFallbackFromCtx lc = unsafePerformIO $ do
    anonRecsSnap <- AnonRec.readAnonRecords
    let home  = LC._lc_module lc
        cgEnv = case LC.lookupCgEnv lc of
            Just env -> env
            Nothing  -> emptyCgEnv
    return $! buildEmitCompileCtx
        home
        cgEnv
        (LC._lc_solved lc)
        (LC._lc_kernelAlias lc)
        (LC._lc_unionDetails lc)
        anonRecsSnap
        (LC._lc_aliases lc)
        (LC._lc_fieldIdx lc)
        (LC._lc_unionNames lc)
        (LC._lc_ffiTypedWrapperNames lc)
        (LC._lc_ffiTypedWrapperParams lc)
```

Replace at Compile.hs:6888-6889:

```haskell
lowerDepBody e =
    if depRetType /= "any"
        then exprToGoExpectGo (phaseAFallbackFromCtx depBodyCtx) depBodyCtx depRetType e
        else exprToGo        (phaseAFallbackFromCtx depBodyCtx) depBodyCtx e
```

`phaseAFallback` and its IORef-reading semantics remain at the
other 16 call sites unchanged.

### Subtlety: lazy thunks captured by `exprToGoExpectGo`

`exprToGoExpectGo`'s return value is a lazy `GoExpr` tree. Some
sub-thunks within that tree may close over the ctx and force
later, e.g. at `renderPackage` time. We must verify that at force
time, the values inside the captured ctx are still the right
ones.

The captured `EmitCompileCtx` is constructed pure-functionally
from `depBodyCtx` (a value, not an IORef). The captured value's
cgEnv is the cgEnv of `phaseACtx` (the compile-entry snapshot)
which IS stable — it does not change during dep emission. The
captured anonRecsSnap is whatever was in `globalAnonRecords` at
the time `phaseAFallbackFromCtx` ran. Per the iter-0 contract,
anon registrations are monotonic (no overwrites), so a snapshot
taken at site 6888 is a subset of the snapshot at end-of-emit;
the renderer reads end-of-emit anyway via the existing
`generateAnonRecordDecls` call. **Captured value never goes
stale: cgEnv is constant, anonRecs is monotone.**

### Verification gates (iter 7 close)

Before declaring iter 7 done:

1. **Targeted spec build**: rebuild compiler, run
   `Sky.Build.PhaseABaselineRegression` (rt.Coerce floor must
   not move) + `Sky.Build.AnonRecordWriterAudit` (iter-0
   contract) + `Sky.Build.NoT1LeakInNotesApp` (does iter 7
   accidentally close this? — see below).
2. **3-example clean-build**: `26-ui-showcase` (largest cgEnv
   surface) + `13-skyshop` (largest FFI surface) +
   `00-standard-libs` (rt.Coerce ratchet). All 3 must build +
   the rt.Coerce count on `26-ui-showcase` must be ≤172 +
   `00-standard-libs` must be ≤125 (post-baseline bump).
3. **Full sweep**: `scripts/example-sweep.sh` — 26/26 must pass.
4. **Bundled-console regenerate**: `scripts/regenerate-console.sh`
   — must still fail at the same point as today (we did NOT
   change the bundled-console regen behaviour). If it now passes
   that's an unexpected bonus to investigate; if it fails
   DIFFERENTLY that's a regression to revert.

### Estimated effort

**1 session.** ~30 LOC added (new helper) + 2 LOC replaced (the
call sites). Gates ~15-20 min.

### Does iter 7 close any of the 5 failing CI tests?

| Failure | Closes? |
|---|---|
| `DictSource` (Linux flake) | No — unrelated |
| `NoT1LeakInNotesApp` (T1-leak) | **Maybe** — site 6888 is the dep body emission path that the T1-leak class flows through. Iter 7 stops one source of stale cgEnv at this site. Worth measuring; not relied upon. |
| `CrossModuleLambdaCollisionC` (bug-compat spec) | No — spec asserts old behavior |
| `DepCurrentModuleHint` × 2 (bug-compat specs) | No — specs assert old behavior |

So iter 7 may *partially* close NoT1LeakInNotesApp. The other 4
need iters 9-12 (bug-compat spec retargets) + Linux flake
investigation.

---

## Iter 8 — Site Compile.hs:9353 (entry-module body emission)

### Site context

```haskell
-- Compile.hs:9353-9354
                    then exprToGoExpectGo (phaseAFallback entryBodyCtx) entryBodyCtx goRetType e
                    else exprToGo (phaseAFallback entryBodyCtx) entryBodyCtx e
```

This is the symmetric site to 6888 for ENTRY module emission.
Same shape: `entryBodyCtx` is constructed pure-functionally
above; the typed-body bracket writes it to scopeStateRef before
forcing the body lowering. Same self-referential IORef hop.

### Same change

Apply `phaseAFallbackFromCtx entryBodyCtx` at both call sites.

### Same gates

Identical to iter 7.

### Estimated effort

**1 session** (smaller — same helper already exists from iter 7).

---

## What iter 7 + iter 8 DO NOT close

This is the "ratchet" framing — iters 7+8 advance 2/17 of the
cgEnv-channel readers, leaving 15 to go. Criterion #3 itself
closes when the LAST reader is drained AND `scopeStateRef` can
be deleted (no remaining writers either).

The 5 currently-failing CI tests close downstream of:

- **#2 NoT1LeakInNotesApp**: needs iter 9 (dep-body-emission inner
  sub-paths that still call phaseAFallback at sub-thunks) + iter
  10 (typedBody bracket IORef write removal — depends on every
  reader of `scopeStateRef` being drained first).

- **#3 CrossModuleLambdaCollisionC**: per the autonomous /loop
  prompt G3, this spec encodes the env-free (incorrect)
  behavior. It needs to be RETARGETED to assert env-aware output
  once iter 9-10 land. Filed as iter 11.

- **#4 #5 DepCurrentModuleHint**: same family as #3. Iter 11.

- **#1 DictSource**: orthogonal — Linux clean-build environment
  flake. Iter 12 investigation.

---

## Per-CLAUDE.md §0 hard rule 1 (verbatim goal restate)

> "100% fully typed e2e, if valid sky code is consumed, the type
> sig is 100% correct through to emitted go code. no runtime
> panics, truly if it compiles it works. rock solid + future
> proof sky compiler + 100% soundness for v0.17."

Iters 7+8 advance criterion #3 (IORef deletion) by 2/17 of the
cgEnv-channel reader migration. They do NOT, by themselves,
satisfy any of the 10 criteria. Per §0 rule 3, the framing of
this iter is *partial-close, ratchet by 2 sites*, not "iter 7
closes <something>".

---

## Per-CLAUDE.md §0.3 rule 3 (N-strikes audit)

The 4 prior reverts on the **`cgEnv reader extension`** lever:

1. **iter 17 / 37 / 42 / Class-A swap**: substituted reader at
   N sites for `ctx.lookupCgEnv` from captured-at-call-time
   LowerCtx. Reverted: stale capture.
2. **Session 8 Option F**: substituted reader at 3 sites for
   `readIORefNoCse scopeStateRef`. Reverted: cross-compile bleed.

The lever this design uses: **drain reader at sites where
threaded ctx is the truth** (the cgEnv flowing through the
threaded LowerCtx is structurally equal to what the IORef would
return at that site BECAUSE the IORef is self-referentially
written immediately above). This is the **inverse** lever — we
remove ONE reader instead of adding readers at MORE sites.

This is N=0 on the new lever, not N=5 on the prior lever. Per
§0.3 rule 3 N-strikes, no re-classification required for iter 7.

If iter 7 ALSO reverts, we have N=1 on the new lever family, and
N=6 across the broader "cgEnv channel surgery" superset. At
that point: STOP iter 8, escalate to the user with the postmortem,
and reclassify whether the criterion-3 path needs the
`continueCompile`-phase-extraction work (iter 33+ in the master
plan) to land before any more reader drains.

---

## Forbidden phrase check (5 PASS per CLAUDE.md §0 rule 3)

This document does NOT use any of:

- "load-bearing-but-pure" — ✗ not used; the `phaseAFallback`
  function survives unchanged for the other 16 sites and IS
  documented as transitional in its docstring (iter 6d).
- "documented as <X>" when goal says deleted — ✗ not used;
  iter 7/8 framed as *partial-close*, not full criterion-3 close.
- "shipped for the scope of <subtask>" — ✗ not used; explicit
  "2/17 sites drained" framing.
- "deferred to later" — ✗ not used; subsequent iters are
  sequenced, not deferred.
- "essentially closed" — ✗ not used; "advances by 2/17" is the
  honest framing.

---

## Iter 7 grill table (5 G's pre-empted)

| # | Adversarial concern | Pre-empted answer |
|---|---|---|
| G1 | Does `LC.lookupCgEnv depBodyCtx` actually return the right cgEnv? | YES. `depBodyCtx = buildLowerCtxFromEmitCtx (… phaseACtx)`'s cgEnv field is sourced from `phaseACtx._cc_cgEnv` (compile-entry snapshot). The transformations `withCurrentDepModule` + `withEnclosingTypeParams` do not touch the cgEnv field. Verified by reading `Sky.Type.LowerCtx`. |
| G2 | Could iter 7 break a lazy thunk that captures cgEnv via the OLD IORef path? | NO. Lazy thunks captured by `exprToGoExpectGo` close over the `EmitCompileCtx` returned by `phaseAFallback*`. That ctx is a pure value; cgEnv inside it is stable for compile lifetime; anonRecsSnap is monotone. Force-time values match capture-time values. |
| G3 | What about sub-thunks within `lowerDepBody` that call `phaseAFallback` AGAIN at deeper sites? | They keep using the OLD `phaseAFallback` (16 sites unchanged in iter 7). The IORef write at the typedBody bracket (line 6903) still happens, so deeper sites still see depBodyCtx via the IORef. Iter 7's change is only at the outer call. |
| G4 | Could iter 7 regress the bundled-console regenerate? | NO. Bundled-console regenerate failure is at a DIFFERENT site (see session-8 bisect: literal coercion target + anon record decls). Iter 7 touches neither. Verification gate #4 confirms unchanged. |
| G5 | What if iter 7 also fails to close NoT1LeakInNotesApp? | Expected possibility. NoT1LeakInNotesApp's T1-leak fires at a sub-thunk site deeper than 6888. Iter 9-10 will need to drain those too. If iter 7 alone closes it, that's a bonus to verify and document. |

---

## Implementation order (when an executor session picks this up)

1. **Pre-build sanity**: confirm `git status` clean,
   `feat/v0.17-pure-sound-codegen` checked out at expected SHA.
2. **Helper added**: `phaseAFallbackFromCtx` above
   `phaseAFallback` in Compile.hs. Build.
3. **Iter 7 swap**: 2 LOC at Compile.hs:6888-6889. Build.
4. **Targeted spec**: `Sky.Build.PhaseABaselineRegression`.
   Must pass.
5. **3-example clean-build**: 26-ui-showcase + 13-skyshop +
   00-standard-libs.
6. **Targeted T1 check**:
   `Sky.Build.NoT1LeakInNotesApp` — measure outcome (close or
   no-change). Either is acceptable; document the result.
7. **Full sweep**: 26/26.
8. **Commit + targeted gates run**.
9. **Iter 8 swap**: 2 LOC at Compile.hs:9353-9354. Build +
   repeat gates 4-7. Commit.
10. **Single push to origin at iter 8 close** (per CLAUDE.md
    §0.1 milestone-batching rule).
11. **Update task tracker**: #678 advances from "in_progress"
    to "iter 7+8 shipped, 2/17 sites drained".

---

## Iter 9 — extended drain inside typeIIFE / wrapTypedReturn (shipped)

### Sites drained

- Compile.hs:10145 / :10150 — `goZeroValue (phaseAFallbackFromCtx ctx)`
  inside `typeIIFE`
- Compile.hs:10161 / :10162 — `wrapTypedReturn (phaseAFallbackFromCtx ctx)` from `typeIIFE`'s "canType=False" + `_` fallback arms
- Compile.hs:10210 / :10214 — `goZeroValue` + `wrapTypedReturn` inside `coerceReturnExprT`
- Compile.hs:10626 — `goExprGoType (phaseAFallbackFromCtx ctx)` INSIDE `wrapTypedReturn` (the "third reader" the iter-9 grill agent flagged; the surface comment claimed to drain it but the inner IORef hop remained until this fix)

### Safety lemma (verified by grill)

The original design doc claimed "typeIIFE has 3 callers". The grill agent corrected this: **typeIIFE has 8 call sites** — `:2039` (caseToGoSealedIface), `:6911` (lowerDepBody typedBody bracket), `:9381` (lowerFnBody typedBody bracket), `:18503` (ifToGo), `:18640` (letToGo), `:19397` (caseToGoLegacy), plus recursive at `:10209`, plus the definition itself.

The actual safety lemma the grill validated: **every render-forced reachability of typeIIFE / coerceReturnExprT / wrapTypedReturn goes through a `scopeStateRef := ctx; force; restore` bracket — verified at Compile.hs:6908-6913, 9378-9383, 13348-13352, 13363-13367**. The 8 typeIIFE call sites are all transitively guarded by one of these brackets.

Critically: iter 9 is **strictly more deterministic** than the legacy IORef hop. Even when nested calls inside typeIIFE OVERWRITE scopeStateRef during typeIIFE's execution, `phaseAFallbackFromCtx ctx` reads the THREADED ctx (the invariant), not the IORef (which may carry a nested-overwrite transient). This is iter 9's actual safety advantage.

### N-strikes audit (iter 9)

Lever family = "drain phaseAFallback caller via phaseAFallbackFromCtx where typedBody bracket holds":
- iter 7 — site 6911 (depBodyCtx). LANDED.
- iter 8 — site 9381 (entryBodyCtx). LANDED.
- iter 9 — 7 sites total (6 in typeIIFE/coerceReturnExprT + 1 in wrapTypedReturn).

Strikes = 0 reverts. iter 7 + 8 SUSTAINED for ~2h before iter 9 retry. Lever earned its first batch-extension via grill validation.

### Grill-mandated revisions accepted

The grill agent returned REVISE with 3 mandatory revisions:
1. **Document the actual 8-caller safety lemma** — done above.
2. **Address the third reader at :10626** — drained in this iter (the same commit).
3. **Add example-sweep verification gate covering the 4 newly-covered paths** (case-of, if-then-else, let-in, sealed-iface dispatch) — full `scripts/example-sweep.sh` exercises examples 19/26/13 which cover all 4. No additional gates beyond the full sweep are needed.

## Iter 10+ preview (next sessions, not designed here)

- **Iter 10**: drain `phaseAFallback ctx` at remaining high-leverage
  sites (whichever surface the T1-leak class — see
  `Sky.Build.NoT1LeakInNotesApp` for the canonical reproducer).
- **Iter 10**: typedBody bracket IORef-write removal (once
  every reader is drained, the write is dead).
- **Iter 11**: bug-compat spec retargets
  (CrossModuleLambdaCollisionC + DepCurrentModuleHint × 2).
- **Iter 12**: DictSource Linux investigation.
- **Iter 13-17**: continue draining; criterion-3 close fires
  when `scopeStateRef` has zero readers AND zero writers.

---

## Sign-off

This design IS NOT executed in the session that produced it. It
is grounding for the next executor session — autonomous loop or
interactive. Per CLAUDE.md §0.4, the agent + grill BEFORE
implementation pattern requires this document exist and pass
review before any Compile.hs edits.

The agent + grill that produced the iter 7 PROPOSAL above missed
the AnonRec channel coupling. This document closes that gap with
the cgEnv-vs-AnonRec asymmetry analysis. A future executor MUST
read this whole document before making the 2 LOC change.
