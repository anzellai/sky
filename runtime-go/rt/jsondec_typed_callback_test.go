package rt

import (
	"testing"
)

// Regression for the typed-codegen vs raw-type-assertion bug
// found 2026-05-18 while wiring the Sky Console.
//
// Symptom: the Sky source `Decode.map (\f -> Math.round f)
// Decode.float` panicked at runtime with:
//
//   panic: interface conversion: interface {} is
//   func(interface {}) int, not func(interface {}) interface {}
//
// at `JsonDec_map`'s `fn.(func(any) any)` type assertion.
//
// Root cause: the v0.13 typed-codegen now infers concrete return
// types for lambdas where it can (here `Math.round : Float -> Int`
// pinned the lambda's return as `int`), so it emits
// `func(f any) int { return rt.CoerceInt(...) }` — NOT the
// `func(any) any` shape that CLAUDE.md claimed every lambda
// always lowered to. The kernel's raw type assertion then
// panics because the actual function shape doesn't match the
// asserted one.
//
// CLAUDE.md was outdated. The contract a Sky-side polymorphic
// HOF's runtime kernel must honour is: accept ANY function
// shape compatible with `any -> any` after coercion. The right
// dispatch mechanism is `SkyCall` (reflect-based), which already
// handles concrete-typed returns by wrapping the result in
// `interface{}` via `out[0].Interface()`.
//
// This test pins the fix at the kernel level. The structurally
// identical scenarios for `JsonDec_andThen` and `JsonList_map`
// (same raw assertion pattern; see git history) are covered by
// the same fix.

func TestJsonDecMap_AcceptsTypedReturnLambda(t *testing.T) {
	// Mirrors the offending lambda shape: `func(any) int`. Before
	// the fix this panics; after the fix it round-trips cleanly.
	dec := JsonDec_map(
		func(v any) int { // typed return — what typed-codegen emits
			f, _ := v.(float64)
			return int(f + 0.5) // round
		},
		JsonDec_float(),
	)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("JsonDec_map panicked on typed-return lambda: %v", r)
		}
	}()
	got := JsonDec_decodeString(dec, "3.7")
	// Result should be Ok(4); the typed `int 4` got wrapped via
	// `interface{}` so the value lands as `any` containing `int(4)`.
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult; got %T %v", got, got)
	}
	if sr.Tag != 0 {
		t.Fatalf("expected Ok; got Err: %#v", sr.ErrValue)
	}
	if i, _ := sr.OkValue.(int); i != 4 {
		t.Errorf("expected 4; got %#v", sr.OkValue)
	}
}

func TestJsonDecMap_AcceptsTypedFloatLambda(t *testing.T) {
	// Another concrete-return shape: float64 -> float64.
	dec := JsonDec_map(
		func(v any) float64 {
			f, _ := v.(float64)
			return f * 2.0
		},
		JsonDec_float(),
	)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("JsonDec_map panicked on float-return lambda: %v", r)
		}
	}()
	got := JsonDec_decodeString(dec, "1.5")
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult; got %T", got)
	}
	if sr.Tag != 0 {
		t.Fatalf("expected Ok; got Err: %#v", sr.ErrValue)
	}
	if f, _ := sr.OkValue.(float64); f != 3.0 {
		t.Errorf("expected 3.0; got %#v", sr.OkValue)
	}
}

func TestJsonDecMap_StillAcceptsAnyToAnyLambda(t *testing.T) {
	// The existing `func(any) any` shape (Sky's pre-typed-codegen
	// default + post-typed-codegen for ambiguous returns) MUST keep
	// working — proves the fix isn't backwards-incompatible.
	dec := JsonDec_map(
		func(v any) any {
			f, _ := v.(float64)
			return f + 100.0
		},
		JsonDec_float(),
	)
	got := JsonDec_decodeString(dec, "5.0")
	sr := got.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("expected Ok; got Err: %#v", sr.ErrValue)
	}
	if f, _ := sr.OkValue.(float64); f != 105.0 {
		t.Errorf("expected 105.0; got %#v", sr.OkValue)
	}
}

func TestJsonDecAndThen_AcceptsTypedReturnLambda(t *testing.T) {
	// `andThen` takes a function that RETURNS a decoder. If the
	// compiler emits a typed concrete return for the closure (e.g.
	// because the lambda body always returns a JsonDecoder built by
	// a typed runtime helper), the kernel's raw assertion would
	// panic the same way as JsonDec_map's.
	dec := JsonDec_andThen(
		func(v any) JsonDecoder {
			// Use the captured value to pick a downstream decoder.
			return JsonDec_succeed(v).(JsonDecoder)
		},
		JsonDec_float(),
	)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("JsonDec_andThen panicked on typed-return lambda: %v", r)
		}
	}()
	got := JsonDec_decodeString(dec, "7.0")
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult; got %T", got)
	}
	if sr.Tag != 0 {
		t.Fatalf("expected Ok; got Err: %#v", sr.ErrValue)
	}
	if f, _ := sr.OkValue.(float64); f != 7.0 {
		t.Errorf("expected 7.0; got %#v", sr.OkValue)
	}
}
