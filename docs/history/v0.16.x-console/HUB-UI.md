# Sky Console v0.16.x — Hub UI design

> The dashboard served by `sky console serve` at its HTTP port.
> Multi-service, multi-tenant capable, Std.Ui-rendered with Sky.Live SSE.
> Lands in v0.16.3.

## Design principles

1. **Server-rendered, live-updated.** Same Sky.Live pattern as user apps — server renders HTML via Std.Ui, pushes updates via SSE. No client-side build step.
2. **Identity-scoped.** Whatever `consoleAuth` returned shapes what the user sees. No data leaks across tenant boundaries.
3. **Opinionated layout.** Five tabs (same as embedded mode), service filter, time-range picker. No custom dashboard editor.
4. **Drill-down friendly.** Click anything to expand. Errors → spans → logs in the same flow. No tab-switching to follow a request.
5. **Fast.** Hot store (24 h) queries sub-100ms. Warm store (30 d) queries sub-500ms. SSE updates on Overview page every 1-2 seconds.

## Information architecture

```
┌─ Hub UI ────────────────────────────────────────────────────────────────┐
│ Header:  [Logo]  [Service: all ▾]  [Time: last 1h ▾]  [Profile]  [Help] │
│                                                                          │
│ Tabs:    [Overview]  [Requests]  [Errors]  [Logs]  [Traces]             │
│                                                                          │
│ Body:    Tab-specific content                                            │
│                                                                          │
│ Footer:  Hub status • build • uptime                                    │
└──────────────────────────────────────────────────────────────────────────┘
```

The service filter is the most important control. Three modes:
- **all** — aggregate view across every service the user has access to
- **single service** — focus on one (e.g., `sky-lang.org`)
- **multi-select** — comparison view (e.g., `sky-lang.org` + `ringfence` side-by-side)

## Tab: Overview

The "is everything OK?" tab. Two-column layout:

**Left column** (per-service stats, scrollable):
- Service name + version + instance count
- Sparkline cards: req/s, p95 latency, error rate (last 60 min)
- Status indicator: green (no errors), yellow (errors but recovering), red (errors trending up)
- Most-traffic route
- Alert badge (if alert active — links to alert detail in OPS.md / v0.16.5)

**Right column** (selected service detail, or aggregate if "all"):
- Larger charts: req/s timeseries (last 24 h), latency histogram, error breakdown by status code
- "Top routes" list with relative traffic share
- "Recent errors" — last 10 distinct errors, click to drill into the trace
- "Active sessions" (if Sky.Live app) — count + average duration

SSE updates: charts refresh every 1-2 seconds for the selected service. Other services' status indicators refresh every 10 seconds.

## Tab: Requests

A waterfall of recent HTTP requests. Inspired by Chrome DevTools' Network tab.

| Time | Service | Method | Route | Status | Duration | Spans |
|---|---|---|---|---|---|---|
| 17:42:13.234 | sky-lang.org | GET | /blog/:slug | ✓ 200 | 87 ms | ▶ 3 spans |
| 17:42:13.198 | ringfence | POST | /api/billing | ⚠ 503 | 1.2 s | ▶ 8 spans (1 error) |

Click a row → expands span tree inline. Click a span → expands its log lines + attributes. Click an error log → opens the full error context (stack, errId, surrounding logs).

Filters: time range, service, status code (2xx/3xx/4xx/5xx), duration (>p95), route pattern (string match).

Pagination: virtual scrolling. 200 rows visible, infinite-scroll loads older. Goes to spool cap (last 24 h hot) by default; "deeper" toggle queries warm store.

## Tab: Errors

The "what's breaking?" tab. Two views toggled at the top:

**Heatmap view** (default):
- 7-day × 24-hour grid
- Cell colour intensity = error count
- Hover for breakdown by error type
- Click a cell to filter Requests tab to that window

**List view**:
- Distinct errors ranked by frequency
- Count, last-seen, first-seen, sample stack snippet
- Click → drill to the trace + logs of the most recent occurrence
- "Affected services" + "Affected routes" facets

Identifies error patterns operators actually want to find ("we get a spike of 503s every day at 14:00 — what's the cron job?").

## Tab: Logs

Streaming log tail. Looks like `journalctl -f` but with structure.

Features:
- SSE-live updates (last 5 minutes default, scrollable to last 24 h hot / 30 d warm)
- Level filter chips: DEBUG / INFO / WARN / ERROR (multi-select)
- Service filter (inherited from header)
- Substring search (FTS5-backed for the hot store)
- Click a log → expand structured fields (req_id, span_id, custom attrs)
- "Show trace" button → drills into the span the log was emitted from

