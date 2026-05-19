package rt

import (
	"strings"
	"testing"
)

// Phase 2.6 — path-aware JSON decode errors. AI debugging an API
// integration should get "at .user.email[3]: expected String, got
// Number" instead of "expected String, got Number" with no
// indication of WHICH field.

// Helper — run decoder against JSON string + return error message.
// Fails the test when result is Ok (caller expects an error).
func decodeExpectingErr(t *testing.T, decoder any, input string) string {
	t.Helper()
	result := JsonDec_decodeString(decoder, input)
	if !isErrResult(result) {
		t.Fatalf("expected Err result, got Ok: %+v", result)
	}
	return extractErrMsg(result)
}

// ─── Leaf decoders show type-aware "expected X, got Y" ──────

func TestJsonDec_String_GotsMessage(t *testing.T) {
	msg := decodeExpectingErr(t, JsonDec_string(), "42")
	if msg != "expected String, got Number" {
		t.Errorf("got %q", msg)
	}
}

func TestJsonDec_Int_GotsMessage(t *testing.T) {
	msg := decodeExpectingErr(t, JsonDec_int(), `"text"`)
	if msg != "expected Int, got String" {
		t.Errorf("got %q", msg)
	}
}

func TestJsonDec_Bool_GotsMessage(t *testing.T) {
	msg := decodeExpectingErr(t, JsonDec_bool(), `null`)
	if msg != "expected Bool, got Null" {
		t.Errorf("got %q", msg)
	}
}

func TestJsonDec_Float_GotsArray(t *testing.T) {
	msg := decodeExpectingErr(t, JsonDec_float(), `[1,2,3]`)
	if msg != "expected Float, got Array" {
		t.Errorf("got %q", msg)
	}
}

// ─── field decorator prepends path ──────────────────────────

