# RFC — v0.16.1 PR10: inline Sky Console as a same-process Sky.Live sub-app

> Status: design (2026-06-03). Implementation paused after PR10-A scaffolding
> at commit `b9654140`. Resumed work depends on this RFC + the four-topology
> telemetry-flow design below.

## Why this RFC exists

PR3/PR8/PR9 shipped a working inline Sky Console at `/_sky/console` with real
KPIs and clickable tabs (Playwright-verified locally and on the sky-lang.org
canary). But the implementation is a **parallel runtime** to the canonical
Sky.Live machinery: separate session map, separate SSE channel, separate
event POST endpoint, separate handlers registry, separate client JS.

The Sky's solo architect's verdict:

> "if the sky console is an 'one-off' app which is NOT really a normal
> sky.live app, how can contributors understand or help make
> improvements/changes? i think the short circuit 'special' case handling
> doesn't look right to me or be scalable and maintainable."
>
> "we've already supported the 'embedded' subapps — the architecture works,
> what we need now is to 'reuse' this mechanism, BUT allow 'same process'"

And on the telemetry surface:

> "you must also consider backward how subapps metrics/traces/logs piping
> to our app's sky console... if really cannot be done, those will need to
> be 'exported' to the sky console app. just need real documents update +
> examples showcase. think really carefully, as sky console is one of the
> biggest features of sky ecosystem"

This RFC lays out the architecture so PR10 lands as a coherent change
across runtime, console_app, docs, and examples.

## Goals

1. **Inline console reuses canonical Sky.Live machinery** — no parallel
   session map, SSE channel, event POST, handlers registry, client JS.
   Contributors who know Sky.Live can read `sky-bundled/console/src/Main.sky`
   and understand the whole app.
2. **Telemetry flows naturally from anywhere in the process to the
   console** — host app, in-process sub-apps, cross-process sub-apps
   (`MountSubApp`), and (v0.16.2) cross-host services via `HubExporter`.
3. **sky-id namespace is unambiguous** — every console-rooted hid is
   `sky-console.<...>` so logs/traces/dev tools clearly identify which
   app produced the event.
4. **Backwards-compatible at the user surface** — `Live.app cfg` works
   exactly as before; sub-app federation via `MountSubApp` still pipes
   to the parent's telemetry.

## Non-goals

- The cross-host hub (v0.16.2). HubExporter already exists in v0.16.1
  PR4; the receiving end (`sky console serve`) is its own cycle.
- Refactoring `MountSubApp`'s fork+exec mode. We're ADDING an
  in-process variant, not replacing the existing one.

## The mechanism: `MountLiveSubAppInProcess`

`rt.MountSubApp` (v0.14+) already runs a Sky.Live app under a prefix
via fork+exec + reverse-proxy. Its prefix-mounting half is proven.

PR10 adds a same-process variant:

```go
// MountLiveSubAppInProcess mounts a Sky.Live app as a sub-app on the
// parent's mux, in the same process. Returns the *liveApp for the
// caller to attach lifecycle hooks (shutdown drain, etc.).
//
// The sub-app gets its OWN liveApp instance — its own session map,
// broker, events map, cookie name (default sky_console_sid), and
// sky-id prefix (derived from basePath). Telemetry, in contrast, is
// process-shared via telemetry.Default() so the console sees host +
// every sub-app's signals in one place.
//
// Idempotent: registering twice with the same prefix panics (Go's
// ServeMux enforces).
func MountLiveSubAppInProcess(parentMux *http.ServeMux, prefix string, cfg any) *liveApp
```

Internally:

1. Resolve a non-listening `liveApp` via `liveAppMountCore(cfg, parentMux, prefix)`
   (extracted from `liveAppRun`).
2. Register `<prefix>/_sky/event`, `<prefix>/_sky/sse`, `<prefix>/_sky/config`
   and the initial-render handler at `<prefix>/` on the parent mux. NO
   catch-all `/` registration (the parent owns that for itself).
3. Derive `cookieName = "sky_" + sanitised(prefix) + "_sid"` and
   `skyIDPrefix = sanitised(prefix)` — e.g., `sky_console_sid` +
   `sky-console`. PR10-A already added the per-app fields for these.
