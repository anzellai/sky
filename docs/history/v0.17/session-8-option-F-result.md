# v0.17 Session 8 — Option F attempt: REVERTED

> Continued from `session-8-bisect-result.md`.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `53e82c44` (working tree
> clean after revert).

## What was tried

Option F per the bisect doc: at `lowerRecordLiteralTo`
(Compile.hs:13754) restore live-IORef cgEnv read so dep-emit-time
record-alias registrations (canonical reproducer:
`sky-bundled/console`'s `State_Store`) are visible.

When the literal-site fix alone left the regenerated console's
`State_Model_R` struct decl still emitting `Store rt.SkyStore`,
the bridge was extended to the two struct-decl sites:

* `generateAliasForDep` @ Compile.hs:7255 (dep-module struct decl)
* `generateStruct` @ Compile.hs:8941 (entry-module struct decl)
* `lowerRecordLiteralTo.fieldTypeMap` @ Compile.hs:13820 (literal
  per-field type substitution)

Pattern at each site:

```haskell
liveCgEnv = case LC._lc_cgEnv (readIORefNoCse scopeStateRef) of
    Just live -> live
    Nothing   -> emptyCgEnv   -- or `lookupCgEnvFromCtx phaseACtx`
fieldGoType fty = substituteTVarsToGoCtx liveCgEnv tvarMap fty
```

## Partial progress

After the struct-decl bridges, the regenerated console_app emitted:

| | Before F | After F (struct-decl sites) |
|---|---|---|
| `State_Model_R.Store` | `rt.SkyStore` | `State_Store_R` ✓ |
| `State_Model_R.Tab` | `any` | `State_Tab` ✓ |
| `State_Model_R.Range` | `any` | `State_Range` ✓ |
| `State_Model` ctor `p3` | `rt.SkyStore` | `State_Store_R` ✓ |
| Literal `Store: …` cast | `.(rt.SkyStore)` | `.(rt.SkyStore)` ✗ |
| `Anon_R_…store…` decls | `rt.SkyStore` | `rt.SkyStore` ✗ |

The struct decl emits correctly typed.  The literal coercion
target type still resolves to `rt.SkyStore` — meaning the cast
target is computed via a code path that does NOT consume the
live cgEnv I threaded through `fieldTypeMap`.  Anonymous record
emission (`generateAnonRecordDecls`) is yet a third independent
path that also still misses the alias.

## Why it regressed

Full sweep with all three F bridges applied:

```
sweep: 15 passed, 11 failed
  - 06-json
  - 08-notes-app
  - 12-skyvote
  - 13-skyshop
  - 14-task-demo
  - 17-skymon
  - 18-job-queue
  - 19-skyforum
  - 26-ui-showcase
  - 27-multi-session-chat
  - 33-websocket-echo
```

Reading the live `scopeStateRef._lc_cgEnv` at struct-decl emit
time isn't safe across compilations: the IORef carries state from
WHATEVER compile most recently wrote to it.  In a multi-example
sweep that compiles examples sequentially via the same process,
example N's struct decls read state that was last set by example
N-1.  The alias names from a prior example bleed into the
current example's mapNamedType lookups and rewrite types
incorrectly (e.g. a `Session` field that should be `rt.SkySession`
becomes `State_Session_R` because some prior example registered
`State_Session`).

This is the same class of bug that motivated commit `7d09b428`
in the first place (RtFieldAdtBug342 — IORef state bleed across
compile boundaries).  My F bridge re-introduced exactly that
hazard.

## Why the bisect was misleading

The bisect showed commit `7d09b428` broke the bundled console
regenerate.  That's TRUE for the regenerate path (where the
compiler runs once on the bundled-console source after a fresh
build).  But it's only LOCALLY true — the same commit was
correct for sweep-internal multi-compile correctness.  Option F
restores regenerate but breaks sweep.  The two readers diverged
intentionally in `7d09b428` because they need different semantics.

## What this means for v0.17 close

We're at N-strikes per CLAUDE.md §0.3.  The "extend cgEnv reader
to one more emit site" lever has been tried in:

* Phase A iters 6 / 6a-d (per-dep ctx rebuild + read-through
  fallback)
* Session 4 commits 1-3 (Class-A reader migration)
* Session 5 Option α (widening at `mapNamedType`)
* Session 7 (`isRecordAlias` predicate widening)
* Session 8 Option F (live-IORef bridge at 3 sites)

Each attempt either left the bundled-console-regenerate broken
or regressed unrelated examples.  The architectural reality:

* The threaded `phaseACtx` cgEnv is a COMPILE-ENTRY snapshot.  It
  doesn't carry per-dep alias registrations.
* The live `scopeStateRef._lc_cgEnv` IS up to date during the
  current compile, but contains BLEED from prior compiles.
* Per-dep ctx rebuild (iter 6c) computed a fresh-per-dep ctx
  but didn't bridge to entry-module emit sites.

**The only path that closes the divergence at the source** is
Option H — push the phaseACtx capture past dep emission, OR
maintain a compile-scoped cgEnv reader that resets at compile
entry AND accumulates per-dep writes BUT doesn't leak to the
next compile.  This is the locked Phase-A direction per
`docs/v0.17-roadmap/phase-A-cgenv-reshape.md`.

## Status

* Branch: `feat/v0.17-pure-sound-codegen` at `53e82c44`.
* Working tree: clean (compiler reverted, on-disk bundled-console
  restored to the working typed version).
* Sweep: 26/26 (baseline).
* Bundled-console regenerate: still BLOCKED, same regression
  commit `7d09b428` identified.
* Bisect doc: `docs/v0.17/session-8-bisect-result.md`.
* This doc: postmortem of Option F's failure.

## Recommendation

**Stop the cgEnv-reader-bridging lever.**  Three sessions of
incremental attempts have produced no shippable close.  The
options that remain:

1. **Option H** — pipeline reshape so phaseACtx captures POST-dep-
   emission.  Multi-week work; touches `continueCompile` /
   `emitPhase`.  Locked direction per Phase-A plan.
2. **Accept regenerate-broken for v0.17 release.**  The on-disk
   bundled-console is the working artifact (last regenerated when
   the compiler was healthy).  The compiler regression is real
   but tooling-only — users don't regenerate the bundled console;
   that's a release-engineering step.  Document the limitation
   in `docs/v0.17/known-limitations.md`, file a Phase-A close
   task, ship v0.17.
3. **Revert commit `7d09b428` entirely** and re-do the
   RtFieldAdtBug342 fix via a different approach.  942-line
   refactor; high regression risk.

User decision required — none of these is autonomous-safe.
