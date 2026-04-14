package rt

import "testing"

// Regression: SkyMaybe[any] flowing into a function declared to return
// SkyMaybe[concreteT] used to panic at coerceInner. The reflect fallback
// now reconstructs the SkyMaybe struct with the target's inner type.
func TestResultCoerceNestedSkyMaybe(t *testing.T) {
	inner := map[string]any{"id": "abc", "title": "test"}
	source := Ok[any, any](Just[any](inner))
	// Target: SkyResult[any, SkyMaybe[map[string]any]]
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, SkyMaybe[map[string]any]](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Ok tag, got %d", coerced.Tag)
	}
	if coerced.OkValue.Tag != 0 {
		t.Fatalf("expected inner Just tag, got %d", coerced.OkValue.Tag)
	}
	if coerced.OkValue.JustValue["id"] != "abc" {
		t.Fatalf("inner map mismatched: %+v", coerced.OkValue.JustValue)
	}
}

// Regression: Result Error (List (Dict String String)) — body produces
// SkyResult[any, []any] of []map[string]any, signature declares
// SkyResult[any, []map[string]any].
func TestResultCoerceNestedList(t *testing.T) {
	rows := []any{
		map[string]any{"id": "1", "title": "a"},
		map[string]any{"id": "2", "title": "b"},
	}
	source := Ok[any, any](rows)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, []map[string]any](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Ok tag, got %d", coerced.Tag)
	}
	if len(coerced.OkValue) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(coerced.OkValue))
	}
	if coerced.OkValue[0]["id"] != "1" {
		t.Fatalf("row[0] id mismatch: %+v", coerced.OkValue[0])
	}
}

// Regression: Result Error (Dict String X) — signature declares
// SkyResult[any, map[string]any], body produces SkyResult[any, any]
// of map[string]any.
func TestResultCoerceNestedDict(t *testing.T) {
	dict := map[string]any{"id": "1", "title": "a"}
	source := Ok[any, any](dict)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, map[string]any](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Ok tag, got %d", coerced.Tag)
	}
	if coerced.OkValue["id"] != "1" {
		t.Fatalf("dict mismatch: %+v", coerced.OkValue)
	}
}

// Regression: nested Dict-of-Dict — Sky's runtime shape for
// Dict String (Dict String String) is map[string]any holding
// map[string]any values; signature may declare map[string]map[string]any.
func TestResultCoerceNestedDictOfDict(t *testing.T) {
	payload := map[string]any{
		"alice": map[string]any{"role": "admin"},
		"bob":   map[string]any{"role": "member"},
	}
	source := Ok[any, any](payload)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, map[string]map[string]any](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Ok tag, got %d", coerced.Tag)
	}
	if coerced.OkValue["alice"]["role"] != "admin" {
		t.Fatalf("alice role mismatch: %+v", coerced.OkValue["alice"])
	}
}

// MaybeCoerce variants — symmetry with ResultCoerce.
func TestMaybeCoerceNestedList(t *testing.T) {
	rows := []any{
		map[string]any{"id": "1"},
		map[string]any{"id": "2"},
	}
	source := Just[any](rows)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MaybeCoerce panicked: %v", r)
		}
	}()
	coerced := MaybeCoerce[[]map[string]any](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Just tag, got %d", coerced.Tag)
	}
	if len(coerced.JustValue) != 2 || coerced.JustValue[0]["id"] != "1" {
		t.Fatalf("list contents mismatch: %+v", coerced.JustValue)
	}
}

// Err-side coercion: Error (Sky.Core.Error ADT) flowing into a typed
// signature that declares SkyResult[Error, A].
func TestResultCoerceErrorSide(t *testing.T) {
	// Simulate rt.Ok/Err returning the any/any shape with an ADT in ErrValue.
	errAdt := ErrIo("db exploded")
	source := Err[any, any](errAdt)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, int](source)
	if coerced.Tag != 1 {
		t.Fatalf("expected Err tag, got %d", coerced.Tag)
	}
	// ErrValue should still be the ADT — any slot preserves it.
	if coerced.ErrValue == nil {
		t.Fatalf("ErrValue unexpectedly nil")
	}
}

// Nothing through the same coercion path.
func TestResultCoerceNestedSkyMaybeNothing(t *testing.T) {
	source := Ok[any, any](Nothing[any]())
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResultCoerce panicked: %v", r)
		}
	}()
	coerced := ResultCoerce[any, SkyMaybe[map[string]any]](source)
	if coerced.Tag != 0 {
		t.Fatalf("expected Ok tag, got %d", coerced.Tag)
	}
	if coerced.OkValue.Tag != 1 {
		t.Fatalf("expected inner Nothing tag, got %d", coerced.OkValue.Tag)
	}
}
