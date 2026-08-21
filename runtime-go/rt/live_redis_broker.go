//go:build !js

// live_redis_broker.go — cross-instance pub/sub Broker (Phase 2).
//
// The in-process *topicRegistry (live_topics.go) fans a broadcast out to
// every subscriber WITHIN one process. When a Sky.Live app runs on N
// instances behind a load balancer, a `Cmd.publish` on instance A must
// also reach subscribers on instances B..N — otherwise a chat message,
// collab edit, or same-user cross-device update is invisible to everyone
// not on the publisher's instance.
//
// redisBroker implements the SAME Broker interface as *topicRegistry, so
// nothing at the call sites (app.Publish, setupSubscriptions,
// runSubscriberLoop) changes. It composes the in-process registry for
// LOCAL delivery and adds a Redis Pub/Sub layer for the cross-instance
// hop:
//
//	Publish(topic, ev):
//	  1. re-stamp ev.GlobalSeq from THIS broker's monotonic counter
//	  2. deliver locally via the wrapped topicRegistry
//	  3. best-effort PUBLISH the (gob-encoded) event to Redis channel
//	     "sky:live:topic:<topic>", tagged with this instance's id
//
//	receive loop (one per instance):
//	  - reads every subscribed Redis channel via one PubSub connection
//	  - DROPS messages tagged with our own instance id (already
//	    delivered locally in step 2 — no double delivery)
//	  - re-stamps ev.GlobalSeq from THIS broker's counter and delivers
//	    locally
//
// ── Why re-stamp globalSeq per instance (not a shared Redis counter) ──
// The browser client dedupes broadcast frames with a monotonic
// watermark: it drops any frame whose globalSeq ≤ the last it applied
// (guards against an SSE reconnect re-delivering a buffered frame). That
// watermark only has to be monotonic PER SUBSCRIBER STREAM — i.e. per
// instance. If instance A and instance B each stamped from their own
// app.globalSeq and shipped that value cross-instance, the two counters
// would collide and a subscriber would wrongly drop valid events. By
// re-stamping every LOCALLY-DELIVERED event (local-origin AND
// remote-origin) from ONE per-instance counter, each subscriber's stream
// stays strictly monotonic and the watermark is correct — with no
// cross-instance seq coordination, no Redis round-trip on the counter,
// and no global sequencer to become a bottleneck. Ordering stays
// best-effort exactly as the in-process broker already is (design doc
// §6.1: the next publish supersedes a rarely-reordered one).
//
// ── Payload serialization ──
// SessionEvent.Payload is an arbitrary Sky value (`any`). It crosses the
// wire via the SAME gob machinery the DB session stores use for the
// Model (gobRegisterAll + the eager GobRegisterTypeGraph(model) at app
// startup, live.go). So any payload whose type appears in the session
// Model — i.e. every realistic pub/sub payload — round-trips on every
// instance. A payload that fails to gob-encode/decode degrades to
// LOCAL-only delivery with a logged-once warning, the same trade-off the
// stores already document for non-encodable Model values. No panic, no
// silent corruption.
//
// ── Failure handling (graceful degradation) ──
// A Redis PUBLISH / SUBSCRIBE error never breaks local pub/sub: the
// local topicRegistry delivery already happened (or still happens), and
// the Redis hop is logged-once and skipped. When Redis is the session
// store too (the usual case), a Redis outage takes the whole deployment
// down regardless — so "Redis down → cross-instance fan-out pauses" is
// consistent with the rest of the tier, not a new failure mode.

package rt

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// topicChanPrefix namespaces Sky.Live pub/sub channels so a shared Redis
// can host other workloads without collision. The topic string is
// appended verbatim (topics are app-controlled, exact-match ids).
const topicChanPrefix = "sky:live:topic:"

func topicChan(topic string) string { return topicChanPrefix + topic }