Performance: SSE buffer 50 lines/sec max per service. Backpressure if user is scrolled to the past (older queries take precedence over live tail).

## Tab: Traces

Distributed trace explorer. For Sky apps, traces span:
- HTTP request → Sky.Live handler → Cmd.perform Task → external API → DB query

For non-Sky apps using OTel SDKs, standard W3C trace context — all spans linked by `trace_id`.

View:
- Search by trace ID
- Filter by service, duration, error status
- Flame graph of selected trace (vertical span tree with timing bars)
- Hover → span detail panel (attributes, events, links)
- "Compare" mode: select two traces side-by-side to diff timing

The flame graph rendering uses Std.Ui's chart primitives + custom SVG layout. Performance: 1000-span traces render in <100 ms.

## Time range control

Top-right header. Click → dropdown:

```
[ Last 5 minutes  ▼ ]
  Last 5 minutes
  Last 1 hour       ← default
  Last 6 hours
  Last 24 hours
  Last 7 days
  Last 30 days
  ──────────
  Custom range...
```

Custom range → date/time picker, max window 30 days (warm store retention).

Quick filter: "live" toggle — bypasses time range, shows everything as SSE-streamed in real-time. Default off; enable to follow a specific incident.

## Multi-tenant ACL

When the hub runs in `app` auth mode (e.g., SkyDeploy hosting customer hubs), the identity returned by `consoleAuth` carries:

```elm
Identity { claims = Dict.fromList [("tenant", "customer-42")] }
```

The UI enforces:
- Service filter only lists services where `service.name LIKE "customer-42-%"`
- All queries pre-filtered by tenant ACL
- "all services" view scoped to tenant only
- Audit log of every dashboard access by `(user, tenant, query)`

This is the same shape SkyDeploy currently uses for per-tenant data isolation; the hub layer just inherits the pattern.

## Multi-instance drill-down

When a single service has multiple replicas (Cloud Run autoscale), all instances tag with the same `service.name` but distinct `service.instance.id`. The UI shows:

- Service-level aggregate by default
- "Per-instance" expand → see each replica's metrics independently
- Useful when one replica is misbehaving while others are healthy
- Implicit: the hub knows from `service.instance.id` how many replicas are pushing; renders a "instances: 5" badge

## Embed in SkyDeploy control plane

SkyDeploy already iframes the per-tenant console via JWT-in-URL handshake (in v0.15.x). Post-v0.16.x:

```
SkyDeploy control plane           Hub
┌─────────────────────────┐      ┌────────────────┐
│ Customer dashboard      │      │ DuckDB store   │
│  ┌──────────────────┐  │      │                │
│  │ <iframe          │──┼──────┼─▶ /tenant/42/  │
│  │   src="hub.../..."│  │      │   (scoped UI) │
│  └──────────────────┘  │      │                │
└─────────────────────────┘      └────────────────┘
```

The iframe source URL carries a one-shot HS256 JWT. Hub validates, sets cookie, redirects to scoped view. Same mechanism as embedded mode's iframe handshake.

## What we deliberately defer

- **Custom dashboards** — feature creep. Defer to v0.17+.
- **Alert configuration UI** — alerts ship in OPS.md / v0.16.5 but config via YAML file first. UI editor v0.17+.
- **Mobile layout** — ops work on desktop. Render unusable < 900 px wide.
- **Dark/light theme toggle** — opinionated dark only.
- **PromQL/LogQL DSL** — keyword + time + level + service is enough for v0.16.x.
- **Anomaly detection / ML insights** — out of scope. Stick to deterministic queries.

## Implementation milestones (v0.16.3)

| Day | Work |
|---|---|
| 1 | Hub UI scaffolding. `sky console serve` mounts a Sky.Live app at its HTTP port. Header + tab navigation. |
| 1 | Service filter (header) + time-range picker. URL state encoded so deep links work. |
| 2 | Overview tab — sparkline cards, status indicators, service list. SSE updates. |
| 2 | Requests tab — waterfall list, expand-on-click, span tree inline. |
| 3 | Errors + Logs + Traces tabs. Cross-tab drill-down (click error → trace → logs). |

The Std.Ui chart primitives (line / area / sparkline / heatmap / bar / span-waterfall) are dependencies from v0.16.0. The trace flame graph is the only chart that's hub-UI-specific.
