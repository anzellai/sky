package rt

// Std.Decimal — arbitrary-precision decimal arithmetic. Production-
// grade surface: comparison helpers, rounding modes, abs/neg, min/
// max, percent, constants, formatting with thousand-separators.
// Backed by github.com/shopspring/decimal.
//
// Sky-side surface lives in sky-stdlib/Std/Decimal.sky. This file
// is the FFI boundary — registers Decimal_* primitives via
// RegisterPure so the Sky source can invoke them through
// Sky.Ffi.callPure.
//
// The Sky `Decimal` type is an opaque ADT (`Decimal__Internal Float`
// with field hidden); the actual decimal.Decimal lives in the
// SkyADT's Fields[0] at runtime — invisible from Sky source so
// users can't break the invariant by reading the phantom Float.

import (
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"
)

// SkyDecimal is the user-facing alias. The Sky-source `Decimal`
// type wraps this in a SkyADT box at runtime.
type SkyDecimal = decimal.Decimal

// decimalBox wraps a decimal.Decimal in the Sky `Decimal` ADT shape
// (single-constructor `Decimal__Internal Float`). Fields[0] holds
// the actual decimal — the Float in the Sky source is purely a
// phantom type-shape so the constructor has a slot.
//
// Every Sky Decimal is made here, so this is where the Decimal range is kept
// (v0.29.2 eighth Judge): a Decimal whose exponent is outside
// ±decimalExponentMax fails as the classified NumericOverflow. Without the
// bound `Decimal.mul` of two values with exponent -2000000000 overflowed
// shopspring's int32 exponent (an unclassified panic reported as a compiler
// bug), and `Decimal.toString` of `fromMinor 2000000000 1` built a
// two-billion-character string.
func decimalBox(d decimal.Decimal) SkyADT {
	if e := d.Exponent(); e > decimalExponentMax || e < -decimalExponentMax {
		if d.IsZero() {
			d = decimal.Zero
		} else {
			panic(fmt.Sprintf("rt.Decimal.range: a Decimal with exponent %d is outside the Decimal range (exponent -%d to %d)", e, decimalExponentMax, decimalExponentMax))
		}
	}
	return SkyADT{Tag: 0, SkyName: "Decimal__Internal", Fields: []any{d}}
}

// decimalExponentMax bounds the exponent of every Decimal (decimalBox) and
// every `places` argument (decimalPlaces). A Decimal inside the bound has a
// text of at most about 100,000 digits plus its coefficient, and the sum of
// two exponents (Decimal.mul) stays far inside shopspring's int32 exponent.
const decimalExponentMax = 100_000

// decimalPlaces is the `places` argument v of the Decimal or Money operation
// op as the int32 shopspring takes. A value outside ±decimalExponentMax fails
// as the classified NumericOverflow (v0.29.2 eighth Judge): it used to be cut
// to int32, so `Decimal.toMinor 4294967298 Decimal.one` gave 100 and
// `Decimal.round 4294967296 1.23456` gave 1, a silent wrong answer.
func decimalPlaces(op string, v any) int32 {
	n := AsInt(v)
	if n > decimalExponentMax || n < -decimalExponentMax {
		panic(fmt.Sprintf("rt.Decimal.range: %s: %d decimal places is outside the range -%d to %d", op, n, decimalExponentMax, decimalExponentMax))
	}
	return int32(n)
}

// decimalUnbox extracts the decimal.Decimal from a Sky `Decimal`
// ADT, falling back to zero on shape mismatch.
func decimalUnbox(v any) decimal.Decimal {
	if adt, ok := v.(SkyADT); ok && len(adt.Fields) > 0 {
		switch x := adt.Fields[0].(type) {
		case decimal.Decimal:
			return x
		case string:
			if d, err := decimalParseBounded(x); err == nil {
				return d
			}
		case float64:
			return decimal.NewFromFloat(x)
		case int:
			return decimal.NewFromInt(int64(x))
		case int64:
			return decimal.NewFromInt(x)
		}
	}
	if d, ok := v.(decimal.Decimal); ok {
		return d
	}
	if s, ok := v.(string); ok {
		if d, err := decimalParseBounded(s); err == nil {
			return d
		}
	}
	return decimal.Zero
}

