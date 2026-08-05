# BlueDB roadmap (2026-08-04)

Synthesized from a four-way deep analysis (querying · multi-writer/scaling ·
security · compat/reliability). Ranked: make what exists **safe** before building
more on it → the two headline features → production hardening → the v2 rework.

## Tier 0 — Data-safety floor (do first; small; protects existing data) — **SHIPPED**
Status: G1/G2/G3 shipped (`5a5ac745` — WAL `BWAL` version header + refuse-newer +
legacy-headerless migration; mid-file-corruption fails closed vs torn-tail
truncate + a 60-seed scribble fuzz; recovery-truncation log + `RecoveryStats()`).
G7 already enforced (`BlueDB_collReindex` refuses `manifest.Layout >
bluedbCollLayoutVersion` at the mandatory startup migration). F7 shipped —
WAL/snapshot files now `0o600` (`TestF7FilePermsAre0600`), and the engine-bypass
write paths (console `HandleConsoleDataMutate`, `sky bluedb put` CLI) reject a
NUL-containing key so they can't corrupt the reserved index/manifest keyspace.
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

## Tier 1 — Querying / filtering (headline feature #1) — **P5 SHIPPED (Phase 1)**
Reuse the `Std.Db.Store` `Cond` builder; expose query terminals on `Std.Persist`
that dispatch `SqlConn`/`KvConn` so the SAME query compiles on both backends.
**Shipped (P5):** `Persist.query |> where_/orderAsc/orderDesc/limit/offset` +
`toList`/`toMaybe`/`toCount` terminals + the full `Cond` builder
(`eq/neq/gt/gte/lt/lte/like/isNull/notNull/inList/and_/or_/not_`) and value
builders (`string/int/float/bool`), all re-exported from Persist so a KV app
queries from one import. KV kernel (`BlueDB.collQuery`/`collQueryCount`,
`runtime-go/rt/bluedb_query_kernel.go`): full collection scan → evaluate the
serialized `Cond` plan (`Store.planJson`) in Go over each decoded record; ORDER
BY sorts in-memory post-scan. **Security baked in:** F1 (scan the exact
`\x00x\x00d\x00<coll>\x00` record prefix — never reserved/index/other-collection
keys), F2 (mandatory 10k result cap + early-stop). e2e parity SQL≡KV proven in
`examples/55-persist-query`. Cost: O(n) memtable scan + per-row JSON decode — a
cold/analytics path, NEVER the reactive hot path (declared `index` +
`findAllByIndex` is the point-lookup path).
**Index acceleration (shipped on top of P5 — equality AND range):** an
AND-reachable equality OR ordering leaf on a declared `index` field now SEEKS the
index for candidate pks instead of full-scanning — O(matches) not O(collection).
The full `Cond` is still re-evaluated on each candidate, so the seek only narrows
(never mis-includes/excludes).
- **Equality** (`bluedbPickEqSeek` + `bluedbCollEqCandidatePks`): gated to
  `(text,str)` + `(int,int)` where the plan value's string encoding provably
  matches the stored index encoding.
- **Range** (`bluedbPickRangeSeek` + `bluedbCollRangeCandidatePks`, reuses the R1
  order-preserving `bluedbCollRangeScan`): `>=`/`>` → inclusive lo, `<` →
  exclusive hi — a SUPERSET-safe mapping (a `>` includes the boundary which the
  full Cond drops); a lone `<=` yields no superset-safe hi so it does NOT seed a
  seek (full-scan). Gated to int/text.
- An eq/range leaf under OR/NOT is not necessary and does NOT seed a seek.
- Differential tests assert seek-result ≡ full-scan-result
  (`TestCollQueryIndexSeek`/`TestPickEqSeek`/`TestCollQueryRangeSeek`/
  `TestPickRangeSeek`); e2e in `examples/55-persist-query` (declares
  `index "status"` + `index "age"`, exercises both eq- and range-seek).
**Phase 2 (deferred):** multi-index AND/OR planning (currently one index per
query); aggregates; exact Money/Time/Bytes predicate representation; index-seek
for bool/float eq once encoding parity is verified. Joins/GROUP BY stay SQL-only.
F3 (console redaction) rides with the surfacing tier.

## Tier 2 — Multi-writer ergonomics (headline feature #2 — mostly already done)
Concurrent in-process writers ALREADY scale via group-commit (~51k durable
writes/s at high concurrency, one fsync per ≤1024-write batch). Work = surfacing:
document "open once, share the handle"; expose `BlueDB.batch`/`withBatch`
(multi-key atomic + one fsync); per-store `Sync=false` relaxed tier (~319k/s);
stripes 256→4096. Multi-*process* write is the irreducible floor — never build it.

## Tier 3 — Graduation-story integrity (compat) — **P1–P4 SHIPPED**
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
  → P3 (serial+insert) → P4 (unique) → P5 (Cond querying), each three-leg tested.
  **All five phases shipped.** Runtime: `runtime-go/rt/bluedb_collection_kernel.go`
  + `bluedb_query_kernel.go`; Sky: `Std.BlueDB` `coll*` verbs, `Std.Persist`
  universal + query surface, `Std.Db.Store.planJson`. e2e demos:
  `examples/53-bluedb-migration`, `examples/55-persist-query`.

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
