package rt

// Cycle 3 P49 — broadcast divergence test (design doc §9.4).
//
// Validates the Msg-shape-broadcast design choice (design doc §3.4):
// each subscriber independently applies the broadcast Msg through its
// OWN update() reducer. Two sessions that share the same update + the
// same model snapshot, given the SAME sequence of broadcast Msgs in
// any order, MUST converge to identical model state.
//
// This is the load-bearing invariant for "publish a Msg, every
// subscriber's view re-renders correctly". If update were not
// commutative on the broadcast Msg type, two sessions could end up
// rendering different views after the same broadcast sequence and
// pull the multi-session collaboration story apart.
//
// The test deliberately keeps the model small + the update pure (no
// side effects, no time, no random) so divergence — if it happened —
// would be a unit-testable algebra failure rather than a flake. The
// chatroom example exercises this in the real wire path; this test
// exercises the algebraic property at the reducer level.

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────
// Model algebra under test
// ────────────────────────────────────────────────────────────────────

// divModel — minimal record-shaped model. Both fields are
// independently set by their respective broadcast Msgs, so any order
// of "set name=alice" + "set role=admin" yields the same result.
type divModel struct {
	Name string
	Role string
	// `Seq` is incremented on EVERY Msg to detect dispatch count
	// asymmetries — two sessions receiving the same N broadcasts MUST
	// end up with the same Seq. (Counts the Msg deliveries, not the
	// global pubsub seq.)
	Seq int
}

type divSetName struct{ Name string }
type divSetRole struct{ Role string }

// divUpdate is the pure reducer — no side effects, no IO. Returns the
// new model only; cmd channel is irrelevant for the divergence test
// because we're auditing the model algebra in isolation.
func divUpdate(msg any, m divModel) divModel {
	out := m
	out.Seq++
	switch v := msg.(type) {
	case divSetName:
		out.Name = v.Name
	case divSetRole:
		out.Role = v.Role
	}
	return out
}

// ────────────────────────────────────────────────────────────────────
// Test: order-independence
// ────────────────────────────────────────────────────────────────────

// Test_BroadcastDivergence_OrderIndependent is the smallest possible
// case: two sessions receive the same two Msgs in OPPOSITE orders.
// Both must end at the same model. Design doc §3.4 — "Msg-shape
// broadcast" is sound IFF update is commutative on broadcast Msgs.
//
// This test pins the example's Msgs (`SendMessage` → MessageReceived)
// — they only ever APPEND to history; order matters for the SHAPE of
// the history, but for a divergence test we need a Msg pair where
// ordering DOESN'T matter, so we pick a field-set algebra (`set name`
// + `set role`) that is genuinely commutative.
//
// The chatroom example's history append is NOT order-independent and
// that's by design — the order users see the messages in is the
// physical-clock order of receipt. The divergence guarantee for the
// chatroom holds because BOTH sessions receive broadcasts in the SAME
// order (the broker stamps globalSeq monotonically — design doc §3.2
// + the seq split tests in live_seq_split_test.go).
func Test_BroadcastDivergence_OrderIndependent(t *testing.T) {
	initial := divModel{Name: "", Role: ""}

	sessA := initial
	sessB := initial

	// Session A: name first, then role.
	sessA = divUpdate(divSetName{Name: "alice"}, sessA)
	sessA = divUpdate(divSetRole{Role: "admin"}, sessA)

	// Session B: role first, then name.
	sessB = divUpdate(divSetRole{Role: "admin"}, sessB)
	sessB = divUpdate(divSetName{Name: "alice"}, sessB)

	if !reflect.DeepEqual(sessA, sessB) {
		t.Fatalf("divergence detected:\n  sessA=%+v\n  sessB=%+v", sessA, sessB)
	}
	want := divModel{Name: "alice", Role: "admin", Seq: 2}
	if !reflect.DeepEqual(sessA, want) {
		t.Fatalf("converged to wrong state: got=%+v want=%+v", sessA, want)
	}
}

