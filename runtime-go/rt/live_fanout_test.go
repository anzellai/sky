package rt

// Phase 1 multi-tab SSE fan-out (v0.18). These tests pin the
// per-connection registry + relay that make 2+ tabs of one session
// render in lockstep. Before Phase 1 a single shared sseCh handed each
// server-pushed frame to ONE random connection; now every frame reaches
// every live connection, and a dispatch mirrors to the OTHER tabs while
// excluding the originating one.
//
// Contract under test:
//   (a) fanOutFrame("") reaches EVERY registered connection.
//   (b) fanOutFrame(exceptTab) skips the matching connection only.
//   (c) hasSSEConnOtherThan gates the dispatch broadcast so a lone tab
//       pays nothing.
//   (d) unregisterSSEConn drops a connection.
//   (e) the relay drains sess.sseCh and fans each frame to all
//       connections, then exits when `done` closes.

import (
	"testing"
	"time"
)

// newFanoutSession builds a minimal session with the channels the
// fan-out relay + registry need. sid is empty (only used as a drop-
// metric label).
func newFanoutSession() *liveSession {
	return &liveSession{
		sseCh: make(chan sseFrame, sseChanBuffer),
		done:  make(chan struct{}),
	}
}

// recvFrame reads one frame from ch within a short deadline, or fails.
func recvFrame(t *testing.T, ch chan sseFrame, what string) sseFrame {
	t.Helper()
	select {
	case fr := <-ch:
		return fr
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: no frame within deadline", what)
		return sseFrame{}
	}
}

// expectNoFrame asserts ch has nothing buffered right now.
func expectNoFrame(t *testing.T, ch chan sseFrame, what string) {
	t.Helper()
	select {
	case fr := <-ch:
		t.Fatalf("%s: unexpected frame %q", what, fr.data)
	default:
	}
}

func TestFanOutFrame_ReachesAllConnections(t *testing.T) {
	sess := newFanoutSession()
	_, chA := sess.registerSSEConn("tabA")
	_, chB := sess.registerSSEConn("tabB")

	fr := sseFrame{event: "patches", data: "<F1>"}
	sess.fanOutFrame(fr, "")

	if got := recvFrame(t, chA, "tabA"); got.data != "<F1>" {
		t.Fatalf("tabA: want <F1>, got %q", got.data)
	}
	if got := recvFrame(t, chB, "tabB"); got.data != "<F1>" {
		t.Fatalf("tabB: want <F1>, got %q", got.data)
	}
}

func TestFanOutFrame_ExcludesOriginatingTab(t *testing.T) {
	sess := newFanoutSession()
	_, chA := sess.registerSSEConn("tabA")
	_, chB := sess.registerSSEConn("tabB")

	// A dispatch from tabA: mirror to others, exclude tabA.
	sess.fanOutFrame(sseFrame{event: "patches", data: "<clickA>"}, "tabA")

	expectNoFrame(t, chA, "tabA (excluded)")
	if got := recvFrame(t, chB, "tabB"); got.data != "<clickA>" {
		t.Fatalf("tabB: want <clickA>, got %q", got.data)
	}
}

func TestHasSSEConnOtherThan(t *testing.T) {
	sess := newFanoutSession()
	if sess.hasSSEConnOtherThan("tabA") {
		t.Fatal("no connections: want false")
	}
	sess.registerSSEConn("tabA")
	if sess.hasSSEConnOtherThan("tabA") {
		t.Fatal("only tabA connected: dispatch from tabA has no sibling; want false")
	}
	if !sess.hasSSEConnOtherThan("tabB") {
		t.Fatal("tabA connected, dispatch from tabB: want true")
	}
	if !sess.hasSSEConnOtherThan("") {
		t.Fatal("any connection with empty except: want true")
	}
	// A second tab makes the gate open for tabA too.
	sess.registerSSEConn("tabB")
	if !sess.hasSSEConnOtherThan("tabA") {
		t.Fatal("tabA + tabB connected, dispatch from tabA: want true")
	}
}

func TestUnregisterSSEConn_Drops(t *testing.T) {
	sess := newFanoutSession()
	idA, chA := sess.registerSSEConn("tabA")
	_, chB := sess.registerSSEConn("tabB")

	sess.unregisterSSEConn(idA)
	if sess.hasSSEConnOtherThan("tabB") {
		t.Fatal("after unregistering tabA, only tabB remains: hasSSEConnOtherThan(tabB) want false")
	}

	sess.fanOutFrame(sseFrame{event: "patch", data: "<F2>"}, "")
	expectNoFrame(t, chA, "tabA (unregistered)")
	if got := recvFrame(t, chB, "tabB"); got.data != "<F2>" {
		t.Fatalf("tabB: want <F2>, got %q", got.data)
	}
}

func TestSSERelay_FansIngressToAllConnections(t *testing.T) {
	sess := newFanoutSession()
	sess.ensureSSERelay()
	// ensureSSERelay is idempotent — a second call must not start a
	// second draining goroutine (which would steal frames).
	sess.ensureSSERelay()

	_, chA := sess.registerSSEConn("tabA")
	_, chB := sess.registerSSEConn("tabB")

	// A producer (runPerformBody / tick / pub-sub / WebSocket bridge)
	// writes to the ingress channel; the relay fans it to both tabs.
	sess.sseCh <- sseFrame{event: "patches", data: "<push>"}

	if got := recvFrame(t, chA, "tabA relay"); got.data != "<push>" {
		t.Fatalf("tabA relay: want <push>, got %q", got.data)
	}
	if got := recvFrame(t, chB, "tabB relay"); got.data != "<push>" {
		t.Fatalf("tabB relay: want <push>, got %q", got.data)
	}

	// Closing `done` (TTL evict / Delete) must stop the relay. After
	// this, ingress frames are no longer fanned out (best-effort check:
	// the relay goroutine returns; nothing asserts an exact race-free
	// stop here — the -race full suite covers the teardown ordering).
	close(sess.done)
}

func TestFanOutFrame_DropOnFullConnBufferDoesNotBlock(t *testing.T) {
	sess := newFanoutSession()
	_, chSlow := sess.registerSSEConn("slow")
	_, chFast := sess.registerSSEConn("fast")

	// Fill the slow connection's buffer without draining it.
	for i := 0; i < sseChanBuffer; i++ {
		chSlow <- sseFrame{event: "patch", data: "filler"}
	}

	// This fan-out must NOT block on the full slow buffer, and the fast
	// connection must still receive the frame.
	done := make(chan struct{})
	go func() {
		sess.fanOutFrame(sseFrame{event: "patches", data: "<live>"}, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fanOutFrame blocked on a full connection buffer")
	}
	if got := recvFrame(t, chFast, "fast"); got.data != "<live>" {
		t.Fatalf("fast: want <live>, got %q", got.data)
	}
}
