# BlueDB Phase 2 — MVCC transaction + validated commit (L2, embedded)

> **Status:** architecture design, `feat/bluedb`. This is the doc Phase 2 **grills**,
> then implements. No production code here — Go interfaces + struct sketches, the
> validation algorithm, the exact Phase-1 touch-points, the serializability proof,
> the conformance-suite list, and a risk register (the grill seed).
>
> **Builds ON:** the committed, Judge-verified Phase-1 engine at
> `runtime-go/bluedb/` (`engine.go`, `committer.go`, `changelog.go`, `reader.go`,
> `watermark.go`, `keys.go`, `hlc.go`, `pebble_engine.go`, `gc.go`). Every
> `file:line` citation below is relative to that package unless it names a `docs/`
> path. **The irreversible on-disk format (`keys.go`, `comparer.go`) is FROZEN — Phase
> 2 does not touch it. It extends only at the Go interface level.**
>
> **Realizes:** clean-slate-architecture.md §L2, **Decision 4** (REAL SERIALIZABLE via
> index-RANGE read-set validation), §6.1 (write path + bounded retry + hot-key
> fallback), §7 Phase 2; phase1-engine-design.md §4.2 (the `KeyChange`/`IndexCoord`
> changelog encoding), §8.2 (the C1–C7 contracts Phase 2 consumes).

---

## 0. TL;DR — the one-paragraph thesis

Phase 1 shipped the substrate: an MVCC-versioned, single-writer, group-commit engine
whose `CommitReq` **already carries** the two Phase-2 hooks — `ReadTs HLC` and
`ReadSet *ReadSet` (`engine.go:99-103`, `nil ⇒ blind-write fast path`) — and whose
committer **already assigns a distinct `commitTs` per job** in FIFO drain order
(`committer.go:83-108`, the comment even says "Phase-2 SSI needs each transaction to
carry its own commitTs"). Phase 2 adds, entirely in the `bluedb` Go package: (1) a
Go-level `Txn` (Begin/Get/Scan/Put/Delete/Commit) that captures a **read-set** (point
keys AND scanned **index ranges**) and buffers a **write-set** with **read-your-writes**
overlay; (2) the `KeyChange`/`IndexCoord` codec that L2 encodes into the opaque
`CommitReq.ChangelogPayload`; (3) a **commit-time validator** that runs **inside the
single committer** — the serialization point — testing each read-set entry against the
`KeyChange`s committed in `(readTs, commitTs]`, answered from an **in-RAM recent-changes
ring** (bounded by the GC watermark) so it is off the Pebble hot path; (4) an
optimistic **Transact(body)** driver with bounded retry → typed `ErrConflict`, and a
committer-arbitrated **per-hot-key FIFO lease** that makes a genuinely contended key
starvation-free. Blind single-key writes keep `ReadSet == nil` → validation is a no-op
→ the OLTP firehose is untouched. The isolation delivered is **strict serializability
on a single node**: the total-order committer serializes commits, and index-range
validation catches predicate phantoms that plain SI misses.

**The single load-bearing new invariant:** because `Txn.Begin()` takes its snapshot via
the begin-snapshot path (§1/§3) — which picks `readTs = durableHi` (the durably-applied
high-water, `pebble_engine.go:82-96`), pins the Pebble snapshot **atomically** with that
choice, and **registers the readTs in the watermark registry** (`watermark.go:35-47`) —
GC's threshold `T` (min over live readers, `watermark.go:87-99`) can never advance past
an open transaction's `readTs`. Sourcing `readTs` from `durableHi` (not the assigned
high-water `hlc.next()` bumps before `Apply`) is what makes the `(readTs, commitTs]`
validation window boundary sound (R-2.8): everything `≤ readTs` is provably in the
snapshot, everything `> readTs` up to `commitTs` is in the ring window, no boundary
blind-spot. Therefore the recent-changes ring (floored at `T`) **always covers `(readTs,
high-water]` for every live transaction**, and validation never needs a Pebble read in
steady state (except the bounded ring-cap spill, §4.2). Validation retention and
version-GC retention are the **same watermark**.

---

## Grill outcomes (Phase-2 design close)

A 2-adversary grill of this design ran against the Phase-1 engine (`runtime-go/bluedb/`).
Verdict + the resulting revisions:

- **The SSI core is architecturally sound and is NOT redesigned.** Validation at the
  serialization point (the single committer, §4.1), the index-RANGE read-set (§2.2), the
  post-`Apply` ring commit (§4.3), the intra-batch `pending` accumulator (§4.3), and
  read-your-writes (§7.5) all hold as designed. Everything below is a fix layered onto
  that core, not a change to it.

