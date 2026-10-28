package rt

import (
	"reflect"
	"testing"
)

func TestListMap2StopsAtTheShorterList(t *testing.T) {
	add := func(a, b int) int { return a + b }
	if got := List_map2T(add, []int{1, 2, 3}, []int{10, 20}); !reflect.DeepEqual(got, []int{11, 22}) {
		t.Fatalf("map2T: got %v", got)
	}
	if got := List_map2T(add, []int{}, []int{1}); len(got) != 0 || got == nil {
		t.Fatalf("map2T over an empty list must be an empty, non-nil slice: %#v", got)
	}
	// The erased form applies a curried Sky callback one argument at a time.
	curried := func(a any) any { return func(b any) any { return AsInt(a) * AsInt(b) } }
	got := AsList(List_map2(curried, []any{2, 3, 4}, []any{5, 6, 7, 8}))
	if !reflect.DeepEqual(got, []any{10, 18, 28}) {
		t.Fatalf("map2: got %v", got)
	}
}

func TestListMap3StopsAtTheShortestList(t *testing.T) {
	f := func(a int, b string, c bool) string {
		if c {
			return b + "!"
		}
		return b + string(rune('0'+a))
	}
	got := List_map3T(f, []int{1, 2, 3}, []string{"a", "b", "c"}, []bool{false, true})
	if !reflect.DeepEqual(got, []string{"a1", "b!"}) {
		t.Fatalf("map3T: got %v", got)
	}
	curried := func(a any) any {
		return func(b any) any { return func(c any) any { return AsInt(a) + AsInt(b) + AsInt(c) } }
	}
	if got := AsList(List_map3(curried, []any{1, 2}, []any{10, 20}, []any{100, 200, 300})); !reflect.DeepEqual(got, []any{111, 222}) {
		t.Fatalf("map3: got %v", got)
	}
}

// map2 Tuple.pair is zip: the same pairs, the same length.
func TestListMap2PairIsZip(t *testing.T) {
	xs := []any{1, 2, 3}
	ys := []any{"a", "b"}
	pair := func(a any) any { return func(b any) any { return SkyTuple2{V0: a, V1: b} } }
	if a, b := AsList(List_map2(pair, xs, ys)), AsList(List_zip(xs, ys)); !reflect.DeepEqual(a, b) {
		t.Fatalf("map2 pair %v != zip %v", a, b)
	}
}

func TestListUniqueKeepsFirstOccurrenceOrder(t *testing.T) {
	got := AsList(List_unique([]any{3, 1, 3, 2, 1, 3}))
	if !reflect.DeepEqual(got, []any{3, 1, 2}) {
		t.Fatalf("unique ints: got %v", got)
	}
	got = AsList(List_unique([]any{"b", "a", "b", "", ""}))
	if !reflect.DeepEqual(got, []any{"b", "a", ""}) {
		t.Fatalf("unique strings: got %v", got)
	}
	// Tuples compare by value, like `==`.
	got = AsList(List_unique([]any{SkyTuple2{V0: 1, V1: "x"}, SkyTuple2{V0: 1, V1: "x"}, SkyTuple2{V0: 1, V1: "y"}}))
	if len(got) != 2 {
		t.Fatalf("unique tuples: got %v", got)
	}
	if got := AsList(List_unique([]any{})); len(got) != 0 {
		t.Fatalf("unique of empty: got %v", got)
	}
}

func TestStringIndexesCountsCharactersNonOverlapping(t *testing.T) {
	cases := []struct {
		sub, s string
		want   []int
	}{
		{"i", "Mississippi", []int{1, 4, 7, 10}},
		{"ss", "Mississippi", []int{2, 5}},
		{"aa", "aaaa", []int{0, 2}}, // non-overlapping, as Elm
		{"needle", "haystack", []int{}},
		{"", "abc", []int{}}, // an empty needle has no occurrences, as Elm
		{"x", "", []int{}},
		// Characters, not bytes: "£" and "─" are 2 and 3 UTF-8 bytes.
		{"=", "£1=─2=", []int{2, 5}},
		{"─", "a──b─", []int{1, 2, 4}},
	}
	for _, c := range cases {
		if got := String_indexesT(c.sub, c.s); !reflect.DeepEqual(got, c.want) {
			t.Errorf("indexes %q %q = %v, want %v", c.sub, c.s, got, c.want)
		}
		erased := AsList(String_indexes(c.sub, c.s))
		if len(erased) != len(c.want) {
			t.Errorf("erased indexes %q %q = %v", c.sub, c.s, erased)
		}
	}
}