// redisPubMsg is the wire envelope on a Redis pub/sub channel. It is a
// concrete struct, so gob encodes it without type registration; only the
// inner Payload (an arbitrary Sky `any`) needs the gobRegisterAll dance.
type redisPubMsg struct {
	InstanceID string // publisher instance — receivers drop their own echo
	Origin     string // publisher session sid (echo-suppression target)
	SkipOrigin bool   // Cmd.publishNoEcho — broker-level self-suppression
	Payload    []byte // gob-encoded Sky value
}

// redisBroker is the cross-instance Broker. It embeds an in-process
// topicRegistry (`local`) for subscriber-channel management + local
// fan-out, and layers a Redis Pub/Sub connection for the cross-instance
// hop. One redisBroker per liveApp per process.
type redisBroker struct {
	local  *topicRegistry
	client *redis.Client
	pubsub *redis.PubSub
	ctx    context.Context
	cancel context.CancelFunc

	// instanceID uniquely identifies this process's broker. Redis
	// delivers a PUBLISH back to the publishing connection too, so the
	// receive loop uses this to drop our own echo (already delivered
	// locally in Publish).
	instanceID string

	// seq is this instance's monotonic globalSeq source. EVERY event
	// delivered to a local subscriber — local-origin or remote-origin —
	// is stamped from here, so each subscriber stream is monotonic. See
	// the file header for why this is per-instance, not shared.
	seq atomic.Int64

	// ownsClient: true when this broker created the *redis.Client (the
	// decoupled SKY_LIVE_BROKER_URL path) and must Close it; false when
	// it shares the session store's client (store=redis path) and must
	// NOT Close it out from under the store.
	ownsClient bool

	// redisSubMu serialises (refcount change + Redis SUBSCRIBE/UNSUBSCRIBE)
	// so a concurrent first-subscribe and last-unsubscribe of the same
	// topic can't reorder into a dangling/absent Redis subscription. The
	// control-channel op is infrequent (only on 0↔1 local-subscriber
	// transitions per topic), so holding the lock across it is cheap.
	redisSubMu sync.Mutex
	subCount   map[string]int

	closed atomic.Bool

	// encode/decode are fields (not direct calls) so tests can inject a
	// codec; production wires the gob payload codec.
	encode func(any) ([]byte, error)
	decode func([]byte) (any, error)
}

// newRedisBroker builds a cross-instance broker over `client`. When
// ownsClient is true the broker Closes the client on Close (decoupled
// path); when false it leaves the client alone (shared with the store).
func newRedisBroker(client *redis.Client, ownsClient bool) *redisBroker {
	ctx, cancel := context.WithCancel(context.Background())
	b := &redisBroker{
		local:      newTopicRegistry(0),
		client:     client,
		ctx:        ctx,
		cancel:     cancel,
		instanceID: generateSkySessionID(),
		ownsClient: ownsClient,
		subCount:   map[string]int{},
		encode:     encodePubSubPayload,
		decode:     decodePubSubPayload,
	}
	// One dedicated Pub/Sub connection; channels are added/removed
	// dynamically as local subscribers come and go. Channel() starts the
	// single reader goroutine the receive loop drains.
	b.pubsub = client.Subscribe(ctx)
	go b.receiveLoop()
	return b
}

// Subscribe registers a local listener for `topic` and ensures this
// instance is subscribed to the topic's Redis channel so cross-instance
// publishes arrive. Mirrors topicRegistry.Subscribe (empty ownerSid).
func (b *redisBroker) Subscribe(topic string) (<-chan SessionEvent, func()) {
	return b.SubscribeWithOwner(topic, "")
}

// SubscribeWithOwner is Subscribe + the owning session sid (for
// Cmd.publishNoEcho self-suppression). The returned cancel is idempotent
// and both drops the local subscription AND releases the Redis-channel
// refcount (unsubscribing the channel when the last local listener goes).
func (b *redisBroker) SubscribeWithOwner(topic, ownerSid string) (<-chan SessionEvent, func()) {
	ch, localCancel := b.local.SubscribeWithOwner(topic, ownerSid)
	b.redisSubscribe(topic)
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			localCancel()
			b.redisUnsubscribe(topic)
		})
	}
	return ch, cancel
}

