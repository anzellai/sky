package rt

// Tiered-session-cache concurrency regressions (docs/skylive/tiered-session-cache.md).
//
// The tiered cache evicts a durable session's live memCache pointer after a
// short idle window with NO active SSE, while KEEPING its blob on disk to the
// full TTL, and resurrects it from disk on next access. This bounds RAM to the
// ACTIVE working set instead of all-within-TTL.
//
// Each test targets a specific grill race from the design doc's "GRILL OUTCOME"
// section. Run under `-race`.
//
//   1. Resurrect single-flight (fix #1/#7): N concurrent Gets on an evicted
//      session share ONE resurrected pointer, freshly touched.
//   2. Evict vs SSE-connect / in-flight Get (fix #1): Get NEVER returns a
//      markDone'd (dead) session, even racing the evict pass.
//   3. Evict vs async Set corpse (fix #2): Set on an evicted pointer does NOT
//      re-enter memCache.
//   4. Encode-fail not evicted (fix #6): a non-encodable session is kept in
//      memCache to the full TTL, never idle-evicted.
//   5. Memory store no-op: memoryStore never idle-evicts.
//   6. RAM bound (the point): flooding K idle no-SSE sessions then running the
//      pass drops memCache to ~0 while the disk blob count stays K.

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTieredSqliteStore builds a sqlite session store on a fresh temp-file DB
// with the given ttl + idleEvict, and registers cleanup. The 60s cleanupLoop
// goroutine never fires during a test; tests drive s.runIdleEvictOnce directly.
func newTieredSqliteStore(t *testing.T, ttl, idleEvict time.Duration) *sqliteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := newSQLiteStore(path, ttl, idleEvict)
	if err != nil {
		t.Fatalf("newSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// tieredModel is a gob-safe model (subset of the proven cross-instance
// round-trip shape) so encodeSession/decodeSession round-trip cleanly.
func tieredModel() map[string]any {
	return map[string]any{"count": 42, "name": "sky"}
}

func isDone(sess *liveSession) bool {
	if sess.done == nil {
		return false
	}
	select {
	case <-sess.done:
		return true
	default:
		return false
	}
}

func diskRowCount(t *testing.T, s *sqliteStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sky_sessions`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func memCacheLen(s *sqliteStore) int {
	s.memMu.RLock()
	defer s.memMu.RUnlock()
	return len(s.memCache)
}

func inMemCache(s *sqliteStore, sid string) (*liveSession, bool) {
	s.memMu.RLock()
	defer s.memMu.RUnlock()
	sess, ok := s.memCache[sid]
	return sess, ok
}

// evictNow backdates a session's lastSeen so it is idle, then runs one pass.
// Precondition: the session is already in the store (Set) with a persisted blob
// and no SSE connection.
func evictNow(s *sqliteStore, sid string, sess *liveSession) {
	sess.setLastSeenTime(time.Now().Add(-time.Minute))
	s.runIdleEvictOnce(time.Now())
}

// ── 1. Resurrect single-flight (fix #1 / #7) ───────────────────────────────

func TestTiered_ResurrectSingleFlight(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)
	const sid = "sess-resurrect"

	orig := buildSess(tieredModel())
	s.Set(sid, orig)

	evictNow(s, sid, orig)

	// Post-evict: pointer gone from memCache, flagged evicted, done closed,
	// blob still on disk.
	if _, ok := inMemCache(s, sid); ok {
		t.Fatal("session should be evicted from memCache")
	}
	if !orig.evicted.Load() {
		t.Fatal("evicted session must have evicted=true")
	}
	if !isDone(orig) {
		t.Fatal("evicted session must be markDone'd (done closed)")
	}
	if got := diskRowCount(t, s); got != 1 {
		t.Fatalf("blob must stay on disk after idle-evict: rows=%d want 1", got)
	}

	// N concurrent Gets must resurrect exactly ONE shared pointer.
	const N = 24
	var wg sync.WaitGroup
	ptrs := make([]*liveSession, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			sess, ok := s.Get(sid)
			if !ok {
				t.Errorf("Get(%s) after evict must resurrect from disk", sid)
				return
			}
			ptrs[i] = sess
		}(i)
	}
	wg.Wait()

	first := ptrs[0]
	if first == nil {
		t.Fatal("no session resurrected")
	}
	for i, p := range ptrs {
		if p != first {
			t.Fatalf("split-brain: Get #%d returned a different pointer (%p != %p)", i, p, first)
		}
	}
	if first == orig {
		t.Fatal("resurrected pointer must be a FRESH decode, never the evicted corpse")
	}
	// The resurrected pointer is THE cached one, live, freshly touched.
	cached, ok := inMemCache(s, sid)
	if !ok || cached != first {
		t.Fatalf("resurrected pointer must be the one in memCache")
	}
	if first.evicted.Load() {
		t.Fatal("resurrected session must not be flagged evicted")
	}
	if isDone(first) {
		t.Fatal("resurrected session must be live (done open)")
	}
	if age := time.Since(first.lastSeenTime()); age > 5*time.Second {
		t.Fatalf("fix #7: resurrected session born-idle (lastSeen age=%s) — would thrash", age)
	}
}

// ── 2. Evict vs SSE-connect / in-flight Get (fix #1) ───────────────────────
//
// Airtight invariant: Get NEVER returns a markDone'd session. A caller that
// took the pointer and is about to registerSSEConn must not be handed a dead
// session by a racing evict pass.

func TestTiered_EvictNeverStrandsGet(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)

	for i := 0; i < 300; i++ {
		sid := "sess-race-" + string(rune('a'+i%26)) + "-" + time.Now().Format("150405.000000000")
		sess := buildSess(tieredModel())
		s.Set(sid, sess)
		sess.setLastSeenTime(time.Now().Add(-time.Minute)) // make it an evict candidate

		var wg sync.WaitGroup
		wg.Add(2)
		// A: idle-evict pass.
		go func() {
			defer wg.Done()
			s.runIdleEvictOnce(time.Now())
		}()
		// B: a request takes the pointer and connects an SSE.
		var got *liveSession
		var ok bool
		go func() {
			defer wg.Done()
			got, ok = s.Get(sid)
			if ok && got != nil {
				got.registerSSEConn("tab-1")
			}
		}()
		wg.Wait()

		// Whatever Get returned MUST be a live, in-cache pointer — never a
		// markDone'd corpse the SSE would strand on.
		if ok {
			if isDone(got) {
				t.Fatalf("iter %d: Get returned a markDone'd (dead) session — stranded SSE", i)
			}
			cached, present := inMemCache(s, sid)
			if !present || cached != got {
				t.Fatalf("iter %d: Get returned a pointer not in memCache — split-brain", i)
			}
		}
	}
}

// A session WITH a live SSE connection is never a candidate, no matter how idle.
func TestTiered_SSEConnectedNeverEvicted(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)
	const sid = "sess-sse"

	sess := buildSess(tieredModel())
	s.Set(sid, sess)
	sess.registerSSEConn("tab-live") // active connection
	sess.setLastSeenTime(time.Now().Add(-time.Hour))

	// Hammer the pass concurrently; the SSE gate must hold every time.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runIdleEvictOnce(time.Now())
		}()
	}
	wg.Wait()

	if _, ok := inMemCache(s, sid); !ok {
		t.Fatal("SSE-connected session must never be evicted from memCache")
	}
	if sess.evicted.Load() {
		t.Fatal("SSE-connected session must never be flagged evicted")
	}
	if isDone(sess) {
		t.Fatal("SSE-connected session must never be markDone'd (SSE would be killed)")
	}
}

// ── 3. Evict vs async Set corpse (fix #2) ──────────────────────────────────

func TestTiered_EvictedSetIsDropped(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)
	const sid = "sess-corpse"

	sess := buildSess(tieredModel())
	s.Set(sid, sess)
	evictNow(s, sid, sess)

	if !sess.evicted.Load() {
		t.Fatal("precondition: session must be evicted")
	}

	// A late async producer calls Set with the evicted (corpse) pointer.
	s.Set(sid, sess)

	// It must NOT re-enter memCache under its own pointer.
	if cur, ok := inMemCache(s, sid); ok && cur == sess {
		t.Fatal("fix #2: evicted corpse re-entered memCache via Set — split-brain")
	}
}

// ── 4. Encode-fail not evicted (fix #6) ────────────────────────────────────

func TestTiered_EncodeFailNotEvicted(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)
	const sid = "sess-encfail"

	// A model carrying a func is NOT gob-encodable — Set keeps the live pointer
	// in memCache but persists no blob (in-memory fallback). It is the ONLY copy.
	bad := buildSess(map[string]any{"name": "x", "cb": func() {}})
	s.Set(sid, bad)

	if _, ok := inMemCache(s, sid); !ok {
		t.Fatal("precondition: non-encodable session must stay in memCache")
	}
	if got := diskRowCount(t, s); got != 0 {
		t.Fatalf("non-encodable session must NOT be on disk: rows=%d want 0", got)
	}

	// Idle it and run the pass: encode fails inside the pass → must NOT evict.
	bad.setLastSeenTime(time.Now().Add(-time.Minute))
	s.runIdleEvictOnce(time.Now())

	if _, ok := inMemCache(s, sid); !ok {
		t.Fatal("fix #6: encode-fail session was evicted — destroyed its only copy")
	}
	if bad.evicted.Load() {
		t.Fatal("fix #6: encode-fail session must not be flagged evicted")
	}
	if isDone(bad) {
		t.Fatal("fix #6: encode-fail session must not be markDone'd")
	}
}

// ── 5. Memory store no-op ──────────────────────────────────────────────────

func TestTiered_MemoryStoreNoOp(t *testing.T) {
	s := newMemoryStore(time.Hour)
	t.Cleanup(func() { _ = s.Close() })
	const sid = "sess-mem"

	sess := buildSess(tieredModel())
	s.Set(sid, sess)
	// Idle well past any idle-evict window (but within ttl). A durable store
	// would drop the pointer; the memory store has no disk backing, so it MUST
	// keep it — evicting would lose the session entirely.
	sess.setLastSeenTime(time.Now().Add(-30 * time.Minute))

	if _, ok := s.Get(sid); !ok {
		t.Fatal("memory store must never idle-evict — the session must survive")
	}
	if sess.evicted.Load() {
		t.Fatal("memory store must never set the evicted flag")
	}
	if isDone(sess) {
		t.Fatal("memory store must never markDone an idle session")
	}
}

// ── 6. RAM bound (the point) ───────────────────────────────────────────────

func TestTiered_RamBoundDropsMemCacheKeepsDisk(t *testing.T) {
	s := newTieredSqliteStore(t, time.Hour, 20*time.Millisecond)
	const K = 200

	sids := make([]string, K)
	for i := 0; i < K; i++ {
		sid := "flood-" + time.Now().Format("150405") + "-" + itoa(i)
		sids[i] = sid
		sess := buildSess(tieredModel())
		s.Set(sid, sess)
		sess.setLastSeenTime(time.Now().Add(-time.Minute)) // idle, no SSE
	}

	if got := memCacheLen(s); got != K {
		t.Fatalf("precondition: memCache should hold all %d live sessions, got %d", K, got)
	}
	if got := diskRowCount(t, s); got != K {
		t.Fatalf("precondition: disk should hold all %d blobs, got %d", K, got)
	}

	s.runIdleEvictOnce(time.Now())

	// RAM tracks the ACTIVE working set: idle no-SSE pointers are gone.
	if got := memCacheLen(s); got != 0 {
		t.Fatalf("RAM bound: memCache should drop to 0 after idle-evict, got %d", got)
	}
	// Durability is unchanged: every blob is still on disk to the full TTL.
	if got := diskRowCount(t, s); got != K {
		t.Fatalf("durability: disk blobs must be PRESERVED, got %d want %d", got, K)
	}
	// And they resurrect on access.
	if _, ok := s.Get(sids[0]); !ok {
		t.Fatal("an evicted-but-on-disk session must resurrect on Get")
	}
}
