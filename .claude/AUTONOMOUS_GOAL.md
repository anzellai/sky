# Autonomous goal (verbatim)

Set 2026-09-12. Supersedes the 2026-09-11 SPA transparent-carry mandate (closed at
the v1 architectural ceiling: runtime gaps transparent + dialect violations
rejected cleanly; darraghstudio SPA live in prod, region-switch + read-set +
hydration fixes shipped in sky v0.24.2/v0.24.3).

> actually perhaps design the auto tests feature for sky FIRST, then we can use
> that to run against DB?
>
> DS app
>
> we're aligned, please proceed completely unattended + autonomous + PIV

## The mandate

Build the Sky **auto-testing** feature per `docs/design/auto-testing.md`, driven by
the darraghstudio (DS) app as the real-world proof (the same way DS drives the
compiler work). The feature must let a Sky app be tested end-to-end **offline, in
CI, with no credentials** for the app's own effects, and catch the
silent-wrong-answer class we hit in prod (RPC read-set drops / Msg-arg collisions)
automatically.

Three pillars (design doc): (1) auto-derived `Msg`/`Model` generators from types;
(2) a deterministic test interpreter for Sky's OWN effects — ephemeral embedded
Postgres (migrate+seed+teardown per run), fixed clock, seeded Random/Uuid, test
Auth, in-process own-HTTP; (3) mock-by-default for EXTERNAL integrations derived
from the typed boundary (`.env.test` config + `.env.test.local` sandbox creds).

Two modes: (A) the **differential split fuzzer** — random reachable `(Model, Msg)`,
assert `update`-via-Sky.Spa-split == `update`-direct + no unclassified panic (the
FREE ORACLE — no hand-written oracle; catches read/write-set + Msg-arg-collision
regressions in CI); (B) **scenario e2e** — scripted `Msg` journeys + synthetic
inbound events (webhooks) + DB/Model assertions, mocked externals.

FLAGSHIP PIV: the DS checkout flow run OFFLINE end to end — ephemeral DB, mocked
Stripe `createSession`/`retrieveSession`, a signed synthetic
`checkout.session.completed` POSTed to `/webhooks/stripe` (real `handleWebhook`),
and the poll-based `RunFinalize` convergence proved idempotent (one order row
whichever of webhook/poll arrives first; redelivery a no-op). Local Postgres is up
on :5433 and DS test keys are in DS `.env`, so the scenario is runnable once the
harness exists.

## Phasing (design doc §Phasing)
1. Auto-derive `Msg`/`Model` generators from types (reuse `Codec.auto` type-walk /
   `xtask erasure-fuzz` value-gen).
2. **Differential split fuzzer** (mode A) — biggest bang, smallest surface (no
   effect mocking beyond identical stubs). Wire over examples + DS as a gate.
3. Deterministic effect-mock harness (ephemeral DB, fixed clock/RNG, in-process
   own-HTTP) — the enabler for mode B.
4. Scenario e2e (mode B), DS checkout/webhook/finalize as the first suite.

## Discipline (§0.3/§0.4)
Architecture-Consult FIRST (docs/rust-rewrite/ + docs/architecture/
sky-stdlib-correctness.md) → adversarial grill → implement per phase → fresh
Judge. Every feature/bug → a regression test first. No `Result String`; secrets
typed; root-cause only. Full release gate green before any merge/tag; user owns
tags (no auto-tag). Responses ASD-STE100 British English, plain punctuation.

The DIAGRAMS (`sky doc --diagram`, 4 kinds, done on feat/sky-doc-diagram) ship as
ONE next-release tooling stream WITH auto-testing — same typed Msg/Model/effect +
Sky.Spa read/write-set foundation (diagrams visualise the machine; the fuzzer
exercises it; `wire` read/write-sets are what the differential check verifies).

