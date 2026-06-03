# v0.16.1 — Collective fixes + In-process Collector: Auditor Report

> Date: 2026-06-03. Branch: `feat/v0.16.1` (clean, on top of main @ 61cf4c3c).
> Auditor: read-only. No code changes made.

This audit answers: "Are the 10 v0.16.1 work items achievable on top of
the v0.16.0 surface, and what's the safe PR ordering?"

## 1. Current state — files + functions

### Item 1 — Cookie Path=/ fix (already shipped)

Commit `61cf4c3c fix(console): __Host-sky_console cookie must use Path=/ per RFC`.
Verified at `runtime-go/rt/console_auth_v2.go` cookie-issuing call sites. NO
follow-up needed in v0.16.1; the audit confirms the fix landed.

### Item 2 — `/_sky/*` namespace reservation in `dispatchRoot`

- `runtime-go/rt/live.go:2755` `func (app *liveApp) dispatchRoot(w, r)` —
  the catch-all "/" route. Today: walks `app.api`, then renders
  `handleInitial` on GET/HEAD. `handleInitial` calls `applyRoute` which on
  no-route + no-session falls through to **`app.notFound`** (line 2894).
  **Bug**: unmounted `/_sky/foo` paths (e.g. typoed `/_sky/conslole`) become
  `Page = NotFoundPage` and render the user's notFound view.
- `runtime-go/rt/live.go:3290` `isBrowserNoisePath` already 404s
  favicons/etc BEFORE the session path; the same shape applies to
  `/_sky/*`. New helper `isReservedFrameworkPath(p)` slots in here.
- `runtime-go/rt/csrf_middleware.go:261` `isObservabilityPath` already
  treats `/_sky/console*` + the static set as framework-owned for CSRF
  purposes — the namespace concept exists, it just isn't enforced at
  dispatch.

### Item 3 — Inline vs legacy mount precedence

- Inline mount: `runtime-go/rt/console.go:111` `MountEmbeddedConsole`
  registers `/_sky/console` (+ `/`) via `MountInlineConsole` →
  `console_app/mount.go:64` `MountInlineConsole`.
- Legacy mount: `runtime-go/rt/console.go:72` `MountConsoleEndpoints`
  unconditionally calls `safeMount(mux, "/_sky/console", HandleConsole)`
  (line 81). The dedup is guarded by `safeMount` (`observability.go:259`,
  panic-recover loop) — silently no-ops when the pattern is already
  taken. JSON API endpoints (`/api/overview` etc.) are MORE specific so
  they coexist.
- `MountObservabilityEndpoints` (observability.go:235) calls
  `MountConsoleEndpoints` at L253 — runs AFTER `MountEmbeddedConsole`
  (live.go:3073) so inline wins. Tested in `console_test.go:336`
  (`MountConsoleEndpoints` alone) and `console_auth_v2_test.go:413`
  (full `MountEmbeddedConsole`).

**The problem the v0.16.1 spec describes**: "exactly ONE of {inline,
legacy} must serve `/_sky/console`". Today both register handlers and
`safeMount`'s panic-recover makes the order accidental — the FIRST one
wins, legacy silently no-ops without diagnostic. Boot-time invariant
check is missing: when `SKY_CONSOLE_AUTH` is set but inline mount
declined (e.g., `ErrInlineConsoleUnavailable` because the binary
shipped without `_ "sky-app/rt/console_app"` blank import), we silently
fall through to the legacy HTML shell — wrong behavior on a production
deploy expecting the gated inline UI.

### Item 4 — Isolated SSE channel for inline console_app

This is the **largest architectural surprise** in v0.16.1.

- Today, `console_app/mount.go` `handleConsoleRoot` (line 99) does a
  **one-shot server-side render**: it builds a `State_Model_R` via
  `init_`, calls `viewWrapped`, renders to HTML, wraps in a static
  HTML5 shell with NO Sky.Live client JS. The console UI works but is
  pure-static — no SSE, no Click→Msg dispatch, no live updates.
- Host's Sky.Live owns `/_sky/event` + `/_sky/sse` (live.go:3056-7).
  Inline JS at live.go:6037/6140/6743/6976 hardcodes `__skyBase + "/_sky/event"`
  and `__skyBase + "/_sky/sse"`. So even if the console rendered
  Sky.Live's client JS, both apps would collide on session-IDs +
  state — the host's `liveSession.handlers` map vs the console's.
