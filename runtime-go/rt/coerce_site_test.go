package rt

// Audit P0-3: typed-return boundary coercion must go through a named
// runtime helper with a diagnostic message on mismatch, not a raw
// `any(body).(T)` assertion that panics with Go's cryptic interface-
// conversion message. Pre-fix, codegen emitted raw assertions at
// typed-return sites; regression class was bug C1 (fibonacci `.(int)`,
// http-server Task-coerce, skychess Piece-ctor).

import (
	"strings"
	"testing"
)

func TestCoerce_PassThroughSameType(t *testing.T) {
	s := Coerce[string]("hello")
	if s != "hello" {
		t.Fatalf("Coerce[string](\"hello\") = %q, want \"hello\"", s)
	}
	i := Coerce[int](42)
	if i != 42 {
		t.Fatalf("Coerce[int](42) = %d, want 42", i)
	}
}

func TestCoerce_PanicsOnMismatch(t *testing.T) {
	// The whole point of P0-3: mismatch produces a clear runtime panic
	// carrying both the expected and actual type. Raw `.(T)` pre-fix
	// produced an opaque "interface conversion" message.
	panicked, msg := didPanic(func() { _ = Coerce[string](42) })
	if !panicked {
		t.Fatal("Coerce[string](42) did not panic")
	}
	if !strings.Contains(msg, "rt.Coerce") {
		t.Fatalf("Coerce panic missing rt.Coerce prefix: %q", msg)
	}
	if !strings.Contains(msg, "int") {
		t.Fatalf("Coerce panic should name the actual type 'int': %q", msg)
	}
}

func TestCoerceString_StrictButClear(t *testing.T) {
	if CoerceString("ok") != "ok" {
		t.Fatal("CoerceString identity broken")
	}
	panicked, msg := didPanic(func() { _ = CoerceString(123) })
	if !panicked {
		t.Fatal("CoerceString(123) did not panic — silent coercion hole")
	}
	if !strings.Contains(msg, "rt.CoerceString") {
		t.Fatalf("missing rt.CoerceString in panic: %q", msg)
	}
}

func TestCoerceInt_DelegatesToAsInt(t *testing.T) {
	if CoerceInt(42) != 42 {
		t.Fatal("CoerceInt identity broken")
	}
	// Inherits AsInt's float-truncate semantics.
	if CoerceInt(3.7) != 3 {
		t.Fatal("CoerceInt(3.7) should truncate to 3")
	}
	// AsInt panics on non-numeric, so CoerceInt does too.
	panicked, _ := didPanic(func() { _ = CoerceInt("hello") })
	if !panicked {
		t.Fatal("CoerceInt on non-numeric should panic (via AsInt)")
	}
}

// SkyTask paths exercise TaskCoerce (already covered by existing
// http-server runtime tests), so no duplicate here. This spec guards
// the generic string/int/bool/float + reflect path introduced for
// P0-3.
