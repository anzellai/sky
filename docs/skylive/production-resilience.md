# Sky.Live production-resilience — hidden silent-failure class (2026-08-01)

> **Status: IN PROGRESS.** Root cause confirmed from a real production app;
> fundamental fixes under adversarial design. This tracks a class of Sky.Live
> bugs that pass `sky check` + `go build` + tests, look healthy in prod, then
> strand or silently degrade real users in ways that are very hard to debug.

## The meta-problem

Sky.Live prefers **"keep limping"** over **"self-heal"** or **"fail loud."**
Every instance below is invisible to the compiler and the test suite, and only
surfaces as a confusing production symptom.

## Confirmed instances (production evidence: a Sky.Live shop on Postgres)

### 1. Desync → strand (the "reconnect after 20-30m idle, refresh fixes it" bug)

- **Symptom:** user idle ~20-30 min, next interaction shows a reconnecting/
  disconnected banner; only a manual page refresh recovers.
- **Evidence:** prod reverse-proxy logs — of 103 `/_sky/event` 404s, **102 are
  "handler not found"** (session valid; `X-Sky-Live` NOT set), 1 is "session
  not found". Real browsers (desktop + mobile), current build, ongoing.
- **Mechanism:** client DOM handler IDs are `<sky-id>.<event>`, assigned by tree
  position (`assignSkyIDs`). They drift from the server's current handler map
  when the view changes between the client's render and now — a **deploy that
  changed the view code** (open tabs keep the old IDs), or an SSE drop leaving
  the DOM stale. Click → POST `/_sky/event` with a stale handler ID → server
  `live.go:~4368 http.Error(w, "handler not found", 404)` with **no X-Sky-Live**.
- **Why the client can't recover:** `__skySend` (live.go ~7269) treats a 404
  without `X-Sky-Live` as a "non-sky response" (proxy wedge) and stalls. The
  only auto-recovery (`__skyProbeSessionLost` ~8194) reloads **only** when the
  body is exactly `"session not found"` — it *explicitly ignores* "handler not
  found" (comment ~8208: "the session is fine... doesn't warrant a reload"),
  wrongly assuming only its own internal probe hits that path. Real clicks on a
  drifted DOM hit it too → **manual refresh required**.
- **Fundamental gap:** recovery is a string/header *whitelist*, not the
  invariant "a live client can always resync to the server's current view
  without a manual reload."

### 2. Store connect fails → silent memory fallback

- `chooseStore` (live_store.go ~1040): `store="postgres"` (or sqlite/redis)
  that fails to connect at boot logs one line and silently returns
  `newMemoryStore(ttl)`. Sessions become RAM-only → vanish on every restart →
  "sessions randomly die." Boot-ordering race (systemd `After=postgresql`
  waits for unit start, not connection-accept) can trip this on an otherwise-
  healthy host. Also `chooseStore ~1047`: a ttl that fails to reach the runtime
  silently defaults to **30 min**.

### 3. Memoised-CAF effect freeze

- `db = Task.run (Db.connect ())` is a zero-arg CAF, evaluated once + cached. A
  first-connect failure (boot race / transient blip) freezes the binding to
  `Err` for the whole process life → every query returns the cached Err →
  broken pages until a manual restart.

## Fundamental fix direction (to be finalized from the adversarial grill)

1. **Universal client resync invariant.** The server ALWAYS marks its desync
   responses; a *session-valid* desync (handler-not-found, patch-target-missing)
   → SOFT resync (reopen SSE → re-render → DOM + handler IDs refresh), no reload,
   no lost session; *session-gone* → hard reload. Possibly also: on a handler
   miss, the server re-renders and re-dispatches (or falls back to the existing
   `BuildAdtFromWire` direct-send path) so the click isn't even lost.
2. **Explicit store = fail-loud, never silent-degrade.** Retry-with-backoff at
   boot (ride out the DB-not-ready race), then FATAL in production / loud WARN +
   fallback in dev. Explicit opt-in for deliberate memory-in-prod.
3. **Self-healing DB handle.** A boot-race connect failure must self-heal on the
   next query rather than freezing to Err for the process lifetime.
4. **Health that doesn't lie** + any other landmines the completeness critic
   surfaces (readyz store probe, TTL sliding, model round-trip fidelity, …).

## Synthesized fix plan (from three adversarial grills, 2026-08-01)

