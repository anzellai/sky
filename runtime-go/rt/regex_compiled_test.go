package rt

import (
	"fmt"
	"strings"
	"testing"
)

// v0.29.0: a Regex is compiled once by Regex.fromString (the only
// constructor), and every Regex function takes the compiled value. An invalid
// pattern is an Err that carries the RE2 error text, never a silent "no
// match".

func mustRegex(t *testing.T, p string) any {
	t.Helper()
	r := Regex_fromString(p)
	res, ok := r.(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("Regex.fromString %q: want Ok, got %#v", p, r)
	}
	return res.OkValue
}

func TestRegexFromStringInvalidIsAnErrWithTheRE2Text(t *testing.T) {
	for _, c := range []struct{ pattern, want string }{
		{"(", "missing closing )"},
		{"[a", "missing closing ]"},
		{"*", "missing argument to repetition operator"},
		{`\8`, "invalid escape sequence"},
		{"a{3,2}", "invalid repeat count"},
	} {
		r, ok := Regex_fromString(c.pattern).(SkyResult[any, any])
		if !ok || r.Tag != 1 {
			t.Fatalf("Regex.fromString %q: want Err, got %#v", c.pattern, r)
		}
		msg := regexErrorText(r.ErrValue)
		if !strings.Contains(msg, c.want) || !strings.Contains(msg, "Regex.fromString") {
			t.Fatalf("Regex.fromString %q: message %q does not name %q", c.pattern, msg, c.want)
		}
		if kind := errorKindName(r.ErrValue); kind != "InvalidInput" {
			t.Fatalf("Regex.fromString %q: kind %q, want InvalidInput", c.pattern, kind)
		}
	}
}

func TestRegexFunctionsTakeACompiledRegex(t *testing.T) {
	ws := mustRegex(t, `\s+`)
	if got := Regex_split(ws, "a  b\tc"); len(asList(got)) != 3 {
		t.Fatalf("split on \\s+: %#v", got)
	}
	word := mustRegex(t, `\b\w+\b`)
	if got := asList(Regex_findAll(word, "one two  three")); len(got) != 3 || got[2] != "three" {
		t.Fatalf("findAll \\b\\w+\\b: %#v", got)
	}
	year := mustRegex(t, `\d{2,4}`)
	if !AsBool(Regex_contains(year, "in 1999")) || AsBool(Regex_contains(year, "in 9")) {
		t.Fatalf("contains \\d{2,4} is wrong")
	}
	if got := AsString(Regex_replace(mustRegex(t, `(a)(b)`), "$2$1", "xaby")); got != "xbay" {
		t.Fatalf("replace with groups: %q", got)
	}
	empty, _ := Regex_find(mustRegex(t, "x*"), "abc").(SkyMaybe[any])
	if empty.Tag != 0 || empty.JustValue != "" {
		t.Fatalf(`find "x*" "abc" must be Just "", got %#v`, empty)
	}
	m, _ := Regex_findMatch(mustRegex(t, `(\w+)=(\d+)`), "x a=12").(SkyMaybe[any])
	if m.Tag != 0 || m.JustValue.(map[string]any)["index"] != 2 {
		t.Fatalf("findMatch: %#v", m)
	}
	if got := len(asList(Regex_findAllMatches(mustRegex(t, `\d`), "1a2b3"))); got != 3 {
		t.Fatalf("findAllMatches: %d", got)
	}
	up := Regex_replaceWith(mustRegex(t, `\d+`), func(x any) any { return "<" + AsString(x.(map[string]any)["match"]) + ">" }, "a1b22")
	if AsString(up) != "a<1>b<22>" {
		t.Fatalf("replaceWith: %q", AsString(up))
	}
}

func TestRegexNeverMatchesNothing(t *testing.T) {
	n := Regex_never()
	for _, s := range []string{"", "a", "\n", "é", "\x00"} {
		if AsBool(Regex_contains(n, s)) {
			t.Fatalf("Regex.never matched %q", s)
		}
	}
	if got := asList(Regex_split(n, "a,b")); len(got) != 1 || got[0] != "a,b" {
		t.Fatalf("split on never: %#v", got)
	}
}

// The typed twins the lowering calls for proven-string arguments agree with
// the erased kernels.
func TestRegexTypedTwinsAgree(t *testing.T) {
	re := mustRegex(t, `[0-9]+`)
	for _, s := range []string{"", "a1", "abc", "42"} {
		if Regex_containsT(re, s) != AsBool(Regex_contains(re, s)) {
			t.Fatalf("containsT %q", s)
		}
		if Regex_replaceT(re, "#", s) != AsString(Regex_replace(re, "#", s)) {
			t.Fatalf("replaceT %q", s)
		}
	}
}

// The typed build twins (v0.29.0) agree with the erased kernels:
// `Regex_fromStringT` returns the same Ok / Err as `Regex_fromString` as a
// typed `SkyResult[SkyADT, SkyADT]` (the emitted
// `rt.SkyResult[Sky_Core_Error_Error, Sky_Core_Regex_Regex]`), `Regex_neverT`
// is `Regex.never`, and `findT` / `findAllT` / `splitT` agree on every input.
func TestRegexBuildAndReadTwinsAgree(t *testing.T) {
	for _, p := range []string{`[0-9]+`, `\s*`, "(", "a{3,2}"} {
		typed := Regex_fromStringT(p)
		erased, _ := Regex_fromString(p).(SkyResult[any, any])
		if typed.Tag != erased.Tag {
			t.Fatalf("fromStringT %q: tag %d, erased %d", p, typed.Tag, erased.Tag)
		}
		if typed.Tag == 1 {
			if regexErrorText(typed.ErrValue) != regexErrorText(erased.ErrValue) {
				t.Fatalf("fromStringT %q: %v, erased %v", p, typed.ErrValue, erased.ErrValue)
			}
			continue
		}
		for _, s := range []string{"", "a1", "abc", "42 7", " x "} {
			if Regex_containsT(typed.OkValue, s) != AsBool(Regex_contains(erased.OkValue, s)) {
				t.Fatalf("fromStringT %q: contains %q differs", p, s)
			}
		}
	}
	for _, s := range []string{"", "a", "\x00"} {
		if Regex_containsT(Regex_neverT(), s) {
			t.Fatalf("Regex_neverT matched %q", s)
		}
	}
	re := mustRegex(t, `[0-9]+`)
	for _, s := range []string{"", "a1", "abc", "42 7", "1,2,3"} {
		ft := Regex_findT(re, s)
		fe, _ := Regex_find(re, s).(SkyMaybe[any])
		if ft.Tag != fe.Tag || (ft.Tag == 0 && ft.JustValue != fe.JustValue) {
			t.Fatalf("findT %q: %#v, erased %#v", s, ft, fe)
		}
		if fmt.Sprint(Regex_findAllT(re, s)) != fmt.Sprint(asList(Regex_findAll(re, s))) {
			t.Fatalf("findAllT %q", s)
		}
		if fmt.Sprint(Regex_splitT(re, s)) != fmt.Sprint(asList(Regex_split(re, s))) {
			t.Fatalf("splitT %q", s)
		}
	}
}

// regexErrorText is the Error's text (its kind and message), read the way
// every platform's runtime prints an Error.
func regexErrorText(e any) string {
	return fmt.Sprintf("%v", e)
}
