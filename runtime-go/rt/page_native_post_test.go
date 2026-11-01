//go:build !js

package rt

// A form submitted before a TEA page is live (v0.29.1).
//
// A Sky.Spa page is server-rendered; until the wasm client hydrates, Enter in
// a field (or a phone's "Go" key, or a password manager) submits the form
// natively: a POST of the fields to the page's own URL. The split mounted its
// pages with `Server.api "GET …"`, which skips the CSRF middleware, so the
// page had no CSRF cookie and its forms no `__sky_csrf` field, and the POST
// ended on the CSRF 403 JSON page. Sky.Live answered the same POST the same
// way before its client script ran.
//
// Now a Spa page is Spa_pageRoute: a CSRF-checked GET whose forms carry the
// field, and a native form POST to a page answers 303 to the same URL, never
// the JSON 403. An App.api route, a Server.rpc route and a JSON POST are
// unchanged.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const pageFormHTML = `<!DOCTYPE html><html><body>` +
	`<form sky-id="r.0#form" sky-submit="_" method="post"><input sky-id="r.0#form.0#input" name="name"></form>` +
	`</body></html>`

func pageHandler(body string) any {
	return func(req SkyRequest) any {
		return func() any {
			return Ok[any, any](SkyResponse{Status: 200, Body: body, ContentType: "text/html"})
		}
	}
}

func textHandler(prefix string) any {
	return func(req SkyRequest) any {
		return func() any {
			return Ok[any, any](SkyResponse{Status: 200, Body: prefix + req.Body, ContentType: "text/plain"})
		}
	}
}

// pageTestServer is the Sky.Http.Server listener chain over the routes that
// build makes. The CSRF state is reset first: a Server.api route registers its
// exemption when it is made.
func pageTestServer(t *testing.T, build func() []any) http.Handler {
	t.Helper()
	resetCsrf(t)
	mux, rootFiles := serverRouteMux(build())
	return serverListenerHandler(mux, rootFiles, "")
}

func formPost(target string, form url.Values, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://app.example"+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func csrfCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == SkyCsrfCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", SkyCsrfCookieName, rec.Header().Values("Set-Cookie"))
	return nil
}

func wantSeeOther(t *testing.T, what string, rec *httptest.ResponseRecorder, location string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%s: status %d, body %q: want 303", what, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != location {
		t.Fatalf("%s: Location %q, want %q", what, got, location)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s: Cache-Control %q, want no-store", what, got)
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("%s: a JSON answer: %q", what, rec.Body.String())
	}
}

// (a) The page carries the CSRF field on a FIRST visit (the token the same
// response sets as a cookie) and on a later one. A page with a token is never
// stored by a shared cache.
func TestSpaPage_FormsCarryTheCsrfFieldFromTheFirstVisit(t *testing.T) {
	h := pageTestServer(t, func() []any { return []any{Spa_pageRoute("/form", pageHandler(pageFormHTML))} })

	first := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/form", nil))
	if first.Code != 200 {
		t.Fatalf("first GET: %d", first.Code)
	}
	tok := csrfCookieOf(t, first).Value
	field := `<input type="hidden" name="__sky_csrf" value="` + tok + `">`
	if !strings.Contains(first.Body.String(), field) {
		t.Fatalf("first visit: the form has no CSRF field for the cookie's token:\n%s", first.Body.String())
	}
	if !headerHasToken(first.Header().Values("Vary"), "Cookie") {
		t.Fatalf("a page with a token must vary on Cookie: %v", first.Header().Values("Vary"))
	}
	if got := first.Header().Get("Cache-Control"); got != "private" {
		t.Fatalf("a page with a token must be private: Cache-Control %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "http://app.example/form", nil)
	req.AddCookie(&http.Cookie{Name: SkyCsrfCookieName, Value: tok})
	second := serve(h, req)
	if !strings.Contains(second.Body.String(), field) {
		t.Fatalf("second visit: the form has no CSRF field:\n%s", second.Body.String())
	}
}

// (c)/(d) A native form POST to a page answers 303 to the same URL, query
// included: with no token, with a token that does not match, with no cookie
// at all, and with the right token.
func TestSpaPage_NativeFormPostAnswers303(t *testing.T) {
	h := pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/{$}", pageHandler(pageFormHTML)),
			Spa_pageRoute("/form", pageHandler(pageFormHTML)),
			Spa_pageRoute("/post/:slug", pageHandler(pageFormHTML))}
	})
	cookie := &http.Cookie{Name: SkyCsrfCookieName, Value: strings.Repeat("a", 64)}
	pw := url.Values{"name": {"x"}, "password": {"secret"}}

	wantSeeOther(t, "no cookie, no field", serve(h, formPost("/form?next=%2Fhome", pw)), "/form?next=%2Fhome")
	wantSeeOther(t, "cookie, no field", serve(h, formPost("/form", pw, cookie)), "/form")
	wrong := url.Values{"name": {"x"}, "__sky_csrf": {strings.Repeat("b", 64)}}
	wantSeeOther(t, "a token that does not match", serve(h, formPost("/form", wrong, cookie)), "/form")
	right := url.Values{"name": {"x"}, "__sky_csrf": {cookie.Value}}
	wantSeeOther(t, "the right token", serve(h, formPost("/form", right, cookie)), "/form")
	wantSeeOther(t, "the root page", serve(h, formPost("/", pw)), "/")
	wantSeeOther(t, "a page with a parameter", serve(h, formPost("/post/hello", pw)), "/post/hello")

	multipart := formPost("/form", pw)
	multipart.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	wantSeeOther(t, "a multipart form", serve(h, multipart), "/form")
}

