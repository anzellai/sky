# Sky Console v0.16.x — Hub mode (`sky console serve`)

> Mode C from `OVERVIEW.md`: a long-running daemon that ingests OTLP
> from multiple Sky and non-Sky apps, stores in SQLite + DuckDB, serves
> a unified dashboard. Lands in v0.16.2 (receivers + storage) and v0.16.3 (UI).

## What it is

A new sub-command on the same `sky` binary:

```bash
sky console serve \
    --port 9000 \
    --data-dir /var/lib/sky-console \
    --auth app \
    --tls-cert /etc/sky/cert.pem --tls-key /etc/sky/key.pem
```

When invoked, the `sky` binary runs as an observability backend rather than compiling Sky code. Same single binary, different command. Like `git` having both `git commit` and `git daemon` — different modes of the same tool.

The hub:
1. Listens on OTLP gRPC (`:port + 4317 offset` by default, configurable)
2. Listens on OTLP HTTP (`:port + 4318 offset`)
3. Authenticates pushers via the auth callback (same `consoleAuth` shape as embedded mode)
4. Tags incoming telemetry by `service.name` resource attribute
5. Writes to a two-tier store (SQLite hot + DuckDB warm)
6. Serves a multi-service dashboard at `https://<host>:<port>/`
7. (Optional) federates aggregated data to an upstream hub

Hub is meant to run on a dedicated small VM — e2-small (2 GB RAM) is the sweet spot. Cloud Run is NOT recommended for the hub (long-lived stateful service, persistent disk for DuckDB).

## Receivers — OTLP gRPC + HTTP

Standard OTLP. Sky apps push via the in-process exporter (`EXPORTER.md`). Non-Sky apps push via their OTel SDK.

### gRPC (`:4317` by default)

```
Endpoint: opentelemetry.proto.collector.{logs,metrics,trace}.v1.{Logs,Metrics,Trace}Service/Export
Auth: Authorization: Bearer <SKY_CONSOLE_HUB_TOKEN>
Encoding: Protobuf (OTLP)
Compression: gzip (auto-negotiated)
```

### HTTP (`:4318` by default)

```
POST /v1/logs
POST /v1/metrics
POST /v1/traces
Auth: Authorization: Bearer <SKY_CONSOLE_HUB_TOKEN>
Content-Type: application/x-protobuf  OR  application/json
```

Both supported in parallel. gRPC preferred for efficiency; HTTP fallback for environments with gRPC-blocked egress.

### Receiver implementation

Backed by `go.opentelemetry.io/collector/receiver/otlpreceiver` (the same library OTel Collector itself uses). We don't rewrite OTLP parsing; we wrap the standard receiver with Sky-specific auth + routing.

Validation: payloads exceeding 4 MiB rejected with 413 (configurable via `SKY_CONSOLE_HUB_MAX_PAYLOAD`). Schema validation defers to the otelproto library.

## Auth — same `consoleAuth` shape

Hub uses the same `SKY_CONSOLE_AUTH=token|app|off` env var as embedded mode.

**`token` mode** (default for single-tenant hub):
- Single env var: `SKY_CONSOLE_HUB_TOKEN`
- All pushers send this token; all dashboard users see all services
- Simplest setup. Suitable when "everyone who can reach the hub should see everything."

**`app` mode** (multi-tenant — the SkyDeploy-as-hub case):
- Hub itself is a Sky.Live app. It defines its own `consoleAuth` callback.
- The callback returns `Identity` with `claims.tenant = "<tenant-id>"`.
- Tenant scoping is enforced by the storage layer: queries filter by `service_name LIKE '<tenant>%'` (kernel-derived, NOT caller-supplied) at the SQLite WHERE-clause layer.
- This is how SkyDeploy can run ONE hub for all its customers, each customer seeing only their services.

### Tenant isolation — defense-in-depth (v0.16.5 / v0.16.6)

Tenant scope flows through THREE layers, each independently sufficient:

1. **Auth-gate layer.** The hub's `consoleAuth` callback runs BEFORE
   the request reaches the bundled console. It validates the
   signed-in user, derives the tenant from your auth backend
   (Auth0/Clerk/your DB), and writes the `Identity` to
   `r.Context()` via `rt.IdentityContextKey`.

2. **Session-mint layer.** When the bundled console (a Sky.Live
   app under the hub) mints a session for the user, it copies
   `IdentityFromContext(r.Context())` onto `liveSession.identity`
   and gob-persists it via the session store so it survives a
   restart or replica fail-over. Subsequent SSE patches and
   `Cmd.perform` calls in the bundled console run within scope of
   that identity.

