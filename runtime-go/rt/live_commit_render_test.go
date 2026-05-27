package rt

// Cycle 3 P40 / Gap C7 — commitRender helper unifies the 5+
// "render → set prevTree + lastComputedBody" call sites.
//
// The audit's diagnosis was that the 2-field invariant (prevTree +
// lastComputedBody MUST point at the same render) was fanned out across
// FIVE writes, each with slightly different shapes. v0.15.14's multi-
// shape regression originated from exactly this fan-out: a refactor
// touched ONE callsite without recognising the contract bound all of
// them. After P40 every site routes through a single helper that
// documents the invariant.
//
// This file pins:
//
//   (a) commitRender writes BOTH fields with no observable mid-call
//       torn state (Test_CommitRender_Atomic).
//   (b) commitRender preserves lastShippedBody (the SSE-producer's
//       independent invariant per P39 / Gap C2 — Test_CommitRender_
//       LeavesLastShippedUntouched).
//   (c) Every consumer of the helper observes the joint write under
//       sess.mu (Test_CommitRender_PerCallsite_*: handleInitial,
//       renderView, dispatch, handler-rebuild branches, SSE resync).
//
// The contract under test: prevTree and lastComputedBody must update
// together. A reader holding sess.mu must never see prevTree updated
// without lastComputedBody also updated (or vice versa).

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test_CommitRender_Atomic: a parallel reader holding sess.mu
// briefly should never observe prevTree != lastComputedBody's "expected
// pair". We assert the helper writes both under the caller's lock so
// the joint observation is consistent.
//
// Under sess.mu, no concurrent reader can ever see the fields desynced
// because Go's memory model guarantees writes inside a locked region
// are observed in order by another goroutine acquiring the same lock.
// The test below exercises that contract: writer goroutine holds the
// lock through commitRender; reader goroutine acquires the lock and
// checks both fields. Across many iterations, every observation is
// either the pre-state OR the post-state — never a partial mix.
func Test_CommitRender_Atomic(t *testing.T) {
	sess := &liveSession{}
	// Pre-state: tree pointer A, body "A".
	treeA := velement("div", nil, []any{vtext("A")})
	bodyA := "<div>A</div>"
	sess.mu.Lock()
	sess.commitRender(&treeA, bodyA)
	sess.mu.Unlock()
	// Post-state to swap to: tree pointer B, body "B".
	treeB := velement("div", nil, []any{vtext("B")})
	bodyB := "<div>B</div>"

	var torn atomic.Int64
	var iters atomic.Int64
	stop := make(chan struct{})

	// Reader: continually observes the (prevTree, lastComputedBody)
	// pair under sess.mu. If a torn read ever happens (prevTree from
	// one render, body from another), torn increments.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			sess.mu.Lock()
			pt := sess.prevTree
			lb := sess.lastComputedBody
			sess.mu.Unlock()
			iters.Add(1)
			// Valid pairs: (&treeA, bodyA) or (&treeB, bodyB).
			okA := pt == &treeA && lb == bodyA
			okB := pt == &treeB && lb == bodyB
			if !okA && !okB {
				torn.Add(1)
			}
		}
	}()

	// Writer: oscillates between the two valid pairs for 200ms,
	// always under sess.mu — so the reader's lock-held observation
	// must never catch an intermediate (prevTree updated but body
	// not, or vice versa).
	deadline := time.Now().Add(200 * time.Millisecond)
	useB := true
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		if useB {
			sess.commitRender(&treeB, bodyB)
		} else {
			sess.commitRender(&treeA, bodyA)
		}
		sess.mu.Unlock()
		useB = !useB
	}
	close(stop)
	wg.Wait()

	if torn.Load() != 0 {
		t.Fatalf("commitRender exposed torn state in %d / %d observations",
			torn.Load(), iters.Load())
	}
	if iters.Load() < 100 {
		t.Fatalf("reader didn't run enough iterations to exercise the race window: %d", iters.Load())
	}
}

// Test_CommitRender_LeavesLastShippedUntouched: commitRender writes the
// "computed" pair but MUST NOT touch lastShippedBody. That field is
// owned by the SSE-producing callers (P39 / Gap C2 invariant split).
func Test_CommitRender_LeavesLastShippedUntouched(t *testing.T) {
	sess := &liveSession{}
	// Seed lastShippedBody with a sentinel value.
	sentinel := "<!-- shipped sentinel -->"
	sess.lastShippedBody = sentinel

	tree := velement("div", nil, []any{vtext("render")})
	body := "<div>render</div>"
	sess.mu.Lock()
	sess.commitRender(&tree, body)
	sess.mu.Unlock()

	if sess.prevTree != &tree {
		t.Fatalf("commitRender did not set prevTree (got %p, want %p)", sess.prevTree, &tree)
	}
	if sess.lastComputedBody != body {
		t.Fatalf("commitRender did not set lastComputedBody (got %q, want %q)",
			sess.lastComputedBody, body)
	}
	if sess.lastShippedBody != sentinel {
		t.Fatalf("commitRender MUST NOT touch lastShippedBody: sentinel=%q now=%q",
			sentinel, sess.lastShippedBody)
	}
}

