package rt

import (
	"testing"
)

// A Sky record, standing in for the `Attr`/`Element` shapes whose lists the
// cons-pattern destructuring actually walks.
type destrElem struct {
	Name  string
	Index int
}

// What the `any`-taking destructuring helpers cost on a TYPED slice.
//
// `SkyLen` / `SkyElem` / `SkyTailSlice` take `x any` and route through
// `AsList`. `AsList` fast-paths a `[]any` — but a `[]T` for any other T misses
// that assertion and falls to the reflect arm, which allocates a fresh `[]any`
// of length n and boxes EVERY element into it (rt.go, `AsList`). So on a typed
// list each of these is O(n) allocation, not the one boxed slice header the
// signature suggests, and a cons loop over n elements rebuilds the whole list
// on every iteration.
//
// This test pins that cost so the typed variants below have something to be
// measured against, and so a future change to `AsList` cannot quietly make this
// claim false while the comment above still asserts it.
func TestUntypedDestructuringOnATypedSliceAllocates(t *testing.T) {
	xs := make([]destrElem, 16)
	for i := range xs {
		xs[i] = destrElem{Name: "a", Index: i}
	}

	lenAllocs := testing.AllocsPerRun(100, func() { _ = SkyLen(xs) })
	elemAllocs := testing.AllocsPerRun(100, func() { _ = SkyElem(xs, 0) })
	tailAllocs := testing.AllocsPerRun(100, func() { _ = SkyTailSlice(xs) })

	if lenAllocs == 0 || elemAllocs == 0 || tailAllocs == 0 {
		t.Fatalf("expected the any-taking helpers to allocate on a typed slice "+
			"(that is the cost the typed variants exist to remove); got "+
			"SkyLen=%v SkyElem=%v SkyTailSlice=%v", lenAllocs, elemAllocs, tailAllocs)
	}
	t.Logf("any-taking, []destrElem len 16: SkyLen=%v SkyElem=%v SkyTailSlice=%v allocs/op",
		lenAllocs, elemAllocs, tailAllocs)
}

// The typed variants must be allocation-free, on a typed slice AND on `[]any`.
//
// Zero is the assertion, not "fewer": these compile to `len(xs)`, `xs[i]` and
// `xs[1:]` behind a bounds guard, so anything above zero means a boxing path
// crept back in.
func TestTypedDestructuringDoesNotAllocate(t *testing.T) {
	xs := make([]destrElem, 16)
	for i := range xs {
		xs[i] = destrElem{Name: "a", Index: i}
	}

	if got := testing.AllocsPerRun(100, func() { _ = SkyLenT(xs) }); got != 0 {
		t.Errorf("SkyLenT allocated %v/op, want 0", got)
	}
	if got := testing.AllocsPerRun(100, func() { _ = SkyElemT(xs, 0) }); got != 0 {
		t.Errorf("SkyElemT allocated %v/op, want 0", got)
	}
	if got := testing.AllocsPerRun(100, func() { _ = SkyTailSliceT(xs) }); got != 0 {
		t.Errorf("SkyTailSliceT allocated %v/op, want 0", got)
	}

	ys := []any{1, 2, 3}
	if got := testing.AllocsPerRun(100, func() { _ = SkyLenT(ys) }); got != 0 {
		t.Errorf("SkyLenT on []any allocated %v/op, want 0", got)
	}
}

// The typed variants must agree with the `any` ones on every value both can
// take, INCLUDING the out-of-range and empty cases the pattern guard is
// supposed to have already excluded. "The guard ran first" is a claim about the
// caller; a helper that panics when it is wrong turns a lowering bug into a
// runtime panic, which the language's whole contract forbids.
func TestTypedDestructuringMatchesTheUntypedSemantics(t *testing.T) {
	for _, xs := range [][]any{nil, {}, {1}, {1, 2, 3}} {
		if got, want := SkyLenT(xs), SkyLen(xs); got != want {
			t.Errorf("SkyLenT(%v)=%d, SkyLen=%d", xs, got, want)
		}
		for i := -1; i <= len(xs); i++ {
			if got, want := SkyElemT(xs, i), SkyElem(xs, i); got != want {
				t.Errorf("SkyElemT(%v,%d)=%v, SkyElem=%v", xs, i, got, want)
			}
		}
		got, want := SkyTailSliceT(xs), SkyTailSlice(xs)
		if len(got) != len(want) {
			t.Errorf("SkyTailSliceT(%v) len %d, SkyTailSlice len %d", xs, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("SkyTailSliceT(%v)[%d]=%v, want %v", xs, i, got[i], want[i])
			}
		}
	}

	// The zero value for a non-`any` T: out of range must yield T's zero, not
	// panic, mirroring `SkyElem`'s nil.
	typed := []destrElem{{Name: "a"}}
	if got := SkyElemT(typed, 5); got != (destrElem{}) {
		t.Errorf("SkyElemT out of range = %v, want the zero value", got)
	}
	if got := SkyElemT(typed, -1); got != (destrElem{}) {
		t.Errorf("SkyElemT negative index = %v, want the zero value", got)
	}
	if got := SkyTailSliceT([]destrElem(nil)); len(got) != 0 {
		t.Errorf("SkyTailSliceT(nil) = %v, want empty", got)
	}
}
