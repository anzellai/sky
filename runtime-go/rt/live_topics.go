// live_topics.go — Sky.Live pub/sub registry (Cycle 3 P46).
//
// In-process, ref-counted topic registry. Subscribers register a unique
// listener per Subscribe call; Publish fans out to every subscriber of
// the topic. When the last subscriber of a topic cancels, the topic
// entry is dropped from the registry — the load-bearing contract that
// keeps the registry bounded under churn.
//
// See docs/skylive/pubsub-design.md §3.1, §3.5 for the full
// architecture. P46 ships the in-process default Broker; v0.16+
// cross-process tiers (Redis Pub/Sub, Cloud Pub/Sub, NATS, Postgres
// LISTEN/NOTIFY — see §11.2.5) plug in by implementing the SAME
// Broker interface and assigning to the store's broker field. No
// call-site change required at the v0.15.x → v0.16 transition.
//
// Concurrency contract:
//
//   - topicRegistry.mu guards `entries` and every `topicEntry.subs`
//     map. Subscribe / Publish / Cancel all acquire it. Per-topic
//     sharding is a future optimisation (design doc §11.1 risk row).
//
//   - Publish iterates the subscriber list with the lock held; the
//     per-subscriber channel push is non-blocking via the `default:`
//     arm, so a slow subscriber can't stall the publisher.
//
//   - The cancel func returned by Subscribe is idempotent (uses
//     sync.Once) so a session's setupSubscriptions diff + a
//     markDone teardown can both fire without double-decrementing
//     the refcount.
//
//   - Channel close: we DO NOT close the per-subscriber channel on
//     cancel. The owning goroutine (in P48) will detect cancellation
//     via its own select on a separate done channel and exit; the
//     subscriber channel becomes garbage-collected once no goroutine
//     holds a reference. Avoids the "publish to closed channel"
//     panic class entirely.

package rt

import (
	"sync"
	"sync/atomic"
)

// ═════════════════════════════════════════════════════════════════════
// SessionEvent — the broadcast envelope on the wire between Publish
// and the subscriber goroutine.
// ═════════════════════════════════════════════════════════════════════

// SessionEvent carries one pub/sub broadcast through the registry.
//
//   - Topic is the wire channel id (exact-match string; pattern subs
//     are out of scope per design doc §1.2 non-goal 4).
//   - Payload is the Sky-side `any` value the receiver session's
//     decoder is called with (P48 invokes the decoder; P46 just
//     transports the value).
//   - GlobalSeq is the app-wide monotonic counter (P47 will populate
//     this from app.globalSeq; P46 lets it ride at 0).
//   - Origin is the publisher sid; subscribers MAY self-skip on
//     match to implement echo suppression at the app layer (echo-by-
//     default is the locked behaviour per design doc Q2).
//   - SkipOrigin (Cycle 4 NE — issue #359) requests broker-level
//     self-suppression: when true, the registry skips delivery to
//     any subscriber whose ownSid matches Origin. Set by
//     `Cmd.publishNoEcho` / `PubSub.publishNoEcho`. Defaults to
//     false → echo-by-default semantics preserved for existing
//     `Cmd.publish` / `PubSub.publish` callers.
type SessionEvent struct {
	Topic      string
	Payload    any
	GlobalSeq  int64
	Origin     string
	SkipOrigin bool
}

// ═════════════════════════════════════════════════════════════════════
// Broker interface — the future-proofing seam for v0.16+ cross-process
// brokers (Redis Pub/Sub, Cloud Pub/Sub, NATS JetStream, Postgres
// LISTEN/NOTIFY). Every SessionStore impl in v0.15.x carries a Broker
// pointer initialised to the default in-process *topicRegistry; a
// future RedisBroker / GcpPubsubBroker just swaps the pointer.
// ═════════════════════════════════════════════════════════════════════

