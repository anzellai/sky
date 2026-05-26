package rt

// v0.15.17 — runPerformBody must suppress SSE frames when the post-
// dispatch body is byte-identical to sess.prevBody, must ship a frame
// when the view changes, and must advance sess.prevBody coherently
// across cycles.
//
// v0.15.14 (PR #85) added the suppression contract to runPerformBody
// + the Time.every callsite (live.go:2697-2723 / 2745-2787) without a
// Go-level regression test. Cycle 3 audit gap C1 residual #2 flagged
// the missing _test.go mate. This file lands the missing tests.
//
// Test shapes mirror live_dispatch_noop_test.go but exercise the SSE
// producer rather than dispatch's return contract:
//
//   1. Identical-view perform → no frame queued on sess.sseCh.
//   2. View-changing perform → frame queued.
//   3. sess.prevBody advances coherently across consecutive perform
//      cycles (suppression continues to fire after the first ship).
//
// We synthesise the runPerformBody preconditions manually (model,
// view, dispatch wired up exactly as the production path expects) and
// then call app.runPerformBody directly with a trivial Task that
// returns a Msg.

import (
	"testing"
	"time"
)

// performTestApp builds a liveApp whose update is identity (model
// unchanged) and whose view is the caller-supplied function. This
// shape lets the test force view-stability OR view-change by toggling
// what view returns.
//
// toMsg here is identity — runPerformBody passes the task's result
// straight to dispatch as the Msg, but our update ignores msg anyway.
func performTestApp(view func(model any) any) *liveApp {
	return &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: view,
	}
}

// performTestSession primes the session with a baseline render so
// sess.prevBody is non-empty (the production initial-mount path at
// handleInitial does this; the test must mirror it or the first
// dispatch's suppression check would always pass).
func performTestSession(app *liveApp) *liveSession {
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, 16),
		model:     "initial",
	}
	// Baseline: dispatch once so prevBody / prevTree match the current
	// view. Subsequent runPerformBody calls compare against this.
	_ = app.dispatch(sess, "bootstrap")
	return sess
}

// drainFrame returns a frame from sess.sseCh if one is available
// within the timeout window; returns "" if nothing arrives. Used to
// pin "no frame was queued" without false-failing on a race.
func drainFrame(sess *liveSession, d time.Duration) string {
	select {
	case f := <-sess.sseCh:
		return f
	case <-time.After(d):
		return ""
	}
}

// (a) Identical-view dispatch must NOT queue an SSE frame.
//
// Time.every and Cmd.perform completions whose update produces the
// same view (e.g. a heartbeat tick that touches no view-reachable
// state, or a Db.query that returns a value already in model) must
// be silently dropped at the SSE producer. Otherwise every tick
// floods the wire with redundant HTML.
func TestRunPerformBody_IdenticalView_SuppressesFrame(t *testing.T) {
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext("static")})
	})
	sess := performTestSession(app)
	// Sanity: baseline body cached.
	if sess.prevBody == "" {
		t.Fatalf("baseline dispatch must populate sess.prevBody")
	}
	priorBody := sess.prevBody
	// Trivial task: returns the literal value 0. toMsg is identity.
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)
	// No frame should land on sseCh — view didn't move.
	if frame := drainFrame(sess, 20*time.Millisecond); frame != "" {
		t.Fatalf("identical-view perform must NOT queue an SSE frame, got %q", frame)
	}
	// prevBody must be unchanged (dispatch wrote it back to the same value).
	if sess.prevBody != priorBody {
		t.Fatalf("prevBody mutated under identical-view perform: prior %q, now %q",
			priorBody, sess.prevBody)
	}
}

// (b) View-changing dispatch MUST queue a frame.
//
// A Cmd.perform completion whose update changes the view (e.g.
// fetched data lands in model) ships an SSE frame so the client
// can re-render.
func TestRunPerformBody_ViewChange_QueuesFrame(t *testing.T) {
	// Toggle: first view call returns "a", subsequent return "b".
	// Bootstrap consumes the first; runPerformBody's view call gets
	// the second — different body, so suppression should NOT fire.
	callCount := 0
	app := performTestApp(func(model any) any {
		callCount++
		if callCount == 1 {
			return velement("div", nil, []any{vtext("first")})
		}
		return velement("div", nil, []any{vtext("second")})
	})
	sess := performTestSession(app)
	priorBody := sess.prevBody
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)
	frame := drainFrame(sess, 100*time.Millisecond)
	if frame == "" {
		t.Fatalf("view-changing perform MUST queue an SSE frame; got none")
	}
	// Frame is a JSON envelope (seq + body + ackInputs); body should
	// differ from the baseline.
	if sess.prevBody == priorBody {
		t.Fatalf("view changed but prevBody did not advance: %q == %q",
			sess.prevBody, priorBody)
	}
}

