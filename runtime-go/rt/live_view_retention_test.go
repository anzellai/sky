package rt

import (
	"strconv"
	"testing"
	"time"
)

func genSession() *liveSession { return &liveSession{} }

func record(s *liveSession, view, hid string, msg any) {
	s.handlers = map[string]any{hid: msg}
	s.recordRenderGeneration(view)
}

// A burst: 40 taps queued on render v0, each processed tap renders again. The
// 40th tap must still resolve against v0 (it was made there), not be refused.
func TestHandlerRetention_ABurstResolvesAgainstTheRenderItWasMadeOn(t *testing.T) {
	s := genSession()
	record(s, "v0", "r.0#button.click", "Tap0")
	for i := 1; i <= 40; i++ {
		record(s, "v"+strconv.Itoa(i), "r.0#button.click", "Tap"+strconv.Itoa(i))
	}
	m, ok := s.resolveHandler("v0", "r.0#button.click")
	if !ok || m != "Tap0" {
		t.Fatalf("a tap made on render v0, 40 renders ago and seconds old, must resolve against v0; got %v %v", m, ok)
	}
}

// The window is bounded: never more than liveHandlerCap renders held.
func TestHandlerRetention_IsCapped(t *testing.T) {
	s := genSession()
	for i := 0; i < liveHandlerCap+50; i++ {
		record(s, "v"+strconv.Itoa(i), "h", i)
	}
	if n := len(s.handlerGens); n > liveHandlerCap {
		t.Fatalf("held %d renders, cap is %d", n, liveHandlerCap)
	}
}

// Past the always-kept window, an OLD render is released: a click on it is a
// desync (ok=false), never another render's Msg.
func TestHandlerRetention_OldRendersPastTheWindowAreReleased(t *testing.T) {
	s := genSession()
	record(s, "old", "h", "Old")
	s.handlerGens[0].at = time.Now().Add(-2 * liveHandlerRecent)
	for i := 0; i < liveHandlerHistory; i++ {
		record(s, "v"+strconv.Itoa(i), "h", i)
	}
	if _, ok := s.resolveHandler("old", "h"); ok {
		t.Fatalf("a render past the %d-render window and older than %s must be released", liveHandlerHistory, liveHandlerRecent)
	}
}
