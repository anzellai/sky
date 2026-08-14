package bluedb

import "sync/atomic"

// validateCalls counts validate() invocations — a test seam (T8/T16/T24 assert the blind
// fast path drives ZERO validations). Package-level so tests can snapshot it around a
// commit without threading a counter through the engine.
var validateCalls atomic.Int64

// validate is the pure, in-RAM commit-time conflict test (§4.3). It runs INSIDE the single
// committer (the serialization point) against window = ring.after(readTs) ++ pending. A
// read-set entry conflicts iff a committed KeyChange in the window touches it:
//
//   - point key: any change with Pk == the read key.
//   - index range (range-optimized): any change whose NewIndex/OldIndex coord for that
//     index falls in the closed [lo, hi] (NewIndex = a phantom appears; OldIndex = a phantom
//     disappears — both required, §2.2).
//   - collection witness (fallback colType / IS-NULL): any change to that collection.
//   - index witness (fallback-colType index scan): any change with a coord on that index.
//
// The witnesses over-reject (coarser) but NEVER under-reject — the conservative fail-safe
// that keeps SERIALIZABLE holding for real/money/blob/IS-NULL (§2.2). Returns the culprit Pk
// AND whether the conflict was a POINT-read hit (pointConflict): Phase-2b hot-key detection
// (§6.2) records an abort ONLY for a point conflict — a range/predicate/witness conflict has
// no single key to lease (§6.4), so the culprit's Pk there is just the changed row, not a key
// the victim read as a point, and leasing it would not help. pointConflict gates recordAbort.
func validate(rs *ReadSet, window []KeyChange) (conflict bool, culprit []byte, pointConflict bool) {
	validateCalls.Add(1)
	// C6b classification — FAIL-OPEN BY DESIGN, and the design is in the type. A nil
	// ReadSet is not "validation failed to produce a read-set"; it is CommitReq's declared
	// encoding for a BLIND write ("ReadSet: nil ⇒ skip validation"), and a blind write has
	// no read to be stale. Txn.buildReq always constructs a non-nil ReadSet even when the
	// txn read nothing, so a transactional job can never arrive here as nil. What would
	// make this unsafe is a caller that dropped a read-set on an error path and passed nil;
	// there is no such caller, and the two producers of CommitReq (Txn.buildReq and the
	// blind Commit path) are the invariant to check if that ever changes.
	if rs == nil {
		return false, nil, false
	}
	for i := range window {
		ch := &window[i]
		// Point read — the leaseable conflict class (§6.2).
		if len(rs.points) > 0 {
			if _, ok := rs.points[string(ch.Pk)]; ok {
				return true, ch.Pk, true
			}
		}
		// Collection-level fallback witness — predicate contention, NOT leaseable.
		if len(rs.collWitness) > 0 && rs.collWitness[ch.Coll] {
			return true, ch.Pk, false
		}
		// Index ranges + index-level fallback witness, over both New and Old coords — predicate
		// contention, NOT leaseable (§6.4).
		if len(rs.ranges) > 0 || len(rs.indexWitness) > 0 {
			if coordHit(rs, ch.NewIndex) || coordHit(rs, ch.OldIndex) {
				return true, ch.Pk, false
			}
		}
	}
	return false, nil, false
}

// coordHit reports whether any coord matches a scanned index range (byte-range test) or a
// fallback index witness.
func coordHit(rs *ReadSet, coords []IndexCoord) bool {
	for i := range coords {
		c := &coords[i]
		if len(rs.indexWitness) > 0 && rs.indexWitness[c.Index] {
			return true
		}
		for j := range rs.ranges {
			r := &rs.ranges[j]
			// C6b classification — AUDIT-N2, a KNOWN fail-open, tracked not accepted.
			// inRangeClosed rejects a coord whose encoded key is longer than the band's
			// bounds, so a descending non-fixed-width (text) index under-rejects a phantom
			// that is inside the value range. It is not fixable here: Descending /
			// rangeOptimized / encodeScanRange all live in index_key.go, which Stage 2
			// deliberately keeps out. Owned by G2.12 and anchored in readset.go's AUDIT-N2
			// note. It is also outside Stage 2's serializability claim by construction —
			// rs.ranges has NO writer in this package, since Txn.Scan/ScanFallback were
			// excised, so this arm is structurally unreachable today.
			if r.index == c.Index && inRangeClosed(r.lo, r.hi, c.Key) {
				return true
			}
		}
	}
	return false
}
