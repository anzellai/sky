# Sky Console v0.16.x — Architecture Overview

> Status: design doc, written 2026-06-02 to drive the v0.16.x release cycle.
> Reviewed shape: in-process exporter + optional `sky console serve` hub.
> Single binary, three deployment modes, OTLP wire format.

## The problem this solves

Sky apps need production-grade observability — logs, metrics, traces, all queryable from a single dashboard. The existing `/_sky/console` is dev-only: it spawns a child `sky console` subprocess that compiles itself via `go build` on first launch, peaks ~1 GB RAM, and OOMs anything smaller than e2-small. In production today, users have three bad choices:

1. Run a meatier VM than they need just to host the console
2. Ship telemetry off-host to Datadog/Honeycomb/Cloud Logging (works, but $$$ or locks data into a vendor)
3. Cobble together Grafana + Loki + Tempo (5 processes, Kubernetes-orbiting, ops-heavy)

The v0.16.x console fixes this by making **observability a first-class part of the Sky runtime** — defaults work on any VM (including e2-micro Always Free), data stays in the user's infra, and the same single `sky` binary powers both the per-app embedded console AND an optional multi-app hub.

## Three deployment modes

A Sky app runs in exactly one combination of these modes. Auto-detected from environment; explicit overrides via env vars.

### Mode A: Embedded (the dev default, the small-VM default)

```
[Sky app process]
  ├── HTTP routes (user code)
  ├── /_sky/console  ← in-process UI, server-rendered Std.Ui
  └── SQLite WAL at <dataDir>/<projectName>.console.db
```

- `/_sky/console` mounted on the same HTTP listener as the app
- Telemetry buffered in-memory (5 MB ring), written through to local SQLite for retention
- Dev mode: zero-config, open in browser at `localhost:8000/_sky/console`
- Production: `SKY_CONSOLE_AUTH=token|app|off` gate (refuses to mount without explicit choice)

**Best for:** solo devs, single-app deployments on a VM, "I just want a dashboard" cases.

### Mode B: Exporter only (use someone else's infra)

```
[Sky app process] ──OTLP──▶  [your existing observability stack]
                              (Cloud Logging / Datadog / Honeycomb / Grafana Cloud / etc.)
```

- Embedded console disabled (`SKY_CONSOLE_EMBED=off`)
- All telemetry pushed via OTLP to a user-configured backend
- This is exactly the path sky-lang.org runs today (via Google Cloud Ops Agent → Cloud Logging/Monitoring/Trace)

**Best for:** teams with existing observability stack, regulated industries that require a specific backend, anyone who's already paying for Datadog/New Relic/etc.

### Mode C: Hub push (the v0.16.x killer mode)

```
[ringfence app]        ┐
[sky-lang.org app]     ├──OTLP──▶  [sky console serve daemon]
[skydeploy tenants]    │             ├── OTLP gRPC + HTTP receivers
[non-Sky services]     ┘             ├── Hot: SQLite (24h, all signals)
                                     ├── Warm: DuckDB (30d, sampled/rolled)
                                     ├── UI: Std.Ui multi-service dashboard
                                     └── Auth: consoleAuth callback
```

- Sky apps push via the in-process exporter (Mode A or B can also be on; not exclusive)
- Non-Sky services (Python/Node/Go/Rust) push via their off-the-shelf OTel SDKs
- Hub serves the unified pane at its own URL (e.g. `obs.your-company.com`)
- One `sky console serve --port 9000` command, no separate binary

**Best for:** multi-app shops without Kubernetes (the target market). Solo operators with 3-10 services across 1-3 VMs. SkyDeploy itself as a hosted multi-tenant hub for its customers.

## How the modes combine

Modes are not exclusive. A single app can be:

| Configuration | Embedded UI | Pushes to hub | Pushes to OTLP backend |
|---|---|---|---|
| Dev default | ✓ | — | — |
| Solo prod | ✓ | — | — |
| Small VM + push to hub | ✓ | ✓ | — |
| Serverless + push to hub | — | ✓ | — |
| Existing infra | — | — | ✓ |
| Hybrid (sky-lang.org pattern) | ✓ | ✓ | ✓ |