func TestCharIsAlphaNumAndIsSpace(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '7', 'é', 'ж', '٣'} {
		if !Char_isAlphaNumT(r) || !AsBool(Char_isAlphaNum(r)) {
			t.Errorf("isAlphaNum %q should be True", r)
		}
	}
	for _, r := range []rune{'_', ' ', '-', '£', '─'} {
		if Char_isAlphaNumT(r) || AsBool(Char_isAlphaNum(r)) {
			t.Errorf("isAlphaNum %q should be False", r)
		}
	}
	for _, r := range []rune{' ', '\t', '\n', '\r', '\v', '\f', 0x85, 0xA0, 0x2003, 0x3000} {
		if !Char_isSpaceT(r) || !AsBool(Char_isSpace(r)) {
			t.Errorf("isSpace %U should be True", r)
		}
	}
	for _, r := range []rune{'a', '0', '_', 0x200B} { // U+200B zero width space is not White_Space
		if Char_isSpaceT(r) || AsBool(Char_isSpace(r)) {
			t.Errorf("isSpace %U should be False", r)
		}
	}
}

const benchN = 1_000_000

func benchInts() ([]int, []int) {
	xs := make([]int, benchN)
	ys := make([]int, benchN)
	for i := range xs {
		xs[i] = i
		ys[i] = benchN - i
	}
	return xs, ys
}

// BenchmarkListMap2 is the typed one-pass map2 over 1,000,000 Ints.
func BenchmarkListMap2(b *testing.B) {
	xs, ys := benchInts()
	add := func(a, c int) int { return a + c }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = List_map2T(add, xs, ys)
	}
}

// BenchmarkListZipThenMap is the `List.map (\( a, b ) -> a + b) (List.zip xs
// ys)` form map2 replaces: n tuples, then a second walk.
func BenchmarkListZipThenMap(b *testing.B) {
	xs, ys := benchInts()
	ax := make([]any, benchN)
	ay := make([]any, benchN)
	for i := range xs {
		ax[i], ay[i] = xs[i], ys[i]
	}
	f := func(t any) any { p := t.(SkyTuple2); return AsInt(p.V0) + AsInt(p.V1) }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = List_mapAny(f, List_zip(ax, ay))
	}
}

// Every v0.29.0 typed twin (lower.rs `kernel_typed_return_twin`,
// `list_map_n_typed`) returns what its erased kernel returns.
func TestCoreHelperTwinsMatchErased(t *testing.T) {
	for _, s := range twinStrings {
		for _, sub := range []string{"", "a", "lo", "wörld", "x", "語"} {
			typed := String_indexesT(sub, s)
			erased := AsList(String_indexes(sub, s))
			if len(typed) != len(erased) {
				t.Fatalf("indexes %q %q: %v vs %v", sub, s, typed, erased)
			}
			for i := range typed {
				if typed[i] != AsInt(erased[i]) {
					t.Errorf("indexes %q %q: %v vs %v", sub, s, typed, erased)
				}
			}
		}
		for _, r := range s {
			if Char_isAlphaNumT(r) != AsBool(Char_isAlphaNum(r)) || Char_isSpaceT(r) != AsBool(Char_isSpace(r)) {
				t.Errorf("char twins disagree on %q", r)
			}
		}
	}
	xs, ys, zs := []int{1, 2, 3}, []string{"a", "b"}, []bool{true, false, true}
	typed2 := List_map2T(func(a int, b string) string { return b + string(rune('0'+a)) }, xs, ys)
	erased2 := AsList(List_map2(func(a any) any {
		return func(b any) any { return AsString(b) + string(rune('0'+AsInt(a))) }
	}, []any{1, 2, 3}, []any{"a", "b"}))
	if len(typed2) != len(erased2) || typed2[0] != erased2[0] || typed2[1] != erased2[1] {
		t.Errorf("map2 twins: %v vs %v", typed2, erased2)
	}
	typed3 := List_map3T(func(a int, b string, c bool) bool { return c && a > 1 }, xs, ys, zs)
	erased3 := AsList(List_map3(func(a any) any {
		return func(b any) any { return func(c any) any { return AsBool(c) && AsInt(a) > 1 } }
	}, []any{1, 2, 3}, []any{"a", "b"}, []any{true, false, true}))
	if len(typed3) != 2 || len(erased3) != 2 || typed3[0] != erased3[0] || typed3[1] != erased3[1] {
		t.Errorf("map3 twins: %v vs %v", typed3, erased3)
	}
}

