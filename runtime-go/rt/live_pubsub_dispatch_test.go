package rt

// Cycle 3 P48 — pub/sub dispatch wiring contract tests.
//
// Pins the end-to-end dispatch path lit up in P48 on top of the P46
// registry + P47 seq split:
//
//   - runCmd("publish") routes through app.Publish so the
//     SessionEvent rides through topicRegistry to subscriber
//     channels, with globalSeq stamped exactly once per call.
//
//   - setupSubscriptions walks Sub.batch + extracts every
//     "subscribeTopic" leaf, then applyTopicSubsDiff opens broker
//     subscriptions only for the *added* set and cancels only the
//     *removed* set. Topics in the intersection keep their existing
//     goroutine (design doc §4.1 — no broadcast loss in the gap).
//
//   - runSubscriberLoop dispatches incoming SessionEvents through
//     app.dispatch (same path as Cmd.perform completions) so
//     subscribers' update + view re-runs naturally rebuild handlers
//     for the new view (design doc §3.3 decision (a) — Msg-shape
//     broadcast).
//
//   - Decoder panic in toMsg consumes the event without crashing
//     the session.
//
//   - markDone teardown is exercised end-to-end through the
//     applyTopicSubsDiff registration path (not just a synthetic
//     map population — pins the wire from setupSubscriptions all
//     the way to broker refcount-zero).

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────
// Helpers: bridge a Msg-collecting fake update with a real liveApp.
// ────────────────────────────────────────────────────────────────────

// pubsubTestApp builds a minimal liveApp shape suitable for exercising
// runCmd("publish") + setupSubscriptions("subscribeTopic") end-to-end.
// The model is a *recordedDispatch; every dispatch appends the
// triggering Msg to its log so tests can assert ordering + count.
type recordedDispatch struct {
	mu   sync.Mutex
	msgs []any
}

func (r *recordedDispatch) record(m any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}

func (r *recordedDispatch) snapshot() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// pubsubTestApp returns an app whose update closes over the supplied
// *recordedDispatch (so tests can introspect the dispatch path), and
// whose subscriptions returns the supplied Sub leaf verbatim. The
// view is a constant single-element VNode — enough for dispatch to
// produce a body without going through HtmlToVNode panics in the
// view conversion layer.
func pubsubTestApp(rec *recordedDispatch, subs any) *liveApp {
	app := &liveApp{
		update: func(msg, model any) any {
			rec.record(msg)
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("pubsub-test")})
		},
		topics: newTopicRegistry(16),
	}
	if subs != nil {
		app.subscriptions = func(model any) any { return subs }
	}
	return app
}

func newPubsubSession() *liveSession {
	return &liveSession{
		sid:       "sid-test",
		sseCh:     make(chan sseFrame, 16),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		handlers:  map[string]any{},
	}
}

// pumpSubBoundary gives the broker channel + the subscriber goroutine
// a moment to deliver + dispatch + push SSE. 80 ms is generous on M1
// CI; the actual delivery latency is sub-µs.
func pumpSubBoundary() {
	time.Sleep(80 * time.Millisecond)
}

// ────────────────────────────────────────────────────────────────────
// runCmd "publish" arm
// ────────────────────────────────────────────────────────────────────

// Test_RunCmd_Publish_RoutesThroughAppPublish pins that the runCmd
// arm calls app.Publish (NOT the topicRegistry direct path) so the
// "one globalSeq bump per publish" invariant holds at the call site.
func Test_RunCmd_Publish_RoutesThroughAppPublish(t *testing.T) {
	rec := &recordedDispatch{}
	app := pubsubTestApp(rec, nil)

	// Open a subscriber on the broker so we can observe the
	// stamped globalSeq.
	ch, cancel := app.topics.Subscribe("room-1")
	defer cancel()

	sess := newPubsubSession()
	// Drive a runCmd("publish") — same shape Sky-side
	// `Std.Cmd.publish "room-1" "msg-1"` lowers to.
	app.runCmd(sess, cmdT{kind: "publish", topic: "room-1", payload: "msg-1"})

	ev, ok := recvWithin(t, ch, recvTimeout)
	if !ok {
		t.Fatalf("runCmd(publish) did not deliver to broker subscriber")
	}
	if ev.Topic != "room-1" || ev.Payload != "msg-1" {
		t.Fatalf("event mismatch: %+v", ev)
	}
	if ev.GlobalSeq != 1 {
		t.Fatalf("event.GlobalSeq=%d want 1 (app.Publish stamps on first call)", ev.GlobalSeq)
	}
	if ev.Origin != "sess.sid" && ev.Origin != "sid-test" {
		t.Fatalf("event.Origin=%q want sid-test (publishing session's sid)", ev.Origin)
	}
}

