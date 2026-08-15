# Attribution run — where the interaction cost and the session memory go

Raw data behind "The attribution" in
[`../../skylive-interaction-cost.md`](../../skylive-interaction-cost.md).

Earlier phases established *how much* an interaction costs and *how much* a
session costs. Neither said *what* the cost was. This run attributes both to
named functions and named retentions, and adds the control that makes the
numbers mean something.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — **arm64** |
| Commit | `4f3da18e` on `perf/skylive-benchmark` |
| Go | 1.26.1 |
| App | `examples/26-ui-showcase` (384 elements) unless stated; `examples/19-skyforum` (94) for the view-size comparison |
| Session store | `memory` (the default) |
| Load average | 3.1–5.6 on 8 cores throughout — the machine was shared with other agents |
| Generator | `tools/skyliveload`, on the same host, loopback |

**These are ARM-on-Apple-silicon numbers.** The GCE work in
`../../skylive-remote-validation.md` found x86 differs by ~30% on the memory
figure. Ratios (Sky.Live vs control, view render vs diff) should travel;
absolute milliseconds should not.

## Layout

```
harness/     the scripts + the two pieces of instrumentation
  perfrun.sh          CPU/mem run driver (starts target, drives load, samples)
  memrun.sh           the 3-phase memory experiment (P0 / P1 idle / P2 load)
  sweep-*.sh          the run matrices
  ms.sh               MemStats field extractor (awk; no jq, no python)
  zz_perfprobe.go.txt the pprof + MemStats probe (see below)
  control-server.go.txt  the minimal Go SSE control server
scaling/     scaling.tsv — throughput vs GOMAXPROCS, both targets, 3 repeats
cpuprof/     closed-r{1,2,3} — 25 s CPU profiles over a steady-state window
mem/         sky-n{25,50,100}, control-n100 — MemStats + heap profiles per phase
viewsize/    forum-r{1,2,3} — the 94-element app under the identical config
```

## The two pieces of instrumentation, and why they were needed

**`zz_perfprobe.go`** — an env-gated (`SKY_PERF_PPROF_ADDR`) pprof + MemStats
listener, dropped into the *generated* `sky-out/rt/` tree, which is gitignored
build output. It does not touch `runtime-go/rt/` and does not need the Rust
compiler: the generated Go tree builds standalone with `go build`, in ~13 s.

`sky run --profile` already exists and was tried first. It could not answer
these questions: it profiles the **whole process lifetime** and writes only at
exit, so startup and session ramp are folded into any per-interaction figure;
it cannot take **two heap snapshots from one process** to diff; and it does not
expose `runtime.MemStats`, without which RSS cannot be split into live heap
versus allocator headroom — which turned out to be the whole point.

**`control-server.go`** — a minimal Go SSE server speaking enough of the
Sky.Live wire protocol (`sky_sid` + `__sky_csrf` cookies, a `data-sky-hid` to
scrape, a held `text/event-stream`, a JSON patch reply) that **the same
generator drives it unmodified**, and it passes the same `-self-check`. It
holds a per-session model so it is a state-holding server, not an echo.

It is **not** feature-equivalent and is not a target: no virtual DOM, no diff,
no reflective dispatch, no session store, no CSRF verification beyond an echo.
It establishes a floor — what the transport plus a server-held model costs in
Go — so Sky.Live's cost is a ratio against something measured rather than an
unanchored number.

## Method notes that matter

- **CPU per interaction** is `ps(1)` process CPU-time delta ÷ interactions,
  taken over the steady-state profile window. It is independent of pprof, so
  it doubles as the profiler-overhead check. pprof's own sample total reads
  83–84% of the `ps` figure, the usual Go undersampling; the `ps` number is
  quoted.
- **Profiler overhead**: profiled runs sustained 106.5/s mean against 109.0/s
  unprofiled, same config — a **2.3% throughput reduction**. Stated, not
  assumed.
- **Memory retention** is measured with sessions **idle** (`-think 1h`, so
  established and then quiescent) after two forced `runtime.GC()` passes.
  This is the phase that separates retention from GC headroom; the earlier
  sweeps measured while interactions were in flight, which confounds them.
- **`MemProfileRate` = 16384** on the memory runs (default 512 KiB is far too
  coarse to name a 336 kB/session retention). Sampled heap totals agree with
  `MemStats` `HeapAlloc` to within 6%, which is the cross-check that the
  attribution shares are trustworthy.
- Every run refuses to start if its port is already listening. A stale app
  from an earlier smoke test answered the readiness probe once while the run's
  own app had died on bind — every number would have described the wrong
  process. That failure is now structurally impossible rather than remembered.

## Known limits

1. **One app dominates the CPU result, and that is the finding, not a flaw** —
   but it does mean the millisecond figures are `26-ui-showcase`'s. The
   `viewsize/` runs give the second point.
2. **The idle-phase session count** is the requested N, corroborated by
   established TCP connections (401 incl. header for N=100) and by goroutine
   count (4/session). The `liveSession` struct is too rare to appear in a
   sampled heap profile, so it could not be counted directly.
3. **Allocation-per-interaction is an upper bound by ~10%**, because the
   `TotalAlloc` delta spans ramp and warmup while the divisor counts only the
   measurement window.
4. **The control's `Mallocs` was not recorded** (the field was omitted from its
   probe), so only its bytes-per-interaction is quoted, not its object count.
5. **No durable session store was profiled here.** The memory store was used
   throughout; the gob/encode path is therefore absent from these profiles by
   construction. The earlier memory-vs-postgres comparison (~21/s vs ~19/s)
   bounds that path at ~10%.
