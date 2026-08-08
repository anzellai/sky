# Sky v1 Production-Readiness Roadmap

> **Goal**: make Sky the default tool for vibe-coding to production —
> AI-written code that runs without manual debugging, ships without
> manual deploy gymnastics, and survives real load.
>
> **Non-goal for v1**: `sky deploy`. Hosting platform competes with
> Sky's own potential offering; leave the closing-loop step for v1.1+.

This doc is the working plan. Each item has:

- **Why** — the user-facing problem (AI footgun, production blocker, etc.).
- **What** — the concrete surface to ship.
- **Acceptance** — how we know we're done (test fence, observable behaviour).
- **Risk** — what could derail it.

Tick items as they land. Promote a section header to "Shipped in vX.Y.Z"
when its acceptance criteria are all green.

---

## Phase 1 — Production trust (must ship before v1.0)

These are the items that turn "compiles cleanly" into "I'd trust this
with a real customer". Skipping any of them ships a v1 that can't
survive a public launch.

### 1.1a Telemetry primitives: `/healthz`, `/readyz`, `/_sky/metrics`, request-id, structured access logs

**Why.** Sky.Live apps go down today with no signal. AI-deployed apps
silently break and the user thinks Sky is broken. Without metrics you
can't prove anything else works in production. This is the foundation
for 1.1b (console) and a prerequisite for 1.2 (CSRF — observe it) and
1.3 (Std.Jobs — meter it).

**What.**
- `Sky.Http.Server` and `Sky.Live` mount four endpoints by default:
  - `GET /_sky/healthz` → 200 `{"status":"ok"}` (process alive).
  - `GET /_sky/readyz` → 200 when session-store + DB pool are warm,
    503 otherwise (drains gracefully on SIGTERM).
  - `GET /_sky/metrics` → Prometheus text exposition. Counters:
    `sky_live_requests_total{method,route,status}`,
    `sky_live_msg_total{name,outcome,noop}`,
    `sky_db_query_total{table,outcome}`,
    `sky_jobs_total{queue,outcome}`.
    Gauges: `sky_live_sessions_active`, `sky_db_pool_in_use`.
    Histograms: `sky_live_request_seconds`, `sky_db_query_seconds`,
    `sky_live_msg_seconds`.
  - `GET /_sky/buildinfo` → JSON with `{commit, builtAt, skyVersion}`.
- Every incoming request gets an `X-Request-Id` (honour client header
  if present, else generate UUID v7). Propagated to all downstream
  logs + `Cmd.perform` tasks + Db queries via implicit context.
- **Structured logging is state-change-based, not per-cycle**. A Msg
  dispatch that returns `(model, Cmd.none)` with no model hash change
  is metered but NOT logged. Kills 99% of `Tick` noise without losing
  traceability. See RFC for full rationale.
- New `Std.Live.lifecycle : msg -> msg` marker for explicit "this is a
  noisy heartbeat" classification (belt-and-braces on top of the diff
  filter).
- OpenTelemetry trace export honours `OTEL_EXPORTER_OTLP_ENDPOINT` env
  with `[observability] trace_sample_rate = 0.01` default (100% of
  errors regardless of sample rate).
- `[log] format = "json"` (already in sky.toml) gains canonical
  fields: `ts`, `level`, `msg`, `req_id`, `trace_id`, `span_id`,
  `route`, `latency_ms`, `status`, `error` (when level≥warn).
- Opt-out via `sky.toml [observability] enabled = false`.

**Storage tiers (the key design decision):**
- **Hot (default ON)**: in-memory ring buffers + Prometheus counters
  in-process. ~7 MB RAM steady-state. Zero disk writes.
- **Warm (opt-in)**: SEPARATE `_sky/telemetry.db` SQLite file — never
  the user's data DB. Batched 30s writes.
- **Cold (opt-in)**: OTLP export to external collector.

Full design + volume math + the "why not the user's SQLite by default"
analysis lives in [`v1-rfc/1-observability.md`](v1-rfc/1-observability.md).

**Acceptance.**
- `examples/09-live-counter` exposes `/_sky/healthz` and
  `/_sky/metrics` without code change; `curl /_sky/metrics` returns
  a non-empty Prometheus exposition that parses under the official
  `expfmt` parser.
- Playwright assertion in `scripts/verify-all-web.sh`: every Live
  app's `/_sky/healthz` responds 200.
- New Go test `runtime-go/rt/observability_test.go` covers all
  metric shapes + the diff-based logging filter (state-change → log,
  no-op → no log + counter bump).