// Test_RunCmd_Publish_NoBroker_NoOp pins the safe-fallback path: a
// test-constructed liveApp with no broker MUST not panic when a
// publish runs.
func Test_RunCmd_Publish_NoBroker_NoOp(t *testing.T) {
	app := &liveApp{
		update: func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:   func(model any) any { return velement("div", nil, nil) },
		// no topics
	}
	sess := newPubsubSession()
	// MUST not panic.
	app.runCmd(sess, cmdT{kind: "publish", topic: "x", payload: "y"})
}

// Test_RunCmd_Publish_GlobalSeqMonotonicAcrossRapidPublishes pins
// the monotonic-and-gap-free contract from the call-site angle.
// Three rapid publishes from the same session land on the broker
// with globalSeq 1, 2, 3.
func Test_RunCmd_Publish_GlobalSeqMonotonicAcrossRapidPublishes(t *testing.T) {
	rec := &recordedDispatch{}
	app := pubsubTestApp(rec, nil)
	ch, cancel := app.topics.Subscribe("topic-x")
	defer cancel()
	sess := newPubsubSession()

	for i := 0; i < 3; i++ {
		app.runCmd(sess, cmdT{kind: "publish", topic: "topic-x", payload: fmt.Sprintf("p%d", i)})
	}

	for i := int64(1); i <= 3; i++ {
		ev, ok := recvWithin(t, ch, recvTimeout)
		if !ok {
			t.Fatalf("publish %d not delivered", i)
		}
		if ev.GlobalSeq != i {
			t.Fatalf("publish %d GlobalSeq=%d want %d", i, ev.GlobalSeq, i)
		}
	}
}

// ────────────────────────────────────────────────────────────────────
// Two-session fan-out — A publishes, B's subscription dispatches Msg
// ────────────────────────────────────────────────────────────────────

// Test_TwoSession_FanOut_DispatchesMsgToSubscriber is the canonical
// design-doc §3.3 + §4.3 contract: session A publishes "topic-x",
// session B is subscribed to "topic-x" via setupSubscriptions, and
// the broadcast lands as a Msg through B's update.
func Test_TwoSession_FanOut_DispatchesMsgToSubscriber(t *testing.T) {
	rec := &recordedDispatch{}
	// Subscribe to "topic-x" with a decoder that just tags the
	// payload as "received-<payload>" so the recorded msg log lets
	// us distinguish a broadcast Msg from any other source.
	subs := subT{kind: "subscribeTopic", topic: "topic-x", toMsg: func(payload any) any {
		return fmt.Sprintf("received-%v", payload)
	}}
	app := pubsubTestApp(rec, subs)

	// Session B = subscriber.
	sessB := newPubsubSession()
	sessB.sid = "sid-B"
	sessB.model = "model-B"
	app.setupSubscriptions(sessB)
	// Pump once so the subscriber goroutine is parked on its
	// select before we publish.
	time.Sleep(20 * time.Millisecond)

	// Session A = publisher. Same app; just drives a runCmd.
	sessA := newPubsubSession()
	sessA.sid = "sid-A"
	app.runCmd(sessA, cmdT{kind: "publish", topic: "topic-x", payload: "hello"})

	pumpSubBoundary()

	// Cleanup before assertions so a failing test doesn't leak
	// goroutines into the next test.
	sessB.markDone()

	msgs := rec.snapshot()
	found := false
	for _, m := range msgs {
		if s, ok := m.(string); ok && s == "received-hello" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("subscriber B did not receive broadcast as Msg; recorded: %v", msgs)
	}
}

