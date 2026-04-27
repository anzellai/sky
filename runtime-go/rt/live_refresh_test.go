package rt

// Regression test for the refresh-404 bug: a Sky.Live app with an
// empty `routes` list (single-page app) must serve GET / on every
// request — including refreshes that hit an existing session.
//
// Before the matchAnyRoute single-page fallback, the !routed &&
// existing 404 guard in handleInitial fired on every refresh because
// matchAnyRoute returns false for ALL paths when app.routes is
// empty. examples/19-skyforum's `routes = []` reproduces this:
// first GET /  → 200, then second GET /  → 404 page not found.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshOnEmptyRoutesServesRoot(t *testing.T) {
	// Minimal SPA: init returns string model, view returns trivial VNode.
	initFn := func(req any) any {
		return SkyTuple2{V0: "model-v1", V1: cmdT{kind: "none"}}
	}
	updateFn := func(msg, model any) any {
		return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
	}
	viewFn := func(model any) any {
		return velement("div", nil, []any{vtext("hi")})
	}
	subsFn := func(model any) any {
		return nil
	}
	app := &liveApp{
		init:          initFn,
		update:        updateFn,
		view:          viewFn,
		subscriptions: subsFn,
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
		// routes intentionally empty — single-page mode.
	}

	// First GET / — should create the session and return 200.
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	rr1 := httptest.NewRecorder()
	app.handleInitial(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first GET /: status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	cookie := rr1.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatalf("first GET / set no Set-Cookie header")
	}

	// Second GET / with the session cookie — refresh.
	// Before the fix, this 404'd because matchAnyRoute returned false
	// for "/" (no routes declared) and the existing-session 404 guard
	// fired.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Cookie", cookie)
	rr2 := httptest.NewRecorder()
	app.handleInitial(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("refresh GET /: status = %d (want 200), body = %s",
			rr2.Code, rr2.Body.String())
	}

	// Third GET / — same. A real browser refreshes more than once.
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("Cookie", cookie)
	rr3 := httptest.NewRecorder()
	app.handleInitial(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("third GET /: status = %d, body = %s", rr3.Code, rr3.Body.String())
	}
}

// Browser noise paths (favicons, devtools probes) must still 404 even
// in single-page mode — they shouldn't render the SPA, and they
// shouldn't wipe the session's handlers either.
func TestRefreshOnEmptyRoutesStillRejectsNoise(t *testing.T) {
	initFn := func(req any) any {
		return SkyTuple2{V0: "m", V1: cmdT{kind: "none"}}
	}
	updateFn := func(msg, model any) any {
		return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
	}
	viewFn := func(model any) any {
		return velement("div", nil, []any{vtext("hi")})
	}
	subsFn := func(model any) any { return nil }
	app := &liveApp{
		init:          initFn,
		update:        updateFn,
		view:          viewFn,
		subscriptions: subsFn,
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
	}

	// Browser noise — should 404 before session creation regardless
	// of routes shape.
	for _, p := range []string{"/favicon.ico", "/.well-known/foo", "/apple-touch-icon.png"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		app.handleInitial(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("noise path %s: status = %d, want 404", p, rr.Code)
		}
	}
}