// A POST that is not a native form submit keeps the 403: a path that is not a
// page, a JSON POST to a page (app code calling fetch), and a form POST that
// carries the X-Sky-Csrf header (a script, not a browser form).
func TestSpaPage_OtherPostsKeepThe403(t *testing.T) {
	h := pageTestServer(t, func() []any { return []any{Spa_pageRoute("/form", pageHandler(pageFormHTML))} })
	pw := url.Values{"name": {"x"}}

	for name, req := range map[string]*http.Request{
		"a path that is not a page":         formPost("/other", pw),
		"a page path with an extra segment": formPost("/form/x", pw),
		"a protocol-relative path":          formPost("//evil.example/form", pw),
	} {
		rec := serve(h, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
			t.Fatalf("%s: status %d Location %q: want the 403", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	jsonPost := httptest.NewRequest(http.MethodPost, "http://app.example/form", strings.NewReader(`{"a":1}`))
	jsonPost.Header.Set("Content-Type", "application/json")
	if rec := serve(h, jsonPost); rec.Code != http.StatusForbidden {
		t.Fatalf("a JSON POST to a page: %d, want 403", rec.Code)
	}
	scripted := formPost("/form", pw)
	scripted.Header.Set(SkyCsrfHeaderName, "nope")
	if rec := serve(h, scripted); rec.Code != http.StatusForbidden {
		t.Fatalf("a form POST with X-Sky-Csrf: %d, want 403", rec.Code)
	}
}

// (e) An App.api (Server.api) POST route that takes a form body is unchanged,
// alone and on the same path as a page (which leaves its POST to it).
func TestSpaPage_ApiFormPostIsUnchanged(t *testing.T) {
	h := pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/form", pageHandler(pageFormHTML)),
			Spa_pageRoute("/items", pageHandler(pageFormHTML)),
			Server_api("POST /unsub", textHandler("unsub:")),
			Server_api("POST /items", textHandler("items:"))}
	})
	for path, want := range map[string]string{"/unsub": "unsub:list=news", "/items": "items:list=news"} {
		rec := serve(h, formPost(path, url.Values{"list": {"news"}}))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("POST %s: %d %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	page := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/items", nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), `name="__sky_csrf"`) {
		t.Fatalf("GET /items next to its POST api route: %d\n%s", page.Code, page.Body.String())
	}
}

// (f) A Server.rpc route keeps its own guard: a form POST is refused, never
// redirected.
func TestSpaPage_RpcIsUnchanged(t *testing.T) {
	h := pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/form", pageHandler(pageFormHTML)),
			Server_rpc("POST /_rpc/Save", textHandler("saved:"))}
	})
	rec := serve(h, formPost("/_rpc/Save", url.Values{"a": {"b"}}))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Fatalf("a form POST to /_rpc: %d Location %q, want 403", rec.Code, rec.Header().Get("Location"))
	}
}

