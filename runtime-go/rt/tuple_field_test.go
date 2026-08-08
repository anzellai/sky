package rt

import "testing"

// TupleField is the reflective, shape-erased tuple-element accessor the tuple
// PATTERN arm uses when the subject arrives as `any` (a HOF-erased callback
// param such as foldr's `func(any,any)any`, a let/case-bound erased value).
// `subj.V{i}` is invalid Go on an `any`, and coercing the whole subject to a
// reconstructed generic instantiation is fragile because Go generics are
// invariant (`T2[[]int, any]` is NOT `T2[[]int, []int]`). These tests pin that
// TupleField reads the i-th element across EVERY tuple instantiation.
func TestTupleField_SkyTuple2(t *testing.T) {
	tup := SkyTuple2{V0: "a", V1: 2}
	if got := TupleField(tup, 0); got != "a" {
		t.Fatalf("TupleField(SkyTuple2, 0) = %v, want \"a\"", got)
	}
	if got := TupleField(tup, 1); got != 2 {
		t.Fatalf("TupleField(SkyTuple2, 1) = %v, want 2", got)
	}
	if got := TupleField(tup, 2); got != nil {
		t.Fatalf("TupleField(SkyTuple2, 2) = %v, want nil (out of range)", got)
	}
}

func TestTupleField_SkyTuple3(t *testing.T) {
	tup := SkyTuple3{V0: 1, V1: "b", V2: true}
	if got := TupleField(tup, 2); got != true {
		t.Fatalf("TupleField(SkyTuple3, 2) = %v, want true", got)
	}
}

// The invariance case: a typed generic instantiation distinct from
// SkyTuple2=T2[any,any]. Direct `.V0` in emitted Go works only when the static
// type is known; TupleField must read it reflectively regardless.
func TestTupleField_TypedGenericInstantiation(t *testing.T) {
	tup := T2[[]any, []int]{V0: []any{"x"}, V1: []int{4, 5}}
	got0 := TupleField(tup, 0)
	if xs, ok := got0.([]any); !ok || len(xs) != 1 || xs[0] != "x" {
		t.Fatalf("TupleField(T2[[]any,[]int], 0) = %v (%T), want []any{\"x\"}", got0, got0)
	}
	got1 := TupleField(tup, 1)
	if xs, ok := got1.([]int); !ok || len(xs) != 2 || xs[1] != 5 {
		t.Fatalf("TupleField(T2[[]any,[]int], 1) = %v (%T), want []int{4,5}", got1, got1)
	}
}

// Slice-backed arity ≥10 tuple.
func TestTupleField_SkyTupleN(t *testing.T) {
	tup := SkyTupleN{Vs: []any{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, "ten"}}
	if got := TupleField(tup, 10); got != "ten" {
		t.Fatalf("TupleField(SkyTupleN, 10) = %v, want \"ten\"", got)
	}
	if got := TupleField(tup, 11); got != nil {
		t.Fatalf("TupleField(SkyTupleN, 11) = %v, want nil (out of range)", got)
	}
}

func TestTupleField_NonTupleAndNil(t *testing.T) {
	if got := TupleField(nil, 0); got != nil {
		t.Fatalf("TupleField(nil, 0) = %v, want nil", got)
	}
	if got := TupleField(42, 0); got != nil {
		t.Fatalf("TupleField(non-struct, 0) = %v, want nil", got)
	}
}
