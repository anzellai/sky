package bluedb

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
type indexRange struct {
	index IndexID
	lo    []byte
	hi    []byte
}
