package bluedb

// recent_changes.go — the in-RAM, commitTs-ordered window of recent KeyChanges (§4.2).
// Validation reads it instead of a Pebble iterator, so it stays off the fsync hot path.
//
// SINGLE-WRITER: the ring is mutated ONLY by the committer goroutine — append AND trim.
// GC does NOT call trim directly (that would race append, Fix-3/R-2.9); it enqueues a trim
// request on e.trimReqs and the committer drains it at the top of each drain (see
// pebbleEngine.drainTrimRequests). So append, after, and trim are all committer-goroutine-
// only → no cross-goroutine mutation, no lock on the hot path, `go test -race` clean.
type recentRing struct {
	entries []ringEntry // ascending commitTs
	floor   HLC         // the lowest commitTs the ring still guarantees to hold; a readTs
	//                     below it → after() reports spilled=true → caller falls back to
	//                     Changelog.Tail for that one txn.
}

type ringEntry struct {
	commitTs HLC
	changes  []KeyChange
}

func newRecentRing() *recentRing { return &recentRing{} }

// after returns every KeyChange with commitTs strictly greater than readTs held in the ring
// (the validation window's ring half). O(commits-since-readTs). If readTs < floor (the ring
// was trimmed/spilled out from under this reader) it returns spilled=true so the caller
// validates via Changelog.Tail(readTs) instead — correct, just off the in-RAM fast path.
func (r *recentRing) after(readTs HLC) (changes []KeyChange, spilled bool) {
	if readTs.Less(r.floor) {
		return nil, true
	}
	var out []KeyChange
	for i := range r.entries {
		if readTs.Less(r.entries[i].commitTs) { // commitTs > readTs
			out = append(out, r.entries[i].changes...)
		}
	}
	return out, false
}

// append adds a just-durable commit's changes (post Apply(Sync) success). Committer
// goroutine only. Callers pass a non-empty change list.
//
// TODO(phase2b): cap at maxRingEntries and spill oldest entries to Changelog.Tail, raising
// r.floor (Fix-8, R-2.4). In 2a the ring is unbounded-but-correct: the retention invariant
// floors it at the GC threshold T (== every live reader's readTs), so a healthy system's
// ring is bounded by reader lag; only a leaked/never-Release'd reader token could grow it
// without bound — the hard RAM cap that closes that is deferred to 2b.
func (r *recentRing) append(commitTs HLC, changes []KeyChange) {
	cp := make([]KeyChange, len(changes))
	copy(cp, changes)
	r.entries = append(r.entries, ringEntry{commitTs: commitTs, changes: cp})
}

// trim drops entries with commitTs < T and raises the floor to T. Committer goroutine only
// (invoked from drainTrimRequests). Entries at commitTs == T are KEPT (a reader at exactly T
// still resolves them; and a reader whose readTs == T needs commits strictly after T, which
// remain). Monotone: never lowers the floor.
func (r *recentRing) trim(T HLC) {
	i := 0
	for i < len(r.entries) && r.entries[i].commitTs.Less(T) {
		i++
	}
	if i > 0 {
		r.entries = r.entries[i:]
	}
	if r.floor.Less(T) {
		r.floor = T
	}
}
