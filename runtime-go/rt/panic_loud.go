package rt

import (
	"fmt"
	"strings"
)

// loudPanicClass is the class and hint of a panic the runtime raises on
// purpose as one of the classified LOUD failures of well-typed code
// (AGENTS.md, "the one honest carve-out"): DivisionByZero, NumericOverflow
// and JsonEncodeFailure. It returns "" for any other panic. classifyPanic
// asks it first, so the markers live in one place, and it is built for every
// target (an Ffi boundary in a wasm client re-raises these too).
func loudPanicClass(msg string) (kind, hint string) {
	switch {
	case strings.Contains(msg, "rt.IntDiv: integer division by zero"),
		strings.Contains(msg, "rt.Rem: modulo by zero"),
		strings.Contains(msg, "rt.Div: division by zero"),
		strings.Contains(msg, "rt.Decimal.fromFloat: "):
		return "DivisionByZero",
			"Integer or float division by zero, or a Float with no value (NaN or Infinity) given to Decimal.fromFloat. Guard the divisor with `if d == 0 then Err … else Ok (n // d)` before the operation."
	case strings.Contains(msg, "rt.Decimal.toInt: "),
		strings.Contains(msg, "rt.Decimal.toMinor: "),
		strings.Contains(msg, "rt.Decimal.toFloat: "):
		return "NumericOverflow",
			"A Decimal too large for an Int (64 bits) or a Float was given to Decimal.toInt, Decimal.toMinor, Money.toMinor or Decimal.toFloat. There is no Int or Float with that value. Compare the Decimal with the bound first, or keep it as a Decimal (store it with Codec.decimal or Codec.moneyIn)."
	case strings.Contains(msg, "Codec.auto: cannot encode"):
		return "JsonEncodeFailure",
			"Codec.auto was given a value with no JSON form: a function, a Secret, a key, a crypto state or a runtime handle (Process, WebSocket, Cache, ...). Keep it out of records you save or send, or write the field's codec by hand."
	case strings.Contains(msg, "rt.JsonEnc_encode: cannot encode"):
		return "JsonEncodeFailure",
			"Json.Encode.encode was given a value JSON cannot represent: a NaN or infinite Float (`Math.nan`, `Math.inf`, `Math.sqrt` of a negative number, a Float overflow). JSON has no such number. Check the Float (`Math.isNaN`) before you encode it."
	}
	return "", ""
}

// isLoudByDesignPanic is true for a loud-by-design panic (loudPanicClass).
// Such a panic must reach the panic net (a 500 in a server, exit 1 in a CLI)
// and is never turned into an `Err` by an Ffi call boundary (runWithRecover)
// or a transaction body (Db.withTransaction rolls back, then re-raises it).
func isLoudByDesignPanic(r any) bool {
	kind, _ := loudPanicClass(fmt.Sprintf("%v", r))
	return kind != ""
}
