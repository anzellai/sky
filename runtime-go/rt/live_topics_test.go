package rt

// Cycle 3 P46 — pub/sub registry contract tests.
//
// What this file pins (each test maps to an acceptance criterion in
// docs/v0.15.x-hardening/plans/CYCLE-03-planner.md P46 + the design
// doc docs/skylive/pubsub-design.md):
//
//   - Subscribe → Publish → receive (single subscriber)
//     ............................. design doc §3.1 / §4.2
//   - Multiple subscribers each receive the event
//     ............................. design doc §3.1 fan-out shape
//   - Cancel stops further delivery to that subscriber
//     ............................. design doc §3.1.1 cancel semantics
//   - Refcount-to-zero drops the topic entry from the registry
//     ............................. design doc §3.5 prereq 5
//   - Memory-bound — N subs across M topics, all closed → registry empty
//     ............................. design doc §3.1 memory bound test
//   - Concurrent Publish + Subscribe + Cancel under -race
//     ............................. design doc §9.6
//   - Echo-to-publisher is the default (publisher's own sub gets the
//     event) .......................... design doc Q2 locked default
//   - SessionStore.Broker() returns the SAME pointer the app caches
//     ............................. design doc §3.1.1 store seam
//   - Diff-mode subscription helper (added/removed sets)
//     ............................. design doc §4.1 diff-mode
//   - markDone releases every active subscription on the session
//     ............................. design doc §4.4 cleanup
//   - End-to-end architecture diagram §3 — store seam + app.topics +
//     publish + fan-out + refcount-to-zero (single Test exercising
//     every step in order).
//   - Broker interface seam — a structurally-equivalent memoryBroker
//     drop-in (mimicking what a future RedisBroker / GcpPubsubBroker
//     would look like) round-trips through the same SessionStore.Broker()
//     accessor.

import (
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────

// recvWithin reads one event from ch with a deadline. Returns the
// event + ok flag. Used so a test failure surfaces as a clear
// "expected delivery, got none" rather than a hung test.
func recvWithin(t *testing.T, ch <-chan SessionEvent, d time.Duration) (SessionEvent, bool) {
	t.Helper()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(d):
		return SessionEvent{}, false
	}
}

// expectNoRecvWithin asserts ch produces NO event within d. Used by
// the cancel test (post-cancel events MUST NOT arrive).
func expectNoRecvWithin(t *testing.T, ch <-chan SessionEvent, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event delivered after cancel: %+v", ev)
	case <-time.After(d):
		// expected
	}
}

// ────────────────────────────────────────────────────────────────────
// Single-subscriber fan-out
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_SubscribePublishReceive(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	ch, cancel := r.Subscribe("chat-room-1")
	defer cancel()

	delivered := r.Publish("chat-room-1", SessionEvent{Payload: "hi", Origin: "sid-a"})
	if delivered != 1 {
		t.Fatalf("Publish delivered=%d, want 1", delivered)
	}
	ev, ok := recvWithin(t, ch, 100*time.Millisecond)
	if !ok {
		t.Fatalf("expected one event within 100ms")
	}
	if ev.Topic != "chat-room-1" || ev.Payload != "hi" || ev.Origin != "sid-a" {
		t.Fatalf("event mismatch: %+v", ev)
	}
}

func TestTopicRegistry_PublishToNonExistentTopic(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()
	delivered := r.Publish("never-subscribed", SessionEvent{Payload: "lost"})
	if delivered != 0 {
		t.Fatalf("Publish to empty topic: delivered=%d, want 0", delivered)
	}
}

