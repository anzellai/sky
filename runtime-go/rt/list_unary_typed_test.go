package rt

import "testing"

// `List_isEmptyT` / `List_lengthT` are the typed twins a PROVEN unary list
// kernel call is re-targeted at. As with the other twins, a twin that is not the
// exact semantic equal of what it replaces is a miscompile, not an optimisation,
// so these are DIFFERENTIAL against the erased kernel rather than against a
// hand-written expectation that could drift alongside them.
//
// The interesting inputs are the DEGENERATE ones, because that is where the two
// implementations reach the same answer by different routes: `List_isEmpty` has
// an explicit `list == nil` guard AHEAD of `asList`, `List_length` has none and
// relies on `AsList(nil)` returning nil, and the typed twins have neither
// because `len` is defined on a nil slice. A fixed non-empty input agrees under
// any of those and proves nothing.

func boxSlice[T any](xs []T) []any {
	if xs == nil {
		return nil
	}
	out := make([]any, len(xs))
	for i, v := range xs {
		out[i] = v
	}
	return out
}

func TestListIsEmptyT_agreesWithErasedKernel(t *testing.T) {
	cases := [][]string{nil, {}, {""}, {"a"}, {"a", "b"}}
	for _, xs := range cases {
		typed := List_isEmptyT(xs)
		erased := AsBool(List_isEmpty(boxSlice(xs)))
		if typed != erased {
			t.Fatalf("xs=%#v: typed %v, erased kernel %v", xs, typed, erased)
		}
	}
}

func TestListLengthT_agreesWithErasedKernel(t *testing.T) {
	cases := [][]int{nil, {}, {0}, {1, 2, 3}}
	for _, xs := range cases {
		typed := List_lengthT(xs)
		erased := AsInt(List_length(boxSlice(xs)))
		if typed != erased {
			t.Fatalf("xs=%#v: typed %d, erased kernel %d", xs, typed, erased)
		}
	}
}

// A nil typed slice is what an un-initialised Sky list field lowers to, and the
// erased kernel's `list == nil` guard means it never reaches `asList`. The twin
// has no guard at all — `len(nil)` is 0 — so this pins that the two still agree.
func TestListUnaryT_nilTypedSlice(t *testing.T) {
	var xs []struct{ K string }
	if !List_isEmptyT(xs) {
		t.Fatal("nil typed slice reported non-empty")
	}
	if got := List_lengthT(xs); got != 0 {
		t.Fatalf("nil typed slice reported length %d", got)
	}
}

// The whole point of the twin: on a TYPED slice the erased kernel misses its
// `[]any` fast path and reflect-boxes every element merely to read a length.
// The twin must produce the same answer with no round trip — including for an
// element type whose zero value is not the empty interface.
func TestListUnaryT_typedStructElements(t *testing.T) {
	type row struct {
		K string
		V int
	}
	xs := []row{{"a", 1}, {"b", 2}}

	if List_isEmptyT(xs) != AsBool(List_isEmpty(xs)) {
		t.Fatal("isEmpty disagreed on a typed struct slice")
	}
	if List_lengthT(xs) != AsInt(List_length(xs)) {
		t.Fatal("length disagreed on a typed struct slice")
	}
	if List_lengthT(xs) != 2 {
		t.Fatalf("length %d, want 2", List_lengthT(xs))
	}
}
