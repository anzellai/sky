# BlueDB KV schema enforcement + per-collection namespacing — design

Goal: a `Std.Db.Store` behaves identically on the KV backend (`Std.Persist` +
`Std.BlueDB`) as on SQL — `unique` / `serial` / `defaultNow` / `touchOnUpdate` /
`default*` all enforced — plus per-collection keyspace isolation. Decisions:
**D1** add `Persist.insert` (returns the row with generated fields filled; `put`
upserts + assigns a serial id when the PK is unset); **D2** KV default = "apply
when the field is its ZERO value" (no SQL 'column omitted' on KV); **D3**
per-collection namespacing of records + indexes + uniques + serial.

## Key layout (all under the reserved `\x00x\x00` space, hidden from raw keys/scan)
`C` = collection name (`Store.fromCodec "name"` → `s.name`; identifier-safe).
- **Record:** `\x00x\x00d\x00 C \x00 <pk>` → codec JSON. (Records move under `d\x00C`
  so they're namespaced AND hidden from the raw string-KV surface; the two layers
  never see each other.)
- **Secondary index:** `\x00x\x00i\x00 C \x00 <field> \x00 E(v)<pk>` (fixed int/bool,
  no delim) / `… <value> \x00 <pk>` (variable text). Order-preserving `E` unchanged.
- **Unique index:** `\x00x\x00u\x00 C \x00 <field> \x00 E(v)` → `<owner-pk>`.
- **Serial seq:** `\x00x\x00s\x00 C` → decimal `<lastId>`.
- **Per-collection manifest:** `\x00x\x00m\x00 C` → `{layout, indexes, uniques, serialPk}`.

`bluedbFieldPrefix`/`bluedbEqPrefix`/`bluedbExtractPk` generalise by taking the
longer `C`-qualified prefix; extract-pk logic (fixed offset / first-NUL) unchanged.
NUL discipline extends to `C` + `<field>` (identifiers → safe). Note: `bluedbReserved`
now means "all enforced-collection data", not just index/manifest — update its docs.

## Plumbing: Store → Persist → kernel
- **Store.sky:** add `schemaOf : Store a -> StoreSchema` where
  `StoreSchema = { name, pk, cols : List (String,String), generated : List String,
  computed : List (String, () -> SqlValue) }`. `cols` carry the flag suffixes
  (`|u` unique, `!` serial, `|dnow`, `|touch`, `|dtext=/|dint=/|dbool=`);
  `generated`/`computed` (defaultWith) aren't in `cols` so need the accessor.
