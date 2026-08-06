package bluedb

import (
	"bytes"
	"testing"
)

// T19 — the ONE-encoder byte-match property (§2.2, R-2.1). encodeIndexKey produces
// scan-bound bytes and coord bytes that BYTE-MATCH for int/text/bool + composite; the
// order-preserving property holds (encode is monotone); descending applies invert AND the
// scan swaps lo/hi; composite orders by concatenated fields. Because BOTH the scan bound and
// the coord go through the SAME encodeIndexKey, drift is structurally impossible.
func TestT19_EncoderScanCoordByteMatch(t *testing.T) {
	const idx = IndexID(7)

	// (a) scan-bound bytes == coord bytes for the same value, every supported colType.
	cases := []struct {
		name string
		ct   ColType
		val  []byte
	}{
		{"int", ColInt, IntKey(42)},
		{"int-neg", ColInt, IntKey(-42)},
		{"text", ColText, []byte("open")},
		{"bool-true", ColBool, []byte{1}},
		{"bool-false", ColBool, []byte{0}},
	}
	for _, c := range cases {
		coord := encodeIndexKey(idx, c.ct, c.val)               // the tx.indexer side
		lo, hi := encodeScanRange(idx, c.ct, c.val, c.val)      // the Txn.Scan side (point range)
		if !bytes.Equal(coord, lo) || !bytes.Equal(coord, hi) { // ONE encoder → identical bytes
			t.Fatalf("%s: coord %x != scan-bound lo %x / hi %x", c.name, coord, lo, hi)
		}
		if !inRangeClosed(lo, hi, coord) {
			t.Fatalf("%s: coord not in its own point range", c.name)
		}
	}

	// (b) int is order-preserving across the sign boundary (negatives sort below positives).
	ints := []int64{-1 << 40, -5, -1, 0, 1, 5, 1 << 40}
	var prev []byte
	for _, n := range ints {
		enc := encodeIndexKey(idx, ColInt, IntKey(n))
		if prev != nil && bytes.Compare(prev, enc) >= 0 {
			t.Fatalf("int not order-preserving at %d: %x >= %x", n, prev, enc)
		}
		prev = enc
	}

	// (c) text is order-preserving (byte order == lexicographic).
	if bytes.Compare(encodeIndexKey(idx, ColText, []byte("apple")),
		encodeIndexKey(idx, ColText, []byte("banana"))) >= 0 {
		t.Fatal("text not order-preserving")
	}

	// (d) encode(lo) ≤ encode(coord) ≤ encode(hi) ⟺ row in range (int band [10,20]).
	lo, hi := encodeScanRange(idx, ColInt, IntKey(10), IntKey(20))
	for _, n := range []int64{10, 15, 20} {
		if !inRangeClosed(lo, hi, encodeIndexKey(idx, ColInt, IntKey(n))) {
			t.Fatalf("int %d should be in [10,20]", n)
		}
	}
	for _, n := range []int64{9, 21, -5} {
		if inRangeClosed(lo, hi, encodeIndexKey(idx, ColInt, IntKey(n))) {
			t.Fatalf("int %d should NOT be in [10,20]", n)
		}
	}

	// (e) DESCENDING: invert applied to BOTH coord and bound, and the scan SWAPS lo/hi so the
	// encoded interval is still lo ≤ hi. A value INSIDE the user range lands inside [lo,hi].
	desc := Descending(ColInt)
	dcoordSmall := encodeIndexKey(idx, desc, IntKey(1)) // small value → LARGER inverted bytes
	dcoordLarge := encodeIndexKey(idx, desc, IntKey(9)) // large value → SMALLER inverted bytes
	if bytes.Compare(dcoordLarge, dcoordSmall) >= 0 {
		t.Fatal("descending should invert order: enc(9) must sort below enc(1)")
	}
	dlo, dhi := encodeScanRange(idx, desc, IntKey(1), IntKey(9)) // user band [1,9]
	if bytes.Compare(dlo, dhi) > 0 {
		t.Fatalf("descending scan bound not swapped: lo %x > hi %x", dlo, dhi)
	}
	for _, n := range []int64{1, 5, 9} {
		if !inRangeClosed(dlo, dhi, encodeIndexKey(idx, desc, IntKey(n))) {
			t.Fatalf("descending: value %d should be in the swapped range", n)
		}
	}
	if inRangeClosed(dlo, dhi, encodeIndexKey(idx, desc, IntKey(100))) {
		t.Fatal("descending: value 100 should NOT be in [1,9]")
	}

	// (f) COMPOSITE orders by concatenated fields (fixed-width int prefix, then text).
	comp := func(n int64, s string) []byte {
		return encodeCompositeKey(idx, []IndexCol{{ColInt, IntKey(n)}, {ColText, []byte(s)}})
	}
	// same int, text tiebreak
	if bytes.Compare(comp(1, "a"), comp(1, "b")) >= 0 {
		t.Fatal("composite: (1,a) should sort below (1,b)")
	}
	// int dominates
	if bytes.Compare(comp(1, "z"), comp(2, "a")) >= 0 {
		t.Fatal("composite: (1,z) should sort below (2,a) — int field dominates")
	}
	// composite scan-bound builder byte-matches the composite coord
	clo, chi := encodeCompositeScanRange(idx,
		[]IndexCol{{ColInt, IntKey(1)}, {ColText, []byte("a")}},
		[]IndexCol{{ColInt, IntKey(1)}, {ColText, []byte("a")}})
	if !bytes.Equal(clo, comp(1, "a")) || !bytes.Equal(chi, comp(1, "a")) {
		t.Fatal("composite scan-bound bytes must byte-match the composite coord")
	}
}

