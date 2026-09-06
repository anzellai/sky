# What a headless Sky.Http.Server + Std.Db read costs

Every capacity figure this project has ever measured is a **Sky.Live SSE**
workload on `examples/19-skyforum` (see
[`skylive-interaction-cost.md`](skylive-interaction-cost.md)). The v1 audit
flagged that the *other* shape a real trial will run — a **stateless JSON API
that reads Std.Db and returns JSON**, no SSE, no sessions, no CSRF — had **zero
measured capacity**. This document records the first measurement of that shape,
the app that produced it, the harness, and — as with every perf doc here — what
the measurement does **not** cover.

> **Rule for quoting anything here** (inherited from the SSE doc): no number may
> be repeated without the conditions attached to it. The raw run and its
> `env.txt` are archived at
> [`runs/http-metadata-local-20260906/`](runs/http-metadata-local-20260906/).

## The app under test

`examples/65-metadata-service` — the shape of the internal "core-metadata"
trial workload:

- **`Sky.Http.Server`** (`Server.listen` + `Server.get` routes) — no Sky.Live,
  no SSE, no sessions, no CSRF.
- Each request is **HTTP → one SQL read → JSON** (`Sky.Core.Json.Encode`).
- **`Std.Db`** against **embedded PostgreSQL 18.6** (`sky run` supervises a
  per-project cluster; `[database] embedded = true`). The connection pool is one
  memoised CAF shared across handlers.
- A `metadata` table (PK on `key`) seeded with **500 rows** at startup.

> **The example now SHIPS on SQLite; the PostgreSQL numbers below stand.**
> `examples/65-metadata-service/sky.toml` ships `[database] driver = "sqlite"`
> (a single in-process file, no cluster/bundle/DSN) so the example runs anywhere
> — including CI's `build-run` gate, which starts the bare `./app` binary with
> no `sky run`, no cluster, and no injected DSN (an `embedded = true` app exits
> on start there, because it cannot reach a PostgreSQL cluster). Because
> `Std.Db` is dialect-safe, this is a **one-line config swap** back to embedded
> PostgreSQL (`[database] embedded = true`) with **no code change** — that
> PostgreSQL configuration is the production target, and it is what every number
> in the "The measurement" section below was measured on. The PostgreSQL figures
> are real and were not re-run; they are not restated for SQLite.
>
> **A local SQLite data point for the shipped default** (same host as the run
> below — Apple M1 Mac mini, Sky `48a6a4be`, closed-loop `load/loadgen.go`, 5 s
> per level after a 1 s warm-up):
>
> | Endpoint | conc | req/s | p50 ms | p99 ms | err % |
> |---|---|---|---|---|---|
> | `GET /metadata/:key` (indexed single-row) | 8 | 5,695.7 | 1.38 | 2.31 | 0.00 |
> | `GET /metadata/:key` (indexed single-row) | 64 | 5,686.2 | 8.29 | 45.72 | 0.00 |
> | `GET /healthz` (server ceiling) | 64 | 38,613.1 | 1.23 | 6.75 | 0.00 |
>
> On this host the SQLite single-row read path lands in the same ~5.7k req/s
> band as embedded PostgreSQL — the DB read, not HTTP/JSON, is the bound in both
> (the `/healthz` framework ceiling is ~7× higher). Treat this as an
> order-of-magnitude sanity point, not a substitute for the PostgreSQL sweep:
> SQLite is single-writer and single-file, so it does not carry the production
> tier's concurrency or multi-instance story.

| Endpoint | Read | Verified |
|---|---|---|
| `GET /healthz` | none (server ceiling) | `200 {"status":"ok"}` |
| `GET /metadata/:key` | one indexed row by PK (`Db.findOneByField`) | `200` JSON object / `404` on miss |
| `GET /metadata?limit=N` | first N rows ordered by key (`Db.query`) | `200 {"count":…,"items":[…]}` |

`sky check` is clean; all four responses (incl. the 404) were curl-verified
before the load run.

## How to reproduce

