package bluedb

import (
	"bytes"
	"errors"
	"testing"
)

// validate_test.go — the commit-time conflict test's OWN behaviour, arm by arm.
//
// # The gap this file closes
//
// `stage2_readset_test.go` asserts that a transaction body POPULATES the read-set. That
// is a statement about `Txn`, not about `validate()`, and the difference is the whole
// distance between "the dependency was recorded" and "the dependency was enforced". An
// adversarial Judge measured it: deleting `validate()`'s collection-witness arm —
//
//	-		// Collection-level fallback witness — predicate contention, NOT leaseable.
//	-		if len(rs.collWitness) > 0 && rs.collWitness[ch.Coll] {
//	-			return true, ch.Pk, false
//	-		}
//
// — let a scan-then-insert transaction that conflicts at HEAD commit CLEAN, a phantom with
// no serial order, while `go test ./bluedb/...`, `cargo test -p xtask`, `--tier=full` and
// `--verify-mutations` all stayed green. Gutting `validate()` ENTIRELY (`return false,
// nil, false` at the top) was caught by exactly one assertion in the whole corpus, and only
// on the point arm.
//
// P1's scope row and `txn.go`'s excision note say Stage 2's serializability claim covers
// **point reads and collWitness only** — the range and index-witness arms are structurally
// unreachable, having no producer. So those two arms are the entirety of what P1 still
// claims about SERIALIZABLE, and each gets a fixture here that asserts the CONSEQUENCE: the
// conflicting transaction is REFUSED.
//
// # Why every fixture has a control arm
//
// "the transaction was refused" is satisfied by a validator that refuses everything, which
// is not serializability — it is an engine that cannot commit. Each fixture therefore runs
// the same shape twice: once where the concurrent change really does intersect the
// read-set, once where it provably does not. A `validate()` gutted to `return false` fails
// the first; one gutted to `return true` fails the second. Only the real one passes both.
//
// # Why each fixture isolates ONE arm
//
// A fixture whose transaction carries both a point read and a collection witness would go
// red if EITHER arm survived, so it could not tell which one did. The point fixture asserts
// its read-set has no witnesses before committing; the witness fixture asserts the phantom
// key is not in its point set. Those two assertions are what make the two mutations
// registered against this file discriminating.

// # A note on how the concurrent writer commits
//
// Both fixtures drive their interfering writer through a `Txn`, never through the `put`
// helper, and the difference is not stylistic. `put` calls `Engine.Commit` with `Writes`
// and an EMPTY `ChangelogPayload`; `processBlindPhase1` builds the recent-changes ring from
// the DECODED payload, so such a commit is applied durably while contributing nothing to
// the SSI validation window. A transaction whose readTs is below it then validates against
// a window that does not mention it — which is N6's under-rejection shape with an ABSENT
// payload in place of an undecodable one. That is a finding about the L1 blind path, not
// about `validate()`, and a fixture built on it would be measuring the hole rather than the
// arm. `Txn.buildReq` always emits a payload, so the transactional path is the one whose
// window is real.

