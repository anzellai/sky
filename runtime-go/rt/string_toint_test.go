package rt

import "testing"

// Regression (conformance finding L2): String.toInt must trim surrounding
// whitespace, consistent with String.toFloat and the typed String_toIntT.
func TestStringToIntTrimsWhitespace(t *testing.T) {
	cases := map[string]bool{
		"42":     true,
		"  42  ": true, // the fix: was Nothing before
		"\t-7\n": true,
		"abc":    false,
		"4.5":    false,
		"":       false,
	}
	for in, wantJust := range cases {
		m, ok := String_toInt(in).(SkyMaybe[any])
		if !ok {
			t.Fatalf("%q: not a SkyMaybe", in)
		}
		if (m.Tag == 0) != wantJust {
			t.Errorf("String.toInt %q: Just=%v want %v", in, m.Tag == 0, wantJust)
		}
	}
}
