package bluedb

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// contention_test.go — Phase 2b contention conformance (§6 hot-key strict-2PL lease + Fix-8
// ring cap/spill). Every concurrent test is wrapped in a bounded-time guard: a HUNG test is a
// DEADLOCK, so the guard fires t.Fatalf rather than letting the run hang.

// runBounded runs fn in a goroutine and fails the test if it does not finish within d — a hung
// (deadlocked/starved) workload trips the timeout instead of hanging the whole run.
func runBounded(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: timed out after %v — a hung test is a deadlock/starvation", name, d)
	}
}

// readIntKey reads a counter stored as decimal-string bytes; absent ⇒ 0.
func readIntKey(t *testing.T, e *pebbleEngine, key string) int {
	t.Helper()
	r, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer r.Close()
	v, _, ok := r.Get([]byte(key))
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(string(v))
	if err != nil {
		t.Fatalf("parse counter %q: %v", v, err)
	}
	return n
}

// incrBody is a pure read-modify-write on one point key (a NON-shardable counter). It records a
// point read (Get) + a write (Put, whose pre-image is also a point read), so a concurrent
// commit on the same key conflicts → the committer records the abort → the key goes hot.
func incrBody(key string) func(*Txn) error {
	return func(tx *Txn) error {
		n := 0
		if v, ok := tx.Get([]byte(key)); ok {
			var err error
			if n, err = strconv.Atoi(string(v)); err != nil {
				return err
			}
		}
		return tx.Put([]byte(key), []byte(strconv.Itoa(n+1)))
	}
}

// ── T25 — hot-key no-starvation: many RMW txns on ONE contended key all commit ─────────────
//
// The point-key lease (§6) makes a genuinely-contended counter starvation-free: EVERY txn
// eventually commits (none returns ErrConflict), and the final value equals the exact number
// of increments (no lost update). It also asserts the lease path actually engaged.
func TestT25_HotKeyNoStarvation(t *testing.T) {
	e := newSSIEngine(t)
	leasePathCalls.Store(0)

	const workers, perWorker = 16, 25
	const key = "counter"

	var conflicts int64
	runBounded(t, 60*time.Second, "hot-key no-starvation", func() {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					if err := e.Transact(incrBody(key)); err != nil {
						atomic.AddInt64(&conflicts, 1) // MUST stay 0 — the lease prevents starvation
					}
				}
			}()
		}
		wg.Wait()
	})

	if c := atomic.LoadInt64(&conflicts); c != 0 {
		t.Fatalf("hot-key contention starved %d txns (returned ErrConflict) — the lease must guarantee commit", c)
	}
	if got, want := readIntKey(t, e, key), workers*perWorker; got != want {
		t.Fatalf("counter = %d, want %d (a lost update = a broken serializable guarantee)", got, want)
	}
	if leasePathCalls.Load() == 0 {
		t.Fatal("expected the hot-key strict-2PL lease path to engage under heavy point contention, but it never did")
	}
}

// ── T26 — multi-hot-key no-deadlock (the grill's X<Y case) ─────────────────────────────────
//
// Txns touch {X,Y} and {Y,X}. Both keys are hot. The strict-2PL WHOLE-SET canonical-order
// acquisition (§6.3/§6.4) — never acquire-on-discovery — makes this deadlock-free: every txn
// acquires X's lease before Y's regardless of the order its body touches them, so no cycle.
// All txns commit, both counters land exact, and the run finishes in bounded time (a deadlock
// would trip the guard).
func TestT26_MultiHotKeyNoDeadlock(t *testing.T) {
	e := newSSIEngine(t)
	leasePathCalls.Store(0)

	const x, y = "x", "y" // "x" < "y" in bytes.Compare → canonical acquire order is X then Y
	const workers, perWorker = 8, 15

	// bodyXY touches X before Y; bodyYX touches Y before X — opposite orders, the grill's
	// deadlock shape. Canonical-order acquisition must serialize both on X first.
	bodyXY := func(tx *Txn) error {
		nx := 0
		if v, ok := tx.Get([]byte(x)); ok {
			nx, _ = strconv.Atoi(string(v))
		}
		ny := 0
		if v, ok := tx.Get([]byte(y)); ok {
			ny, _ = strconv.Atoi(string(v))
		}
		if err := tx.Put([]byte(x), []byte(strconv.Itoa(nx+1))); err != nil {
			return err
		}
		return tx.Put([]byte(y), []byte(strconv.Itoa(ny+1)))
	}
	bodyYX := func(tx *Txn) error {
		ny := 0
		if v, ok := tx.Get([]byte(y)); ok {
			ny, _ = strconv.Atoi(string(v))
		}
		nx := 0
		if v, ok := tx.Get([]byte(x)); ok {
			nx, _ = strconv.Atoi(string(v))
		}
		if err := tx.Put([]byte(y), []byte(strconv.Itoa(ny+1))); err != nil {
			return err
		}
		return tx.Put([]byte(x), []byte(strconv.Itoa(nx+1)))
	}

	var conflicts int64
	runBounded(t, 90*time.Second, "multi-hot-key no-deadlock", func() {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			body := bodyXY
			if w%2 == 1 {
				body = bodyYX // half the workers touch Y before X
			}
			wg.Add(1)
			go func(b func(*Txn) error) {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					if err := e.Transact(b); err != nil {
						atomic.AddInt64(&conflicts, 1)
					}
				}
			}(body)
		}
		wg.Wait()
	})

	if c := atomic.LoadInt64(&conflicts); c != 0 {
		t.Fatalf("multi-hot-key contention starved %d txns — strict-2PL must guarantee commit", c)
	}
	total := workers * perWorker
	if gx, gy := readIntKey(t, e, x), readIntKey(t, e, y); gx != total || gy != total {
		t.Fatalf("x=%d y=%d, want both %d (lost update under multi-key contention)", gx, gy, total)
	}
	if leasePathCalls.Load() == 0 {
		t.Fatal("expected the multi-key lease path to engage, but it never did")
	}
}