// T-codec — KeyChange codec round-trips (encode → decode identity), incl. New/Old coords.
func TestKeyChangeCodecRoundTrip(t *testing.T) {
	in := []KeyChange{
		{
			Coll: 3, Pk: []byte("user:1"), Op: OpPut, Record: []byte(`{"n":1}`),
			NewIndex: []IndexCoord{{Index: 1, Key: encodeIndexKey(1, ColText, []byte("open"))}},
			OldIndex: nil,
		},
		{
			Coll: 3, Pk: []byte("user:2"), Op: OpDelete, Record: nil,
			NewIndex: nil,
			OldIndex: []IndexCoord{
				{Index: 1, Key: encodeIndexKey(1, ColText, []byte("closed"))},
				{Index: 2, Key: encodeIndexKey(2, ColInt, IntKey(-7))},
			},
		},
	}
	payload := EncodeChangelogPayload(in)
	out, err := DecodeChangelogPayload(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("count: got %d want %d", len(out), len(in))
	}
	for i := range in {
		a, b := in[i], out[i]
		if a.Coll != b.Coll || !bytes.Equal(a.Pk, b.Pk) || a.Op != b.Op || !bytes.Equal(a.Record, b.Record) {
			t.Fatalf("change %d envelope mismatch: %+v vs %+v", i, a, b)
		}
		if len(a.NewIndex) != len(b.NewIndex) || len(a.OldIndex) != len(b.OldIndex) {
			t.Fatalf("change %d coord counts differ", i)
		}
		for j := range a.NewIndex {
			if a.NewIndex[j].Index != b.NewIndex[j].Index || !bytes.Equal(a.NewIndex[j].Key, b.NewIndex[j].Key) {
				t.Fatalf("change %d NewIndex %d mismatch", i, j)
			}
		}
		for j := range a.OldIndex {
			if a.OldIndex[j].Index != b.OldIndex[j].Index || !bytes.Equal(a.OldIndex[j].Key, b.OldIndex[j].Key) {
				t.Fatalf("change %d OldIndex %d mismatch", i, j)
			}
		}
	}

	// Empty list → nil payload round-trip; garbage → error (never panics).
	if p := EncodeChangelogPayload(nil); len(p) == 0 {
		t.Fatal("empty change list still needs a tagged payload")
	}
	if _, err := DecodeChangelogPayload([]byte{0xFF, 0xFF}); err == nil {
		t.Fatal("malformed payload should error, not accept")
	}
	if got, err := DecodeChangelogPayload(nil); err != nil || got != nil {
		t.Fatalf("nil payload should decode to (nil,nil), got (%v,%v)", got, err)
	}
}

