# Sky.Live pub/sub

> **What you'll learn.** How to push real-time updates between
> Sky.Live sessions without polling. The two primitives — `Cmd.publish`
> + `Sub.subscribeTopic` — and the patterns that keep the
> implementation honest under crashes, network blips, and multi-tab
> editing.
>
> **Reading order.** This page is the user-facing tutorial. The full
> architecture write-up lives in
> [`pubsub-design.md`](pubsub-design.md) — read that when you need to
> reason about wire shapes, cross-process broker tiers, or the seq
> split between local + global event ordering. The working example is
> [`examples/27-multi-session-chat`](../../examples/27-multi-session-chat/).

## What pub/sub gives you

Sky.Live is server-driven: the model lives on the server, the
browser runs a tiny JS shim, and the runtime ships DOM patches over
SSE. Pub/sub adds a **server-side broadcast channel** that bypasses
the user's tab — when session A publishes to a topic, every session
subscribed to that topic receives the payload as a Msg through its
own `update` reducer.

Concretely, this closes the loop in apps where one user's action
should appear in another user's open tab:

- A chatroom: alice posts; bob's tab gets the message immediately.
- A collaborative document: alice moves a shape; bob's diagram view
  re-renders.
- A live dashboard: a background job publishes new data; every
  watching tab updates without re-polling.

