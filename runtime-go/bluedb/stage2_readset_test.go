package bluedb

import "testing"

// TestStage2ReadSetRangesHaveNoProducer pins the SCOPE of Stage 2's serializability claim.
//
// Excising Txn.Scan / Txn.ScanRange / Txn.ScanFallback (P1-STAGE2-PLAN §"Excision") removed
// the ONLY writers of ReadSet.ranges and ReadSet.indexWitness. Stage 2 therefore ships an SSI
// validator (validate.go) whose range-conflict and index-witness arms are structurally
// unreachable, and its serializability claim covers POINT READS and collWitness only.
//
// That is a fact about the code, not a comment, so it is asserted: a transaction body that
// uses every read-recording verb Stage 2 still exposes — Get, Put and Delete (both take a
// pre-image point read), ScanCollection and WitnessCollection — must build a ReadSet with an
// EMPTY ranges slice.
//
// The test is deliberately two-sided. If it only asserted ranges == 0 it would also pass
// against a ReadSet that recorded nothing whatsoever, which is the failure mode that would
// matter most; so it also asserts the arms that ARE live (points, collWitness) are populated.
//
// This is one of the three anchors that keep AUDIT-N2 (readset.go) from evaporating — the
// other two being that comment and the C1 commit message. Its corollary, which no Stage-2
// gate may violate: mutating inRangeClosed to `return false` must not change any gate.
func TestStage2ReadSetRangesHaveNoProducer(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	const coll = CollID(7)
	prefix := []byte("things\x1f")

	// Seed two committed rows so the scan has something real to materialize and the
	// Get/Delete below have a pre-image. Without them the read-set could be empty for
	// uninteresting reasons.
	put(t, e, "things\x1fa", "A")
	put(t, e, "things\x1fb", "B")

	var rs *ReadSet
	scanned := 0
	if err := e.Transact(func(tx *Txn) error {
		tx.SetCollection(coll)

		if v, ok := tx.Get([]byte("things\x1fa")); !ok || string(v) != "A" {
			t.Fatalf("Get(things/a) = %q,%v want A,true", v, ok)
		}
		if err := tx.Put([]byte("things\x1fc"), []byte("C")); err != nil {
			return err
		}
		if err := tx.Delete([]byte("things\x1fb")); err != nil {
			return err
		}

		cur := tx.ScanCollection(coll, prefix)
		for cur.Next() {
			scanned++
		}
		if err := cur.Err(); err != nil {
			t.Fatalf("ScanCollection cursor: %v", err)
		}
		cur.Close()

		tx.WitnessCollection(CollID(9))

		rs = tx.buildReq().ReadSet
		return nil
	}); err != nil {
		t.Fatalf("Transact: %v", err)
	}

	if rs == nil {
		t.Fatal("buildReq produced a nil ReadSet")
	}

	// ── The claim under test ──────────────────────────────────────────────────────────
	if len(rs.ranges) != 0 {
		t.Fatalf("ReadSet.ranges = %d entries, want 0: something in Stage 2 produces index "+
			"ranges, so the excision is incomplete and validate()'s range arm is reachable "+
			"WITHOUT the AUDIT-N2 (Descending(ColText)) fix", len(rs.ranges))
	}
	if len(rs.indexWitness) != 0 {
		t.Fatalf("ReadSet.indexWitness = %d entries, want 0: Txn.ScanFallback was excised, so "+
			"nothing in Stage 2 may write the index-level witness", len(rs.indexWitness))
	}

	// ── Non-vacuity: the arms that ARE live must have fired ───────────────────────────
	if scanned == 0 {
		t.Fatal("ScanCollection returned no rows — the body did not exercise the scan path")
	}
	if len(rs.points) == 0 {
		t.Fatal("ReadSet.points is empty: Get + the Put/Delete pre-images must record point " +
			"reads, so this test would be asserting emptiness of an empty read-set")
	}
	if !rs.collWitness[coll] {
		t.Fatalf("ReadSet.collWitness missing coll %d: ScanCollection must record it", coll)
	}
	if !rs.collWitness[CollID(9)] {
		t.Fatal("ReadSet.collWitness missing coll 9: WitnessCollection must record it")
	}
}
