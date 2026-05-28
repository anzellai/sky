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

// Test_PubSubPublishTask_LastRegisteredWins — a process running
// multiple Live.app starts (rare but legal) sees PubSub_publish
// route to the most recently registered app's broker.
func Test_PubSubPublishTask_LastRegisteredWins(t *testing.T) {
	unregisterProcessBroker()
	t.Cleanup(unregisterProcessBroker)

	first := &liveApp{topics: newTopicRegistry(16)}
	second := &liveApp{topics: newTopicRegistry(16)}

	registerProcessBroker(first)
	registerProcessBroker(second) // overwrites; last writer wins

	firstCh, firstCancel := first.topics.Subscribe("topic")
	defer firstCancel()
	secondCh, secondCancel := second.topics.Subscribe("topic")
	defer secondCancel()

	taskFn := PubSub_publish("topic", "p").(func() any)
	_ = taskFn()

	// First app's subscriber must not receive — publish went to
	// second app's broker.
	select {
	case ev := <-firstCh:
		t.Fatalf("first app's subscriber unexpectedly received: %v", ev.Payload)
	default:
	}

	// Second app's subscriber receives.
	select {
	case ev := <-secondCh:
		if ev.Payload != "p" {
			t.Fatalf("expected payload \"p\", got %v", ev.Payload)
		}
	default:
		t.Fatal("second app's subscriber did not receive the broadcast")
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
