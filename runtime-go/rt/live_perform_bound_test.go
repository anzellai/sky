package rt

// Concurrency-bound regression tests for runPerform (goroutine-audit B5).
//
// runPerform acquires a per-session permit (perfSem) and a global permit
// (performSemGlobal) around the user Task so a client that fires Cmd.perform
// faster than the effects complete cannot spawn unbounded concurrent effect
// work (DB conns / effect memory / CPU). These tests pin the two properties
// that make the bound real AND safe:
//
//   1. BOUND HOLDS — at most `cap` performs run their Task concurrently.
//   2. PROGRESS    — every submitted perform still completes; the bound
//      queues excess work, it never DROPS an effect.
//
// The naive test (an in-flight max-tracker, fire >cap, assert max<=cap)
// passes VACUOUSLY: if performs complete faster than they pile up, max never
// approaches cap even with NO semaphore. So each Task blocks on a barrier,
// forcing concurrency to accumulate — with the per-session acquire removed
// from runPerform, TestRunPerform_PerSessionConcurrencyBound reaches cap+extra
// and goes RED. That removal is the declared falsifier.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seedPerfSem forces the session's per-session perform cap to a deterministic
// size, independent of GOMAXPROCS (which perfSem() derives the real cap from).
// It completes performSemOnce so the production perfSem() returns this channel.
func seedPerfSem(sess *liveSession, n int) {
	sess.performSemOnce.Do(func() { sess.performSem = make(chan struct{}, n) })
}

// TestRunPerform_PerSessionConcurrencyBound: fire cap+extra performs whose
// Task blocks on a barrier. While the barrier is held, no more than `cap`
// may be in-flight; after release, ALL cap+extra must complete.
//
// FALSIFIER: delete the `case ssem <- struct{}{}` acquire in runPerform and
// this test goes RED — maxSeen reaches cap+extra.
func TestRunPerform_PerSessionConcurrencyBound(t *testing.T) {
	const cap = 2
	const extra = 3
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext("x")})
	})
	sess := performTestSession(app)
	sess.done = make(chan struct{}) // non-nil so the escape select is well-formed
	seedPerfSem(sess, cap)

	var inflight int64
	var maxSeen int64
	release := make(chan struct{})
	var wg sync.WaitGroup

	task := func() any {
		n := atomic.AddInt64(&inflight, 1)
		for { // record the running maximum
			m := atomic.LoadInt64(&maxSeen)
			if n <= m || atomic.CompareAndSwapInt64(&maxSeen, m, n) {
				break
			}
		}
		<-release // hold the permit until the test lets go
		atomic.AddInt64(&inflight, -1)
		return 0
	}
	toMsg := func(r any) any { return r }

	for i := 0; i < cap+extra; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.runPerform(sess, task, toMsg, context.Background())
		}()
	}

	// Wait until `cap` performs are actually parked on the barrier — proves
	// the cap FILLS (guards against a vacuous pass where nothing piled up).
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&inflight) < int64(cap) {
		select {
		case <-deadline:
			t.Fatalf("only %d performs reached the barrier; cap=%d never filled (vacuous)",
				atomic.LoadInt64(&inflight), cap)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Hold the barrier and confirm the bound is never breached.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&maxSeen); got > int64(cap) {
		t.Fatalf("concurrency bound breached: max in-flight %d > cap %d", got, cap)
	}
	if got := atomic.LoadInt64(&inflight); got > int64(cap) {
		t.Fatalf("in-flight %d exceeds cap %d while the barrier is held", got, cap)
	}

	// PROGRESS: release the barrier; every submitted perform must complete.
	close(release)
	completed := make(chan struct{})
	go func() { wg.Wait(); close(completed) }()
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatalf("performs did not all complete after release — an effect was dropped or deadlocked")
	}
	if got := atomic.LoadInt64(&maxSeen); got != int64(cap) {
		t.Fatalf("max in-flight should reach EXACTLY cap=%d (filled but never breached), got %d", cap, got)
	}
}