// Publish re-stamps the event's globalSeq from this instance's counter,
// delivers it to local subscribers, and best-effort PUBLISHes it to the
// topic's Redis channel for other instances. Returns the LOCAL delivered
// count (the cross-instance count is unknowable synchronously and only
// used for tracing).
func (b *redisBroker) Publish(topic string, event SessionEvent) int {
	event.Topic = topic
	event.GlobalSeq = b.seq.Add(1)
	delivered := b.local.Publish(topic, event)

	if b.closed.Load() {
		return delivered
	}
	// Cross-instance hop — best-effort. Any failure degrades to
	// local-only with a logged-once warning; it never affects the local
	// delivery above.
	payloadBytes, err := b.encode(event.Payload)
	if err != nil {
		logOnce("pubsub-encode-"+topic, func() {
			log.Printf("[sky.live] pub/sub: payload for topic %q not gob-encodable (%v); delivering LOCAL-only", topic, err)
		})
		return delivered
	}
	envBytes, err := encodeEnvelope(redisPubMsg{
		InstanceID: b.instanceID,
		Origin:     event.Origin,
		SkipOrigin: event.SkipOrigin,
		Payload:    payloadBytes,
	})
	if err != nil {
		logOnce("pubsub-envelope-"+topic, func() {
			log.Printf("[sky.live] pub/sub: envelope encode failed for topic %q (%v); LOCAL-only", topic, err)
		})
		return delivered
	}
	if err := b.client.Publish(b.ctx, topicChan(topic), envBytes).Err(); err != nil {
		logOnce("pubsub-redis-publish", func() {
			log.Printf("[sky.live] pub/sub: Redis PUBLISH failed (%v); cross-instance fan-out paused, LOCAL delivery unaffected", err)
		})
	}
	return delivered
}

// Close tears the broker down: cancels the context, closes the Pub/Sub
// connection (which ends the receive loop), and releases the local
// registry. The shared session-store client is closed ONLY when this
// broker owns it.
func (b *redisBroker) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.cancel()
	err := b.pubsub.Close()
	_ = b.local.Close()
	if b.ownsClient {
		_ = b.client.Close()
	}
	return err
}

// receiveLoop drains the single Pub/Sub connection and fans remote
// events into the local registry. Exits when pubsub.Close() closes the
// channel (Close / process teardown).
func (b *redisBroker) receiveLoop() {
	for msg := range b.pubsub.Channel() {
		env, err := decodeEnvelope([]byte(msg.Payload))
		if err != nil {
			logOnce("pubsub-envelope-decode", func() {
				log.Printf("[sky.live] pub/sub: dropping malformed cross-instance message (%v)", err)
			})
			continue
		}
		// Our own PUBLISH echoes back on the subscribed connection —
		// skip it (already delivered locally in Publish). This is what
		// prevents double delivery to the publisher's own subscribers.
		if env.InstanceID == b.instanceID {
			continue
		}
		payload, err := b.decode(env.Payload)
		if err != nil {
			logOnce("pubsub-payload-decode", func() {
				log.Printf("[sky.live] pub/sub: dropping cross-instance event with undecodable payload (%v) — is the type in the session Model / registered for gob?", err)
			})
			continue
		}
		topic := strings.TrimPrefix(msg.Channel, topicChanPrefix)
		b.local.Publish(topic, SessionEvent{
			Topic:      topic,
			Payload:    payload,
			Origin:     env.Origin,
			SkipOrigin: env.SkipOrigin,
			GlobalSeq:  b.seq.Add(1), // per-instance re-stamp — see header
		})
	}
}

// redisSubscribe increments the local-subscriber refcount for `topic`
// and, on the 0→1 transition, subscribes the Redis channel so remote
// publishes for this topic start arriving.
func (b *redisBroker) redisSubscribe(topic string) {
	b.redisSubMu.Lock()
	defer b.redisSubMu.Unlock()
	b.subCount[topic]++
	if b.subCount[topic] == 1 {
		if err := b.pubsub.Subscribe(b.ctx, topicChan(topic)); err != nil {
			logOnce("pubsub-redis-subscribe", func() {
				log.Printf("[sky.live] pub/sub: Redis SUBSCRIBE %q failed (%v); this instance will miss cross-instance events for the topic (local delivery unaffected)", topic, err)
			})
		}
	}
}

