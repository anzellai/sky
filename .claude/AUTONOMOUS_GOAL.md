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

- Phase A — tooling + baseline: DONE. erasure-fuzz parallelized (6db7a440) +
  clean-retry for flake robustness (1c5267de).
- Phase B — widening pass: DONE (1c5267de). Result: exactly ONE open root-cause
  class — the cross-module kernel collision. Confirmed REP-SPECIFIC to Live.Route
  (among 5 kernels), POSITION-GENERAL (all 6 positions incl. no-map container
  literal). NO third class (fn/record/adt value-shape matrix all green; the
  7a0e5efc fn-erasure fix holds across positions). Gate: 70 pass · 0 new · 6/30
  known-open probes manifest (Live.Route × 6).
- Phase C — FIX the cross-module collision.
  * Architecture-consult (fresh agent, doc 14): PROCEED. Root cause = `Std.Live`
    never DECLARES `type Route`; it is a bare kernel-implicit name, so goty's
    cur_mod-preferred lookup binds the foreign `Live.Route` value to a same-named
    LOCAL type → rt.Coerce[wrong nominal] → panic. Std.Spa/Http.Server declare it
    (safe). Rep-specific to Live.Route (rt.liveRoute, non-ADT).
  * FIX 1 DONE + VERIFIED: declare `type Route = Route_OPAQUE` in Std.Live +
    expose it (mirrors Std.Spa/Http.Server). Reverted Std.App AppRoute→Route (the
    workaround) as the real-code acceptance proof. Verified: erasure-fuzz 76 pass
    / 0 bugs (all Live.Route collision coords promoted to MustPass guards);
    19-skyforum builds + runs HTTP 200 with the colliding `Route page`;
    coerce-floor re-blessed (+3 narrow/Live project, adapter floor unchanged 0),
    green. STILL TODO: workspace + full sweep + apps at milestone; commit.
  * FIX 2 (class fix) APPLIED: rather than declare each undeclared kernel-implicit
    name, harden `goty.rs` `app_to_go` so a BARE kernel-implicit name
    (hir::KERNEL_IMPLICIT_TYPES) never takes the cur_mod-preferred lookup (which
    captured a same-named LOCAL type). Flat-only for those names → resolves a
    DECLARED same-named type correctly (Error) and erases a truly-undeclared one
    to `any` (correct for a kernel handle). Closes the class for all 10 undeclared
    names (Value/Handler/Middleware/Session/VNode/Request/Response/Cmd/Sub/Db) in
    ONE change. Proven on a SECOND name: Sky.Http.Server.Response collided (via
    Dict.map) pre-fix, runs clean post-fix. coerce-floor: NULL diff (now=golden,
    adapter 0) — zero emission impact. Verified: Response + Route repros run clean.
    STILL TODO: full fuzzer + example-sweep + workspace at milestone; commit.
- Phase Z — Judge verification.
