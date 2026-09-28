package rt

import (
	"math"
	"strings"
	"testing"
)

func decodeOK(t *testing.T, dec any, input string) any {
	t.Helper()
	tag, okV, errV := anyResultView(JsonDec_decodeString(dec, input))
	if tag != 0 {
		t.Fatalf("decode %q: got Err %v", input, errV)
	}
	return okV
}

// Json.Decode.value returns the raw tree as a Value that Json.Encode accepts,
// and numbers keep their exact source text: an integer past 2^53 and a long
// decimal survive a decode -> encode round trip unrounded.
func TestJsonDecValue_RoundTripsExactNumbers(t *testing.T) {
	in := `{"big":9007199254740993,"dec":0.1000000000000000055511151231257827,"list":[1,true,null,"s"]}`
	v := decodeOK(t, JsonDec_value(), in)
	if _, ok := v.(JsonValue); !ok {
		t.Fatalf("Decode.value returned %T, want JsonValue (the Value type Encode uses)", v)
	}
	got := AsString(JsonEnc_encode(0, v))
	if got != in {
		t.Fatalf("round trip changed the document:\n in: %s\nout: %s", in, got)
	}
}

// A Value from Decode.value nests inside Encode.object / Encode.list like any
// other Value.
func TestJsonDecValue_NestsInEncoders(t *testing.T) {
	inner := decodeOK(t, JsonDec_field("x", JsonDec_value()), `{"x":{"k":[1,2.50]}}`)
	obj := JsonEnc_object([]any{SkyTuple2{V0: "wrapped", V1: inner}})
	if got := AsString(JsonEnc_encode(0, obj)); got != `{"wrapped":{"k":[1,2.50]}}` {
		t.Fatalf("got %s", got)
	}
	lst := JsonEnc_list([]any{inner, JsonEnc_int(3)})
	if got := AsString(JsonEnc_encode(0, lst)); got != `[{"k":[1,2.50]},3]` {
		t.Fatalf("got %s", got)
	}
}

// Json.Encode.raw validates its text and embeds it verbatim.
func TestJsonEncRaw_ValidEmbedsVerbatim(t *testing.T) {
	tag, okV, errV := anyResultView(JsonEnc_raw(`{"b":1,"a":[12345678901234567890]}`))
	if tag != 0 {
		t.Fatalf("raw of valid JSON returned Err %v", errV)
	}
	obj := JsonEnc_object([]any{SkyTuple2{V0: "doc", V1: okV}})
	// Key order and the out-of-int64 number are exactly as given.
	if got := AsString(JsonEnc_encode(0, obj)); got != `{"doc":{"b":1,"a":[12345678901234567890]}}` {
		t.Fatalf("got %s", got)
	}
	// Indented encode re-indents the embedded text; the content is unchanged.
	pretty := AsString(JsonEnc_encode(2, okV))
	if !strings.Contains(pretty, "\n  \"b\": 1,") {
		t.Fatalf("indent did not apply to the raw value:\n%s", pretty)
	}
}

// Invalid text is an Err InvalidInput, never a Value.
func TestJsonEncRaw_InvalidIsErr(t *testing.T) {
	for _, bad := range []string{``, `{`, `{"a":}`, `nul`, `[1,]`, `{"a":1} x`} {
		tag, _, errV := anyResultView(JsonEnc_raw(bad))
		if tag != 1 {
			t.Fatalf("raw %q: want Err, got Ok", bad)
		}
		if msg := extractErrMsg(Err[any, any](errV)); !strings.Contains(msg, "not valid JSON") {
			t.Fatalf("raw %q: error message %q does not say why", bad, msg)
		}
	}
}

// Json.Encode.encode used to return "" when the value could not be marshalled
// (a silent wrong answer). The only Value the builders can make that JSON
// cannot represent is a NaN or infinite Float. `encode` has no error channel,
// so this is now a classified panic.
func TestJsonEncEncode_NonFiniteFloatPanicsClassified(t *testing.T) {
	cases := map[string]any{
		"NaN top level":      JsonEnc_float(math.NaN()),
		"+Inf in an object":  JsonEnc_object([]any{SkyTuple2{V0: "x", V1: JsonEnc_float(math.Inf(1))}}),
		"-Inf in a list":     JsonEnc_list([]any{JsonEnc_float(math.Inf(-1))}),
		"NaN, indented form": JsonEnc_float(math.NaN()),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			indent := 0
			if strings.Contains(name, "indented") {
				indent = 2
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("encode of a non-finite Float returned instead of failing loudly")
				}
				msg, _ := r.(string)
				if kind, _ := classifyPanic(msg); kind != "JsonEncodeFailure" {
					t.Fatalf("panic %q classified as %q, want JsonEncodeFailure", msg, kind)
				}
			}()
			out := JsonEnc_encode(indent, v)
			t.Fatalf("encode returned %q", out)
		})
	}
}

// Finite floats are unaffected.
func TestJsonEncEncode_FiniteFloatStillEncodes(t *testing.T) {
	if got := AsString(JsonEnc_encode(0, JsonEnc_float(1.5))); got != "1.5" {
		t.Fatalf("got %q", got)
	}
}

// Result.toMaybe: Ok -> Just, Err -> Nothing, whatever the Result's type args.
func TestResultToMaybe(t *testing.T) {
	if m := Result_toMaybe(Ok[any, any](7)).(SkyMaybe[any]); m.Tag != 0 || m.JustValue != 7 {
		t.Fatalf("Ok 7 -> %+v, want Just 7", m)
	}
	if m := Result_toMaybe(Err[any, any]("e")).(SkyMaybe[any]); m.Tag != 1 {
		t.Fatalf("Err -> %+v, want Nothing", m)
	}
	// A typed Result from typed codegen reads the same way.
	if m := Result_toMaybe(Ok[string, int](3)).(SkyMaybe[any]); m.Tag != 0 || m.JustValue != 3 {
		t.Fatalf("typed Ok 3 -> %+v, want Just 3", m)
	}
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Fatalf("non-Result input: panic %v classified %q, want CoerceFailure", r, kind)
		}
	}()
	Result_toMaybe(42)
}
