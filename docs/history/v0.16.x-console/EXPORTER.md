# Sky Console v0.16.x — In-process Exporter (Collector)

> The exporter is the reliability lever for hub mode. This doc covers
> design, production-grade checklist, and concrete scalability numbers.
> Implementation lands in v0.16.1.

## What it is

A goroutine pair that lives inside every Sky app:

1. **Writer** — receives telemetry from app code (`Log.*`, `Trace.span`, metric counters). Non-blocking. Writes to an in-memory channel.
2. **Drainer** — consumes the channel in batches, OTLP-encodes, pushes to configured destination(s), handles retry + backpressure + spool persistence.

This is what OpenTelemetry calls a "BatchSpanProcessor" (and equivalents for logs/metrics). Sky's exporter adds: spool-on-disk persistence (where available), priority-aware backpressure, OTLP fan-out to multiple destinations, and serverless-aware mode switching.

## Why in-process (not sidecar / not node-agent)

| Pattern | Sky's verdict |
|---|---|
| **In-process** (ours) | ✓ Single binary. ✓ Serverless-native. ✓ No socket hop. ✓ Latency-friendly. |
| Sidecar collector (OTel Collector pod) | ✗ Breaks single-binary. ✗ Cloud Run-hostile. Wins at >10k req/s + polyglot pods. |
| Node-local agent (DataDog Agent) | ✗ Extra ops surface. ✗ Hard to run on Cloud Run. Wins at many-apps-per-host. |
| Direct OTLP from SDK (no buffering) | ✗ Loses data on hub outage. ✗ Synchronous push hurts latency. |

For Sky's target (small-medium scale, multi-app on small VMs OR serverless), in-process is right. We retain the option to add a node-local agent in v0.17+ if scaling demands; the OTLP wire format means it's drop-in replaceable.

## Two reliability profiles — auto-detected

The exporter detects environment at startup and chooses the right buffering strategy:

```
Detection:
  K_SERVICE                  set → serverless (Cloud Run)
  AWS_LAMBDA_FUNCTION_NAME   set → serverless (Lambda)
  otherwise                       → VM / always-on container
```

| Aspect | File mode (VMs) | Memory mode (serverless) |
|---|---|---|
| Spool location | `<dataDir>/<projectName>.spool.db` (SQLite WAL) | RAM ring buffer |
| Spool cap | 50 MB (configurable) | 5 MB (configurable) |
| Survives process restart | ✓ (durable SQLite) | ✗ (gone on container recycle) |
| Push cadence | 2 s (batched) | 200 ms (eager) |
| Retry on hub failure | Exponential backoff, retry forever until spool fills | 3 attempts max (1s/2s/4s), then drop |
| Worst-case data loss | None unless spool fills | ~5 s of telemetry per container kill |
| SIGTERM drain budget | 8 s (we exit cleanly within 10 s grace) | 8 s |
| Eager exporter spawn | Yes (immediate at runtime init) | Yes (critical — cold-start needs it ready) |

**Override via env:**
```
SKY_CONSOLE_SPOOL=auto    # default, detect via K_SERVICE / AWS_LAMBDA_FUNCTION_NAME
SKY_CONSOLE_SPOOL=file    # force file mode (e.g. Cloud Run with volume mount)
SKY_CONSOLE_SPOOL=memory  # force memory mode
SKY_CONSOLE_SPOOL=none    # disable spool, synchronous push only (testing)
```

## The 10-point production-grade checklist

Every item below MUST be verified by a regression test before the v0.16.1 release.

### 1. Never blocks the hot path

App code calling `Log.info` / `Trace.span` / metric updates MUST return in <1 ms at the 99.99th percentile, regardless of exporter state.

Mechanism: writer goroutine consumes a buffered channel. If channel full → drop-and-count, never wait.

Regression test:
- Synthetic load: 10k `Log.info` calls/sec for 60 seconds, with the hub deliberately unreachable
- Assert: p99.99 of `Log.info` call duration < 1 ms

### 2. Bounded memory

In-memory ring + spool combined must never exceed configured caps.

- In-memory ring: 5 MB / 10k entries (configurable via `SKY_TELEMETRY_RING_BYTES`)
- File spool: 50 MB hard-stop (configurable via `SKY_CONSOLE_SPOOL_MAX_MB`)
- Memory spool: 5 MB hard-stop

Regression test:
- Fill the buffer at 10× the drain rate for 5 minutes
- Assert: RSS growth flatlines at expected cap, doesn't OOM

### 3. Honest failure surface

The exporter ITSELF emits metrics about itself. "Telemetry on telemetry":

