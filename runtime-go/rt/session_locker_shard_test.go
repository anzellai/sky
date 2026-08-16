package rt

// Regression gates for the sessionLocker map-guard sharding.
//
// The defect: sessionLocker's map guard was ONE process-wide mutex, taken
// twice per interaction (once in Lock, once in Unlock), so two interactions on
// DIFFERENT sessions serialised on it. A mutex profile of examples/19-skyforum
// at GOMAXPROCS=8 attributed 23.0% of all contention to it, against 40
// microseconds total at GOMAXPROCS=1 — a pure parallelism cost that grows with
// cores. See docs/perf/runs/gomaxprocs-scaling-20260816/.
//
// Sharding is only sound if it keeps the two properties the locker exists for,
// so both are asserted here alongside the distribution property that is the
// fix itself. Falsifying mutation for the distribution gate: set
// sessionLockerShards = 1.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSessionLocker_SerialisesSameSid — the property the locker exists for.
// Concurrent holders of ONE sid must never overlap. Sharding must not weaken
// this: the same sid always hashes to the same shard, so the lazy-create and
// the refcount stay atomic per session.
func TestSessionLocker_SerialisesSameSid(t *testing.T) {
	l := newSessionLocker()
	const goroutines, iters = 16, 200

	var inside atomic.Int32
	var overlaps atomic.Int32
	var counter int // deliberately unguarded: the locker is the only guard

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				l.Lock("one-and-only-sid")
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				counter++
				inside.Add(-1)
				l.Unlock("one-and-only-sid")
			}
		}()
	}
	wg.Wait()

	if got := overlaps.Load(); got != 0 {
		t.Fatalf("mutual exclusion broken: %d overlapping critical sections", got)
	}
	if counter != goroutines*iters {
		t.Fatalf("lost updates under the lock: counter = %d, want %d",
			counter, goroutines*iters)
	}
}

// TestSessionLocker_NoEntryLeak — the refcount must still drop entries when
// the last holder leaves, or a long-lived process accumulates one mutex per
// session ever seen. This walks the shards because the map is no longer one
// map; a sharded locker that forgot to delete would show up here.
func TestSessionLocker_NoEntryLeak(t *testing.T) {
	l := newSessionLocker()
	for i := 0; i < 500; i++ {
		sid := fmt.Sprintf("sid-%d", i)
		l.Lock(sid)
		l.Unlock(sid)
	}
	live := 0
	for i := range l.shards {
		l.shards[i].mu.Lock()
		live += len(l.shards[i].locks)
		l.shards[i].mu.Unlock()
	}
	if live != 0 {
		t.Fatalf("locker leaked %d entries after every holder released", live)
	}
}

// TestSessionLocker_ConcurrentDistinctSidsDoNotShareAShard — the fix itself.
//
// This is a DISTRIBUTION assertion, not a timing one: a timing test ("do they
// run in parallel?") is flaky on a loaded host and would prove nothing on one
// core. What the fix actually claims is that distinct sessions mostly do not
// contend on the same guard, and that is checkable exactly.
//
// Real session ids are used (generateSkySessionID: 256-bit random, base64url)
// rather than "sid-0"-style strings, so the gate exercises the hash on the
// keys production actually presents.
func TestSessionLocker_ConcurrentDistinctSidsDoNotShareAShard(t *testing.T) {
	const sessions = 4096
	l := newSessionLocker()

	counts := make([]int, sessionLockerShards)
	for i := 0; i < sessions; i++ {
		counts[shardKey(generateSkySessionID(), sessionLockerShards-1)]++
	}

	used := 0
	maxShare := 0
	for _, c := range counts {
		if c > 0 {
			used++
		}
		if c > maxShare {
			maxShare = c
		}
	}

	// These thresholds are ABSOLUTE and deliberately not derived from
	// sessionLockerShards. An earlier version asserted `used != sessionLockerShards`
	// and `maxShare > 4*sessions/sessionLockerShards`, which is vacuous under the
	// gate's own falsifying mutation: at sessionLockerShards = 1 every id lands in
	// the single shard and `1 != 1` passes. mutate.sh caught it staying green.
	// The property being asserted is "4096 sessions spread over many distinct
	// guards", and that has to be stated as a number.
	const minDistinctShards = 32
	const maxPerShard = sessions / 8 // 512; the mean at 64 shards is 64

	if used < minDistinctShards {
		t.Fatalf("session ids reached only %d distinct shards (need >= %d) — the "+
			"map guard is still effectively process-wide", used, minDistinctShards)
	}
	// Guard against a hash that technically touches many shards but piles the
	// keys into one. 8x the mean is far outside binomial noise at this n.
	if maxShare > maxPerShard {
		t.Fatalf("worst shard holds %d of %d ids (limit %d) — hash distributes "+
			"badly and the contention would survive sharding", maxShare, sessions, maxPerShard)
	}

	// And the sharded locker must still be usable concurrently across sids.
	var wg sync.WaitGroup
	for i := 0; i < 256; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := fmt.Sprintf("concurrent-sid-%d", i)
			l.Lock(sid)
			l.Unlock(sid)
		}(i)
	}
	wg.Wait()
}

// BenchmarkSessionLocker_DistinctSids is the measurement the fix is for: N
// goroutines locking N DIFFERENT sessions. Under the process-wide guard this
// scales negatively with -cpu; under the sharded guard it should not.
func BenchmarkSessionLocker_DistinctSids(b *testing.B) {
	l := newSessionLocker()
	var n atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		sid := fmt.Sprintf("bench-sid-%d", n.Add(1))
		for pb.Next() {
			l.Lock(sid)
			l.Unlock(sid)
		}
	})
}
