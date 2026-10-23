package rt

import (
	"strings"
	"testing"
)

// Regression: `Pipeline.optional name dec default` dropped the inner
// decoder's Err when the field was PRESENT and non-null, and gave the
// default instead. A wrongly typed value (a string where an Int is
// expected) then decoded as `Ok default`, which hides a schema change in
// the upstream payload. The NoRedInk elm-json-decode-pipeline rule is:
// absent or null gives the default, a present value the inner decoder
// rejects is an Err. These cases pin that rule, and the Elm semantics of
// the three decoders added beside it (`null`, `nullable`, `maybe`).

func optionalTestDecoder(inner any, def any) any {
	return JsonDecP_optional("n", inner, def, JsonDec_succeed(func(v any) any { return v }))
}

func decodeForTest(t *testing.T, dec any, input string) SkyResult[any, any] {
	t.Helper()
	r, ok := JsonDec_decodeString(dec, input).(SkyResult[any, any])
	if !ok {
		t.Fatalf("decodeString(%q) did not return a SkyResult", input)
	}
	return r
}

func TestPipelineOptionalPresentWrongTypeIsErr(t *testing.T) {
	r := decodeForTest(t, optionalTestDecoder(JsonDec_int(), 0), `{"n": "7"}`)
	if r.Tag == 0 {
		t.Fatalf("present, wrongly typed field must be Err, got Ok %v", r.OkValue)
	}
	if msg := extractErrMsg(r); msg == "" || !containsAll(msg, ".n", "Int") {
		t.Fatalf("error must name the field and the expected type, got %q", msg)
	}
}

func TestPipelineOptionalAbsentAndNullGiveDefault(t *testing.T) {
	for _, in := range []string{`{}`, `{"n": null}`, `{"m": 1}`} {
		r := decodeForTest(t, optionalTestDecoder(JsonDec_int(), 42), in)
		if r.Tag != 0 || AsInt(r.OkValue) != 42 {
			t.Fatalf("%s: absent or null field must give the default 42, got %+v", in, r)
		}
	}
}

func TestPipelineOptionalPresentValidValue(t *testing.T) {
	r := decodeForTest(t, optionalTestDecoder(JsonDec_int(), 0), `{"n": 7}`)
	if r.Tag != 0 || AsInt(r.OkValue) != 7 {
		t.Fatalf("present valid field must decode, got %+v", r)
	}
}

func TestPipelineOptionalNullReachesANullableInner(t *testing.T) {
	// The inner decoder runs first: a decoder that accepts null decides
	// what null means (elm-json-decode-pipeline `nullOr`).
	def := Just[any](99)
	r := decodeForTest(t, optionalTestDecoder(JsonDec_nullable(JsonDec_int()), def), `{"n": null}`)
	if r.Tag != 0 {
		t.Fatalf("null with a nullable inner must be Ok, got %+v", r)
	}
	if m, ok := r.OkValue.(SkyMaybe[any]); !ok || m.Tag != 1 {
		t.Fatalf("null with a nullable inner must be Nothing, got %#v", r.OkValue)
	}
}

func TestPipelineOptionalNonObjectGivesDefault(t *testing.T) {
	// Elm: `field` fails on a non-object, and `optional` falls back.
	r := decodeForTest(t, optionalTestDecoder(JsonDec_int(), 5), `[1, 2]`)
	if r.Tag != 0 || AsInt(r.OkValue) != 5 {
		t.Fatalf("non-object input must give the default, got %+v", r)
	}
}

func TestDecodeNull(t *testing.T) {
	if r := decodeForTest(t, JsonDec_null("dflt"), `null`); r.Tag != 0 || r.OkValue != "dflt" {
		t.Fatalf("null on null must be Ok \"dflt\", got %+v", r)
	}
	for _, in := range []string{`0`, `""`, `false`, `[]`, `{}`} {
		if r := decodeForTest(t, JsonDec_null("dflt"), in); r.Tag == 0 {
			t.Fatalf("null on %s must be Err, got Ok %v", in, r.OkValue)
		}
	}
}

func TestDecodeNullable(t *testing.T) {
	r := decodeForTest(t, JsonDec_nullable(JsonDec_int()), `null`)
	if m, ok := r.OkValue.(SkyMaybe[any]); r.Tag != 0 || !ok || m.Tag != 1 {
		t.Fatalf("nullable on null must be Ok Nothing, got %+v", r)
	}
	r = decodeForTest(t, JsonDec_nullable(JsonDec_int()), `3`)
	if m, ok := r.OkValue.(SkyMaybe[any]); r.Tag != 0 || !ok || m.Tag != 0 || AsInt(m.JustValue) != 3 {
		t.Fatalf("nullable on 3 must be Ok (Just 3), got %+v", r)
	}
	if r := decodeForTest(t, JsonDec_nullable(JsonDec_int()), `"3"`); r.Tag == 0 {
		t.Fatalf("nullable on a string must be Err, got Ok %v", r.OkValue)
	}
}

func TestDecodeMaybe(t *testing.T) {
	r := decodeForTest(t, JsonDec_maybe(JsonDec_int()), `"x"`)
	if m, ok := r.OkValue.(SkyMaybe[any]); r.Tag != 0 || !ok || m.Tag != 1 {
		t.Fatalf("maybe on a failing value must be Ok Nothing, got %+v", r)
	}
	r = decodeForTest(t, JsonDec_maybe(JsonDec_int()), `4`)
	if m, ok := r.OkValue.(SkyMaybe[any]); r.Tag != 0 || !ok || m.Tag != 0 || AsInt(m.JustValue) != 4 {
		t.Fatalf("maybe on 4 must be Ok (Just 4), got %+v", r)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
