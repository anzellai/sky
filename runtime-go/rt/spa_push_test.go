//go:build !js

package rt

import "testing"

// Test_Spa_newBroker_returns_a_broker asserts the constructed value implements
// the Broker interface (the standalone in-process registry the auto-split
// backend uses without a Live.app).
func Test_Spa_newBroker_returns_a_broker(t *testing.T) {
	b := Spa_newBroker("")
	if _, ok := b.(Broker); !ok {
		t.Fatalf("Spa_newBroker should return a Broker, got %T", b)
	}
}

// Test_Spa_interpretPublish_fans_out_a_publish is the core push-path unit: a
// `Cmd.publish` fed to Spa_interpretPublish reaches a broker subscriber.
func Test_Spa_interpretPublish_fans_out_a_publish(t *testing.T) {
	broker := Spa_newBroker("").(Broker)
	ch, cancel := broker.Subscribe("count")
	defer cancel()

	// Force the interpret Task (Ffi.kernel returns a `Task Error ()` thunk).
	task := Spa_interpretPublish(broker, Cmd_publish("count", 7))
	if fn, ok := task.(func() any); ok {
		fn()
	} else {
		t.Fatalf("Spa_interpretPublish should return a Task thunk (func() any), got %T", task)
	}

	select {
	case ev := <-ch:
		if AsInt(ev.Payload) != 7 {
			t.Fatalf("subscriber payload = %v, want 7", ev.Payload)
		}
	default:
		t.Fatal("publish did not reach the subscriber")
	}
}

// Test_Spa_interpretPublish_handles_batch asserts a publish nested inside a
// Cmd.batch is fanned out too.
func Test_Spa_interpretPublish_handles_batch(t *testing.T) {
	broker := Spa_newBroker("").(Broker)
	ch, cancel := broker.Subscribe("t")
	defer cancel()

	batch := Cmd_batch([]any{Cmd_none(), Cmd_publish("t", "hi")})
	Spa_interpretPublish(broker, batch).(func() any)()

	select {
	case ev := <-ch:
		if AsString(ev.Payload) != "hi" {
			t.Fatalf("subscriber payload = %v, want hi", ev.Payload)
		}
	default:
		t.Fatal("batched publish did not reach the subscriber")
	}
}

// Test_Spa_interpretPublish_ignores_non_publish asserts a non-publish Cmd
// (Cmd.none / a perform) delivers nothing — the backend fans out publishes only.
func Test_Spa_interpretPublish_ignores_non_publish(t *testing.T) {
	broker := Spa_newBroker("").(Broker)
	ch, cancel := broker.Subscribe("t")
	defer cancel()

	Spa_interpretPublish(broker, Cmd_none()).(func() any)()

	select {
	case ev := <-ch:
		t.Fatalf("Cmd.none should deliver nothing, got %v", ev.Payload)
	default:
	}
}

// Test_Spa_interpretPublish_nil_broker_is_safe asserts a foreign/nil broker is a
// no-op Ok, never a panic.
func Test_Spa_interpretPublish_nil_broker_is_safe(t *testing.T) {
	res := Spa_interpretPublish("not-a-broker", Cmd_publish("t", 1)).(func() any)()
	if _, ok := res.(SkyResult[any, any]); !ok {
		t.Fatalf("interpret with a foreign broker should return a SkyResult, got %T", res)
	}
}