// Broker is the abstract pub/sub channel manager. The default
// in-process implementation is *topicRegistry; cross-process tiers
// implement the same interface differently.
//
// The interface is deliberately minimal:
//
//   - Subscribe returns a receive-only channel AND a cancel func. The
//     caller MUST call cancel when done; the registry uses cancel
//     refcount transitions to drop the topic entry once it reaches 0.
//
//   - SubscribeWithOwner (Cycle 4 NE, issue #359) is Subscribe + an
//     ownerSid argument the broker tracks alongside the channel. When
//     a SkipOrigin publish arrives, the broker compares each
//     subscriber's ownerSid to event.Origin and suppresses self-
//     delivery. Legacy callers may keep using Subscribe (records ""
//     as ownerSid; never matches a non-empty Origin).
//
//   - Publish returns the number of subscribers the event reached
//     (useful for tracing / metrics + observability surfaces; the
//     publisher's own subscription IS counted unless event.SkipOrigin
//     suppresses it).
//
//   - Close releases any backend resources (no-op for the in-process
//     impl; Redis / Cloud Pub/Sub close their network connections).
type Broker interface {
	Subscribe(topic string) (<-chan SessionEvent, func())
	SubscribeWithOwner(topic, ownerSid string) (<-chan SessionEvent, func())
	Publish(topic string, event SessionEvent) int
	Close() error
}

// ═════════════════════════════════════════════════════════════════════
// topicRegistry — default in-process Broker.
// ═════════════════════════════════════════════════════════════════════

// topicRegistry is the in-process default Broker. All five v0.15.x
// SessionStore backends share ONE topicRegistry per liveApp — the
// pub/sub registry is app-scoped, NOT store-scoped, because the
// store's job is session-state persistence and pub/sub is app-level
// fan-out (design doc §3.1.1).
//
// Why this lives in a separate file from live_store.go: the broker
// is conceptually app-level even though it's wired through the store
// interface for future cross-process backends. Keeping it on its own
// file makes the Broker / SessionEvent / topicRegistry surface
// browsable as a unit.
type topicRegistry struct {
	mu      sync.Mutex
	entries map[string]*topicEntry
	// subBuf — buffer capacity for each subscriber channel. Read
	// from SKY_LIVE_SSE_BUFFER at construction time so the broker
	// inherits the same buffer knob the SSE channel uses (sensible
	// default; a future env knob can split them if a workload
	// needs different bounds for broadcast vs SSE).
	subBuf int
}

type topicEntry struct {
	// subs holds every active subscriber's channel keyed by a
	// per-subscribe unique id. Refcount = len(subs); when it reaches
	// 0 the entry is removed from registry.entries.
	subs map[uint64]topicSub
}

// topicSub carries a single subscriber's delivery channel plus the
// owning session sid (used by SkipOrigin self-suppression in
// Cmd.publishNoEcho / PubSub.publishNoEcho — issue #359). ownerSid is
// "" for legacy callers that registered via Subscribe (no-owner) —
// such subscribers are never self-skipped because publishers never
// dispatch with an empty Origin in the normal Live.app path.
type topicSub struct {
	ch       chan SessionEvent
	ownerSid string
}

// subIDCounter — monotonic source of unique subscriber ids. atomic so
// concurrent Subscribe calls across different topics don't need to
// hold the registry mutex for ID generation.
var subIDCounter atomic.Uint64

// newTopicRegistry constructs an empty in-process broker. subBuf is the
// per-subscriber channel capacity; a sensible default is the same as
// sseChanBuffer so the broker inherits the SSE-buffer env knob shape.
// A subBuf of 0 falls back to sseChanBuffer.
func newTopicRegistry(subBuf int) *topicRegistry {
	if subBuf <= 0 {
		subBuf = sseChanBuffer
	}
	return &topicRegistry{
		entries: map[string]*topicEntry{},
		subBuf:  subBuf,
	}
}

// Subscribe registers a fan-out listener for `topic` and returns:
//
//  1. The receive-only channel on which broadcast events arrive
//     (buffered; capacity = registry.subBuf). Oversend drops via
//     `default:` at the publisher, mirroring sess.sseCh semantics.
//
//  2. A cancel func the caller MUST call when the subscription is no
//     longer needed (typically: on setupSubscriptions re-eval that
//     drops the topic, OR on session.Delete via markDone). cancel is
//     idempotent (sync.Once) so double-call from concurrent teardown
//     paths is safe.
//
// When the cancel drops the refcount to zero, the topic entry is
// removed from the registry — the contract that keeps the registry
// bounded under churn (design doc §3.5).
//
// Subscribe registers with an empty ownerSid (legacy callers); to
// participate in SkipOrigin self-suppression — `Cmd.publishNoEcho` /
// `PubSub.publishNoEcho` (#359) — register via SubscribeWithOwner
// instead.
func (r *topicRegistry) Subscribe(topic string) (<-chan SessionEvent, func()) {
	return r.SubscribeWithOwner(topic, "")
}

