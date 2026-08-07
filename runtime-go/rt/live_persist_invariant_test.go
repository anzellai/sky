package rt

// Phase-5d (grill B3): STRUCTURAL enforcement of the persist-before-ack funnel.
//
// Every path that mutates the session Model and ACKs it (ships an SSE frame) must
// persist the session BEFORE the ack — else a crash loses a change the user saw
// succeed (grill A1). Rather than a per-site count, the async server-initiated
// frames now ship through exactly ONE helper, app.persistAndShipFrame, which
// persists-before-ack. So there is exactly ONE raw `case sess.sseCh <- frame:`
// send in live.go, and it is inside persistAndShipFrame. A new raw send trips this
// test — route it through the funnel instead of skipping the persist.
//
// (The 3 fanOutFrame broadcast sites are the multi-connection paths: the sky-nav
// mirror and the batch flush persist separately just before fanning out, and
// ensureSSERelay re-sends ALREADY-shipped frames — no new Model mutation. They are
// pinned so a new broadcast site gets reviewed for persist-before-ack.)

import (
	"os"
	"strings"
	"testing"
)

func TestPersistBeforeAck_FunnelIsSoleSender(t *testing.T) {
	src, err := os.ReadFile("live.go")
	if err != nil {
		t.Fatalf("read live.go: %v", err)
	}
	s := string(src)

	// Exactly one raw async send, and it lives in the funnel.
	if got := strings.Count(s, "case sess.sseCh <- frame:"); got != 1 {
		t.Fatalf(`raw "case sess.sseCh <- frame:" sends = %d, want 1 (the persistAndShipFrame funnel).

A frame-shipping site was added or removed. INVARIANT (grill A1/B3): async
server-initiated frames must ship through app.persistAndShipFrame, which
persists-before-ack. If you added an emit path, route it through the funnel
(don't raw-send); it cannot then forget to persist.`, got)
	}
	funnel := strings.Index(s, "func (app *liveApp) persistAndShipFrame(")
	send := strings.Index(s, "case sess.sseCh <- frame:")
	if funnel < 0 || send < funnel {
		t.Fatalf("the sole sseCh send is not inside persistAndShipFrame (funnel@%d send@%d) — it may skip persist-before-ack", funnel, send)
	}

	// Pin the fanOutFrame broadcast sites (call sites use `.fanOutFrame(`; the
	// method def is `) fanOutFrame(`, no leading dot).
	if got := strings.Count(s, ".fanOutFrame("); got != 3 {
		t.Fatalf(`fanOutFrame call sites = %d, want 3.

A broadcast site was added/removed. It must persist-before-ack (like the sky-nav
mirror / batch flush) or be a relay of already-persisted frames (ensureSSERelay),
then update this count with a note.`, got)
	}
}
