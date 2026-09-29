# Relay soak: a slow RSS rise is bounded telemetry rings under GOGC=400 (2026-09-29)

## Question

A downstream project soaked one open session through its relay app for 10
minutes. The relay's RSS rose from 47.7 MB to 49.6 MB over the last 5 minutes
(its other two processes stayed flat). Is this a leak in the Sky runtime, or
heap growth that stops?

The relay is a Sky `Sky.Http.Server` app. A daemon holds WebSockets (a control
connection and one per channel). A client uses an HTTP tunnel: one long poll
(`GET /t/recv`, up to 20 s, a `Task.loop` that sleeps 40 ms per step) always
waits, and one `POST /t/send` runs at a time. The state is in `Std.Cache`
tables.

## Answer

**Not a leak.** Goroutines and file descriptors are flat and return to the
baseline when the session closes. The live heap grows while the runtime's
telemetry rings fill, and stops when they are full. The access-log ring holds
10,000 entries and the trace ring 1,000 (`runtime-go/rt/telemetry/store.go`,
`NewStore`). Each entry keeps its own attribute map and strings. Under the
shipped `GOGC=400` the next GC target is five times the live heap, so each MB
of ring content can add up to about 5 MB of heap before a collection. RSS
follows that target, then plateaus.

At the soak's request rate (about 24 requests a minute) the trace ring fills in
about 42 minutes and the access-log ring in about 7 hours. So a 10-minute soak
sees only the start of the rise.

## Runs

Machine: MacBook, 16 GB, macOS 27. The relay copy was built by the Sky
compiler at release/v0.27.0 `4f20dfab` (runtime unchanged by this branch for
these paths), dev mode, no `ENV`. The runtime derived `GOMEMLIMIT 11.8GB,
GOGC 400`. Only for these runs, the copy's emitted Go got one extra file that
serves `net/http/pprof` on a side port (and, in run B, sets
`runtime.MemProfileRate = 1` for exact heap profiles). The original app was
not changed.

### A. Soak, 46 minutes (`soak-samples.tsv`)

`driver.mjs` is one fake daemon (Ed25519 hello, a channel WebSocket that
echoes each message) and one client: a long poll always waiting
(`wait=20000`) and one send at a time, every 5 s. `sample.sh` records RSS,
open descriptors, goroutines and `runtime.MemStats` every minute.

Result: 552 sends, 552 echoes received, 0 errors.

| minute | RSS MB | fds | goroutines | HeapSys MB | Sys MB | NextGC MB |
|---|---|---|---|---|---|---|
| 1 | 35.5 | 15 | 13 | 23.2 | 30.8 | 24.89 |
| 10 | 44.8 | 16 | 14 | 31.1 | 39.6 | 25.97 |
| 20 | 33.5 | 16 | 14 | 31.1 | 39.6 | 26.88 |
| 30 | 48.4 | 16 | 14 | 35.0 | 43.6 | 27.86 |
| 40 | 36.6 | 16 | 14 | 35.0 | 43.6 | 28.85 |
| 46 (session closed) | 15.4 | 12 | 7 | 35.0 | 43.6 | 29.18 |

- Goroutines 13 to 14 and descriptors 15 to 16 for the whole session. After the
  client closed, 7 goroutines (the count before the session) and 12
  descriptors.
- `Sys` did not grow after minute 25. `NextGC` rose slowly (24.9 to 29.2 MB),
  which is about 5.0 to 5.8 MB of live heap: the rings filling.
- RSS on macOS moves by 10 to 30 MB between samples, as the heap cycles to its
  GC target and the OS reclaims pages. It is not a leak signal on its own.
- Heap profile at minute 40 against minute 10 (`diff-10-40.txt`, sampled at
  512 KB): +3 MB, all under the request path (the trace entry's attribute map
  in `ObservabilityMiddleware`, `emitAccessLog`, buffers of requests in
  flight).

### B. Accelerated: 40,000 requests (`burst-sampled.tsv`) and 100,000 with exact profiles (`burst-exact.tsv`)

`burst.mjs` sends requests through the same HTTP stack, 4 at a time, and after
each step reads the live heap after a forced GC (`/debug/pprof/heap?gc=1`).

| requests | live heap after GC | goroutines |
|---|---|---|
| 0 | 5.76 MB | 8 |
| 5,000 | 7.94 MB | 16 |
| 10,000 | 9.53 MB | 16 |
| 20,000 | 9.55 MB | 16 |
| 40,000 | 9.58 MB | 16 |

The live heap grows about 0.38 KB a request until the access-log ring is full
(10,000 entries), then stops. A new process with `MemProfileRate = 1` (every
allocation recorded): after 20,000 requests 9.46 MB, after 100,000 requests
9.46 MB. The exact profile diff over those 80,000 requests is +10 KB net, in
runtime and profiler internals (`diff-exact-20k-100k.txt`). No per-request
growth.

## What this means for sizing

The ceiling of this effect is about 3.8 MB of live heap for a full access-log
ring (run B: 5.76 to 9.53 MB) plus the trace ring. At `GOGC=400` that allows
up to about 19 MB more heap before a collection, so a quiet relay's RSS can
climb for hours at low traffic before it plateaus. The derived `GOMEMLIMIT`
still bounds the process.

## Reproduce

```sh
# relay copy built with sky build (RELAY_PORT: the app's own port setting); SOAK_PPROF enables the side-port pprof file
RELAY_PORT=8781 SOAK_PPROF=127.0.0.1:6071 ./sky-out/app &
node driver.mjs http://127.0.0.1:8781 46 5000 &      # RELAY_AUTH_LABEL=<the app's label>
./sample.sh . <relay pid> 46
node burst.mjs http://127.0.0.1:8781 http://127.0.0.1:6071/debug/pprof <relay pid> 40000 2500
```
