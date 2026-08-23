//go:build !js

// spa_push.go — Sky.Spa server→client PUSH (SSE) runtime for the auto-split
// backend (docs/skyspa/auto-split.md §16).
//
// The `sky spa-split` generator emits a NATIVE Sky.Http.Server backend that
// runs each effectful `update` branch behind a generated `POST /_rpc/<Msg>`
// endpoint. When the app also uses `Sub.subscribeTopic` / `Cmd.publish`, the
// backend gains a server→client push channel:
//
//   * one process-shared broker (`Spa_newBroker`) — a standalone
//     *topicRegistry (the SAME in-process Broker Sky.Live uses, live_topics.go),
//     constructed directly rather than via the Live.app-registered process
//     broker (PubSub_publish needs a Live.app; a plain backend registers none);
//   * `Spa_interpretPublish(broker, cmd)` — after an RPC handler runs the real
//     effect via the app's own `update`, its returned `Cmd` is fed here; a
//     `Cmd.publish` / `Cmd.publishNoEcho` (including inside a `Cmd.batch`) fans
//     out through the broker. Non-publish Cmds are ignored (the stateless
//     backend has no client to `perform` toward — that is the frontend's job);
//   * `Spa_streamTopic(broker, topic)` — the SSE handler body: subscribe to the
//     topic on the broker and `emit` each published payload as an SSE
//     `data: <json>\n\n` frame over `Sky.Http.Server.Stream`, until the client
//     disconnects. Reuses the server_stream.go writer + serveStreamingResponse
//     head-flush (which now sets SSE-friendly proxy headers for
//     `text/event-stream`).
//
// All three are referenced ONLY from generated backend code (native), so there
// is no `//go:build js` counterpart — a wasm frontend never links them.
//
// Multi-replica: the broker defaults to in-process, exactly like Sky.Live's
// default — a publish on replica A reaches only subscribers connected to A.
// Cross-replica fan-out is the same seam as Sky.Live: a Redis broker
// implementing the Broker interface, selected by SKY_LIVE_BROKER_URL or the URL
// baked in by `sky spa-split --broker <url>` (env overrides the baked arg),
// reconciled by effectiveBrokerUrl in live_redis_broker.go. See
// docs/skyspa/auto-split.md §16.

package rt

import (
	"encoding/json"
	"time"
)

// spaSSEHeartbeat is how often an idle SSE connection is pinged with a comment
// frame. The ping's purpose is disconnect detection: with no traffic the
// handler would block on the broker channel forever and never notice a gone
// client, leaking one goroutine + one broker subscription per dead connection.
// A failed heartbeat write flips the stream closed and unwinds the loop.
const spaSSEHeartbeat = 15 * time.Second

// spaSSEPad primes proxy buffers that ignore X-Accel-Buffering — the same
// ≥2 KB comment padding Sky.Live's SSE endpoint writes before its hello frame
// (live.go). Sent as the first bytes so an intermediary flushes past its buffer
// threshold before the first real event.
var spaSSEPad = func() string {
	b := make([]byte, 0, 2050)
	b = append(b, ':', ' ')
	for i := 0; i < 2048; i++ {
		b = append(b, '.')
	}
	b = append(b, '\n', '\n')
	return string(b)
}()

// Spa_newBroker constructs the pub/sub broker for the auto-split backend. Sky
// surface (generated backend):
//
//	spaNewBroker : String -> any
//	spaNewBroker = Ffi.kernel "Spa_newBroker"
//	spaBroker = spaNewBroker "<url>"   -- memoised CAF: one broker for the process
//
// Returns a Broker as an opaque `any`. The generated backend holds it as a
// top-level binding, so every RPC handler + the SSE endpoint share the ONE
// broker. `urlArg` is the URL baked in by `sky spa-split --broker <url>` (empty
// string when the flag is absent). It defaults to an in-process *topicRegistry
// (single replica); when a URL resolves through effectiveBrokerUrl — the env
// SKY_LIVE_BROKER_URL if set, else the baked arg — `maybeOverrideBroker`
// upgrades it to the SAME cross-instance Redis broker Sky.Live uses, so a
// publish on replica A reaches an SSE subscriber on replica B, with no session
// store required (the broker is app-scoped, not store-scoped). The env still
// OVERRIDES the baked arg. An undialable URL degrades to in-process (logged),
// and SKY_LIVE_BROKER=inprocess forces local.
func Spa_newBroker(urlArg any) any {
	return maybeOverrideBroker(newTopicRegistry(0), spaBrokerArgURL(urlArg))
}

