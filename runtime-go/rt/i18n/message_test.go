package i18n

import (
	"errors"
	"strings"
	"testing"
)

func render(t *testing.T, loc, src string, args Args) string {
	t.Helper()
	m, err := ParseMessage(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	l := MustLocale(loc)
	out, _ := m.Render(l, l, args)
	return out
}

func TestMessageSimpleForms(t *testing.T) {
	cases := []struct {
		src  string
		args Args
		want string
	}{
		{"Hello {name}", Args{"name": {ArgString, "Ada"}}, "Hello Ada"},
		{"Hello {$name}!", Args{"name": {ArgString, "Ada"}}, "Hello Ada!"},
		{"Literal {|name|}", nil, "Literal name"},
		{"Esc \\{ \\} \\\\ \\|", nil, "Esc { } \\ |"},
		{"{count} visits", Args{"count": {ArgInt, "1234"}}, "1,234 visits"},
		{"{$n :number minimumFractionDigits=2}", Args{"n": {ArgInt, "5"}}, "5.00"},
		{"{$n :number maximumFractionDigits=1}", Args{"n": {ArgDecimal, "2.25"}}, "2.2"},
		{"{$n :integer}", Args{"n": {ArgDecimal, "2.5"}}, "2"},
		{"{$n :integer}", Args{"n": {ArgDecimal, "3.5"}}, "4"},
		{"{$n :number useGrouping=never}", Args{"n": {ArgInt, "12345"}}, "12345"},
		{"{$s :string}", Args{"s": {ArgString, "x"}}, "x"},
		{"Total {total}", Args{"total": {ArgMoney, "GBP 1234.5"}}, "Total £1,234.50"},
		{"{p}", Args{"p": {ArgPercent, "0.125"}}, "12.5%"},
		{"{d}", Args{"d": {ArgDate, "2026-11-17"}}, "Nov 17, 2026"},
		{"{xs}", Args{"xs": {ArgList, "a\x1fb\x1fc"}}, "a, b, and c"},
		{"Missing {who}", nil, "Missing {who}"},
		{"Missing {$who}", nil, "Missing {$who}"},
		{"{{Quoted {name}}}", Args{"name": {ArgString, "A"}}, "Quoted A"},
		{".local $x = {$n :number minimumFractionDigits=1}\n{{v={$x}}}", Args{"n": {ArgInt, "3"}}, "v=3.0"},
	}
	for _, c := range cases {
		if got := render(t, "en", c.src, c.args); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMessageMatch(t *testing.T) {
	src := ".input {$count :number}\n.match $count\n0 {{no visits}}\none {{{count} visit left}}\n* {{{$count} visits left}}"
	for n, want := range map[string]string{"0": "no visits", "1": "1 visit left", "2": "2 visits left", "1000": "1,000 visits left"} {
		if got := render(t, "en", src, Args{"count": {ArgInt, n}}); got != want {
			t.Errorf("count %s: got %q, want %q", n, got, want)
		}
	}
	// A missing selector argument takes the catch-all.
	if got := render(t, "en", src, nil); got != "{$count} visits left" {
		t.Errorf("missing: %q", got)
	}
	ord := ".input {$n :number select=ordinal}\n.match $n\none {{{$n}st}}\ntwo {{{$n}nd}}\nfew {{{$n}rd}}\n* {{{$n}th}}"
	for n, want := range map[string]string{"1": "1st", "2": "2nd", "3": "3rd", "4": "4th", "11": "11th", "22": "22nd"} {
		if got := render(t, "en", ord, Args{"n": {ArgInt, n}}); got != want {
			t.Errorf("ordinal %s: got %q, want %q", n, got, want)
		}
	}
	str := ".input {$g :string}\n.match $g\nf {{she}}\nm {{he}}\n* {{they}}"
	if got := render(t, "en", str, Args{"g": {ArgString, "f"}}); got != "she" {
		t.Errorf("string select: %q", got)
	}
	two := ".input {$a :number}\n.input {$b :number}\n.match $a $b\none one {{1-1}}\none * {{1-*}}\n* one {{*-1}}\n* * {{*-*}}"
	if got := render(t, "en", two, Args{"a": {ArgInt, "1"}, "b": {ArgInt, "5"}}); got != "1-*" {
		t.Errorf("two selectors: %q", got)
	}
	// Decimal plural: 1.0 with a fraction digit is "other" in English.
	dec := ".input {$n :number minimumFractionDigits=1}\n.match $n\none {{one}}\n* {{other}}"
	if got := render(t, "en", dec, Args{"n": {ArgInt, "1"}}); got != "other" {
		t.Errorf("decimal plural: %q", got)
	}
}

func TestMessageErrors(t *testing.T) {
	syntax := []string{
		"Hello {name",
		"Hello }",
		"bad \\n escape",
		"{}",
		"{#b}",
		"{$x @attr}",
		".match $n\none {{x}}\n* {{y}}", // no annotation
		".input {$n :number}\n.match $n\none {{x}}",       // no catch-all
		".input {$n :number}\n.match $n\none {{x}}\n* {{", // unterminated
		".input {$n :number}\n.input {$n :number}\n{{x}}", // declared twice
		".unknown {{x}}",
		"{{x}} trailing",
		"{|unterminated}",
	}
	for _, s := range syntax {
		_, err := ParseMessage(s)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("%q: want a syntax error, got %v", s, err)
		}
	}
	funcs := []string{
		"{$d :datetime}",
		"{$n :number style=percent}",
		"{$n :number minimumFractionDigits=99}",
		"{$n :integer maximumFractionDigits=2}",
		"{$n :number select=cardinal}",
	}
	for _, s := range funcs {
		_, err := ParseMessage(s)
		var fe *FunctionError
		if !errors.As(err, &fe) {
			t.Errorf("%q: want a function error, got %v", s, err)
		}
	}
}

func TestMessageRoundTrip(t *testing.T) {
	for _, s := range []string{
		"Hello {$name}, {|lit|} \\{x\\}",
		".input {$count :number}\n.match $count\n0 {{none}}\none {{{$count} one}}\n* {{{$count} many \\| bar}}",
		".local $x = {$n :number minimumFractionDigits=2}\n{{{$x}}}",
	} {
		m, err := ParseMessage(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		again, err := ParseMessage(m.String())
		if err != nil {
			t.Fatalf("re-parse %q: %v", m.String(), err)
		}
		if again.String() != m.String() {
			t.Errorf("round trip: %q then %q", m.String(), again.String())
		}
	}
}

func TestVariables(t *testing.T) {
	m, err := ParseMessage(".local $x = {$n :number}\n.input {$c :number}\n.match $c\none {{{$x} {name}}}\n* {{{$y :number minimumFractionDigits=$d}}}")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Variables(), ","); got != "c,d,n,name,y" {
		t.Errorf("variables: %s", got)
	}
}

func TestPseudo(t *testing.T) {
	m, _ := ParseMessage("Hello {name}")
	got := Pseudo(m).String()
	if !strings.HasPrefix(got, "[Ĥéļļö {$name}") || !strings.HasSuffix(got, "]") {
		t.Errorf("pseudo: %q", got)
	}
}

func TestCatalogueRender(t *testing.T) {
	c, err := NewCatalogue(CatalogueJSON{
		Default: "en", Locales: []string{"en"},
		Messages: map[string]map[string]string{"en": {"hi": "Hi {name}"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	en := MustLocale("en")
	if got := c.Render(nil, MustLocale("en-AU"), "hi", Args{"name": {ArgString, "A"}}); got != "Hi A" {
		t.Errorf("fallback: %q", got)
	}
	SetProduction(false)
	if got := c.Render(nil, en, "nope", nil); got != "⟦nope⟧" {
		t.Errorf("marker: %q", got)
	}
	SetProduction(true)
	if got := c.Render(nil, en, "nope", nil); got != "nope" {
		t.Errorf("production: %q", got)
	}
	SetProduction(false)
	o, err := NewOverrides([][3]string{{"en", "hi", "Welcome {name}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Render(o, en, "hi", Args{"name": {ArgString, "B"}}); got != "Welcome B" {
		t.Errorf("override: %q", got)
	}
	if _, err := NewOverrides([][3]string{{"en", "hi", "Bad {"}}); err == nil {
		t.Error("a bad override must be refused")
	}
	if _, err := NewOverrides([][3]string{{"xx-!!", "hi", "x"}}); err == nil {
		t.Error("a bad override locale must be refused")
	}
}
