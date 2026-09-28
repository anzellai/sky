package rt

import (
	"testing"
)

// Cycle 4 PT — Task-shaped Std.PubSub.publish.
//
// Pins the contract that publish from any goroutine (i.e. NOT from a
// Sky.Live update return) reaches every subscriber on the process's
// active broker, with the same `int` delivery count that runCmd's
// publish arm sees inside the update loop.

// Test_PubSubPublishTask_NoLiveApp — when no Live.app has been
// registered in the process, PubSub_publish returns an Err with the
// Unavailable code. CLI tools / unit tests that never start a
// Live.app see this; production deploys never should.
func Test_PubSubPublishTask_NoLiveApp(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	taskFn := PubSub_publish("any-topic", "payload").(func() any)
	result := taskFn()

	// Decode the SkyResult shape — Err carries the Error value.
	if !isResultErr(result) {
		t.Fatalf("expected Err, got Ok with value: %v", result)
	}
}

// Test_PubSubPublishTask_NilTopicsField — Live.app registered but its
// topics field is nil (test apps that skip the store.Broker() wiring).
// PubSub_publish returns Ok(0) without panicking — no subscribers
// means zero delivery, which is success, not error.
func Test_PubSubPublishTask_NilTopicsField(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	app := &liveApp{} // .topics intentionally nil
	registerProcessBroker(app)

	taskFn := PubSub_publish("any-topic", "payload").(func() any)
	result := taskFn()

	if !isResultOk(result) {
		t.Fatalf("expected Ok(0), got: %v", result)
	}
	delivered := resultOkValue(result).(int)
	if delivered != 0 {
		t.Fatalf("expected delivery count 0, got %d", delivered)
	}
}

// Test_PubSubPublishTask_DeliversToSubscriber — the happy path. A
// real topicRegistry with one subscriber sees the publish; the
// returned Ok carries delivery count 1.
func Test_PubSubPublishTask_DeliversToSubscriber(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	app := &liveApp{topics: newTopicRegistry(16)}
	registerProcessBroker(app)

	// Subscribe before publishing; otherwise the delivery count
	// is naturally zero (matches in-process broker semantics —
	// publishes to an empty topic are not buffered).
	ch, cancel := app.topics.Subscribe("metrics:request")
	defer cancel()

	taskFn := PubSub_publish("metrics:request", map[string]any{"path": "/healthz"}).(func() any)
	result := taskFn()

	if !isResultOk(result) {
		t.Fatalf("expected Ok, got: %v", result)
	}
	delivered := resultOkValue(result).(int)
	if delivered != 1 {
		t.Fatalf("expected delivery count 1, got %d", delivered)
	}

	// Confirm the subscriber actually received the payload.
	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any payload, got %T (%v)", ev.Payload, ev.Payload)
		}
		if payload["path"] != "/healthz" {
			t.Fatalf("expected path /healthz, got %v", payload["path"])
		}
		if ev.Origin != "" {
			t.Fatalf("expected empty Origin (server-side publish), got %q", ev.Origin)
		}
	default:
		t.Fatal("subscriber did not receive the broadcast")
	}
}

// Test_PubSubPublishTask_ReachesEveryHostApp — a process running two host
// Live apps (two Live.serve starts, v0.27) sees PubSub_publish reach the
// subscribers of BOTH. A topic is a process-wide name; before v0.27 the
// first app took every publish and the second never saw one. A stopped app
// (unregisterProcessBrokerApp, Live.stop) receives nothing more.
//
// Sub-apps (the inline console) never register, so the v0.16.1 PR10-F
// guarantee holds: a sub-app's broker never receives the host's publishes.
func Test_PubSubPublishTask_ReachesEveryHostApp(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	first := &liveApp{topics: newTopicRegistry(16)}
	second := &liveApp{topics: newTopicRegistry(16)}

	registerProcessBroker(first)
	registerProcessBroker(second)
	registerProcessBroker(second) // a second registration is a no-op

	firstCh, firstCancel := first.topics.Subscribe("topic")
	defer firstCancel()
	secondCh, secondCancel := second.topics.Subscribe("topic")
	defer secondCancel()

	result := PubSub_publish("topic", "p").(func() any)()
	if got := resultOkValue(result).(int); got != 2 {
		t.Fatalf("delivery count: got %d, want 2 (one per app)", got)
	}
	for name, ch := range map[string]<-chan SessionEvent{"first": firstCh, "second": secondCh} {
		select {
		case ev := <-ch:
			if ev.Payload != "p" {
				t.Fatalf("%s app: expected payload \"p\", got %v", name, ev.Payload)
			}
		default:
			t.Fatalf("%s app's subscriber did not receive the broadcast", name)
		}
	}

	// The first app stops: only the second one is reached from now on.
	unregisterProcessBrokerApp(first)
	result = PubSub_publish("topic", "q").(func() any)()
	if got := resultOkValue(result).(int); got != 1 {
		t.Fatalf("after stop: delivery count %d, want 1", got)
	}
	select {
	case ev := <-firstCh:
		t.Fatalf("stopped app's subscriber received %v", ev.Payload)
	default:
	}
	select {
	case <-secondCh:
	default:
		t.Fatal("the running app's subscriber did not receive the broadcast")
	}
}

// ────────────────────────────────────────────────────────────────
// Result-shape helpers — SkyResult uses {Tag: int, OkValue, ErrValue}
// with Tag 0=Ok, 1=Err.

func isResultOk(v any) bool {
	r, ok := v.(SkyResult[any, any])
	if !ok {
		return false
	}
	return r.Tag == 0
}

func isResultErr(v any) bool {
	r, ok := v.(SkyResult[any, any])
	if !ok {
		return false
	}
	return r.Tag == 1
}

func resultOkValue(v any) any {
	r := v.(SkyResult[any, any])
	return r.OkValue
}