- Docs page `docs/observability.md` with default field reference +
  Grafana dashboard JSON in `docs/dashboards/sky-live.json`.

**Risk.** Threading req-id through `Cmd.perform` requires kernel sig
change — additive only, but needs careful design. Covered in the RFC.

---

### 1.1b `/_sky/console` — pre-built monitoring dashboard

**Why.** Phoenix LiveDashboard is famously half of why teams choose
LiveView. Sky's equivalent is the "wow, I can SEE what my app is
doing" moment that converts skeptics. Without it, the Phase 1.1a
metrics are inert — users have to spin up Grafana to see anything.

**What.** A `/_sky/console` dashboard mounted into every Sky.Live
binary, **built as a Sky.Live app itself** (eats its own dogfood,
becomes a visible quality bar). Auth: requires `Std.Auth` admin role
in production; default-open in dev mode.

Tabs (in user-reach order):

| Tab | Content |
|---|---|
| Overview | req/sec, active sessions, p50/p99/p99.9 latency, error rate (5/15/60 min sparklines) |
| Live Sessions | Real-time list: who's connected, current page, last Msg, idle time |
| Msg Flow | Per-Msg counter + latency histogram, sorted by frequency. Click → last 50 dispatches with model diff |
| Routes | Per-route metrics + slow-route ranking |
| DB | Pool stats, slow queries, per-query latency, EXPLAIN button |
| Jobs | Queue depths, recent failures, dead-letter contents, retry button (depends on 1.3) |
| Logs | Tail of structured logs, filter by level / req_id / msg / user |
| Traces | Recent traces, click to expand spans waterfall |
| FFI | Count + latency per Go `pkg.func` |
| Errors | Ranked distinct errors + counts + most-recent stack trace |

Backing store: reads from 1.1a's Hot tier (in-memory). Production
exports flow Cold tier in parallel.

**Acceptance.**
- `examples/09-live-counter` shows the console at `/_sky/console`
  with seeded traffic; Playwright asserts all tabs render.
- `runtime-go/rt/console_*_test.go` per tab's data plumbing.
- Doc page `docs/console.md` with screenshots.

**Risk.** Scope creep — keep the dashboard minimal-but-complete.
Tabs that depend on later phases (Jobs needs 1.3) ship as
"placeholder when not configured" rather than blocking.

---

### 1.2 CSRF middleware default-on in Sky.Live

**Why.** SameSite cookies are not enough for a real app. An AI that
wires `Cmd.perform (Db.deleteAll dbConn) Deleted` doesn't think about
CSRF. Default-off is a footgun.

**What.**
- Every `POST /_sky/event` requires a session-bound CSRF token sent
  via an `X-Sky-Csrf` header. Token is generated at session creation,
  stored in the session, exposed to JS via the inlined liveJS so
  `__skySend` adds the header automatically — zero user code needed.
- API endpoints (`Live.api`, `Sky.Http.Server` POST/PUT/DELETE) get
  the same protection unless explicitly opted out via
  `Middleware.withoutCsrf` (escape hatch for webhook receivers).
- `sky.toml [security] csrf = false` available for explicit opt-out
  globally (DEFAULT: true).

**Acceptance.**
- New Go test `runtime-go/rt/csrf_test.go`: POST `/_sky/event`
  without `X-Sky-Csrf` → 403; with valid token → 200; with wrong
  session's token → 403.
- New `examples/14-task-demo` style spec: webhook endpoint declares
  `Middleware.withoutCsrf` and receives external POST cleanly.
- Playwright sanity: live-counter still works (the auto-attached
  header keeps the existing JS path transparent).

**Risk.** Changes the wire format. Need to bump the SSE handshake
to include the token. Backward compat: a missing header on a session
without a stored token (old binary, fresh client) is allowed once
per session (one-shot grace), then enforced.

---

### 1.3 `Std.Jobs` — promote `examples/18-job-queue` to a kernel module

**Why.** Every real app needs background jobs. AI shouldn't have to
hand-roll retry/dead-letter logic — they will get it wrong.

**What.**
- New stdlib module `Std.Jobs` exposing:
  - `enqueue : Queue -> Job a -> Task Error JobId`
  - `enqueueAt : Queue -> Time -> Job a -> Task Error JobId`
  - `enqueueIn : Queue -> Int -> Job a -> Task Error JobId` (ms)
  - `cancel : JobId -> Task Error ()`
  - `define : String -> (a -> Task Error ()) -> Job a` (declarative
    job definition; the runner discovers them at boot).
