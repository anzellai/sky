package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issue #328: the base reset must not override an app's own CSS. It sits in
// the `sky-reset` cascade layer, and every page shell writes it as the FIRST
// stylesheet in <head> so that layer is declared first and ranks lowest. The
// default font sits on <html>, not <body>, so a design's <html> typography
// inherits.

// resetLayerBody returns the text inside `@layer sky-reset{...}`.
func resetLayerBody(t *testing.T) string {
	t.Helper()
	const open = `@layer sky-reset{`
	if !strings.HasPrefix(liveBaseCSS, open) {
		t.Fatalf("liveBaseCSS must open with %q so the layer is declared first:\n%s", open, liveBaseCSS)
	}
	depth := 0
	for i := len(open) - 1; i < len(liveBaseCSS); i++ {
		switch liveBaseCSS[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return liveBaseCSS[len(open):i]
			}
		}
	}
	t.Fatalf("unterminated sky-reset layer:\n%s", liveBaseCSS)
	return ""
}

func TestBaseResetIsLayered(t *testing.T) {
	layer := resetLayerBody(t)
	outside := strings.Replace(liveBaseCSS, `@layer sky-reset{`+layer+`}`, "", 1)

	// Every visual reset rule is inside the layer.
	for _, rule := range []string{
		`h1,h2,h3,h4,h5,h6,p,ul,ol,li,figure,blockquote,pre,dl,dd{`,
		`button{background:none;border:0;padding:0;`,
		`button,input,select,textarea{font:inherit;`,
		`a{color:inherit;text-decoration:none}`,
		`img,video,canvas,svg{`,
		`*,*::before,*::after{box-sizing:border-box}`,
		`html,body{margin:0;padding:0}`,
	} {
		if !strings.Contains(layer, rule) {
			t.Errorf("visual reset rule %q must be inside @layer sky-reset", rule)
		}
		if strings.Contains(outside, rule) {
			t.Errorf("visual reset rule %q must not also appear unlayered", rule)
		}
	}

	// The default font is on html inside the layer, never on body anywhere:
	// an explicit body declaration blocks inheritance from a design's html.
	if !strings.Contains(layer, `html{font-family:`) || !strings.Contains(layer, `line-height:1.4`) {
		t.Errorf("default font-family and line-height must be set on html inside the layer:\n%s", layer)
	}
	for _, part := range strings.Split(liveBaseCSS, "}") {
		sel, decl, ok := strings.Cut(part, "{")
		if !ok {
			continue
		}
		sel = strings.TrimPrefix(sel, "@layer sky-reset{")
		if strings.Contains(sel, "body") && (strings.Contains(decl, "font") || strings.Contains(decl, "line-height")) {
			t.Errorf("body must carry no font or line-height (blocks inheritance from html): %q{%s}", sel, decl)
		}
	}

	// Runtime layout and behaviour stay unlayered so an app layer cannot drop them.
	for _, rule := range []string{
		`body{min-height:100vh;display:flex;flex-direction:column}`,
		`#sky-root,#app{display:flex;flex-direction:column;flex:1 0 auto;min-height:0}`,
		`html[data-sky-hydrating]::after{`,
	} {
		if !strings.Contains(outside, rule) {
			t.Errorf("runtime rule %q must stay outside the layer", rule)
		}
	}
}

// assertResetFirstInHead fails unless the first <style or <link in <head> is the
// base reset, and the reset precedes the marker the app's head content carries.
func assertResetFirstInHead(t *testing.T, page, appMarker string) {
	t.Helper()
	head, _, ok := strings.Cut(page, "</head>")
	if !ok {
		t.Fatalf("page has no </head>:\n%s", page)
	}
	reset := strings.Index(head, liveBaseStyleTag)
	if reset < 0 {
		t.Fatalf("head must carry the base reset tag (id=%q):\n%s", liveBaseResetID, head)
	}
	if n := strings.Count(page, `@layer sky-reset{`); n != 1 {
		t.Fatalf("the reset must appear exactly once, got %d", n)
	}
	for _, tag := range []string{"<style", "<link"} {
		if i := strings.Index(head, tag); i >= 0 && i < reset {
			t.Fatalf("a %s precedes the base reset, so sky-reset would not be the lowest layer:\n%s", tag, head)
		}
	}
	if appMarker != "" {
		app := strings.Index(head, appMarker)
		if app < 0 {
			t.Fatalf("app head content %q missing:\n%s", appMarker, head)
		}
		if app < reset {
			t.Fatalf("app head content precedes the base reset:\n%s", head)
		}
	}
}

func TestBaseResetFirstOnLivePage(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	const appCSS = `<style>@layer design{h2{font-size:1.5rem}}</style>`
	app.head = func(any) any {
		return []any{VNode{Kind: "raw", Text: appCSS}}
	}
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	assertResetFirstInHead(t, rr.Body.String(), appCSS)
}

func TestBaseResetFirstOnSpaSSRPage(t *testing.T) {
	const appCSS = `<link rel="stylesheet" href="/design.css">`
	page := SpaSSRPage(appCSS, "<p>hi</p>", "main.wasm", "{}")
	assertResetFirstInHead(t, page, appCSS)
}

func TestBaseResetFirstOnSpaSSRPageWithScrolls(t *testing.T) {
	const appCSS = `<link rel="stylesheet" href="/design.css">`
	wire := Spa_collectScrolls(scrollTest("top", false, false))
	page := Spa_ssrPageSeededScrolls(appCSS, "<p>hi</p>", "main.wasm", "{}", true, false, []string{}, wire)
	assertResetFirstInHead(t, page, appCSS)
}
