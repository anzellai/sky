package rt

// Sharding helpers for the process-wide maps on Sky.Live's per-interaction
// path.
//
// A single mutex guarding a map that every interaction must write is a scaling
// defect rather than a correctness one: it is free on one core and costs more
// the more cores the process is given. A mutex profile of examples/19-skyforum
// under load at GOMAXPROCS=8 attributed 39.6% of all contention to the session
// store's in-memory cache guard and 23.0% to the session locker's map guard —
// against 40 MICROseconds of contention in total at GOMAXPROCS=1. The cost is
// entirely a parallelism effect, and it grows with cores.
//
// Sharding by key hash keeps every operation single-key, which is what these
// maps actually do on the interaction path, while letting two sessions proceed
// without meeting. The whole-map sweeps — the TTL cleanup and the idle
// eviction pass, both on a 60 s tick — walk the shards in turn and so keep
// their semantics; they simply no longer hold every session's guard at once.
//
// See docs/perf/runs/gomaxprocs-scaling-20260816/ for the profile.

// shardCacheLine is the padding stride that keeps each shard's mutex on its
// own cache line. Apple silicon uses 128-byte lines, x86-64 uses 64; 128 is
// correct on both and costs a few KB per sharded map. Without the padding the
// shards pack several to a line and neighbours invalidate each other on every
// lock — which trades one contended mutex for false sharing and can measure
// WORSE than the single lock it replaced.
const shardCacheLine = 128

// shardKey returns a shard index for a key, given a power-of-two mask
// (shardCount-1).
//
// FNV-1a: allocation-free, needs no import, and — unlike taking bytes off the
// end of the string — does not assume the key is uniformly distributed.
// Session ids normally are (generateSkySessionID is 256-bit random, base64url)
// but its fallback path returns "sid-<unix-nanos>", whose leading bytes are
// near-constant within a run; a prefix or suffix hash would pile those into
// one shard precisely when the process is already degraded.
func shardKey(key string, mask uint64) uint64 {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= fnvPrime64
	}
	return h & mask
}