In v0.15.x delivery is **in-process** — sub-100 μs at the broker
layer, 10–100 ms end-to-end including SSE flush. Cross-process
broker tiers (Redis / Cloud Pub/Sub / NATS / Postgres LISTEN/NOTIFY)
land in v0.16+; see [Cross-process delivery](#cross-process-delivery).

## When to use Pub/Sub vs Tick (`Time.every`)

| Concern | Pub/Sub (`Cmd.publish` + `Sub.subscribeTopic`) | Tick (`Time.every`) |
|---|---|---|
| **Source of change** | An action somewhere in the SAME Sky.Live app (this process) | External — a clock, a service that doesn't push, a periodic refresh |
| **Latency** | ~10–100 ms (intra-process channel hop + SSE flush) | Defined by tick interval (typically 1–10 s) |
| **Bandwidth at idle** | Zero — no traffic until someone publishes | Constant — every interval ships a frame even if nothing changed |
| **Composition** | `Sub.batch` mixes tick + topic in the same `subscriptions` | — |
| **Best for** | Live collaboration (chat, diagrams, dashboards) | Animation, polling external state, clock displays, watchdog ticks |
| **Worst for** | External state — a counter changing on another service won't publish here | Multi-session collaboration — every user pays the polling cost |

The decision rule: **DB writes in your own app → publish; external
state → tick**. Both primitives stay first-class in Sky.Live; pub/sub
doesn't deprecate polling for use cases that genuinely need it.

## API

Three primitives — one per side of the TEA loop, plus a Task-shaped
escape hatch for non-Live contexts, each with an echo-by-default form
and a `publishNoEcho` variant that opts the publisher's own session
out of delivery:

```elm
-- Std.Cmd            (update-return tuple side)
publish       : String -> any -> Cmd msg
publishNoEcho : String -> any -> Cmd msg
-- Std.Sub            (subscriptions side)
subscribeTopic : String -> (any -> msg) -> Sub msg
-- Std.PubSub         (Task-shaped — any goroutine)
publish       : String -> any -> Task Error Int
publishNoEcho : String -> any -> Task Error Int
```

`Cmd.publish` and `Std.PubSub.publish` route to the same in-process
broker — a subscriber sees the same `SessionEvent` regardless of
which side fired it. They differ only in where they can be CALLED
from:

- `Cmd.publish` / `Cmd.publishNoEcho` live inside an
  `update msg model -> (Model, Cmd Msg)` return. Use them for "user
  clicked → broadcast" or "another Msg triggered a broadcast".
- `Std.PubSub.publish` / `Std.PubSub.publishNoEcho` return
  `Task Error Int` runnable from ANY goroutine — raw
  `Sky.Http.Server` `api` handlers, post-init bootstrap, scheduled
  jobs, callbacks from external systems (webhooks). The `Int` is the
  broker's delivery count. Errors with `Unavailable` when no
  `Live.app` is running in this process (CLI tools, isolated unit
  tests, pure HTTP servers).

### `Cmd.publish topic payload`

Broadcast `payload` to every Sky.Live session in THIS PROCESS that
has declared `Sub.subscribeTopic topic` in its `subscriptions`. Fire-
and-forget: there is no result feedback to the publisher; if the
broker has zero subscribers, the call is a no-op.

The publisher's OWN subscription (if any) ALSO receives the payload —
**echo-by-default** matches Redis / NATS / MQTT semantics. App code
can self-skip on origin if needed (see [Echo and origin tracking](#echo-and-origin-tracking)).

```elm
update msg model =
    case msg of
        SendChat text ->
            ( { model | draft = "" }
            , Cmd.publish
                ("chat:room-" ++ model.room)
                (encodeChatMessage { author = model.me, text = text })
            )
```

The payload is opaque — `any` — so you can ship any value the runtime
can carry. In v0.15.x, in-process delivery preserves the original Go
value as-is; for cross-process tiers in v0.16+, payloads are JSON-
encoded between processes. **Dict-shaped payloads (`Dict.fromList […]`)
are the safest portable choice.**

### `Sub.subscribeTopic topic toMsg`

Receive every payload broadcast to `topic`, decoded into a Msg via
`toMsg : any -> msg`, and dispatched to `update` like any other Msg.

```elm
subscriptions model =
    case model.page of
        ChatPage room ->
            Sub.subscribeTopic
                ("chat:room-" ++ room)
                MessageReceived
        _ ->
            Sub.none

update msg model =
    case msg of
        MessageReceived payload ->
            let
                chatMsg = decodeChatMessage payload
            in
                ( { model | history = model.history ++ [chatMsg] }
                , Cmd.none
                )
```

Topics are exact-match strings (no wildcards in v0.15.x). Compose
per-room / per-user topics by string concatenation.

**Sub.batch composes pub/sub with everything else** — mix
`Sub.subscribeTopic` with `Sub.every`, `Sub.none`, or other
subscriptions in the same `subscriptions` evaluation:

```elm
subscriptions model =
    Sub.batch
        [ Sub.subscribeTopic ("chat:room-" ++ model.room) MessageReceived
        , Sub.subscribeTopic ("presence:" ++ model.room) PresenceChanged
        , Sub.every 30000 KeepAliveTick
        ]
```

The runtime diff-updates subscriptions on every dispatch: topics in
the intersection of the old + new sets keep their existing goroutine
+ broker registration (no broadcast loss in the gap); only added
topics open new subscriptions and only removed topics cancel.

### `Std.PubSub.publish topic payload` — Task-shaped

Same broadcast semantics as `Cmd.publish`, but returns a
`Task Error Int` you can run from contexts that don't have a `Cmd msg`
channel:

- Raw `Sky.Http.Server` `api` handlers (webhooks, callbacks from
  external systems, internal admin endpoints).
- Post-init bootstrap (e.g. seed-data load that needs to notify
  warming subscribers).
- Scheduled jobs (cron handlers, queue workers).
- Anything else running on a goroutine that's not the Sky.Live
  update loop.

The Task resolves with the count of subscribers that received the
broadcast (the same `int` `Cmd.publish` gets in its internal path).
Errors with `Unavailable` when no `Live.app` is registered in this
process.

```elm
import Std.PubSub as PubSub

-- A GitHub webhook lands as a raw api handler — no update tuple
-- in scope, but we want every dashboard tab watching this repo's
-- deploys to learn instantly.
handleGithubWebhook : Dict String any -> Response
handleGithubWebhook req =
    case decodeWebhook req of
        Err _ ->
            plain 400 "bad webhook payload"

        Ok ev ->
            let
                -- 1) durable write first (matches the same pattern
                --    described below for Cmd.publish — the topic is
                --    a notification, the DB row is the source of
                --    truth).
                _ = Store.recordWebhook ev
                -- 2) push the notification — Task.run forces the
                --    Task at the handler boundary.
                _ = Task.run
                        (PubSub.publish
                            ("deploy:status:" ++ ev.appSlug)
                            (encodeDeployEvent ev))
            in
                plain 200 "ok"
```

Origin is the empty string on the broker side — server-side publishes
have no originating session and therefore no echo-suppression target.
Subscribers that filter on `Origin == ownSid` see these as foreign and
will receive them normally.

The same in-process scope rules as `Cmd.publish` apply (v0.15.x:
in-process only; v0.16+ cross-process tiers cover both APIs uniformly).

## The durability pattern: write to DB FIRST, publish SECOND

This is the most important pattern when publishing data that matters.
**Always persist before you publish:**

```elm
SendMessage text ->
    let
        chatMsg = { author = model.me, text = text, at = nowString () }
    in
        ( model
        , Cmd.batch
            [ Cmd.perform                                  -- 1. DB write
                  (persistMessage model.room chatMsg)
                  PersistResult
            , Cmd.publish                                  -- 2. broadcast
                  ("chat:room-" ++ model.room)
                  (chatMessageToDict chatMsg)
            ]
        )
```

The two reasons:

1. **Notification loss is acceptable; data loss is not.** A process
   crash, network blip, or a subscriber that disconnected milliseconds
   before the publish can all silently drop a broadcast. If the DB
   write hadn't happened first, that message is gone forever — even
   from the publisher's own history when they next refresh the page.

2. **The DB row is the source of truth for the room's history.** Late
   joiners (`subscriptions` running for a session that just opened
   `/chat/<room>`) load history from the DB via `loadRoomHistory` in
   `init`. Subscribers who connect AFTER a broadcast fires don't see
   it via pub/sub — but they DO see it via the persisted history.

The pattern in `examples/27-multi-session-chat` shows this verbatim
in the `SendMessage` handler.

## Echo and origin tracking

Echo-to-publisher is **on by default**. When session A publishes to
"foo" and is ALSO subscribed to "foo", session A's own subscription
will fire. This matches Redis / NATS / MQTT semantics and gives every
session a single uniform path to apply broadcasts — A's tab sees its
own message arrive through the same `MessageReceived` Msg as B's tab
does, so there's no separate "I just sent this" code path to
maintain.

If a particular app needs to suppress self-echo, the broadcast carries
an `Origin` field on the wire that subscribers can match against their
own sid. App-level suppression is the responsibility of the
subscriber, not the publisher — this keeps the broker contract
universal.

### When to use echo vs no-echo

`Cmd.publishNoEcho` (and `PubSub.publishNoEcho`) flips one bit:
`SessionEvent.SkipOrigin = true`. The broker then skips delivery to
any subscriber whose ownSid matches the publisher's sid — every other
subscriber receives the event exactly as it would under the default
`publish`.

| Pattern | Use |
|---|---|
| Universal Msg handler — "A's tab and B's tab both apply the broadcast the same way through `MessageReceived`" | `publish` (echo-by-default) |
| Instant feedback for publisher — "user clicked send, I've already appended the message to their model locally; just tell the OTHER tabs" | `publishNoEcho` |

The savings:

- **One broker round-trip per publish.** In v0.15.x in-process
  broker, that's a channel push + a dispatch goroutine wakeup — sub-
  microsecond, but eliminated entirely.
- **Network latency in v0.16+ broker tiers.** Redis Pub/Sub, Cloud
  Pub/Sub, NATS, and Postgres LISTEN/NOTIFY all route the publisher's
  own echo back through the network. That's 10-100ms+ depending on
  tier — visible UX latency on every keystroke / mouse click that
  triggers a broadcast.

The "instant feedback" pattern with `publishNoEcho`:

```elm
update msg model =
    case msg of
        SendChat text ->
            let
                chatMsg = { author = model.me, text = text, at = nowString () }
                -- 1) Update the publisher's OWN model directly.
                model_ =
                    { model | history = model.history ++ [chatMsg], draft = "" }
            in
                ( model_
                , Cmd.batch
                    [ -- 2) Durable write — pattern from "write to DB first".
                      Cmd.perform (persistMessage model.room chatMsg) PersistResult
                    , -- 3) Broadcast to OTHER sessions only — broker suppresses
                      --    delivery back to this session.
                      Cmd.publishNoEcho
                        ("chat:room-" ++ model.room)
                        (chatMessageToDict chatMsg)
                    ]
                )

        MessageReceived payload ->
            -- Only fires for messages published by OTHER sessions —
            -- the publisher's own message lands via the direct model
            -- update above.
            let
                chatMsg = decodeChatMessage payload
            in
                ( { model | history = model.history ++ [chatMsg] }
                , Cmd.none
                )
```

`Cmd.publishNoEcho` is the right default for "I'm the source of
truth for my own state" patterns. `Cmd.publish` remains the right
default when the publisher's own dispatch path naturally consumes
the broadcast — for example, a kanban board where every tab applies
the same `CardMoved` Msg through `MessageReceived` whether they
originated it or not.

## Cross-process delivery (v0.16+)

In v0.15.x, pub/sub is **in-process only.** A single Sky.Live
instance — sessions on different instances do NOT see each other's
publishes. This is correct for:

- Single-instance Cloud Run apps (autoscaler at 1)
- Self-hosted single VPS deployments
- Local dev

For multi-instance deployments (autoscaling Cloud Run, multi-pod
Kubernetes, blue/green with concurrent traffic on both versions), the
v0.16+ broker tiers will plug into the existing `Sub.subscribeTopic`
+ `Cmd.publish` calls WITHOUT a Sky source change. The
[`liveStore.Subscribe`](pubsub-design.md#32-storesubscribe-interface)
interface in v0.15.x is precisely the seam for that swap.

Planned tiers (see [design doc §11.2.5](pubsub-design.md#1125-cross-process-broker-tiers-cloud-run-scaling)):

| Tier | Tech | When |
|---|---|---|
| 0 (v0.15.x default) | In-process Go channels | Single-instance Cloud Run; dev |
| 1 (v0.16 priority) | Redis Pub/Sub | Multi-instance Cloud Run; sub-ms VPC latency |
| 2 (v0.16+) | Google Cloud Pub/Sub | GCP-native stacks; IAM-authenticated; replay |
| 3 (v0.16+) | PostgreSQL LISTEN/NOTIFY | Already-on-Postgres apps; zero extra infra |
| 4 (deferred) | NATS JetStream | High-throughput apps that outgrow Redis |

Each tier is selected via `sky.toml`:

```toml
[live.broker]
kind   = "redis"             # in-process (default) | redis | gcp-pubsub | pg-notify
url    = "$REDIS_URL"
prefix = "myapp"             # optional topic namespace (multi-tenant)
```

Apps that already use `Cmd.publish` / `Sub.subscribeTopic` need ZERO
source changes when switching tiers.

## Migration: replacing `Time.every` polling with pub/sub

The pattern in the assignment brief (and `examples/16-skychess`'s
opponent-move refresh) is a `Time.every` poller that hits the DB
every N seconds. Pub/sub replaces this with push delivery + a
matching publish on every mutation.

### Before — polling

```elm
subscriptions model =
    if model.gameInProgress then
        Sub.every 10000 RefreshTick
    else
        Sub.none


update msg model =
    case msg of
        RefreshTick _ ->
            ( model, Cmd.perform (loadOpponentMoves model.gameId) MovesLoaded )

        MovesLoaded (Ok moves) ->
            ( { model | board = applyMoves moves model.board }, Cmd.none )

        SubmitMove move ->
            ( { model | board = applyMove move model.board }
            , Cmd.perform (saveMove model.gameId move) MoveSaved
            )
```

Every player's tab fires a DB read every 10 s regardless of activity.
The latency to "I see my opponent moved" is bounded by the tick
interval — up to 10 s.

### After — pub/sub

```elm
subscriptions model =
    if model.gameInProgress then
        Sub.subscribeTopic ("game-" ++ model.gameId) MoveReceived
    else
        Sub.none


update msg model =
    case msg of
        MoveReceived payload ->
            case decodeMove payload of
                Ok move -> ( { model | board = applyMove move model.board }, Cmd.none )
                Err _ -> ( model, Cmd.none )

        SubmitMove move ->
            ( { model | board = applyMove move model.board }
            , Cmd.batch
                [ Cmd.perform (saveMove model.gameId move) MoveSaved
                , Cmd.publish ("game-" ++ model.gameId) (encodeMove move)
                ]
            )
```

**Result.** Latency drops from 10 s to ~10 ms. DB read traffic at
idle drops to zero. The DB save still happens (durability — see
[the pattern](#the-durability-pattern-write-to-db-first-publish-second)),
the broadcast is the low-latency hint.

A complete worked example lives in
[`examples/27-multi-session-chat`](../../examples/27-multi-session-chat/) —
clone it, open two browser tabs at `/chat/lounge`, type in one tab,
watch it appear in the other within ~100 ms.

## Reference

- [Architecture write-up (`pubsub-design.md`)](pubsub-design.md) —
  wire shape, runtime, lifecycle, broker tiers, full design rationale.
- [`examples/27-multi-session-chat`](../../examples/27-multi-session-chat/) —
  end-to-end worked example: chatroom with SQLite persistence + pub/sub
  broadcast, ~250 lines of Sky.
- [`scripts/verify-pubsub-multitab.sh`](../../scripts/verify-pubsub-multitab.sh) —
  Playwright probe driving two browser tabs through the same room,
  asserts sub-500 ms delivery in both directions plus echo.
- API source: [`sky-stdlib/Std/Cmd.sky`](../../sky-stdlib/Std/Cmd.sky),
  [`sky-stdlib/Std/Sub.sky`](../../sky-stdlib/Std/Sub.sky).
