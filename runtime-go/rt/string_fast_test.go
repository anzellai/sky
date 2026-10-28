package rt

import (
	"math/rand"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// refSlice is String.slice as it was before string_fast.go: whole-string
// []rune conversion. The walking kernels must agree with it on valid UTF-8.
func refSlice(st, en int, s string) string {
	runes := []rune(s)
	total := len(runes)
	if st < 0 {
		st += total
	}
	if en < 0 {
		en += total
	}
	if st < 0 {
		st = 0
	}
	if en > total {
		en = total
	}
	if st > en {
		return ""
	}
	return string(runes[st:en])
}

func refLeft(n int, s string) string {
	r := []rune(s)
	if n > len(r) {
		n = len(r)
	}
	if n < 0 {
		n = 0
	}
	return string(r[:n])
}

func refRight(n int, s string) string {
	r := []rune(s)
	if n > len(r) {
		n = len(r)
	}
	if n < 0 {
		n = 0
	}
	return string(r[len(r)-n:])
}

func refDropLeft(n int, s string) string {
	r := []rune(s)
	if n <= 0 {
		return s
	}
	if n >= len(r) {
		return ""
	}
	return string(r[n:])
}

func refDropRight(n int, s string) string {
	r := []rune(s)
	if n <= 0 {
		return s
	}
	if n >= len(r) {
		return ""
	}
	return string(r[:len(r)-n])
}

var sampleRunes = []rune("ab é漢字€ü🙂z\n")

func randomText(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteRune(sampleRunes[r.Intn(len(sampleRunes))])
	}
	return b.String()
}

func TestStringWalkingKernelsMatchTheRuneReference(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for _, size := range []int{0, 1, 2, 7, 63, 64, 65, 300, 5000} {
		s := randomText(r, size)
		total := utf8.RuneCountInString(s)
		idx := []int{-total - 3, -total, -total + 1, -2, -1, 0, 1, 2, 63, 64, 65, total - 1, total, total + 4}
		for _, i := range idx {
			if got, want := String_left(i, s), refLeft(i, s); got != want {
				t.Fatalf("left %d (size %d): %q want %q", i, size, got, want)
			}
			if got, want := String_right(i, s), refRight(i, s); got != want {
				t.Fatalf("right %d (size %d): %q want %q", i, size, got, want)
			}
			if got, want := String_dropLeft(i, s), refDropLeft(i, s); got != want {
				t.Fatalf("dropLeft %d (size %d)", i, size)
			}
			if got, want := String_dropRight(i, s), refDropRight(i, s); got != want {
				t.Fatalf("dropRight %d (size %d)", i, size)
			}
			if got, want := String_leftTA(i, s), refLeft(i, s); got != want {
				t.Fatalf("leftIntT %d", i)
			}
			if got, want := String_dropRightTA(i, s), refDropRight(i, s); got != want {
				t.Fatalf("dropRightIntT %d", i)
			}
			for _, j := range idx {
				want := refSlice(i, j, s)
				if got := String_slice(i, j, s); got != want {
					t.Fatalf("slice %d %d (size %d): %q want %q", i, j, size, got, want)
				}
				if got := String_sliceT(i, j, s); got != want {
					t.Fatalf("sliceT %d %d (size %d)", i, j, size)
				}
			}
		}
	}
}

// A per-character slice loop over a large string uses the index cache and
// still gives the reference answer at every step, including the steps after
// another large string replaced the entry.
func TestStringSliceLoopOverALargeStringUsesTheIndex(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	s := randomText(r, 20000)
	other := randomText(r, 9000)
	rs := []rune(s)
	for i := 0; i < len(rs); i += 37 {
		if got := String_sliceT(i, i+1, s); got != string(rs[i]) {
			t.Fatalf("slice %d: %q want %q", i, got, string(rs[i]))
		}
		if i%3700 == 0 {
			_ = String_sliceT(5, 6, other) // evicts, then is rebuilt
			_ = String_sliceT(6, 7, other)
		}
	}
	if c := runeIndexCache.Load(); c == nil {
		t.Fatal("a slice loop over a large string never built the index")
	}
	if got, want := String_sliceT(-3, -1, s), string(rs[len(rs)-3:len(rs)-1]); got != want {
		t.Fatalf("negative slice through the index: %q want %q", got, want)
	}
}