// Ring unit — after/trim/spill semantics (§4.2): after returns commits strictly > readTs;
// trim raises the floor and drops below-T entries; a readTs below the floor reports spilled.
func TestRecentRingAfterTrimSpill(t *testing.T) {
	r := newRecentRing()
	mk := func(w uint64) HLC { return HLC{WallMs: w} }
	r.append(mk(10), []KeyChange{{Pk: []byte("a")}})
	r.append(mk(20), []KeyChange{{Pk: []byte("b")}})
	r.append(mk(30), []KeyChange{{Pk: []byte("c")}})

	if got, spilled := r.after(mk(15)); spilled || len(got) != 2 {
		t.Fatalf("after(15): got %d spilled=%v, want 2 changes", len(got), spilled)
	}
	if got, _ := r.after(mk(30)); len(got) != 0 {
		t.Fatalf("after(30): want 0 (commit at exactly readTs is in the snapshot, not the window)")
	}

	r.trim(mk(20)) // drop <20 → entry(10) gone, entries 20,30 kept; floor=20
	if got, _ := r.after(mk(15)); len(got) != 2 {
		// readTs 15 < floor 20 → spilled path; but the entries it needs (20,30) survived — the
		// caller re-derives via Changelog.Tail. after must report spilled so it does.
	}
	if _, spilled := r.after(mk(15)); !spilled {
		t.Fatal("after(15) below floor(20) should report spilled=true")
	}
	if got, spilled := r.after(mk(25)); spilled || len(got) != 1 {
		t.Fatalf("after(25): got %d spilled=%v, want 1 (only commit 30)", len(got), spilled)
	}
}

// Fix-2 — composite index layout guard (§2.2). encodeCompositeKey concatenates columns with NO
// separator, so it is order-preserving ONLY when every non-suffix column is fixed-width (int BE8 /
// bool 1B); a variable-width (text/blob/real/money) column in a non-suffix position would silently
// UNDER-REJECT at validation. checkCompositeLayout must ACCEPT the safe layouts and REJECT the
// unsafe ones, and encodeCompositeKey must PANIC (fail loud at construction) on an unsafe layout —
// a guard before Phase 3 wires schema-driven composites.
func TestFix2_CompositeLayoutGuard(t *testing.T) {
	const idx = IndexID(7)

	accept := [][]IndexCol{
		{{ColInt, IntKey(1)}, {ColText, []byte("a")}},                       // (int, text) — fixed prefix, variable suffix
		{{ColBool, []byte{1}}, {ColInt, IntKey(2)}, {ColText, []byte("z")}}, // (bool, int, text)
		{{ColInt, IntKey(1)}, {ColInt, IntKey(2)}},                          // (int, int) — all fixed
		{{ColText, []byte("solo")}},                                         // single variable column IS the suffix
		{{ColInt, IntKey(1)}, {ColMoney, []byte("USD 1.00")}},               // fallback money as suffix is allowed
	}
	for _, cols := range accept {
		if err := checkCompositeLayout(cols); err != nil {
			t.Fatalf("checkCompositeLayout rejected an order-preserving layout %v: %v", cols, err)
		}
		mustNotPanic(t, func() { _ = encodeCompositeKey(idx, cols) }, cols)
	}

	reject := [][]IndexCol{
		{{ColText, []byte("a")}, {ColInt, IntKey(1)}},                      // (text, int) — variable-width non-suffix
		{{ColText, []byte("a")}, {ColText, []byte("b")}},                   // (text, text) — first text is non-suffix
		{{ColBlob, []byte("x")}, {ColBool, []byte{1}}},                     // (blob, bool) — variable-width non-suffix
		{{ColInt, IntKey(1)}, {ColText, []byte("a")}, {ColInt, IntKey(2)}}, // text in the middle
	}
	for _, cols := range reject {
		if err := checkCompositeLayout(cols); err == nil {
			t.Fatalf("checkCompositeLayout ACCEPTED a non-order-preserving layout %v — it must reject (variable-width non-suffix)", cols)
		}
		mustPanic(t, func() { _ = encodeCompositeKey(idx, cols) }, cols)
	}
}

func mustPanic(t *testing.T, fn func(), cols []IndexCol) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("encodeCompositeKey must PANIC (fail loud) on unsafe layout %v, but it returned", cols)
		}
	}()
	fn()
}

func mustNotPanic(t *testing.T, fn func(), cols []IndexCol) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("encodeCompositeKey must NOT panic on a safe layout %v, but it panicked: %v", cols, r)
		}
	}()
	fn()
}
