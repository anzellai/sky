package rt

import (
	"math"
	"strings"
	"testing"
)

// Every typed twin the lowerer routes to (lower.rs `kernel_typed_return_twin`)
// returns exactly what its erased kernel returns, on the same inputs.

var twinStrings = []string{"", "a", "héllo wörld", "ÀÉÎ", "a b&c=d/e?f", "日本語テキスト", " spaced "}

func TestStringTwinsMatchErased(t *testing.T) {
	for _, s := range twinStrings {
		if got, want := String_toUpperT(s), String_toUpper(s); got != want {
			t.Errorf("toUpper %q: %q vs %q", s, got, want)
		}
		if got, want := String_toLowerT(s), String_toLower(s); got != want {
			t.Errorf("toLower %q: %q vs %q", s, got, want)
		}
		if got, want := Encoding_urlEncodeT(s), Encoding_urlEncode(s); got != want {
			t.Errorf("urlEncode %q: %q vs %q", s, got, want)
		}
		for _, sub := range []string{"", "a", "lo", "wörld", "x"} {
			if got, want := String_endsWithT(sub, s), String_endsWith(sub, s); got != want {
				t.Errorf("endsWith %q %q", sub, s)
			}
			if got, want := String_startsWithT(sub, s), String_startsWith(sub, s); got != want {
				t.Errorf("startsWith %q %q", sub, s)
			}
			if got, want := String_containsT(sub, s), String_contains(sub, s); got != want {
				t.Errorf("contains %q %q", sub, s)
			}
		}
		for _, n := range []int{-3, 0, 1, 2, 5, 100} {
			if got, want := String_leftT(n, s), String_left(n, s); got != want {
				t.Errorf("left %d %q", n, s)
			}
			if got, want := String_rightT(n, s), String_right(n, s); got != want {
				t.Errorf("right %d %q", n, s)
			}
			if got, want := String_dropLeftT(n, s), String_dropLeft(n, s); got != want {
				t.Errorf("dropLeft %d %q", n, s)
			}
			if got, want := String_dropRightT(n, s), String_dropRight(n, s); got != want {
				t.Errorf("dropRight %d %q", n, s)
			}
		}
	}
	for _, n := range []int{0, 1, -1, 42, math.MaxInt64, math.MinInt64} {
		if got, want := String_fromIntT(n), String_fromInt(n); got != want {
			t.Errorf("fromInt %d: %q vs %q", n, got, want)
		}
	}
	for _, parts := range [][]string{nil, {}, {"a"}, {"a", "", "c"}} {
		for _, sep := range []string{"", ", ", "·"} {
			if got, want := String_joinT(sep, parts), String_join(sep, parts); got != want {
				t.Errorf("join %q %v: %q vs %q", sep, parts, got, want)
			}
		}
	}
	for _, b := range []bool{true, false} {
		if got, want := Basics_notT(b), Basics_not(b); got != want {
			t.Errorf("not %v", b)
		}
	}
}

func TestJsonEncodeTwinMatchesErased(t *testing.T) {
	values := []any{
		JsonEnc_object([]any{SkyTuple2{V0: "model", V1: JsonEnc_string("m")}, SkyTuple2{V0: "n", V1: JsonEnc_int(3)}}),
		JsonEnc_list(func(x any) any { return JsonEnc_string(x) }, []any{"a", "b"}),
		JsonEnc_null(),
	}
	for _, v := range values {
		for _, indent := range []int{0, 2} {
			if got, want := JsonEnc_encodeT(indent, v), JsonEnc_encode(indent, v); got != want {
				t.Errorf("encode %d: %q vs %q", indent, got, want)
			}
		}
	}
	// A NaN still fails loudly, as the erased kernel does.
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "JsonEnc_encode") {
			t.Fatalf("NaN must panic with the classified message, got %v", r)
		}
	}()
	JsonEnc_encodeT(0, JsonEnc_float(math.NaN()))
}
