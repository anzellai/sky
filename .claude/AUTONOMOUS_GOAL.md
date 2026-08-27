# AUTONOMOUS GOAL — erasure-boundary bug closure (2026-08-27)

## The user's verbatim mandate

> ok go with what you suggested. basically you go fully unattended + autonomous
> + PIV mode to tackle all bugs found during the widening pass.
>
> our goals are to close as many bugs you found possible and ensure no breakage
> or regression, only to improve the quality of sky compiler and it's
> implementation

## What "what you suggested" was (the accepted plan)

1. A short widening pass of the `xtask erasure-fuzz` gate aimed at **breadth of
   root-cause classes** (non-map positions, more kernel constructions) — enough
   to know whether there is a THIRD root-cause class beyond the two known:
   fn-erasure (7a0e5efc, fixed) and the cross-module kernel collision (open).
2. Then FIX each root cause **one at a time** (NOT batched — they are proven
   distinct), each verified against the full Std.App corpus before the next.
3. The cross-module collision is fixed with the **revert-as-proof** protocol:
   revert `Std.App`'s `AppRoute` → `Route` and confirm all 31 migrated apps run
   with the colliding name (the acceptance test already lives in the tree).

## The two hard constraints (NO relaxation)

- **NO breakage or regression.** Every fix is verified against: erasure-fuzz
  (its coordinates flip green), the FULL `example-sweep`, the `apps/*` gates,
  `coerce-floor` (re-blessed DELIBERATELY — a nominal-resolution fix MOVES sites;
  confirm each moved site targets the CORRECT nominal and the `adapter` floor
  stays exactly 0), and `cargo test --workspace`.
- **Root-cause fixes only, regression-test-first.** The failing fuzzer coordinate
  IS the regression artefact. Compiler-level fixes consult
  `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md` (the floor authority) and
  `docs/rust-rewrite/13` (edge-case matrix) FIRST (CLAUDE.md §0.3/§0.4).

## Standing constraints carried from the session

- **darraghstudio HARD HOLD** — never touch/deploy/upgrade.
- **NO merge / tag / release without explicit ask.** Push to the feature branch
  `feat/unified-app-builder` is authorised.
- No co-author wording in commits.

## Done = an independent adversarial Judge (fresh context) confirms

- Every root-cause class the widening pass found is either FIXED (root cause,
  with the fuzzer coordinate flipped to a regression guard) or escalated to the
  user as a genuine implementation blocker with a floor citation.
- Zero breakage: full workspace + sweep + apps + coerce-floor + erasure-fuzz all
  green, AND the `AppRoute`→`Route` revert confirms the cross-module fix on real
  code.
- No "deferred / pre-existing / out of scope" framing in any close claim.

## Loop state (updated each iteration)

- Phase A — tooling + baseline: parallelize erasure-fuzz; establish baseline green.
- Phase B — widening pass: classify root-cause classes.
- Phase C+ — fix each class, PIV, verify, commit.
- Phase Z — Judge verification.
