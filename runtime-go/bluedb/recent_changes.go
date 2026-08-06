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

	// maxEntries is the HARD RAM cap (Fix-8, §4.2/R-2.4): a config bound on the entry count
	// the in-RAM ring will hold. When an append would exceed it, the ring PROACTIVELY spills
	// its oldest entries and raises `floor` above them, so a leaked / never-Release'd reader
	// token (which pins the GC threshold T low and would otherwise grow the ring UNBOUNDED,
	// strictly worse than the Phase-1 disk-retention bloat) instead pays a per-validation
	// Changelog.Tail (Pebble) read for its stale readTs. RAM is bounded UNCONDITIONALLY,
	// independent of reader liveness; correctness is preserved because the durable changelog
	// holds every spilled change (the spill validation sees EXACTLY the same window). 0 ⇒ no
	// cap (unbounded-but-correct, the pre-2b behaviour — floored only at the GC threshold T).
	maxEntries int
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
// Fix-8 (§4.2/R-2.4): when maxEntries is set and appending would exceed it, the oldest
// entries are SPILLED (dropped from RAM) and `floor` is raised to the commitTs of the new
// oldest retained entry. A later txn whose readTs < floor then takes after()'s spilled=true
// branch → validation falls back to Changelog.Tail(readTs) for that one txn (a Pebble scan —
// correct, off the in-RAM fast path). This bounds the ring's RAM UNCONDITIONALLY (a
// leaked/lagging reader can no longer grow it), at the cost of a Pebble read for a validation
// that reaches below the retained window.
func (r *recentRing) append(commitTs HLC, changes []KeyChange) {
	cp := make([]KeyChange, len(changes))
	copy(cp, changes)
	r.entries = append(r.entries, ringEntry{commitTs: commitTs, changes: cp})
	if r.maxEntries > 0 && len(r.entries) > r.maxEntries {
		r.spillOldest(len(r.entries) - r.maxEntries)
	}
}

// spillOldest drops the oldest `n` entries and raises `floor` to the commitTs of the new oldest
// retained entry, so a readTs below it correctly reports spilled (Fix-8). Committer goroutine
// only. The dropped entries' change slices are niled before the reslice so they are GC'd
// immediately (the reslice keeps the backing array, which Go compacts on the next grow — RAM
// stays O(maxEntries)). floor is monotone (never lowered).
func (r *recentRing) spillOldest(n int) {
	if n <= 0 || n >= len(r.entries) {
		return
	}
	newFloor := r.entries[n].commitTs // the commitTs of the new oldest retained entry
	for i := 0; i < n; i++ {
		r.entries[i].changes = nil // release the dropped change lists for GC
	}
	r.entries = r.entries[n:]
	if r.floor.Less(newFloor) {
		r.floor = newFloor
	}
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
