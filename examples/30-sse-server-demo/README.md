# SSE server demo (Sky.Http.Server.Stream)

A minimal Sky.Http.Server app demonstrating server-side streaming
HTTP responses via `Sky.Http.Server.Stream` (the mirror image of
`Sky.Core.Http.Stream`, which handles client-side incremental
reads).

## What this exercises

- `Stream.stream "text/event-stream" handler` — open a streaming
  response.  The dispatcher writes headers + flushes BEFORE
  invoking `handler`, so the client sees `200 OK` +
  `Content-Type: text/event-stream` immediately.
- `Stream.emit chunk writer` — write a chunk and flush to the
  client socket.  Bytes hit the wire before `emit`'s Task
  resolves.
- `Stream.finish writer` — mark the response complete (idempotent;
  also implicit at handler return).

## Running

```
cd examples/30-sse-server-demo
sky run src/Main.sky
```

Then in another terminal:

```
curl --no-buffer http://localhost:8000/events
```

You should see five `tick` events arrive ~200ms apart followed by a
`done` event, then the connection closes.  The `--no-buffer` flag is
critical — without it, `curl` buffers the whole body and the demo
appears non-streaming.

Browser console:

```js
const s = new EventSource('http://localhost:8000/events');
s.addEventListener('tick', e => console.log('tick:', e.data));
s.addEventListener('done', () => { console.log('done'); s.close(); });
```

## Use cases

- LLM token streaming (proxy upstream streaming completions to the
  browser).
- Long-running task progress (build / deploy / data-import dashboards).
- Live notifications without WebSocket complexity.
- Chunked file responses for very large downloads.
