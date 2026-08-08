package rt

// live_reactive_initial_render_test.go — G1. THE INITIAL-RENDER LAYER.
//
// live_reactive_test.go / live_reactive_delivery_test.go drive app.reactiveLoop
// DIRECTLY — below handleInitial → setupSubscriptions → ensureReactiveStarted.
// live_nav_mirror_test.go drives handleInitial correctly but with
// reactiveBindings == nil. The Phase-4b deadlock lives in the INTERSECTION of
// those two fixtures, and no test occupied it: every initial page load of every
// reactive Sky.Live app hung forever while the suite stayed green.
//
// These tests close that gap. They drive the REAL HTTP entry point on an app
// that declares reactive bindings, under a timeout, so a re-introduced sess.mu
// re-entry below setupSubscriptions FAILS in 5s instead of hanging the suite.
//
// See docs/bluedb/g1-reactive-deadlock-fix-design.md.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// resetReactiveGateForTest re-arms the process-global RG#2 boot-gate
// (bluedb_reactive_gate.go: reactiveGateOnce, a sync.Once). Without this the
// FIRST test in the process that reaches ensureReactiveStarted consumes the
// Once, and every later test's gate arms silently never evaluate — the coverage
// would be void rather than merely weak. Test-only: it lives in a _test.go file
// so no emitted user project can reach it.
func resetReactiveGateForTest() { reactiveGateOnce = sync.Once{} }

// newReactiveTestApp is newMirrorTestApp (live_nav_mirror_test.go) plus the ONE
// difference that matters: the app declares reactive bindings, i.e. the Sky
// surface `Live.withReactive` / `Persist.liveInto` sets app.reactiveBindings.
func newReactiveTestApp(bindings func(model any) any) *liveApp {
	app := newMirrorTestApp()
	app.reactiveBindings = bindings
	return app
}

// TestHandleInitial_ReactiveApp_DoesNotDeadlock — G1's discovery artefact.
//
// handleInitial holds sess.mu (live.go:4176 — it guards renderVNode's write to
// sess.handlers against a concurrent Cmd.perform goroutine; a fatal "concurrent
// map writes" without it) and calls setupSubscriptions INSIDE that critical
// section. setupSubscriptions therefore has a hard contract: callers hold
// sess.mu, and nothing it calls may re-acquire it. Phase-4b hooked
// ensureReactiveStarted in, and that callee re-locked sess.mu to read
// sess.model. Go mutexes are not reentrant → self-deadlock on the same
// goroutine, on the very first request.
//
// An EMPTY binding list is deliberate and sufficient: the deadlock is upstream
// of any binding being read, so the test stays hermetic (no Pebble temp dir, no
// engine) and keeps the RG#2 gate on its backend=="" → nil branch so it can
// never os.Exit the test binary. The real-backend path is covered by
// TestHandleInitial_ReactiveApp_EmbeddedBinding_EndToEnd below.
func TestHandleInitial_ReactiveApp_DoesNotDeadlock(t *testing.T) {
	resetReactiveGateForTest()

	app := newReactiveTestApp(func(model any) any { return []any{} })

	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		done <- rr.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("GET / returned %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: handleInitial did not return within 5s — a callee below " +
			"setupSubscriptions re-acquired sess.mu (Go mutexes are not reentrant). " +
			"See docs/bluedb/g1-reactive-deadlock-fix-design.md")
	}
}
