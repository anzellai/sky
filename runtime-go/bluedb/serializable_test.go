package bluedb

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// ── test infrastructure ─────────────────────────────────────────────────────────────────

func newSSIEngine(t *testing.T) *pebbleEngine {
	t.Helper()
	e, err := openWith(config{dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// statusIdx is a single secondary index over a row's status text (the record IS the status
// bytes in these tests). indexer maps a row → its status coordinate.
const statusIdx = IndexID(1)

func statusIndexer(_, rec []byte) []IndexCoord {
	return []IndexCoord{{Index: statusIdx, Key: encodeIndexKey(statusIdx, ColText, rec)}}
}

// blindPutReq builds a blind (ReadSet==nil) commit that carries a KeyChange payload so a
// concurrent open transaction can validate against it.
func blindPutReq(pk, val string, coords []IndexCoord) CommitReq {
	chg := KeyChange{Coll: 0, Pk: []byte(pk), Op: OpPut, Record: []byte(val), NewIndex: coords}
	return CommitReq{
		Writes:           []VersionedWrite{{UserKey: []byte(pk), Op: OpPut, Value: []byte(val)}},
		ChangelogPayload: EncodeChangelogPayload([]KeyChange{chg}),
	}
}

// countDataVersions counts stored MVCC versions of userKey (T8 — "exactly one data Set").
func countDataVersions(t *testing.T, e *pebbleEngine, userKey string) int {
	t.Helper()
	prefix := dataKeyPrefix([]byte(userKey)) // 0x00 ‖ userKey ‖ 0x00 (== key[:Split])
	it, err := e.db.NewIter(&pebble.IterOptions{LowerBound: []byte{tagData}, UpperBound: []byte{tagChangelog}})
	if err != nil {
		t.Fatalf("iter: %v", err)
	}
	defer it.Close()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		k := it.Key()
		if bytesEqual(k[:skydbSplit(k)], prefix) {
			n++
		}
	}
	return n
}

// submitForTest enqueues a raw job without blocking on the result (manualDrain mode).
func (e *pebbleEngine) submitForTest(req CommitReq) chan CommitResult {
	job := &commitJob{req: req, done: make(chan CommitResult, 1)}
	e.ch <- job
	return job.done
}

// drainOnceForTest drains ALL buffered jobs into ONE batch and processes it (manualDrain
// mode) — the deterministic single-batch seam for the intra-batch `pending` test.
func (e *pebbleEngine) drainOnceForTest() {
	var batch []*commitJob
	for {
		select {
		case j := <-e.ch:
			batch = append(batch, j)
		default:
			if len(batch) > 0 {
				e.process(batch)
			}
			return
		}
	}
}

// ── T1 — predicate phantom / write-skew REJECTED (SI would ACCEPT) ──────────────────────

func TestT1_PredicatePhantomRejected(t *testing.T) {
	e := newSSIEngine(t)

	// tx1 scans WHERE status='open' at readTs — even with ZERO rows it records the RANGE.
	tx1, _ := e.Begin()
	tx1.SetIndexer(statusIndexer)
	lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
	cur := tx1.Scan(statusIdx, lo, hi)
	n := 0
	for cur.Next() {
		n++
	}
	cur.Close()
	if n != 0 {
		t.Fatalf("tx1 scan should find 0 open rows, found %d", n)
	}

	// tx2 inserts a matching row and commits FIRST.
	tx2, _ := e.Begin()
	tx2.SetIndexer(statusIndexer)
	if err := tx2.Put([]byte("r1"), []byte("open")); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit: %v", err)
	}

	// tx1 tries to insert another open row → its scanned range now contains tx2's NewIndex
	// coord → CONFLICT (SI would accept: tx1's key-set was empty).
	if err := tx1.Put([]byte("r2"), []byte("open")); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict (predicate phantom), got %v", err)
	}

	// After a retry, the invariant ("at most one open") holds: the retry sees r1 and declines.
	inserts := 0
	err := e.Transact(func(tx *Txn) error {
		tx.SetIndexer(statusIndexer)
		c := tx.Scan(statusIdx, lo, hi)
		found := false
		for c.Next() {
			found = true
		}
		c.Close()
		if found {
			return nil // an open row already exists → do not insert a second
		}
		inserts++
		return tx.Put([]byte("r3"), []byte("open"))
	})
	if err != nil {
		t.Fatalf("retry txn: %v", err)
	}
	if inserts != 0 {
		t.Fatalf("invariant violated: retry inserted a second open row (%d)", inserts)
	}
}

