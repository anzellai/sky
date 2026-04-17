package rt

// Audit P0-2: numeric coercers and arithmetic/comparison primitives
// must fail loudly on type mismatch, not silently return zero. Pre-fix:
//   rt.AsInt("hello") == 0
//   rt.Add("x", 1)    == 1
//   rt.Lt("a", "b")   == (0 < 0) == false
// The fix makes AsInt/AsBool/AsFloat panic on mismatch (surface as Err
// via rt's panic-recovery) and makes arithmetic + comparison type-
// aware so strings compare lexicographically and floats keep their
// precision.

import (
	"fmt"
	"testing"
)

// Recover-into-string helper.
func didPanic(run func()) (panicked bool, msg string) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg = fmt.Sprintf("%v", r)
		}
	}()
	run()
	return
}

func TestAsInt_PanicsOnNonNumeric(t *testing.T) {
	// Pre-fix: AsInt("hello") == 0. Post-fix: panic with descriptive msg.
	cases := []any{"hello", true, nil, []int{1, 2, 3}, map[string]any{}}
	for _, c := range cases {
		panicked, msg := didPanic(func() { _ = AsInt(c) })
		if !panicked {
			t.Fatalf("AsInt(%v of %T) did not panic (returned silently — coercion hole)", c, c)
		}
		// Message should name the offending type so users can diagnose.
		if !contains(msg, "rt.AsInt") {
			t.Fatalf("AsInt panic message missing rt.AsInt prefix: %q", msg)
		}
	}
}

func TestAsInt_AcceptsNumerics(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{42, 42},
		{int64(42), 42},
		{int32(42), 42},
		{float64(3.7), 3}, // truncates — matches Sky's `floor` semantics for the int coercion path
		{float32(2.9), 2},
	}
	for _, c := range cases {
		got := AsInt(c.in)
		if got != c.want {
			t.Fatalf("AsInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAsIntOrZero_StillLenient(t *testing.T) {
	// The explicit display-only fallback.
	if AsIntOrZero("hello") != 0 {
		t.Fatal("AsIntOrZero should return 0 on non-numeric (that's the whole point)")
	}
	if AsIntOrZero(42) != 42 {
		t.Fatal("AsIntOrZero should still accept numerics")
	}
}

func TestAsBool_PanicsOnNonBool(t *testing.T) {
	panicked, _ := didPanic(func() { _ = AsBool("true") })
	if !panicked {
		t.Fatal("AsBool(string) did not panic — silently returning false hides type errors")
	}
	if AsBool(true) != true {
		t.Fatal("AsBool(true) should be true")
	}
	if AsBoolOrFalse("true") != false {
		t.Fatal("AsBoolOrFalse(string) should return false")
	}
}

func TestAsFloat_PanicsOnNonNumeric(t *testing.T) {
	panicked, _ := didPanic(func() { _ = AsFloat("3.14") })
	if !panicked {
		t.Fatal("AsFloat(string) did not panic — silent 0 on mismatch is a coercion hole")
	}
	if AsFloat(3.14) != 3.14 {
		t.Fatal("AsFloat(3.14) should round-trip")
	}
	if AsFloat(int(5)) != 5.0 {
		t.Fatal("AsFloat should accept int")
	}
	if AsFloatOrZero("3.14") != 0 {
		t.Fatal("AsFloatOrZero(string) should return 0")
	}
}

func TestArithmetic_FloatAware(t *testing.T) {
	// Pre-fix: Add(1.5, 2.5) went through AsInt → 1 + 2 == 3. Precision
	// loss, silently wrong.
	if got := Add(1.5, 2.5); got != 4.0 {
		t.Fatalf("Add(1.5, 2.5) = %v, want 4.0 (float-aware addition)", got)
	}
	if got := Mul(2.5, 4.0); got != 10.0 {
		t.Fatalf("Mul(2.5, 4.0) = %v, want 10.0", got)
	}
	// Int arithmetic still intact.
	if got := Add(3, 4); got != 7 {
		t.Fatalf("Add(3, 4) = %v, want 7", got)
	}
}

func TestArithmetic_RejectsNonNumeric(t *testing.T) {
	// Pre-fix: Add("hello", 1) silently returned 1. Now it panics.
	panicked, _ := didPanic(func() { _ = Add("hello", 1) })
	if !panicked {
		t.Fatal("Add(string, int) did not panic — silent wrong answer was the pre-fix bug")
	}
}

func TestDiv_FloatByDefault(t *testing.T) {
	// Sky's `/` is float division (Elm convention).
	if got := Div(10, 4); got != 2.5 {
		t.Fatalf("Div(10, 4) = %v, want 2.5 (float division)", got)
	}
}

func TestDiv_ByZeroPanics(t *testing.T) {
	// Pre-fix: Div(1, 0) silently returned 0. Now it panics → Err at
	// the Task boundary. Callers that want a specific default must
	// test explicitly.
	panicked, _ := didPanic(func() { _ = Div(1, 0) })
	if !panicked {
		t.Fatal("Div by 0 should panic, not silently return 0")
	}
}

func TestLt_StringsCompareLexicographically(t *testing.T) {
	// Pre-fix: "apple" < "banana" went through AsInt → 0 < 0 → false.
	// Both Strings treated as 0, comparison always false. Silent wrong
	// answer for any string comparison.
	if got := Lt("apple", "banana"); got != true {
		t.Fatalf("Lt(\"apple\", \"banana\") = %v, want true", got)
	}
	if got := Lt("banana", "apple"); got != false {
		t.Fatalf("Lt(\"banana\", \"apple\") = %v, want false", got)
	}
	// Equality case.
	if got := Lt("same", "same"); got != false {
		t.Fatalf("Lt(equal strings) = %v, want false", got)
	}
}

func TestLt_FloatsPreservePrecision(t *testing.T) {
	// Pre-fix: 2.7 < 2.5 went through AsInt → 2 < 2 → false (correct by
	// accident). 2.3 < 2.7 went through AsInt → 2 < 2 → false (WRONG).
	if got := Lt(2.3, 2.7); got != true {
		t.Fatalf("Lt(2.3, 2.7) = %v, want true (pre-fix was wrong via int truncation)", got)
	}
}

func TestLt_RejectsMismatchedTypes(t *testing.T) {
	// String vs int comparison is a user error — panic, don't silently
	// pick one interpretation.
	panicked, _ := didPanic(func() { _ = Lt("a", 1) })
	if !panicked {
		t.Fatal("Lt(string, int) should panic on type mismatch")
	}
}

func TestNegate_FloatAware(t *testing.T) {
	if got := Negate(3.14); got != -3.14 {
		t.Fatalf("Negate(3.14) = %v, want -3.14", got)
	}
	if got := Negate(5); got != -5 {
		t.Fatalf("Negate(5) = %v, want -5", got)
	}
}

// --- helpers

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
