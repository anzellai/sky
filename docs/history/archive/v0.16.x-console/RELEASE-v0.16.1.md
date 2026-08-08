# v0.16.1 — collective fixes + HubExporter (collector)

Released after v0.16.0's embedded-console hardening. Branch: `feat/v0.16.1`.
Predecessor: `9a17613e` (v0.16.1 audit). Final HEAD: `a3dcbf94`.

## What landed

### Hardening (PRs 1-3) — fix-class scope

| PR | SHAs | Surface |
|---|---|---|
| PR1 — `/_sky/*` namespace reservation | `7e36d152` `b355eb80` | `dispatchRoot` returns plain `http.NotFound` for unmounted `/_sky/*` paths instead of falling through to the user's `notFound` page. Closes the info-leakage class of #438 (mistyped `/_sky/conslole` rendering the app's 404 surface). |
| PR2 — console mount precedence + boot invariant | `9f568b53` `94334596` `23be9b52` | Atomic `inlineConsoleHealthy` / `legacyConsoleHealthy` flags. Legacy `/_sky/console` HTML shell skipped when inline is healthy (resolves ordering-dependent first-write-wins). Boot-time fatal when `SKY_CONSOLE_AUTH` is set but neither inline nor legacy mounted — prevents silent auth-shipping-but-no-console state. |
| pre-PR — cookie RFC compliance | `61cf4c3c` | `__Host-sky_console` cookie issued with `Path=/` per RFC 6265bis §4.1.3.2. The pre-fix `Path=/_sky/console` was rejected by strict browsers and curl. |
| PR3 — isolated SSE channel | `ca48b087` `cb5f1d3e` `96f2fbac` | `/_sky/console/_sse` + `/_sky/console/_event` get their own session map, queue, drop counter, heartbeat — never cross-contaminate with the host app's `/_sky/sse`. **Transport-only.** Full live-UI dispatch deferred to v0.16.2 (task #429). |

### HubExporter (PRs 4-5) — in-process collector

| PR | SHAs | Surface |
|---|---|---|
| PR4 — exporter core | `360a90af` `fda63d28` `da4d3364` | `runtime-go/rt/exporter.go` (~880 LOC). `HubExporter` with channel-based writer + drainer goroutines. Submit hot-path API (non-blocking, never waits). Bounded ring (5 MB default). OTLP HTTP+JSON transport (gRPC deferred — no `otlptracegrpc` in `go.sum`, avoiding 4+ MB dep surface). Circuit breaker (50 failures → 30 s open → half-open probe → closed). Priority backpressure (errors + metrics never dropped). `runtime-go/rt/shutdown.go` LIFO registry — SIGTERM drain coordinates via `RegisterShutdownHook` / `RunShutdownHooks(8 * time.Second)`. |
| PR5 — durable + memory spool + auto-detect | `2ad839a0` `a3dcbf94` | `runtime-go/rt/exporter_spool.go` (~720 LOC). `fileSpool` (sqlite WAL via existing `modernc.org/sqlite` dep), `memorySpool` (bounded RAM), `resolveSpoolConfig` reading env (`SKY_CONSOLE_SPOOL_MODE` / `_PATH` / `_RETENTION` / `_MAX_BYTES`). Auto-detect via existing `rt.IsServerless()` (`K_SERVICE` / `AWS_LAMBDA_FUNCTION_NAME`). Retention sweep + circular truncation. Crash-resilience: spool writes BEFORE OTLP push, row deleted on push ack only — boot replays unacked rows. |

## Gate results

`TestHubExporter_HotPathNeverBlocks` is the architectural gate — Submit must stay sub-millisecond p99.99 under hub-unreachable conditions.

| Configuration | p99.99 | Throughput | Status |
|---|---|---|---|
| PR4 baseline (no spool) | 96.08 µs | 2.38M Hz | ✅ |
| PR5 file-mode (no `-race`) | 34.0 – 98.4 µs | — | ✅ |
| PR5 file-mode + simulated 100 ms fsync | 214.6 µs | — | ✅ (spool is async) |
| PR5 with `-race` enabled | 100.2 µs | — | ✅ |

All under the 1 ms ceiling by 10× minimum.

Other reliability tests:

| Test | Result |
|---|---|
| `TestHubExporter_MemoryBounded` | queueBytes capped at 95 % of ring; heap delta -525 KB after 200k Submits |
| `TestHubExporter_PriorityDrops` | 243 DEBUG dropped at backpressure; 0 ERROR/METRIC dropped |
| `TestHubExporter_CircuitOpen` | closed → open at exactly 50 consecutive failures; half-open probe restored closed state |
| `TestHubExporter_SIGTERMDrain` | 1000 Submits → 100 % delivered in 26.8 ms (well under 8 s budget) |
| `TestSpool_CrashResilience` | 50 batches persisted under simulated kill, replayed on next boot |
| `TestSpool_AutoDetect_*` | 4 mode-resolution paths verified |
| `TestSpool_RetentionDeletesOldRows` | sweep deletes pre-window rows in both modes |
| `TestSpool_CircularTruncation` | oldest rows evicted past `maxBytes`; counter incremented |
| `TestConsoleSSE_IsolatedFromHostSession` | host + console SSE concurrent — no cross-bleed |