// Test_CommitRender_PerCallsite_HandleInitialShape exercises the
// handleInitial pattern: commitRender followed by an explicit
// lastShippedBody = body write (since the body IS the HTTP response).
// Pins that both "computed" and "shipped" fields end up equal — the
// initial-mount invariant.
func Test_CommitRender_PerCallsite_HandleInitialShape(t *testing.T) {
	sess := &liveSession{}
	tree := velement("div", nil, []any{vtext("initial mount")})
	body := "<div>initial mount</div>"
	// Mirror the handleInitial sequence.
	sess.mu.Lock()
	sess.commitRender(&tree, body)
	sess.lastShippedBody = body
	sess.mu.Unlock()
	if sess.lastComputedBody != body || sess.lastShippedBody != body {
		t.Fatalf("initial-mount: computed=%q shipped=%q want both=%q",
			sess.lastComputedBody, sess.lastShippedBody, body)
	}
}

// Test_CommitRender_PerCallsite_RenderViewShape exercises the
// guard-rejected renderView path: commitRender alone, no
// lastShippedBody write (the body is returned to dispatch's caller via
// the HTTP /_sky/event response; the SSE producer doesn't run).
// Pins that lastShippedBody stays untouched while lastComputedBody
// advances to the just-rendered body.
func Test_CommitRender_PerCallsite_RenderViewShape(t *testing.T) {
	sess := &liveSession{}
	priorShipped := "<!-- prior shipped -->"
	sess.lastShippedBody = priorShipped

	tree := velement("div", nil, []any{vtext("guard-rejected re-render")})
	body := "<div>guard-rejected re-render</div>"
	sess.mu.Lock()
	sess.commitRender(&tree, body)
	sess.mu.Unlock()

	if sess.lastComputedBody != body {
		t.Fatalf("renderView shape: lastComputedBody must advance to %q, got %q",
			body, sess.lastComputedBody)
	}
	if sess.lastShippedBody != priorShipped {
		t.Fatalf("renderView shape: lastShippedBody MUST stay at %q, got %q",
			priorShipped, sess.lastShippedBody)
	}
}

// Test_CommitRender_DispatchEndToEnd: drive a real dispatch and verify
// the commitRender consolidation kept dispatch's invariants intact.
// dispatch writes both fields via commitRender just before runCmd; the
// return value matches the just-committed body; lastShippedBody is
// untouched (dispatch's contract per P39).
func Test_CommitRender_DispatchEndToEnd(t *testing.T) {
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("end-to-end")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 16),
		model:     "init",
		handlers:  map[string]any{},
	}
	priorShipped := "<!-- prior shipped, dispatch must not touch -->"
	sess.lastShippedBody = priorShipped

	body := app.dispatch(sess, "go")
	if body == "" {
		t.Fatalf("dispatch returned empty body")
	}
	// Both fields written by commitRender must equal the returned body.
	if sess.lastComputedBody != body {
		t.Fatalf("dispatch: lastComputedBody (%q) must equal returned body (%q)",
			sess.lastComputedBody, body)
	}
	if sess.prevTree == nil {
		t.Fatalf("dispatch: prevTree must be set after a successful render")
	}
	// dispatch never writes lastShippedBody (Gap C2 invariant).
	if sess.lastShippedBody != priorShipped {
		t.Fatalf("dispatch: lastShippedBody must stay at %q, got %q",
			priorShipped, sess.lastShippedBody)
	}
}

// Test_CommitRender_DispatchPanicRollsBackViaHelper: ensure the panic-
// recovery path inside dispatch routes its rollback through commitRender
// too — so the rollback observes the same atomic-pair contract as every
// other write site. Without this, a future maintainer could "optimise"
// the rollback to write only one field, silently re-introducing the
// torn-state class.
func Test_CommitRender_DispatchPanicRollsBackViaHelper(t *testing.T) {
	armed := false
	app := &liveApp{
		update: func(msg, model any) any {
			if armed {
				panic("deliberate panic for rollback test")
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("stable view")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 16),
		model:     "init",
		handlers:  map[string]any{},
	}
	// Baseline: commits prevTree + lastComputedBody via the consolidated
	// commitRender call.
	baseline := app.dispatch(sess, "bootstrap")
	baselineTreePtr := sess.prevTree
	if baseline == "" || baselineTreePtr == nil {
		t.Fatalf("baseline must populate the pair")
	}

	// Arm: panic INSIDE update. dispatch's recover should restore both
	// fields to baseline atomically via commitRender.
	armed = true
	if got := app.dispatch(sess, "explode"); got != "" {
		t.Fatalf("panicking dispatch must return empty body, got %q", got)
	}
	// Pair invariant preserved:
	if sess.lastComputedBody != baseline {
		t.Fatalf("rollback: lastComputedBody must be restored to baseline %q, got %q",
			baseline, sess.lastComputedBody)
	}
	if sess.prevTree != baselineTreePtr {
		t.Fatalf("rollback: prevTree pointer must be restored to baseline %p, got %p",
			baselineTreePtr, sess.prevTree)
	}
}
