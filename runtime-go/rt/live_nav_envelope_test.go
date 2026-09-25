package rt

// Sky.Live full-page patch envelope — the dev "Console" badge regression.
//
// THE DEFECT: a sky-nav click (and popstate, and the full-HTML fallback) fetches
// the WHOLE page and hands it to the client's __skyPatch, which strips the
// document envelope with a regex that expects the rendered body to be followed
// IMMEDIATELY by the runtime script: `<div id="sky-root">BODY</div><script>`.
// In development the page carried the dev Console badge BETWEEN the two
// (`</div><a id="__sky-dev-console" …>…</a><script>`), so the regex never
// matched and __skyPatch spliced the ENTIRE document into #sky-root: a second
// Console badge (inside the root, next to the fixed one), the whole runtime as an
// inline <script> (which script revival then rejected with a console warning on
// every sky-nav), and a nested <head>/<style>. Production pages carry no badge,
// so only development showed it.
//
// These tests run the client's OWN extraction regex (read out of the page the
// server actually serves) against that page.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// patchEnvelopeRegexp returns the regex __skyPatch uses to strip the document
// envelope, taken verbatim from the served page's JS and translated to Go syntax.
func patchEnvelopeRegexp(t *testing.T, page string) *regexp.Regexp {
	t.Helper()
	// The client is the external /_sky/live.<hash>.js (live_client_asset.go);
	// the page must load exactly that file.
	if !strings.Contains(page, `<script src="`+liveClientPath+`"></script>`) {
		t.Fatal("served page does not load the Live client")
	}
	page = liveClientJS
	fn := strings.Index(page, "function __skyPatch(t) {")
	if fn < 0 {
		t.Fatal("the Live client has no __skyPatch function")
	}
	const open = "var m = t.match(/"
	i := strings.Index(page[fn:], open)
	if i < 0 {
		t.Fatal("__skyPatch no longer strips the envelope with `var m = t.match(/…/)`; update this test")
	}
	start := fn + i + len(open)
	end := strings.Index(page[start:], "/);")
	if end < 0 {
		t.Fatal("unterminated envelope regex literal in __skyPatch")
	}
	lit := page[start : start+end]
	// JS regex literal → Go RE2: the only JS-only escape used is `\/`.
	re, err := regexp.Compile(strings.ReplaceAll(lit, `\/`, `/`))
	if err != nil {
		t.Fatalf("envelope regex %q does not compile as RE2: %v", lit, err)
	}
	return re
}

func servedPage(t *testing.T, app *liveApp) string {
	t.Helper()
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET /: status %d", rr.Code)
	}
	return rr.Body.String()
}

func assertEnvelopeStripsToBody(t *testing.T, page, wantInBody string) {
	t.Helper()
	m := patchEnvelopeRegexp(t, page).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("__skyPatch's envelope regex does not match the served page, so a sky-nav "+
			"would splice the WHOLE document into #sky-root:\n%.600s", page)
	}
	body := m[1]
	if !strings.Contains(body, wantInBody) {
		t.Fatalf("extracted body lost the rendered view (want %q):\n%s", wantInBody, body)
	}
	if strings.Contains(body, "__sky-dev-console") {
		t.Fatalf("extracted body contains the dev Console badge — it would render twice:\n%s", body)
	}
	if strings.Contains(body, "sky-live-cfg") || strings.Contains(body, liveClientPath) {
		t.Fatalf("extracted body contains the runtime script:\n%.600s", body)
	}
}

// TestNavEnvelopeStripsDevBadgePage — the development page (Console badge on).
func TestNavEnvelopeStripsDevBadgePage(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("SKY_ENV", "")
	t.Setenv("SKY_DEV_BANNER", "")
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	page := servedPage(t, app)
	// Non-vacuity: the badge the defect needs must be on the page.
	if !strings.Contains(page, `id="__sky-dev-console"`) {
		t.Fatal("dev Console badge absent from the development page; the test is vacuous")
	}
	assertEnvelopeStripsToBody(t, page, "<button")
}

// TestNavEnvelopeStripsProductionPage — the production page (no badge).
func TestNavEnvelopeStripsProductionPage(t *testing.T) {
	t.Setenv("ENV", "production")
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	page := servedPage(t, app)
	if strings.Contains(page, `id="__sky-dev-console"`) {
		t.Fatal("production page must not carry the dev Console badge")
	}
	assertEnvelopeStripsToBody(t, page, "<button")
}
