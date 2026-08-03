# BlueDB — single-instance capacity

Rough capacity of one **embedded** instance, and when to leave it.

> **What v1 actually is.** The v1 engine is a group-committed **WAL + in-RAM
> memtable + periodic snapshot** — *not* an LSM/SSTable engine (SSTable tiering,
> compaction, and spill-to-disk are design-target v2). The whole working set is
> resident in RAM and does **not** spill to disk. The numbers below are
> **measured from the v1 engine** (§ Measured) — an order-of-magnitude guide, not
> a tuned load test.

## The mental model — three limits

A single instance's ceiling is set by exactly three things:

1. **RAM** — does the working set fit → read speed (memory vs disk).
2. **One SSD's fsync + bandwidth** — durable write throughput.
3. **Cores** — compaction + reactive fan-out headroom.

Everything below falls out of those three.

## Numbers

| | Small VM (2 vCPU, 4 GB, PD-SSD) | Bigger VM (8 vCPU, 32 GB, local NVMe) |
|---|---|---|
| Cached point reads (working set in RAM) | ~200k–500k /sec | ~1M+ /sec |
| Durable small writes (~1 KB, group commit) | ~20k–50k /sec | ~100k–300k /sec |
| p99 write latency | ~1–3 ms | sub-ms |
| p99 cached read | single-digit µs | single-digit µs |

Two reasons reads are that fast — both the point of **embedded-first**:

- **No network tax.** A cached point read is a function call + a map lookup
  (~1 µs). Postgres over a *localhost* socket is ~50–200 µs/query (parse → plan →
  execute → wire) — 50–200× more overhead per op.
- **Working set in RAM** → reads never touch disk; CPU-bound, not IOPS-bound.

**Group commit is what makes the write number real.** Naive fsync-per-write caps
any single SSD at ~1–2k durable writes/sec (the fsync-latency floor); group commit
amortizes one fsync across a whole batch, lifting it to tens of thousands. Without
it, "fast frequent durable writes on one box" is impossible.

## Measured — bluedb v1 engine (`go test -bench`)

Real numbers from `runtime-go/bluedb/bench_test.go` on an 8-core Apple-silicon
laptop (macOS, where Go's `File.Sync()` issues `F_FULLFSYNC` — a *true* durable
barrier, ~ms; Linux `fsync` is faster, so these are a conservative floor):

| Benchmark | ns/op | Throughput | Group-commit batch |
|---|---|---|---|
| Cached point read (parallel) | ~115 | **~8.7M reads/sec** | — |
| Durable write, ~8 in-flight | ~1.21M | ~0.8k/sec | 4 writes/fsync |
| Durable write, ~512 in-flight | ~19.5k | **~51k durable writes/sec** | **326 writes/fsync** |
| Relaxed (NoSync) write | ~3.1k | **~319k writes/sec** | — |

**The load-bearing result: durable write throughput SCALES with concurrency.**
As in-flight writers grow, group commit packs more writes into each fsync
(4 → 326), so durable throughput climbs from ~0.8k to **~51k writes/sec** — the
amortization the whole design turns on. A reactive app with many concurrent
sessions is exactly the high-concurrency regime, so it lands near the top. p99
per-write latency stays ~one fsync regardless. (These are Go-benchmark averages,
not a tuned load test; treat them as order-of-magnitude, and expect Linux to beat
the macOS `F_FULLFSYNC` floor.)

## darraghstudio reality check

Its entire database was **507 KB gzipped** — sub-megabyte. It fits in CPU cache,
never mind RAM, so every read is a memory lookup. Actual write load: a handful/sec
at peak. Against a ceiling of tens-of-thousands of writes/sec and hundreds-of-
thousands of reads/sec **on the existing small VM**, that's ~5–6 orders of
magnitude of headroom. One embedded instance serves thousands of concurrent
shoppers without noticing.

## When you've actually outgrown one instance — only three signals

1. **Working set approaches RAM** → v1 holds everything in RAM and does **not**
   spill to disk; as it fills you hit `ErrFull` (the `MaxKeys` ceiling — a clean
   error, not an OOM kill) rather than a slow spill. Size the store to fit RAM.
   (Transparent spill/tiering is v2.)
2. **Sustained writes saturate one disk's fsync/bandwidth** — group commit
   amortizes the fsync, but one disk is one disk.
3. **You need to survive the node dying.**

## The honest punchline

For most apps the reason to go multi-node is **not throughput — it's
availability.** A single instance, however fast, is one disk and one process from
an outage. So the real progression isn't "one instance → too slow → shard"; it's
"one embedded instance (plenty fast, but a SPOF) → cluster for HA/DR." In BlueDB
that jump is `embedded = true` → `url = "…"` in sky.toml, **app code unchanged** —
you move for resilience, and get horizontal write scale for free when you
eventually need it.
