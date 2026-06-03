# Sky Console v0.16.x — Embedded mode

> Mode A from `OVERVIEW.md`: in-process console UI served at `/_sky/console`
> on the same listener as the app, backed by local SQLite.
> Shipped: v0.16.0 (HTML mount + auth + Std.Ui charts) and v0.16.1 (isolated
> SSE channel transport surface). Full live-UI interactivity follows in
> v0.16.2 (task #429) — the SSE plumbing is in place.

## Goal

Sky binaries auto-mount a production-grade dashboard at `/_sky/console`. Zero config in dev, opinionated-safe-defaults in production. Survives across small VMs (e2-micro 1 GB RAM) without OOM. No subprocess, no runtime `go build`, no separate binary.

## Architecture — inline (no subprocess)

**What we delete from v0.15.x:**

The current `runtime-go/rt/console.go` spawns a child `sky console` process via `SpawnSkyConsole`, builds the child's binary on first launch via `go build` against TH-embedded source at `sky-bundled/console/`, then reverse-proxies HTTP from parent's `/_sky/console/*` to the child's port. This is what OOMed sky-lang.org's e2-micro on 2026-06-02.

**What we ship in v0.16.0:**

The console becomes Go code compiled INTO the runtime, not a separate Sky.Live app. The compiler-side build pipeline does the Sky-to-Go translation ONCE at `cabal install` time and embeds the output as ordinary Go source under `runtime-go/rt/console_app/`. The runtime mounts handlers directly on the same `*http.ServeMux` the app uses.

```
[Sky compiler build pipeline]
    1. sky-bundled/console/ — Sky source for the console UI
    2. cabal build invokes the local sky binary to compile this source
    3. Output Go code written to runtime-go/rt/console_app/*.go
    4. runtime-go is built with these files included (//go:build-tagged)
    5. Final sky binary has console code as part of its runtime package

[Sky app at runtime]
    framework.Mount(mux)
    if shouldMountConsole() {
        rt.MountEmbeddedConsole(mux)  // adds /_sky/console/* handlers
    }
```

No subprocess, no `go build` on the deploy target, no Go toolchain dependency on VMs.

**Binary size impact:** approximately +5-10 MB on the compiled Sky binary (the console code + its dependencies). This is a fair trade for eliminating runtime-build complexity, and Sky binaries are already 15-25 MB. Final app binaries grow to ~25-35 MB — still small for Go.

## Storage — app-scoped SQLite

```
<dataDir>/<projectName>.console.db
```

- `<dataDir>` derived from runtime context (CWD-relative for dev, configurable via `SKY_DATA_DIR` for production, defaulted to `./` for dev and to a service-appropriate path on systemd / Cloud Run)
- `<projectName>` from `sky.toml`'s `[project] name` field, already known to the runtime via embedded build metadata
- Format: SQLite WAL
- One file per app. Multiple apps on the same host with the same `(dataDir, projectName)` pair → framework emits `console.storage.collision` warn log at boot (operator-visible, doesn't break, doesn't silently merge data)

**Why project name and not build ID:** the security agent in the design debate argued for a build-time random ID to prevent collision. We rejected that: operators expect "rebuild + redeploy" to preserve console history, not wipe it. Project name is predictable, debuggable, and collision-warn-loud-not-silent-merged.

**Why one file per app:** parallel writers from concurrent app instances would contend on a shared SQLite file. App-scoped means each app's process owns its console DB. Sub-app federation (see HUB.md) handles aggregation; embedded mode is intentionally per-app-process.

**Schema:** unchanged from v0.15.x's existing `telemetry/persist.go` (logs / spans / metrics tables with time + level + trace_id indexes). v0.16.0 retains the schema; v0.16.5 adds a hot/warm split (see `OPS.md`).

## Auth — three modes, production gates explicit

The production-gate problem: in v0.15.x the console silently doesn't mount in production (`maybeAutoMountConsole` early-returns), but if you set `SKY_CONSOLE_EMBED=on` to force it, the auth surface is ambiguous (Bearer token? JWT? Both?). v0.16.0 makes the choice explicit.

```
SKY_CONSOLE_AUTH=token     # token-based (default for single-tenant)
SKY_CONSOLE_AUTH=app       # app-pluggable callback (for SSO / multi-tenant / custom)
SKY_CONSOLE_AUTH=off       # disabled, no console surface
```

In production mode (`ENV` set to anything except dev / development / local), `SKY_CONSOLE_AUTH` is **required**. Unset → console doesn't mount + `console.disabled reason=auth-unset` warn at boot. No silent open-to-the-world.

### `token` mode (single-tenant default)

```
SKY_CONSOLE_TOKEN=<openssl rand -hex 32>     # ≥32 bytes enforced
```

Flow:
1. Admin opens `https://app.example.com/_sky/console`
2. Login page: POST form (not GET — query strings leak via Referer)
3. Cookie minted: `__Host-sky_console` + `HttpOnly` + `Secure` + `SameSite=Strict` + `Path=/_sky/console` + `Max-Age=14400` (4 h)
4. Subsequent requests: cookie auth, no further token entry needed
5. Token rotation: change env var + SIGHUP. Cookies signed with derived HKDF key, so old cookies fail on next request.

The `__Host-` cookie prefix is RFC 6265bis-compliant. Browsers enforce `Secure` + `Path=/` (interpreted as no `Domain` attribute), making cross-domain attacks structurally impossible.

### `app` mode (SSO / multi-tenant)

Row-polymorphic optional field on `Live.app` cfg (same shape as v0.15.58 `head` field):

```elm
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subs
        , routes = [ ... ], notFound = ...
        , consoleAuth = \req -> identifyAdmin req
        }
```

Type:
```elm
consoleAuth : Request -> Task Error (Maybe Identity)

type alias Identity =
    { subject : String      -- user identity (GitHub login, email, etc.)
    , email : String         -- for audit logging
    , claims : Dict String String   -- extra attrs for RBAC (v0.16.5)
    }
```

The framework calls this callback before mounting console routes. `Nothing` → 403 + `console.auth.denied` audit log. `Just identity` → console renders, identity attached to subsequent requests via the same `__Host-sky_console` cookie.

The callback gets the raw `Request` (cookies, headers, query) and nothing else. The whole 100-line JWT-mint dance in sky-lang.org's `src/Auth/Console.sky` disappears.

**Backwards compatibility:** apps that don't set `consoleAuth` and don't set `SKY_CONSOLE_AUTH=app` use the `token` default. Existing apps build unchanged.

### `off` mode

Explicit opt-out. Mount no console surface at all. Telemetry buffers + exporter still work — this is for the "we ship telemetry to Datadog, no embedded UI needed" case.

## URL handshake — one-shot, for iframe embedding

For SkyDeploy-style scenarios (control-plane embeds the console iframe with a short-lived URL token), v0.16.0 preserves the JWT-in-URL pattern from v0.15.x's `console_auth.go` but hardens it:

- One-shot enforced via server-side `sync.Map` of seen `jti` claims. Replays denied.
- `aud` claim must match the runtime's build ID — not just any HS256-signed blob with the right secret.
- Only available in `token` mode (irrelevant when `app` callback handles auth end-to-end).
- Off by default. Opt in via `SKY_CONSOLE_EMBED_ORIGIN=<exact-origin>`.

This closes the security agent's biggest attack-surface concern from the design debate.

## UI scope — five tabs

The console UI is server-rendered with Std.Ui, server-pushed via SSE. No client-side JS framework. Reuses Sky.Live's existing patch protocol (`runtime-go/rt/live.go`'s SSE plumbing).

