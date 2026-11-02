package rt

import (
	"fmt"
	"strings"
	"testing"
)

// v0.29.2 seventh Judge: `Decimal.toInt (Decimal "1e30")` reached the kernel
// through `Ffi.callPure`, whose recover turned the NumericOverflow panic into
// `Err`, and `AsInt(Err …)` then called itself until the Go stack ran out — an
// unrecoverable `fatal error: stack overflow` that ended the whole process.
//
// These tests drive the exact chain the emitted code runs: Ffi_callPure, then
// the As* narrowing the bare-typed Sky signature implies.

func recoverMsg(run func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = fmt.Sprintf("%v", r), true
		}
	}()
	run()
	return "", false
}

func decimalArg(t *testing.T, s string) any {
	t.Helper()
	r := Ffi_callPure("Decimal_fromString", []any{s})
	sr, ok := r.(SkyResult[any, any])
	inner := sr.OkValue
	if !ok || sr.Tag != 0 {
		t.Fatalf("Decimal_fromString %q: %v", s, r)
	}
	if m, isMaybe := inner.(SkyMaybe[any]); isMaybe {
		if m.Tag != 0 {
			t.Fatalf("Decimal_fromString %q gave Nothing", s)
		}
		return m.JustValue
	}
	return inner
}

func TestCallPureNumericOverflowStaysClassified(t *testing.T) {
	cases := []struct {
		name   string
		kernel string
		args   func() []any
		narrow func(any)
	}{
		{"Decimal.toInt 1e30", "Decimal_toInt", func() []any { return []any{decimalArg(t, "1e30")} }, func(v any) { _ = AsInt(v) }},
		{"Decimal.toInt -1e30", "Decimal_toInt", func() []any { return []any{decimalArg(t, "-1e30")} }, func(v any) { _ = AsInt(v) }},
		{"Decimal.toMinor 18 10", "Decimal_toMinor", func() []any { return []any{18, decimalArg(t, "10")} }, func(v any) { _ = AsInt(v) }},
		{"Decimal.toFloat 1e400", "Decimal_toFloat", func() []any { return []any{decimalArg(t, "1e400")} }, func(v any) { _ = AsFloat(v) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, panicked := recoverMsg(func() {
				c.narrow(Ffi_callPure(c.kernel, c.args()))
			})
			if !panicked {
				t.Fatalf("%s: no panic; want the classified NumericOverflow", c.name)
			}
			if kind, _ := classifyPanic(msg); kind != "NumericOverflow" {
				t.Fatalf("%s: panic %q classified %s, want NumericOverflow", c.name, msg, kind)
			}
			if strings.Contains(msg, "Ffi ") {
				t.Errorf("%s: the panic went through an Err (%q); a loud kernel panic must keep its own message", c.name, msg)
			}
		})
	}
}

// The other loud classes cross an Ffi boundary unchanged too.
func TestRunWithRecoverReRaisesLoudClasses(t *testing.T) {
	for _, loud := range []string{
		"rt.Div: division by zero",
		"rt.Decimal.fromFloat: NaN has no decimal value",
		"rt.Decimal.toInt: 1e30 is outside the Int range",
		"Codec.auto: cannot encode this value — a function",
		"rt.JsonEnc_encode: cannot encode the value as JSON: NaN",
	} {
		msg, panicked := recoverMsg(func() {
			runWithRecover("probe", nil, func([]any) any { panic(loud) })
		})
		if !panicked || msg != loud {
			t.Errorf("loud panic %q: re-raised=%v msg=%q; want it re-raised unchanged", loud, panicked, msg)
		}
	}
	// An unclassified kernel panic is still an Err (the documented FFI contract).
	r := runWithRecover("probe", nil, func([]any) any { panic("something else") })
	if sr, ok := r.(SkyResult[any, any]); !ok || sr.Tag != 1 {
		t.Errorf("unclassified panic: got %v, want Err", r)
	}
}

// No As* helper may loop on a Sky container that holds no value. Each of
// these used to recurse on `unwrapAny(Err …)` / `unwrapAny(Nothing)`, which
// hands the value back unchanged. Strict helpers panic (classified); lenient
// ones return their own default. Every case must return, which is the test.
func TestAsHelpersNeverRecurseOnErrOrNothing(t *testing.T) {
	empties := map[string]any{
		"Err":     Err[any, any](ErrFfi("x")),
		"Nothing": Nothing[any](),
	}
	for label, v := range empties {
		for name, run := range map[string]func(){
			"AsInt":     func() { _ = AsInt(v) },
			"AsFloat":   func() { _ = AsFloat(v) },
			"AsBool":    func() { _ = AsBool(v) },
			"AsRune":    func() { _ = AsRune(v) },
			"AsList":    func() { _ = AsList(v) },
			"AsListAny": func() { _ = AsListAny(v) },
			"AsListT":   func() { _ = AsListT[int](v) },
			"AsMapT":    func() { _ = AsMapT[int](v) },
			"asSkyDict": func() { _ = asSkyDict(v) },
			"isNumeric": func() { _ = isNumeric(v) },
			"AsString":  func() { _ = AsString(v) },
		} {
			msg, panicked := recoverMsg(run)
			if panicked {
				if kind, _ := classifyPanic(msg); kind == "Unexpected" {
					t.Errorf("%s(%s): unclassified panic %q", name, label, msg)
				}
			}
		}
	}
	// An Ok / a Just still unwraps.
	if AsInt(Ok[any, any](7)) != 7 || AsInt(Just[any](8)) != 8 {
		t.Error("AsInt no longer unwraps Ok / Just")
	}
}
