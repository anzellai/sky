package rt

// Phase-5d (grill A1 — acked-then-lost). A Cmd.perform completion mutates the
// Model and ACKs it to the client via an SSE frame; runPerformBody previously
// never persisted, so a crash after the ack (client saw success) but before the
// next sync handleEvent lost the change. This pins that a shipped async frame is
// persisted-BEFORE-ack (store.Set called with the mutated Model).

import "testing"

// spyStore records Set calls; the other SessionStore methods are inert.
type spyStore struct {
	setCalls  int
	lastSid   string
	lastModel any
}

func (s *spyStore) Get(string) (*liveSession, bool) { return nil, false }
func (s *spyStore) Set(sid string, sess *liveSession) {
	s.setCalls++
	s.lastSid = sid
	s.lastModel = sess.model
}
func (s *spyStore) Delete(string) {}
func (s *spyStore) NewID() string { return "spy" }
func (s *spyStore) Close() error  { return nil }
func (s *spyStore) Broker() Broker { return nil }
func (s *spyStore) Ping() error    { return nil }

func TestPerformBody_PersistsBeforeAck(t *testing.T) {
	store := &spyStore{}
	app := &liveApp{
		store: store,
		update: func(_ any, model any) any {
			n := 0
			if v, ok := model.(int); ok {
				n = v
			}
			return SkyTuple2{V0: n + 1, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("v" + itoa(model.(int)))})
		},
	}
	sess := &liveSession{
		sid:       "sess-1",
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 8),
		model:     0,
		handlers:  map[string]any{},
	}
	// Bootstrap so the perform's frame differs from lastShippedBody and ships.
	_ = app.dispatch(sess, 0)
	sess.lastShippedBody = sess.lastComputedBody
	setsAfterBootstrap := store.setCalls // dispatch itself must not persist

	task := func(any) any { return 0 }
	identity := func(x any) any { return x }
	app.runPerformBody(sess, task, identity)

	if store.setCalls <= setsAfterBootstrap {
		t.Fatal("A1 acked-then-lost: runPerformBody shipped a frame but never called store.Set (no persist-before-ack)")
	}
	if store.lastSid != "sess-1" {
		t.Fatalf("persisted under the wrong sid: %q", store.lastSid)
	}
	// The persisted Model must be the MUTATED state the client was acked, not stale 0.
	if n, ok := store.lastModel.(int); !ok || n < 1 {
		t.Fatalf("persisted a stale/wrong Model: %#v (want the mutated int >= 1)", store.lastModel)
	}
}

// A suppressed perform (no frame shipped → no ack) need not persist: nothing was
// observed by the client, so there is no acked-then-lost hazard to close.
func TestPerformBody_NoFrameNoRequiredPersist(t *testing.T) {
	store := &spyStore{}
	app := &liveApp{
		store: store,
		// Identity update — model unchanged → view unchanged → frame suppressed.
		update: func(_ any, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:   func(model any) any { return velement("div", nil, []any{vtext("static")}) },
	}
	sess := &liveSession{
		sid:       "sess-2",
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 8),
		model:     7,
		handlers:  map[string]any{},
	}
	_ = app.dispatch(sess, 0)
	sess.lastShippedBody = sess.lastComputedBody
	before := store.setCalls

	app.runPerformBody(sess, func(any) any { return 0 }, func(x any) any { return x })

	// No frame ships (view identical) → the persist is correctly skipped.
	if store.setCalls != before {
		t.Fatalf("suppressed perform should not persist (no ack); setCalls %d -> %d", before, store.setCalls)
	}
}

// Phase-5d (grill A1): a pub/sub broadcast (Cmd.publish → Sub.subscribeTopic)
// mutates the RECEIVER's Model and acks it via an SSE frame — it must
// persist-before-ack, else a crash after the receiver saw the broadcast land loses
// it. runSubscriberDispatch previously never persisted.
func TestSubscriberDispatch_PersistsBeforeAck(t *testing.T) {
	store := &spyStore{}
	app := &liveApp{
		store: store,
		update: func(_ any, model any) any {
			n := 0
			if v, ok := model.(int); ok {
				n = v
			}
			return SkyTuple2{V0: n + 1, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("s" + itoa(model.(int)))})
		},
	}
	sess := &liveSession{
		sid:       "sub-1",
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 8),
		model:     0,
		handlers:  map[string]any{},
	}
	_ = app.dispatch(sess, 0)
	sess.lastShippedBody = sess.lastComputedBody
	before := store.setCalls

	toMsg := func(payload any) any { return payload } // the payload IS the Msg
	ev := SessionEvent{Topic: "chat", Payload: 0, GlobalSeq: 1}
	app.runSubscriberDispatch(sess, toMsg, ev)

	if store.setCalls <= before {
		t.Fatal("A1: a pub/sub broadcast mutated the receiver's Model and acked a frame but never persisted (store.Set not called)")
	}
	if n, ok := store.lastModel.(int); !ok || n < 1 {
		t.Fatalf("persisted a stale/wrong Model: %#v (want the mutated int >= 1)", store.lastModel)
	}
}