| Tab | Content | Priority |
|---|---|---|
| **Overview** | Three sparkline cards (req/s, p50/p95/p99 latency, error rate %); top 5 routes by traffic; live session count; build commit + uptime | Must-have |
| **Requests** | Waterfall list of last 200 requests, status colour bar, inline span tree expand-on-click. Click a 500 → opens the span with its error log inline | Must-have |
| **Errors** | Frequency heatmap (hour × day, last 7 d) + ranked distinct errors with count, last-seen, sample stack | Must-have |
| **Logs** | Tail-style log stream via SSE, level/route filter chips, regex search | Must-have |
| **DB** | Applied migrations, slowest queries, connection-pool gauge | Must-have |

**What we deliberately don't ship in v0.16.0:**
- Custom dashboard editor (feature-creep magnet)
- Query DSL (PromQL/LogQL/TraceQL — defer to v0.17)
- Alert configuration UI (lives in OPS.md / v0.16.5)
- Mobile-responsive layout (desktop-only; the console is for ops, render unusable < 900 px wide)
- Dark/light theme toggle (one opinionated theme — dark)

## Std.Ui chart primitives (v0.16.0 also)

To build the five tabs we need:
- Line chart (single + multi-series)
- Area chart (stacked optional)
- Bar chart (vertical, horizontal)
- Sparkline (no axes, compact)
- Heatmap (2D grid with colour intensity)
- Span waterfall (custom for trace UI)

