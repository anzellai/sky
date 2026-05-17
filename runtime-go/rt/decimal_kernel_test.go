package rt

// Regression fence for Std.Decimal kernel primitives.
// Phase 2.4 — covers the FFI-registered Decimal_* surface and the
// box/unbox invariants. New helpers in decimal_kernel.go MUST land
// with at least one test case here.

import (
	"testing"

	"github.com/shopspring/decimal"
)

// invokeDecimal is the test harness — calls the runtime-registered
// FFI by name, same path the Sky-side `Ffi.callPure` takes.
func invokeDecimal(t *testing.T, name string, args ...any) any {
	t.Helper()
	ffiRegistryMu.RLock()
	fn, ok := ffiPureRegistry[name]
	ffiRegistryMu.RUnlock()
	if !ok {
		t.Fatalf("decimal kernel %q not registered", name)
	}
	return fn(args)
}

// unwrapOk extracts the OkValue of a SkyResult[any,any] for assertions.
// Test fails if the value isn't a Result-shaped Ok.
func unwrapOk(t *testing.T, r any) any {
	t.Helper()
	if sr, ok := r.(SkyResult[any, any]); ok {
		if sr.Tag != 0 {
			t.Fatalf("expected Ok, got Err: %v", sr.ErrValue)
		}
		return sr.OkValue
	}
	t.Fatalf("expected SkyResult, got %T", r)
	return nil
}

func TestDecimal_FromStringToString(t *testing.T) {
	d := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "3.14"))
	got := invokeDecimal(t, "Decimal_toString", d)
	if got != "3.14" {
		t.Fatalf("round-trip: want %q got %q", "3.14", got)
	}
}

func TestDecimal_FromIntFromMinor(t *testing.T) {
	d := invokeDecimal(t, "Decimal_fromInt", 42)
	if invokeDecimal(t, "Decimal_toString", d) != "42" {
		t.Fatalf("fromInt mismatch")
	}
	// 12345 cents = 123.45
	d2 := invokeDecimal(t, "Decimal_fromMinor", 2, 12345)
	if invokeDecimal(t, "Decimal_toString", d2) != "123.45" {
		t.Fatalf("fromMinor mismatch: %v", invokeDecimal(t, "Decimal_toString", d2))
	}
	// Round-trip to minor
	cents := invokeDecimal(t, "Decimal_toMinor", 2, d2)
	if cents != 12345 {
		t.Fatalf("toMinor: want 12345 got %v", cents)
	}
}

func TestDecimal_AddSubMul_Exact(t *testing.T) {
	// 0.1 + 0.2 = 0.3 exactly (the classic float trap).
	a := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "0.1"))
	b := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "0.2"))
	sum := invokeDecimal(t, "Decimal_add", a, b)
	if invokeDecimal(t, "Decimal_toString", sum) != "0.3" {
		t.Fatalf("0.1 + 0.2 mismatch: %v", invokeDecimal(t, "Decimal_toString", sum))
	}
	// Subtraction
	diff := invokeDecimal(t, "Decimal_sub", a, b)
	if invokeDecimal(t, "Decimal_toString", diff) != "-0.1" {
		t.Fatalf("0.1 - 0.2 mismatch: %v", invokeDecimal(t, "Decimal_toString", diff))
	}
	// Multiplication: 0.1 * 0.2 = 0.02
	prod := invokeDecimal(t, "Decimal_mul", a, b)
	if invokeDecimal(t, "Decimal_toString", prod) != "0.02" {
		t.Fatalf("0.1 * 0.2 mismatch: %v", invokeDecimal(t, "Decimal_toString", prod))
	}
}

func TestDecimal_DivByZero(t *testing.T) {
	a := invokeDecimal(t, "Decimal_fromInt", 1)
	b := invokeDecimal(t, "Decimal_fromInt", 0)
	got := invokeDecimal(t, "Decimal_div", a, b)
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", got)
	}
	if sr.Tag != 1 {
		t.Fatalf("expected Err on divide-by-zero, got Ok: %v", sr.OkValue)
	}
}

