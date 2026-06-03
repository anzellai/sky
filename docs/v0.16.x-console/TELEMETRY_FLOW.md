# Sky Console — telemetry flow & deployment topologies

> Status: v0.16.1 PR10-H. Companion to
> [`RFC-v0.16.1-pr10-architecture.md`](./RFC-v0.16.1-pr10-architecture.md).
> Updated 2026-06-03.

Sky Console's headline value proposition is **one pane of glass for
every signal your Sky app generates** — logs, metrics, traces, errors.
Achieving that across the four topologies Sky deployments actually take
in the wild requires a small handful of disciplined choices: a
process-global `telemetry.Default()` Store, automatic context-based
`service.namespace` labelling, and a clean separation between the
"data plane" (writers + the Store) and the "UI plane" (the inline
console reader).

This doc covers the four topologies, the namespace label contract,
and the cookie + sky-id scoping decisions.

---

## Topology 1 — single-process Sky.Live (host only)

The default shape AI-generated Sky apps take. One binary, one port,
one Sky.Live app.

```
┌─────────────────────────────────────────────────────────────┐
│ Process P1 (e.g. examples/09-live-counter)                  │
│                                                             │
│  user code:                                                 │
│    Log.info "user signed in"  ──► writes ──►               │
│    Sky.Trace.span "db.query"  ──► writes ──►               │
│                                              │              │
│                                              ▼              │
│                                      ┌──────────────────┐   │
│                                      │ telemetry.       │   │
│                                      │   Default()      │   │
│                                      │ (process-global) │   │
│                                      └──────────────────┘   │
│                                              ▲              │
│  console mount:                              │              │
│    /_sky/console  ───── reads ──────────────┘              │
└─────────────────────────────────────────────────────────────┘
```

**No additional configuration required.** `Live.app cfg` boots, every
Sky stdlib effect writes to `telemetry.Default()`, the auto-mounted
inline console at `/_sky/console` reads it back.

`service.namespace` for these signals is the empty string (rendered
as "host" in the console UI's namespace filter chip).

Concrete code, no special cases:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log
import Sky.Live as Live

main =
    Live.app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" HomePage ]
        , notFound = HomePage
        }
```

Visit `http://localhost:8080/_sky/console` to see logs / metrics /
traces from the running app.

---

## Topology 2 — single process, nested in-process sub-apps

When the user wants to compose multiple logical apps into one binary
— a host app + an admin sub-app + a billing sub-app, each with its
own pages and update loop. Sky.Live's `MountLiveSubAppInProcess`
primitive (v0.16.1+) wires this without subprocess overhead.

```
┌──────────────────────────────────────────────────────────────────┐
│ Process P1                                                       │
│                                                                  │
│  hostApp ────  Log.info     ─────┐                              │
│    (/)        Cmd.publish        │                              │
│                                  │                              │
│  billingApp ── Log.info ─────────┼──► telemetry.Default()       │
│    (/billing) Cmd.publish        │            │                  │
│                                  │            │                  │
│  jobsApp ──── Log.info ──────────┘            │                  │
│    (/jobs)   Cmd.publish                      ▼                  │
│                                       ┌──────────────────┐       │
│  consoleApp ─── (auto-mounted) ──────►│ inline console   │       │
│    (/_sky/console)                    │  UI reads + filters       │
│                                       │  by service.namespace     │
│                                       └──────────────────┘       │
└──────────────────────────────────────────────────────────────────┘
```

### How `service.namespace` propagation works

Every incoming request lands at the parent mux. The
`ObservabilityMiddleware` (wrapping the user mux) calls
`WithSubAppNamespace`, which inspects the request's URL path against
the in-process sub-app prefix table
(`snapshotInProcessSubAppRoutes()`):

- request `GET /billing/invoice/42` → namespace `/billing`
- request `GET /_sky/console/api/logs` → namespace `/_sky/console`
- request `GET /` → namespace `""` (host)

Longest-prefix-wins ordering ensures `/billing/admin` (registered as
its own sub-app) beats `/billing` for paths under the admin tree.

The namespace is stamped into the request's `context.Context`. Sky
stdlib telemetry call sites that have access to the active request
context (HTTP middlewares, page handlers, spans) inherit the namespace
and tag their emissions.

### Code snippet

```go
// main.go for examples/34-multi-tier-console
package main

import (
    "net/http"
    rt "sky-app/rt"
)

func main() {
    mux := http.NewServeMux()
    // The host app and the console both work as before.
    // The new primitive mounts the billing + jobs apps on the
    // same mux, in the same process.
    rt.MountLiveSubAppInProcess(mux, "/billing", billingCfg)
    rt.MountLiveSubAppInProcess(mux, "/jobs",    jobsCfg)
    // Host app's Live_app runs the listener.
    rt.Live_app(hostCfg)
}
```

