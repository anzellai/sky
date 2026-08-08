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
	"strings"
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

// skyLiveBindingRecord is the emitted Go shape of a Std.Persist.LiveBinding —
//
//	{ store : Int, coll : String, schema : String, plan : String
//	, run : () -> Task Error (model -> model) }
//
// as a typed struct with PascalCase fields, which is what the compiler emits and
// what recordField's reflect path decodes. Using the REAL record shape (rather
// than a hand-built reactiveBindingRT) is the point: reactiveBindingsFor silently
// `continue`s past any binding whose Run field it cannot find, so a renamed or
// mistyped field drops the binding with no error, no log, and a page that paints
// once and then never updates. The decode assertion below is what turns that
// silent drop into a test failure.
type skyLiveBindingRecord struct {
	Store  int64
	Coll   string
	Schema string
	Plan   string
	Run    any
}

// TestHandleInitial_ReactiveApp_EmbeddedBinding_EndToEnd — the gate Phase-4
// claimed ("2-browser live demo") but never actually had, headless.
//
// A REAL embedded backend and a REAL LiveBinding record, driven through the REAL
// HTTP entry point, then asserted at the CLIENT boundary: the frame must arrive
// on the per-connection channel returned by registerSSEConn, not on sess.sseCh.
// sess.sseCh is UPSTREAM of the fan-out relay — asserting there proves the frame
// was enqueued, not that any client would ever receive it.
//
// Covers the whole chain, all of which was unreachable before G1 because the
// initial render never completed:
//
//	handleInitial → setupSubscriptions → ensureReactiveStarted → reactiveLoop
//	  → WatchTenant subscription → initial fill (paint-then-fill)
//	→ Embedded_put write → engine change feed → reactiveRefreshOnce
//	  → persistAndShipFrame → sess.sseCh → relay → per-connection channel
func TestHandleInitial_ReactiveApp_EmbeddedBinding_EndToEnd(t *testing.T) {
	// The RG#2 boot-gate is a process-global sync.Once that the deadlock test
	// above already consumed. Without this re-arm, every gate assertion below
	// would be silently void rather than merely weak.
	resetReactiveGateForTest()

	b, storeID := openReactiveBackend(t)
	b.Register(schemaFor(t))

	// The view renders the model's order-id list, so the rendered body literally
	// contains each id the query returns — the pre-fill paint and the post-write
	// repaint are directly distinguishable.
	view := func(model any) any {
		ids, _ := model.([]string)
		kids := make([]any, 0, len(ids))
		for _, id := range ids {
			kids = append(kids, velement("li", nil, []any{vtext(id)}))
		}
		return velement("ul", nil, kids)
	}

	// queried fires on every run of the binding's re-query closure. The reactive
	// loop opens its WatchTenant subscription BEFORE its initial fill, so
	// observing the first run proves the subscription is live — which removes the
	// race between handleInitial returning and the write below.
	queried := make(chan struct{}, 16)
	binding := skyLiveBindingRecord{
		Store:  storeID,
		Coll:   "orders",
		Schema: reactiveTestSchema,
		Plan:   "",
		Run: func(_ any) any {
			return SkyTask[any, any](func() SkyResult[any, any] {
				ids := queryOpenOrderIDs(t, b)
				select {
				case queried <- struct{}{}:
				default:
				}
				fold := func(_ any) any { return any(ids) }
				return Ok[any, any](any(fold))
			})
		},
	}

	app := &liveApp{
		init:             func(req any) any { return SkyTuple2{V0: any([]string{}), V1: cmdT{kind: "none"}} },
		update:           func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:             view,
		subscriptions:    func(model any) any { return nil },
		store:            newMemoryStore(30 * time.Minute),
		locker:           newSessionLocker(),
		msgTags:          map[string]int{},
		skyIDPrefix:      "r",
		reactiveBindings: func(model any) any { return []any{binding} },
	}

	// ── 1. The initial render, under the same deadlock guard ────────────────
	type initialResult struct {
		code   int
		cookie string
		body   string
	}
	done := make(chan initialResult, 1)
	go func() {
		rr := httptest.NewRecorder()
		app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		done <- initialResult{rr.Code, rr.Header().Get("Set-Cookie"), rr.Body.String()}
	}()
	var got initialResult
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: handleInitial did not return within 5s on a REAL embedded reactive binding")
	}
	if got.code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", got.code)
	}
	// Paint-then-fill: the response is the PRE-fill paint (empty list). It must
	// be a real render, and must not already contain the row written later.
	if !strings.Contains(got.body, "<ul") {
		t.Fatalf("initial paint did not render the view: %q", got.body)
	}
	if strings.Contains(got.body, "e2e-1") {
		t.Fatalf("initial paint already contains the not-yet-written row: %q", got.body)
	}

	sess, ok := app.store.Get(sidFromSetCookie(t, got.cookie))
	if !ok {
		t.Fatal("session not created by handleInitial")
	}

	// ── 2. The binding actually decoded (the silent-drop hazard) ────────────
	// sess.model is read under sess.mu: the reactive loop is already running and
	// writes it from reactiveRefreshOnce. (Production reads it the same way — the
	// unlocked read inside setupSubscriptions is safe only because its caller
	// holds the lock, which is the contract G1 wrote down.)
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()
	bindings := app.reactiveBindingsFor(model)
	if len(bindings) != 1 {
		t.Fatalf("LiveBinding record decoded to %d bindings, want 1 — reactiveBindingsFor "+
			"silently skips a binding whose Run field it cannot resolve, so a renamed/mistyped "+
			"record field drops reactivity with no error and no log", len(bindings))
	}
	if bindings[0].store != storeID || bindings[0].coll != "orders" {
		t.Fatalf("binding decoded to the wrong values: %+v (want store=%d coll=orders)", bindings[0], storeID)
	}
	if kind := reactiveDataBackendKind(bindings); kind != "embedded" {
		t.Fatalf("RG#2 gate classified the data backend as %q, want \"embedded\" — a misclassified "+
			"backend means the boot gate guards the wrong hazard", kind)
	}

	// ── 3. Attach a client connection, exactly as handleSSE does ────────────
	sess.ensureSSERelay()
	_, connCh, _ := sess.registerSSEConn("tabA")

	// The loop subscribes before its initial fill, so the first query run means
	// the subscription is live and the write below cannot be missed.
	select {
	case <-queried:
	case <-time.After(5 * time.Second):
		t.Fatal("the reactive loop never ran its initial fill — the binding was started but never queried")
	}

	// ── 4. A write must reach the CLIENT connection ─────────────────────────
	putRowAs(t, sess, storeID, `{"id":"e2e-1","status":"open"}`)

	frame, ok := awaitFrameContaining(connCh, "e2e-1", 5*time.Second)
	if !ok {
		t.Fatalf("the write never reached the client connection within 5s. The frame must travel "+
			"reactiveRefreshOnce → persistAndShipFrame → sess.sseCh → relay → per-connection "+
			"channel; last frame seen: %q", frame.data)
	}

	// And the session's rendered body reflects the write (query-scoped repaint).
	sess.mu.Lock()
	body := sess.lastShippedBody
	sess.mu.Unlock()
	if !strings.Contains(body, "e2e-1") {
		t.Fatalf("session body did not repaint with the new row: %q", body)
	}
}
