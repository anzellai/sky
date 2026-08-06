# BlueDB Phase 2 — status

> Branch `feat/bluedb`. Package `runtime-go/bluedb/`. Authoritative design:
> `docs/bluedb/phase2-txn-design.md` (grilled). Builds on the Judge-verified Phase-1 engine
> (`docs/bluedb/phase1-status.md`).

## Phase 2a — the SSI (serializable) transaction CORE — DONE

The correctness core of the MVCC serializable transaction is implemented and verified. It
extends the Phase-1 engine **only at the Go interface level** — the irreversible on-disk
format (`keys.go` / `comparer.go`, `skydb.mvcc.v1`) and the `CommitReq`/`Changelog` storage
seam are untouched; Phase 1 pre-stubbed every hook (`CommitReq.ReadTs` + `ReadSet` with
`nil ⇒ blind`, per-job `commitTs`, opaque `ChangelogPayload`).

Shipped (all §-refs to `phase2-txn-design.md`):

1. **`Txn` API + read-set** (`txn.go`) — `Begin`/`Get`/`Scan`/`ScanRange`/`ScanFallback`/
   `WitnessCollection`/`Put`/`Delete`/`Commit`/`Abort` + the optimistic `Transact` loop. The
   read-set has three halves: point reads (§2.1), index ranges (§2.2 — the SSI crux), and the
   conservative collection/index-level fallback witnesses (§2.2). Read-your-writes overlay +
   pre-image/OldIndex derivation (§1.4).
2. **ONE canonical `encodeIndexKey`** (`index_key.go`) — the SOLE producer of index-coordinate
   bytes, used by BOTH the scan-bound builder (`encodeScanRange`) AND the coord emission
   (`tx.indexer`). int (sign-biased BE8) / text (UTF-8) / bool (1B); composite = concat;
   descending = invert bytes **and** the scan swaps lo/hi (all inside the one encoder path).
   Fallback (`real`/`money`/`blob`/IS-NULL) → conservative witness, never a byte-range test.
3. **`KeyChange`/`IndexCoord` codec** (`keychange.go`) — L2 encodes the change list into the
   opaque `ChangelogPayload`; the validator / ring-rebuild / spill-fallback decode it. Both
   `NewIndex` (put) and `OldIndex` (update/delete pre-image) carried.
4. **Validate-then-assign committer** (`committer.go`) + the in-RAM recent-changes ring
   (`recent_changes.go`) — validation runs INSIDE the single committer (the serialization
   point), against `window = ring.after(readTs) ++ pending`. Aborted jobs consume no
   `commitTs`; the ring commits only after a durable `Apply(Sync)`.
5. **`readTs = durableHi`, atomically pinned** (`beginSnapshot`, `pebble_engine.go`) — the
   begin-snapshot path reads `durableHi`, pins the Pebble snapshot, and `RegisterAt`s the
   token, all under `durMu` so GC can't advance `T` past `readTs` between pin and register
   (R-2.8).
6. **Bounded retry → typed `ErrConflict`** (`Transact`) + purity (effect-free `Txn` verbs) +
   the blind-write fast path (`ReadSet == nil` → zero validation).
7. **The 4 grill fixes:** seal-on-Apply-error (Fix-5), all-blind batch pays zero SSI via the
   pre-scan (Fix-6), the `acked` set so inline-aborted jobs aren't double-acked by the recover
   defer (Fix-7), and `trim` marshalled onto the committer via `trimReqs` so the ring is
   single-writer and `-race` clean (Fix-3/R-2.9).
8. **The serializability conformance suite** (`serializable_test.go`, `index_key_test.go`) —
   predicate phantom / phantom-disappears / descending phantom / conservative fallback /
   point write-skew / lost-update / read-your-writes / window-boundary / blind fast path /
   retry→ErrConflict / intra-batch / GC-race, all under `go test -race`.

### Verification (all green)

```
go build ./bluedb/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./bluedb/...
go vet ./bluedb/
go test ./bluedb/ -race -count=1        # 35 Phase-1 + 19 Phase-2a tests, -race clean
go test ./bluedb/ -bench GroupCommit -run '^$'   # ~49k durable blind writes/s (unchanged)
```

## Phase 2b — the contention hardening — DONE → **Phase 2 complete**

The two deferred contention guards are implemented + verified. Phase 2 (SSI core + contention
hardening) is now complete.

### 1. Hot-key strict-2PL lease (§6, the GRILL-REWORKED deadlock-free version)

- **Detection** (`hotkey.go` — `hotKeyTable`). The committer calls `recordAbort(culprit)` in
  `processTxn` (`committer.go`) when `validate()` reports a **POINT** conflict — gated on
  `validate`'s new `pointConflict` return, so a range/predicate/witness conflict (whose culprit
  is the changed row, not a key the victim point-read) is NEVER recorded (§6.4). A key crossing
  `hotThreshold` recent aborts is promoted **hot**; it stays hot while its lease queue is
  contended (stickiness → no cool-mid-contention oscillation), and the reaper's `decay()`
  retires it once contention ends.
