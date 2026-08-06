package bluedb

// blindput_underreject_test.go — regression for the shipped Phase-2 SSI under-reject exposed by the
// Phase-4 design grill (docs/bluedb/phase4-grill-findings.md, "EXPOSED: shipped Phase-2 SSI
// under-reject"). The autocommit blind upsert (embedded.go blindPut) emitted NewIndex only and NO
// OldIndex — unlike blindDelete — so a concurrent range-optimized (PRECISE-tier) scanner never saw
// a row LEAVE its scanned range when an autocommit Put moved it out, and committed on a stale read
// (non-serializable). These tests drive the update through the AUTOCOMMIT blind path (b.Put on a
// collection with no unique columns) that the existing write-skew suite never hit — it drove
// updates through Txn.Put (→ ensurePreimage → OldIndex populated).

import (
	"errors"
	"testing"
)

// TestBlindPutUnderReject_RangeDeparture — the confirmed under-reject. T range-scans age∈[20,40]
// (the PRECISE tier: records the interval, NOT the returned row's PK as a point). A concurrent
// autocommit blindPut moves r1 out of that range (age 30 → 100). T's committed decision is derived
// from the now-stale read, so T MUST be REJECTED. Before the fix (blindPut emits OldIndex=nil) the
// validator sees no point hit and no coord hit for the OLD in-range position → T wrongly commits.
func TestBlindPutUnderReject_RangeDeparture(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema() // text PK + text "status" index + int "age" index; NO unique columns
	b.Register(orders)

	// Precondition: no unique columns → Put MUST take the blindPut fast path (not the txWrite path).
	if got := len(uniqueColRefs(&orders)); got != 0 {
		t.Fatalf("fixture must have no unique columns so Put routes to blindPut; got %d unique refs", got)
	}

	// Seed r1 IN-range (age=30) via the autocommit blind path — durable before T begins.
	if err := b.Put(orders, "r1", jsonRow(`{"id":"r1","status":"open","age":30}`), nil); err != nil {
		t.Fatalf("seed r1: %v", err)
	}

	// age in [20,40] — a bounded AND range on the declared int "age" index → the PRECISE tier.
	agePlan := QueryPlan{Where: CondNode{Op: CondAnd, Kids: []CondNode{
		{Op: CondGte, Col: "age", Type: ColInt, Val: IntVal(20)},
		{Op: CondLte, Col: "age", Type: ColInt, Val: IntVal(40)},
	}}, Limit: -1}

	// T begins (readTs pinned), scans [20,40], reads r1.
	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	rows, err := et.Query(orders, agePlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("T should read exactly r1 (age=30) in [20,40]; got %d rows", len(rows))
	}

	// Bug precondition, asserted directly: the PRECISE tier records a range, is NOT the coarse
	// collection witness, and does NOT record the returned row r1 as a point read.
	if len(tx.ranges) != 1 {
		t.Fatalf("precise tier expected exactly 1 recorded range; got %d", len(tx.ranges))
	}
	if tx.collWitness[orders.ID] {
		t.Fatal("precise tier must NOT fall back to the coarse collection witness")
	}
	if _, isPoint := tx.points[string(dataUserKey("orders", "r1"))]; isPoint {
		t.Fatal("precise-tier scan must NOT record the returned row as a point read (bug precondition)")
	}

	// Concurrent autocommit blindPut MOVES r1 out of the scanned range: age 30 → 100.
	if err := b.Put(orders, "r1", jsonRow(`{"id":"r1","status":"closed","age":100}`), nil); err != nil {
		t.Fatalf("concurrent blind update of r1: %v", err)
	}

	// T commits a decision derived from its now-stale read → MUST be REJECTED (serializable).
	_ = tx.Put(dataUserKey("orders", "decision"), jsonRow(`{"id":"decision","status":"open","age":25}`))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("UNDER-REJECT: a blindPut moving r1 out of T's scanned [20,40] range must conflict; got %v", err)
	}
}

