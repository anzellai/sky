package rt

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.29.2 eighth Judge: every `places` argument of the Decimal and Money
// kernels was cut to int32 (`int32(AsInt(…))`), so a value past the int32
// range wrapped round to a small one and gave a silent wrong answer:
// `Decimal.toMinor 4294967298 Decimal.one` was 100, `Decimal.round
// 4294967296 1.23456` was 1. A Decimal's exponent had no bound either:
// `Decimal.mul` of two `fromMinor 2000000000 1` overflowed shopspring's int32
// exponent (an unclassified panic, reported as a compiler bug), and
// `Money.allocate` of a huge part count failed in `make`.

// loudClass runs one kernel through Ffi_callPure and the narrowing its Sky
// signature implies, and returns the class of the panic it raised, or "".
func loudClass(kernel string, args []any, narrow func(any)) (string, string) {
	msg, panicked := recoverMsg(func() { narrow(Ffi_callPure(kernel, args)) })
	if !panicked {
		return "", ""
	}
	kind, _ := classifyPanic(msg)
	return kind, msg
}

func TestDecimalPlacesNeverWrap(t *testing.T) {
	one := decimalArg(t, "1")
	x := decimalArg(t, "1.23456")
	noNarrow := func(any) {}
	asString := func(v any) { _ = AsString(v) }
	asInt := func(v any) { _ = AsInt(v) }
	kernels := []struct {
		name   string
		args   func(p int) []any
		narrow func(any)
	}{
		{"Decimal_fromMinor", func(p int) []any { return []any{p, 12345} }, noNarrow},
		{"Decimal_toStringFixed", func(p int) []any { return []any{p, one} }, asString},
		{"Decimal_toMinor", func(p int) []any { return []any{p, one} }, asInt},
		{"Decimal_round", func(p int) []any { return []any{p, x} }, noNarrow},
		{"Decimal_roundHalfUp", func(p int) []any { return []any{p, x} }, noNarrow},
		{"Decimal_truncate", func(p int) []any { return []any{p, x} }, noNarrow},
		{"Decimal_formatWith", func(p int) []any { return []any{",", ".", p, x} }, asString},
		{"Money_allocate", func(p int) []any { return []any{p, 3, x} }, func(v any) { _ = AsList(v) }},
	}
	outside := []int{
		decimalExponentMax + 1, -decimalExponentMax - 1,
		1 << 31, -(1 << 31) - 1,
		1<<32 + 2, 1 << 32, 1<<32 + 1, -(1 << 32) + 2,
		math.MaxInt64, math.MinInt64,
	}
	for _, k := range kernels {
		for _, p := range outside {
			kind, msg := loudClass(k.name, k.args(p), k.narrow)
			if kind != "NumericOverflow" {
				t.Errorf("%s places=%d: class %q (%q), want NumericOverflow, never a wrapped answer", k.name, p, kind, msg)
			}
		}
		for _, p := range []int{0, 2, -2, 10} {
			if kind, msg := loudClass(k.name, k.args(p), k.narrow); kind != "" {
				t.Errorf("%s places=%d: panicked %s (%q)", k.name, p, kind, msg)
			}
		}
	}
	// The bound itself is inside the range.
	if kind, msg := loudClass("Decimal_round", []any{decimalExponentMax, x}, noNarrow); kind != "" {
		t.Errorf("round at the bound: %s %q", kind, msg)
	}
	// A large in-range shift that leaves the Int range is still NumericOverflow.
	if kind, _ := loudClass("Decimal_toMinor", []any{decimalExponentMax, one}, asInt); kind != "NumericOverflow" {
		t.Errorf("toMinor %d 1: %q, want NumericOverflow", decimalExponentMax, kind)
	}
}

func TestDecimalExponentIsBounded(t *testing.T) {
	tiny := callPureOk(t, "Decimal_fromMinor", decimalExponentMax, 1)
	if kind, msg := loudClass("Decimal_mul", []any{tiny, tiny}, func(any) {}); kind != "NumericOverflow" {
		t.Errorf("mul of two values with exponent -%d: %q (%q), want NumericOverflow", decimalExponentMax, kind, msg)
	}
	// A zero with a huge exponent is zero, not a failure.
	zero := callPureOk(t, "Decimal_fromMinor", decimalExponentMax, 0)
	if kind, msg := loudClass("Decimal_mul", []any{zero, zero}, func(any) {}); kind != "" {
		t.Errorf("mul of two zeros: %s %q", kind, msg)
	}
}

func TestMoneyAllocatePartCount(t *testing.T) {
	amount := decimalArg(t, "100")
	asList := func(v any) { _ = AsList(v) }
	for _, n := range []int{0, -2, math.MinInt64} {
		if kind, msg := loudClass("Money_allocate", []any{2, n, amount}, asList); kind != "DivisionByZero" {
			t.Errorf("allocate %d: %q (%q), want DivisionByZero (the amount must not vanish into [])", n, kind, msg)
		}
	}
	for _, n := range []int{moneyAllocateMaxParts + 1, math.MaxInt64} {
		if kind, msg := loudClass("Money_allocate", []any{2, n, amount}, asList); kind != "NumericOverflow" {
			t.Errorf("allocate %d: %q (%q), want NumericOverflow", n, kind, msg)
		}
	}
	if kind, msg := loudClass("Money_allocate", []any{2, 3, amount}, asList); kind != "" {
		t.Errorf("allocate 3: %s %q", kind, msg)
	}
}

// No int narrowing in the Decimal and Money kernels bypasses decimalPlaces:
// a new `int32(` there is a wrap waiting to happen. A currency's minor units
// come from the fixed currency table (at most 18) and are allowed.
func TestDecimalKernelsNarrowOnlyThroughDecimalPlaces(t *testing.T) {
	re := regexp.MustCompile(`int32\(|int16\(|int8\(|uint32\(`)
	for _, file := range []string{"decimal_kernel.go", "money_kernel.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !re.MatchString(line) {
				continue
			}
			if strings.Contains(line, "return int32(n)") || strings.Contains(line, "info.Minor") {
				continue
			}
			t.Errorf("%s:%d narrows an Int outside decimalPlaces: %s", file, i+1, strings.TrimSpace(line))
		}
	}
}

// callPureOk is the value of a kernel call through Ffi_callPure (its Ok).
func callPureOk(t *testing.T, kernel string, args ...any) any {
	t.Helper()
	r := Ffi_callPure(kernel, args)
	if sr, ok := r.(SkyResult[any, any]); ok {
		if sr.Tag != 0 {
			t.Fatalf("%s %v: %v", kernel, args, r)
		}
		return sr.OkValue
	}
	return r
}