## Phase 2 CORRECTED oracle design (after §0.4 grill, 2026-09-12 — REVISE→PROCEED)
The naive "check every server branch; overlay identity away" oracle has false
POSITIVES and false NEGATIVES. Corrected (4 blocking fixes, priority order):
1. SEED identity identically, do not overlay it. Mint a signed `sky_sid` from the
   generated model's identity projection (reuse Auth.signToken / signedResponse_
   with the test secret) and feed BOTH legs, so `verifiedSession_` returns the
   same identity. Overlaying masks a real drop in an excluded field AND
   false-positives when an excluded field feeds a non-excluded write (DS
   RunLoadAdmin: session read → adminProducts write).
2. The DIRECT reference mirrors the split's SERVER semantics: run guard +
   withRequest + session-verify on BOTH legs; they differ ONLY in the read-set /
   write-set / Msg-arg plumbing. Reference = update on the WHOLE model (no read-set
   projection) with the same seeded identity + guard/withRequest; split = the real
   RPC round-trip. A guarded-DENY branch → same result both legs (no divergence).
3. TRANSITIVE forces-effect fence. Check only server branches whose update forces
   NO effect in a RUN position (Task.run / `let _ = task` in-body, propagated
   through callees). `inline_force` is arm-local + reason-only (spa_partition.rs
   :217,343,412) — NOT a gate; build the transitive analysis. `System.getenvOr` is
   EXEMPT (pure-typed String->String->String, deterministic both legs). Skip
   DB-reaching branches → phase 3.
4. SKIP chaining + pattern-2 client-result ROOT branches (field-excluding their
   writes makes the check vacuous).
FLAGSHIP scope: phase 2 covers the shipped bug class via `SetRegion` + the pure
recompute branches (IncQty/DecQty/RemoveFromBasket). `AddToBasket`/`KickCheckout`/
`RunFinalize`/`RunLoadAdmin` force DB reads → phase 3. genModel NOT withheld for DS
(all Model fields generatable). Falsifiers caught: drop a read/write-set field OR
remove the `spaMsgArg_` rename → `SetRegion`/basket diverge → gate red.

## Progress (2026-09-12)
- Phase 1 DONE (0bb49a6c): shared wire emit + spa_diff_gen value-generator emitter.
- Phase 2 DONE (through cf7d2e83): differential split fuzzer.
  - forces-effect fence (spa_partition: BranchVerdict.forces_effect, Graph
    fixpoint) — keys on the `Task_run` Ffi symbol (Task.run is stdlib Sky source,
    resolves Res::Def not Res::Kernel — the original inline_force arm never fired).
  - emit_ctor_app extracted (shared by backend + harness).
  - spa_diff_harness emitter + `sky spa-diff-fuzz` CLI (synthesises the Spa entry
    for Std.App apps) + HIR-backed MapTypeResolver.
  - Gate `spa-diff-fuzz` (Tier::T2) in the harness registry + bodies, falsifier
    `drop-msgarg-rename` PROVEN; scripts/spa-diff-fuzz.sh runner.
  - FLAGSHIP: darraghstudio fuzzes OFFLINE (no DB/creds) — fence selects the 4
    pure branches (SetRegion, DecQty, RemoveFromBasket, ClearBasket), 200 checks
    pass; the collision falsifier catches `SetRegion` (the shipped region-switch
    bug) with exit 1 while compiling clean.
  - JUDGE (fresh context, cf7d2e83): "PHASE 2 ACHIEVED + VERIFIED". Two
    non-disqualifying findings, both now CLOSED (4246a6de):
    * A. read/write-set field-drop was under-proven -> added the spa-diff-narrow
      fixture (narrow read/write sets) + a 2nd declared falsifier
      (drop-read-field). Both falsifiers PROVEN; gate 6/6.
    * B. falsifier machinery could leave a stale xtask binary (false-RED, never
      false-green) -> revert now bumps mtime, rebuilds, restores mtime; verified
      the gate self-heals immediately after --verify-falsifiers.