// Test_BroadcastDivergence_SameOrder_SameMsgCount pins the
// chatroom-shaped case: two sessions receive the SAME Msgs in the
// SAME order (the broker is the source of order). Even with an
// append-only history that is NOT order-independent, the two sessions
// must converge.
//
// We model history as `Seq` (each Msg bumps it); both sessions must
// end with the same `Seq` because the broker shipped the same number
// of broadcasts to each.
func Test_BroadcastDivergence_SameOrder_SameMsgCount(t *testing.T) {
	initial := divModel{}
	sessA, sessB := initial, initial

	feed := []any{
		divSetName{Name: "alice"},
		divSetRole{Role: "admin"},
		divSetName{Name: "bob"},
		divSetRole{Role: "guest"},
	}

	for _, m := range feed {
		sessA = divUpdate(m, sessA)
		sessB = divUpdate(m, sessB)
	}

	if !reflect.DeepEqual(sessA, sessB) {
		t.Fatalf("same-order divergence:\n  sessA=%+v\n  sessB=%+v", sessA, sessB)
	}
	if sessA.Seq != len(feed) {
		t.Fatalf("Seq=%d want %d (Msg count mismatch)", sessA.Seq, len(feed))
	}
}

// Test_BroadcastDivergence_TopicRegistryRealFanout is the closer test
// — it routes broadcasts through the REAL topicRegistry (P46) and
// asserts each subscriber's chan receives the same publish in the
// same order. This is the wire-level guarantee that underlies the
// reducer-level convergence above. If the registry ever stamped
// different events per subscriber, this would fail.
func Test_BroadcastDivergence_TopicRegistryRealFanout(t *testing.T) {
	reg := newTopicRegistry(16)

	// Two subscribers on the same topic.
	chA, cancelA := reg.Subscribe("conv")
	defer cancelA()
	chB, cancelB := reg.Subscribe("conv")
	defer cancelB()

	// Publish three events in order. `reg.Publish(topic, event)`
	// canonicalises event.Topic from the explicit topic arg, so we
	// leave it blank in the struct literal.
	publishes := []SessionEvent{
		{Payload: "p1", Origin: "sid-pub", GlobalSeq: 1},
		{Payload: "p2", Origin: "sid-pub", GlobalSeq: 2},
		{Payload: "p3", Origin: "sid-pub", GlobalSeq: 3},
	}
	for _, ev := range publishes {
		reg.Publish("conv", ev)
	}

	collected := func(ch <-chan SessionEvent) []SessionEvent {
		out := make([]SessionEvent, 0, len(publishes))
		deadline := time.After(2 * time.Second)
		for len(out) < len(publishes) {
			select {
			case ev := <-ch:
				out = append(out, ev)
			case <-deadline:
				return out
			}
		}
		return out
	}

	var wg sync.WaitGroup
	var aSeen, bSeen []SessionEvent
	wg.Add(2)
	go func() {
		defer wg.Done()
		aSeen = collected(chA)
	}()
	go func() {
		defer wg.Done()
		bSeen = collected(chB)
	}()
	wg.Wait()

	if !reflect.DeepEqual(aSeen, bSeen) {
		t.Fatalf("wire-level divergence between subscribers:\n  A=%+v\n  B=%+v",
			aSeen, bSeen)
	}
	if len(aSeen) != len(publishes) {
		t.Fatalf("subscriber A received %d events, want %d", len(aSeen), len(publishes))
	}
	for i, ev := range aSeen {
		if ev.GlobalSeq != publishes[i].GlobalSeq {
			t.Fatalf("event[%d] GlobalSeq=%d want %d", i, ev.GlobalSeq, publishes[i].GlobalSeq)
		}
	}
}

// Test_BroadcastDivergence_SingleSubscriberSeesOwnPublishOnce pins
// the echo-to-publisher invariant (design doc Q2 default): when a
// session is BOTH publisher and subscriber on the same topic, it
// receives EXACTLY ONE copy of its own broadcast — not zero (would
// break echo) and not two (would imply double-dispatch).
func Test_BroadcastDivergence_SingleSubscriberSeesOwnPublishOnce(t *testing.T) {
	reg := newTopicRegistry(16)
	ch, cancel := reg.Subscribe("self")
	defer cancel()

	reg.Publish("self", SessionEvent{Payload: "hi", Origin: "sid-A", GlobalSeq: 7})

	select {
	case ev := <-ch:
		if ev.Payload != "hi" || ev.GlobalSeq != 7 {
			t.Fatalf("first delivery mismatch: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("self-subscriber did not receive own publish (echo broken)")
	}

	// Second receive MUST time out — no double-dispatch.
	select {
	case ev := <-ch:
		t.Fatalf("self-subscriber received own publish twice: 2nd=%+v", ev)
	case <-time.After(150 * time.Millisecond):
		// expected
	}
}