// txnPut commits one write through the transactional path, so the change reaches the
// recent-changes ring (see the note above) and can be seen by a concurrent transaction's
// validation window. Fails the test if the write itself does not commit — an interfering
// writer that never landed interferes with nothing.
func txnPut(t *testing.T, e *pebbleEngine, coll CollID, key, val string) {
	t.Helper()
	tx, err := e.Begin()
	if err != nil {
		t.Fatalf("txnPut %q: begin: %v", key, err)
	}
	tx.SetCollection(coll)
	if err := tx.Put([]byte(key), []byte(val)); err != nil {
		t.Fatalf("txnPut %q: put: %v", key, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("txnPut %q: commit: %v", key, err)
	}
}

// TestValidateDetectsAPointReadOverwrittenConcurrently is validate()'s point arm — the
// leaseable conflict class (§6.2), and the lost-update guard every read-modify-write in the
// engine rests on.
//
// The shape is the classic one: a transaction reads a row, a concurrent commit supersedes
// that row, and the transaction then writes a value derived from what it read. Exactly one
// of the two may commit, and it is not the one whose premise expired.
func TestValidateDetectsAPointReadOverwrittenConcurrently(t *testing.T) {
	clk := &fakeClock{}
	clk.set(61000)
	e := openDisk(t, clk.fn())

	put(t, e, "pt-read", "v1")
	put(t, e, "pt-other", "v1")

	// ── The conflict arm ──────────────────────────────────────────────────────────────
	tx, err := e.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if v, ok := tx.Get([]byte("pt-read")); !ok || string(v) != "v1" {
		t.Fatalf("fixture: Get(pt-read) = %q,%v want \"v1\",true — the txn must really have read "+
			"the row for the point dependency below to exist", v, ok)
	}

	// A concurrent writer supersedes the row the txn read, strictly after its readTs.
	txnPut(t, e, CollID(1), "pt-read", "v2")

	// The body's conclusion, derived from the value it read.
	if err := tx.Put([]byte("pt-summary"), []byte("derived-from-v1")); err != nil {
		t.Fatalf("put: %v", err)
	}

	// The point arm must be the ONLY live arm here, or the assertion below would pass on
	// a validator whose point arm had been deleted and whose witness arm caught it instead.
	if len(tx.collWitness) != 0 || len(tx.ranges) != 0 || len(tx.indexWitness) != 0 {
		t.Fatalf("fixture: this txn carries %d collection witness(es), %d range(s) and %d index "+
			"witness(es); the point arm must be the only live arm or the conflict below could be "+
			"detected by a different one", len(tx.collWitness), len(tx.ranges), len(tx.indexWitness))
	}

	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("a transaction that READ pt-read at v1, and then wrote a value derived from it, "+
			"COMMITTED (err=%v) even though a concurrent commit had already superseded pt-read "+
			"with v2. validate()'s point arm did not detect the conflict, so the resulting history "+
			"has no serial order: the committed summary claims to be derived from a version that "+
			"was overwritten before it landed — a lost update", err)
	}

	// ── The control: nothing in the window intersects the read-set ─────────────────────
	ctl, err := e.Begin()
	if err != nil {
		t.Fatalf("begin control: %v", err)
	}
	if v, ok := ctl.Get([]byte("pt-other")); !ok || string(v) != "v1" {
		t.Fatalf("fixture: control Get(pt-other) = %q,%v want \"v1\",true", v, ok)
	}
	// A concurrent change that is REALLY in the control's validation window — same path,
	// same window, a key it never read. A blind `put` here would leave the window empty and
	// the control would pass for the wrong reason.
	txnPut(t, e, CollID(1), "pt-untouched", "v1")
	if err := ctl.Put([]byte("pt-summary-control"), []byte("derived-from-pt-other")); err != nil {
		t.Fatalf("control put: %v", err)
	}
	if err := ctl.Commit(); err != nil {
		t.Fatalf("control: a transaction whose read-set NOTHING in its validation window touched "+
			"was refused (%v). validate() over-rejects, so the conflict arm above proves nothing — "+
			"a validator that returns `conflict` unconditionally would satisfy it", err)
	}
}