// redisUnsubscribe decrements the refcount and, on the 1→0 transition,
// unsubscribes the Redis channel so an idle instance stops receiving a
// topic's traffic — the bound that keeps per-instance Redis fan-in
// proportional to topics actually subscribed, not all app topics.
func (b *redisBroker) redisUnsubscribe(topic string) {
	b.redisSubMu.Lock()
	defer b.redisSubMu.Unlock()
	if b.subCount[topic] == 0 {
		return
	}
	b.subCount[topic]--
	if b.subCount[topic] == 0 {
		delete(b.subCount, topic)
		if err := b.pubsub.Unsubscribe(b.ctx, topicChan(topic)); err != nil {
			logOnce("pubsub-redis-unsubscribe", func() {
				log.Printf("[sky.live] pub/sub: Redis UNSUBSCRIBE %q failed (%v)", topic, err)
			})
		}
	}
}

// TopicCount / SubscriberCount delegate to the local registry so the
// existing introspection (used by the memory-bound regression tests)
// works against the redisBroker too.
func (b *redisBroker) TopicCount() int                  { return b.local.TopicCount() }
func (b *redisBroker) SubscriberCount(topic string) int { return b.local.SubscriberCount(topic) }

// ── Payload codec ────────────────────────────────────────────────────
// Reuses the store's gob machinery. Encoding registers the concrete
// type (idempotent); decoding relies on the eager GobRegisterTypeGraph
// at app startup + the publisher-side registration so the type is known
// on every instance for Model-shaped payloads.

// gobRegisterCommonWireTypes eagerly registers the typed-container shapes
// that Sky's Dict/List compile to, so a receiving instance can DECODE
// them from startup — before it has itself ever published one. The
// store's value-walkers (gobRegisterAll / GobRegisterTypeGraph) only
// register NAMED struct types; unnamed composites like map[string]string
// (== `Dict String String`) fall through, and a pub/sub payload's type is
// `any`-erased in the Msg so the Model type graph never surfaces it.
// Registering the primitive-valued Dict/List shapes here closes that gap
// for the overwhelmingly common cases; anything exotic is still covered
// on the publish side by registerWirePayloadTypes and degrades gracefully
// (logged, local-only) if a decoder somewhere lacks it.
func init() {
	gob.Register(map[string]string{})
	gob.Register(map[string]int{})
	gob.Register(map[string]bool{})
	gob.Register(map[string]float64{})
	gob.Register([]string{})
	gob.Register([]int{})
	gob.Register([]bool{})
	gob.Register([]float64{})
}

