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

func TestMathAndListTwinsMatchErased(t *testing.T) {
	floats := []float64{0, 1, -1, 0.5, 2.75, -3.5, 1e9, math.Inf(1), math.Inf(-1), math.NaN()}
	same := func(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }
	for _, x := range floats {
		pairs := []struct {
			name string
			typ  float64
			era  any
		}{
			{"sqrt", Math_sqrtT(x), Math_sqrt(x)},
			{"sin", Math_sinT(x), Math_sin(x)},
			{"cos", Math_cosT(x), Math_cos(x)},
			{"tan", Math_tanT(x), Math_tan(x)},
			{"log", Math_logT(x), Math_log(x)},
			{"exp", Math_expT(x), Math_exp(x)},
		}
		for _, p := range pairs {
			if !same(p.typ, p.era.(float64)) {
				t.Errorf("%s %v: %v vs %v", p.name, x, p.typ, p.era)
			}
		}
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			if Math_floorT(x) != Math_floor(x) || Math_ceilT(x) != Math_ceil(x) || Math_roundT(x) != Math_round(x) {
				t.Errorf("floor/ceil/round %v", x)
			}
		}
		for _, y := range floats {
			if !same(Math_powT(x, y), Math_pow(x, y).(float64)) {
				t.Errorf("pow %v %v", x, y)
			}
			if !same(Math_minFloatT(x, y), Math_min(x, y).(float64)) || !same(Math_maxFloatT(x, y), Math_max(x, y).(float64)) {
				t.Errorf("min/max %v %v", x, y)
			}
		}
	}
	ints := []int{0, 1, -1, 3, -7, 100, math.MaxInt32}
	for _, a := range ints {
		if Math_absT(a) != Math_abs(a) {
			t.Errorf("abs %d", a)
		}
		for _, b := range ints {
			if Math_minT(a, b) != Math_min(a, b) || Math_maxT(a, b) != Math_max(a, b) {
				t.Errorf("min/max %d %d", a, b)
			}
			if Basics_modByT(a, b) != Basics_modBy(a, b) {
				t.Errorf("modBy %d %d", a, b)
			}
		}
	}
	for _, r := range [][2]int{{1, 5}, {0, 0}, {3, 1}, {-2, 2}} {
		got := List_rangeT(r[0], r[1])
		want := List_range(r[0], r[1]).([]any)
		if len(got) != len(want) {
			t.Fatalf("range %v: %v vs %v", r, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("range %v: %v vs %v", r, got, want)
			}
		}
	}
	for _, f := range []float64{0, 1.5, -2.25, 1e21, 123456789.125} {
		if String_fromFloatAnyT(f) != String_fromFloat(f) {
			t.Errorf("fromFloat %v", f)
		}
	}
	for _, c := range [][3]string{{"a+", "X", "caaab"}, {"(", "x", "a(b"}, {"```\\w*", "", "```json {}```"}} {
		if Regex_replaceT(c[0], c[1], c[2]) != Regex_replace(c[0], c[1], c[2]) {
			t.Errorf("regex replace %v", c)
		}
	}
}

// v0.28.0: the length, trim, replace and regex-match twins.
func TestStringLengthTrimReplaceMatchTwinsMatchErased(t *testing.T) {
	for _, s := range twinStrings {
		if got, want := String_lengthT(s), String_length(s); got != want {
			t.Errorf("length %q: %v vs %v", s, got, want)
		}
		if got, want := String_trimT(s), String_trim(s); got != want {
			t.Errorf("trim %q: %q vs %q", s, got, want)
		}
		for _, old := range []string{"", "a", "ö", " "} {
			if got, want := String_replaceT(old, "_", s), String_replace(old, "_", s); got != want {
				t.Errorf("replace %q in %q: %q vs %q", old, s, got, want)
			}
		}
		for _, re := range []string{"^a", "\\s", "[", "ö", "^$"} {
			if got, want := Regex_matchT(re, s), Regex_match(re, s); got != want {
				t.Errorf("match %q %q: %v vs %v", re, s, got, want)
			}
		}
	}
}
