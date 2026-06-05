# Cycle 1 Item P2-followup — implementation report

Date: 2026-05-25
Branch: `feat/v0.15.x-hardening-P2-followup-goexpr-skip-gate`
Tag target: v0.15.8 (subsumes original P2 which never tagged)
Worktree: `.claude/worktrees/agent-ab6b008230af4895f`

## Direction followed

Verbatim from
`docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md`
("Direction for Developer P2-followup" section).  Six steps:

1. Failing-test-first: `CoerceArgListMapInterplaySpec`.
2. Standing lock: `SkyshopCompilesSpec`.
3. Refine `coerceArg`'s skip-check gate.
4. Audit every `coerceArg mSrc` consumer.
5. Remove any "TEMPORARILY" debug comment (none present on
   main; original P2 worktree's debug comment never landed).
6. Verify cabal-sweep + clean-build + sky fmt.

## Files changed

- `src/Sky/Build/Compile.hs` — `goExprGoType` extended to take
  `Maybe Can.Expr` with structural fallback via `inferExprType`;
  14 call sites threaded with the appropriate `mSrc` choice per
  the arbitration's table.  The skip-check at line 8786
  (post-edit numbering) gated to use `goExprGoType Nothing e`.
- `test/Sky/Build/CoerceArgListMapInterplaySpec.hs` — NEW LOCK
  spec (~140 LOC).
- `test/Sky/Build/SkyshopCompilesSpec.hs` — NEW standing lock
  spec (~70 LOC).
- `test/fixtures/coerce-arg-list-map-interplay/sky.toml` — NEW
  fixture project.
- `test/fixtures/coerce-arg-list-map-interplay/src/Main.sky` —
  NEW reduced reproducer (post `sky fmt`).
- `sky-compiler.cabal` — register two new specs in test-suite
  other-modules.
- `test/Spec.hs` — wire new specs into hspec entry.
- `docs/v0.15.x-hardening/plans/CYCLE-01-planner.md` — P2 row
  updated to "P2 + P2-followup combined: 6-9h"; new
  "Coordination caveat — P2's structural fallback" section
  added per arbitration.
- `docs/fragility-audit-v0.15.3.md` — appended new "Closed in
  v0.15.x" section with item #19 (three-way σ consensus
  invariant).
- `docs/v0.15.x-hardening/CYCLE_LOG.md` — cycle line appended.

## Three-way σ consensus voter audit (PER ARBITRATION'S STANDING DIRECTION 1)

The PR touches `goExprGoType`, `coerceArg`'s skip-check, AND
σ-recovery in `coerceCallArgs(At)`.  Per the standing direction,
this audit MUST be in the PR description.

### Voter 1: σ-recovery in `coerceCallArgs(At)` / kernel branch

**Behaviour:** unchanged.  σ-recovery passes `Nothing` to
`goExprGoType` at every site (lines 6914, 6923, 7948, 7957,
8274, 8283 in the post-edit worktree).  No new precision flows
into σ pinning.

### Voter 2: TVar erasure

**Behaviour:** unchanged.  When σ leaves a TVar unbound,
`eraseTypeParams` still collapses to `any` at the callee's
target slot.

### Voter 3: `coerceArg`'s skip-check arm

**Behaviour:** changed (the refinement).  The skip-check at
line 8786 now passes `Nothing` to `goExprGoType`.  This restores
the pre-P2 behaviour at this voting site — the skip is only
fired when the IR-shape classifier alone recovers a matching
type.  When the IR-shape classifier returns Nothing, the wrap
fires (as it did pre-P2, in lossy-but-Go-safe mode).

### Consensus preservation proof

- Pre-P2: all three voters voted "any" uniformly under
  lossy-σ.  Three-way agree.
- P2 (regressed): voter 3 (skip-check) saw `mSrc=Just` and
  consumed the structural fallback; saw source matched target;
  elided the wrap.  Voters 1 + 2 still voted "any".
  Disagreement at the kernel's `[]T1` slot — Go rejected.
