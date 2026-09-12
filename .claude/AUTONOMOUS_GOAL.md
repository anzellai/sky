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

## Not-done tail (carry, not blockers)
- Sky.Spa cache-busting headers (HTML `no-cache` + `immutable` hashed assets +
  hash `wasm_exec.js`) — queued Sky.Spa runtime patch (a separate fix).
- DS corrupt Chatterbus product image (4f8e2542, 103-byte) — needs admin re-upload
  (user).
- CI parallelized gate (merged main) validates on the next release.
