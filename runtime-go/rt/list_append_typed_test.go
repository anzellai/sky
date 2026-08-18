package rt

import (
	"reflect"
	"testing"
)

// `List_appendT` is the typed twin of the list arm of `rt.Concat` — the helper a
// PROVEN `xs ++ ys` call site is re-targeted at, where both operands have the
// same statically-known Go element type. As with the `foldl`/`any` twins, a twin
// that is not the exact semantic equal of what it replaces is a miscompile, not
// an optimisation, so these tests are DIFFERENTIAL against `rt.Concat` rather
// than against a hand-written expectation that could drift alongside it.
//
// The property that matters most, and the one the original one-line body got
// wrong, is FRESHNESS. `return append(a, b...)` reuses `a`'s backing array
// whenever `cap(a) > len(a)`, so the append writes THROUGH `a` into memory some
// other Sky value may still be holding. Sky lists are immutable values and
// `rt.Concat` has always allocated a fresh slice, so an aliasing twin would make
// `ys ++ zs` visibly mutate a list nobody appended to — a wrong-answer bug that
// only shows up when the left operand happens to carry spare capacity, which is
// exactly the case no fixed-input round-trip test constructs by accident.

func TestListAppendT_doesNotAliasItsLeftOperand(t *testing.T) {
	// A slice with spare capacity, and a second live view of the same array.
	backing := make([]int, 2, 8)
	backing[0], backing[1] = 1, 2
	witness := backing[:2:8]

	got := List_appendT(backing, []int{9, 9})

	if witness[0] != 1 || witness[1] != 2 || len(witness) != 2 {
		t.Fatalf("left operand was mutated: witness = %v", witness)
	}
	// The proof that the result is not a view onto `backing`'s array: writing
	// through the result must not be observable through a re-slice of the
	// original.
	got[0] = 77
	if reSlice := backing[:cap(backing)]; reSlice[0] == 77 {
		t.Fatalf("result aliases the left operand's backing array")
	}
	if backing[0] != 1 {
		t.Fatalf("left operand element 0 changed to %d", backing[0])
	}
}

func TestListAppendT_agreesWithConcat(t *testing.T) {
	cases := []struct{ a, b []int }{
		{nil, nil},
		{[]int{}, []int{}},
		{[]int{1, 2, 3}, nil},
		{nil, []int{4, 5}},
		{[]int{1, 2, 3}, []int{4, 5}},
	}
	for _, c := range cases {
		typed := List_appendT(c.a, c.b)

		anyA := make([]any, len(c.a))
		for i, v := range c.a {
			anyA[i] = v
		}
		anyB := make([]any, len(c.b))
		for i, v := range c.b {
			anyB[i] = v
		}
		erased := AsList(Concat(anyA, anyB))

		if len(typed) != len(erased) {
			t.Fatalf("a=%v b=%v: typed len %d, Concat len %d", c.a, c.b, len(typed), len(erased))
		}
		for i := range typed {
			if !reflect.DeepEqual(any(typed[i]), erased[i]) {
				t.Fatalf("a=%v b=%v: index %d typed %v, Concat %v", c.a, c.b, i, typed[i], erased[i])
			}
		}
	}
}

// A typed element type is the whole point: the erased path boxes every element
// through `reflect.Value.Interface` on its way in and back out again, and the
// twin must produce the same VALUES without that round trip.
func TestListAppendT_typedStructElements(t *testing.T) {
	type row struct {
		K string
		V int
	}
	a := []row{{"a", 1}}
	b := []row{{"b", 2}, {"c", 3}}

	got := List_appendT(a, b)
	want := []row{{"a", 1}, {"b", 2}, {"c", 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Appending again to the FIRST result must not disturb it, which is the
	// same freshness property under the shape codegen actually emits.
	again := List_appendT(got, []row{{"d", 4}})
	if len(got) != 3 || got[2] != (row{"c", 3}) {
		t.Fatalf("first result disturbed by a second append: %v", got)
	}
	if len(again) != 4 {
		t.Fatalf("second append produced %d elements", len(again))
	}
}

// Empty-in / empty-out must be a non-nil empty slice rather than nil, matching
// what `rt.AsListT` hands the rest of the emitted program. `nil` and `[]T{}`
// differ where a Sky value is marshalled (JSON `null` vs `[]`).
func TestListAppendT_emptyOperandsProduceEmptyNotNil(t *testing.T) {
	got := List_appendT([]string{}, []string{})
	if got == nil {
		t.Fatal("empty ++ empty returned nil, not an empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("empty ++ empty returned %d elements", len(got))
	}
}
