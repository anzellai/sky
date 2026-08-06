# BlueDB Phase 1 — Engine substrate (Pebble + MVCC + single-writer committer)

> **Status:** Phase-1 engine design, `feat/bluedb`. This is the doc the next step
> **grills**, then implements. No production code here — the LOCKED key format, the
> `Engine` interface + contracts, the committer/commit path, the changelog shape, the
> GC mechanism, build integration, the `errorfs` fault harness, success criteria, and
> the Phase-1 risk/open-question seed.
>
> **Parent:** `docs/bluedb/clean-slate-architecture.md` (§3 Decisions 1/3/4, §6, §7
> Phase 1, Grill outcomes). This doc REFINES and, in two places, **CORRECTS** the
> parent against the *verified* Pebble v2.x API (see §0.1).
>
> **Reference checkout:** the prior implementation is at
> `.claude/worktrees/ref-exp-bluedb/`. Every non-`docs/` `file:line` below is relative
> to that worktree.
>
> **Pebble API provenance.** Every Pebble/Cockroach API fact in this doc was verified
> against pkg.go.dev + the `cockroachdb/pebble` and `cockroachdb/cockroach` sources.
> Items that could NOT be confirmed are tagged **[UNCONFIRMED]** inline and collected
> in §9. Do not implement an [UNCONFIRMED] detail without re-checking against the
> pinned Pebble version first.

---

## Grill outcomes (Phase-1 design close)

A 2-adversary grill of this design ran before Phase-1 implementation. The **structure
is SOUND and is NOT redesigned**: embed Pebble; MVCC-timestamp-in-key; single-writer
committer; HLC restart-floor and commit-metadata-in-batch crash-atomicity are
**validated**; the changelog `NewIndex`/`OldIndex` SSI design and the keyspace-tag
scheme stand. The grill surfaced concrete fixes to the *irreversible* on-disk format
and the GC subsystem — all foldable now at **zero data cost** (pre-first-SSTable):

- **Byte layout (§2.2) validated** — the trailing-length-byte `Split` scheme is correct.
- **Comparer (§2.4) — the big fix.** The hand-rolled comparer had format-fatal bugs
  (whole-key `Separator`/`Successor` truncating inside the suffix → negative-index
  `Split`; missing `ImmediateSuccessor`; whole-key `AbbreviatedKey`; undefined suffix
  comparers). Resolution: **MIRROR** Pebble's shipped `cockroachkvs` comparer
  techniques exactly, adapted to our tag scheme + inverted suffix (the adopt-wholesale
  path is prohibited by the validated keyspace-tag scheme — see §2.4).
- **GC (§5) — two silent-corruption holes fixed.** (2a) watermark TOCTOU closed with a
  persisted, monotonic GC threshold + atomic `Snapshot` readTs-pick-and-register; (2b)
  GC deletes are now **physical-only** (no committer, no `commitTs`, no changelog entry,
  no `hlc_hi` bump).
- **Layering fixed (§3.1/§4).** The L1 changelog payload is now an **opaque `[]byte`**;
  L2 owns `KeyChange` encode/decode, so the "swap for SQLite at L1" claim holds.
- **HLC / crash-atomicity validated** — restart floor + single-batch metadata carried
  forward unchanged (§3.3/§3.4), with the logical-counter overflow rule stated (§3.3).
- **`base.CheckComparer` is the irreversible-format insurance** — a hard Phase-1 gate
  (§8.1); a round-trip test alone cannot catch the F2/F3 layout bugs.

Section-level detail is folded inline below; §9 risks reflect the resolutions.

---

## 0. Orientation

### 0.1 Two corrections to the parent architecture doc (READ FIRST)

Phase-0 verification of the real Pebble API forces two corrections. Both are folded
into the sections below; they are called out here so the grill sees them plainly.

1. **Pebble has NO user-defined compaction filter.** The parent doc (Decision 3,
   §6.3, §7 Phase-1) repeatedly says MVCC GC rides "a Pebble **compaction filter**."
   **That API does not exist in Pebble.** Unlike RocksDB, `pebble.Options` exposes no
   `CompactionFilter` hook. The real mechanism (what CockroachDB itself does) is an
   **explicit GC pass that issues point/range `Delete`s** for stale versions, which
   Pebble's background compaction then physically reclaims — optionally accelerated by
   a **`BlockPropertyCollector`** keyed on `commitTs` for read-side block skipping (a
   filter, not a GC). §5 rewrites GC to this real mechanism. **This is the single most
   important Phase-1 correction.**

2. **The key encoding needs a trailing version-length byte.** The parent's layout
   `<user-key> 0x00 <inverted commitTs>` is *under-specified*: with a bare `0x00`
   separator, a user-key that itself contains `0x00` makes `Split` ambiguous (§2.2).
   CockroachDB's proven `EngineKey` solves this with a **trailing length byte** so
   `Split` decodes the prefix boundary arithmetically from the tail, never by scanning
   for `0x00`. We adopt that. §2 is the corrected, LOCKED layout.

Everything else in the parent (embed Pebble; MVCC-timestamp-in-key; single-writer
committer; HLC restart floor; commit-metadata-in-batch; SSI via index-range
validation) is **validated and carried forward unchanged**.

### 0.2 What Phase 1 delivers

The L1 `Engine` over Pebble: the LOCKED `Comparer`, versioned MVCC key encoding,
snapshot reads, a single-writer group-commit committer assigning HLC `commitTs`,
commit-metadata-in-batch (crash-atomic), a changelog indexed by `commitTs` (payload
opaque at L1 — §4), the explicit **physical-only** delete-pass GC keyed on a persisted,
advancing threshold `T` (§5), `flock`, and the `errorfs` fault harness that runs the old
crash corpus as a conformance oracle.

Phase 1 does **not** build the transaction validator (Phase 2), the Persist surface
(Phase 3), or reactivity fan-out (Phase 4). It builds the substrate they stand on and
locks the interface contracts (§8) they depend on.

---

## 1. The verified Pebble surface Phase 1 stands on

Confirmed against Pebble v2.x source/godoc (provenance §0). Signatures we call:

