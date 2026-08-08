# v0.17 Session 10 — Phase A iters 12-14 shipped

**Date**: 2026-06-30
**Branch**: `feat/v0.17-pure-sound-codegen`
**HEAD**: `71568bdd`
**Continues**: `session-9-phase-A-iters-7-to-11.md`

## Session summary

| Iter | SHA | Sites drained | Notes |
|---|---|---|---|
| 12 | `35cc7c9d` | 5 sites: coerceToFieldTypeMSrc, coerceVia, coerceArg trio | Multi-entry helper batch, grill-validated |
| 13 | `e90b20b4` | **102 sites — BULK DRAIN** via Edit replace_all | All remaining `phaseAFallback ctx` → `phaseAFallbackFromCtx ctx` |
| 14 | `71568bdd` | 1 site (parentCtx) | Final ctx-shape drain — `lowerTypedLambda`'s patternBindings call |

**Total this session**: 108 phaseAFallback IORef hops eliminated.
**Cumulative iter 7-14**: 128+ hops eliminated.
**N-strikes audit**: 8 consecutive iters, zero reverts.

## Critical insight from iter 13 bulk drain

The "drain reader at threaded-ctx-truth sites" lever, after 6 grilled
single-batch iters with zero reverts, EARNED its bulk-application
status. Iter 13 swapped 102 sites in one `replace_all` operation
with zero regression (sweep 26/26 green). This validates the
architectural invariant: every `phaseAFallback ctx` call site where
`ctx` is a directly-threaded LowerCtx parameter is structurally
identical to its `phaseAFallbackFromCtx ctx` replacement under the
typedBody bracket contract (verified at 4 install sites: 6908-6913,
9378-9383, 13348-13362, 13363-13377).

## Remaining sites (path to criterion #3 close)

After iter 14, only 8 `ctxFromIORef ()`-pattern sites remain:

| Site | Function | Sub-iter plan |
|---|---|---|
| :14023 | `lookupKernelAlias` bridge | iter 16 (signature widen) |
| :17525 | `isParametricCompatibleSource` | **iter 15 target** |
| :17627 | `isPlainIdentForTypedRouting` | **iter 15 target** |
| :20673 | `exprToMainStmtsTyped` entry | iter 16 |
| :20856 | `exprToMainStmts` legacy wrapper | iter 16 |
| :20869 | `exprToGoTypedWithRet` legacy wrapper | iter 16 |
| :21918 | `lookupLambdaType` caller | iter 16 |
| :21922 | `lookupLambdaGoType` caller | iter 16 |

### Iter 15 plan (per architecture-consult agent verdict)

Widen 2 helper signatures:
- `isParametricCompatibleSource :: GoIr.GoExpr -> Bool`
  → `isParametricCompatibleSource :: LC.LowerCtx -> GoIr.GoExpr -> Bool`
- `isPlainIdentForTypedRouting :: GoIr.GoExpr -> Bool`
  → `isPlainIdentForTypedRouting :: LC.LowerCtx -> GoIr.GoExpr -> Bool`

Thread `ctx` from the SINGLE caller in `coerceArg` @ :17425. No
external callers other than the recursive arm + IsPlainIdentSpec
fixture (per agent grep).

Estimated effort: ~30 LOC + signature changes + 2 call-site
updates. Build + gates + sweep cycle.

### Iter 16 plan

`lookupKernelAlias` @ :14023 + `lookupLambdaType` @ :21918 +
`lookupLambdaGoType` @ :21922 — these are inside the bottom-of-file
legacy entry helpers. The 3 `exprToMainStmts*` / `exprToGoTypedWithRet`
helpers at :20673/:20856/:20869 are LEGACY wrappers used by
test/LSP. Strategy: migrate them to take `LC.emptyLowerCtx`
explicitly at the call site, removing the IORef-bridge pattern.

Estimated effort: per-site audit + signature widening of 4-5
helpers. May require careful spec migration.

### Iter 17 — criterion #3 close

With all readers drained:
- DELETE `ctxFromIORef` function (:13392)
- DELETE `phaseAFallback` function (:13420-13443)
- DELETE `scopeStateRef` IORef + all `writeIORef scopeStateRef`
  sites + `resetCompileState` IORef-clear line
- Update the iter-0 contract's spec gate to assert
  `scopeStateRef`'s deletion

This is criterion #3 of the AUTONOMOUS_GOAL.md — the IORef DELETE
that the v0.17 architectural close has been building towards.

## Why halt now

Per CLAUDE.md "Stop conditions and honesty": this session shipped
8 iters across ~4 hours of focused work. Iter 15 is a more
complex change (signature widening + cross-call-site threading)
that needs fresh agent budget to grill + implement + verify
without risk of mid-iter context exhaustion leaving the working
tree in a partial state.

The path forward is fully documented above. A fresh session can
pick up at iter 15 cleanly, with:
- Agent verdict for iter 15 already on file (iter 14
  agent's "Path to criterion #3 close" section)
- All deferred sites enumerated above
- Branch + HEAD known

## Resume protocol for next session

1. Read this doc + `session-9-phase-A-iters-7-to-11.md`.
2. Confirm `git status -s` clean + HEAD = `71568bdd`.
3. Spawn Architecture-Consult agent for iter 15 with the prompt:
   "Widen `isParametricCompatibleSource` + `isPlainIdentForTypedRouting`
   signatures to thread `LC.LowerCtx`. Drain the 2 sites at :17525
   + :17627. Verify caller-tree (single coerceArg site @ :17425 +
   recursive arms + IsPlainIdentSpec)."
4. Standard gates: build + PhaseABaselineRegression + AnonRecord +
   sweep 26/26. Ship + push.
5. Same protocol for iter 16 (5-6 site coherent batch).
6. Iter 17 = criterion #3 close. Final verification gates + Judge
   verdict.

## Pushed commits this session

```
71568bdd feat(v0.17 Phase A iter 14): drain final phaseAFallback parentCtx site
e90b20b4 feat(v0.17 Phase A iter 13): BULK DRAIN — 102 phaseAFallback ctx sites
35cc7c9d feat(v0.17 Phase A iter 12): drain 5 phaseAFallback callers across multi-entry helpers
a1768d7c docs(v0.17): session-9 checkpoint — Phase A iters 7-11 shipped
9013630a feat(v0.17 Phase A iter 11): drain 3 phaseAFallback callers + delete dead coerceFfiArg helper
95d38ba4 feat(v0.17 Phase A iter 10): drain 6 SAFE phaseAFallback callers
539a597d feat(v0.17 Phase A iter 9): drain 7 phaseAFallback callers inside typeIIFE
b211c254 feat(v0.17 Phase A iter 8): drain phaseAFallback caller at entry body emission
ff32b01c feat(v0.17 Phase A iter 7): drain phaseAFallback caller at dep body emission
```

## Pre-session context (4 user fixes)

Before iter 7 work began, this session also shipped the 4 user-
reported bug closures:

- `3362e908` — bump 00-standard-libs rt.Coerce baseline 124→125
- `277ee217` — #2 PORT env, #3 anon record, #4 slider, #5 mktemp
