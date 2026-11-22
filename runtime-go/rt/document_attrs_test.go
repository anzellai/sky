package rt

import (
	"strings"
	"testing"
)

// docAttrADT builds a Std.Html.Document `Attribute name value` value.
func docAttrADT(name, value string) any {
	return SkyADT{SkyName: "Attribute", Fields: []any{name, value}}
}

func docFn(items ...any) func(any) any {
	return func(any) any { return items }
}

func TestWellFormedLangTag(t *testing.T) {
	good := []string{
		"en", "EN", "en-GB", "fr-CA", "zh-Hant-TW", "sr-Latn-RS", "ar", "es-419",
		"de-CH-1901", "sl-rozaj-biske", "en-US-u-ca-gregory", "x-private",
		"en-x-mine", "i-klingon", "zh-min-nan", "yue", "zh-yue-HK", "hy-Latn-IT-arevela",
	}
	for _, s := range good {
		if !wellFormedLangTag(s) {
			t.Errorf("%q must be a well-formed tag", s)
		}
	}
	bad := []string{
		"", "e", "en-", "-en", "en--GB", "english language", "en_GB", "toolongtag",
		"en-GB-u", "en-a-b-x", "en-a-bb-a-cc", "x", "en-x", `en"`, "en<", "1en",
		"en-ÿ",
	}
	for _, s := range bad {
		if wellFormedLangTag(s) {
			t.Errorf("%q must not be a well-formed tag", s)
		}
	}
}

func TestDocumentAttrsChecksAndLastWins(t *testing.T) {
	got := documentAttrs(docFn(
		docAttrADT("lang", "en"),
		docAttrADT("dir", "rtl"),
		docAttrADT("lang", "not a tag"), // dropped: the earlier lang stays
		docAttrADT("data-theme", "dark"),
		docAttrADT("data-sky-hydrating", "0"), // reserved
		docAttrADT("data-sky", "x"),           // reserved
		docAttrADT("data-Bad", "x"),           // upper case
		docAttrADT("data-", "x"),              // empty name
		docAttrADT("onclick", "alert(1)"),     // not allowed
		docAttrADT("dir", "sideways"),         // not a direction
		docAttrADT("translate", "no"),
		docAttrADT("lang", "fr-CA"), // the last valid lang wins
		docAttrADT("class", "a"),
		docAttrADT("class", "b c"),
	), nil)
	want := []docAttr{
		{"lang", "fr-CA"}, {"dir", "rtl"}, {"data-theme", "dark"},
		{"translate", "no"}, {"class", "b c"},
	}
	if !sameDocAttrs(got, want) {
		t.Fatalf("documentAttrs = %+v, want %+v", got, want)
	}
}

func TestDocumentAttrsNoFunctionAndPanic(t *testing.T) {
	if got := documentAttrs(nil, nil); got != nil {
		t.Fatalf("no function must give nil, got %+v", got)
	}
	if got := documentAttrs(docFn(), nil); got == nil || len(got) != 0 {
		t.Fatalf("an empty list must give an empty, non-nil set, got %#v", got)
	}
	boom := func(any) any { panic("boom") }
	if got := documentAttrs(boom, nil); got != nil {
		t.Fatalf("a panicking function must give nil (keep the old set), got %+v", got)
	}
}

// Every value is HTML-escaped: quotes, <, > and & cannot leave the attribute
// or open a tag, and a newline stays inside the quoted value.
func TestRenderDocAttrsEscapes(t *testing.T) {
	attrs := documentAttrs(docFn(
		docAttrADT("lang", "en"),
		docAttrADT("class", "a\" onload=\"x' <script>&amp;\nb"),
		docAttrADT("data-note", "<b>\"&'"),
	), nil)
	got := renderDocAttrs(attrs)
	want := ` lang="en" class="a&#34; onload=&#34;x&#39; &lt;script&gt;&amp;amp;` + "\n" + `b" data-note="&lt;b&gt;&#34;&amp;&#39;"`
	if got != want {
		t.Fatalf("renderDocAttrs =\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{`"x'`, "<script>", `onload="`} {
		if strings.Contains(got, bad) {
			t.Fatalf("unescaped %q in %s", bad, got)
		}
	}
}

func TestWithDefaultLang(t *testing.T) {
	if got := renderDocAttrs(withDefaultLang([]docAttr{{"dir", "rtl"}})); got != ` lang="en" dir="rtl"` {
		t.Fatalf("no lang must get lang=\"en\": %s", got)
	}
	if got := renderDocAttrs(withDefaultLang([]docAttr{{"lang", "ar"}})); got != ` lang="ar"` {
		t.Fatalf("an app lang must stay: %s", got)
	}
}
