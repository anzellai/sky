package rt

// v0.15.17 — runPerformBody must suppress SSE frames when the post-
// dispatch body is byte-identical to the body the client last
// received, must ship a frame when the view changes, and must
// advance the "last shipped" bookkeeping coherently across cycles.
//
// v0.15.14 (PR #85) added the suppression contract to runPerformBody
// + the Time.every callsite (live.go) without a Go-level regression
// test. Cycle 3 audit gap C1 residual #2 flagged the missing _test.go
// mate. This file lands the missing tests.
//
// Cycle 3 P39 (Gap C2) split the historical single `prevBody` field
// into two fields with distinct invariants:
//
//   - lastComputedBody — written by every dispatch with the just-
//     rendered HTML. Mirrors prevTree.
//   - lastShippedBody  — written only by the SSE-producing call
//     sites, and only when a frame is actually emitted to the wire
//     (sseCh enqueue or direct SSE response write).
//
// Suppression compares against lastShippedBody (what the client
// actually has), not lastComputedBody (what dispatch happens to
// have just rendered). This file pins both invariants.
//
// Test shapes mirror live_dispatch_noop_test.go but exercise the SSE
// producer rather than dispatch's return contract:
//
//   (a) Identical-view perform → no frame queued; lastShippedBody
//       unchanged; lastComputedBody advanced.
//   (b) View-changing perform → frame queued; lastShippedBody and
//       lastComputedBody both advanced to the new body.
//   (c) lastShippedBody advances coherently across cycles.
//   (d) Post-panic dispatch preserves lastComputedBody (rollback)
//       and never touches lastShippedBody.
//   (e) Suppressed dispatch advances lastComputedBody but leaves
//       lastShippedBody untouched (the Gap C2 invariant — the two
//       fields are NOT in lockstep).

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

// performTestSession primes the session with a baseline render and
// mirrors the production initial-mount path (handleInitial) which
// writes BOTH lastComputedBody and lastShippedBody — the initial
// HTML response IS the page the client receives.
//
// Without seeding lastShippedBody too, the first runPerformBody's
// suppression check would compare a non-empty body against an empty
// lastShippedBody and always fire a frame, masking the suppression
// contract entirely.
func performTestSession(app *liveApp) *liveSession {
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, 16),
		model:     "initial",
	}
	// Baseline: dispatch once so prevTree + lastComputedBody match
	// the current view. dispatch writes lastComputedBody; mirror the
	// initial-mount path by also writing lastShippedBody so the test
	// starts in the same state as a freshly-mounted production
	// session would.
	body := app.dispatch(sess, "bootstrap")
	sess.lastShippedBody = body
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

// (a) Identical-view perform must NOT queue an SSE frame, and must
// leave lastShippedBody unchanged.
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
	// Sanity: baseline body cached in both fields.
	if sess.lastComputedBody == "" {
		t.Fatalf("baseline dispatch must populate sess.lastComputedBody")
	}
	if sess.lastShippedBody == "" {
		t.Fatalf("baseline mount must populate sess.lastShippedBody")
	}
	priorShipped := sess.lastShippedBody
	priorComputed := sess.lastComputedBody
	// Trivial task: returns the literal value 0. toMsg is identity.
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)
	// No frame should land on sseCh — view didn't move.
	if frame := drainFrame(sess, 20*time.Millisecond); frame != "" {
		t.Fatalf("identical-view perform must NOT queue an SSE frame, got %q", frame)
	}
	// lastShippedBody must be unchanged — nothing was actually shipped.
	if sess.lastShippedBody != priorShipped {
		t.Fatalf("identical-view perform mutated lastShippedBody: prior %q, now %q",
			priorShipped, sess.lastShippedBody)
	}
	// lastComputedBody must be the same value (dispatch re-rendered the
	// same view) — pinning that dispatch DID run and DID write the field.
	if sess.lastComputedBody != priorComputed {
		t.Fatalf("identical-view perform mutated lastComputedBody: prior %q, now %q",
			priorComputed, sess.lastComputedBody)
	}
}

