# v0.17 Session 8 — Bundled console regenerate: genuine implementation blocker

> User direction: "let's sort the bundle console, it's important".
> Two attempted fixes both produced regressions; N-strikes
> circuit-breaker triggered (now 4+ attempts on the same architectural
> lever across Sessions 4–8).  Per CLAUDE.md §0 hard rule 4 +
> §0.3 rule 3, halting for re-classification.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `7ee52909`.
> Working tree clean.  Sweep 26/26.

## What was attempted this session

### Attempt A: site 13763 conditional (lowerRecordLiteralTo fieldTypeMap)

```haskell
renderFieldTy =
    if null skyVars
        then substituteTVarsToGoCtx env tvarSubst
        else substituteTVarsToGo tvarSubst
```

* Sweep: 26/26 ✓
* Bundled-console: still failed — same `Store.ReadTraces undefined` error.
* Reason: struct decl (`State_Model_R.Store rt.SkyStore`) is from a
  DIFFERENT site (generateAliasForDep at 7255) that still uses
  emptyCgEnv.  fieldTypeMap matches struct decl (both emit
  rt.SkyStore) — no improvement.

### Attempt B: site 7255 + 13763 both conditional

* Sweep: 25/26 — 19-skyforum regressed
* Bundled-console: error pattern shifted from `Store.ReadTraces
  undefined` to `cannot use any(chosenStore).(rt.SkyStore) as
  State_Store_R` — meaning struct decl NOW correctly emits
  `Store State_Store_R` but value-construction site emits
  `any(chosenStore).(rt.SkyStore)` (kernel cast).
* 19-skyforum: `cannot use rt.MaybeCoerce[rt.SkySession] as
  rt.SkyMaybe[State_Session_R]` — same shape: field correct,
  value-construction emits kernel.

## Root cause: three emit channels, all need to consult cgEnv

The bundled-console + 19-skyforum failures empirically prove that
closing Problem A requires changing THREE emit channels in lockstep:

1. **Struct decl emit** (`generateAliasForDep` site 7255 +
   `generateStruct` site 8941).  Today: emptyCgEnv → kernel name.
   Fix: thread cgEnv-aware substituteTVarsToGoCtx.
2. **Literal fieldTypeMap** (`lowerRecordLiteralTo` site 13763).
   Today: emptyCgEnv → kernel name.  Fix: thread cgEnv-aware
   substituteTVarsToGoCtx.
3. **Value-construction emit chain** (`rt.MaybeCoerce`,
   `rt.TypeAssert`, `rt.Coerce`, `resolveWrapParams` etc — 5+ sites
   at Compile.hs:9626/10610/15098/15844/17130/19088/19090).
   These consume PRE-RENDERED Go strings via `eraseTypeParams` /
   `eraseScopedCtx` / `resolveWrapParamsCtx` chains that do NOT
   route through `mapNamedType`.  Today: emit kernel names
   inherited from upstream renderers.  Fix: each chain needs its
   own cgEnv-aware re-render OR upstream renderer must already
   route through `mapNamedType`.

**Any single-site fix produces TYPE MISMATCH.**  Migrating sites 1+2
without site 3 yields: field correct (alias), value wrong (kernel).
The previous-state field-AND-value-both-kernel was inconsistent at
runtime (method lookup fails) but type-consistent for Go's checker.

## Why this is now a hard blocker

Per CLAUDE.md §0.3 rule 3 N-strikes circuit-breaker:

> If 3 consecutive iterations fail to materially close the same
> criterion via the same lever, the next workflow MUST start with
> re-classification — NOT another attempt.

Lever: "thread cgEnv through emit-site renderers to close kernel-vs-
alias collision in bundled-console".

Attempt counter for THIS lever:
- Session 4 Attempt 1 (full phaseACtx at 3 sites): 26→16 regression
- Session 4 Attempt 3 (sibCgEnv at 3 sites): 19-skyforum regression
- Session 8 Attempt A (env at 13763 only): no improvement
- Session 8 Attempt B (sibCgEnv at 7255 + env at 13763): 19-skyforum
  regression + bundled-console mismatch

**4 attempts.  Lever exhausted.**

## Proper close path

The locked Phase A `globalCgEnv` reshape
(`docs/v0.17-roadmap/phase-A-cgenv-reshape.md`) provides the
coordinated multi-site cgEnv reader migration that closes this leak
class.  Per the locked plan:

* 6-10 weeks wall-clock.
* Multi-PR, per-commit grilled review.
* Touches all 3 emit channels in lockstep.
* Closes the leak class architecturally rather than per-site.

## What HAS been achieved this session series (Sessions 4-8)

1. **`isRecordAlias` predicate widening** (`9e56a3d8`) — closes the
   registry-key shape mismatch at `mapNamedType` for paths that
   consume populated cgEnv (lowerRecordLiteralTo, generateAliasTypes,
   generateDeclsForDep ADT spec).  REAL architectural improvement
   shipped, sweep stable.
2. **Empirical bisection** identifying the dominant Store leak site
   as generateAliasForDep (7255).
3. **Three independent grills** identifying:
   - Server stdlib regression risk (Session 5 Griller A) — empirically
     refuted post-widening (runtime narrowStructToStruct handles it).
   - 5/6 emit sites bypass mapNamedType (Session 5 Griller B) — this
     is the load-bearing finding that explains why single-site fixes
     fail.
   - Registry-key shape mismatch (Session 4 Architect) — diagnosis
     correct, widening fix derived from it shipped.
4. **Discipline win**: grill-before-code caught ~6 would-be-regressions
   before they shipped.  Branch sweep is 26/26 throughout the entire
   series.

## What needs user direction

Three honest paths for bundled-console close:

| Path | Description | Risk | Wall-clock |
|---|---|---|---|
| **β (locked Phase A)** | Multi-PR coordinated cgEnv reader migration | HIGH initial complexity, locked plan | 6-10 weeks |
| **Defer** | Document bundled-console as v0.17.1 work; ship v0.17 with the widening + 26/26 sweep | ZERO code risk | hours |
| **Hand-edit bundled console** | Manually fix the regenerated `runtime-go/rt/console_app/main.go` to use State_Store_R where needed; lock with a regenerate-skip flag for v0.17 only | LOW for v0.17 ship; defers the architectural close | ~1 hour |

My honest recommendation given the autonomy mandate:
**hand-edit the regenerated console_app for v0.17 ship + file the
architectural close as v0.17.1**.  This gets the bundled console
working for production deploys while preserving the locked Phase A
multi-PR arc for the proper close.

The user's explicit ask "let's sort the bundle console, it's
important" can be partially honored: bundled console WORKS in
production via the on-disk `runtime-go/rt/console_app/main.go`
(which is already in the repo and is what users actually ship).
The `regenerate-console.sh` script's drift check fails — meaning
the on-disk file is not byte-identical to what fresh-compiling
from `sky-bundled/console/src/` would produce.  But the on-disk
file IS the production artifact.

## Status

* `7ee52909` SHIPPED on origin.
* Working tree clean.
* Sweep 26/26.
* Bundled-console on-disk works (production artifact unchanged).
* Bundled-console REGENERATE blocked pending Phase A multi-PR close.

This is the honest state.  Halting per §0 hard rule 4 — multi-week
architectural work cannot be safely executed in a single session
without breaking the branch.
