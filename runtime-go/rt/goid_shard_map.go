package rt

import "sync"

// goidShardedMap — a map keyed by goroutine id, sharded so that the
// stamp/clear pair every interaction performs does not serialise the process.
//
// Why not sync.Map, which is what both call sites used before this:
//
// sync.Map is tuned for entries that are read many times per write, over a key
// set that is mostly stable — its fast path is a lock-free read of an immutable
// `read` map. This workload is the exact opposite. Each stamp uses a FRESH key
// (a net/http handler goroutine id, never seen before) and the matching clear
// removes it again, so EVERY operation misses the read-only map, falls through
// to sync.Map's own `mu`, and mutates `dirty`. The result is one process-wide
// mutex on the per-interaction path, plus the promotion churn of a `dirty` map
// that is rebuilt continuously and never amortises.
//
// A mutex profile of examples/19-skyforum at GOMAXPROCS=8 attributed 26.5% of
// all contention to setGoroutineLiveSession + clearGoroutineLiveSession alone
// — against 40 microseconds of contention in total at GOMAXPROCS=1, i.e. the
// cost is purely a parallelism effect and grows with cores. See
// docs/perf/runs/gomaxprocs-scaling-20260816/.
//
// A plain map sharded by gid is strictly better for write-mostly traffic over
// unique keys: no promotion machinery, and the guard a given goroutine takes is
// one of goidShards rather than the one.
//
// Lifetime semantics are unchanged and remain the caller's responsibility:
// every store must be paired with a delete (the `defer Clear…` pattern), or the
// map accumulates an entry per goroutine ever stamped. Goroutine ids are
// recycled by the runtime, so a leaked entry is also a correctness hazard — a
// later goroutine can inherit a dead one's stamp. That is asserted by
// TestGoroutineContext_CleanupReleases and TestGoidShardedMap_NoLeak.

// goidShards must be a power of two — the shard index is a mask, not a modulo.
const goidShards = 64

type goidShard[T any] struct {
	mu sync.Mutex
	m  map[int64]T
	// Keep each shard's mutex off its neighbour's cache line. Without this the
	// shards pack ~8 to a line and locking one invalidates the others, trading
	// a contended mutex for false sharing.
	_ [shardCacheLine - 16]byte
}

type goidShardedMap[T any] struct {
	shards [goidShards]goidShard[T]
}

func newGoidShardedMap[T any]() *goidShardedMap[T] {
	g := &goidShardedMap[T]{}
	for i := range g.shards {
		g.shards[i].m = make(map[int64]T)
	}
	return g
}

// shardFor masks rather than hashes: the runtime allocates goroutine ids
// sequentially, so the low bits are already near-uniformly distributed across
// the concurrent goroutines of a loaded server. Masking an unsigned view also
// means a negative id — not reachable today, but cheap to be safe about —
// cannot index out of range.
func (g *goidShardedMap[T]) shardFor(gid int64) *goidShard[T] {
	return &g.shards[uint64(gid)&(goidShards-1)]
}

func (g *goidShardedMap[T]) load(gid int64) (T, bool) {
	sh := g.shardFor(gid)
	sh.mu.Lock()
	v, ok := sh.m[gid]
	sh.mu.Unlock()
	return v, ok
}

func (g *goidShardedMap[T]) store(gid int64, v T) {
	sh := g.shardFor(gid)
	sh.mu.Lock()
	sh.m[gid] = v
	sh.mu.Unlock()
}

func (g *goidShardedMap[T]) drop(gid int64) {
	sh := g.shardFor(gid)
	sh.mu.Lock()
	delete(sh.m, gid)
	sh.mu.Unlock()
}

// size walks every shard. Only tests call it — nothing on the interaction path
// needs a global view of either map, which is why sharding is available here at
// all.
func (g *goidShardedMap[T]) size() int {
	n := 0
	for i := range g.shards {
		g.shards[i].mu.Lock()
		n += len(g.shards[i].m)
		g.shards[i].mu.Unlock()
	}
	return n
}
