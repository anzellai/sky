//go:build !js

package rt

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// The Cmd.toIsland delivery contract (docs/skyui/overview.md, "Widget
// islands"): every command reaches the widget once and in order, or the
// client learns that one was lost and remounts the island (an explicit
// resync). Written FIRST: before the fix a command dropped by a full SSE
// buffer (the session's ingress channel, a connection's buffer, the queue
// kept while no tab is connected, or the handover of that queue to a new
// connection) was lost with nothing to tell the widget; the per-connection
// view resync repairs the DOM, and the widget kept its stale state.
//
// Each case plays a client model: it adopts the hello baseline, then takes
// the frames and the island sync the connection writes, and must end with
// every command received in order OR a detected gap. A case also checks
// that a loss really happened, so it cannot pass by not flooding.

type islandClientModel struct {
	last     map[string]int64
	gap      map[string]bool
	received map[string][]int64
}

func newIslandClientModel(base map[string]int64) *islandClientModel {
	m := &islandClientModel{last: map[string]int64{}, gap: map[string]bool{}, received: map[string][]int64{}}
	for id, g := range base {
		m.last[id] = g
	}
	return m
}

// frame is the client's handling of one "island" event.
func (m *islandClientModel) frame(t *testing.T, fr sseFrame) {
	t.Helper()
	if fr.event != "island" {
		return
	}
	var d struct {
		ID  string `json:"id"`
		Seq int64  `json:"seq"`
	}
	if err := json.Unmarshal([]byte(fr.data), &d); err != nil {
		t.Fatalf("island frame is not JSON: %q", fr.data)
	}
	if d.Seq == 0 {
		t.Fatalf("island frame carries no seq: %q", fr.data)
	}
	last := m.last[d.ID]
	switch {
	case d.Seq <= last:
		return // stale: already resynced past it
	case d.Seq > last+1:
		m.gap[d.ID] = true
	}
	m.last[d.ID] = d.Seq
	m.received[d.ID] = append(m.received[d.ID], d.Seq)
}

// sync is the client's handling of an "islandsync" map.
func (m *islandClientModel) sync(s map[string]int64) {
	for id, g := range s {
		if g > m.last[id] {
			m.gap[id] = true
			m.last[id] = g
		}
	}
}

// complete reports whether id received 1..n in order with no gap.
func (m *islandClientModel) complete(id string, n int64) bool {
	r := m.received[id]
	if int64(len(r)) != n {
		return false
	}
	for i, g := range r {
		if g != int64(i+1) {
			return false
		}
	}
	return true
}

func pushIslands(s *liveSession, id string, n int) {
	for i := 0; i < n; i++ {
		s.pushIslandCmd(islandCmd{ID: id, Name: "set", Payload: json.RawMessage(fmt.Sprintf("%d", i))})
	}
}

func waitRelayDrained(t *testing.T, s *liveSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.sseCh) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the relay did not drain the ingress channel")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // the relay's last fan-out
}

// A flood past a connection's buffer: frames are dropped at egress (and at
// ingress when the relay falls behind); the client must detect it.
func TestIslandDelivery_FloodPastTheBufferIsNeverSilent(t *testing.T) {
	s := &liveSession{sseCh: make(chan sseFrame, 4), cancelSub: make(chan struct{}), done: make(chan struct{})}
	defer close(s.done)
	s.ensureSSERelay()
	connID, _, resync := s.registerSSEConn("tab")
	cl := newIslandClientModel(s.islandHelloBase(connID))
	pushIslands(s, "editor", 200)
	pushIslands(s, "chart", 40)
	waitRelayDrained(t, s)
	if !signalled(resync) {
		t.Fatal("a dropped island command must signal the connection's resync at once")
	}
	frames, sync := s.islandSyncFor(connID)
	for _, fr := range frames {
		cl.frame(t, fr)
	}
	cl.sync(sync)
	lost := false
	for id, n := range map[string]int64{"editor": 200, "chart": 40} {
		if !cl.complete(id, n) {
			lost = true
			if !cl.gap[id] {
				t.Errorf("%s: commands were lost (received %d of %d) and the client was not told", id, len(cl.received[id]), n)
			}
		}
	}
	if !lost {
		t.Fatal("the flood lost nothing: the test did not exercise a drop")
	}
}

