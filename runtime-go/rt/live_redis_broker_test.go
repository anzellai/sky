package rt

// Cross-instance pub/sub broker (Phase 2). Two redisBrokers sharing one
// miniredis simulate two app instances behind a load balancer. Covers:
// cross-instance delivery, no-self-double-delivery (instance-id dedup),
// payload round-trip, per-topic Redis subscribe/unsubscribe refcounting,
// per-instance monotonic globalSeq re-stamping, graceful degradation on a
// non-encodable payload, Close teardown, and broker selection.

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// twoInstances spins up one miniredis and two brokers on separate
// clients — the wire-accurate shape of two instances sharing one Redis.
func twoInstances(t *testing.T) (a, b *redisBroker) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	ca := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	a = newRedisBroker(ca, true)
	b = newRedisBroker(cb, true)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// recvEvent waits for one SessionEvent or fails.
func recvEvent(t *testing.T, ch <-chan SessionEvent, what string) SessionEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: no event within deadline", what)
		return SessionEvent{}
	}
}

func expectNoEvent(t *testing.T, ch <-chan SessionEvent, within time.Duration, what string) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("%s: unexpected event payload=%v", what, ev.Payload)
	case <-time.After(within):
	}
}

// settle gives the async Redis SUBSCRIBE + Channel() reader time to land
// before a publish (miniredis is in-process + fast, so this is short).
func settle() { time.Sleep(150 * time.Millisecond) }

func TestRedisBroker_CrossInstanceDelivery(t *testing.T) {
	a, b := twoInstances(t)
	chB, cancelB := b.SubscribeWithOwner("room1", "sidB")
	defer cancelB()
	settle()

	got := a.Publish("room1", SessionEvent{Payload: "hello from A", Origin: "sidA"})
	// A has no local subscribers, so its local delivered count is 0 —
	// delivery to B happens via Redis, asynchronously.
	if got != 0 {
		t.Fatalf("A local delivered = %d, want 0 (B is on another instance)", got)
	}
	ev := recvEvent(t, chB, "B receives A's publish")
	if ev.Payload != "hello from A" {
		t.Fatalf("payload=%v", ev.Payload)
	}
	if ev.Topic != "room1" {
		t.Fatalf("topic=%q", ev.Topic)
	}
	if ev.GlobalSeq <= 0 {
		t.Fatalf("globalSeq not stamped: %d", ev.GlobalSeq)
	}
}

func TestRedisBroker_NoSelfDoubleDelivery(t *testing.T) {
	a, b := twoInstances(t)
	// A subscribes to its OWN topic and also publishes to it.
	chA, cancelA := a.SubscribeWithOwner("room1", "sidA")
	defer cancelA()
	chB, cancelB := b.SubscribeWithOwner("room1", "sidB")
	defer cancelB()
	settle()

	delivered := a.Publish("room1", SessionEvent{Payload: "x", Origin: "sidA"})
	if delivered != 1 {
		t.Fatalf("A local delivered = %d, want 1 (its own subscriber)", delivered)
	}
	// A's subscriber must get it exactly ONCE (local fan), NOT twice
	// (the Redis echo of A's own publish is dropped by instance-id).
	first := recvEvent(t, chA, "A's own subscriber, first")
	if first.Payload != "x" {
		t.Fatalf("A payload=%v", first.Payload)
	}
	expectNoEvent(t, chA, 500*time.Millisecond, "A's own subscriber must not receive its Redis echo")

	// B (other instance) gets it exactly once via Redis.
	bev := recvEvent(t, chB, "B receives")
	if bev.Payload != "x" {
		t.Fatalf("B payload=%v", bev.Payload)
	}
	expectNoEvent(t, chB, 300*time.Millisecond, "B must receive exactly once")
}

func TestRedisBroker_PayloadRoundTrip_Record(t *testing.T) {
	a, b := twoInstances(t)
	chB, cancelB := b.SubscribeWithOwner("chat", "sidB")
	defer cancelB()
	settle()

	// Record-shaped payload — the common Cmd.publish case. map[string]any
	// is gob-registered at init, so it round-trips across the wire.
	payload := map[string]any{"user": "alice", "text": "hi", "n": 42}
	a.Publish("chat", SessionEvent{Payload: payload, Origin: "sidA"})

	ev := recvEvent(t, chB, "B receives record")
	got, ok := ev.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", ev.Payload)
	}
	if got["user"] != "alice" || got["text"] != "hi" {
		t.Fatalf("record did not round-trip: %#v", got)
	}
	if got["n"] != 42 {
		t.Fatalf("int field did not round-trip: %#v (%T)", got["n"], got["n"])
	}
}