// A page whose path another route takes POST on (Server.any here) leaves the
// POST to it. A page on a path shared with a non-POST route gets its own POST
// entry. Neither panics the mux at start.
func TestSpaPage_SharedPathsLeavePostToTheApp(t *testing.T) {
	h := pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/any", pageHandler(pageFormHTML)),
			Server_any("/any", textHandler("any:"))}
	})
	if rec := serve(h, formPost("/any", url.Values{"a": {"b"}})); rec.Code == http.StatusSeeOther {
		t.Fatalf("a page took the POST of a Server.any route: %d", rec.Code)
	}
	h = pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/doc", pageHandler(pageFormHTML)),
			Server_api("PUT /doc", textHandler("put:"))}
	})
	wantSeeOther(t, "a page next to a PUT route", serve(h, formPost("/doc", url.Values{"a": {"b"}})), "/doc")
}

// The 303 never leaves the origin, whatever the request line holds.
func TestSameOriginLocation(t *testing.T) {
	for in, want := range map[string]string{
		"/form?x=1":           "/form?x=1",
		"//evil.example/x":    "/evil.example/x",
		"///evil.example":     "/evil.example",
		"/\\evil.example":     "/evil.example",
		"\\\\evil.example":    "/evil.example",
		"/a?next=//b":         "/a?next=//b",
		"":                    "/",
		"http://evil.example": "/",
		"*":                   "/",
	} {
		if got := sameOriginLocation(in); got != want {
			t.Fatalf("sameOriginLocation(%q) = %q, want %q", in, got, want)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://app.example/x", nil)
	req.URL.Path = "//evil.example/x"
	pagePostRedirect(rec, req)
	if got := rec.Header().Get("Location"); got != "/evil.example/x" {
		t.Fatalf("pagePostRedirect Location %q", got)
	}
}

func TestPagePatternMatch(t *testing.T) {
	for _, c := range []struct {
		pat, path string
		want      bool
	}{
		{"/{$}", "/", true},
		{"/{$}", "//", false},
		{"/{$}", "/x", false},
		{"/form", "/form", true},
		{"/form", "/form/", false},
		{"/post/:slug", "/post/a", true},
		{"/post/{slug}", "/post/a", true},
		{"/post/:slug", "/post/", false},
		{"/post/:slug", "/post/a/b", false},
	} {
		if got := pagePatternMatch(c.pat, c.path); got != c.want {
			t.Fatalf("pagePatternMatch(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestPrivateCacheControl(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "private",
		"public, max-age=600":            "private, max-age=600",
		"max-age=60, s-maxage=600":       "private, max-age=60",
		"private, max-age=60":            "private, max-age=60",
		"no-store":                       "no-store",
		"no-cache":                       "private, no-cache",
		"PUBLIC, S-MAXAGE=10, immutable": "private, immutable",
	} {
		if got := privateCacheControl(in); got != want {
			t.Fatalf("privateCacheControl(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sky.Live: a native form POST to a page answers 303 (before the client
// script runs, there is no event POST), with or without a token. A Live.api
// POST route and the event endpoint are unchanged.
func TestLivePage_NativeFormPostAnswers303(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	app.api = []apiRoute{{method: "POST", pattern: "/unsub", handler: textHandler("unsub:")}}
	SetCsrfEnabled(true)
	t.Cleanup(func() { refreshCsrfEnabled() })
	ResetWithoutCsrf()
	WithoutCsrfMethod("POST", "/unsub")
	t.Cleanup(ResetWithoutCsrf)
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/", app.dispatchRoot)
	h := liveListenerHandlerFor(app, mux, "")

	page := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/", nil))
	if page.Code != 200 {
		t.Fatalf("page GET: %d", page.Code)
	}
	if got := page.Header().Get("Cache-Control"); got != "private" {
		t.Fatalf("a Sky.Live page carries a token: Cache-Control %q, want private", got)
	}
	cookie := csrfCookieOf(t, page)
	pw := url.Values{"password": {"secret"}}
	wantSeeOther(t, "Live: no token", serve(h, formPost("/?a=1", pw)), "/?a=1")
	wantSeeOther(t, "Live: cookie, no field", serve(h, formPost("/settings", pw, cookie)), "/settings")
	right := url.Values{"__sky_csrf": {cookie.Value}}
	wantSeeOther(t, "Live: the right token", serve(h, formPost("/", right, cookie)), "/")

	if rec := serve(h, formPost("/unsub", url.Values{"list": {"news"}})); rec.Code != 200 {
		t.Fatalf("Live.api POST /unsub: %d %q", rec.Code, rec.Body.String())
	}
	ev := formPost("/_sky/event", pw)
	if rec := serve(h, ev); rec.Code != http.StatusForbidden {
		t.Fatalf("a form POST to /_sky/event: %d, want 403", rec.Code)
	}
}

// Sky.Live in header session mode (no cookies): the same 303.
func TestLivePage_HeaderModeNativeFormPostAnswers303(t *testing.T) {
	app := newHeaderTestApp(t)
	SetCsrfEnabled(true)
	t.Cleanup(func() { refreshCsrfEnabled() })
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/", app.dispatchRoot)
	h := liveListenerHandlerFor(app, mux, "")
	wantSeeOther(t, "header mode", serve(h, formPost("/login", url.Values{"password": {"x"}})), "/login")
	ev := formPost("/_sky/event", url.Values{"a": {"b"}})
	if rec := serve(h, ev); rec.Code != http.StatusForbidden {
		t.Fatalf("header mode: a form POST to /_sky/event: %d, want 403", rec.Code)
	}
}

// Two routes on one mux path whose wildcards have different names are one
// path to Go's mux. Counted by their text, both were registered method-less
// and the mux panicked at start: a page `/post/:slug` (or any GET route) next
// to `App.api "POST /post/:id"` crash-looped the backend. Each now reads its
// own parameter, and a preflight reaches the route it names.
func TestRoutesWithDifferentWildcardNamesShareAPath(t *testing.T) {
	param := func(name string) any {
		return func(req SkyRequest) any {
			return func() any {
				return Ok[any, any](SkyResponse{Status: 200, Body: name + "=" + AsString(req.Params[name]), ContentType: "text/plain"})
			}
		}
	}
	h := pageTestServer(t, func() []any {
		return []any{
			Server_get("/post/:slug", param("slug")),
			Server_api("POST /post/:id", param("id")),
			Spa_pageRoute("/doc/:name", pageHandler(pageFormHTML)),
			Server_api("POST /doc/:key", param("key")),
		}
	})
	if rec := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/post/a", nil)); rec.Body.String() != "slug=a" {
		t.Fatalf("GET /post/a: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(h, formPost("/post/b", url.Values{})); rec.Body.String() != "id=b" {
		t.Fatalf("POST /post/b: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(h, formPost("/doc/c", url.Values{})); rec.Body.String() != "key=c" {
		t.Fatalf("POST /doc/c next to a page: %d %q", rec.Code, rec.Body.String())
	}
	pre := httptest.NewRequest(http.MethodOptions, "http://app.example/post/d", nil)
	pre.Header.Set("Access-Control-Request-Method", "POST")
	if rec := serve(h, pre); rec.Body.String() != "id=d" {
		t.Fatalf("preflight for POST /post/d: %d %q", rec.Code, rec.Body.String())
	}
}

// The split's `/` catch-all (Server.staticNotFound) renders the NotFound page
// for a path no route takes. A form on it submitted natively answers 303 too,
// with or without a token, and its forms carry the CSRF field. The runtime's
// own paths keep the 403.
func TestSpaNotFoundFallback_NativeFormPostAnswers303(t *testing.T) {
	dir := t.TempDir()
	h := pageTestServer(t, func() []any {
		return []any{
			Spa_pageRoute("/form", pageHandler(pageFormHTML)),
			Server_staticNotFound("/", dir, pageHandler(pageFormHTML)),
		}
	})
	get := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/no/such/page", nil))
	if !strings.Contains(get.Body.String(), `name="__sky_csrf"`) {
		t.Fatalf("the NotFound page has no CSRF field: %d\n%s", get.Code, get.Body.String())
	}
	wantSeeOther(t, "NotFound page, no token", serve(h, formPost("/no/such/page", url.Values{"a": {"b"}})), "/no/such/page")
	cookie := csrfCookieOf(t, get)
	right := url.Values{"__sky_csrf": {cookie.Value}}
	wantSeeOther(t, "NotFound page, the right token", serve(h, formPost("/no/such/page", right, cookie)), "/no/such/page")
	for _, path := range []string{"/_sky/event", "/_rpc/Save"} {
		if rec := serve(h, formPost(path, url.Values{"a": {"b"}})); rec.Code == http.StatusSeeOther {
			t.Fatalf("%s: a runtime path was redirected", path)
		}
	}
}

// The boot loader holds a submit made while the page hydrates and hands it
// back to the client (spaReplayHeldInput, live_wasm.go). The browser stage
// (scripts/spa-early-submit-verify.mjs) proves the behaviour; this pins the
// pieces, so a loader rewrite cannot drop them unseen.
func TestSpaBootLoaderHoldsAnEarlySubmit(t *testing.T) {
	for _, piece := range []string{
		`document.addEventListener("submit", function (ev) {`,
		`!form.hasAttribute("sky-submit")`,
		`ev.preventDefault();`,
		`ev.stopImmediatePropagation();`,
		`root.hasAttribute("data-sky-hydrating")`,
		`document.addEventListener("input", onEdit, true);`,
		`globalThis.__skyHydrated = function () {`,
		`form.requestSubmit(`,
	} {
		if !strings.Contains(SpaBootJS, piece) {
			t.Fatalf("the Sky.Spa boot loader lost %q", piece)
		}
	}
}

// The page set belongs to one listener's routes. A second server in the same
// process, with no page on that path, keeps the 403.
func TestSpaPage_PagesBelongToTheirOwnServer(t *testing.T) {
	withPages := pageTestServer(t, func() []any {
		return []any{Server_staticNotFound("/", t.TempDir(), pageHandler(pageFormHTML))}
	})
	plain := pageTestServer(t, func() []any { return []any{Server_get("/form", pageHandler(pageFormHTML))} })
	wantSeeOther(t, "the server with the catch-all", serve(withPages, formPost("/form", url.Values{})), "/form")
	if rec := serve(plain, formPost("/form", url.Values{})); rec.Code != http.StatusForbidden {
		t.Fatalf("a server without pages answered %d, want 403", rec.Code)
	}
}

// Any response that sets the CSRF cookie is private (v0.29.1). A Sky.Spa page
// with no form got no `__sky_csrf` field, so it was not marked, yet it set the
// visitor's CSRF cookie: a shared cache that stored it handed every later
// visitor the same cookie. Now every response that sets the cookie carries
// `Vary: Cookie` and `Cache-Control: private`, keeping an app's own
// directives less `public` and `s-maxage`.
func TestCsrfCookieResponseIsPrivate(t *testing.T) {
	const noForm = `<!DOCTYPE html><html><body><p>hello</p></body></html>`
	h := pageTestServer(t, func() []any {
		return []any{Spa_pageRoute("/about", pageHandler(noForm))}
	})
	rec := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/about", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /about: %d", rec.Code)
	}
	csrfCookieOf(t, rec)
	if got := rec.Header().Get("Cache-Control"); got != "private" {
		t.Fatalf("a page with no form that sets the CSRF cookie: Cache-Control %q, want private", got)
	}
	if !headerHasToken(rec.Header().Values("Vary"), "Cookie") {
		t.Fatalf("a page that sets the CSRF cookie has no Vary: Cookie: %v", rec.Header().Values("Vary"))
	}

	// Any handler behind the middleware, whatever it writes and when.
	SetCsrfEnabled(true)
	t.Cleanup(func() { refreshCsrfEnabled() })
	for name, handler := range map[string]http.HandlerFunc{
		"write": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=600")
			_, _ = w.Write([]byte("x"))
		},
		"writeHeader": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=600")
			w.WriteHeader(204)
		},
		"nothing": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=600")
		},
		"flush": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=600")
			f, ok := w.(http.Flusher)
			if !ok {
				t.Errorf("the CSRF middleware hides http.Flusher")
				return
			}
			f.Flush()
		},
	} {
		rec := serve(CSRFMiddleware(handler), httptest.NewRequest(http.MethodGet, "http://app.example/x", nil))
		csrfCookieOf(t, rec)
		if got := rec.Header().Get("Cache-Control"); got != "private, max-age=60" {
			t.Fatalf("%s: Cache-Control %q, want %q", name, got, "private, max-age=60")
		}
		if !headerHasToken(rec.Header().Values("Vary"), "Cookie") {
			t.Fatalf("%s: no Vary: Cookie: %v", name, rec.Header().Values("Vary"))
		}
	}
}

// readCounter counts the bytes a handler or a middleware read from a body.
type readCounter struct {
	r *strings.Reader
	n int
}

func (c *readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A native form POST that fails the CSRF check reaches the 303 from more than
// api and rpc routes: a `Server.staticNotFound` catch-all page, and any
// non-api path of a Sky.Live app. Both answer 303 from the middleware, with
// no handler run and the body not read.
func TestCsrfRejectAnswers303ForCatchAllAndLivePages(t *testing.T) {
	post := func(target string) (*http.Request, *readCounter) {
		body := &readCounter{r: strings.NewReader("password=secret")}
		req := httptest.NewRequest(http.MethodPost, "http://app.example"+target, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, body
	}

	dir := t.TempDir()
	h := pageTestServer(t, func() []any {
		return []any{Server_staticNotFound("/", dir, pageHandler(pageFormHTML))}
	})
	req, body := post("/no/such/page")
	wantSeeOther(t, "catch-all page", serve(h, req), "/no/such/page")
	if body.n != 0 {
		t.Fatalf("catch-all page: the 303 read %d body bytes, want 0", body.n)
	}

	app := newBindingTestApp("sky_sid")
	SetCsrfEnabled(true)
	t.Cleanup(func() { refreshCsrfEnabled() })
	ran := false
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			ran = true
		}
		app.dispatchRoot(w, r)
	})
	lh := liveListenerHandlerFor(app, mux, "")
	req, body = post("/account/settings")
	wantSeeOther(t, "Live non-api POST", serve(lh, req), "/account/settings")
	if ran {
		t.Fatalf("Live non-api POST: the page handler ran for a POST that failed the check")
	}
	if body.n != 0 {
		t.Fatalf("Live non-api POST: the 303 read %d body bytes, want 0", body.n)
	}
}

// A `Server.staticNotFound` catch-all makes only the paths it serves pages
// (v0.29.1 regression). A hand-written app with `Server.post "/login"` (or
// `Server.any`) next to the catch-all answered every native form POST to
// /login with the 303, even with the right token, so its handler never ran.
// A path another route takes for the request's method is not a page: the
// handler runs with the right token, and a missing or wrong token is the
// normal CSRF 403 JSON. A path no route takes, under the catch-all's prefix,
// is still a page (303).
func TestStaticNotFound_OtherRoutesKeepTheirPost(t *testing.T) {
	builds := map[string]func(dir string) []any{
		"Server.post": func(dir string) []any {
			return []any{Server_get("/login", pageHandler(pageFormHTML)),
				Server_post("/login", textHandler("login:")),
				Server_staticNotFound("/", dir, pageHandler(pageFormHTML))}
		},
		"Server.any": func(dir string) []any {
			return []any{Server_any("/login", textHandler("login:")),
				Server_staticNotFound("/", dir, pageHandler(pageFormHTML))}
		},
	}
	for name, build := range builds {
		dir := t.TempDir()
		h := pageTestServer(t, func() []any { return build(dir) })
		get := serve(h, httptest.NewRequest(http.MethodGet, "http://app.example/login", nil))
		cookie := csrfCookieOf(t, get)

		right := url.Values{"user": {"ada"}, "__sky_csrf": {cookie.Value}}
		rec := serve(h, formPost("/login", right, cookie))
		if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "login:") {
			t.Fatalf("%s: POST /login with the right token: %d %q, want the handler's 200",
				name, rec.Code, rec.Body.String())
		}
		for what, form := range map[string]url.Values{
			"no token":  {"user": {"ada"}},
			"bad token": {"user": {"ada"}, "__sky_csrf": {strings.Repeat("b", 64)}},
		} {
			rec := serve(h, formPost("/login", form, cookie))
			if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" ||
				!strings.Contains(rec.Header().Get("Content-Type"), "json") {
				t.Fatalf("%s: POST /login with %s: %d Location %q: want the CSRF 403 JSON",
					name, what, rec.Code, rec.Header().Get("Location"))
			}
		}
		wantSeeOther(t, name+": an unclaimed path", serve(h, formPost("/no/such/page", right, cookie)), "/no/such/page")
	}
}

// A catch-all mounted under a prefix makes pages only under that prefix.
func TestStaticNotFound_PagesStayUnderItsPrefix(t *testing.T) {
	dir := t.TempDir()
	h := pageTestServer(t, func() []any {
		return []any{Server_staticNotFound("/app/", dir, pageHandler(pageFormHTML))}
	})
	wantSeeOther(t, "under the prefix", serve(h, formPost("/app/x", url.Values{})), "/app/x")
	// The mux redirects "/app" to the mount "/app/", so "/app" is the mount
	// too: a native form POST to it answers 303, as it did before v0.29.1
	// made the catch-all's pages follow the mux.
	wantSeeOther(t, "the prefix without its slash", serve(h, formPost("/app", url.Values{})), "/app")
	for _, p := range []string{"/other", "/appx", "/ap"} {
		if rec := serve(h, formPost(p, url.Values{})); rec.Code != http.StatusForbidden {
			t.Fatalf("%s, outside the prefix, answered %d, want 403", p, rec.Code)
		}
	}
}