// Test_EchoToPublisher_ViaDispatchPath is the design-doc Q2 default:
// publisher A's own subscription to "topic-x" ALSO sees the message.
// (Q2 locked echo-by-default; matches Redis/NATS/MQTT.)
func Test_EchoToPublisher_ViaDispatchPath(t *testing.T) {
	rec := &recordedDispatch{}
	subs := subT{kind: "subscribeTopic", topic: "topic-x", toMsg: func(payload any) any {
		return fmt.Sprintf("echo-%v", payload)
	}}
	app := pubsubTestApp(rec, subs)

	// Single session A — both publisher AND subscriber.
	sess := newPubsubSession()
	sess.sid = "sid-A"
	sess.model = "model-A"
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	app.runCmd(sess, cmdT{kind: "publish", topic: "topic-x", payload: "self"})
	pumpSubBoundary()
	sess.markDone()

	msgs := rec.snapshot()
	found := false
	for _, m := range msgs {
		if s, ok := m.(string); ok && s == "echo-self" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("publisher's own subscription did not see the echo; recorded: %v", msgs)
	}
}

// ────────────────────────────────────────────────────────────────────
// Diff-mode subscription update
// ────────────────────────────────────────────────────────────────────

// Test_SetupSubscriptions_DiffMode_NoSpuriousChurn pins design doc
// §4.1: subscribing to {A} then re-subscribing to {A, B} on the next
// dispatch must NOT cancel + re-subscribe A. A's existing channel +
// goroutine + broker registration are reused; only B is added.
//
// We verify by:
//   - Counting topic broker refcounts before / after each call.
//   - Confirming a publish to A AFTER the second setupSubscriptions
//     still reaches A's subscriber (proves no channel was orphaned).
func Test_SetupSubscriptions_DiffMode_NoSpuriousChurn(t *testing.T) {
	rec := &recordedDispatch{}

	// Track the subscription set we return from `subscriptions`
	// across dispatches via an atomic toggle.
	var desired atomic.Value
	desired.Store([]string{"topic-A"})

	makeSubs := func() any {
		topics := desired.Load().([]string)
		leaves := make([]any, 0, len(topics))
		for _, top := range topics {
			topCopy := top // closure
			leaves = append(leaves, subT{
				kind:  "subscribeTopic",
				topic: topCopy,
				toMsg: func(p any) any { return fmt.Sprintf("%s|%v", topCopy, p) },
			})
		}
		if len(leaves) == 1 {
			return leaves[0]
		}
		return subT{kind: "batch", batch: leaves}
	}

	app := &liveApp{
		update: func(msg, model any) any {
			rec.record(msg)
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:          func(model any) any { return velement("div", nil, []any{vtext("x")}) },
		topics:        newTopicRegistry(16),
		subscriptions: func(model any) any { return makeSubs() },
	}

	sess := newPubsubSession()

	// Pass 1: subscribe to {A}.
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)
	reg, ok := app.topics.(*topicRegistry)
	if !ok {
		t.Fatalf("topics is not *topicRegistry")
	}
	if reg.SubscriberCount("topic-A") != 1 {
		t.Fatalf("pass 1: topic-A SubscriberCount=%d want 1", reg.SubscriberCount("topic-A"))
	}
	if reg.SubscriberCount("topic-B") != 0 {
		t.Fatalf("pass 1: topic-B SubscriberCount=%d want 0", reg.SubscriberCount("topic-B"))
	}
	// Capture the registration pointer so the post-pass-2 check
	// can prove it's the SAME registration (no churn).
	regA1 := sess.activeSubs["topic-A"]
	if regA1 == nil {
		t.Fatalf("pass 1: activeSubs[topic-A] missing")
	}

	// Pass 2: subscribe to {A, B}.
	desired.Store([]string{"topic-A", "topic-B"})
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	if reg.SubscriberCount("topic-A") != 1 {
		t.Fatalf("pass 2: topic-A SubscriberCount=%d want 1 (must NOT have churned)", reg.SubscriberCount("topic-A"))
	}
	if reg.SubscriberCount("topic-B") != 1 {
		t.Fatalf("pass 2: topic-B SubscriberCount=%d want 1 (must be NEW)", reg.SubscriberCount("topic-B"))
	}
	regA2 := sess.activeSubs["topic-A"]
	if regA2 == nil {
		t.Fatalf("pass 2: activeSubs[topic-A] missing")
	}
	if regA1 != regA2 {
		t.Fatalf("pass 2: topic-A registration was REPLACED (spurious churn); want stable pointer")
	}

	// Publish to A after the second pass — must still reach the
	// subscriber (proves channel wasn't orphaned during the diff).
	sessPub := newPubsubSession()
	sessPub.sid = "sid-pub"
	app.runCmd(sessPub, cmdT{kind: "publish", topic: "topic-A", payload: "ping"})
	pumpSubBoundary()

	got := rec.snapshot()
	found := false
	for _, m := range got {
		if s, ok := m.(string); ok && s == "topic-A|ping" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("after diff-mode pass 2, topic-A no longer delivers; recorded=%v", got)
	}

	// Cleanup.
	sess.markDone()
}

