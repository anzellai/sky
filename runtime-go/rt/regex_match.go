package rt

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Sky.Core.Regex: a compiled RE2 regular expression.
//
// Since v0.29.0 a `Regex` is a value: `Regex.fromString` compiles a pattern
// once and is the only constructor, and every other function takes the
// compiled value. An invalid pattern is the `Err` of `fromString`, with the
// RE2 error text (`error parsing regexp: missing closing ): ...`); before
// v0.29.0 every function took the pattern as a String and an invalid one
// silently matched nothing. A literal pattern is also checked when the program
// compiles (`[E2014]`, ty/src/re2_syntax.rs).
//
// Representation. A Regex is the stdlib ADT `Regex__Internal` whose one field
// holds the `*regexp.Regexp`, the way `Std.Sync` holds its pointers. Sky code
// never matches on it (the constructor is not exported). A `*regexp.Regexp` is
// safe for concurrent use, so one Regex may be shared by every request.
//
// Matches. `findMatch` / `findAllMatches` / `replaceWith` hand Sky the record
// `{ match : String, index : Int, number : Int, submatches : List (Maybe String) }`
// (a `map[string]any` the call site narrows to `Sky_Core_Regex_Match_R`, as
// every record-returning kernel does). `index` counts characters (Unicode code
// points, the unit of `String.slice`), never bytes. `number` is the 1-based
// position of the match among all matches. A group that did not take part in
// the match is `Nothing`; a group that matched the empty string is `Just ""`.

const regexADTName = "Regex__Internal"

// regexNeverRE matches nothing: a character class that excludes every code
// point.
var regexNeverRE = regexp.MustCompile(`[^\x00-\x{10FFFF}]`)

func regexBox(re *regexp.Regexp) SkyADT {
	return SkyADT{Tag: 0, SkyName: regexADTName, Fields: []any{re}}
}

// regexOf returns the compiled expression a Regex carries. Every Regex is made
// by fromString or never, so another shape cannot reach here from well-typed
// code; it is treated as `never` rather than a panic.
func regexOf(v any) *regexp.Regexp {
	if adt, ok := v.(SkyADT); ok && adt.SkyName == regexADTName && len(adt.Fields) == 1 {
		if re, ok := adt.Fields[0].(*regexp.Regexp); ok && re != nil {
			return re
		}
	}
	if re, ok := v.(*regexp.Regexp); ok && re != nil {
		return re
	}
	return regexNeverRE
}

// Regex_fromString is `Regex.fromString : String -> Result Error Regex`.
func Regex_fromString(pattern any) any {
	re, err := regexp.Compile(AsString(pattern))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Regex.fromString: " + err.Error()))
	}
	return Ok[any, any](regexBox(re))
}

// Regex_fromStringT is the typed twin of Regex_fromString for a proven String
// pattern (lower.rs `kernel_typed_return_twin`). Its result is the Go type the
// Sky `Result Error Regex` lowers to: `Sky_Core_Error_Error` and
// `Sky_Core_Regex_Regex` are both emitted as `= rt.SkyADT`, so the call site
// narrows nothing (doc 14 R5, lever §5.3).
func Regex_fromStringT(pattern string) SkyResult[SkyADT, SkyADT] {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Err[SkyADT, SkyADT](makeErrorT(7, "InvalidInput", "Regex.fromString: "+err.Error()))
	}
	return Ok[SkyADT, SkyADT](regexBox(re))
}

// Regex_never is `Regex.never : Regex`, which matches nothing.
func Regex_never() any { return regexBox(regexNeverRE) }

// Regex_neverT is Regex_never typed as the emitted `Sky_Core_Regex_Regex`
// (`= rt.SkyADT`), used where the value lands in a `Regex` slot.
func Regex_neverT() SkyADT { return regexBox(regexNeverRE) }

// Regex_contains is `Regex.contains re s` (also exported as `Regex.match`).
func Regex_contains(re any, s any) any {
	return regexOf(re).MatchString(AsString(s))
}

// Regex_find is `Regex.find re s`: the first match, or Nothing. An empty
// match is a match (`find "x*" "abc"` is `Just ""`): the location is tested,
// not the matched text.
func Regex_find(re any, s any) any {
	str := AsString(s)
	loc := regexOf(re).FindStringIndex(str)
	if loc == nil {
		return Nothing[any]()
	}
	return Just[any](str[loc[0]:loc[1]])
}

// Regex_findAll is `Regex.findAll re s`: every non-overlapping match.
func Regex_findAll(re any, s any) any {
	matches := regexOf(re).FindAllString(AsString(s), -1)
	result := make([]any, len(matches))
	for i, m := range matches {
		result[i] = m
	}
	return result
}

// Regex_replace is `Regex.replace re replacement s`; `replacement` may name
// groups as `$1` / `${name}` (RE2 substitution syntax).
func Regex_replace(re any, replacement any, s any) any {
	return regexOf(re).ReplaceAllString(AsString(s), AsString(replacement))
}

// Regex_split is `Regex.split re s`.
func Regex_split(re any, s any) any {
	parts := regexOf(re).Split(AsString(s), -1)
	result := make([]any, len(parts))
	for i, p := range parts {
		result[i] = p
	}
	return result
}

// Typed companions, used when the lowering proves the string arguments are
// strings (lower.rs `typed_kernel_twin`). Each agrees with its erased kernel
// (regex_compiled_test.go).
func Regex_containsT(re any, s string) bool { return regexOf(re).MatchString(s) }
func Regex_findT(re any, s string) SkyMaybe[string] {
	loc := regexOf(re).FindStringIndex(s)
	if loc == nil {
		return Nothing[string]()
	}
	return Just[string](s[loc[0]:loc[1]])
}
func Regex_findAllT(re any, s string) []string {
	out := regexOf(re).FindAllString(s, -1)
	if out == nil {
		return []string{}
	}
	return out
}
func Regex_replaceT(re any, replacement, s string) string {
	return regexOf(re).ReplaceAllString(s, replacement)
}
func Regex_splitT(re any, s string) []string { return regexOf(re).Split(s, -1) }

// regexMatches returns every match of `re` in `s`, at most `limit` of them
// (-1 for all). Positions are converted from bytes to characters in one
// left-to-right walk, so the whole call is O(len(s)) plus the regex engine's
// own (linear, RE2) cost.
func regexMatches(re *regexp.Regexp, s string, limit int) ([]map[string]any, [][]int) {
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

// Regex_findMatch is `Regex.findMatch re s`: the first match, or Nothing.
func Regex_findMatch(re any, s any) any {
	ms, _ := regexMatches(regexOf(re), AsString(s), 1)
	if len(ms) == 0 {
		return Nothing[any]()
	}
	return Just[any](ms[0])
}

// Regex_findAllMatches is `Regex.findAllMatches re s`: every non-overlapping
// match, left to right.
func Regex_findAllMatches(re any, s any) any {
	ms, _ := regexMatches(regexOf(re), AsString(s), -1)
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

// Regex_replaceWith is `Regex.replaceWith re f s`: every match replaced by
// `f match`. The text between matches is copied once, so the result is built
// in O(len(s)) plus the cost of `f`.
func Regex_replaceWith(re any, fn any, s any) any {
	str := AsString(s)
	ms, locs := regexMatches(regexOf(re), str, -1)
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