// (b) View-changing perform MUST queue a frame and advance both fields.
//
// A Cmd.perform completion whose update changes the view (e.g.
// fetched data lands in model) ships an SSE frame so the client
// can re-render. Both lastComputedBody (dispatch wrote it) and
// lastShippedBody (the SSE producer wrote it under the same lock)
// advance to the new body.
func TestRunPerformBody_ViewChange_QueuesFrame(t *testing.T) {
	// Toggle: first view call returns "first", subsequent return "second".
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
	priorShipped := sess.lastShippedBody
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)
	frame := drainFrame(sess, 100*time.Millisecond)
	if frame == "" {
		t.Fatalf("view-changing perform MUST queue an SSE frame; got none")
	}
	// Both fields must have advanced — and they must agree (the SSE
	// producer ships whatever dispatch just computed).
	if sess.lastShippedBody == priorShipped {
		t.Fatalf("view changed but lastShippedBody did not advance: %q", priorShipped)
	}
	if sess.lastShippedBody != sess.lastComputedBody {
		t.Fatalf("after view-changing ship the two fields must agree: shipped=%q computed=%q",
			sess.lastShippedBody, sess.lastComputedBody)
	}
}

// (c) lastShippedBody advances coherently across cycles.
//
// Two consecutive view-changing perform cycles followed by a no-op
// cycle. Pins that lastShippedBody tracks the last body the client
// received so the suppression check on cycle 3 correctly fires.
func TestRunPerformBody_LastShippedAdvancesCoherently(t *testing.T) {
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
	bodyA := sess.lastShippedBody
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }

	// Cycle 1: A → B (view changes, frame ships, lastShippedBody = B).
	app.runPerformBody(sess, task, toMsg)
	if drainFrame(sess, 100*time.Millisecond) == "" {
		t.Fatalf("cycle 1: expected frame for A → B")
	}
	bodyB := sess.lastShippedBody
	if bodyB == bodyA {
		t.Fatalf("cycle 1: lastShippedBody must advance, A=%q B=%q", bodyA, bodyB)
	}

	// Cycle 2: B → C (frame ships, lastShippedBody = C).
	app.runPerformBody(sess, task, toMsg)
	if drainFrame(sess, 100*time.Millisecond) == "" {
		t.Fatalf("cycle 2: expected frame for B → C")
	}
	bodyC := sess.lastShippedBody
	if bodyC == bodyB {
		t.Fatalf("cycle 2: lastShippedBody must advance, B=%q C=%q", bodyB, bodyC)
	}

	// Cycle 3: C → C (view stable, no frame, lastShippedBody stays C).
	app.runPerformBody(sess, task, toMsg)
	if frame := drainFrame(sess, 20*time.Millisecond); frame != "" {
		t.Fatalf("cycle 3: identical-view perform must suppress; got %q", frame)
	}
	if sess.lastShippedBody != bodyC {
		t.Fatalf("cycle 3: lastShippedBody must remain C, got %q want %q",
			sess.lastShippedBody, bodyC)
	}
}