// ────────────────────────────────────────────────────────────────────
// Multi-subscriber fan-out
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_MultipleSubscribersAllReceive(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	const N = 5
	chans := make([]<-chan SessionEvent, N)
	cancels := make([]func(), N)
	for i := 0; i < N; i++ {
		chans[i], cancels[i] = r.Subscribe("broadcast")
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	delivered := r.Publish("broadcast", SessionEvent{Payload: "announce"})
	if delivered != N {
		t.Fatalf("Publish delivered=%d, want %d", delivered, N)
	}
	for i, ch := range chans {
		ev, ok := recvWithin(t, ch, 100*time.Millisecond)
		if !ok {
			t.Fatalf("subscriber %d did not receive event", i)
		}
		if ev.Payload != "announce" {
			t.Fatalf("subscriber %d payload mismatch: %+v", i, ev)
		}
	}
}

// ────────────────────────────────────────────────────────────────────
// Echo-to-publisher (Q2 default — locked behaviour)
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_EchoToPublisherByDefault(t *testing.T) {
	// Echo-by-default matches Redis/NATS/MQTT (design doc Q2). The
	// publisher's own subscription on the same topic SHOULD receive
	// its own event; an app that wants suppression filters on
	// event.Origin in `update`.
	r := newTopicRegistry(8)
	defer r.Close()

	pubCh, cancelPub := r.Subscribe("typing")
	defer cancelPub()

	delivered := r.Publish("typing", SessionEvent{Payload: "alice", Origin: "sid-alice"})
	if delivered != 1 {
		t.Fatalf("echo test: delivered=%d, want 1 (publisher itself)", delivered)
	}
	ev, ok := recvWithin(t, pubCh, 100*time.Millisecond)
	if !ok {
		t.Fatalf("publisher's own subscription did not receive event")
	}
	if ev.Origin != "sid-alice" {
		t.Fatalf("event.Origin mismatch: got %q want sid-alice", ev.Origin)
	}
}

// ────────────────────────────────────────────────────────────────────
// Cancel + refcount drop
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_CancelStopsDelivery(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	ch, cancel := r.Subscribe("ephemeral")
	// Sanity: a Publish lands before cancel.
	r.Publish("ephemeral", SessionEvent{Payload: "before"})
	if _, ok := recvWithin(t, ch, 100*time.Millisecond); !ok {
		t.Fatalf("pre-cancel event lost")
	}

	cancel()

	// Post-cancel publish: the registry no longer has this
	// subscriber so the Publish must not deliver to ch.
	delivered := r.Publish("ephemeral", SessionEvent{Payload: "after"})
	if delivered != 0 {
		t.Fatalf("post-cancel Publish delivered=%d, want 0", delivered)
	}
	expectNoRecvWithin(t, ch, 50*time.Millisecond)
}

func TestTopicRegistry_CancelIdempotent(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()
	_, cancel := r.Subscribe("idem-topic")
	// Double-cancel must be safe (sync.Once gate) — concurrent
	// setupSubscriptions teardown + markDone teardown can both
	// fire on the SAME registration.
	cancel()
	cancel()
	cancel()
	if r.TopicCount() != 0 {
		t.Fatalf("after triple-cancel TopicCount=%d, want 0", r.TopicCount())
	}
}

func TestTopicRegistry_RefcountDropsToZero(t *testing.T) {
	r := newTopicRegistry(8)
	defer r.Close()

	if got := r.TopicCount(); got != 0 {
		t.Fatalf("baseline TopicCount=%d, want 0", got)
	}

	// Two subscribers on the same topic.
	_, c1 := r.Subscribe("once")
	_, c2 := r.Subscribe("once")

	if got := r.SubscriberCount("once"); got != 2 {
		t.Fatalf("after 2 Subscribes SubscriberCount=%d, want 2", got)
	}
	if got := r.TopicCount(); got != 1 {
		t.Fatalf("after 2 Subscribes TopicCount=%d, want 1", got)
	}

	c1()
	if got := r.SubscriberCount("once"); got != 1 {
		t.Fatalf("after first cancel SubscriberCount=%d, want 1", got)
	}
	if got := r.TopicCount(); got != 1 {
		t.Fatalf("after first cancel TopicCount=%d, want 1 (still one sub)", got)
	}

	c2()
	if got := r.SubscriberCount("once"); got != 0 {
		t.Fatalf("after final cancel SubscriberCount=%d, want 0", got)
	}
	if got := r.TopicCount(); got != 0 {
		t.Fatalf("after final cancel TopicCount=%d, want 0 (entry dropped)", got)
	}
}

// ────────────────────────────────────────────────────────────────────
// Memory-bound test — design doc §3.1 prereq 5
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_MemoryBound_ManySubsThenAllCancel(t *testing.T) {
	// Open 1,000 subscriptions spread across 100 topics, then close
	// them all and assert the registry returns to zero entries. The
	// load-bearing test that prevents a future refactor from
	// regressing the cleanup-on-zero contract.
	r := newTopicRegistry(8)
	defer r.Close()

	const Topics = 100
	const SubsPerTopic = 10
	cancels := make([]func(), 0, Topics*SubsPerTopic)
	for i := 0; i < Topics; i++ {
		topic := fmt.Sprintf("topic-%03d", i)
		for j := 0; j < SubsPerTopic; j++ {
			_, c := r.Subscribe(topic)
			cancels = append(cancels, c)
		}
	}
	if got, want := r.TopicCount(), Topics; got != want {
		t.Fatalf("after seeding TopicCount=%d want %d", got, want)
	}
	for _, c := range cancels {
		c()
	}
	if got := r.TopicCount(); got != 0 {
		t.Fatalf("after cancel-all TopicCount=%d want 0 (registry leak)", got)
	}
}

func TestTopicRegistry_MemoryBound_RepeatNoGoroutineLeak(t *testing.T) {
	// Same shape as the memory-bound test but repeated 10× with a
	// goroutine baseline check between rounds. The registry itself
	// spawns NO goroutines (it's purely sync) — this test pins that
	// invariant so a future refactor that adds eg. a cleanup ticker
	// surfaces here.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for round := 0; round < 10; round++ {
		r := newTopicRegistry(8)
		const N = 100
		cancels := make([]func(), N)
		for i := 0; i < N; i++ {
			_, c := r.Subscribe(fmt.Sprintf("topic-%d", i%10))
			cancels[i] = c
		}
		for _, c := range cancels {
			c()
		}
		_ = r.Close()
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	current := runtime.NumGoroutine()
	if current > baseline+1 {
		t.Fatalf("registry leaked goroutines: baseline=%d current=%d", baseline, current)
	}
}

// ────────────────────────────────────────────────────────────────────
// Concurrent Subscribe + Publish + Cancel — go test -race surface
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_ConcurrentSubscribePublishCancel_RaceClean(t *testing.T) {
	// 1,000 mixed ops across 10 goroutines on 8 topics. Lock
	// discipline at topicRegistry.mu must hold up under -race.
	r := newTopicRegistry(8)
	defer r.Close()

	const Workers = 10
	const OpsPerWorker = 100
	var wg sync.WaitGroup
	wg.Add(Workers)
	for w := 0; w < Workers; w++ {
		go func(id int) {
			defer wg.Done()
			for op := 0; op < OpsPerWorker; op++ {
				topic := fmt.Sprintf("t-%d", op%8)
				ch, cancel := r.Subscribe(topic)
				// Publish a few events; drain whatever lands.
				for k := 0; k < 3; k++ {
					r.Publish(topic, SessionEvent{
						Payload: id*1000 + op*10 + k,
						Origin:  strconv.Itoa(id),
					})
				}
				// Drain non-blockingly so a missed event (channel
				// full → drop) doesn't hang the worker.
				for drain := 0; drain < 3; drain++ {
					select {
					case <-ch:
					default:
					}
				}
				cancel()
			}
		}(w)
	}
	wg.Wait()
	// All workers cancelled; registry must be empty.
	if got := r.TopicCount(); got != 0 {
		t.Fatalf("after concurrent workers TopicCount=%d want 0", got)
	}
}

// ────────────────────────────────────────────────────────────────────
// Backpressure — slow subscriber drops
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_BackpressureDropsOnFullChannel(t *testing.T) {
	// subBuf=1 channel; publish more than the buffer fits without
	// draining → the extra events drop via the `default:` arm. The
	// publisher's `delivered` count reflects the actual successful
	// pushes, not the attempted ones.
	r := newTopicRegistry(1)
	defer r.Close()

	_, cancel := r.Subscribe("slow")
	defer cancel()

	// First Publish fills the buffer.
	d1 := r.Publish("slow", SessionEvent{Payload: 1})
	if d1 != 1 {
		t.Fatalf("first Publish delivered=%d want 1", d1)
	}
	// Second Publish to a now-full channel: drops.
	d2 := r.Publish("slow", SessionEvent{Payload: 2})
	if d2 != 0 {
		t.Fatalf("buffer-full Publish delivered=%d want 0 (drop)", d2)
	}
}

// ────────────────────────────────────────────────────────────────────
// Topic canonicalisation
// ────────────────────────────────────────────────────────────────────

func TestTopicRegistry_PublishCanonicalisesTopic(t *testing.T) {
	// Caller might leave event.Topic blank (only the registry-side
	// key matters); Publish must overwrite event.Topic with the
	// registry-key string so subscribers see a populated value.
	r := newTopicRegistry(8)
	defer r.Close()
	ch, cancel := r.Subscribe("canonical")
	defer cancel()
	r.Publish("canonical", SessionEvent{Payload: "x"}) // Topic deliberately blank
	ev, ok := recvWithin(t, ch, 100*time.Millisecond)
	if !ok {
		t.Fatalf("no event received")
	}
	if ev.Topic != "canonical" {
		t.Fatalf("event.Topic=%q want %q (canonicalisation broken)", ev.Topic, "canonical")
	}
}

// ────────────────────────────────────────────────────────────────────
// Diff-mode subscription helper
// ────────────────────────────────────────────────────────────────────

func TestDiffSubscriptions_AddedRemovedSets(t *testing.T) {
	old := map[string]*subRegistration{
		"a": {topic: "a"},
		"b": {topic: "b"},
	}
	desired := map[string]any{
		"a": nil,
		"c": nil,
	}
	added, removed := diffSubscriptions(old, desired)
	if !sameSet(added, []string{"c"}) {
		t.Fatalf("added=%v want [c]", added)
	}
	if !sameSet(removed, []string{"b"}) {
		t.Fatalf("removed=%v want [b]", removed)
	}
}

func TestDiffSubscriptions_NoChange(t *testing.T) {
	// Topics in BOTH old and desired keep their existing channel +
	// goroutine — neither added nor removed. This is the load-
	// bearing diff-mode property (design doc §4.1): a re-render
	// that doesn't change the subscription set does NOT bounce the
	// registry. Without this, every dispatch cycles every topic
	// through cancel + re-subscribe, racing broadcast loss.
	old := map[string]*subRegistration{
		"a": {topic: "a"},
		"b": {topic: "b"},
	}
	desired := map[string]any{
		"a": nil,
		"b": nil,
	}
	added, removed := diffSubscriptions(old, desired)
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("no-change diff produced added=%v removed=%v want empty", added, removed)
	}
}