// ── T27 — range contention → bounded retry + typed ErrConflict, NO lease (honest) ──────────
//
// Predicate contention has NO lease (§6.4 — you cannot enqueue on a predicate). A perpetually
// invalidated scan therefore stays on bounded optimistic retry and returns a typed ErrConflict
// after the bound — it does NOT hang, and it NEVER enters the lease path. Two parts: (1) a
// deterministic single-attempt range conflict proving a range culprit is not promoted hot;
// (2) a victim under a live flood proving bounded retry → ErrConflict with zero lease calls.
func TestT27_RangeContentionBoundedRetryNoLease(t *testing.T) {
	e := newSSIEngine(t)

	// Part 1 — deterministic: a range conflict must NOT feed hot-key detection (its culprit is
	// the changed row's Pk, not a key the victim point-read). So the inserted pks stay cold.
	for i := 0; i < 4; i++ {
		victim, _ := e.Begin()
		victim.SetIndexer(statusIndexer)
		lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
		cur := victim.Scan(statusIdx, lo, hi)
		for cur.Next() {
		}
		cur.Close()

		insPk := fmt.Sprintf("ins-%d", i)
		other, _ := e.Begin()
		other.SetIndexer(statusIndexer)
		if err := other.Put([]byte(insPk), []byte("open")); err != nil {
			t.Fatal(err)
		}
		if err := other.Commit(); err != nil {
			t.Fatalf("insert commit: %v", err)
		}

		if err := victim.Put([]byte(fmt.Sprintf("v-%d", i)), []byte("open")); err != nil {
			t.Fatal(err)
		}
		if err := victim.Commit(); !errors.Is(err, ErrConflict) {
			t.Fatalf("range phantom must conflict, got %v", err)
		}
		if e.hotKeys.isHot([]byte(insPk)) {
			t.Fatalf("a RANGE conflict promoted %q to hot — predicate contention must never be leased", insPk)
		}
	}

	// Part 2 — a victim under a live flood of matching inserts: bounded retry → ErrConflict,
	// zero lease-path calls, bounded time.
	leasePathCalls.Store(0)
	stop := make(chan struct{})
	flooderDone := make(chan struct{})
	var flooded int64
	go func() { // continuous flood of unique 'open' rows → invalidates any 'open' scan window
		defer close(flooderDone)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			pk := fmt.Sprintf("flood-%d", i)
			i++
			coords := statusIndexer(nil, []byte("open"))
			if e.Commit(blindPutReq(pk, "open", coords)).Err == nil {
				atomic.AddInt64(&flooded, 1)
			}
		}
	}()

	victimBody := func(tx *Txn) error {
		tx.SetIndexer(statusIndexer)
		lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
		cur := tx.Scan(statusIdx, lo, hi)
		for cur.Next() {
		}
		cur.Close()
		return tx.Put([]byte("victim-row"), []byte("open"))
	}

	var victimErr error
	runBounded(t, 30*time.Second, "range contention bounded retry", func() {
		victimErr = e.Transact(victimBody)
	})
	close(stop)
	<-flooderDone // let the flood fully stop before the engine is Closed (Cleanup) — no Commit/Close race

	if !errors.Is(victimErr, ErrConflict) {
		t.Fatalf("range contention should degrade to a typed ErrConflict, got %v (flooded=%d)", victimErr, flooded)
	}
	if lp := leasePathCalls.Load(); lp != 0 {
		t.Fatalf("range/predicate contention took the lease path %d times — it must never be leased (§6.4)", lp)
	}
}

