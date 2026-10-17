//go:build !js

package rt

import (
	"testing"
	"time"
)

// A slow Sub.every must still fire while a fast one keeps updating the
// model. Pre-fix, subManager.update stopped and respawned EVERY ticker on
// each update, so the 150 ms ticker restarted its countdown every 20 ms
// and never fired (T10 / SA-4).
func TestSubManager_SlowTimerSurvivesFrequentUpdates(t *testing.T) {
	msgCh := make(chan any, 256)
	m := newSubManager(msgCh)
	defer m.stopAll()
	subs := func(_ any) any {
		return Sub_batch([]any{Sub_every(20, "fast"), Sub_every(150, "slow")})
	}
	m.update(subs, nil)
	deadline := time.After(700 * time.Millisecond)
	slow, fast := 0, 0
	for {
		select {
		case raw := <-msgCh:
			switch everyPayload(raw) {
			case "fast":
				fast++
			case "slow":
				slow++
			}
			// Every delivered Msg is an update in a real loop — reconcile.
			m.update(subs, nil)
		case <-deadline:
			if fast < 5 {
				t.Fatalf("fast ticker fired %d times, want >= 5", fast)
			}
			if slow < 2 {
				t.Fatalf("slow ticker fired %d times under frequent updates, want >= 2 (timer restarted on every update)", slow)
			}
			return
		}
	}
}

// Two Sub.every on the SAME interval are both honoured, and an interval
// dropped from the subscriptions stops.
func TestSubManager_SameIntervalBothDispatchAndRemovalStops(t *testing.T) {
	msgCh := make(chan any, 256)
	m := newSubManager(msgCh)
	defer m.stopAll()
	m.update(func(_ any) any {
		return Sub_batch([]any{Sub_every(30, "a"), Sub_every(30, "b")})
	}, nil)
	seen := map[any]int{}
	timeout := time.After(400 * time.Millisecond)
collect:
	for {
		select {
		case raw := <-msgCh:
			seen[everyPayload(raw)]++
			if seen["a"] >= 2 && seen["b"] >= 2 {
				break collect
			}
		case <-timeout:
			t.Fatalf("same-interval subscriptions not both honoured: %v", seen)
		}
	}
	m.update(func(_ any) any { return Sub_none() }, nil)
	if m.hasTimers() {
		t.Fatalf("timer still registered after Sub.none")
	}
	// Drain anything in flight, then assert silence.
	time.Sleep(50 * time.Millisecond)
	for len(msgCh) > 0 {
		<-msgCh
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("stopped ticker still dispatched %v", msg)
	case <-time.After(120 * time.Millisecond):
	}
}

// everyPayload unwraps a queued Sub.every tick to its Msg.
func everyPayload(raw any) any {
	if em, ok := raw.(teaEveryMsg); ok {
		return em.msg
	}
	return raw
}

// A tick queued behind the update that removes its Sub.every never reaches
// update: the loop resolves it against its timer's cancel when it dequeues
// it. Before, the ticker sent the bare Msg, and a Tick sitting in the
// channel behind a Stop was applied after the Stop.
func TestTeaLoop_TickQueuedBehindStopIsDropped(t *testing.T) {
	msgCh := make(chan any, 8)
	loop := newTeaLoop(msgCh, nil, nil, nil)
	defer loop.subs.stopAll()
	cancel := make(chan struct{})
	queued := teaEveryMsg{msg: "tick", cancel: cancel}
	if got, ok := loop.resolve(queued); !ok || got != "tick" {
		t.Fatalf("a tick of a running timer must resolve to its Msg, got %v %v", got, ok)
	}
	close(cancel) // the update ahead of it in the queue stopped the timer
	if got, ok := loop.resolve(queued); ok {
		t.Fatalf("a tick of a stopped timer reached update: %v", got)
	}
}

// subscribeTopic registers a handler the loop resolves published payloads
// against (T11 / SA-7).
func TestSubManager_TopicHandlerRegistered(t *testing.T) {
	msgCh := make(chan any, 4)
	m := newSubManager(msgCh)
	toMsg := func(p any) any { return p }
	m.update(func(_ any) any {
		return Sub_batch([]any{Sub_subscribeTopic("chat", toMsg), Sub_none()})
	}, nil)
	if m.topicHandler("chat") == nil {
		t.Fatalf("topic handler for chat not registered")
	}
	if m.topicHandler("other") != nil {
		t.Fatalf("unexpected handler for an unsubscribed topic")
	}
	m.update(func(_ any) any { return Sub_none() }, nil)
	if m.topicHandler("chat") != nil {
		t.Fatalf("topic handler survived Sub.none")
	}
}
