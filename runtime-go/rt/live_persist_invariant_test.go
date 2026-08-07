package rt

// Phase-5d (grill B3): a STRUCTURAL tripwire for the persist-before-ack invariant.
//
// Every path that mutates the session Model and ACKs it (ships an SSE frame) must
// call app.store.Set BEFORE the ack — else a crash loses a change the user already
// saw succeed (grill A1). Go has no package-private funnel to enforce this at
// compile time, so this test PINS the set of frame-shipping sites in live.go:
// adding a new one trips this test, forcing the author to wire persist-before-ack
// (or confirm the site re-sends already-persisted state).
//
// Audited sites (each covered):
//   sess.sseCh <- frame  (5): handleEvent, runPerformBody, the Time.every tick,
//       runSubscriberDispatch, runStreamSubscriberDispatch — the async paths call
//       store.Set immediately before the send; handleEvent persists at ~:4575.
//   fanOutFrame (3): the sky-nav mirror (persist ~:4213), the sendBeacon batch
//       flush (persist ~:4575), and ensureSSERelay (a relay of ALREADY-shipped
//       frames — no new Model mutation, so no persist needed).

import (
	"os"
	"strings"
	"testing"
)

func TestPersistBeforeAck_EmitSiteTripwire(t *testing.T) {
	src, err := os.ReadFile("live.go")
	if err != nil {
		t.Fatalf("read live.go: %v", err)
	}
	s := string(src)

	const wantSseCh, wantFanout = 5, 3

	gotSseCh := strings.Count(s, "sess.sseCh <- frame")
	// Call sites use `<recv>.fanOutFrame(`; the method definition is `) fanOutFrame(`
	// (no leading dot), so counting `.fanOutFrame(` matches only the call sites.
	gotFanout := strings.Count(s, ".fanOutFrame(")

	if gotSseCh != wantSseCh || gotFanout != wantFanout {
		t.Fatalf(`persist-before-ack emit-site count changed (sess.sseCh<-frame %d→%d, fanOutFrame %d→%d).

A frame-shipping site was added or removed in live.go. INVARIANT (grill A1): any
path that mutates the session Model and ships a frame MUST call app.store.Set
BEFORE the send, or a crash loses a change the user saw acked.

If you ADDED an emit site: wire persist-before-ack (copy the pattern in
runPerformBody), OR confirm it only re-sends already-persisted state (a relay /
initial render), THEN update the expected counts in this test with a note.`,
			wantSseCh, gotSseCh, wantFanout, gotFanout)
	}
}
