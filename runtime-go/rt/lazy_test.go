// lazy_test.go — Std.Ui.Lazy memoisation regression tests.
//
// Pins the v0.12 Lazy implementation:
//   * Same args + same fn pointer → cache hit (returns CACHED value,
//     not re-executed).
//   * Different args → cache miss (re-executes).
//   * LRU eviction works at the configured cap.

package rt

import (
	"testing"
)

func TestLazyCache_HitOnSameArgs(t *testing.T) {
	LazyCacheClear()
	defer LazyCacheClear()

	// Counter to detect re-execution. If the cache hits, fn isn't
	// called the second time so calls stays at 1.
	calls := 0
	fn := func(any) any {
		calls++
		return "result"
	}

	r1 := Std_Ui_Lazy_lazy(fn, 42)
	r2 := Std_Ui_Lazy_lazy(fn, 42)

	if calls != 1 {
		t.Errorf("expected 1 call (cache hit), got %d", calls)
	}
	if r1 != r2 || r1 != "result" {
		t.Errorf("expected both calls to return %q, got %v / %v", "result", r1, r2)
	}
}

func TestLazyCache_MissOnDifferentArgs(t *testing.T) {
	LazyCacheClear()
	defer LazyCacheClear()

	calls := 0
	fn := func(a any) any {
		calls++
		return a
	}

	Std_Ui_Lazy_lazy(fn, 1)
	Std_Ui_Lazy_lazy(fn, 2)
	Std_Ui_Lazy_lazy(fn, 3)

	if calls != 3 {
		t.Errorf("expected 3 calls (different args, no hit), got %d", calls)
	}
}

func TestLazyCache_TwoArgVariant(t *testing.T) {
	LazyCacheClear()
	defer LazyCacheClear()

	calls := 0
	fn := func(a any) any {
		// Sky lambdas curry: lazy2 invokes via SkyCall(SkyCall(fn, a), b).
		// In Go's plain-func form, two SkyCalls would unwrap two
		// returned closures. Here we model that with a closure
		// returning a closure.
		return func(b any) any {
			calls++
			return []any{a, b}
		}
	}

	r1 := Std_Ui_Lazy_lazy2(fn, 1, 2)
	r2 := Std_Ui_Lazy_lazy2(fn, 1, 2)
	r3 := Std_Ui_Lazy_lazy2(fn, 1, 3)

	if calls != 2 {
		t.Errorf("expected 2 calls (one hit on 1,2 + one miss on 1,3), got %d", calls)
	}
	_ = r1
	_ = r2
	_ = r3
}

func TestLazyCache_LRUEvictsOldest(t *testing.T) {
	// Force a tiny cap so we can verify eviction.
	old := lazyCacheCap
	lazyCacheCap = 4
	LazyCacheClear()
	defer func() {
		lazyCacheCap = old
		LazyCacheClear()
	}()

	calls := 0
	fn := func(a any) any {
		calls++
		return a
	}

	// Fill the cache: 4 distinct keys → 4 misses.
	Std_Ui_Lazy_lazy(fn, 1)
	Std_Ui_Lazy_lazy(fn, 2)
	Std_Ui_Lazy_lazy(fn, 3)
	Std_Ui_Lazy_lazy(fn, 4)

	// One more miss → evicts key=1 (LRU).
	Std_Ui_Lazy_lazy(fn, 5)

	// Re-call key=1: should be a miss (was evicted).
	Std_Ui_Lazy_lazy(fn, 1)

	if calls != 6 {
		t.Errorf("expected 6 calls (4 fills + 1 evicting + 1 re-miss for evicted key), got %d", calls)
	}

	// Re-call key=5: should be a hit (still in cache).
	Std_Ui_Lazy_lazy(fn, 5)
	if calls != 6 {
		t.Errorf("expected key=5 hit (still 6 calls), got %d", calls)
	}
}