```
sky_telemetry_dropped_total{level="debug|info|warn|error",signal="log|metric|span"}  counter
sky_telemetry_push_attempts_total                                                     counter
sky_telemetry_push_failures_total{reason="network|timeout|4xx|5xx|circuit-open"}      counter
sky_telemetry_spool_size_bytes                                                        gauge
sky_telemetry_export_duration_seconds                                                 histogram
sky_telemetry_circuit_state{state="closed|half-open|open"}                            gauge
```

These appear on the app's own `/_sky/metrics` so any external monitor (Prometheus / Cloud Monitoring) can detect a degraded exporter.

### 4. Circuit breaker

After 50 consecutive push failures, enter `open` state:
- No push attempts for 30 s
- All writes still buffer locally (subject to spool cap)
- After 30 s, single heartbeat push as `half-open` probe
- Probe succeeds → return to `closed`. Fails → another 30 s `open`.

Prevents burning app CPU on retry loops when hub is genuinely down.

### 5. Graceful degradation chain

```
Hub reachable + healthy        → all data flows in real-time
Hub reachable + slow           → backoff + spool fills, no data loss yet
Hub unreachable + spool space  → buffer locally, retry on backoff
Hub unreachable + spool full   → drop by priority (see §8)
Spool corrupt / write fails    → fall back to in-memory ring only + stderr warn
Total catastrophe              → app keeps serving requests, telemetry silently degrades, /_sky/metrics shows degraded counters
```

The app NEVER crashes from an exporter failure. The user never knows except via the degraded-counters surface.

### 6. Connection management

- gRPC: single long-lived `grpc.ClientConn` per drainer, keep-alive ping every 30 s, automatic reconnect on `EOF` / `Unavailable`
- HTTP fallback (gRPC blocked by corporate proxy): drainer detects gRPC failure pattern (3 consecutive `Unavailable`s), retries via OTLP/HTTP on port 4318
- TLS verification on by default
- `SKY_CONSOLE_HUB_TLS_INSECURE=1` opt-out for dev only — emits a startup warning, never gated behind production check

### 7. Auth + secrets hygiene

- Bearer token from `SKY_CONSOLE_HUB_TOKEN` (≥32 bytes enforced)
- SIGHUP re-reads env (rotation without restart)
- Token never appears in: logs, error messages, metric labels, exception traces, span attributes
- Hub-side: token registry table; revoke = delete row → next push returns 401 → app circuit-opens

### 8. Priority-aware backpressure

When spool capacity > 80%, start dropping from bottom of this list:

```
Highest priority  →  errors (any level)
                  →  metrics (always retained)
                  →  spans with status=error
                  →  spans with duration > p95 (slow)
                  →  WARN logs
                  →  INFO logs
                  →  spans below sampling threshold (fast)
Lowest priority   →  DEBUG logs                         ← dropped first
```

When > 95%, also reject new writes for the bottom 3 categories at the writer (drop-at-source rather than drop-at-spool).

Errors and metrics are NEVER dropped — if we can't keep them, we crash a startup invariant (configuration error) rather than silently lose them.

### 9. SIGTERM drain — framework-default

```go
// runtime-go/rt/exporter.go (sketch)
signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
go func() {
    <-c
    rt.MarkShuttingDown()                  // stop accepting new telemetry writes
    exporter.FlushWithDeadline(8 * time.Second)
    os.Exit(0)
}()
```

8-second budget leaves 2 s safety within Cloud Run's 10-s grace window. Lambda's similar. Verifiable via SkyDeploy's existing per-tenant Cloud Run deploys.

### 10. Replaceable exporter interface

Sky's built-in exporter is one implementation of a Go interface. Users can plug their own:

```
SKY_TELEMETRY_EXPORTER=otlp     # default — Sky's built-in with the spool design
SKY_TELEMETRY_EXPORTER=stdout   # for dev/debug, write OTLP JSON to stderr
SKY_TELEMETRY_EXPORTER=noop     # full disable (testing)
SKY_TELEMETRY_EXPORTER=<custom> # registered via Sky API (v0.17+)
```

The interface lives in `runtime-go/rt/exporter.go` from v0.16.0; the third-party-plugin API surfaces in v0.17+.

## Scalability — concrete numbers

Per-app instance overhead under realistic load:

| Load (req/s) | Telemetry events/s | Bytes/s (raw) | Bytes/s (gzip ~5×) | CPU | RAM (exporter) |
|---|---|---|---|---|---|
| 10 | ~30 | ~15 KB | ~3 KB | <0.5% | ~5 MB |
| 100 | ~300 | ~150 KB | ~30 KB | 1-2% | ~5 MB |
| 1,000 | ~3,000 | ~1.5 MB | ~300 KB | 3-5% | ~10 MB |
| 10,000 | ~30,000 | ~15 MB | ~3 MB | 10-15% | 15-25 MB |