- **Strict-2PL discovery** (`txn.go` — `transactUnderLeases`). NO single-key acquire-on-
  discovery (the shape the grill proved deadlock-prone). Instead: **Phase A** runs the pure body
  once holding no lease to DISCOVER the full touched-key set, then aborts; **Phase B** acquires
  ALL hot-key leases in ascending `bytes.Compare` order (one global lock order → no
  hold-and-wait cycle → deadlock-free); **Phase C** re-runs the pure body under the held set. A
  lease-holder is the sole active writer of every hot key it holds → its commit cannot lose the
  point-key race. `Transact` switches to this path when `anyHot(touched)` — checked BOTH before
  the optimistic commit and after an `ErrConflict`.
- **Range/predicate contention has NO lease** (honest, §6.4) — you cannot enqueue on a
  predicate. It stays on bounded optimistic retry + backoff → typed `ErrConflict` after the
  bound. Starvation-freedom is claimed for POINT-key contention only.
- **FIFO queue + release** (`hotkey.go` — `leaseManager`). One FIFO queue per hot key → waiters
  served in arrival order (no starvation among holders). Release is **driver-side** (`defer
  releaseAll` in `transactUnderLeases`); a **dedicated lease-reaper goroutine** (`go e.leaseReaper()`
  → `leaseManager.reap`, timeout `defaultLeaseTimeout`; NOT the committer) is the backstop that reclaims a lease held
  past the timeout so a driver that crashes between `Commit`-return and its defer cannot wedge
  the queue forever.
- **Purity under re-run.** Phases A and C both re-run the pure `func(*Txn) error` body; the
  `Txn` verbs are effect-free (buffer/record only until Commit), so a body built from them is
  automatically re-runnable (§5.3). An impure body is a caller error — the API exposes only txn
  ops.

### 2. Ring cap + proactive spill (Fix-8, §4.2 / R-2.4)

- `recentRing` gains `maxEntries` (`recent_changes.go`), defaulting to
  `defaultMaxRingEntries = 100_000` entries (config-overridable). When an `append` would exceed
  the cap, `spillOldest` drops the oldest entries (niling their change slices for immediate GC)
  and raises `floor` to the commitTs of the new oldest retained entry — so a lagging reader
  whose `readTs < floor` takes `after()`'s `spilled=true` branch → validation falls back to
  `Changelog.Tail(readTs)` (already wired in 2a). RAM is now bounded **unconditionally**,
  independent of reader liveness (a leaked/never-`Release`d reader can no longer grow the ring;
  it pays a Pebble read instead).
- **Correctness preserved.** A spilled validation reads the missing `(readTs, ringFloor]` range
  from the durable changelog and sees **exactly** the same `KeyChange`s the ring holds — the cap
  introduces no under-reject (proven by `TestT29`, which rejects a phantom identically via the
  ring and via the spill).

### Contention conformance (`contention_test.go`, all `-race`)

