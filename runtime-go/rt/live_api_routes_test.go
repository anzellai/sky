package rt

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCollectLiveRoutes_ApiInRoutesList — regression for the Std.Live.api
// dispatch bug. `route` and `api` both return a `Route`, so api routes are
// placed IN the `routes` list (the documented shape). An older parser kept
// only `liveRoute` values from `routes` and read api routes from a separate
// `Api` cfg field that the documented surface never populates — so every api
// route in `routes` was silently dropped (GET fell through to notFound, POST
// 405'd). collectLiveRoutes must extract BOTH kinds from the `routes` list.
func TestCollectLiveRoutes_ApiInRoutesList(t *testing.T) {
	h := func(req any) any { return func() any { return Ok[any, any]("x") } }
	cfg := map[string]any{
		"Routes": []any{
			liveRoute{path: "/", page: "home"},
			apiRoute{method: "GET", pattern: "/ping", handler: h},
			liveRoute{path: "/about", page: "about"},
			apiRoute{method: "", pattern: "/echo", handler: h}, // any-method
		},
	}
	pages, apis := collectLiveRoutes(cfg)
	if len(pages) != 2 {
		t.Fatalf("page routes = %d, want 2", len(pages))
	}
	if len(apis) != 2 {
		t.Fatalf("api routes = %d, want 2 (api routes in `routes` list must be collected)", len(apis))
	}
	if apis[0].pattern != "/ping" || apis[0].method != "GET" {
		t.Errorf("api[0] = %+v, want {GET /ping}", apis[0])
	}
	if apis[1].pattern != "/echo" || apis[1].method != "" {
		t.Errorf("api[1] = %+v, want {any /echo}", apis[1])
	}
}

// TestCollectLiveRoutes_SeparateApiField — the back-compat `api = [...]` cfg
// field is still honoured (additively with any api routes in `routes`).
func TestCollectLiveRoutes_SeparateApiField(t *testing.T) {
	h := func(req any) any { return func() any { return Ok[any, any]("x") } }
	cfg := map[string]any{
		"Routes": []any{liveRoute{path: "/", page: "home"}},
		"Api":    []any{apiRoute{method: "POST", pattern: "/hook", handler: h}},
	}
	pages, apis := collectLiveRoutes(cfg)
	if len(pages) != 1 || len(apis) != 1 {
		t.Fatalf("pages=%d apis=%d, want 1/1", len(pages), len(apis))
	}
}

// apiTestApp builds a minimal liveApp with the given api routes (mirrors the
// dispatch surface without the full cfg-assembly).
func apiTestApp(api []apiRoute) *liveApp {
	return &liveApp{
		init:       func(req any) any { return SkyTuple2{V0: map[string]any{"Page": "home"}, V1: cmdT{kind: "none"}} },
		update:     func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:       func(model any) any { return velement("div", nil, []any{vtext("home")}) },
		routes:     []liveRoute{{path: "/", page: "home"}},
		notFound:   "home",
		store:      newMemoryStore(30 * time.Minute),
		locker:     newSessionLocker(),
		msgTags:    map[string]int{},
		sessionTTL: 30 * time.Minute,
		api:        api,
	}
}

// TestServeAPI_RunsHandlerTaskAndRenders — regression for the SECOND bug in the
// same path: the handler is `Request -> Task Error Response`, so serveAPI must
// FORCE the task and unwrap the Result. An older serveAPI passed the un-run
// Task straight to the renderer, which reflected the thunk and wrote a raw
// pointer address (`0x…`) to the wire instead of the response body.
func TestServeAPI_RunsHandlerTaskAndRenders(t *testing.T) {
	// Sky-shaped handler: applied to req, returns a Task (a `func() any`
	// thunk) that yields Ok(Response).
	handler := func(req any) any {
		return func() any {
			return Ok[any, any](SkyResponse{
				Status:      200,
				Body:        "pong",
				ContentType: "text/plain; charset=utf-8",
			})
		}
	}
	app := apiTestApp([]apiRoute{{method: "GET", pattern: "/ping", handler: handler}})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if body != "pong" {
		t.Errorf("body = %q, want %q (the task must be RUN and the Response rendered, not the thunk)", body, "pong")
	}
	if strings.Contains(body, "0x") {
		t.Errorf("body %q looks like a raw pointer — serveAPI rendered the un-run task", body)
	}
}

// TestServeAPI_ForwardsHeadersQueryParams — regression for the THIRD gap in the
// same path: serveAPI built an ad-hoc `map[string]any` request, which
// `asSkyRequest` (struct-only) rejected — so every `Server.header` /
// `Server.queryParam` / `Server.param` returned Nothing. A Stripe-Signature
// webhook mounted via Std.Live.api could never read its signature header.
// serveAPI now builds a canonical SkyRequest and invokes the handler via
// SkyCall (as the Sky.Http.Server listener does), so all accessors work.
func TestServeAPI_ForwardsHeadersQueryParams(t *testing.T) {
	handler := func(req any) any {
		return func() any {
			sig := fmt.Sprintf("%v", Maybe_withDefault("MISSING", Server_header("X-Test-Sig", req)))
			q := fmt.Sprintf("%v", Maybe_withDefault("MISSING", Server_queryParam("x", req)))
			id := fmt.Sprintf("%v", Maybe_withDefault("MISSING", Server_param("id", req)))
			return Ok[any, any](SkyResponse{Status: 200, Body: sig + "|" + q + "|" + id})
		}
	}
	app := apiTestApp([]apiRoute{{method: "", pattern: "/u/:id", handler: handler}})

	req := httptest.NewRequest(http.MethodPost, "/u/42?x=hello", nil)
	req.Header.Set("X-Test-Sig", "abc123")
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)

	if got, want := rr.Body.String(), "abc123|hello|42"; got != want {
		t.Errorf("body = %q, want %q — header/query/param must reach the api handler", got, want)
	}
}

// TestServeAPI_TaskFailureRenders500 — a handler whose Task fails renders a 500
// with the Sky error message, not a crash or a leaked thunk.
func TestServeAPI_TaskFailureRenders500(t *testing.T) {
	handler := func(req any) any {
		return func() any {
			return Err[any, any](ErrUnexpected("boom"))
		}
	}
	app := apiTestApp([]apiRoute{{method: "", pattern: "/boom", handler: handler}})

	req := httptest.NewRequest(http.MethodPost, "/boom", nil)
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "boom") {
		t.Errorf("body = %q, want it to contain the error message %q", rr.Body.String(), "boom")
	}
}
