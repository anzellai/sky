package rt

import (
	"math"
	"testing"
)

// -0.0 == 0.0 in Sky, so they are one Dict key (before v0.31.0 they were two,
// and Dict.get 0.0 missed an entry stored under -0.0).
func TestDictFloatKey_NegativeZeroIsZero(t *testing.T) {
	negZero := math.Copysign(0, -1)
	if a, b := encodeDictKey(negZero), encodeDictKey(0.0); a != b {
		t.Fatalf("-0 key %q, 0 key %q", a, b)
	}
	d := Dict_fromList([]any{SkyTuple2{V0: negZero, V1: "neg"}, SkyTuple2{V0: 0.0, V1: "pos"}})
	if n := AsInt(Dict_size(d)); n != 1 {
		t.Fatalf("size %d, want 1", n)
	}
	if got := dictJSONKey(encodeDictKey(negZero)); got != "0" {
		t.Fatalf("JSON key %q, want \"0\"", got)
	}
}

func TestCodecFloatKey_TextAndStrictRead(t *testing.T) {
	cases := map[float64]string{
		1.5:                  "1.5",
		2:                    "2",
		1e21:                 "1e+21",
		1e-7:                 "1e-07",
		math.Copysign(0, -1): "0",
		math.Inf(1):          "+Inf",
		math.Inf(-1):         "-Inf",
		math.NaN():           "NaN",
	}
	for f, want := range cases {
		if got := Codec_floatKeyText(JsonValue{raw: f}); got != want {
			t.Errorf("text of %v: got %v, want %q", f, got, want)
		}
		// The text Codec.auto writes for the same key.
		if got := dictJSONKey(encodeDictKey(f)); got != want {
			t.Errorf("auto key of %v: got %q, want %q", f, got, want)
		}
		back := Codec_floatKeyValue(want).(JsonValue).raw
		bf, ok := back.(float64)
		if !ok || dictFloatKeyText(bf) != want {
			t.Errorf("read of %q: got %v", want, back)
		}
	}
	for _, bad := range []string{"1.50", "-0", "Inf", "inf", "+1.5", "1e21", "0x1p0", "1e400", "", " 1"} {
		if v := Codec_floatKeyValue(bad).(JsonValue).raw; v != nil {
			t.Errorf("%q read as %v, want refused", bad, v)
		}
	}
}
