// live_time_every_panic_test.go — the Time.every ticker survives a panicking
// tick, and the session mutex survives with it.
//
//	live-time-every-survives-a-panic   TestTimeEveryTickerSurvivesAPanic
//	                                   TestTimeEveryPanicLeavesTheSessionMutexAcquirable
//
// # The defect
//
// `setupSubscriptions` spawned the Time.every goroutine with NO recover, and
// its tick took `sess.mu.Lock()` with a MANUAL `sess.mu.Unlock()` after the
// dispatch. `app.dispatch` runs generated Sky code, so any panic that reaches
// it — a compiler defect, a kernel edge case, a nil map write in a handler —
// did two things at once:
//
//  1. killed the ticker goroutine permanently, and
//  2. left `sess.mu` LOCKED for the lifetime of the process.
//
// (2) is the worse half. Every later dispatch, every SSE resync, and every
// user interaction on that session blocks forever on a mutex nobody will
// release. The user sees a tab that is simply frozen, on Sky's pinned default
// app shape, and nothing anywhere says why.
//
// It is also why a per-cycle recover ALONE would have been the wrong fix: it
// converts a permanent wedge into a different permanent wedge — the loop
// survives and every tick after the first blocks on the mutex the panicking
// tick never released. The lock discipline (`defer sess.mu.Unlock()` in
// timeEveryDispatch) is the substance; the recover is what makes the loop live
// long enough for it to matter.
//
// Both tests drive `app.runTimeEvery` — the real production loop, with the
// real periodic.Config — so a future edit that rewires it cannot leave these
// gates testing a copy.
//
// Fixture isolation: nothing touches the filesystem; sessions are constructed
// in-process.
package rt

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// panickingEvery builds a `toMsg` that panics on the Nth tick. It is a
// function value, so `isFunc` sends it through `sky_call` INSIDE the locked
// region of timeEveryDispatch — which is precisely where the shipped defect
// left the mutex held.
type panickingEvery struct {
	mu      sync.Mutex
	ticks   int
	panicOn map[int]bool
}

func (p *panickingEvery) toMsg() any {
	return func(ms int64) any {
		p.mu.Lock()
		p.ticks++
		n := p.ticks
		p.mu.Unlock()
		if p.panicOn[n] {
			panic(fmt.Sprintf("injected Time.every panic on tick %d (ms=%d)", n, ms))
		}
		return "tick"
	}
}

func (p *panickingEvery) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ticks
}

// timeEveryFixture wires a session and an app minimal enough that a tick runs
// end to end: `sky_call(toMsg, …)` fires inside the locked region, then
// `app.dispatch` runs and returns "" on a nil model, so no frame ships and the
// SSE channel stays out of the picture. The defect under test is upstream of
// any of that.
//
// `cancelSub` is initialised because `dispatch` reaches `setupSubscriptions`,
// which closes and rotates it — the two production construction sites
// (live.go's session builder and live_store.go's restore) both `make` it, so a
// fixture that left it nil would be testing a state the runtime never reaches.
// It is deliberately NOT the channel the test cancels with: the loop captures
// its `cancel` once at setup, and rotateCancelSub replacing sess.cancelSub
// under it is the normal per-dispatch behaviour, not an exit signal.
func timeEveryFixture() (*liveApp, *liveSession) {
	sess := &liveSession{
		sid:       "time-every-panic-fixture",
		inputSeqs: map[string]int64{},
		sseCh:     make(chan sseFrame, 8),
		cancelSub: make(chan struct{}),
	}
	app := &liveApp{}
	return app, sess
}

// mutexAcquirableWithin reports whether sess.mu can be taken inside `budget`.
// It is the whole point of the exercise: "was the mutex released" is a fact
// about the mechanism, not a duration budget that a slow machine can flatter.
// The budget only bounds the failure — a wedged mutex is wedged forever, so
// any budget at all distinguishes it from a healthy one.
func mutexAcquirableWithin(sess *liveSession, budget time.Duration) bool {
	got := make(chan struct{})
	go func() {
		sess.mu.Lock()
		sess.mu.Unlock()
		close(got)
	}()
	select {
	case <-got:
		return true
	case <-time.After(budget):
		return false
	}
}

