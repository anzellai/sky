//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A standalone Sky.Live app with SKY_LIVE_BASE_PATH=/app behind a proxy that
// strips the prefix sees unprefixed paths. The page must embed the token of
// the cookie CSRFMiddleware set for that path. Before v0.25.20 the page read
// `__sky_csrf_app` (from the app's basePath) while the middleware set
// `__sky_csrf`: the page carried no token and every event POST was a 403.
func TestCsrfTokenForStandaloneBasePathMatchesTheMiddleware(t *testing.T) {
	var embedded string
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embedded = CurrentCsrfTokenForRequest(r)
	}))
	// First visit: the middleware issues the cookie.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var issued *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, SkyCsrfCookieName) {
			issued = c
		}
	}
	if issued == nil {
		t.Fatalf("the middleware issued no CSRF cookie")
	}
	// Second visit with that cookie: the page embeds the same token.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: issued.Name, Value: issued.Value})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if embedded != issued.Value {
		t.Fatalf("the page embeds %q, the middleware's cookie %s holds %q", embedded, issued.Name, issued.Value)
	}
	// The basePath-derived name (the v0.25.19 read) misses that cookie.
	if got := CurrentCsrfTokenForBasePath(req, "/app"); got == issued.Value {
		t.Fatalf("precondition: the basePath-derived name should differ from the middleware's")
	}
}
