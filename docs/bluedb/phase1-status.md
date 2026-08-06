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
| **R-U3** silence Logger | no-op `Logger`; `DiscardLogger` may not exist | CONFIRMED no reliable `DiscardLogger`. **The Logger interface has THREE methods (`Infof`/`Errorf`/`Fatalf`), not two as the doc said.** | `quietLogger` silences `Infof`/`Errorf`; **`Fatalf` PANICS (Fix-1) — a no-op there breaks durability** (see the Fatalf row below). |
| **`Logger.Fatalf` must not be a no-op** (Fix-1, net-new critical finding) | design assumed a silent Logger was safe | **At v2.1.6, `applyInternal` (db.go:882-897) calls `Logger.Fatalf(...)` on a fatal WAL commit error and then FALLS THROUGH to `return nil`.** The stock logger's `Fatalf` does `os.Exit(1)`; a NO-OP made `Apply(Sync)` return `nil` for a write that never fsync'd → the committer acked `Err:nil` for a lost write → **acked⇒durable violated deterministically.** | `quietLogger.Fatalf` now **panics**. Under `pebble.Sync` + `!noSyncWait` the WAL sync is synchronous, so the panic unwinds through `Apply` on the committer goroutine, where `process()`'s deferred recover seals the engine + delivers an ERRORED ack (never a false `nil`). Asserted by the rewritten `TestInjectedFaultsReopenConsistent`. |
| **R-U2** cgo-zstd build tag | tag name `pebblegozstd` (unconfirmed) | **CONFIRMED VERBATIM.** `internal/compression/zstd_cgo.go` is `//go:build cgo && !pebblegozstd` (DataDog/zstd, cgo); `zstd_nocgo.go` is `//go:build !cgo \|\| pebblegozstd` (pure-Go klauspost). Under `CGO_ENABLED=0` only pure-Go zstd is in the graph; under `CGO_ENABLED=1` DataDog/zstd (cgo) is pulled **unless** `-tags pebblegozstd`. | **Build-integration guidance for `sky build`'s cgo-retry path (`run_go_build_once`): pass `-tags pebblegozstd` so the retry stays cgo-free for Pebble compression.** (Wiring into `build.rs` is a build-integration follow-up, not part of the engine package.) |
| **R-U1** `errorfs` shape | injector/op differs released-vs-master | **RESOLVED at v2.1.6 (the "master" shape).** `errorfs.Injector` is `interface { MaybeError(op errorfs.Op) error }`; `errorfs.Op` is a STRUCT `{ Kind OpKind; Path string; Offset int64 }` (NOT an int enum); `errorfs.InjectorFunc(func(Op) error)` adapts a plain func; `errorfs.Wrap(baseFS vfs.FS, inj Injector) *FS`; `errorfs.ErrInjected` is the sentinel. OpKinds used: `OpFileWrite` / `OpFileSync` / `OpFileSyncData` / `OpCreate`. No in-package `And/Or/PathMatch` DSL needed (a plain `InjectorFunc` closure over `op.Kind` suffices). Crash simulation uses `vfs.NewCrashableMem()` + `(*MemFS).CrashClone(vfs.CrashCloneCfg{})` — the FS state containing exactly the last-SYNCED data (deterministic). Harness in `crashsim_test.go`. |
| `Checkpoint` hot-backup | backup rows | Still DEFERRED (out of the 1b engine-substrate scope; L2/backup phase). Crash-consistency is proven instead via `vfs.CrashClone` recovery, which is the canonical Pebble crash-consistency method. |
| **Durability-fault surfacing** (net-new finding, revised by Fix-1) | design assumed injected faults surface as an `Apply` error | **At v2.1.6 a fatal WAL-sync fault is NOT returned synchronously from `Apply` — Pebble calls `Logger.Fatalf` then `return nil` (db.go:882-897).** With `quietLogger.Fatalf` panicking (Fix-1), that fatal path unwinds SYNCHRONOUSLY through `Apply` on the committer goroutine (the WAL sync is synchronous under `pebble.Sync` + `!noSyncWait`), where `process()`'s recover seals + errored-acks. A NON-fatal faulted WAL write can still leave that write unsynced (truncated WAL tail per WAL semantics) with the commit acking — that class is proven via `CrashClone` recovery-to-last-synced-prefix. So the committer's seal is driven by (a) an `Apply`-returned error and (b) the synchronous Fatalf-panic recovered on the committer goroutine; a `nil` ack always means durable. |
| **Single-process dir lock** | design proposed a `flock` port | **Pebble PROVIDES IT — do not reinvent.** `pebble.Open` acquires an exclusive OS lock (a `LOCK` file; `flock` on unix via `vfs/file_lock_unix.go`, `MemFS.Lock` → `EAGAIN` in-process). A second `Open` of the same directory FAILS, including from the same process. Asserted by `TestSecondOpenFailsSingleProcessLock`. No custom flock added. |

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

