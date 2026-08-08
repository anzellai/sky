# CYCLE-01 — Developer record (Plan Item P3)

**Closes:** Audit Gap A4 + fragility-audit-v0.15.3.md item #7 residual.

**Branch:** `feat/v0.15.x-hardening-P3-isplain-ident-deep-recursion`

**Target patch tag:** v0.15.9 (push is the human's gate — branch +
PR is the developer's finish line).

**PR:** [#76](https://github.com/anzellai/sky/pull/76) — CI 4/4 GREEN
(Linux push 23m24s · Linux PR 36m49s · macOS push 33m2s · macOS PR
36m51s).

## Architectural diagnosis

`isPlainIdent` (`src/Sky/Build/Compile.hs` line 8733 pre-fix) is the
shape predicate consumed by the generic-param-bearing target arm of
`coerceArg` (`Compile.hs` line ~8660).  When the target slot is a
function type carrying a Go-side generic parameter (`T1`/`T2`/…),
the arm asks: *"is the source a plain user-ident chain whose static
Go type we can trust Go's call-site inference to pin T from?"*  If
yes, emit the expression raw — wrapping in `rt.Coerce[func(P) any]`
would erase the generic-param connection.  If no, wrap.

The pre-fix `isPlainIdent` answered structurally:
```haskell
isPlainIdent (GoIdent name)       = not ("rt." `List.isPrefixOf` name)
isPlainIdent (GoSelector base _)  = isPlainIdent base
isPlainIdent _                     = False
```
The audit's Gap A4 hypothesis — that the `GoSelector` recursion is
"only one level deep" — is technically wrong (the recursive call
itself has the same `GoSelector` arm and walks the chain to the
leaf, correctly returning False for the deep-`rt.SkyCall(...)`-
rooted case).  Recursion-correctness IS in place.

The REAL gap is one level above: even when the leaf is a plain
user ident, the chain `cfg.someAnyField.deep` may have an
intermediate selector whose base resolves to the static Go type
`any`.  Go's call-site inference cannot pin a TVar from an
`any`-typed base — the raw-pass silently routes a runtime panic
(the missing Coerce wrap was load-bearing).  The pre-fix
structural classifier had NO arm for this; the consumer at line
8660 accepted any chain whose leaf was plain.

## Closure mechanism

Two pieces:

1. **`isPlainIdent` stays pure + structural** (Compile.hs ~8733).
   The recursion invariants are now locked by a dedicated unit
   table in `test/Sky/Build/IsPlainIdentSpec.hs` — 20 assertions
   covering leaf cases, single-level selectors, deep selector
   chains rooted in idents AND in kernel-call results, and
   regression cases for the existing v0.15.3 acceptance set.

2. **`isPlainIdentForTypedRouting`** (Compile.hs new helper,
   immediately following `isPlainIdent`).  Layers a
   `goExprGoType`-on-every-selector-base check over the
   structural predicate.  For each selector in the chain, the
   base's `goExprGoType` MUST return `Just t` with `t /= "any"`.
   The Nothing-→-reject rule is intentionally strict: an
   un-trackable base could be a parametric-record-alias case the
   v0.15.3 arm handles, BUT could equally be a heterogeneous
   user expression — the soundness floor is to WRAP unless we
   can prove the unwrap-safe contract.  Companion unit table
   (8 assertions) locks the typed-gate behaviour.

3. **`coerceArg`'s consumer at line 8660** swaps the structural
   classifier call for the typed-routing call.  Updated comment
   documents the v0.15.x soundness invariant + locked-by-spec
   reference.

## Sequenced steps

1. **Failing-test-first.**  `test/Sky/Build/IsPlainIdentSpec.hs`
   builds a unit table over crafted `GoExpr` shapes.  The spec
   exercises BOTH `isPlainIdent` (structural) AND
   `isPlainIdentForTypedRouting` (typed).  On the starting
   worktree the spec fails to COMPILE — the typed-gate function
   doesn't exist yet:
   ```
   Not in scope: 'C.isPlainIdentForTypedRouting'
   NB: the module 'Sky.Build.Compile' does not export
   'isPlainIdentForTypedRouting'.
   ```
   Confirmed by `git stash push -- src/Sky/Build/Compile.hs`
   followed by `cabal build --enable-tests`.

2. **Add the typed-routing companion.**  New
   `isPlainIdentForTypedRouting` immediately follows
   `isPlainIdent` in `Compile.hs`.  Doc comment captures the
   soundness contract and the Nothing-→-reject rule.

3. **Update the consumer.**  `coerceArg`'s line-8660 gate switches
   from `isPlainIdent e` to `isPlainIdentForTypedRouting e`.
   Updated comment documents the v0.15.x P3 closure and the
   locked-by-spec reference.

4. **Re-run the spec.**  28/28 assertions green.

5. **Cabal-file deps.**  Test stanza imports `Sky.Build.Compile`
   directly for the first time; pulls in `async`, `mtl`,
   `binary`, `utf8-string`, `optparse-applicative`,
   `cryptohash-sha256`, `ansi-terminal`, `unix` (POSIX) parity
   with the library stanza.

6. **Wider cabal test sweep** (standard skip pattern) green.

7. **Golden-size delta** on three representative examples (per
   Planner's risk register, ≤ ±3 %):
   - `12-skyvote`:    1562 LOC pre = 1562 LOC post (sha
     `b87610bb…` byte-identical)
   - `13-skyshop`:    4351 LOC pre = 4351 LOC post (sha
     `67b048ec…` byte-identical)
   - `19-skyforum`:   1454 LOC pre = 1454 LOC post (sha
     `427e75bf…` byte-identical)

   The stricter typed gate did NOT shift the codegen on any of the
   three — the v0.15.3 arm's existing acceptance set was already
   only firing where `goExprGoType` returns Just non-`any`.  The
   fix is a pure soundness tightening with zero codegen change.

## Files touched

- `src/Sky/Build/Compile.hs` (≈ +100 LOC: doc on `isPlainIdent` +
  new `isPlainIdentForTypedRouting` + consumer-site comment +
  switch).
- `test/Sky/Build/IsPlainIdentSpec.hs` (NEW, 28 assertions —
  structural + typed-gate unit tables).
- `test/Spec.hs` (`describe "Sky.Build.IsPlainIdent" …`
  registration).
- `sky-compiler.cabal` (test stanza missing-deps parity; new
  `Sky.Build.IsPlainIdentSpec` other-module entry).

## New tests

- `Sky.Build.IsPlainIdentSpec` — 28 assertions:
  - 8 leaf cases (`GoIdent` / `rt.*` / literals / call / func /
    struct literal / typed-block).
  - 4 single-level selectors (`ident.f` / `rt.*.f` / `(call).f` /
    `(lit).f`).
  - 6 deep selector chains (ident root accepted at depths 2-3;
    kernel-call root rejected at depths 2-4 plus a
    Coerce-wrapper-root variant plus a typed-block-base variant).
  - 2 regression invariants (the original v0.15.3 acceptance
    cases `cfg.WfSubmit` + a deep user chain still accepted).
  - 8 typed-gate cases (bare ident accepted; `rt.*` rejected;
    `cfg.WfSubmit` rejected in empty env via the strict
    Nothing-→-reject rule; kernel-call-rooted chains rejected
    via structural-reject; deep user chains rejected in empty
    env; literal / call rejected).

## Test evidence

```
Sky.Build.IsPlainIdent
  isPlainIdent (Gap A4 / P3 structural classifier)
    leaf cases (8 assertions) — all passed
    single-level selectors (4 assertions) — all passed
    deep selector chains (6 assertions) — all passed
    regression invariants (2 assertions) — all passed
  isPlainIdentForTypedRouting (Gap A4 / P3 typed gate)
    (8 assertions) — all passed

Finished in 0.0016 seconds
28 examples, 0 failures
Test suite sky-tests: PASS
```

## Rollout / regression gates

- 28-assertion IsPlainIdent unit table green.
- Wider cabal test sweep (standard skip pattern) — **332 examples,
  0 failures, 1 pending** (matches prior pending count). `1 of 1
  test suites (1 of 1 test cases) passed`.
- 3 representative examples (12-skyvote, 13-skyshop, 19-skyforum)
  build clean from wiped slate.
- Golden-size delta byte-identical on all 3 representative
  examples (well within Planner's ±3 % tolerance).

## Verification

| Gate | Status |
|---|---|
| mem-guard alive | Yes (pgrep `mem-guard.sh` returned PID) |
| IsPlainIdent unit table | 28/28 green |
| Wider cabal test sweep | 332/333 PASS (1 pending matches prior) |
| 12-skyvote clean build | OK (1562 LOC, sha b87610bb…) |
| 13-skyshop clean build | OK (4351 LOC, sha 67b048ec…) |
| 19-skyforum clean build | OK (1454 LOC, sha 427e75bf…) |
| Golden-size delta | 0 % (byte-identical on all 3) |

## Risk register (closed)

- Risk: stricter gate over-rejects in the v0.15.3-codified
  acceptance set, inflating main.go LOC.  Mitigation: golden-size
  measurement on three representative examples.  Outcome: ZERO
  change on all three — the v0.15.3 acceptance set is bounded to
  cases where `goExprGoType` already returns Just non-`any`.
- Risk: impure `goExprGoType` reads through `unsafePerformIO`
  destabilise the structural-recursion test.  Mitigation: split
  into pure `isPlainIdent` (test-able) + impure
  `isPlainIdentForTypedRouting` (production gate).

## Sign-off checklist

- [x] mem-guard alive throughout.
- [x] All cabal tests green (309+ specs; new IsPlainIdent adds
      28).
- [x] 3 representative examples build clean from wiped slate.
- [x] `sky fmt` — no `.sky` files in this PR.
- [x] Background tasks cleaned up before declaring done.
- [x] Golden-size delta logged.
- [x] PR opened (#76), CI 4/4 green.
- [x] CYCLE_LOG line appended.