// TestRunPerform_SessionDoneEscapesParkedPermit: a perform parked waiting for
// a permit must exit promptly when its session is evicted (sess.done closed),
// rather than block forever. Pins the cancellation-gap fix.
//
// FALSIFIER: remove the `case <-sessDone: return` arm from either select in
// runPerform and the parked perform never returns → this test times out RED.
func TestRunPerform_SessionDoneEscapesParkedPermit(t *testing.T) {
	app := performTestApp(func(model any) any {
		return velement("div", nil, []any{vtext("x")})
	})
	sess := performTestSession(app)
	sess.done = make(chan struct{})
	seedPerfSem(sess, 1) // cap of 1 — the second perform must park

	block := make(chan struct{})
	holding := make(chan struct{})
	blockingTask := func() any {
		close(holding)
		<-block // occupy the only permit indefinitely
		return 0
	}
	toMsg := func(r any) any { return r }

	// Perform #1 takes the sole permit and holds it.
	go app.runPerform(sess, blockingTask, toMsg, context.Background())
	<-holding

	// Perform #2 parks waiting for the permit. Run it in a goroutine and
	// assert it RETURNS once we close sess.done — not after `block`.
	returned := make(chan struct{})
	go func() {
		app.runPerform(sess, func() any { return 0 }, toMsg, context.Background())
		close(returned)
	}()

	// Give #2 a moment to reach the parked select, then evict the session.
	time.Sleep(20 * time.Millisecond)
	close(sess.done)

	select {
	case <-returned:
		// Good — the parked perform escaped on sess.done.
	case <-time.After(2 * time.Second):
		t.Fatalf("parked perform did not exit after sess.done closed — cancellation-gap escape missing")
	}
	close(block) // let #1 finish so the test leaks nothing
}

// TestRunPerform_DependentPerformDoesNotDeadlock: with a per-session cap of 1,
// a perform whose completion's update emits ANOTHER Cmd.perform must still run
// BOTH. The fire-and-forget spawn (`go runPerform` in runCmd) means the parent
// releases its permit on return, before the child needs one — so the chain
// queues, it does not cycle. If the bound instead made the parent JOIN the
// child while holding the permit (the deadlock the old comment feared), the
// child would never acquire and this test would hang.
//
// FALSIFIER: a design that acquires the permit at the submit site under the
// parent's frame (holding it across the child spawn+run) would deadlock here.
func TestRunPerform_DependentPerformDoesNotDeadlock(t *testing.T) {
	var ranChild int64
	childDone := make(chan struct{})
	var closeOnce sync.Once
	toMsg := func(r any) any { return r } // identity: the Msg IS the task result

	// The child perform's Task records that it ran and tags its result "child".
	childTask := func() any {
		atomic.StoreInt64(&ranChild, 1)
		return "child"
	}
	// Msg-keyed state machine — NOT a dispatch counter (bootstrap must not be
	// mistaken for the parent, and every non-first dispatch must not re-close
	// the channel). The parent perform's result ("parent") emits the child
	// perform; the child's result ("child") signals completion exactly once.
	app := &liveApp{
		update: func(msg, model any) any {
			switch msg {
			case "parent":
				return SkyTuple2{V0: model, V1: cmdT{kind: "perform", task: childTask, toMsg: toMsg}}
			case "child":
				closeOnce.Do(func() { close(childDone) })
				return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
			default:
				return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
			}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("x")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 16),
		done:      make(chan struct{}),
		model:     "init",
		handlers:  map[string]any{},
	}
	sess.app.Store(app)
	app.dispatch(sess, "bootstrap") // seed prevTree; msg "bootstrap" -> none
	seedPerfSem(sess, 1)            // sole permit — the deadlock trap

	// Parent perform: its result "parent" makes update emit the child perform
	// (fire-and-forget). Under a cap of 1, the parent must release its permit
	// on return so the child can acquire it — no join, no cycle.
	go app.runPerform(sess, func() any { return "parent" }, toMsg, context.Background())

	select {
	case <-childDone:
		if atomic.LoadInt64(&ranChild) != 1 {
			t.Fatalf("child dispatch fired but child Task never ran")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("dependent perform deadlocked: child never ran under a cap of 1")
	}
}