- The console is logically a **second Sky.Live app inside the same
  process**. There's currently no API to mount a second one with its
  own event + SSE plumbing. We'd need:
   1. A second `liveApp` instance constructed for the console (`Live_app`-equivalent over the bundled console code).
   2. Routes scoped to `/_sky/console/_event` + `/_sky/console/_sse`.
   3. A way to inject `__skyBase = "/_sky/console"` into the rendered HTML so the console's inline JS prefixes correctly.
   4. Console session cookie keyed by `__Host-sky_console` (already done in v0.16.0 PR3 for auth gate; needs to extend to Sky.Live session-cookie name).
   5. CSRF middleware path scoping — currently treats `/_sky/console*` as observability (skip). Console POSTs to `/_sky/console/_event` must EITHER bypass CSRF (cookie auth is sufficient given Same-Site Strict + Path=/ + HttpOnly) OR carry their own console-scoped CSRF token. **Reach for the bypass** — simpler, less code, no double-token complexity.

### Items 6-10 — In-process collector / exporter

- **New file**: `runtime-go/rt/exporter.go` (~700 lines). The doc says
  it's a "goroutine pair" — writer (non-blocking ingest) + drainer
  (OTLP push). Note name collision concern: `runtime-go/rt/observability_push.go`
  already defines `PushExporter` for sub-app→parent ingest federation.
  These are DIFFERENT concepts:
    - existing `PushExporter` (observability_push.go:49) pushes to
      `<parent>/_sky/observability/ingest` (JSON over HTTP, sub-app
      federation, 2s batch).
    - new EXPORTER.md exporter pushes to `SKY_CONSOLE_HUB` via OTLP
      (gRPC primary, HTTP fallback) with disk spool + circuit breaker.
- Suggest naming: `HubExporter` / `HubExporterStart` / `HubExporterStop`
  to keep clear of the existing `PushExporter`.
- Spool dependency `modernc.org/sqlite` is already in `runtime-go/go.mod`
  (no-cgo). gRPC + `go.opentelemetry.io/proto/otlp` are already imported
  (used by tracing). Net-new deps: none required.
- SIGTERM hook: `runtime-go/rt/live.go:3216-3251` already installs the
  Sky.Live SIGINT/SIGTERM/SIGHUP handler. The exporter drain needs to
  hook into this BEFORE `srv.Close()` (line 3241). Same handler exists
  in Sky.Http.Server (`Server_listen` in `rt.go` around L7670). Best
  approach: **`HubExporterStart()` registers a teardown closure in a new
  `runtime-go/rt/shutdown.go` registry**; both server runtimes invoke
  the registry inside their existing signal handler.
- Self-observability metrics: `runtime-go/rt/observability.go`
  `MountObservabilityEndpoints` calls `telemetry.Default().WriteProm(w)`
  at L214. New `sky_telemetry_*` metrics register against
  `telemetry.Default()` via existing `IncCounter` / `SetGauge` helpers
  in `runtime-go/rt/telemetry/store.go`.

## 2. Design vs reality — surprises

### S1: SSE channel — the inline console isn't a Sky.Live app yet

The console_app today is a **one-shot HTML renderer**, not a running
Sky.Live instance. Mount logic just calls `init_` + `viewWrapped` per
request. The spec wants a second Sky.Live app with its own event + SSE
+ session loop. The minimum-viable shape:

1. Build a `*liveApp` for the bundled console at first request. Cache it.
2. Register `/_sky/console/_event` + `/_sky/console/_sse` handlers that
   delegate to the console's `handleEvent` + `handleSSE` with a forced
   `basePath = "/_sky/console"`.
3. Make `live.go`'s SSE/event handlers tolerant of being mounted under
   a sub-path — `__skyBase` is already done correctly via
   `liveAppCfg.BasePath` (live.go:2620+), so this works if we
   construct the console's `liveApp` with `basePath = "/_sky/console"`.
4. The console's session cookie must be DIFFERENT from the host's. Today
   `sessionID` (live.go ~L1900) reads `sky_sid` — the host owns that. The
   console reuses `__Host-sky_console` (already set by v0.16.0 auth
   gate). We need a session-cookie-name field on `liveApp` so the
   console instance keys off `__Host-sky_console` and the host stays on
   `sky_sid`. Or simpler: the console session **derives** from the auth
   cookie itself — no separate Sky.Live session, the auth cookie IS the
   session key. This avoids a second cookie + a second session store.
