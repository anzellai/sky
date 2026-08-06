# BlueDB Phase 1 — implementation status

> Tracks what the `runtime-go/bluedb/` package delivers against
> `docs/bluedb/phase1-engine-design.md`. Branch: `feat/bluedb`.

## Pinned dependency

- **`github.com/cockroachdb/pebble/v2 v2.1.6`** (latest stable v2.x at pin time).
  - **Deviation from the design doc:** the doc references
    `github.com/cockroachdb/pebble`; the real v2.x module path is
    `github.com/cockroachdb/pebble/v2`. All imports use `/v2`.
  - Pure Go under `CGO_ENABLED=0`. No `internal/base` import is needed — the public
    `pebble` package re-exports everything used (`Comparer`, `CheckComparer`,
    `DefaultComparer`, `Sync`, `NoSync`, `Logger`, …).

## [UNCONFIRMED] items resolved against v2.1.6 (§9)

| Item | Design assumption | Reality at v2.1.6 | Action taken |
|---|---|---|---|
| **R-U4** Comparer field set | `ComparePointSuffixes`/`CompareRangeSuffixes` split; `ImmediateSuccessor` required | CONFIRMED. `pebble.Comparer` (= `base.Comparer`) has exactly these fields plus `ValidateKey`/`FormatKey`/`Name` (a string field). | Comparer literal matches the real struct; `ValidateKey` left nil (optional), `FormatKey` borrowed from `DefaultComparer` so `CheckComparer` error paths never nil-panic. |
| `base.CheckComparer` signature | `(cmp, prefixes, suffixes)` | CONFIRMED `CheckComparer(c *Comparer, prefixes, suffixes [][]byte) error`, re-exported as `pebble.CheckComparer`. **Also discovered: it STRIPS leading bytes off each prefix and requires the stripped prefix to still Split correctly.** | Forced `Split` to be **tag-independent** (pure trailing-length-byte) — see the deviation below. |
| `DB.Apply` / `WriteOptions{Sync}` | `db.Apply(batch, pebble.Sync)` | CONFIRMED. `pebble.Sync`/`pebble.NoSync` are exported `*WriteOptions`. `Set/Delete` take `*WriteOptions` (nil OK). | Committer uses `db.Apply(b, pebble.Sync)`. |
| `NewSnapshot` | `(*DB).NewSnapshot() *Snapshot`; `Get`/`NewIter`/`Close` | CONFIRMED. | Every `Reader` pins a `*pebble.Snapshot`. |
| **R-U3** silence Logger | no-op `Logger`; `DiscardLogger` may not exist | CONFIRMED no reliable `DiscardLogger`. **The Logger interface has THREE methods (`Infof`/`Errorf`/`Fatalf`), not two as the doc said.** | `quietLogger` implements all three. |
| **R-U2** cgo-zstd build tag | tag name `pebblegozstd` (unconfirmed) | **CONFIRMED VERBATIM.** `internal/compression/zstd_cgo.go` is `//go:build cgo && !pebblegozstd` (DataDog/zstd, cgo); `zstd_nocgo.go` is `//go:build !cgo \|\| pebblegozstd` (pure-Go klauspost). Under `CGO_ENABLED=0` only pure-Go zstd is in the graph; under `CGO_ENABLED=1` DataDog/zstd (cgo) is pulled **unless** `-tags pebblegozstd`. | **Build-integration guidance for `sky build`'s cgo-retry path (`run_go_build_once`): pass `-tags pebblegozstd` so the retry stays cgo-free for Pebble compression.** (Wiring into `build.rs` is a build-integration follow-up, not part of the engine package.) |
| **R-U1** `errorfs` shape | injector/op differs released-vs-master | Not resolved — DEFERRED to 1b (fault harness). |
| `Checkpoint` signature | backup rows | Not resolved — DEFERRED to 1b (backup). |

## LOCKED-format deviations from the design doc (necessary, documented)

1. **`Split` is tag-independent, not tag-dispatched.** The design's `skydbSplit`
   pseudo-code branched on `key[0] == 0x00`. `base.CheckComparer` strips leading
   bytes off prefixes and re-tests `Split`, so a tag-dispatched `Split` FAILS the
   gate (a stripped data prefix no longer starts with `0x00`). Resolution: `Split`
   reads ONLY the trailing length byte (with the F2 oversized-length guard) — exactly
   cockroachkvs's technique — so it is position-independent from the front.
2. **Unversioned (changelog/metadata) keys carry an explicit trailing `0x00` length
   byte.** To keep `Split` uniformly trailing-byte-driven (point 1), changelog keys
   are `0x01 ‖ commitTs(12 BE) ‖ 0x00` and metadata keys are `0x02 ‖ name ‖ 0x00`.
   The trailing `0x00` means "no version suffix" → `Split` returns `len(key)`, matching
   the design's INTENT (§2.1 "Split returns len(key) for them") without a tag branch.
   The design's byte-level changelog sketch omitted this trailer.

Everything else in §2 is as locked: `Name = "skydb.mvcc.v1"` (permanent); data key
`0x00 ‖ userKey ‖ 0x00 ‖ ~(wallMs BE8 ‖ logical BE4) ‖ 0x0D`; inverted 12-byte HLC
(newest sorts first); keyspace tags 0x00 data / 0x01 changelog / 0x02 metadata.

