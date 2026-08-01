package rt

import (
	"sync"
	"testing"
	"time"
)

// Test suite for #9 — drop-keyed inline resync. Written FIRST (TDD): it
// references the API being added (sseConn.outOfSync / .resync, map[uint64]*sseConn,
// markAllConnsOutOfSync, connOutOfSync, clearConnOutOfSync, renderResyncFrame) so
// the package fails to compile until the fix lands, then goes green.
//
// #9: an SSE frame dropped by a full server-side buffer (fanOutFrame egress-full,
// or an ingress-full producer) leaves the client silently diverged. The server
// already detects every drop; these tests pin that a drop FLAGS the affected
// connection(s) + signals an inline full-body resync, with zero false positives
// on a healthy connection.

// signalled reports whether a cap-1 resync channel has a pending wake.
func signalled(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func newResyncTestSession() *liveSession {
	return &liveSession{
		sseCh:     make(chan sseFrame, 1),
		cancelSub: make(chan struct{}),
	}
}

// 1. Egress drop (a full connection buffer) flags ONLY the dropped connection,
//    and signals its resync — while a healthy sibling is untouched.
func TestEgressDropFlagsOnlyDroppedConn(t *testing.T) {
	sess := newResyncTestSession()
	idFull, _, resyncFull := sess.registerSSEConn("tabA")
	idOK, chOK, resyncOK := sess.registerSSEConn("tabB")

	// Fill tabA's buffer to capacity so the next fanOut drops for it. tabB stays
	// drained.
	sess.sseConnMu.Lock()
	full := sess.sseConns[idFull]
	sess.sseConnMu.Unlock()
	for i := 0; i < cap(full.ch); i++ {
		full.ch <- sseFrame{data: "x"}
	}

	sess.fanOutFrame(sseFrame{data: "y"}, "")

	if !sess.connOutOfSync(idFull) {
		t.Fatal("the connection whose buffer was full must be flagged outOfSync")
	}
	if !signalled(resyncFull) {
		t.Fatal("the dropped connection's resync channel must be signalled")
	}
	if sess.connOutOfSync(idOK) {
		t.Fatal("a healthy sibling connection must NOT be flagged")
	}
	if signalled(resyncOK) {
		t.Fatal("a healthy sibling's resync must NOT be signalled")
	}
	// The healthy connection actually received the frame.
	select {
	case <-chOK:
	default:
		t.Fatal("the healthy connection should have received the fan-out frame")
	}
}

// 2. Ingress drop (sess.sseCh full → every connection misses the frame) flags ALL
//    connections and signals each resync.
func TestIngressDropFlagsAllConns(t *testing.T) {
	sess := newResyncTestSession()
	id1, _, r1 := sess.registerSSEConn("t1")
	id2, _, r2 := sess.registerSSEConn("t2")

	sess.markAllConnsOutOfSync()

	if !sess.connOutOfSync(id1) || !sess.connOutOfSync(id2) {
		t.Fatal("an ingress drop must flag every connection")
	}
	if !signalled(r1) || !signalled(r2) {
		t.Fatal("an ingress drop must signal every connection's resync")
	}
}

// 3. No false positive: a healthy fan-out (no buffer full) flags nobody.
func TestHealthyFanOutFlagsNobody(t *testing.T) {
	sess := newResyncTestSession()
	id, ch, r := sess.registerSSEConn("t")
	sess.fanOutFrame(sseFrame{data: "z"}, "")
	if sess.connOutOfSync(id) {
		t.Fatal("a healthy fan-out must not flag the connection")
	}
	if signalled(r) {
		t.Fatal("a healthy fan-out must not signal a resync")
	}
	select {
	case <-ch:
	default:
		t.Fatal("the frame should have been delivered")
	}
}

// 4. The resync channel is cap-1 and coalesces — repeated drops don't block.
func TestResyncChannelCoalesces(t *testing.T) {
	sess := newResyncTestSession()
	id, _, r := sess.registerSSEConn("t")
	// Many marks in a row must not block (cap-1 + non-blocking send).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			sess.markAllConnsOutOfSync()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("repeated marks blocked — resync signal is not non-blocking / cap-1")
	}
	if !signalled(r) {
		t.Fatal("at least one signal should be pending")
	}
	_ = id
}

// 5. clearConnOutOfSync resets the flag (called after a resync is delivered).
func TestClearConnOutOfSync(t *testing.T) {
	sess := newResyncTestSession()
	id, _, _ := sess.registerSSEConn("t")
	sess.markAllConnsOutOfSync()
	if !sess.connOutOfSync(id) {
		t.Fatal("precondition: connection should be flagged")
	}
	sess.clearConnOutOfSync(id)
	if sess.connOutOfSync(id) {
		t.Fatal("clearConnOutOfSync must reset the flag")
	}
}

// 6. renderResyncFrame produces a full-body frame with a fresh, advancing seq.
func TestRenderResyncFrameProducesFullBody(t *testing.T) {
	app := &liveApp{
		update:  func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:    func(model any) any { return velement("div", nil, []any{vtext("hello")}) },
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	init := velement("div", nil, []any{vtext("hello")})
	assignSkyIDs(&init, "r")
	sess := &liveSession{model: "m", handlers: map[string]any{}, prevTree: &init,
		sseCh: make(chan sseFrame, 1), cancelSub: make(chan struct{})}

	snap, ok := app.renderResyncFrame(sess)
	if !ok {
		t.Fatal("renderResyncFrame should succeed for a valid session")
	}
	if snap.body == "" {
		t.Fatal("resync frame must carry a full body")
	}
	if snap.seq <= 0 {
		t.Fatalf("resync frame must carry a fresh positive seq, got %d", snap.seq)
	}
}

// 7. Concurrency: fan-out, marking, and register/unregister must be race-free
//    (run the package with -race to exercise).
func TestSSEConnStateConcurrent(t *testing.T) {
	sess := newResyncTestSession()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				id, _, _ := sess.registerSSEConn("t")
				sess.fanOutFrame(sseFrame{data: "d"}, "")
				sess.markAllConnsOutOfSync()
				_ = sess.connOutOfSync(id)
				sess.clearConnOutOfSync(id)
				sess.unregisterSSEConn(id)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent SSE-conn ops deadlocked")
	}
}
