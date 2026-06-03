package rt

// Sky.Live framework namespace reservation — `/_sky/*` belongs to the
// runtime (event POST, SSE, console, metrics, healthz, readyz, buildinfo,
// etc). Specific /_sky/* endpoints are registered EXACT-match on the
// mux and never reach dispatchRoot. Anything that DOES reach
// dispatchRoot under /_sky/* is an UNMOUNTED framework path —
// e.g. a typoed `/_sky/conslole` or a probe for `/_sky/foo`. The fix
// (v0.16.1 PR 1) returns a plain 404 there, never the user's notFound
// page. Before this fix, dispatchRoot fell through to `handleInitial`
// on GET/HEAD, which routed the request through `applyRoute` →
// `app.notFound`, rendering the app's branded NotFoundPage. That's
// info leakage + irrelevant UX for a framework path the user never
// owned.
//
// What this file pins:
//   - GET /_sky/foo                  → 404 (NOT user's notFound HTML)
//   - GET /_sky/console/missing      → 404 (same)
//   - GET /api/test (custom apiRoute) → handled by api route (regression check)
//   - GET /random-path               → user's notFound page renders (regression check)

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// notFoundMarker is a unique string emitted by the test app's view
// when the model.Page is set to the user's notFound sentinel. Tests
// assert this marker is ABSENT from /_sky/* 404 bodies and PRESENT
// in unhandled /random-path bodies. The marker contains no special
// HTML chars so it survives `vtext`'s escaping unchanged.
const notFoundMarker = "TESTUSERnNOTFOUNDPAGEsentinel"

// namespaceTestApp builds a minimal liveApp with:
//   - One api route: GET /api/test → returns 200 + "api-ok".
//   - One Live page route: "/" → HomePage sentinel.
//   - A notFound sentinel string the view renders verbatim so the
//     test can check whether the user's notFound surface leaked.
func namespaceTestApp() *liveApp {
	app := &liveApp{
		// init returns ({Page: "home"}, Cmd.none). The view dispatches
		// on Page; the user-notFound case emits notFoundMarker.
		init: func(req any) any {
			model := map[string]any{"Page": "home"}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			page := ""
			if m, ok := model.(map[string]any); ok {
				if p, ok := m["Page"].(string); ok {
					page = p
				}
			}
			if page == "notFound" {
				// Marker is emitted INSIDE a text node so it survives
				// HTML rendering verbatim.
				return velement("div", nil,
					[]any{vtext(notFoundMarker)})
			}
			return velement("div", nil, []any{vtext("home page " + page)})
		},
		routes: []liveRoute{
			{path: "/", page: "home"},
		},
		notFound:   "notFound",
		store:      newMemoryStore(30 * time.Minute),
		locker:     newSessionLocker(),
		msgTags:    map[string]int{},
		sessionTTL: 30 * time.Minute,
		api: []apiRoute{
			{
				method:  "GET",
				pattern: "/api/test",
				handler: func(req any) any {
					return "api-ok"
				},
			},
		},
	}
	return app
}

// TestDispatchRoot_ReservedSkyNamespace_ReturnsPlain404 — the smoking
// gun for #438-class bugs. An unmounted /_sky/foo path MUST NOT render
// the user's notFound page.
func TestDispatchRoot_ReservedSkyNamespace_ReturnsPlain404(t *testing.T) {
	app := namespaceTestApp()

	cases := []string{
		"/_sky/foo",
		"/_sky/console/missing",
		"/_sky/conslole",  // typoed probe — the original #438 surface
		"/_sky/notamount/deep/path",
	}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		app.dispatchRoot(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", p, rr.Code)
		}
		body := rr.Body.String()
		if strings.Contains(body, notFoundMarker) {
			t.Errorf("%s: response body contains user's notFound marker %q — namespace guard breached.\nBody: %s",
				p, notFoundMarker, body)
		}
		// http.NotFound emits a plain text body. Sanity-check the body
		// looks like the stdlib's plain-404 ("404 page not found\n").
		if !strings.Contains(body, "404") {
			t.Errorf("%s: expected plain stdlib 404 body, got %q", p, body)
		}
	}
}

// TestDispatchRoot_ApiRoute_StillServed — regression check that the
// reserved-path guard doesn't break api routes. A registered apiRoute
// at /api/test must still serve as before.
func TestDispatchRoot_ApiRoute_StillServed(t *testing.T) {
	app := namespaceTestApp()

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("api route returned %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "api-ok") {
		t.Errorf("api route body = %q, want it to contain %q", rr.Body.String(), "api-ok")
	}
}

// TestDispatchRoot_UserNotFound_StillRenders — regression check that
// non-/_sky/* unhandled paths still render the user's notFound page.
// The reserved-path guard MUST be scoped to /_sky/* only — generic
// unknown paths keep the existing Sky.Live behaviour.
func TestDispatchRoot_UserNotFound_StillRenders(t *testing.T) {
	app := namespaceTestApp()

	req := httptest.NewRequest(http.MethodGet, "/random-path", nil)
	rr := httptest.NewRecorder()
	app.dispatchRoot(rr, req)

	// The user's notFound page IS the intended response for arbitrary
	// non-/_sky paths. We assert the notFoundMarker is PRESENT
	// (proving the user's view ran) — the HTTP status itself is
	// chosen by Sky.Live's existing initial-render path (which may
	// be 200 with notFound rendered as content). The key invariant is
	// that the user's notFound surface IS reachable for non-namespace
	// paths.
	if rr.Code >= 500 {
		t.Errorf("/random-path returned %d, want < 500 (user notFound page should render)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), notFoundMarker) {
		t.Errorf("/random-path body missing notFoundMarker — the user's notFound page no longer renders for non-/_sky/* paths.\nBody snippet: %s",
			truncate(rr.Body.String(), 400))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