func TestRedisBroker_PerInstanceMonotonicSeq(t *testing.T) {
	a, b := twoInstances(t)
	chB, cancelB := b.SubscribeWithOwner("t", "sidB")
	defer cancelB()
	settle()

	for i := 0; i < 5; i++ {
		a.Publish("t", SessionEvent{Payload: i, Origin: "sidA"})
	}
	var last int64
	for i := 0; i < 5; i++ {
		ev := recvEvent(t, chB, "seq stream")
		if ev.GlobalSeq <= last {
			t.Fatalf("globalSeq not strictly increasing on B's stream: got %d after %d", ev.GlobalSeq, last)
		}
		last = ev.GlobalSeq
	}
}

func TestRedisBroker_PerTopicSubscribeRefcount(t *testing.T) {
	a, b := twoInstances(t)

	// Two local subscribers to the same topic on B → ONE Redis SUBSCRIBE.
	ch1, cancel1 := b.SubscribeWithOwner("room", "s1")
	_, cancel2 := b.SubscribeWithOwner("room", "s2")
	settle()
	if b.subCount["room"] != 2 {
		t.Fatalf("subCount=%d, want 2", b.subCount["room"])
	}
	if b.TopicCount() != 1 {
		t.Fatalf("local TopicCount=%d, want 1", b.TopicCount())
	}

	// A publish reaches B while at least one local sub remains.
	a.Publish("room", SessionEvent{Payload: "p1", Origin: "sidA"})
	if ev := recvEvent(t, ch1, "sub1 gets p1"); ev.Payload != "p1" {
		t.Fatalf("payload=%v", ev.Payload)
	}

	// Drop one → still subscribed on Redis (refcount 1).
	cancel1()
	if b.subCount["room"] != 1 {
		t.Fatalf("after one cancel subCount=%d, want 1", b.subCount["room"])
	}

	// Drop the last → Redis UNSUBSCRIBE, topic entry gone.
	cancel2()
	if _, ok := b.subCount["room"]; ok {
		t.Fatalf("after last cancel subCount still present: %v", b.subCount["room"])
	}
	if b.TopicCount() != 0 {
		t.Fatalf("local TopicCount=%d, want 0 after all cancels", b.TopicCount())
	}
	settle()
	// A publish now must NOT be delivered anywhere on B (unsubscribed).
	// (No channel to assert on — the absence is covered by subCount/TopicCount
	// above + the unsubscribe call; this publish just must not panic.)
	a.Publish("room", SessionEvent{Payload: "p2", Origin: "sidA"})
}

func TestRedisBroker_GracefulDegrade_NonEncodablePayload(t *testing.T) {
	a, _ := twoInstances(t)
	// A subscribes locally; the payload is a channel — gob cannot encode
	// it, so the cross-instance hop must be skipped WITHOUT breaking the
	// local delivery and WITHOUT panicking.
	chA, cancelA := a.SubscribeWithOwner("local", "sidA")
	defer cancelA()
	settle()

	weird := make(chan int) // not gob-encodable
	delivered := a.Publish("local", SessionEvent{Payload: weird, Origin: "sidA"})
	if delivered != 1 {
		t.Fatalf("local delivered=%d, want 1 (degrade must not drop local delivery)", delivered)
	}
	ev := recvEvent(t, chA, "local subscriber still gets non-encodable payload")
	if _, ok := ev.Payload.(chan int); !ok {
		t.Fatalf("local payload lost its identity: %T", ev.Payload)
	}
}

func TestRedisBroker_Close_Idempotent(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b := newRedisBroker(c, true)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	// A publish after Close must not panic and delivers nothing remotely.
	if got := b.Publish("t", SessionEvent{Payload: "x"}); got != 0 {
		t.Fatalf("post-close local delivered=%d, want 0", got)
	}
}

func TestRedisBroker_ConcurrentPublish_NoRace(t *testing.T) {
	a, b := twoInstances(t)
	chB, cancelB := b.SubscribeWithOwner("busy", "sidB")
	defer cancelB()
	settle()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			a.Publish("busy", SessionEvent{Payload: i, Origin: "sidA"})
		}
	}()
	go func() {
		defer wg.Done()
		// A second concurrent publisher on the SAME instance (B) — local
		// deliveries + a Redis publish that B drops as its own echo.
		for i := 0; i < n; i++ {
			b.Publish("busy", SessionEvent{Payload: 1000 + i, Origin: "sidB"})
		}
	}()

	received := 0
	deadline := time.After(4 * time.Second)
	// Expect at least the n from A (cross-instance) + n from B (local).
	for received < n {
		select {
		case <-chB:
			received++
		case <-deadline:
			// Best-effort delivery: a few may drop under buffer pressure.
			// Require the bulk to arrive so the path is exercised, not a
			// strict count (that would be a flaky assertion on a
			// best-effort channel).
			if received < n/2 {
				t.Fatalf("received only %d/%d — delivery path looks broken", received, n)
			}
			wg.Wait()
			return
		}
	}
	wg.Wait()
}