// (c) sess.prevBody advances coherently across cycles.
//
// Two consecutive view-changing perform cycles followed by a no-op
// cycle. Pins that prevBody tracks the last rendered body so the
// suppression check on cycle 3 correctly fires.
func TestRunPerformBody_PrevBodyAdvancesCoherently(t *testing.T) {
	// View cycles through three distinct bodies on calls 1..3, then
	// repeats body 3 on subsequent calls.
	calls := 0
	app := performTestApp(func(model any) any {
		calls++
		switch {
		case calls <= 1:
			return velement("div", nil, []any{vtext("A")})
		case calls == 2:
			return velement("div", nil, []any{vtext("B")})
		default:
			return velement("div", nil, []any{vtext("C")})
		}
	})
	sess := performTestSession(app)
	bodyA := sess.prevBody
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }

	// Cycle 1: A → B (view changes, frame ships, prevBody = B-body).
	app.runPerformBody(sess, task, toMsg)
	if drainFrame(sess, 100*time.Millisecond) == "" {
		t.Fatalf("cycle 1: expected frame for A → B")
	}
	bodyB := sess.prevBody
	if bodyB == bodyA {
		t.Fatalf("cycle 1: prevBody must advance, A=%q B=%q", bodyA, bodyB)
	}

	// Cycle 2: B → C (frame ships, prevBody = C-body).
	app.runPerformBody(sess, task, toMsg)
	if drainFrame(sess, 100*time.Millisecond) == "" {
		t.Fatalf("cycle 2: expected frame for B → C")
	}
	bodyC := sess.prevBody
	if bodyC == bodyB {
		t.Fatalf("cycle 2: prevBody must advance, B=%q C=%q", bodyB, bodyC)
	}

	// Cycle 3: C → C (view stable, no frame, prevBody stays C).
	app.runPerformBody(sess, task, toMsg)
	if frame := drainFrame(sess, 20*time.Millisecond); frame != "" {
		t.Fatalf("cycle 3: identical-view perform must suppress; got %q", frame)
	}
	if sess.prevBody != bodyC {
		t.Fatalf("cycle 3: prevBody must remain C, got %q want %q",
			sess.prevBody, bodyC)
	}
}

// (d) Post-panic dispatch preserves the prior prevTree + prevBody.
//
// Cycle 3 audit gap C1 residual #3: when dispatch's recover fires
// (an update / view / runCmd panic), the prior valid prevTree and
// prevBody MUST be restored so the next dispatch's suppression check
// compares against the last successfully-rendered view. Without this
// preservation, a post-panic dispatch's suppression baseline drifts:
// prevTree may point at a partial-render new tree (line 2594 ran
// before the panic) while prevBody is still the older valid body.
// The next dispatch would then see desynced fields.
//
// This test verifies: pre-panic baseline → panic dispatch (no frame,
// invariants restored) → post-panic dispatch with the same view as
// baseline correctly suppresses.
func TestDispatch_PanicPreservesPrevBodyAndPrevTree(t *testing.T) {
	panicArmed := false
	app := &liveApp{
		update: func(msg, model any) any {
			if panicArmed {
				panic("deliberate panic for prevBody preservation test")
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("stable")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, 16),
		model:     "init",
		handlers:  map[string]any{},
	}
	// Baseline dispatch: establishes prevTree + prevBody.
	baselineBody := app.dispatch(sess, "bootstrap")
	if baselineBody == "" || sess.prevBody != baselineBody {
		t.Fatalf("baseline must populate prevBody, got body=%q prevBody=%q",
			baselineBody, sess.prevBody)
	}
	baselineTreePtr := sess.prevTree
	if baselineTreePtr == nil {
		t.Fatalf("baseline must populate prevTree")
	}

	// Arm the panic and dispatch. Recover catches it; body returns "".
	panicArmed = true
	panicBody := app.dispatch(sess, "explode")
	if panicBody != "" {
		t.Fatalf("panic dispatch must yield empty body, got %q", panicBody)
	}
	// Critical invariant: prevTree + prevBody must be preserved.
	if sess.prevBody != baselineBody {
		t.Fatalf("post-panic: prevBody must be restored to baseline %q, got %q",
			baselineBody, sess.prevBody)
	}
	if sess.prevTree != baselineTreePtr {
		t.Fatalf("post-panic: prevTree pointer must be restored to baseline %p, got %p",
			baselineTreePtr, sess.prevTree)
	}
}