- Job runner ships as part of the binary; opt-in via sky.toml
  `[jobs] enabled = true` (default ON for any binary that imports
  `Std.Jobs`).
- Backends: in-memory (default, dev), SQLite (single-host prod),
  Postgres (multi-host). Same `[jobs] store = "..."` shape as
  `[live] store`.
- Defaults: exponential backoff (1s, 2s, 4s, 8s, 16s, 32s, 60s, …
  cap at 1h), max 10 attempts, then dead-letter table
  `_sky_jobs_dead` with full payload + error chain.
- Metrics fold into `/_sky/metrics`:
  `sky_jobs_total{queue,outcome=succeeded|failed|dlq}`,
  `sky_jobs_duration_seconds`, `sky_jobs_inflight`.
- Web UI at `/_sky/jobs` (gated by `Std.Auth` admin role) showing
  queue depth, recent failures, dead-letter contents, retry button.

**Acceptance.**
- New example `examples/25-jobs-demo` showing enqueue from Live
  handler, status polled from UI.
- Go tests `runtime-go/rt/jobs_*_test.go`: enqueue, retry, dlq,
  cancel; all three backends.
- Playwright assertion: `/_sky/jobs` admin UI renders with seeded
  data.
- Deprecation note in `examples/18-job-queue/README.md` pointing at
  `Std.Jobs`.

**Risk.** Persistence semantics (at-least-once vs exactly-once)
need careful docs. Idempotency keys not in v1 — leave for v1.1.

---

## Phase 2 — AI-quality moat (ships across v1.1–v1.3)

Each of these makes AI-written Sky better. They're not blockers for
v1.0 but compound the value-prop.

### 2.1 Type-directed lowering — SHIPPED in v0.15.0

**Why.** Lambdas, record-field inits, list elements, and call args
were all lowered as `func(any) any` / `any`, forcing reflect-backed
coercion at the boundary and leaving cosmetic
`sky-input="makeFuncStub"` artefacts in rendered HTML.

**What shipped (v0.15.0).**
1. Solver writes per-region type map (`globalRegionTypes`); `LowerCtx`
   threads the expected type down through `exprToGoExpectGo`.
2. Lambda bodies, record-field inits, list elements, and call args at
   typed slots lower with the slot's typed Go form propagated.
3. Go generics on parametric record aliases: `type alias Cfg msg = {
   onSubmit : msg, ... }` emits `type Cfg_R[T1 any] struct { OnSubmit
   T1; ... }` with per-instance type args.
4. Same-module polymorphic call re-instantiation — sibling refs
   alpha-rename per call site.
5. Wildcard-`any` soundness gate — same-mod CForeign requires at
   least one non-`any` freeVar.

**Acceptance — green.**
- 27/27 examples clean-build, 120/120 stdlib assertions, 306/306
  cabal specs, full web + cli verify sweeps.
- Architecture write-up:
  [`v1-rfc/type-soundness-deep-analysis.md`](v1-rfc/type-soundness-deep-analysis.md).

---

### 2.2 LSP code actions

**Why.** AI iterates faster when small fixes are one click. Cursor
relies on this for the "fix with AI" loop.

**What.**
- `textDocument/codeAction` returns:
  - **Add import**: when an unbound name matches a known stdlib
    symbol, offer `import Std.X exposing (foo)`.
  - **Did you mean**: typo-distance suggestions for misspelled
    identifiers (within Levenshtein 2).
  - **Extract let-binding**: select an expression → wrap in
    `let _newName = ... in` with rename-ready cursor.
  - **Organize imports**: alphabetise + group (Prelude → stdlib →
    user → FFI) with a single command.
  - **Inline let**: inverse of extract.
- All actions are pure refactors (no semantics change, idempotent).

**Acceptance.**
- New `test/Sky/Lsp/CodeActionSpec.hs` per action class.
- Headless Neovim driver in `scripts/lsp-test-nvim.lua` exercises
  each action end-to-end.

**Risk.** Refactors must NEVER change semantics. Need a property
test that round-trips through `sky check` before applying.

---

### 2.3 `sky doctor`

**Why.** AI doesn't know to delete `.skycache/`, kill port 8000,
re-run `sky install`. A single command that diagnoses common bad
states saves AI iteration loops.