5. CSRF: `isObservabilityPath` treats `/_sky/console*` as bypass-all
   already (csrf_middleware.go:266). Console-app POSTs to
   `/_sky/console/_event` therefore pass freely. **No CSRF middleware
   change required** — but the bypass is intentional, document it.

**Recommendation**: split item 4 across two PRs. v0.16.1 ships the
plumbing (separate SSE channel + isolated session keying), v0.16.2
follows up with **interactive Msg dispatch testing**. Reasoning: the
Std.Ui Sky-source console has ~1.6k lines and many click handlers; an
interactive end-to-end gate needs Playwright drive against the live
session-store-routed event loop + SSE patches. Doable but more than 1
PR's worth of test surface. Plumbing alone is mechanical and ships
cleanly.

### S2: Exporter naming collision — call the new one HubExporter

`observability_push.go` already owns `PushExporter`. Don't shadow it.
**`HubExporter` / `StartHubExporter` / `StopHubExporter`** keep the
two clearly distinct: one pushes to the parent process (sub-app
federation, JSON-over-HTTP, existing v0.15 surface), one pushes to a
remote hub (OTLP, new v0.16.1 surface). Both can coexist (sub-app
federation routes telemetry parent-ward, then the parent's
HubExporter routes the aggregated stream onward).

### S3: SIGTERM drain — coexist with Sky.Live's existing signal handler

Sky.Live's signal handler at live.go:3216-3251 currently calls
`SetReady(false)`, `ShutdownTracing`, `JobsShutdown`, `srv.Close()`.
Add `HubExporterDrain(8 * time.Second)` BEFORE `srv.Close()` (after
`ShutdownTracing` — that way OTel spans flush first, then the exporter
catches anything in-flight, then the listener closes).

Sky.Http.Server has the same pattern (search `rt.go` for `signal.Notify`).
Both call sites need the same drain. Best to define
`RegisterShutdownHook(fn func(deadline time.Duration))` in
`shutdown.go` and have both handlers call `runShutdownHooks(8 * time.Second)`.

Risk: the existing `defer rt.LogPanicAndExit()` at `func main()` head
(per CLAUDE.md §"Synchronous-panic gate") runs on synchronous panics,
NOT on SIGTERM. So drain in the SIGTERM handler is the right surface.
No coexist issue.

### S4: Spool path resolution — depends on project name (v0.16.0 deliverable)

`<dataDir>/<projectName>.spool.db` mirrors `<projectName>.console.db`
(v0.16.0 EMBEDDED.md). The v0.16.0 ld-flag `-X sky-app/rt.projectName=...`
already injects projectName. Reuse the SAME variable; the exporter just
reads it for the spool path. Verify it landed in v0.16.0 (search
runtime-go/rt for `var projectName`).

### S5: `SKY_TELEMETRY_EXPORTER=stdout|noop` interface plumbing

EXPORTER.md item 10 mandates a replaceable exporter interface. Define
`type Exporter interface { Enqueue(Signal); Drain(time.Duration) }` and
have the `otlp` / `stdout` / `noop` variants implement it. Built-in
selector via `SKY_TELEMETRY_EXPORTER`. The selector lives in
`HubExporterStart()`. Public registration API for third parties is
v0.17+ (per the doc); v0.16.1 just needs the three built-ins.

### S6: Memory-mode spool — auto-detection via existing helpers

`runtime-go/rt/serverless.go` already exposes `IsServerless()` (reads
`K_SERVICE` / `AWS_LAMBDA_FUNCTION_NAME`). Reuse it for spool mode
detection. SERVERLESS.md says memory mode + 200ms cadence when
`IsServerless() == true`.

## 3. PR decomposition

6 PRs land cleanly on `feat/v0.16.1`. Order matters: hygiene fixes
first (cheap, isolate surface), then SSE plumbing (architectural,
but contained), then exporter (largest piece, depends on stable
framework surface).

### PR 1 — `fix(rt): reserve /_sky/* namespace at dispatchRoot`
**Scope**:
- New `isReservedFrameworkPath(p string) bool` in `runtime-go/rt/live.go`
  (next to `isBrowserNoisePath`, ~L3290). Returns true for any
  `/_sky/` prefix.