func TestDecimal_BankersRounding(t *testing.T) {
	// Banker's rounding: half-to-even.
	// 2.5 → 2 (nearest even); 3.5 → 4
	a := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "2.5"))
	r := invokeDecimal(t, "Decimal_round", 0, a)
	if invokeDecimal(t, "Decimal_toString", r) != "2" {
		t.Fatalf("Banker's round(2.5,0): want 2 got %v", invokeDecimal(t, "Decimal_toString", r))
	}
	b := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "3.5"))
	r2 := invokeDecimal(t, "Decimal_round", 0, b)
	if invokeDecimal(t, "Decimal_toString", r2) != "4" {
		t.Fatalf("Banker's round(3.5,0): want 4 got %v", invokeDecimal(t, "Decimal_toString", r2))
	}
}

func TestDecimal_RoundHalfUp(t *testing.T) {
	a := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "2.5"))
	r := invokeDecimal(t, "Decimal_roundHalfUp", 0, a)
	if invokeDecimal(t, "Decimal_toString", r) != "3" {
		t.Fatalf("HalfUp round(2.5): want 3 got %v", invokeDecimal(t, "Decimal_toString", r))
	}
}

func TestDecimal_Predicates(t *testing.T) {
	zero := invokeDecimal(t, "Decimal_zero")
	if invokeDecimal(t, "Decimal_isZero", zero) != true {
		t.Fatal("isZero on zero")
	}
	one := invokeDecimal(t, "Decimal_one")
	if invokeDecimal(t, "Decimal_isPositive", one) != true {
		t.Fatal("isPositive on one")
	}
	neg := invokeDecimal(t, "Decimal_neg", one)
	if invokeDecimal(t, "Decimal_isNegative", neg) != true {
		t.Fatal("isNegative on -one")
	}
}

func TestDecimal_PercentOf(t *testing.T) {
	// 8.875% of 99.99 = 8.8741125
	pct := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "8.875"))
	price := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "99.99"))
	tax := invokeDecimal(t, "Decimal_percentOf", pct, price)
	rounded := invokeDecimal(t, "Decimal_round", 2, tax)
	if invokeDecimal(t, "Decimal_toString", rounded) != "8.87" {
		t.Fatalf("8.875%% of 99.99 (round 2): want 8.87 got %v",
			invokeDecimal(t, "Decimal_toString", rounded))
	}
}

func TestDecimal_FormatWith(t *testing.T) {
	d := unwrapOk(t, invokeDecimal(t, "Decimal_fromString", "1234567.891"))
	if got := invokeDecimal(t, "Decimal_formatWith", ",", ".", 2, d); got != "1,234,567.89" {
		t.Fatalf("US format: want %q got %q", "1,234,567.89", got)
	}
	if got := invokeDecimal(t, "Decimal_formatWith", ".", ",", 2, d); got != "1.234.567,89" {
		t.Fatalf("EU format: want %q got %q", "1.234.567,89", got)
	}
	if got := invokeDecimal(t, "Decimal_formatWith", " ", ",", 0, d); got != "1 234 568" {
		t.Fatalf("FR format (round to 0 dp): want %q got %q", "1 234 568", got)
	}
}

func TestDecimal_Comparisons(t *testing.T) {
	a := invokeDecimal(t, "Decimal_fromInt", 5)
	b := invokeDecimal(t, "Decimal_fromInt", 7)
	if invokeDecimal(t, "Decimal_lt", a, b) != true {
		t.Fatal("5 < 7")
	}
	if invokeDecimal(t, "Decimal_gt", a, b) != false {
		t.Fatal("5 > 7 should be false")
	}
	if invokeDecimal(t, "Decimal_compare", a, b) != -1 {
		t.Fatal("compare 5 7 = -1")
	}
	if invokeDecimal(t, "Decimal_compare", b, a) != 1 {
		t.Fatal("compare 7 5 = 1")
	}
	if invokeDecimal(t, "Decimal_compare", a, a) != 0 {
		t.Fatal("compare 5 5 = 0")
	}
}

func TestDecimal_BoxUnboxRoundtrip(t *testing.T) {
	// Stress: round-trip through SkyADT box and back.
	d := decimal.NewFromFloat(123.456)
	box := decimalBox(d)
	if box.SkyName != "Decimal__Internal" {
		t.Fatalf("box name: want Decimal__Internal got %q", box.SkyName)
	}
	if box.Tag != 0 {
		t.Fatalf("box tag: want 0 got %d", box.Tag)
	}
	if got := decimalUnbox(box); !got.Equal(d) {
		t.Fatalf("unbox: want %v got %v", d, got)
	}
}

// Allocate test lives in money_kernel_test.go (same package).