// Test_SetupSubscriptions_DiffMode_RemovedDropsRegistration pins the
// other half of §4.1: removing a topic from the desired set MUST
// cancel its registration + drop the broker refcount.
func Test_SetupSubscriptions_DiffMode_RemovedDropsRegistration(t *testing.T) {
	rec := &recordedDispatch{}
	var desired atomic.Value
	desired.Store([]string{"topic-A", "topic-B"})

	makeSubs := func() any {
		topics := desired.Load().([]string)
		leaves := make([]any, 0, len(topics))
		for _, top := range topics {
			topCopy := top
			leaves = append(leaves, subT{
				kind:  "subscribeTopic",
				topic: topCopy,
				toMsg: func(p any) any { return p },
			})
		}
		if len(leaves) == 0 {
			return subT{kind: "none"}
		}
		if len(leaves) == 1 {
			return leaves[0]
		}
		return subT{kind: "batch", batch: leaves}
	}

	app := &liveApp{
		update: func(msg, model any) any {
			rec.record(msg)
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:          func(model any) any { return velement("div", nil, nil) },
		topics:        newTopicRegistry(16),
		subscriptions: func(model any) any { return makeSubs() },
	}
	sess := newPubsubSession()

	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	reg := app.topics.(*topicRegistry)
	if reg.TopicCount() != 2 {
		t.Fatalf("pass 1: TopicCount=%d want 2", reg.TopicCount())
	}

	// Pass 2: drop topic-B.
	desired.Store([]string{"topic-A"})
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	if reg.SubscriberCount("topic-A") != 1 {
		t.Fatalf("pass 2: topic-A=%d want 1", reg.SubscriberCount("topic-A"))
	}
	if reg.SubscriberCount("topic-B") != 0 {
		t.Fatalf("pass 2: topic-B=%d want 0 (must be removed)", reg.SubscriberCount("topic-B"))
	}
	if _, present := sess.activeSubs["topic-B"]; present {
		t.Fatalf("pass 2: activeSubs[topic-B] still present after diff removal")
	}

	// Pass 3: drop everything.
	desired.Store([]string{})
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	if reg.TopicCount() != 0 {
		t.Fatalf("pass 3: TopicCount=%d want 0", reg.TopicCount())
	}
	if len(sess.activeSubs) != 0 {
		t.Fatalf("pass 3: activeSubs not drained: %v", sess.activeSubs)
	}

	sess.markDone()
}

// ────────────────────────────────────────────────────────────────────
// Sub.batch composition — every + subscribeTopic together
// ────────────────────────────────────────────────────────────────────

// Test_SetupSubscriptions_BatchEveryAndSubscribeTopic_Coexist pins
// the documented multi-source shape — Sub.batch with one Sub.every
// AND one Sub.subscribeTopic — actually composes. The Time.every
// ticker goroutine spawns AND the broker subscription is active.
func Test_SetupSubscriptions_BatchEveryAndSubscribeTopic_Coexist(t *testing.T) {
	rec := &recordedDispatch{}
	batch := subT{kind: "batch", batch: []any{
		subT{kind: "every", ms: 5000, toMsg: "tick"}, // 5s — long enough we don't actually tick during this test
		subT{kind: "subscribeTopic", topic: "ch", toMsg: func(p any) any {
			return fmt.Sprintf("got-%v", p)
		}},
	}}
	app := pubsubTestApp(rec, batch)

	sess := newPubsubSession()
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	// Broker subscription is live.
	reg := app.topics.(*topicRegistry)
	if reg.SubscriberCount("ch") != 1 {
		t.Fatalf("Sub.batch composition: topic 'ch' SubscriberCount=%d want 1", reg.SubscriberCount("ch"))
	}

	// Publish reaches the subscriber.
	app.runCmd(sess, cmdT{kind: "publish", topic: "ch", payload: "hi"})
	pumpSubBoundary()

	got := rec.snapshot()
	found := false
	for _, m := range got {
		if s, ok := m.(string); ok && s == "got-hi" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("batch composition: broadcast didn't dispatch; recorded=%v", got)
	}

	sess.markDone()
}

// ────────────────────────────────────────────────────────────────────
// Cleanup — markDone end-to-end through setupSubscriptions
// ────────────────────────────────────────────────────────────────────

// Test_Cleanup_MarkDone_ReleasesAllSetupSubs runs the *full* wire:
// setupSubscriptions registers via the broker, markDone walks
// activeSubs + cancels — verified by broker.TopicCount() returning to 0.
func Test_Cleanup_MarkDone_ReleasesAllSetupSubs(t *testing.T) {
	rec := &recordedDispatch{}
	batch := subT{kind: "batch", batch: []any{
		subT{kind: "subscribeTopic", topic: "a", toMsg: func(p any) any { return p }},
		subT{kind: "subscribeTopic", topic: "b", toMsg: func(p any) any { return p }},
		subT{kind: "subscribeTopic", topic: "c", toMsg: func(p any) any { return p }},
	}}
	app := pubsubTestApp(rec, batch)
	sess := newPubsubSession()

	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)
	reg := app.topics.(*topicRegistry)
	if got := reg.TopicCount(); got != 3 {
		t.Fatalf("post-setup TopicCount=%d want 3", got)
	}
	if got := len(sess.activeSubs); got != 3 {
		t.Fatalf("post-setup activeSubs len=%d want 3", got)
	}

	sess.markDone()
	// Give the per-topic goroutines a moment to exit on sess.done.
	time.Sleep(30 * time.Millisecond)

	if got := reg.TopicCount(); got != 0 {
		t.Fatalf("post-markDone TopicCount=%d want 0 (broker leak)", got)
	}
	if sess.activeSubs != nil {
		t.Fatalf("post-markDone activeSubs not nilled: %v", sess.activeSubs)
	}
}

