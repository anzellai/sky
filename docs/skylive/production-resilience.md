# Sky.Live production-resilience — hidden silent-failure class (2026-08-01)

> **Status: Tier 1 + Tier 2 SHIPPED + verified live (2026-08-01).** Branch
> `feat/skylive-resilience` (pushed). All milestone gates green (runtime
> `go test ./rt/`, example-sweep 29/0, verify-cli 13/0, verify-all-web PASS).
> darraghstudio redeployed on the resilience runtime; the A1 fix is verified
> LIVE — a drifted-handler POST returns `200 + X-Sky-Status: desync` (soft
> resync) instead of the bare stranding `404 "handler not found"`.
> Tier 3 (L5-L10, seq-gap) remains tracked below. Not yet merged to main /
> tagged (awaiting user decision).
>
> Tracks a class of Sky.Live bugs that pass `sky check` + `go build` + tests,
> look healthy in prod, then strand or silently degrade real users in ways
> that are very hard to debug.

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

### ✅ Tier 1 — DONE (feat/skylive-resilience: ab13572a, ab9edabd, 7882f4d6)
All three shipped with red-on-bug regressions; full runtime `go test ./rt/` green.
Remaining before merge: milestone gates (cargo test + xtask + example sweep +
verify-all-web) + darraghstudio redeploy as the e2e check.

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

### ✅ Tier 2 — DONE (feat/skylive-resilience: 9da61633)
L2/L3/L4 shipped with the L2 regression; full runtime `go test ./rt/` green.

### Tier 2 — session-lifecycle correctness (ship with Tier 1)
- **L2 sliding `sky_sid` cookie** (re-issue MaxAge each request; live.go:6022).
- **L3 SSE heartbeat touches `lastSeen` + `case <-sessDone: return`** in the SSE
  loop (live.go:5974) so an idle-but-connected session isn't evicted under a
  live connection.
- **L4 DB-store memCache-hit `Get` touches `lastSeen`** (live_store.go:454/601/759)
  to match memoryStore semantics.

### ✅ Tier 3 — 5 of 7 DONE (feat/skylive-resilience-tier3)
Shipped with red-on-bug regressions: **L5** persistent+sliding CSRF cookie ·
**L6** multi-replica in-process-broker heads-up · **L7** typed route params
coerced (no reflect panic) · **L8** dispatch panic → structured Error+errId+user
notification · **L9** sub-app no longer inherits the host's durable store.

**Update (2026-08-01, post-grill):** all four deep items were adversarially
grilled (3 fresh-context agents). Outcomes:
- **L10b content-derived handler IDs — REJECTED as unsound** (grill proof): they
  collide by construction for identical-content payload-free siblings (10 `Delete`
  buttons → all hash equal → click deletes the wrong row, silently) AND break
  diff/patch targeting (sky-id is dual-purpose). Drift-resilience is already met
  by A1 (heal-on-drift is the correct architecture; prevent-drift is impossible
  for the payload case) and reorder-stability by `Ui.Keyed`. **Do not build.** The
  only sound refinement it surfaced: an arity-gated direct-send handler-miss
  fallback (payload-free clicks survive drift without a resync) — optional.
- **L10a — SHIPPED (runtime parts):** the real runtime gaps were (1) a
  panic-caches-success bug in gob registration (fixed: `tryGobRegisterVal`) and
  (2) a silent encode-drop (now `sky_live_session_encode_fail_total{store}`).
  "Register-on-encode" was already shipped; it was never the gap.
- **L10c — SHIPPED:** opt-in `view()`-determinism dev check
  (`SKY_LIVE_VIEW_DETERMINISM_CHECK=1`). Opt-in because the 2nd render doubles an
  impure view's side effects. Chosen over a compiler lint (catches raw-map-FFI
  nondeterminism a lint can't see).

**✅ BOTH remaining items now SHIPPED (feat/skylive-9-drop-resync → v0.19.7),
test-first + grilled + gated:**
- **#9 drop-keyed inline SSE resync** — DONE. sseConn.outOfSync (atomic) + cap-1
  resync chan; egress drop flags one conn, 5 ingress sites flag all; handleSSE
  resync case ships renderResyncFrame's full body direct to `w`. 7-test suite incl.
  a `-race` concurrency test; full rt package passes with `-race`.
