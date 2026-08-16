package rt

// Gates for the Set fast path that skips memCache's process-wide WRITE lock
// when the cache already holds the exact pointer being set.
//
// The defect: handleEvent does store.Get(sid) then store.Set(sid, sess) with
// the pointer Get just returned, so in the steady state the write was
// `memCache[sid] = sess` where memCache[sid] was ALREADY sess — a no-op that
// took a process-wide write lock on every interaction. A mutex profile of
// examples/19-skyforum at GOMAXPROCS=8 attributed 39.6% of all contention to
// that Unlock. See docs/perf/runs/gomaxprocs-scaling-20260816/.
//
// The fast path is only sound because idleEvictPass sets `evicted` and deletes
// the entry TOGETHER under memMu.Lock, so a reader holding RLock cannot see the
// half-evicted state. These gates pin the three ways the shortcut could be got
// wrong: resurrecting a corpse, missing a pointer swap, and missing an insert.
//
// Falsifying mutations, each of which must turn one of these red:
//   * drop `&& !sess.evicted.Load()` from memCacheAlreadyHolds
//     → TestSetFastPath_NeverResurrectsEvictedCorpse
//   * weaken `entry == sess` to `ok` (any entry counts as a hit)
//     → TestSetFastPath_ReplacesADifferentPointer
//   * make memCacheAlreadyHolds return true unconditionally
//     → TestSetFastPath_StillInsertsNewSessions

import (
	"sync"
	"testing"
	"time"
)

// TestMemCacheAlreadyHolds_Truth — the predicate itself, over every case Set
// can present it with. This is where the logic lives, so it is tested directly
// rather than only through a store that needs a live engine.
func TestMemCacheAlreadyHolds_Truth(t *testing.T) {
	var mu sync.RWMutex
	cache := map[string]*liveSession{}
	a, b := &liveSession{}, &liveSession{}

	if memCacheAlreadyHolds(&mu, cache, "sid", a) {
		t.Error("empty cache reported as already holding the session")
	}

	cache["sid"] = a
	if !memCacheAlreadyHolds(&mu, cache, "sid", a) {
		t.Error("cache holding exactly this pointer reported as not holding it")
	}
	if memCacheAlreadyHolds(&mu, cache, "sid", b) {
		t.Error("a DIFFERENT pointer under the same sid reported as a hit — " +
			"a resurrected session would never reach the cache")
	}
	if memCacheAlreadyHolds(&mu, cache, "other", a) {
		t.Error("a different sid reported as a hit")
	}

	a.evicted.Store(true)
	if memCacheAlreadyHolds(&mu, cache, "sid", a) {
		t.Error("an EVICTED session reported as a hit — Set would skip the " +
			"TOCTOU re-check and the corpse would survive in memCache")
	}
}

// TestSetFastPath_NeverResurrectsEvictedCorpse — fix #2 must survive the
// shortcut. A late async result (Cmd.perform completing, a Time.every tick)
// arriving after the idle-evict pass markDone'd the session must be dropped,
// not written back.
func TestSetFastPath_NeverResurrectsEvictedCorpse(t *testing.T) {
	var mu sync.RWMutex
	cache := map[string]*liveSession{}
	sess := &liveSession{}
	cache["sid"] = sess

	// The evict pass, verbatim in the order idleEvictPass performs it.
	mu.Lock()
	sess.evicted.Store(true)
	delete(cache, "sid")
	mu.Unlock()

	if memCacheAlreadyHolds(&mu, cache, "sid", sess) {
		t.Fatal("post-evict Set took the fast path; the corpse would be treated " +
			"as correctly cached")
	}
}

// TestSetFastPath_ReplacesADifferentPointer — the single-flight resurrect path
// (fix #1) hands out ONE pointer per session. If Set were to treat "some entry
// exists" as a hit, a genuinely new pointer would never land and the two would
// split-brain.
func TestSetFastPath_ReplacesADifferentPointer(t *testing.T) {
	s := newMemoryStore(30 * time.Minute)
	defer s.Close()

	first := &liveSession{}
	s.Set("sid", first)
	if got, _ := s.Get("sid"); got != first {
		t.Fatal("first Set did not land")
	}

	second := &liveSession{}
	s.Set("sid", second)
	got, ok := s.Get("sid")
	if !ok || got != second {
		t.Fatal("Set with a different pointer did not replace the cached entry")
	}
}

// TestSetFastPath_StillInsertsNewSessions — the obvious way to break this is a
// predicate that always says "already held".
func TestSetFastPath_StillInsertsNewSessions(t *testing.T) {
	s := newMemoryStore(30 * time.Minute)
	defer s.Close()

	sessions := map[string]*liveSession{}
	for _, sid := range []string{"a", "b", "c", "d"} {
		sess := &liveSession{}
		sessions[sid] = sess
		s.Set(sid, sess)
	}
	for sid, want := range sessions {
		got, ok := s.Get(sid)
		if !ok || got != want {
			t.Fatalf("session %q did not survive Set (ok=%v)", sid, ok)
		}
	}
}

// TestSetFastPath_RepeatedSetIsIdempotent — the actual interaction shape:
// Get then Set with the same pointer, over and over. Must leave exactly one
// entry and keep sliding lastSeen (the TTL reaper depends on it).
func TestSetFastPath_RepeatedSetIsIdempotent(t *testing.T) {
	s := newMemoryStore(30 * time.Minute)
	defer s.Close()

	sess := &liveSession{}
	s.Set("sid", sess)
	before := sess.lastSeenTime()

	time.Sleep(2 * time.Millisecond)
	for i := 0; i < 50; i++ {
		got, _ := s.Get("sid")
		s.Set("sid", got) // the steady-state pair, which now skips the write lock
	}

	s.mu.RLock()
	n := len(s.sessions)
	s.mu.RUnlock()
	if n != 1 {
		t.Fatalf("repeated Set left %d cache entries, want 1", n)
	}
	if got, _ := s.Get("sid"); got != sess {
		t.Fatal("repeated Set lost the session pointer")
	}
	if !sess.lastSeenTime().After(before) {
		t.Fatal("the fast path stopped sliding lastSeen — the TTL reaper would " +
			"collect a session that is actively in use")
	}
}

// TestSetFastPath_ConcurrentGetSetIsRaceFree drives the real pair from many
// goroutines. Its value is under `go test -race`, which is where a fast path
// that read the map outside the guard would be caught.
func TestSetFastPath_ConcurrentGetSetIsRaceFree(t *testing.T) {
	s := newMemoryStore(30 * time.Minute)
	defer s.Close()
	for i := 0; i < 8; i++ {
		s.Set(string(rune('a'+i)), &liveSession{})
	}

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sid := string(rune('a' + g%8))
			for i := 0; i < 200; i++ {
				if sess, ok := s.Get(sid); ok {
					s.Set(sid, sess)
				}
			}
		}(g)
	}
	wg.Wait()

	s.mu.RLock()
	n := len(s.sessions)
	s.mu.RUnlock()
	if n != 8 {
		t.Fatalf("concurrent Get/Set left %d entries, want 8", n)
	}
}
