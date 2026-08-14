package bluedb

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
)

// changelogTailCalls counts the ring-spill fallback (a Pebble scan). A test seam: T16 asserts
// a fresh-readTs txn is served entirely by the in-RAM ring (zero Tail scans).
var changelogTailCalls atomic.Int64

// changelogTailFaultInject is a TEST SEAM (Fix-1 regression, T31): when set, changelogTailChanges
// returns errInjectedChangelogFault instead of reading Pebble, so a test can exercise the
// fail-CLOSED spill-error path (a phantom whose change spilled out of the ring must ABORT the txn,
// never commit against a nil window). atomic so the committer-goroutine read races cleanly with
// the test-goroutine set under `go test -race`.
var changelogTailFaultInject atomic.Bool

// errInjectedChangelogFault is the sentinel the Fix-1 test seam raises on the spill path.
var errInjectedChangelogFault = errors.New("bluedb: injected changelog-tail fault (test)")

// committer is the single writer goroutine (§3.2). It group-commits: grabs the
// first queued job, drains up to maxBatch more non-blocking, and processes the
// batch as ONE Pebble Batch + one Apply(Sync). Assigning commitTs here (not in the
// caller) makes it monotonic in commit order — the single serialization point the
// total order relies on (C1). Reuses the driving pattern from the retired
// hand-built engine (ref db.go:445-559 committer()/process()), with the durable
// sink swapped to Pebble.
func (e *pebbleEngine) committer() {
	defer e.wg.Done()
	for first := range e.ch {
		batch := []*commitJob{first}
	drain:
		for len(batch) < maxBatch {
			select {
			case j, ok := <-e.ch:
				if !ok {
					break drain // channel closed & drained
				}
				batch = append(batch, j)
			default:
				break drain
			}
		}
		e.process(batch)
	}
}

// process is the group-commit entry (§4.3). It drains any GC-enqueued ring-trim FIRST (Fix-3,
// on the committer goroutine — the ring stays single-writer), then PRE-SCANS the batch: if NO
// job carries a ReadSet, the whole drain window takes the pure Phase-1 blind path with ZERO
// SSI cost (no decode, no pending, no validation — Fix-6/T24). Otherwise it runs the
// validate-then-assign transactional path.
func (e *pebbleEngine) process(batch []*commitJob) {
	e.drainTrimRequests() // Fix-3: apply GC trim(T) here, before touching the ring

	anyTxn := false
	for _, j := range batch {
		if j.req.ReadSet != nil {
			anyTxn = true
			break
		}
	}
	if !anyTxn {
		e.processBlindPhase1(batch)
		return
	}
	e.processTxn(batch)
}

