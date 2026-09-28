package rt

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func decodeValueOK(t *testing.T, dec any, v any) any {
	t.Helper()
	tag, okV, errV := anyResultView(JsonDec_decodeValue(dec, v))
	if tag != 0 {
		t.Fatalf("decodeValue: got Err %v", errV)
	}
	return okV
}

func decodeValueErr(t *testing.T, dec any, v any) string {
	t.Helper()
	r := JsonDec_decodeValue(dec, v)
	tag, _, _ := anyResultView(r)
	if tag == 0 {
		t.Fatalf("decodeValue: got Ok, want Err")
	}
	return extractErrMsg(r)
}

func mustRaw(t *testing.T, text string) any {
	t.Helper()
	tag, okV, errV := anyResultView(JsonEnc_raw(text))
	if tag != 0 {
		t.Fatalf("Encode.raw %q: %v", text, errV)
	}
	return okV
}

// decodeValue reads a Json.Encode tree: an Encode.object is an object, an
// Encode.int is a number `int` accepts, an Encode.list a list.
func TestJsonDecodeValue_ReadsEncodeTrees(t *testing.T) {
	v := JsonEnc_object([]any{
		SkyTuple2{V0: "n", V1: JsonEnc_int(42)},
		SkyTuple2{V0: "big", V1: JsonEnc_int(int(9007199254740993))},
		SkyTuple2{V0: "f", V1: JsonEnc_float(2.5)},
		SkyTuple2{V0: "s", V1: JsonEnc_string("hi")},
		SkyTuple2{V0: "xs", V1: JsonEnc_list([]any{JsonEnc_int(1), JsonEnc_int(2)})},
		SkyTuple2{V0: "b", V1: JsonEnc_bool(true)},
	})
	if got := AsInt(decodeValueOK(t, JsonDec_field("n", JsonDec_int()), v)); got != 42 {
		t.Fatalf("field n = %d, want 42", got)
	}
	if got := AsInt(decodeValueOK(t, JsonDec_field("big", JsonDec_int()), v)); got != 9007199254740993 {
		t.Fatalf("field big = %d, want 9007199254740993 (exact)", got)
	}
	if got := AsFloat(decodeValueOK(t, JsonDec_field("f", JsonDec_float()), v)); got != 2.5 {
		t.Fatalf("field f = %v", got)
	}
	if got := AsString(decodeValueOK(t, JsonDec_field("s", JsonDec_string()), v)); got != "hi" {
		t.Fatalf("field s = %q", got)
	}
	xs := asList(decodeValueOK(t, JsonDec_field("xs", JsonDec_list(JsonDec_int())), v))
	if len(xs) != 2 || AsInt(xs[1]) != 2 {
		t.Fatalf("field xs = %v", xs)
	}
	if got := decodeValueOK(t, JsonDec_at([]any{"b"}, JsonDec_bool()), v); got != true {
		t.Fatalf("at b = %v", got)
	}
	// An Encode.float is not an Int: the same answer decodeString gives.
	msg := decodeValueErr(t, JsonDec_field("f", JsonDec_int()), v)
	if !strings.Contains(msg, ".f") {
		t.Fatalf("error must carry the path .f, got %q", msg)
	}
	// A missing field is an Err naming it.
	if msg := decodeValueErr(t, JsonDec_field("nope", JsonDec_int()), v); !strings.Contains(msg, "nope") {
		t.Fatalf("missing field error %q", msg)
	}
}