// ── phantom-disappears (delete-out-of-range) REJECTED via OldIndex ──────────────────────

func TestPhantomDisappearsRejected(t *testing.T) {
	e := newSSIEngine(t)
	// Seed one open row.
	tx0, _ := e.Begin()
	tx0.SetIndexer(statusIndexer)
	_ = tx0.Put([]byte("r1"), []byte("open"))
	if err := tx0.Commit(); err != nil {
		t.Fatal(err)
	}

	// tx1 scans open rows, sees r1, decides to keep a banner.
	tx1, _ := e.Begin()
	tx1.SetIndexer(statusIndexer)
	lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
	c := tx1.Scan(statusIdx, lo, hi)
	seen := 0
	for c.Next() {
		seen++
	}
	c.Close()
	if seen != 1 {
		t.Fatalf("tx1 should see 1 open row, saw %d", seen)
	}

	// tx2 closes that row (status open → closed) and commits — its OldIndex vacates open.
	tx2, _ := e.Begin()
	tx2.SetIndexer(statusIndexer)
	_ = tx2.Put([]byte("r1"), []byte("closed"))
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	// tx1 commits (a no-op write to record its decision) → conflict via OldIndex in range.
	_ = tx1.Put([]byte("banner"), []byte("shown"))
	if err := tx1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict (phantom disappears via OldIndex), got %v", err)
	}
}

// ── T20 — phantom under a DESCENDING index (proves invert+swap) ─────────────────────────

func TestT20_PhantomUnderDescendingIndex(t *testing.T) {
	e := newSSIEngine(t)
	// A descending int "priority" index. record bytes = BE8 priority.
	const prioIdx = IndexID(2)
	descIndexer := func(_, rec []byte) []IndexCoord {
		return []IndexCoord{{Index: prioIdx, Key: encodeIndexKey(prioIdx, Descending(ColInt), rec)}}
	}

	// tx1 scans priorities in [10, 20] (descending index) — zero rows.
	tx1, _ := e.Begin()
	tx1.SetIndexer(descIndexer)
	c := tx1.ScanRange(prioIdx, Descending(ColInt), IntKey(10), IntKey(20))
	for c.Next() {
	}
	c.Close()

	// tx2 inserts a row with priority 15 (inside the descending band) and commits.
	tx2, _ := e.Begin()
	tx2.SetIndexer(descIndexer)
	_ = tx2.Put([]byte("job1"), IntKey(15))
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	// tx1 commits → the coord (invert(15)) lands in the swapped [invert(20), invert(10)]
	// interval → CONFLICT. Proves the descending invert+swap coordination.
	_ = tx1.Put([]byte("marker"), IntKey(0))
	if err := tx1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict under descending index, got %v", err)
	}
}

// ── T21 — conservative fallback (real/money/blob) over-rejects, never under-rejects ─────

func TestT21_ConservativeFallback(t *testing.T) {
	e := newSSIEngine(t)
	const priceIdx = IndexID(3)
	const coll = CollID(9)
	// A "real" priced index — no order-preserving encoding, so the txn uses ScanFallback,
	// recording an index-level witness. The indexer still emits a coord on priceIdx.
	fallbackIndexer := func(_, rec []byte) []IndexCoord {
		return []IndexCoord{{Index: priceIdx, Key: encodeIndexKey(priceIdx, ColReal, rec)}}
	}

	// tx1 does a fallback scan over the price index (a range predicate we can't byte-encode).
	tx1, _ := e.Begin()
	tx1.SetIndexer(fallbackIndexer)
	tx1.SetCollection(coll)
	c := tx1.ScanFallback(priceIdx, func(_, rec []byte) bool { return true })
	for c.Next() {
	}
	c.Close()

	// tx2 inserts ANY row into that index/collection and commits.
	tx2, _ := e.Begin()
	tx2.SetIndexer(fallbackIndexer)
	tx2.SetCollection(coll)
	_ = tx2.Put([]byte("p1"), []byte("3.14"))
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	// tx1 commits → the index-level witness conflicts (over-reject; NEVER under-reject).
	_ = tx1.Put([]byte("marker"), []byte("x"))
	if err := tx1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("fallback should conflict on any change to the witnessed index, got %v", err)
	}

	// Collection-level witness variant: a txn witnessing the whole collection conflicts with
	// any change to it (the coarsest safe witness) — proves no under-reject for IS-NULL-class.
	txA, _ := e.Begin()
	txA.WitnessCollection(coll)
	txB, _ := e.Begin()
	txB.SetCollection(coll)
	txB.SetIndexer(fallbackIndexer)
	_ = txB.Put([]byte("p2"), []byte("9.99"))
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = txA.Put([]byte("marker2"), []byte("x"))
	if err := txA.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("collection witness should conflict on any change to the collection, got %v", err)
	}
}