// processBlindPhase1 is the UNCHANGED Phase-1 blind-commit body (an all-blind drain window):
// one DISTINCT commitTs per job, all writes + changelog + metadata in ONE batch, one
// Apply(Sync), seal-on-fault, ack-after-Apply. NO validation, NO pending accumulation — the
// OLTP firehose is byte-for-byte Phase 1 (Fix-6). The one addition: after a durable Apply,
// blind commits that carry a KeyChange payload are appended to the recent-changes ring so a
// CONCURRENT open transaction (readTs below these commits) still validates against them — the
// ring invariant must hold regardless of who wrote the commit. Empty-payload blind writes
// (the OLTP firehose / the throughput benchmark) append nothing → truly zero SSI cost.
func (e *pebbleEngine) processBlindPhase1(batch []*commitJob) {
	if e.sealed.Load() {
		for _, j := range batch {
			j.done <- CommitResult{Err: ErrSealed}
		}
		return
	}

	// ── C6b: decode every payload FIRST, before a single byte enters the batch. ──
	//
	// This is the SECOND instance of N6's class, in the same file. The ring append below
	// used to sit after Apply and read `derr == nil && len(chg) > 0`, so a blind commit
	// whose payload would not decode was written durably and acked, while its changes
	// never entered the recent-changes ring. A CONCURRENT open transaction — one whose
	// readTs is below this commit, in a different drain window — validates against
	// ring.after(readTs), so it validates against a window missing a committed change:
	// under-rejection, exactly as in decodePayload's `pending` case. The ring append is
	// not an optimisation here; the doc above says why it is a correctness obligation.
	//
	// Decoding up front makes the only available remedy reachable: abort the job before
	// anything is written. After Apply the write is durable and there is nothing to undo.
	// It costs the firehose nothing — an empty payload returns immediately, exactly as
	// the post-Apply `len(...) == 0` check did — and it removes the second decode.
	live := make([]*commitJob, 0, len(batch))
	changes := make([][]KeyChange, 0, len(batch))
	for _, j := range batch {
		chg, derr := decodePayload(j.req.ChangelogPayload)
		if derr != nil {
			j.done <- CommitResult{Err: derr}
			continue
		}
		live = append(live, j)
		changes = append(changes, chg)
	}
	if len(live) == 0 {
		return // every job rejected, each already acked — nothing to apply, no metadata
	}
	// From here on `batch` means the surviving jobs, INCLUDING inside the deferred
	// recover below (it closes over the variable, and this assignment precedes it), so a
	// rejected job cannot be acked twice.
	batch = live

	// SEAL on a synchronous durability panic (see the Phase-1 contract). Acks happen only
	// AFTER Apply, so on a panic no job has been acked and this recover acks each exactly once.
	//
	// STILL LOAD-BEARING AFTER N3, which is easy to get wrong. N3 made Logger.Fatalf latch
	// instead of panic, so this recover no longer catches THAT. But pebble also raises a
	// RAW panic on a WAL WriteRecord failure (db.go:955, `panic(err)` — no logger
	// involved), synchronously on this goroutine inside Apply. Deleting this recover
	// because "Fatalf doesn't panic any more" would turn that class into a process kill,
	// which is the same defect N3 exists to close, one site over.
	acked := false
	defer func() {
		if r := recover(); r != nil && !acked {
			e.sealed.Store(true)
			fmt.Fprintf(os.Stderr, "bluedb: durability fault — sealing engine: %v\n", r)
			err := fmt.Errorf("%w: durability panic: %v", ErrSealed, r)
			for _, j := range batch {
				j.done <- CommitResult{Err: err}
			}
		}
	}()

	b := e.db.NewBatch()
	defer b.Close()

	jobTs := make([]HLC, len(batch))
	var hasWrites bool
	var lastCommitTs HLC
	for i, j := range batch {
		commitTs := e.hlc.next() // §3.3 — strictly monotonic, floored across restart
		jobTs[i] = commitTs
		lastCommitTs = commitTs
		if len(j.req.Writes) > 0 {
			hasWrites = true
		}
		e.writeJob(b, j, commitTs)
	}

	// Commit metadata IN THE SAME batch (§3.4) at the LAST (highest) job's commitTs.
	hlcBytes := encodeHLC(lastCommitTs)
	_ = b.Set(encodeMetaKey(metaHLCHi), hlcBytes, nil)
	_ = b.Set(encodeMetaKey(metaChangelogCursor), hlcBytes, nil)

	if err := enforceLogicalBatchInvariant(hasWrites, true); err != nil {
		e.sealed.Store(true)
		acked = true
		for _, j := range batch {
			j.done <- CommitResult{Err: err}
		}
		return
	}

	err := e.db.Apply(b, pebble.Sync) // ONE fsync amortized over the whole group
	err = e.foldFatal(err)            // N3 consumption point 3/5 — BEFORE the branch below
	if err != nil {
		e.sealed.Store(true)
	} else {
		e.advanceDurableHi(lastCommitTs)
		// Ring append for concurrent-open-txn correctness (see doc above), using the
		// changes decoded BEFORE the batch was built (C6b) — every surviving job's payload
		// is known to decode, so there is no error arm left here to swallow. Empty-payload
		// writes carry no changes → append nothing. The Phase-4 change-feed (§4.1) emits
		// the SAME list, strictly AFTER advanceDurableHi (durable-before-notify, §7),
		// carrying the job's transient tenant tag.
		feed := e.hasChangeSubs()
		for i, j := range batch {
			if len(changes[i]) == 0 {
				continue
			}
			e.recent.append(jobTs[i], changes[i])
			if feed {
				e.emitChangeBatch(ChangeBatch{CommitTs: jobTs[i], Tenant: j.req.Tenant, Changes: changes[i]})
			}
		}
	}

	acked = true
	for i, j := range batch {
		j.done <- CommitResult{CommitTs: jobTs[i], Err: err} // ACK ONLY AFTER Apply returns
	}
}

// appliedJob records a job that passed validation and was written into the batch (to
// ring-append + ack post-Apply).
type appliedJob struct {
	job      *commitJob
	commitTs HLC
	changes  []KeyChange
}

