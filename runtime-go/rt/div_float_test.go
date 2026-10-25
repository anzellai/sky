package rt

import (
	"math"
	"strings"
	"testing"
)

// `DivF` is the typed twin of `Div` for float `/` over two operands the
// compiler proves are `Float` (doc 14 R5, lever §5.3). A twin that differs from
// what it replaces is a miscompile, so the tests are differential against the
// erased `Div` rather than against hand-written values.

func TestDivF_agreesWithDiv(t *testing.T) {
	vals := []float64{1, -1, 3, 0.1, 1e-300, 1e300, -2.5, math.MaxFloat64, math.SmallestNonzeroFloat64, 7.0 / 3.0}
	for _, a := range append(vals, 0, math.Copysign(0, -1)) {
		for _, b := range vals {
			typed := DivF(a, b)
			erased := AsFloat(Div(a, b))
			if math.Float64bits(typed) != math.Float64bits(erased) {
				t.Fatalf("%v / %v: DivF %v (bits %x), Div %v (bits %x)", a, b, typed, math.Float64bits(typed), erased, math.Float64bits(erased))
			}
		}
	}
}

// Both forms fail the same way on a zero divisor (positive or negative zero):
// the panic that classifyPanic names DivisionByZero, never a silent ±Inf/NaN.
func TestDivF_zeroDivisorIsTheClassifiedPanicOfDiv(t *testing.T) {
	catch := func(f func()) (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		f()
		return ""
	}
	for _, z := range []float64{0, math.Copysign(0, -1)} {
		typed := catch(func() { DivF(1, z) })
		erased := catch(func() { Div(1.0, z) })
		if typed == "" || typed != erased {
			t.Fatalf("divisor %v: DivF panicked %q, Div panicked %q", z, typed, erased)
		}
		if !strings.Contains(typed, "division by zero") {
			t.Fatalf("divisor %v: panic %q is not the division-by-zero class", z, typed)
		}
		tk, _ := classifyPanic(typed)
		ek, _ := classifyPanic(erased)
		if tk != ek || tk == "" {
			t.Fatalf("divisor %v: DivF class %q, Div class %q", z, tk, ek)
		}
	}
}
