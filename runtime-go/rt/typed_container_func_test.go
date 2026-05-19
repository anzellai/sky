package rt

// Regression tests for typed-container conversions involving function
// values. Closes the edge-case classes found during the v0.13 Stage 1
// typed-codegen push (session 2026-05-17):
//
//   * Maybe (Int -> Int) — `Just (\x -> x*3)` must round-trip through
//     `MaybeCoerce[func(int) int]` without panic.
//   * List (Int -> Int) — `AsListT[func(int) int]` must produce non-
//     nil typed functions (previously zero'd each element).
//   * Dict String (Int -> Int) — `AsMapT[func(int) int]` must
//     narrow function values via reflect-based adapter (previously
//     silently dropped them).
//   * Maybe (Maybe (Int -> Int)) — nested SkyMaybe narrowing must
//     recursively call narrowReflectValue (previously corrupted the
//     inner JustValue to zero instantiation).

import (
	"reflect"
	"testing"
)

// helper: build a Sky-shape func(any) any that returns its int arg + d.
func skyAdder(d int) func(any) any {
	return func(x any) any { return AsInt(x) + d }
}

func TestNarrow_MaybeOfFunc_RoundTrips(t *testing.T) {
	// Source: SkyMaybe[any] holding a Sky func(any) any.
	src := Just[any](any(skyAdder(3)))

	out := MaybeCoerce[func(int) int](src)
	if out.Tag != 0 {
		t.Fatalf("expected Just (Tag=0), got Tag=%d", out.Tag)
	}
	if out.JustValue == nil {
		t.Fatal("expected non-nil typed function")
	}
	if got := out.JustValue(10); got != 13 {
		t.Errorf("typed fn(10): want 13, got %d", got)
	}
}

func TestNarrow_NestedMaybeOfFunc_RecursiveCoerce(t *testing.T) {
	// Source: SkyMaybe[any] holding a SkyMaybe[any] holding the fn.
	inner := Just[any](any(skyAdder(7)))
	src := Just[any](any(inner))

	out := MaybeCoerce[SkyMaybe[func(int) int]](src)
	if out.Tag != 0 {
		t.Fatalf("outer: expected Just, got Tag=%d", out.Tag)
	}
	if out.JustValue.Tag != 0 {
		t.Fatalf("inner: expected Just, got Tag=%d", out.JustValue.Tag)
	}
	if out.JustValue.JustValue == nil {
		t.Fatal("inner: expected non-nil typed function")
	}
	if got := out.JustValue.JustValue(5); got != 12 {
		t.Errorf("inner fn(5): want 12, got %d", got)
	}
}

func TestNarrow_ListOfTypedFuncs_AsListT(t *testing.T) {
	src := []any{
		any(skyAdder(1)),
		any(skyAdder(2)),
		any(skyAdder(3)),
	}

	out := AsListT[func(int) int](src)
	if len(out) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(out))
	}
	for i, f := range out {
		if f == nil {
			t.Errorf("element %d: nil function (narrow failed)", i)
			continue
		}
		want := 10 + (i + 1)
		if got := f(10); got != want {
			t.Errorf("element %d (10): want %d, got %d", i, want, got)
		}
	}
}

func TestNarrow_DictOfTypedFuncs_AsMapT(t *testing.T) {
	src := map[string]any{
		"inc": any(skyAdder(1)),
		"dbl": any(func(x any) any { return AsInt(x) * 2 }),
	}

	out := AsMapT[func(int) int](src)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}

	incFn, ok := out["inc"]
	if !ok || incFn == nil {
		t.Fatal("'inc' missing or nil — narrow failed")
	}
	if got := incFn(5); got != 6 {
		t.Errorf("inc(5): want 6, got %d", got)
	}

	dblFn, ok := out["dbl"]
	if !ok || dblFn == nil {
		t.Fatal("'dbl' missing or nil — narrow failed")
	}
	if got := dblFn(5); got != 10 {
		t.Errorf("dbl(5): want 10, got %d", got)
	}
}

func TestNarrow_CoerceInner_FuncTypeFallback(t *testing.T) {
	// Direct test of coerceInner's func-type case: source any holding
	// func(any) any, target func(int) int.
	src := any(skyAdder(100))
	out := coerceInner[func(int) int](src)
	if out == nil {
		t.Fatal("coerceInner returned nil function")
	}
	if got := out(42); got != 142 {
		t.Errorf("fn(42): want 142, got %d", got)
	}
}

func TestNarrow_ReflectFunc_NarrowReflectValue(t *testing.T) {
	// narrowReflectValue with src=func(any) any, target=func(int) int.
	srcFn := skyAdder(50)
	src := reflect.ValueOf(srcFn)
	target := reflect.TypeOf((func(int) int)(nil))

	out := narrowReflectValue(src, target)
	if !out.IsValid() {
		t.Fatal("narrowReflectValue returned invalid value for func conversion")
	}
	if out.Kind() != reflect.Func {
		t.Fatalf("expected Func kind, got %v", out.Kind())
	}
	called := out.Interface().(func(int) int)
	if got := called(5); got != 55 {
		t.Errorf("narrowed fn(5): want 55, got %d", got)
	}
}
