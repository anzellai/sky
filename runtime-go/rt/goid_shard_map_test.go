package rt

// Gates for the goroutine-keyed sharded maps that replaced two sync.Maps on
// the per-interaction path (goroutineCtx, liveSessionByGoroutine).
//
// The defect being fixed: sync.Map's fast path is a lock-free read of an
// immutable `read` map, which pays off when entries are read many times per
// write. These two are the opposite — every stamp is a fresh goroutine id and
// the matching clear removes it — so every operation fell through to
// sync.Map's own mutex. A mutex profile at GOMAXPROCS=8 put 26.5% of all
// contention on setGoroutineLiveSession + clearGoroutineLiveSession.
//
// Falsifying mutation for the distribution gate: set goidShards = 1.
// Falsifying mutation for the leak gate: drop the `defer Clear…` in
// runWithLiveSession, or make goidShardedMap.drop a no-op.

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestGoidShardedMap_StoreLoadDrop — the flat semantics the two call sites
// need, including that a dropped key really is gone (goroutine ids are
// recycled, so a stale entry is a correctness bug, not just a leak).
func TestGoidShardedMap_StoreLoadDrop(t *testing.T) {
	m := newGoidShardedMap[string]()

	if _, ok := m.load(42); ok {
		t.Fatal("empty map returned a value")
	}
	m.store(42, "a")
	if v, ok := m.load(42); !ok || v != "a" {
		t.Fatalf("load after store = (%q, %v), want (\"a\", true)", v, ok)
	}
	m.store(42, "b") // overwrite, not insert
	if v, _ := m.load(42); v != "b" {
		t.Fatalf("load after overwrite = %q, want \"b\"", v)
	}
	if m.size() != 1 {
		t.Fatalf("overwrite grew the map to %d", m.size())
	}
	m.drop(42)
	if _, ok := m.load(42); ok {
		t.Fatal("value survived drop — a recycled goroutine id would inherit it")
	}
	if m.size() != 0 {
		t.Fatalf("size = %d after dropping the only key", m.size())
	}
}

// TestGoidShardedMap_NoLeak — every stamp paired with a clear must leave the
// map empty. This is the assertion TestGoroutineContext_CleanupReleases had to
// make INDIRECTLY ("we can't inspect sync.Map size — no public API"); the
// sharded map can be counted, so the property is now asserted head-on.
func TestGoidShardedMap_NoLeak(t *testing.T) {
	const N = 500
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SetGoroutineTraceContext(WithRequestID(context.Background(), "req"))
			ClearGoroutineTraceContext()
		}()
	}
	wg.Wait()
	if n := goroutineCtx.size(); n != 0 {
		t.Fatalf("goroutineCtx retained %d entries after every stamp was cleared; "+
			"recycled goroutine ids would inherit them", n)
	}

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runWithLiveSession(&liveSession{}, func() {
				if currentLiveSession() == nil {
					t.Error("stamp not visible inside runWithLiveSession")
				}
			})
		}()
	}
	wg.Wait()
	if n := liveSessionByGoroutine.size(); n != 0 {
		t.Fatalf("liveSessionByGoroutine retained %d entries after every "+
			"runWithLiveSession returned", n)
	}
}

// TestGoidShardedMap_ConcurrentGoroutinesDoNotShareAShard — the fix itself.
//
// A distribution assertion rather than a timing one: "did they run in
// parallel?" is flaky under load and vacuous on one core, whereas "do
// concurrent goroutines land on different guards?" is exactly what the fix
// claims and is checkable. Real goroutine ids are used, not synthetic
// counters, so the gate exercises the ids the runtime actually allocates.
func TestGoidShardedMap_ConcurrentGoroutinesDoNotShareAShard(t *testing.T) {
	const N = 512
	ids := make(chan int64, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- currentGoroutineID()
		}()
	}
	wg.Wait()
	close(ids)

	m := newGoidShardedMap[int]()
	seen := map[*goidShard[int]]int{}
	for gid := range ids {
		seen[m.shardFor(gid)]++
	}

	if len(seen) < goidShards/2 {
		t.Fatalf("%d concurrent goroutines reached only %d of %d shards — the "+
			"guard is still effectively process-wide", N, len(seen), goidShards)
	}
	worst := 0
	for _, c := range seen {
		if c > worst {
			worst = c
		}
	}
	if limit := 4 * N / goidShards; worst > limit {
		t.Fatalf("worst shard holds %d of %d goroutines (limit %d) — the mask "+
			"distributes badly and contention would survive sharding",
			worst, N, limit)
	}
}

// TestGoidShardedMap_IsolatesValuesAcrossGoroutines — sharding must not let
// one goroutine read another's stamp. The hazard is a shard index computed
// from something other than the full key.
func TestGoidShardedMap_IsolatesValuesAcrossGoroutines(t *testing.T) {
	const N = 200
	var wg sync.WaitGroup
	bad := make(chan string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("req-%d", i)
			SetGoroutineRequestID(want)
			defer ClearGoroutineRequestID()
			if got := CurrentRequestID(); got != want {
				bad <- fmt.Sprintf("goroutine %d read %q, wanted %q", i, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(bad)
	for msg := range bad {
		t.Error(msg)
	}
}

// BenchmarkGoidStamp_StoreDrop is the measurement the fix is for: the
// stamp/clear pair on the per-interaction path, run in parallel.
func BenchmarkGoidStamp_StoreDrop(b *testing.B) {
	sess := &liveSession{}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			setGoroutineLiveSession(sess)
			clearGoroutineLiveSession()
		}
	})
}
