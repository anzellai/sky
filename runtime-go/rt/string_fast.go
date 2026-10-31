package rt

// string_fast.go — String kernels that walk the UTF-8 bytes instead of
// converting the whole string to []rune.
//
// THE UNIT. Every index and every length in Sky.Core.String counts Unicode
// code points (runes): not bytes (Sky.Core.Bytes) and not graphemes
// (String.graphemes). An invalid UTF-8 byte counts as one code point, the
// same rule as Go's `range` over a string and `[]rune(s)`. The walking
// kernels return a SUBSTRING of the input, so an invalid byte inside the
// result is kept as it was (the old []rune round trip replaced it with
// U+FFFD). A valid string gives the same result as before, byte for byte.
//
// COSTS (k is the index argument, n the length of the string in runes):
//
//	String.left k / dropLeft k / slice i j with i, j >= 0:  O(k) or O(j) time,
//	    no allocation (the result shares the input's bytes)
//	String.right k / dropRight k:  O(k) time, walking back from the end
//	String.slice with a negative index:  O(n), the length is needed
//	String.uncons:  O(1)
//
// THE INDEX CACHE. A loop of `String.slice i (i + 1) s` over a large string
// would still be O(n²) (each call walks to i). For a string of at least
// largeStringBytes bytes, the SECOND indexed call on the same string builds a
// sparse index (one byte offset every runeIndexStride runes, O(n) once) and
// later calls start their walk at the nearest index entry: O(runeIndexStride).
// The first call only records the string, so one `String.left 10 big` stays
// O(10). Limits, which are part of the contract:
//
//   - ONE entry for the whole process. The entry holds the string itself, so
//     the string stays alive (and its address cannot be reused) until another
//     large string replaces it; that is at most one string plus n/64 ints.
//   - Concurrent goroutines that slice DIFFERENT large strings replace each
//     other's entry, and each loop can then go back to O(n) per call (O(n²) in
//     all). For a character loop use String.uncons, String.foldl or
//     String.toList: they are O(n) in all, always.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"
)

const (
	largeStringBytes = 4096
	runeIndexStride  = 64
)

// skyStr is the erased String argument. A Go string passes through; anything
// else keeps the `%v` rendering the kernels always used (no AsString: that
// renders VNodes and unwraps Maybe, which `%v` never did).
func skyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// runeIndex is the sparse index of one large string.
type runeIndex struct {
	s     string
	offs  []int // offs[i] = byte offset of rune i*runeIndexStride
	total int   // runes in s
}

type runeIndexSeen struct{ s string }

var (
	runeIndexCache atomic.Pointer[runeIndex]
	runeIndexLast  atomic.Pointer[runeIndexSeen]
)

func sameString(a, b string) bool {
	return len(a) == len(b) && unsafe.StringData(a) == unsafe.StringData(b)
}

// indexFor returns the cached index for s, building it on the second call
// for the same large string. nil for a small string or a first call.
func indexFor(s string) *runeIndex {
	if len(s) < largeStringBytes {
		return nil
	}
	if c := runeIndexCache.Load(); c != nil && sameString(c.s, s) {
		return c
	}
	if last := runeIndexLast.Load(); last == nil || !sameString(last.s, s) {
		runeIndexLast.Store(&runeIndexSeen{s: s})
		return nil
	}
	idx := &runeIndex{s: s, offs: make([]int, 0, len(s)/runeIndexStride+1)}
	n := 0
	for i := range s {
		if n%runeIndexStride == 0 {
			idx.offs = append(idx.offs, i)
		}
		n++
	}
	idx.total = n
	runeIndexCache.Store(idx)
	return idx
}

// runeOffset is the byte offset of rune k (k >= 0) in s, or len(s) when s has
// k runes or fewer.
func runeOffset(s string, k int) int {
	if k <= 0 {
		return 0
	}
	start, done := 0, 0
	if idx := indexFor(s); idx != nil {
		if k >= idx.total {
			return len(s)
		}
		slot := k / runeIndexStride
		start, done = idx.offs[slot], slot*runeIndexStride
	}
	return walkRunes(s, start, k-done)
}

// walkRunes is the byte offset reached by stepping k runes forward from byte
// offset i, or len(s) when the string ends first. It never touches the cache.
func walkRunes(s string, i, k int) int {
	for done := 0; i < len(s); {
		if done == k {
			return i
		}
		if s[i] < utf8.RuneSelf {
			i++
		} else {
			_, w := utf8.DecodeRuneInString(s[i:])
			i += w
		}
		done++
	}
	return len(s)
}

