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

// regexErrorText is the Error's text (its kind and message), read the way
// every platform's runtime prints an Error.
func regexErrorText(e any) string {
	return fmt.Sprintf("%v", e)
}