- **L10a-codegen** — DONE. runtime `RegisterSkyGobTypes([]any)` + codegen emits
  the whole-binary type list (non-generic record + ADT structs, sorted). REPRO
  53/53 byte-stable, build-run 55/55, golden 24/24, coerce-floor 9250 unchanged.

Tier 3 is now COMPLETE: L5-L10 + #9 all resolved (L10b correctly rejected as
unsound). The Sky.Live silent-degrade/strand class is closed.

**Superseded "remaining (2)" text:**
- **#9 seq-gap — drop-keyed inline resync.** Grill correction: the drop source is
  **server-side SSE buffer overflow** (5 `recordSseDrop` sites: 4 ingress-full +
  1 egress-full in `fanOutFrame`), NOT network loss, and the server already
  detects every drop. The client-seq heuristic (original idea) is WRONG — it
  false-positives because `localSeq` is non-contiguous per connection. Design:
  add `outOfSync bool` + cap-1 `resync chan struct{}` to `sseConn`; change
  `sseConns map[uint64]sseConn` → `map[uint64]*sseConn` (touches
  registerSSEConn/unregisterSSEConn/fanOutFrame/hasSSEConnOtherThan); egress drop
  flags the one conn, ingress drop flags all; a new `case <-resync:` in the
  handleSSE select renders + writes a full-body resync DIRECTLY to `w` (bypassing
  the full buffer), reusing the reconnect-resync render (factor into
  `renderResyncFrame`); the fresh seq > buffered stale frames so the client's
  seq-guard orders it. No wire/client change, zero false-positives, composes with
  A1. Risk: concurrent SSE fan-out — needs `sseConnMu`-as-leaf-lock discipline +
  focused race testing. Bounded by A1 today.
- **L10a-codegen — compiler-emitted exhaustive gob registration.** The deep
  L10a defect is decode-side blindness to `any`-typed Model fields ACROSS
  processes (gob's name→type registry is process-local; after a restart process B
  never `gob.Register`ed the concrete type that only lived in an `any` field →
  decode fails → session lost). Register-on-encode can't fix it (encoder isn't
  the blind one). Fix: emit `rt.RegisterSkyGobTypes([]any{ State_Model_R{}, … })`
  in generated `main.go` listing zero-values of every record-alias struct + ADT
  ctor (crates/codegen), + a thin `rt` entry that walks each under `gobRegMu`.
  Complication: parametric records (`Foo_R[T]`) can't be zero-valued without type
  args — emit only monomorphised/concrete instantiations. Needs the full corpus
  gates.

**Superseded original text (2 deep items):**
- **#9 seq-gap.** Needs the client to send its `__skyLastAppliedSeq` (it currently
  sends only its request counter `__skyClientSeq`) AND per-connection
  last-delivered-seq tracking on the server so a "client behind → full-body
  resync" check doesn't over-trigger full renders on the hot path (an in-flight
  SSE frame would look like a gap). A naive `clientSeq < serverSeq → full-body`
  is correct but a performance regression on chatty apps. Design + benchmark
  before landing. Impact today: bounded — A1 soft-resyncs on the next detectable
  miss.
- **L10 model-fidelity + content-derived handler IDs + view-determinism lint.**
  (a) register-on-encode so an `any`-typed Model field that was nil at init but
  later holds a concrete value doesn't fail `encodeSession` → silent memory-only;
  (b) **content-derived (hashed) handler IDs** replacing position-based
  `<sky-id>.<event>` so a view-code change doesn't invalidate open clients' IDs
  at all — a core dispatch-identity redesign (touches assignSkyIDs + the diff +
  the handler map), benchmark + broad regression required; (c) a compiler
  `view()`-determinism lint (Rust-side). Impact today: bounded — A1 self-heals
  the drift these would prevent.

### Tier 3 — original catalogue (for reference)
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