**What.**
- `sky doctor` checks:
  - Stale `.skycache/` (newer than source modification time?).
  - Stale `sky-out/` (compiler version mismatch?).
  - Ports in use by previous runs (process holding `[live] port`).
  - Missing FFI deps (referenced in source but not in
    `.skycache/ffi/`).
  - mem-guard alive (for dev sessions).
  - `sky.toml` syntax + required sections present.
  - Embedded stdlib version matches compiler.
- Prints one line per issue + suggested fix. `--fix` auto-applies
  the suggestion (deletes stale caches, kills port holder).
- Exit 0 = clean, 1 = issues found, 2 = could not run.

**Acceptance.**
- `runtime-go/rt/doctor_test.go` per check class (seeded bad states
  → assert detection + fix).
- New CLI command landed in `app/Main.hs`.
- Docs page `docs/tooling/doctor.md`.

**Risk.** Low. Should be a quick win.

---

### 2.4 Time zones + Decimal type

**Why.** AI builds billing/scheduling apps. Float-money rounding
bugs and naive "add 1 month" are silent killers.

**What.**
- `Time` gains:
  - `Time.Zone` ADT (`UTC`, `Local`, `Named String`).
  - `Time.inZone : Zone -> Time -> ZonedTime`.
  - `ZonedTime.formatLocal : String -> ZonedTime -> String`
    (strftime-like).
  - `Time.addMonths`, `Time.addYears`, `Time.startOfDay`,
    `Time.endOfMonth` — calendar-aware, not millisecond-naive.
  - IANA zone database embedded via TH (~1 MB binary growth, OK).
- New `Std.Decimal` module:
  - `Decimal` opaque type backed by Go's `math/big`.
  - `Decimal.fromString`, `Decimal.fromInt`, `Decimal.toString`.
  - `Decimal.add`, `Decimal.sub`, `Decimal.mul`, `Decimal.div`,
    `Decimal.round` with `RoundingMode` ADT.
  - SQL adapter: `Db.getDecimal`, `Db.bindDecimal`.

**Acceptance.**
- Test fixtures covering DST transitions, leap years, end-of-month
  rollover.
- Decimal arithmetic property tests (commutative, associative,
  identity, no precision loss across `fromString`-`toString`
  round-trip).

**Risk.** Decimal needs FFI to `math/big` — bindings already
auto-generate via `sky add`, so should be straightforward.

---

### 2.5 Closed limitations sweep

**Why.** Each documented limitation is a per-item AI failure mode.
Closing them removes footguns.

Closed across v0.14.x → v0.15.0:

- ~~Zero-arity env reads cached at init time~~ — zero-arity bindings
  touching `System.getenv` / `loadEnv` now emit non-memoised.
- ~~`exposing (Type(..))` for user-module ADT constructors~~ —
  canonicaliser + LSP both follow.
- ~~`let` bindings forward references~~ — let blocks now promote to a
  single mutual-rec group.
- ~~Parametric record alias bugs (Surfaces 1, 2, 3)~~ — closed by
  v0.15 type-directed lowering + Go generics on parametric records.
- ~~Same-module polymorphic call pinned by first instantiation~~ —
  sibling refs to polymorphic annotated TypedDefs alpha-rename per
  call site.
- ~~`import X as Alias` leaks alias into codegen~~ — emits the source
  module name, not the alias.

Still active — see `docs/KNOWN_LIMITATIONS.md`:
- Dict.toList returns string keys for Dict Int v (deferred —
  workaround via `Dict.get` over known ranges).
- Kernel sigs for non-primitive returns (runtime port for Std.Html,
  Std.Attr, Std.Event, Std.Css opaque returns — deferred).

---

### 2.6 Better JSON / wire errors

**Why.** AI debugging API integrations needs path-aware errors.
"expected String, got Number" is useless; "expected String at
`.user.email[3]`, got Number" is fixable.

**What.**
- `Json.Decode` errors carry a `[PathSeg]` (`Field String`,
  `Index Int`); renderer pretty-prints to `.user.email[3]`.
- Same shape for `Db.queryDecode`, Sky.Live form-data decoder,
  Cmd.perform JSON return decode.

**Acceptance.**
- Existing tests updated to match new shape.
- New tests assert path rendering for nested error sites.

---

### 2.7 Std.Ui Playwright snapshot suite

**Why.** Just fixed `<textarea>` and Fill cascade — there are
more lurking bugs. Need automated visual regression per Std.Ui
primitive so future fixes don't break siblings.

**What.**
- New `scripts/snapshot-std-ui.mjs`: spins up a fixture-per-primitive
  server, takes Playwright DOM snapshots, diffs against
  `snapshots/std-ui/*.html`.
