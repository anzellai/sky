package rt

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Sky.Core.Regex.findMatch / findAllMatches / replaceWith: matches with their
// position and capture groups (Elm's `Regex.Match` shape).
//
// A match crosses to Sky as the record
// `{ match : String, index : Int, number : Int, submatches : List (Maybe String) }`
// (a `map[string]any` the call site narrows to `Sky_Core_Regex_Match_R`, as
// every record-returning kernel does). `index` counts characters (Unicode code
// points, the unit of `String.slice`), never bytes. `number` is the 1-based
// position of the match among all matches. A group that did not take part in
// the match is `Nothing`; a group that matched the empty string is `Just ""`.
//
// An invalid pattern behaves as the older Regex functions do: no match
// (`Nothing`, `[]`, the input unchanged).

// regexMatches compiles `pattern` and returns every match of it in `s`, at
// most `limit` of them (-1 for all). Positions are converted from bytes to
// characters in one left-to-right walk, so the whole call is O(len(s)) plus
// the regex engine's own (linear, RE2) cost.
func regexMatches(pattern string, s string, limit int) ([]map[string]any, [][]int) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil
	}
	locs := re.FindAllStringSubmatchIndex(s, limit)
	out := make([]map[string]any, len(locs))
	byteAt, runeAt := 0, 0
	for i, loc := range locs {
		runeAt += utf8.RuneCountInString(s[byteAt:loc[0]])
		byteAt = loc[0]
		subs := make([]any, 0, len(loc)/2-1)
		for g := 2; g+1 < len(loc); g += 2 {
			if loc[g] < 0 {
				subs = append(subs, Nothing[any]())
			} else {
				subs = append(subs, Just[any](s[loc[g]:loc[g+1]]))
			}
		}
		out[i] = map[string]any{
			"match":      s[loc[0]:loc[1]],
			"index":      runeAt,
			"number":     i + 1,
			"submatches": subs,
		}
	}
	return out, locs
}

// Regex_findMatch is `Regex.findMatch pattern s`: the first match, or Nothing.
func Regex_findMatch(pattern any, s any) any {
	ms, _ := regexMatches(AsString(pattern), AsString(s), 1)
	if len(ms) == 0 {
		return Nothing[any]()
	}
	return Just[any](ms[0])
}

// Regex_findAllMatches is `Regex.findAllMatches pattern s`: every
// non-overlapping match, left to right.
func Regex_findAllMatches(pattern any, s any) any {
	ms, _ := regexMatches(AsString(pattern), AsString(s), -1)
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

// Regex_replaceWith is `Regex.replaceWith pattern f s`: every match replaced
// by `f match`. The text between matches is copied once, so the result is
// built in O(len(s)) plus the cost of `f`.
func Regex_replaceWith(pattern any, fn any, s any) any {
	str := AsString(s)
	ms, locs := regexMatches(AsString(pattern), str, -1)
	if len(ms) == 0 {
		return str
	}
	var b strings.Builder
	b.Grow(len(str))
	prev := 0
	for i, m := range ms {
		b.WriteString(str[prev:locs[i][0]])
		b.WriteString(AsString(SkyCall(fn, m)))
		prev = locs[i][1]
	}
	b.WriteString(str[prev:])
	return b.String()
}
