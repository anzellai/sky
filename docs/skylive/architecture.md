# Sky.Live architecture

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


Technical reference for how Sky.Live dispatches events, renders, and diffs. For user-facing usage see [overview.md](overview.md).

## Process flow

```
┌─────────────────┐         ┌───────────────────┐
│  browser        │         │  sky-live server  │
│                 │         │                   │
│  1. GET /       │────────▶│  initial render   │
│  ◀────HTML──────│         │  view model → dom │
│                 │         │                   │
│  2. open SSE    │         │                   │
│  ──EventSrc───▶ │─session │  session store    │
│                 │ created │  (mem/sqlite/...) │
│                 │         │                   │
│  3. click       │         │                   │
│  fetch /_sky/   │────────▶│  dispatch msg     │
│    event        │         │  update msg model │
│                 │         │                   │
│                 │         │  diff(vOld, vNew) │
│  4. patch       │◀────SSE─│  serialised patch │
│  apply to DOM   │         │                   │
│                 │         │                   │
│  5. cmd result  │◀────SSE─│  goroutine → msg  │
└─────────────────┘         └───────────────────┘
```

## Session lifecycle

1. **Page load** — server renders `init ()`. The resulting model + view are cached under a session id (cookie or query param). A `session_id` cookie is set with `HttpOnly; SameSite=Lax` (the CSRF cookie is separately `SameSite=Strict`).
2. **SSE open** — client connects to `/_sky/sse?session=<id>`. Server locks the session and emits a `hello` event.
3. **Event post** — client sends `POST /_sky/event` with `{ session, msg }`. Server decodes `msg`, locks the session, runs `update`, diffs, emits patch over SSE.
4. **Cmd dispatch** — if `update` returned a non-none `cmd`, server spawns a goroutine per command. Each goroutine holds the session lock only to apply the resulting `Msg`, not while the task runs — so long-running HTTP requests don't block other events.
5. **TTL expiry** — sessions expire after `[live] ttl` seconds of inactivity. The store sweeps expired rows periodically.

## Runtime location

All the plumbing lives in `runtime-go/rt/live.go` (HTTP handlers, VNode diff, SSE encoding) and `runtime-go/rt/live_store.go` (session backends). These are embedded into every project's binary.

The Sky-facing `Std.Live` module exposes `app` + `route`; subscriptions / commands live in their own modules (`Std.Sub.{none,every}`, `Std.Cmd.{none,perform,batch}`); HTML primitives are in `Std.Html` / `Std.Html.Attributes` / `Std.Html.Events`; Std.Ui sits on top of those.

## VNode shape

The view returns a tree of `vnode` values:

```go
type vnode struct {
    kind     string            // "elem" | "text"
    tag      string            // div, span, ...
    attrs    map[string]string
    events   map[string]string // "click" -> msg-serial
    children []vnode
    text     string            // for kind="text"
    key      string            // for keyed diff
}
```

Sky-side `Html.div [ Attr.class "x" ] [ Html.text "hi" ]` produces a `vnode` literal.

## Diff algorithm

`diff(oldNode, newNode)` is recursive:

- Same tag + same attrs → recurse into children.
- Different tag → emit a `replace` patch.
- Different attrs → emit `attr-set` / `attr-del`.
- Keyed children → LCS-style reordering via `key` attribute.
- Non-keyed children → positional.

Patches are encoded as JSON and streamed over SSE.

## SSE transport: `event: patches` vs `event: patch`

(Cycle 3 P50 / Gap C11 — landed in v0.15.x hardening.)

The SSE channel carries TWO event types, chosen per render by the
server-side `chooseSSEFrame` helper:

| Event | Envelope shape | Used when |
|---|---|---|
| `event: patches` | `{seq, ackInputs, patches: [...]}` (mirrors `writeEventJSON`'s HTTP reply) | A structural diff between the previous tree and the just-rendered tree fits in a small patch list. Typical 200-1000 B per frame. |
| `event: patch` | `{seq, ackInputs, body: "<html>..."}` (legacy full-body shape) | First render after session creation (no previous tree to diff against); reconnect-resync (server has the model but the client may have lost DOM state); the diff degenerated to a single root-level `innerHTML` replace (`patchesAreFullReplace`). Typical 5-50 KB per frame. |

The client routes via two `addEventListener` calls on the same
`EventSource`:

```js
__skySSE.addEventListener("patches", function(e) {
  var frame = JSON.parse(e.data);
  __skyHandleResponse(frame.seq, frame.ackInputs, function() {
    __skyApplyPatches(frame.patches);
  });
});
__skySSE.addEventListener("patch", function(e) {
  // legacy full-body shape — __skyPatch() driven by frame.body
});
```

Both consumers route through `__skyHandleResponse` for the same
monotonic seq guard the HTTP path uses, so out-of-order frames
(e.g. a stale patches frame arriving after a fresher patch frame
across a brief network blip) are dropped at the same point.

**Input-authority preservation on the SSE path.** SSE producers
pass `nil` as `clientState` to `diffTrees` — server-driven renders
(Cmd.perform completion, Time.every tick) carry no fresh client
inputState. The client-side `__skyApplyPatches` filter
(`__skyIsDirty(el)`) drops `value`/`checked`/`selected` attrs on
dirty inputs, so in-flight typing is preserved without server-side
alignment. See [input-authority-protocol.md](input-authority-protocol.md).

**Backwards compatibility.** A pre-P50b client (no `patches`
listener) is unaffected: `EventSource` silently no-ops events
without a registered listener, and the producer's fallback path
(first-render / full-replace) still uses `event: patch` so the
client receives a full-body frame for those cases. The producer
NEVER ships `event: patches` to a session that hasn't yet seen
a prev tree.

## Per-session fan-out — every tab of one session mirrors one shared view

A session (`sky_sid` cookie) holds ONE server-side Model; multiple tabs of the
same browser share the cookie, so they share that Model. As of v0.18 the tabs
of a session **mirror one shared view**: they always show the same page AND the
same state. Every committed frame — an action's patch, a server push, AND a
navigation — is fanned out to **all** live connections of the session.

**This is a deliberate semantic, and it is what makes the fan-out sound.**
Because every tab is kept at the shared `sess.prevTree`, a broadcast diff always
targets a DOM that matches its baseline. If navigation did NOT mirror, one tab
could drift onto a different page than the shared Model, and a later action's
diff (computed against the shared page) would target `sky-id`s that don't exist
in the stale tab's DOM — silent corruption. Mirroring navigation closes that.
The consequence to know: navigating one tab (or opening a new tab at a URL)
moves ALL tabs of that session — they are one logical window. (Two people who
must browse independently are two different sessions, not two tabs; see
*Same-user, different sessions* under Horizontal scale.)

- **Ingress + relay.** `sess.sseCh` is the session's single ingress channel
  every producer (Cmd.perform completion, Time.every tick, pub/sub delivery,
  the tab-unload batch, the WebSocket bridge) writes to. A per-session relay
  goroutine — started once by the first `handleSSE`, exits when the session's
  `done` closes — drains `sseCh` and broadcasts each frame to every registered
  connection. Before this, a single shared channel handed each pushed frame to
  ONE random connection, so a second tab never saw server pushes.
- **Per-connection channels.** Each `handleSSE` registers a private buffered
  channel (capacity `SKY_LIVE_SSE_BUFFER`) keyed by a connection id + the
  client's per-page `tab` id (the `?tab=` query param). Delivery is
  non-blocking per connection: a full buffer drops that frame for that
  connection only (counted via `sky_live_sse_drops_total`; it recovers on its
  next reconnect-resync) without stalling the relay or the other tabs.
- **Dispatch mirror.** A `POST /_sky/event` still replies with the acting
  tab's patch on its HTTP response for latency; it ALSO mirrors the frame
  (same `seq`, `clientState = nil`) to the OTHER tabs so they reflect the
  shared model. The originating tab is excluded by its `tab` id (and the
  client seq guard would drop the duplicate regardless), so the common
  single-tab dispatch runs no extra diff/marshal.
- **Navigation mirror.** A `sky-nav` fetch / popstate / initial load is a
  `GET` served by `handleInitial`, which mutates the shared Model's page. It
  fans a **full-body** frame to the OTHER tabs (a page change is structural,
  so a full swap converges any tab regardless of its prior page — no
  diff-baseline dependency). The requesting tab is excluded via the
  `X-Sky-Tab` request header the client sends on nav fetches; a bare browser
  load carries no header and has no SSE yet, so it is naturally excluded.
  Gated on a sibling tab being connected, so a lone tab / first load pays
  nothing.
- **Who-wins is unchanged.** The per-session mutex still serializes dispatches
  (serialized last-writer-wins, no lost update). Fan-out only makes every
  connection *see* the resolved state, so all tabs converge. In-flight typing
  in an observer tab is preserved by the client's `__skyIsDirty` authority —
  mirrored frames carry `clientState = nil`, identical to a Cmd/tick push.
- **Ordering.** Every frame carries the session's monotonic `seq`; the relay
  preserves channel FIFO, and the client drops any frame with `seq ≤` the last
  it applied. A newly-connected tab receives a full-body reconnect-resync at
  the current state, then only later frames — so a late joiner never applies a
  stale diff.

Zero config, zero app-code change: default-on at every store tier. Horizontal
scale across instances (a shared store + a cross-process broker so fan-out
crosses instances, and same-user cross-session sync) is the follow-on work; the
`Broker` interface is already the seam for it.

## SSE connection lifecycle + scaling

Each loaded page opens exactly ONE `EventSource` to `/_sky/sse` and holds it
open for pushed frames. A streaming SSE connection consumes one of the
browser's ~6-connections-per-host HTTP/1.1 budget, so the connection lifecycle
is managed on both ends:

**Client — one connection, released on navigation.**
- `__skyOpenSSE` is idempotent: it closes any existing `__skySSE` before
  opening a new one, so a reconnect race can never orphan a live stream.
- A `pagehide` handler closes the `EventSource` the instant the page navigates
  away, freeing the slot before the next page opens its own. `pageshow`
  (bfcache restore) reopens it. Without this, an app that navigates via
  **full-page loads** (plain `<a href>` links — a fresh SSE per page) overlaps
  the closing stream with the next page's new one; rapid clicking piles them up
  until the 6-connection limit is hit and the tab freezes (spinner stuck, all
  clicks no-op).

**Server — prompt cleanup, no per-session supersede.** `handleSSE` returns as
soon as `r.Context().Done()` fires (the client's TCP connection closed), so a
navigated-away or closed tab frees its goroutine + connection immediately. The
server does NOT try to bound connections to one-per-session: two live tabs
share a session (same cookie), and EventSource auto-reconnects when a 200
stream ends — so closing one same-session connection just makes the tabs
ping-pong reconnecting. Per-tab bounding belongs on the client (idempotent
open + release-on-`pagehide`, above); server-side scale is Go's cheap
goroutine-per-connection model + prompt disconnect cleanup. At N concurrent
tabs the server holds ~N SSE connections — Go handles this well; raise the
file-descriptor limit (`ulimit -n`) for large N, and terminate over HTTP/2
(below) so the browser side isn't the bottleneck.

**For multi-page apps, prefer `sky-nav` over full-page links.** A `sky-nav`
link keeps ONE persistent SSE for the whole session and swaps the body via a
client-side patch, instead of tearing down + reopening an SSE on every page.
Fewer connections, no per-navigation reconnect/resync, and no exposure to the
per-host limit at all. Reach for plain `<a href>` (full-page) only when you
genuinely want a fresh document.

**In production, terminate over HTTP/2.** HTTP/2 multiplexes many streams over
one TCP connection, so SSE no longer consumes a scarce per-host slot and the
6-connection limit stops applying — the robust answer for high-navigation or
many-tab usage. A TLS front (Cloud Run, nginx, Caddy) gives you this for free.

## Horizontal scale — many instances (Phase 2)

Sky.Live scales to N app instances behind a load balancer with two rules,
one about session ownership and one about broadcast fan-out.

### Sessions are single-owner — route sticky by cookie (load-bearing)

A session (`sky_sid`) holds ONE authoritative Model, mutated under ONE
per-session mutex that serializes dispatches (serialized last-writer-wins,
no lost update). That guarantee only holds while the session lives on ONE
instance at a time. **The load balancer MUST route by session affinity —
the `sky_sid` cookie is the affinity key.** This is the same model as
Phoenix LiveView (a LiveView process lives on one node) or Rails
ActionCable; it is the correct architecture for server-held session state,
not a limitation to engineer around.

- All TABS of one session share the cookie, so affinity keeps them on one
  instance → the Phase 1 per-session fan-out (in-process) reaches them all
  and the mutex serializes their dispatches. Correct with zero
  cross-instance machinery.
- If a session MOVES instances (instance dies, deploy, LB reshuffle): the
  new instance loads the current Model from the shared session store
  (every dispatch does `store.Set`, so the store is always current), the
  browser's `EventSource` reconnects and lands on the new instance via the
  cookie, and the reconnect-resync repaints at the current state. No
  distributed lock needed — because only one instance owns the session at
  a time.
- Without affinity, two instances would each load + mutate the same
  session's Model independently → lost updates on the store and split
  in-process fan-out. Cross-instance frame fan-out would NOT fix this (the
  Model is still split); single ownership is the only sound fix. SkyDeploy
  sets affinity automatically; on your own LB, enable sticky sessions
  keyed on the session cookie.

### Cross-instance pub/sub — the Redis broker

`Cmd.publish` / `Std.PubSub.publish` / `Sub.subscribeTopic` fan out through
a `Broker`. Single-instance uses the in-process registry. Multi-instance
uses the **cross-instance Redis broker**, which is selected automatically
when the session store is Redis (`runtime-go/rt/live_redis_broker.go`):

- **Publish**: re-stamp the event's `globalSeq` from THIS instance's
  counter, deliver to local subscribers, then `PUBLISH` the gob-encoded
  event to `sky:live:topic:<topic>` tagged with this instance's id.
- **Receive** (one loop per instance): read every subscribed channel,
  DROP messages tagged with our own instance id (already delivered
  locally — no double delivery), re-stamp `globalSeq` from this instance's
  counter, and deliver locally.
- **Per-topic subscribe**: the Redis channel is subscribed on the 0→1
  local-subscriber transition and unsubscribed on 1→0, so an instance
  only receives traffic for topics it actually has subscribers for.

**Why `globalSeq` is re-stamped per instance, not shared.** The browser
dedupes broadcast frames with a monotonic watermark (drop `globalSeq ≤`
last applied). That only has to be monotonic per subscriber STREAM — i.e.
per instance. Re-stamping every locally-delivered event (local- AND
remote-origin) from one per-instance counter keeps each stream monotonic
with no cross-instance sequencer, no Redis `INCR` on the hot path, and no
global bottleneck. Ordering stays best-effort exactly as the in-process
broker already is — a rarely-reordered broadcast is superseded by the next
one.

**Payloads** cross the wire via the same gob machinery the DB stores use
for the Model, plus eager registration of the common typed-`Dict`/`List`
shapes so a `Dict String String` payload round-trips on every instance
from startup. A payload that can't be gob-encoded degrades to LOCAL-only
delivery with a logged-once warning — never a panic.

**Graceful degradation.** A Redis `PUBLISH`/`SUBSCRIBE` error never breaks
local delivery; the cross-instance hop is logged-once and skipped. Since
the session store is Redis too in this tier, a Redis outage takes the
whole deployment down regardless — so "Redis down → cross-instance
fan-out pauses" is consistent with the rest of the tier.

### Configuration

| Env | Effect |
|---|---|
| `SKY_LIVE_STORE=redis` + `SKY_LIVE_STORE_PATH=<url>` | Shared session store AND (by default) the cross-instance broker. The scalable-by-default path: deploy multi-instance ⇒ sessions must be shared ⇒ pub/sub crosses instances with no extra config. |
| `SKY_LIVE_BROKER_URL=<redis-url>` | Run a Redis broker even when sessions are NOT on Redis (e.g. Postgres sessions + Redis pub/sub). The broker is app-scoped, so the two are legitimately decoupled. |
| `SKY_LIVE_BROKER=inprocess` | Escape hatch — force the in-process registry back on a single-instance Redis deploy or when debugging. |

A native Postgres `LISTEN/NOTIFY` broker (zero-config cross-instance for
Postgres-only deploys) is the next backend; today a Postgres-store deploy
opts into cross-instance pub/sub via `SKY_LIVE_BROKER_URL`.

### Same-user, different sessions (two devices)

Two browsers signed into one account are two DIFFERENT sessions (different
`sky_sid` → different Models), possibly on different instances. They sync
by publishing to a user-scoped topic keyed on the stable auth identity
(e.g. `"user:" ++ userId`): every one of that user's sessions subscribes
to it, and — via the Redis broker — the broadcast reaches them across
instances. This is opt-in by design: different sessions may be on
different pages with different view state, so the APP decides which shared
state syncs (typically re-read the account row + re-render), rather than
blindly replicating a whole Model. Conflicts resolve at the DB
(last-writer-wins), the same as any two writers to shared rows.

## Event serialisation

Sky closures can't cross the wire. Event handlers are serialised to string tags:

```elm
onClick Increment          -- serialises as "Increment"
onInput (\s -> SetName s)  -- serialises as "SetName@<slot>"
```

The server stores a per-session event-handler table. When the client posts a tagged event, the server looks up the handler closure and applies it to the decoded payload (input value, form data, etc.).

## Session store interface

```go
type SessionStore interface {
    Get(ctx context.Context, id string) (*Session, error)
    Put(ctx context.Context, id string, s *Session) error
    Delete(ctx context.Context, id string) error
    Sweep(ctx context.Context, olderThan time.Duration) error
}
```

Implementations:

- `memSessionStore` — `sync.Map`; lost on restart.
- `sqliteSessionStore` — single-node persistence.
- `redisSessionStore` — multi-instance via shared Redis.
- `postgresSessionStore` — shared SQL backend.
- `firestoreSessionStore` — GCP serverless.

Sessions are serialised as JSON. The model itself is always `any`-boxed Sky data structures, encoded via `SkyEncode`.

## Concurrency

Each session has a `sync.Mutex`. Events and command-callback dispatches both lock the session before running `update`. The view + diff happen while the lock is still held, so the patch stream is always consistent with the dispatched messages.

Commands (`Cmd.perform`) run their `Task` outside the session lock, then re-acquire it to dispatch the result. This means long-running HTTP requests don't block other events.

## Security defaults

- Cookies: `HttpOnly`, `Secure` (when served over HTTPS); session cookie is `SameSite=Lax`, CSRF cookie is `SameSite=Strict`.
- Rate limit: per-IP + per-session token bucket; configurable via `[live]`.
- CORS: off by default. Turn on by configuring allowed origins explicitly.
- Event payload size cap: configurable via `[live] maxBodyBytes` / `SKY_LIVE_MAX_BODY_BYTES` (default `5242880` = 5 MiB; bump for `Event.onFile` / `Event.onImage` uploads). Larger payloads are rejected with HTTP 413.

## Client-side runtime

`runtime-go/rt/live_client.js` (embedded, served at `/_sky/live.js`) — about 2 KB gzipped.

Responsibilities:

1. Open SSE, reconnect with exponential backoff.
2. Apply VNode patches to the DOM.
3. Intercept form submits, clicks, input events — POST to `/_sky/event`.
4. Handle navigation (pushState / popState) when the server routes it.

No framework dependency. No bundle step.
