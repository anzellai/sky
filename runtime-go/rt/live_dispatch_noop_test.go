package rt

// When a Time.every subscription ticks (or a Cmd.perform completes)
// but the resulting view is byte-identical to the previous one, the
// runtime must suppress the SSE push. Pre-fix, every tick dispatched
// a fresh HTML frame even when nothing observable changed — clients
// running `subscriptions = Time.every 500 Tick` received a diff
// twice a second forever.

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


func TestDispatch_suppressesDuplicateBody(t *testing.T) {
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
	// Second dispatch with identical view must suppress.
	second := app.dispatch(sess, "tick")
	if second != "" {
		t.Fatalf("repeat dispatch with identical view must return \"\" (no-op), got %q", second)
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


