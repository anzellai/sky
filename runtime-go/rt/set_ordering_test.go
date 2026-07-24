package rt

import (
	"reflect"
	"testing"
)

// Set.toList must return a deterministic, natural-order sorted list (Elm's Set
// is ordered), not the randomised Go-map iteration order.
func TestSetToListSortedDeterministic(t *testing.T) {
	// Ints sort numerically (NOT lexicographically by string repr — "10" < "2").
	s := Set_fromList([]any{5, 1, 10, 2, 3})
	got := Set_toList(s).([]any)
	want := []any{1, 2, 3, 5, 10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Set.toList ints = %v, want %v", got, want)
	}

	// Strings sort lexicographically.
	ss := Set_fromList([]any{"banana", "apple", "cherry"})
	gotS := Set_toList(ss).([]any)
	wantS := []any{"apple", "banana", "cherry"}
	if !reflect.DeepEqual(gotS, wantS) {
		t.Fatalf("Set.toList strings = %v, want %v", gotS, wantS)
	}

	// union / intersect / diff are observed through toList — also sorted.
	a := Set_fromList([]any{3, 1, 2})
	b := Set_fromList([]any{2, 4, 3})
	if u := Set_toList(Set_union(a, b)).([]any); !reflect.DeepEqual(u, []any{1, 2, 3, 4}) {
		t.Fatalf("Set.union toList = %v, want [1 2 3 4]", u)
	}
	if i := Set_toList(Set_intersect(a, b)).([]any); !reflect.DeepEqual(i, []any{2, 3}) {
		t.Fatalf("Set.intersect toList = %v, want [2 3]", i)
	}
	if d := Set_toList(Set_diff(a, b)).([]any); !reflect.DeepEqual(d, []any{1}) {
		t.Fatalf("Set.diff toList = %v, want [1]", d)
	}
}
