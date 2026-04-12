package sky_sky_core_math

import "math"

func Sqrt(x any) any  { return math.Sqrt(asFloat(x)) }
func Pow(b, e any) any { return math.Pow(asFloat(b), asFloat(e)) }
func Log(x any) any   { return math.Log(asFloat(x)) }
func Exp(x any) any   { return math.Exp(asFloat(x)) }
func Sin(x any) any   { return math.Sin(asFloat(x)) }
func Cos(x any) any   { return math.Cos(asFloat(x)) }
func Tan(x any) any   { return math.Tan(asFloat(x)) }
func Asin(x any) any  { return math.Asin(asFloat(x)) }
func Acos(x any) any  { return math.Acos(asFloat(x)) }
func Atan(x any) any  { return math.Atan(asFloat(x)) }
func Atan2(y, x any) any { return math.Atan2(asFloat(y), asFloat(x)) }
func Floor(x any) any { return int(math.Floor(asFloat(x))) }
func Ceil(x any) any  { return int(math.Ceil(asFloat(x))) }
func Round(x any) any { return int(math.Round(asFloat(x))) }
func Abs(x any) any   { return math.Abs(asFloat(x)) }
func Pi(_ any) any    { return math.Pi }
func E(_ any) any     { return math.E }
func Inf(_ any) any   { return math.Inf(1) }
func IsNaN(x any) any { return math.IsNaN(asFloat(x)) }
func IsInf(x any) any { return math.IsInf(asFloat(x), 0) }
func MaxFloat(a, b any) any { fa, fb := asFloat(a), asFloat(b); if fa > fb { return fa }; return fb }
func MinFloat(a, b any) any { fa, fb := asFloat(a), asFloat(b); if fa < fb { return fa }; return fb }

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64: return x
	case int: return float64(x)
	default: return 0
	}
}
