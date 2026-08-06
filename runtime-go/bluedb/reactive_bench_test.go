package bluedb

// reactive_bench_test.go — Phase-4c NB-1 (realistic-N fan-out) + NB-3 (resync thundering-herd
// bound). These CLOSE the two non-blocking findings the design forbade deferring:
//
//   NB-1: match DETECTION is N-INDEPENDENT (O(changes × distinct-predicates), not subs × changes).
//         Proven by the reactiveMatchDecodes / reactiveMatchEvals counters staying flat as the
//         subscriber count grows 40×. Delivery is the honest O(N) LiveView floor — characterized,
//         not hidden.
//   NB-3: a resync storm (buffer overflow → every affected sub re-queries) is BOUNDED: per-sub
//         coalescing collapses a drop-burst to ONE re-query per sub, and the global re-query
//         semaphore caps concurrent engine scans at K = min(GOMAXPROCS, 8).
//
// Run: go test ./bluedb/ -run 'NB1|NB3' -v   and   go test ./bluedb/ -bench ReactiveFanout -run x

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// newMemBackendTB is newMemBackend for either a *testing.T or *testing.B.
func newMemBackendTB(tb testing.TB) *EmbeddedBackend {
	tb.Helper()
	e, err := openWith(config{dir: "mem", fs: vfs.NewMem()})
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = e.Close() })
	b := NewEmbeddedBackend(e)
	b.Register(ordersSchema())
	return b
}

// registerWatchers registers n same-query subs and drains each so a full deliver buffer never
// blocks — we measure detection, not backpressure. Returns the backend.
func registerWatchers(tb testing.TB, b *EmbeddedBackend, n int, plan QueryPlan) {
	tb.Helper()
	for i := 0; i < n; i++ {
		sub, _, err := b.WatchTenant(ordersSchema(), plan, "")
		if err != nil {
			tb.Fatalf("WatchTenant %d: %v", i, err)
		}
		tb.Cleanup(sub.Close)
		go func(s *subscription) {
			for range s.Changes() {
			}
		}(sub)
	}
}