### Required namespace labels — automatic

Telemetry stamping happens at the middleware boundary so application
code doesn't manually thread namespace context through every Log /
Trace call. Sub-apps' code looks identical to standalone apps' code;
the namespace tag appears automatically once their pages handle a
request under the mounted prefix.

For BACKGROUND goroutines (post-init Tasks, scheduled jobs, Cmd.perform
completions firing after the originating request has returned) the
namespace falls back to "" unless the goroutine explicitly captures
and propagates the request context. v0.17+ may add an explicit
`Std.Trace.withNamespace` Sky-side surface for this case; v0.16.1
documents the fallback.

---

## Topology 3 — same host, multiple processes (`MountSubApp` fork+exec)

Pre-dates the in-process variant. A parent Sky.Live app spawns child
binaries via `rt.MountSubApp(mux, prefix, rt.SpawnBinary(...))`. Each
child is its own OS process — its own session store, its own
update loop, completely isolated.

```
┌──────────────────────┐        ┌──────────────────────┐
│ Process P1 (parent)  │        │ Process P2 (billing) │
│                      │        │                      │
│  /_sky/observability/│◄───────│ PushExporter         │
│   ingest             │   POST │   (every 2s, batched)│
│        │             │        │                      │
│        ▼             │        │ writes ── billing's  │
│  telemetry.Default()│        │  Log.* / metrics /   │
│        │             │        │  spans               │
│        ▼             │        └──────────────────────┘
│  console mount       │
│  /_sky/console       │        ┌──────────────────────┐
│   reads aggregated   │        │ Process P3 (admin)   │
└──────────────────────┘        │                      │
                                │ PushExporter ────────►
                                │                      │
                                └──────────────────────┘
```

The parent's mux reverse-proxies child requests; the children's
`PushExporter` (in `runtime-go/rt/observability_push.go`) batches
log + metric + span deltas and POSTs them to the parent every 2s
under a shared `X-Sky-Ingest-Token` (auto-rotated per parent boot).

The parent's `telemetry.Default()` merges incoming child signals with
its own. The inline console at the parent's `/_sky/console` reads the
merged view.

### Existing — no PR10 change

PushExporter shipped in v0.14. The `service.namespace` label is set
by the child via the `SKY_LIVE_NAMESPACE` env var that `MountSubApp`
injects when it spawns the child (default: the URL prefix the child
is mounted under, sanitised).

---

## Topology 4 — distributed: multiple Sky processes on different hosts

The v0.16+ deployment story for multi-service or multi-region Sky
apps. Each Sky process writes locally to its own
`telemetry.Default()` AND ALSO exports a subset to a central hub via
the v0.16.1 `HubExporter` (PR4).

```
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ Service A        │  │ Service B        │  │ Service C        │
│  (host: api-1)   │  │  (host: api-2)   │  │  (host: worker)  │
│                  │  │                  │  │                  │
│ HubExporter ─────┼──┼──────────────────┼──┼───── HubExporter │
│                  │  │ ┌─────────────┐  │  │                  │
└──────────────────┘  │ │   Hub       │  │  └──────────────────┘
                      │ │ (sky console│  │
                      │ │   serve)    │  │
                      │ │             │  │
                      │ │ multi-      │  │
                      │ │ service     │  │
                      │ │ console     │  │
                      │ └─────────────┘  │
                      └──────────────────┘
```