## DONE (Phase 1b) — completes the Phase-1 engine substrate

- **Watermark version-GC pass (§5)** (`gc.go`, `watermark.go`) — `Engine.GC() (GCStats,
  error)`, the explicit PHYSICAL-ONLY delete-pass with the grill-critical fixes, NOT a
  naive one:
  - **Persisted, monotone GC threshold `T`** in the `0x02` metadata keyspace
    (`gc_threshold`). GC deletes ONLY versions strictly below `T`. `T` is persisted
    (Sync) BEFORE any physical delete, so a crash can't leave a durable delete under a
    regressed `T`. Recovered on `Open` and re-affirmed monotone
    (`setThresholdAtLeast`). Asserted: `TestGCPersistsThresholdMonotone`.
  - **`Snapshot()`/`Register()` atomically pick `readTs := high-water` AND record the
    token in ONE critical section, rejecting `readTs < T` with `ErrSnapshotTooOld`**
    (closes grill 2a — no caller-supplied `readTs`, no register-gap TOCTOU). Asserted:
    `TestGC2aReaderProtected`, `TestGCSnapshotTooOld`.
  - **`advanceThreshold()` — the register-before-advance barrier** — computes the
    candidate (min over live tokens, or the high-water when the live set is EMPTY) AND
    commits it as the new `T` under the same registry lock `Register` takes, so no
    in-flight registration can sit below the new `T`. Monotone (up only).
  - **Delete pass keeps the newest version `< T` per user-key** (a reader at exactly `T`
    still resolves it) and deletes strictly-older versions; **never GCs a key's sole
    version.** Asserted: `TestGCDropsStaleVersionsBelowT`,
    `TestGCKeepsNewestBelowFloorAndSoleVersion`.
  - **GC deletes are PHYSICAL ONLY** — raw `db.Delete(dataKey(K, oldTs))` on the exact
    version key via a side `db.Apply(batch, pebble.NoSync)`: **NO commitTs, NO changelog
    entry, NO hlc_hi bump** (closes grill 2b). GC is a SECOND physical writer on keys
    DISJOINT from the committer's fresh-commitTs writes → concurrent `Apply` is key-safe
    (C1 amendment). Asserted: `TestGC2bPhysicalOnly` (changelog byte-unchanged + no
    hlc_hi bump across a pass, incl. a reopen check), `TestGCConcurrentWithCommitter`
    (`-race`).
  - **Changelog retention** — trims `0x01 ‖ [0, T)` via `Batch.DeleteRange`. Asserted:
    `TestGCChangelogRetentionTrimsBelowT`.
- **`errorfs` crash-corpus harness (§7)** (`crashsim_test.go`) — R-U1 resolved (see the
  table above). Scenarios + invariants, all GREEN: acked⇒survives
  (`TestCrashAckedWritesSurvive`), no-torn-batch all-or-nothing (`TestCrashNoTornBatch`),
  HLC-no-reissue-under-clock-rewind (`TestCrashHLCNoReissue`, net-new R8), concurrent
  no-acked-loss (`TestCrashConcurrentNoAckedLoss`), metadata+data recover together
  (recovered hlc_hi == max data version), fault→reopen-consistent
  (`TestInjectedFaultsReopenConsistent`), and the fail-loud seal contract
  (`TestSealContractRefusesWrites` — a sealed engine refuses Commit AND GC with
  `ErrSealed`). The committer now SEALS on a durability fault (an `Apply` error OR a
  synchronous durability panic recovered on the committer goroutine).
