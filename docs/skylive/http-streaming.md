# Streaming HTTP responses (`Sky.Core.Http.Stream`)

> v0.15.x feature. Shipped Cycle 4 HS.

## What this gives you

HTTP response bodies that flow into your `update` loop **as bytes
arrive**, one chunk at a time, instead of waiting for the full reply
to land. The view re-renders progressively; the standard Sky.Live
SSE channel patches the open browser tab.

**Driving use case**: LLM streaming. With Anthropic Claude Haiku 4.5
returning ~50 tokens/s, an `Http.get` waits ~5 s for the full reply
before the user sees anything. With `Http.Stream.open`, the first
token paints in ~300 ms — perceived latency drops 5-10×.

Other fits:

- Server-Sent Events (SSE) consumers
- Long-running file downloads with progress bars
- Tail-style log streams
- Any HTTP endpoint where time-to-first-byte matters more than total
  bytes throughput.

## API

```elm
module Sky.Core.Http.Stream exposing
    ( StreamId(..)
    , ChunkEvent(..)
    , open
    , chunks
    , close
    )

type StreamId = StreamId Int

type ChunkEvent
    = Chunk String       -- raw UTF-8 bytes just arrived
    | Done               -- clean EOF
    | Errored Error      -- network / protocol error

open   : HttpRequest -> Task Error StreamId
chunks : StreamId -> (ChunkEvent -> msg) -> Sub msg
close  : StreamId -> Task Error ()
```

`HttpRequest` is the same record `Http.request` takes — method, URL,
body, headers — so any existing call site switches to streaming by
changing only the function name.

## Canonical shape

The reference example is `examples/28-streaming-chat` (a mock LLM
streaming chatroom). Boiled-down sketch:

```elm
import Sky.Core.Http.Stream as HttpStream exposing (StreamId, ChunkEvent(..))
import Std.Cmd as Cmd
import Std.Sub as Sub

type alias Model =
    { reply : String
    , activeStream : Maybe StreamId
    }

type Msg
    = SendPrompt PromptForm
    | StreamOpened (Result Error StreamId)
    | Chunked ChunkEvent

update msg model =
    case msg of
        SendPrompt form ->
            let req = { method = "POST", url = "https://api/...", body = form.prompt, headers = [] }
            in
                ( { model | reply = "", activeStream = Nothing }
                , Cmd.perform (HttpStream.open req) StreamOpened
                )

        StreamOpened (Ok sid) ->
            ( { model | activeStream = Just sid }, Cmd.none )

        StreamOpened (Err e) ->
            ( { model | activeStream = Nothing }, Cmd.none )

        Chunked (Chunk text) ->
            ( { model | reply = model.reply ++ text }, Cmd.none )

        Chunked Done ->
            ( { model | activeStream = Nothing }
            , case model.activeStream of
                Just sid -> Cmd.perform (HttpStream.close sid) (\_ -> Noop)
                Nothing -> Cmd.none
            )

        Chunked (Errored _) ->
            ( { model | activeStream = Nothing }, Cmd.none )

subscriptions model =
    case model.activeStream of
        Just sid -> HttpStream.chunks sid Chunked
        Nothing  -> Sub.none
```

### Three rules

1. **Subscribe ONLY while a stream is in-flight.** Wrap the
   `chunks` call in a `case model.activeStream of` so the next
   `subscriptions` re-eval (post-Done / post-Errored) drops the Sub
   and the drain goroutine retires. Leaving the Sub up after the
   stream finished is harmless (the goroutine has already exited),
   but signals intent more clearly when explicit.

2. **Set `activeStream = Nothing` BEFORE `Cmd.perform open`.** The
   StreamId only arrives via `StreamOpened (Ok sid)`. Until then,
   `subscriptions` should report no stream (no `chunks` Sub
   evaluating with a stale id).

3. **`close` is idempotent — call it freely.** Calling on an
   already-closed / unknown id is a no-op returning `Ok ()`. Pair
   it on BOTH the Done arm AND the Errored arm without worrying
   about double-close.

## Lifecycle guarantees

| Failure mode | What the runtime does |
|---|---|
| User closes the browser tab | Session TTL eventually evicts → markDone walks `sess.streams` → every owned stream closes; log: `[sky.stream] cleaned N orphaned streams on session close` |
| `Cmd.perform close` after Done already fired | No-op (idempotent) |
| Upstream sends a 4xx / 5xx response | Stream still opens; the chunk subscription receives whatever body the upstream returned, then `Done`. Use the HTTP status carried elsewhere if you want to inspect it. |
| Upstream drops connection mid-stream | `Chunked (Errored Error)` fires; stream closes; subscription retires |
| Sky's dispatch loop wedges | Spool goroutine waits up to 30 s for the channel push, then drops the chunk + abandons the stream (logs `[sky.stream] consumer stall on stream N`) |

## Defaults (NOT configurable in v0.15.x)

| Knob | Value | Why locked |
|---|---|---|
| Chunk type | `String` (UTF-8) | LLM SSE + JSON streams are the v1 use cases; a future `Bytes` overload can ship alongside without breaking this surface. |
| Registry scope | Per-session | Clean cleanup on session disconnect; no global cross-session leak class. |
| Drain rate | 8 events / pass per stream | Bounds the dispatch burst per subscriber iteration so one fast stream can't starve other Subs. |
| Header timeout | 30 s (matches `Http.request`) | Initial connect + TLS + header read must succeed in 30 s. **Body has no timeout** — long-lived streams are the use case. |
| Channel buffer | 16 events / stream | Matches `SKY_LIVE_SSE_BUFFER` default; symmetric with the SSE channel's backpressure. |
| Consumer-stall timeout | 30 s | If the dispatch loop can't drain for 30 s, the spool goroutine abandons rather than pinning the body connection. |

## Composition with `Sub.batch`

A page can subscribe to a stream AND a periodic tick AND a pub/sub
topic simultaneously:

```elm
subscriptions model =
    Sub.batch
        [ Time.every 1000 Tick
        , case model.activeStream of
              Just sid -> HttpStream.chunks sid Chunked
              Nothing -> Sub.none
        , Sub.subscribeTopic "alerts" AlertReceived
        ]
```

## Backpressure + reliability

- **Bounded channel + non-blocking send.** If the consumer falls
  behind, oversend drops via `default:` rather than blocking the
  spool. A stall lasting more than 30 s logs + abandons.

- **Goroutine hygiene.** One spool goroutine per open stream +
  one drain goroutine per active subscription. Both exit promptly
  on close OR session teardown. Verified under -race with
  `runtime.NumGoroutine()` baselining (see
  `runtime-go/rt/http_stream_test.go`).

- **No request-body streaming in v0.15.x.** This module is
  **response-body streaming only**. Sending a large body still
  buffers via the standard `Http.request` path. Request-body
  streaming is a separate primitive (out of scope).

- **No WebSockets.** WebSocket is a separate bidirectional
  primitive — also out of scope for v0.15.x.

## When NOT to use this

- Small responses (< 100 KB, < 500 ms) — the buffered `Http.get`
  shape is simpler.
- Endpoints where you need the FULL response before deciding what
  to do with it (e.g. JSON validation, signature verification).
  Stream-decoding partial JSON is brittle; in those cases buffer.
- Polling-style "give me the latest state" calls — use
  `Time.every` plus `Http.get`, not streaming.

Streaming is for endpoints where time-to-first-byte beats
end-to-end throughput. Pick deliberately.