- **Two correctness-critical gaps closed — "serializable" now holds.** As originally
  written the SERIALIZABLE claim was **false** in two spots:
  1. **Index-encoding drift (R-2.1 / R-C3).** The scan bound `[lo,hi)` and a change's
     `NewIndex`/`OldIndex` coord were produced by two encoders in two layers → silent
     drift → under-reject → phantom commit. Closed by **ONE canonical encoder**
     (`encodeIndexKey`, §2.2/§3.3) used by both sides, plus a **conservative fail-safe
     fallback** for the colTypes/predicates with no proven order-preserving encoding
     (real, money, blob, IS-NULL). SERIALIZABLE is now guaranteed for **all**
     colTypes/predicates; range-optimized validation covers int/text/bool (+composite/
     descending), the rest use the coarser-but-correct fallback. This closes the R-C3
     item handed forward from Phase 1.
  2. **Validation-window boundary (R-2.8).** `readTs` was drawn from the *assigned*
     high-water (`hlc.next()` raises `c.last` BEFORE `Apply`), so a commit stamped
     exactly at `readTs` could be invisible to BOTH the Pebble snapshot (pinned before
     that commit's `Apply`) AND the half-open `(readTs, commitTs]` window → serializability
     violated. Closed by sourcing `readTs` from the **durably-applied high-water**
     (`durableHi`, advanced post-`Apply`, `pebble_engine.go:90`/`149`) and pinning the
     Pebble snapshot **atomically** with picking `readTs` (§1/§3, R-2.8).

- **Two mechanisms reworked (must-close):**
  3. **Ring `trim`/`append` data race.** `trim` runs on the GC goroutine
     (`advanceThreshold`, `watermark.go:110`), `append`/`after` on the committer — the
     "single-writer, no lock" claim was false in Phase 2. Closed by **marshalling `trim`
     onto the committer goroutine** (GC enqueues a trim request; the committer drains it
     at the top of each drain) so the ring is only ever mutated by one goroutine (§4.2,
     touch-point #7). `go test -race` gates it.
  4. **Hot-key lease.** The single-culprit mid-body acquisition admitted a concrete
     `X<Y` multi-key deadlock. Reworked to **strict-2PL discovery**: run the body once to
     discover the full hot-key set, abort, acquire all touched leases in `bytes.Compare`
     canonical order, re-run under the held set (§6). Range/predicate contention has **no
     lease** → bounded optimistic retry + typed `ErrConflict`; starvation-freedom is
     stated honestly as covering **point-key contention only**.

- **Four fixes (pseudocode / hot-path):** (5) restore `e.sealed.Store(true)` on the
  `Apply` error branch (§4.3); (6) pre-scan the drained batch so an all-blind-write batch
  pays **zero** SSI cost (§4.3); (7) track an acked-set so inline-aborted jobs are not
  re-acked by the seal/recover loop (§4.3); (8) a hard ring-size cap with spill-to-
  `Changelog.Tail` so a leaked reader token can't grow the ring unbounded in RAM (§4.2,
  R-2.4).

The risk register (§9) records each as RESOLVED with its mechanism.

---

## 1. The `Txn` API + lifecycle (the Go-level mechanism)

Phase 3's Sky `Persist.transaction conn (\tx -> body)` calls this Go mechanism; Phase
2's conformance suite calls it directly. Two altitudes:

- **`Engine.Transact(body)`** — the full optimistic loop (Begin → run body → Commit →
  retry-or-lease → typed error). This is what Phase 3 wires to.
- **`Engine.Begin()` / `txn.Commit()`** — the single-attempt primitives `Transact`
  drives (and the conformance suite pokes directly to force interleavings).

### 1.1 New Engine surface (additive to `engine.go`)

```go
// Added to the Engine interface (engine.go:29-63). Additive — no existing method changes.
type Engine interface {
    // ... existing Snapshot/NowTs/Commit/Changelog/Readers/GC/Close ...

    // Begin opens a single-attempt transaction pinned at a fresh snapshot+readTs via
    // the begin-snapshot path (§3.4): readTs = durableHi (the durably-applied high-water,
    // NOT the assigned high-water), the Pebble snapshot pinned atomically with that choice,
    // and the readTs registered in the watermark — the retention invariant (§0) AND the
    // window-boundary soundness (R-2.8). The body reads through the Txn (recording the
    // read-set) and buffers writes; txn.Commit() funnels one CommitReq to the committer.
    Begin() (*Txn, error)

    // Transact runs the optimistic loop: Begin → body → Commit; on ErrConflict it
    // re-runs body against a FRESH snapshot (bounded retry + backoff), and on a
    // detected hot key acquires a committer-issued FIFO lease (§6). Returns nil on
    // durable commit, or a typed ErrConflict after the retry bound. The body MUST be
    // pure (re-runnable, no external effects) — §5.3.
    Transact(body func(tx *Txn) error) error
}
```

### 1.2 The `Txn` struct

```go
// Txn is one transaction attempt. NOT safe for concurrent use by multiple goroutines
// (a transaction body is sequential). Lives entirely in package bluedb (L2-embedded is
// realized as more Go code in runtime-go/bluedb/, not a separate package — §3.1).
type Txn struct {
    e      *pebbleEngine
    reader Reader          // begin-snapshot (§3.4) — pins readTs=durableHi + registers the token
    readTs HLC             // == durableHi at Begin; the (readTs, commitTs] window's lower edge (R-2.8)

    // read-set (§2) — grows as the body reads.
    points  map[string]pointRead    // key: string(userKey)
    ranges  []indexRange

    // write-set (§1.3) — buffered, applied atomically at Commit.
    writes  map[string]bufferedWrite // key: string(userKey); last-write-wins within the txn

    // indexer maps a (userKey, record) to its index coordinates for the collections
    // this txn touches. Set at Begin from the collection registry; Phase 3 populates it
    // from L0 (Codec + Collection.indexes). Phase-2 tests supply a trivial indexer.
    indexer func(userKey, record []byte) []IndexCoord

    done bool // Commit/Abort called → further ops error
}

type bufferedWrite struct {
    op       Op       // OpPut | OpDelete (keys.go / engine.go:6-11)
    value    []byte   // put: row bytes; delete: nil
    newIndex []IndexCoord // put: positions the row enters; nil for delete
    oldIndex []IndexCoord // update/delete: positions vacated; derived from the pre-image (§1.4)
}
```

### 1.3 The read + write methods

```go
// Get resolves userKey with read-your-writes overlay, then records a point read.
//   1. write-set overlay: a buffered Put → (value, true); a buffered Delete → (nil, false).
//      An overlaid key is NOT added to the read-set (the txn's own write, not a dependency).
//   2. else reader.Get (engine.go:67) → records points[userKey] = {versionSeen, present}.
func (tx *Txn) Get(userKey []byte) (value []byte, ok bool)

// Scan records the FULL index range as a read-set entry (the SSI crux, §2.2), then
// returns an ordered cursor over the range with the write-set merged in (read-your-writes
// over a range — buffered puts in [lo,hi) appear, buffered deletes mask). lo/hi are keys
// in the SAME order-preserving index encoding coordinates use (§2.2, §3.3).
func (tx *Txn) Scan(index IndexID, lo, hi []byte) Cursor

// Put buffers an upsert. newIndex = tx.indexer(userKey, value). oldIndex is derived
// once, lazily, from the pre-image read at readTs (§1.4). Last-write-wins within the txn.
func (tx *Txn) Put(userKey, value []byte) error

// Delete buffers a versioned delete. oldIndex derived from the pre-image (§1.4).
func (tx *Txn) Delete(userKey []byte) error

// Commit builds one CommitReq and funnels it to the committer (§4). Returns ErrConflict
// (validation failed — the driver retries) or a durability error, else nil. Idempotent
// after the first call (sets tx.done); Close()s the reader.
func (tx *Txn) Commit() error

// Abort releases the snapshot without committing (Close()s the reader → releases the
// watermark token). Called by the driver between retries and on a body error.
func (tx *Txn) Abort()
```

### 1.4 Pre-image / OldIndex derivation

A `Put` that updates an existing row, and every `Delete`, must record the index
coordinates the row **vacated** (`OldIndex`) so the validator can catch a
phantom-disappears (§3, phase1-engine-design.md §4.2). The txn derives them from the
**pre-image at readTs**, which it can always read from its own pinned snapshot:

- On the first `Put`/`Delete` of a `userKey`, read the pre-image: `pre, ok :=
  tx.reader.Get(userKey)`. If `ok`, `oldIndex = tx.indexer(userKey, pre)`; else nil
  (insert, nothing vacated). **This pre-image read is also recorded as a point read** —
  an update/delete depends on the row's prior state, so it must conflict with a
  concurrent change to it (this is the lost-update guard, §7).
- The pre-image is snapshot-cheap (no committer coordination), and cached in the
  `bufferedWrite` so repeated writes to one key read it once.

---

## 2. The read-set — point AND index-range

The read-set is what upgrades snapshot isolation to serializable. It has two halves.

### 2.1 Point reads

```go
type pointRead struct {
    versionSeen HLC  // reader.Get's returned commitTs; HLC{} if the key read ABSENT
    present     bool // false ⇒ the txn's logic depended on this key being ABSENT
}
// Stored in tx.points keyed by string(userKey).
```

**Validation semantics (§4).** A point read of `K` conflicts iff **any** committed
`KeyChange` in `(readTs, commitTs]` has `Pk == K`. The window already excludes
everything `≤ readTs`, so *any* change to `K` in the window is a dependency violation —
`versionSeen` is recorded (matches `reader.Get`'s third return, `engine.go:67`) but the
**window membership is authoritative**; `versionSeen` is a defensive tightening + a debug
aid, not load-bearing. The `present=false` case is the point-phantom (a concurrent
INSERT of a key the txn observed absent) — caught identically because that INSERT emits a
`KeyChange{Pk: K, Op: OpPut}`.

This subsumes the old unique-constraint TOCTOU stripe-lock dance (Decision 4,
`docs/bluedb/schema-enforcement-design.md:80-94`): a unique-index existence check is a
**point read of the unique-index entry key** → a concurrent insert at that value emits a
`KeyChange` on that point key → conflict. No lock manager.

### 2.2 Index-range reads (the SSI crux)

```go
type indexRange struct {
    index  IndexID // which secondary index the scan traversed
    lo, hi []byte  // the order-preserving index-entry byte range actually scanned
                   // [lo, hi) half-open — MUST match the Cursor's bounds exactly
}
// Appended to tx.ranges on every Scan.
```

A `Scan(J, lo, hi)` records `indexRange{J, lo, hi}` — the *interval traversed*, not the
keys that happened to be present. **This is the difference between SI and serializable**:
a `WHERE status='open'` scan that finds zero rows still records
`status_idx[u|open, u|open+1)`, so a concurrent `INSERT ... status='open'` (whose
`NewIndex` coord lands in that interval) is caught even though a key-only read-set would
be empty (Decision 4, headline #1).

**Validation semantics (§4).** For a **range-optimized** index (int/text/bool + composite/
descending — the types with a proven order-preserving `encodeIndexKey`, see the encoding
contract below), an `indexRange{J, lo, hi}` conflicts iff **any** committed
`KeyChange` in `(readTs, commitTs]` has an entry in `NewIndex` **or** `OldIndex` with
`.Index == J` and `lo ≤ .Key < hi` (byte compare). For a scan over an **unsupported**
colType (real/money/blob) or an **IS-NULL** predicate, the byte-range test is not sound, so
the read is validated by the conservative fallback witness instead (encoding contract
below). `NewIndex`-in-range = a phantom
**appears** (insert/update-into-range); `OldIndex`-in-range = a phantom **disappears**
(delete/update-out-of-range). Both are required (phase1-engine-design.md §4.2).

**Encoding contract — ONE canonical encoder (R-2.1 / R-C3, RESOLVED).** The entire SSI
upgrade rests on `indexRange.lo/hi` (built by a `Scan`) and `IndexCoord.Key` (emitted by a
`Put`/`Delete`) living in **one** order-preserving coordinate space. If two encoders in two
layers (scan bounds live at query/Persist; coords at `tx.indexer`) drift, the byte-range
test **silently under-rejects** → phantom commit → the SERIALIZABLE claim is false. The
grill found three concrete drift vectors: real/money/blob have no order-preserving encoding
(the retired `bluedb_index_kernel.go` REFUSES them), descending columns have zero support
(they need a bitwise invert AND a lo/hi swap), and IS-NULL predicates have no coordinate
witness at all.

**The fix is exactly ONE Go function** —

```go
// encodeIndexKey is the SOLE producer of index-coordinate bytes. BOTH the scan-bound
// construction (Txn.Scan's lo/hi) AND the coord emission (tx.indexer → IndexCoord.Key)
// call it. There is no second encoder anywhere; drift is structurally impossible.
func encodeIndexKey(indexID IndexID, colType ColType, value []byte) []byte
```

covering the colTypes with a **proven** order-preserving encoding:

- **int** — sign-biased big-endian 8-byte (flip the sign bit so negatives sort below
  positives, then compare as unsigned bytes).
- **text** — raw UTF-8 bytes (lexicographic == byte order).
- **bool** — a single byte (`0x00` / `0x01`).
- **composite index** — the concatenation of the per-column encodings, in declared column
  order (so lexicographic byte order == tuple order).
- **descending column** — the encoded bytes are **bitwise-inverted**. Descending is ONE
  encoder's responsibility across THREE coordinated facts that must never drift apart:
  (a) coord emission inverts the column's bytes; (b) the scan inverts its bound bytes for
  that column; and (c) **the scan SWAPS `lo`/`hi`** (a descending range's user-facing
  `[hi..lo]` becomes `[invert(lo), invert(hi))` in encoded space). Because all three live
  inside `encodeIndexKey` + the single `Scan` bound-builder that calls it, they cannot be
  applied on one side only.

**Conservative fail-safe fallback (real / money / blob / IS-NULL).** These have **no**
proven order-preserving encoding, so they do NOT get index-range validation. They fall
back to a **broader witness that guarantees no under-reject** (it fails safe toward
over-reject/abort, never toward committing a phantom):

- the actual rows the read returned are recorded as **point reads** (§2.1) — so a change
  to any row the txn actually saw always conflicts; **plus**
- a coarse **collection/index-level conflict witness**: the read-set records a
  `collWitness{coll}` (or, for an unsupported-colType index scan, `indexWitness{index}`)
  that conflicts if **ANY** committed `KeyChange` in `(readTs, commitTs]` touches that
  collection (or that index). A `KeyChange` already carries `Coll` (§3.2) and its
  `NewIndex`/`OldIndex` carry `Index`, so the witness is a membership test over the same
  window — no new changelog field.

This **over-rejects** (more spurious aborts on those predicates → more retries) but is
**always correct**: any phantom that a range test would have caught touches the same
collection/index, so the coarse witness catches it too.

**The guarantee, stated precisely.** SERIALIZABLE is guaranteed for **all**
colTypes/predicates. *Range-optimized* validation (the tight byte-range test) is used for
int / text / bool (+ composite / descending); real / money / blob / IS-NULL use the
conservative fallback (correct, coarser, more aborts). A property test (§7.7) asserts that
for every supported type `encodeIndexKey` produces scan-bound bytes and coord bytes that
byte-match — plus descending invert+swap and composite ordering — so the one-encoder
guarantee is machine-checked. **This closes R-C3, the open item handed forward from
phase1-engine-design.md §9.**

### 2.3 Why not record scanned *keys* instead of the range

A key-set records what *was* there; a phantom is what *wasn't*. Two txns scanning an
empty predicate both have an empty key-set → no overlap → SI accepts → invariant broken.
The range records the *absence witness*. This is the whole reason the changelog carries
`NewIndex`/`OldIndex` coordinates and not just `Pk` (phase1-engine-design.md §4.2).

---

## 3. The `KeyChange`/`IndexCoord` changelog encoding (L2 owns the opaque payload)

Phase 1 stores `CommitReq.ChangelogPayload []byte` **verbatim** at `0x01‖commitTs`
(`committer.go:104-107`) and returns it unparsed from `Changelog.Tail`
(`changelog.go:40-44`) — opaque to L1 (`engine.go:93-97`). L2 defines what the bytes
mean.

### 3.1 Layering resolution (grill 1b, reconciled)

The parent docs call the payload "L2-owned / opaque to L1." Phase-2-embedded (L2) is
**realized as more Go code in the same `runtime-go/bluedb/` package** — there is no
separate Go package boundary between "L1" and "L2-embedded." So the `KeyChange` codec
lives in `bluedb` (new file `keychange.go`) alongside the committer, and the committer
decodes tail entries to validate. **The "opaque" property is preserved where it
matters:** the *storage* interface (`CommitReq.ChangelogPayload []byte` +
`Changelog.Tail() []byte`) never interprets the bytes, so the "swap L1 for a
SQLite/Postgres storage adapter" claim (phase1-engine-design.md §4 line 700) holds — a
storage adapter stores/returns bytes and never calls the validator. Validation is an
**embedded-only** concern (SQL backends use `BEGIN/COMMIT`, Phase 3), so it is correct
that only the embedded committer understands the encoding. **This is a grill target
(§9 R-2.2): does colocating the codec with the committer leak L1/L2? Argument: no — the
storage seam is bytes; the codec is embedded-L2's, colocated for the serialization
point.**

### 3.2 The types + codec

```go
// keychange.go — L2 encoding of one committed transaction's row-level changes.
type CollID  uint32   // stable per-collection id (assigned by the L0/L3 registry, Phase 3)
type IndexID uint32   // stable per-(collection,index) id

type KeyChange struct {
    Coll     CollID
    Pk       []byte       // the user-key (== VersionedWrite.UserKey) — point-read validation
    Op       Op           // OpPut | OpDelete
    Record   []byte       // put: row bytes (for L4 cond-membership + record fan-out); delete: nil
    NewIndex []IndexCoord // positions the row NOW occupies (put); nil for delete
    OldIndex []IndexCoord // positions the row VACATED (update/delete); nil for insert
}
type IndexCoord struct {
    Index IndexID
    Key   []byte // order-preserving index-entry key — produced by encodeIndexKey (§2.2),
                 // the SAME single encoder Txn.Scan's lo/hi bounds go through
}
// ColType tags a column's value domain so the ONE encoder (encodeIndexKey, §2.2) can pick
// its order-preserving encoding. int/text/bool (+ composite/descending) are range-optimized;
// real/money/blob have no order-preserving encoding → the conservative fallback witness (§2.2).
type ColType uint8

// EncodeChangelogPayload serializes one transaction's KeyChange list → the opaque bytes
// L2 puts in CommitReq.ChangelogPayload. Length-prefixed, deterministic, versioned by a
// 1-byte format tag (payloadFmtV1) so the on-disk shape can evolve without a store rewrite
// (unlike keys.go, the payload is NOT comparer-frozen — only its commitTs KEY is).
func EncodeChangelogPayload(changes []KeyChange) []byte

// DecodeChangelogPayload is the inverse — used by (a) the committer's cold-start ring
// rebuild and (b) L4 reactivity fan-out (Phase 4). Returns the KeyChange list.
func DecodeChangelogPayload(payload []byte) ([]KeyChange, error)
```

### 3.3 Who fills each field

- `Coll`, `Pk`, `Op`, `Record` — from the `bufferedWrite` (the write-set) at Commit.
- `NewIndex` — `tx.indexer(userKey, value)` for a put; nil for a delete. Each coord's
  `Key` is produced by the **single** `encodeIndexKey` (§2.2) — the very function
  `Txn.Scan`'s `lo`/`hi` bounds go through, so a scan bound and a change coord can never
  drift (R-2.1).
- `OldIndex` — from the pre-image derivation (§1.4), through the same `encodeIndexKey`.
- The payload is built **once per transaction** at `txn.Commit()`, then handed to the
  committer inside `CommitReq.ChangelogPayload`. The committer stores it verbatim
  (`committer.go:104-107`) — **no change to Phase-1 storage code**.

The `Record` field is carried inline (matches the retired `ChangeEvent.Value`,
`changefeed.go:14`) so Phase-4 L4 reactivity is self-contained; validation ignores it.
The fetch-on-demand alternative (phase1-engine-design.md §4.4) is a Phase-4 tuning knob,
not a Phase-2 decision.

### 3.4 The begin-snapshot path (readTs = `durableHi`, atomically pinned) — R-2.8

`Txn.Begin()` does **not** reuse the plain `Engine.Snapshot()` (`pebble_engine.go:179-194`),
because that path draws `readTs` from the **assigned** high-water: `Snapshot()` → `reg.Register()`
→ `w.highWater()` reads `hlc.last`, and the committer raises `hlc.last` in `hlc.next()`
(`committer.go:89`, `hlc.go:68-81`) at commitTs-**assignment** time — **before** the
batch's `Apply` (`committer.go:132`). So a commit stamped exactly at that high-water can be
invisible to BOTH the Pebble snapshot (pinned before its `Apply`) AND the half-open
`(readTs, commitTs]` window (which excludes `readTs`). That is a serializability hole — the
grill exhibited the interleave.

**The begin-snapshot path closes it** by sourcing `readTs` from the **durably-applied
high-water** and pinning the snapshot atomically with it, in ONE critical section:

```text
beginSnapshot():                          # a new pebbleEngine method backing Begin()
  # ── one critical section: read durableHi, pin snapshot, register — no interleave ──
  readTs := e.durableHi()                 # pebble_engine.go:82 — advanced only AFTER Apply(Sync)
  snap   := e.db.NewSnapshot()            # ordered AFTER the durableHi read → snap ⊇ every commit ≤ readTs
  tok, _ := e.reg.RegisterAt(readTs)      # floors GC's T at readTs (retention invariant, §0);
                                          # RegisterAt still rejects readTs < T (watermark.go:39-42)
  return &pebbleReader{snap, readTs, tok, e.reg}
```

- **Why `durableHi` is the correct edge.** `durableHi` is advanced by the committer
  **after** `Apply(Sync)` returns (`pebble_engine.go:90`, `committer.go:149`), so every
  commit `≤ durableHi` has already been made visible to new Pebble snapshots. Reading
  `durableHi` **before** `NewSnapshot()` guarantees the pinned snapshot reflects **all**
  commits `≤ readTs`. And every commit `> readTs` up to this txn's `commitTs` is, by
  construction, in the ring window `ring.after(readTs)` (§4.2). **No boundary blind-spot**:
  the `≤ readTs` half is the snapshot, the `> readTs` half is the window, and the split is
  exactly at a *durable* timestamp, not an *assigned-but-unapplied* one.
- **Atomic pin.** The durableHi-read → NewSnapshot → RegisterAt ordering is what's
  load-bearing; taking it under the engine's `durMu` (the same lock `advanceDurableHi`
  holds, `pebble_engine.go:90-96`) makes it atomic by construction — no `advanceDurableHi`
  interleaves between the read and the pin, so the snapshot can never lag the `readTs` it
  is paired with. `durMu` is not nested under `w.mu`, so this adds no lock-ordering hazard
  (the same non-nesting the Fix-3 clamp relies on, `watermark.go:111-119`).
- **Composes with the watermark.** `RegisterAt(readTs)` is `Register` with an explicit
  readTs (`watermark.go:35-47`), still enforcing `readTs ≥ T` (unreachable rejection: the
  Fix-3 clamp keeps `T ≤ durableHi = readTs`, so it never trips). The registered token
  floors GC's `T` at `readTs` → the ring always covers `(readTs, high-water]` (§0, §4.2).

`Abort`/`Commit` `Close()` the reader → `Release` the token (unchanged).

---

## 4. Commit-time validation — the algorithm, where it runs, and group-commit composition

### 4.1 Where it runs — inside the committer, the serialization point

Validation runs in `pebbleEngine.process(batch)` (`committer.go:48`), the **single writer
goroutine** — the one place with a total order over commits (contract C1,
phase1-engine-design.md §8.2). Running it anywhere else races: two txns could both
validate-clean against the same snapshot and both apply. Because the committer assigns
`commitTs` and applies in one serialized loop, validating there **is** the serialization
point — no lock manager, no 2PC (Decision 4).

### 4.2 The recent-changes ring (validation is off the Pebble hot path)

The naive design calls `Changelog.Tail(readTs)` (`changelog.go:17`) — a Pebble iterator —
**per transactional job in the committer**. That would throttle the hot path (a Pebble
NewIter + scan on the fsync-critical goroutine). Instead the committer owns an **in-RAM
ordered ring of recent `KeyChange`s**:

```go
// recent_changes.go — an in-RAM, commitTs-ordered window of recent KeyChanges. It is
// mutated ONLY by the committer goroutine (append AND trim, see below), so its own reads/
// appends need no lock (a small RWMutex is added only if Phase-4 fan-out reads it
// off-goroutine).
type recentRing struct {
    entries []ringEntry // ascending commitTs; floor tracks the GC threshold T
    floor   HLC         // the lowest commitTs still held; a readTs below it → spill (Fix-8)
}
type ringEntry struct { commitTs HLC; changes []KeyChange }

// after returns every KeyChange with commitTs in (readTs, +inf) held in the ring.
// O(commits-since-readTs) — a bounded tail slice, no Pebble I/O. If readTs < r.floor
// (the ring was capped/spilled, Fix-8), it returns (nil, spilled=true) so the caller
// falls back to Changelog.Tail(readTs) for that one txn.
func (r *recentRing) after(readTs HLC) (changes []KeyChange, spilled bool)
// append adds a just-durable commit (post Apply(Sync) success). Committer goroutine only.
func (r *recentRing) append(commitTs HLC, changes []KeyChange)
// trim drops entries with commitTs < T. Committer goroutine only — GC does NOT call it
// directly (that would race append). See the trim-marshalling note below (Fix-3).
func (r *recentRing) trim(T HLC)
```

**Ring mutation is single-goroutine — `trim` is marshalled onto the committer (Fix-3).**
The naive design had GC call `recent.trim(T)` from `advanceThreshold` (`watermark.go:110`),
which runs on the **GC caller's goroutine** — concurrent with the committer's `append`.
That is a genuine data race: a torn `after()` read against a concurrent slice mutation →
a truncated window → a silent **wrong-ACCEPT** (a phantom commits). "Single-writer, no
lock" is **false** once GC also writes the ring. The fix keeps the ring single-goroutine:
GC does **not** touch the ring; instead `advanceThreshold` **enqueues a trim request**
(the new `T`) on a small coalescing channel, and the committer **drains it at the top of
each drain** (before `process`), applying `recent.trim(T)` on its own goroutine. So
`append`, `after`, and `trim` are all committer-goroutine-only → no cross-goroutine ring
mutation, no lock on the fsync hot path. `go test ./bluedb/ -race` gates it (a `-race`
build MUST be clean).

**The retention invariant (the load-bearing composition with C6, §0).** `Txn.Begin()`
takes its snapshot via the begin-snapshot path (§3.4), which registers `readTs = durableHi`
in the watermark (`watermark.go:35`). GC's threshold `T` = min over live tokens
(`watermark.go:87-99`), so **`T ≤ readTs` for every open transaction**. The ring is
floored at `T` (its `trim(T)` is marshalled onto the committer, above). Therefore the ring
**normally covers `(readTs, high-water]` for every live transaction** — validation needs no
Pebble read in steady state.

**Ring bound + spill (Fix-8, R-2.4).** The retention invariant floors the ring at `T`, and
`T` is held down by the **oldest live reader token**. A leaked / never-`Release`d reader
token (the Phase-1 R-2 liveness hole) therefore pins `T` low and would grow the ring
**unbounded in RAM** — strictly worse than the Phase-1 on-disk version-retention bloat. The
Phase-2 guard is a **hard ring-size cap** (`maxRingEntries`, a config bound): when appending
would exceed it, the ring **spills its oldest entries and raises `r.floor`** above `T`. A txn
whose `readTs < r.floor` (spilled out from under it) then takes the `after()` `spilled=true`
branch → validation falls back to `Changelog.Tail(readTs)` (a Pebble scan — correct, just
off the in-RAM fast path) for that one txn. So the ring's RAM is bounded **unconditionally**,
independent of reader liveness, and correctness is preserved via the durable changelog. The
`Changelog.Tail` path is thus used for (a) **cold-start rebuild** on `Open` (seed from
`Tail(T)`) and (b) the **spill fallback** (a real, reachable slow path now — not the
`panic`-class assertion the naive design assumed). A **reader-token max-age / heartbeat**
(the shared Phase-1 R-2 fix) remains the complementary liveness cure that keeps a healthy
system off the spill path entirely; the cap is the hard RAM backstop that does not depend on
it.

### 4.3 The per-job validation, composed with group commit

`process(batch)` drains up to `maxBatch` jobs (`committer.go:19-34`) and today assigns a
distinct `commitTs` per job, applies all in one Pebble batch, one `Apply(Sync)`
(`committer.go:83-156`). Phase 2 restructures the per-job loop to **validate-then-assign**,
with an intra-batch `pending` accumulator so a job validates against **earlier jobs in the
same drain window** (which have lower commitTs = are "committed before" it in the serial
order, but are not yet in the ring — they're not durable until this batch's `Apply`):

```text
process(batch):
  drainTrimRequests()               # Fix-3: apply any GC-enqueued recent.trim(T) HERE,
                                    #        on the committer goroutine, before touching the ring

  # ── Fix-6: all-blind batch pays ZERO SSI cost. Pre-scan; if NO job carries a ReadSet,
  #          take the pure Phase-1 path verbatim — no pending decode, no ring bookkeeping. ──
  anyTxn := false
  for j in batch: if j.req.ReadSet != nil: anyTxn = true; break
  if !anyTxn:
      return processBlindPhase1(batch)   # the UNCHANGED committer.go:80-156 body (blind commit)

  pending := []KeyChange{}          # decoded changes of clean jobs SO FAR this batch
  applied := []appliedJob{}         # (job, commitTs, changes) to ring-append post-Apply
  acked   := map[*commitJob]bool{}  # Fix-7: jobs already acked inline (aborts) — excluded from seal loop
  for j in batch:
      if j.req.ReadSet == nil:                       # ── BLIND-WRITE FAST PATH (§5.4) ──
          commitTs := hlc.next()
          writeData(j, commitTs); writeChangelog(j, commitTs)
          if len(j.req.ChangelogPayload) > 0: pending += decode(j.req.ChangelogPayload)
          applied += {j, commitTs, decode(j.req.ChangelogPayload)}
          continue
      # ── TRANSACTIONAL JOB: validate against (readTs, now] = ring.after(readTs) ++ pending ──
      base, spilled := ring.after(j.req.ReadTs)      # in-RAM, O(commits since readTs)
      if spilled: base = changelogTail(j.req.ReadTs) # Fix-8: readTs fell below the ring floor → Pebble scan
      window := append(base, pending...)             # same-batch earlier jobs
      if conflict, culprit := validate(j.req.ReadSet, window); conflict:
          j.done <- CommitResult{Err: ErrConflict}   # abort THIS job; assign NO commitTs
          acked[j] = true                            # Fix-7: record the inline ack
          hotkeys.recordAbort(culprit)               # §6 hot-key detection
          continue
      commitTs := hlc.next()                         # validate-then-assign (no burned ts)
      writeData(j, commitTs); writeChangelog(j, commitTs)
      chg := decode(j.req.ChangelogPayload)
      pending = append(pending, chg...)
      applied += {j, commitTs, chg}
  # metadata (hlc_hi + changelog_cursor) at the HIGHEST APPLIED commitTs; skip if none applied
  if len(applied) > 0: writeMetadata(maxAppliedCommitTs)
  err := db.Apply(batch, pebble.Sync)                # ONE fsync amortized over the group
  if err != nil:
      e.sealed.Store(true)                           # Fix-5: SEAL on durability fault (Phase-1 fail-loud, committer.go:141-142)
  else:
      advanceDurableHi(maxAppliedCommitTs)
      for a in applied: ring.append(a.commitTs, a.changes)   # ring commit AFTER durability
  for a in applied: a.job.done <- CommitResult{CommitTs: a.commitTs, Err: err}   # ack after Apply
  # (the recover defer, committer.go:67-78, must ALSO skip any j in `acked` — Fix-7)
```

Key properties, each a change to `process()` (`committer.go:48-160`):

- **All-blind batch pays zero (Fix-6).** The pseudocode originally decoded
  `ChangelogPayload` and ran `pending` bookkeeping on the fsync goroutine for **every** blind
  write — "zero added work" was false. The pre-scan routes a batch with **no** `ReadSet` to
  the pure Phase-1 `processBlindPhase1` (the unchanged `committer.go:80-156` body): no decode,
  no `pending`, no ring append driven by validation. The OLTP firehose is byte-for-byte
  Phase 1 again. (A *mixed* batch still decodes blind jobs' payloads into `pending`, because a
  later transactional job in the same batch must validate against them — that cost is borne
  only when the batch actually contains a transaction.)
- **`validate()` is a pure in-RAM function** (`validate.go`): for each point key, membership
  in `window`; for each index range, the byte-range test over `NewIndex`/`OldIndex`
  (§2.1/§2.2), or the collection/index-level witness for a fallback colType (§2.2). Returns
  `(conflict bool, culprit []byte)` — culprit feeds hot-key detection.
- **Intra-batch soundness.** A job validates against `pending` (earlier clean jobs this
  batch). Their commitTs are lower → they precede it in the serial order → correct. They
  are not yet durable, but they share this batch's atomic `Apply` — all-or-nothing (C3), so
  if `Apply` fails the whole batch (pending + ring append) is discarded and every job acks
  errored. The ring is committed **only after `Apply(Sync)` returns nil** — so a failed
  Apply never leaves a phantom ring entry.
- **Aborted jobs consume no commitTs** (validate-then-assign). `commitTs` need only be
  strictly monotonic, not gapless — assigning only on clean keeps the ring/metadata clean.
- **Metadata** (`committer.go:110-115`) moves to "highest **applied** commitTs"; the
  all-abort batch writes no metadata (nothing changed) — the `enforceLogicalBatchInvariant`
  gate (`committer.go:121`, `166-171`) already keys on `hasWrites`, so a no-writes batch is
  a valid no-op.
- **Seal-on-Apply-error is RESTORED (Fix-5).** The original §4.3 pseudocode dropped
  `e.sealed.Store(true)` on the non-panic `Apply` error branch — a regression of the Phase-1
  fail-loud contract (`committer.go:141-142`: a failed `Apply(Sync)` cannot know how much
  reached the WAL, so the engine MUST seal). Restored above on `err != nil`.
- **The seal/recover path (Fix-7).** The Phase-1 durability-panic recover defer
  (`committer.go:66-78`) loops the **entire** batch and re-sends `ErrSealed` to every
  `j.done`. But Phase-2 acks **aborted** jobs **inline** (`j.done <- ErrConflict`) before
  `Apply`. Without a guard, a panic after some inline aborts would **double-send** on those
  channels (a second send blocks/panics or delivers a false second result). The `acked` set
  records every inline-acked job; the recover defer (and the invariant-fail branch,
  `committer.go:121-130`) skip any job already in `acked`, sending to each `j.done` exactly
  once. Validation itself still runs before `Apply`, purely in RAM, so it cannot panic on a
  durability fault.
- **Throughput preserved:** one batch, one `Apply(Sync)`, one fsync amortized over the group
  (`committer.go:132`). Validation adds only in-RAM ring scans for the *transactional* jobs;
  blind writes skip it entirely (and an all-blind batch skips even the pre-scan bookkeeping).

### 4.4 Cost analysis (grill Q: does validation throttle the hot path?)

- **Blind writes (the North-Star firehose):** `ReadSet == nil` → zero added work → identical
  to Phase 1.
- **Transactional jobs:** `O(W)` where `W = commits-since-readTs` in the ring (in-RAM slice
  walk) × `O(|read-set|)`. `W` is bounded by **max reader lag** (the same watermark bound as
  GC). A short txn under a fresh readTs → `W` tiny.
- **Residual:** a long-lagging `readTs` (a slow txn) grows its own window. Bounded by the
  retry timeout + the reader-watermark liveness guard (phase1-engine-design.md §9 R-2), and
  — regardless of reader liveness — the **ring size cap** (Fix-8, §4.2): once a `readTs`
  falls below the ring floor its validation spills to `Changelog.Tail` (slower, still
  correct) and the ring's RAM stays bounded. §9 R-2.4 (RESOLVED) owns this.

---

## 5. Retry, typed Conflict, purity, and the blind-write fast path

### 5.1 The optimistic loop (`Engine.Transact`)

```text
Transact(body):
  for attempt := 0; attempt < maxAttempts; attempt++:
      tx, err := Begin()                 # begin-snapshot: readTs=durableHi, registered → pins T (§3.4)
      if err != nil: return err
      if err := body(tx); err != nil:    # a body (logic) error, NOT a conflict
          tx.Abort(); return err
      err := tx.Commit()
      if err == nil: return nil          # durable
      if !errors.Is(err, ErrConflict): tx.Abort(); return err   # durability error → propagate
      tx.Abort()
      if hotkeys.anyHot(tx.touchedKeys()):   # §6 — a POINT hot key was touched → strict-2PL path
          return transactUnderLeases(body)   # discover full hot-key set, acquire in order, re-run
      backoff(attempt)                   # exponential + jitter (range/predicate contention has NO lease)
  return ErrConflict                     # retry bound exhausted → typed, surfaced by Phase 3
```

- **Bounded:** `maxAttempts` (default 8) + exponential backoff with jitter (dampens the
  two-txns-ping-ponging livelock — Decision 4 / R4).
- **Typed `ErrConflict`** (new sentinel in `engine.go:14-26`): on exhaustion, returned to
  the caller. Phase 3 surfaces it into `update()` as `Result Error a`; **the SSE frame acks
  on the error path** (never hangs — the R4×R1 UI-freeze fix, §6.1). Phase 2 owns the Go
  sentinel + the bound; Phase 3/5 own the frame-ack wiring.

### 5.2 Sentinels (added to `engine.go`)

```go
var (
    // ErrConflict: the read-set failed commit-time validation (a concurrent commit touched
    // a read point key or fell into a scanned index range). Retried by Transact; returned
    // typed after maxAttempts. errors.Is-friendly so Phase 3 can branch on it.
    ErrConflict = errors.New("bluedb: transaction conflict")
)
```

That is the **only** new engine sentinel Phase 2 needs. `ErrSealed`/`ErrClosed`/
`ErrSnapshotTooOld`/`ErrMissingCommitMetadata` (`engine.go:14-26`) are unchanged.

### 5.3 The purity gate

The body must be **pure + re-runnable** (no external effects — the same rule the reactive
fold enforces, `docs/bluedb/reactive-sync-design.md:319`; clean-slate-architecture.md §L2).
Two layers:

- **Structural (Phase 3, primary).** The Sky signature `Persist.transaction : Conn cap ->
  (Tx cap -> Task Error a) -> Task Error a` gives the body a `Tx` handle whose only verbs are
  `txGet/txScan/txPut/txDelete` — it **cannot emit `Cmd`s** (no `Cmd` in scope inside the
  body; that is a compile-time fact, the reactive-fold precedent). So a Sky body is pure by
  construction; a re-run repeats only reads+compute+buffered-writes.
- **Go-mechanism assumption (Phase 2).** The Go `Txn` mechanism **assumes** the `body
  func(*Txn) error` closure is side-effect-free and re-runnable — it re-invokes it on retry.
  The conformance suite supplies pure bodies. The `Txn` verbs themselves are effect-free
  (buffer/record only until Commit), so a body built *only* from them is automatically
  re-runnable. **Grill target (§9 R-2.3):** the Go API cannot *enforce* purity of an
  arbitrary Go closure — it is an assumption discharged by Phase 3's Sky type. Documented as
  a boundary contract, not a Go-level guarantee.

### 5.4 The blind-write fast path (one append, unchanged from Phase 1)

A single-key `Persist.put`/`insert`/`delete` with **no prior read** is its own transaction
with an **empty read-set**. Phase 3 emits it as `Engine.Commit(CommitReq{Writes: [...],
ChangelogPayload: encode([oneKeyChange]), ReadTs: HLC{}, ReadSet: nil})`. Because `ReadSet
== nil`, `process()` takes the fast-path branch (§4.3) — **no validation, no retry, no ring
scan** — exactly the Phase-1 blind commit (`committer.go:92-107`). When the whole drain
window is blind (no job carries a `ReadSet`), the §4.3 pre-scan routes it to
`processBlindPhase1` — the unchanged Phase-1 body, with **zero** SSI bookkeeping (no
`pending` decode, no ring append) (Fix-6, T24). It is one `b.Set` for the
data version + (optionally) one for the changelog, folded into the group `Apply`. **The OLTP
hot path is unaffected by SSI** (Decision 4, fast path). Append-only / CRDT-style shared work
modeled as blind unique-key inserts has an empty read-set → never conflicts (R4).

Conformance asserts this: a blind put produces **exactly one** data-version Set and drives
**zero** validation calls (a test seam counts `validate()` invocations).

---

## 6. Hot-key pessimistic fallback (committer-arbitrated FIFO lease)

### 6.1 The problem

Under a genuinely contended read-modify-write key (a shared counter that isn't sharded, a
single hot row), optimistic retry can **starve** an individual transaction — each retry
re-reads, re-computes, and loses the validation race to a faster writer. Combined with
persist-before-ack (R1), the starved user's frame never acks → the UI freezes (R4×R1). The
fix is a **committer-arbitrated FIFO lease** for detected hot keys — a natural extension of
the single-committer floor, **not** a general lock manager (Decision 4 / §6.1 / R4).

### 6.2 Detection

```go
// hotkey.go — owned by the committer (single-writer → no lock for its own updates).
type hotKeyTable struct {
    aborts map[string]int   // culprit userKey → recent abort count (decays over time/commits)
    hot    map[string]*lease
}
// recordAbort is called in process() when validate() reports a culprit (§4.3). When a
// key's abort count crosses hotThreshold within a window, it is promoted to hot and a
// FIFO lease is created. Counts decay so a key that cools is retired (hot map entry removed).
// Only POINT-key culprits are recorded; a range/predicate conflict has no single key to
// lease (§6.4), so it stays on the optimistic-retry path.
func (h *hotKeyTable) recordAbort(culprit []byte)
func (h *hotKeyTable) isHot(userKey []byte) bool
// anyHot reports whether ANY of the txn's touched point keys is currently hot — the driver's
// signal to switch from optimistic retry to the strict-2PL lease path (§6.3).
func (h *hotKeyTable) anyHot(touched [][]byte) bool
// hotSubset returns the touched keys that are currently hot, in ascending bytes.Compare
// order — the canonical acquisition order (§6.4).
func (h *hotKeyTable) hotSubset(touched [][]byte) [][]byte
```

Detection is **cheap and local** to the committer (the one place aborts are observed). No
global scan. The `Txn` records the point keys it touched (`tx.touchedKeys()` — the union of
its write-set keys and its point-read keys) so the driver can ask `anyHot`/`hotSubset`
without the committer.

### 6.3 The lease protocol — strict-2PL discovery (reworked)

The grill found a concrete multi-key deadlock in the naive single-culprit design: it
acquired one culprit key on *mid-body* discovery, so a txn touching hot keys `X` and `Y`
could acquire `X` then discover and acquire `Y`, while a peer acquires `Y` then `X` →
canonical-order acquisition is **impossible** once a lease is taken mid-body → cycle. The
rework makes lease acquisition **strict two-phase**: discover the FULL hot-key set *before*
holding any lease, acquire **all** of them in canonical order, then run under the held set.

```text
transactUnderLeases(body):
  # ── Phase A: DISCOVER the full hot-key set (holding NO lease) ──
  tx0 := Begin()
  _ = body(tx0)                          # run once purely to observe which keys it touches
  hot := hotkeys.hotSubset(tx0.touchedKeys())   # ascending bytes.Compare order (§6.4)
  tx0.Abort()                            # discard — this run committed nothing
  if len(hot) == 0: return Transact(body)       # nothing hot after all → back to optimistic

  # ── Phase B: ACQUIRE all leases in canonical order (strict-2PL: acquire-all-then-run) ──
  tickets := []ticket{}
  for K in hot:                          # hot is sorted → global lock order → no cycle
      t := engine.acquireLease(K); <-t.granted
      tickets = append(tickets, t)
  defer releaseAll(tickets)              # driver releases (see below); committer timeout is the backstop

  # ── Phase C: run ONE attempt as the sole writer of every hot key it touches ──
  tx := Begin()                          # snapshot AFTER the prior holders' durable commits
  if err := body(tx); err != nil: tx.Abort(); return err
  return tx.Commit()                     # cannot conflict on any HELD hot key; a range/other-key
                                         # conflict is still possible → returns ErrConflict, caller retries
```

- **A lease-holder is the sole active writer of every hot key it holds** → its commit
  cannot lose the validation race **on those point keys**. It may still conflict on a
  **range/predicate** or a non-hot key → `ErrConflict` → the outer `Transact` retries. That
  is honest: the lease bounds **point-key** contention, not predicate contention (§6.4).
- **Purity makes the discovery run safe.** Phase A runs `body` only to observe touched keys,
  then `Abort`s — it commits nothing and has no external effect (§5.3 purity). A body whose
  touched-key set depends on data it reads could touch a *different* set on the Phase-C run;
  if the Phase-C run touches a hot key NOT in the held set, it aborts and the driver
  re-discovers (bounded by `maxAttempts`) — correctness is preserved (an un-held hot key just
  means an optimistic attempt), only the starvation-freedom guarantee is best-effort for such
  data-dependent bodies.
- **Lease release — the driver releases (contradiction resolved).** Earlier text had both
  the committer and the driver releasing; **the driver owns release** via
  `defer releaseAll` (the body executes in the driver; the committer can't invoke a Sky
  closure). Because release is driver-side, the **committer's lease-holder timeout is
  load-bearing**, not decorative: a driver that crashes between `Commit` returning and its
  `defer` firing would otherwise wedge the queue forever — the committer expires a lease whose
  holder hasn't made progress within the timeout so the next ticket proceeds.
- **Grant sequences with durability:** ticket N+1 is granted only after ticket N's commit is
  durable (`Apply(Sync)` returned) — so N+1's fresh snapshot (readTs=durableHi, §3.4) sees
  N's write. The committer, which acks after `Apply` (`committer.go:152-156`), releases the
  next ticket at that same point.
- **Auto-retirement:** when a hot key's abort count decays below the threshold and its lease
  queue drains, the `hot` entry is removed — the key returns to the optimistic fast path. The
  lease exists **only** for the contended window.

### 6.4 Deadlock + starvation analysis (grill target, §9 R-2.5)

- **Deadlock-free by strict-2PL + total order.** Every txn acquires ALL its hot-key leases
  up front (Phase B), in ascending `bytes.Compare` order — a single global lock order over a
  total order on bytes. No lease is ever acquired mid-body while another is held (that was the
  naive design's flaw), so no two txns can hold-and-wait in opposite order → no cycle →
  deadlock-free. `bytes.Compare` is a total order → the canonical order is always definable.
- **Starvation-freedom covers POINT-key contention only (stated honestly).** Each hot key is
  a FIFO queue → a ticket waits at most the queue-ahead count of durable commits, never
  skipped. This makes a contended **point** key (shared counter, single hot row)
  starvation-free. **Range/predicate contention has NO lease** — you cannot lease a predicate
  (there is no single key to enqueue on). A workload where two txns perpetually invalidate
  each other's *scan ranges* falls back to bounded optimistic retry → typed `ErrConflict`
  after `maxAttempts` (surfaced to Phase 3). We do NOT claim starvation-freedom for
  predicate contention; the retry bound + backoff jitter is the only mitigation there.
- **Data-dependent touched-set:** a body whose hot-key set depends on read data may touch an
  un-held hot key on the Phase-C run → that attempt aborts and re-discovers (bounded). Correct
  always; starvation-free only for the fixed-touched-set common case.
- **Lease-holder crash/panic:** the driver holds leases via `defer releaseAll`; a panicking
  body unwinds through the defer → leases released. The committer's lease-holder **timeout**
  backstop (above) covers the driver-crash-before-defer case. The timeout is set well above a
  normal commit latency so it does not expire a *legitimately slow* holder and reintroduce the
  race; a holder that is merely slow keeps the lease.

---

## 7. The isolation level — proving SERIALIZABLE, not SI

**Claim:** index-range read-set validation against the single total-order committer yields
**strict serializability on a single node**. A transaction commits only if neither its point
reads NOR its scanned predicate ranges changed in `(readTs, commitTs]`; so the committed
history is equivalent to the serial order = the committer's `commitTs` order (Decision 4).
Because the committer assigns `commitTs` **after** validation and applies in that order,
real-time order is respected → **strict** serializability.

**Two load-bearing preconditions (established by the grill fixes above — the proofs are
valid ONLY with them):**

1. **One encoder (R-2.1, §2.2).** Every range proof below assumes a scan bound
   `indexRange.lo/hi` and a change coord `IndexCoord.Key` are byte-comparable in ONE
   coordinate space. That holds only because both go through the single `encodeIndexKey`.
   For colTypes without a proven order-preserving encoding (real/money/blob) and for IS-NULL
   predicates, the range test is *replaced* by the conservative collection/index-level
   witness (§2.2) — coarser, over-rejecting, but the same "no phantom commits" guarantee.
   So the claim is: SERIALIZABLE for **all** colTypes; range-optimized for int/text/bool
   (+composite/descending), conservative-fallback for the rest.
2. **Durable window boundary (R-2.8, §3.4).** Every proof splits history at `readTs` into
   "snapshot" (`≤ readTs`) and "window" (`(readTs, commitTs]`). That split is only clean
   because `readTs = durableHi` — a *durably-applied* timestamp — so everything `≤ readTs`
   is provably in the pinned snapshot and everything above it up to `commitTs` is in the
   ring window. Drawing `readTs` from the *assigned* high-water (the original design) left a
   commit stamped exactly at `readTs` in neither half → the boundary blind-spot the fix
   closes. The proofs below use `readTs = durableHi` throughout.

Below, each anomaly SI would permit is REJECTED. (SI = snapshot read + write-write
conflict detection only. It rejects lost-update + dirty-write but **not** predicate
phantoms.)

### 7.1 Predicate phantom / predicate write-skew — REJECTED (SI ACCEPTS)

Invariant: "at most one row with `status='open'`." `T1`, `T2` each
`Scan(status_idx, [open, open+1))` at `readTs`, both see zero rows, both `Insert` a row with
`status='open'`.

- **Under SI:** each read-set is key-empty (no `open` rows existed at read time); write-sets
  are disjoint (different pks) → no write-write conflict → **both commit → invariant
  broken.**
- **Under our validation:** `T1` commits first at `c1`; its `KeyChange` has `NewIndex =
  [{status_idx, u|open|pk1}]`. `T2` (commitTs `c2 > c1`) validates its read-set entry
  `indexRange{status_idx, [open, open+1)}` against `window = ring.after(readTs_T2)`, which
  contains `T1`'s change; `u|open|pk1 ∈ [open, open+1)` → **conflict → `T2` aborts →
  retries → now sees `pk1` → its "at most one open" logic prevents the second insert.**
  **REJECTED.** A key-only read-set (SI) is empty for `T2` → this is exactly the case the
  **range** read-set catches and SI cannot.

### 7.2 Phantom-disappears (delete-out-of-range) — REJECTED

Invariant: "if any `open` row exists, keep the banner." `T1` scans `[open, open+1)`, sees one
row, decides to keep the banner. Concurrently `T2` deletes that row (`status` was `open`).

- **Under our validation:** `T2`'s `KeyChange` has `OldIndex = [{status_idx, u|open|pk}]`
  (the position it vacated, §1.4). `T1`'s range validation tests `OldIndex` too → `u|open|pk
  ∈ [open, open+1)` → **conflict → `T1` retries → sees the row gone → drops the banner.**
  **REJECTED.** This is why `OldIndex` is required, not just `NewIndex`
  (phase1-engine-design.md §4.2).

### 7.3 Point write-skew — REJECTED

Invariant: "`x + y ≥ 0`", both currently 100. `T1` reads `x, y`, sets `x = -50`; `T2` reads
`x, y`, sets `y = -60`.

- **Under SI:** disjoint writes → both commit → `x+y = -110 < 0` → broken.
- **Under our validation:** `T1` commits first (`KeyChange{Pk: x}`). `T2`'s point read of `x`
  is in its read-set; `x ∈ window` → **conflict → `T2` retries → reads `x=-50` → its check
  fails → aborts the withdrawal.** **REJECTED.** (Point write-skew is caught by point-read
  validation; the predicate case §7.1 is the one that additionally needs the range.)

### 7.4 Lost update — PREVENTED

`T1`, `T2` both read `counter=5`, both write `6`. Second to commit has `counter` in its
read-set (the pre-image read, §1.4) → `counter ∈ window` → conflict → retry → reads `6` →
writes `7`. **PREVENTED.**

### 7.5 Read-your-writes — HOLDS

Within a txn, `Put(K, v)` then `Get(K)` returns `v` (write-set overlay, §1.3); a buffered
`Delete(K)` then `Get(K)` returns absent. A `Scan` over a range containing buffered writes
reflects them (merge cursor). **HOLDS by construction** (no validation involved — it's the
overlay).

### 7.6 Residual (stated honestly)

- **Read-only transactions** still validate (their read-set is non-empty) — correct for
  serializability, but a pure read-only txn could be given a **validation-free** fast path
  (a consistent snapshot read *is* serializable on its own). Phase-2 keeps validation on for
  uniformity; a read-only-skip is a safe optimization flagged for later (§9 R-2.6).
- **Cross-node** strict serializability is out of scope (embedded, single committer). The
  cluster tier layers HLC + Calvin command ordering (clean-slate-architecture.md Decision 4)
  — designed-for, not built.

### 7.7 The conformance suite (the Phase-2 success gate)

All under `go test ./bluedb/ -race -count=1` (matching phase1-status.md:217-228). A
`validate()`-invocation counter + a controllable committer interleaving seam (drive two
`Txn`s to commit in a chosen order) are test infrastructure.

| # | Test | Asserts |
|---|---|---|
| T1 | `predicate_phantom_rejected` | §7.1 — two empty-scan inserts; second gets `ErrConflict`; after retry, invariant holds. |
| T2 | `phantom_disappears_rejected` | §7.2 — scan-keep vs concurrent delete; scanner conflicts via `OldIndex`. |
| T3 | `point_write_skew_rejected` | §7.3 — `x+y≥0`; second conflicts on the point read. |
| T4 | `lost_update_prevented` | §7.4 — concurrent counter increment; second retries, final = start+2. |
| T5 | `read_your_writes` | §7.5 — buffered put/delete visible to Get + Scan within the txn. |
| T6 | `retry_on_conflict_then_success` | conflict → auto-retry → durable commit within the bound. |
| T7 | `retry_bound_returns_typed_conflict` | forced perpetual conflict → `errors.Is(err, ErrConflict)` after `maxAttempts`. |
| T8 | `blind_write_single_append_no_validation` | §5.4 — `ReadSet==nil` → 0 `validate()` calls, exactly 1 data-version Set. |
| T9 | `hot_key_lease_fifo_no_starvation` | N contenders on one hot POINT key all commit; FIFO order; none starves. |
| T10 | `hot_key_two_keys_no_deadlock` | §6.3 — txns touching two hot keys in both orders → strict-2PL discovery + full-set canonical-order acquisition → no deadlock. |
| T11 | `intra_batch_conflict` | two conflicting txns in ONE drain window → second aborts (validated against `pending`). |
| T12 | `unique_constraint_toctou` | schema-enforcement `:92-94` — concurrent same-value inserts; one wins via unique-index point read. |
| T13 | `unique_is_serial_pk` | serial-pk-as-unique edge (`:92-94`) re-proven under MVCC. |
| T14 | `null_skip_unique` | NULL values skip the unique check (`:92-94`) — no false conflict. |
| T15 | `self_upsert_no_conflict` | a txn updating a row it read does not self-conflict (own write not in window). |
| T16 | `validation_off_pebble_hotpath` | validation for a fresh-readTs txn drives **zero** `Changelog.Tail` (ring-served). |
| T17 | `ring_covers_open_txn_window` | GC cannot advance `T` past an open txn's readTs (watermark invariant, §4.2). |
| T18 | `descending_composite_index_range` | §2.2 — a scan over a descending/composite index range validates correctly against coords. |
| T19 | `index_encoder_scan_coord_bytematch` | **R-2.1 property test** (§2.2) — for every supported colType, `encodeIndexKey` scan-bound bytes and coord bytes byte-match; `encode(lo) ≤ encode(coord) < encode(hi) ⟺ row in range`; descending applies invert **AND** lo/hi swap; composite orders by concatenated fields. ONE encoder, both sides. |
| T20 | `phantom_under_descending_index` | §2.2 — a concurrent insert whose coord lands in a **descending**-index scan range is caught (exercises the invert+swap coordination, not just ascending). |
| T21 | `fallback_coltype_conservative` | §2.2 — a `real`/`money`/`blob` scan (and an IS-NULL predicate) uses the collection/index-level witness: a concurrent change to that collection/index **conflicts** (over-rejects), and a phantom that a range test would catch is **never** under-rejected. Correct + coarser. |
| T22 | `window_boundary_durablehi` | **R-2.8** (§3.4) — a commit stamped at the *assigned* high-water but not yet durable is excluded from `readTs`; `readTs = durableHi`; the interleave that broke serializability under assigned-high-water (commit at exactly `readTs`, invisible to both snapshot and window) is now caught. |
| T23 | `ring_trim_append_race` | **Fix-3** (§4.2) — concurrent GC-driven `trim` + committer `append` under `-race`: trim is marshalled onto the committer, no torn `after()`, `go test -race` clean. |
| T24 | `all_blind_batch_zero_ssi` | **Fix-6** (§4.3) — a drain window with no `ReadSet` takes `processBlindPhase1`: zero `pending` decode, zero validation-driven ring bookkeeping; byte-identical to Phase 1. |
| T25 | `ring_cap_spill_fallback` | **Fix-8** (§4.2) — a leaked reader token pins `T` low, ring exceeds `maxRingEntries` → spill raises `r.floor` → a txn with `readTs < floor` validates via `Changelog.Tail`, still correct; ring RAM stays bounded. |
| T26 | `abort_then_seal_single_ack` | **Fix-7** (§4.3) — an inline-aborted job followed by a durability panic: each `j.done` receives **exactly one** result (the `acked` set excludes inline-acked jobs from the recover loop). |

T1–T8 are the architecture's explicit Phase-2 success criteria (clean-slate §7 Phase 2);
T12–T14 are the schema-enforcement edge cases required re-proven under MVCC
(`docs/bluedb/schema-enforcement-design.md:92-94`, clean-slate §7 Phase 2 success). T19–T26
gate the grill-close fixes (one-encoder R-2.1, durableHi boundary R-2.8, ring-race Fix-3,
blind-batch Fix-6, acked-set Fix-7, ring-cap Fix-8).

---

## 8. Exact Phase-1 touch-points (minimal interface extension, no format change)

**No irreversible-format change. No new `CommitReq` field.** Phase 1 pre-stubbed every
hook. The extension is: fill one struct, add one sentinel, restructure one function, add
new files.

| # | Touch-point | file:line | Change | Why minimal |
|---|---|---|---|---|
| 1 | `CommitReq.ReadTs` + `CommitReq.ReadSet` | `engine.go:99-103` | **USE (no change)** — fields already exist; `nil ReadSet ⇒ skip validation` already documented. | The "does CommitReq need a read-set field?" answer: **no, it's already there.** |
| 2 | `ReadSet` struct | `engine.go:118-122` | **FILL the stub** with `points []pointRead`-equivalent + `ranges []indexRange` (§2). The type stays in package `bluedb` (L2-embedded is same-package Go). | Phase 1 declared it `TODO(phase1b)`; Phase 2 gives it fields. `CommitReq`'s shape is unchanged (it holds `*ReadSet`). |
| 3 | `ErrConflict` sentinel | `engine.go:14-26` | **ADD one** sentinel (§5.2). | The only new engine error. `errors.Is`-friendly. |
| 4 | `Engine` interface | `engine.go:29-63` | **ADD** `Begin() (*Txn, error)` + `Transact(func(*Txn) error) error` (§1.1). Additive; no existing method changes. | Phase 3's entry points. |
| 5 | `process(batch)` | `committer.go:48-160` | **RESTRUCTURE** the per-job loop to validate-then-assign + `pending` accumulator + fast-path branch + all-blind pre-scan (Fix-6) + `acked` set (Fix-7) (§4.3). Metadata → highest **applied** commitTs. **Seal-on-Apply-error (`:141-142`) PRESERVED (Fix-5)**; the recover defer (`:66-78`) additionally skips inline-acked jobs (Fix-7). Drain GC trim-requests at loop top (Fix-3). | The **one real Phase-1 code modification**. The blind-write path (`:92-107`) is preserved verbatim as `processBlindPhase1` (all-blind batch) and as the per-job `ReadSet==nil` branch. |
| 6 | `pebbleEngine` struct | `pebble_engine.go:54-79` | **ADD** fields `recent *recentRing`, `hotkeys *hotKeyTable`, `trimReqs chan HLC` (Fix-3); init in `openWith` (`:105-154`) — seed `recent` from `Changelog().Tail(persistedThreshold)` (cold-start rebuild, §4.2). Add `beginSnapshot` (readTs=durableHi + atomic pin, §3.4), `Begin`/`Transact`/lease methods. `RegisterAt(readTs)` on the watermark registry. | `commitJob` (`:48-51`) unchanged. `durableHi()`/`advanceDurableHi()` (`:82-96`) already exist — reused, not added. |
| 7 | GC ↔ ring | `gc.go:29-131` (`GC`), `watermark.go:110` (`advanceThreshold`) | **ENQUEUE** a trim request (the new `T`) when `advanceThreshold` moves `T`; the **committer** drains it and calls `recent.trim(T)` on its own goroutine (Fix-3 — GC never mutates the ring). One coalescing channel; GC's delete-pass logic unchanged. | Retention invariant (§4.2) rides the existing watermark; ring stays single-writer (committer-only) → `-race` clean. |
| 8 | `Changelog.Tail` | `changelog.go:17` | **USE (no change)** — cold-start ring rebuild + the **ring-cap spill fallback** (Fix-8, a real reachable slow path when `readTs < ring.floor`, §4.2), no longer a `panic`-class assertion. | Already O(commits-since-after). |
| 9 | §8.2 contracts | phase1-engine-design.md §8.2 | **CONSUME, don't weaken:** C1 (total order → validation is the serialization point), C3 (atomic batch → intra-batch soundness), C4 (snapshot register → the retention invariant), C5 (ordered changelog → the ring), C6 (advancing watermark → ring floor == GC floor). **No new frozen L1 contract needed.** | Phase 2 is pure L2 on top of frozen L1. |

**New files (all in `runtime-go/bluedb/`, all pure L2):** `txn.go` (Txn + Begin/Transact
loop), `readset.go` (fill §2 types + `validate`'s inputs), `keychange.go` (the §3 codec),
`validate.go` (`validate(rs, window) (bool, culprit)`), `recent_changes.go` (the ring),
`hotkey.go` (detection + lease). Plus `txn_test.go` / `serializable_test.go` (§7.7).

**What Phase 2 does NOT touch:** `keys.go`, `comparer.go` (frozen format), `hlc.go`,
`reader.go` (snapshot read is complete), the changelog **keyspace** (`changelog.go`'s
`0x01‖commitTs` layout), the GC delete-pass, the seal/durability contract.

---

## 9. Top RISKS / open questions (the grill seed + close status)

Ordered by blast radius. The 2-adversary grill closed the correctness-critical + must-close
items (marked **RESOLVED** with the mechanism); the rest stay as implementation grill
targets.

- **R-2.1 — index-encoding drift between scan bounds and change coordinates (HIGHEST). —
  RESOLVED (one encoder + conservative fallback), CLOSES R-C3.**
  §2.2 correctness rests on `indexRange.lo/hi` (built by a `Scan`) and `IndexCoord.Key`
  (emitted by a `Put`/`Delete`) living in **one order-preserving coordinate space**. The
  grill confirmed the risk was real and worse than stated: real/money/blob have **no**
  order-preserving encoder (the retired `bluedb_index_kernel.go` REFUSES them), descending
  had zero support (needs invert **AND** a lo/hi swap), the two encoders lived in different
  layers (scan=query/Persist, coord=`tx.indexer`) → silent under-reject → phantom commit,
  and IS-NULL predicates had no coordinate witness. **Resolution (§2.2/§3.3):** ONE canonical
  `encodeIndexKey(indexID, colType, value)` called by BOTH sides (int sign-biased BE8 / text
  UTF-8 / bool 1-byte; composite = concatenation; descending = invert bytes + scan swaps
  lo/hi — all three coordinated inside the one encoder). Types with no proven order-preserving
  encoding (real/money/blob) and IS-NULL predicates use a **conservative fail-safe fallback**
  — point read-set of rows actually read **plus** a collection/index-level conflict witness —
  which over-rejects but **never** under-rejects. Guarantee: SERIALIZABLE for **all**
  colTypes/predicates; range-optimized for int/text/bool(+composite/descending), conservative
  fallback for the rest. Property test T19 (+ T20 descending, T21 fallback). **This closes the
  R-C3 open item from phase1-engine-design.md §9.**

- **R-2.2 — does colocating the `KeyChange` codec with the committer leak L1/L2?** §3.1
  argues no (the storage seam is opaque bytes; the codec is embedded-L2's, colocated for the
  serialization point). Grill: does a future SQLite/Postgres **storage** adapter ever need to
  parse the payload? (Answer should be no — SQL backends validate via `BEGIN/COMMIT`, never
  via our changelog.) If any path makes L1 parse it, the "swap the storage adapter" claim
  (phase1-engine-design.md §4 line 700) breaks. Confirm the codec is reachable **only** from
  the embedded committer + Phase-4 fan-out, never from the `Changelog`/`CommitReq` storage
  seam.

- **R-2.3 — the Go mechanism cannot enforce body purity (§5.3).** `Transact` re-runs an
  arbitrary Go closure on retry; a closure with side effects would double them. Purity is
  discharged by **Phase 3's Sky type** (no `Cmd` in the txn body), not by the Go API. Grill:
  is that boundary contract airtight — can any Phase-3 lowering let an effect leak into the
  body? Is there a Go-level guard (e.g. the `Txn` verbs are the *only* effectful surface, so
  a body built solely from them is provably re-runnable) worth adding as defense-in-depth?

- **R-2.4 — validation cost + ring RAM under a lagging/leaked `readTs` (§4.4). — RESOLVED
  (ring-size cap + spill-to-`Changelog.Tail`).** A long/slow txn's window `(readTs,
  high-water]` grows with commit volume (`O(W)` ring walk), and a leaked reader token (the
  Phase-1 R-2 liveness hole) that pins a low `T` would grow the ring **unbounded in RAM** —
  worse than Phase-1's on-disk bloat. **Resolution (Fix-8, §4.2):** a hard `maxRingEntries`
  cap; when a window would exceed it the ring spills its oldest entries and raises
  `r.floor`, and a txn whose `readTs < r.floor` validates via `Changelog.Tail` (correct,
  slower). Ring RAM is bounded **unconditionally**, independent of reader liveness. A
  reader-token max-age / heartbeat (the shared Phase-1 R-2 fix) remains the complementary
  cure that keeps a healthy system off the spill path; the cap is the hard backstop that does
  not depend on it. Gate T25.

- **R-2.5 — hot-key lease deadlock (§6.4). — RESOLVED (strict-2PL full-set discovery).** The
  grill found a concrete `X<Y` deadlock: the naive design acquired a single culprit key on
  **mid-body** discovery, so canonical-order acquisition of a *second* discovered key was
  impossible → cycle. **Resolution (§6.3):** run the body once to discover the FULL hot-key
  set (holding no lease), abort, acquire ALL touched hot-key leases in `bytes.Compare`
  canonical order, then re-run under the held set (strict two-phase → single global lock
  order → no hold-and-wait in opposite order → deadlock-free). **Range/predicate contention
  has no lease** (you cannot lease a predicate) → bounded optimistic retry + typed
  `ErrConflict`; starvation-freedom is claimed **only** for point-key contention (stated
  honestly). Lease release is **driver-side** (`defer releaseAll`), so the committer's
  lease-holder **timeout backstop is load-bearing** (covers a driver that crashes between
  `Commit`-return and the defer); the timeout is set above normal commit latency so it does
  not expire a legitimately slow holder. Gates T9 (FIFO no-starvation), T10 (two-key
  no-deadlock).

- **R-2.6 — read-only-txn validation is conservative but not free (§7.6).** A pure read-only
  txn validates unnecessarily (a consistent snapshot read is already serializable). Safe to
  skip, but the "is it read-only?" check must be exact (no buffered writes AND the caller
  didn't intend a write-on-commit). Deferred as an optimization — grill whether it's worth
  the special case or a footgun.

- **R-2.7 — intra-batch validation vs the group-commit window (§4.3).** The `pending`
  accumulator makes a job validate against earlier same-batch jobs. Grill the edge: does a
  job ever need to validate against a *later* same-batch job? (No — later jobs have higher
  commitTs, they follow it in the serial order; a txn only depends on the past.) And: if the
  batch's `Apply(Sync)` fails, are `pending` + the ring-append + the hot-key abort counts all
  correctly discarded/not-committed? (Design: pending is batch-local; ring-append is
  post-Apply-success only; abort counts are advisory and self-decay — a spurious one is
  harmless.) Confirm no state escapes a failed batch.

- **R-2.8 — validation-window boundary at `readTs` (§2.1/§3.4). — RESOLVED (`readTs =
  durableHi`, atomic snapshot pin).** The original argument assumed a change at exactly
  `readTs` is "either seen by the snapshot or in the window." The grill **broke** that: the
  design drew `readTs` from the **assigned** high-water (`hlc.next()` raises `c.last` BEFORE
  `Apply`, `committer.go:89`/`hlc.go:68`; `Snapshot()`→`Register()`→`highWater()` reads
  `c.last`), so a commit stamped exactly at `readTs` can be invisible to BOTH the Pebble
  snapshot (pinned before that commit's `Apply`) AND the half-open `(readTs, commitTs]`
  window (which excludes `readTs`) → serializability **violated** (a concrete interleave).
  **Resolution (§3.4):** the begin-snapshot path sources `readTs` from the **durably-applied**
  high-water `durableHi` (advanced post-`Apply`, `pebble_engine.go:90`/`committer.go:149`)
  and pins the Pebble snapshot **atomically** with it (durableHi-read → `NewSnapshot` →
  `RegisterAt(readTs)`, ordered under `durMu`). Now every commit `≤ readTs` is provably in
  the snapshot and every commit `> readTs` up to `commitTs` is in the ring window — no
  boundary blind-spot. Composes with `Register` (still rejects `readTs < T`; the Fix-3 clamp
  keeps `T ≤ durableHi = readTs`, so it never trips). `versionSeen` stays advisory (window
  membership is authoritative). Gate T22.

- **R-2.9 — recent-ring `trim`/`append` data race (§4.2). — RESOLVED (trim marshalled onto
  the committer).** `trim` was hooked into `advanceThreshold` (`watermark.go:110`), which runs
  on the **GC caller's** goroutine, concurrent with the committer's `append`/`after` — so the
  "single-writer, no lock" claim was **false** in Phase 2, and a torn `after()` read → a
  truncated window → a silent wrong-ACCEPT (a phantom commits). **Resolution (Fix-3):** GC
  enqueues a trim request (the new `T`) on a coalescing channel; the committer drains it at
  the top of each drain and calls `recent.trim(T)` on its own goroutine, so the ring is
  mutated by exactly one goroutine — no lock on the fsync hot path. `go test ./bluedb/ -race`
  gates it (T23).

---

## Appendix — reuse ledger (what Phase 2 carries vs builds)

| Carries forward | From | As |
|---|---|---|
| Per-job distinct `commitTs` in FIFO order | `committer.go:83-108` | The serialization order validation rides (already Phase-1) |
| `CommitReq.{ReadTs, ReadSet, ChangelogPayload}` hooks | `engine.go:93-103` | USE — the Phase-2 fields are pre-stubbed |
| Opaque changelog storage `0x01‖commitTs` | `committer.go:104-107`, `changelog.go` | USE — L2 payload stored verbatim, no change |
| `Changelog.Tail(after)` O(commits-since) | `changelog.go:17` | Cold-start ring rebuild + ring-cap spill fallback (Fix-8) |
| `durableHi` / `advanceDurableHi` | `pebble_engine.go:82-96`, `committer.go:149` | The begin-snapshot `readTs` source — the R-2.8 window boundary (§3.4) |
| Snapshot register/pin → watermark token | `pebble_engine.go:179-194`, `watermark.go:35` | Begin-snapshot pins at `readTs=durableHi` (§3.4) → the retention invariant (ring floor == GC floor) |
| GC advancing watermark `T` | `gc.go:40`, `watermark.go:110` | ENQUEUE a trim request → committer drains `recent.trim(T)` (Fix-3, single-writer ring); ring retention == version-GC |
| Group-commit one-Apply(Sync) | `committer.go:132` | Preserved — validation is in-RAM, off the fsync path |
| Seal-on-durability-fault + recover | `committer.go:66-78`,`141-142` | Seal PRESERVED on Apply-error (Fix-5); recover defer skips inline-acked jobs (Fix-7); validation runs before Apply, cannot fault |
| Blind-write path (one append) | `committer.go:92-107` | Preserved verbatim — `processBlindPhase1` for an all-blind batch (Fix-6) + the per-job `ReadSet==nil` branch |
| `KeyChange`/`IndexCoord` shape | phase1-engine-design.md §4.2 | Frozen encoding — Phase 2 implements the codec + validator |
| Unique/serial/NULL-skip contract | `bluedb_collection_kernel.go:228`, `schema-enforcement-design.md:70-94` | Re-proven under MVCC as point-read validation (T12–T14) |

**Builds new (all L2, all package `bluedb`):** the `Txn` + optimistic `Transact` loop, the
point+range read-set, the `KeyChange` codec, the in-RAM recent-changes ring, the commit-time
validator, and the committer-arbitrated hot-key FIFO lease.

**The moat this phase delivers:** a portable, **pure**, **serializable** transaction the
type system will guarantee (Phase 3's no-`Cmd` body) and the single committer serializes —
the L2 differentiator no SQL-first DB can copy (clean-slate-architecture.md Decision 4,
`docs/bluedb/strategy.md:61-67`).