- **Single-process locking** — Pebble PROVIDES the exclusive dir lock (see the table);
  `TestSecondOpenFailsSingleProcessLock` asserts a second `Open` fails. No custom flock
  added. Bonus gate: `TestWrongComparerNameRefusesOpen` (§7 G1 comparer immutability).
- **Throughput benchmark + honest ceiling (§8.1)** (`bench_test.go`) — measured on
  Apple M1, Pebble default sync, `go test -bench . -benchmem -run '^$'`:
  - **`BenchmarkGroupCommitDurableWrites` — ~56,000 durable writes/s** at 512-writer
    concurrency — **MEETS/EXCEEDS the ~51k target.** Group commit is the throughput
    lever: many concurrent writers coalesce into one `Apply(Sync)`/one fsync (the commit
    channel was made buffered so writers enqueue without blocking; FIFO delivery keeps
    the commitTs total order). Per grill finding 4 this is NOT concurrent Applies (which
    would break the total order). At low concurrency (≤8 writers) throughput is fsync-
    bound (~1 fsync/commit); the ceiling needs many in-flight writers to fill batches.
  - **`BenchmarkPointRead` — ~1.8 µs/op** cached point read off a pinned snapshot (block
    cache).
  - **`BenchmarkRangeScan` — ~15 ms for a 50k-key ordered scan (~300 ns/key)**, native
    Pebble iteration, O(log n + k), no scan-then-sort.
  - **`TestSpillToDiskNoRAMCeiling`** — 60k keys × 256 B (~15 MB) with a 256 KiB memtable
    spills to SSTables and reads back exactly; the old `MaxKeys`/`ErrFull` cliff is gone.

**PHASE 1 (the engine substrate) is COMPLETE** and ready for the phase-boundary
grill + Judge. The C1–C7 interface contracts (§8.2) are all satisfied and frozen.

## DONE (Phase 1b hardening — grill-found blocking fixes)

Two fresh-context adversaries traced three blocking holes to exact lines; all three
are now closed with regression tests (all `-race` green; group-commit throughput
re-measured at ~51–54k durable-writes/s, unchanged — the fsync stays per-batch).

- **Fix-1 — `quietLogger.Fatalf` no-op defeated Pebble's fail-stop (CRITICAL,
  durability).** Pebble's `applyInternal` (db.go:882-897, v2.1.6) calls `Logger.Fatalf`
  on a WAL fsync failure then `return nil`; a no-op `Fatalf` made `Apply(Sync)` ack
  `Err:nil` for a NON-durable write. Now `Fatalf` **panics** (`pebble_engine.go`); the
  panic unwinds synchronously through `Apply` on the committer goroutine, where
  `process()`'s recover (`committer.go`) seals the engine, logs the fault to
  `os.Stderr`, and delivers an ERRORED ack for the in-flight batch. A `nil` ack now
  always means durable. Regression: the rewritten `TestInjectedFaultsReopenConsistent`
  injects a WAL-fsync fault and asserts every `nil`-acked commit survives reopen +
  the engine fail-stops (no `nil`-acked-and-absent write) after the fault.