// processTxn is the validate-then-assign path for a drain window containing >= 1 txn (§4.3).
// Per job: a blind job (ReadSet==nil) commits directly (its payload joins `pending` so later
// txns in this batch validate against it); a txn job validates its read-set against
// window = ring.after(readTs) ++ pending — clean → assign commitTs, write, add to pending;
// conflict → ack ErrConflict INLINE (assign no commitTs) and record in `acked` (Fix-7). Then
// ONE Apply(Sync); on success append validated changes to the ring + advance durableHi.
//
// The four grill fixes are all here: Fix-5 (seal on Apply error), Fix-6 (the all-blind
// pre-scan is in process(), routing away from this path), Fix-7 (the acked set excludes
// inline-aborted jobs from the recover/seal loops), and Fix-3 (trim drained in process()).
func (e *pebbleEngine) processTxn(batch []*commitJob) {
	if e.sealed.Load() {
		for _, j := range batch {
			j.done <- CommitResult{Err: ErrSealed}
		}
		return
	}

	acked := make(map[*commitJob]bool) // Fix-7: jobs already acked inline (aborts + seals)
	// SEAL on a synchronous durability panic. Validation runs before Apply, purely in RAM,
	// so it cannot fault; the only panic source is Apply. On a panic no APPLIED job has been
	// acked yet — the recover acks them ErrSealed, and SKIPS inline-acked jobs (Fix-7), so
	// every j.done receives exactly one result.
	//
	// Kept after N3 for the same reason as processBlindPhase1's: pebble's raw
	// `panic(err)` on a WAL WriteRecord failure (db.go:955) has no logger in its path and
	// is therefore untouched by the Fatalf latch.
	defer func() {
		if r := recover(); r != nil {
			e.sealed.Store(true)
			fmt.Fprintf(os.Stderr, "bluedb: durability fault — sealing engine: %v\n", r)
			err := fmt.Errorf("%w: durability panic: %v", ErrSealed, r)
			for _, j := range batch {
				if acked[j] {
					continue
				}
				j.done <- CommitResult{Err: err}
			}
		}
	}()

	b := e.db.NewBatch()
	defer b.Close()

	var pending []KeyChange // decoded changes of clean/blind jobs SO FAR this batch
	var applied []appliedJob
	var hasWrites bool
	var maxApplied HLC

	for _, j := range batch {
		if j.req.ReadSet == nil {
			// ── BLIND-WRITE FAST PATH within a mixed batch (§5.4). No validation; but a
			// later txn in THIS batch must validate against it → decode into pending. ──
			// N6: decode BEFORE assigning a commitTs or writing anything. An undecodable
			// payload aborts this job rather than contributing an invisible hole to
			// `pending` — see decodePayload.
			chg, derr := decodePayload(j.req.ChangelogPayload)
			if derr != nil {
				j.done <- CommitResult{Err: derr} // NOT ErrConflict: a retry decodes identically
				acked[j] = true                   // Fix-7: record the inline ack
				continue
			}
			commitTs := e.hlc.next()
			e.writeJob(b, j, commitTs)
			if len(j.req.Writes) > 0 {
				hasWrites = true
			}
			pending = append(pending, chg...)
			applied = append(applied, appliedJob{job: j, commitTs: commitTs, changes: chg})
			if maxApplied.Less(commitTs) {
				maxApplied = commitTs
			}
			continue
		}

		// ── TRANSACTIONAL JOB — validate against (readTs, now] = ring.after(readTs) ++ pending ──
		base, spilled := e.recent.after(j.req.ReadTs)
		if spilled {
			// Fix-8 spill fallback: readTs fell below the ring floor → validate via the durable
			// changelog (correct, off the in-RAM fast path). (In 2a the ring is uncapped, so
			// this is only reached if GC trimmed past a lagging readTs — still correct.)
			//
			// Fix-1 (fail CLOSED): if that changelog read ERRORS — a Pebble iterator error OR a
			// malformed/truncated changelog entry surfaced by DecodeChangelogPayload — we CANNOT
			// compute the validation window. We MUST NOT fall through with base == nil: that would
			// validate the txn against `pending` ONLY, dropping the spilled committed changes in
			// (readTs, floor] and UNDER-REJECTING a phantom whose conflicting change spilled out of
			// the ring (a serializability break — violates validate.go's NEVER-under-reject
			// contract). Instead ABORT this job (assign NO commitTs). The driver re-Begins at a
			// fresher readTs (>= durableHi >= ring floor → no spill on retry; a transient I/O error
			// also clears), so this fails closed WITHOUT starving the txn.
			tail, terr := changelogTailChanges(e.db, j.req.ReadTs)
			if terr != nil {
				j.done <- CommitResult{Err: ErrConflict} // fail closed — never commit a non-serializable history
				acked[j] = true                          // Fix-7: record the inline ack
				continue
			}
			base = tail
		}
		window := make([]KeyChange, 0, len(base)+len(pending))
		window = append(window, base...)
		window = append(window, pending...)

		if conflict, culprit, pointConflict := validate(j.req.ReadSet, window); conflict {
			j.done <- CommitResult{Err: ErrConflict} // abort THIS job; assign NO commitTs
			acked[j] = true                          // Fix-7: record the inline ack
			// §6.2 hot-key detection: only a POINT-read conflict is leaseable — a
			// range/predicate/witness conflict has no single key to enqueue on (§6.4).
			if pointConflict {
				e.hotKeys.recordAbort(culprit)
			}
			continue
		}

		// N6, same as the blind arm: decode before the ts is assigned and before anything
		// is written, so an undecodable payload aborts instead of holing `pending`.
		chg, derr := decodePayload(j.req.ChangelogPayload)
		if derr != nil {
			j.done <- CommitResult{Err: derr}
			acked[j] = true
			continue
		}
		commitTs := e.hlc.next() // validate-then-assign (no burned ts)
		e.writeJob(b, j, commitTs)
		if len(j.req.Writes) > 0 {
			hasWrites = true
		}
		pending = append(pending, chg...)
		applied = append(applied, appliedJob{job: j, commitTs: commitTs, changes: chg})
		if maxApplied.Less(commitTs) {
			maxApplied = commitTs
		}
	}

	if len(applied) == 0 {
		return // every job aborted (already acked inline) — nothing to apply, no metadata
	}

	// Metadata at the HIGHEST APPLIED commitTs (§4.3). Apply is all-or-nothing so metadata
	// can never diverge from the data it describes.
	hlcBytes := encodeHLC(maxApplied)
	_ = b.Set(encodeMetaKey(metaHLCHi), hlcBytes, nil)
	_ = b.Set(encodeMetaKey(metaChangelogCursor), hlcBytes, nil)

	if err := enforceLogicalBatchInvariant(hasWrites, true); err != nil {
		e.sealed.Store(true)
		for _, a := range applied {
			a.job.done <- CommitResult{Err: err}
			acked[a.job] = true
		}
		return
	}

	err := e.db.Apply(b, pebble.Sync) // ONE fsync amortized over the group
	err = e.foldFatal(err)            // N3 consumption point 4/5 — BEFORE the branch below
	if err != nil {
		e.sealed.Store(true) // Fix-5: seal on the durability fault (Phase-1 fail-loud)
	} else {
		e.advanceDurableHi(maxApplied)
		feed := e.hasChangeSubs()
		for _, a := range applied { // ring commit AFTER durability
			if len(a.changes) > 0 {
				e.recent.append(a.commitTs, a.changes)
				// Phase-4 change-feed emit (§4.1), strictly AFTER advanceDurableHi
				// (durable-before-notify, §7), reusing a.changes + the job's tenant tag.
				if feed {
					e.emitChangeBatch(ChangeBatch{CommitTs: a.commitTs, Tenant: a.job.req.Tenant, Changes: a.changes})
				}
			}
		}
	}
	for _, a := range applied {
		a.job.done <- CommitResult{CommitTs: a.commitTs, Err: err} // ack after Apply
		acked[a.job] = true
	}
}

