package rt

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// CSRF cookie isolation for in-process sub-apps (app-inside-app).
//
// THE BUG this guards. The SESSION cookie is isolated per app by a distinct
// NAME (sky_<sanitised(basePath)>_sid vs the host's sky_sid — see
// live.go cookieNameOrDefault / subapp_inprocess.go). The CSRF cookie was
// NOT: csrf_middleware.go set the single global name SkyCsrfCookieName
// ("__sky_csrf") at Path=/ for EVERY app, host and sub-app alike. A browser
// keeps ONE cookie per (name, path) in its jar, so a host at "/" and an
// in-process sub-app at "/billing/" both wrote "__sky_csrf" at Path=/ and
// clobbered each other. Whichever app last issued a token owned the cookie:
// after a user visited the sub-app, every host POST submitted the host page's
// embedded token but the jar now held the sub-app's — double-submit mismatch,
// 403 on every host interaction until a manual refresh re-minted the host's.
//
// The fix mirrors the session cookie's isolation: a per-app CSRF cookie NAME
// (__sky_csrf_<sanitised(basePath)> for sub-apps, __sky_csrf for the host),
// so the two never collide in the one jar the browser keeps.
//
// This test drives the REAL CSRFMiddleware wrapping a parent mux (exactly how
// live.go mounts it at :4345) with a REAL cookie jar, so it reproduces the
// browser's single-jar collision end-to-end.
//
// Mutation proof: make csrfCookieNameForPath always return SkyCsrfCookieName
// (i.e. revert the per-app name) and the "host POST after sub-app visit"
// assertion 403s again — proving this test catches the bug.

// withRegisteredSubApp installs `prefix` into the in-process sub-app registry
// for the duration of the test and restores the prior registry after.
func withRegisteredSubApp(t *testing.T, prefix string) {
	t.Helper()
	p := normaliseBasePath(prefix)
	inProcessSubAppsMu.Lock()
	prevMap := inProcessSubApps
	prevSnap := inProcessSubAppRoutes.Load()
	// Copy so we don't mutate the shared map in place.
	cp := make(map[string]*liveApp, len(prevMap)+1)
	for k, v := range prevMap {
		cp[k] = v
	}
	cp[p] = &liveApp{basePath: p}
	inProcessSubApps = cp
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()
	t.Cleanup(func() {
		inProcessSubAppsMu.Lock()
		inProcessSubApps = prevMap
		if prevSnap != nil {
			inProcessSubAppRoutes.Store(prevSnap)
		} else {
			empty := []inProcessSubAppRoute{}
			inProcessSubAppRoutes.Store(&empty)
		}
		inProcessSubAppsMu.Unlock()
	})
}

// cookieByName returns the value of the cookie named exactly `name` in `jar`
// for `u`, or "" if absent.
func cookieByName(jar http.CookieJar, u *url.URL, name string) string {
	for _, c := range jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// subAppCsrfCookie returns the (name, value) of the sub-app CSRF cookie in
// `jar` for `u` — the one whose name is __sky_csrf_<something>, distinct from
// the bare host name. Returns ("","") if none.
func subAppCsrfCookie(jar http.CookieJar, u *url.URL) (string, string) {
	for _, c := range jar.Cookies(u) {
		if c.Name != SkyCsrfCookieName && strings.HasPrefix(c.Name, SkyCsrfCookieName+"_") {
			return c.Name, c.Value
		}
	}
	return "", ""
}

func TestCsrfCookieIsIsolatedForInProcessSubApps(t *testing.T) {
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	defer csrfEnabled.Store(prev)

	withRegisteredSubApp(t, "/billing")

	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("/", ok)
	mux.HandleFunc("/_sky/event", ok)
	mux.HandleFunc("/billing/", ok)
	mux.HandleFunc("/billing/_sky/event", ok)

	srv := httptest.NewServer(CSRFMiddleware(mux))
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}

	hostURL, _ := url.Parse(srv.URL + "/")
	billingURL, _ := url.Parse(srv.URL + "/billing/")

	get := func(path string) {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
	}

	// 1. Host first paint: issues the host CSRF cookie. The host page embeds
	//    whatever CurrentCsrfToken(r) reads — capture it from the jar as the
	//    browser sees it right now, BEFORE the sub-app visit.
	get("/")
	hostToken := cookieByName(jar, hostURL, SkyCsrfCookieName)
	if hostToken == "" {
		t.Fatalf("host GET / did not issue a %q cookie", SkyCsrfCookieName)
	}

	// 2. User visits the sub-app: it issues ITS CSRF cookie. Pre-fix this
	//    overwrote the host's shared "__sky_csrf" at Path=/ in the jar; the
	//    fix gives it a distinct per-app name so both survive.
	get("/billing/")
	subName, subToken := subAppCsrfCookie(jar, billingURL)
	if subToken == "" {
		t.Fatalf("sub-app GET /billing/ did not issue a per-app __sky_csrf_* cookie "+
			"(collision: it shares the host's %q name)", SkyCsrfCookieName)
	}

	// ISOLATION GATE: the host's cookie must STILL be present after the
	// sub-app visit (not clobbered), and the two names must differ.
	if hostStill := cookieByName(jar, hostURL, SkyCsrfCookieName); hostStill == "" {
		t.Fatalf("host CSRF cookie %q was clobbered by the sub-app visit (collision)", SkyCsrfCookieName)
	}
	if subName == SkyCsrfCookieName {
		t.Fatalf("CSRF cookies collide: sub-app shares the host name %q at Path=/ "+
			"(browser keeps one) — expected a per-app name", SkyCsrfCookieName)
	}

	// 3. Back on the host, the page submits the token the HOST embedded
	//    (hostToken). The browser attaches the jar cookie for the host path.
	//    Pre-fix the jar's "__sky_csrf" now holds the sub-app's token, so the
	//    double-submit mismatches → 403. Post-fix the host's own cookie is
	//    still present under its own name → match → 200.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/_sky/event", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SkyCsrfHeaderName, hostToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("host POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host POST after sub-app visit: got %d, want 200 — CSRF cookie "+
			"collision (host token=%s, jar holds the sub-app's under the shared name)",
			resp.StatusCode, hostToken)
	}

	// 4. The sub-app's own CSRF must still work independently.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/billing/_sky/event", strings.NewReader("{}"))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(SkyCsrfHeaderName, subToken)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("sub-app POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("sub-app POST with its own token: got %d, want 200", resp2.StatusCode)
	}
}

