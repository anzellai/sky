# Sky Console v0.16.x — Serverless deployment guide

> Cloud Run / AWS Lambda specifics. Why naive disk-buffer designs break,
> and how Sky's exporter handles it cleanly. Relevant to SkyDeploy tenants
> (every customer app is Cloud Run) and to Sky users deploying to Lambda.

## Why this needs its own doc

The collector design in `EXPORTER.md` assumes always-on processes with persistent disk. Serverless violates both assumptions:

1. **No durable filesystem.** Cloud Run + Lambda containers have ephemeral disks. `/tmp` writes survive within a single invocation but vanish on container kill.
2. **Scale to zero.** Cloud Run can shut idle containers down. Background goroutines aren't guaranteed to run between requests.
3. **Cold starts.** First request to a fresh container hits an empty buffer + uninitialised exporter.
4. **Horizontal scale.** Multiple container instances run in parallel for the same service.
5. **10s SIGTERM grace.** Container has a fixed window to flush in-flight data before kill.

A spool-on-disk design loses data on every container recycle. A "retry forever in background" loop doesn't run when the container is paused. The naive in-process exporter from `EXPORTER.md` would silently lose telemetry.

## Auto-detection — what triggers serverless mode (v0.16.1 shipped)

The exporter inspects these env vars at startup via `rt.IsServerless()`:

```
K_SERVICE                   set → Cloud Run        → serverless mode (memory spool)
K_REVISION                  set → Cloud Run revision → tag service.instance.id
AWS_LAMBDA_FUNCTION_NAME    set → Lambda            → serverless mode (memory spool)
AWS_LAMBDA_LOG_STREAM_NAME  set → Lambda invocation → tag service.instance.id
```

If `K_SERVICE` OR `AWS_LAMBDA_FUNCTION_NAME` is set, the exporter switches to **memory mode** automatically. Explicit override via `SKY_CONSOLE_SPOOL_MODE=file` only if you've arranged a persistent volume mount (Cloud Run with a Cloud Storage FUSE mount, for example).

Tested by `TestSpool_AutoDetect_ServerlessUsesMemory` and `TestSpool_AutoDetect_VMUsesFile`.

## Serverless-specific exporter behaviour

