package rt

// Cycle 4 NE — no-echo publish variants (Cmd.publishNoEcho /
// PubSub.publishNoEcho).
//
// What this file pins:
//
//   - SessionEvent.SkipOrigin = true causes topicRegistry.Publish to
//     skip delivery to subscribers whose ownSid matches event.Origin.
//   - SkipOrigin = false (the default / existing behaviour) delivers
//     to every subscriber — echo-by-default preserved.
//   - PubSub_publishNoEcho with empty Origin behaves like a normal
//     fan-out (no subscriber's ownSid is "", so nobody is "self").
//   - Cmd_publishNoEcho builds a "publishNoEcho" cmdT whose runCmd
//     arm routes through app.Publish with SkipOrigin=true.

import (
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────
// Registry-level SkipOrigin semantics
// ────────────────────────────────────────────────────────────────────

// TestTopicRegistry_SkipOriginSuppressesSelfDelivery pins the core
// runtime contract: when event.SkipOrigin is true, a subscriber
// registered with ownSid == event.Origin receives nothing while every
// other subscriber receives the event normally.
func TestTopicRegistry_SkipOriginSuppressesSelfDelivery(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	pubCh, cancelPub := r.SubscribeWithOwner("room", "sid-publisher")
	defer cancelPub()
	otherCh, cancelOther := r.SubscribeWithOwner("room", "sid-other")
	defer cancelOther()

	delivered := r.Publish("room", SessionEvent{
		Payload:    "hello",
		Origin:     "sid-publisher",
		SkipOrigin: true,
	})
	if delivered != 1 {
		t.Fatalf("SkipOrigin Publish delivered=%d, want 1 (other only)", delivered)
	}

	// Publisher's own subscription must NOT receive.
	expectNoRecvWithin(t, pubCh, 50*time.Millisecond)

	// The foreign subscriber must receive.
	ev, ok := recvWithin(t, otherCh, 100*time.Millisecond)
	if !ok {
		t.Fatalf("foreign subscriber did not receive event")
	}
	if ev.Payload != "hello" || ev.Origin != "sid-publisher" {
		t.Fatalf("event mismatch: %+v", ev)
	}
}

// TestTopicRegistry_SkipOriginNoSubscriptionForPublisher pins the
// "publisher has no own subscription" case: SkipOrigin still delivers
// to every other subscriber (nothing to skip — no-op).
func TestTopicRegistry_SkipOriginNoSubscriptionForPublisher(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	otherCh, cancelOther := r.SubscribeWithOwner("room", "sid-other")
	defer cancelOther()

	delivered := r.Publish("room", SessionEvent{
		Payload:    "broadcast",
		Origin:     "sid-publisher-no-sub",
		SkipOrigin: true,
	})
	if delivered != 1 {
		t.Fatalf("SkipOrigin Publish delivered=%d, want 1", delivered)
	}
	ev, ok := recvWithin(t, otherCh, 100*time.Millisecond)
	if !ok {
		t.Fatalf("foreign subscriber did not receive event")
	}
	if ev.Payload != "broadcast" {
		t.Fatalf("event mismatch: %+v", ev)
	}
}

// TestTopicRegistry_SkipOriginEmptyOriginDeliversAll pins the
// server-side PubSub_publishNoEcho case: an empty Origin (server-side
// publish, not tied to any session) delivers to every subscriber —
// nobody's ownSid matches "".
func TestTopicRegistry_SkipOriginEmptyOriginDeliversAll(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	chA, cancelA := r.SubscribeWithOwner("room", "sid-a")
	defer cancelA()
	chB, cancelB := r.SubscribeWithOwner("room", "sid-b")
	defer cancelB()

	delivered := r.Publish("room", SessionEvent{
		Payload:    "server-broadcast",
		Origin:     "",
		SkipOrigin: true,
	})
	if delivered != 2 {
		t.Fatalf("empty-origin SkipOrigin Publish delivered=%d, want 2", delivered)
	}
	if _, ok := recvWithin(t, chA, 100*time.Millisecond); !ok {
		t.Fatalf("sub A did not receive server-side publishNoEcho")
	}
	if _, ok := recvWithin(t, chB, 100*time.Millisecond); !ok {
		t.Fatalf("sub B did not receive server-side publishNoEcho")
	}
}

// TestTopicRegistry_SkipOriginFalsePreservesEcho pins the backward-
// compat contract: the existing Cmd.publish / PubSub.publish path
// leaves SkipOrigin=false (zero value) and MUST still echo to the
// publisher's own subscription — the v0.15 default behaviour.
func TestTopicRegistry_SkipOriginFalsePreservesEcho(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	pubCh, cancelPub := r.SubscribeWithOwner("room", "sid-publisher")
	defer cancelPub()

	delivered := r.Publish("room", SessionEvent{
		Payload: "echoed",
		Origin:  "sid-publisher",
		// SkipOrigin defaults to false — existing semantics.
	})
	if delivered != 1 {
		t.Fatalf("echo-default Publish delivered=%d, want 1 (publisher echo)", delivered)
	}
	ev, ok := recvWithin(t, pubCh, 100*time.Millisecond)
	if !ok {
		t.Fatalf("publisher's own subscription did not receive echo")
	}
	if ev.Payload != "echoed" {
		t.Fatalf("event mismatch: %+v", ev)
	}
}

// TestTopicRegistry_SubscribeBackwardCompat — the old Subscribe entry
// point (no ownSid arg) keeps working; such subscribers register with
// ownSid == "" and therefore are NEVER skipped by SkipOrigin (because
// publishers never have an empty Origin in the dispatch path — they
// always carry their sess.sid, or are server-side which already keeps
// Origin = "").
func TestTopicRegistry_SubscribeBackwardCompat(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	ch, cancel := r.Subscribe("legacy") // no ownSid — backward compat
	defer cancel()
	delivered := r.Publish("legacy", SessionEvent{
		Payload:    "x",
		Origin:     "sid-anybody",
		SkipOrigin: true,
	})
	if delivered != 1 {
		t.Fatalf("legacy subscriber w/ SkipOrigin Publish delivered=%d, want 1", delivered)
	}
	if _, ok := recvWithin(t, ch, 100*time.Millisecond); !ok {
		t.Fatalf("legacy subscriber lost event")
	}
}

// ────────────────────────────────────────────────────────────────────
// Cmd_publishNoEcho / PubSub_publishNoEcho kernel surfaces
// ────────────────────────────────────────────────────────────────────

// Test_CmdPublishNoEcho_BuildsCorrectCmd pins that Cmd_publishNoEcho
// returns a cmdT whose kind = "publishNoEcho" and carries the topic
// + payload. runCmd's matching arm is exercised in the dispatch test
// below.
func Test_CmdPublishNoEcho_BuildsCorrectCmd(t *testing.T) {
	cmd := Cmd_publishNoEcho("topic-x", "payload-y")
	if cmd.kind != "publishNoEcho" {
		t.Fatalf("cmd.kind = %q, want %q", cmd.kind, "publishNoEcho")
	}
	if cmd.topic != "topic-x" {
		t.Fatalf("cmd.topic = %q, want %q", cmd.topic, "topic-x")
	}
	if cmd.payload != "payload-y" {
		t.Fatalf("cmd.payload = %v, want %v", cmd.payload, "payload-y")
	}
}

// Test_PubSubPublishNoEcho_DeliversToOthersOnly — Task-shaped
// publishNoEcho with an explicit Origin in the registered process
// broker delivers to every subscriber except the one whose ownSid
// matches Origin.
//
// The Task-shaped surface is normally called server-side (Origin =
// ""), but a caller can pass an Origin via a future overload OR by
// invoking the kernel directly from a context that has access to a
// session sid. v0.15.x ships PubSub_publishNoEcho with Origin always
// "" (server-side), so this test pins that the empty-origin call
// behaves like a regular fan-out (no subscriber is "self").
func Test_PubSubPublishNoEcho_ServerSideDeliversAll(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	app := &liveApp{topics: newTopicRegistry(16)}
	registerProcessBroker(app)

	ch, cancel := app.topics.SubscribeWithOwner("noecho-topic", "sid-receiver")
	defer cancel()

	taskFn := PubSub_publishNoEcho("noecho-topic", "server-payload").(func() any)
	result := taskFn()

	if !isResultOk(result) {
		t.Fatalf("expected Ok, got: %v", result)
	}
	delivered := resultOkValue(result).(int)
	if delivered != 1 {
		t.Fatalf("expected delivery count 1, got %d", delivered)
	}

	select {
	case ev := <-ch:
		if ev.Payload != "server-payload" {
			t.Fatalf("payload mismatch: %v", ev.Payload)
		}
		if !ev.SkipOrigin {
			t.Fatalf("expected SkipOrigin=true on delivered event")
		}
	default:
		t.Fatal("subscriber did not receive the broadcast")
	}
}

// Test_PubSubPublishNoEcho_NoLiveApp — same Err(Unavailable) path as
// PubSub_publish when no Live.app is registered.
func Test_PubSubPublishNoEcho_NoLiveApp(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	taskFn := PubSub_publishNoEcho("any-topic", "payload").(func() any)
	result := taskFn()

	if !isResultErr(result) {
		t.Fatalf("expected Err, got Ok with value: %v", result)
	}
}