// ── Broker selection ─────────────────────────────────────────────────

func TestBrokerSelection_EscapeHatch(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer c.Close()

	t.Setenv("SKY_LIVE_BROKER", "inprocess")
	loadEnvForTest() // re-read any cached prefix state (no-op if none)
	got := brokerForRedisStore(c)
	if _, ok := got.(*topicRegistry); !ok {
		t.Fatalf("SKY_LIVE_BROKER=inprocess should yield in-process registry, got %T", got)
	}
	_ = got.Close()
}

func TestBrokerSelection_DefaultRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	got := brokerForRedisStore(c)
	rb, ok := got.(*redisBroker)
	if !ok {
		t.Fatalf("Redis store default should be a cross-instance broker, got %T", got)
	}
	_ = rb.Close()
}

func TestBrokerSelection_OverrideUnsetIsNoop(t *testing.T) {
	t.Setenv("SKY_LIVE_BROKER_URL", "")
	fallback := newTopicRegistry(0)
	if got := maybeOverrideBroker(fallback, ""); got != Broker(fallback) {
		t.Fatalf("unset SKY_LIVE_BROKER_URL must return the fallback unchanged")
	}
}

func TestBrokerSelection_OverrideSkipsWhenAlreadyRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	rb := newRedisBroker(c, true)
	defer rb.Close()

	t.Setenv("SKY_LIVE_BROKER_URL", "redis://localhost:6379")
	if got := maybeOverrideBroker(rb, ""); got != Broker(rb) {
		t.Fatalf("override must not replace an existing cross-instance broker")
	}
}

// loadEnvForTest is a hook point; SKY_LIVE_BROKER is read live via
// skyGetenv so there is no cached state to refresh, but keeping the call
// documents intent if a cache is added later.
func loadEnvForTest() {}

// TestPubSubPayloadRoundTrip_SkyShapes pins that the ACTUAL Sky runtime
// value shapes used as pub/sub payloads survive the cross-instance gob
// codec — most importantly `Dict String String` (map[string]string),
// which the multi-session-chat example publishes and which is NOT
// registered by the store's named-struct walkers. A regression here
// would silently degrade those payloads to local-only delivery.
func TestPubSubPayloadRoundTrip_SkyShapes(t *testing.T) {
	cases := []struct {
		name string
		val  any
	}{
		{"dict-string-string", map[string]string{"user": "alice", "text": "hi"}},
		{"dict-untyped", map[string]any{"a": 1, "b": "two", "c": true}},
		{"sky-adt", SkyADT{Tag: 2, SkyName: "Msg", Fields: []any{"payload", 7}}},
		{"tuple2", SkyTuple2{V0: "k", V1: 99}},
		{"list-any", []any{"x", "y", "z"}},
		{"list-string", []string{"a", "b"}},
		{"nested", map[string]any{"items": []any{map[string]string{"id": "1"}}, "n": 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := encodePubSubPayload(tc.val)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := decodePubSubPayload(enc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, tc.val) {
				t.Fatalf("round-trip mismatch:\n want %#v\n  got %#v", tc.val, got)
			}
		})
	}
}

// Test_effectiveBrokerUrl_precedence pins the ONE resolution rule the config
// layer promises for the pub/sub broker URL: an operator's SKY_LIVE_BROKER_URL
// wins over the in-code value (Sky.Config.withLiveBroker / spa-split --broker);
// with no env, the in-code value is used; with neither, it is empty (in-process).
func Test_effectiveBrokerUrl_precedence(t *testing.T) {
	t.Setenv("SKY_LIVE_BROKER_URL", "redis://from-env:6379")
	if got := effectiveBrokerUrl("redis://from-config:6379"); got != "redis://from-env:6379" {
		t.Fatalf("env must win over the in-code value; got %q", got)
	}

	t.Setenv("SKY_LIVE_BROKER_URL", "")
	if got := effectiveBrokerUrl("redis://from-config:6379"); got != "redis://from-config:6379" {
		t.Fatalf("with no env, the in-code value must be used; got %q", got)
	}
	if got := effectiveBrokerUrl("  redis://spaced:6379  "); got != "redis://spaced:6379" {
		t.Fatalf("the in-code value must be trimmed; got %q", got)
	}
	if got := effectiveBrokerUrl(""); got != "" {
		t.Fatalf("with neither env nor in-code value, the result must be empty; got %q", got)
	}

	// A whitespace-only operator var is treated as unset, so the in-code value
	// still applies (env "wins" only when it names a real URL).
	t.Setenv("SKY_LIVE_BROKER_URL", "   ")
	if got := effectiveBrokerUrl("redis://from-config:6379"); got != "redis://from-config:6379" {
		t.Fatalf("a blank env var must not shadow the in-code value; got %q", got)
	}
}