| Behaviour | VM mode | Serverless mode |
|---|---|---|
| Spool backend | SQLite WAL on disk | Bounded in-memory queue |
| Spool cap | 50 MB | 5 MB |
| Push cadence | 2 s (batched) | 200 ms (eager) |
| Retry on hub fail | Exp-backoff, forever | 3 attempts (1s/2s/4s), then drop |
| Worst-case data loss | None unless spool fills | ~5 s per container kill |
| Eager exporter spawn | Yes | Yes — critical |
| SIGTERM drain | 8 s budget | 8 s budget |
| `/_sky/metrics` exposed | Yes (Prometheus scrapeable) | Yes (still serves, but external scrape isn't viable) |

The push cadence change (200 ms vs 2 s) is the most important behavioural difference. In serverless we can't afford to batch — a container may live only a few seconds before being recycled, and telemetry queued for 2 seconds of batching has a 30-50% chance of being lost.

## SIGTERM drain — the critical mechanism

Cloud Run sends `SIGTERM` to the container ~10 seconds before forced kill. Sky's runtime installs a handler at framework init via the `runtime-go/rt/shutdown.go` registry (v0.16.1 shipped):

```go
// runtime-go/rt/shutdown.go — LIFO registry
rt.RegisterShutdownHook("hub-exporter", func(deadline time.Duration) {
    exporter.Flush(deadline)
})

// Sky.Live (live.go) + Sky.Http.Server (rt.go) signal handlers
go func() {
    <-c // SIGTERM
    rt.RunShutdownHooks(8 * time.Second) // 8 s budget, LIFO
    srv.Close()
}()
```

8-second budget leaves 2 s safety within Cloud Run's 10-s grace window. Lambda's similar (`runtime_extension`-style hook). Tested by `TestHubExporter_SIGTERMDrain` — 1000 Submits flushed at 100 % delivery in 26.8 ms (well within budget).

**Why not push synchronously during the request lifecycle:** that would add 10-50 ms latency to every response. Async + SIGTERM drain is the right trade-off.

## Cold-start telemetry

The first request to a fresh container generates startup logs (boot messages, DB connection setup, etc.). These need to push before the request is even served — otherwise they're lost when the container scales back to zero after the request.

Mechanism: the exporter goroutine spawns at `init()` (NOT lazily on first telemetry write). It's running before `main()` is called. By the time the first HTTP request lands, the goroutine is already pumping the queue.

For Lambda specifically: the exporter runs during the "init phase" before the first handler invocation. Container reuse across multiple invocations is leveraged when present.

## Multi-instance concurrent push

Cloud Run autoscales horizontally. 5 containers serving sky-lang.org-blog simultaneously, each independently pushing telemetry to the hub.

This is fine for the hub:
- All 5 containers carry the same `service.name = "sky-lang.org-blog"`
- Distinct `service.instance.id` per container (from `K_REVISION + hostname`)
- Hub aggregates metric counters per (service.name, instance.id)
- Hub UI offers a "per-instance" drill-down when needed

The OTLP wire format supports concurrent push from multiple sources to the same backend. The hub backend (DuckDB + SQLite WAL) handles parallel writes via standard SQLite WAL concurrency.

## `/_sky/metrics` does not work in serverless

The embedded `/_sky/metrics` endpoint is still served by the app, but it can't be reliably scraped externally:

- Cloud Run instances scale to zero — Prometheus scrape returns "no instance" half the time
- Instance hostnames are ephemeral and not stable for external configuration
- Cloud Run's HTTP layer doesn't support direct port-based scraping

**Conclusion:** in serverless, push (to hub) is the only viable mode. `SKY_CONSOLE_HUB` is required for observability. The exporter detects serverless and warns at startup if `SKY_CONSOLE_HUB` is unset:

```
[exporter] WARN: serverless detected (K_SERVICE=blog) but SKY_CONSOLE_HUB is unset.
                 Telemetry will buffer in-memory and be lost on container recycle.
                 Set SKY_CONSOLE_HUB to a hub endpoint, or accept the data loss.
```

## SkyDeploy tenant deployment recipe

SkyDeploy spins up per-tenant Cloud Run services. Each tenant app should:

1. Inherit `SKY_CONSOLE_HUB=https://hub.skydeploy.app:4317` from the SkyDeploy control plane
2. Receive a per-tenant `SKY_CONSOLE_HUB_TOKEN` that the hub recognises
3. The hub uses `app` auth mode with a callback that resolves the bearer token to a tenant identity
4. UI scopes data by `service.name LIKE "<tenant-id>-%"` per tenant ACL

SkyDeploy's existing Litestream pattern (per-tenant `/data/console.db` with GCS replication) **can be deprecated** in favour of pushing to the SkyDeploy-hosted hub. That removes per-tenant volume-mount complexity and gives every tenant a cross-app pane "for free." Migration plan in `MIGRATION.md`.

## AWS Lambda specifics

Same memory-mode exporter, with one twist: Lambda's CPU is throttled between invocations. The exporter goroutine may be paused for minutes if the container is idle but not yet killed.

Mitigation: Lambda Extensions API allows registering a separate process that runs DURING the freeze. Future enhancement (v0.17+): ship a Sky Lambda Extension that drains the exporter queue during freezes. For v0.16.x, the accepted trade-off is ~5 s of data loss per idle-freeze transition.

## Recovery on container restart

What happens to in-flight telemetry when a container is killed?

| Scenario | Behaviour |
|---|---|
| Graceful SIGTERM → SIGKILL after 10 s | 8 s drain window flushes ~95% of in-flight data. ~5% loss (last 200 ms of writes) acceptable. |
| Cloud Run OOM-kill (instant SIGKILL) | All in-memory data lost. Rare; happens only if app uses > Memory Limit. Mitigation: keep app RSS well under limit (Sky's typical 80-150 MB on 256 MB limit). |
| Cloud Run autoscale-down (idle for > 15 min) | Goroutine paused, no telemetry to send. Drain happens on next invocation via lazy resume. |
| Lambda freeze | Same as Cloud Run idle. Resumes on next invocation. |
| Crash from app panic | `defer rt.LogPanicAndExit()` (existing) ensures the panic is reported. Exporter drain runs in the same defer chain. |

The honest answer: **serverless telemetry has fundamentally bounded reliability.** Sky's exporter handles ~95% of cases well; the remaining 5% (instant-kill scenarios) require external observability infrastructure if zero loss is critical. Most teams accept this trade-off.

## Operational checklist for serverless deploys

Before going to production:

- [ ] `SKY_CONSOLE_HUB` set
- [ ] `SKY_CONSOLE_HUB_TOKEN` registered with hub (provisioned by SkyDeploy or self-set)
- [ ] Hub on a non-serverless instance (e2-small VM, not Cloud Run)
- [ ] App memory limit ≥ 2× expected RSS (give exporter headroom)
- [ ] Cloud Run min-instances ≥ 1 if telemetry continuity matters (eliminates cold-start data gaps; costs ~$5/mo per instance)
- [ ] Verify hub-side: `service.instance.id` shows multiple replicas under load
- [ ] Set up alerts on `sky_telemetry_dropped_total` rate (rising drops = exporter degradation)

## Testing the serverless mode

Without deploying to Cloud Run, you can simulate locally:

```bash
K_SERVICE=test-app K_REVISION=v1 ./sky-out/app
```

Setting `K_SERVICE` triggers serverless detection. The exporter switches to memory mode. Observable via the `sky_telemetry_spool_mode` gauge (value `0`=file, `1`=memory, `2`=none).

Regression test in v0.16.1: spawn the app with `K_SERVICE=test`, generate 1000 telemetry events, send `SIGTERM`, assert >95% arrive at a stub hub within 9 seconds.
