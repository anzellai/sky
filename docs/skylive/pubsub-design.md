# Sky.Live topic-based pub/sub — design doc

> **Status:** DRAFT for review. Cycle 3 P45 deliverable; gates Cycle 3
> P46-P49 implementation work. Tracker issue #259 (Phase 3g).
> No code in this PR — design only.

> **Audience:** the human reviewer who decides whether the Sky-side API
> surface + the runtime architecture below are the shape we want before
> Cycle 3 P46-P49 turn it into code.

> **TL;DR.** Today Sky.Live can only do multi-session coordination via
> Time.every-driven DB polling. This doc proposes a runtime-level
> publish / subscribe primitive: Sky code calls `Live.publish topic
> payload` from any handler; every session that has declared a matching
> `Live.subscribeTopic topic decoder` in its `subscriptions` receives
> the payload as a Msg, dispatched through its normal TEA loop. Replaces
> polling with push. Five prerequisites must land in order; this doc
> nails them down.

---

## 1. Goals and non-goals

### 1.1 Goals

1. **Push-driven multi-session coordination, in-process.** A Sky-source
   author can write multi-tab chat / collaborative editing / presence
   without writing Go FFI and without polling the DB.

2. **TEA-shape integration.** Subscribers receive published payloads as
   Msgs through the existing `update msg model` path. No new dispatch
   pathway in user code — only a new `Sub` constructor and a new `Cmd`
   constructor.

3. **Backend-uniform interface.** The `liveStore` interface gets a
   `Subscribe(topic) (<-chan SessionEvent, cancel func())` API. The
   memory backend implements it natively; SQLite / Postgres / Redis /
   Firestore each get a backend-appropriate fan-out. Behaviour-equivalent
   from the Sky author's point of view.

4. **Bounded memory.** No `map[topic][]chan` that grows without bound.
   Ref-counted topic registry; entries removed when their refcount hits
   zero. Validated by a memory-bound test that opens 1,000 subscriptions,
   closes them, and asserts the registry map returns to zero entries.

5. **Lossless seq under broadcast.** A global per-`liveApp` monotonic
   counter alongside the existing per-session counter. Clients can detect
   missed broadcast frames by gap-checking the global seq.

6. **Multi-session Playwright coverage.** Cycle 3 tooling-gap fix from
   the audit: `scripts/verify-all-web.sh` gets a multi-tab probe that
   opens N parallel sessions and asserts cross-session delivery.

### 1.2 Non-goals (explicitly out of scope for v0.15.x)

1. **Cross-process delivery.** This is in-process pub/sub. A
   horizontally-scaled deployment with N Sky.Live processes behind a
   load balancer does NOT get automatic cross-process fan-out in
   v0.15.x. The Redis backend's *native* pub/sub gives a path forward
   (every process subscribes to a Redis channel; publishes broadcast to
   the channel; every process re-fans out to local subscribers), but
   that's a v0.16+ concern — it requires a separate design pass on
   message envelope format, idempotency, and reconnect semantics.

2. **Message persistence.** Published payloads are NOT retained beyond
   delivery to currently-subscribed sessions. If session B subscribes
   *after* session A publishes, B does not see A's message. Apps that
   need "history on join" persist the state in Std.Db and read it on
   subscription. (Future: a `Live.publishRetained topic payload` could
   bolt this on, but adds replay-on-subscribe complexity that's worth
   deferring.)