// firehose writes `changes` distinct open orders through the real autocommit commit path (each a
// distinct commitTs + pk ⇒ no dedup) and waits until the async reactive pump has processed all of
// them (observed via the decode counter reaching the target). Returns the observed (decodes, evals).
func firehose(tb testing.TB, b *EmbeddedBackend, changes int) (uint64, uint64) {
	tb.Helper()
	atomic.StoreUint64(&reactiveMatchDecodes, 0)
	atomic.StoreUint64(&reactiveMatchEvals, 0)
	for i := 0; i < changes; i++ {
		key := fmt.Sprintf("o%d", i)
		row := []byte(fmt.Sprintf(`{"id":%q,"status":"open","age":5}`, key))
		if err := b.Put(ordersSchema(), key, row, nil); err != nil {
			tb.Fatalf("Put %s: %v", key, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadUint64(&reactiveMatchDecodes) < uint64(changes) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	return atomic.LoadUint64(&reactiveMatchDecodes), atomic.LoadUint64(&reactiveMatchEvals)
}

// TestNB1_DetectionIsNIndependent — the load-bearing NB-1 assertion. N subs share ONE query; a fixed
// firehose of distinct changes runs through the real commit → pump → fan-out path. The EXPENSIVE
// detection work (row decode + predicate eval) must be performed a per-change-constant number of
// times REGARDLESS of N. If detection were O(subs × changes) the counts would grow 10× from N=40 to
// N=400.
func TestNB1_DetectionIsNIndependent(t *testing.T) {
	const changes = 200

	run := func(n int) (decodes, evals uint64) {
		b := newMemBackendTB(t)
		registerWatchers(t, b, n, openPlan())
		return firehose(t, b, changes)
	}

	dLow, eLow := run(40)
	dHigh, eHigh := run(400)

	t.Logf("detection counts — N=40: decodes=%d evals=%d ; N=400: decodes=%d evals=%d (changes=%d)",
		dLow, eLow, dHigh, eHigh, changes)

	// One decode + one eval per change, INDEPENDENT of subscriber count.
	if dLow != changes || dHigh != changes {
		t.Fatalf("decode count not N-independent: N=40 → %d, N=400 → %d (want %d each)", dLow, dHigh, changes)
	}
	if eLow != changes || eHigh != changes {
		t.Fatalf("eval count not N-independent: N=40 → %d, N=400 → %d (want %d each)", eLow, eHigh, changes)
	}
}

// TestNB1_MultiplePredicatesScaleWithDistinctNotN — a stronger form: with D distinct predicates each
// shared by many subs, detection cost is O(changes × D), NOT O(changes × subs). Uses two DISTINCT
// non-indexable (collection-witness) predicates so BOTH residuals are evaluated per change (a witness
// footprint always "enters", so the short-circuit doesn't hide the second eval).
func TestNB1_MultiplePredicatesScaleWithDistinctNotN(t *testing.T) {
	b := newMemBackendTB(t)
	// P1: status IN {open, pending}; P2: status IN {open, closed} — both OR ⇒ non-indexable ⇒
	// collection-witness footprint ⇒ every change is "entered", so the residual is always evaluated.
	p1 := QueryPlan{Where: CondNode{Op: CondOr, Kids: []CondNode{
		{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("open")},
		{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("pending")},
	}}, Limit: -1}
	p2 := QueryPlan{Where: CondNode{Op: CondOr, Kids: []CondNode{
		{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("open")},
		{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("closed")},
	}}, Limit: -1}
	for i := 0; i < 100; i++ {
		s1, _, _ := b.WatchTenant(ordersSchema(), p1, "")
		s2, _, _ := b.WatchTenant(ordersSchema(), p2, "")
		t.Cleanup(s1.Close)
		t.Cleanup(s2.Close)
		go func(a, c *subscription) {
			go func() {
				for range a.Changes() {
				}
			}()
			for range c.Changes() {
			}
		}(s1, s2)
	}

	const changes = 100
	decodes, evals := firehose(t, b, changes)
	t.Logf("200 subs / 2 distinct predicates / %d changes → decodes=%d evals=%d", changes, decodes, evals)

	// One decode per change (body shared across all subs). Two distinct predicates → 2 evals per
	// change. Both independent of the 200 subscribers (would be 200×changes if per-sub).
	if decodes != changes {
		t.Fatalf("decodes = %d, want %d (one per change, not per sub)", decodes, changes)
	}
	if evals != changes*2 {
		t.Fatalf("evals = %d, want %d (distinct-predicates × changes, not subs × changes)", evals, changes*2)
	}
}

// BenchmarkReactiveFanoutDispatch reports the actual per-change dispatch cost as N grows, driving one
// synthetic batch per iteration with a strictly-increasing commitTs (so no dedup). Detection
// (decode+eval) is N-independent; the ns/op still rises with N because DELIVERY is the honest O(N)
// LiveView floor (one channel push per sub). The bench REPORTS both so the floor is characterized.
func BenchmarkReactiveFanoutDispatch(b *testing.B) {
	for _, n := range []int{1, 50, 200, 500} {
		b.Run(fmt.Sprintf("subs=%d", n), func(b *testing.B) {
			backend := newMemBackendTB(b)
			registerWatchers(b, backend, n, openPlan())
			// One real put → capture its full batch shape (coords/record) once, then replay it with
			// a strictly-rising commitTs so each iteration is a fresh (non-deduped) change.
			feed, cancel := backend.Subscribe(4)
			_ = backend.Put(ordersSchema(), "seed", []byte(`{"id":"seed","status":"open","age":5}`), nil)
			tmpl := <-feed
			cancel()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tmpl.CommitTs = HLC{WallMs: tmpl.CommitTs.WallMs + uint64(i) + 1}
				backend.reactive.dispatchLocal(tmpl)
			}
		})
	}
}

// TestNB3_ResyncStormBound — the NB-3 bound. A synthetic engine-feed overflow latches EVERY sub's
// resync flag; the bounded fan-out (ResyncPending) must run at most K = min(GOMAXPROCS,8) re-queries
// CONCURRENTLY (the engine sees ≤ K scans, not N) while still resyncing EVERY affected sub exactly
// once (over-resync never under-notify). Also proves per-sub coalescing: repeated overflows collapse
// to one re-query per sub.
func TestNB3_ResyncStormBound(t *testing.T) {
	const n = 500
	b := newMemBackendTB(t)
	subs := make([]*subscription, 0, n)
	for i := 0; i < n; i++ {
		sub, _, err := b.WatchTenant(ordersSchema(), openPlan(), "")
		if err != nil {
			t.Fatalf("WatchTenant %d: %v", i, err)
		}
		t.Cleanup(sub.Close)
		subs = append(subs, sub)
	}

	// Synthetic overflow — latch resync on EVERY sub, THREE times (simulating a burst of dropped
	// batches). Per-sub coalescing must collapse this to ONE re-query per sub.
	b.reactive.markResyncAll()
	b.reactive.markResyncAll()
	b.reactive.markResyncAll()

	k := ReQuerySlots()
	if k != wantSlots() {
		t.Fatalf("ReQuerySlots() = %d, want min(GOMAXPROCS,8) = %d", k, wantSlots())
	}

	var inFlight int64
	var peak int64
	var total int64
	reQuery := func(s *subscription) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		time.Sleep(3 * time.Millisecond) // hold the slot so concurrency is observable
		atomic.AddInt64(&total, 1)
		atomic.AddInt64(&inFlight, -1)
	}

	got := b.reactive.ResyncPending(reQuery)

	t.Logf("NB-3 resync storm: subs=%d K=%d peak-concurrent-requeries=%d total-requeries=%d",
		n, k, atomic.LoadInt64(&peak), atomic.LoadInt64(&total))

	// (a) per-sub coalescing: exactly N re-queries despite 3 overflow latches.
	if got != n || atomic.LoadInt64(&total) != int64(n) {
		t.Fatalf("resync count = %d (total=%d), want %d (one per sub, coalesced)", got, total, n)
	}
	// (b) the hard bound: concurrent re-queries never exceed K.
	if p := atomic.LoadInt64(&peak); p > int64(k) {
		t.Fatalf("peak concurrent re-queries = %d, exceeds semaphore cap K=%d", p, k)
	}
	// (c) the semaphore actually limited — with 500 subs and a hold, we saturate K.
	if p := atomic.LoadInt64(&peak); p < int64(k) {
		t.Fatalf("peak concurrent re-queries = %d < K=%d — semaphore not saturated (bound not exercised)", p, k)
	}
	// A second ResyncPending finds nothing pending (flags were cleared by NeedsResync).
	if again := b.reactive.ResyncPending(reQuery); again != 0 {
		t.Fatalf("second ResyncPending found %d pending — resync flags not cleared", again)
	}
}

func wantSlots() int {
	k := runtime.GOMAXPROCS(0)
	if k > 8 {
		k = 8
	}
	if k < 1 {
		k = 1
	}
	return k
}

// TestNB3_SemaphoreGloballyCapsConcurrency exercises AcquireReQuery directly: 200 goroutines racing
// for slots never exceed K in flight.
func TestNB3_SemaphoreGloballyCapsConcurrency(t *testing.T) {
	k := ReQuerySlots()
	var inFlight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel := AcquireReQuery()
			defer rel()
			cur := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
		}()
	}
	wg.Wait()
	if p := atomic.LoadInt64(&peak); p > int64(k) {
		t.Fatalf("peak in-flight = %d exceeds K=%d", p, k)
	}
	t.Logf("global semaphore: 200 racers, K=%d, peak in-flight=%d", k, atomic.LoadInt64(&peak))
}
