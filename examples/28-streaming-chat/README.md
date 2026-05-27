# examples/28-streaming-chat — incremental HTTP streaming demo

Sky.Live app that streams a "completion" reply token-by-token via
`Sky.Core.Http.Stream` — perceived latency drops 5-10× vs the
buffered `Http.get` shape (first byte ~100 ms vs full reply at the
end of the upstream).

This is the canonical reference for the streaming API introduced in
Cycle 4 HS — see `docs/skylive/http-streaming.md` for the design
write-up.

## Architecture

```
                    POST text/plain                       chunks SSE
  browser  ────────────────────────►  Sky.Live  ────────────────────►  browser
   (form)                              (this app)                       (live view)
                                          │
                                          │ Http.Stream.open req
                                          ▼
                                  ┌───────────────────┐
                                  │ mock/main.go      │
                                  │ /stream — 20×100ms│
                                  └───────────────────┘
```

The app subscribes to the stream only while it's in-flight:

```elm
subscriptions model =
    case model.activeStream of
        Just sid -> HttpStream.chunks sid Chunked
        Nothing  -> Sub.none
```

`Chunked Done` clears `model.activeStream` and the next
`subscriptions` eval drops the chunk Sub — the drain goroutine
retires cleanly. `Chunked Errored` does the same plus surfaces the
error.

## Running locally

```bash
# Terminal 1 — mock streaming server
go run examples/28-streaming-chat/mock/main.go
# (listens on :8765, sends 20 chunks @ 100 ms each per /stream POST)

# Terminal 2 — Sky.Live app
cd examples/28-streaming-chat
sky build src/Main.sky
sky run src/Main.sky
# (listens on :8000)

# Browser
open http://localhost:8000
```

Type a prompt, hit Send. The reply paints token-by-token in the
blue "Streaming…" panel. When it finishes, the panel hides and the
full reply lands in the history list above.

## Automated verification

```bash
# Boot both services + drive a single-tab Playwright probe
bash scripts/verify-streaming-chat.sh
```

PASS output:

```
PASS verify-streaming-chat
    first_chunk_ms=<n>    # ≤ 1000 ms — proposal acceptance #3
    all_chunks_ms=<n>     # ≤ 8000 ms for 5 chunks @ 100 ms cadence
    done_ms=<n>           # ≤ 12 000 ms full 20-chunk stream
```

## Pointing at a real upstream

Open `src/Main.sky` and replace `mockStreamUrl` with the SSE / chat-
completion URL you want to drive. The request shape is the standard
`HttpRequest` record — method / url / body / headers. For LLM
upstreams, you typically set:

```elm
req =
    { method = "POST"
    , url = "https://api.anthropic.com/v1/messages"
    , body = """{"model": "claude-haiku-4-5", "max_tokens": 1024, "stream": true, "messages": [{"role": "user", "content": "..."}]}"""
    , headers =
        [ ( "Content-Type", "application/json" )
        , ( "X-Api-Key", System.getenvOr "ANTHROPIC_API_KEY" "" )
        , ( "Anthropic-Version", "2023-06-01" )
        , ( "Accept", "text/event-stream" )
        ]
    }
```

The chunk parser stays the same — the `Chunked Chunk text` arm
receives raw UTF-8 bytes; if the upstream is SSE you'll typically
buffer until you see `\n\n` then parse one event at a time.

## Lifecycle guarantees (from the runtime contract)

- `close` is **idempotent** — safe from a Done handler that always
  closes regardless of the prior message AND from a Done +
  per-error branch both closing in the same `update` pass.

- **Session disconnect** (TTL eviction OR explicit Delete) walks
  every owned stream and closes it. The runtime logs:
  `[sky.stream] cleaned N orphaned streams on session close (sid=...)`.

- **Backpressure**: if the dispatch loop falls behind, the spool
  goroutine drops chunks via non-blocking send + a 30 s
  consumer-stall timeout. A wedged subscriber doesn't pin the body
  connection forever.

- **Header timeout** (30 s) on the initial request mirrors
  `Http.request`. **No body timeout** — long-lived streams are the
  point.
