package rt

// Redis session-store round-trip. Uses miniredis (in-process Redis
// clone) so the test has no external dependency and runs in CI.
// Covers the three user-visible behaviours: Set → Get round-trips,
// Delete removes the key, and Get refreshes TTL so an active session
// survives.

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func withRedis(t *testing.T) (*redisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	store, err := newRedisStore(mr.Addr(), 30*time.Minute)
	if err != nil {
		t.Fatalf("newRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, mr
}

func TestRedisStore_SetGetRoundTrip(t *testing.T) {
	store, _ := withRedis(t)

	orig := map[string]any{
		"id":    42,
		"email": "alice@example.com",
		"roles": []any{"admin", "member"},
	}
	store.Set("sid-1", &liveSession{model: orig})

	// Clear the in-process mem cache so Get is forced through Redis.
	store.memMu.Lock()
	delete(store.memCache, "sid-1")
	store.memMu.Unlock()

	sess, ok := store.Get("sid-1")
	if !ok {
		t.Fatal("Get should find the session after Set")
	}
	decoded, ok := sess.model.(map[string]any)
	if !ok {
		t.Fatalf("decoded model wrong type: %T", sess.model)
	}
	if decoded["email"] != "alice@example.com" {
		t.Fatalf("email did not round-trip: %v", decoded["email"])
	}
	if decoded["id"] != 42 {
		t.Fatalf("id did not round-trip: %v", decoded["id"])
	}
}

func TestRedisStore_Delete(t *testing.T) {
	store, _ := withRedis(t)

	store.Set("sid-2", &liveSession{model: map[string]any{"x": 1}})
	store.Delete("sid-2")

	store.memMu.Lock()
	delete(store.memCache, "sid-2")
	store.memMu.Unlock()

	if _, ok := store.Get("sid-2"); ok {
		t.Fatal("Get should miss after Delete")
	}
}

func TestRedisStore_GetRefreshesTTL(t *testing.T) {
	// Active session must not expire mid-conversation. Get should
	// extend the TTL so sessions with regular traffic stay alive.
	store, mr := withRedis(t)
	store.Set("sid-3", &liveSession{model: map[string]any{"x": 1}})

	// Advance miniredis clock to within the TTL window, then Get to
	// refresh. The key's TTL should reset; stepping forward by the
	// original TTL shouldn't expire it because Get refreshed it.
	mr.FastForward(10 * time.Minute)

	store.memMu.Lock()
	delete(store.memCache, "sid-3")
	store.memMu.Unlock()

	if _, ok := store.Get("sid-3"); !ok {
		t.Fatal("Get should find the session before TTL lapse")
	}
	// Now 25m has elapsed since Set; TTL (30m) was refreshed at the
	// 10m mark by Get, so remaining window is ~30m - 15m = 15m.
	mr.FastForward(15 * time.Minute)

	store.memMu.Lock()
	delete(store.memCache, "sid-3")
	store.memMu.Unlock()

	if _, ok := store.Get("sid-3"); !ok {
		t.Fatal("Get should still find the session after TTL refresh")
	}
}

func TestRedisStore_Expiry(t *testing.T) {
	// Without a Get, the session should expire after TTL.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	store, err := newRedisStore(mr.Addr(), 1*time.Minute)
	if err != nil {
		t.Fatalf("newRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	store.Set("sid-4", &liveSession{model: map[string]any{"x": 1}})

	// Drop the in-process cache so Get must go to Redis.
	store.memMu.Lock()
	delete(store.memCache, "sid-4")
	store.memMu.Unlock()

	mr.FastForward(2 * time.Minute)
	if _, ok := store.Get("sid-4"); ok {
		t.Fatal("Get should miss after TTL expiry")
	}
}

func TestRedisStore_RejectsClosureInModel(t *testing.T) {
	// Same validateSessionValue guard as sqlite/postgres: a model
	// containing a closure can't round-trip, so Set silently keeps
	// the in-process pointer but Redis stays empty. Checking that
	// we never wrote a malformed blob is the actual invariant.
	store, mr := withRedis(t)

	bad := map[string]any{
		"name": "alice",
		"cb":   func() {},
	}
	store.Set("sid-5", &liveSession{model: bad})

	keys := mr.Keys()
	for _, k := range keys {
		if k == redisKey("sid-5") {
			t.Fatalf("Redis should not contain a blob for unencodable session; keys=%v", keys)
		}
	}
}

func TestRedisStore_URLForm(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	// redis://host:port form should parse and connect.
	store, err := newRedisStore("redis://"+mr.Addr(), 1*time.Minute)
	if err != nil {
		t.Fatalf("newRedisStore with URL form: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	store.Set("sid-6", &liveSession{model: map[string]any{"ok": true}})
	store.memMu.Lock()
	delete(store.memCache, "sid-6")
	store.memMu.Unlock()
	if _, ok := store.Get("sid-6"); !ok {
		t.Fatal("Get should find the session via URL-form dial")
	}
}

func TestChooseStore_Redis(t *testing.T) {
	// Integration through the factory: kind="redis" with a bare
	// host:port path should return a redisStore, not fall back to
	// memory. A missing-server URL should fall back gracefully.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	s := chooseStore("redis", mr.Addr(), 1*time.Minute)
	if _, ok := s.(*redisStore); !ok {
		t.Fatalf("chooseStore(redis, <ok>) = %T, want *redisStore", s)
	}
	_ = s.Close()

	// Unreachable Redis: should fall back to memory rather than
	// crash the server at startup.
	fallback := chooseStore("redis", "127.0.0.1:1", 1*time.Minute)
	if _, ok := fallback.(*memoryStore); !ok {
		t.Fatalf("chooseStore(redis, <unreachable>) = %T, want *memoryStore fallback", fallback)
	}
	_ = fallback.Close()
}