// Commands pushed with no tab connected wait in a bounded queue; the
// overflow, and the handover to a connection whose buffer is smaller than
// the queue, must both be detected by the connecting tab.
func TestIslandDelivery_QueueOverflowWithNoConnectionIsDetected(t *testing.T) {
	s := &liveSession{sseCh: make(chan sseFrame, 1024), cancelSub: make(chan struct{})}
	for i := 0; i < islandPendingMax+40; i++ {
		fr := s.islandFrame(islandCmd{ID: "editor", Name: "set", Payload: json.RawMessage("1")})
		s.fanOutFrame(fr, "")
	}
	connID, _, resync := s.registerSSEConn("tab")
	cl := newIslandClientModel(s.islandHelloBase(connID))
	if !signalled(resync) {
		t.Fatal("a connection that inherits lost commands must be resynced at once")
	}
	frames, sync := s.islandSyncFor(connID)
	for _, fr := range frames {
		cl.frame(t, fr)
	}
	cl.sync(sync)
	if cl.complete("editor", int64(islandPendingMax+40)) {
		t.Fatal("nothing was lost: the test did not exercise the overflow")
	}
	if !cl.gap["editor"] {
		t.Fatalf("the queue lost commands and the client was not told (received %d)", len(cl.received["editor"]))
	}
}

// An ingress drop (the relay's channel full) loses a frame before any
// connection sees it: every connection must learn of it.
func TestIslandDelivery_IngressDropIsDetectedByEveryTab(t *testing.T) {
	s := &liveSession{sseCh: make(chan sseFrame, 1), cancelSub: make(chan struct{})}
	a, _, _ := s.registerSSEConn("a")
	b, _, _ := s.registerSSEConn("b")
	ca, cb := newIslandClientModel(s.islandHelloBase(a)), newIslandClientModel(s.islandHelloBase(b))
	pushIslands(s, "editor", 3) // the first fits; two are dropped at ingress
	fr := <-s.sseCh
	s.fanOutFrame(fr, "")
	for _, c := range []struct {
		id uint64
		m  *islandClientModel
	}{{a, ca}, {b, cb}} {
		frames, sync := s.islandSyncFor(c.id)
		for _, f := range frames {
			c.m.frame(t, f)
		}
		c.m.sync(sync)
		if !c.m.gap["editor"] {
			t.Errorf("connection %d: two commands were dropped at ingress and the client was not told", c.id)
		}
	}
}

// A tab that reconnects after missing commands (other tabs got them) sees
// the gap from the hello baseline; a tab that missed nothing sees none.
func TestIslandDelivery_ReconnectBaseline(t *testing.T) {
	s := &liveSession{sseCh: make(chan sseFrame, 64), cancelSub: make(chan struct{})}
	a, cha, _ := s.registerSSEConn("a")
	b, _, _ := s.registerSSEConn("b")
	ca := newIslandClientModel(s.islandHelloBase(a))
	cb := newIslandClientModel(s.islandHelloBase(b))
	for i := 0; i < 3; i++ {
		fr := s.islandFrame(islandCmd{ID: "chart", Name: "data", Payload: json.RawMessage("1")})
		s.fanOutFrame(fr, "")
	}
	for len(cha) > 0 {
		ca.frame(t, <-cha)
	}
	frames, _ := s.islandSyncFor(b)
	for _, f := range frames[:1] { // tab b took one command, then its connection died
		cb.frame(t, f)
	}
	s.unregisterSSEConn(b)
	fr := s.islandFrame(islandCmd{ID: "chart", Name: "data", Payload: json.RawMessage("1")})
	s.fanOutFrame(fr, "")
	for len(cha) > 0 {
		ca.frame(t, <-cha)
	}
	b2, _, _ := s.registerSSEConn("b")
	cb.sync(s.islandHelloBase(b2))
	if !cb.gap["chart"] {
		t.Error("a reconnecting tab that missed three commands must be told")
	}
	a2, _, _ := s.registerSSEConn("a")
	ca.sync(s.islandHelloBase(a2))
	if ca.gap["chart"] || !ca.complete("chart", 4) {
		t.Errorf("a tab that missed nothing must see no gap: gap=%v received=%v", ca.gap["chart"], ca.received["chart"])
	}
}

// No false positive: a healthy stream is complete and gap-free, and the
// sync map a heartbeat writes agrees with it.
func TestIslandDelivery_HealthyStreamHasNoGap(t *testing.T) {
	s := &liveSession{sseCh: make(chan sseFrame, 64), cancelSub: make(chan struct{})}
	connID, ch, resync := s.registerSSEConn("tab")
	cl := newIslandClientModel(s.islandHelloBase(connID))
	for i := 0; i < 10; i++ {
		fr := s.islandFrame(islandCmd{ID: "editor", Name: "set", Payload: json.RawMessage("1")})
		s.fanOutFrame(fr, "")
		if i == 4 {
			// A heartbeat in the middle of the stream: it drains what is
			// buffered before it states the map.
			frames, sync := s.islandSyncFor(connID)
			for _, f := range frames {
				cl.frame(t, f)
			}
			cl.sync(sync)
		}
	}
	for len(ch) > 0 {
		cl.frame(t, <-ch)
	}
	_, sync := s.islandSyncFor(connID)
	cl.sync(sync)
	if signalled(resync) || cl.gap["editor"] || !cl.complete("editor", 10) {
		t.Fatalf("a healthy stream: resync=%v gap=%v received=%v", signalled(resync), cl.gap["editor"], cl.received["editor"])
	}
}
