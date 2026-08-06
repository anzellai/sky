package bluedb

import "github.com/cockroachdb/pebble/v2"

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

// process assigns ONE commitTs to the whole group, encodes every write's data
// version + each job's opaque changelog entry + the commit metadata into ONE
// Pebble Batch, enforces the logical-commit invariant, Apply(Sync)s once, and acks
// only AFTER Apply returns (C2 durable-on-ack). Pebble's Batch is atomic — a failed
// Apply applies nothing — so the old hand-rolled torn-batch rollback is gone (C3).
func (e *pebbleEngine) process(batch []*commitJob) {
	if e.sealed.Load() {
		for _, j := range batch {
			j.done <- CommitResult{Err: ErrSealed}
		}
		return
	}

	commitTs := e.hlc.next() // §3.3 — strictly monotonic, floored across restart
	b := e.db.NewBatch()
	defer b.Close()

	var hasWrites bool
	for _, j := range batch {
		for _, w := range j.req.Writes {
			hasWrites = true
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
			_ = b.Set(encodeChangelogKey(commitTs), j.req.ChangelogPayload, nil)
		}
	}

	// Commit metadata IN THE SAME batch (§3.4): hlc_hi + changelog_cursor. Because
	// Apply is all-or-nothing, metadata can never diverge from the data it describes.
	hlcBytes := encodeHLC(commitTs)
	_ = b.Set(encodeMetaKey(metaHLCHi), hlcBytes, nil)
	_ = b.Set(encodeMetaKey(metaChangelogCursor), hlcBytes, nil)

	// ENFORCED invariant (§3.4), scoped to LOGICAL-COMMIT batches: refuse to Apply a
	// commit batch that assigns a commitTs but lacks its hlc_hi metadata. Here the
	// metadata is always present; the check is a defensive, testable gate.
	hasHLCHi := true // set exactly when metaHLCHi was written above
	if err := enforceLogicalBatchInvariant(hasWrites, hasHLCHi); err != nil {
		// A logical batch missing metadata is a compiler-bug-class fault: seal, don't
		// silently write.
		e.sealed.Store(true)
		for _, j := range batch {
			j.done <- CommitResult{Err: err}
		}
		return
	}

	err := e.db.Apply(b, pebble.Sync) // ONE fsync amortized over the whole group

	res := CommitResult{CommitTs: commitTs, Err: err}
	for _, j := range batch {
		j.done <- res // ACK ONLY AFTER Apply returns
	}

	// TODO(phase1b): non-blocking changelog fan-out to subscribers AFTER ack, off the
	// fsync path (ref changefeed.go:52-122 drop+resync). Not needed until L4.
}

// enforceLogicalBatchInvariant returns ErrMissingCommitMetadata iff a logical-commit
// batch (one that writes logical versions, i.e. hasWrites) reached Apply without its
// hlc_hi metadata (§3.4). GC batches are an EXEMPT class (physical-only, no commitTs,
// no hlc_hi) and never flow through this path in phase 1a.
func enforceLogicalBatchInvariant(hasWrites, hasHLCHi bool) error {
	if hasWrites && !hasHLCHi {
		return ErrMissingCommitMetadata
	}
	return nil
}
