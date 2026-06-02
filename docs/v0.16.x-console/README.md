# Sky Console v0.16.x — Design Folder

This folder is the design spec for the v0.16.x release cycle. Every doc
here was written 2026-06-02 to drive implementation; nothing here ships
yet. Code lands across v0.16.0 → v0.16.5 over ~5 weeks of focused work.

## Quick context

Sky's existing `/_sky/console` was dev-grade — broke on small VMs
because the `sky console` subprocess go-built itself at runtime
(OOMing e2-micro). v0.16.x makes the console production-grade:

- **Embedded mode**: in-process console UI, no subprocess, no
  runtime `go build`. Works on e2-micro.
- **Exporter (collector) mode**: in-process OTLP push to user's
  existing observability backend (Cloud Logging / Datadog /
  Honeycomb / etc.) with on-disk spool + retry + backpressure.
- **Hub mode**: a new `sky console serve` daemon that accepts
  OTLP from multiple Sky AND non-Sky apps, stores in SQLite
  (hot) + DuckDB (warm), serves a unified multi-service
  dashboard.

The hub mode is the GTM wedge — sets up "Datadog at $0/mo for
small teams without Kubernetes" positioning. See
`sky-strategy/` repo for the market/positioning thesis.

## Reading order

| # | Doc | Read it when |
|---|---|---|
| 1 | [OVERVIEW.md](OVERVIEW.md) | First. Big picture, three modes, cycle scope. |
| 2 | [EMBEDDED.md](EMBEDDED.md) | You want to know what's served at `/_sky/console`. |
| 3 | [EXPORTER.md](EXPORTER.md) | You want to know how telemetry reaches the hub reliably. The reliability lever. |
| 4 | [HUB.md](HUB.md) | You want to know what `sky console serve` is. |
| 5 | [HUB-UI.md](HUB-UI.md) | You want to know what the hub dashboard looks like. |
| 6 | [SERVERLESS.md](SERVERLESS.md) | Your app deploys to Cloud Run / Lambda. |
| 7 | [NON-SKY.md](NON-SKY.md) | You have Python / Node / Go services that should push to the hub. |
| 8 | [OPS.md](OPS.md) | You're operating the hub in production — alerts, RBAC, replication. |
| 9 | [MIGRATION.md](MIGRATION.md) | You're upgrading an existing app (sky-lang.org / skydeploy / ringfence as worked examples). |

## Cycle scope — six patches in one minor version

| Patch | Scope | Days | Detail doc |
|---|---|---|---|
| v0.16.0 | Embedded console hardening (inline, app-scoped DB, auth gate, Std.Ui chart primitives) | ~6 | EMBEDDED.md |
| v0.16.1 | Exporter / collector (two-mode spool, OTLP push, backpressure, SIGTERM drain) | ~3 | EXPORTER.md + SERVERLESS.md |
| v0.16.2 | `sky console serve` hub (OTLP receivers, SQLite hot + DuckDB warm storage) | ~5 | HUB.md |
| v0.16.3 | Hub UI (multi-service dashboard, drill-down, SSE updates) | ~3 | HUB-UI.md |
| v0.16.4 | Non-Sky ingestion (Python / Node / Go / Rust / JVM recipes validated) | ~3 | NON-SKY.md |
| v0.16.5 | Production polish (alerts, RBAC, Litestream replication, keyword query DSL) | ~5 | OPS.md |

**Total: ~25 days. Single v0.16.x minor version family, six patch tags.**

## Key design decisions

These are the calls made in the design (synthesised from a 3-agent
debate — DX-first, security-first, perf-first lenses). Each is named
explicitly so future questions can be answered against the design,
not re-litigated:

| Decision | Choice | Rationale |
|---|---|---|
| Console binary | Inlined into `sky` (no separate executable) | User constraint, single-binary positioning |
| Runtime go-build | Removed entirely | Source of e2-micro OOM, breaks single-binary promise |
| Storage path | `<dataDir>/<projectName>.console.db` | Predictable, debuggable; collision warned not silently merged |
| Auth gate | `SKY_CONSOLE_AUTH=token\|app\|off`, explicit in prod | No silent open-to-world mounts |
| App-level auth | Row-poly optional `consoleAuth` callback on `Live.app` cfg | Same shape as v0.15.58 `head` — existing apps don't break |
| Wire format | OTLP everywhere (gRPC + HTTP) | Industry standard; non-Sky apps integrate without effort |
| Spool backend | Auto-detect: SQLite WAL on disk OR in-memory ring | Serverless reality (Cloud Run has no durable disk) |
| Hub hot store | SQLite WAL | Fast row writes, 50k ops/s, no separate process |
| Hub warm store | DuckDB | Columnar OLAP, 30-day scans sub-500ms |
| Sampling | 100% errors/metrics, 10% slow, 1% fast, configurable | Operators want signal not lossless ledger |
| Query DSL | Keyword + time + level + service for v0.16.x | PromQL/LogQL deferred — out of scope |
| Custom dashboards | Out of scope | Feature-creep magnet |
| Mobile UI | Out of scope | Ops work happens on desktop |

## Test apps (the three canaries)

Each design decision validated end-to-end on three real apps:

| App | Deployment | Validates |
|---|---|---|
| **sky-lang.org** | GCE VM e2-micro | Embedded mode on tiny VM; Hub push from VM |
| **skydeploy** | Cloud Run (per-tenant) | Serverless exporter; SIGTERM drain; multi-tenant hub UI |
| **ringfence** | GCE VM | Multi-app federation; second canary for VM mode |

A v0.16.x patch isn't ready to tag until all three canaries are green.

## Out of scope for v0.16.x

To keep the cycle focused, these are explicitly deferred:

- **Kubernetes-native deploy** — no operators, no Helm, no daemonset. Sky's positioning is the no-K8s segment.
- **Multi-region active-active hub** — single-region hub for v0.16.x; federation pattern for v0.16.5 if simple.
- **Custom alerting DSL beyond keyword + simple metric expressions** — defer.
- **Anomaly detection / ML-driven insights** — out.
- **Hub UI customisation (custom dashboards)** — out for v0.16.x; possible v0.17.
- **Sharding the hub** — single-VM hub for v0.16.x; sharding for v0.17 if real adoption demands.

## Strategic positioning

Sky Console is potentially Sky's **GTM wedge** — more than the language itself. See:
- `sky-strategy/strategy/positioning.md` (in the sky-strategy repo)
- Memory note `sky_console_strategy.md`

The pitch: "Datadog at $0/mo for small teams that don't run Kubernetes,
that ALSO works for your Python/Node/Go services, and ALSO happens to
power Sky Lang." Hub adoption is easier than language adoption →
hub is the trojan horse for Sky Lang itself.