```bash
# 1. run the app (embedded PostgreSQL, binds :8137)
cd examples/65-metadata-service && sky run src/Main.sky

# 2. drive load from another shell (closed-loop Go harness, stdlib only)
./load/run-load.sh                          # default sweep, localhost:8137
LEVELS="512,1024,2048,4096" DUR=8s ./load/run-load.sh
```

The harness (`load/loadgen.go`) is **closed-loop / constant-concurrency**: at
each level, N goroutines each loop "send → measure → repeat" for the level's
duration — the model `wrk`/`hey` use. `/metadata/:key` requests a random
`svc-0001…svc-0500` key each time. No `oha`/`hey`/`wrk`/`bombardier` was
present on the host, so a Go harness (the most accurate option available) was
written; the client uses a 2048-conn pool and a 10 s per-request timeout.

## The measurement

**Host:** Apple M1 Mac mini, 8 cores, 16 GiB, macOS 26.5.2, Go 1.26.1, Sky
`b83e9493`. 8 s per level after a 1 s warm-up. **The app, PostgreSQL, the load
generator and `go` all shared the same 8 cores**, and the host's 5-minute load
average was elevated (~8–10) from concurrent builds. This is a **co-located
single-laptop baseline**, not an isolated benchmark.

### `GET /metadata/:key` — indexed single-row read (the hot path)

| conc | req/s | p50 ms | p90 ms | p99 ms | max ms | err % |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 4,054.8 | 0.24 | 0.27 | 0.30 | 1.13 | 0.00 |
| 8 | 5,603.1 | 1.40 | 1.87 | 2.33 | 4.71 | 0.00 |
| 16 | 5,765.3 | 2.58 | 4.32 | 6.15 | 11.06 | 0.00 |
| 32 | 5,671.5 | 5.04 | 9.83 | 15.22 | 29.69 | 0.00 |
| 64 | 5,664.5 | 10.24 | 19.04 | 29.16 | 47.22 | 0.00 |
| 128 | 5,645.1 | 19.47 | 40.63 | 69.80 | 167.58 | 0.00 |
| 256 | 5,677.4 | 34.88 | 91.11 | 169.95 | 355.43 | 0.00 |
| 512 | 5,639.4 | 66.60 | 196.17 | 379.03 | 788.85 | 0.00 |
| 1024 | 5,658.5 | 130.32 | 401.79 | 784.64 | 2,154.71 | 0.00 |
| 2048 | 5,657.9 | 262.38 | 795.74 | 1,552.62 | 4,545.86 | 0.00 |
| 4096 | 5,533.3 | 531.52 | 1,614.97 | 3,087.37 | 6,288.09 | 0.00 |

### `GET /metadata?limit=50` — range read, 50 rows

| conc | req/s | p50 ms | p90 ms | p99 ms | max ms | err % |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 2,139.9 | 0.46 | 0.49 | 0.58 | 2.05 | 0.00 |
| 8 | 5,097.3 | 1.52 | 2.06 | 2.79 | 10.47 | 0.00 |
| 32 | 5,133.3 | 5.49 | 10.99 | 16.97 | 30.56 | 0.00 |
| 128 | 5,022.1 | 21.63 | 46.01 | 80.12 | 217.09 | 0.00 |
| 256 | 5,009.1 | 39.31 | 103.57 | 193.52 | 442.38 | 0.00 |
| 512 | 5,059.2 | 73.78 | 217.03 | 427.55 | 1,000.38 | 0.00 |

### `GET /healthz` — no DB touch (the framework ceiling)

| conc | req/s | p50 ms | p99 ms | err % |
|---:|---:|---:|---:|---:|
| 1 | 14,566.0 | 0.07 | 0.10 | 0.00 |
| 16 | 37,817.7 | 0.37 | 1.22 | 0.00 |
| 64 | 39,054.3 | 1.22 | 6.61 | 0.00 |
| 256 | 37,860.8 | 6.49 | 15.33 | 0.00 |
| 512 | 37,542.6 | 13.48 | 24.22 | 0.00 |