4. Skip `MountObservabilityEndpoints` — the parent already owns
   `/_sky/healthz`, `/_sky/readyz`, `/_sky/metrics`, `/_sky/buildinfo`.

### What `liveAppMountCore` looks like

Extracted from the current `liveAppRun`. Same setup minus `srv.ListenAndServe`:

```go
type liveMountOpts struct {
    basePath     string // "" for the host app; "/_sky/console" for the console
    isSubApp     bool   // true → skip catch-all, observability, banner injection
    cookieName   string // "sky_sid" for host; "sky_console_sid" for console
    skyIDPrefix  string // "r" for host; "sky-console" for console
}

func liveAppMountCore(cfg any, mux *http.ServeMux, opts liveMountOpts) *liveApp
```

The current `liveAppRun(cfg)` becomes:

```go
func liveAppRun(cfg any) any {
    mux := http.NewServeMux()
    app := liveAppMountCore(cfg, mux, liveMountOpts{
        cookieName:  "sky_sid",
        skyIDPrefix: "r",
    })
    // unchanged: signal handlers, srv.ListenAndServe, shutdown hooks
    ...
}
```

## Telemetry flow — the four topologies

The console's value proposition is "one pane of glass for every signal
your app generates." Here's how each deployment topology gets signals into
that pane:

### Topology 1 — single process, host app only

```
┌─────────────────────────────────────────────────┐
│ Process P1 (e.g. examples/09-live-counter)      │
│                                                 │
│  liveApp_main ─── writes ───▶ telemetry.Default()
│      │                              │           │
│      │                              ▼           │
│      └─── /_sky/console (sub) ─── reads         │
│             liveApp_console                     │
└─────────────────────────────────────────────────┘
```

`telemetry.Default()` is a process-global singleton (defined in
`runtime-go/rt/telemetry/store.go`). All Sky stdlib effects (`Log.*`,
`Sky.Trace.span`, automatic HTTP/session spans) write to it.

The console sub-app, also in P1, reads it directly. No piping needed.

**This is the most common shape — single-binary Sky.Live apps.**

### Topology 2 — single process, nested in-process sub-apps

```
┌──────────────────────────────────────────────────┐
│ Process P1                                       │
│                                                  │
│  liveApp_main ──── writes ───┐                   │
│                              │                   │
│  liveApp_billing ─── writes ─┼─▶ telemetry.Default()
│   (mounted at /billing)      │           │       │
│                              │           ▼       │
│  liveApp_console (/_sky/console) ─────── reads   │
└──────────────────────────────────────────────────┘
```

Multiple `liveApp` instances mounted via `MountLiveSubAppInProcess` all
share `telemetry.Default()`.

**Required namespace labels**: each `liveApp` stamps its log/metric/span
emissions with a `service.namespace` attribute derived from `basePath`:

- host app → `service.namespace = ""` (default)
- console → `service.namespace = "_sky/console"`
- billing → `service.namespace = "/billing"`

The console UI surfaces a `service.namespace` filter pill so operators
can drill into per-sub-app signals. v0.16.x console_app's `view` gains a
namespace selector at the top of each tab.

**Telemetry stamping** is automatic via context propagation:

- HTTP request lands at the parent mux
- Parent mux's dispatcher inspects the request path → matches a sub-app
  prefix → sets `currentServiceNamespace` on the request context
- Sky stdlib effects (`Log.info`, `Sky.Trace.span`) inherit the
  namespace from the context

This is the **only new infra PR10 needs on the telemetry side** —
auto-stamping `service.namespace` from the path prefix.

### Topology 3 — same host, multiple processes (`MountSubApp` fork+exec)

```
┌──────────────────────┐    ┌──────────────────────┐
│ Process P1 (parent)  │    │ Process P2 (billing) │
│                      │    │                      │
│ telemetry.Default()  │◀━━━│ PushExporter ◀ writes│
│        │             │    └──────────────────────┘
│        ▼             │
│  liveApp_console ── reads
│  (/_sky/console)     │
└──────────────────────┘
```