// TestBlindPutEnter_StillCaught — the symmetric ENTER guard. A blindPut moving a row INTO the
// scanned range is caught via NewIndex today; the OldIndex fix must not break it. r1 starts
// out-of-range (age=100, so T's scan does NOT return it) and a concurrent blindPut moves it in
// (age=30) → NewIndex ∈ [20,40] → conflict.
func TestBlindPutEnter_StillCaught(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	// Seed r1 OUT-of-range (age=100).
	if err := b.Put(orders, "r1", jsonRow(`{"id":"r1","status":"closed","age":100}`), nil); err != nil {
		t.Fatalf("seed r1: %v", err)
	}

	agePlan := QueryPlan{Where: CondNode{Op: CondAnd, Kids: []CondNode{
		{Op: CondGte, Col: "age", Type: ColInt, Val: IntVal(20)},
		{Op: CondLte, Col: "age", Type: ColInt, Val: IntVal(40)},
	}}, Limit: -1}

	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	rows, err := et.Query(orders, agePlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("T should read 0 rows in [20,40] (r1 is age=100); got %d", len(rows))
	}

	// Concurrent autocommit blindPut MOVES r1 INTO the scanned range: age 100 → 30.
	if err := b.Put(orders, "r1", jsonRow(`{"id":"r1","status":"open","age":30}`), nil); err != nil {
		t.Fatalf("concurrent blind update of r1: %v", err)
	}

	_ = tx.Put(dataUserKey("orders", "decision"), jsonRow(`{"id":"decision","status":"open","age":25}`))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("ENTER: a blindPut moving r1 INTO T's scanned [20,40] range must conflict; got %v", err)
	}
}

// TestBlindPutInsert_OutOfRange_NoFalseConflict — a genuine out-of-range INSERT (no pre-image, so
// OldIndex stays nil correctly) must NOT conflict with a disjoint scanned range. Guards the fix
// against over-rejecting: the pre-image read on an insert finds nothing, so no spurious OldIndex
// coord is emitted.
func TestBlindPutInsert_OutOfRange_NoFalseConflict(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	agePlan := QueryPlan{Where: CondNode{Op: CondAnd, Kids: []CondNode{
		{Op: CondGte, Col: "age", Type: ColInt, Val: IntVal(20)},
		{Op: CondLte, Col: "age", Type: ColInt, Val: IntVal(40)},
	}}, Limit: -1}

	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	if _, err := et.Query(orders, agePlan); err != nil {
		t.Fatal(err)
	}

	// A brand-new row inserted fully OUT of [20,40] via blindPut (age=100) — no pre-image.
	if err := b.Put(orders, "brand-new", jsonRow(`{"id":"brand-new","status":"closed","age":100}`), nil); err != nil {
		t.Fatalf("out-of-range insert: %v", err)
	}

	_ = tx.Put(dataUserKey("orders", "decision"), jsonRow(`{"id":"decision","status":"open","age":25}`))
	if err := tx.Commit(); err != nil {
		t.Fatalf("an out-of-range blind INSERT must NOT conflict with the disjoint [20,40] scanner; got %v", err)
	}
}

// TestBlindPutIndexLessFastPath_NoPreimageSnapshot — the ZERO-extra-cost guarantee. An index-less
// collection has no range to leave, so blindPut must NOT read a pre-image: the OldIndex fix is
// gated on len(cs.Indexes) > 0. Asserted directly via the snapshotCalls seam — a blind Put on an
// index-less collection drives ZERO Engine.Snapshot() calls, both for an insert AND an update.
func TestBlindPutIndexLessFastPath_NoPreimageSnapshot(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	kv := CollSchema{
		Name: "kv", ID: 7, Key: "k",
		Cols: []ColSpec{{Name: "k", Type: ColText}, {Name: "v", Type: ColText}},
		// No Indexes → the blind fast path must skip the pre-image Snapshot entirely.
	}
	b.Register(kv)

	// Insert (no pre-image) — zero snapshots.
	before := snapshotCalls.Load()
	if err := b.Put(kv, "k1", jsonRow(`{"k":"k1","v":"a"}`), nil); err != nil {
		t.Fatal(err)
	}
	if got := snapshotCalls.Load() - before; got != 0 {
		t.Fatalf("index-less blind INSERT drove %d Snapshot() calls, want 0 (fast path preserved)", got)
	}

	// Update the same key (a pre-image EXISTS, but index-less → still must not snapshot for it).
	before = snapshotCalls.Load()
	if err := b.Put(kv, "k1", jsonRow(`{"k":"k1","v":"b"}`), nil); err != nil {
		t.Fatal(err)
	}
	if got := snapshotCalls.Load() - before; got != 0 {
		t.Fatalf("index-less blind UPDATE drove %d Snapshot() calls, want 0 (fast path preserved)", got)
	}

	// Contrast: an INDEXED collection's blind update DOES read the pre-image (exactly 1 snapshot).
	orders := ordersSchema()
	b.Register(orders)
	if err := b.Put(orders, "o1", jsonRow(`{"id":"o1","status":"open","age":30}`), nil); err != nil {
		t.Fatal(err)
	}
	before = snapshotCalls.Load()
	if err := b.Put(orders, "o1", jsonRow(`{"id":"o1","status":"closed","age":40}`), nil); err != nil {
		t.Fatal(err)
	}
	if got := snapshotCalls.Load() - before; got != 1 {
		t.Fatalf("indexed blind update should read the pre-image with exactly 1 Snapshot(); got %d", got)
	}
}