- **Persist.sky:** `kvDescriptor : Collection a -> KvSchema` (collection=name,
  keyField, cols passthrough, indexes via `indexFieldTypes`, computed pre-evaluated
  to `(col,valueString)` since `()->SqlValue` closures can't cross to Go). colType
  via existing `colTypeFor`. The KERNEL parses the flag grammar (reuse
  `codecColExtras`/`codecColIsAutoInc`/`codecColIsTouch` in db_codec.go) → one
  grammar shared SQL↔KV, no drift.
- **New kernel** `runtime-go/rt/bluedb_collection_kernel.go`: `BlueDB_collPut`
  (returns stored JSON with id+timestamps filled), `collGet`/`collDelete`/`collAll`/
  `collCount`/`collFindByIndex[Range]`/`collReindex`, all taking `C`.
- **Persist verbs:** `put` (upsert, isInsert=False), new `insert` (isInsert=True,
  decode returned JSON → `a`), `get/delete/all/count/scan/findByIndex/reindex`
  thread `C`; `all`/`count` become `C`-prefix scans (fixes the whole-store bug).

## Migration (layout versioning; ties to Tier-0)
Per-collection manifest carries `layout:int`. legacy = global manifest + bare-pk
records = `layout 0`. `collReindex` at startup (before serving): if
`layout >= CURRENT` and sets unchanged → O(1) skip; else FULL relayout under pk
locks (re-key bare records to `\x00x\x00d\x00C\x00pk`, rebuild namespaced index +
unique entries, set serial counter = max int pk, sweep old un-namespaced index),
write manifest `layout=1` LAST (crash → stays 0 → next boot redoes). Refuse-newer
gate on `layout > CURRENT`. **Ambiguity:** legacy bare keys carry no `C` → the
migration attributes them to the running collection and HARD-ERRORS if a different
collection's `\x00x\x00d\x00` records already exist (never silently merge). Safe for
single-collection exp stores; document the one-time reindex + wipe-and-reseed hatch.

## Defaults / touch (D2)
Insert-vs-update = `db.Get(recordKey)` inside the pk lock. Decode JSON → mutate map:
- insert: `dnow` fields (zero → now), `dtext=/dint=/dbool=` (zero → default),
  computed overrides (defaultWith).
- update: `touch` fields → now (override).
Re-encode → that JSON is both stored AND returned (so `insert` sees stamped values).
`now` = `time.Now().UTC()` in SQLite `datetime('now')` text shape (cross-backend
parity), via an injectable `var bluedbNow` (test-overridable, deterministic).
**D2 lossiness:** deliberate-zero is indistinguishable from unset on KV → document
per-field; parity is sound for timestamps (0==unset), narrow+documented gap for
deliberately-zero scalars with a declared default.

## Serial (atomicity + crash-safety)
Per-collection counter `\x00x\x00s\x00C` under a per-collection seq lock (stripe by
`id|C`). On serial insert with unset pk: lock seq (outermost) → next = Get+1 → set
pk → lock pk → ONE WriteBatch { Put(record), Put(seqKey, next), index+unique } →
one fsync. Crash outcomes: batch durable → record@N AND seq=N (consistent); not
durable → neither (next insert reassigns N to a different record — no gap, no
double-assign, no lost bump). Single-batch is load-bearing: a separate counter
batch could leave a durable record with a lost bump → id reuse → PK collision.
Serial inserts serialize on the seq lock; non-serial puts don't.

## Unique (keystone — cross-pk TOCTOU)
The race is CROSS-pk (two pks racing for one value) → E2's per-pk lock doesn't
serialize it. Add a per-`(C,field,E(value))` stripe lock. Key
`\x00x\x00u\x00C\x00field\x00E(v)` → owner-pk. In collPut: for each unique (field,v):
`owner,ok = Get(uniqueKey)`; `ok && owner != thisPk` → Err (unique violation), no
batch. Update-changes-value: Delete(old)+Put(new) in batch. Delete: remove.
**Lock order (deadlock-free):** seq lock (outermost, if serial) → per-(field,value)
locks acquired in TOTAL byte order of the unique keys (sort → same order for all
writers → no cycle even with multiple unique cols) → pk lock (innermost).
**Proof:** two inserts for value v contend on the same (field,v) stripe → serialize;
winner writes owner, WriteBatch commits (memtable visible before lock release);
loser reads owner≠self → Err. Read+write bracketed by the same held lock ⇒ ≤1 owner
per (C,field,v). ∎ Caveats (test): self-upsert (owner==thisPk OK); unique-is-serial-pk
(distinct locks, order holds); NULL/absent unique value → SKIP the entry (SQL allows
multiple NULLs; empty E(v) would collide all-null records).

## Phasing (each ships + tests in isolation; three-leg per phase)
- **P1** per-collection namespacing (records+indexes+uniques+seq+manifest) + `coll*`
  kernels + Persist threads C + `all`/`count` C-scoped + layout-0→1 migration +
  refuse-newer. Tests: key round-trip, reserved-hidden, extract-pk; migration fuzz
  (count preserved, no bare keys, hard-error on foreign collection); e2e two
  collections isolated (whole-store-bug regression — red before P1).
- **P2** defaults/touch injection + injectable clock. Tests: zero-value application,
  touch-on-update-not-insert, D2 boundary (deliberate-false + defaultBool True).
- **P3** serial + `Persist.insert`. Tests: counter reopen, N-goroutine distinct
  contiguous ids (-race), crash-injection between batch+fsync → no partial/lost.
- **P4** unique (keystone). Tests: dup→Err, self-upsert OK, value-change frees old,
  delete frees, NULL-skip; M-goroutine same-value → exactly 1 wins (-race + stress);
  opposite-field-order two-unique-col inserts → no deadlock; crash mid-batch atomic.
- Milestone gate at phase boundaries: cargo test --workspace + go test -race
  ./runtime-go/... + example-sweep + a real Persist example exercising all four
  guarantees on BOTH backends.

## Files
`runtime-go/rt/bluedb_index_kernel.go` (generalise prefixes+manifest per-collection),
NEW `runtime-go/rt/bluedb_collection_kernel.go` (coll* + defaults/serial/unique),
`sky-stdlib/Std/BlueDB.sky` (bindings), `sky-stdlib/Std/Persist.sky` (insert +
descriptor + C-scoped verbs), `sky-stdlib/Std/Db/Store.sky` (schemaOf/StoreSchema).
Reuse: codecColExtras/codecColIsAutoInc/codecColIsTouch (db_codec.go:46-80),
schema `now` shape (schema_kernel.go:191), WriteBatch atomicity (db.go:266,
wal.go:266), stripe-lock+manifest-migration patterns (bluedb_index_kernel.go).
