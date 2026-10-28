package rt

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sky.Core.List.map2 / map3 / unique, Sky.Core.String.indexes and
// Sky.Core.Char.isAlphaNum / isSpace.
//
// map2 and map3 are one pass with the output allocated once at the shorter
// length: no intermediate list of tuples (the `List.map f (List.zip xs ys)`
// form allocates n tuples and walks twice). The `T` forms are what the typed
// lowering calls when the element types and the callback's Go shape are
// statically known (lower.rs `list_map_n_typed`); the erased forms take the
// Sky callback as `any` and apply it one argument at a time, like
// `List_indexedMap`.

// List_map2 is `List.map2 f xs ys`: `f` applied pairwise, stopping at the
// shorter list. O(min(n, m)) time, one output allocation.
func List_map2(fn any, a any, b any) any {
	la := asList(a)
	lb := asList(b)
	n := min(len(la), len(lb))
	out := make([]any, n)
	// The erased lowering emits a two-parameter lambda as a flat
	// `func(any, any) any`: call it directly, without a partial per element.
	if f, ok := fn.(func(any, any) any); ok {
		for i := 0; i < n; i++ {
			out[i] = f(la[i], lb[i])
		}
		return out
	}
	for i := 0; i < n; i++ {
		out[i] = SkyCall(SkyCall(fn, la[i]), lb[i])
	}
	return out
}

// List_map3 is `List.map3 f xs ys zs`, stopping at the shortest list.
func List_map3(fn any, a any, b any, c any) any {
	la := asList(a)
	lb := asList(b)
	lc := asList(c)
	n := min(min(len(la), len(lb)), len(lc))
	out := make([]any, n)
	if f, ok := fn.(func(any, any, any) any); ok {
		for i := 0; i < n; i++ {
			out[i] = f(la[i], lb[i], lc[i])
		}
		return out
	}
	for i := 0; i < n; i++ {
		out[i] = SkyCall(SkyCall(SkyCall(fn, la[i]), lb[i]), lc[i])
	}
	return out
}

// List_map2T is the typed twin of List_map2: a typed callback over typed
// slices, no `any` box per element.
func List_map2T[A, B, C any](fn func(A, B) C, xs []A, ys []B) []C {
	n := min(len(xs), len(ys))
	out := make([]C, n)
	for i := 0; i < n; i++ {
		out[i] = fn(xs[i], ys[i])
	}
	return out
}

// List_map3T is the typed twin of List_map3.
func List_map3T[A, B, C, D any](fn func(A, B, C) D, xs []A, ys []B, zs []C) []D {
	n := min(min(len(xs), len(ys)), len(zs))
	out := make([]D, n)
	for i := 0; i < n; i++ {
		out[i] = fn(xs[i], ys[i], zs[i])
	}
	return out
}

// List_unique is `List.unique xs`: the list with each value kept at its
// FIRST occurrence, in order. Two values are the same when `==` says so; the
// check is the identity key `Set` uses, so the cost is O(n) expected time
// plus the key of each element (O(n · k) for elements of key size k), and
// O(n) extra memory.
func List_unique(xs any) any {
	items := asList(xs)
	seen := make(map[string]struct{}, len(items))
	out := make([]any, 0, len(items))
	for _, x := range items {
		k := identityKey(x)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, x)
	}
	return out
}

// String_indexes is `String.indexes sub s`: the start of every
// non-overlapping occurrence of `sub` in `s`, left to right, counted in
// characters (Unicode code points, the unit of String.slice and
// String.length). An empty `sub` gives `[]`, as in Elm. O(len(s)) time.
func String_indexes(sub any, s any) any {
	is := String_indexesT(AsString(sub), AsString(s))
	out := make([]any, len(is))
	for i, n := range is {
		out[i] = n
	}
	return out
}

// String_indexesT is the typed twin of String_indexes.
func String_indexesT(sub string, s string) []int {
	out := []int{}
	if sub == "" {
		return out
	}
	subRunes := utf8.RuneCountInString(sub)
	byteAt, runeAt := 0, 0
	for {
		j := strings.Index(s[byteAt:], sub)
		if j < 0 {
			return out
		}
		runeAt += utf8.RuneCountInString(s[byteAt : byteAt+j])
		out = append(out, runeAt)
		runeAt += subRunes
		byteAt += j + len(sub)
	}
}

// Char_isAlphaNum is `Char.isAlphaNum`: `isAlpha c || isDigit c`, a Unicode
// letter or a Unicode decimal digit (Sky's isAlpha and isDigit are Unicode).
func Char_isAlphaNum(c any) any { return Char_isAlphaNumT(firstRune(c)) }

// Char_isAlphaNumT is the typed twin of Char_isAlphaNum.
func Char_isAlphaNumT(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }

// Char_isSpace is `Char.isSpace`: Go's unicode.IsSpace, the Unicode
// White_Space property. In Latin-1 that is tab, newline, vertical tab, form
// feed, carriage return, space, U+0085 (next line) and U+00A0 (no-break
// space). It is the set `String.trim` removes.
func Char_isSpace(c any) any { return Char_isSpaceT(firstRune(c)) }

// Char_isSpaceT is the typed twin of Char_isSpace.
func Char_isSpaceT(c rune) bool { return unicode.IsSpace(c) }
