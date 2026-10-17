package rt

import (
	"sync"
	"testing"
	"time"
)

// TestTimeEveryTickAfterStopDoesNotDispatch: a Sub.every tick that woke
// before an `update` stopped its timer must not reach `update` after it.
//
// The tick goroutine takes sess.mu to dispatch. An `update Stop` that removes
// the Sub.every holds sess.mu and stops the timer (applyEverySubsDiff runs
// inside dispatch). A tick waiting on the lock used to take it next and
// dispatch `Tick` anyway, so the model advanced after the app had stopped
// the timer: the islands e2e read `ticks=248` after its stop and then saw
// `ticks=249`. Here the test holds sess.mu as the stopping update would,
// starts a tick, stops the timer, and releases the lock. The tick's `toMsg`
// must never run. The verdict does not depend on scheduling: whether the
// tick reached the lock before or after the stop, the stop is visible to it
// once it holds the lock.
func TestTimeEveryTickAfterStopDoesNotDispatch(t *testing.T) {
	app, sess := timeEveryFixture()
	var mu sync.Mutex
	calls := 0
	toMsg := func(ms int64) any {
		mu.Lock()
		calls++
		mu.Unlock()
		return "tick"
	}
	cancel := make(chan struct{})

	sess.mu.Lock() // the stopping update holds the session lock
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		app.timeEveryTick(sess, toMsg, time.Now(), cancel)
	}()
	time.Sleep(20 * time.Millisecond) // let the tick block on the lock (not required for the verdict)
	close(cancel)                     // the update stops the timer
	sess.mu.Unlock()

	select {
	case <-tickDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick never returned")
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Fatalf("a tick of a stopped timer reached update (%d call(s) of its toMsg)", got)
	}

	// Control: a tick of a running timer still dispatches.
	running := make(chan struct{})
	app.timeEveryTick(sess, toMsg, time.Now(), running)
	mu.Lock()
	got = calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("a tick of a running timer must dispatch once, got %d", got)
	}
	reportAssertions(t, 2)
}
