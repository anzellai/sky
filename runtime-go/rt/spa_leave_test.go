package rt

import (
	"testing"
	"time"
)

func TestSpaLeaveGateDoesNotHoldAPageThatStays(t *testing.T) {
	var g spaLeaveGate
	now := time.Unix(1000, 0)
	if d, _ := g.hold(now); d != 0 {
		t.Fatalf("a page that never left holds for %v", d)
	}
	calls := 0
	g.wait(func() time.Time { return now }, func(time.Duration) <-chan time.Time { calls++; return nil })
	if calls != 0 {
		t.Fatal("wait armed a timer for a page that is not leaving")
	}
}

func TestSpaLeaveGateHoldsAttemptsForTheWindow(t *testing.T) {
	var g spaLeaveGate
	now := time.Unix(1000, 0)
	g.leave(now)
	d, stay := g.hold(now.Add(time.Second))
	if d != spaLeaveWindow-time.Second || stay == nil {
		t.Fatalf("hold = %v, %v", d, stay)
	}
	// The wait sleeps for the rest of the window, then lets the attempt go.
	clock := now.Add(time.Second)
	var slept []time.Duration
	g.wait(func() time.Time { return clock }, func(d time.Duration) <-chan time.Time {
		slept = append(slept, d)
		clock = clock.Add(d)
		ch := make(chan time.Time, 1)
		ch <- clock
		return ch
	})
	if len(slept) != 1 || slept[0] != spaLeaveWindow-time.Second {
		t.Fatalf("slept %v", slept)
	}
	if d, _ := g.hold(clock); d != 0 {
		t.Fatalf("the window did not end: %v", d)
	}
}

func TestSpaLeaveGatePageshowReleasesWaitingAttempts(t *testing.T) {
	var g spaLeaveGate
	g.leave(time.Now())
	done := make(chan struct{})
	go func() {
		g.wait(time.Now, func(time.Duration) <-chan time.Time { return nil }) // only stay() can end it
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("an attempt started while the page was leaving")
	case <-time.After(50 * time.Millisecond):
	}
	g.stayed()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pageshow did not release the waiting attempt")
	}
	if d, _ := g.hold(time.Now()); d != 0 {
		t.Fatal("still leaving after pageshow")
	}
}

func TestSpaLeaveGateSecondBeforeunloadExtendsTheWindow(t *testing.T) {
	var g spaLeaveGate
	now := time.Unix(1000, 0)
	g.leave(now)
	g.leave(now.Add(8 * time.Second))
	if d, _ := g.hold(now.Add(12 * time.Second)); d != 6*time.Second {
		t.Fatalf("hold = %v", d)
	}
}
