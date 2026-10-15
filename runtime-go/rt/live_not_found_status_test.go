package rt

// v0.27.9: an unknown path rendered the app's NotFound page with HTTP 200 (a
// "soft 404"). A crawler indexed it as a real page and an uptime probe on a
// path the app does not declare read it as healthy. The page is now sent with
// 404, with the same body: it still boots the Sky.Live client and starts a
// session, so the user can navigate away from it. Declared routes stay 200.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func liveGet(app *liveApp, method, path string, hdr map[string]string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)
	return rr
}

func TestLiveUnknownPathIs404WithTheNotFoundPage(t *testing.T) {
	app := namespaceTestApp()

	rr := liveGet(app, http.MethodGet, "/no/such/page", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /no/such/page: status %d, want 404", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, notFoundMarker) {
		t.Fatalf("GET /no/such/page: the 404 does not carry the app's NotFound page:\n%s", truncate(body, 400))
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /no/such/page: Content-Type %q, want text/html", ct)
	}
	// The page still boots the client: the runtime script, its config block
	// and the render id, so the user can navigate away from it.
	for _, want := range []string{liveClientPath, `id="sky-live-cfg"`, `id="sky-root"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /no/such/page: the 404 page does not boot the client (no %q):\n%s", want, truncate(body, 600))
		}
	}
	if rr.Header().Get("X-Sky-View") == "" {
		t.Fatal("GET /no/such/page: no X-Sky-View render id on the 404 page")
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("GET /no/such/page: the runtime security headers are missing on the 404 page")
	}
	// A session was started for the page, so its SSE stream can attach.
	var sid *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if strings.Contains(c.Name, "sky_sid") {
			sid = c
		}
	}
	if sid == nil {
		t.Fatalf("GET /no/such/page: no session cookie on the 404 page (cookies %v)", rr.Result().Cookies())
	}
	if sess, ok := app.store.Get(sid.Value); !ok || sess == nil || sess.model == nil {
		t.Fatal("GET /no/such/page: the 404 page has no live session in the store")
	}

	// The same session on a declared route: 200, the route's page.
	home := liveGet(app, http.MethodGet, "/", nil, []*http.Cookie{sid})
	if home.Code != http.StatusOK || strings.Contains(home.Body.String(), notFoundMarker) {
		t.Fatalf("GET /: status %d (want 200), NotFound page rendered: %v", home.Code, strings.Contains(home.Body.String(), notFoundMarker))
	}

	// An existing session on an unknown path, and a sky-nav fetch of one:
	// 404 with the page and its render id, which the client patches in.
	again := liveGet(app, http.MethodGet, "/still/missing", map[string]string{"X-Sky-Nav": "1", "X-Sky-Tab": "tab1"}, []*http.Cookie{sid})
	if again.Code != http.StatusNotFound || !strings.Contains(again.Body.String(), notFoundMarker) || again.Header().Get("X-Sky-View") == "" {
		t.Fatalf("sky-nav GET /still/missing: status %d, NotFound page %v, X-Sky-View %q; want 404 with the page and a render id",
			again.Code, strings.Contains(again.Body.String(), notFoundMarker), again.Header().Get("X-Sky-View"))
	}

	// HEAD answers the same status.
	if head := liveGet(app, http.MethodHead, "/no/such/page", nil, nil); head.Code != http.StatusNotFound {
		t.Fatalf("HEAD /no/such/page: status %d, want 404", head.Code)
	}

	// SKY_CSP=strict reaches the 404 page like every page.
	t.Setenv("SKY_CSP", "strict")
	csp := liveGet(app, http.MethodGet, "/no/such/page", nil, nil)
	if csp.Code != http.StatusNotFound {
		t.Fatalf("GET /no/such/page under SKY_CSP=strict: status %d, want 404", csp.Code)
	}
}

// An app with no NotFound page keeps what it did: a fresh session on an
// unknown path renders init's page (200), an existing one gets a plain 404.
func TestLiveUnknownPathWithoutNotFoundPageIsUnchanged(t *testing.T) {
	app := namespaceTestApp()
	app.notFound = nil
	rr := liveGet(app, http.MethodGet, "/no/such/page", nil, nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), notFoundMarker) {
		t.Fatalf("GET /no/such/page with no NotFound page: status %d, want 200 with init's page", rr.Code)
	}
}