- `dispatchRoot` (live.go:2755): if no api-route match AND path is
  reserved AND no mux handler claimed it (i.e. fell through to dispatchRoot),
  return `http.NotFound` with body `"Sky framework path — not mounted"`,
  NEVER touching `app.notFound`.
- Equivalent guard at `handleInitial` start (live.go:3311): reserved
  path → 404 BEFORE session creation.
- Sky.Http.Server has its own dispatcher; mirror the same guard.

**Verification**: new spec `runtime-go/rt/live_reserved_path_test.go`
asserts unmounted `/_sky/foo` returns 404 with framework body, NOT the
app's notFound view. Run from a Live.app with notFound set to an
identifiable page.

**Blocked by**: nothing. Tiny, mechanical.

**Risk**: low — pure 404 routing fix.

### PR 2 — `fix(console): boot-time invariant when SKY_CONSOLE_AUTH set but no mount`
**Scope**:
- `runtime-go/rt/console.go` `MountEmbeddedConsole` (L111): on
  `ErrInlineConsoleUnavailable` path (L146) when `SKY_CONSOLE_AUTH` is
  explicitly set (token / app), DO NOT silently fall through to the
  legacy HTML shell. Log a FATAL error: "SKY_CONSOLE_AUTH=<m> set but
  console_app not linked — rebuild with the inline console enabled".
  Return without mounting anything. The user gets a missing console
  surface and a loud, traceable diagnostic at boot.
