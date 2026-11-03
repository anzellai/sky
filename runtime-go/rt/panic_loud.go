package rt

import (
	"fmt"
	"reflect"
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

// skyErrorMessage is the message of a Sky `Error` value (its ErrorInfo's
// `message`), for a log line; any other value is printed as it is.
func skyErrorMessage(v any) string {
	ev := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if ev.IsValid() && ev.Kind() == reflect.Struct {
		if fields := ev.FieldByName("Fields"); fields.IsValid() && fields.Kind() == reflect.Slice && fields.Len() == 2 {
			if info := reflectElem(fields.Index(1)); info.IsValid() && info.Kind() == reflect.Struct {
				if m := info.FieldByName("Message"); m.IsValid() && m.Kind() == reflect.String {
					return m.String()
				}
			}
		}
	}
	return fmt.Sprintf("%v", v)
}

// failOnValuelessContainer panics when v is an `Err` or a `Nothing`: a Sky
// Result or Maybe that holds no value. Every value conversion (AsString,
// AsInt, AsList, AsTuple2, …) calls it once its own shapes and the `Ok` /
// `Just` unwrap have not answered, so such a container never becomes a value.
// Before v0.29.2 AsString printed it (`{1 <nil> {…}}`, so a `callPure` String
// kernel that panicked made the app show text) and the list, dict, rune and
// tuple conversions answered an empty value: a silent wrong answer.
//
// The panic is CoerceFailure ("rt.Coerce: expected"), the class of a value of
// the wrong shape at a typed slot. An `Err` whose message carries a
// loud-by-design marker (loudPanicClass) keeps that class, because
// classifyPanic asks loudPanicClass first and the message is in the panic.
func failOnValuelessContainer(want string, v any) {
	if !isSkyContainer(v) || reflect.ValueOf(v).FieldByName("Tag").Int() == 0 {
		return
	}
	if ev := reflect.ValueOf(v).FieldByName("ErrValue"); ev.IsValid() {
		panic(fmt.Sprintf("rt.Coerce: expected %s, got an Err, which has no value: %s",
			want, skyErrorMessage(ev.Interface())))
	}
	panic(fmt.Sprintf("rt.Coerce: expected %s, got Nothing, which has no value", want))
}