func encodePubSubPayload(v any) ([]byte, error) {
	// gobRegisterAll covers named struct + Sky-wrapper types (the store's
	// proven walker); registerWirePayloadTypes additionally registers
	// UNNAMED composite types at interface boundaries (map[string]string,
	// []T, nested), which gob needs to encode a value behind an interface.
	gobRegisterAll(v)
	registerWirePayloadTypes(v)
	var buf bytes.Buffer
	// Encode a POINTER to the interface so gob records the dynamic type.
	if err := gob.NewEncoder(&buf).Encode(&v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// registerWirePayloadTypes walks a value and gob-registers every concrete
// type sitting at an interface boundary — the top-level value, `any`
// map-values, `any` slice-elements, `any` struct fields — INCLUDING
// unnamed composites like map[string]string that the store's walkGob
// skips (it registers only PkgPath'd structs). Depth-bounded + type-set
// guarded like walkGobSeen so opaque FFI handles with pointer cycles
// can't overflow the stack.
func registerWirePayloadTypes(v any) {
	gobRegMu.Lock()
	defer gobRegMu.Unlock()
	registerWireVal(reflect.ValueOf(v), make(map[reflect.Type]bool, 16), 0)
}

func registerWireVal(rv reflect.Value, seen map[reflect.Type]bool, depth int) {
	if !rv.IsValid() || depth > 64 {
		return
	}
	t := rv.Type()
	if shouldGobRegisterWire(t) && !gobRegistered[t] {
		gobRegistered[t] = true
		func() {
			defer func() { _ = recover() }()
			gob.Register(reflect.Zero(t).Interface())
		}()
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !rv.IsNil() {
			registerWireVal(rv.Elem(), seen, depth+1)
		}
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < rv.NumField(); i++ {
			registerWireVal(rv.Field(i), seen, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			registerWireVal(rv.Index(i), seen, depth+1)
		}
	case reflect.Map:
		it := rv.MapRange()
		for it.Next() {
			registerWireVal(it.Value(), seen, depth+1)
		}
	}
}

// shouldGobRegisterWire reports whether t needs gob registration to cross
// an interface boundary. Composite types (map/slice/array/struct/ptr)
// always do; named primitives (`type Kind int`) do; unnamed primitives
// (int/string/bool/float64/…) are handled by gob natively and are skipped.
func shouldGobRegisterWire(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Ptr:
		return true
	default:
		return t.PkgPath() != ""
	}
}

func decodePubSubPayload(data []byte) (any, error) {
	var v any
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func encodeEnvelope(m redisPubMsg) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeEnvelope(data []byte) (redisPubMsg, error) {
	var m redisPubMsg
	err := gob.NewDecoder(bytes.NewReader(data)).Decode(&m)
	return m, err
}

// ── Broker selection ─────────────────────────────────────────────────

// brokerForRedisStore returns the cross-instance broker for a Redis
// session store — the scalable-by-default path (deploy multi-instance ⇒
// sessions must be shared ⇒ store=redis ⇒ pub/sub crosses instances with
// no extra config). SKY_LIVE_BROKER=inprocess forces the in-process
// registry back for a single-instance Redis deploy or debugging.
func brokerForRedisStore(client *redis.Client) Broker {
	if brokerForcedInProcess() {
		return newTopicRegistry(0)
	}
	return newRedisBroker(client, false)
}

// maybeOverrideBroker lets a deploy run a Redis broker EVEN when the
// session store is not Redis — e.g. Postgres sessions + Redis pub/sub —
// by setting SKY_LIVE_BROKER_URL. Because the broker is app-scoped, not
// store-scoped, the two are legitimately decoupled. Returns `fallback`
// unchanged when: the var is unset; the store already yielded a
// cross-instance broker (store=redis); the in-process escape hatch is
// set; or the URL can't be dialled (logged — degrade to local). A broker
// created here owns its client and Closes it on Close.
func maybeOverrideBroker(fallback Broker) Broker {
	url := strings.TrimSpace(skyGetenv("LIVE_BROKER_URL"))
	if url == "" || brokerForcedInProcess() {
		return fallback
	}
	if _, ok := fallback.(*redisBroker); ok {
		return fallback // store=redis already gave us one
	}
	client, err := dialRedis(url)
	if err != nil {
		log.Printf("[sky.live] pub/sub: SKY_LIVE_BROKER_URL dial failed (%v); using in-process broker (single-instance fan-out only)", err)
		return fallback
	}
	return newRedisBroker(client, true)
}

func brokerForcedInProcess() bool {
	return strings.EqualFold(strings.TrimSpace(skyGetenv("LIVE_BROKER")), "inprocess")
}

// dialRedis parses a Redis URL or host:port, connects, and Pings so a
// misconfiguration surfaces immediately instead of silently degrading on
// first publish. Mirrors newRedisStore's dial so the two stay consistent.
func dialRedis(addr string) (*redis.Client, error) {
	var opt *redis.Options
	if strings.Contains(addr, "://") {
		parsed, err := redis.ParseURL(addr)
		if err != nil {
			return nil, fmt.Errorf("redis: parse URL: %w", err)
		}
		opt = parsed
	} else {
		opt = &redis.Options{Addr: addr}
	}
	client := redis.NewClient(opt)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return client, nil
}

// compile-time assertion: redisBroker satisfies Broker.
var _ Broker = (*redisBroker)(nil)