// foldFatal folds a latched pebble Logger.Fatalf into an Apply's error result (defect N3).
//
// WHERE IT MUST BE CALLED. Immediately after Apply and BEFORE the
// `if err != nil {seal} else {advanceDurableHi; ring append; emit}` branch. Both halves
// matter:
//
//   - BEFORE, because a fatal that arrives after the branch has already advanced
//     durableHi and fired the change feed for a write that is not durable. Readers would
//     then be handed a readTs naming it and subscribers notified of it.
//   - AFTER Apply rather than instead of it, because Apply's own error is the common
//     case and the two are independent: db.go:885 calls Fatalf and then FALLS THROUGH to
//     `return nil`, so a WAL commit fault reaches us as (err == nil, latch set) — the
//     exact shape that used to require Fatalf to panic.
//
// errors.Join keeps both when both fire; it returns nil when neither does, so the
// no-fault path (every commit, always) allocates nothing and the branch below is
// unchanged.
//
// The latch is NOT cleared by this read, so every later batch sees it too. That is
// deliberate: a pebble fatal is unrollbackable, the engine is sealed on the first
// consumption anyway, and a clear-on-read latch would let a second fatal vanish.
func (e *pebbleEngine) foldFatal(err error) error {
	msg, ok := e.fatal.takeFatal()
	if !ok {
		return err
	}
	return errors.Join(err, fmt.Errorf("%w: %s", ErrPebbleFatal, msg))
}