// ── T28 — lease timeout backstop: committer reclaims a crashed holder's lease ───────────────
//
// Release is driver-side, so a driver that crashes holding a lease would wedge the FIFO queue
// forever. The committer-side reaper reclaims a lease held past the timeout so the next waiter
// proceeds (§6.3). Here a "crashed driver" acquires a lease and never releases it; a waiter must
// still be granted after the reaper reclaims the stale holder.
func TestT28_LeaseTimeoutBackstop(t *testing.T) {
	const timeout = 80 * time.Millisecond
	e, err := openWith(config{dir: t.TempDir(), leaseTimeout: timeout})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	// "Crashed" driver: acquire the lease, hold it, NEVER release.
	crashed := e.leases.acquire("hot")
	<-crashed.granted

	// A waiter enqueues behind the crashed holder. Without the reaper it would block forever.
	waiter := e.leases.acquire("hot")
	runBounded(t, 5*timeout, "lease timeout backstop", func() {
		<-waiter.granted // granted only after the reaper reclaims the crashed holder
	})
	e.leases.release(waiter)
}

// ── T29 — ring cap + spill: a spilled validation rejects a phantom IDENTICALLY (Fix-8) ─────
//
// A small ring cap forces the phantom's ring entry to spill below a lagging reader's readTs.
// The reader's validation then falls back to Changelog.Tail and must reject the phantom EXACTLY
// as an un-capped ring would (no under-reject introduced by the cap). Run capped (spill path,
// changelogTailCalls > 0) and uncapped (ring path, no tail scan); both MUST reject.
func TestT29_RingCapSpillIdenticalValidation(t *testing.T) {
	run := func(t *testing.T, capEntries int, wantSpill bool) {
		e, err := openWith(config{dir: t.TempDir(), maxRingEntries: capEntries})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer e.Close()

		// A lagging reader opened at readTs=durableHi (== {0,0} on a fresh store), BEFORE the
		// phantom — the reader whose window must still see the phantom after a spill.
		tx1, _ := e.Begin()
		tx1.SetIndexer(statusIndexer)
		lo, hi := encodeScanRange(statusIdx, ColText, []byte("open"), []byte("open"))
		cur := tx1.Scan(statusIdx, lo, hi)
		for cur.Next() {
		}
		cur.Close()

		// The phantom: an 'open' row that falls in tx1's scanned range.
		if r := e.Commit(blindPutReq("r1", "open", statusIndexer(nil, []byte("open")))); r.Err != nil {
			t.Fatalf("phantom commit: %v", r.Err)
		}
		// Flood the ring past the cap with NON-matching 'closed' rows so the phantom's entry
		// spills below tx1.readTs but no filler itself conflicts tx1's range — proving the spill
		// returns EXACTLY the conflicting phantom (identical to the ring).
		for i := 0; i < 6; i++ {
			pk := fmt.Sprintf("closed-%d", i)
			if r := e.Commit(blindPutReq(pk, "closed", statusIndexer(nil, []byte("closed")))); r.Err != nil {
				t.Fatalf("filler commit: %v", r.Err)
			}
		}

		changelogTailCalls.Store(0)
		if err := tx1.Put([]byte("r2"), []byte("open")); err != nil {
			t.Fatal(err)
		}
		err = tx1.Commit()
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("phantom must conflict whether validated via the ring OR the spill, got %v", err)
		}
		if gotSpill := changelogTailCalls.Load() > 0; gotSpill != wantSpill {
			t.Fatalf("spill path taken=%v, want %v (cap=%d)", gotSpill, wantSpill, capEntries)
		}
	}

	t.Run("capped-spills-to-changelog-tail", func(t *testing.T) { run(t, 4, true) })
	t.Run("uncapped-served-from-ring", func(t *testing.T) { run(t, 0, false) }) // 0 ⇒ default (huge) cap
}

// ── T30 — ring cap unit: append past the cap spills oldest + raises the floor (Fix-8) ──────
func TestT30_RingCapSpillRaisesFloor(t *testing.T) {
	r := newRecentRing()
	r.maxEntries = 3
	mk := func(w uint64) HLC { return HLC{WallMs: w} }
	for _, w := range []uint64{10, 20, 30} {
		r.append(mk(w), []KeyChange{{Pk: []byte{byte(w)}}})
	}
	// Under cap: a fresh-enough reader is served from the ring with no spill.
	if got, spilled := r.after(mk(5)); spilled || len(got) != 3 {
		t.Fatalf("at cap: after(5) got %d spilled=%v, want 3 changes no spill", len(got), spilled)
	}

	r.append(mk(40), []KeyChange{{Pk: []byte{40}}}) // 4th entry over cap 3 → spill oldest (10), floor→20
	if !r.floor.Less(mk(21)) || r.floor.Less(mk(20)) {
		t.Fatalf("floor = %+v, want 20 after spilling the oldest entry", r.floor)
	}
	if _, spilled := r.after(mk(15)); !spilled {
		t.Fatal("a readTs below the raised floor must report spilled=true (→ Changelog.Tail fallback)")
	}
	if got, spilled := r.after(mk(25)); spilled || len(got) != 2 {
		t.Fatalf("after(25): got %d spilled=%v, want 2 (commits 30 and 40 are > 25, both retained)", len(got), spilled)
	}
	if len(r.entries) != 3 {
		t.Fatalf("ring holds %d entries, want the cap of 3", len(r.entries))
	}
}