// (d) Post-panic dispatch preserves the prior prevTree +
// lastComputedBody, and never touches lastShippedBody.
//
// Cycle 3 audit gap C1 residual #3: when dispatch's recover fires
// (an update / view / runCmd panic), the prior valid prevTree and
// lastComputedBody MUST be restored so the next dispatch's diff
// baseline is the last successfully-rendered view. Without this
// preservation, a post-panic dispatch's baseline drifts: prevTree
// may point at a partial-render new tree (line 2594 ran before the
// panic) while lastComputedBody is still the older valid body.
// The next dispatch would then see desynced fields.
//
// lastShippedBody is independent — dispatch never writes it, so
// the panic-rollback path doesn't touch it either. This test pins
// that contract.
func TestDispatch_PanicPreservesPrevTreeAndLastComputed(t *testing.T) {
	panicArmed := false
	app := &liveApp{
		update: func(msg, model any) any {
			if panicArmed {
				panic("deliberate panic for invariant-preservation test")
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
	// Baseline dispatch: establishes prevTree + lastComputedBody.
	baselineBody := app.dispatch(sess, "bootstrap")
	if baselineBody == "" || sess.lastComputedBody != baselineBody {
		t.Fatalf("baseline must populate lastComputedBody, got body=%q lastComputedBody=%q",
			baselineBody, sess.lastComputedBody)
	}
	baselineTreePtr := sess.prevTree
	if baselineTreePtr == nil {
		t.Fatalf("baseline must populate prevTree")
	}
	// Also seed lastShippedBody (mirror the production initial-mount
	// path) so we can pin "panic recovery doesn't touch it".
	sess.lastShippedBody = baselineBody
	shippedSentinel := sess.lastShippedBody

	// Arm the panic and dispatch. Recover catches it; body returns "".
	panicArmed = true
	panicBody := app.dispatch(sess, "explode")
	if panicBody != "" {
		t.Fatalf("panic dispatch must yield empty body, got %q", panicBody)
	}
	// Critical invariant: prevTree + lastComputedBody must be restored.
	if sess.lastComputedBody != baselineBody {
		t.Fatalf("post-panic: lastComputedBody must be restored to baseline %q, got %q",
			baselineBody, sess.lastComputedBody)
	}
	if sess.prevTree != baselineTreePtr {
		t.Fatalf("post-panic: prevTree pointer must be restored to baseline %p, got %p",
			baselineTreePtr, sess.prevTree)
	}
	// lastShippedBody is dispatch's no-touch zone — the panic-
	// recovery path must not clobber it.
	if sess.lastShippedBody != shippedSentinel {
		t.Fatalf("post-panic: lastShippedBody is dispatch's no-touch zone; "+
			"prior %q, now %q", shippedSentinel, sess.lastShippedBody)
	}
}

// (e) Suppressed dispatch — dispatch advances lastComputedBody to the
// just-rendered value, but lastShippedBody stays at whatever the SSE
// producer last set. This is the Gap C2 invariant: the two fields
// are NOT in lockstep, and any future cleanup that re-fused them
// (e.g. "only write lastComputedBody when shipping") would silently
// break suppression of identical views.
//
// Scenario: a runPerformBody whose dispatch re-renders the SAME view
// the client already has must leave lastShippedBody at its prior
// value, while lastComputedBody gets the freshly-computed (identical)
// value. The two fields happen to be byte-equal here, but the
// SEMANTIC distinction (separate writers) is what the test pins.
func TestRunPerformBody_SuppressedDispatch_OnlyComputedAdvances(t *testing.T) {
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext("stable-view")})
	})
	sess := performTestSession(app)
	// Force a divergence so we can observe the "shipped is untouched"
	// invariant clearly: pretend the client last received a DIFFERENT
	// body. The next perform's dispatch will compute "stable-view"
	// (same as baseline lastComputedBody), but lastShippedBody is the
	// sentinel — suppression check (body != lastShippedBody) returns
	// "differ → ship". After the ship, lastShippedBody advances to
	// the just-shipped body.
	//
	// Then we run a SECOND perform with the same view. dispatch
	// re-renders the same HTML; lastComputedBody is overwritten with
	// the same value; suppression check fires (body == lastShippedBody);
	// lastShippedBody stays put.
	sess.lastShippedBody = "<sentinel-the-client-actually-has-this>"
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }

	// Cycle 1: dispatch's body differs from sentinel → ships.
	app.runPerformBody(sess, task, toMsg)
	if drainFrame(sess, 100*time.Millisecond) == "" {
		t.Fatalf("cycle 1: body differs from sentinel, must ship")
	}
	shippedAfterCycle1 := sess.lastShippedBody
	if shippedAfterCycle1 == "<sentinel-the-client-actually-has-this>" {
		t.Fatalf("cycle 1: lastShippedBody must advance off the sentinel")
	}
	computedAfterCycle1 := sess.lastComputedBody
	if computedAfterCycle1 != shippedAfterCycle1 {
		t.Fatalf("cycle 1: post-ship the two fields must agree: shipped=%q computed=%q",
			shippedAfterCycle1, computedAfterCycle1)
	}

	// Cycle 2: dispatch re-renders the same view → suppression fires;
	// lastComputedBody is rewritten with the SAME value; lastShippedBody
	// is untouched. The distinction matters: if a future refactor made
	// dispatch skip the lastComputedBody write on suppression, every
	// subsequent diff baseline would drift. Pin that dispatch always
	// writes lastComputedBody.
	preCycle2Computed := sess.lastComputedBody
	preCycle2Shipped := sess.lastShippedBody
	app.runPerformBody(sess, task, toMsg)
	if frame := drainFrame(sess, 20*time.Millisecond); frame != "" {
		t.Fatalf("cycle 2: identical-view perform must suppress, got frame %q", frame)
	}
	// lastShippedBody untouched.
	if sess.lastShippedBody != preCycle2Shipped {
		t.Fatalf("cycle 2: suppressed dispatch must NOT mutate lastShippedBody: "+
			"prior %q, now %q", preCycle2Shipped, sess.lastShippedBody)
	}
	// lastComputedBody re-written (to the same value — dispatch always writes).
	if sess.lastComputedBody != preCycle2Computed {
		t.Fatalf("cycle 2: dispatch must write lastComputedBody (idempotently): "+
			"prior %q, now %q", preCycle2Computed, sess.lastComputedBody)
	}
}
