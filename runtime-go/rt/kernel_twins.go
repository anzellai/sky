package rt

// kernel_twins.go — typed-result twins of erased kernels whose Go result has
// exactly one type. Each calls the erased kernel and asserts that one type, so
// its semantics are the erased kernel's by construction; the lowerer calls it
// for a fully applied call (lower.rs `kernel_typed_return_twin`, doc 14 R5,
// lever §5.3), and the call site then narrows nothing. A value reference or a
// partial application keeps the erased symbol.

func String_leftT(n, s any) string      { return String_left(n, s).(string) }
func String_rightT(n, s any) string     { return String_right(n, s).(string) }
func String_dropLeftT(n, s any) string  { return String_dropLeft(n, s).(string) }
func String_dropRightT(n, s any) string { return String_dropRight(n, s).(string) }

// JsonEnc_encodeT keeps JsonEnc_encode's classified panic for a value JSON
// cannot hold (JsonEncodeFailure, panic_recover.go).
func JsonEnc_encodeT(indent, v any) string { return JsonEnc_encode(indent, v).(string) }

// String_fromFloatAnyT keeps String_fromFloat's 'f' formatting (String_fromFloatT
// formats with 'g', so it is not the same function).
func String_fromFloatAnyT(f any) string { return String_fromFloat(f).(string) }

// List_rangeT is List_range over ints: `lo..hi` inclusive, empty when hi < lo.
func List_rangeT(lo, hi int) []int {
	if hi < lo {
		return []int{}
	}
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

// Math_minFloatT / Math_maxFloatT are Math_min / Math_max over two floats,
// through the erased kernel, so the order (skyLessThan, NaN included) is
// exactly the erased one.
func Math_minFloatT(a, b float64) float64 { return Math_min(a, b).(float64) }
func Math_maxFloatT(a, b float64) float64 { return Math_max(a, b).(float64) }