| Test | Proves |
|---|---|
| `TestT25_HotKeyNoStarvation` | 16×25 RMW on ONE counter → all commit (0 conflicts), final == 400 (no lost update), lease path engaged. |
| `TestT26_MultiHotKeyNoDeadlock` | txns touching {X,Y} and {Y,X}, both hot → all commit, no deadlock, exact counts (the grill's X<Y case). |
| `TestT27_RangeContentionBoundedRetryNoLease` | a range conflict never promotes a key hot; a victim under a live flood degrades to typed `ErrConflict` with `leasePathCalls == 0` (no predicate lease, no hang). |
| `TestT28_LeaseTimeoutBackstop` | a crashed driver holding a lease → the lease-reaper goroutine reclaims it → the waiter proceeds. |
| `TestT29_RingCapSpillIdenticalValidation` | a capped ring spills a phantom below a lagging reader's readTs → still rejected via `Changelog.Tail`, identical to the uncapped ring path. |
| `TestT30_RingCapSpillRaisesFloor` | ring unit: append past the cap spills the oldest + raises the floor. |

### Verification (all green)

```
go build ./bluedb/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./bluedb/...
go vet ./bluedb/
go build ./...
go test ./bluedb/ -race -count=1        # 54 Phase-1/2a + 6 Phase-2b, -race clean
go test ./bluedb/ -race -count=5 -run 'TestT25|TestT26|TestT27|TestT28|TestT29|TestT30'  # no flakes/deadlocks
go test ./bluedb/ -bench GroupCommit -run '^$'   # ~49k durable blind writes/s (unchanged — the blind path is untouched)
```

## Phase 2 boundary grill — 5 fixes (SSI + lease layer, architecture SOUND)

Two fresh-context adversaries read the actual SSI + lease code. The architecture is sound (no
lost update, no deadlock, no race, no rework). They found ONE correctness blocker (an
under-reject), one latent guard, two liveness-honesty gaps, and comment drift. All five closed.

### Fix-1 (CORRECTNESS BLOCKER) — the spill fallback now fails CLOSED

`processTxn` (`committer.go`) previously fell through with `base == nil` when the ring-spill
`changelogTailChanges` read ERRORED (a Pebble iterator error OR a malformed/truncated changelog
entry via `DecodeChangelogPayload`). A `nil` window drops the spilled committed changes in
`(readTs, floor]` → a phantom whose conflicting change spilled out of the ring is MISSED → the txn
commits a **non-serializable** history (an under-reject — the one failure `validate.go` forbids).
Now the spill-read error path ABORTS the job (`ErrConflict` + inline ack) — fail CLOSED. The
driver re-`Begin`s at a fresher `readTs` (≥ `durableHi` ≥ ring floor → no spill on retry; a
transient I/O error also clears). No under-reject remains on ANY path, including the spill-error
branch. Regression: `TestT31_SpillChangelogErrorFailsClosed` fault-injects the tail error on the
spill path (seam: `changelogTailFaultInject`) with a spilled phantom and asserts the txn ABORTS
(negative-checked: with the old fall-through, T31 commits `<nil>` — the under-reject). T29 still
covers the fallback SUCCESS path.

### Fix-2 (LATENT — guard before Phase 3) — composite index column-order guard

`encodeCompositeKey` (`index_key.go`) concatenates encoded columns with NO separator/length prefix
— order-preserving ONLY when every non-suffix column is fixed-width (int BE8 / bool 1B); a
variable-width (text/blob/real/money) column in a non-suffix position silently under-rejects.
`checkCompositeLayout` now REJECTS such a layout at construction and `encodeCompositeKey` PANICS
(fail loud) — so a bad Phase-3 schema-driven composite can never silently mis-validate at runtime.
Regression: `TestFix2_CompositeLayoutGuard` (accepts `(int,text)`/`(bool,int,text)`/single-var;
rejects `(text,int)`/`(text,text)`/`(blob,bool)`/text-in-the-middle).

### Fix-3 (LIVENESS) — a blind write to a HOT key routes through the lease

A blind `Commit` (`ReadSet == nil`) skipped validation AND the lease, so a blind-write firehose on
a hot key could starve a lease-holding RMW to `ErrConflict`. `e.Commit` (`pebble_engine.go`) now
routes a blind write whose target is currently hot through the SAME FIFO lease (`blindHotLeases`,
canonical `bytes.Compare` order for multi-key), so it queues BEHIND the RMW → the RMW makes
progress. The overwhelming common case (no hot key) is a **lock-free** atomic gate
(`hotKeyTable.hotN == 0 && leaseManager.waiterN == 0`) → the OLTP firehose pays a single pair of
atomic loads and takes NO lock. Throughput unchanged: `BenchmarkGroupCommit` ≈ 53k durable
blind writes/s. The false `txn.go` `maxLeaseAttempts` comment ("a point-contended txn never
returns ErrConflict") is corrected to the actual guarantee. Regression:
`TestT32_BlindFirehoseDoesNotStarveRMW` (4 blind flooders on a hot key + a 20-iteration RMW → RMW
commits, lease path engaged).

### Fix-4 (LIVENESS) — data-dependent touched-key set re-discovery

`transactUnderLeases` (`txn.go`) discovered the hot-key set at Phase A but a data-dependent Phase-C
body could touch a DIFFERENT hot key `M` (never leased) → race → livelock. Phase C now checks (via
`unheldHot`) whether the run touched any hot key OUTSIDE the held lease set; if so it ABORTS,
expands the set, and re-acquires the WHOLE set in canonical order (release-all-then-reacquire keeps
deadlock-freedom) — bounded by `maxLeaseRediscover` (honest typed `ErrConflict` if the touched set
never stabilizes). Regression: `TestT33_DataDependentTouchedSetConverges` (bodies RMW A or B by a
flag another txn flips → converge, no livelock, no non-conflict error, `-race` clean at `-count=5`).

### Fix-5 (honesty) — comment corrections

The "committer-side reaper" comments (`hotkey.go`, `txn.go`, `contention_test.go`) are corrected:
`leaseManager.reap` runs on its OWN dedicated lease-reaper goroutine (`pebble_engine.go`
`go e.leaseReaper()`), NOT the committer. The stale-empty-queue that `reap()` leaves on a
crashed-driver reclaim is noted as harmless (bounded, inert; `waiterN` stays accurate).

### Verification (all green)

```
go build ./bluedb/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./bluedb/...
go vet ./bluedb/
go build ./...
go test ./bluedb/ -race -count=1                              # 60 prior + 4 new (T31/T32/T33/Fix2), -race clean
go test ./bluedb/ -run 'TestT2[5-9]|TestT3[0-3]|TestFix2' -race -count=5   # no flake/deadlock/livelock
go test ./bluedb/ -bench GroupCommit -run '^$'               # ~53k durable blind writes/s (blind non-hot pays zero)
```
