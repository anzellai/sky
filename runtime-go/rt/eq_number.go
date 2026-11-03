package rt

import (
	"math"
	"reflect"
)

// numEqCrossKind compares two numbers of DIFFERENT Go kinds by value. It
// reports ok=false when either side is not a number, so the caller keeps its
// other cross-kind rules.
//
// A Sky `number` value travels as a Go `any`, and the Go kind it holds is not
// fixed by the Sky type: an integer literal in a `number` slot is a Go `int`
// even when the caller instantiated `number` at Float (`zero _ = 0`,
// `f xs = 1 :: xs`, the `[] -> 0` base case of a recursive sum). The two are
// one Sky value, so `==` (and `List.member`, and equality inside a Maybe, a
// tuple or a record) compares them by value.
//
// The comparison is exact: a float equals an integer only when it is a whole
// number that converts to that integer without rounding. NaN equals nothing.
func numEqCrossKind(a, b reflect.Value) (eq, ok bool) {
	ai, aInt := eqNumInt(a)
	bi, bInt := eqNumInt(b)
	af, aFloat := eqNumFloat(a)
	bf, bFloat := eqNumFloat(b)
	switch {
	case aInt && bInt:
		return ai == bi, true
	case aFloat && bFloat:
		return af == bf, true
	case aInt && bFloat:
		return floatEqInt(bf, ai), true
	case aFloat && bInt:
		return floatEqInt(af, bi), true
	}
	return false, false
}

// eqNumInt reads a signed or unsigned Go integer as an int64 (an unsigned value
// above MaxInt64 is not representable and is reported as not an integer, so
// it never compares equal to a float by accident).
func eqNumInt(v reflect.Value) (int64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > math.MaxInt64 {
			return 0, false
		}
		return int64(u), true
	}
	return 0, false
}

func eqNumFloat(v reflect.Value) (float64, bool) {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	}
	return 0, false
}

// floatEqInt: f is a whole number in int64 range whose conversion is exactly i.
func floatEqInt(f float64, i int64) bool {
	if f != math.Trunc(f) || f < -9.223372036854775808e18 || f >= 9.223372036854775808e18 {
		return false
	}
	return int64(f) == i
}