// G1 (grill, no-deferral). Phase-5d built persistAndShipFrame as "the SINGLE
// durability funnel" but only converted the live.go sites — reactiveRefreshOnce
// and dispatchOneWsSub kept their own raw `sess.sseCh <- frame` send with NO
// store.Set, so the funnel was 4 of 6 async ack sites.
//
// The reactive one is the sharper of the two, and G1 is what makes it reachable:
// until the deadlock was fixed, no reactive app ever completed an initial render,
// so this path had never run in a real app. The failure is worse than "the Model
// is lost". reactiveRefreshOnce advances localSeq via prepareFrameSnapshot, so an
// idle reactive dashboard ships seq 5..40 unpersisted; on restart the session
// reloads at persisted seq 4 and re-ships 5, 6, 7 … which the client SILENTLY
// DISCARDS (it drops any frame with seq <= its __skyLastAppliedSeq). The page is
// frozen until a hard reload, and the SSE reconnect-resync only papers over it
// when a connection re-opens — which for an idle dashboard is exactly what does
// not happen.
func TestReactiveRefreshOnce_PersistsBeforeAck(t *testing.T) {
	store := &spyStore{}
	app := &liveApp{
		store: store,
		update: func(_ any, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view: func(model any) any {
			n, _ := model.(int)
			return velement("div", nil, []any{vtext("r" + itoa(n))})
		},
	}
	sess := &liveSession{
		sid:       "react-1",
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 8),
		model:     0,
		handlers:  map[string]any{},
	}
	// Bootstrap so the reactive refresh's frame differs from lastShippedBody.
	_ = app.dispatch(sess, 0)
	sess.lastShippedBody = sess.lastComputedBody
	before := store.setCalls

	// The binding's re-query closure: () -> Task Error (model -> model). The fold
	// bumps the counter, so the re-render differs and a frame ships.
	binding := reactiveBindingRT{
		run: func(_ any) any {
			return SkyTask[any, any](func() SkyResult[any, any] {
				fold := func(m any) any {
					n, _ := m.(int)
					return any(n + 1)
				}
				return Ok[any, any](any(fold))
			})
		},
	}
	app.reactiveRefreshOnce(sess, binding)

	// The frame must actually have shipped, else the assertion below is vacuous.
	if len(sess.sseCh) == 0 {
		t.Fatal("no frame shipped — fixture broken, the persist assertion would be vacuous")
	}
	if store.setCalls <= before {
		t.Fatal("A1 acked-then-lost: reactiveRefreshOnce mutated the Model, advanced localSeq and " +
			"shipped an SSE frame but never called store.Set. On restart the session reloads at the " +
			"stale persisted seq and re-ships seq numbers the client silently discards — the page " +
			"freezes. Route the send through app.persistAndShipFrame (the durability funnel).")
	}
	if n, ok := store.lastModel.(int); !ok || n < 1 {
		t.Fatalf("persisted a stale/wrong Model: %#v (want the folded int >= 1)", store.lastModel)
	}
}

// The websocket twin of the above: dispatchOneWsSub mutates the Model from an
// inbound socket event and acks it with the same raw, unpersisted send.
func TestWsSubDispatch_PersistsBeforeAck(t *testing.T) {
	store := &spyStore{}
	app := &liveApp{
		store: store,
		update: func(_ any, model any) any {
			n, _ := model.(int)
			return SkyTuple2{V0: n + 1, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			n, _ := model.(int)
			return velement("div", nil, []any{vtext("w" + itoa(n))})
		},
		// The inbound WebSocketMessage IS an ADT, so dispatch caches its
		// SkyName→Tag pair; the map must exist (a plain-int Msg never gets here).
		msgTags: map[string]int{},
	}
	sess := &liveSession{
		sid:       "ws-1",
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 8),
		model:     0,
		handlers:  map[string]any{},
	}
	_ = app.dispatch(sess, 0)
	sess.lastShippedBody = sess.lastComputedBody
	before := store.setCalls

	reg := &wsSubReg{kind: "message", toMsg: func(payload any) any { return payload }}
	app.dispatchOneWsSub(sess, reg, wsEvent{kind: wsMessageEv, text: "ping"})

	if len(sess.sseCh) == 0 {
		t.Fatal("no frame shipped — fixture broken, the persist assertion would be vacuous")
	}
	if store.setCalls <= before {
		t.Fatal("A1 acked-then-lost: dispatchOneWsSub mutated the Model and acked an SSE frame but " +
			"never called store.Set. Route the send through app.persistAndShipFrame.")
	}
	if n, ok := store.lastModel.(int); !ok || n < 1 {
		t.Fatalf("persisted a stale/wrong Model: %#v (want the mutated int >= 1)", store.lastModel)
	}
}