func TestDiffSubscriptions_AllRemoved(t *testing.T) {
	old := map[string]*subRegistration{"a": {topic: "a"}, "b": {topic: "b"}}
	desired := map[string]any{}
	added, removed := diffSubscriptions(old, desired)
	if len(added) != 0 {
		t.Fatalf("added=%v want empty", added)
	}
	if !sameSet(removed, []string{"a", "b"}) {
		t.Fatalf("removed=%v want [a b]", removed)
	}
}

func TestDiffSubscriptions_AllAdded(t *testing.T) {
	old := map[string]*subRegistration{}
	desired := map[string]any{"x": nil, "y": nil}
	added, removed := diffSubscriptions(old, desired)
	if !sameSet(added, []string{"x", "y"}) {
		t.Fatalf("added=%v want [x y]", added)
	}
	if len(removed) != 0 {
		t.Fatalf("removed=%v want empty", removed)
	}
}

// sameSet returns true iff the two slices contain the same string
// elements (order-insensitive). Defined inline so the diff helper
// tests don't need a slices-sort import that would otherwise pull
// the runtime-go module's go-version min above what CI sets.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, v := range a {
		m[v]++
	}
	for _, v := range b {
		m[v]--
		if m[v] < 0 {
			return false
		}
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

// ────────────────────────────────────────────────────────────────────
// SessionStore.Broker() seam — every backend exposes the same Broker
// (the v0.15.x default in-process *topicRegistry); future cross-
// process backends override the broker field without touching any
// call site that goes through this accessor.
// ────────────────────────────────────────────────────────────────────

func TestSessionStore_BrokerReturnsTopicRegistry(t *testing.T) {
	store := newMemoryStore(time.Minute)
	defer store.Close()
	b := store.Broker()
	if b == nil {
		t.Fatalf("memoryStore.Broker() returned nil")
	}
	// Must round-trip a Subscribe/Publish.
	ch, cancel := b.Subscribe("via-store")
	defer cancel()
	if got := b.Publish("via-store", SessionEvent{Payload: "ping"}); got != 1 {
		t.Fatalf("via-store Publish delivered=%d want 1", got)
	}
	ev, ok := recvWithin(t, ch, 100*time.Millisecond)
	if !ok || ev.Payload != "ping" {
		t.Fatalf("via-store delivery failed: ok=%v ev=%+v", ok, ev)
	}
}

func TestSessionStore_BrokerIsStable(t *testing.T) {
	// Two Broker() calls on the same store MUST return the same
	// pointer — the broker is per-store, not per-call. Without this,
	// app.topics caching in liveAppRun would point at a different
	// registry than the one the store sees.
	store := newMemoryStore(time.Minute)
	defer store.Close()
	a := store.Broker()
	b := store.Broker()
	if a != b {
		t.Fatalf("Broker() returned distinct instances on successive calls — caching invariant broken")
	}
}

// ────────────────────────────────────────────────────────────────────
// markDone releases every active subscription on the session
// ────────────────────────────────────────────────────────────────────

func TestLiveSession_MarkDoneReleasesActiveSubs(t *testing.T) {
	// Per design doc §4.4: session.Delete (via markDone) MUST walk
	// activeSubs and call each cancel so the broker refcounts drop
	// to zero. Without this, a TTL-evicted session keeps its
	// broker registrations alive for the lifetime of the process.
	r := newTopicRegistry(8)
	sess := &liveSession{
		done: make(chan struct{}),
	}
	_, cancel1 := r.Subscribe("session-topic-a")
	_, cancel2 := r.Subscribe("session-topic-b")
	sess.activeSubs = map[string]*subRegistration{
		"session-topic-a": {topic: "session-topic-a", cancel: cancel1},
		"session-topic-b": {topic: "session-topic-b", cancel: cancel2},
	}
	if got := r.TopicCount(); got != 2 {
		t.Fatalf("pre-markDone TopicCount=%d want 2", got)
	}

	sess.markDone()

	if got := r.TopicCount(); got != 0 {
		t.Fatalf("post-markDone TopicCount=%d want 0 (broker leak)", got)
	}
	if sess.activeSubs != nil {
		t.Fatalf("post-markDone activeSubs not cleared: %v", sess.activeSubs)
	}
	// Idempotent: a second markDone must not panic + must not
	// re-enter the cancel loop (sync.Once protects).
	sess.markDone()
}

// ────────────────────────────────────────────────────────────────────
// End-to-end §3 architecture diagram
// ────────────────────────────────────────────────────────────────────

func TestPubsub_E2E_StoreSeam_AppTopics_PublishFanout_RefcountZero(t *testing.T) {
	// Exercise every step of the design doc §3 architecture diagram
	// in order so a future refactor that breaks ANY link surfaces
	// here. Steps:
	//   1. Construct an app with a store backend.
	//   2. app.topics == app.store.Broker() (one shared registry).
	//   3. Open two subscriptions across two sessions.
	//   4. Publish from a third "session" (just an Origin sid).
	//   5. Both subscribers receive (echo-by-default extra-validates
	//      against the publisher's own subscription).
	//   6. Subscriber-1 cancels → refcount drops to 1.
	//   7. Subscriber-2 cancels → refcount drops to 0; topic entry
	//      removed.
	store := newMemoryStore(time.Minute)
	defer store.Close()
	app := &liveApp{
		store:  store,
		topics: store.Broker(),
		locker: newSessionLocker(),
	}

	// Step 2 — the cache is the SAME pointer as the store's broker.
	if app.topics == nil {
		t.Fatalf("app.topics nil after wiring")
	}
	if app.topics != app.store.Broker() {
		t.Fatalf("app.topics differs from app.store.Broker() — caching broken")
	}

	// Step 3.
	ch1, cancel1 := app.topics.Subscribe("e2e")
	ch2, cancel2 := app.topics.Subscribe("e2e")

	// Step 4-5.
	delivered := app.topics.Publish("e2e", SessionEvent{
		Payload: "round-trip",
		Origin:  "sid-pub",
	})
	if delivered != 2 {
		t.Fatalf("E2E Publish delivered=%d want 2", delivered)
	}
	for i, ch := range []<-chan SessionEvent{ch1, ch2} {
		ev, ok := recvWithin(t, ch, 100*time.Millisecond)
		if !ok || ev.Payload != "round-trip" {
			t.Fatalf("E2E subscriber %d miss: ok=%v ev=%+v", i, ok, ev)
		}
	}

	// Step 6.
	cancel1()
	if got := app.topics.(*topicRegistry).SubscriberCount("e2e"); got != 1 {
		t.Fatalf("post-cancel-1 SubscriberCount=%d want 1", got)
	}
	// Step 7.
	cancel2()
	tr := app.topics.(*topicRegistry)
	if got := tr.SubscriberCount("e2e"); got != 0 {
		t.Fatalf("post-cancel-2 SubscriberCount=%d want 0", got)
	}
	if got := tr.TopicCount(); got != 0 {
		t.Fatalf("post-cancel-2 TopicCount=%d want 0", got)
	}
}

// ────────────────────────────────────────────────────────────────────
// Broker-interface drop-in seam — a structurally equivalent broker
// (the shape a future RedisBroker / GcpPubsubBroker would have)
// round-trips through SessionStore.Broker() identically.
//
// This is the acceptance criterion 4 lock test: "Subscribe interface
// is broker-agnostic; v0.16+ can add Redis/Cloud Pub/Sub plug-ins by
// implementing the interface differently. Test the seam by adding a
// memoryBroker that's structurally identical to what a real Redis
// adapter would look like."
// ────────────────────────────────────────────────────────────────────

// stubMemoryBroker is the minimal Broker implementation a v0.16+
// network-backed broker would mirror. Internally it's just a thin
// counter; the test only verifies that the SessionStore.Broker()
// seam accepts a non-*topicRegistry Broker and round-trips through it.
type stubMemoryBroker struct {
	mu           sync.Mutex
	subscribed   map[string]int
	published    map[string]int
	publishCalls atomic.Int64
}

func newStubMemoryBroker() *stubMemoryBroker {
	return &stubMemoryBroker{
		subscribed: map[string]int{},
		published:  map[string]int{},
	}
}

func (s *stubMemoryBroker) Subscribe(topic string) (<-chan SessionEvent, func()) {
	return s.SubscribeWithOwner(topic, "")
}

func (s *stubMemoryBroker) SubscribeWithOwner(topic, _ownerSid string) (<-chan SessionEvent, func()) {
	s.mu.Lock()
	s.subscribed[topic]++
	s.mu.Unlock()
	ch := make(chan SessionEvent, 1)
	cancelOnce := sync.Once{}
	return ch, func() {
		cancelOnce.Do(func() {
			s.mu.Lock()
			s.subscribed[topic]--
			if s.subscribed[topic] <= 0 {
				delete(s.subscribed, topic)
			}
			s.mu.Unlock()
		})
	}
}

func (s *stubMemoryBroker) Publish(topic string, event SessionEvent) int {
	s.publishCalls.Add(1)
	s.mu.Lock()
	s.published[topic]++
	count := s.subscribed[topic]
	s.mu.Unlock()
	return count
}

func (s *stubMemoryBroker) Close() error {
	s.mu.Lock()
	s.subscribed = map[string]int{}
	s.published = map[string]int{}
	s.mu.Unlock()
	return nil
}

// memoryStoreWithBroker is a test fixture: a memory-store-shaped
// wrapper that overrides the Broker accessor to return a stub. This
// mirrors how a future RedisStore would compose `*memoryStore` (or
// implement SessionStore directly) and return its own Broker.
type memoryStoreWithBroker struct {
	*memoryStore
	stub *stubMemoryBroker
}

func (m *memoryStoreWithBroker) Broker() Broker { return m.stub }

func TestBrokerSeam_StubBrokerPluggable(t *testing.T) {
	stub := newStubMemoryBroker()
	wrap := &memoryStoreWithBroker{
		memoryStore: newMemoryStore(time.Minute),
		stub:        stub,
	}
	defer wrap.Close()

	// Compile-time check: wrap satisfies SessionStore. Catch any
	// future interface drift at build time, not just run time.
	var _ SessionStore = wrap

	// app.topics caching uses store.Broker() — must resolve to the
	// stub, not the underlying memoryStore's *topicRegistry.
	app := &liveApp{store: wrap, topics: wrap.Broker(), locker: newSessionLocker()}
	if app.topics != stub {
		t.Fatalf("app.topics wired to %T, want stubMemoryBroker", app.topics)
	}

	// Round-trip a Subscribe + Publish through the stub to prove
	// the interface is broker-agnostic.
	_, cancel := app.topics.Subscribe("stub-topic")
	defer cancel()
	if got := app.topics.Publish("stub-topic", SessionEvent{Payload: "x"}); got != 1 {
		t.Fatalf("stub.Publish delivered=%d want 1", got)
	}
	if got := stub.publishCalls.Load(); got != 1 {
		t.Fatalf("stub publishCalls=%d want 1 — wiring broken", got)
	}
}
