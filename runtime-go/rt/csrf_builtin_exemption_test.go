package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The runtime's own CSRF exemptions are keyed by METHOD + path, like the user
// exemptions (csrfExemption). Before v0.27 they were path-only: every method
// on /_sky/console/* and on /_sky/observability/ingest skipped the check. The
// inline console is a Sky.Live sub-app mounted at /_sky/console, so its
// state-changing POSTs (/_sky/console/_sky/event, /_sky/console/_sky/rotate)
// ran with no CSRF check at all for a signed-in admin's cookie.

func csrfOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func withCsrfOn(t *testing.T) {
	t.Helper()
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	t.Cleanup(func() { csrfEnabled.Store(prev) })
}

func serveBuiltinCsrf(h http.Handler, method, path string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCsrfBuiltinExemptions_AreKeyedByMethod(t *testing.T) {
	withCsrfOn(t)
	withRegisteredSubApp(t, "/_sky/console")
	h := CSRFMiddleware(csrfOKHandler())

	// Exempt method + path: pass with no token.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/_sky/console/_login"},
		{http.MethodPost, "/_sky/console/_logout"},
		{http.MethodGet, "/_sky/console/_logout"},
		{http.MethodPost, "/_sky/observability/ingest"},
		{http.MethodGet, "/_sky/console/api/overview"},
		{http.MethodGet, "/_sky/healthz"},
	} {
		if rr := serveBuiltinCsrf(h, c.method, c.path, nil); rr.Code != http.StatusOK {
			t.Errorf("%s %s is exempt, got %d: %s", c.method, c.path, rr.Code, rr.Body.String())
		}
	}

	// A non-exempt method on the same paths, and every state-changing request
	// of the console sub-app, is checked: no token, 403.
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/_sky/console/_login"},
		{http.MethodDelete, "/_sky/console/_logout"},
		{http.MethodPut, "/_sky/observability/ingest"},
		{http.MethodDelete, "/_sky/observability/ingest"},
		{http.MethodPost, "/_sky/console/api/overview"},
		{http.MethodDelete, "/_sky/console/api/logs"},
		{http.MethodPost, "/_sky/console/_sky/event"},
		{http.MethodPost, "/_sky/console/_sky/rotate"},
		{http.MethodPost, "/_sky/console/"},
		{http.MethodPost, "/_sky/healthz"},
		{http.MethodPost, "/_sky/metrics"},
	} {
		rr := serveBuiltinCsrf(h, c.method, c.path, nil)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s must be CSRF-checked (403 with no token), got %d", c.method, c.path, rr.Code)
		}
	}
}

// The console sub-app's own page issues its CSRF cookie, and an event POST
// that carries it passes: removing the blanket exemption does not break the
// console, it makes the console use the per-app token every sub-app has.
func TestCsrfConsoleSubApp_UsesItsOwnToken(t *testing.T) {
	withCsrfOn(t)
	withRegisteredSubApp(t, "/_sky/console")
	h := CSRFMiddleware(csrfOKHandler())

	page := serveBuiltinCsrf(h, http.MethodGet, "/_sky/console/", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("console page GET: %d", page.Code)
	}
	name := csrfCookieNameForBasePath("/_sky/console")
	var tok string
	for _, c := range page.Result().Cookies() {
		if c.Name == name {
			tok = c.Value
		}
	}
	if tok == "" {
		t.Fatalf("the console page must issue its CSRF cookie %q; got %v", name, page.Header()["Set-Cookie"])
	}
	cookie := &http.Cookie{Name: name, Value: tok}
	ok := serveBuiltinCsrf(h, http.MethodPost, "/_sky/console/_sky/event",
		map[string]string{SkyCsrfHeaderName: tok}, cookie)
	if ok.Code != http.StatusOK {
		t.Fatalf("an event POST with the console's token must pass, got %d: %s", ok.Code, ok.Body.String())
	}
	bad := serveBuiltinCsrf(h, http.MethodPost, "/_sky/console/_sky/event",
		map[string]string{SkyCsrfHeaderName: "forged"}, cookie)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("an event POST with a wrong token must be refused, got %d", bad.Code)
	}
}

// A health probe gets no cookie: the GET exemption on the probe paths stays.
func TestCsrfBuiltinExemptions_ProbeGetsNoCookie(t *testing.T) {
	withCsrfOn(t)
	h := CSRFMiddleware(csrfOKHandler())
	for _, p := range []string{"/_sky/healthz", "/_sky/readyz", "/_sky/metrics", "/_sky/buildinfo", "/_sky/console/api/logs"} {
		rr := serveBuiltinCsrf(h, http.MethodGet, p, nil)
		if sc := rr.Header().Get("Set-Cookie"); strings.Contains(sc, SkyCsrfCookieName) {
			t.Errorf("GET %s must not issue a CSRF cookie, got %q", p, sc)
		}
	}
}

// In header session mode the host's defence is the X-Sky-Session header, but
// the console sub-app keeps the cookie transport, so its paths get the
// double-submit check (they used to be exempt in both modes).
func TestCsrfHeaderMode_ConsoleSubAppIsChecked(t *testing.T) {
	withCsrfOn(t)
	withRegisteredSubApp(t, "/_sky/console")
	h := headerSessionCSRF(csrfOKHandler())

	if rr := serveBuiltinCsrf(h, http.MethodPost, "/_sky/console/_sky/event", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("header mode: a console event POST with no token must be refused, got %d", rr.Code)
	}
	if rr := serveBuiltinCsrf(h, http.MethodPost, "/_sky/console/_login", nil); rr.Code != http.StatusOK {
		t.Fatalf("header mode: the console login POST stays exempt, got %d", rr.Code)
	}
	if rr := serveBuiltinCsrf(h, http.MethodPut, "/_sky/observability/ingest", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("header mode: PUT on the ingest path must be refused, got %d", rr.Code)
	}
	name := csrfCookieNameForBasePath("/_sky/console")
	page := serveBuiltinCsrf(h, http.MethodGet, "/_sky/console/", nil)
	var tok string
	for _, c := range page.Result().Cookies() {
		if c.Name == name {
			tok = c.Value
		}
	}
	if tok == "" {
		t.Fatalf("header mode: the console page must still issue its own CSRF cookie")
	}
	ok := serveBuiltinCsrf(h, http.MethodPost, "/_sky/console/_sky/event",
		map[string]string{SkyCsrfHeaderName: tok}, &http.Cookie{Name: name, Value: tok})
	if ok.Code != http.StatusOK {
		t.Fatalf("header mode: a console event POST with its token must pass, got %d: %s", ok.Code, ok.Body.String())
	}
}