**Meta-fix (highest leverage, both agents converged): health that doesn't lie.**
`RegisterReadinessProbe` (observability.go:83) has **zero production callers**, so
`/_sky/readyz` returns 200 even when the store fell back to memory. Wiring it
converts the ENTIRE silent class (store fallback, degraded broker, memory
console) from invisible → orchestrator-visible.

### Tier 1 — core fundamental fixes (confirmed prod bug + meta-landmine)
- **C1 (readiness probes).** Wire `RegisterReadinessProbe("session-store", …)` +
  `("db", …)`. Memory-fallback branches register a *degraded* probe. Add
  `Ping()` to `SessionStore` (durable impls ping; memory returns nil). Sites:
  observability.go:83, live_store.go call site live.go:3520.
- **A1 (universal resync invariant).** New `X-Sky-Status` header
  (`session-lost` → hard reload · `desync` → soft resync) authored at the 4
  server desync sites (handler-miss live.go:4368, session-not-found 4272+5808,
  unknown-Msg 4359). Client reads it FIRST via a *total* classifier; on
  handler-miss the server re-renders current view inline + returns it → DOM +
  handler IDs heal in one round-trip, no SSE churn, no banner, action dropped
  (documented). Fixes the darraghstudio disconnect.
- **B1 (store fail-loud).** `chooseStore` retry-with-backoff (ride the boot
  race), then FATAL in production / loud WARN+memory in dev, ONLY for explicit
  durable stores (postgres/sqlite/redis). Opt-in for memory-in-prod =
  `SKY_LIVE_STORE=memory`. live_store.go:1040,1055-1088.
- **B2 (self-healing Db handle).** Real cause: `Db_connect`'s eager `conn.Ping()`
  (db_auth.go:253) defeats `database/sql`'s self-healing lazy pool × `LazyCaf`
  caches the Err once = permanent outage. Fix: bounded-retry ping, then on
  exhaustion return `Ok(pool)` (healable) not `Err` — self-heals on next query,
  zero contract change. Same for analytics_store.go (S10).

### Tier 2 — session-lifecycle correctness (ship with Tier 1)
- **L2 sliding `sky_sid` cookie** (re-issue MaxAge each request; live.go:6022).
- **L3 SSE heartbeat touches `lastSeen` + `case <-sessDone: return`** in the SSE
  loop (live.go:5974) so an idle-but-connected session isn't evicted under a
  live connection.
- **L4 DB-store memCache-hit `Get` touches `lastSeen`** (live_store.go:454/601/759)
  to match memoryStore semantics.

### Tier 3 — tracked follow-ups (file now, fix in the next patch; no-deferral)
- **L5 CSRF** persistent cookie + `HMAC(key,sid)` token + recovery signal.
- **L6 multi-replica broker**: non-redis store on >1 replica → in-process broker
  silently drops cross-replica fan-out; require `SKY_LIVE_BROKER_URL` or warn.
- **L7 typed route params** (reject non-String ctor, or coerce, never reflect
  panic).
- **L8 recovered dispatch panic** → structured Error+errId+user signal, not a
  silent stderr drop (a deterministic panic = a permanently dead button).
- **L9 console sub-app** shares host store / explicit memory (no DB double-open).
- **L10 model round-trip fidelity** — register Model type graph from the static
  init type (an `any`-nil field never registers its future concrete type →
  session silently memory-only); content-derived (hashed) handler IDs so
  tree-order changes don't strand; `view()`-determinism lint.
- **#9 seq-gap** silent divergence — client carries last-applied-seq; server
  full-body-resyncs when the client is behind.

### False alarms (verified NOT live)
- TTL-not-reaching-runtime is already fixed (`parseTTL` reads env+toml, both
  duration+bare-seconds). The surviving TTL issue is L2 (cookie vs sliding TTL).
- Process-level panic isolation is sound (recovered per-session; residual is L8's
  *silent* drop, not a leak).
- `Dict` iteration is deterministic (`sortedDictKeys`); L10(b) only bites raw-map
  / clock / RNG reads in user `view()`.

## Verification bar

Each fix ships with a regression that is **red-on-bug** (reproduces the prod
symptom before the fix), plus the full milestone gates. The darraghstudio app
is the real-world e2e check. All fixes are pure-runtime (no compiler/stdlib
change) → apps get them by rebuilding with the new `sky`.
