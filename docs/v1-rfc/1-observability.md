# RFC 1 — Observability: telemetry primitives + console dashboard

> Status: **DRAFT** · Branch: `feat/v1-roadmap` · Phase: 1.1a + 1.1b ·
> Author kickoff: 2026-05-17 · Land before: any Phase 1.2 / 1.3 work
> (CSRF needs to be observed; jobs need to be metered).
>
> Supersedes nothing. Foundation for all Phase 1.

## Background and motivation

Sky.Live apps go down today with no signal. The compiler catches most
bugs, but production failures (a slow DB query, a session store
hiccup, a Cmd.perform task that times out, a Msg dispatch that
panics inside user code's `update`) leave the user staring at a blank
browser tab with no way to find out what broke.

This is the single biggest gap between "Sky compiles cleanly" and "I
trust Sky with a real customer". It's especially acute for the
AI-vibe-coding use case the project is targeting next: AI-written
code that hits a runtime problem in production needs *visible*
failure modes, not silent ones.

Phase 1.1 closes this gap with two pieces:

- **1.1a — telemetry primitives**: the data-collection layer.
  Endpoints, metrics, structured logs, traces, request-id
  propagation. The plumbing.
- **1.1b — `/_sky/console`**: a pre-built monitoring dashboard,
  itself a Sky.Live app. The visible win that converts skeptics —
  the "wow, I can SEE what my app is doing" moment Phoenix
  LiveDashboard delivered for LiveView.

Together they ship as the v1.0 prerequisite. Without them, no
production user will trust Sky enough to ship a paying customer's
app on it.

## Goals

1. **Observability defaults are ON, with zero configuration**. AI-
   written code gets metrics, healthz, structured logs, and a
   dashboard out of the box. The opt-out path exists but is rarely
   needed.
2. **No write amplification on the user's database.** Telemetry never
   competes with `users` / `orders` / `pages` for SQLite write
   throughput. Hot tier is in-memory; warm tier uses a *separate*
   SQLite file; cold tier ships externally.
3. **Tick-style heartbeat Msgs don't flood logs**. A 100ms `Tick`
   subscription that returns `(model, Cmd.none)` produces a metric
   bump but zero log lines.
4. **AI-debuggable**: when something breaks, the log line + dashboard
   tab show *what Msg caused it*, *which req_id*, *what trace
   spans were active*. AI doesn't read logs naturally, but a glance
   at the console tells a human what to feed back to the AI.
5. **Industry-standard wire formats**: Prometheus text exposition,
   OpenTelemetry OTLP for traces, W3C `traceparent` for propagation.
   Compatible with every observability vendor.

## Non-goals (deferred)

- **Built-in alerting / paging**. Users wire alerts via their
  Prometheus + Alertmanager / Grafana setup.
- **Long-term metrics storage**. Hot tier is ~10-30 min; long-term
  graphs need Prometheus push or scrape.
- **User-defined custom metrics** (post-v1). The kernel metrics are
  enough for v1. `Std.Metrics.counter` API can land in v1.x once
  the kernel set is stable.
- **Per-user RUM (Real User Monitoring) in the browser**. The
  client-side runs `__sky*` plumbing; injecting browser-side perf
  collection is v1.x.
- **APM-style code-level profiling**. Sky's Go runtime already exposes
  `/debug/pprof` to the curious; we don't replicate it.

---

## Design — Storage tiers

The single most important design decision: **don't write telemetry
into the user's SQLite database, ever, by default**. Telemetry is a
high-volume write workload; the user's data is the application's
durability concern. Mixing them is always wrong.

Three tiers, each opt-in beyond the default:

| Tier | Default | Storage | Retention | Use case |
|---|---|---|---|---|
| **Hot** | **ON** | In-memory ring buffers + Prometheus counters in-process | ~10-30 min | `/_sky/console` dashboard, dev debugging |
| **Warm** | OFF | Separate SQLite file `_sky/telemetry.db` (NEVER the user's DB) | hours-days | local forensics, single-host prod |
| **Cold** | OFF | OTLP export to external collector | indefinite | multi-host prod, long-term retention |

### Volume math (justifies the design)

Representative small-medium Sky.Live app at **100 req/sec**:

- HTTP request lines: ~100/sec
- Msg-dispatch lines (state-change-filtered): ~50/sec (~half of all
  dispatches mutate state in typical apps)
- Trace spans at 1% sample: ~10/sec
- Counter / gauge increments: in-memory only, no writes

Total ~150 log writes/sec, ~30 KB/sec, **~2.6 GB/day** if persisted.

SQLite WAL mode handles ~10k writes/sec in theory; per-row inserts
collapse to ~500/sec because of transaction overhead. Putting 2.6
GB/day on the same SQLite file that holds `users` / `orders`:
- Lock contention with user queries
- Disk fills silently
- VACUUM pauses degrade p99 latency
- Backup bloat (a 7-day rolling backup of a 20 GB telemetry-polluted
  DB is materially slower)
- WAL checkpoint stalls under load

### Hot tier (the default)

Pure in-process ring buffers + counter map:

| Buffer | Capacity | Bytes | RAM |
|---|---|---|---|
| Log lines | 10,000 | ~200 each | ~2 MB |
| Trace spans | 1,000 | ~5 KB each | ~5 MB |
| Prometheus counters / gauges / histograms | ~50 metric x ~50 label combos x 16 B | — | ~50 KB |
| **Total steady-state** | | | **~7 MB** |

At 100 req/sec the log buffer refreshes every ~7 minutes; trace
buffer every ~16 minutes. Sufficient for live debugging on the
dashboard.

Backing struct (Go side):
```go
type telemetryStore struct {
    mu       sync.RWMutex
    logs     *ring.Ring[LogEntry]      // 10k cap
    traces   *ring.Ring[TraceEntry]    // 1k cap
    counters map[counterKey]float64
    gauges   map[counterKey]float64
    hists    map[counterKey]*histogram // bucketed
}
```

Lock-free reads via atomic snapshot pattern; writes serialise on
`mu`. Profiled cost: <100 ns per log append, <50 ns per counter bump
under contention.

### Warm tier (opt-in, separate file)

```toml
[observability]
persist = "sqlite:./_sky/telemetry.db"   # MUST be a different file from [database] path
persist_retention = "7d"                  # rolling delete; default 7 days
persist_flush_interval = "30s"            # batched writes
```

Schema (fixed, no ORM):
```sql
CREATE TABLE logs (
    ts INTEGER PRIMARY KEY,   -- nanoseconds since epoch
    level TEXT,               -- "info" / "warn" / "error" / "debug"
    msg TEXT,
    req_id TEXT,
    trace_id TEXT,
    fields_json TEXT          -- extra attrs as JSON blob
);
CREATE INDEX logs_by_req ON logs(req_id);
CREATE INDEX logs_by_level_ts ON logs(level, ts);

CREATE TABLE metric_snapshots (
    ts INTEGER PRIMARY KEY,
    blob BLOB                 -- Prometheus exposition gzipped
);

CREATE TABLE traces (
    trace_id TEXT,
    span_id TEXT,
    parent_id TEXT,
    name TEXT,
    started_ns INTEGER,
    duration_ns INTEGER,
    attrs_json TEXT,
    PRIMARY KEY (trace_id, span_id)
);
```

Background goroutine batches `INSERT`s every 30s, runs `DELETE
WHERE ts < now() - retention` nightly, `VACUUM` weekly. Compression:
log fields stored as gzipped JSON when >1 KB.

For 100 req/sec, this lands ~70 KB/min of compressed log writes,
~100 MB/day. 7-day retention = ~700 MB. Manageable.

The `_sky/` directory is added to `.gitignore` automatically by
`sky init` (already on the list).

### Cold tier (opt-in, OTLP)

```toml
[observability]
otlp_endpoint = "https://otel.honeycomb.io"
otlp_headers = { "x-honeycomb-team" = "${HONEYCOMB_KEY}" }
```

Standard OpenTelemetry OTLP/HTTP shipper. Logs use the OTel Log
Data Model; metrics use OTel Metric Data Model; traces use OTLP
Trace Data Model. Compatible with every collector — Jaeger, Tempo,
Honeycomb, Datadog, AWS X-Ray, Google Cloud Trace, etc.

When OTLP is configured:
- Hot tier still runs (dashboard needs it)
- Warm tier still runs if configured (local forensics survive
  collector outages)
- ALL signals additionally ship to OTLP via the standard exporter

OTLP exporter uses exponential backoff + retry on transient failures
(network blips, collector restarts). On persistent failure (10+ min
of 4xx/5xx), drops to backpressure mode — keeps Hot/Warm tiers
running, logs the export failure once per minute to stderr (not into
its own buffers — would loop).

---

## Design — Tick-noise handling

The textbook hard problem with TEA-style architectures: a 100ms
`Tick` subscription generates 36,000 Msg dispatches/hour. Naïve
"log every Msg" approach floods logs uselessly.

**The rule: log when state changes, meter always.**

### Diff-based logging

A Msg dispatch produces a log line ONLY when:
1. `hash(new_model) /= hash(old_model)`, **OR**
2. The update returned a non-`Cmd.none` command, **OR**
3. The dispatch failed (panic, type error in update body, guard
   rejection).

Otherwise: increment counters + histograms, no log line.

Why this works for Tick:
- Tick that just resyncs from DB and finds nothing new → `(model,
  Cmd.none)` → no log + `sky_live_msg_total{name="Tick",noop=true}`
  counter bump.
- Tick that fetches a new value and updates model → log line + same
  counter bump.
- Tick that fires a Cmd.perform → log line (Cmd is non-none).
- Tick that errors → log line at level=error.

The 99% of useful Ticks (no state change) are invisible in logs but
visible in metrics. The 1% that do something interesting are fully
visible.

### Hashing model state

`hash(model)` is challenging in Go because `model` is `any`. Approach:
- For typed-codegen records (`Foo_R`): structural hash via reflect
  walk (one-time cost amortised across dispatches; ~100 ns for
  a small model).
- For ADTs: tag + field hashes.
- For maps/lists: sorted-element hash for determinism.

Cost budget: ~200 ns per dispatch for hash. At 100 dispatches/sec
that's 20 μs/sec of CPU — negligible. Cached per-dispatch (computed
once before and once after).

Optimisation: skip the hash entirely when the dispatch is a known
mutating Msg (e.g. user click) — log unconditionally. The hash is
only consulted for "this might be a no-op" Msgs.

### `Std.Live.lifecycle` — explicit escape hatch

For Msgs the developer KNOWS are noisy heartbeats and where they
want to skip even the metric bump:

```elm
import Std.Live exposing (lifecycle)

type Msg = ... | Tick | Heartbeat | ...

subscriptions model =
    Sub.batch
        [ Sub.every 100 (lifecycle Tick)
        , Sub.every 5000 (lifecycle Heartbeat)
        ]
```

`lifecycle msg` tags the Msg with metadata that the logger treats as
ALWAYS sample-1%-or-error-only, regardless of state change. Belt-
and-braces on top of the diff filter.

This is opt-in; the diff filter alone handles 99% of cases without
any user annotation.

---

## Design — Request-id propagation

Every incoming HTTP request gets an `X-Request-Id`:
- Honour client-supplied header if present (lets upstream load
  balancers / CDNs control the ID).
- Else generate UUID v7 (time-sortable, collision-free).

Propagated through:
- Outgoing response header (so the client can correlate)
- Every log line emitted while handling the request (`req_id` field)
- Every `Cmd.perform` task's context (so DB queries inherit the ID)
- Every OTel span (`req.id` attribute)
- The Sky.Live Msg dispatch path (each dispatch carries the
  triggering request's ID)

### Kernel sig changes

`Cmd.perform`'s signature is currently:
```elm
Cmd.perform : Task err a -> (Result err a -> msg) -> Cmd msg
```

Adding req-id propagation requires the Task carry context. Options
considered:

**A. Implicit context via runtime IORef** (chosen)
- Runtime maintains a goroutine-local request context.
- `Cmd.perform` reads it at spawn time, stamps it into the
  goroutine running the Task.
- No kernel sig change. Backwards compatible.
- Trade-off: req-id propagation lost when user manually spawns
  goroutines outside Cmd.perform (rare; opt-in API for that case).

**B. Explicit context arg** (rejected)
- `Cmd.perform : RequestCtx -> Task err a -> (Result err a -> msg) -> Cmd msg`.
- Threads context as a value through user code.
- More principled but breaks every existing user.

Going with A. The runtime context is a hidden field on the dispatch
record passed to `update`; `Cmd.perform` reads it via FFI helper.

---

## Design — Endpoints

### `GET /_sky/healthz`
Returns 200 OK with `{"status":"ok"}` while the process is alive.
Cheap — no DB / store checks. Used by orchestrators for "is the
process running".

### `GET /_sky/readyz`
Returns 200 when ready to serve, 503 when not:
- Session store pingable
- DB pool has at least one healthy connection
- Jobs runner is consuming (when Std.Jobs configured)
- Not currently in SIGTERM-draining state

When the process receives SIGTERM, immediately flips readyz to 503
+ continues serving in-flight requests for `shutdownGracePeriod`
(default 30s) before exiting. Orchestrators (k8s, fly.io, ECS) use
this to drain traffic.

### `GET /_sky/metrics`
Standard Prometheus text exposition format. Metrics families:

**Counters:**
- `sky_live_requests_total{method,route,status}` — HTTP requests
- `sky_live_msg_total{name,outcome,noop}` — Msg dispatches
- `sky_live_sse_connections_total{outcome}` — SSE open/close events
- `sky_db_query_total{table,outcome}` — DB queries
- `sky_jobs_total{queue,outcome}` — Job runs (1.3-dependent)
- `sky_ffi_calls_total{pkg,outcome}` — FFI invocations

**Gauges:**
- `sky_live_sessions_active` — count of live SSE sessions
- `sky_db_pool_in_use` / `sky_db_pool_idle` — pool state
- `sky_jobs_inflight{queue}` — currently-running jobs
- `sky_jobs_queue_depth{queue}` — pending jobs

**Histograms** (bucket: 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s, +Inf):
- `sky_live_request_seconds{route}`
- `sky_live_msg_seconds{name}`
- `sky_db_query_seconds{table}`
- `sky_jobs_duration_seconds{queue}`

Production: `[observability] metrics_auth = true` (default) requires
the `Std.Auth` admin role; metrics endpoint returns 401 otherwise.
Dev: open. Prevents accidentally publishing internal metric labels
(may contain low-cardinality user data like route names).

### `GET /_sky/buildinfo`
```json
{
    "commit": "6cdbf0c",
    "builtAt": "2026-05-17T11:26:31Z",
    "skyVersion": "v0.13.4",
    "goVersion": "go1.22.0"
}
```
Always-on; no auth. Used by CI to verify deployment, dashboards to
show "running version".

### `GET /_sky/console` (Phase 1.1b)
The dashboard. See § 1.1b below.

---

## Design — `/_sky/console` dashboard (Phase 1.1b)

### Architecture

**Built as a Sky.Live app itself**, mounted at `/_sky/console`. Same
runtime, same diff protocol, same input-preservation — eats its own
dogfood, becomes a visible quality bar.

Reads from the Hot tier's in-memory buffers directly (zero-copy
snapshots via atomic pointer swap). Refreshes via a `Sub.every 1000
Tick` subscription — and yes, that Tick goes through the diff filter
so it doesn't spam logs.

### Auth

- **Production** (`[security] env = "production"` OR
  `[observability] console_auth = true` explicit): requires
  `Std.Auth` admin role. Returns 401 otherwise. AI may not configure
  this correctly out of the box, so default-on for safety.
- **Development** (default): open. Lets users see the dashboard on
  their first `sky run`.

### Tabs

In order of "what developers reach for first":

| Tab | Purpose |
|---|---|
| **Overview** | req/sec, active sessions, p50/p99/p99.9 latency, error rate (5/15/60 min sparklines). The "is it on fire?" tab. |
| **Live Sessions** | Real-time list: session ID (truncated), user (if signed in), current page, last Msg, idle time. Sortable. Click → session detail. |
| **Msg Flow** | Per-Msg counter + latency histogram, sorted by frequency. Click a Msg → last 50 dispatches with model diff (JSON patch format). |
| **Routes** | Per-route metrics + slow-route ranking. p99 outliers highlighted. |
| **DB** | Pool stats, slow queries (>100ms), per-query latency. Click a query → EXPLAIN (when SQLite). |
| **Jobs** | Queue depths, recent failures, dead-letter contents, retry button. Empty until 1.3 lands. |
| **Logs** | Tail of structured logs. Filter by level / req_id / msg / user. Click req_id → all logs for that request. |
| **Traces** | Recent traces. Click → spans waterfall view (start time, duration, parent-child). |
| **FFI** | Count + latency per Go `pkg.func`. Catches Stripe SDK / Firestore regressions. |
| **Errors** | Ranked distinct errors + counts + most-recent stack trace. The "what's breaking?" tab. |

### Implementation budget

- Main view module: ~800 LOC
- Per-tab modules: ~200-400 LOC each → ~3,000 LOC total
- Backing helpers (snapshot extraction, formatting): ~500 LOC

Scope creep risk is real. Strict rule: tabs that depend on later
phases (Jobs tab → 1.3) ship as "Std.Jobs not configured" placeholder
rather than blocking the dashboard release.

---

## Implementation plan

### Phase 1.1a — telemetry primitives

Order matters; each layer depends on previous.

**Step 1: Hot-tier storage** (`runtime-go/rt/telemetry/`)
- New package `telemetry` with `Store`, `LogEntry`, `TraceEntry`,
  `histogram` types.
- Lock-free counter / gauge maps via `sync.Map` + `atomic.Int64`.
- Ring buffers via a thin wrapper around a fixed-size slice.
- Tests: concurrent writes don't lose data, snapshot reads are
  consistent.

**Step 2: Request-id propagation**
- Hidden context field on Sky.Live dispatch record.
- FFI helper `rt.CurrentRequestId() string` accessible from Sky.
- `Cmd.perform` spawn site stamps context onto the goroutine via a
  hidden `goroutineContext` map (keyed by goroutine ID via
  `runtime.Stack` hack — Go doesn't expose goroutine-local storage,
  so we use a `sync.Map[gid]requestCtx`).
- Tests: req-id propagates through Cmd.perform chains, through
  `Task.parallel`, through `Task.andThen`.

**Step 3: HTTP middleware**
- `requestIDMiddleware`: generates / honours `X-Request-Id`,
  stamps context, sets response header.
- `metricsMiddleware`: bumps `sky_live_requests_total`,
  observes `sky_live_request_seconds`.
- `accessLogMiddleware`: emits one structured log line per request
  via the telemetry store.

**Step 4: Endpoints**
- `/_sky/healthz`, `/_sky/readyz`, `/_sky/metrics`,
  `/_sky/buildinfo` mounted into `Sky.Http.Server` + `Sky.Live`.
- `/_sky/metrics` exposition via `prometheus/client_model` Go lib
  (already a transitive dep via Stripe; if not, ~50 KB binary
  growth).

**Step 5: Diff-based Msg logging**
- Hook into the existing dispatch path in `live.go` (line 1958ish).
- Compute `hashModel(old)` and `hashModel(new)` after `update`.
- Log line emitted only on hash change OR non-`Cmd.none` OR error.
- Always bump `sky_live_msg_total` (with `noop=true` when
  no-op detected).

**Step 6: `Std.Live.lifecycle` kernel function**
- Wraps a Msg with a metadata tag the dispatcher reads.
- Kernel sig: `lifecycle : msg -> msg`.
- Runtime: stamps `LifecycleMeta` on the dispatched value;
  dispatcher consults before applying the diff filter.

**Step 7: OTel trace export**
- Auto-create spans for: HTTP request → Msg dispatch → update body
  → each Cmd.perform → each DB query → each FFI call.
- W3C `traceparent` header parsed on inbound; emitted on outbound
  Http.get / Http.post.
- OTLP/HTTP exporter (gzip + protobuf encoding) shipped via
  `go.opentelemetry.io/otel/exporters/otlp`.
- Sampling: head-based at 1% by default, errors always sampled,
  configurable via `[observability] trace_sample_rate`.

**Step 8: Warm tier** (optional, post-MVP)
- New `runtime-go/rt/telemetry/sqlite.go`.
- Schema migrations on first run.
- Background goroutine: batch INSERT every 30s, DELETE expired
  rows nightly, VACUUM weekly.
- Tests against a temp SQLite file.

**Step 9: Cold tier** (optional, can land with 1.1b)
- OTel auto-instrumentation already wired in step 7; the OTLP
  exporter is just one config flag away.
- Logs export uses OTel Log Data Model.
- Metrics export uses OTel Metric Data Model (pull from the
  Prometheus registry).

### Phase 1.1b — `/_sky/console`

Standalone Sky.Live app under `runtime-go/rt/console/`. Sky source
in `sky-stdlib/Std/Observability/Console.sky` (~3000 LOC). Mounted
into every Sky.Live app via a `_sky` route prefix that the existing
router already supports.

Per-tab modules: develop in order Overview → Live Sessions → Msg
Flow → Routes → DB → Logs → Traces → FFI → Errors → Jobs (last,
depends on 1.3).

---

## Open questions

These need resolution before / during implementation. Capture
answers as they're decided; flip from `?` to a citation when locked.

1. **? Metrics auth in production**: requires `Std.Auth` admin role
   to view `/_sky/metrics`? Or open with rate-limiting? Industry
   norm is auth (k8s scrape uses service-account JWT). Lean YES,
   but how should AI-generated code without Std.Auth set up handle
   this?

2. **? Trace context propagation through `Http.get` / `Http.post`**:
   should the runtime auto-inject `traceparent` on outbound HTTP?
   Pros: distributed tracing works out of the box. Cons: leaks the
   trace context to third parties (Stripe, Firestore) which may
   surprise users. Lean YES — every modern HTTP lib does this; opt
   out per-call via `Http.withoutTracing`.

3. **? Console auth dev/prod boundary**: how do we detect
   "production" reliably? `[security] env = "production"` is
   explicit. `NODE_ENV`-style heuristics are fragile. Lean: ONLY
   explicit `[security] env` flag; default-open in dev; default-on
   when binding to `0.0.0.0` (rough but useful heuristic).

4. **? `req_id` for SSE events**: an SSE connection is long-lived;
   each event within it inherits the connection's initial req_id?
   Or generates a new ID per event? Lean: per-event new ID,
   linked to the connection's parent ID via trace span.

5. **? Warm tier compression**: gzip log fields on insert (~5x
   ratio on JSON), or store raw and gzip on backup? Lean: gzip
   only when field >1 KB.

6. **? Histogram bucket boundaries**: fixed (1ms / 5ms / 10ms / 50ms
   / 100ms / 500ms / 1s / 5s / +Inf) for consistency across
   apps, or per-metric? Lean: fixed for v1; per-metric in v1.x if
   demanded.

7. **? "No-op detection" cost on every Msg**: 200 ns per dispatch is
   the budget. If hashing typed records is too slow, fall back to
   "always log" with a `[observability] log_all_msgs = true` opt-in.
   Lean: ship with hashing; profile in 1.1a; degrade to always-log
   if needed.

---

## Acceptance criteria

Phase 1.1a is "shipped" when ALL of these are green:

- [ ] `examples/09-live-counter` exposes `/_sky/healthz`,
      `/_sky/readyz`, `/_sky/metrics`, `/_sky/buildinfo` without any
      user code change.
- [ ] `curl /_sky/metrics` returns Prometheus exposition that parses
      under the official `expfmt` parser (tested in
      `runtime-go/rt/observability_test.go`).
- [ ] Playwright assertion in `scripts/verify-all-web.sh`: every
      Live app's `/_sky/healthz` returns 200.
- [ ] Diff-based logging: `runtime-go/rt/telemetry_test.go` asserts
      a `Tick` that returns `(model, Cmd.none)` produces 0 log lines
      AND `sky_live_msg_total{name="Tick",noop="true"}` is bumped.
- [ ] State-change Msg: same test asserts a `Click` Msg that mutates
      model produces 1 log line + `noop="false"` counter bump.
- [ ] Request-id propagates: integration test sends `X-Request-Id:
      abc123`, asserts response header echoes it, asserts log line
      from a Cmd.perform task inherits the same ID.
- [ ] OTLP exporter dispatches a span when `OTEL_EXPORTER_OTLP_ENDPOINT`
      env is set (verified via local OTel collector in CI).
- [ ] `Std.Live.lifecycle` Sky source compiles and the marker is
      respected (test fixture in `examples/`).
- [ ] Warm tier with `_sky/telemetry.db` works: 30s batched writes,
      retention DELETE, VACUUM all exercise cleanly.
- [ ] Docs: `docs/observability.md` covers all defaults, opt-outs,
      and the storage tier table.
- [ ] Sample Grafana dashboard JSON in `docs/dashboards/sky-live.json`.

Phase 1.1b is "shipped" when:

- [ ] `/_sky/console` renders all 10 tabs with seeded traffic on
      `examples/09-live-counter`.
- [ ] Playwright assertion: clicking each tab loads without error.
- [ ] Auth: production-mode binary returns 401 without admin role;
      dev-mode binary renders open.
- [ ] Doc page `docs/console.md` with annotated screenshots.

---

## References

- Phoenix LiveDashboard: https://hexdocs.pm/phoenix_live_dashboard/
  — the design we're emulating.
- OpenTelemetry Specification: https://opentelemetry.io/docs/specs/
- Prometheus exposition format:
  https://prometheus.io/docs/instrumenting/exposition_formats/
- W3C Trace Context: https://www.w3.org/TR/trace-context/
- Google SRE Book, Ch. 6 (Monitoring) — the four golden signals
  (latency, traffic, errors, saturation) which our metrics cover.

---

## Changelog

- 2026-05-17 — Initial draft. Branched off `main` as
  `feat/v1-roadmap`.
