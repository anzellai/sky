package rt

// Audit P0-6: skyCallDirect must reject argument-type mismatches at
// the boundary with a clear diagnostic, not silently pass the wrong
// value into reflect.Call. Pre-fix path:
//
//	if av.Type() == pt {
//	    vals[i] = av
//	} else if av.Type().ConvertibleTo(pt) {
//	    vals[i] = av.Convert(pt)
//	} else {
//	    vals[i] = av   // ← silent; reflect.Call panics inside
//	}
//
// reflect.Call's panic was caught by SkyFfiRecover and surfaced as
// Err, which is safe but masked the real boundary check. The fix
// surfaces a "rt.skyCallDirect: argument N type mismatch" diagnostic
// directly so observability shows the FFI mismatch.

import (
	"strings"
	"testing"
)

func TestSkyCallDirect_RejectsTypeMismatchEarly(t *testing.T) {
	// A function that takes a string. Pre-fix, passing an int would
	// reach reflect.Call and panic with reflect's cryptic message.
	// Post-fix, skyCallDirect itself panics with rt.skyCallDirect tag.
	stringFn := func(s string) string { return "hi " + s }
	panicked, msg := didPanic(func() { _ = SkyCall(stringFn, 42) })
	if !panicked {
		t.Fatal("SkyCall(string-fn, int) did not panic — silent bad-arg-pass hole")
	}
	if !strings.Contains(msg, "rt.skyCallDirect") {
		t.Fatalf("Type-mismatch panic should carry rt.skyCallDirect tag: %q", msg)
	}
	if !strings.Contains(msg, "argument 0") {
		t.Fatalf("Panic should identify which argument index: %q", msg)
	}
}

func TestSkyCallDirect_AnyParamAcceptsAnything(t *testing.T) {
	// Sky-internal dispatch is mostly all-`any`. Calling such a
	// function with any value must keep working — the strict check
	// is gated on real-type params (string, int, struct), not the
	// universal `any`.
	anyFn := func(v any) any { return v }
	got := SkyCall(anyFn, "hello")
	if got != "hello" {
		t.Fatalf("SkyCall(any-fn, \"hello\") = %v, want \"hello\"", got)
	}
	got = SkyCall(anyFn, 42)
	if got != 42 {
		t.Fatalf("SkyCall(any-fn, 42) = %v, want 42", got)
	}
}

func TestSkyCallDirect_NumericWideningStillWorks(t *testing.T) {
	// int → int64 is a safe Go conversion that legit Sky-internal
	// dispatch relies on (e.g. SQL parameter binding). The strict
	// check whitelists numeric widening so this path doesn't break.
	int64Fn := func(n int64) int64 { return n * 2 }
	got := SkyCall(int64Fn, 21)
	if got != int64(42) {
		t.Fatalf("SkyCall(int64-fn, 21) = %v, want 42", got)
	}
}
