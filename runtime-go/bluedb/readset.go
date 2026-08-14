package bluedb

import "bytes"

// readset.go — the point + index-range + fallback-witness read-set (§2). The ReadSet
// struct itself is declared in engine.go (so CommitReq's shape stays frozen); its fields
// and the supporting types live here (same package — L2-embedded is more Go in bluedb).

// pointRead records a point-key dependency (§2.1). A point read of K conflicts iff ANY
// committed KeyChange in (readTs, commitTs] has Pk == K. The window already excludes
// everything ≤ readTs, so window membership is authoritative; versionSeen / present are a
// defensive tightening + debug aid.
type pointRead struct {
	versionSeen HLC  // reader.Get's returned commitTs; HLC{} if the key read ABSENT
	present     bool // false ⇒ the txn's logic depended on this key being ABSENT (point-phantom)
}

// indexRange records a scanned index interval (§2.2) — the SSI crux. lo/hi are the CLOSED
// order-preserving encoded bounds actually scanned (built by encodeScanRange, through the
// ONE encoder). A conflict is any committed change whose NewIndex/OldIndex coord for this
// index falls in [lo, hi].
//
// AUDIT-N2 — Descending(ColText) is NOT order-preserving; this arm UNDER-REJECTS.
// Owning gate: G2.12. MUST be fixed in P2 BEFORE Txn.Scan returns.
//
// `rangeOptimized` (index_key.go, not in Stage 2) masks the descending flag off and
// returns true for ColText even though `fixedWidthCol` already knows text is
// variable-width. Worked example: a band over ["a","b"] encodes lo=[0x9D], hi=[0x9E];
// a phantom at "ab" encodes [0x9E,0x9D], which inRangeClosed below rejects as longer.
// The phantom IS inside the value range and is NOT matched — a serializability break by
// under-rejection, not a false conflict.
//
// It is UNFIXABLE in Stage 2 because there is nothing here to fix: Descending,
// rangeOptimized and encodeScanRange all live in index_key.go, which must not enter this
// stage. It is also currently UNREACHABLE here — Txn.Scan / Txn.ScanRange /
// Txn.ScanFallback were excised, so nothing writes ReadSet.ranges (see txn.go's excision
// note and TestStage2ReadSetRangesHaveNoProducer).
//
// Recommended P2 fix (conservative, NO encoding change): rangeOptimized returns false for
// a descending non-fixed-width column, so descending text degrades to the collection
// witness — over-rejects, never under-rejects. Re-encoding is rejected because
// IndexCoord.Key is serialised into the DURABLE changelog and would need a payloadFmtV1
// bump.
type indexRange struct {
	index IndexID
	lo    []byte
	hi    []byte
}

// inRangeClosed reports lo ≤ key ≤ hi in byte order (the closed-interval membership the
// validator uses for range-optimized index coords).
//
// RELOCATED VERBATIM from index_key.go:194-196 (P1-STAGE2-PLAN §"Excision"). Its only
// surviving consumer is validate.go's coordHit; the other (Txn.rowMatches) was excised.
// Keeping it here is what lets index_key.go stay out of Stage 2 with nothing dangling.
func inRangeClosed(lo, hi, key []byte) bool {
	return bytes.Compare(lo, key) <= 0 && bytes.Compare(key, hi) <= 0
}
