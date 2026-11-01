//go:build !js

package rt

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HTTP header names are case-insensitive (RFC 9110 section 5.1). HTTP/2 sends
// every name in lower case, and a proxy may change the case of any name. A Sky
// app must find a header whatever case the client or a proxy used:
//
//   - the `headers` Dict on a request holds each name in canonical form
//     (`X-Sky-Api-Key`), on every path that builds one, so
//     `Dict.get "X-Sky-Api-Key" req.headers` finds `x-sky-api-key`;
//   - every runtime lookup of a request header (`Server.header`, the
//     middlewares) ignores case, also on a request a test built by hand.

// A request whose header map holds a name that is not canonical: a handler
// wrapper, a test or a proxy library can build one. Go's own server always
// canonicalises; the runtime does not rely on it.
func nonCanonicalHeaderRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header["x-sky-api-key"] = []string{"k-lower"}
	req.Header["X-REQUEST-ID"] = []string{"r-upper"}
	return req
}

func assertCanonicalHeaders(t *testing.T, where string, headers map[string]any) {
	t.Helper()
	if got := headers["X-Sky-Api-Key"]; got != "k-lower" {
		t.Fatalf("%s: headers[\"X-Sky-Api-Key\"] = %v, want k-lower (keys %v)", where, got, mapKeys(headers))
	}
	if got := headers["X-Request-Id"]; got != "r-upper" {
		t.Fatalf("%s: headers[\"X-Request-Id\"] = %v, want r-upper (keys %v)", where, got, mapKeys(headers))
	}
	for k := range headers {
		if k == "x-sky-api-key" || k == "X-REQUEST-ID" {
			t.Fatalf("%s: a non-canonical key %q is left in the headers", where, k)
		}
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestServerRouteRequestHeaderKeysAreCanonical(t *testing.T) {
	var seen SkyRequest
	w := httptest.NewRecorder()
	dispatchSkyHandler(w, nonCanonicalHeaderRequest(t, "/"), hostEchoHandler(&seen), nil)
	assertCanonicalHeaders(t, "Sky.Http.Server route", seen.Headers)
}

func TestLiveApiRequestHeaderKeysAreCanonical(t *testing.T) {
	var seen SkyRequest
	app := &liveApp{}
	w := httptest.NewRecorder()
	app.serveAPI(apiRoute{method: "GET", pattern: "/api/x", handler: hostEchoHandler(&seen)}, nil, w, nonCanonicalHeaderRequest(t, "/api/x"))
	assertCanonicalHeaders(t, "Sky.Live api route", seen.Headers)
}

func TestConsoleAuthRequestHeaderKeysAreCanonical(t *testing.T) {
	got := buildConsoleAuthRequest(nonCanonicalHeaderRequest(t, "/_sky/console"))
	assertCanonicalHeaders(t, "console auth request", got.Headers)
}

// The App.withRequest / init seed: a Sky Dict.
func TestRequestHeadersDictKeysAreCanonical(t *testing.T) {
	d := requestHeadersToDict(nonCanonicalHeaderRequest(t, "/"))
	for name, want := range map[string]string{"X-Sky-Api-Key": "k-lower", "X-Request-Id": "r-upper"} {
		m, ok := Dict_get(name, d).(SkyMaybe[any])
		if !ok || m.Tag != 0 || m.JustValue != want {
			t.Fatalf("Dict.get %q seed.headers = %#v, want Just %q", name, Dict_get(name, d), want)
		}
	}
	if m, _ := Dict_get("x-sky-api-key", d).(SkyMaybe[any]); m.Tag == 0 {
		t.Fatalf("the seed Dict kept the non-canonical key x-sky-api-key")
	}
}

// Two spellings of one name: the canonical one wins, and the result does not
// depend on map order.
func TestCanonicalRequestHeadersPrefersTheCanonicalSpelling(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := http.Header{}
		h["x-foo"] = []string{"lower"}
		h["X-Foo"] = []string{"canonical"}
		h["X-FOO"] = []string{"upper"}
		got := canonicalRequestHeaders(h, "")
		if got["X-Foo"] != "canonical" || len(got) != 1 {
			t.Fatalf("canonicalRequestHeaders = %v, want only X-Foo=canonical", got)
		}
	}
}

// A typed Request record (what `App.withRequest` hands the app, and what a
// `sky test` builds by hand) whose headers Dict holds any case: Server.header
// finds it by any spelling.
func TestServerHeaderOnATypedRequestRecordIgnoresCase(t *testing.T) {
	type typedRequest struct {
		Method  string
		Path    string
		Headers SkyDict[any]
	}
	d := Dict_empty()
	d = Dict_insert("x-sky-api-key", "k1", d)
	d = Dict_insert("Stripe-Signature", "sig", d)
	req := typedRequest{Method: "POST", Path: "/hook", Headers: d.(SkyDict[any])}
	for _, c := range []struct{ name, want string }{
		{"X-Sky-Api-Key", "k1"},
		{"x-sky-api-key", "k1"},
		{"X-SKY-API-KEY", "k1"},
		{"stripe-signature", "sig"},
		{"STRIPE-SIGNATURE", "sig"},
	} {
		m, ok := Server_header(c.name, req).(SkyMaybe[any])
		if !ok || m.Tag != 0 || m.JustValue != c.want {
			t.Fatalf("Server.header %q = %#v, want Just %q", c.name, Server_header(c.name, req), c.want)
		}
	}
}

func okResponse(t *testing.T, v any) SkyResponse {
	t.Helper()
	r, ok := v.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("want Ok response, got %#v", v)
	}
	resp, ok := asSkyResponse(r.OkValue)
	if !ok {
		t.Fatalf("Ok value is not a response: %#v", r.OkValue)
	}
	return resp
}

