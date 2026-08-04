# BlueDB roadmap (2026-08-04)

Synthesized from a four-way deep analysis (querying · multi-writer/scaling ·
security · compat/reliability). Ranked: make what exists **safe** before building
more on it → the two headline features → production hardening → the v2 rework.

## Tier 0 — Data-safety floor (do first; small; protects existing data)
- **G1 [CRITICAL] WAL magic + version header + refuse-newer gate.** The WAL has no
  version byte; an older binary replaying a newer WAL hits an unknown opcode,
  treats it as a torn tail, and `Open` **truncates the file** (silent data
  destruction). Snapshot format already does this right — copy it.
- **G2 [CRITICAL] Mid-file corruption → refuse, don't truncate.** A rotted byte in
  record 5/10000 currently discards records 5–10000. A valid record *after* an
  invalid one ⇒ corruption ⇒ fail closed. Add a mid-file-scribble fuzz.
- **G3 Log + metric on every recovery truncation** (silent today).
- **G7 Index manifest: refuse a newer format** (same class as G1).
- **F7 `0o600` on WAL/snapshot + engine-level NUL/reserved guard (F5)** (files are
  world-readable `0o644`; NUL/reserved rejection is kernel-only so console-mutate +
  CLI can corrupt the index keyspace).

## Tier 1 — Querying / filtering (headline feature #1)
Reuse the `Std.Db.Store` `Cond` builder; expose query terminals on `Std.Persist`
that dispatch `SqlConn`/`KvConn` so the SAME query compiles on both backends. KV
kernel: index-seek one sargable eq/range leaf → re-evaluate the full `Cond` in Go
over decoded JSON via the order-preserving encoder (no injection, fail-fast on
unknown columns). Phase 1: full `Cond` + single-index accel + single-key sort +
limit/offset/toList/toMaybe/count. Phase 2: multi-index AND/OR, aggregates.
Joins/GROUP BY stay SQL-only. **Bake in security must-fixes:** F1 skip-reserved,
F2 mandatory cap + streaming (Scan not ForEach), F4 enforced collection/tenant
scope, F3 console redaction. Cost: indexed = O(m log m); non-indexed = O(n)
memtable scan + per-row JSON decode (cold/analytics path, never the hot path).

## Tier 2 — Multi-writer ergonomics (headline feature #2 — mostly already done)
Concurrent in-process writers ALREADY scale via group-commit (~51k durable
writes/s at high concurrency, one fsync per ≤1024-write batch). Work = surfacing:
document "open once, share the handle"; expose `BlueDB.batch`/`withBatch`
(multi-key atomic + one fsync); per-store `Sync=false` relaxed tier (~319k/s);
stripes 256→4096. Multi-*process* write is the irreducible floor — never build it.

## Tier 3 — Graduation-story integrity (compat) — **IN PROGRESS**
**G5: enforce the SQL schema guarantees on KV** so a `Store` behaves identically
on both backends. Scope decided with the user:
- **D1** Add `Persist.insert : Conn cap -> Collection a -> a -> Task Error a`
  (returns the row with generated fields filled). `put` upserts + assigns a serial
  id when the PK is unset.
- **D2** KV default rule = "apply the default when the field is its ZERO value"
  (the record always carries all fields on KV; no SQL 'column omitted'). Correct
  for timestamps; documented caveat for deliberately-zero values.
- **D3** **Per-collection** namespacing — records, secondary indexes, unique
  constraints, and the serial sequence are all scoped by the collection name, so
  one BlueDB store cleanly holds multiple ISOLATED collections. (Also fixes a
  latent bug: today `all`/`scan`/`count` over one store see every collection.)
- Enforce: **unique** (per-(field,value) cross-pk lock + unique index
  `\x00x\x00u\x00<coll>\x00<field>\x00<value> → <pk>`), **serial** (per-collection
  seq counter, assigned + `Put(seq)` in the same WriteBatch = atomic/crash-safe),
  **defaultNow/touchOnUpdate/default***.
- Reuses the E2/R1 index infra (reserved keyspace, striped locks, WriteBatch,
  order-preserving encoder, manifest+reindex migration). Layout change (records
  bare-pk → collection-prefixed) needs a manifest LAYOUT VERSION + boot migration
  (ties to Tier-0 versioning).
- **G6** keyspace partition for the unified-store vision (sessions gob vs app JSON).
- Detailed phased design + concurrency/lock-ordering spec: produced by the design
  agent + grill, then implemented P1 (namespacing+migration) → P2 (defaults/touch)
  → P3 (serial+insert) → P4 (unique), each three-leg tested.

## Tier 4 — Operability / production-readiness
- **G4 `sky bluedb <path> verify`** (full CRC scan, reports first bad offset, no
  truncate) — the ONLY defense against G2; land it earlyish. Then `backup` +
  segmented-WAL / ship-before-truncate (checkpoint `Truncate(0)` destroys the log
  today, so "the WAL is the stream" is false — Litestream-style needs segments).
- **G8** fuzz gaps: power-loss/un-fsync'd-page-drop model + torn-record-mid-index-
  WriteBatch (index crash test uses a graceful close today).
- G9 reindex races serving; G10 no cross-process lock on Windows; G11 shared-handle
  close footgun (refcount).

## Tier 5 — v2 (the storage rework; NOT now)
- **On-disk sorted storage (SSTable/LSM or B-tree)** → range/scan O(log n + k)
  instead of O(total keys); kills the in-memory sort + the full-snapshot checkpoint
  pause (synchronous O(working-set) rewrite every 10k writes — first thing to
  degrade a large store). The order-preserving key encoding is already SSTable-ready.
- Incremental checkpoint + spill-to-disk (removes the RAM/MaxKeys ceiling).
- Distributed tier (Raft per range-shard, Calvin-style deterministic Sky txns) —
  the only real horizontal *write* scale + the answer to single-node SPOF/HA.

## Irreducible floor
1. One committer per open file is permanent + correct. "Scaling writers" = more
   concurrency into that committer (done) + more shards each single-writer (v2).
   Never multiple committers on one file.
2. Multi-process write on one file: never (seq ownership, checkpoint truncation,
   per-process memtable make it meaningless).
3. Non-indexed queries are O(n) scans until v2 sorted storage — analytics/cold-path.

## Recommended sequence
Tier 0 → Tier 1 (querying, with F1/F2/F4 baked in) → Tier 2 (surfacing) → Tier 4
`verify` → Tier 3 (compat gating / schema enforcement — started) → Tier 5 (v2, a
separate strategic effort). Start with Tier 0 G1 (small, stops silent data loss).

## What's solid today (don't re-litigate)
Steady-state crash-consistency (torn-tail recovery, WriteBatch all-or-nothing, seq
guard) is well-built + fuzzed. Snapshot format (magic+version+refuse-unknown+atomic
rename) is the model the WAL should copy. Order-preserving index encoding (R1) is
coherent + SSTable-ready. Reserved-key hiding on the Sky `keys`/`scan` surface is real.