// ── point write-skew + lost-update REJECTED ─────────────────────────────────────────────

func TestPointWriteSkewRejected(t *testing.T) {
	e := newSSIEngine(t)
	// x=100, y=100; invariant x+y>=0.
	seed, _ := e.Begin()
	_ = seed.Put([]byte("x"), []byte("100"))
	_ = seed.Put([]byte("y"), []byte("100"))
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}

	tx1, _ := e.Begin()
	_, _ = tx1.Get([]byte("x"))
	_, _ = tx1.Get([]byte("y"))
	tx2, _ := e.Begin()
	_, _ = tx2.Get([]byte("x"))
	_, _ = tx2.Get([]byte("y"))

	_ = tx1.Put([]byte("x"), []byte("-50"))
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	// tx2 read x (now changed by tx1) → x in window → conflict (SI would accept, x+y=-110).
	_ = tx2.Put([]byte("y"), []byte("-60"))
	if err := tx2.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict (write-skew), got %v", err)
	}
}

func TestLostUpdatePrevented(t *testing.T) {
	e := newSSIEngine(t)
	seed, _ := e.Begin()
	_ = seed.Put([]byte("counter"), []byte("5"))
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}

	// Two concurrent increments; the second must retry and land on start+2.
	var wg sync.WaitGroup
	inc := func() {
		defer wg.Done()
		_ = e.Transact(func(tx *Txn) error {
			v, _ := tx.Get([]byte("counter"))
			n := 0
			fmt.Sscanf(string(v), "%d", &n)
			return tx.Put([]byte("counter"), []byte(fmt.Sprintf("%d", n+1)))
		})
	}
	wg.Add(2)
	go inc()
	go inc()
	wg.Wait()

	fin, _ := e.Begin()
	v, _ := fin.Get([]byte("counter"))
	fin.Abort()
	if string(v) != "7" {
		t.Fatalf("lost update: counter=%s, want 7 (5+2)", v)
	}
}

// ── T15 — read-your-writes + self-upsert does NOT self-conflict ─────────────────────────

func TestT5_ReadYourWrites(t *testing.T) {
	e := newSSIEngine(t)
	tx, _ := e.Begin()
	tx.SetIndexer(statusIndexer)
	_ = tx.Put([]byte("k"), []byte("v1"))
	if v, ok := tx.Get([]byte("k")); !ok || string(v) != "v1" {
		t.Fatalf("read-your-writes: got %q ok=%v", v, ok)
	}
	_ = tx.Delete([]byte("k"))
	if _, ok := tx.Get([]byte("k")); ok {
		t.Fatal("read-your-writes: deleted key should read absent")
	}
	tx.Abort()
}

func TestT15_SelfUpsertNoConflict(t *testing.T) {
	e := newSSIEngine(t)
	seed, _ := e.Begin()
	_ = seed.Put([]byte("row"), []byte("v1"))
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	// A txn reads row then updates it — with NO concurrent writer, it must NOT self-conflict.
	tx, _ := e.Begin()
	_, _ = tx.Get([]byte("row"))
	_ = tx.Put([]byte("row"), []byte("v2"))
	if err := tx.Commit(); err != nil {
		t.Fatalf("self-upsert should not conflict, got %v", err)
	}
	// read-then-write records the read → a CONCURRENT writer is caught.
	txA, _ := e.Begin()
	_, _ = txA.Get([]byte("row"))
	txB, _ := e.Begin()
	_ = txB.Put([]byte("row"), []byte("v3"))
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = txA.Put([]byte("row"), []byte("v4"))
	if err := txA.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("read-then-write should catch a concurrent writer, got %v", err)
	}
}