// TestValidateDetectsAPhantomInsertIntoAWitnessedCollection is validate()'s collection-
// witness arm — the coarse, over-rejecting fallback (§2.2) that Stage 2's serializability
// claim rests on for everything a point read cannot express, and the arm the Judge's
// four-line deletion removed.
//
// A phantom is invisible to the point arm BY CONSTRUCTION: the inserted key is one the
// scanning transaction never read, and could not have read, because it did not exist at its
// readTs. Nothing but the collection witness can catch it.
func TestValidateDetectsAPhantomInsertIntoAWitnessedCollection(t *testing.T) {
	clk := &fakeClock{}
	clk.set(62000)
	e := openDisk(t, clk.fn())

	const watched = CollID(7)
	const elsewhere = CollID(8)
	prefix := []byte("things\x1f")

	put(t, e, "things\x1fa", "A")

	// T1 scans the collection — "these are all the things there are".
	t1, err := e.Begin()
	if err != nil {
		t.Fatalf("begin t1: %v", err)
	}
	t1.SetCollection(watched)
	seen := 0
	cur := t1.ScanCollection(watched, prefix)
	for cur.Next() {
		seen++
	}
	if err := cur.Err(); err != nil {
		t.Fatalf("t1 scan: %v", err)
	}
	cur.Close()
	if seen != 1 {
		t.Fatalf("fixture: the scan saw %d row(s), want 1 — the summary written below has to be a "+
			"real conclusion about a real scan", seen)
	}

	// T2 inserts a BRAND-NEW pk into the same collection and commits. This is the phantom.
	t2, err := e.Begin()
	if err != nil {
		t.Fatalf("begin t2: %v", err)
	}
	t2.SetCollection(watched)
	if err := t2.Put([]byte("things\x1fz"), []byte("Z")); err != nil {
		t.Fatalf("t2 put: %v", err)
	}
	if err := t2.Commit(); err != nil {
		t.Fatalf("fixture: the phantom insert did not commit (%v) — there is then no concurrent "+
			"change for T1 to conflict with", err)
	}

	// T1 writes the summary its scan justifies.
	if err := t1.Put([]byte("things-count"), []byte("1")); err != nil {
		t.Fatalf("t1 put: %v", err)
	}

	// The phantom must NOT be a point dependency, or the point arm would catch it and this
	// fixture would say nothing about the witness.
	if _, isPoint := t1.points["things\x1fz"]; isPoint {
		t.Fatal("fixture: the phantom key is in T1's POINT set, so validate()'s point arm would " +
			"detect this conflict and the assertion below would not be about the collection witness")
	}

	if err := t1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("a transaction that SCANNED collection %d and wrote a summary of what it found "+
			"COMMITTED (err=%v) even though a concurrent transaction had inserted a new row into "+
			"that same collection first. That is a PHANTOM: the committed summary says 1 row, "+
			"the store holds 2, and no serial order explains the pair. The inserted key was "+
			"never read by this transaction, so the point arm cannot see it: "+
			"validate()'s collection-witness arm is the only thing that detects it",
			watched, err)
	}

	// ── The control: a change to a collection this txn did NOT witness ─────────────────
	t3, err := e.Begin()
	if err != nil {
		t.Fatalf("begin t3: %v", err)
	}
	t3.SetCollection(watched)
	cur3 := t3.ScanCollection(watched, prefix)
	for cur3.Next() {
	}
	if err := cur3.Err(); err != nil {
		t.Fatalf("t3 scan: %v", err)
	}
	cur3.Close()

	t4, err := e.Begin()
	if err != nil {
		t.Fatalf("begin t4: %v", err)
	}
	t4.SetCollection(elsewhere)
	if err := t4.Put([]byte("others\x1fq"), []byte("Q")); err != nil {
		t.Fatalf("t4 put: %v", err)
	}
	if err := t4.Commit(); err != nil {
		t.Fatalf("fixture: the control's concurrent write did not commit: %v", err)
	}

	if err := t3.Put([]byte("things-count-again"), []byte("2")); err != nil {
		t.Fatalf("t3 put: %v", err)
	}
	if err := t3.Commit(); err != nil {
		t.Fatalf("control: a transaction that witnessed collection %d was REFUSED (%v) for a "+
			"change to collection %d, which it never read. The witness matches on the changed "+
			"row's collection id, so this arm failing means validate() rejects on something "+
			"coarser than the witness — and a validator that rejects everything would satisfy "+
			"the phantom assertion above", watched, err, elsewhere)
	}
}

// TestChangelogPayloadCarriesTheCollectionIdTheWitnessMatchesOn is the wire half of the
// witness arm, one layer below the fixture above.
//
// The SSI validation window is not built from in-memory KeyChanges: the committer DECODES
// `CommitReq.ChangelogPayload` on every commit to build `pending`, the recent-changes ring
// and the change feed (see CommitReq's doc and `decodePayload`). validate()'s witness arm
// then matches `rs.collWitness[ch.Coll]` on the DECODED change. A collection id that does
// not survive the round-trip therefore disables the witness arm globally — every phantom
// insert reads as a clean commit — without a single line of `validate.go` being touched.
func TestChangelogPayloadCarriesTheCollectionIdTheWitnessMatchesOn(t *testing.T) {
	in := []KeyChange{
		{Coll: CollID(7), Pk: []byte("things\x1fz"), Op: OpPut, Record: []byte("Z")},
		{Coll: CollID(8), Pk: []byte("others\x1fq"), Op: OpDelete},
	}

	out, err := DecodeChangelogPayload(EncodeChangelogPayload(in))
	if err != nil {
		t.Fatalf("the payload this package encodes does not decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("round-trip returned %d change(s), want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Coll != in[i].Coll {
			t.Fatalf("change %d came back carrying collection id %d, want %d. "+
				"The SSI validation window is built by DECODING this payload, so a collection id "+
				"that does not survive the wire makes validate()'s collection-witness arm match "+
				"nothing at all: every phantom insert then reads as a clean commit, with no line "+
				"of validate.go altered", i, out[i].Coll, in[i].Coll)
		}
		if !bytes.Equal(out[i].Pk, in[i].Pk) || out[i].Op != in[i].Op {
			t.Fatalf("change %d came back as pk=%q op=%v, want pk=%q op=%v — the point arm matches "+
				"on Pk and the window's op drives the ring, so neither may drift either",
				i, out[i].Pk, out[i].Op, in[i].Pk, in[i].Op)
		}
	}
}
