//go:build !js

package rt

// Two intermittent failures the gate-stress job found (x40 under -race), each
// pinned here by a test that fails every time on the code before the fix.
//
//  1. A Task-owned socket whose reader had been parked past the idle bound was
//     reaped at the first sweep after the Task took one frame: deliver cleared
//     `parked` before anything refreshed the activity time, so the reaper saw
//     an unparked socket that had been "silent" for the whole wait.
//  2. The heartbeat and the reader read the ping and stall timeouts from
//     package variables on every tick, so a value written after connect (a
//     test restoring its knobs) raced a running socket's goroutines.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsWaitUntil polls `cond` until it holds or `within` passes.
func wsWaitUntil(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// TestWsDeliver_UnparkRefreshesActivity — a Task-owned reader parks on a full
// queue for longer than the idle bound, then the Task receives one frame. The
// socket must not be reapable in the moment between leaving the park and the
// next delivered frame: the end of the wait is itself a sign of life.
func TestWsDeliver_UnparkRefreshesActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sh := &wsHandle{id: nextWsID(), ch: make(chan wsEvent, 1), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	t.Cleanup(sh.Close)
	sh.owner.Store(wsOwnerTask)
	sh.ch <- wsEvent{kind: wsMessageEv, text: "queued"}

	returned := make(chan bool, 1)
	go func() { returned <- sh.deliver(wsEvent{kind: wsMessageEv, text: "next"}) }()
	if !wsWaitUntil(5*time.Second, sh.parked.Load) {
		t.Fatal("deliver never parked on a full queue")
	}
	// The reader has now waited longer than the idle bound.
	sh.lastActivityNano.Store(time.Now().Add(-2 * sessionlessIdleTTL).UnixNano())

	<-sh.ch // the Task receives one frame
	if !<-returned {
		t.Fatal("deliver reported a stall for a Task-owned socket")
	}
	if sh.parked.Load() {
		t.Fatal("deliver returned still parked")
	}

	var m sync.Map
	m.Store(sh.id, sh)
	sweepSessionlessMap(&m, time.Now())
	if sh.IsClosed() {
		t.Fatal("the reaper closed a Task-owned socket the moment its reader left a long park")
	}
}

// TestWsHeartbeat_PingTimeoutFixedAtConnect — the pong deadline is the one in
// force when the socket opened. A later write to the package value (a test
// restoring its knobs) does not reach a running heartbeat.
func TestWsHeartbeat_PingTimeoutFixedAtConnect(t *testing.T) {
	withWsTestKnobs(t, wsReadChanCap, wsConsumerStallTimeout, 5*time.Second)
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx) // reads, so pings are answered
	})
	id := wsTaskConnect(t, url, 20*time.Millisecond)
	sh := lookupWs(nil, id)
	// Pre-fix the next heartbeat tick read this and its ping expired at once.
	wsPingTimeout = time.Nanosecond

	if !wsWaitUntil(5*time.Second, func() bool { return sh.pingOK.Load() || sh.IsClosed() }) {
		t.Fatal("no heartbeat ping completed")
	}
	if sh.IsClosed() {
		t.Fatal("the heartbeat used a ping timeout written after connect and closed a live socket")
	}
}

// TestWsDeliver_StallTimeoutFixedAtConnect — the consumer-stall bound is the
// one in force when the socket opened, for the same reason.
func TestWsDeliver_StallTimeoutFixedAtConnect(t *testing.T) {
	withWsTestKnobs(t, 2, 5*time.Second, wsPingTimeout)
	release := make(chan struct{})
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		for i := 0; i < 8; i++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte("x")); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	})
	id := wsTaskConnect(t, url, 0)
	sh := lookupWs(nil, id)
	sh.owner.Store(wsOwnerSub) // a Sub reader stalls out; a Task reader never does
	// Pre-fix the parked reader read this and abandoned the socket at once.
	wsConsumerStallTimeout = time.Nanosecond
	close(release)

	if !wsWaitUntil(5*time.Second, sh.parked.Load) {
		t.Fatal("the reader never parked on a full queue")
	}
	// A 5 s bound cannot expire in 300 ms; a 1 ns one expires at once.
	if wsWaitUntil(300*time.Millisecond, sh.IsClosed) {
		t.Fatal("the reader used a stall timeout written after connect and closed the socket")
	}
}