// writeJob encodes one job's data versions + its changelog entry into the batch at
// commitTs. Shared by the blind and transactional paths (identical Phase-1 encoding).
//
// C6b classification — the discarded `b.Set` errors are FAIL-OPEN BY DESIGN, and here is
// the argument rather than the assumption. pebble's Batch.Set can return non-nil on
// EXACTLY one condition: `b.index != nil && b.index.Add(...) != nil` (batch.go). e.db is
// always driven with db.NewBatch(), which builds a NON-indexed batch (index == nil), so
// Set is unconditionally nil here. Checking it would add a branch per write to the
// firehose's innermost loop to test something that cannot happen.
//
// THE CONDITION UNDER WHICH THIS STOPS BEING TRUE, stated so a future change cannot
// silently invalidate it: switching any of these batches to db.NewIndexedBatch(). If that
// ever happens, a failed Set silently omits a write from a batch that then applies and
// acks as durable — the fail-open this note exists to make impossible-to-miss. The same
// argument covers the two metadata Sets in processBlindPhase1/processTxn.
func (e *pebbleEngine) writeJob(b *pebble.Batch, j *commitJob, commitTs HLC) {
	for _, w := range j.req.Writes {
		k := encodeDataKey(w.UserKey, commitTs)
		if w.Op == OpDelete {
			_ = b.Set(k, []byte{markerTombstone}, nil) // versioned delete marker
		} else {
			v := make([]byte, 0, 1+len(w.Value))
			v = append(v, markerPut)
			v = append(v, w.Value...)
			_ = b.Set(k, v, nil)
		}
	}
	if len(j.req.ChangelogPayload) > 0 {
		// Keyed by this job's OWN commitTs → distinct changelog entries, none lost.
		_ = b.Set(encodeChangelogKey(commitTs), j.req.ChangelogPayload, nil)
	}
}

// decodePayload decodes a changelog payload to its KeyChange list. An EMPTY payload is
// legitimately no changes; a payload that does not DECODE is an error (defect N6).
//
// It used to return nil for both, and its docstring argued that was safe: "a malformed
// payload validates as 'no changes' for that job, never a false accept of a later txn
// against garbage". THAT REASONING IS INVERTED for the `pending` path. pending is the
// intra-batch half of the validation window: the changes of jobs already written into
// THIS drain's batch, against which every later txn in the same window is validated. A
// job whose payload silently contributes nothing to pending is a job whose committed
// changes a later txn never sees — so that txn commits against a window missing them.
// That is UNDER-rejection, i.e. a serializability break, and it is the identical shape
// this package refuses `continue` for one file over in changelog.go's Tail.
//
// "Never a false accept against garbage" was answering the wrong question. Validating
// against garbage would over-reject, which is safe; validating against a HOLE
// under-rejects, which is not. The two are not symmetric and the comment treated them
// as if they were.
//
// The caller's remedy is to ABORT the job — assign no commitTs, write nothing. Then the
// window has nothing to miss, because the job never committed. Aborting is also the only
// remedy available before the batch is applied, which is where both call sites now sit.
func decodePayload(payload []byte) ([]KeyChange, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	return DecodeChangelogPayload(payload)
}

// changelogTailChanges decodes the durable Changelog.Tail(after) into a flat KeyChange list —
// the ring-spill fallback (Fix-8, §4.2) and available for cold-start rebuild.
func changelogTailChanges(db *pebble.DB, after HLC) ([]KeyChange, error) {
	changelogTailCalls.Add(1)
	if changelogTailFaultInject.Load() {
		return nil, errInjectedChangelogFault // Fix-1 test seam: force the fail-CLOSED spill-error path
	}
	cl := &changelog{db: db}
	entries, err := cl.Tail(after)
	if err != nil {
		return nil, err
	}
	var out []KeyChange
	for _, entry := range entries {
		chg, derr := DecodeChangelogPayload(entry.Payload)
		if derr != nil {
			return nil, derr
		}
		out = append(out, chg...)
	}
	return out, nil
}

// enforceLogicalBatchInvariant returns ErrMissingCommitMetadata iff a logical-commit
// batch (one that writes logical versions, i.e. hasWrites) reached Apply without its
// hlc_hi metadata (§3.4). GC batches are an EXEMPT class (physical-only, no commitTs,
// no hlc_hi) and never flow through this path.
func enforceLogicalBatchInvariant(hasWrites, hasHLCHi bool) error {
	if hasWrites && !hasHLCHi {
		return ErrMissingCommitMetadata
	}
	return nil
}
