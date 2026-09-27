//go:build !js

package rt

// Server.withHeader and the CORS / CSRF interplay.
//
// Two defects are pinned here:
//
//   - withHeader MUTATED the response's headers map. Two responses derived
//     from one shared base (a common `ok = Server.text "ok"` value decorated
//     per route) leaked headers into each other.
//   - Header names that differ only in case were stored as two keys and then
//     applied in random map order, so which value went on the wire changed
//     from request to request.
//
// The CORS test documents what a "dropped header" report usually is: a
// cross-origin POST without an Authorization header is refused by the CSRF
// guard (a 403 that carries no CORS headers), and a preflight OPTIONS is
// answered only by a handler wrapped in Middleware.withCors.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithHeaderIsCopyOnWrite(t *testing.T) {
	base, _ := asSkyResponse(Server_withHeader("X-Base", "1", SkyResponse{Status: 200, Body: "ok"}))
	a, _ := asSkyResponse(Server_withHeader("X-A", "a", base))
	b, _ := asSkyResponse(Server_withHeader("X-B", "b", base))

	if _, leaked := b.Headers["X-A"]; leaked {
		t.Fatalf("a header set on one derived response leaked into its sibling: %v", b.Headers)
	}
	if _, leaked := a.Headers["X-B"]; leaked {
		t.Fatalf("a header set on one derived response leaked into its sibling: %v", a.Headers)
	}
	if len(base.Headers) != 1 || base.Headers["X-Base"] != "1" {
		t.Fatalf("the shared base response was mutated: %v", base.Headers)
	}
	if a.Headers["X-Base"] != "1" || b.Headers["X-Base"] != "1" {
		t.Fatalf("derived responses lost the base header: a=%v b=%v", a.Headers, b.Headers)
	}
}

func TestWithHeaderCanonicalisesNamesAndLastWriteWins(t *testing.T) {
	r0 := SkyResponse{Status: 200}
	r1, _ := asSkyResponse(Server_withHeader("X-Custom", "first", r0))
	r2, _ := asSkyResponse(Server_withHeader("x-custom", "second", r1))
	if len(r2.Headers) != 1 {
		t.Fatalf("names differing only in case were stored twice: %v", r2.Headers)
	}
	if got := r2.Headers["X-Custom"]; got != "second" {
		t.Fatalf("X-Custom = %q, want the last write %q (headers %v)", got, "second", r2.Headers)
	}
}

// A headers map built directly (a record literal in Sky) can still hold two
// spellings of one name. Emission must be deterministic: the canonical
// spelling wins, on every run.
func TestApplyHeadersIsDeterministicForCaseVariants(t *testing.T) {
	resp := SkyResponse{Headers: map[string]string{
		"x-dup": "lower", "X-Dup": "canonical", "X-DUP": "upper",
	}}
	for i := 0; i < 50; i++ {
		h := http.Header{}
		applySkyResponseHeaders(h, httptest.NewRequest("GET", "/", nil), resp)
		if got := h.Values("X-Dup"); len(got) != 1 || got[0] != "canonical" {
			t.Fatalf("run %d: X-Dup = %v, want exactly [canonical]", i, got)
		}
	}
}

func TestWithCorsDoesNotMutateTheInnerResponse(t *testing.T) {
	shared := SkyResponse{Status: 200, Body: "ok", Headers: map[string]string{"X-Base": "1"}}
	inner := func(req SkyRequest) any {
		return func() any { return Ok[any, any](shared) }
	}
	h := Middleware_withCors([]any{"https://front.example"}, inner)
	req := SkyRequest{Method: "GET", Path: "/", Headers: map[string]any{"Origin": "https://front.example"}}
	_ = anyTaskInvoke(SkyCall(h, req))
	if _, leaked := shared.Headers["Access-Control-Allow-Origin"]; leaked {
		t.Fatalf("withCors wrote into the handler's shared headers map: %v", shared.Headers)
	}
}

// corsTestServer mounts the route list behind the CSRF middleware, the same
// order the real listener uses.
func corsTestServer(t *testing.T) http.Handler {
	t.Helper()
	resetCsrf(t)
	resp := func(body string) any {
		return func(req SkyRequest) any {
			return func() any {
				return Ok[any, any](SkyResponse{Status: 200, Body: body, ContentType: "text/plain"})
			}
		}
	}
	twoHeaders := func(req SkyRequest) any {
		return func() any {
			r := Server_withHeader("X-One", "1", SkyResponse{Status: 200, Body: "two"})
			return Ok[any, any](Server_withHeader("X-Two", "2", r))
		}
	}
	cors := func(h any) any { return Middleware_withCors([]any{"https://front.example"}, h) }
	routes := []any{
		Server_get("/api/headers", twoHeaders),
		Server_post("/api/single", cors(resp("single"))),
		Server_get("/api/two", cors(resp("two-get"))),
		Server_post("/api/two", cors(resp("two-post"))),
	}
	mux, _ := serverRouteMux(routes)
	return CSRFMiddleware(mux)
}