- Delete the duplicate legacy `safeMount` call for `/_sky/console` HTML
  shell when inline mount succeeded (the JSON API endpoints stay —
  they're more-specific patterns and serve fresh telemetry).
  Replacement: `MountConsoleEndpoints` only mounts the JSON API, NOT
  the HTML shell; inline owns the shell.
- Delete `consoleHTML` const + `HandleConsole` function from
  `console.go` (only the HTML shell goes; JSON handlers stay).
  Validation: grep that nothing references them.
- Test updates: `console_test.go` HTML body assertions get rewritten to
  hit `MountEmbeddedConsole` + assert the inline HTML markers.

**Verification**: new test cases —
- `TestMountEmbeddedConsole_AuthSetButInlineUnavailable_LogsFatalDoesntMount`
- `TestMountConsoleEndpoints_JsonOnly_NoHtmlShell`

**Blocked by**: PR 1 (so `/_sky/console` is properly framework-namespace-owned when nothing mounts it).

**Risk**: low — legacy HTML shell deletion clarifies precedence; existing tests adapt to inline-only.

### PR 3 — `feat(console): isolated SSE channel for inline console_app`
**Scope**:
- New file `runtime-go/rt/console_live.go` (~250 lines): builds a
  `*liveApp` from the bundled console source at first request. Caches
  it (sync.Once). The console's `basePath = "/_sky/console"` so its
  inline JS prefixes correctly.
- Mount: `/_sky/console/_event` (POST → console liveApp.handleEvent),
  `/_sky/console/_sse` (GET → console liveApp.handleSSE). Done from
  `MountEmbeddedConsole` after the auth gate decision.
- Session derivation: the console session is keyed off the
  `__Host-sky_console` auth cookie value (so a single user session
  drives both the auth surface AND the Sky.Live session — no second
  cookie). New helper `liveApp.sessionIDFromCookie(cookieName, cookieVal)`.
- CSRF: confirm `isObservabilityPath` already bypasses
  `/_sky/console*`. Document the bypass with an explicit comment
  pointing here; auth cookie + Strict same-site + Host-prefix provides
  the equivalent guarantee.
- Update `console_app/mount.go` `handleConsoleRoot` to register the
  inline JS bundle with `__skyBase = "/_sky/console"` (the per-app
  base-path injection mechanism in live.go:5430-5455 already supports
  this — we just need to pass the basePath through).
- **DEFER interactive Click-Msg verification to v0.16.2**. PR 3 ships
  the plumbing + smoke tests (SSE connects, hello frame arrives, one
  POST round-trips). Full interactivity over Playwright in v0.16.2.

**Verification**:
- New runtime test `console_live_test.go`: GET `/_sky/console/_sse`
  returns 200 with `text/event-stream`, receives `event: hello`. POST
  to `/_sky/console/_event` with a no-op msg returns 200 + valid
  envelope.
- Smoke: spin up `examples/09-live-counter`, set
  `SKY_CONSOLE_AUTH=token SKY_CONSOLE_TOKEN=<32hex>`, open both
  `/` and `/_sky/console` in two browser tabs, verify host's
  `/_sky/sse` heartbeat continues + console's `/_sky/console/_sse`
  heartbeat runs independently.

**Blocked by**: PR 2.

**Risk**: medium — second Sky.Live in the same process is a new
runtime topology. Mitigation: bundled console source is small + well-
tested (v0.16.0 PR 4 shipped), the host's session-store contract is
unchanged.

### PR 4 — `feat(rt): HubExporter — channel-based writer + drainer + OTLP transport`
**Scope**:
- New file `runtime-go/rt/exporter.go` (~700 lines): `HubExporter`
  struct, `Exporter` interface, `StartHubExporter` / `StopHubExporter`
  selectors. gRPC primary via existing `go.opentelemetry.io/proto/otlp`
  imports. HTTP fallback at port 4318.
- Hot-path API: `HubExporterPushLog(LogEntry)` / `PushMetric` /
  `PushSpan` — non-blocking, drop-and-count on full ring.
- New file `runtime-go/rt/shutdown.go`: process-wide shutdown-hook
  registry. `RegisterShutdownHook(fn func(time.Duration))` and
  `runShutdownHooks(deadline time.Duration)`.
- Wire-in: Sky.Live signal handler (live.go:3217-3251) calls
  `runShutdownHooks(8 * time.Second)` BEFORE `srv.Close()`.
  Sky.Http.Server signal handler (rt.go near L7670) same.
- Wire-in: `liveAppRun` and `Server_listen` call `StartHubExporter()`
  in their boot sequence (next to the existing `StartPushExporter()`
  call at live.go:3077).
- Telemetry hot-path tie-in: `telemetry.AppendLog` /
  `IncCounter` / etc. fan out to `ActiveHubExporter()` when non-nil
  (single sync.atomic.Pointer load per call, <50 ns overhead).

**Verification** (gates the patch):
- New file `runtime-go/rt/exporter_test.go` (~500 lines).
- `TestHubExporter_NeverBlocksHotPath`: 10k `PushLog`/s for 60s with
  hub unreachable, assert p99.99 < 1ms.
- `TestHubExporter_DroppedCounterIncrementsWhenFull`: fill ring at 10×
  drain rate, assert `sky_telemetry_dropped_total` counter rises.
- `TestHubExporter_GrpcConnectAndPush`: stub OTLP gRPC server, verify
  batched push arrives within 2s in file-mode.
- `TestHubExporter_HttpFallbackOnGrpcUnavailable`: stub gRPC returning
  Unavailable 3×; verify HTTP fallback fires.
- `TestHubExporter_CircuitBreakerOpensAfterFailures`: stub server
  returns 500 × 50; verify circuit opens for 30s + counter
  `sky_telemetry_circuit_state{state="open"}` flips to 1.
- `TestHubExporter_SigtermDrainCompletesWithin8s`: load 10k entries, call
  Drain(8 * time.Second), assert all entries pushed.

**Blocked by**: PR 3 — needs the framework boot surface to be stable
before adding a new goroutine pair.

**Risk**: high (largest single piece; networking + concurrency).
Mitigation: the "never blocks hot path" test gates the patch — must
pass before merge. Circuit breaker + drop-and-count protect the app
regardless of exporter health.

### PR 5 — `feat(rt): file + memory spool modes + auto-detect`
**Scope**:
- New file `runtime-go/rt/exporter_spool.go` (~400 lines).
- File mode: `<dataDir>/<projectName>.spool.db` (SQLite WAL via
  `modernc.org/sqlite`). 50MB cap. Schema: single table
  `outgoing_batch(id INTEGER PRIMARY KEY, payload BLOB, attempt INT)`.
  Insert on enqueue-fail; delete on push-success.
- Memory mode: 5MB ring (no SQLite). Per SERVERLESS.md: 200ms cadence,
  3-attempt max, eager push.
- Auto-detect via `IsServerless()` (existing
  `runtime-go/rt/serverless.go`). Override via `SKY_CONSOLE_SPOOL=auto|file|memory|none`.
- Priority-aware drop: at 80% spool, drop DEBUG → INFO → fast spans.
  At 95%, reject WARN+ at writer. Errors + metrics NEVER dropped (per
  EXPORTER.md item 8).
- New self-observability metrics emitted to `telemetry.Default()`:
  `sky_telemetry_spool_size_bytes`, `sky_telemetry_circuit_state`,
  `sky_telemetry_dropped_total{level,signal}`,
  `sky_telemetry_push_attempts_total`, `sky_telemetry_push_failures_total{reason}`,
  `sky_telemetry_export_duration_seconds` histogram,
  `sky_telemetry_spool_mode` gauge (0=file, 1=memory, 2=none).

**Verification**:
- `TestSpool_FileMode_SurvivesProcessRestart`: write 100 entries, kill
  & re-open the exporter, verify entries pushed on next drain.
- `TestSpool_MemoryMode_DropsOnOom_3xRetryThenDrop`: fill 5MB ring,
  push fails 3×, assert entries dropped + counter.
- `TestSpool_K_SERVICE_TriggersMemoryMode`: set `K_SERVICE=test`, init
  exporter, assert `sky_telemetry_spool_mode=1`.
- `TestSpool_PriorityDrop_AtCapacity`: fill spool to 81%, write DEBUG
  + ERROR, assert DEBUG drops + ERROR retained.

**Blocked by**: PR 4.

**Risk**: medium. SQLite spool needs careful WAL config + EXCLUSIVE
lock so multi-instance writes (Cloud Run replicas with persistent
volume) don't corrupt. Lock-skip fallback to memory mode on EBUSY.

### PR 6 — `docs(v0.16.1): env vars, OPS guide, SkyDeploy adoption recipe`
**Scope**:
- Update `docs/v0.16.x-console/EXPORTER.md` with final implementation
  notes (which deltas vs design).
- New section in `CLAUDE.md` § "Environment variables" for
  `SKY_CONSOLE_HUB`, `SKY_CONSOLE_HUB_TOKEN`, `SKY_CONSOLE_SPOOL`,
  `SKY_CONSOLE_SPOOL_MAX_MB`, `SKY_CONSOLE_BATCH_INTERVAL_MS`,
  `SKY_TELEMETRY_RING_BYTES`, `SKY_TELEMETRY_EXPORTER`,
  `SKY_CONSOLE_HUB_TLS_INSECURE`.
- Update `templates/CLAUDE.md` mirror (mandatory per CLAUDE.md).
- New SkyDeploy migration recipe in
  `docs/v0.16.x-console/MIGRATION.md` referencing the SKY_VERSION bump
  + the env-var injection step on tenant Cloud Run.
- Release notes draft for v0.16.1.

**Verification**: `sky doc --serve` renders new entries cleanly.

**Blocked by**: PR 5.

**Risk**: low (docs only).

## 4. Test plan

**New runtime-go tests (5 files, ~30 cases)**:
- `runtime-go/rt/live_reserved_path_test.go` (PR 1)
- `runtime-go/rt/console_test.go` — restructured for PR 2; new
  `TestMountEmbeddedConsole_AuthSetInlineUnavailable`
- `runtime-go/rt/console_live_test.go` (PR 3) — SSE channel isolation
- `runtime-go/rt/exporter_test.go` (PR 4) — the 10-point checklist;
  the "never blocks hot path" test gates the patch
- `runtime-go/rt/exporter_spool_test.go` (PR 5) — spool semantics + auto-detection

**Cabal specs**: zero new. v0.16.1 is purely runtime work (no
compiler / type-system / stdlib changes). Skip the cabal-test cost
for this cycle.

**Existing tests that change**:
- `console_test.go` — `MountConsoleEndpoints` no longer mounts HTML
  shell; remove HTML body assertions; JSON-API tests stay.
- `console_auth_v2_test.go` — no change.
- `console_inline_test.go` — no change.

**Existing examples that change**: zero. No Sky source changes;
existing examples continue to build byte-identical.

## 5. Release-gate sweep for v0.16.1

Adapted from CLAUDE.md "Release checklist". Order matters.

1. `cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky` — clean rebuild.
2. `sky-out/sky --version` — smoke (must print v0.16.1).
3. `scripts/regenerate-console.sh && git diff --exit-code runtime-go/rt/console_app/` — drift check.
4. `timeout 1800 cabal test` — full Haskell suite (no v0.16.1 specs but
   confirm no v0.16.0 regression).
5. `cd runtime-go && timeout 600 go test ./...` — runtime suite. The
   `TestHubExporter_NeverBlocksHotPath` test is the load-bearing gate.
6. Clean-build sweep: `for d in examples/*/; do (cd "$d" && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky); done`.
7. `scripts/verify-all-web.sh` — Sky.Live + Sky.Http.Server Playwright
   canaries. Verify `/_sky/console` AND host `/_sky/sse` work
   independently in two tabs.
8. `scripts/verify-cli.sh` — CLI / Tui / Cli sweep.
9. `cd examples/12-skyvote && sky check` — large-example HM/build smoke.
10. From-scratch `sky init mytest && sky build && sky run`.
11. **The sky-lang.org canary**: deploy v0.16.1 binary to sky-lang.org's
    e2-micro VM. Confirm console renders, host SSE works, RSS < 200 MB.
12. **Hub-side smoke (optional, recommended)**: stand up a local OTLP
    collector (jaeger / minimal grpc server), set `SKY_CONSOLE_HUB`
    on a local sky binary, generate 1k log entries, assert all arrive
    within 10s.
13. **Cloud Run smoke (recommended for v0.16.1)**: deploy a sample app
    to Cloud Run with `K_SERVICE` set, `SKY_CONSOLE_HUB` pointed at the
    local OTLP collector via Cloud Run egress, verify memory-mode
    exporter pushes within 200ms cadence.

## 6. SkyDeploy migration considerations

The v0.16.1 surface touches SkyDeploy in three ways:

- **Cookie Path=/ fix** (already shipped) — tenants on v0.16.0 had a
  cookie-scoped-to-`/_sky/console` issue; v0.16.1 carries the v0.15.x
  cookie-path-/ behaviour.
- **Isolated SSE channel** — tenants who use the Pro+ Sky Console JWT-
  in-URL handshake should be unaffected (handshake still hits
  `/_sky/console` GET, which the inline mount serves). Need a smoke
  test against an existing tenant on dev.skydeploy.app.
- **HubExporter** — opt-in via env. Tenants that don't set
  `SKY_CONSOLE_HUB` get zero behaviour change. SkyDeploy can offer a
  per-tenant flag to inject `SKY_CONSOLE_HUB` pointing at the future
  v0.16.2 hub (deferred — v0.16.1 ships the exporter, v0.16.2 ships
  the hub receiver).

**SkyDeploy v0.16.1 bump checklist** (5 files in
`~/works/playground/skydeploy`):
1. `sky-tools/Dockerfile` — `SKY_VERSION=0.16.1`
2. `deploy/Dockerfile` — same
3. `agent-service/Dockerfile` — same
4. `build-image/Dockerfile` — same
5. `control-plane/deploy/setup-remote.sh` — same

Then commit + push origin main, then `timeout 1200 bash control-plane/deploy/deploy.sh`.
Per CLAUDE.md §5: park gracefully on gcloud auth fail; warn user.

## Summary of cycle scope + honest recommendation

10 items, 6 PRs, ~6-8 days of work. The biggest risk is **PR 3 (SSE
channel) + PR 4 (HubExporter)** landing back-to-back without enough
shared-test rotation. Recommend:

- PRs 1-2 land **fast** (low-risk hygiene). Within 1-2 days.
- PR 3 lands as **plumbing-only** — the bundled console interactively
  works with click-Msg in v0.16.2 after Playwright gates are written.
- PRs 4-5 (exporter) land as the **core deliverable** — the 10-point
  production-grade checklist is the gate. Run the 10k-entry "never
  blocks hot path" test as a CI gate.
- PR 6 docs ship with PR 5.

**Scope-cut honesty**: PR 3 (isolated SSE channel) IS interactive Sky.Live
inside the v0.16.1 cycle, BUT we ship the plumbing only and defer the
full Playwright-driven Click-Msg interactivity gate to v0.16.2. The
console RENDERS today (one-shot, no live updates); v0.16.1 wires the
channel; v0.16.2 verifies interactivity at sky-lang.org canary scale.
This keeps the cycle shippable without piling new test surface onto an
already-big exporter PR.

End artefact: v0.16.1 binary with the in-process collector ready for
production, the `/_sky/*` framework namespace hardened, and the inline
console mount path made unambiguous + diagnosable.
