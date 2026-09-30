//go:build !js

package rt

import (
	"sync"
	"testing"
	"time"
)

// fakeSource is a subSource with one consumer: a second claim while the first
// is held is refused, and overlapping readers are recorded.
type fakeSource struct {
	mu        sync.Mutex
	claimed   bool
	reading   int
	overlap   bool
	events    chan any
	claimsSum int
}

func (f *fakeSource) claimSub() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed {
		return errFakeClaimed
	}
	f.claimed = true
	f.claimsSum++
	return nil
}

func (f *fakeSource) releaseSub() {
	f.mu.Lock()
	f.claimed = false
	f.mu.Unlock()
}

func (f *fakeSource) pump(stop <-chan struct{}, emit func(ev any) bool) {
	f.mu.Lock()
	f.reading++
	if f.reading > 1 {
		f.overlap = true
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.reading--
		f.mu.Unlock()
	}()
	for {
		select {
		case <-stop:
			return
		case ev := <-f.events:
			if !emit(ev) {
				return
			}
		}
	}
}

type fakeClaimErr struct{}

func (fakeClaimErr) Error() string { return "fake source already claimed" }

var errFakeClaimed error = fakeClaimErr{}

// The generic runner (both Sky.Live and the Cli/Tui/Webview manager start
// runners through startSourceRunner): a source whose holder is STOPPING is
// handed over to the next runner once the holder released; a holder that is
// still running is a real second consumer and is refused. The two runners
// never read the source at the same time.
func TestSourceRunnerHandsOverAStoppingClaim(t *testing.T) {
	src := &fakeSource{events: make(chan any)}
	const key = "fake:handover"

	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	hook := func(k string) {
		if k != key {
			return
		}
		once.Do(func() {
			close(entered)
			<-gate
		})
	}
	testHookSourceBeforeRelease.Store(&hook)
	defer testHookSourceBeforeRelease.Store(nil)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	got := make(chan any, 4)
	deliver := func(msg any, stop <-chan struct{}) bool {
		select {
		case got <- msg:
			return true
		case <-stop:
			return false
		}
	}
	first, err := startSourceRunner(key, src, nil, deliver, nil)
	if err != nil {
		t.Fatalf("first claim refused: %v", err)
	}
	// A second consumer while the first still runs is refused.
	if _, err := startSourceRunner(key, src, nil, deliver, nil); err == nil {
		t.Fatal("a second consumer of a running source was accepted")
	}

	first.cancel()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped runner never reached its release")
	}
	// Stopped reading, still holding the claim: a re-request is handed over.
	second, err := startSourceRunner(key, src, func(ev any) any { return ev }, deliver, nil)
	if err != nil {
		t.Fatalf("a source re-requested while its stopped runner was releasing was refused: %v", err)
	}
	defer second.cancelAndWait(5 * time.Second)
	// And while the handover is pending the successor holds the entry: a
	// third consumer is refused.
	if _, err := startSourceRunner(key, src, nil, deliver, nil); err == nil {
		t.Fatal("a third consumer was accepted during a handover")
	}

	openGate()
	select {
	case src.events <- "after-handover":
	case <-time.After(5 * time.Second):
		t.Fatal("the handed-over runner never read the source")
	}
	select {
	case msg := <-got:
		if msg != "after-handover" {
			t.Fatalf("delivered %v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handed-over runner never delivered")
	}
	src.mu.Lock()
	overlap, claims := src.overlap, src.claimsSum
	src.mu.Unlock()
	if overlap {
		t.Fatal("two runners read the source at the same time")
	}
	if claims != 2 {
		t.Fatalf("claims = %d, want 2 (first runner, then the handover)", claims)
	}
}

// A runner dropped while it still waits for its predecessor never claims, and
// the runner after it still waits for the predecessor's release (the handover
// chain is ordered).
func TestSourceRunnerDroppedDuringHandoverNeverClaims(t *testing.T) {
	src := &fakeSource{events: make(chan any)}
	const key = "fake:chain"

	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	hook := func(k string) {
		if k != key {
			return
		}
		once.Do(func() {
			close(entered)
			<-gate
		})
	}
	testHookSourceBeforeRelease.Store(&hook)
	defer testHookSourceBeforeRelease.Store(nil)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	deliver := func(msg any, stop <-chan struct{}) bool { return true }
	first, err := startSourceRunner(key, src, nil, deliver, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.cancel()
	<-entered
	middle, err := startSourceRunner(key, src, nil, deliver, nil)
	if err != nil {
		t.Fatalf("handover refused: %v", err)
	}
	middle.cancel()
	last, err := startSourceRunner(key, src, nil, deliver, nil)
	if err != nil {
		t.Fatalf("handover after a dropped successor refused: %v", err)
	}
	defer last.cancelAndWait(5 * time.Second)
	select {
	case <-middle.done:
		t.Fatal("a successor ended before its predecessor released")
	case <-time.After(50 * time.Millisecond):
	}
	openGate()
	if !middle.cancelAndWait(5 * time.Second) {
		t.Fatal("the dropped successor never ended")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		src.mu.Lock()
		claims, claimed := src.claimsSum, src.claimed
		src.mu.Unlock()
		if claims == 2 && claimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("claims = %d (claimed %v), want the first and the last runner only", claims, claimed)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