| Concern | Verified API |
|---|---|
| Comparer | `pebble.Comparer` is a **struct** (alias of `internal/base.Comparer`) of func-typed fields: `Compare func(a,b []byte) int`, `Equal func(a,b []byte) bool`, `Split func(a []byte) int`, `AbbreviatedKey func([]byte) uint64`, `Separator`/`Successor func(dst,a,b []byte) []byte`, **`ImmediateSuccessor func(dst,a []byte) []byte`** (REQUIRED — range-key support), `ComparePointSuffixes`/`CompareRangeSuffixes func(a,b []byte) int`, `ValidateKey`, `FormatKey`, and **`Name string`** (a field, not a method). |
| Comparer.Name persistence | The comparer name is written into SSTable metadata; opening a DB with a different comparer than it was created with is an **error**. Immutable for the life of the store. |
| **Pebble ships an MVCC comparer** | `github.com/cockroachdb/pebble/cockroachkvs` exports `cockroachkvs.Comparer` (Cockroach's `EngineComparer`): a public, battle-tested, range-key-ready MVCC comparer that correctly implements every field above (`Split`/`Compare`/`Separator`/`Successor`/`AbbreviatedKey`/`ImmediateSuccessor`/`ComparePointSuffixes`/`CompareRangeSuffixes`) and passes `base.CheckComparer`. §2.4 evaluates adopting it vs mirroring it. |
| Comparer self-check | `base.CheckComparer(cmp *Comparer, prefixes, suffixes [][]byte) error` mechanically verifies the Split/Separator/Successor/suffix invariants over supplied sample keys. The cheapest insurance against an irreversible-format comparer bug (§8.1). |
| Open / batch | `pebble.Open(dir, *Options)`; `(*DB).NewBatch() *Batch`; `(*Batch).Set(key,val,*WriteOptions)`, `.Delete(key,*WriteOptions)`, `.DeleteRange(start,end,*WriteOptions)`; `(*DB).Apply(*Batch,*WriteOptions) error`. |
| Durability | `pebble.Sync = &WriteOptions{Sync:true}` (and `pebble.NoSync`). `db.Apply(batch, pebble.Sync)` fsyncs the WAL before returning. |
| Snapshot | `(*DB).NewSnapshot() *Snapshot`; `(*Snapshot).Get(key) ([]byte, io.Closer, error)`; `(*Snapshot).NewIter(*IterOptions) (*Iterator,error)`; `(*Snapshot).Close() error`. Also `NewEventuallyFileOnlySnapshot([]KeyRange)` (EFOS) — not needed Phase 1. |
| Iteration | `(*DB).NewIter(*IterOptions) (*Iterator,error)`; iterator positioning returns `bool`: `SeekGE`, `SeekPrefixGE`, `SeekLT`, `First`, `Last`, `Next`, `Prev`; access `Key() []byte`, `Value() []byte`, `Valid() bool`, `Error() error`, `Close() error`. `SeekPrefixGE` keys the bloom filter on `key[:Split(key)]`. |
| GC | **No `CompactionFilter`.** Available: `Batch.DeleteRange`, point `Delete`, `Options.BlockPropertyCollectors []func() BlockPropertyCollector`, `(*DB).Checkpoint(destDir, ...CheckpointOption) error` for hot backup **[UNCONFIRMED-verbatim: `Checkpoint` signature — verify against pinned version]**. |
| Fault injection | `github.com/cockroachdb/pebble/vfs/errorfs`: `errorfs.Wrap(fs vfs.FS, inj Injector) *FS`, `errorfs.OnIndex(int32)`, `errorfs.ErrInjected`; wired via `Options.FS`. **[UNCONFIRMED: exact `Injector`/`Op` shape differs released-vs-master — §9.]** |
| Options | `Options{ FS vfs.FS; Comparer *Comparer; Logger base.Logger; Merger *Merger; DisableWAL bool; BlockPropertyCollectors []func() BlockPropertyCollector; ... }`. `Logger` is the 2-method interface `Infof(fmt,...any)`/`Fatalf(fmt,...any)`. |
| Pure Go | Pebble builds under `CGO_ENABLED=0` on every target. cgo-zstd is **opt-in** behind a build tag (default is pure-Go zstd). **[UNCONFIRMED: exact tag name — §6, §9.]** |

**Proven precedent — CockroachDB `EngineKey`** (`cockroach/pkg/storage/engine_key.go`):
`<roachpb-key> 0x00 [ <timestamp bytes> <length-byte> ]`; `sentinel=0x00`,
`suffixEncodedLengthLen=1`; **last byte = `len(version)+1` when versioned, else `0`**;
versions sort **descending timestamp** via a custom `EngineComparer.Compare`; `Split`
returns the prefix length *including* the sentinel. This is exactly the shape §2
locks. **[UNCONFIRMED-verbatim: the `EngineComparer{...}` struct literal in
`pkg/storage/pebble.go` was not fetched field-by-field; the encoding *behavior* above
is confirmed from `engine_key.go`.]**

---

## 2. THE IRREVERSIBLE GATE — MVCC key encoding + the custom `Comparer` (LOCKED)

> **This is the one decision in the whole rebuild that cannot be walked back.**
> `Comparer.Name` is baked into every SSTable's metadata; a store written with one
> comparer refuses to open under another. Everything in §2 must be correct and frozen
> **before the first SSTable is written.**

### 2.1 Keyspace discriminator (one byte, first)

The store is ONE Pebble keyspace under ONE `Comparer`. The **first byte** of every
storage key is a keyspace tag so the three internal namespaces never collide and the
comparer can treat them differently:

| Tag | Namespace | Key body | Versioned? |
|---|---|---|---|
| `0x00` | **MVCC data** | `<user-key> 0x00 <invTs(12)> <lenByte(1)>` | yes (§2.2) |
| `0x01` | **Changelog** | `<commitTs: 12 bytes, NON-inverted BE>` | no (§4) |
| `0x02` | **Metadata** | `<ascii meta-name>` (e.g. `hlc_hi`, `changelog_cursor`, `gc_threshold`, `schema_version`) | no (§3.3, §5.2) |

Tags `0x01`/`0x02` are single-version (no MVCC suffix): the comparer's `Split` returns
`len(key)` for them (whole key is the prefix), and their ordering is plain
`bytes.Compare`. Only tag `0x00` carries the version suffix + trailing length byte.
Because the tag is byte 0, the three namespaces are contiguous and disjoint, and a
user-key can contain any byte (including `0x00`) without colliding across namespaces.

### 2.2 MVCC data key — the LOCKED byte layout

```
 tag   user-key (variable, any bytes incl. 0x00)   sep    inverted HLC (12)        len
┌────┬──────────────────────────────────────────┬──────┬────────────────────────┬──────┐
│0x00│  <collection-id> 0x00 <encoded-pk>        │ 0x00 │ ~(wallMs BE8 ‖ logi BE4)│ 0x0D │
└────┴──────────────────────────────────────────┴──────┴────────────────────────┴──────┘
                    the "user-key"                  ▲            version suffix       ▲
                                                  sentinel                       len(suffix
                                                                                 after sep)+1
```

- **`tag` = `0x00`** — the MVCC-data namespace (§2.1).
- **`user-key`** — the logical key L2/L3 hand the engine. Convention (not enforced by
  the engine, but by the layer above): `<collection-id> 0x00 <order-preserving pk>`.
  The engine treats it as an **opaque byte string that MAY contain `0x00`.**
- **`sep` = `0x00`** — one sentinel byte separating user-key from the version suffix.
- **`invTs`** — the 12-byte version = **bitwise-NOT** of `wallMs(uint64 BE, 8) ‖
  logical(uint32 BE, 4)` (§2.3). Inverting makes a *larger* real `commitTs` produce a
  *smaller* suffix, so newest sorts first under an ascending suffix compare.
- **`lenByte`** — a single trailing byte = **`len(suffix-after-sep) + 1`**, i.e.
  `12 + 1 = 0x0D` for a versioned key. A hypothetical unversioned data key would be
  `0x00 <user-key> 0x00 0x00` (`lenByte = 0`). MVCC data is *always* versioned, so
  `lenByte` is constant `0x0D` in practice; it exists so **`Split` is unambiguous.**

**Why the trailing length byte (not a bare separator, not an escape).** The prompt's
question — how is the separator unambiguous when the user-key contains `0x00`? — has
three candidate answers; we pick the third:

1. *Scan for the first `0x00`* → **WRONG:** a user-key containing `0x00` yields the
   wrong boundary. Rejected.
2. *Order-preserving escape (`0x00`→`0x00 0xFF`, separator `0x00 0x00`)* → works but
   adds an encode/decode pass on every key and complicates `Split`. Rejected as
   needless complexity.
3. *Trailing length byte (Cockroach's proven scheme)* → **CHOSEN.** `Split` reads the
   **last byte**, computes `suffixLen`, and returns `len(key) - suffixLen`. It never
   scans for `0x00`, so any `0x00` inside the user-key is irrelevant. Zero escaping,
   arithmetic `Split`, battle-tested by CockroachDB. This is the correction to the
   parent's under-specified layout (§0.1 #2).

### 2.3 HLC encoding (12 bytes)

`commitTs = { WallMs uint64, Logical uint32 }` — physical wall-clock **milliseconds**
(8 bytes, big-endian) + a **logical counter** (4 bytes, big-endian) that breaks ties
within one physical millisecond and after a restart clock-floor (§3.2).

- **Width:** fixed 12 bytes. Big-endian so lexicographic byte order == numeric order.
- **Inversion:** the stored suffix is `^(wallMs_BE8 ‖ logical_BE4)` (bitwise NOT of all
  12 bytes). Newest (largest) `commitTs` → smallest inverted suffix → sorts FIRST among
  a user-key's versions.
- **Logical width headroom:** `commitTs` is assigned **per group-commit batch**, not
  per write (§3). At the measured ceiling (~51k durable *writes*/s packed 326/batch →
  ~160 *batches*/s; NoSync ~319k writes/s), batches-per-millisecond is far below
  `uint32` capacity even in the pathological all-same-ms case. `uint32` is abundant.
- **[Alternative, not chosen]** nanosecond physical (matches Cockroach HLC) is a
  drop-in if finer resolution is ever wanted; ms matches the parent doc's stated
  "physical ms + logical counter" and is locked for v1.

### 2.4 The `Comparer` (LOCKED — mirror `cockroachkvs`; every field defined)

> **Grill F1/F2/F3/F6 — the biggest finding.** The prior draft of this section had
> **format-fatal** bugs: it derived `Separator`/`Successor` from `DefaultComparer` over
> the **whole key** (F2 — truncates *inside* the inverted-ts suffix → `Split` computes
> `len(key) - ~255` → negative-index panic / mis-indexed SSTable blocks baked into the
> format forever), **omitted the REQUIRED `ImmediateSuccessor`** and never mentioned
> `ComparePointSuffixes`/`CompareRangeSuffixes`/`ValidateKey` (F1), and computed
> `AbbreviatedKey` over the whole key instead of the prefix (F3). Pebble now **ships**
> this exact battle-tested MVCC comparer as `cockroachkvs.Comparer` (§1). This section
> is rewritten to resolve the bug class by mirroring it.

**Resolution — ADOPT vs MIRROR, decided: MIRROR `cockroachkvs`'s techniques, keep our
layout.**

Two paths were evaluated:

- **(A) ADOPT `cockroachkvs.Comparer` wholesale, fork only `Name` to `"skydb.mvcc.v1"`.**
  This dissolves F1–F6 for free (every field is already correct + `CheckComparer`-proven).
  BUT adopting the comparer means adopting Cockroach's **key/suffix encoding shape**:
  its `Split`/suffix-comparers assume **every** store key is a Cockroach `EngineKey`
  with the engine-key trailing-length-byte suffix convention, and it stores the MVCC
  suffix **non-inverted**, reversing order inside `ComparePointSuffixes`.
- **(B) KEEP our fixed-12B *inverted* layout + three-namespace tag scheme, and implement
  the `Comparer` by MIRRORING `cockroachkvs` exactly** for the versioned (tag `0x00`)
  keyspace, with the tag dispatch layered on top.

**Decision: (B) MIRROR.** Path (A)'s layout incompatibility is **prohibitive** because it
would unwind the two decisions the parent doc *validated* and this grill does **not**
redesign:

1. **The keyspace-tag scheme (§2.1) is incompatible with wholesale adoption.**
   `cockroachkvs.Comparer` treats every key as an `EngineKey` and interprets each key's
   trailing byte(s) as MVCC-suffix structure. Our **validated** three-namespace scheme
   has two *unversioned* namespaces — changelog (`0x01`) and metadata (`0x02`) — whose
   keys are flat, not engine-key-shaped (a 12-byte BE `commitTs`, an ASCII meta-name).
   `cockroachkvs.Split` would read the last byte of a changelog key as a suffix length
   and **mis-split** it. Reconciling this would force reshaping the changelog/metadata
   namespaces into engine-key form — i.e., **redesigning the validated tag scheme**.
2. **The inverted 12-byte suffix (§2.3) is our layout, not Cockroach's.** Adoption drops
   inversion in favour of `cockroachkvs`'s non-inverted-suffix + reverse-in-comparer
   encoding — another irreversible change to a validated decision.

Mirroring captures `cockroachkvs`'s **proven techniques** (the exact fixes below) and is
gated by the same mechanical check `cockroachkvs` itself passes — `base.CheckComparer`
(§8.1) — so we inherit its correctness guarantee **without** the coupling or the layout
unwind. `Name` is forked to `"skydb.mvcc.v1"` and is permanent either way.

```
Comparer{
  Name:                 "skydb.mvcc.v1",   // PERMANENT — see below
  Split:                skydbSplit,        // arithmetic, trailing-len byte (F2 guard)
  Compare:              skydbCompare,      // prefix asc, then inverted-suffix asc
  Equal:                func(a,b) bool { return skydbCompare(a,b) == 0 },
  Separator:            skydbSeparator,    // prefix-only delegate + 0x00  (F2)
  Successor:            skydbSuccessor,    // prefix-only delegate + 0x00  (F2)
  ImmediateSuccessor:   skydbImmediateSuccessor, // append 0x00           (F1)
  AbbreviatedKey:       skydbAbbrev,       // over key[:Split] only        (F3)
  ComparePointSuffixes: skydbCmpPointSfx,  // 13B point suffix             (F4)
  CompareRangeSuffixes: skydbCmpRangeSfx,  // 12B range suffix (strip len) (F4)
  // ValidateKey: optional structural check; FormatKey optional (debug only).
}
```

**`Split(key) int` — returns the prefix length (user-key + sentinel), incl. the tag.**
Reads the **trailing length byte** arithmetically; never scans for `0x00`. Includes the
**F2 guard** so a corrupt/oversized length byte can never produce a negative or
out-of-range boundary:

```
skydbSplit(key):
  if key[0] != 0x00:                 // changelog / metadata namespace
      return len(key)                // whole key is the prefix (no version suffix)
  suffixLen := int(key[len(key)-1])  // trailing length byte = len(version)+1 (0 if none)
  if suffixLen == 0:                 // unversioned data key
      return len(key)
  if suffixLen > len(key)-1:         // F2 GUARD: suffix cannot exceed the body →
      return len(key)                //   treat as unversioned rather than index negative
  return len(key) - suffixLen        // boundary = everything up to & incl. the 0x00 sep
```

`Split` is REQUIRED for two things and both must key on the **user-key including the
`0x00` sentinel**: (a) `SeekPrefixGE` prefix-bloom point reads; (b) the GC pass keying
stale-version detection on the user-key (§5). `key[:Split(key)]` == `0x00 <user-key>
0x00`.

**`Compare(a,b) int` — user-key ascending, then version descending (newest first):**

```
skydbCompare(a,b):
  na, nb := skydbSplit(a), skydbSplit(b)
  if c := bytes.Compare(a[:na], b[:nb]); c != 0:   // user-key (+ tag + sentinel) ascending
      return c
  // same user-key: order by version. Suffix is inverted, so ascending bytes.Compare
  // of the suffix == descending real commitTs == newest first. The trailing len byte
  // is constant across a key's versions, so it does not perturb intra-key ordering.
  return bytes.Compare(a[na:], b[nb:])
```

**Confirmation the inverted-suffix trick lets us avoid a hand-written descending
compare:** because the suffix bytes are pre-inverted, a *plain ascending*
`bytes.Compare` on the suffix already yields newest-first. So `Compare` is
"`bytes.Compare` on the prefix, then `bytes.Compare` on the suffix" — no sign-flip
arithmetic. But `Compare` **cannot** be a single whole-key `bytes.Compare`: with a
`0x00`-bearing user-key, a whole-key compare would interleave `"a\x00b"` and `"a"`
incorrectly. It MUST `Split` first, then compare prefix and suffix separately. (This is
why Cockroach ships a custom `EngineComparer.Compare` rather than reusing
`bytes.Compare`.)

**`Separator` / `Successor` / `ImmediateSuccessor` — prefix-only, mirroring
`cockroachkvs` (fixes F1/F2).** The prior "derive from `DefaultComparer` over the whole
key" was the format-fatal bug: `DefaultComparer.Separator` shortens `a` toward `b` byte
by byte and would happily cut **inside** the inverted-ts suffix, producing a key whose
trailing byte no longer equals its true suffix length — so a later `Split` reads a bogus
length and mis-indexes (or negative-indexes) the block boundary, **permanently** in the
SSTable index. The fix is exactly what `cockroachkvs` does:

```
skydbSeparator(dst, a, b):
  // operate on the PREFIX only; never touch the version suffix
  sa, sb := skydbSplit(a), skydbSplit(b)
  sep := DefaultComparer.Separator(dst, a[:sa], b[:sb])   // shorten within prefix space
  if len(sep) < sa && Compare(sep, a[:sa]) ... :          // a proper shortening was found
      return sep                                          // sep is a bare prefix (no suffix) — valid
  return append(dst, a...)                                // no shortening → emit a verbatim

skydbSuccessor(dst, a):
  sa := skydbSplit(a)
  succ := DefaultComparer.Successor(dst, a[:sa])          // successor of the PREFIX only
  if len(succ) < sa || ... proper successor found:
      return succ                                          // bare prefix, no suffix — valid
  return append(dst, a...)                                // no proper successor → verbatim

skydbImmediateSuccessor(dst, a):                           // REQUIRED field (F1)
  return append(append(dst, a...), 0x00)                  // a ‖ 0x00 — the next prefix
```

A `Separator`/`Successor` that emits a **bare prefix** (no version suffix) is sound: a
prefix has no trailing length byte, so `Split` returns `len(prefix)` and it sorts
correctly between user-keys. The invariant `CheckComparer` enforces — `a ≤ sep < b` and
`Split`-consistency — is met because we shorten strictly within the prefix and never
fabricate a suffix. `ImmediateSuccessor(prefix)` = `prefix ‖ 0x00` is the smallest key
strictly greater than every version of `prefix` (used by the range-scan jump-seek, §2.5).

**`AbbreviatedKey` — over the prefix only (fixes F3).** Pebble uses the `uint64` digest
to short-circuit comparisons; it MUST be a monotone function of the *prefix*, or two
versions of one user-key get different abbreviations and the fast path disagrees with
`Compare`:

```
skydbAbbrev(key):
  return DefaultComparer.AbbreviatedKey(key[:skydbSplit(key)])   // NOT the whole key
```

**Point vs range suffix comparers — BOTH defined now (fixes F4, pins the range-key
decision).** Pebble compares suffixes through two hooks; a comparer that leaves them at
the default silently mis-orders any range key written later:

```
skydbCmpPointSfx(a, b):        // a,b are POINT suffixes = 13 bytes (invTs12 ‖ lenByte)
  return bytes.Compare(a, b)   // inverted → ascending bytes == descending commitTs; default is
                               // consistent BECAUSE the inverted store makes it so
skydbCmpRangeSfx(a, b):        // a,b are RANGE suffixes = 12 bytes (invTs12, NO lenByte)
  return bytes.Compare(stripLenByte(a), stripLenByte(b))   // MUST strip the trailing length
                               // byte: point suffix = 13B, range suffix = 12B — comparing
                               // a 13B point suffix against a 12B range suffix under the
                               // DEFAULT comparer mis-orders on the length trailer
```

**Range-key decision (F4), pinned — path (a): both suffix comparers are defined now, so
Pebble MVCC range keys ARE safe to adopt later under `skydb.mvcc.v1`.** With
`ComparePointSuffixes` and `CompareRangeSuffixes` both correct, a future "delete
collection as-of ts" / range-tombstone GC that writes Pebble **range keys** is provably
well-ordered under the *same* permanent `Name` — no `v2` migration forced by a suffix
mismatch. This is deliberately not left silent: writing range keys later while relying
on the **default** `CompareRangeSuffixes` would silently mis-order because of the
point-vs-range length-trailer mismatch (13B vs 12B), and a wrong suffix comparer is as
irreversible as a wrong `Compare`. **Note:** §5's GC uses `Batch.DeleteRange`, which is a
**point rangedel** — it deletes a contiguous span of *point* keys via `Compare`, and does
**not** use range-key suffixes at all — so GC needs nothing from this decision; the
suffix comparers exist purely to keep the *future* range-key door open safely.

**`Name = "skydb.mvcc.v1"` — PERMANENT format string.** Written into SSTable metadata;
a store created with it refuses to open under any other name. Changing the byte layout,
the HLC width, the inversion, or the length-byte convention **requires a new name
(`skydb.mvcc.v2`) AND a full store rewrite/migration** — there is no in-place upgrade.
Locking `Name` on day 1 IS the irreversible gate.

### 2.5 Snapshot read semantics (how a versioned read resolves)

Every `Reader` pins a Pebble seqnum (see the invariant below) and reads `userKey` as of
`readTs`:

```
target        := 0x00 ‖ userKey ‖ 0x00 ‖ invTs(readTs) ‖ 0x0D
targetPrefix  := target[:Split(target)]              // == 0x00 ‖ userKey ‖ 0x00
iter.SeekPrefixGE(target)     // prefix bloom keys on targetPrefix (= key[:Split(key)])
// GRILL C1/C1b FIX: compare prefix BYTES, not Split() return values (prefix LENGTHS).
// Two distinct user-keys of equal length share a Split() int → comparing ints would
// return another key's value when readTs is below all versions of the target.
if !iter.Valid() || !bytes.Equal(iter.Key()[:Split(iter.Key())], targetPrefix):
    return (nil, {}, false)   // fell off this user-key → NO visible version at/below readTs
k := iter.Key()
// k is the first version with invTs >= invTs(readTs) i.e. commitTs <= readTs
// (ascending suffix == descending commitTs; SeekGE lands on newest <= readTs)
commitTs := decodeInvTs(versionOf(k))
if isTombstone(iter.Value()):
    return (nil, commitTs, false)   // visible version is a delete → key absent as-of readTs
return (copy(iter.Value()), commitTs, true)
```

- **"No visible version"** = the seek lands on a *different* user-key (or invalid) →
  every version of `userKey` is newer than `readTs`, or the key never existed. The
  boundary test is a **byte compare of the two prefixes** (`bytes.Equal(k[:Split(k)],
  targetPrefix)`) — never an equality of the two `Split` **integers**, which collide for
  any two equal-length user-keys and would leak a neighbouring key's value.
- **Tombstone** = a versioned delete. Value carries a 1-byte marker discriminating
  `put` (payload follows) from `delete` (empty/marker). A tombstone at the resolved
  version means "absent as-of `readTs`" — distinct from "no version." This case is
  already correct: the newest-first suffix sort + `SeekGE` lands on the newest version
  `≤ readTs`; if that version is a delete, the key reads absent.
- **Range/iterate** (`Reader.Iterate(prefix, readTs)`): iterate ascending user-keys; at
  each *distinct* user-key take the FIRST version (newest ≤ readTs via the same seek),
  skip tombstoned ones, then **jump-seek** past the current key's older versions with
  `SeekGE(ImmediateSuccessor(currentPrefix))` — i.e. `SeekGE(currentPrefix ‖ 0x00)`, the
  smallest key strictly greater than every version of `currentPrefix` (§2.4). (Not a
  vague "SeekGE(nextUserKey…)": `ImmediateSuccessor` is the exact, comparer-defined next
  prefix.) This yields an ordered, snapshot-consistent scan in **O(log n + k)** (k
  distinct visible keys), the property the old `sort.Slice` executor
  (`bluedb_query_kernel.go:482-566`) lacked.

- **Invariant (reader pins a seqnum).** Every `Reader` — not only the long-analytics
  case — pins a Pebble sequence number via the default `Snapshot()` read path (§3.1). A
  reader iterating mid-GC therefore sees a frozen view and can never observe a version
  the GC pass is concurrently deleting; this is what makes mid-GC range scans safe
  (paired with the GC threshold `T` in §5.2).

---

## 3. The `Engine` interface + the committer/commit path

### 3.1 The Go interface (package `runtime-go/bluedb`, rebuild)

Minimal, backend-shaped so an SQLite/Postgres adapter can satisfy the same *L3* surface
later (this interface IS the embedded engine; L3 dispatches to it or to a SQL backend).

```go
package bluedb

// HLC is the total-order commit timestamp assigned by the single committer.
type HLC struct { WallMs uint64; Logical uint32 }

// Engine is the L1 substrate. One Engine == one open file == one committer.
type Engine interface {
    // Snapshot atomically picks readTs := current HLC high-water AND registers its
    // reader token in ONE critical section, then pins a lock-free consistent view.
    // There is NO caller-supplied readTs (grill 2a): a caller must not be able to name
    // a readTs below the GC threshold T, which is the watermark-TOCTOU hole (§5.2).
    // Returns ErrSnapshotTooOld iff the picked readTs would sit below T (defensive; a
    // freshly-picked readTs cannot be below T under the register-before-advance barrier,
    // §5.2). Reader.Close unregisters the token. A transaction's ReadTs for Phase-2
    // validation comes from Reader.ReadTs(), never from NowTs().
    Snapshot() (Reader, error)

    // NowTs returns the current HLC high-water. Informational (metadata, metrics). NOT a
    // way to derive a readTs for a later Snapshot — that reintroduces the 2a TOCTOU;
    // use Snapshot().ReadTs() for a transaction's read view. Cheap, no fsync.
    NowTs() HLC

    // Commit is the ONLY write path. It enqueues req to the single committer, which
    // assigns commitTs, writes data + changelog + metadata in ONE atomic batch,
    // Apply(Sync), and only then acks. Blocks until durable-or-error.
    Commit(req CommitReq) CommitResult

    // Changelog exposes the post-commit stream + the ordered-by-commitTs index that
    // BOTH L2 validation and L4 reactivity read (§4).
    Changelog() Changelog

    // Readers advances/queries the GC watermark registry (§5).
    Readers() WatermarkRegistry

    Close() error   // drains the committer, releases flock, closes Pebble.
}

type Reader interface {
    Get(userKey []byte) (value []byte, commitTs HLC, ok bool)  // ok=false: absent/tombstoned
    Iterate(prefix []byte) Cursor                              // ordered, snapshot-consistent
    ReadTs() HLC
    Close()   // unregisters this readTs from the watermark registry
}

type Cursor interface {
    Next() bool
    Key() []byte      // the user-key (version stripped)
    Value() []byte
    CommitTs() HLC
    Err() error
    Close()
}

// CommitReq is one atomic unit funneled to the committer.
type CommitReq struct {
    Writes  []VersionedWrite   // the buffered write-set (put/delete per user-key)

    // ChangelogPayload is OPAQUE to L1 (grill 1b). L2 owns the encode/decode of the
    // KeyChange list (§4.2 is an L2-owned encoding, NOT an engine type); the engine
    // stores these bytes verbatim at 0x01‖commitTs and hands them back unparsed on
    // tail-read. This is what keeps bluedb.Engine a clean KV/MVCC substrate so the
    // "swap L1 for a SQLite/Postgres adapter" claim actually holds — the engine never
    // interprets CollID/IndexID/IndexCoord.
    ChangelogPayload []byte

    // Phase-2 fields (nil/empty in Phase 1 → pure blind-write fast path). These are
    // L2-owned validation inputs the committer passes to an L2 validator hook in its
    // critical section (Phase 2); the engine does not interpret their contents:
    ReadTs  HLC                // the body's read view = Reader.ReadTs() (never NowTs())
    ReadSet *ReadSet           // point keys + scanned index ranges (nil ⇒ skip validation)
}

type VersionedWrite struct {
    UserKey []byte
    Op      Op       // OpPut | OpDelete
    Value   []byte   // nil for delete
}

type CommitResult struct {
    CommitTs HLC
    Err      error   // nil ⇒ durable. ErrConflict (Phase 2), ErrSealed, Apply error.
}
```

**Contract summary (the load-bearing guarantees L2/L3/L4 build on) — see §8 for the
full list:** `Commit` is the only **assigner of `commitTs`** and the only writer of
**logical** versions — the single serialization point for the total order (GC is a
second, *physical-only* writer that deletes provably-dead versions on disjoint keys,
§5.2, and assigns no `commitTs`); a `CommitResult{Err:nil}` means the batch (data +
changelog + metadata) is durable via `Apply(Sync)`; `Snapshot()` is lock-free and
consistent; the changelog is ordered by `commitTs` and tail-readable in
O(commits-since-readTs).

### 3.2 The single-writer group-commit committer

Reuses the **driving pattern** from `db.go:445-559` (the `committer()` drain loop +
`process()` group) — but the durable sink is a Pebble `Batch` + `Apply(Sync)`, not the
hand-built WAL.

```
committer():                                        // one goroutine per open file
  for first := range ch:                            // db.go:447 pattern
    batch := [first]
    drain up to maxBatch from ch (non-blocking)     // db.go:449-460 pattern
    process(batch)

process(reqs):
  b := db.NewBatch()
  commitTs := hlc.Next()                             // §3.3 — strictly monotonic
  for r in reqs:
    // Phase 2 only: if r.ReadSet != nil, call the L2 validator hook over the changelog
    //               tail (r.ReadTs, commitTs] (L2 decodes the opaque payloads, §4);
    //               on conflict, r.result <- ErrConflict and CONTINUE (exclude from b).
    DEBUG_ASSERT(Writes ↔ ChangelogPayload cover the same UserKeys)  // grill 1b — see below
    for w in r.Writes:
      k := encodeDataKey(w.UserKey, commitTs)        // §2.2
      if w.Op == OpDelete: b.Set(k, tombstoneMarker) // versioned delete = a marker value
      else:                b.Set(k, putMarker ‖ w.Value)
    b.Set(changelogKey(commitTs), r.ChangelogPayload)  // §4, tag 0x01 — OPAQUE L1 bytes
  b.Set(metaKey("hlc_hi"),           encodeHLC(commitTs))       // §3.3, tag 0x02
  b.Set(metaKey("changelog_cursor"), encodeHLC(commitTs))
  ENFORCE_INVARIANT(b contains metaKey("hlc_hi"))               // §3.4 — refuse otherwise
  err := db.Apply(b, pebble.Sync)                    // ONE fsync amortized over the batch
  for r in reqs (non-excluded):
    r.result <- CommitResult{commitTs, err}          // ACK ONLY AFTER Apply returns
  if err == nil && changelog has subscribers:
    fanoutChangelog(commitTs, reqs)                  // §4/L4 — non-blocking, AFTER ack
```

- **Group commit = the throughput floor.** One `Apply(Sync)` amortizes the fsync over
  every write queued in the drain window — the exact amortization the measured
  ~51k-durable-writes/s (326 writes/fsync) result turns on
  (`docs/bluedb/capacity.md:56-63`; ref `bench_test.go`). `maxBatch` mirrors the old
  cap (~1024).
- **Ack-only-after-durable** = the parent's durability contract
  (`docs/bluedb/durability.md:7`). No `r.result <-` before `Apply(Sync)` returns.
- **No mid-batch torn-record hand-rolling.** The old committer manually rolled the WAL
  back to a clean boundary on a mid-batch write fault (`db.go:530-549`). Pebble's
  `Batch` is atomic — a failed `Apply` applies **nothing** — so that entire class of
  code is deleted; the invariant it protected ("never a torn group behind good ones")
  is Pebble's to keep. §7 proves no regression via the ported corpus.
- **Fan-out AFTER ack, off the fsync path**, non-blocking, mirroring
  `changefeed.go:108-122` (`emitChanges`: a full subscriber drops + resyncs; the
  committer is NEVER stalled by a slow consumer).
- **Writes ↔ Changes debug assertion (grill 1b).** Because the changelog payload is
  opaque to L1 (§3.1), the *engine* cannot decode it — so the "every `Write.UserKey`
  maps to a change and vice-versa" check is a **Phase-1 debug-build assertion owned by
  L2** (which constructs both `Writes` and `ChangelogPayload`), run before enqueue; the
  engine optionally accepts a debug-only decode hook to cross-check. This catches a
  silent Writes↔Changes divergence (a write with no changelog entry → an SSI/reactivity
  blind spot) at the layer boundary rather than in production.

- **The single-committer throughput ceiling — stated honestly (grill 4).** Routing all
  writes through one goroutine → one `Batch` → one `Apply(Sync)` means **at most one
  outstanding `Apply`** by design. This is deliberate: it is what buys the HLC total
  order and the serial SSI-validation point (§8.2 C1). The consequence is that Pebble's
  **native commit-pipeline concurrency** — multiple concurrent `Apply`s plus its
  `syncQueue` fsync amortization across independent committers — is **unavailable to us
  by design.** We are not fighting Pebble; we are simply not using its pipeline.
  Concretely: the old ~51k-durable-writes/s figure was measured on the hand-built WAL;
  Pebble adds per-`Apply` overhead (memtable insert, seqnum reservation, commit-pipeline
  mutex, WAL encode, checksum). So the 51k target must be hit via **`maxBatch` /
  memtable / WAL tuning ALONE** (bigger drain windows amortizing the one fsync over more
  writes) — **concurrent `Apply`s are not a lever available to us.** If tuning alone
  cannot reach it, that is an **architectural ceiling to escalate to the user**, not a
  knob to turn — gated by the ported `bench_test.go` (§8.1 criterion 3).

### 3.3 HLC assignment + the restart FLOOR

```
hlc.Next():                       // called only on the committer goroutine (no lock)
  now := wallClock().UnixMilli()  // wallClock is DEPENDENCY-INJECTED (§7 clock-rewind test)
  if now > last.WallMs:            last = {now, 0}
  else if last.Logical == 0xFFFFFFFF:                            // logical EXHAUSTED this wall-ms
                                   last = {last.WallMs + 1, 0}   // borrow into wall+1 — NEVER wrap
  else:                           last = {last.WallMs, last.Logical + 1}  // floor to last, bump logical
  return last

Open(path):
  ... open Pebble ...
  persisted := readMeta("hlc_hi")            // tag 0x02; {0,0} if absent (fresh store)
  now := wallClock().UnixMilli()
  if now > persisted.WallMs: hlc.last = {now, 0}
  else:                      hlc.last = {persisted.WallMs, persisted.Logical}   // floor
  // First Next() after Open therefore returns STRICTLY > persisted (bumps logical or
  // advances wall) regardless of a backward wall clock.
```

- **The rule (parent §6.4 / R8):** `HLC = max(persisted_high_water + 1_tick,
  wall_clock)`. Reading the high-water is not enough — the clock must be **floored** to
  it. A backward wall step (NTP correction, VM migration, scheduler reschedule) MUST NOT
  re-issue a used `commitTs`; if it did, two distinct versions would collide at one key
  → silent corruption.
- **Logical-counter overflow (grill minor).** If the `uint32` logical counter would
  exceed `0xFFFFFFFF` within a single wall-ms (pathological same-ms burst), the committer
  **borrows into `wall+1`** (`{WallMs+1, 0}`) — or seals if that is somehow also taken —
  and **never wraps silently.** A silent wrap would send `commitTs` *backward*, violating
  strict monotonicity (C7) and colliding two versions at one key. Headroom makes this
  unreachable in practice (§2.3), but the guard is unconditional because the failure mode
  is silent corruption.
- **Precedent:** the old engine already recovers a monotonic counter from durable state
  — `db.seq = maxSeq` after WAL replay (`db.go:186`). The HLC floor is that same idea
  (recover the high-water) **plus** the clock-floor guard the seq counter didn't need
  (seq had no wall-clock input; HLC does).

### 3.4 Commit-metadata-in-batch + the enforced invariant

- **`hlc_hi` and `changelog_cursor` are written in the SAME Pebble batch as the data
  versions.** Because `Apply` is all-or-nothing, metadata can never diverge from the
  data it describes — the "single-batch is load-bearing" discipline the old serial
  counter used (`docs/bluedb/schema-enforcement-design.md:70-78`), now inherited from
  Pebble's atomic batch instead of a hand-built commit record.
- **ENFORCED in-batch invariant (parent §6.4 (a) / R8) — scoped to LOGICAL-COMMIT
  batches (grill 2b).** The committer **refuses to `Apply`** any **logical-commit** batch
  (one that assigns a `commitTs` and writes logical versions) that does not also carry
  the `hlc_hi` metadata key. A logical-commit batch reaching `Apply` without it is a
  compiler-bug-class fault (panic/seal), never a silent write — this closes the "metadata
  forgotten, high-water stale on next restart" hole structurally. **GC batches are an
  EXEMPT class:** a GC batch is data-bearing (it physically `Delete`s dead version keys,
  §5.2) but assigns **no** `commitTs` and MUST NOT carry `hlc_hi` (bumping the high-water
  from a GC pass would corrupt the restart floor). GC batches are tagged as the exempt
  class so the invariant distinguishes "logical commit missing its metadata" (a bug) from
  "physical GC delete, correctly metadata-free" (expected).
- **On restart** Pebble replays its own WAL to the last durable batch; the recovered
  `hlc_hi` is the exact high-water of the last durable commit (same batch ⇒ same
  fate). §3.3 floors the clock to it.

---

## 4. The changelog — indexed by `commitTs` (serves BOTH SSI + reactivity)

This is the single structure L2 (validation) and L4 (reactivity) both read. Its shape
is load-bearing for both; get it right in Phase 1 even though the *consumers* land in
Phases 2/4.

**Layering (grill 1b): the changelog KEYSPACE is L1's; the changelog PAYLOAD is L2's.**
The engine owns *where* the changelog lives and *how it is ordered* (tag `0x01`, keyed
by `commitTs`, crash-atomic in the commit batch) — that is pure KV/MVCC substrate. The
engine does **not** own *what the payload means*: the `KeyChange` list in §4.2 is an
**L2-owned encoding** that L1 stores as opaque `[]byte` (`CommitReq.ChangelogPayload`,
§3.1) and returns unparsed on tail-read. So the engine never interprets
`CollID`/`IndexID`/`IndexCoord`, and the "swap L1 for a SQLite/Postgres adapter" claim
holds — the adapter stores the same opaque bytes against the same `commitTs` order.

### 4.1 Where it lives

**A reserved Pebble keyspace, tag `0x01`, keyed by `commitTs` (NON-inverted, 12-byte
BE).** Consequences:

- **Ordered by `commitTs`** — ascending scan == chronological order. The validation
  tail `(readTs, commitTs]` is `SeekGE(0x01 ‖ (readTs+1))` then `Next()` to the
  high-water: **O(commits-since-readTs)**, a bounded recent-tail walk, NOT an O(N)
  changelog scan (parent Decision 4's REQUIRED constraint).
- **Crash-atomic** — written in the SAME batch as the data versions it describes
  (§3.2), so a committed change and its changelog entry share one durable fate. A reader
  can never see a data version whose changelog entry is missing (or vice-versa).
- **Non-inverted** (unlike data suffixes) precisely because we want *ascending =
  chronological* for the tail read; data wants ascending = newest-first for point reads.

### 4.2 Entry payload (per `commitTs`) — an L2-owned encoding

The value at `0x01 ‖ commitTs` is opaque to L1 (§4.1); the layout below is the
**L2-owned** encoding L2 puts into `CommitReq.ChangelogPayload` and decodes on tail-read.
`CollID`/`IndexID`/`IndexCoord` are L2 types the engine never sees. It is an encoded list
of `KeyChange`, one per changed row:

```go
// L2 type (encoded into the opaque L1 changelog payload) — NOT part of bluedb.Engine.
type KeyChange struct {
    Coll     CollID
    Pk       []byte
    Op       Op            // OpPut | OpDelete
    Record   []byte        // put: the row bytes (for L4 cond-membership + read-your-writes);
                           // delete: nil. (fetch-on-demand alt in §4.4)
    NewIndex []IndexCoord  // index positions this row NOW occupies (put); empty for delete
    OldIndex []IndexCoord  // index positions this row VACATED (update/delete); empty for insert
}
type IndexCoord struct { Index IndexID; Key []byte }  // encoded index-entry key (order-preserving)
```

**Why both `NewIndex` and `OldIndex` — this is the crucial SSI detail.** The Phase-2
validator asks, for a transaction that scanned index range `R` on index `J`: *did any
committed change in `(readTs, commitTs]` fall into `R`?* A change falls into `R` if
**either**:

- an **insert/update** placed the row *into* `R` (`NewIndex` has an entry on `J` within
  `R`) → a **phantom appears**; OR
- a **delete/update** removed the row *from* `R` (`OldIndex` has an entry on `J` within
  `R`) → a **phantom disappears**.

Recording only the new position would miss deletes-and-moves-out (a scan that counted
"open" rows must conflict with a concurrent close-an-open-row). Recording both
new+old index coordinates is what upgrades key-only Snapshot Isolation to genuine
**serializable (SSI)** — a key read-set alone cannot witness the absence/removal of a
row (parent Decision 4, headline #1). Point-key reads validate against `Pk`
directly (the unique-constraint TOCTOU case, parent Decision 4).

**For L4 reactivity** the promoted jewel `bluedbChangeAffectsQuery`
(`bluedb_reactive.go:94-142`) needs `Coll`, `Pk`, `Op`, and — on a put that might
*enter* a watched query — the `Record` to test `bluedbEvalCond`. The entry carries
`Record` inline so the reactive path is self-contained (matches the old
`ChangeEvent.Value`, `changefeed.go:14`).

### 4.3 Retention / GC

Changelog entries below the **GC threshold `T`** (§5.2) are trimmed: once every live
reader's `readTs` and every open transaction's `readTs` exceed `commitTs = C` (i.e. `T`
has advanced past `C`), no validator will ever need `(·, C]` and no reactive binding is
still behind `C`, so `0x01 ‖ [0, T)` is dropped with a single `Batch.DeleteRange` — cheap,
ordered, one range tombstone. This is a **physical GC-class delete** (side `Apply`, no
`commitTs`, no `hlc_hi` bump — §3.4's exempt class), so it never perturbs the high-water.
Retention is bounded by **max reader lag**, the same floor `T` as version GC (§5) — the
two trims advance together.

### 4.4 Notes / alternatives

- **[Alt: fetch-on-demand record]** Instead of inlining `Record`, L4 could snapshot-read
  the row at `commitTs`. Saves changelog volume (no row duplication) at the cost of a
  read per change during fan-out. For the short retention window, inline is simpler and
  matches the proven old changefeed; flagged as a Phase-4 tuning knob, not a Phase-1
  decision.
- **`IndexCoord.Key` encoding must be the SAME order-preserving index encoding the scan
  uses** so "falls into `[lo,hi]`" is a byte-range test. Phase 1 fixes the *encoding*;
  Phase 2 builds the range test. (The old order-preserving index kernel
  `bluedb_index_kernel.go` is DROPPED for *data storage* — Pebble orders that — but its
  colType→order-preserving-bytes mapping *informs* the `IndexCoord.Key` encoding.)

---

## 5. Version GC — explicit delete-pass keyed on an advancing watermark

> **CORRECTION (§0.1 #1): Pebble has no compaction filter.** The parent's "compaction
> filter drops versions below the GC threshold" describes an API that does not exist.
> This section is the real mechanism.

### 5.1 The real GC mechanism

**An explicit background GC pass issues *physical-only* `Delete`s for stale versions;
Pebble's background compaction physically reclaims the resulting tombstones.** This is
precisely what CockroachDB does (its MVCC GC queue issues clears; Pebble compacts them) —
there is no callback-in-compaction hook to ride.

```
gcPass():                                    // low-priority goroutine, periodic / write-triggered
  T := gcThreshold()                         // §5.2 — PERSISTED, monotonically-advancing floor
  iter := db.NewIter(dataKeyspace)           // tag 0x00, ordered by (user-key asc, version desc)
  batch := db.NewBatch()
  for each distinct user-key K:
    kept := false                            // keep the newest version < T (readers just above T need it)
    for each version V of K (newest first):
      if commitTs(V) >= T:         continue  // at/above threshold → some reader may need it → keep
      if !kept:                    kept = true; continue   // the newest < T → keep (1 per key)
      batch.Delete(dataKey(K, commitTs(V)))                // strictly older than the kept one → drop
  // PHYSICAL-ONLY (grill 2b). GC deletes go via a SIDE Apply on the exact version keys —
  // NOT through the committer, NO commitTs, NO changelog entry, NO hlc_hi bump:
  db.Apply(batch, pebble.NoSync)             // dead versions need no individual fsync; a later
                                             // real commit's Sync flushes them. Disjoint physical
                                             // keys from the committer → concurrent Apply is key-safe.
```

- **GC deletes are PHYSICAL ONLY (grill 2b).** The earlier "apply through the committer
  path" option is **struck** — it was type-wrong two ways: (i) the committer writes a
  *tombstone at a fresh `commitTs`* (a logical delete), not a raw delete of
  `dataKey(K, oldTs)` — so the key would read **DELETED forever** after, the opposite of
  reclaiming a dead old version; and (ii) it would emit a **changelog entry**, but GC is
  not a logical change, so every reactive query watching that key would misfire
  (`bluedbChangeAffectsQuery` fires on any `Pk` in `resultPks`, including deletes). GC
  therefore does a raw `db.Delete(dataKey(K, oldTs))` on the exact version key via a side
  `db.Apply(batch, NoSync)`: no `commitTs`, no changelog, no `hlc_hi` bump. GC is a
  **second physical writer** whose keys are disjoint from any the committer will write
  (it only ever deletes already-committed, provably-dead versions), so its `Apply` is
  key-safe concurrent with the committer's under Pebble's batch semantics — the C1
  amendment (§8.2) restates "only writer" precisely for this.
- **Always keep the newest version `< T`** so a reader just above `T` still resolves a
  value (§2.5's SeekGE lands on it). Never GC a key's sole remaining version.
- **Tombstone GC:** a versioned *delete* below `T` whose key has no live-reader interest
  is dropped along with the value versions it shadows — the key genuinely vanishes.
  (Correctness: no reader `< T` exists, so no one can observe the key's prior presence.)
- **Range-delete optimization:** for a key with many stale versions, `Batch.DeleteRange`
  over `[dataKey(K, version-just-below-the-kept-one) , ImmediateSuccessor(prefix(K)))`
  collapses them to one range tombstone. This `DeleteRange` is a **point rangedel**
  (spans point keys via `Compare`; no range-key suffixes — §2.4 F4 note), so it needs
  nothing from the range-key decision. Baseline is per-version point `Delete`;
  range-delete is a Phase-1+ tuning knob. **[Grill seed R-1 §9: mixing range tombstones
  with the "keep newest `< T`" rule is fiddly — the range must EXCLUDE the kept version.
  Spec it carefully or stay on point deletes.]**

### 5.2 The advancing-watermark registry + the GC threshold `T` (parent §6.3 / R3; grill 2a)

> **Grill 2a — the watermark TOCTOU (CRITICAL, silent corruption).** The prior design
> let a reader pick `readTs := NowTs()` and *then* register a snapshot. Between the two,
> a reader that has no live token yet does not lower the GC floor — so GC (floor =
> high-water in the empty-set case) could delete a version the stalled reader still
> needs, and a **present key would later read ABSENT.** The fix has three parts.

```go
type WatermarkRegistry interface {
    // Register ATOMICALLY picks readTs := hlc.current() AND records the token in ONE
    // critical section (closes 2a). No caller-supplied readTs. Returns ErrSnapshotTooOld
    // iff readTs < T — defensive only; under the register-before-advance barrier a
    // freshly-picked readTs is always >= T.
    Register() (tok ReaderToken, readTs HLC, err error)
    Advance(tok ReaderToken, readTs HLC) error  // reactive bindings move FORWARD; must be >= T
    Release(tok ReaderToken)
    Threshold() HLC                             // T — persisted, monotone GC floor
}
```

**(i) A persisted, monotonically-advancing GC threshold `T`.** `T` is durable metadata
(tag `0x02`, `gc_threshold`) that only ever moves **up**. GC deletes only versions with
`commitTs` **strictly below `T`** (§5.1). Because `T` is persisted and monotone, no
restart and no clock event can move it backward, and "what GC has decided is
collectible" is a single authoritative value rather than a recomputed-each-pass minimum.

**(ii) `Snapshot()`/`Register()` atomically picks `readTs` AND registers, in one critical
section, rejecting below `T`.** The caller cannot name a `readTs` (the `readTs` arg is
**dropped** from `Engine.Snapshot`, §3.1) — so a caller can never register a token below
`T`. The pick reads the current HLC high-water and inserts the token under the same lock
that `Threshold()` and `Advance()` take, so a registration is never invisible to a
concurrent GC-floor read.

**(iii) GC advances `T` only behind a register barrier.** GC computes a candidate floor =
`min` over live tokens, and **`high-water` when the live set is empty** (codified:
empty-set → high-water, never `{0,0}` or a stale value). It then advances `T` to that
candidate **only inside the registry lock**, which guarantees **no in-flight registration
sits below the new `T`** (register-happens-before-floor-read): any token that will exist
below the candidate has already been recorded and pulls the candidate down; any token
registered after the barrier picks `readTs ≥` the new `T`. This is CockroachDB's
`gcThreshold` discipline. "min over live tokens" **alone** is insufficient — the empty-set
rule and the register-before-advance ordering are the load-bearing parts.

- **A reactive binding holds a `readTs` token it ADVANCES forward** (`Advance`, which must
  land `≥ T`) to each `commitTs` it processes off the changelog — it never pins an old
  Pebble snapshot, so it never blocks GC (parent §6.3). It only ever needs versions
  `≥ its current position`.
- **Every `Reader` pins a Pebble seqnum** (the invariant in §2.5) for the lifetime of its
  token, so a reader mid-scan cannot observe a version GC is concurrently deleting even
  before `T` catches up — the seqnum pin and the threshold `T` are belt-and-braces.
- **Incremental, background, no stop-the-world** — the antithesis of the old
  O(working-set) checkpoint pause (`db.go:602-609`).
- **The one irreducible pin:** a long analytics scan that needs one consistent `readTs`
  for minutes legitimately holds a fixed token (and, if it wants a *frozen* view rather
  than a moving watermark, an actual `pebble.NewSnapshot()`). Honest per-query tradeoff:
  bounded bloat for its lifetime, or an opt-in `snapshot-too-old` error — never a silent
  global cap (parent §6.3).

### 5.3 Optional read-acceleration (NOT GC)

`Options.BlockPropertyCollectors` keyed on per-block `[minCommitTs, maxCommitTs]` lets
read iterators skip whole SSTable blocks that cannot contain a version ≤ `readTs`. This
is the CockroachDB MVCC block-property precedent. It **filters reads, it does not GC** —
orthogonal to §5.1, and a Phase-1+ optimization, not a correctness requirement. Flagged
so the grill doesn't conflate it with GC.

---

## 6. Build integration

### 6.1 Dependency

Add `github.com/cockroachdb/pebble` to `runtime-go/go.mod` (module `sky-app`, go
1.25.0 — verified no pebble dep today). Pebble is **pure Go**; `CGO_ENABLED=0`
cross-compiles to every Sky target (confirmed §1). Follow the existing dep-management
pattern (the runtime already vendors `pgx`, `modernc.org/sqlite`, `golang-lru`, etc.).

### 6.2 The cgo-retry path + the zstd build tag

`sky build` does **two-phase cgo detection** (`rust/crates/project/src/build.rs`):
`run_go_build_detecting_cgo` (build.rs:569) tries **`CGO_ENABLED=0` first**
(build.rs:505-514), and on failure retries with cgo via `run_go_build_once(out_dir,
"1", bin_name)` (build.rs:600). A Sky.Webview app flips straight to cgo up front
(build.rs:576-595).

- **The risk the parent flags:** the cgo-RETRY path (triggered by an FFI package that
  needs cgo) could pull a **cgo zstd** into an otherwise-pure-Go Pebble, adding a C
  toolchain dependency. The parent's fix is a build tag (`-tags pebblegozstd`) so the
  retry path stays cgo-free for Pebble's compression.
- **[UNCONFIRMED — §9]** the exact tag name `pebblegozstd` was NOT source-confirmed
  against the pinned Pebble version; the *intent* (Pebble's default zstd is pure Go;
  cgo-zstd is opt-in behind a tag) is confirmed. **Phase-1 action:** pin Pebble, then
  `go build` the runtime under BOTH `CGO_ENABLED=0` and `CGO_ENABLED=1` and inspect for
  any linked C zstd; if the pinned version's cgo path pulls cgo-zstd, add the *verified*
  opt-out tag to the arg list built in `run_go_build_once` (and to the static path, for
  symmetry). Do not hardcode `pebblegozstd` before confirming it.

### 6.3 Silence Pebble's Logger

`Options.Logger` is the 2-method interface `Infof`/`Fatalf`; `pebble.DefaultLogger`
writes to stderr on `Open`. Pass a **no-op logger** (or route to `Std.Log`):

```go
type quietLogger struct{}
func (quietLogger) Infof(string, ...any)  {}
func (quietLogger) Fatalf(string, ...any) {}   // (consider surfacing Fatalf to Std.Log.error)
opts.Logger = quietLogger{}
```

**[UNCONFIRMED]** do NOT rely on a `pebble.DiscardLogger` export — it was not confirmed
in the root package; the no-op above is the reliable path.

### 6.4 Binary size + transitive trim

Expect **+10–18 MB** on the ~30 MB floor (empirically verified by the parent's grill A).
Pebble pulls sentry/prometheus transitively; trim via build tags / `go.mod replace`
where they're dead weight so the flagship binary stays lean. **[Action: confirm which
transitive deps actually land after `go mod tidy` on the pinned version before writing
`replace` directives.]**

---

## 7. The conformance-oracle fault harness (`errorfs`)

The old crash-corpus **scenarios** port; the **injection harness is net-new** — the old
`walWrap` hook (`fault_test.go:16-31`, `crashsim_test.go:40-77`) was WAL-v2-format-bound
and dies with the hand-built WAL. Re-express injection on Pebble's fault-injecting VFS.

### 7.1 Wiring

```go
inj  := /* errorfs injector, matched to the pinned API shape — see §9 */
opts := &pebble.Options{ FS: errorfs.Wrap(baseFS, inj), Comparer: skydbComparer, Logger: quietLogger{} }
db, _ := pebble.Open(dir, opts)
```

- `baseFS` = `vfs.NewMem()` for deterministic sector-level tests, or `vfs.Default`
  wrapped for on-disk runs.
- **[UNCONFIRMED — §9]** the `errorfs.Injector`/`Op` shape differs *released* (`Op` int
  enum; `MaybeError(op, path)`) vs *master* (`Op` struct; `MaybeError(op)`); there is no
  in-package `And/Or/PathMatch` DSL confirmed (master factors matching into a separate
  `dsl` package). **Pin the Pebble version and match the harness to it before writing
  injectors.**

### 7.2 Scenario → injection map + asserted invariants

| Old corpus scenario (ref) | errorfs / mechanism | Invariant asserted |
|---|---|---|
| Power-loss drops unsynced pages — `crashsim_test.go:199` `TestFuzzPowerLossDropsUnsyncedPages` | crash after `Batch.Set`s but before `Apply(Sync)` returns (inject error on the WAL sync op; or drop the un-synced mem-FS tail) → reopen | Acked (Apply-returned) survive; un-acked in-flight cleanly discarded by Pebble WAL replay |
| Surviving commit after a hole — `crashsim_test.go:280` `TestPowerLossSurvivingCommitAfterHole` | inject fsync error mid-stream, then heal, commit more → reopen | A durable commit *after* a dropped one still recovers; no stranding of good-behind-bad |
| Torn mid-batch all-or-nothing — `crashsim_test.go:362` `TestFuzzTornMidWriteBatchAllOrNothing` | inject a write error partway through one `Apply` → reopen | **No partial apply** — the batch is all-or-nothing (now Pebble's guarantee, not our rollback) |
| Process crash, no sync, preserves pending — `crashsim_test.go:451` `TestProcessCrashNoSyncPreservesPending` | close without final Sync (`DisableWAL=false`, no `pebble.Sync`) → reopen | Everything acked-with-Sync survives; NoSync writes may/may-not — never corrupt |
| Write-fault rollback / no resurrect — `fault_test.go:35` `TestWriteErrorRollsBackNoResurrect` | inject `ErrInjected` (ENOSPC-like) on a WAL write; heal; write again → reopen | Failed `Apply` returns error + **not acked** + not resurrected; post-fault writes land; count exact |
| Concurrent fault, no acked loss — `fault_test.go:81` `TestConcurrentWriteFaultNoAckedLoss` | inject one mid-stream write fault under G-goroutine load → reopen | Every `Commit` that returned nil survives; `Len == ackedCount` |
| Seal on unrollbackable error — `fault_test.go:143` `TestSealOnUnrollbackableError` | inject a fatal background FS error | Engine fails **loud** (`ErrSealed`), refuses further writes; never silent |
| Refuse newer format / wrong comparer — `tier0_safety_test.go:18` `TestG1RefuseNewerWalVersion` | open a store with a DIFFERENT `Comparer.Name` | **Refuses to open** (THE comparer-name immutability test — §2.4) |
| Mid-file scribble refuses + preserves tail — `tier0_safety_test.go:155` | corrupt an SSTable block's bytes | Pebble checksum **fails closed** (read error), no silent truncation (parent §6.4 "mid-file corruption") |
| Torn tail still recovers — `tier0_safety_test.go:212` | drop the WAL's unsynced tail | Recovers to last durable batch; acked prefix intact |
| Hot backup consistency — `backup_test.go:12,126,178` | `(*DB).Checkpoint(dir)` **[UNCONFIRMED sig §9]** while writes proceed | Checkpoint is point-in-time consistent; verify-clean; live store untouched; self-clobber rejected |
| **HLC no re-issue under clock rewind (NET-NEW, R8)** | inject a **backward wall clock** (the committer's `wallClock` is dependency-injected — §3.3), kill mid-commit (drop pending), reopen, commit | The first post-restart `commitTs` is **strictly > persisted `hlc_hi`** despite the backward clock (§3.3 floor); no key sees two versions at one `commitTs` |

**Three invariants every scenario ultimately asserts** (parent §7 Phase-1 success):

1. **acked ⇒ survives** — a `CommitResult{Err:nil}` is durable across any crash.
2. **no torn-batch partial apply** — a batch is all-or-nothing on disk.
3. **HLC never re-issued after recovery** — the restart floor holds under clock rewind.

The clock-rewind test drives a design requirement back into §3.3: **the committer's
clock source MUST be injectable** so the harness can rewind it. Bake that in Phase 1.

---

## 8. Phase-1 success criteria + the interface contracts L2/L3/L4 depend on

### 8.1 Success criteria (the Phase-1 gate)

1. **The `Comparer` is LOCKED and `base.CheckComparer`-verified** — `Name =
   "skydb.mvcc.v1"`, all fields per §2.4 (`Split`/`Compare`/`Separator`/`Successor`/
   `ImmediateSuccessor`/`AbbreviatedKey`/`ComparePointSuffixes`/`CompareRangeSuffixes`),
   frozen before the first SSTable is written. **HARD GATE: `base.CheckComparer(cmp,
   prefixes, suffixes)` passes** over representative user-keys — including `0x00`-bearing
   keys, a key that is a prefix of another, and the empty key — crossed with multiple
   versions and the tombstone case. This mechanically verifies the Split/Separator/
   Successor/suffix invariants and is the cheapest insurance against the irreversible
   format bug (it catches the F2 whole-key-`Separator` truncation and the F3 whole-key-
   `AbbreviatedKey` mismatch that a round-trip test alone would miss). A comparer
   round-trip + prefix-bloom point-read test also passes; a wrong-`Name` open refuses
   (§7 G1).
2. **Point read/write p99 ≤ the old engine** (~1 µs cached read; write p99 ≈ one fsync).
3. **Group-commit throughput ≥ old** (~51k durable writes/s at concurrency —
   `docs/bluedb/capacity.md:56-63`), demonstrated by a port of `bench_test.go` showing
   writes/fsync scaling with in-flight concurrency.
4. **Ordered range scan O(log n + k)** via native Pebble iteration — no scan-then-sort.
5. **No RAM ceiling** — the working set spills to SSTables; a dataset larger than RAM
   reads correctly (the old `MaxKeys`/`ErrFull` cliff, `db.go:94-97`, is gone).
6. **The old crash corpus is green** on the `errorfs` harness (§7 table), incl. the
   three invariants (§7).
7. **Clock-rewind crash test proves no re-issued `commitTs`** (§7 net-new row).
8. **Build facts hold:** builds `CGO_ENABLED=0` on all targets; the cgo-retry path stays
   cgo-free for Pebble (zstd tag verified §6.2); Logger silenced; +10–18 MB.

### 8.2 The interface contracts L2/L3/L4 build on (freeze these in Phase 1)

- **C1 — single serialization point (amended, grill 2b).** "`Commit` is the ONLY writer"
  is now **false** and is restated precisely: `Commit` is the only **assigner of
  `commitTs`** and the only writer of **logical** versions, and it assigns a
  **strictly-monotonic** `commitTs` (total order for free on one node). **GC is a second,
  physical-only writer** (§5) that deletes provably-dead versions on **physical keys
  disjoint** from anything the committer will write — it assigns no `commitTs`, writes no
  logical version, and emits no changelog entry, so it cannot perturb the total order or
  fire reactivity. Its side `Apply` is therefore key-safe concurrent with the committer.
  L2's SSI correctness rests on the commit total order; L4's ordered fan-out rests on it.
- **C2 — durable-on-ack.** `CommitResult{Err:nil}` ⇒ the data + changelog + metadata
  batch is on disk (`Apply(Sync)` returned). L2 may ack a transaction; L4 may fan out;
  Phase-5's async-persist funnel may ack a frame — all only after C2.
- **C3 — atomic commit unit.** The data versions, the changelog entry, and the metadata
  update for one `commitTs` share one Pebble batch → one durable fate. No consumer ever
  sees a data version without its changelog entry, or a stale high-water.
- **C4 — snapshot consistency, lock-free.** `Snapshot()` atomically picks a `readTs`
  (its `Reader.ReadTs()`) and registers its watermark token in one critical section
  (§3.1, §5.2), then returns a view where every `Get`/`Iterate` reflects exactly the
  versions with `commitTs ≤ readTs`, with no locks and no committer coordination. There
  is no caller-supplied `readTs` (that was the 2a TOCTOU). L2 reads the txn body here; L4
  bindings read here.
- **C5 — changelog is ordered by `commitTs` and tail-readable in
  O(commits-since-readTs).** L2 validation walks `(readTs, commitTs]`; L4 consumes the
  tail forward. Each `KeyChange` carries `{Coll, Pk, Op, Record, NewIndex, OldIndex}`
  (§4.2) — sufficient for BOTH point + index-range validation (L2) and
  cond-membership fan-out (L4).
- **C6 — advancing reader watermark.** Readers register/advance/release a `readTs`
  token; GC never drops a version any live reader can still need. L4 bindings advance
  (not pin); L2 open transactions hold their `readTs` until commit/abort.
- **C7 — HLC restart floor.** The first `commitTs` after any restart is strictly greater
  than the last durable one, independent of wall-clock direction. Everything above
  depends on `commitTs` never repeating.

These seven are what Phases 2–4 assume without re-proving. Any later phase that needs a
new contract must add it here, not weaken one.

---

## 9. Top RISKS / open questions (the grill seed)

Ordered by blast radius. Items tagged **[UNCONFIRMED]** are Pebble-API facts this design
depends on that Phase-0 verification could not fully pin — resolve each against the
pinned Pebble version *before* implementation.

- **R-C1 — the GC mechanism — RESOLVED (was HIGHEST).** The parent's "compaction filter"
  does not exist (§0.1 #1); GC is an **explicit physical-only delete-pass** (§5). The
  grill found and closed **two silent-corruption holes**: (2a) the watermark TOCTOU — a
  reader that picked `readTs` before registering could have a needed version GC'd → a
  present key reads ABSENT; fixed by a **persisted, monotone threshold `T`** + an
  **atomic pick-and-register `Snapshot()`** (no caller-supplied `readTs`) + a
  register-before-advance barrier (§5.2). (2b) the GC-delete routing — "through the
  committer" was type-wrong (fresh-`commitTs` tombstone, not a raw old-version delete)
  and would misfire reactivity via a spurious changelog entry; fixed by making GC deletes
  **physical-only** (raw `db.Delete(dataKey(K,oldTs))` via side `Apply(NoSync)`, no
  `commitTs`/changelog/`hlc_hi`), with C1 restated (§8.2) and GC batches made an exempt
  class of §3.4's metadata invariant. **Remaining open (tuning, not correctness):** is a
  periodic pass fast enough to bound version bloat under the North-Star write firehose
  without starving the committer? — a scheduling/throughput question, gated by benching,
  no longer a correctness risk.

- **R-C2 — the `Comparer` — RESOLVED (was IRREVERSIBLE-risk).** The grill found the prior
  spec **format-fatal**: whole-key `Separator`/`Successor` truncating inside the suffix
  (F2 → negative-index `Split`), a missing `ImmediateSuccessor` + unmentioned suffix
  comparers (F1), and whole-key `AbbreviatedKey` (F3). Resolution (§2.4): **MIRROR
  Pebble's shipped `cockroachkvs.Comparer` techniques exactly** — prefix-only
  `Separator`/`Successor`, `ImmediateSuccessor = a‖0x00`, `AbbreviatedKey` over
  `key[:Split]`, a `Split` guard for an oversized length byte, and **both**
  `ComparePointSuffixes` (13B) and `CompareRangeSuffixes` (12B, strip the length trailer)
  defined so range keys are safe later (F4). Adopting `cockroachkvs` **wholesale** was
  rejected because it would unwind the validated keyspace-tag scheme (its `Split` mis-
  interprets our unversioned changelog/metadata keys) and the inverted-suffix layout —
  mirroring captures its correctness without the coupling. **Hard gate:** `base.
  CheckComparer` (§8.1). **Remaining:** the exact Pebble field set must still be matched
  against the pinned version (R-U4) before the literal compiles.

- **R-C3 — the changelog shape is insufficient for SSI range-validation.** §4.2 records
  `NewIndex` + `OldIndex` coordinates so a scanned range can be tested; the design stands
  (it validates phantom-appears AND phantom-disappears). **Layering fixed (grill 1b):**
  the payload is now an **L2-owned opaque encoding** the L1 engine stores verbatim
  (§3.1/§4), so the encoding is entirely L2's to freeze — but freeze it in Phase 1 it
  must, because the *bytes* are on disk from the first commit. Grill (unchanged, Phase-2
  concern): is per-row index-coordinate capture enough to reconstruct "did a change fall
  in scanned range `R` on index `J`", including *composite* indexes, *descending* index
  order, and *covering* scans? Is the `IndexCoord.Key` encoding truly the SAME
  order-preserving encoding a Phase-2 scan will range over? If they drift, SSI validation
  silently under- or over-rejects.

- **R-C4 — HLC / metadata crash-atomicity edge cases (partially resolved).** §3.3/§3.4
  rely on Pebble replaying the metadata key in the same batch as the data, and on an
  injectable clock. **Resolved by the grill:** the "does the `hlc_hi` gate cover
  GC-delete batches?" question — GC batches are now an **exempt class** (§3.4): they are
  physical-only, assign no `commitTs`, and MUST NOT carry `hlc_hi`, so the invariant is
  scoped to logical-commit batches and correctly ignores GC. The logical-counter overflow
  edge is also pinned (§3.3 — borrow into `wall+1`, never wrap). **Still to pin against
  the pinned Pebble version:** on a WAL replay that recovers to batch N, is the recovered
  `hlc_hi` *guaranteed* to be batch N's (not N-1's, not a memtable-flush artifact)? Does
  Pebble's internal sequence-number recovery interact with our HLC in any way we must
  account for? (The §7 clock-rewind crash test is the empirical check.)

- **R-U1 — [UNCONFIRMED] `errorfs` API shape** (§1, §7). `Injector`/`Op` differs
  released-vs-master; no in-package matcher DSL confirmed. The whole §7 harness is
  written against it. **Pin Pebble first; match the harness to that exact version.**

- **R-U2 — [UNCONFIRMED] `pebblegozstd` build tag** (§6.2). The tag *name* is not
  source-confirmed; the cgo-retry path's zstd behavior must be empirically checked under
  `CGO_ENABLED=1` on the pinned version before trusting the parent's tag claim.

- **R-U3 — [UNCONFIRMED] `(*DB).Checkpoint` signature** (§1, §7 backup rows) and
  **`pebble.DiscardLogger`** non-existence (§6.3). Backup scenarios and logger silencing
  depend on these; verify signatures against the pinned godoc.

- **R-U4 — [UNCONFIRMED] Comparer suffix fields** (§1). Recent Pebble splits the suffix
  comparator into `CompareRangeSuffixes`/`ComparePointSuffixes`; older v1.x differs.
  Our `Comparer` literal must match the pinned version's field set exactly or it won't
  compile / won't behave.

- **R-1 — range-tombstone GC vs "keep newest `< T`"** (§5.1). The DeleteRange
  optimization must EXCLUDE the kept version; mis-specified, it deletes a key's only live
  version. It is a point rangedel (no range-key suffixes), so F4 does not bear on it.
  Baseline stays on point deletes until the range variant is proven.

- **R-2 — reader-watermark liveness.** The GC threshold `T` advances no further than
  `min` over live tokens (§5.2). A leaked token (a reactive binding that never releases, a
  crashed long scan) pins `T` and unbounds retention — the 2a fix closes the *correctness*
  hole (a live reader is never GC'd out from under) but not this *liveness* hole. Grill: is
  there a max-token-age / heartbeat so a dead reader can't freeze `T` forever? (Parent's
  `snapshot-too-old` is the opt-in escape for the analytics case; the *leak* case needs a
  liveness guard.)

---

## Appendix — reuse ledger (ref-worktree citations)

| Carries forward | From | As |
|---|---|---|
| Group-commit drain+process driving pattern | `db.go:445-559` (`committer()`/`process()`) | ADAPT over Pebble `Batch`+`Apply(Sync)` (§3.2) |
| Non-blocking post-commit fan-out (drop+resync, never stall committer) | `changefeed.go:52-122` (`Subscribe`/`emitChanges`) | ADAPT into the changelog fan-out (§3.2, §4) |
| Monotonic-counter-recovered-from-durable-state | `db.go:186` (`db.seq = maxSeq`) | PRECEDENT for the HLC restart floor (§3.3) — plus the net-new clock-floor |
| Single-writer file lock | `flock_unix.go:14-16`, `db.go:214-219` | PORT as-is (needed by any embedded engine) |
| The two reactive jewels | `bluedb_query_kernel.go:338` (`bluedbEvalCond`), `bluedb_reactive.go:94-142` (`bluedbChangeAffectsQuery`/`bluedbQuerySub`) | Changelog `KeyChange.Record` shaped to feed them (§4.2); PROMOTE in Phase 4 |
| Crash/fault/backup **scenarios** | `crashsim_test.go`, `fault_test.go`, `tier0_safety_test.go`, `backup_test.go` | PORT-as-oracle via net-new `errorfs` (§7) |
| Durability contract (ack-only-after-recoverable) | `docs/bluedb/durability.md:7` | Contract C2 (§8.2) |
| Single-batch-is-load-bearing discipline | `docs/bluedb/schema-enforcement-design.md:70-78` | Commit-metadata-in-batch + invariant (§3.4) |

**Retired (replaced by Pebble + MVCC):** the hand-built WAL (`wal.go`), the RAM-map
memtable + O(N) checkpoint (`db.go:113-114,254-259,602-609`), the manual
order-preserving index kernel (`bluedb_index_kernel.go`), the manual torn-tail rollback
(`db.go:530-549`), and — the correction that motivated this doc — the *imagined* Pebble
compaction filter (§0.1 #1).
