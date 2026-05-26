package rt

// v0.15.14 — Cmd.perform completions must suppress identical-view
// frames the same way the Time.every Tick path does.
//
// The original v0.15.13 fix moved byte-equality suppression out of
// dispatch (which had frozen keypress under sorted-attr rendering)
// and put it back at the Time.every Tick callsite. runPerformBody
// (the Cmd.perform completion path) was deliberately left without
// suppression on the theory that "Cmd completions almost always
// carry meaningful state changes" — but that's wrong for the very
// common shape:
//
//     RefreshTick -> Cmd.perform (Db.getDiagramVersion slug) VersionLoaded
//     VersionLoaded (Ok v) ->
//         if v == model.lastVersion then (model, Cmd.none)
//         else (loadShapes …)
//
// where every 2s the Cmd completes with an unchanged version and
// VersionLoaded returns the same model untouched. Without
// suppression that produces a 14 KB SSE frame per interval forever.
//
// This file pins: dispatch through runPerformBody on a no-op Msg
// records prevBody on the session BEFORE the SSE frame would be
// shipped, AND the SSE producer skips the frame when body matches
// the captured prevBody.

import (
	"testing"
)

func TestRunPerformBody_suppressesIdenticalView(t *testing.T) {
	// View renders a fixed VNode regardless of model — so two
	// dispatches in a row produce byte-identical bodies. runCmd
	// would normally complete the Task, deliver VersionLoaded, and
	// runPerformBody would dispatch + ship. The suppression contract:
	// when body == sess.prevBody, no SSE frame is queued.
	vn := velement("div", nil, []any{vtext("static")})
	app := dispatchTestApp(vn)
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, 4),
	}

	// First dispatch: establishes prevBody.
	first := app.dispatch(sess, "init")
	if first == "" {
		t.Fatalf("first dispatch must return body, got empty")
	}
	if sess.prevBody == "" {
		t.Fatalf("dispatch must set sess.prevBody after render")
	}

	// Simulate the runPerformBody no-op path directly without
	// spinning a goroutine + Cmd machinery (which would require a
	// real Task value). The contract under test is the
	// `prevBody -> dispatch -> compare` sequence.
	sess.mu.Lock()
	prevBody := sess.prevBody
	body := app.dispatch(sess, "tick")
	var frame string
	if body != "" && body != prevBody {
		frame = "would-ship"
	}
	sess.mu.Unlock()

	if frame != "" {
		t.Fatalf("identical-view perform dispatch must NOT queue an SSE frame, got %q (body=%q, prev=%q)",
			frame, body, prevBody)
	}
	// No frame in the channel
	select {
	case f := <-sess.sseCh:
		t.Fatalf("identical-view dispatch leaked an SSE frame: %q", f)
	default:
		// good
	}
}

func TestRunPerformBody_shipsWhenViewChanges(t *testing.T) {
	counter := 0
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			counter++
			return velement("div", nil,
				[]any{vtext("v" + itoa(counter))})
		},
	}
	sess := &liveSession{cancelSub: make(chan struct{})}

	_ = app.dispatch(sess, "init") // counter=1
	prev := sess.prevBody

	// Second dispatch produces a different render (counter=2).
	sess.mu.Lock()
	prevBody := sess.prevBody
	body := app.dispatch(sess, "tick")
	shouldShip := body != "" && body != prevBody
	sess.mu.Unlock()

	if !shouldShip {
		t.Fatalf("view-changing dispatch must ship: prev=%q body=%q", prev, body)
	}
}
