package rt

// dispatch always returns the rendered body. The byte-equality short-
// circuit that used to live inside dispatch was load-bearing on map
// iteration order being random — once renderVNode's attr/event
// emission was sorted (v0.15.x deterministic-HTML fix) the byte-
// equality check started firing through legitimate keypress dispatch
// paths, freezing live editing. Suppression of identical-view SSE
// pushes is now the SSE producer's responsibility (it compares the
// returned body to sess.lastShippedBody — Cycle 3 P39 split — before
// encoding a frame).
//
// What this file pins:
//   * dispatch returns a non-empty body for both the initial dispatch
//     AND for repeat dispatches over an identical view.
//   * dispatch returns DIFFERENT bodies when the view changes.

import (
	"testing"
)


// dispatchTestApp builds a minimal liveApp where view is identity
// over a model-string (so we can force view-stability by keeping the
// model unchanged).
func dispatchTestApp(viewResult VNode) *liveApp {
	return &liveApp{
		update: func(msg, model any) any {
			// Identity update: return (model, Cmd.none).
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return viewResult
		},
	}
}


func TestDispatch_returnsBodyForIdenticalView(t *testing.T) {
	vn := velement("div", nil, []any{vtext("hello")})
	app := dispatchTestApp(vn)
	sess := &liveSession{
		cancelSub: make(chan struct{}),
	}
	// First dispatch establishes the baseline body.
	first := app.dispatch(sess, "init")
	if first == "" {
		t.Fatalf("first dispatch must return body, got empty")
	}
	// Second dispatch with identical view returns the SAME body — not
	// "" — so the SSE producer can decide whether to ship it. (Pre-
	// v0.15.13 dispatch returned "" here; that contract froze keypress
	// dispatch under sorted-attr rendering.)
	second := app.dispatch(sess, "tick")
	if second == "" {
		t.Fatalf("repeat dispatch must return body, not '' (v0.15.13: keypress-freeze guard)")
	}
	if second != first {
		t.Fatalf("identical view must produce byte-identical body, got %q vs %q", first, second)
	}
}


func TestDispatch_emitsWhenViewChanges(t *testing.T) {
	// Return a different VNode each time the view fn is called.
	counter := 0
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			counter++
			return velement("div", nil,
				[]any{vtext("count " + itoa(counter))})
		},
	}
	sess := &liveSession{cancelSub: make(chan struct{})}
	first := app.dispatch(sess, "m1")
	second := app.dispatch(sess, "m2")
	if first == "" || second == "" {
		t.Fatalf("both dispatches must return bodies when view changes: %q / %q", first, second)
	}
	if first == second {
		t.Fatalf("distinct views must render distinct bodies, got %q == %q", first, second)
	}
}