3. **SQL-WHERE layer.** Every `Hub_readFiltered*` kernel reads the
   identity off `currentLiveSession()`, extracts `claims["tenant"]`
   as the tenant prefix, and dispatches to the `WithTenant`
   variant of the storage reader. The SQL appends
   `AND service_name LIKE prefix || '%'` — the SQLite engine, not
   the bundled console code, enforces row scope. A caller-provided
   `serviceName` that doesn't start with the tenant prefix is
   rejected at the kernel layer with
   `Err("service outside tenant scope")` and never reaches the
   store.

Bundled-console code DOES surface the identity into its `Model`
(via `Hub.currentIdentity` → `GotIdentity` Msg arm) so the UI can
render "logged in as <email>" and pre-derive a tenantPrefix for
the service selector. But the security boundary lives in layers
1 and 3 — a bug in the Sky-source console (or a malicious replay)
can't widen the scope.

**Operator naming convention.** Use a consistent prefix scheme on
your `service.name` attributes. The simplest is
`<tenant>-<service>` (e.g. `customer-42-billing`,
`customer-42-frontend`).  Your `consoleAuth` callback returns
`claims["tenant"] = "customer-42-"` (with trailing separator) —
the LIKE clause becomes `service_name LIKE 'customer-42-%'`.

`%` and `_` in tenant claims are stripped at the SQL helper
(`escapeLikePrefix`) so a malicious claim can't widen the scope
via wildcard injection.

### Why not framework-automatic?

A previous design had the runtime auto-derive tenant scoping
purely from claims with no Sky-source visibility.  That's
brittle: bundled console UI couldn't show "filtered by tenant
X", the scope was invisible at call sites, and adding a second
tenant claim shape (per-project subscoping, say) required a
runtime change.

The shipped design exposes Identity to the Sky-source console
via `Hub.currentIdentity`, lets it thread the tenantPrefix on
explicit service-name arguments, AND keeps the kernel-layer
gate so the security boundary remains operator-controlled (not
caller-controlled).  Best of both: explicit + enforced.

## Storage — two-tier (hot SQLite + warm DuckDB)

### Hot store: SQLite WAL, last 24 hours

```
<data-dir>/console-hot.db
```

- Receives all writes in real-time
- Schema unchanged from embedded mode (logs / spans / metrics tables, time + level + service_name indexes)
- 24-hour rolling window. Pruned hourly by a background goroutine.
- ~200 MB-2 GB depending on load (10 apps @ 100 req/s ≈ 500 MB)

Why SQLite for hot: fast row writes (50k ops/s), low overhead, no separate process, well-understood operationally. Excellent for "recent" queries (live tail, last-hour requests).

### Warm store: DuckDB, last 30 days

```
<data-dir>/console-warm.db
```

- Receives ROLLED-UP data from hot store nightly (or hourly for high-cardinality services)
- Schema: columnar tables for spans + metrics + summarised logs
- Metrics rolled to 1-minute buckets (saves ~60× storage vs raw)
- Spans sampled per policy (errors 100%, slow 10%, fast 1% — see `OPS.md`)
- Logs: WARN+ retained 100%; INFO/DEBUG dropped from warm
- ~2-10 GB for 30 days of 10 services

Why DuckDB for warm: columnar OLAP storage, blazingly fast on time-range scans across 30 days (100-500ms for typical queries), embedded (no separate process), great for "trends over time" queries (p99 over the last week, error rate by service over the month).

The hot → warm roll-up runs as a background goroutine inside `sky console serve`, scheduled via a cron-style loop. Roll-up parameters configurable via `SKY_CONSOLE_HUB_*_RETENTION_DAYS`.

### Litestream replication (v0.16.5)