func corsDo(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTwoWithHeaderValuesSurviveASameOriginGet(t *testing.T) {
	rec := corsDo(corsTestServer(t), "GET", "http://app.example/api/headers", nil)
	if rec.Code != 200 || rec.Header().Get("X-One") != "1" || rec.Header().Get("X-Two") != "2" {
		t.Fatalf("status %d, headers %v: want 200 with X-One and X-Two", rec.Code, rec.Header())
	}
}

func TestCrossOriginPostWithoutAuthorizationIsTheCsrf403(t *testing.T) {
	h := corsTestServer(t)
	rec := corsDo(h, "POST", "http://app.example/api/single", map[string]string{
		"Origin": "https://front.example", "Content-Type": "application/json",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie POST: status %d, want the CSRF 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("the CSRF 403 carried a CORS header %q; the handler never ran", got)
	}
	// With a non-ambient credential the request is CSRF-exempt, reaches the
	// withCors-wrapped handler and gets its CORS header.
	rec = corsDo(h, "POST", "http://app.example/api/single", map[string]string{
		"Origin": "https://front.example", "Content-Type": "application/json",
		"Authorization": "Bearer t",
	})
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://front.example" {
		t.Fatalf("Bearer cross-origin POST: status %d headers %v, want 200 + ACAO", rec.Code, rec.Header())
	}
}

func preflight(h http.Handler, target, method string) *httptest.ResponseRecorder {
	return corsDo(h, "OPTIONS", target, map[string]string{
		"Origin":                         "https://front.example",
		"Access-Control-Request-Method":  method,
		"Access-Control-Request-Headers": "content-type, authorization",
	})
}

func TestPreflightOnASingleRoutePath(t *testing.T) {
	rec := preflight(corsTestServer(t), "http://app.example/api/single", "POST")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://front.example" {
		t.Fatalf("preflight on a single-route path: status %d headers %v, want 204 + ACAO", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight does not allow Authorization: %v", rec.Header())
	}
}

// Two routes on one path are registered method-keyed ("GET /p", "POST /p"),
// so Go's mux answered OPTIONS with its own 405 and the withCors wrapper never
// saw the preflight.
func TestPreflightOnATwoRoutePath(t *testing.T) {
	h := corsTestServer(t)
	for _, m := range []string{"POST", "GET"} {
		rec := preflight(h, "http://app.example/api/two", m)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://front.example" {
			t.Fatalf("preflight for %s on a two-route path: status %d headers %v, want 204 + ACAO", m, rec.Code, rec.Header())
		}
	}
	// A preflight for a method no route serves is not answered by a handler.
	rec := preflight(h, "http://app.example/api/two", "DELETE")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("preflight for an unserved method: status %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Fatalf("405 Allow header = %q, want GET and POST", allow)
	}
}

// Server.use was an identity function: `Server.use (Middleware.withCors …)
// routes` returned the routes unwrapped, so the CORS headers the docs promise
// never appeared. It now wraps every route's handler.
func TestServerUseAppliesTheMiddlewareToEveryRoute(t *testing.T) {
	resetCsrf(t)
	h := func(req SkyRequest) any {
		return func() any { return Ok[any, any](SkyResponse{Status: 200, Body: "ok"}) }
	}
	cors := func(inner any) any { return Middleware_withCors([]any{"https://front.example"}, inner) }
	routes := Server_use(cors, []any{
		Server_get("/a", h),
		Server_post("/b", h),
		Server_static("/static/", t.TempDir()),
	}).([]any)
	if len(routes) != 3 {
		t.Fatalf("Server.use changed the route count: %d", len(routes))
	}
	mux, _ := serverRouteMux(routes)
	srv := CSRFMiddleware(mux)
	rec := corsDo(srv, "GET", "http://app.example/a", map[string]string{"Origin": "https://front.example"})
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://front.example" {
		t.Fatalf("GET through Server.use withCors: status %d headers %v, want 200 + ACAO", rec.Code, rec.Header())
	}
	rec = preflight(srv, "http://app.example/b", "POST")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://front.example" {
		t.Fatalf("preflight through Server.use withCors: status %d headers %v", rec.Code, rec.Header())
	}
}