- CI integration: snapshot mismatches block merge.
- Update flow: `--update` flag regenerates snapshots after intentional
  changes.

**Acceptance.**
- Snapshot per `Ui.*` primitive (~50 primitives → ~50 snapshot
  files).
- CI step in `.github/workflows/ci.yml` runs the snapshot diff.

---

## Phase 3 — Ecosystem (ships across v1.4+)

### 3.1 Interactive playground at try.sky-lang.org

**Why.** "Try X in browser" is now table stakes for language
adoption. AI tools that can't be tried in <30s lose to those that
can.

**What.** Sky compiler compiled to WASM, hosted SPA editor with
preset examples, share-link state in URL hash. Use GHC-WASM via
`ghc-wasm-meta` if mature; else server-side compile with rate-limited
sandbox.

**Acceptance.** try.sky-lang.org loads under 3s, compiles
`hello-world` in <1s, shares state via URL.

**Risk.** GHC WASM is bleeding-edge. Server-side fallback adds
hosting cost.

---

### 3.2 Docs site auto-built from `docs/`

**Why.** AI scrapers and humans both want a web search target.
`docs/` lives in repo but isn't browseable.

**What.** Generate site from `docs/*.md` via mdBook or Docusaurus.
Auto-publish to `docs.sky-lang.org` via GitHub Actions on push.

**Acceptance.** docs.sky-lang.org reflects main within 5 min of
push. Includes stdlib API reference auto-generated from kernel
type signatures.

---

### 3.3 Starter templates: `sky new --template <name>`

**Why.** AI iteration is faster when the scaffolding is one
command. Common patterns shouldn't be reinvented.

**What.**
- `sky new --template saas` — auth + payments + admin dashboard.
- `sky new --template chat` — Sky.Live + presence + history.
- `sky new --template admin` — table grid + CRUD + filters.
- `sky new --template api` — REST + JSON + auth + rate limit.
- `sky new --template spa` — single-page no-DB demo.

**Acceptance.** Each template builds + runs + passes a
template-specific Playwright smoke test under `examples/templates/`.

---

## Phase 4 — Cross-cutting nice-to-haves (post-v1)

- Property-based testing in `Sky.Test` (Hedgehog-style).
- WebSocket support (`Sky.Live.WebSocket`).
- Streaming HTTP / file ops.
- `Sky.Live` snapshot tests (VNode-diff fixtures).
- Package registry (sky-registry.org) with semver enforcement.

---

## Process notes

- Each Phase 1 item needs a design doc under `docs/v1-rfc/N-name.md`
  BEFORE implementation. Land the design doc + agree the shape, then
  implement.
- After each item lands: re-run the full preflight (`scripts/preflight-tag.sh`),
  tag a patch release, update this doc's status.
- `docs/KNOWN_LIMITATIONS.md` is the authoritative open-bug list —
  keep this doc in sync.

---

## Status

| Phase | Item | Status |
|---|---|---|
| 1.1a | Telemetry primitives (incl. serverless detection + OTel) | **shipped** (v0.14.x) |
| 1.1b | `/_sky/console` dashboard (5-tab MVP — overview/metrics/logs/traces/errors) | **shipped** (v0.14.x) |
| 1.2 | CSRF default-on | **shipped** (v0.14.x) |
| 1.3 | Std.Jobs (memory backend + SQLite + Postgres) | **shipped** (v0.14.x) |
| 2.1 | Type-directed lowering (lambdas + record-field + list + call args) | **shipped** (v0.15.0) |
| 2.3 | `sky doctor` command | **shipped** (v0.14.x) |
| 2.4 | `Std.Decimal` + `Std.Money` + IANA time zones | **shipped** (v0.14.x) |
| 2.5 | Limitations sweep (incl. Lim #11, #14, #15, and Surfaces 1-3 in v0.15) | **shipped** |
| 2.6 | JSON path-aware decode errors | **shipped** (v0.14.x) |
| 2.2 | LSP code actions | deferred |
| 2.7 | Std.Ui Playwright snapshot suite | deferred (examples/26-ui-showcase covers visual-regression for now) |
| 3.1 | Playground | not started |
| 3.2 | Docs site | not started |
| 3.3 | Starter templates | not started |

**Phases 1 + most of 2 COMPLETE.** Remaining Phase 2 items (LSP code
actions, Std.Ui snapshot suite) are bigger-scope or lower-leverage —
deferred until the v1.x cadence settles.