// SubscribeWithOwner is Subscribe + an `ownerSid` that the registry
// records alongside the channel. When a SkipOrigin publish arrives,
// the registry compares each subscriber's ownerSid to event.Origin
// and skips delivery to matches. Legacy Subscribe is a thin wrapper
// over this with ownerSid="" — empty ownerSid never matches a
// non-empty Origin so legacy subscribers are not affected.
//
// Wired into Sky.Live's setupSubscriptions in P48 follow-up: the
// session's sid IS the owner sid for every topic the session
// subscribes to. The dispatch path passes session.sid as Origin on
// every publish, so a session's own SkipOrigin publishes never echo
// back to itself.
func (r *topicRegistry) SubscribeWithOwner(topic, ownerSid string) (<-chan SessionEvent, func()) {
	ch := make(chan SessionEvent, r.subBuf)
	id := subIDCounter.Add(1)

	r.mu.Lock()
	entry, ok := r.entries[topic]
	if !ok {
		entry = &topicEntry{subs: map[uint64]topicSub{}}
		r.entries[topic] = entry
	}
	entry.subs[id] = topicSub{ch: ch, ownerSid: ownerSid}
	r.mu.Unlock()

	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			e, ok := r.entries[topic]
			if !ok {
				return
			}
			delete(e.subs, id)
			if len(e.subs) == 0 {
				delete(r.entries, topic)
			}
		})
	}
	return ch, cancel
}

// Publish fans `event` out to every subscriber of `event.Topic`.
// Returns the count of subscribers that received the event (a slow
// subscriber whose channel was full is NOT counted — drops are
// surfaced via sky_live_sse_drops_total{kind="broadcast"} once P48
// wires the metric).
//
// Best-effort delivery: a slow subscriber drops via the `default:`
// arm. The publisher does not block; the next user dispatch (or
// next broadcast) supersedes the missed event (per design doc §6.1).
//
// Echo behaviour:
//
//   - event.SkipOrigin == false (the default — `Cmd.publish` /
//     `PubSub.publish`): every subscriber receives the event,
//     including a subscriber whose ownerSid matches event.Origin.
//     This is the echo-by-default behaviour from design doc Q2 and
//     matches Redis / NATS / MQTT semantics.
//
//   - event.SkipOrigin == true (`Cmd.publishNoEcho` /
//     `PubSub.publishNoEcho`, issue #359): every subscriber whose
//     ownerSid matches event.Origin is skipped. Other subscribers
//     receive normally. Pattern: publisher updates own model in
//     `update`, calls publishNoEcho for the foreign fan-out — saves
//     the broker round-trip + (in v0.16+ cross-process tiers) the
//     network hop back.
func (r *topicRegistry) Publish(topic string, event SessionEvent) int {
	event.Topic = topic // canonicalise — caller might've left it blank
	r.mu.Lock()
	entry, ok := r.entries[topic]
	if !ok {
		r.mu.Unlock()
		return 0
	}
	// Snapshot the subscriber set under the lock, then release
	// before the non-blocking pushes — keeps the critical section
	// short so unrelated Subscribe / Cancel calls aren't stalled by
	// a fan-out over many subscribers. We snapshot topicSub (not
	// just the channel) so the SkipOrigin filter can compare
	// ownerSid outside the lock.
	subs := make([]topicSub, 0, len(entry.subs))
	for _, sub := range entry.subs {
		subs = append(subs, sub)
	}
	r.mu.Unlock()

	delivered := 0
	for _, sub := range subs {
		// SkipOrigin self-suppression — issue #359. The "" guard on
		// ownerSid is load-bearing for the legacy Subscribe path
		// (registered with empty ownerSid); without it, an empty
		// Origin would silently skip every legacy subscriber. In
		// practice publishers either carry sess.sid (Cmd.* dispatch
		// arm) or "" (server-side PubSub.* path), and legacy
		// subscribers register with "" — so the only intentional
		// match is sess.sid → sess.sid.
		if event.SkipOrigin && sub.ownerSid != "" && sub.ownerSid == event.Origin {
			continue
		}
		select {
		case sub.ch <- event:
			delivered++
		default:
			// Slow / wedged subscriber — drop the event for THIS
			// subscriber. Counter wiring is P48's job (the registry
			// has no sid context to label by; the subscribing
			// goroutine, which DOES have sid, will be the one to
			// increment).
		}
	}
	return delivered
}

