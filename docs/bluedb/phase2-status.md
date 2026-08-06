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

## Phase 2b — deferred

Left as `// TODO(phase2b)` in the code, with the correct-but-unbounded 2a behaviour in place:

- **Hot-key strict-2PL lease (§6).** The committer-arbitrated FIFO lease for a genuinely-
  contended POINT key (starvation-freedom). 2a uses bounded optimistic retry + backoff for all
  contention (`Transact`, `txn.go`); range/predicate contention has no lease even in 2b.
- **Ring cap + spill-to-`Changelog.Tail` (Fix-8, §4.2 / R-2.4).** A hard `maxRingEntries` cap
  that spills the oldest ring entries and raises `r.floor` so a leaked reader token can't grow
  the ring unbounded in RAM. In 2a the ring is unbounded-but-correct — floored at the GC
  threshold `T` (== every live reader's `readTs`), so a healthy system's ring is bounded by
  reader lag. The spill *fallback* read path (`after` → `spilled` → `changelogTailChanges`) is
  already implemented and correct; only the proactive cap that triggers it is deferred.