func TestStringIndexCacheIsRaceFree(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	texts := []string{randomText(r, 6000), randomText(r, 7000), randomText(r, 8000)}
	refs := make([][]rune, len(texts))
	for i, s := range texts {
		refs[i] = []rune(s)
	}
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			s, rs := texts[g%3], refs[g%3]
			for i := 0; i < len(rs); i += 11 {
				if got := String_sliceT(i, i+2, s); got != string(rs[i:min(i+2, len(rs))]) {
					t.Errorf("goroutine %d slice %d: %q", g, i, got)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestStringUnconsWalksEveryCodePoint(t *testing.T) {
	s := "aé漢🙂"
	var got []rune
	for {
		m := String_unconsT(s)
		if m.Tag != 0 {
			break
		}
		got = append(got, m.JustValue.V0)
		s = m.JustValue.V1
	}
	if string(got) != "aé漢🙂" {
		t.Fatalf("uncons walk: %q", string(got))
	}
	if m := String_unconsT(""); m.Tag == 0 {
		t.Fatal("uncons \"\" must be Nothing")
	}
	e := String_uncons("é!").(SkyMaybe[any])
	tup := e.JustValue.(SkyTuple2)
	if e.Tag != 0 || tup.V0 != 'é' || tup.V1 != "!" {
		t.Fatalf("erased uncons: %#v", e)
	}
	if String_uncons("").(SkyMaybe[any]).Tag == 0 {
		t.Fatal("erased uncons \"\" must be Nothing")
	}
}

// The typed uncons returns its tuple by value: a character loop allocates
// nothing per character.
func TestStringUnconsTDoesNotAllocate(t *testing.T) {
	s := strings.Repeat("aé漢", 100)
	allocs := testing.AllocsPerRun(10, func() {
		rest := s
		for {
			m := String_unconsT(rest)
			if m.Tag != 0 {
				return
			}
			rest = m.JustValue.V1
		}
	})
	if allocs != 0 {
		t.Fatalf("uncons loop allocated %.0f times, want 0", allocs)
	}
}

func TestStringFoldsVisitInOrder(t *testing.T) {
	cons := func(c any) any {
		return func(acc any) any { return string(c.(rune)) + acc.(string) }
	}
	if got := String_foldl(cons, "", "abé"); got != "éba" {
		t.Fatalf("foldl: %q", got)
	}
	if got := String_foldr(cons, "", "abé"); got != "abé" {
		t.Fatalf("foldr: %q", got)
	}
}

func TestStringListTwinsMatchErased(t *testing.T) {
	s := "aé漢🙂 z"
	cs := String_toListT(s)
	if string(cs) != s || len(cs) != 6 {
		t.Fatalf("toListT: %q", string(cs))
	}
	if got := String_fromListT(cs); got != s {
		t.Fatalf("fromListT: %q", got)
	}
	if got := String_fromList(String_toList(s)); got != s {
		t.Fatalf("erased round trip: %q", got)
	}
	if got := String_fromList(cs); got != s {
		t.Fatalf("erased fromList over []rune: %q", got)
	}
	parts := []string{"a", "é", "", "漢"}
	if got := String_concatT(parts); got != "aé漢" {
		t.Fatalf("concatT: %q", got)
	}
	if got := String_concat(parts); got != "aé漢" {
		t.Fatalf("concat typed: %q", got)
	}
	if got := String_concat([]any{"a", 1, "b"}); got != "a1b" {
		t.Fatalf("concat erased: %q", got)
	}
	if got := String_join(", ", []any{"a", "b"}); got != "a, b" {
		t.Fatalf("join erased: %q", got)
	}
	if got := String_join("-", []string{}); got != "" {
		t.Fatalf("join empty: %q", got)
	}
	if got := String_join("-", nil); got != "" {
		t.Fatalf("join nil: %q", got)
	}
}

// The erased String kernels take a Go string as it is and render anything
// else with %v, as before (no AsString unwrapping of Maybe or VNodes).
func TestSkyStrKeepsThePercentVFallback(t *testing.T) {
	if skyStr("x") != "x" || skyStr(12) != "12" {
		t.Fatal("skyStr")
	}
	if got := String_length(12345); got != 5 {
		t.Fatalf("length of a non-string: %v", got)
	}
}

// A CSV field splitter written with uncons: the use case beyond the request.
func TestUnconsCsvFieldSplitter(t *testing.T) {
	split := func(line string) []string {
		var out []string
		var cur []rune
		rest := line
		for {
			m := String_unconsT(rest)
			if m.Tag != 0 {
				return append(out, string(cur))
			}
			c := m.JustValue.V0
			rest = m.JustValue.V1
			if c == ',' {
				out = append(out, string(cur))
				cur = cur[:0]
			} else {
				cur = append(cur, c)
			}
		}
	}
	got := split("näme,€5,,漢字")
	want := []string{"näme", "€5", "", "漢字"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("csv: %q", got)
	}
}
