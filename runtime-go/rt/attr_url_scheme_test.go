package rt

import "testing"

// A URL-bearing attribute reached the page with its scheme unexamined, so
// `Std.Markdown.render "[x](javascript:alert(1))"` produced
// `<a href="javascript:alert(1)">x</a>` — against that module's own docstring,
// which says it is "Safe to feed UNTRUSTED markdown … No bluemonday-equivalent
// sanitiser needed."
//
// Found by giving Family S a way to reach `Std.Markdown` at all (it renders an
// `Element`, so a value assertion needed a fold over the tree). Counting `Raw`
// nodes proved the module never emits raw HTML — which was true, and not
// sufficient: the payload here needs no metacharacter, so HTML-escaping, which
// is all `Std.Ui` had, leaves it intact.
//
// Every case below fails on the pre-fix runtime.

func TestSafeAttrURLBlocksExecutableSchemes(t *testing.T) {
	blocked := []struct{ name, in string }{
		{"plain", "javascript:alert(1)"},
		// A browser lowercases the scheme before resolving it.
		{"mixed_case", "JaVaScRiPt:alert(1)"},
		{"upper", "JAVASCRIPT:alert(1)"},
		// …and strips leading whitespace, and ASCII control characters
		// ANYWHERE inside the scheme. All three of these navigate.
		{"leading_space", "  javascript:alert(1)"},
		{"leading_newline", "\njavascript:alert(1)"},
		{"embedded_tab", "java\tscript:alert(1)"},
		{"embedded_newline", "java\nscript:alert(1)"},
		{"embedded_nul", "java\x00script:alert(1)"},
		{"vbscript", "vbscript:msgbox(1)"},
		// A `data:` document runs script in the page's origin.
		{"data_html", "data:text/html,<script>alert(1)</script>"},
		{"data_html_base64", "data:text/html;base64,PHNjcmlwdD4="},
		// No MIME type at all is not an image either.
		{"data_bare", "data:,hello"},
	}
	for _, c := range blocked {
		t.Run(c.name, func(t *testing.T) {
			if got := SafeAttrURL("href", c.in); got != "about:blank" {
				t.Fatalf("SafeAttrURL(href, %q) = %q, want about:blank", c.in, got)
			}
		})
	}
}

// The other half, and it is the half that stops this from being a blanket
// "block every URL" that would pass the test above while breaking every app.
func TestSafeAttrURLLeavesOrdinaryURLsAlone(t *testing.T) {
	kept := []string{
		"https://example.com/p?q=1#f",
		"http://example.com",
		"/relative/path",
		"relative/path",
		"#fragment",
		"?q=1",
		"mailto:a@example.com",
		"tel:+15551234",
		"blob:https://example.com/abc",
		// An inline image is inert and is exactly what `data:` is for.
		"data:image/png;base64,iVBORw0KGgo=",
		"data:image/svg+xml;utf8,<svg/>",
		// A custom app scheme.
		"myapp://open/thing",
		// Empty, and a value that merely CONTAINS the word.
		"",
		"/docs/javascript:notascheme",
		"https://example.com/javascript:alert(1)",
	}
	for _, in := range kept {
		if got := SafeAttrURL("href", in); got != in {
			t.Fatalf("SafeAttrURL(href, %q) = %q, want it unchanged", in, got)
		}
	}
}

// Only URL-BEARING attributes are touched. A `title` or a `value` that happens
// to read `javascript:…` is ordinary text and must survive — rewriting it would
// corrupt data to no security benefit, since a browser never resolves it.
func TestSafeAttrURLOnlyTouchesURLBearingAttributes(t *testing.T) {
	payload := "javascript:alert(1)"
	for _, k := range []string{"title", "value", "alt", "class", "id", "data-note"} {
		if got := SafeAttrURL(k, payload); got != payload {
			t.Fatalf("SafeAttrURL(%q, …) rewrote a non-URL attribute to %q", k, got)
		}
	}
	// …and every attribute a browser DOES resolve as a URL is covered, not just
	// `href`. `formaction` and `xlink:href` are the two that get forgotten.
	for _, k := range []string{"href", "src", "action", "formaction", "xlink:href", "poster", "data"} {
		if got := SafeAttrURL(k, payload); got != "about:blank" {
			t.Fatalf("SafeAttrURL(%q, …) = %q — this attribute is URL-bearing and is not guarded", k, got)
		}
	}
}

// The guard must be IDEMPOTENT: it runs on the VNode-build path and again on
// `Html.attrToString`, and a value that has already been neutralised must not
// be neutralised into something else.
func TestSafeAttrURLIsIdempotent(t *testing.T) {
	once := SafeAttrURL("href", "javascript:alert(1)")
	if twice := SafeAttrURL("href", once); twice != once {
		t.Fatalf("SafeAttrURL is not idempotent: %q then %q", once, twice)
	}
}
