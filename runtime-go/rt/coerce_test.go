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
