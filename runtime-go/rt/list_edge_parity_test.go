package rt

import (
	"reflect"
	"testing"
)

// L1 (conformance finding): the list-building ops are now Ffi.kernel aliases
// backed by the O(n) Go kernels below. When wiring them, three kernels had
// latent edge-case bugs that would panic once they became reachable from Sky:
//   - List_range with hi < lo: make([]any, 0, hi-lo+1) with a negative cap.
//   - List_take with n < 0:    items[:n] with a negative bound.
//   - List_drop with n < 0:    items[n:] with a negative bound.
// These assert EXACT parity with the Sky-source semantics they replaced
// (range lo>hi -> []; take n<=0 -> []; drop n<=0 -> whole list) and, above all,
// that they never panic.

func TestListRangeEmptyWhenHiBelowLo(t *testing.T) {
	got := List_range(5, 1) // must be [] (no panic), matching Sky `range 5 1`
	xs := AsList(got)
	if len(xs) != 0 {
		t.Fatalf("List_range(5,1) = %v, want empty", xs)
	}
	// Normal inclusive range still correct.
	if !reflect.DeepEqual(AsList(List_range(1, 3)), []any{1, 2, 3}) {
		t.Fatalf("List_range(1,3) != [1 2 3]")
	}
	// Single-point range.
	if len(AsList(List_range(3, 3))) != 1 {
		t.Fatalf("List_range(3,3) should be a singleton")
	}
}

func TestListTakeNegativeAndOverflow(t *testing.T) {
	src := []any{1, 2, 3}
	if len(AsList(List_take(-3, src))) != 0 {
		t.Fatalf("List_take(-3, [1 2 3]) should be empty")
	}
	if len(AsList(List_take(0, src))) != 0 {
		t.Fatalf("List_take(0, [1 2 3]) should be empty")
	}
	if !reflect.DeepEqual(AsList(List_take(10, src)), []any{1, 2, 3}) {
		t.Fatalf("List_take(10, [1 2 3]) should be the whole list")
	}
	if !reflect.DeepEqual(AsList(List_take(2, src)), []any{1, 2}) {
		t.Fatalf("List_take(2, [1 2 3]) should be [1 2]")
	}
}

func TestListDropNegativeAndOverflow(t *testing.T) {
	src := []any{1, 2, 3}
	if !reflect.DeepEqual(AsList(List_drop(-2, src)), []any{1, 2, 3}) {
		t.Fatalf("List_drop(-2, [1 2 3]) should be the whole list")
	}
	if !reflect.DeepEqual(AsList(List_drop(0, src)), []any{1, 2, 3}) {
		t.Fatalf("List_drop(0, [1 2 3]) should be the whole list")
	}
	if len(AsList(List_drop(10, src))) != 0 {
		t.Fatalf("List_drop(10, [1 2 3]) should be empty")
	}
	if !reflect.DeepEqual(AsList(List_drop(1, src)), []any{2, 3}) {
		t.Fatalf("List_drop(1, [1 2 3]) should be [2 3]")
	}
}
