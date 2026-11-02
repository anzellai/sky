package rt

// Sky `==` on a Decimal and on a Json.Value compares the VALUE, not the Go
// representation (v0.29.2 sixth Judge). `Decimal "1.10" == Decimal "1.1"`
// was False (shopspring keeps the exponent), so a Decimal read back from a
// decimal column, a codec or `Decimal.mul 1.5 2 == 3` was never `==` to the
// value written. A Json.Value compared its Go form (`int` against `float64`,
// a map against an ordered object), so `fromJson Codec.value (toJson v) ==
// Ok v` was False. `/=`, `List.member` and every other caller of deepEq
// follow.
//
// Neither type is `comparable` in Sky (no `<`, no Dict or Set key), so no
// ordering has to agree with this equality.

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"

	"github.com/shopspring/decimal"
)

// eqValueType compares a and b when either is a Decimal or a Json.Value;
// handled is false for every other type.
func eqValueType(a, b any) (eq, handled bool) {
	switch x := a.(type) {
	case decimal.Decimal:
		if y, ok := b.(decimal.Decimal); ok {
			return decimalSameValue(x, y), true
		}
	case JsonValue:
		if y, ok := b.(JsonValue); ok {
			return jsonRawEqual(x.raw, y.raw), true
		}
	}
	return false, false
}

// decimalSameValue reports whether two decimals have one value. The
// coefficients are reduced (trailing zeros moved into the exponent) and
// compared with their exponents, so no rescale to a common exponent is made:
// 1e900 and 1e-900 compare without a 1800-digit number.
func decimalSameValue(a, b decimal.Decimal) bool {
	ca, ea := decimalReduced(a)
	cb, eb := decimalReduced(b)
	if ca.Sign() == 0 || cb.Sign() == 0 {
		return ca.Sign() == cb.Sign()
	}
	return ea == eb && ca.Cmp(cb) == 0
}

var bigTen = big.NewInt(10)

func decimalReduced(d decimal.Decimal) (*big.Int, int64) {
	c := new(big.Int).Set(d.Coefficient())
	e := int64(d.Exponent())
	if c.Sign() == 0 {
		return c, 0
	}
	q, r := new(big.Int), new(big.Int)
	for {
		q.QuoRem(c, bigTen, r)
		if r.Sign() != 0 {
			return c, e
		}
		c.Set(q)
		e++
	}
}

// jsonRawEqual is JSON equality: numbers by value (`5`, `5.0` and the int
// 5 are one number), objects by their keys and values in any order, arrays
// element by element.
func jsonRawEqual(a, b any) bool {
	if ja, ok := a.(JsonValue); ok {
		a = ja.raw
	}
	if jb, ok := b.(JsonValue); ok {
		b = jb.raw
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if na, ok := jsonNumberValue(a); ok {
		nb, ok := jsonNumberValue(b)
		return ok && decimalSameValue(na, nb)
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonRawEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	if ma, ok := jsonObjectMap(a); ok {
		mb, ok := jsonObjectMap(b)
		if !ok || len(ma) != len(mb) {
			return false
		}
		for k, va := range ma {
			vb, present := mb[k]
			if !present || !jsonRawEqual(va, vb) {
				return false
			}
		}
		return true
	}
	return deepEq(a, b)
}

// jsonNumberValue is a JSON number as a decimal. A NaN or an infinite float
// has none and is only equal to itself through the fallback.
func jsonNumberValue(v any) (decimal.Decimal, bool) {
	switch n := v.(type) {
	case int:
		return decimal.NewFromInt(int64(n)), true
	case int64:
		return decimal.NewFromInt(n), true
	case int32:
		return decimal.NewFromInt(int64(n)), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return decimal.Decimal{}, false
		}
		d, err := decimal.NewFromString(strconv.FormatFloat(n, 'g', -1, 64))
		return d, err == nil
	case json.Number:
		d, err := decimal.NewFromString(string(n))
		return d, err == nil
	}
	return decimal.Decimal{}, false
}