// Test_Cleanup_NoGoroutineLeak_AfterDiffSwap pins the goroutine
// hygiene contract across multiple diff-mode swaps. 10 rounds of
// "subscribe + immediately replace + immediately drop" must return
// the goroutine count to baseline.
func Test_Cleanup_NoGoroutineLeak_AfterDiffSwap(t *testing.T) {
	rec := &recordedDispatch{}
	var desired atomic.Value
	desired.Store([]string{})
	makeSubs := func() any {
		topics := desired.Load().([]string)
		leaves := make([]any, 0, len(topics))
		for _, top := range topics {
			topCopy := top
			leaves = append(leaves, subT{
				kind:  "subscribeTopic",
				topic: topCopy,
				toMsg: func(p any) any { return p },
			})
		}
		if len(leaves) == 0 {
			return subT{kind: "none"}
		}
		if len(leaves) == 1 {
			return leaves[0]
		}
		return subT{kind: "batch", batch: leaves}
	}
	app := &liveApp{
		update: func(msg, model any) any {
			rec.record(msg)
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:          func(model any) any { return velement("div", nil, nil) },
		topics:        newTopicRegistry(16),
		subscriptions: func(model any) any { return makeSubs() },
	}
	sess := newPubsubSession()

	runtime.GC()
	time.Sleep(30 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for round := 0; round < 10; round++ {
		// Subscribe to fresh per-round topics.
		desired.Store([]string{
			fmt.Sprintf("rd-%d-a", round),
			fmt.Sprintf("rd-%d-b", round),
		})
		app.setupSubscriptions(sess)
		time.Sleep(10 * time.Millisecond)
		// Drop them all.
		desired.Store([]string{})
		app.setupSubscriptions(sess)
		time.Sleep(10 * time.Millisecond)
	}
	sess.markDone()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	current := runtime.NumGoroutine()
	// Allow a couple of slack goroutines for scheduling jitter.
	if current > baseline+2 {
		t.Fatalf("goroutine leak after 10 diff-swap rounds: baseline=%d current=%d", baseline, current)
	}

	reg := app.topics.(*topicRegistry)
	if got := reg.TopicCount(); got != 0 {
		t.Fatalf("after diff-swap rounds TopicCount=%d want 0", got)
	}
}

// ────────────────────────────────────────────────────────────────────
// Decoder panic safety
// ────────────────────────────────────────────────────────────────────

// Test_SubscriberDispatch_DecoderPanic_Recovered pins design doc
// §4.3: a decoder that panics must NOT crash the subscriber goroutine
// or the session. The event is logged + swallowed; subsequent events
// continue to dispatch normally.
func Test_SubscriberDispatch_DecoderPanic_Recovered(t *testing.T) {
	rec := &recordedDispatch{}
	// First event triggers panic; second event passes through.
	var calls atomic.Int32
	decoder := func(payload any) any {
		n := calls.Add(1)
		if n == 1 {
			panic(fmt.Sprintf("synthetic decoder panic on payload %v", payload))
		}
		return fmt.Sprintf("got-%v", payload)
	}
	subs := subT{kind: "subscribeTopic", topic: "fragile", toMsg: decoder}
	app := pubsubTestApp(rec, subs)

	sess := newPubsubSession()
	app.setupSubscriptions(sess)
	time.Sleep(20 * time.Millisecond)

	// First publish: decoder panics; recorded msgs MUST NOT grow
	// (no Msg lands because the decoder produced none).
	app.runCmd(sess, cmdT{kind: "publish", topic: "fragile", payload: "bad"})
	pumpSubBoundary()
	if len(rec.snapshot()) != 0 {
		t.Fatalf("after panic-decode, recorded=%v want []", rec.snapshot())
	}

	// Second publish: decoder succeeds; broadcast dispatched.
	app.runCmd(sess, cmdT{kind: "publish", topic: "fragile", payload: "ok"})
	pumpSubBoundary()
	got := rec.snapshot()
	found := false
	for _, m := range got {
		if s, ok := m.(string); ok && s == "got-ok" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("after recovered-panic, subscriber didn't resume; recorded=%v", got)
	}

	sess.markDone()
}

// ────────────────────────────────────────────────────────────────────
// flattenSubs unit test
// ────────────────────────────────────────────────────────────────────

// Test_FlattenSubs_BareLeaf is the simplest case — non-batch Sub
// returned verbatim as one leaf.
func Test_FlattenSubs_BareLeaf(t *testing.T) {
	leaf := subT{kind: "every", ms: 100, toMsg: "tick"}
	out := flattenSubs(leaf, nil)
	if len(out) != 1 || out[0].kind != "every" {
		t.Fatalf("bare leaf: got %+v", out)
	}
}

// Test_FlattenSubs_BatchOfTwo flattens a batch of two leaves.
func Test_FlattenSubs_BatchOfTwo(t *testing.T) {
	batch := subT{kind: "batch", batch: []any{
		subT{kind: "every", ms: 100, toMsg: "tick"},
		subT{kind: "subscribeTopic", topic: "ch", toMsg: "decode"},
	}}
	out := flattenSubs(batch, nil)
	if len(out) != 2 {
		t.Fatalf("batch of 2: got %d leaves: %+v", len(out), out)
	}
}

// Test_FlattenSubs_NestedBatches walks a Sub.batch that itself
// contains a Sub.batch — recursion path.
func Test_FlattenSubs_NestedBatches(t *testing.T) {
	inner := subT{kind: "batch", batch: []any{
		subT{kind: "subscribeTopic", topic: "x", toMsg: "decode"},
		subT{kind: "subscribeTopic", topic: "y", toMsg: "decode"},
	}}
	outer := subT{kind: "batch", batch: []any{
		subT{kind: "every", ms: 100, toMsg: "tick"},
		inner,
		subT{kind: "subscribeTopic", topic: "z", toMsg: "decode"},
	}}
	out := flattenSubs(outer, nil)
	// Expect 4 leaves: every, x, y, z.
	if len(out) != 4 {
		t.Fatalf("nested batches: got %d leaves: %+v", len(out), out)
	}
	kinds := map[string]int{}
	for _, l := range out {
		kinds[l.kind]++
	}
	if kinds["every"] != 1 || kinds["subscribeTopic"] != 3 {
		t.Fatalf("nested batches leaf kinds: %+v", kinds)
	}
}

// Test_FlattenSubs_NonSubReturned skips a non-subT value gracefully
// (e.g. a `subscriptions = \model -> some Msg` mistake should not
// panic; just yields zero leaves).
func Test_FlattenSubs_NonSubReturned(t *testing.T) {
	out := flattenSubs("not-a-sub", nil)
	if len(out) != 0 {
		t.Fatalf("non-sub input: got %d leaves", len(out))
	}
}