- **Fix-2 — one `commitTs` shared across N group-commit jobs collided (HIGH, silent
  corruption).** `process` assigned ONE `commitTs` to the whole drained batch, so every
  job's data-version key AND changelog key collided at that ts → in a multi-job batch,
  last-Set-wins silently dropped all but the last (both acked `nil`). Now a **DISTINCT
  `commitTs` per JOB** is assigned in FIFO drain order (still ONE `Batch` + ONE
  `Apply(Sync)` — fsync amortization preserved), with `hlc_hi`/`changelog_cursor`
  metadata set to the last (highest) job's ts and each job acking its OWN ts.
  Per-job commitTs also aligns forward (Phase-2 SSI needs per-transaction commitTs).
  Regressions: `TestGroupCommitPerJobDistinctChangelog` (≥2 distinct changelog payloads
  in one batch → all present at distinct ts), `TestGroupCommitPerJobSameKeyDistinctVersions`
  (two same-key writes in one batch → two distinct MVCC versions).
- **Fix-3 — `persisted_T` could exceed `durable_hlc_hi` (BLOCKER, durability-invariant
  break).** GC's `advanceThreshold` derived its candidate from the IN-MEMORY high-water
  (advanced at commitTs-ASSIGNMENT time, before `Apply(Sync)`); a crash between GC's
  threshold-Sync and a later commit's Apply-Sync could recover `hlc_hi < gc_threshold`
  → every reader wedged on `ErrSnapshotTooOld` + post-recovery commits fell in the
  trimmed changelog tail. Now the committer maintains a **`durableHi`** advanced ONLY
  after `Apply(Sync)` returns (`pebble_engine.go`); `advanceThreshold` clamps its
  candidate to `durableHi` (`watermark.go`), guaranteeing `persisted_T ≤ durableHi ≤
  durable hlc_hi` unconditionally (clamping DOWN is always correctness-safe). Regressions:
  `TestAdvanceThresholdClampsToDurableHi` (unit), `TestGCThresholdNeverExceedsDurableHi`
  (post-GC invariant), `TestGCThresholdClampSurvivesCrashNoReaderWedge` (crash regression
  — in-flight-but-not-applied interleave → reopen has `hlc_hi ≥ gc_threshold`, no wedge).

## Deferred to Phase 2 (tracked, not dropped)

- **Tombstone reclamation** — once `T` advances past a LONE tombstone's ts and it is the
  SOLE remaining version of a deleted-then-quiescent key, it is provably unobservable
  (any reader `≥ T` reads absent) and safe to drop. Not implemented: the GC delete-pass
  currently KEEPS the newest `< T` version per key even when it is a tombstone (correct,
  never a corruption — a reader `≥ T` still reads absent). This is BOUNDED bloat ∝ the
  deleted-key count, not unbounded growth. Reclaiming lone below-`T` tombstones is a
  Phase-2 GC optimization.

## Still DEFERRED (later phases — outside the Phase-1 engine substrate)

- **L2 SSI validator** — the changelog `NewIndex`/`OldIndex` decode + range-validation
  (L2-owned; L1 stores opaque bytes only).
- **Non-blocking changelog fan-out** — post-ack subscriber fan-out (L4 reactivity).
- **`sky build` `-tags pebblegozstd` wiring** — add the tag to the cgo-retry path in
  `rust/crates/project/src/build.rs` `run_go_build_once` (and the static path for
  symmetry). Verified-correct tag; wiring is a build-integration change outside the
  engine package.
- **GC scheduling** — a background scheduler / write-triggered GC cadence (the `GC()`
  pass exists and is deterministically test-driven; wiring a periodic low-priority
  goroutine that doesn't starve the committer is a tuning task, not a correctness one).
- **Block-property read acceleration (§5.3)**, `Checkpoint` hot-backup, range-tombstone
  GC collapse (R-1), tombstone-full-collapse GC (§5.1 optimization — the pass currently
  KEEPS the newest `< T` version even when it is a tombstone, which is correct; fully
  vanishing such a key is a later optimization).

## Verification commands (all green at Phase-1-complete)

```
cd runtime-go
go build ./bluedb/...
CGO_ENABLED=0 go build ./bluedb/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./bluedb/...
go build ./...                 # rest of runtime-go unaffected by the Pebble dep
go vet ./bluedb/
go test ./bluedb/ -race -count=1
go test ./bluedb/ -bench . -benchmem -run '^$'   # throughput numbers (run without -race)
```