func runHandler(handler any, req any) any {
	return anyTaskInvoke(SkyCall(handler, req))
}

func okTextHandler() func(any) any {
	return func(req any) any {
		return func() any { return Ok[any, any](Server_text("ok")) }
	}
}

// Middleware.withCors reads Origin whatever its case.
func TestMiddlewareCorsReadsOriginWhateverTheCase(t *testing.T) {
	h := Middleware_withCors([]any{"https://a.example"}, okTextHandler())
	for _, name := range []string{"origin", "ORIGIN", "Origin"} {
		req := SkyRequest{Method: "GET", Path: "/", Headers: map[string]any{name: "https://a.example"}}
		resp := okResponse(t, runHandler(h, req))
		if got := resp.Headers["Access-Control-Allow-Origin"]; got != "https://a.example" {
			t.Fatalf("withCors, Origin sent as %q: Access-Control-Allow-Origin = %q, want https://a.example", name, got)
		}
	}
}

// Middleware.withBasicAuth reads Authorization whatever its case.
func TestMiddlewareBasicAuthReadsAuthorizationWhateverTheCase(t *testing.T) {
	h := Middleware_withBasicAuth("ada", "pw", okTextHandler())
	for _, name := range []string{"authorization", "AUTHORIZATION", "Authorization"} {
		req := SkyRequest{Method: "GET", Path: "/", Headers: map[string]any{name: "Basic YWRhOnB3"}}
		resp := okResponse(t, runHandler(h, req))
		if resp.Status != 0 && resp.Status != 200 {
			t.Fatalf("withBasicAuth, Authorization sent as %q: status %d, want 200", name, resp.Status)
		}
	}
}

// Middleware.withRateLimit keys on X-Forwarded-For / X-Real-IP whatever their
// case: two clients behind one proxy are two buckets, not one "unknown".
func TestMiddlewareRateLimitReadsForwardedForWhateverTheCase(t *testing.T) {
	for _, name := range []string{"x-forwarded-for", "X-FORWARDED-FOR", "x-real-ip"} {
		bucket := "hdr-case-" + name
		h := Middleware_withRateLimit(bucket, 1, 1, okTextHandler())
		for _, ip := range []string{"203.0.113.1", "203.0.113.2"} {
			req := SkyRequest{Method: "GET", Path: "/", Headers: map[string]any{name: ip}}
			resp := okResponse(t, runHandler(h, req))
			if resp.Status == 429 {
				t.Fatalf("withRateLimit, client IP sent as %q: client %s was limited; the header was not read", name, ip)
			}
		}
	}
}