Full `go test ./rt/`: 7.820 s, all green.

## Production verification — sky-lang.org canary

`feat/v0.16.0` had already been canary'd to sky-lang.org's e2-micro (`8c2f4a9e…`). The v0.16.1 cookie fix was verified end-to-end at deploy time:

```
=== rebuild + redeploy sky-lang.org ===
Compilation successful
Build complete: sky-out/app
Jun 03 08:38:42 sky-lang-org sky-lang-org[22078]: [sky.console] inline console mounted at /_sky/console mode=token
Jun 03 08:38:42 sky-lang-org sky-lang-org[22078]: Sky.Live listening on :8000
==> done

=== verify cookie flow end-to-end ===
POST login HTTP 303
Cookie captured: dG9rZW4tYXV0aA.1780490331.9YmLxn_M2cJzOg6NCyPlF3ADHb-1LxLy9DkNBpdeLEw
GET console HTTP 200 (expect 200)
```

Full v0.16.1 (with HubExporter wired-in step from v0.16.2) needs sky-lang.org redeploy + a clean canary before tag.

## Known carry-overs to v0.16.2

These were scoped OUT of v0.16.1 per the audit:

1. **gRPC OTLP transport** — currently HTTP+JSON. Adding `otlptracegrpc` adds 4+ MB to the binary. Defer until benchmarks demand it.
2. **Console live-UI dispatch** — PR3 ships only the wire surface. The bundled console_app's `update` loop still needs plumbing into `ConsoleEventChannel` + `ConsoleSSEBroadcast`. Task #429.
3. **HubExporter auto-start** — `NewHubExporter()` reads env + returns nil when unset; explicit `Start(context.Background())` is required in app boot. v0.16.2 wires this into `liveAppRun` + `Server_listen` automatically.

## SkyDeploy bump

After v0.16.1 tag: `SKY_VERSION` bump from `0.16.0` → `0.16.1` across the five Dockerfile refs in `~/works/playground/skydeploy/`. Tracked as task #433.

## PR10 — `MountLiveSubAppInProcess` primitive + telemetry namespace foundation

Landed 2026-06-03. Two atomic commits:

- `efa26137` — `feat(rt): MountLiveSubAppInProcess + service.namespace propagation (PR10-B/C/D)`
- `68cc04ab` — `docs(v0.16.x-console): TELEMETRY_FLOW.md + examples/34-multi-tier-console (PR10-H/I/J)`

### What PR10 ships

1. **`rt.MountLiveSubAppInProcess(parentMux, prefix, cfg)`** — new
   public primitive at `runtime-go/rt/subapp_inprocess.go`. Mounts a
   second Sky.Live app on the parent's mux under a path prefix, with
   its own session map / sky-id namespace / broker. Backed by
   11 regression tests covering sanitisation, registry shape,
   longest-prefix matching, double-mount panic, and zero-cost in
   the empty-registry case.

2. **`service.namespace` propagation middleware** —
   `runtime-go/rt/telemetry_namespace.go` adds `WithSubAppNamespace`
   (wired into `ObservabilityMiddleware`) that stamps every request
   context with the matching sub-app's prefix as a namespace label.
   Foundation for per-app filtering in the console UI.

3. **`docs/v0.16.x-console/TELEMETRY_FLOW.md`** — design doc with
   ASCII diagrams for the four telemetry topologies (single-process
   host, single-process nested sub-apps, fork+exec MountSubApp,
   distributed hub) + cookie/sky-id namespace decisions + privacy-mode
   fallback.

4. **`examples/34-multi-tier-console/`** — one Sky.Live binary with
   four logical tiers (host / billing / jobs / auth) each emitting
   `service.namespace`-tagged logs. Console aggregates all four into
   one pane. Playwright e2e (13 PASS assertions, ALL GREEN).

### What PR10 explicitly defers to v0.16.2

PR10-E/F/G (refactoring the inline console itself to use
`MountLiveSubAppInProcess` and deleting the parallel
`console_loop.go` / `console_sse.go` / `console_app_hooks.go`
~1750 LOC) deferred. The console's current parallel infra
entangles four process-global hooks (consoleAuth slot, processBroker,
SKY_PARENT_URL env seed, CSRF-vs-isolated-SSE separation) that need
their own decomposition cycle. Per the RFC's "Why E/F/G defer"
section, the cleanest landing site is v0.16.2 alongside the
hub-mode console source unification.

### Acceptance criteria

| Criterion | Status |
|---|---|
| `go test ./rt/...` green | ✅ |
| `examples/34-multi-tier-console/` builds + Playwright green | ✅ |
| `docs/v0.16.x-console/TELEMETRY_FLOW.md` 4-topology design | ✅ |
| Existing `console-click-test.mjs` against examples/09 still green | ✅ |
| `cabal test` clean | pending (run before tag) |
| sky-lang.org canary | pending (separate redeploy) |