Detection logic at startup:
- `SKY_CONSOLE_EMBED` (default `auto`): on for VMs, off for serverless
- `SKY_CONSOLE_HUB` (default unset): if set, exporter pushes to that endpoint
- `OTEL_EXPORTER_OTLP_ENDPOINT` (default unset): if set, parallel OTLP push to that endpoint

Multiple destinations supported simultaneously — the exporter fan-outs.

## OTLP as the wire format

OpenTelemetry Protocol (OTLP) is the open standard for logs/metrics/traces. Sky uses it everywhere:

- App → embedded console DB: OTLP-encoded payloads stored
- App → hub: OTLP gRPC (port 4317) or OTLP HTTP (port 4318)
- App → external backend: OTLP HTTP/gRPC to whatever the user configured
- Hub → external backend (federation): OTLP push to upstream hub or Cloud Logging-style sink

**Why OTLP**: industry-standard (Datadog, Honeycomb, Grafana, Cloud platforms all speak it). Sky doesn't invent a wire format. Non-Sky services integrate via their existing OTel SDK without changing anything. The hub becomes universal — not Sky-only.

## What this isn't

To stay scoped:
- **Not Kubernetes-first.** No daemonsets, no operators, no Helm. Single VMs + serverless are the primary deployment shapes.
- **Not a Datadog clone.** No marketplace, no integrations directory, no GenAI summarisation. Focused on the 95% case.
- **Not a query DSL.** v0.16.x ships keyword/time/level filters. PromQL/LogQL/TraceQL deferred to a future cycle.
- **Not multi-region active-active.** Hub federation pattern exists for hierarchy, but multi-master concurrent-write coordination is out of scope.

## Cycle scope (single v0.16.x minor version)

Six patch releases, all under v0.16.x:

| Patch | Scope | Days | Reference doc |
|---|---|---|---|
| v0.16.0 | Embedded console hardening — inline + app-scoped DB + auth gate + Std.Ui chart primitives | ~6 | EMBEDDED.md |
| v0.16.1 | Exporter (collector) — two-mode spool, OTLP push, backpressure, SIGTERM drain | ~3 | EXPORTER.md |
| v0.16.2 | `sky console serve` hub — OTLP receivers, DuckDB warm storage | ~5 | HUB.md |
| v0.16.3 | Hub UI — multi-service dashboard, filters, drill-down | ~3 | HUB-UI.md |
| v0.16.4 | Non-Sky ingestion — OTel SDK recipes (Python/Node/Go) | ~3 | NON-SKY.md |
| v0.16.5 | Production polish — alerts, RBAC, Litestream replication, keyword query | ~5 | OPS.md |

**Total ~25 days, ~5 weeks of focused work.** Tested incrementally on sky-lang.org + skydeploy + ringfence (the three real apps) as each patch lands.

## Backwards compatibility

Three commitments:

1. **No `Live.app` cfg fields become required.** New `consoleAuth` field uses row-polymorphic optional shape (same as v0.15.58 `head`). Existing apps build unchanged.
2. **Existing env vars stay supported.** `SKY_ADMIN_TOKEN`, `SKY_CONSOLE_TOKEN_SECRET`, `SKY_CONSOLE_DB_PATH` continue to work; new vars are additive.
3. **Wire format never breaks.** OTLP is versioned by OpenTelemetry; we follow their compatibility guarantees.

If an existing app does nothing on upgrade to v0.16.x: same behaviour as v0.15.x (embedded console in dev, off in production). Opt into hub mode by setting `SKY_CONSOLE_HUB`. Opt into push-only by setting `SKY_CONSOLE_EMBED=off` + `SKY_CONSOLE_HUB`.

## Reading order for this design folder

1. **OVERVIEW.md** (this doc) — big picture, three modes
2. **EMBEDDED.md** — Mode A details: in-process UI, storage, auth
3. **EXPORTER.md** — the in-process collector design (reliability + scalability checklist)
4. **HUB.md** — `sky console serve` daemon: receivers, storage, federation
5. **HUB-UI.md** — multi-service dashboard design
6. **SERVERLESS.md** — Cloud Run / Lambda specifics
7. **MIGRATION.md** — how the three real apps adopt
8. **OPS.md** — alerts, RBAC, retention, replication
9. **NON-SKY.md** — integrating Python/Node/Go services
