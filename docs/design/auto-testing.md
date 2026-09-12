# Sky auto-testing — design (draft)

> Status: DESIGN, driven by the darraghstudio (DS) app as the real-world proof,
> the same way DS drives the compiler work. Nothing here is built yet. Next-release
> stream, alongside `sky doc --diagram`.

## Why

Two production bugs this cycle were **silent wrong answers**, not crashes: the
Sky.Spa RPC read-set dropped `basket` (shipping computed on an empty basket), and
a Msg-arg/Model-field name collision dropped the new region (switching did
nothing). Both type-checked, built, and deployed. Only **manual testing on DS in
prod** found them. We need a test harness that catches this class in CI.

The insight that makes it cheap: a Sky app is a pure state machine
(`update : Msg -> Model -> (Model, Cmd)`, `Msg` a finite typed ADT, `Model` a
typed record), and the Sky.Spa split gives us a **free oracle** — run a branch two
ways and assert they agree.

## The three pillars

### 1. Auto-derived generators (no hand-written fuzzers)

The compiler already walks type structure for `Std.Codec.auto` and the erasure
fuzzer (`xtask erasure-fuzz`, `WellTypedFuzzerSpec`). Reuse that to derive
generators for a project's `Msg` and `Model` from their types. No per-app
boilerplate; the vocabulary of actions IS the `Msg` ADT.

### 2. A deterministic test interpreter for Sky's OWN effects (the enabler)

Sky owns its effect boundary — `Db`, `Http`(to own backend), `Time`, `Random`,
`Uuid`, `Auth`, `File`, `Log` all go through typed kernels. In **test mode** the
runtime supplies a deterministic interpreter for them, so a test needs no config
for Sky's own effects:

- `Db` → an **ephemeral embedded Postgres** (Sky already ships the bundle) or
  sqlite: created, migrated (`sky db migrate`), seeded, and torn down per test
  run. Real engine parity, no external DB, deterministic.
- `Time` → a fixed, advanceable clock. `Random`/`Uuid` → a seeded stream.
- `Auth` → test tokens minted with a test secret.
- `Log` → captured for assertions.
- `Http` to the app's OWN backend → in-process (no socket), so a client-leg test
  drives the real RPC handlers.

The app under test does not know it is in test mode — same code, swapped
interpreter. This is the whole reason the common case (an app with a DB + auth +
a little HTTP) is fully runnable in CI with **no credentials**.

### 3. Mock-by-default for EXTERNAL integrations (contract-first)

Third parties (Stripe, Resend, Slack) cross a **typed** boundary — a Go-FFI call
returning `Result Error a`, or an `Http` call whose response decodes through a
typed `Std.Codec`. So the mock is not boilerplate:

- **Derive a default mock from the type** (the `Codec.auto` type-walk), returning
  well-typed fixtures until real creds exist. "No integration configured" ⇒ "use
  the derived mock", automatically.
- **Fuzz the failure modes**: inject `Err`, timeouts, 5xx, malformed responses on
  demand — better coverage than a happy-path sandbox, and only possible because
  errors are typed.
- **Swap with zero code change**: mock → `.env.test.local` sandbox → prod is an
  env change, never a code change (the seam is the same typed interface).
- A mock proves the app handles the **declared** contract, not that the real
  service still honours it, so the real integration shrinks to an occasional
  **contract-drift** check with sandbox creds in a gitignored `.env.test.local`.
  Daily dev + CI stay fully mocked and offline — which **decouples dev progress
  from devops/business provisioning**.

Config: `.env.test` (committed, non-secret config + mock toggles) +
`.env.test.local` (gitignored, sandbox creds). Mirrors `.env`/`.env.local`.

## The two test modes

### A. Property / differential (the compiler + app soundness net)

For random **reachable** `(Model, Msg)` (drive via `Msg` from `init`, not random
Models), assert:

- **`update`-via-split == `update`-direct.** Run the branch directly (the
  Sky.Live/reference semantics, whole model in scope) and through the Sky.Spa
  split (build the request from the read-set → reconstruct the server model →
  apply the write-set delta). A dropped read/write or a Msg-arg collision makes
  the two diverge — the exact bugs above, caught with **no hand-written oracle**.
  Effects are stubbed to the SAME deterministic value on both paths, so divergence
  comes only from the read/write-set plumbing.
- **No unclassified panic** from well-typed input.
- Any user-declared invariants.

Failures **shrink** to a minimal `Msg` sequence. Run over the examples + DS in CI
(a T2/nightly gate) so read/write-set regressions are caught in CI, not prod.

### B. Scenario / e2e (the DS flows, end to end, offline)

A scripted sequence of `Msg`s (a user journey — the same view→`Msg` extraction the
`journey` diagram uses) plus **inbound events** (webhooks), with assertions on the
resulting Model and DB, everything mocked. This is where the DS Stripe flow lives.

Inbound events: the harness can POST a synthetic event to an `App.api` route (e.g.
`POST /webhooks/stripe`) — with a helper that computes a valid signature from the
test secret (DS's `verifySignature` is `Crypto.hmacSha256 secret (t "." body)`),
so no test-mode bypass is needed and the real handler path runs.

## Worked example — DS checkout: pay → webhook → finalize (the flagship)

DS's stateless SPA has no server push; on `CheckoutCompletePage sid` it subscribes
to `Sub.every 350 (RunFinalize sid)` and **polls**, and the webhook +
`RunFinalize` both build the same order **idempotently** (unique index on
`payment_intent`/`stripe_session`). The harness runs the whole thing offline:

1. Ephemeral DB up, migrated, seeded with products.
2. Drive `AddToBasket` … then `KickCheckout` — **Stripe `createSession` mocked** to
   return a canned session id + client secret (no Stripe call).
3. Persist the pending cart (the real `Data.saveCart`, real DB).
4. **Inbound webhook**: POST a signed synthetic `checkout.session.completed` to
   `/webhooks/stripe` → real `Payments.handleWebhook` → order built from the event
   + pending cart. Assert the order + line items in the DB.
5. **Client reaction**: fire `RunFinalize sid` with **Stripe `retrieveSession`
   mocked** to report paid → assert it converges (idempotent: finds the order the
   webhook made) and the Model shows the confirmation, `finalizeFailed = False`.
6. **Race + idempotency**: run steps 4 and 5 in both orders and assert exactly one
   order row either way; assert a redelivered webhook is a no-op.

This answers "if the user pays and the webhook arrives, how does the SPA react?"
mechanically and repeatably, with no Stripe account and no network.

## CLI surface (sketch)

- `sky test --fuzz [--iters N] [--seed S]` — mode A over the project.
- `sky test tests/CheckoutScenario.sky` — mode B (a scenario suite; extends the
  existing `Sky.Test` runner). Test mode wires the deterministic interpreter +
  derived/declared mocks + the ephemeral DB automatically.
- Mocks declared in `.env.test` / a `tests/mocks/` fixture dir; sandbox creds in
  `.env.test.local` for the opt-in contract-drift tier.

## Phasing

1. Auto-derive `Msg`/`Model` generators from types.
2. **Differential split fuzzer** (mode A) — biggest bang, smallest surface (no
   effect mocking beyond identical stubs). Wire over examples + DS as a gate.
3. The **deterministic effect-mock harness** (ephemeral DB, fixed clock/RNG,
   in-process own-HTTP) — the enabler for mode B.
4. **Scenario e2e** (mode B) on top, with the DS checkout/webhook/finalize flow as
   the first suite.

## Relationship to the diagrams

Same foundation — the typed `Msg`/`Model`/effect boundary + the Sky.Spa
read/write-sets. `sky doc --diagram` **visualises** the state machine (pages,
actions, the `wire` contract); the fuzzer **exercises** it; the `wire` read/write
sets are literally what the differential fuzzer checks. They ship as one
next-release tooling stream.