// spaBrokerArgURL extracts the baked broker URL from the kernel arg. Only a
// real string counts; anything else (e.g. the legacy `()` unit, or a nil) is
// treated as "no baked URL" so the resolver falls back to the env / in-process.
func spaBrokerArgURL(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Spa_interpretPublish interprets an `update`-returned Cmd against the broker,
// firing any publish it carries. Sky surface (generated backend):
//
//	spaInterpretPublish : any -> Cmd msg -> Task Error ()
//	spaInterpretPublish = Ffi.kernel "Spa_interpretPublish"
//
// Returns a `Task Error ()` so the handler forces it (via `Task.andThen`) before
// answering the RPC — the publish is on the wire before the caller's response.
// A nil/foreign broker or a non-Cmd value is a no-op (Ok unit), never a panic.
func Spa_interpretPublish(brokerArg, cmdArg any) any {
	return func() any {
		if broker, ok := brokerArg.(Broker); ok {
			spaInterpretCmd(broker, cmdArg)
		}
		return Ok[any, any](skyUnit())
	}
}

// spaInterpretCmd walks a Cmd value (the `cmdT` the runtime builds) and fans
// each publish through the broker. Handles `Cmd.batch` by recursing. Fields of
// `cmdT` are unexported, which is why this interpreter lives in package rt.
func spaInterpretCmd(broker Broker, cmdArg any) {
	cmd, ok := cmdArg.(cmdT)
	if !ok {
		return
	}
	switch cmd.kind {
	case "batch":
		for _, c := range cmd.batch {
			spaInterpretCmd(broker, c)
		}
	case "publish":
		broker.Publish(cmd.topic, SessionEvent{Payload: cmd.payload, Origin: ""})
	case "publishNoEcho":
		// No originating session on a stateless backend (Origin ""), so
		// SkipOrigin has no self to suppress; it rides through for forward-compat
		// with a cross-process broker tier that advertises the bit.
		broker.Publish(cmd.topic, SessionEvent{Payload: cmd.payload, Origin: "", SkipOrigin: true})
	}
	// Any other kind (none / perform / …) is intentionally ignored — a stateless
	// push backend delivers publishes, not client-side effects.
}

// Spa_streamTopic is the SSE handler body for `GET /_sky/sub?topic=<topic>`.
// Sky surface (generated backend):
//
//	spaStreamTopic : any -> String -> (StreamWriter -> Task Error ())
//	spaStreamTopic = Ffi.kernel "Spa_streamTopic"
//	subHandler req =
//	    Stream.stream "text/event-stream"
//	        (spaStreamTopic spaBroker (Maybe.withDefault "" (Server.queryParam "topic" req)))
//
// Called with (broker, topic) it returns the `StreamWriter -> Task Error ()`
// closure `Sky.Http.Server.Stream.stream` invokes: subscribe to `topic`, prime
// the proxy pad, then loop emitting each published payload as an SSE
// `data: <json>\n\n` frame until the client disconnects (a failed write) — then
// cancel the subscription and finish.
func Spa_streamTopic(brokerArg, topicArg any) any {
	return func(writerArg any) any {
		return func() any {
			broker, ok := brokerArg.(Broker)
			if !ok {
				return Err[any, any](ErrUnavailable("Spa_streamTopic: no broker wired for the SSE endpoint"))
			}
			topic := AsString(topicArg)
			sh := lookupServerStream(spaStreamWriterID(writerArg))
			if sh == nil {
				// The stream head was never registered (writer resolved to no
				// live handle) — nothing to write to.
				return Ok[any, any](skyUnit())
			}
			ch, cancel := broker.Subscribe(topic)
			defer cancel()

			// Prime proxy buffers before the first event.
			if !spaSSEWrite(sh, spaSSEPad) {
				return Ok[any, any](skyUnit())
			}

			ping := time.NewTicker(spaSSEHeartbeat)
			defer ping.Stop()
			for {
				select {
				case ev, open := <-ch:
					if !open {
						return Ok[any, any](skyUnit())
					}
					data, err := json.Marshal(ev.Payload)
					if err != nil {
						// A payload that will not marshal is skipped rather than
						// killing the stream — the next publish supersedes it.
						continue
					}
					if !spaSSEWrite(sh, "data: "+string(data)+"\n\n") {
						return Ok[any, any](skyUnit()) // client gone
					}
				case <-ping.C:
					if !spaSSEWrite(sh, ": ping\n\n") {
						return Ok[any, any](skyUnit()) // client gone
					}
				}
			}
		}
	}
}

// spaStreamWriterID extracts the runtime stream id from the StreamWriter value
// serveStreamingResponse hands the handler. It arrives as the single-field
// SkyADT `StreamWriter Int` (server_stream.go), but tolerate a bare int too.
func spaStreamWriterID(writerArg any) int64 {
	if adt, ok := writerArg.(SkyADT); ok && len(adt.Fields) > 0 {
		return asInt64(adt.Fields[0])
	}
	return asInt64(writerArg)
}

// spaSSEWrite writes one frame to the stream handle + flushes, mirroring
// ServerStream_emit's write path (server_stream.go) but callable directly from
// the subscribe loop. Returns false once the client is gone (write error) or
// the stream is closed, so the caller unwinds and cancels its subscription.
func spaSSEWrite(sh *serverStreamHandle, frame string) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.closed.Load() {
		return false
	}
	sh.headerSent.Store(true)
	if _, err := sh.w.Write([]byte(frame)); err != nil {
		sh.closed.Store(true)
		return false
	}
	sh.flusher.Flush()
	return true
}
