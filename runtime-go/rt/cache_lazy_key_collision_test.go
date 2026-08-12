package rt

import (
	"testing"
)

// The same non-injective `fmt.Sprintf("%v", …)` identity that made `Set` drop
// elements also keyed two LOOKUP-shaped stores. Neither lets a wrong TYPE
// escape — the stored value is returned as-is — but both hand back the wrong
// VALUE for a key that never went in, which is the more dangerous half of the
// same defect:
//
//   * `Cache.getRaw/putRaw/removeRaw` (`cache_kernel.go`) — a `put` under one
//     composite key is readable under a different, colliding one, and a
//     `remove` of one evicts the other.
//   * `Std.Ui.Lazy` (`lazy.go`, `lazyKey`) — the memoised view cache. A
//     collision renders a DIFFERENT subtree than the arguments describe, and
//     the pipe delimiter it used could be forged by an argument containing a
//     literal `|`.

// force runs a `Task`-shaped thunk and unwraps its `Result`, failing the test
// on `Err`.
func force(t *testing.T, task any) any {
	t.Helper()
	fn, ok := task.(func() any)
	if !ok {
		t.Fatalf("expected a Task thunk, got %T", task)
	}
	res, ok := fn().(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", fn())
	}
	if res.Tag != 0 {
		t.Fatalf("task returned Err: %#v", res.ErrValue)
	}
	return res.OkValue
}

func newTestCache(t *testing.T) any {
	t.Helper()
	return force(t, Cache_new(map[string]any{"maxEntries": 64, "ttlMs": 0}))
}

func cachePut(t *testing.T, id, k, v any) {
	t.Helper()
	force(t, Cache_put(id, k, v))
}

// cacheGet returns (value, present).
func cacheGet(t *testing.T, id, k any) (any, bool) {
	t.Helper()
	m, ok := force(t, Cache_get(id, k)).(SkyMaybe[any])
	if !ok {
		t.Fatalf("Cache_get did not return a Maybe")
	}
	return m.JustValue, m.Tag == 0
}

func TestCacheDistinguishesCollidingCompositeKeys(t *testing.T) {
	id := newTestCache(t)
	ka := T2[any, any]{V0: "a b", V1: "c"}
	kb := T2[any, any]{V0: "a", V1: "b c"}

	cachePut(t, id, ka, "first")
	cachePut(t, id, kb, "second")

	if got, ok := cacheGet(t, id, ka); !ok || got != "first" {
		t.Fatalf("cache lost the first entry: got %#v present=%v, want \"first\"", got, ok)
	}
	if got, ok := cacheGet(t, id, kb); !ok || got != "second" {
		t.Fatalf("cache returned the wrong value for a distinct key: got %#v present=%v, want \"second\"", got, ok)
	}
	if n := AsInt(force(t, Cache_size(id))); n != 2 {
		t.Fatalf("cache size %d, want 2 — one entry overwrote the other", n)
	}
	// removing one must not evict the other
	force(t, Cache_remove(id, ka))
	if _, ok := cacheGet(t, id, kb); !ok {
		t.Fatal("removing one key evicted a different key")
	}
}

func TestCacheStillHitsOnEqualKeys(t *testing.T) {
	id := newTestCache(t)
	cachePut(t, id, T2[any, any]{V0: "a b", V1: "c"}, "v")
	// A structurally-equal key built separately must HIT.
	if got, ok := cacheGet(t, id, T2[any, any]{V0: "a b", V1: "c"}); !ok || got != "v" {
		t.Fatalf("an equal key missed the cache: got %#v present=%v", got, ok)
	}
	// Scalars must keep behaving exactly as before.
	cachePut(t, id, 1, "one")
	if got, ok := cacheGet(t, id, int64(1)); !ok || got != "one" {
		t.Fatalf("int and int64 1 must be one Sky Int key: got %#v present=%v", got, ok)
	}
}

// `lazyKey` joined `%v` renderings with "|". Two different argument lists must
// never fingerprint the same, or `Std.Ui.Lazy` serves a memoised view built
// from other arguments.
func TestLazyKeyIsInjective(t *testing.T) {
	fn := func(x any) any { return x }
	cases := []struct {
		name   string
		a, b   []any
		reason string
	}{
		{
			name:   "forged delimiter",
			a:      []any{"a|b"},
			b:      []any{"a", "b"},
			reason: "an argument containing the delimiter forged an argument boundary",
		},
		{
			name:   "colliding tuples",
			a:      []any{T2[any, any]{V0: "a b", V1: "c"}},
			b:      []any{T2[any, any]{V0: "a", V1: "b c"}},
			reason: "%v is not injective on composites",
		},
		{
			name:   "colliding records",
			a:      []any{map[string]any{"a": "x y", "b": "z"}},
			b:      []any{map[string]any{"a": "x", "b": "y z"}},
			reason: "%v is not injective on records",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if lazyKey(fn, c.a...) == lazyKey(fn, c.b...) {
				t.Fatalf("lazyKey collided on %v and %v (%s) — Std.Ui.Lazy would serve the wrong subtree",
					c.a, c.b, c.reason)
			}
		})
	}
	// And it must still HIT for equal arguments, or memoisation is dead.
	if lazyKey(fn, "a", 1) != lazyKey(fn, "a", 1) {
		t.Fatal("lazyKey is not stable for equal arguments — the cache would never hit")
	}
	if lazyKey(fn, map[string]any{"a": 1, "b": 2}) != lazyKey(fn, map[string]any{"b": 2, "a": 1}) {
		t.Fatal("lazyKey is not stable across Go map iteration order — every render would miss")
	}
	// Different functions, same args, must not share a fingerprint.
	other := func(x any) any { return x }
	if lazyKey(fn, "a") == lazyKey(other, "a") {
		t.Fatal("lazyKey ignored the function identity")
	}
}
