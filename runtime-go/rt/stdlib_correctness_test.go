package rt

import (
	"strings"
	"testing"
)

// Json.Decode.int must reject a fractional number instead of truncating it,
// while still accepting integral-valued floats (3.0, 1e2) — JSON has no
// int/float split, so an integer literal arrives as float64.
func TestJsonDecIntRejectsFractional(t *testing.T) {
	dec := JsonDec_int().(JsonDecoder)

	// 3.5 → Err (was silently truncated to 3).
	if r := dec.run(3.5); AsInt(resultTag(r)) != 1 {
		t.Fatalf("JsonDec_int(3.5) should be Err, got %#v", r)
	}
	// 3.0 → Ok 3.
	if r := dec.run(float64(3)); AsInt(resultTag(r)) != 0 {
		t.Fatalf("JsonDec_int(3.0) should be Ok, got %#v", r)
	}
	// 1e2 → Ok 100.
	if r := dec.run(1e2); AsInt(resultTag(r)) != 0 {
		t.Fatalf("JsonDec_int(1e2) should be Ok, got %#v", r)
	}
}

func resultTag(r any) any {
	if res, ok := r.(SkyResult[any, any]); ok {
		return res.Tag
	}
	return -1
}

// Json.Encode.object must preserve insertion order (Go maps sort keys).
func TestJsonEncObjectPreservesOrder(t *testing.T) {
	pairs := []any{
		SkyTuple2{V0: "zebra", V1: JsonEnc_int(1)},
		SkyTuple2{V0: "apple", V1: JsonEnc_int(2)},
		SkyTuple2{V0: "mango", V1: JsonEnc_int(3)},
	}
	obj := JsonEnc_object(pairs)
	out := JsonEnc_encode(0, obj).(string)
	want := `{"zebra":1,"apple":2,"mango":3}`
	if out != want {
		t.Fatalf("JsonEnc_object order = %s, want %s", out, want)
	}
}

// errorToString / toString must render the Error ADT as "<Kind>: <msg>",
// not the raw Go struct.
func TestErrorToStringRendersAdt(t *testing.T) {
	err := ErrUnexpected("boom")
	got := Basics_errorToStringT(err)
	if got != "Unexpected: boom" {
		t.Fatalf("errorToString(ErrUnexpected boom) = %q, want %q", got, "Unexpected: boom")
	}
	if s := Basics_toString(err); s != "Unexpected: boom" {
		t.Fatalf("toString(err) = %q, want %q", s, "Unexpected: boom")
	}
	// Non-Error values still stringify generically.
	if strings.Contains(Basics_toString(42), "Error") {
		t.Fatalf("toString(42) should not mention Error")
	}
	if r := Basics_errorToStringT(ErrDecode("bad json")); r != "Decode: bad json" {
		t.Fatalf("errorToString(ErrDecode) = %q", r)
	}
}