- P2-followup (this PR): voter 3 (skip-check) gated on
  `goExprGoType Nothing e`.  Cannot consume the fallback.
  Three-way agrees with voters 1 + 2 again.

### Where the structural fallback IS consumed (safe sites)

- **`coerceArg`'s parametric-alias arm** (line 8703) — passes
  `mSrc`.  Safe because the equality check is STRUCTURAL alias-
  base (target `Cfg_R[X]` matches source's `Cfg_R[Y]` base),
  not value-vs-target.  This is the Gap A2 closure that
  motivated P2.  Pinning T from the source's alias instantiation
  is correct under Go's generic-call inference.
- **`wrapTypedReturn` + `coerceToFieldType`** — already pass
  `Nothing` because they don't have a source `Can.Expr` in
  scope (the caller pre-lowered through `exprToGo`).  Not
  voters in the σ consensus.

## Consumer audit table (per arbitration Step 4)

| Site (post-edit line) | Function | mSrc | Status |
|---|---|---|---|
| 7956 | `emitPartialCtor.extraIdents` | Nothing | OK (synthetic `__pN` ident, no source expr) |
| 8278 | `coerceCallArgsAt` CSI-captured | `Just e` | OK (σ pinned, source-aware skip safe) |
| 8429 | `coerceCallArgsAt` no-CSI fallback | `Just e` | OK (gated skip-check makes this safe) |
| 9042 | `kernelCoerceArg` | `Just e` | OK (same as 8278) |
| 9279 | `lowerArgExpect` | `Just e` | OK (gated skip-check makes this safe) |
| 9370 | `emitPartialUserCall.extraIdents` | Nothing | OK (synthetic `__ppN` ident) |
| 10381 | `tcoBodyStmts.coerceForTco` | `mSrc` (the Just version) | OK (pre-existing v0.15.x A1 wiring) |

All consumers verified.

## Test evidence

### Cabal sweep (arbitration step 6.1)

```
$ cabal test --test-show-details=streaming \
    --test-options="--skip=/Sky.Lsp.NvimDriver/ \
                    --skip=/Sky.Lsp.Scale/ \
                    --skip=/Sky.Build.VerifyAll/ \
                    --skip=/Sky.Build.VerifyScenario/ \
                    --skip=/Sky.Build.EmbeddedRuntime/ \
                    --skip=/Sky.Build.EmbeddedInspector/ \
                    --skip=/Sky.Cli/"
...
Finished in 694.3896 seconds
307 examples, 0 failures, 1 pending
Test suite sky-tests: PASS
1 of 1 test suites (1 of 1 test cases) passed.
```

Including the 2 new lock specs:

```
Sky.Build.CoerceArgListMapInterplay
  coerceArg <-> goExprGoType three-way σ consensus
    (List.map / List.take interplay)
    clean-builds the List.map (\cat -> cat ++ "!") (List.take 6 xs) shape [✔]
    runtime output is the 6-element concat [✔]

Sky.Build.SkyshopCompiles
  examples/13-skyshop clean-build lock
    compiles the Stripe-SDK-scale benchmark cleanly [✔]
```

### Smoke clean-build (arbitration step 6.2)

```
cd examples/12-skyvote && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky
  → Build complete: sky-out/app

cd examples/13-skyshop && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky
  → Build complete: sky-out/app

cd examples/19-skyforum && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky
  → Build complete: sky-out/app
```

### sky fmt (arbitration step 6.3)

`sky fmt` applied to the new fixture; second pass byte-identical
(`diff` clean).

## Process hygiene

- mem-guard alive throughout (pid 91395).
- No orphan zsh wait-loops (verified via `ps -u $USER`).
- Process count under 500 throughout the cycle.
- Final cleanup: pending.

## Sign-off

All six arbitration verification steps green.  The lock specs
prove the three-way σ consensus stays consistent under this
edit.  Ready for PR + merge + v0.15.8 tag.
