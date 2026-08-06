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
// (feeds Phase-2b hot-key detection; advisory in 2a).
func validate(rs *ReadSet, window []KeyChange) (conflict bool, culprit []byte) {
	validateCalls.Add(1)
	if rs == nil {
		return false, nil
	}
	for i := range window {
		ch := &window[i]
		// Point read.
		if len(rs.points) > 0 {
			if _, ok := rs.points[string(ch.Pk)]; ok {
				return true, ch.Pk
			}
		}
		// Collection-level fallback witness.
		if len(rs.collWitness) > 0 && rs.collWitness[ch.Coll] {
			return true, ch.Pk
		}
		// Index ranges + index-level fallback witness, over both New and Old coords.
		if len(rs.ranges) > 0 || len(rs.indexWitness) > 0 {
			if coordHit(rs, ch.NewIndex) || coordHit(rs, ch.OldIndex) {
				return true, ch.Pk
			}
		}
	}
	return false, nil
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
			if r.index == c.Index && inRangeClosed(r.lo, r.hi, c.Key) {
				return true
			}
		}
	}
	return false
}