// runeOffsetFromEnd is the byte offset where the last k runes of s start
// (0 when s has k runes or fewer).
func runeOffsetFromEnd(s string, k int) int {
	i := len(s)
	for ; k > 0 && i > 0; k-- {
		if s[i-1] < utf8.RuneSelf {
			i--
		} else {
			_, w := utf8.DecodeLastRuneInString(s[:i])
			i -= w
		}
	}
	return i
}

// runeTotal is the number of runes in s, from the index when there is one.
func runeTotal(s string) int {
	if c := runeIndexCache.Load(); c != nil && sameString(c.s, s) {
		return c.total
	}
	return utf8.RuneCountInString(s)
}

func stringLeft(n int, s string) string {
	if n <= 0 {
		return ""
	}
	return s[:runeOffset(s, n)]
}

func stringDropLeft(n int, s string) string {
	if n <= 0 {
		return s
	}
	return s[runeOffset(s, n):]
}

func stringRight(n int, s string) string {
	if n <= 0 {
		return ""
	}
	return s[runeOffsetFromEnd(s, n):]
}

func stringDropRight(n int, s string) string {
	if n <= 0 {
		return s
	}
	return s[:runeOffsetFromEnd(s, n)]
}

// stringSlice is Elm's String.slice: a negative index counts from the end,
// the range is clamped, and start > end is "".
func stringSlice(st, en int, s string) string {
	if st < 0 || en < 0 {
		total := runeTotal(s)
		if st < 0 {
			st += total
		}
		if en < 0 {
			en += total
		}
		if st < 0 {
			st = 0
		}
	}
	if en <= st {
		return ""
	}
	a := runeOffset(s, st)
	if a == len(s) {
		return ""
	}
	// Walk on from a instead of from the start: O(en) in all.
	b := walkRunes(s, a, en-st)
	return s[a:b]
}

// String_unconsT — String.uncons over a proven string: the first code point
// and the rest of the string, which shares the input's bytes. O(1).
func String_unconsT(s string) SkyMaybe[T2[rune, string]] {
	if s == "" {
		return Nothing[T2[rune, string]]()
	}
	r, w := rune(s[0]), 1
	if r >= utf8.RuneSelf {
		r, w = utf8.DecodeRuneInString(s)
	}
	return Just(T2[rune, string]{V0: r, V1: s[w:]})
}

// String_uncons — the erased form: Maybe (Char, String) with an erased tuple.
func String_uncons(s any) any {
	m := String_unconsT(skyStr(s))
	if m.Tag != 0 {
		return Nothing[any]()
	}
	return Just[any](SkyTuple2{V0: m.JustValue.V0, V1: m.JustValue.V1})
}

// String_foldl / String_foldr — the erased kernels behind the kernel
// pseudo-module (`String.foldl` without an import). Sky.Core.String defines
// both in Sky over String.uncons; these follow the same order: foldl from
// the first code point, foldr from the last. O(n).
func String_foldl(fn any, acc any, s any) any {
	for _, r := range skyStr(s) {
		acc = SkyCall(fn, r, acc)
	}
	return acc
}

func String_foldr(fn any, acc any, s any) any {
	rs := []rune(skyStr(s))
	for i := len(rs) - 1; i >= 0; i-- {
		acc = SkyCall(fn, rs[i], acc)
	}
	return acc
}

// String_toListT / String_fromListT — typed twins: one allocation each.
func String_toListT(s string) []rune { return []rune(s) }

func String_fromListT(cs []rune) string { return string(cs) }

// String_concatT — String.concat over a proven []string: one allocation.
func String_concatT(parts []string) string { return strings.Join(parts, "") }

// String_leftTA, String_rightTA, … — typed twins taking a proven int and
// a proven string (no box for an index above 255).
func String_leftTA(n int, s string) string      { return stringLeft(n, s) }
func String_rightTA(n int, s string) string     { return stringRight(n, s) }
func String_dropLeftTA(n int, s string) string  { return stringDropLeft(n, s) }
func String_dropRightTA(n int, s string) string { return stringDropRight(n, s) }

// joinErased renders an erased list of strings with sep: one pass to size the
// result, one copy.
func joinErased(sep string, list any) string {
	switch xs := list.(type) {
	case []string:
		return strings.Join(xs, sep)
	case nil:
		return ""
	}
	items := AsList(list)
	if len(items) == 0 {
		return ""
	}
	strs := make([]string, len(items))
	total := len(sep) * (len(items) - 1)
	for i, it := range items {
		strs[i] = skyStr(it)
		total += len(strs[i])
	}
	var b strings.Builder
	b.Grow(total)
	for i, p := range strs {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p)
	}
	return b.String()
}