This is already shipped (v0.14+). `runtime-go/rt/observability_push.go`'s
`PushExporter` batches logs/metrics/spans every 2 s and POSTs to the
parent's `/_sky/observability/ingest` with namespace + token labels. The
parent merges into its `telemetry.Default()`.

**No PR10 change required** — the in-process console naturally sees the
aggregated stream.

### Topology 4 — distributed: multiple Sky processes on different hosts

```
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ Service A (P1)   │  │ Service B (P2)   │  │ Service C (P3)   │
│                  │  │                  │  │                  │
│ HubExporter ─────┼──┼─▶  ┌─────────┐ ◀─┼──┼─ HubExporter     │
│                  │  │    │  Hub    │   │  │                  │
└──────────────────┘  │    │ (sky    │   │  └──────────────────┘
                      │    │ console │   │
                      │    │ serve)  │   │
                      │    └─────────┘   │
                      └──────────────────┘
```

This is the v0.16.2 hub mode (task #429). PR4's HubExporter is the
push side; the receiving `sky console serve` hub binary lands later.
v0.16.1's console UI is the SAME Sky source — at v0.16.2 the hub
mode just runs it against an aggregated SQLite/DuckDB store fed by
HubExporter receivers.

**No PR10 change required** beyond ensuring the in-process console
and the hub console can share the same `sky-bundled/console/` source.

## Cookie & sky-id namespace decisions

| Concern | Host app | Inline console sub-app |
|---|---|---|
| Session cookie name | `sky_sid` | `sky_console_sid` |
| Auth cookie | (user-defined or none) | `__Host-sky_console` (unchanged from v0.16.0) |
| sky-id prefix | `r` (legacy) | `sky-console` |
| Cookie Path | `/` | `/_sky/console/` (scopes the session cookie to the sub-app's path) |

The auth cookie (`__Host-sky_console`) stays `Path=/` per RFC 6265bis §4.1.3.2
(the `__Host-` prefix requires it). The session cookie (`sky_console_sid`)
uses `Path=/_sky/console/` so the host app's Sky.Live can't see it and
vice versa.

## Files that change

### Added

- `runtime-go/rt/subapp_inprocess.go` (~150 LOC) — `MountLiveSubAppInProcess`
- `runtime-go/rt/subapp_inprocess_test.go` (~300 LOC) — isolation + shared
  telemetry regression suite
- `runtime-go/rt/telemetry/namespace.go` (~60 LOC) — `service.namespace`
  context propagation
- `docs/v0.16.x-console/TELEMETRY_FLOW.md` — the 4-topology design above,
  with diagrams + concrete examples
- `examples/34-multi-tier-console/` — a multi-tier showcase: host Sky.Live
  app + 1 in-process sub-app (`MountLiveSubAppInProcess`) + 1 cross-process
  sub-app (`MountSubApp`) + the console showing all three's telemetry
  with namespace filters

### Modified

- `runtime-go/rt/live.go` — extract `liveAppMountCore` from `liveAppRun`
- `runtime-go/rt/console.go` — `MountEmbeddedConsole` uses
  `MountLiveSubAppInProcess` instead of bespoke wiring
- `runtime-go/rt/console_app/register.go` — registers the console as a
  Sky.Live cfg (init/update/view/subs/api) instead of hooks
- `docs/v0.16.x-console/EMBEDDED.md` — replace the "parallel infra" notes
  with "console is a Sky.Live sub-app mounted at /_sky/console"

### Deleted

- `runtime-go/rt/console_loop.go` (~627 LOC) — replaced by canonical
  `liveApp` event handling
- `runtime-go/rt/console_app_hooks.go` (~167 LOC) — `RegisterConsoleAppHooks`
  shim no longer needed
- `runtime-go/rt/console_app/console_client.go` (~242 LOC) — host emits
  the standard Sky.Live client JS for the sub-app
- `runtime-go/rt/console_app/register_v2.go` (~203 LOC) — `hookDecodeMsg`
  replaced by canonical hid lookup
- `runtime-go/rt/console_sse.go` (~519 LOC) — kept as a stub for back-compat
  of `ConsoleSSEHealthy()` exports; handlers deleted

**Net code change**: ~+450 LOC additive (new primitive + tests + docs +
example), ~−1750 LOC parallel infra deleted. Net **−1300 LOC** with much
cleaner architecture.

## Implementation plan — atomic PRs

| PR | What | Status | Risk |
|---|---|---|---|
| PR10-A | Per-app `cookieName` + `skyIDPrefix` scaffolding | ✅ Landed at `b9654140` | Low |
| PR10-B | Extract `liveAppMountCore` from `liveAppRun` | ✅ Landed (subsumed into PR10-C — `newLiveAppFromCfg` + `registerSubAppRoutes` are the extracted halves) | Medium |
| PR10-C | Add `MountLiveSubAppInProcess` primitive + tests | ✅ Landed at `efa26137` — 11 regression tests green | Low |
| PR10-D | Telemetry namespace context propagation | ✅ Landed at `efa26137` — middleware wraps `ObservabilityMiddleware`, namespace flows through ctx | Medium |
| PR10-E | console_app registers as Sky.Live cfg | ⏸ **Deferred to v0.16.2** (see below) | High |
| PR10-F | `MountEmbeddedConsole` uses `MountLiveSubAppInProcess` | ⏸ **Deferred to v0.16.2** | High |
| PR10-G | Delete parallel infra (console_loop, hooks, sse stubs, client_js) | ⏸ **Deferred to v0.16.2** (after F) | Low |
| PR10-H | `docs/v0.16.x-console/TELEMETRY_FLOW.md` | ✅ Landed at `68cc04ab` | Low |
| PR10-I | `examples/34-multi-tier-console/` showcase | ✅ Landed at `68cc04ab` — 4 logical tiers in one binary, console aggregates per-namespace | Low |
| PR10-J | Playwright verification: tab clicks + namespace filters on the showcase | ✅ Landed at `68cc04ab` — 13 PASS assertions; legacy console-click-test.mjs still green | **Hard gate met** |

### Why E/F/G defer to v0.16.2

After landing PR10-B/C/D, the audit of `liveAppRun`'s setup
sequence surfaced FOUR process-global hooks that the current parallel
console infra entangles non-trivially:

1. `SetConsoleAuthCallback(app.consoleAuth)` — single-app slot in
   `console_auth.go`; the console-as-Sky.Live path needs its own
   `consoleAuth` field which the sub-app's cfg shape doesn't carry.
2. `registerProcessBroker(app)` — `Std.PubSub.publish` reaches THE
   process-global broker; the sub-app shouldn't overwrite the host's.
   Already "last writer wins" tolerant, but a console-as-sub-app
   would currently STOMP the host's broker since the console mount
   runs DURING `liveAppRun` (between fields being populated).
3. `os.Setenv("SKY_PARENT_URL", ...)` — the console's `init_` reads
   this from env; moving init to inside `MountLiveSubAppInProcess`
   would re-trigger the env seed for every sub-app boot.
4. CSRF middleware integration — the console currently uses its own
   isolated SSE channel + POST endpoint at `/_sky/console/_event`
   (PR3 isolation). Routing through canonical Sky.Live `/_sky/event`
   would re-introduce the cookie-vs-CSRF interaction that PR3 cleanly
   separated.

The v0.16.1 in-process primitive ships TODAY because the
multi-tier-example use case + telemetry namespace propagation are
unambiguously additive — neither touches the entangled hooks.

The console-as-Sky.Live refactor (E/F/G) requires its OWN cycle to:
- Decompose the four hooks into clean injection points (each
  hook gets a per-app capability rather than a process global)
- Add a Sky cfg→Go bridge so console_app's typed hooks can satisfy
  `Field(cfg, "Init")` without losing static typing
- Run the migration in the same cycle that v0.16.2's `sky console serve`
  hub mode lands, which ALSO needs a unified console source

Splitting v0.16.2 into "(a) deferred E/F/G + (b) hub binary" gives the
console exactly ONE refactor instead of TWO — and the user-facing
multi-tier example pattern is the same in both worlds because
`MountLiveSubAppInProcess` is already the public primitive.

### Acceptance criteria status (v0.16.1)

1. ✅ `go test ./rt/...` green
2. ⏳ `cabal test` — pending (run before push to main)
3. ✅ `examples/34-multi-tier-console/` builds + Playwright green
4. ⏳ sky-lang.org production canary — pending (separate redeploy step)
5. ✅ `TELEMETRY_FLOW.md` covers 4 topologies with diagrams + tested example
6. ⏸ Console source unification — moves to v0.16.2 per the rationale above

## Backwards compatibility

- `Live.app cfg` shape: unchanged.
- `Live.app cfg.consoleAuth`: unchanged.
- `__Host-sky_console` cookie + `SKY_CONSOLE_AUTH` env: unchanged.
- `/_sky/console` URL: unchanged (sub-app mount point).
- v0.16.0/v0.16.1's console UI source (`sky-bundled/console/src/Main.sky`):
  the SAME source compiles for both the inline embedded mode AND v0.16.2's
  hub `sky console serve` mode. Two deployment shapes, one app.

The only user-visible delta: a NEW session cookie `sky_console_sid`
appears on first console visit. Pre-PR10 users had no such cookie
(the parallel infra used `__Host-sky_console_sse`).

## Open questions

1. **Should `MountLiveSubAppInProcess` accept an auth-wrap function** so
   the console's `ConsoleGate` is applied uniformly? Or should every
   handler the sub-app registers individually wrap itself? Leaning
   toward the wrap-once pattern.

2. **Should the console's `subscribe` clock tick (`Sub.every 3000`) tick
   on the parent's broker** or the sub-app's own broker? Since
   telemetry.Default() is process-global, ticking on the sub-app's
   broker is sufficient — no cross-broker synchronisation needed.

3. **Cookie collision when host app + console run on the same domain**:
   the auth cookie is `__Host-sky_console` (clearly console-scoped) and
   the session cookie is `sky_console_sid` (Path=/_sky/console/). The
   host's `sky_sid` (Path=/) wouldn't be visible to the console
   sub-app's handlers — but might be cleared if the host app calls
   `clearCookie`. **Resolution**: never clear cookies by name unless
   you wrote them. (Already the convention in Sky.Live.)

4. **What about `Sky.Http.Server` hosts (not Sky.Live)**? `Server_listen`
   ALSO calls `MountEmbeddedConsole`, so the console must work on a
   host that doesn't have a `liveApp` at all. The console sub-app's
   own `liveApp` is enough — the host's mux is still its mount point.

5. **What if the user disables session cookies entirely** (privacy
   mode)? The console-mode session cookie is functionally required
   for the SSE channel to associate frames with the right tab. If
   disabled, the console falls back to "no SSE; refresh to update".
   Document this fallback in `TELEMETRY_FLOW.md`.

## Cut-line decisions

These are explicit "we're not doing this in PR10":

- **Hub-mode console (sky console serve)** — v0.16.2 cycle (task #429).
  The same console UI source will run there; only the data backend
  changes (SQLite → DuckDB warm tier).
- **Per-sub-app retention policies** — currently every signal lives in
  the global `telemetry.Default()` with global TTLs. Per-namespace
  retention is v0.17+ if demand arises.
- **Cross-region aggregation** — geographic distribution is hub-mode
  territory; not relevant for in-process.

## Acceptance criteria

PR10 lands when ALL of these are green:

1. `runtime-go go test ./rt/...` (every test in the rt + sub-packages).
2. `cabal test` (timeout 3600).
3. `examples/34-multi-tier-console/` builds + runs; Playwright script
   verifies (a) tab clicks switch tabs, (b) namespace filter shows
   host vs sub-apps' signals separately, (c) telemetry from a
   cross-process MountSubApp ALSO appears under its namespace.
4. sky-lang.org production canary deploys cleanly, Playwright on the
   real domain passes.
5. `docs/v0.16.x-console/TELEMETRY_FLOW.md` covers all 4 topologies
   with diagrams + tested example links.
6. Console-app source under `sky-bundled/console/src/` reads as a
   normal Sky.Live app — no special `RegisterConsoleAppHooks` calls,
   no parallel runtime references.

Only after all 6 are green does v0.16.1 get tagged.
