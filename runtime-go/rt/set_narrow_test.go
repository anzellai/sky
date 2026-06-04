package rt

import (
	"reflect"
	"testing"
)

// #461 — SkySet → map[any]V narrowing. Sky's typed Go form for `Set a`
// is `map[any]bool`, but rt.Set_* kernels return an opaque `SkySet`
// struct ({items: map[string]any}). At a cross-module call where the
// callee is declared `Set String`, the typed-codegen wrapper emits
// `rt.Coerce[map[any]bool](Set_fromList(...))`. Pre-fix this panicked
// with `rt.Coerce: expected map[interface {}]bool, got rt.SkySet`.
//
// The fix routes through skySetToMap, which iterates SkySet.items's
// VALUES (the originals; the keys are stringified reps used for
// dedup) and builds a typed map[any]V.

func TestSkySetToMapStringElements(t *testing.T) {
	// SkySet of three strings, one duplicate (matches the #461 repro
	// shape: `Set.fromList ["a", "b", "a"]`).
	src := Set_fromList(AsListAny([]any{"a", "b", "a"})).(SkySet)

	out, ok := skySetToMap(src, reflect.TypeOf(map[any]bool{}))
	if !ok {
		t.Fatalf("skySetToMap returned ok=false for a SkySet → map[any]bool conversion")
	}
	m, ok := out.Interface().(map[any]bool)
	if !ok {
		t.Fatalf("skySetToMap did not return map[any]bool, got %T", out.Interface())
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 unique elements, got %d (%v)", len(m), m)
	}
	if !m["a"] || !m["b"] {
		t.Fatalf("expected {a:true, b:true}, got %v", m)
	}
}

func TestSkySetToMapIntElements(t *testing.T) {
	src := Set_fromList(AsListAny([]any{1, 2, 3, 2})).(SkySet)
	out, ok := skySetToMap(src, reflect.TypeOf(map[any]bool{}))
	if !ok {
		t.Fatalf("skySetToMap returned ok=false")
	}
	m := out.Interface().(map[any]bool)
	if len(m) != 3 {
		t.Fatalf("expected 3 unique elements, got %d (%v)", len(m), m)
	}
}

func TestSkySetToMapEmpty(t *testing.T) {
	src := Set_fromList(AsListAny([]any{})).(SkySet)
	out, ok := skySetToMap(src, reflect.TypeOf(map[any]bool{}))
	if !ok {
		t.Fatalf("skySetToMap returned ok=false for empty SkySet")
	}
	m := out.Interface().(map[any]bool)
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

// Top-level Coerce path: this is the SHAPE that fails in the #461
// repro. Without the skySetToMap branch in Coerce, this panicked.
func TestCoerceSkySetToMapAny(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rt.Coerce[map[any]bool] panicked on a SkySet input: %v", r)
		}
	}()
	src := Set_fromList(AsListAny([]any{"a", "b", "a"}))
	m := Coerce[map[any]bool](src)
	if len(m) != 2 {
		t.Fatalf("expected 2 unique elements, got %d (%v)", len(m), m)
	}
	if !m["a"] || !m["b"] {
		t.Fatalf("expected {a:true, b:true}, got %v", m)
	}
}

// Coerce should leave non-SkySet inputs alone — make sure the new
// branch is gated tight and doesn't catch generic struct → map cases.
func TestCoerceMapAnyHappyPathNotRegressed(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rt.Coerce[map[any]bool] panicked on an identity input: %v", r)
		}
	}()
	src := map[any]bool{"a": true, "b": true}
	m := Coerce[map[any]bool](src)
	if len(m) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(m))
	}
}

// Reverse direction — `map[any]V` flowing back into an any-typed
// kernel like Set_insert must still produce the right answer.
// toSkySet now accepts the map shape directly so the dedup string-
// keying still works.
func TestToSkySetAcceptsMap(t *testing.T) {
	src := map[any]bool{"a": true, "b": true, "c": true}
	got := toSkySet(src)
	if len(got.items) != 3 {
		t.Fatalf("expected 3 items in reified SkySet, got %d (%v)", len(got.items), got.items)
	}
}

// End-to-end shape: Set.insert(Set.fromList(...)) through the any-
// typed kernels with map[any]bool boxing between. Mirrors the chain
// shape in the #461 cross-module bug after the cross-call narrows
// the SkySet to a typed map.
func TestSetInsertViaTypedMap(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Set kernel chain panicked through map[any]bool boxing: %v", r)
		}
	}()
	s1 := Set_fromList(AsListAny([]any{"a", "b"}))
	asMap := Coerce[map[any]bool](s1) // simulate the cross-module return narrow
	s2 := Set_insert("c", any(asMap))
	asMap2 := Coerce[map[any]bool](s2)
	if len(asMap2) != 3 {
		t.Fatalf("expected 3 elements after insert, got %d (%v)", len(asMap2), asMap2)
	}
}
