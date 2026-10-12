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