These ship as `Std.Ui.Chart` — useful for the console AND any user app that wants dashboards. They render to SVG (server-side) for static views and to HTML+SSE for live views.

This is its own v0.16.0 deliverable; doesn't block on console-specific work.

## What changes for existing apps

**Apps that did NOTHING in v0.15.x:** same behaviour. Embedded console in dev, off in production (unless `SKY_CONSOLE_EMBED=on` + `SKY_CONSOLE_AUTH=token` + `SKY_CONSOLE_TOKEN` are set).

**Apps that set `SKY_CONSOLE_EMBED=on` in v0.15.x:** still works. v0.16.0 adds `SKY_CONSOLE_AUTH` requirement in production; default `token`, set `SKY_CONSOLE_TOKEN` to enable.

**Apps that use SkyDeploy's Pro+ JWT pattern:** still works. The JWT-in-URL handshake is preserved with the new one-shot hardening.

**New in v0.16.0**: apps can set `consoleAuth` field on `Live.app` to gate the console behind their existing session middleware. sky-lang.org's `src/Auth/Console.sky` (the 100-line JWT-mint pattern from tonight) can be deleted post-v0.16.0 deploy.

## Implementation milestones — SHIPPED

### v0.16.0

| Day | Work |
|---|---|
| 1-2 | Build pipeline: cabal target that runs `sky` against `sky-bundled/console/` to emit Go code, writes to `runtime-go/rt/console_app/`. Cabal install includes this in the runtime package. |
| 3 | Framework `MountEmbeddedConsole(mux)` replaces `SpawnSkyConsole`. Subprocess + reverse-proxy code deleted. |
| 4 | `<dataDir>/<projectName>.console.db` storage path + collision warn. v0.15.x's `SKY_CONSOLE_DB_PATH` env back-compat. |
| 5 | Auth: `SKY_CONSOLE_AUTH` gate + `consoleAuth` row-poly field + `__Host-` cookie + HKDF key derivation + one-shot JTI enforcement. |
| 6 | Std.Ui chart primitives (line / area / bar / sparkline / heatmap). |

Tested on sky-lang.org's e2-micro VM before tagging v0.16.0. No OOM, console accessible at `/_sky/console`, all five tabs render real data.

### v0.16.1

| PR | SHA | Work |
|---|---|---|
| PR1 | `7e36d152` `b355eb80` | `/_sky/*` namespace reserved in `dispatchRoot` — unmounted paths return plain 404 instead of user's `notFound` page (closes info-leakage class). |
| PR2 | `9f568b53` `94334596` `23be9b52` | Atomic `inlineConsoleHealthy` / `legacyConsoleHealthy` flags. Legacy `/_sky/console` HTML shell skipped when inline is healthy. Boot-time fatal when `SKY_CONSOLE_AUTH` is set but neither path mounts. 13 specs. |
| 61cf4c3c | (pre-PR audit) | `__Host-sky_console` cookie now uses `Path=/` per RFC 6265bis §4.1.3.2 — the `__Host-` prefix requires it. |
| PR3 | `ca48b087` `cb5f1d3e` `96f2fbac` | Isolated SSE channel: `/_sky/console/_sse` + `/_sky/console/_event` with own session map / queue / drop counter (not shared with host `/_sky/sse`). Auth reuses `__Host-sky_console` cookie. 13 specs. **Transport-only — full live-UI interactivity is v0.16.2 (#429).** |

The host-app SSE channel and the console SSE channel cannot cross-contaminate sessions, queues, or drop counters — verified by `TestConsoleSSE_IsolatedFromHostSession`.

## Memory budget on e2-micro

Sky.Live app on e2-micro (1 GB RAM, 768 MB budget per systemd):

| Component | RAM |
|---|---|
| Sky.Live runtime + app code | ~30-50 MB |
| Embedded console UI handlers | ~5-10 MB |
| Telemetry ring buffer | ~5 MB |
| SQLite WAL + page cache | ~64 MB mmap (OS-pageable, not Go heap) |
| Exporter + spool (if hub configured) | ~5-15 MB |
| **Total Go heap** | ~50-80 MB |
| **Total RSS** | ~120-200 MB |

Headroom against 768 MB budget: 4-6×. Comfortable. The v0.15.x OOM cause was the subprocess-go-build, which we've deleted.