## DONE (Phase 1a) — `runtime-go/bluedb/`

- **Pebble dep wired; CGO=0 clean** — native build, `CGO_ENABLED=0` build, and
  `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` cross-compile all clean; the rest of
  `runtime-go` (`go build ./...`) still builds. `go mod tidy` + `go mod verify` clean.
- **LOCKED MVCC key encoding + custom `Comparer`** (`keys.go`, `comparer.go`) —
  mirrors cockroachkvs's techniques (prefix-only `Separator`/`Successor`,
  `ImmediateSuccessor = a‖0x00`, `AbbreviatedKey` over `key[:Split]`, F2 `Split`
  guard, both 13B point + 12B range suffix comparers). **`base.CheckComparer` GREEN**
  over the adversarial key set (`TestCheckComparer`).
- **`Engine` interface (§3.1)** (`engine.go`) — `Snapshot`/`NowTs`/`Commit`/
  `Changelog`/`Readers`/`Close` + `Reader`/`Cursor`/`Changelog`/`WatermarkRegistry`
  + `CommitReq`/`VersionedWrite`/`CommitResult`/`ReadSet`/`ChangelogEntry` frozen.
- **Versioned Put/Get + snapshot read (§2.5)** (`reader.go`) — MVCC resolution with
  the grill **C1 prefix-BYTES boundary fix**; tombstone at ≤readTs reads absent;
  ordered `Iterate` cursor via `ImmediateSuccessor` jump-seek. `snapshotAt(readTs)`
  is an in-package time-travel reader backing the versioned round-trip tests (the
  public `Snapshot()` drops caller-supplied readTs per the 2a fix).
- **Single-writer group-commit committer (§3.2/3.3/3.4)** (`committer.go`, `hlc.go`,
  `pebble_engine.go`) — one goroutine, drain ≤ maxBatch → one `Batch` → one
  `Apply(Sync)` → ack-after-durable. HLC strictly-monotonic with the restart FLOOR
  (`max(persisted_high_water, wall)`, floored so a backward clock never re-issues) +
  `uint32` logical overflow **borrows into wall+1, never wraps**. `hlc_hi` +
  `changelog_cursor` metadata written IN THE SAME batch; `enforceLogicalBatchInvariant`
  refuses a logical batch lacking `hlc_hi` (§3.4, scoped to logical batches).
  Injectable wall clock for the clock-rewind test.
- **Changelog WRITE (§4)** (`changelog.go`) — opaque `[]byte` payload stored verbatim
  at `0x01 ‖ commitTs`; commitTs-ordered ascending `Tail(after)` read. The engine
  never interprets the payload (L2-owned encoding).
- **`-race` unit tests** (`comparer_test.go`, `engine_test.go`), all GREEN:
  `TestCheckComparer`, `TestComparerName`, `TestSplitTagIndependent`,
  `TestVersionOrderingNewestFirst`, `TestPrefixBoundaryDistinctKeys`,
  `TestVersionedRoundTrip`, `TestSnapshotIsolation`, `TestTombstone`,
  `TestHLCMonotonicRestartFloor`, `TestMetadataInBatch`, `TestGroupCommitBasic`,
  `TestChangelogWrite`.

## DEFERRED (Phase 1b) — noted here + as `// TODO(phase1b)` in code

- **Watermark GC pass (§5)** — the explicit physical-only delete-pass keyed on the
  persisted, advancing threshold `T`; the register-before-advance barrier that ADVANCES
  `T` (min-over-live / empty-set→high-water). Phase 1a implements the registry's
  Register/Release/Threshold + `minLive()` bookkeeping but never collects (T stays at
  its persisted value; `Register` can't be rejected). `watermark.go`, `committer.go`.
- **`errorfs` crash-corpus harness (§7)** — the fault-injection oracle (R-U1 needs the
  injector shape pinned to v2.1.6 first).
- **`flock`** — single-writer file lock (port of the retired `flock_unix.go`).
- **Throughput benchmark (§8.1 #3)** — the `bench_test.go` port proving ≥ old
  ~51k durable writes/s via `maxBatch`/memtable/WAL tuning.
- **L2 SSI validator** — the changelog `NewIndex`/`OldIndex` decode + range-validation
  (L2-owned; L1 stores opaque bytes only).
- **Non-blocking changelog fan-out** — post-ack subscriber fan-out (L4 reactivity).
- **`sky build` `-tags pebblegozstd` wiring** — add the tag to the cgo-retry path in
  `rust/crates/project/src/build.rs` `run_go_build_once` (and the static path for
  symmetry). Verified-correct tag; wiring is a build-integration change outside the
  engine package.
- **Block-property read acceleration (§5.3)**, `Checkpoint` hot-backup (R-U3 sig),
  range-tombstone GC (R-1).

## Verification commands (all green at status time)

```
cd runtime-go
go build ./bluedb/...
CGO_ENABLED=0 go build ./bluedb/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./bluedb/...
go build ./...                 # rest of runtime-go unaffected by the Pebble dep
go vet ./bluedb/...
go test ./bluedb/ -race -count=1
go mod tidy && go mod verify
```