The hub binary (`sky console serve`, v0.16.2 — task #429) receives
OTLP-formatted batches from every service's `HubExporter`, persists
to a SQLite hot tier (≤24h) + a DuckDB warm tier (≤30d), and serves
the SAME Sky.Live console UI source as the embedded mode — just
against a different data backend.

`service.namespace` in the hub mode comes from the SERVICE name, set
via `SKY_SERVICE_NAME` env. Within a service, the in-process
sub-app namespace from Topology 2 is preserved as
`service.subnamespace`.

### v0.16.x roadmap

| Version | Status | What |
|---|---|---|
| v0.16.1 | shipping | HubExporter (the push side) |
| v0.16.2 | #429 | `sky console serve` hub binary (the receive side) |
| v0.16.3 | #430 | Hub UI: multi-service dashboard with cross-service drill-down |
| v0.16.4 | #431 | OTel SDK recipes — Python/Node/Go/Rust/JVM hosts pushing into the hub |
| v0.16.5 | #432 | Alerting, RBAC, query DSL, Litestream replication |

---

## Cookie & sky-id namespace decisions

The console's session cookie and the host app's session cookie can
coexist on the same browser origin without colliding because of
per-app cookie naming + path scoping:

| Concern | Host app | Inline console |
|---|---|---|
| Session cookie name | `sky_sid` | `__Host-sky_console_sse` (legacy parallel infra) |
| Auth cookie | (user-defined or none) | `__Host-sky_console` |
| sky-id prefix | `r` | `sky-console` (PR10-A) |
| Cookie Path | `/` | `/` for auth (RFC 6265 `__Host-` requirement) |

The `__Host-` prefix on the auth cookie REQUIRES `Path=/` per RFC
6265bis §4.1.3.2. The session cookie is differentiated from the
host's by name (`__Host-sky_console_sse` vs `sky_sid`), so even
sharing the path they don't collide on lookup.

When `MountLiveSubAppInProcess` mounts a generic sub-app at e.g.
`/billing`, the sub-app uses:
- Session cookie: `sky_billing_sid` (Path=`/billing/`)
- sky-id prefix: `sky-billing`

The host's `sky_sid` (Path=`/`) and the sub-app's `sky_billing_sid`
(Path=`/billing/`) coexist cleanly — the browser sends both to the
sub-app's routes but only `sky_sid` to the host's `/` routes.

---

## Privacy mode / cookies disabled

If a user opens the console in a strict-cookie browser configuration
(third-party-cookies-blocked, or session-cookies-disabled entirely),
the SSE patch channel cannot associate frames with the right tab.
The console falls back to "no live updates; refresh to retrieve
latest data".

The data-plane (`telemetry.Default()` writes) is unaffected — only
the UI's live-update channel degrades. A manual page refresh always
shows the latest data.

---

## Console architecture status (v0.16.1)

The current `/_sky/console` mount uses a **parallel infrastructure**
(`runtime-go/rt/console_loop.go`, `console_sse.go`,
`console_app_hooks.go`) that mirrors but does not share Sky.Live's
machinery. This was the v0.15 → v0.16 migration shape: it let the
inline console ship as a working Sky.Live UI without committing the
runtime to a unified `MountLiveSubAppInProcess` primitive that hadn't
been designed yet.

v0.16.1 PR10 introduces `MountLiveSubAppInProcess` as that unified
primitive. It's used by `examples/34-multi-tier-console` to
demonstrate the in-process composition story (Topology 2 above) and
is the migration target for the console itself.

The unification follows in **v0.16.2** alongside the hub-mode
console (task #429), where converting the console to a real Sky.Live
sub-app simultaneously delivers:

1. Deletion of the parallel infra (~1750 LOC)
2. A single console UI source that ALSO runs in `sky console serve`
3. Multi-tier example shape (host + N sub-apps + console) becomes a
   first-class user-facing pattern, not a runtime internal

The reason for the v0.16.1 / v0.16.2 split: the parallel infra
embeds several process-global hooks (`SetConsoleAuthCallback`,
`registerProcessBroker`, `SKY_PARENT_URL` env seed, console-specific
SSE cookie + CSRF handling) that are entangled with `liveAppRun`'s
initialisation order. Splitting those out cleanly is its own PR cycle
and would have made PR10 itself uncomfortably large. Shipping the
primitive + telemetry foundation NOW means contributors can build
multi-tier examples using the new shape immediately; the console
migration follows once the user-facing patterns are settled.

---

## Cross-references

- [`RFC-v0.16.1-pr10-architecture.md`](./RFC-v0.16.1-pr10-architecture.md)
  — design rationale + atomic PR plan + acceptance criteria
- [`EMBEDDED.md`](./EMBEDDED.md) — inline console mount details, auth
  modes, production gate
- [`EXPORTER.md`](./EXPORTER.md) — HubExporter (v0.16.1 PR4 / PR5)
- [`SERVERLESS.md`](./SERVERLESS.md) — Cloud Run / Lambda spool +
  drain semantics
- `examples/34-multi-tier-console/README.md` — multi-tier showcase
  using `MountLiveSubAppInProcess` (PR10-I)
- `runtime-go/rt/subapp_inprocess.go` — primitive source
- `runtime-go/rt/telemetry_namespace.go` — context propagation
- `runtime-go/rt/observability_push.go` — Topology 3 PushExporter
- `runtime-go/rt/observability_hub.go` — Topology 4 HubExporter
