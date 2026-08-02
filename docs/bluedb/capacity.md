# BlueDB — single-instance capacity

Rough capacity of one **embedded** instance, and when to leave it. Numbers are
order-of-magnitude for a Pebble/RocksDB-class LSM (the substrate BlueDB is built
on); no measured BlueDB figures exist yet.

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
amortizes one fsync across a whole batch, lifting it to tens of thousands while
p99 stays ~1 ms. Without it, "fast frequent durable writes on one box" is
impossible.

## darraghstudio reality check

Its entire database was **507 KB gzipped** — sub-megabyte. It fits in CPU cache,
never mind RAM, so every read is a memory lookup. Actual write load: a handful/sec
at peak. Against a ceiling of tens-of-thousands of writes/sec and hundreds-of-
thousands of reads/sec **on the existing small VM**, that's ~5–6 orders of
magnitude of headroom. One embedded instance serves thousands of concurrent
shoppers without noticing.

## When you've actually outgrown one instance — only three signals

1. **Working set > RAM** → reads hit the SSD → IOPS-bound (~15k–100k random
   reads/sec on cloud SSD).
2. **Sustained writes saturate the disk**, or **compaction saturates CPU** (LSM
   background compaction competes for cores/bandwidth under a constant torrent;
   bursty small writes are fine).
3. **You need to survive the node dying.**

## The honest punchline

For most apps the reason to go multi-node is **not throughput — it's
availability.** A single instance, however fast, is one disk and one process from
an outage. So the real progression isn't "one instance → too slow → shard"; it's
"one embedded instance (plenty fast, but a SPOF) → cluster for HA/DR." In BlueDB
that jump is `embedded = true` → `url = "…"` in sky.toml, **app code unchanged** —
you move for resilience, and get horizontal write scale for free when you
eventually need it.