// Close releases broker-level resources. No-op for the in-process
// default; Redis / Cloud Pub/Sub / NATS implementations close their
// network connections + cancel their long-lived subscriptions here.
func (r *topicRegistry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Reset the registry to its zero state so a Close immediately
	// followed by a re-Subscribe sees an empty map (test fixtures
	// reuse brokers across sub-tests).
	r.entries = map[string]*topicEntry{}
	return nil
}

// ═════════════════════════════════════════════════════════════════════
// Introspection helpers (used by tests + future telemetry surface)
// ═════════════════════════════════════════════════════════════════════

// TopicCount returns the number of distinct topics with at least one
// active subscriber. Load-bearing for the memory-bound regression
// test (design doc §3.1 prereq 5): assert this returns to zero after
// all subscriptions are cancelled.
func (r *topicRegistry) TopicCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// SubscriberCount returns the number of active subscribers for
// `topic`. 0 means the topic has no entry in the registry (which is
// the post-cancel terminal state — the registry doesn't keep
// zero-refcount entries around).
func (r *topicRegistry) SubscriberCount(topic string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[topic]
	if !ok {
		return 0
	}
	return len(e.subs)
}

// ═════════════════════════════════════════════════════════════════════
// Diff-mode subscription update (helper for P48)
// ═════════════════════════════════════════════════════════════════════

// subRegistration is a single live subscription on a session. P46
// defines the shape; P48 wires setupSubscriptions to produce + walk
// these. The fields:
//
//   - topic — the registry topic key.
//   - ch    — the broker-supplied event channel (receive-only).
//   - cancel — the broker-supplied teardown func; idempotent.
//   - toMsg — the user's `any -> msg` decoder (called by the
//             subscriber goroutine in P48). P46 stores it but does
//             not invoke it.
type subRegistration struct {
	topic  string
	ch     <-chan SessionEvent
	cancel func()
	toMsg  any
}

// diffSubscriptions computes the (added, removed) topic sets between
// the CURRENT live registrations (`old`) and the DESIRED set
// (`desired`). Returns the topics that need new subscriptions opened
// AND the topics whose existing subscriptions should be cancelled.
//
// Diff-mode (vs blow-up-and-rebuild) avoids two costs the design doc
// §4.1 calls out:
//
//   1. The race-window between cancel + re-subscribe where a
//      broadcast fires + is silently lost.
//   2. Registry-mutex contention proportional to dispatch rate ×
//      number of subscribed topics.
//
// Pure function — no I/O, no registry mutation. Callers apply the
// returned actions to the registry. Order of operations matters: P48
// will cancel `removed` first (releases refcount), then Subscribe
// `added` (acquires refcount). Topics in BOTH old and desired keep
// their existing channel + goroutine.
//
// Returned slices are deterministic per (old, desired) pair —
// iteration order over maps is randomised in Go but the diff is
// computed via set-membership tests so the OUTPUT topic ORDER is
// itself non-deterministic. Tests that compare equality MUST treat
// the slices as sets (build a map[string]bool and compare).
func diffSubscriptions(old map[string]*subRegistration, desired map[string]any) (added []string, removed []string) {
	for topic := range desired {
		if _, ok := old[topic]; !ok {
			added = append(added, topic)
		}
	}
	for topic := range old {
		if _, ok := desired[topic]; !ok {
			removed = append(removed, topic)
		}
	}
	return added, removed
}