// ── T22 — window boundary: readTs = durableHi, atomic pin ───────────────────────────────

func TestT22_WindowBoundaryDurableHi(t *testing.T) {
	e := newSSIEngine(t)
	// Commit W durably.
	r := e.Commit(blindPutReq("r1", "open", statusIndexer(nil, []byte("open"))))
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	cW := r.CommitTs

	tx, _ := e.Begin()
	// readTs MUST be sourced from durableHi (== W's commitTs), NOT a higher assigned ts.
	if tx.readTs != cW {
		t.Fatalf("readTs %v != durableHi/cW %v (R-2.8)", tx.readTs, cW)
	}
	if tx.readTs != e.durableHi() {
		t.Fatalf("readTs %v != durableHi %v", tx.readTs, e.durableHi())
	}
	// Everything ≤ readTs is in the snapshot: tx sees W.
	if v, ok := tx.Get([]byte("r1")); !ok || string(v) != "open" {
		t.Fatalf("snapshot should include W: got %q ok=%v", v, ok)
	}

	// W2 commits AFTER tx began (commitTs > readTs) → in the window, not the snapshot.
	if r2 := e.Commit(blindPutReq("r1", "closed", statusIndexer(nil, []byte("closed")))); r2.Err != nil {
		t.Fatal(r2.Err)
	}
	// tx read r1 → r1 in window → conflict (the boundary commit is NOT missed).
	_ = tx.Put([]byte("marker"), []byte("x"))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("boundary commit at > readTs must be caught, got %v", err)
	}
}

// ── T8/T24 — blind fast path: zero validation, one data-version Set ──────────────────────

func TestT8_BlindWriteNoValidation(t *testing.T) {
	e := newSSIEngine(t)
	before := validateCalls.Load()
	r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte("b1"), Op: OpPut, Value: []byte("v")}}})
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := validateCalls.Load() - before; got != 0 {
		t.Fatalf("blind write drove %d validate() calls, want 0", got)
	}
	if n := countDataVersions(t, e, "b1"); n != 1 {
		t.Fatalf("blind put should be exactly one data-version Set, got %d", n)
	}
}

func TestT24_AllBlindBatchZeroSSI(t *testing.T) {
	e := newSSIEngine(t)
	before := validateCalls.Load()
	// A concurrent burst of blind writes → committer forms all-blind batches → processBlindPhase1.
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("k%04d", i)), Op: OpPut, Value: []byte("v")}}})
		}(i)
	}
	wg.Wait()
	if got := validateCalls.Load() - before; got != 0 {
		t.Fatalf("all-blind batches drove %d validate() calls, want 0 (Fix-6)", got)
	}
}

// ── T6/T7 — retry then success; retry bound → typed ErrConflict ─────────────────────────

func TestT6_RetryOnConflictThenSuccess(t *testing.T) {
	e := newSSIEngine(t)
	seed, _ := e.Begin()
	_ = seed.Put([]byte("k"), []byte("0"))
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	// One interfering writer commits between this txn's first Begin and Commit; the second
	// attempt (fresh snapshot) succeeds.
	var interfered atomic.Bool
	err := e.Transact(func(tx *Txn) error {
		v, _ := tx.Get([]byte("k"))
		if interfered.CompareAndSwap(false, true) {
			// commit a conflicting write out-of-band so THIS attempt loses the race.
			ix, _ := e.Begin()
			_ = ix.Put([]byte("k"), []byte("interfere"))
			_ = ix.Commit()
		}
		return tx.Put([]byte("k"), []byte(string(v)+"!"))
	})
	if err != nil {
		t.Fatalf("retry-then-success should return nil, got %v", err)
	}
}

