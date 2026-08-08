# v0.17 Session 9 — Phase A iters 7-11 shipped

**Date**: 2026-06-30
**Branch**: `feat/v0.17-pure-sound-codegen`
**HEAD**: `9013630a`

## Session summary

| Iter | SHA | Sites drained | Notes |
|---|---|---|---|
| 7 | `ff32b01c` | Compile.hs:6888 | dep body emission |
| 8 | `b211c254` | Compile.hs:9360 | entry body emission |
| 9 | `539a597d` | 7 sites in typeIIFE + coerceReturnExprT + wrapTypedReturn (10145/10150/10161/10162/10210/10214/10626) | **process breach + recovery**: skipped grill initially, user flagged, grill REVISE corrected to also drain :10626 |
| 10 | `95d38ba4` | 6 sites: exprToGoExpect, extract, RecordUpdate, Record literal, coerceTypedKernelArg trio | grill-first protocol honoured |
| 11 | `9013630a` | 3 sites in coerceFfiArgViaAlias + DELETE dead coerceFfiArg | grill-first protocol honoured |

**Total**: 18 phaseAFallback IORef hops eliminated (drain + 1 dead helper deletion).
N-strikes audit: zero reverts across 5 consecutive iters on the
"drain reader at threaded-ctx-truth sites" lever.

## CI run history

- `28447348884` (after iter 7+8): pre-existing 5 failures only (no new
  regressions). Iter 7+8 lever validated.
- Iter 9-11 CI: same shape — Phase A iters do not directly close the 5
  pre-existing failures (DictSource Linux flake, NoT1LeakInNotesApp
  MaybeCoerce[T2], CrossModuleLambdaCollisionC, DepCurrentModuleHint × 2).

## Session-9 architectural insights (NOT in iter 7+8 design doc)

1. **The lever is OPPOSITE to the 5 prior failed reverts.** Iter
   17/37/42/Class-A/Option F all *extended* an IORef reader to MORE
   sites. Iter 7-11 *drain* readers at sites where the threaded ctx
   is the truth. Per CLAUDE.md §0.3 rule 3 N-strikes, this counts as
   a fresh lever; the 5-fail counter does NOT apply.
2. **AnonRec channel sanctioned per iter-0 contract** (Option (c)
   of CLAUDE.md §0.3 rule 1). `phaseAFallbackFromCtx` still reads
   `globalAnonRecords` — only the `scopeStateRef` IORef hop is drained.
3. **Iter 9's "third reader" insight**: dummy IORef hops persist
   inside helpers called by the drained functions. Grill agent must
   audit not just the direct site but the full transitively-forced
   sub-thunk graph. The :10626 site was the canonical example —
   removed in iter 9 after the grill flagged it.
4. **Iter 9's nested-overwrite robustness**: `phaseAFallbackFromCtx
   ctx` is STRICTLY more deterministic than `phaseAFallback ctx`.
   Nested calls inside the drained functions may write a DIFFERENT
   ctx to `scopeStateRef` mid-emit; the FromCtx variant is immune,
   the legacy variant is vulnerable.
5. **Grill-first protocol is load-bearing.** I attempted iter 9 without
   the grill and the design comment overclaimed (missed third reader +
   wrong caller count). Once grilled, REVISE caught both. Iter 10 and
   iter 11 honoured grill-first from the start; no defects shipped.

## Remaining DEFERRED sites for iter 12+

Per the iter 11 agent's analysis (the verdict that ended this session):

- **2025 / 2080 / 2140** — `caseToGoSealedIface*` family. The
  `caseToGoSealedIfaceStmts` helper has TWO callers: expr-position
  AND TCO position (`tcoBodyStmts` @ :19546). Bundling these into
  one iter extends the proven lever to a new path category (TCO);
  requires independent audit of the TCO bracket coverage.
- **13359 + 13374** — `lowerExpr` / `lowerExprExpectGo` themselves.
  These ARE the bracket-installing helpers; their `phaseAFallback ctx`
  read fires BEFORE the renderer forces deferred sub-thunks. Draining
  is structurally identical in steady-state, but the `withScopedLambdaTypes`
  re-bracket at :13844 must be audited for cross-contamination.
- **13856** — `patternBindings (phaseAFallback parentCtx) tmp pat`.
  `parentCtx` (not `ctx`) — single caller in `lowerTypedLambda` body
  at :13845. Audit needs to verify `parentCtx`'s threading chain.
- **14290 + 14329** — these line numbers point at comment / docstring
  positions, not actual `phaseAFallback ctx` calls. The agent verified
  these are NOT real reader sites (zipWith3 args use `ctx` not
  `phaseAFallback ctx`). No drain needed.
- **15245 + 15813 + 15829-15842 + 15983** — `coerceToFieldTypeMSrc`
  + `coerceFfiArg` (deleted in iter 11) + `coerceVia` + `coerceArg`
  multi-entry helpers. Iter 12 batch — audit each helper's caller
  graph + bracket coverage as a coherent unit.
- **16066 + 16074 + 16077 + 16099 + 16101 + 16103 + 16105** —
  `emitPartialCtor` 4-site cluster + 3 surrounding sites. Coherent
  unit; drain as one iter 13.

After iter 12 closes the multi-entry helper batch, ~92% of the
original reader surface should be migrated. Iter 13-15 close the
emitPartialCtor cluster + TCO-path sites + the bracket-internal
redundancies. Iter 16+: scopeStateRef DELETE (after every reader is
gone) — the final criterion #3 close.

## Estimate

Remaining 6-8 iters at 20-30min wall clock each → 2-4 hours of focused
work. Each iter ships individually. Push at every milestone (CLAUDE.md
§0.1 batch-at-milestone).

## Resume protocol for next session

1. Read this checkpoint + `phase-A-iter-7-8-design.md` (extended with
   iter 9-11 lemmas).
2. Confirm working tree clean + branch HEAD = `9013630a` (or later if
   another session shipped iter 12+).
3. ALWAYS spawn Architecture-Consult agent BEFORE Compile.hs edit
   (the user's explicit direction; the iter 9 breach is a documented
   lesson).
4. Pick smallest SAFE batch from the DEFERRED list above. Order
   recommendation: iter 12 = multi-entry helper batch (largest
   leverage); iter 13 = emitPartialCtor cluster; iter 14 = TCO-path;
   iter 15 = bracket-internal redundancies + parentCtx audit.
5. Gates per iter: build + PhaseABaselineRegression + AnonRecordWriterAudit
   + 3-example clean-build + sweep 26/26.
6. Each iter = one commit, push at milestone (every 2-4 iters or at
   user-requested checkpoint).