For durability:
- `console-hot.db` → continuously streamed to S3/GCS via Litestream
- `console-warm.db` → snapshotted nightly to S3/GCS (DuckDB doesn't benefit from streaming WAL the way SQLite does)
- Catastrophe recovery: restore from Litestream snapshot + replay last few minutes

Lives in `OPS.md`. Optional, opt-in via `SKY_CONSOLE_HUB_LITESTREAM_BUCKET`.

## Service identity

Each incoming telemetry batch carries OTLP resource attributes. The hub keys data by:

```
service.name              required — the "app identity" (e.g., "sky-lang.org", "ringfence", "skydeploy")
service.instance.id       optional — distinct app instance (helpful for Cloud Run replicas)
service.version           optional — for filtering by deployed version
```

Sky's in-process exporter auto-fills these:
- `service.name` from `sky.toml` project name (overridable via `OTEL_SERVICE_NAME`)
- `service.instance.id` from hostname + `K_REVISION` (Cloud Run) / `AWS_LAMBDA_LOG_STREAM_NAME` (Lambda)
- `service.version` from build commit + `OTEL_SERVICE_VERSION`

Non-Sky apps set these per OTel SDK conventions; the hub doesn't care.

## Federation (v0.16.5 / v0.17)

Hub-to-hub: a hub can ALSO push aggregated data to an upstream hub:

```
[Region us hub]   ┐
[Region eu hub]   ├──OTLP──▶  [Global hub]
[Region apac hub] ┘
```

Rolled-up metrics (1-minute buckets) + sampled error spans only. Full log/span data stays in regional hub. Saves bandwidth, gives global view.

This is the architecture pattern Datadog/Honeycomb use under the hood. For Sky users, this lights up "multi-region SaaS with regional data residency."

Out of scope for v0.16.2-0.16.4. Possible v0.16.5 if push protocol stays simple, otherwise v0.17.

## Resource overhead

Hub on **e2-small** (1 vCPU shared, 2 GB RAM, 30 GB SSD):

| Load (concurrent sources × req/s) | Aggregate ingest/s | DB writes/s | RAM | CPU |
|---|---|---|---|---|
| 1 × 100 | 100 events/s | 300 | ~150 MB | <5% |
| 10 × 100 | 1,000 events/s | 3,000 | ~300 MB | 10-15% |
| 50 × 100 | 5,000 events/s | 15,000 | ~600 MB | 30-50% |
| 100 × 100 | 10,000 events/s | 30,000 | ~1 GB | 60-80% |

Comfortable up to ~50 source apps on e2-small. At ~100 sources, recommend e2-medium (2 vCPU, 4 GB). At ~500 sources, recommend sharding (separate hub per service grouping) — that's a v0.17+ concern.

Storage on 30 GB SSD:
- Hot: 24 h × 5,000 events/s × 200 bytes = ~85 GB raw — but we don't store raw for 24 h at 5k events/s. Realistic: 1-2 GB hot.
- Warm: 30 d × rolled-up rates × ~50 bytes = ~5-10 GB warm
- Total: 6-12 GB for 30 days of 10 apps. Headroom for 50 apps on 30 GB.

## When to run a hub vs use the embedded console

| Scenario | Recommendation |
|---|---|
| 1 app, 1 VM | Embedded only. Don't bother with hub. |
| 1 app, 1 VM, want offsite backup | Embedded + Litestream replication of `<project>.console.db` to S3/GCS. |
| 2-3 apps, 1 VM each, want unified pane | Hub on one of the VMs (or a 4th dedicated small VM). |
| 5-50 apps, mix of VMs + Cloud Run | Dedicated hub VM (e2-small). |
| 50-500 apps | Dedicated hub VM (e2-medium / e2-large), consider sharding strategy. |
| Strict regulated data residency | Hub per region, no federation (or strict tenant isolation). |

Embedded + hub can both be on. Embedded gives the app a "local debug pane" even when the hub is unreachable; hub gives the unified multi-service view.

## Implementation milestones (v0.16.2 + v0.16.3)

**v0.16.2** (~5 days):
| Day | Work |
|---|---|
| 1 | `sky console serve` command scaffolding. CLI flags + cobra subcommand. |
| 1 | OTLP gRPC + HTTP receivers, wrapped from upstream `otlpreceiver`. Auth middleware. |
| 2 | Hot store: SQLite WAL writer, service-keyed schema, 24h pruner. |
| 3 | Warm store: DuckDB integration via cgo FFI. Bookend with build-tag fallback in case cgo unavailable. |
| 4 | Hot → warm roll-up goroutine. Cron-style scheduling. |
| 5 | End-to-end test: sky-lang.org pushes from local laptop to a hub on the same machine. Logs + metrics + traces visible in hot. After 1h, roll-up to warm. |

**v0.16.3** (~3 days):
| Day | Work |
|---|---|
| 1 | Hub UI scaffolding. Sky.Live app at the hub's HTTP port. Multi-service nav. |
| 2 | Service filter, time-range pickers, span waterfall view, log tail. Reuses Std.Ui chart primitives from v0.16.0. |
| 3 | End-to-end: all 3 apps (sky-lang.org + skydeploy + ringfence) pushing to one hub; UI shows them all, filterable. |

## Operating the hub

Recommended deployment for sky-lang.org / skydeploy / ringfence shared hub:

```bash
# On a dedicated GCE VM (e2-small, $13/mo) at obs.your-company.com
sudo systemctl enable sky-console-hub.service

# Apps push by setting these in their .env:
SKY_CONSOLE_HUB=https://obs.your-company.com:4317
SKY_CONSOLE_HUB_TOKEN=<token-registered-with-hub>
```

Operational checklist (`OPS.md` covers in detail):
- Backup `console-warm.db` nightly to GCS
- Monitor hub's own `/_sky/metrics` (it has an embedded console too, observing itself)
- Set up alerts for hub: ingest_rate < expected (apps not pushing), storage_usage > 80%, push_failures > threshold