Full sweeps: [`runs/http-metadata-local-20260906/sweep.txt`](runs/http-metadata-local-20260906/sweep.txt)
and [`knee-search.txt`](runs/http-metadata-local-20260906/knee-search.txt).

## What the numbers say

- **Local throughput ceiling for the DB read path: ~5.6–5.8k req/s**
  (indexed single-row), **~5.0–5.1k req/s** (50-row range read), on this
  machine, co-located with its database and load generator. The bare framework
  (`/healthz`) tops out at **~38k req/s**, so the DB read — not HTTP/JSON — is
  the cost, as expected.
- **The saturation knee is at concurrency ≈ 8.** Throughput is flat from 8 to
  4096 concurrent; added concurrency past that point buys only latency, exactly
  as Little's Law predicts for a fixed service rate.
- **There is no *failure* knee up to 4096 concurrent — 0.00% errors at every
  level.** The failure mode of this shape on this host is **latency growth
  under a fixed service rate, not dropped requests**: p99 rises from 2.3 ms
  (conc 8) to 3.1 s (conc 4096) while req/s holds at ~5.6k.
  - Caveat on "no knee": a closed-loop harness cannot manufacture a
    request-drop knee until latency exceeds the client timeout (10 s here); the
    max latency at 4096 was 6.3 s, still under it. So "0% errors" means "the
    server never refused or reset a connection", not "there is no operational
    limit" — the **SLA knee** is where p99 crosses your budget. Against a
    common 100 ms p99 budget on `/metadata/:key`, that is **between concurrency
    128 (p99 69.8 ms) and 256 (p99 170 ms)**.
- **App RSS after the full sweep: ~759 MB.** This is GC *headroom* retained
  under the derived `GOMEMLIMIT 11.8 GB` / `GOGC 400`, not working set — the
  runtime lets the heap grow rather than collect aggressively when memory is
  plentiful. It is not a per-request leak (throughput and error rate were flat
  across the whole run). On a small instance the derived `GOMEMLIMIT` scales
  this down; see AGENTS.md's GC section.

## What this does NOT establish — and what remains

**This is a local-laptop baseline. It is not the trial's capacity.** It proves
three things and only three: the shape **works** end-to-end (Sky.Http.Server +
Std.Db + embedded PostgreSQL, real 200s with JSON bodies), it has **a
per-request cost floor** (~0.24 ms p50 single-row at concurrency 1), and its
**local ceiling** on an 8-core M1 sharing its cores with the DB and the load
generator is ~5.6k req/s with **zero errors** to 4096 concurrent.

It does **not** establish the number a capacity claim needs, because:

1. **Wrong machine.** The trial runs on a **GCE instance** (a specific
   `e2`/`n2`/`c3` family + size), not an M1. Prior work in this repo has been
   wrong by **2.5–5×** carrying a laptop/container number to real GCE hardware,
   and by roughly an order of magnitude sizing on the wrong resource — see the
   CPU-binds-before-memory and count-physical-cores-not-vCPUs sections of
   [`skylive-interaction-cost.md`](skylive-interaction-cost.md). **No
   extrapolation to a GCE number is made here**, deliberately.
2. **Co-located and contended.** The app shared 8 cores with PostgreSQL, the
   load generator, and `go`, under an elevated load average. A real deployment
   separates the load driver (and often the database) onto other hosts; the
   ceiling would move.
3. **No burst/soak behaviour.** The SSE doc found a rested burstable e2
   overstates sustained capacity by ~2.7× on its first run; that decay is
   unmeasured for this shape.

**What remains (blocked on instance access):** the at-scale run of this exact
app on the target GCE instance family — `sky db provision --embed` (or a Cloud
SQL DSN) on the instance, the load driven from a **separate** bench host,
sweeping concurrency until the real knee (SLA p99 breach or genuine errors)
appears, with `MemAvailable` and CPU sampled alongside. That run — not this one
— produces the capacity number the trial can be sized on. The app and harness
in `examples/65-metadata-service/` are written to run unchanged against a remote
`URL=http://<instance-ip>:8137`.