// TestTimeEveryPanicLeavesTheSessionMutexAcquirable — the assertion that
// matters most in this file.
//
// A tick panics INSIDE the locked region. Afterwards the session mutex must
// still be acquirable, because timeEveryDispatch releases it with `defer`.
// Under the shipped code the panic escaped the manual `sess.mu.Unlock()` and
// this hangs until the budget expires.
func TestTimeEveryPanicLeavesTheSessionMutexAcquirable(t *testing.T) {
	app, sess := timeEveryFixture()
	p := &panickingEvery{panicOn: map[int]bool{1: true}}

	cancel := make(chan struct{})
	done := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		// Stands in for the absent recover the shipped goroutine had — it is
		// here so the defect lands as a failed assertion rather than as a
		// crashed test binary, which is indistinguishable from a harness
		// fault. Under the fixed code nothing ever reaches it.
		defer func() {
			_ = recover()
			close(loopDone)
		}()
		app.runTimeEvery(sess, p.toMsg(), 5*time.Millisecond, cancel, done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for p.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	n := 0

	n++
	if p.count() < 1 {
		t.Fatal("the Time.every tick never fired — the fixture is not exercising the loop")
	}

	n++
	if !mutexAcquirableWithin(sess, 3*time.Second) {
		t.Error("sess.mu is still held after a tick panicked inside the locked region.\n" +
			"The session is wedged for the lifetime of the process: every later dispatch, " +
			"every SSE resync and every user interaction blocks on a mutex nobody will " +
			"release, and the user sees a permanently frozen tab. timeEveryDispatch must " +
			"take the lock with `defer sess.mu.Unlock()`, so the unlock runs on the " +
			"panicking path too.")
	}

	close(cancel)
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the Time.every loop did not return after cancel closed")
	}

	reportAssertions(t, n)
}

// TestTimeEveryTickerSurvivesAPanic — a panic costs THAT TICK, not the ticker.
//
// The assertion that discriminates is the one about ticks 2 and 3. "Tick 1
// panicked" is true under the broken and the fixed shape alike; only "the
// ticker was still running afterwards" tells them apart.
func TestTimeEveryTickerSurvivesAPanic(t *testing.T) {
	app, sess := timeEveryFixture()
	p := &panickingEvery{panicOn: map[int]bool{1: true}}

	cancel := make(chan struct{})
	done := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(loopDone)
		}()
		app.runTimeEvery(sess, p.toMsg(), 5*time.Millisecond, cancel, done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for p.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := p.count()
	close(cancel)
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the Time.every loop did not return after cancel closed")
	}

	n := 0

	n++
	if got < 3 {
		t.Errorf("the Time.every ticker fired %d tick(s) after a panic in its first one, want >= 3.\n"+
			"The panic killed the goroutine: this session's Time.every subscription is dead for "+
			"the lifetime of the process. The recover must be scoped to ONE TICK "+
			"(periodic.Guard, via periodic.Every), never wrapped around the ticker loop.", got)
	}

	n++
	if _, ok := warnLogged("live.time-every.cycle_panicked"); !ok {
		t.Error("no warn logged for the panicked tick — a recover that discards what it " +
			"caught is how a dead ticker produces no evidence at all")
	}

	reportAssertions(t, n)
}

// TestTimeEveryStopsOnEitherChannel — the loop's two exits. `cancelSub` is
// recreated by every setupSubscriptions call, so a session deleted BETWEEN
// dispatches is only reachable through `done`; losing either exit leaks a
// goroutine pushing to an unread sseCh for the process lifetime (Cycle 3 P36 /
// Gap C4). periodic.Config.AlsoStop exists for exactly this pair.
func TestTimeEveryStopsOnEitherChannel(t *testing.T) {
	n := 0
	for _, which := range []string{"cancelSub", "done"} {
		app, sess := timeEveryFixture()
		p := &panickingEvery{}
		cancel := make(chan struct{})
		done := make(chan struct{})
		loopDone := make(chan struct{})
		go func() {
			app.runTimeEvery(sess, p.toMsg(), 2*time.Millisecond, cancel, done)
			close(loopDone)
		}()
		time.Sleep(10 * time.Millisecond)
		if which == "cancelSub" {
			close(cancel)
		} else {
			close(done)
		}
		n++
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Errorf("the Time.every loop ignored a closed %s channel — the goroutine "+
				"leaks and keeps pushing to an unread sseCh", which)
		}
	}
	reportAssertions(t, n)
}