- Phase 3 (deterministic effect-mock harness) — STARTED. Full architecture at
  docs/design/auto-testing-phase3.md (Architecture-Consult PROCEED; 3a-3e).
  - 3a DONE (determinism kernels): Time/Random/Uuid gated on SKY_TEST_MODE
    (fixed+advanceable clock + seeded per-call stream), off by default. Go tests
    + full rt suite green (55.9s). runtime-go/rt/test_mode.go.
  - 3a tail DEFERRED (not flagship-blocking): Log capture needs a stdlib+kernel
    read API; test Auth needs no runtime change (secret is an argument).
  - NEXT: 3b ephemeral DB against local PG :5433 (testrunner creates + migrates +
    tears down, sets DATABASE_URL before spawn — mind the CAF-connect
    env-before-first-force point). Then 3c outbound-HTTP mock (skyHTTPClient
    RoundTripper — the Stripe seam), 3d in-process signed webhook, 3e temp
    embedded cluster.
- Phase 4 (DS Stripe scenario e2e) — FLAGSHIP WEBHOOK HALF PROVEN.
  darraghstudio/tests/CheckoutWebhookTest.sky (committed LOCAL to DS main, NOT
  pushed — a DS push deploys to prod): a signed synthetic
  checkout.session.completed -> REAL Payments.handleWebhook -> 1 order + both
  line items (200) -> redelivery idempotent (200, no 2nd row). Runs OFFLINE on
  the existing sky test runner against local PG :5433, no Stripe account/network.
  Wired into DS ci.yml beside OrderTxnTest (dummy DS_STRIPE_WEBHOOK_SECRET).
  Proves mode-B scenario testing works TODAY on existing Sky primitives — the
  effect-mock harness only reduces setup.
  - REMAINING Phase 4: the CLIENT convergence leg (RunFinalize/OrderFinalized).
    RunFinalize's Just branch is exactly Data.orderByStripe sid (the "poll finds
    the webhook's order" convergence — data half already proven by the webhook
    test); its model-mapping needs `update` importable, which DS Main does not
    expose -> needs the harness-synth entry / Server.inject RPC-in-process leg.
  - REMAINING for the full checkout flow: createSession/retrieveSession need the
    3c outbound-HTTP mock (skyHTTPClient RoundTripper); the webhook (crux) does
    not.
- 3c MOCK-BY-DEFAULT (outbound HTTP) — DONE. runtime-go/rt/test_http.go: in test
  mode the shared client's transport intercepts every outbound request; a
  declarative fixture (tests/mocks/*.json, matched by method+urlContains) serves
  it, anything unmatched FAILS CLOSED (auto error-mode coverage). No per-project
  mock CODE — a fixture is DATA. Prod unchanged (passthrough off test mode).
- 3a-DECOUPLE — DONE. SKY_TEST_MODE gates only the offline mock; determinism
  (fixed clock / seeded Random+Uuid) is opt-in via SKY_TEST_CLOCK_MS /
  SKY_TEST_SEED, so Data.newId() stays unique across runs (no broken DB tests).
- ACTIVATION — DONE. testrunner: a project with a `.env.test` runs `sky test` in
  test mode (sets SKY_TEST_MODE, loads .env.test + .env.test.local). Opt-in ->
  existing projects unaffected. PROVEN end-to-end (mockdemo: .env.test + a
  tests/mocks fixture -> sky test auto-mocks offline, unmocked fails closed, no
  manual env). This answers the user's "aren't mocks automatic?" — YES: zero test
  code, fixtures as data, auto failure-mode.
- REMAINING (friction reducers / completeness): 3b ephemeral-DB automation (so a
  scenario needs no live PG); client convergence RunFinalize (needs update-import
  / RPC-in-process leg); auto-DERIVED happy mock from a typed Codec boundary (the
  fuzzer-side auto-mock; DS Stripe hand-rolls its decoder so it needs a fixture);
  3d webhook helper (DRY the signed-request build); 3e temp embedded cluster;
  3a-tail Log capture.

## Not-done tail (carry, not blockers)
- Sky.Spa cache-busting headers (HTML `no-cache` + `immutable` hashed assets +
  hash `wasm_exec.js`) — queued Sky.Spa runtime patch (a separate fix).
- DS corrupt Chatterbus product image (4f8e2542, 103-byte) — needs admin re-upload
  (user).
- CI parallelized gate (merged main) validates on the next release.