// decodeValue d v == decodeString d (encode 0 v) for values encode can write,
// including a Decode.value sub-document and an Encode.raw document.
func TestJsonDecodeValue_AgreesWithDecodeStringOfEncode(t *testing.T) {
	sub := decodeOK(t, JsonDec_field("meta", JsonDec_value()),
		`{"meta":{"id":12345678901234567890,"tags":["a","b"],"on":false,"x":null}}`)
	raw := mustRaw(t, `{"id":7,"tags":[],"on":true,"x":null}`)
	for _, v := range []any{sub, raw} {
		text := AsString(JsonEnc_encode(0, v))
		for _, dec := range []any{
			JsonDec_field("tags", JsonDec_list(JsonDec_string())),
			JsonDec_field("on", JsonDec_bool()),
			JsonDec_field("id", JsonDec_int()),
			JsonDec_field("id", JsonDec_float()),
			JsonDec_field("x", JsonDec_string()),
			JsonDec_value(),
		} {
			a := JsonDec_decodeValue(dec, v)
			b := JsonDec_decodeString(dec, text)
			ta, oa, _ := anyResultView(a)
			tb, ob, _ := anyResultView(b)
			if ta != tb {
				t.Fatalf("decodeValue / decodeString disagree on %s: %v vs %v", text, a, b)
			}
			if ta == 0 {
				ea, _ := json.Marshal(oa)
				eb, _ := json.Marshal(ob)
				if jv, ok := oa.(JsonValue); ok {
					ea = []byte(AsString(JsonEnc_encode(0, jv)))
					eb = []byte(AsString(JsonEnc_encode(0, ob)))
				}
				if string(ea) != string(eb) {
					t.Fatalf("decodeValue %s != decodeString %s for %s", ea, eb, text)
				}
			}
		}
	}
}

// A duplicate key in an Encode.object keeps its LAST value, as the parser
// does for the encoded text.
func TestJsonDecodeValue_DuplicateKeyLastWins(t *testing.T) {
	v := JsonEnc_object([]any{
		SkyTuple2{V0: "k", V1: JsonEnc_int(1)},
		SkyTuple2{V0: "k", V1: JsonEnc_int(2)},
	})
	if got := AsInt(decodeValueOK(t, JsonDec_field("k", JsonDec_int()), v)); got != 2 {
		t.Fatalf("duplicate key: got %d, want the last value 2", got)
	}
	text := AsString(JsonEnc_encode(0, v))
	if got := AsInt(decodeOK(t, JsonDec_field("k", JsonDec_int()), text)); got != 2 {
		t.Fatalf("decodeString of %s: got %d, want 2", text, got)
	}
}

// A NaN Float cannot be encoded, but decodeValue reads it as the Float it is
// rather than panicking.
func TestJsonDecodeValue_NaNFloatIsAFloat(t *testing.T) {
	got := AsFloat(decodeValueOK(t, JsonDec_float(), JsonEnc_float(math.NaN())))
	if !math.IsNaN(got) {
		t.Fatalf("got %v, want NaN", got)
	}
}

// decodeString reads ONE document: text after the value is an error. It used
// to decode the first value and ignore the rest (`"3 x"` was Ok 3).
func TestJsonDecodeString_RejectsTrailingText(t *testing.T) {
	for _, in := range []string{"3 x", "1 2", `{"a":1} {"a":2}`, `[1]]`, `"s" "t"`} {
		tag, okV, _ := anyResultView(JsonDec_decodeString(JsonDec_value(), in))
		if tag == 0 {
			t.Fatalf("decodeString %q: got Ok %v, want Err (text after the value)", in, okV)
		}
	}
	for _, in := range []string{"3", " 3 ", "3\n", "\t{\"a\":1}\r\n"} {
		decodeOK(t, JsonDec_value(), in)
	}
}

// The documented key-order contract of Decode.value -> Encode.encode: keys
// are written sorted byte-wise at every depth. Encode.raw keeps the source.
func TestJsonDecValue_KeyOrderIsSortedAndRawKeepsIt(t *testing.T) {
	in := `{"b":1,"a":{"d":1,"c":2},"B":[{"z":0,"y":1}]}`
	v := decodeOK(t, JsonDec_value(), in)
	if got, want := AsString(JsonEnc_encode(0, v)), `{"B":[{"y":1,"z":0}],"a":{"c":2,"d":1},"b":1}`; got != want {
		t.Fatalf("Decode.value -> encode: got %s, want the sorted form %s", got, want)
	}
	if got := AsString(JsonEnc_encode(0, mustRaw(t, in))); got != in {
		t.Fatalf("Encode.raw -> encode: got %s, want the source %s", got, in)
	}
}
