# SSE relay (#373) — `Sky.Core.Http.Stream.forEachChunk`

Canonical example for the **synchronous-relay shape** — the bridge
that lets a plain `Sky.Http.Server` handler forward an upstream
streaming response chunk-for-chunk to its own client, without going
through Sky.Live's update-loop Sub dispatch (which is unavailable
in handler goroutines).

## Run

```bash
sky run src/Main.sky
curl --no-buffer http://localhost:8001/relay
```

`/relay` proxies `/upstream` chunk-for-chunk. Both serve identical
SSE bytes. Chunks arrive progressively (~200ms apart) — NOT all at
once at the end.

## Architecture

```
browser/curl ──GET /relay──> Sky.Http.Server (port 8001)
                                 │
                                 │ Http.Stream.open upstreamReq
                                 ▼
                            /upstream handler
                            (Server.Stream.stream)
                                 │
                                 │ HttpStream.forEachChunk hdl
                                 │   (\chunk -> Server.Stream.emit chunk writer)
                                 ▼
browser/curl <───chunked SSE── Sky.Http.Server
```

## Code shape

```elm
handleRelay : Request -> Task Error Response
handleRelay _ =
    ServerStream.stream "text/event-stream" (\writer ->
        HttpStream.open upstreamRequest
            |> Task.andThen (\hdl ->
                HttpStream.forEachChunk hdl
                    (\chunk -> ServerStream.emit chunk writer))
            |> Task.andThen (\_ -> ServerStream.finish writer))
```

`forEachChunk` blocks the handler goroutine until upstream EOF.
Each chunk is emitted downstream synchronously. Backpressure flows
naturally from the downstream consumer back through to the upstream
read.

## Driving use case

SkyDeploy's `agent-service` `/generate/stream` endpoint relays
Anthropic SSE tokens to control-plane. Before `forEachChunk`,
agent-service shipped one downstream SSE per agent-loop *phase*
(workaround); after, it ships one SSE per *token*.

See `docs/skylive/http-streaming.md` §"Synchronous relay".