func TestT7_RetryBoundReturnsTypedConflict(t *testing.T) {
	e := newSSIEngine(t)
	seed, _ := e.Begin()
	_ = seed.Put([]byte("k"), []byte("0"))
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	// A body that is ALWAYS beaten: on every attempt, an out-of-band writer commits after the
	// read, so validation always finds k in the window → perpetual conflict → typed ErrConflict.
	err := e.Transact(func(tx *Txn) error {
		_, _ = tx.Get([]byte("k"))
		ix, _ := e.Begin()
		_ = ix.Put([]byte("k"), []byte("beat"))
		_ = ix.Commit()
		return tx.Put([]byte("k"), []byte("mine"))
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("perpetual conflict should return typed ErrConflict after the bound, got %v", err)
	}
}

// ── T11 — intra-batch conflict (validated against `pending`, manualDrain single batch) ───

func TestT11_IntraBatchConflict(t *testing.T) {
	e, err := openWith(config{dir: t.TempDir(), manualDrain: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// Seed x=100,y=100 via a manually-drained batch.
	seed, _ := e.Begin()
	_ = seed.Put([]byte("x"), []byte("100"))
	_ = seed.Put([]byte("y"), []byte("100"))
	sd := e.submitForTest(seed.buildReq())
	seed.reader.Close()
	e.drainOnceForTest()
	if r := <-sd; r.Err != nil {
		t.Fatal(r.Err)
	}

	// Two write-skew txns at the SAME readTs, funneled into ONE batch.
	tx1, _ := e.Begin()
	_, _ = tx1.Get([]byte("x"))
	_, _ = tx1.Get([]byte("y"))
	_ = tx1.Put([]byte("x"), []byte("-50"))

	tx2, _ := e.Begin()
	_, _ = tx2.Get([]byte("x"))
	_, _ = tx2.Get([]byte("y"))
	_ = tx2.Put([]byte("x"), []byte("-70")) // both write x → the second in-batch conflicts on it

	d1 := e.submitForTest(tx1.buildReq())
	d2 := e.submitForTest(tx2.buildReq())
	tx1.reader.Close()
	tx2.reader.Close()
	e.drainOnceForTest() // ONE batch: tx2 validates against pending (tx1's change)

	r1, r2 := <-d1, <-d2
	ok1, ok2 := r1.Err == nil, r2.Err == nil
	if ok1 == ok2 {
		t.Fatalf("exactly one should commit; got tx1.err=%v tx2.err=%v", r1.Err, r2.Err)
	}
	loser := r2.Err
	if ok2 {
		loser = r1.Err
	}
	if !errors.Is(loser, ErrConflict) {
		t.Fatalf("the in-batch loser should get ErrConflict, got %v", loser)
	}
}

// ── T16 — validation for a fresh-readTs txn is ring-served (zero Changelog.Tail scans) ──

func TestT16_ValidationOffPebbleHotpath(t *testing.T) {
	e := newSSIEngine(t)
	// Prime some committed changes so the ring is non-empty.
	for i := 0; i < 5; i++ {
		tx, _ := e.Begin()
		tx.SetIndexer(statusIndexer)
		_ = tx.Put([]byte(fmt.Sprintf("r%d", i)), []byte("open"))
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	before := changelogTailCalls.Load()
	// A fresh-readTs txn (readTs = current durableHi) validates entirely off the in-RAM ring.
	tx, _ := e.Begin()
	tx.SetIndexer(statusIndexer)
	lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
	c := tx.Scan(statusIdx, lo, hi)
	for c.Next() {
	}
	c.Close()
	_ = tx.Put([]byte("new"), []byte("open"))
	if err := tx.Commit(); err != nil {
		t.Fatalf("fresh txn commit: %v", err)
	}
	if got := changelogTailCalls.Load() - before; got != 0 {
		t.Fatalf("fresh-readTs validation drove %d Changelog.Tail scans, want 0 (ring-served)", got)
	}
}

// ── T23 — GC concurrent with the committer: -race clean (trim marshalled) ────────────────

func TestT23_RingTrimAppendRace(t *testing.T) {
	e := newSSIEngine(t)
	var stop atomic.Bool

	// GC hammer — enqueues trim requests concurrent with the committer's append/after.
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for !stop.Load() {
			_, _ = e.GC()
		}
	}()

	// Concurrent transactional commits (drive ring append + validation on the committer).
	var writers sync.WaitGroup
	for wid := 0; wid < 4; wid++ {
		writers.Add(1)
		go func(wid int) {
			defer writers.Done()
			for i := 0; i < 100; i++ {
				tx, _ := e.Begin()
				tx.SetIndexer(statusIndexer)
				_ = tx.Put([]byte(fmt.Sprintf("w%d-%d", wid, i)), []byte("open"))
				_ = tx.Commit()
			}
		}(wid)
	}
	writers.Wait()
	stop.Store(true)
	gcWG.Wait()
	// The value: `go test -race` reports NO data race on the ring (trim is marshalled onto
	// the committer, so append/after/trim are all single-goroutine).
}