func TestJsonDec_Field_PrependsPath(t *testing.T) {
	d := JsonDec_field("name", JsonDec_string())
	msg := decodeExpectingErr(t, d, `{"name": 42}`)
	want := "at .name: expected String, got Number"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestJsonDec_Field_MissingFieldClear(t *testing.T) {
	d := JsonDec_field("email", JsonDec_string())
	msg := decodeExpectingErr(t, d, `{"name": "alice"}`)
	if !strings.Contains(msg, "missing field") || !strings.Contains(msg, ".email") {
		t.Errorf("expected missing-field error mentioning .email, got %q", msg)
	}
}

func TestJsonDec_Field_NotObject(t *testing.T) {
	d := JsonDec_field("name", JsonDec_string())
	msg := decodeExpectingErr(t, d, `"raw string"`)
	if !strings.Contains(msg, "expected Object") {
		t.Errorf("expected 'expected Object' error, got %q", msg)
	}
	if !strings.Contains(msg, ".name") {
		t.Errorf("error should mention field name being looked up: %q", msg)
	}
}

// ─── Nested field paths chain correctly ─────────────────────

func TestJsonDec_NestedFields_ChainPath(t *testing.T) {
	// {"user": {"email": 42}}
	// .user . email → field "user" → field "email" → string
	d := JsonDec_field("user", JsonDec_field("email", JsonDec_string()))
	msg := decodeExpectingErr(t, d, `{"user": {"email": 42}}`)
	want := "at .user.email: expected String, got Number"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestJsonDec_TripleNested(t *testing.T) {
	// {"a": {"b": {"c": null}}} expecting string at c
	d := JsonDec_field("a", JsonDec_field("b", JsonDec_field("c", JsonDec_string())))
	msg := decodeExpectingErr(t, d, `{"a": {"b": {"c": null}}}`)
	want := "at .a.b.c: expected String, got Null"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

// ─── Array index decorator ──────────────────────────────────

func TestJsonDec_Index_PrependsBracketIndex(t *testing.T) {
	d := JsonDec_index(2, JsonDec_string())
	msg := decodeExpectingErr(t, d, `["a", "b", 42, "d"]`)
	want := "at [2]: expected String, got Number"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestJsonDec_Index_OutOfRange(t *testing.T) {
	d := JsonDec_index(5, JsonDec_string())
	msg := decodeExpectingErr(t, d, `["a", "b"]`)
	if !strings.Contains(msg, "out of range") || !strings.Contains(msg, "[5]") {
		t.Errorf("expected out-of-range message with index 5, got %q", msg)
	}
}

func TestJsonDec_Index_NotArray(t *testing.T) {
	d := JsonDec_index(0, JsonDec_string())
	msg := decodeExpectingErr(t, d, `{"not": "array"}`)
	if !strings.Contains(msg, "expected Array") || !strings.Contains(msg, "[0]") {
		t.Errorf("got %q", msg)
	}
}

// ─── Mixed field + index paths ──────────────────────────────

func TestJsonDec_FieldThenIndex(t *testing.T) {
	// {"items": [{"name": 42}, ...]}
	// .items [0] .name → expected String
	d := JsonDec_field("items",
		JsonDec_index(0,
			JsonDec_field("name", JsonDec_string())))
	msg := decodeExpectingErr(t, d, `{"items": [{"name": 42}]}`)
	want := "at .items[0].name: expected String, got Number"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestJsonDec_DeeplyNestedRealistic(t *testing.T) {
	// Real-world API response: {"data": {"users": [..., ..., ..., {"email": 42}]}}
	// .data.users[3].email
	d := JsonDec_field("data",
		JsonDec_field("users",
			JsonDec_index(3,
				JsonDec_field("email", JsonDec_string()))))
	input := `{"data": {"users": [{"email":"a@x"}, {"email":"b@x"}, {"email":"c@x"}, {"email": 42}]}}`
	msg := decodeExpectingErr(t, d, input)
	want := "at .data.users[3].email: expected String, got Number"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

// ─── .at chain decorator ────────────────────────────────────

func TestJsonDec_At_ChainsSegments(t *testing.T) {
	// .at ["user", "address", "city"] expecting String, got null
	d := JsonDec_at([]any{"user", "address", "city"}, JsonDec_string())
	msg := decodeExpectingErr(t, d,
		`{"user": {"address": {"city": null}}}`)
	want := "at .user.address.city: expected String, got Null"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestJsonDec_At_MissingSegment(t *testing.T) {
	d := JsonDec_at([]any{"a", "b", "c"}, JsonDec_string())
	msg := decodeExpectingErr(t, d, `{"a": {"missing": "data"}}`)
	if !strings.Contains(msg, "at .a.b") || !strings.Contains(msg, "missing field") {
		t.Errorf("expected 'at .a.b: missing field', got %q", msg)
	}
}

// ─── list combinator reports which element failed ───────────

func TestJsonDec_List_PointsAtFirstFailingIndex(t *testing.T) {
	d := JsonDec_list(JsonDec_string())
	msg := decodeExpectingErr(t, d, `["a", "b", 42, "d"]`)
	// First failing element is at [2].
	if !strings.Contains(msg, "[2]") || !strings.Contains(msg, "expected String, got Number") {
		t.Errorf("expected '[2]: expected String, got Number', got %q", msg)
	}
}

// ─── Ok path still passes — no regression ───────────────────

func TestJsonDec_Field_OkRoundTrip(t *testing.T) {
	d := JsonDec_field("name", JsonDec_string())
	result := JsonDec_decodeString(d, `{"name": "alice"}`)
	if isErrResult(result) {
		t.Errorf("decode should succeed: %+v", result)
	}
}

func TestJsonDec_TripleNested_OkRoundTrip(t *testing.T) {
	d := JsonDec_field("a", JsonDec_field("b", JsonDec_field("c", JsonDec_string())))
	result := JsonDec_decodeString(d, `{"a": {"b": {"c": "deep"}}}`)
	if isErrResult(result) {
		t.Errorf("decode should succeed: %+v", result)
	}
}

// ─── Type-aware messages cover every JSON type ──────────────

func TestJsonValueKind_AllTypes(t *testing.T) {
	cases := map[string]any{
		"Null":    nil,
		"Boolean": true,
		"Number":  float64(42),
		"String":  "x",
		"Array":   []any{1, 2},
		"Object":  map[string]any{"k": 1},
	}
	for want, val := range cases {
		got := jsonValueKind(val)
		if got != want {
			t.Errorf("jsonValueKind(%v): got %q, want %q", val, got, want)
		}
	}
}