// BenchmarkListMap2Erased is the erased map2 (a callback the lowering cannot
// type), for comparison with the typed twin and the zip+map form.
func BenchmarkListMap2Erased(b *testing.B) {
	xs, ys := benchInts()
	ax := make([]any, benchN)
	ay := make([]any, benchN)
	for i := range xs {
		ax[i], ay[i] = xs[i], ys[i]
	}
	f := func(a any) any { return func(c any) any { return AsInt(a) + AsInt(c) } }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = List_map2(f, ax, ay)
	}
}

// BenchmarkListMap2ErasedFlat is the erased map2 with the flat
// `func(any, any) any` callback the erased lowering emits for `\a b -> …`.
func BenchmarkListMap2ErasedFlat(b *testing.B) {
	xs, ys := benchInts()
	ax := make([]any, benchN)
	ay := make([]any, benchN)
	for i := range xs {
		ax[i], ay[i] = xs[i], ys[i]
	}
	f := func(a, c any) any { return AsInt(a) + AsInt(c) }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = List_map2(f, ax, ay)
	}
}

func TestListMap2ErasedFlatCallback(t *testing.T) {
	f := func(a, c any) any { return AsInt(a) - AsInt(c) }
	if got := AsList(List_map2(f, []any{5, 6}, []any{1, 2, 3})); !reflect.DeepEqual(got, []any{4, 4}) {
		t.Fatalf("flat map2: %v", got)
	}
	g := func(a, c, d any) any { return AsInt(a) + AsInt(c) + AsInt(d) }
	if got := AsList(List_map3(g, []any{1}, []any{2, 3}, []any{4, 5})); !reflect.DeepEqual(got, []any{7}) {
		t.Fatalf("flat map3: %v", got)
	}
}

func TestRegexMatchesPositionsAndGroups(t *testing.T) {
	ms, _ := regexMatches(`(\d+)(px)?`, "£4 ─ 10px", -1)
	if len(ms) != 2 {
		t.Fatalf("matches: %v", ms)
	}
	if ms[0]["index"] != 1 || ms[1]["index"] != 5 || ms[1]["number"] != 2 {
		t.Errorf("positions: %v", ms)
	}
	subs := ms[0]["submatches"].([]any)
	if AsString(subs[0].(SkyMaybe[any]).JustValue) != "4" || subs[1].(SkyMaybe[any]).Tag == 0 {
		t.Errorf("groups: %#v", subs)
	}
	if got := AsString(Regex_replaceWith(`\d+`, func(m any) any { return "[" + AsString(m.(map[string]any)["match"]) + "]" }, "£4 ─ 10")); got != "£[4] ─ [10]" {
		t.Errorf("replaceWith: %q", got)
	}
	if got := Regex_findT(`x*`, "abc"); got.Tag != 0 || got.JustValue != "" {
		t.Errorf("find empty match: %#v", got)
	}
}