3. **Authorisation and topic-level access control.** v0.15.x assumes
   the topic namespace is trusted within an app process — any
   subscriber can subscribe to any topic. Apps that need per-user topic
   gating do the check in their `update` function (subscribe to a
   namespace topic; ignore payloads the user isn't authorised for).
   Topic-level ACLs are a v0.16+ concern.

4. **Wildcard / pattern subscriptions.** `Live.subscribeTopic "chat.*"`
   is NOT supported. Subscriptions are exact-match string keys. Apps
   needing pattern matching subscribe to a namespace topic and filter
   client-side in `update`.

5. **Backpressure beyond the existing SSE channel.** A slow subscriber
   drops broadcast frames the same way it drops dispatch frames today
   — via the buffered `sess.sseCh` `default:` arm, counted into
   `sky_live_sse_drops_total`. We do NOT introduce a separate
   broadcast queue or per-topic flow control; that's a v0.16+ concern.

6. **Delivery guarantees.** Best-effort fan-out. A subscriber whose
   session is being deleted mid-publish silently misses the message;
   `sky_live_sse_drops_total` counts the loss. No at-least-once or
   exactly-once mode in v0.15.x.

---

## 2. API surface (Sky-side)

### 2.1 Stdlib additions

Two new primitives, one on each side:

```elm
-- Std.Sub
subscribeTopic : String -> (any -> msg) -> Sub msg
-- "any" here is the runtime-typed payload; the decoder turns it into msg.

-- Std.Cmd
publish : String -> any -> Cmd msg
-- Fire-and-forget; no result feedback to the publisher.
```

Both reuse the existing `Sub` / `Cmd` runtime-typed (empty home) ADTs.
No new wire-types in the Sky type system; the runtime carries the
payload as `any` and the decoder is responsible for shape-validation
inside `update`.

### 2.2 Concrete example — collaborative diagram editor

`examples/24-skydiagram-collab` (hypothetical Cycle 3 P49 example).
Today's polling shape:

```elm
subscriptions model =
    Time.every 2000 RefreshTick           -- every 2s, fetch from DB

update msg model =
    case msg of
        RefreshTick _ ->
            ( model, Cmd.perform (loadDiagram model.slug) RefreshLoaded )
        RefreshLoaded (Ok newDiagram) ->
            ( { model | diagram = newDiagram }, Cmd.none )
        ...
```

With pub/sub:

```elm
subscriptions model =
    Sub.subscribeTopic ("diagram-" ++ model.slug) RefreshLoaded

update msg model =
    case msg of
        ShapeMoved newDiagram ->
            -- session A moves a shape; persist + publish
            ( { model | diagram = newDiagram }
            , Cmd.batch
                [ Cmd.perform (saveDiagram newDiagram) Saved
                , Cmd.publish ("diagram-" ++ model.slug) (encodeDiagram newDiagram)
                ]
            )
        RefreshLoaded payload ->
            -- session B receives A's broadcast; decode + update
            case JsonDec.decodeValue diagramDecoder payload of
                Ok newDiagram -> ( { model | diagram = newDiagram }, Cmd.none )
                Err _ -> ( model, Cmd.none )
        Saved (Ok _) -> ( model, Cmd.none )
        Saved (Err _) -> ( { model | notification = Just "save failed" }, Cmd.none )
        ...
```

Trade-offs:

- Latency: ~10 ms (intra-process channel hop + SSE flush) vs ~1-2 s
  polling baseline.
- Bandwidth: zero baseline when idle (no polling traffic); each edit
  generates exactly one broadcast frame per subscribed session.
- Composes with C11 SSE diff-then-patch (P50): the broadcast frame's
  view-recompute can go through the same `diffTrees` chokepoint, so
  multi-session frames also ship as patches.

### 2.3 Concrete example — chatroom

```elm
type Msg
    = SendMessage String
    | MessageReceived ChatMessage
    | TypingChanged Bool
    | TypingReceived String  -- "alice is typing..."

subscriptions model =
    Sub.batch
        [ Sub.subscribeTopic ("chat-" ++ model.roomId) MessageReceived
        , Sub.subscribeTopic ("typing-" ++ model.roomId) TypingReceived
        ]

update msg model =
    case msg of
        SendMessage text ->
            let
                m = { author = model.user, text = text, ts = ... }
            in
                ( model
                , Cmd.batch
                    [ Cmd.publish ("chat-" ++ model.roomId) (encodeMessage m)
                    , Cmd.perform (persistChatMessage m) MessageSaved
                    ]
                )
        MessageReceived payload ->
            case JsonDec.decodeValue messageDecoder payload of
                Ok m -> ( { model | history = m :: model.history }, Cmd.none )
                Err _ -> ( model, Cmd.none )
        TypingChanged isTyping ->
            ( model
            , Cmd.publish ("typing-" ++ model.roomId) (typingNotice model.user isTyping)
            )
        TypingReceived payload -> ...
        ...
```

Observations:

- A single session can subscribe to multiple topics (via `Sub.batch`
  — see §3.3 for the subscription registration mechanism).
- A publish can race a Db.persist; if Db fails, the broadcast still
  fires. App author owns reconciliation — fits Sky's "Task → Result
  → Msg" boundary.
- "Typing" is a transient signal that never persists; pub/sub is
  exactly the right primitive (was previously impossible without
  polling a transient indicator out of the DB).

---

## 3. Runtime architecture

The audit C3 lists FIVE prereqs. Each is a runtime-level decision; each
maps to a sub-section here.

### 3.1 `liveStore.Subscribe` API (prereq 1)

#### Interface

Extend `SessionStore` (runtime-go/rt/live_store.go:253) with:

```go
type SessionStore interface {
    // ...existing methods unchanged...

    // Subscribe registers a fan-out listener for `topic`. Returns a
    // receive-only channel on which broadcast SessionEvents arrive,
    // and a cancel function that must be called when the subscription
    // is no longer needed (typically: on session.Delete or on a
    // setupSubscriptions re-eval that no longer mentions this topic).
    //
    // Topic registry is ref-counted; closing the cancel func decrements
    // the count. When the count reaches zero the topic entry is removed.
    //
    // The returned channel is buffered (capacity = SKY_LIVE_SSE_BUFFER
    // by default; oversend drops via `default:` arm at the publisher,
    // mirroring sess.sseCh semantics).
    Subscribe(topic string) (<-chan SessionEvent, func())

    // Publish fans out `event` to every subscriber of `topic`.
    // Best-effort: a slow subscriber's drop is counted via
    // sky_live_sse_drops_total and recovery is the next event.
    //
    // Returns the count of subscribers that received the event (so the
    // publisher can observe fan-out size for tracing/metrics).
    Publish(topic string, event SessionEvent) int
}

// SessionEvent carries the broadcast payload. `Topic` is the wire
// channel id; `Payload` is the Sky-side `any` value that the receiver
// session's decoder will be called with.
type SessionEvent struct {
    Topic     string
    Payload   any
    GlobalSeq int64   // app-wide; see §3.2
    Origin    string  // publisher sid; subscribers can self-skip (echo suppression)
}
```

#### Backend feasibility analysis

| Backend | Native fan-out | v0.15.x plan |
|---|---|---|
| `memory` | n/a (in-process) | Native — `map[string]*topicRegistry` keyed by topic; each entry holds a slice of subscriber channels + a refcount. |
| `sqlite` | n/a (file/disk) | Falls through to the **app-level memory fan-out registry** (§3.1.1) — sqlite stores SESSION state but the pub/sub registry lives in `app.topics` in-process. P46 ships the memory backend; sqlite uses the same in-process registry. |
| `postgres` | LISTEN/NOTIFY | v0.15.x: in-process registry (same as sqlite). Cross-process via `LISTEN broadcast_<topic>` is a v0.16+ concern. |
| `redis` | Native pub/sub | v0.15.x: in-process registry. Cross-process via Redis `SUBSCRIBE`/`PUBLISH` is a v0.16+ concern; the affordance exists in the backend but P46 deliberately doesn't wire it. |
| `firestore` | Snapshot listeners | NOT IN RUNTIME — `firestoreSessionStore` is mentioned in docs but no Go code exists today. P46 skips it; v0.15.x ships without firestore pub/sub. |

#### 3.1.1 Where the registry actually lives

The five backend store impls all share **the same** in-process
`topicRegistry` for v0.15.x. The cleanest place is on `liveApp` (not
on the store), because:

- The store's job is **session state persistence**. Pub/sub is
  **app-level fan-out**. Coupling them ties the registry's lifetime to
  the store's, but we want it to live for the lifetime of the app.
- Storing it on `liveApp` means `Subscribe` / `Publish` work
  identically across all five backends. The store interface still owns
  the methods (for future cross-process backends to override) but the
  default implementation delegates to `app.topics`.

Implementation sketch (NOT code for this PR — design only):

```go
type liveApp struct {
    // ...existing fields...
    topics *topicRegistry
}

type topicRegistry struct {
    mu sync.Mutex
    // topic name → registry entry
    entries map[string]*topicEntry
}

type topicEntry struct {
    // Subscriber channels; key = unique sub id (per-subscribe-call).
    subs map[uint64]chan SessionEvent
    // Refcount = len(subs); when 0, the entry is removed from `entries`.
}

// Subscribe / Publish below the registry are straightforward sync.Mutex-guarded
// map operations. The audit's "ref-counted topics + cleanup on session.Delete"
// is implemented as: every Subscribe returns a cancel func that:
//   1. removes the sub channel from topicEntry.subs
//   2. if len(subs) == 0, removes the entry from registry.entries.
```

#### Memory bound test (P46 deliverable)

`runtime-go/rt/live_store_subscribe_memory_bound_test.go` — opens
1,000 subscriptions across 100 topics, closes them all, asserts
`len(app.topics.entries) == 0`. Repeats under `-race`. This is the
load-bearing test that prevents a future refactor from regressing
the cleanup-on-zero contract.

### 3.2 Global + per-session seq split (prereq 2)

#### Today

`sess.outSeq` (live.go:1341) is per-session. Every outgoing frame
bumps it. The client uses it for stale-drop ordering.

#### Problem

A broadcast event fans out to N sessions; each session bumps its
*own* `outSeq` independently. But broadcast events themselves need a
**global** monotonic identifier so:

1. Observers (the Sky.Live dev console, future cross-process bridges,
   telemetry) can sequence events across sessions.
2. Subscribers can detect a dropped broadcast by gap-checking the
   global seq.

#### Proposed change

- Rename `sess.outSeq` → `sess.localSeq` everywhere. Mechanical rename
  through ~15 sites. (Audit recommends; P47's plan endorses.)
- New `app.globalSeq atomic.Int64`. Bumped by:
  - Every `Publish` call (one bump per publish, BEFORE fan-out, so
    every subscriber sees the same globalSeq for one publish).
- `SessionEvent.GlobalSeq` carries the value to subscribers.
- SSE frames already carry `{seq, body, ackInputs}`. Add `globalSeq`
  (optional/zero when not a broadcast frame). Client stores
  `__skyLastAppliedLocalSeq` (existing) AND `__skyLastAppliedGlobalSeq`
  (new). Gap-check on globalSeq surfaces missed broadcast frames.

#### Why not just one counter?

A single counter forces every dispatch on every session to compete
for one atomic — pessimistic synchronisation on a per-session
operation. The split keeps per-session dispatch lock-free (no
cross-session contention) and pays the atomic cost only on broadcast.

#### Forward compatibility with C11

When P50 ships SSE diff-then-patch, broadcast-induced patches are
*also* patches (the recipient session's view recomputes; diffTrees
produces structural patches). The globalSeq field is orthogonal to the
patch envelope — it tags the event regardless of whether the body is
a full HTML body or a patches array. Compatible by construction.

### 3.3 Handler rebuild on broadcast-induced patch (prereq 3)

#### Why this matters

`renderVNode` (live.go:258) populates `sess.handlers` as a side effect
of rendering. The client sends event payloads keyed by sky-id; the
server looks up the handler in `sess.handlers`. If a broadcast
changes session B's view (B re-renders to show a new message), B's
handlers map must be rebuilt — otherwise B's next click hits a stale
sky-id whose handler reference is for the *previous* render.

#### Two paths considered

| Approach | Pros | Cons |
|---|---|---|
| (a) **Msg-shape broadcast** — broadcast just dispatches a Msg on each subscribing session; existing dispatch path re-renders + rebuilds handlers naturally. | Reuses existing dispatch; no new code path; handler rebuild is automatic. Symmetric with how Cmd.perform completions already work. | Each subscriber pays the full dispatch cost on each broadcast. (Acceptable — the cost is `update msg model` plus a re-render; same as a user click.) |
| (b) **Broadcast-render pathway** — bypass `update`, just apply a "shape" to model and re-render. | Skips `update` cost. | Two render paths; handler-rebuild contract becomes "every view-changing event MUST re-render". Fragile; one missed call site silently drops handlers. |

**Decision: (a) Msg-shape broadcast.** Reasons:

1. Symmetric with Cmd.perform completion (existing precedent). Author
   mental model is "the result of a publish lands as a Msg, just like
   the result of a Task lands as a Msg".
2. Handler rebuild is a free side effect — dispatch already re-renders
   and `renderVNode` already populates `sess.handlers`. No new
   invariant.
3. The cost concern is theoretical: a broadcast triggers exactly the
   same work a user click would (one `update` call + one re-render).
   No worse than today's interactive load.
4. Composes with prereq 4 (Msg-shape is *the* divergence-detection
   shape; see §3.4).

#### Concrete dispatch path

When `Publish(topic, event)` fires:

1. The registry looks up every subscriber channel for `topic`.
2. For each subscriber, push `SessionEvent{Topic, Payload, GlobalSeq, Origin}`
   onto the channel (non-blocking; drop via `default:` if full +
   `sky_live_sse_drops_total++`).
3. Each subscribing session has a goroutine (started by
   `setupSubscriptions`) that selects on its subscription channels.
   When an event arrives, the goroutine:
   - Acquires `sess.mu`.
   - Calls the user's `decoder payload` to get a `Msg` (via
     `sky_call`, which is how user functions are invoked from runtime).
   - Calls `app.dispatch(sess, msg)` — re-runs update + view +
     handler rebuild + SSE patch frame, exactly like a user-event
     dispatch.
   - Releases `sess.mu`.

The subscription goroutine is the OWNER of the subscribe-channel's
cancel func: when `setupSubscriptions` re-evaluates and a topic is no
longer in the new `Sub.batch`, the old goroutine's `cancel` is closed
and the goroutine exits, releasing its registry refcount.

### 3.4 prevTree divergence detection (prereq 4)

#### The race

> Session A applies broadcast event 100; session B applies user event
> 99 then broadcast 100. If event 99 modifies a field broadcast 100
> reads, A and B diverge.

#### Why Msg-shape solves it

If broadcast events are Msg-shape (not "total replacement of state"):

- Each session's `update` applies the broadcast Msg **after** any
  user-Msgs that arrived before it. The ordering is deterministic
  per-session via `sess.mu` serialisation.
- The broadcast Msg itself is **pure data** — it doesn't carry a
  reference to the publisher's model state, it carries a payload
  that describes what changed.
- Convergence is the user's responsibility: as long as `update`'s
  effect is commutative *enough* (in practice: idempotent or
  append-only), all sessions converge.

For non-commutative state (e.g. "remove the third element from a
list"), the user encodes the change as a *content-addressed* operation
("remove element with id 42") rather than a positional one. Same
discipline as any distributed eventually-consistent system.

#### What about divergence detection itself?

Two strategies, neither mandatory in v0.15.x but both available
post-P49:

1. **Per-app monotonic versioning of shared state.** Apps that need
   strict serialisation persist shared state in Std.Db; the broadcast
   Msg carries a version number; subscribers reject Msgs with stale
   versions and re-fetch from the DB. (Same pattern as optimistic
   concurrency control on web forms.) **Implementation effort: zero in
   runtime; user-space pattern.**

2. **Runtime-level "broadcast Msg replaces local state" mode.** Skipped
   for v0.15.x; (1) is sufficient + Elm-shape. Revisit only if the
   user-space pattern proves consistently painful across apps.

### 3.5 Ref-counted topic registry (prereq 5 — folded into §3.1)

Already covered above. Recap:

- `topicEntry.subs map[uint64]chan SessionEvent` keyed by per-subscribe
  unique id.
- Subscribe increments by adding an entry.
- Cancel decrements by removing the entry; when `len(subs) == 0` the
  whole `topicEntry` is deleted from `app.topics.entries`.
- Memory-bound test asserts the contract.
- `session.Delete` invokes every active subscription's cancel func
  before clearing the session (closing the loop on prereq 5's
  "cleanup on session.Delete" — see §4.4).

---

## 4. Lifecycle

### 4.1 Session subscribes on `subscriptions` evaluation

The runtime calls `setupSubscriptions(sess)` after every dispatch
(live.go:3060). Today this only handles `Sub.every` (Time.every). Add
a `Sub.subscribeTopic` arm:

1. Walk the `subT` value's `kind`. If `"batch"`, recurse into children.
2. For each `"subscribeTopic"` entry, call
   `app.topics.Subscribe(topic)` and spawn a goroutine that selects on:
   - The returned event channel (deliver Msg → dispatch).
   - `sess.cancelSub` (per-subscription, replaced every
     setupSubscriptions call — exits the goroutine on next eval).
   - `sess.done` (terminal teardown; closes the goroutine when the
     session is deleted).

3. Track the cancel func on `sess.subCancels []func()` so
   `setupSubscriptions`'s next pass can release the registry refcount
   when a topic is no longer in the new subscription set.

#### Diff vs replace?

Today `setupSubscriptions` does a "blow-up-and-rebuild" pass: every
call cancels the prior ticker, opens a new one. For Time.every
(single subscription), this is fine.

For pub/sub, doing the same means every dispatch cycles every topic
subscription through cancel + re-subscribe. Two costs:

1. Brief window where a broadcast fired between cancel and
   re-subscribe is silently dropped.
2. Registry mutex contention proportional to dispatch rate × number
   of subscribed topics.

**Decision:** diff-mode subscription update.

- `sess.activeSubs map[string]subRegistration` (topic → registration)
  persists across dispatches.
- `setupSubscriptions` builds the NEW topic set from the model;
  computes `added := new - old`, `removed := old - new`.
- Cancels only `removed`. Opens only `added`. Topics in both
  `old ∩ new` keep their existing goroutine + channel; no broadcast
  loss in the gap.

This is a small but load-bearing refinement; P48's implementation
must get it right.

### 4.2 Publisher fan-out

```
update msg model
    -> returns (Cmd.publish topic payload)
    -> Cmd_publish kernel: app.topics.Publish(topic, SessionEvent{...})
    -> registry: for each subscriber chan, non-blocking push
    -> drops counted via sky_live_sse_drops_total{kind="broadcast"}
```

Latency: ~10 µs from publish to channel receive on M1 (pure goroutine
hop, no I/O). 99th percentile ≪ 1 ms.

### 4.3 Subscriber Msg dispatch

Per subscriber goroutine, for each `<-eventCh`:

1. `decoder := sub.toMsg` (the user-supplied `any -> Msg`).
2. `msg := sky_call(decoder, event.Payload)`.
3. `app.dispatch(sess, msg)` — re-uses existing dispatch path.
4. The dispatch produces an SSE frame (or suppresses on
   byte-identical view); ships via `sess.sseCh`.

**Open: what happens if the decoder panics?** Sky guarantee: well-typed
code never panics, but `any → Msg` is untyped at the runtime layer.
The decoder is wrapped in `runWithRecover` (same as Ffi.callTask /
callPure). Panic → no Msg dispatched, error logged via `Log.errorWith
"pubsub.decoder" [...]`, broadcast frame "consumed" without a
state change. App author sees a log line, can fix the decoder.

### 4.4 Session delete cleanup

When `Delete(sid)` fires (TTL eviction OR explicit teardown):

1. Existing path: `sess.markDone()` closes `sess.done` channel.
2. NEW: in `markDone`, walk `sess.activeSubs` and call each cancel
   func. This removes the session's registrations from `app.topics`
   AND decrements topic refcounts.
3. Subscription goroutines exit via the `sess.done` arm of their
   select.

The `sess.subCancels` field's locking model: protected by `sess.mu`
on writes (setupSubscriptions); read once on `markDone` after closing
`sess.done`. Concurrent setupSubscriptions vs markDone is safe because
markDone is idempotent (sync.Once) and setupSubscriptions checks
`sess.done` is open before mutating activeSubs.

---

## 5. Wire protocol

### 5.1 Where does the broadcast frame travel?

**Same SSE channel.** No new HTTP endpoint, no new connection. The
existing `/_sky/sse` per-session SSE stream carries:

- Today: dispatch-induced patch frames (full body for now; patches
  after P50).
- New: broadcast-induced patch frames (same shape; same encoding;
  client doesn't distinguish at the transport layer).

The session's subscription goroutine dispatches a Msg, which
re-renders, which encodes an SSE frame, which lands on `sess.sseCh`,
which `handleSSE` reads + writes. **Zero new transport code.**

This is by design: the broadcast IS a dispatch (per §3.3 decision (a)),
just one whose Msg came from `Publish` rather than `Cmd.perform`.

### 5.2 Does the frame carry the topic name?

The **SSE frame envelope does not** carry the topic name. The frame is
the rendered view (or patches). The topic is metadata visible only at
the subscription goroutine layer, not at the wire layer.

**Why:** the client doesn't care which topic produced the view-change.
Its job is to apply the patch. Differentiating "this patch came from a
broadcast vs a user dispatch" is a telemetry concern, not a transport
concern.

**Exception:** the dev console (Sky Console) MAY want to surface
"this frame's broadcast origin" for debug visibility. Implement via
an OPTIONAL `X-Sky-Broadcast-Topic` SSE event-level field on the
broadcast-induced frame, surfaced via the Std.Trace span. NOT load-
bearing; can ship in a follow-up cycle.

### 5.3 Authentication / authorisation per topic

**v0.15.x scope (per §1.2 non-goal #3):** trust the in-process topic
namespace. No authn/authz at the topic layer. Apps that need it do
the check in `update`:

```elm
RefreshLoaded payload ->
    case JsonDec.decodeValue diagramDecoder payload of
        Ok newDiagram ->
            if model.user.canRead newDiagram.ownerId then
                ( { model | diagram = newDiagram }, Cmd.none )
            else
                ( model, Cmd.none )    -- silently drop
        Err _ -> ( model, Cmd.none )
```

The auth check happens *after* dispatch, against the session's auth
state. The runtime doesn't need to know.

### 5.4 Forward compatibility with cross-process pub/sub (v0.16+)

When/if v0.16 ships cross-process pub/sub:

- A new envelope shape (`X-Sky-Broadcast-Origin: <process-id>`) over
  Redis/Postgres backbone.
- Per-process subscriptions to backbone topics; fan-out becomes
  two-step (process A publishes → backbone → all processes receive →
  each process fans out to local subscribers).
- Idempotency requires the publish to carry a `(origin, globalSeq)`
  tuple; v0.15.x's `globalSeq` is already per-process; the cross-
  process layer prepends the process id.

**The v0.15.x design does not preclude any of this.** `SessionEvent`
already has `Origin` (publisher sid) and `GlobalSeq` (app-wide); add a
`ProcessId` field at the v0.16 transition without re-architecting.

---

## 6. Failure modes

### 6.1 Subscriber falls behind (backpressure)

Subscriber's SSE channel buffer fills. Behaviour:

- Broadcast `Publish` finds the subscriber's event channel full.
- Drops via `default:`, increments `sky_live_sse_drops_total{kind="broadcast"}`.
- Subscriber misses the broadcast entirely. The next broadcast (or
  user dispatch) re-renders the view from the current model, which —
  per Msg-shape design (§3.4) — does NOT depend on the missed Msg.

**Mitigation:** apps that absolutely cannot afford a drop persist
state in Std.Db and publish only "fetch latest" notifications. The
subscriber's `update` does a DB read to catch up. Convergent
eventually.

### 6.2 Publisher floods

Publisher in a tight loop. Behaviour:

- Each `Publish` is non-blocking (drops on full subscriber channels).
- The publisher's own session is unaffected (publisher does NOT
  subscribe to its own topic by default; if it does, the message
  arrives via the subscription goroutine like any other).
- Risk: `globalSeq` advances at high rate; observers see a high-volume
  series.

**Mitigation in v0.15.x:** none at runtime. App author owns rate
limiting (use Time.every + a counter; bounded queue inside `update`).
Future: `Live.publish` could grow an env-knob rate limiter, but defer.

### 6.3 Session deleted mid-publish

Race: `Publish` is iterating subscribers when one session's
`Delete` fires.

- Registry mutex protects the subscriber list. `Publish` holds the
  mutex for the iteration; `Delete`'s subscription cancellation
  acquires the same mutex before mutating the list. Mutually
  exclusive.
- If a session is deleted *after* `Publish` snapshots the subscriber
  list but *before* the channel push: the channel push succeeds (the
  channel exists; closing it isn't part of cancel — cancel just
  removes it from the registry); the subscriber's session is gone, so
  the goroutine reading the channel exits via `sess.done` arm before
  it sees the event. Event silently lost. Acceptable (the session is
  gone; there is no observer).

### 6.4 Memory backend vs persistent backends

Memory: all state in-process; pub/sub registry shares the lifetime.
Restart loses everything (sessions, subscriptions, in-flight events).

SQLite / Postgres / Redis: sessions persist; subscriptions DO NOT
(they're in-process state — `sess.activeSubs` is not serialised).

**On restart:** persisted sessions are loaded; their
`setupSubscriptions` re-evaluates from the restored model; subscriptions
are re-opened naturally. **No special restart code needed.** This works
because subscriptions are derived state of the model (`subscriptions
model = ...`), not stored independently.

### 6.5 Single-process vs eventual cross-process story

Single-process today. Cross-process is v0.16+ via Redis pub/sub. The
v0.15.x interface (`Subscribe` / `Publish` on the SessionStore) is
deliberately shaped so a future `redisStore.Subscribe` implementation
can override the default app-level registry with a true Redis-backed
fan-out. **No Sky-side API change required** at the v0.15.x → v0.16
transition.

---

## 7. Compatibility with C11 SSE diff-then-patch (P50)

C11 (P50a/P50b) ships structural patches over SSE: every SSE frame
becomes an `event: patches` envelope carrying a JSON patch array
instead of full HTML.

**How does pub/sub fit?**

1. **Same channel.** Broadcast-induced re-renders go through the same
   dispatch path that produces SSE frames. P50 wraps that path with
   `diffTrees(prevTree, newTree)` before encoding. The broadcast IS a
   dispatch (per §3.3) — so it automatically benefits from P50.

2. **Bandwidth gain.** Multi-session apps with frequent broadcasts
   (chat, collab editor) see the largest gain from P50 — every
   broadcast on every session fans out as a tiny patch, not a 50 KB
   HTML body.

3. **Sequencing.** P50's patches carry the per-session local seq for
   ordering; pub/sub adds `globalSeq`. Both seqs travel in the same
   JSON envelope. No conflict.

4. **Order of landing.** P45 (this doc) does not block P50; P50
   doesn't block P45. They can land in either order. The
   implementation sequencing (§8) recommends **P50 first** for
   bandwidth-win-on-day-one, but either order works.

5. **Compatibility test.** Once both land, a multi-tab Playwright
   probe (per audit tooling-gap 2) exercises: tab A publishes; tab B
   receives via SSE as an `event: patches` envelope; tab B's
   `__skyApplyPatches` updates the DOM; the new view's sky-ids
   resolve to fresh handlers; tab B's next click dispatches
   correctly. This is the integration test that pins compatibility.

---

## 8. Implementation sequencing (P46-P49)

### Recommended order

```
P45 (this doc — design + review)
  │
  ├──> P46 (Store Subscribe API + ref-counted topic registry)
  │       Independently lands; new API exists but unused by Sky-side.
  │
  ├──> P47 (Global + local seq split)
  │       Independently lands; can parallel-track with P46.
  │       Both P46 and P47 land BEFORE P48.
  │
  └──> P48 (handler rebuild on broadcast-induced patch)
          Depends on P46 (Subscribe exists) + P47 (globalSeq exists).
          Implements Msg-shape dispatch from subscription goroutine.
          The Sky-side `Sub.subscribeTopic` + `Cmd.publish` stdlib
          primitives land here (so the runtime + Sky-source surface
          ship together).
          │
          └──> P49 (Msg-shape broadcast — divergence test fixture +
                    example + Playwright multi-tab probe + docs)
                  Depends on P48.
                  Smaller than P48 — primarily test + docs work.
```

### Dependency rationale

- **P46 and P47 can run in parallel.** P46 is storage / registry; P47
  is sequencing. They don't touch each other.
- **P48 needs both.** Subscribe → goroutine → dispatch → SSE frame.
  Each step depends on one prior PR.
- **P49 is mostly verification + the user-visible example.** It's
  small (per planner: 12-16 hours) because the heavy lifting is in
  P46-P48.
- **P50 (SSE diff-then-patch) is orthogonal.** Can ship before, after,
  or interleaved with P46-P49. The cycle-3 planner sequences P50a/b
  AFTER the C3 work, but it's not load-bearing.

### Estimated total: 30-45 hours

Per planner:
- P46: 10-14 h
- P47: 5-7 h
- P48: 6-8 h
- P49: 12-16 h
- **= 33-45 h, plus this design doc P45 (~3 h actual).**

---

## 9. Test plan

### 9.1 Memory-store unit tests (P46)

- `live_store_subscribe_test.go` — table-driven:
  - Subscribe → Publish → receive on channel.
  - Multiple subscribers, one publish → all receive.
  - Cancel → no further events on the channel.
  - Cancel last subscriber → topic entry removed from registry map.
  - Concurrent Subscribe + Publish + Cancel under `-race` (1,000-op
    burst).
- `live_store_subscribe_memory_bound_test.go` — opens 1,000
  subscriptions across 100 topics, closes them, asserts
  `len(app.topics.entries) == 0`. Repeat 10×; assert no goroutine
  leak via `runtime.NumGoroutine` returns to baseline.

### 9.2 Seq split tests (P47)

- `live_seq_split_test.go`:
  - Single-session dispatch: localSeq advances; globalSeq stays at 0
    (no Publish call). Wire frame includes `globalSeq: 0` (or
    omitted; either is OK as long as the JS client is tolerant).
  - Publish from session A: `app.globalSeq` advances by 1.
  - Publish to N subscribers: all N see the SAME globalSeq.
  - Interleaved local + broadcast on session B: localSeq advances
    monotonically; globalSeq tracks publishes only.

### 9.3 Handler rebuild tests (P48)

- `live_broadcast_handler_rebuild_test.go`:
  - Open 2 sessions (A, B) subscribed to same topic.
  - A publishes (e.g. "increment counter" Msg).
  - Assert B's view re-renders (`sess.lastShippedBody` advances).
  - Assert B's `sess.handlers` map size matches the new view's
    sky-id count.
  - Simulate a user click on B's new view (one of the newly-bound
    sky-ids). Assert dispatch resolves the handler correctly.

### 9.4 Divergence test (P49)

- `live_broadcast_divergence_test.go`:
  - Two sessions sharing model state via DB.
  - Session A applies user event "set name to alice".
  - Concurrently, session B applies user event "set role to admin".
  - Each session publishes its change.
  - Both sessions receive both broadcasts (each as a Msg).
  - Assert both converge to `{ name = "alice", role = "admin" }`.

### 9.5 Multi-session Playwright probe (P49)

- New script: `scripts/verify-pubsub-multitab.sh` (or fold into
  `verify-all-web.sh` as a new probe).
- Spin up `examples/27-multi-session-chat`.
- Open 3 headless Playwright tabs against the same session origin
  (different sky_sid cookies).
- Tab 1 sends "hello"; assert tabs 2 + 3 see "hello" within 500 ms.
- Tab 2 sends "world"; assert tabs 1 + 3 see "world".
- Tab 1 closes; assert tab 2 publish reaches tab 3 (and registry
  has only 2 entries).
- Tab 3 closes; assert tab 2's `Live.publish` returns 0 subscribers
  (sole publisher; itself doesn't subscribe to own message).

### 9.6 Race detector

Every `_test.go` runs under `-race`. Particularly: concurrent
Publish + Subscribe + Cancel on the same topic registry. Mutex
discipline at `topicRegistry.mu` must hold up.

### 9.7 Bench

`live_pubsub_bench_test.go` — measure publish-to-receive latency
distribution on 10/100/1000 subscribers. P50 latency should be ≤ 100
µs at 100 subs; P99 ≤ 1 ms. Document baseline so future regressions
surface.

---

## 10. Migration path

### 10.1 Apps still using polling

**No automatic migration.** Time.every + Cmd.perform polling continues
to work unchanged. Apps that benefit from pub/sub can opt in module-
by-module.

### 10.2 examples/16-skychess (Time.every 10000 Tick)

Today polls Db every 10 s. Could migrate to pub/sub:

```elm
-- Before:
subscriptions model =
    if model.gameInProgress then
        Time.every 10000 RefreshTick
    else
        Sub.none

-- After:
subscriptions model =
    if model.gameInProgress then
        Sub.subscribeTopic ("game-" ++ model.gameId) MoveReceived
    else
        Sub.none

-- In update (handler for the opponent's move):
SubmitMove move ->
    ( { model | board = applyMove move model.board }
    , Cmd.batch
        [ Cmd.perform (saveMove model.gameId move) MoveSaved
        , Cmd.publish ("game-" ++ model.gameId) (encodeMove move)
        ]
    )
```

Each player sees the opponent's moves in ~10 ms instead of waiting up
to 10 s for the next poll. Persistence in Db (unchanged). The DB save
+ the publish race; if the publish arrives at the opponent first and
they crash + reconnect, the persisted state catches them up.

### 10.3 examples/12-skyvote (Time.every 10000 Tick)

Similar — vote tallies update every 10 s today; could be push-driven.
But voting is low-frequency; polling is fine. Migration is optional.

### 10.4 Hypothetical sky-diagram

The motivating use case from the assignment brief. Replace
`Time.every 2000 RefreshTick` with `Sub.subscribeTopic
("diagram-" ++ slug) RefreshLoaded` and add `Cmd.publish` on every
shape mutation. **Latency drops from 2 s to ~10 ms; bandwidth drops to
zero baseline.**

### 10.5 No deprecation of `Sub.every` / `Time.every`

These remain first-class. Polling is the right answer for many
use cases (heartbeat to a service that doesn't push; periodic
animation tick; clock display). Pub/sub is an *additional* primitive,
not a replacement.

### 10.6 Documentation

P49 ships `docs/skylive/pubsub.md` (user-facing tutorial) and updates
`templates/CLAUDE.md` (the AI-author template) with the API. This doc
(`docs/skylive/pubsub-design.md`) stays as the *architectural*
reference; user-facing docs are a separate deliverable.

---

## 11. Risks and open questions

### 11.1 Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Registry mutex becomes a bottleneck at high publish rate | Medium | `topicRegistry.mu` is per-app; per-topic shards would scale further but add complexity. Measure with bench before sharding. Bench gate (§9.7): P99 latency ≤ 1 ms at 100 subs. |
| Subscriber goroutines leak on partial setupSubscriptions failure | Medium | Diff-mode subscription update (§4.1) keeps the cancel funcs paired with the live channels. `sess.markDone` cancels EVERY remaining sub; tested under `-race`. |
| `Cmd.publish` from `init` arrives before subscribers attach | Low | Document: publish-during-init has undefined delivery semantics. Use Cmd.perform → Msg → publish-in-update if init-time fan-out is needed. |
| Cross-process apps assume pub/sub works between processes | High (user surprise) | `docs/skylive/pubsub.md` documents "in-process only" prominently. Future v0.16 work adds cross-process via Redis backbone. |
| Decoder panics consume the event silently | Low | Logged via Log.errorWith (§4.3); user sees the error message. Could escalate to "session enters error state" but that's heavy-handed for v0.15.x. |
| Memory backend + many topics + restart → no continuity | Acceptable | Memory backend is dev-mode default; persistent backends (sqlite / postgres / redis) preserve sessions across restart and subscriptions re-attach automatically (§6.4). |
| Existing apps that use polling continue to poll AND now also subscribe (double-load) | None | Opt-in; no automatic migration (§10.5). Apps that adopt pub/sub remove their polling explicitly. |

### 11.2 Open questions for human reviewer — RESOLVED 2026-05-27

User locked in all four recommended defaults. Summary in §11.2.5;
full original analysis preserved below for context.

| Q | Default chosen |
|---|---|
| Q1 — API naming | `Std.Cmd.publish` / `Std.Sub.subscribeTopic` |
| Q2 — Echo to publisher | Yes (matches Redis/NATS/MQTT) |
| Q3 — Per-session sub-rate limit | No limit in v0.15.x |
| Q4 — Cmd_publish routing | Thread through dispatch context (option a) |

P46 unblocked to begin implementation with these defaults.

---

**Q1: Is `Live.publish` / `Live.subscribeTopic` the right naming?**

Proposed:

```elm
Std.Cmd.publish        : String -> any -> Cmd msg
Std.Sub.subscribeTopic : String -> (any -> msg) -> Sub msg
```

Alternatives considered:

| Option | Pros | Cons |
|---|---|---|
| `Cmd.publish` / `Sub.subscribeTopic` (proposed) | Lives where the existing primitives live; concise on the publish side. | "subscribeTopic" is asymmetric with "publish" — would `Sub.publish` ↔ `Sub.subscribe` be cleaner? |
| `Live.publish` / `Live.subscribeTopic` | Visually grouped under "Live" module; clearer that this is Sky.Live-specific. | Doesn't fit Sky.Tui (which has its own Sub but no pub/sub). User has to remember which module the function lives in. |
| `Pubsub.publish` / `Pubsub.subscribe` | Own module; namespaced; cleanest. | Yet another module to import; "publish" and "subscribe" lose their tea-shape pairing with Cmd/Sub. |

**Recommendation: proposed.** Keeps the Sub/Cmd tea-shape; minor
asymmetry (subscribeTopic vs publish) is acceptable because subscribe
NEEDS a decoder param, which makes it look different by necessity.

**Decision needed:** confirm or pick alternative.

**Q2: Echo suppression by default?**

When session A publishes, does A itself receive its own message
through any matching subscription?

- **Yes (current proposal):** symmetric; if A is in a chatroom and
  publishes a message, A sees its own message arrive like any other
  user's. App can dedupe via `Origin == sess.sid`.
- **No:** A is auto-skipped via `Origin` field comparison in the
  registry's fan-out loop. Saves bandwidth + one dispatch.

Most pub/sub systems (Redis, NATS, MQTT) echo by default. App authors
expect echo. **Recommendation: echo.** Skip via app-level filter if
desired.

**Decision needed:** confirm or pick alternative.

**Q3: Per-session subscribe-rate limit?**

A misbehaving subscription could open thousands of topics from
`subscriptions`. Currently no limit. Options:

- **No limit (proposed).** `subscriptions model` runs every dispatch;
  if user puts `List.map subscribeTopic` over an unbounded list, the
  diff-mode update (§4.1) keeps registry mutations proportional to
  the actual change set. Memory bound is `O(sum of all
  subscriptions)`; this is the same as today's Sub model.
- **Env knob `SKY_LIVE_MAX_SUBS_PER_SESSION` (default 100).** Refuse
  excess subscriptions; log warning. Belt-and-braces.

**Recommendation: no limit in v0.15.x; revisit if a real app hits a
runaway.** Add only if observed.

**Decision needed:** confirm or pick alternative.

**Q4: Where does `Std.Cmd.publish` actually dispatch from?**

The runtime needs to know which `liveApp` to publish to. Three
options:

(a) `Cmd.publish` gets wired via the same dispatch loop that runs
    Cmd.perform — the dispatch context carries the app reference;
    `Cmd_publish` kernel reads it. **Recommendation; cleanest.**

(b) `Cmd.publish` takes an extra (hidden) param threaded from the
    runtime entry. Messier; leaks an implementation detail.

(c) Global per-process registry. Works in v0.15.x (one app per
    process) but breaks the v0.16+ multi-app-per-process story
    (sub-apps).

**Recommendation: (a).** The dispatch loop already has the app
reference; passing it through is a one-line plumbing change.

**Decision needed:** confirm or pick alternative.

### 11.2.5 Cross-process broker tiers (Cloud Run scaling)

The in-process registry in §3 covers a SINGLE Sky.Live instance
end-to-end. For multi-instance deployments (Cloud Run autoscaling,
multi-pod Kubernetes, blue/green deploys with concurrent traffic on
both versions) sessions on different instances do NOT see each
other's publishes — the registry is per-process.

**v0.15.x ships in-process only.** The `liveStore.Subscribe`
interface already exists (§3.2) precisely so v0.16+ can plug in
cross-process brokers without touching call-sites. This subsection
records the planned implementation tiers so apps can pick the right
broker when they need cross-instance fan-out.

| Tier | Tech | Cost | When |
|---|---|---|---|
| 0 (v0.15.x default) | In-process Go channels + refcounted registry | $0 / no extra infra | Single-instance Cloud Run; dev; sky-diagram-shaped apps with low cross-session traffic |
| 1 (v0.16 priority) | Redis Pub/Sub via `github.com/redis/go-redis/v9` PSubscribe | $5-30/mo managed Redis | Multi-instance Cloud Run; ubiquitous broker; sub-ms latency on same VPC; doesn't persist (acceptable for live-collab) |
| 2 (v0.16+) | Google Cloud Pub/Sub streaming pull | ~$0.40 per million msgs | GCP-native deployments (Cloud Run + Firestore stacks); IAM-authenticated; auto-scales infinitely; persistence + replay if needed |
| 3 (v0.16+ nice-to-have) | PostgreSQL LISTEN/NOTIFY | $0 if already on Postgres | Apps already on Postgres that want zero extra moving parts; 8KB payload cap acceptable for Msg-shape broadcasts |
| 4 (deferred) | NATS JetStream | $5+/mo managed NATS | High-throughput apps that outgrow Redis Pub/Sub; subjects + persistence |

**Selection at runtime** mirrors `[live] store` in sky.toml:

```toml
[live.broker]
kind    = "redis"             # in-process (default) | redis | gcp-pubsub | pg-notify
url     = "$REDIS_URL"        # broker-specific URL
prefix  = "sky-live"          # topic prefix for namespacing
```

`sky_live_broker_msgs_total{kind,direction}` Prometheus counter
exported alongside the SSE drop counter (§6.7).

### 11.2.6 Cloud Run connection-duration cap

Cloud Run terminates HTTP requests at `--timeout` (max 60 min). The
SSE long-lived response is one such request; it gets cut at the
ceiling. Existing v0.15.13+ behaviour handles this without changes:

1. EventSource auto-reconnects on close
2. handleSSE's reconnect-resync (§4.4 / `live.go:~2820`) re-renders
   from current sess.model and ships a fresh frame on connect
3. Pub/sub messages published DURING the reconnect window:
   - In-process (tier 0): SAFE — same process retains session state
     across the SSE reconnect; messages buffered briefly in
     `sess.sseCh` (capacity gated by `SKY_LIVE_SSE_BUFFER`)
   - Cross-process tiers (1-4): broker buffers per-subscriber
     queues; on reconnect the subscriber rejoins + drains the
     queue. Redis Pub/Sub does NOT buffer (ephemeral) — small
     window of message loss across reconnect; Cloud Pub/Sub +
     Redis Streams + NATS JetStream all buffer durably

User-visible impact during reconnect: the connection-status banner
flashes "reconnecting…" for the SSE handshake (~50-500ms typical).
No polling, no message loss when using durable brokers.

### 11.2.7 Push at every layer (no polling anywhere)

Recap of the no-polling guarantee for downstream readers:

| Layer | Direction | Mechanism |
|---|---|---|
| Browser ← Sky.Live | Push | SSE (existing) — server-sent events; browser receives without polling |
| Sky.Live instance ← Broker | Push | Long-lived subscription. Redis PSubscribe blocks on the connection; Cloud Pub/Sub streaming pull holds the connection open; both are push from the broker's perspective |
| Sky.Live publish → Broker | Synchronous request | Single PUBLISH on the existing broker connection; no polling involved |

The previous polling-based approach (`Time.every 2000 RefreshTick`)
is **completely replaced** in apps that adopt pub/sub. The
migration path (§10) walks through this for sky-diagram and the
chatroom example.

### 11.2.8 Security defaults

| Concern | v0.15.x | v0.16+ broker tiers |
|---|---|---|
| Transport | N/A (in-process) | TLS to broker (Redis 6+ TLS, Cloud Pub/Sub HTTPS-only) |
| Broker auth | N/A | Redis AUTH/ACL, Cloud Pub/Sub IAM |
| Topic namespacing by tenant | App responsibility (prefix topics manually) | Recommended pattern: `<tenant>:<app>:<topic>` — sky.toml `[live.broker] prefix = "<tenant>"` does the first segment automatically |
| Server-side topic-access guard | Extend existing `guard : Msg → Model → Result Error ()` to a `subscribeGuard : Topic → Model → Result Error ()` so apps gate subscription from Std.Auth context. **Implementation deferred to P47 or later — call out in plan.** |
| Cross-tenant accidental subscription | Prevented if both broker-prefix + app-level topic-namespacing are used | Audited in P49's example |

### 11.3 Things that DON'T need a decision

- `liveStore.Subscribe` belongs on the SessionStore interface even
  though the v0.15.x implementation delegates to a shared
  `app.topics` registry. (Future-proofs for cross-process backends.)
- Msg-shape over total-replacement (decided in §3.3 + §3.4).
- Same SSE channel for broadcast frames (decided in §5.1).
- No topic-level ACL in v0.15.x (decided in §1.2).
- No retained messages (decided in §1.2).

---

## 12. Appendix — File touch surface (informational; P46-P49 will refine)

| File | Touched by | Approx LoC |
|---|---|---|
| `runtime-go/rt/live_store.go` | P46 | +80 (interface methods + default impl) |
| `runtime-go/rt/live.go` (topic registry, app.topics, subscribe goroutine, Cmd_publish kernel) | P46+P48 | +200 |
| `runtime-go/rt/live.go` (seq split: outSeq → localSeq rename + app.globalSeq) | P47 | +30 mechanical |
| `runtime-go/rt/live.go` (embedded JS — store + ack both seqs) | P47 | +15 |
| `sky-stdlib/Std/Sub.sky` | P48 | +5 (`subscribeTopic`) |
| `sky-stdlib/Std/Cmd.sky` | P48 | +5 (`publish`) |
| `runtime-go/rt/live_store_subscribe_test.go` | P46 | +200 |
| `runtime-go/rt/live_store_subscribe_memory_bound_test.go` | P46 | +100 |
| `runtime-go/rt/live_seq_split_test.go` | P47 | +150 |
| `runtime-go/rt/live_broadcast_handler_rebuild_test.go` | P48 | +150 |
| `runtime-go/rt/live_broadcast_divergence_test.go` | P49 | +200 |
| `examples/27-multi-session-chat/` (NEW example, full Sky source) | P49 | +300 |
| `scripts/verify-all-web.sh` or new `verify-pubsub-multitab.sh` | P49 | +80 |
| `docs/skylive/pubsub.md` (user-facing tutorial) | P49 | +400 |
| `docs/skylive/overview.md` | P49 | +50 (new section) |
| `templates/CLAUDE.md` | P49 | +30 (API reference for AI-written Sky code) |
| `runtime-go/rt/live_pubsub_bench_test.go` (latency probe) | P48 or P49 | +100 |

**Total: ~2,000 LoC across runtime, stdlib, tests, example, docs.**

---

## 13. Sign-off checklist

Before P46 implementation starts, this doc must have:

- [ ] Human reviewer thumbs-up on §11.2 Q1-Q4.
- [ ] Cross-reference from issue #259 (Phase 3g).
- [ ] Architectural decision log entry in CYCLE_LOG (already done by
      the P45 Planner closeout).
- [ ] No new gaps surfaced in §3-§7 that require runtime architecture
      changes (i.e. P46 doesn't discover a missing prereq).

Revisions to this doc after P46 lands are expected and welcome —
implementation reveals constraints. Each revision should land as a
new commit on the design doc with a short rationale; sub-section
versions are NOT needed.

---

*End of design doc — P45 deliverable.*
