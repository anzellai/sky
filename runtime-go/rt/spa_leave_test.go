package rt

import (
	"strconv"
	"strings"
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

// A navigation that started before the client ran (the boot loader recorded
// it in __skyLeavingUntil) holds the client's first requests, such as SpaInit_.
func TestSpaLeaveGateAdoptsTheBootLoadersLeavingTime(t *testing.T) {
	var g spaLeaveGate
	now := time.UnixMilli(1_000_000)
	g.adoptBoot(now, float64(now.Add(4*time.Second).UnixMilli()))
	d, stay := g.hold(now)
	if d != 4*time.Second || stay == nil {
		t.Fatalf("hold = %v, %v; want the boot loader's 4s", d, stay)
	}
	// Later events still count: a back/forward cache restore releases it.
	g.stayed()
	if d, _ := g.hold(now); d != 0 {
		t.Fatalf("pageshow did not release the boot hold: %v", d)
	}
	// A second beforeunload after the client started extends the window.
	g.adoptBoot(now, float64(now.Add(time.Second).UnixMilli()))
	g.leave(now.Add(500 * time.Millisecond))
	if d, _ := g.hold(now.Add(500 * time.Millisecond)); d != spaLeaveWindow {
		t.Fatalf("beforeunload after boot: hold %v", d)
	}
}

func TestSpaLeaveGateIgnoresAnEndedOrMissingBootTime(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	for _, ms := range []float64{0, -1, float64(now.Add(-time.Second).UnixMilli()), float64(now.UnixMilli())} {
		var g spaLeaveGate
		g.adoptBoot(now, ms)
		if d, stay := g.hold(now); d != 0 || stay != nil {
			t.Fatalf("boot time %v: hold %v, %v", ms, d, stay)
		}
	}
	// A time past the window (a clock that moved) is clamped to the window.
	var g spaLeaveGate
	g.adoptBoot(now, float64(now.Add(time.Hour).UnixMilli()))
	if d, _ := g.hold(now); d != spaLeaveWindow {
		t.Fatalf("clamp: hold %v", d)
	}
}

// The boot loader and the Go gate use one window, and the loader listens
// before the wasm starts.
func TestSpaBootLoaderRecordsLeavingBeforeTheWasmStarts(t *testing.T) {
	want := "__skyLeavingUntil = Date.now() + " + strconv.FormatInt(spaLeaveWindow.Milliseconds(), 10)
	i := strings.Index(spaBootLoaderJS, want)
	if i < 0 {
		t.Fatalf("spaBootLoaderJS does not set %q", want)
	}
	listen := strings.Index(spaBootLoaderJS, `addEventListener("beforeunload"`)
	start := strings.Index(spaBootLoaderJS, "instantiateStreaming")
	if listen < 0 || start < 0 || listen > start {
		t.Fatal("the beforeunload listener must be added before the wasm starts")
	}
	if !strings.Contains(spaBootLoaderJS, "if (e.persisted) globalThis.__skyLeavingUntil = 0") {
		t.Fatal("only a back/forward cache restore may end the boot loader's leaving time")
	}
}

// Sky.Live keeps the same rule: only a back/forward cache restore ends the
// leaving time, never the first pageshow of a load.
func TestLiveClientEndsLeavingOnlyOnABfcacheRestore(t *testing.T) {
	if !strings.Contains(liveClientCoreJS, "if (e.persisted) __skyLeavingUntil = 0;") {
		t.Fatal("the Sky.Live pageshow handler must end the leaving time only when persisted")
	}
	if strings.Contains(liveClientCoreJS, "\n  __skyLeavingUntil = 0;") {
		t.Fatal("the Sky.Live client still ends the leaving time on every pageshow")
	}
}
