# Sky auto-testing — Phase 3: the deterministic effect-mock harness

> Status: DESIGN (Architecture-Consult PROCEED, 2026-09-12). The enabler for
> mode B (scenario e2e), whose flagship is the darraghstudio (DS) Stripe
> checkout -> webhook -> finalize flow run OFFLINE. Phase 2 (the differential
> split fuzzer, mode A) is DONE + Judge-verified. See `docs/design/auto-testing.md`.

## Headline: there is NO runtime effect-dispatch table to swap

`Ffi.kernel "X"` is a build-time sentinel — `rt.Ffi_kernel` panics if reached at
runtime (`runtime-go/rt/rt.go:4295`). Stage-4 rewrites every `Ffi.kernel "Name"`
call-site to a `Can.VarKernel` lowering to a DIRECT Go symbol via a static
compile-time table (`rust/crates/lower/src/kernel.rs:99` `table()`). So "swap the
interpreter" is not a registry swap — the seam is INSIDE each Go kernel, keyed on
a runtime-global test flag. The determinism-relevant kernels are few and funnel
through two shared helpers (`skyGetenv` for the DB DSN, `skyHTTPClient` for
outbound HTTP), so the surface is small and mostly closeable.

## Test-mode activation

The existing `sky test` runner (`rust/crates/testrunner/src/lib.rs:52` `run_test`)
synthesises `main = Test.runMain Suite.tests`, builds, and SPAWNS the compiled
binary with inherited stdio (`:175`). The runner sets env (`SKY_TEST_MODE`, seed,
fixed-clock ms, `DATABASE_URL`) BEFORE spawn — same app code, swapped interpreter,
app never knows. Matches the `ENV`/`SKY_*` convention. Env-before-`main` is also
REQUIRED by the CAF connect footgun: `db = Task.run (Db.connect …)` memoises the
pool handle (`lower.rs:919-923`), so the DSN must be set before the first DB force.

## The seams (file:line)

- Determinism kernels: `Time_now` rt.go:7025, `Time_unixMillis` rt.go:7317,
  `Random_int` rt.go:7411 (note `Random_seededInt` rt.go:7582 already seedable),
  `Uuid_v4`/`v7` validate.go:151/164. Gate each on a package-global test
  clock/seed set when `SKY_TEST_MODE`.
- DB: `Db_connect` DSN resolution db_auth.go:244-249 (`DB_PATH` then
  `DATABASE_URL`); migrate `Db_renderMigrations` db_migrate_ops.go:164 +
  `Db_migrateApply` db_auth.go:1584; embedded lifecycle pg_embed.go:95/113;
  temp-dir REFUSAL pg_embed.go:295 (blocks `/tmp`,`/var/folders` — why `--embed`
  cannot be reused for an ephemeral temp cluster as-is).
- In-process own-HTTP: `dispatchSkyHandler(w, req, handler, paramNames)`
  rt_server.go:52 (drivable with an `httptest` recorder); Sky-level
  `Handler = Request -> Task Error Response` + constructible `Request` record
  (sky-stdlib/Sky/Http/Server.sky:87,54).
- Outbound-HTTP mock (external integrations): `skyHTTPClient` `sync.OnceValue`
  package var stdlib_http_server.go:30 — install a mock `http.RoundTripper`
  routing e.g. `api.stripe.com` to canned typed responses. ONE seam covers all
  `Http.*` externals. (DS Stripe is outbound HTTP, `Payments.sky:35` apiBase, NOT
  a Go-FFI boundary — corrects design pillar 3's framing.)
- Webhook (pure Sky, zero runtime work): DS `Payments.handleWebhook`
  (Payments.sky:297) + `verifySignature` = `Crypto.hmacSha256 secret (ts++"."++body)`
  (:541). A Sky test signs a synthetic `checkout.session.completed` with the test
  secret via the same `Crypto.hmacSha256` kernel, builds a `Request`, calls the
  real handler.
- `.env.test` / `.env.test.local`: dotenv.go:109/143 + `System_loadEnv` rt.go:7298;
  runner loads `.env.test` then `.env.test.local` (override) before spawn.

## Phased plan (smallest-surface-first, each shippable + verifiable)

- **3a — determinism kernels + Log capture + test Auth secret.** Global
  clock/seed inside Time/Random/Uuid gated on `SKY_TEST_MODE`; captured Log;
  test-secret Auth. No DB. Verify: a Sky test asserts `Time.now` == fixed ms and
  the UUID stream is byte-identical across two runs. Clock ADVANCEABLE (not just
  fixed); seed advances PER CALL-SITE (not frozen per CAF).
- **3b — DB against external PG (:5433).** Runner creates a throwaway database,
  migrates via existing kernels, sets `DATABASE_URL` before spawn, drops on
  teardown. Verify: write+read a row; teardown leaves no residue.
- **3c — outbound-HTTP mock.** RoundTripper swap on `skyHTTPClient` keyed by host;
  fixtures from `.env.test`/`tests/mocks/`, decoder-derived defaults. Verify: DS
  `createSession` returns a canned session with no network.
- **3d — in-process webhook (Sky-level).** Request builder + `Crypto`-signed
  synthetic event -> `handleWebhook`, routed through `dispatchSkyHandler` +
  `httptest` so real request parsing runs. Verify: a signed event builds one
  order row; a redelivery is a no-op.
- **3e — embedded ephemeral cluster.** A temp-permitting test mode DISTINCT from
  `--embed` (strictly gated on `SKY_TEST_MODE`), replacing 3b's external-PG
  dependency so CI needs no PG. Verify: full run offline with no preset
  `DATABASE_URL`.
- **(Phase 4 tail):** RPC-handler in-process (`RunFinalize` server leg via a
  harness-synth backend import, or a `Server.inject` helper) + race/idempotency +
  the scenario DSL.

## Hazards (guard against)

- G1 false NEGATIVE: a mock that always returns `Ok` hides a contract break.
  Mitigate: derive the mock through the app's OWN typed decoder (DS
  `sessionDecoder` Payments.sky:162) so a shape drift still fails; keep the
  opt-in contract-drift tier (`.env.test.local` sandbox); fuzz Err/timeout/5xx.
- G1: a test seed frozen per-CAF makes two "random" ids equal -> seed must
  advance per call-site.
- G2 false POSITIVE: a merely-frozen clock fires TTL/expiry branches wrongly ->
  clock must be advanceable.
- G2: a hand-built `Request` that skips `dispatchSkyHandler`'s body-limit
  (rt_server.go:78) / form-parse (:108) / header canonicalisation hides real
  bugs -> route the webhook through `dispatchSkyHandler` + `httptest`.
- Isolation: the temp-permitting DB test mode must be gated STRICTLY on
  `SKY_TEST_MODE`, never on `--embed` (pg_embed.go:295 exists to stop a prod
  `--embed` app being steered at a temp data root).

## Verdict

PROCEED. No floor blocker needing user auth — every seam is runtime-owned; the DB
is the user's already-running local PG (:5433) for 3a-3b and the shipped embedded
bundle for 3e; no credentials, no network.