**Sky's target:** comfortable up to ~1,000 req/s per app instance on commodity hardware (1+ vCPU). At 10k req/s on a single small core, the exporter starts to contend; mitigations include higher sampling (drop 99% of fast spans, kept the priority lanes) or scaling to a beefier instance.

**Out-of-scope for v0.16.x:** sustained >30k req/s per single instance. That's >100M requests/day per process; users at that scale are operating beyond Sky's single-binary positioning and should be at a node-local agent design.

## When this design breaks (honest list)

1. **Single app at >30k req/s** — exporter CPU + GC pressure becomes visible to the request loop. Mitigation: more aggressive sampling OR external collector pattern (v0.17+).
2. **Network partition lasting > spool capacity** — if hub is unreachable for hours AND telemetry rate exceeds drain rate AND spool cap is tight, data loss begins. Mitigation: larger spool, accept that priority drops save the important data.
3. **Spool DB corruption** — extremely rare but possible (mid-write crash on bad disk). Falls back to in-memory ring; restart restores spool DB. Doesn't lose data permanently.
4. **gRPC + HTTP both blocked by network policy** — exporter circuit-opens, all data buffered until spool fills. Operator must fix network reachability.

None of these break Sky's target users. They're upper-bound limits, documented to users in `OPS.md`.

## Implementation milestones (v0.16.1) — SHIPPED

| PR | SHA | Work |
|---|---|---|
| PR4.a | `360a90af` | Channel-based writer + drainer skeleton in `runtime-go/rt/exporter.go`. Bounded ring + Submit hot-path API. |
| PR4.b | `360a90af` | OTLP encoding via HTTP+JSON transport (gRPC deferred to v0.16.2 — no `otlptracegrpc` in `runtime-go/go.sum` and avoiding 4+ MB dep surface). Single-push fully tested. |
| PR4.c | `360a90af` | Backpressure + priority drop logic. Counter emission via Prometheus `/_sky/metrics`. |
| PR4.d | `360a90af` | Circuit breaker (50 consecutive failures → 30 s open → single half-open probe → closed-on-success). |
| PR4.e | `fda63d28` | SIGTERM drain hook via `runtime-go/rt/shutdown.go` registry. 8 s deadline. LIFO ordering so newest subsystem cleans up first. |
| PR4.f | `da4d3364` | 10 tests in `runtime-go/rt/exporter_test.go` — 5 reliability invariants + 5 smoke. All green. |
| PR5.a | `2ad839a0` | SQLite WAL spool schema + write/read/delete batches. Retention sweep + circular truncation. `modernc.org/sqlite` (already in go.mod via `live_store`/`telemetry/persist`). |
| PR5.b | `2ad839a0` | Memory-mode spool (bounded ring, no disk writes). Auto-detection via existing `IsServerless()` (`K_SERVICE` / `AWS_LAMBDA_FUNCTION_NAME`). |
| PR5.c | `a3dcbf94` | 16 spool tests including `TestSpool_CrashResilience` (replay-on-boot) and `TestSpool_FileMode_DoesNotBlockOnSlowDisk` (p99.99 stays sub-ms with 100 ms simulated fsync). |

The gate: `TestHubExporter_HotPathNeverBlocks` — Submit at 2.38M Hz, p99.99 = 96.08 µs no-race / 100.2 µs with `-race`, 10× below the 1 ms architectural ceiling. After PR5 spool integration: still 34.0-98.4 µs no-race. Spool writes are confirmed async on the drainer goroutine, never on the caller.

Tests live in `runtime-go/rt/exporter_test.go` and `runtime-go/rt/exporter_spool_test.go`. The "never blocks hot path" test gates the v0.16.1 tag.

## Env var summary

```dotenv
# Required to enable hub push
SKY_CONSOLE_HUB=https://obs.example.com:4317
SKY_CONSOLE_HUB_TOKEN=<openssl rand -hex 32>

# Optional — defaults shown
SKY_CONSOLE_SPOOL=auto          # file | memory | none
SKY_CONSOLE_SPOOL_MAX_MB=50     # file mode; memory mode caps at 5 MB
SKY_CONSOLE_BATCH_INTERVAL_MS=2000   # file mode; memory mode = 200
SKY_TELEMETRY_RING_BYTES=5242880     # 5 MB in-memory ring
SKY_TELEMETRY_EXPORTER=otlp     # otlp | stdout | noop
SKY_CONSOLE_HUB_TLS_INSECURE=0  # 1 = skip TLS verify (dev only)
```

All defaults work without any env config for a VM that ships to a hub. Serverless apps need only `SKY_CONSOLE_HUB` + `SKY_CONSOLE_HUB_TOKEN`.