// sanitiseBasePathForCookie is not injective: "/billing-v2" and "/billing_v2"
// both sanitise to "billing_v2". Two sub-apps at such prefixes would silently
// share CSRF/session cookie names, sky-id prefixes and telemetry namespaces —
// a cross-app identity merge. The mount must REFUSE the second one loudly
// rather than merge them.
func TestSubAppSanitiserCollisionRefusedAtMount(t *testing.T) {
	// First app occupies the registry at "/billing-v2" (sanitises to
	// "billing_v2"). Registered directly to avoid needing a full Sky cfg.
	withRegisteredSubApp(t, "/billing-v2")

	// Sanity: the two prefixes really do collide under the sanitiser.
	if sanitiseBasePathForCookie("/billing-v2") != sanitiseBasePathForCookie("/billing_v2") {
		t.Fatalf("test premise broken: prefixes no longer collide")
	}

	mux := http.NewServeMux()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected mount to PANIC on a sanitiser collision, got no panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "collides") {
			t.Fatalf("collision panic message unexpected: %v", r)
		}
	}()
	// cfg is never reached — the collision check fires before construction.
	MountLiveSubAppInProcess(mux, "/billing_v2", nil)
}

// A plain (non-sub-app) Sky.Live app's CSRF cookie must be byte-identical to
// the pre-change behaviour: name "__sky_csrf", Path=/. With no sub-app
// registered, csrfCookieNameForPath must resolve to the bare host name for
// every path.
func TestCsrfCookieHostByteIdentical(t *testing.T) {
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	defer csrfEnabled.Store(prev)

	// No sub-apps registered → host name everywhere.
	if got := csrfCookieNameForPath("/"); got != SkyCsrfCookieName {
		t.Fatalf("host path cookie name = %q, want %q", got, SkyCsrfCookieName)
	}
	if got := csrfCookieNameForPath("/anything/_sky/event"); got != SkyCsrfCookieName {
		t.Fatalf("host path cookie name = %q, want %q", got, SkyCsrfCookieName)
	}

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	sc := rr.Header().Get("Set-Cookie")
	if !strings.HasPrefix(sc, SkyCsrfCookieName+"=") {
		t.Fatalf("host CSRF cookie must be exactly %q; got %q", SkyCsrfCookieName, sc)
	}
	if !strings.Contains(sc, "Path=/;") && !strings.HasSuffix(sc, "Path=/") &&
		!strings.Contains(sc, "Path=/ ") {
		// http.SetCookie serialises Path=/ ; assert the host stays Path=/.
		if !strings.Contains(sc, "Path=/") {
			t.Fatalf("host CSRF cookie must be Path=/; got %q", sc)
		}
	}
}
