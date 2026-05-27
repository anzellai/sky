# Sky.Live architecture

> **v0.15 state**: type-directed lowering across callback fields,
> record-field inits, list elements, and call args; Go generics on
> parametric record aliases. Whole-program Sky DCE prunes unused FFI
> bindings (Stripe-SDK scale: −82 % source). LSP 100 % coverage;
> runtime verification across all 27 examples. See
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