// Middleware.withCsrf reads X-Csrf-Token whatever its case.
func TestMiddlewareCsrfReadsTokenHeaderWhateverTheCase(t *testing.T) {
	h := Middleware_withCsrf(okTextHandler())
	for _, name := range []string{"x-csrf-token", "X-CSRF-TOKEN"} {
		req := SkyRequest{
			Method:  "POST",
			Path:    "/",
			Headers: map[string]any{name: "tok"},
			Cookies: map[string]string{"__Host-sky_csrf": "tok"},
		}
		resp := okResponse(t, runHandler(h, req))
		if resp.Status == 403 {
			t.Fatalf("withCsrf, token sent as %q: 403 %q", name, resp.Body)
		}
	}
}

// A bare response record whose headers name Content-Type in another case: the
// app's value is kept, not replaced by the ContentType default.
func TestUnpackResponseSeesContentTypeWhateverTheCase(t *testing.T) {
	type rec struct {
		Status      int
		Body        string
		Headers     map[string]string
		ContentType string
	}
	for i := 0; i < 20; i++ {
		_, headers, _ := unpackResponse(rec{Status: 200, Body: "a,b", Headers: map[string]string{"content-type": "text/csv"}, ContentType: "text/plain"})
		got := http.Header{}
		for k, v := range headers {
			got.Set(k, v)
		}
		if ct := got.Get("Content-Type"); ct != "text/csv" {
			t.Fatalf("Content-Type = %q, want text/csv (headers %v)", ct, headers)
		}
	}
}

// Over real HTTP/2, which sends every header name in lower case: a Sky.Http.Server
// route and a Sky.Live api route see canonical keys, and Server.header finds the
// header by any spelling.
func TestHttp2LowerCaseHeadersReachSkyHandlers(t *testing.T) {
	var seenServer, seenLive SkyRequest
	app := &liveApp{}
	mux := http.NewServeMux()
	mux.HandleFunc("/server", func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("request used %s, want HTTP/2", r.Proto)
		}
		dispatchSkyHandler(w, r, hostEchoHandler(&seenServer), nil)
	})
	mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		app.serveAPI(apiRoute{method: "GET", pattern: "/live", handler: hostEchoHandler(&seenLive)}, nil, w, r)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.ForceAttemptHTTP2 = true
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
	}
	for _, path := range []string{"/server", "/live"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header["x-sky-api-key"] = []string{"k2"}
		req.Header["STRIPE-SIGNATURE"] = []string{"t=1,v1=abc"}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatalf("%s: response used %s, want HTTP/2", path, resp.Proto)
		}
	}
	for where, seen := range map[string]SkyRequest{"Sky.Http.Server": seenServer, "Sky.Live api": seenLive} {
		if seen.Headers["X-Sky-Api-Key"] != "k2" || seen.Headers["Stripe-Signature"] != "t=1,v1=abc" {
			t.Fatalf("%s over HTTP/2: headers %v, want canonical X-Sky-Api-Key and Stripe-Signature", where, seen.Headers)
		}
		for _, name := range []string{"x-sky-api-key", "X-SKY-API-KEY", "X-Sky-Api-Key"} {
			m, _ := Server_header(name, seen).(SkyMaybe[any])
			if m.Tag != 0 || m.JustValue != "k2" {
				t.Fatalf("%s over HTTP/2: Server.header %q = %#v", where, name, m)
			}
		}
	}
}

// The HTTP client's response headers are canonical (Go's client canonicalises
// them), even when the server writes a name in lower case, so `Dict.get "Content-Type" resp.headers` and
// `Http.header` agree with the server side.
func TestHttpClientResponseHeaderKeysAreCanonical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["x-upstream-id"] = []string{"u1"}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	r := anyTaskInvoke(Http_get(srv.URL))
	if r.Tag != 0 {
		t.Fatalf("Http.get: %#v", r)
	}
	resp := r.OkValue.(HttpResponse)
	for _, name := range []string{"X-Upstream-Id", "Content-Type"} {
		m, _ := Dict_get(name, resp.Headers).(SkyMaybe[any])
		if m.Tag != 0 {
			t.Fatalf("Dict.get %q resp.headers = Nothing; keys must be canonical", name)
		}
	}
	if !strings.HasPrefix(AsString(Dict_get("Content-Type", resp.Headers).(SkyMaybe[any]).JustValue), "text/plain") {
		t.Fatalf("Content-Type wrong")
	}
}
