# BlueDB — durability & recovery

How writes survive process crashes, power loss, in-flight failures, and disk/node
loss. Companion to `README.md` (§ North star) and `strategy.md`.

The core rule: **a write is only ever acked after it is recoverable.** "Acked"
means fsync'd (embedded) or quorum-committed (cluster). Everything below is the
machinery that makes that true without killing the fast/frequent-write hot path.

> **Status — v1 (built) vs design (v2).** v1 implements the embedded path:
> group-committed **WAL** (CRC + length framing), an **in-RAM memtable**,
> **snapshot/checkpoint** + torn-tail truncation, crash recovery, all-or-nothing
> batch rollback, and an advisory **file lock**. **What v1 guarantees:** an acked
> write is fsync-durable and survives process crash / power loss / torn tail
> (recovered from the WAL); a second engine can't open one file. Sections below
> that describe an **SSTable/MANIFEST** LSM, **idempotency-key exactly-once**,
> **multi-key transactions**, **WAL-shipping DR**, or **Raft quorum** are the
> **target design (v2)** — read them as the roadmap, not current v1 guarantees.
> DR beyond one disk (replication / WAL-shipping) is v2; for v1, take periodic
> file backups of the store (it's a single file + its `.snap`).

## Failure taxonomy — what survives what

| Failure | Mechanism | Guarantee |
|---|---|---|
| Process crash / OOM-kill / `kill -9` / restart | WAL replay from last fsync | No loss up to the last **acked** write |
| Power loss / kernel panic | fsync + CRC'd WAL records (torn-write detection) | No loss of acked writes; unacked in-flight discarded |
| Partial disk corruption | per-record CRC + LSM block checksums | Detected → repair from replica/backup |
| Disk / node total loss | cluster: Raft quorum · embedded: continuous WAL shipping | Cluster **RPO 0**; embedded RPO ≈ ship lag (seconds) |
| Region loss | async cross-region replica / remote backup | Bounded RPO (async) |
| In-flight request at crash | commit boundary + idempotency keys | Exactly-once **effect** on retry |

## The write path and the durability point

```
serialize → append to WAL (CRC'd record) → GROUP-COMMIT fsync → apply to memtable → ACK
                                            └─ durability point ─┘
```

- **Group commit** is the enabling mechanism, not an optimization: concurrent
  writers share **one fsync** per ~0.5–1 ms window. Naive fsync-per-write caps a
  single SSD at ~1–2k durable writes/sec; group commit lifts that to tens of
  thousands because a higher write rate just fills a bigger batch under the same
  fsync. Latency = one fsync (bounded, constant); throughput scales with load.
- The **memtable** (RAM) is volatile by design — reconstructable from the WAL, so
  losing it on a crash costs nothing.
- Periodic memtable → immutable **SSTable** flush; a crash-safe **MANIFEST**
  records the live SSTable set; the WAL prefix before the flush is truncated.

Nothing is acked before it is on stable storage. That is the whole contract.

## Restart / crash recovery (bounded, tail-only)

Recovery replays only the WAL *tail* since the last flush — seconds of log, not
history:

```
boot:
  1. read MANIFEST            → the durable LSM state (crash-safe)
  2. open referenced SSTables → already-flushed data
  3. locate WAL segment(s) after the last flush
  4. for each record in WAL tail:
        if CRC valid → apply to fresh memtable
        else         → STOP        (torn record = the crash boundary)
  5. state == last acked write. Serve.
```

The first torn/invalid record *is* the crash boundary: everything past it was
never fully written, hence never acked. There is nothing to roll back — it simply
isn't there. No full-scan integrity check, no "repairing database" screen.

## In-flight requests (the subtle case)

An in-flight request straddles the durability point:

- **Died before fsync** → not durable, client got **no ack** → absent on restart →
  client retries. Correct.
- **Died after fsync, before ack reached client** (network dropped) → durable and
  replays, but the client doesn't know → a naive retry double-applies.

Fix: **idempotency keys.** Every mutation carries a client-assigned op id; the
engine dedups applied ids within a window and returns the original result on
retry. At-least-once delivery → **exactly-once effect.** For the reactive path
this is free — the key is `(session, op-seq)`, and Sky.Live already ships both the
client-side `__skyEventQueue` (FIFO replay on reconnect) and server seq ordering.
BlueDB's idempotency keys are what make that existing replay *safe* to re-fire.

**Multi-key transactions** recover from the **commit record**: the commit marker
is the durable commit point — recovery rolls intents *forward* if present, *aborts
+ cleans up* if not. The **deterministic-Sky-transaction** model is simpler still:
the ordered command log is the source of truth, so a crashed executor just re-runs
the log from the last checkpoint and lands on the identical state — no intents to
resolve. Determinism turns crash recovery into replay.

## Reactive-layer recovery (how the magic survives a restart)

The Model is **durable in BlueDB**, not held in process RAM — so:

- On server restart, a reconnecting session **rehydrates its Model from BlueDB**.
  Sky.Live already reconnects the SSE and resyncs on `hello`; `autoBlueDB` extends
  that to "reload Model for this scope." User sees a ~1 s reconnect, state intact
  up to the last durable write.
- **Optimistic writes rebase.** Unacked client ops replay with their idempotency
  keys and rebase against the authoritative BlueDB state (Replicache/Zero-style
  server-authoritative rebase). Acked updates never vanish; only never-acked ones
  can drop — correct, because they were never durable.

"Server restart mid-edit" degrades to "brief reconnect, no lost committed state."

## Disaster recovery (disk / node / region loss)

Restart recovery handles a crash; DR handles losing the disk.

- **Embedded / single-node:** continuously ship WAL segments + periodic SSTable
  snapshots to an object store (GCS/S3). RPO ≈ ship lag (seconds). Restore = last
  snapshot + replay shipped WAL; **PITR** = replay to a chosen timestamp. (Same
  shape as the darraghstudio bucket backup, but continuous, not nightly.)
- **Cluster mode:** **Raft quorum** — acked only after a majority hold the write.
  Node/disk loss keeps serving from survivors; a fresh replica rebuilds from a
  peer snapshot + log. **RPO 0** for acked writes; RTO ≈ leaseholder failover
  (seconds). Cross-region async replica + remote backup covers region loss.

## Durability is a knob (tied to the consistency menu)

| Mode | Ack after | Survives | Cost |
|---|---|---|---|
| Relaxed / ephemeral | WAL buffered (pre-fsync) | process crash, **not** power loss | lowest latency — presence/cursors/UI scratch |
| **Strong (default)** | fsync (embedded) / quorum (cluster) | power loss / node loss | one group-commit fsync (~1 ms) |
| Replicated-durable | cross-region quorum | region loss | + inter-region RTT |

Hot path default = **Strong via group commit** (durable *and* fast). Drop to
Relaxed only where a few lost milliseconds genuinely don't matter.

## Crash-consistency test harness (day one, not bolted on)

None of the above is real until it survives fault injection. Required from the
first commit of the engine:

| Injection | Invariant asserted |
|---|---|
| `kill -9` at every fsync boundary (deterministic schedule) | after recovery, exactly the acked-prefix is present; no torn record applied |
| Simulated torn write (truncate/scribble last WAL record) | recovery stops at the boundary; no partial apply |
| Power-loss sim (drop un-fsync'd pages) | no acked write lost; no unacked write surfaces |
| Disk-full mid-write | write fails cleanly (not-acked); no corruption; recovers |
| Idempotency replay (re-fire every op) | exactly-once effect; no double apply |
| Clock skew / HLC uncertainty (cluster) | no stale read violates linearizability |
| Jepsen partition + nemesis (cluster) | no lost/torn committed writes across partitions |

This is the same discipline the Sky compiler already lives by (differential
oracle + fuzzers), applied to durability. A durability claim without this harness
green is not a claim.