func init() {
	// ── Construction ────────────────────────────────────────────

	RegisterPure("Decimal_fromString", func(args []any) any {
		if len(args) < 1 {
			return Err[any, any](ErrDecode("Decimal.fromString: missing arg"))
		}
		str := fmt.Sprintf("%v", args[0])
		d, err := decimalParseBounded(str)
		if err != nil {
			return Err[any, any](ErrDecode("Decimal.fromString: " + err.Error()))
		}
		return Ok[any, any](decimalBox(d))
	})

	RegisterPure("Decimal_fromInt", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimal.NewFromInt(int64(AsInt(args[0]))))
	})

	RegisterPure("Decimal_fromFloat", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		f := AsFloat(args[0])
		// A NaN or an infinite Float (Math.sqrt -1, Math.pow 10 400) has no
		// decimal value. shopspring panics with an unclassified message; Sky
		// has no NaN or Infinity shape, so this fails as the classified
		// DivisionByZero class a Float division with no answer fails as
		// (v0.29.2 fifth Judge audit), never a silent zero.
		if math.IsNaN(f) || math.IsInf(f, 0) {
			panic(fmt.Sprintf("rt.Decimal.fromFloat: %v has no decimal value", f))
		}
		return decimalBox(decimal.NewFromFloat(f))
	})

	// fromMinor : Int -> Int -> Decimal — `fromMinor 2 12345` = 123.45
	// (cents → dollars). For money construction from integer cents.
	RegisterPure("Decimal_fromMinor", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		minor := int64(AsInt(args[1]))
		places := decimalPlaces("Decimal.fromMinor", args[0])
		return decimalBox(decimal.New(minor, -places))
	})

	// ── Constants ───────────────────────────────────────────────

	RegisterPure("Decimal_zero", func(args []any) any {
		return decimalBox(decimal.Zero)
	})
	RegisterPure("Decimal_one", func(args []any) any {
		return decimalBox(decimal.NewFromInt(1))
	})
	RegisterPure("Decimal_oneHundred", func(args []any) any {
		return decimalBox(decimal.NewFromInt(100))
	})

	// ── Conversion ──────────────────────────────────────────────

	RegisterPure("Decimal_toString", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return decimalUnbox(args[0]).String()
	})

	// toStringFixed : Int -> Decimal -> String — always shows N
	// decimal places (e.g. `toStringFixed 2` on Decimal(3) = "3.00").
	RegisterPure("Decimal_toStringFixed", func(args []any) any {
		if len(args) < 2 {
			return ""
		}
		return decimalUnbox(args[1]).StringFixed(decimalPlaces("Decimal.toStringFixed", args[0]))
	})

	// toFloat : Decimal -> Float (LOSSY — display only). A decimal past the
	// Float range (1e400) has no Float: it fails as the classified
	// NumericOverflow, never an Infinity (Sky has no Infinity shape).
	RegisterPure("Decimal_toFloat", func(args []any) any {
		if len(args) < 1 {
			return 0.0
		}
		d := decimalUnbox(args[0])
		v, _ := d.Float64()
		if math.IsInf(v, 0) {
			panic("rt.Decimal.toFloat: " + decimalShort(d) + " is outside the Float range")
		}
		return v
	})

	// toInt : Decimal -> Int — truncates fractional part. A whole part past
	// the Int range (64 bits) fails as the classified NumericOverflow; it
	// used to wrap round (1e30 gave 5076944270305263616).
	RegisterPure("Decimal_toInt", func(args []any) any {
		if len(args) < 1 {
			return 0
		}
		return decimalIntOrPanic("Decimal.toInt", decimalUnbox(args[0]))
	})

	// toMinor : Int -> Decimal -> Int — `toMinor 2 (3.14)` = 314.
	// Inverse of fromMinor; the integer-cents representation.
	RegisterPure("Decimal_toMinor", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		places := decimalPlaces("Decimal.toMinor", args[0])
		shifted := decimalUnbox(args[1]).Shift(places)
		return decimalIntOrPanic("Decimal.toMinor", shifted)
	})

	// ── Arithmetic ──────────────────────────────────────────────

	RegisterPure("Decimal_add", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Add(decimalUnbox(args[1])))
	})

	RegisterPure("Decimal_sub", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Sub(decimalUnbox(args[1])))
	})

	RegisterPure("Decimal_mul", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Mul(decimalUnbox(args[1])))
	})

	RegisterPure("Decimal_div", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Decimal.div: missing args"))
		}
		denom := decimalUnbox(args[1])
		if denom.IsZero() {
			return Err[any, any](ErrInvalidInput("Decimal.div: division by zero"))
		}
		return Ok[any, any](decimalBox(decimalUnbox(args[0]).Div(denom)))
	})

	// mod : Decimal -> Decimal -> Result Error Decimal
	RegisterPure("Decimal_mod", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Decimal.mod: missing args"))
		}
		denom := decimalUnbox(args[1])
		if denom.IsZero() {
			return Err[any, any](ErrInvalidInput("Decimal.mod: divisor is zero"))
		}
		return Ok[any, any](decimalBox(decimalUnbox(args[0]).Mod(denom)))
	})

	// neg : Decimal -> Decimal
	RegisterPure("Decimal_neg", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Neg())
	})

	// abs : Decimal -> Decimal
	RegisterPure("Decimal_abs", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Abs())
	})

	// ── Rounding ────────────────────────────────────────────────

	// round : Int -> Decimal -> Decimal (banker's rounding — half-to-even)
	RegisterPure("Decimal_round", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[1]).RoundBank(decimalPlaces("Decimal.round", args[0])))
	})

	// roundHalfUp : Int -> Decimal -> Decimal (4/5 round-up — schools rounding)
	RegisterPure("Decimal_roundHalfUp", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[1]).Round(decimalPlaces("Decimal.roundHalfUp", args[0])))
	})

	// truncate : Int -> Decimal -> Decimal — toward zero
	RegisterPure("Decimal_truncate", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[1]).Truncate(decimalPlaces("Decimal.truncate", args[0])))
	})

	// floor : Decimal -> Decimal — toward -∞
	RegisterPure("Decimal_floor", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Floor())
	})

	// ceil : Decimal -> Decimal — toward +∞
	RegisterPure("Decimal_ceil", func(args []any) any {
		if len(args) < 1 {
			return decimalBox(decimal.Zero)
		}
		return decimalBox(decimalUnbox(args[0]).Ceil())
	})

	// ── Comparison ──────────────────────────────────────────────

	// compare : Decimal -> Decimal -> Int (-1 / 0 / 1)
	RegisterPure("Decimal_compare", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		return decimalUnbox(args[0]).Cmp(decimalUnbox(args[1]))
	})

	// eq / neq / lt / lte / gt / gte
	RegisterPure("Decimal_eq", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return decimalUnbox(args[0]).Equal(decimalUnbox(args[1]))
	})
	RegisterPure("Decimal_neq", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return !decimalUnbox(args[0]).Equal(decimalUnbox(args[1]))
	})
	RegisterPure("Decimal_lt", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return decimalUnbox(args[0]).LessThan(decimalUnbox(args[1]))
	})
	RegisterPure("Decimal_lte", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return decimalUnbox(args[0]).LessThanOrEqual(decimalUnbox(args[1]))
	})
	RegisterPure("Decimal_gt", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return decimalUnbox(args[0]).GreaterThan(decimalUnbox(args[1]))
	})
	RegisterPure("Decimal_gte", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		return decimalUnbox(args[0]).GreaterThanOrEqual(decimalUnbox(args[1]))
	})

	// min / max
	RegisterPure("Decimal_min", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		a := decimalUnbox(args[0])
		b := decimalUnbox(args[1])
		if a.LessThan(b) {
			return decimalBox(a)
		}
		return decimalBox(b)
	})
	RegisterPure("Decimal_max", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		a := decimalUnbox(args[0])
		b := decimalUnbox(args[1])
		if a.GreaterThan(b) {
			return decimalBox(a)
		}
		return decimalBox(b)
	})

	// ── Predicates ──────────────────────────────────────────────

	RegisterPure("Decimal_isZero", func(args []any) any {
		if len(args) < 1 {
			return true
		}
		return decimalUnbox(args[0]).IsZero()
	})
	RegisterPure("Decimal_isPositive", func(args []any) any {
		if len(args) < 1 {
			return false
		}
		return decimalUnbox(args[0]).IsPositive()
	})
	RegisterPure("Decimal_isNegative", func(args []any) any {
		if len(args) < 1 {
			return false
		}
		return decimalUnbox(args[0]).IsNegative()
	})

	// ── Percent helpers ─────────────────────────────────────────

	// percentOf : Decimal -> Decimal -> Decimal
	// `percentOf 20 100` = 20  (20% of 100). Pure arithmetic, no
	// rounding — combine with `round 2` for currency output.
	RegisterPure("Decimal_percentOf", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		hundred := decimal.NewFromInt(100)
		return decimalBox(decimalUnbox(args[1]).Mul(decimalUnbox(args[0])).Div(hundred))
	})

	// addPercent : Decimal -> Decimal -> Decimal
	// `addPercent 10 100` = 110  (add 10% to 100). Useful for tax /
	// markup. Symmetric subPercent for discount.
	RegisterPure("Decimal_addPercent", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		base := decimalUnbox(args[1])
		hundred := decimal.NewFromInt(100)
		return decimalBox(base.Add(base.Mul(decimalUnbox(args[0])).Div(hundred)))
	})

	RegisterPure("Decimal_subPercent", func(args []any) any {
		if len(args) < 2 {
			return decimalBox(decimal.Zero)
		}
		base := decimalUnbox(args[1])
		hundred := decimal.NewFromInt(100)
		return decimalBox(base.Sub(base.Mul(decimalUnbox(args[0])).Div(hundred)))
	})

	// ── Formatting ──────────────────────────────────────────────

	// formatWith : String -> String -> Int -> Decimal -> String
	// formatWith thousandsSep decimalSep places d
	// e.g. formatWith "," "." 2 (1234567.891) = "1,234,567.89"
	// formatWith "." "," 2 (1234567.891) = "1.234.567,89"  (European)
	RegisterPure("Decimal_formatWith", func(args []any) any {
		if len(args) < 4 {
			return ""
		}
		thousandsSep := fmt.Sprintf("%v", args[0])
		decimalSep := fmt.Sprintf("%v", args[1])
		places := decimalPlaces("Decimal.formatWith", args[2])
		d := decimalUnbox(args[3])
		fixed := d.StringFixed(places)
		// Split into integer + fractional parts on the literal "."
		// that StringFixed always emits.
		neg := strings.HasPrefix(fixed, "-")
		if neg {
			fixed = fixed[1:]
		}
		intPart := fixed
		fracPart := ""
		if dot := strings.Index(fixed, "."); dot >= 0 {
			intPart = fixed[:dot]
			fracPart = fixed[dot+1:]
		}
		// Group integer part with thousandsSep.
		var grouped strings.Builder
		n := len(intPart)
		for i, c := range intPart {
			if i > 0 && (n-i)%3 == 0 {
				grouped.WriteString(thousandsSep)
			}
			grouped.WriteRune(c)
		}
		out := grouped.String()
		if places > 0 || fracPart != "" {
			out += decimalSep + fracPart
		}
		if neg {
			out = "-" + out
		}
		return out
	})
}

// decimalIntOrPanic is the whole part of d as an Int. A whole part outside
// the 64-bit Int range has no Int: it fails as the classified
// NumericOverflow, naming the operation and the value, never a wrapped
// number (v0.29.2 sixth Judge: `Money.toMinor` of 10 ETH gave
// -8446744073709551616).
func decimalIntOrPanic(op string, d decimal.Decimal) int {
	whole := d.Truncate(0).BigInt()
	if !whole.IsInt64() {
		panic("rt." + op + ": " + decimalShort(d) + " is outside the Int range (-9223372036854775808 to 9223372036854775807)")
	}
	return int(whole.Int64())
}

// decimalShort is d's text for a message, cut to 60 characters.
func decimalShort(d decimal.Decimal) string {
	s := d.String()
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
