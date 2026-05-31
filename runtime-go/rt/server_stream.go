// server_stream.go — Sky.Http.Server.Stream server-side streaming
// HTTP response runtime (Cycle 4 HS-Server).
//
// Mirror image of http_stream.go (Sky.Core.Http.Stream — client-side
// incremental body reads).  Where http_stream.go reads chunks FROM
// an upstream into Sky's update loop as Msgs, this file writes
// chunks FROM a Sky handler TO an HTTP client over a long-lived
// connection.
//
// The shape:
//
//   Sky side:                              Runtime side:
//   ─────────                              ─────────────
//   Stream.stream ct handler            → ServerStream_stream:
//                                         pack (ct, handler) into a
//                                         streaming SkyResponse; the
//                                         dispatcher detects the
//                                         StreamHandler sentinel and
//                                         takes the chunk-write path.
//
//   Stream.emit chunk writer            → ServerStream_emit:
//                                         resolve writer id → handle,
//                                         w.Write(chunk) + Flush().
//
//   Stream.finish writer                → ServerStream_finish:
//                                         flag the handle closed so
//                                         late `emit`s become no-ops
//                                         and the dispatcher knows
//                                         the handler is done.
//
//   Stream.withContentType ct writer    → ServerStream_withContentType:
//                                         set Content-Type if headers
//                                         haven't been flushed yet
//                                         (best-effort; no-op otherwise).
//
// Concurrency contract:
//
//   - Each streaming response gets its own serverStreamHandle with
//     a unique id (process-global atomic counter, NEVER zero).
//
//   - Handles live on serverStreamHandles (a sync.Map) for the
//     lifetime of the user's handler.  serveStreamingResponse
//     registers + sweeps under defer so an early panic or handler
//     return cleans up.
//
//   - http.ResponseWriter is single-goroutine-write by Go convention.
//     We don't take a mutex on emit() — the user's handler runs in
//     ONE goroutine (the http.Handler goroutine) and emits
//     sequentially.  If user code spawns goroutines and emits
//     concurrently, behaviour is undefined (same as raw http stdlib).
//
//   - http.Flusher: every chunk is followed by Flush().  If the
//     underlying ResponseWriter doesn't implement Flusher (rare —
//     only middleware wrapping that doesn't pass-through Flusher),
//     stream() returns Err ErrUnavailable BEFORE invoking the
//     handler so the user gets a clear error instead of buffered
//     non-streaming output.

package rt

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// ═══════════════════════════════════════════════════════════════
// serverStreamHandle — one in-flight streaming response
// ═══════════════════════════════════════════════════════════════

// serverStreamHandle owns one open http.ResponseWriter for the
// duration of a streaming response.  The user's handler emits
// chunks via the handle's id; the dispatcher cleans up on handler
// return.
type serverStreamHandle struct {
	id      int64
	w       http.ResponseWriter
	flusher http.Flusher

	// closed — set true the first time finish fires (OR the
	// dispatcher sweeps after handler return).  Subsequent emit /
	// finish / withContentType are no-ops.
	closed atomic.Bool

	// headerSent — set true on the first successful emit OR
	// explicit pre-emit header flush.  Once true, withContentType
	// is a no-op (HTTP can't undo a flushed header).
	headerSent atomic.Bool

	// mu guards the write path against concurrent emit / finish
	// from misbehaved user code.  Optional — the documented
	// contract is single-writer, but the lock prevents a runtime
	// panic if the user violates it.
	mu sync.Mutex
}

// ═══════════════════════════════════════════════════════════════
// Registry — id → handle
// ═══════════════════════════════════════════════════════════════

// serverStreamHandles maps stream id → *serverStreamHandle for the
// duration of the user's handler.  Process-global (NOT per-session)
// so a Sky.Http.Server handler — which has no session context — can
// resolve its writer id.  Tradeoff: a runaway handler that never
// returns + never finishes leaks one entry forever; the dispatcher's
// defer-sweep neutralises this for normal handler exits + panics.
var serverStreamHandles sync.Map

// serverStreamIDCounter — monotonic source of unique stream ids.
// Process-wide, atomic for lock-free allocation under concurrent
// requests.
var serverStreamIDCounter atomic.Int64

// nextServerStreamID — fresh non-zero id.  0 is reserved so a zero-
// valued StreamWriter (uninitialised model field) can't accidentally
// resolve to a live stream.
func nextServerStreamID() int64 {
	for {
		id := serverStreamIDCounter.Add(1)
		if id != 0 {
			return id
		}
	}
}

// lookupServerStream — resolve an id to the in-flight handle.
// Returns nil if the id was never registered OR was already swept.
func lookupServerStream(id int64) *serverStreamHandle {
	v, ok := serverStreamHandles.Load(id)
	if !ok {
		return nil
	}
	return v.(*serverStreamHandle)
}

// ═══════════════════════════════════════════════════════════════
// Sky-facing kernel entries
// ═══════════════════════════════════════════════════════════════

// pendingStreamHandlers carries StreamHandler closures across the
// typed-codegen boundary.  v0.15.44 made user-side `Response` a
// typed record alias (Sky_Http_Server_Response_R) — a struct shape
// with no StreamHandler field.  When TaskCoerceT[E, Response_R]
// narrows the rt.SkyResponse the user produced via
// ServerStream_stream, the StreamHandler `any` field gets silently
// dropped (target struct has no slot for it).
//
// To preserve streaming end-to-end, ServerStream_stream registers
// the closure here under a fresh token AND stamps the token into
// the response Body (as `__sky_stream:<token>`).  The listener's
// asSkyResponse detects the sentinel + restores StreamHandler from
// this map before routing to serveStreamingResponse.  The entry is
// removed on lookup so a long-lived token can't leak indefinitely.
var pendingStreamHandlers sync.Map // map[string]any (any = handler closure)
var pendingStreamTokenSeq atomic.Int64

const pendingStreamSentinelPrefix = "__sky_stream:"

func registerPendingStreamHandler(handler any) string {
	id := pendingStreamTokenSeq.Add(1)
	token := fmt.Sprintf("%d", id)
	pendingStreamHandlers.Store(token, handler)
	return token
}

func takePendingStreamHandler(token string) (any, bool) {
	v, ok := pendingStreamHandlers.LoadAndDelete(token)
	if !ok {
		return nil, false
	}
	return v, true
}

// extractPendingStreamToken returns the token + true when body
// starts with the sentinel prefix.  Otherwise empty + false.  The
// listener uses this to detect a streaming response and look up
// its handler.
func extractPendingStreamToken(body string) (string, bool) {
	if !strings.HasPrefix(body, pendingStreamSentinelPrefix) {
		return "", false
	}
	return body[len(pendingStreamSentinelPrefix):], true
}

// ServerStream_stream implements:
//
//	Sky.Http.Server.Stream.stream
//	    : String -> (StreamWriter -> Task Error ()) -> Task Error Response
//
// Returns a Task that resolves to a `Response` carrying the
// streaming sentinel.  The dispatcher in Server_listen detects
// the non-nil StreamHandler and routes to serveStreamingResponse.
//
// The user's handler closure (the `(StreamWriter -> Task Error ())`
// argument) is stashed in pendingStreamHandlers under a unique
// token; the token is encoded into Body as `__sky_stream:<token>`
// so it survives the typed-codegen Response_R boundary.  The
// listener's asSkyResponse path detects the sentinel + restores
// the closure to StreamHandler before serving.  StreamHandler is
// ALSO set on the returned SkyResponse so the legacy any-typed
// path (FFI direct return, pre-v0.15.44 codegen) still works.
func ServerStream_stream(contentType any, handler any) any {
	ct := fmt.Sprintf("%v", contentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	token := registerPendingStreamHandler(handler)
	return func() any {
		return Ok[any, any](SkyResponse{
			Status:        200,
			ContentType:   ct,
			Body:          pendingStreamSentinelPrefix + token,
			StreamHandler: handler,
		})
	}
}

// ServerStream_emit implements:
//
//	Sky.Http.Server.Stream.emit : String -> Int -> Task Error ()
//
// (The Sky-side wrapper unwraps StreamWriter to the inner Int.)
//
// Writes the chunk + flushes.  On an unknown / closed id, returns
// Ok unit — emit-after-finish is documented as a no-op so a tail
// `finish` in the handler doesn't make a final `emit` race the
// dispatcher's sweep.
func ServerStream_emit(chunkArg any, sidArg any) any {
	id := asInt64(sidArg)
	chunk := fmt.Sprintf("%v", chunkArg)
	return func() any {
		sh := lookupServerStream(id)
		if sh == nil || sh.closed.Load() {
			return Ok[any, any](skyUnit())
		}
		sh.mu.Lock()
		defer sh.mu.Unlock()
		if sh.closed.Load() {
			return Ok[any, any](skyUnit())
		}
		// Mark header sent — once the first byte goes out the
		// status / Content-Type / withContentType is sealed.
		sh.headerSent.Store(true)
		if _, err := sh.w.Write([]byte(chunk)); err != nil {
			// Client disconnect mid-stream is the common case.
			// Mark the handle closed so subsequent emits become
			// no-ops; surface as a typed error so the handler
			// can choose to abort cleanly.
			sh.closed.Store(true)
			return Err[any, any](ErrNetwork("server.stream emit: " + err.Error()))
		}
		sh.flusher.Flush()
		return Ok[any, any](skyUnit())
	}
}

// ServerStream_finish implements:
//
//	Sky.Http.Server.Stream.finish : Int -> Task Error ()
//
// Idempotent: marking the handle closed prevents further emits.
// The dispatcher's actual unregister + return-to-net/http path
// runs after the user's handler Task fully resolves; finish just
// flips the closed flag.
func ServerStream_finish(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		sh := lookupServerStream(id)
		if sh != nil {
			sh.closed.Store(true)
		}
		return Ok[any, any](skyUnit())
	}
}

// ServerStream_withContentType implements:
//
//	Sky.Http.Server.Stream.withContentType : String -> Int -> Task Error ()
//
// Best-effort: if headers have already been flushed (any prior
// emit) this is a no-op.  Otherwise rewrites the Content-Type on
// the underlying http.Header so the eventual flush sees the new
// value.
func ServerStream_withContentType(ctArg any, sidArg any) any {
	id := asInt64(sidArg)
	ct := fmt.Sprintf("%v", ctArg)
	return func() any {
		sh := lookupServerStream(id)
		if sh == nil || sh.closed.Load() || sh.headerSent.Load() {
			return Ok[any, any](skyUnit())
		}
		sh.mu.Lock()
		defer sh.mu.Unlock()
		if sh.headerSent.Load() {
			return Ok[any, any](skyUnit())
		}
		sh.w.Header().Set("Content-Type", ct)
		return Ok[any, any](skyUnit())
	}
}

// ═══════════════════════════════════════════════════════════════
// Dispatcher integration — called from Server_listen
// ═══════════════════════════════════════════════════════════════

// serveStreamingResponse drives a streaming response end-to-end:
//
//  1. Assert http.Flusher (reject with 503 if not implemented —
//     buffered output would silently break the streaming contract).
//  2. Apply per-response Content-Type + user-supplied headers.
//  3. Apply safe-by-default security headers (parity with the
//     buffered path).  CSRF auto-injection is skipped — streaming
//     bodies aren't form-bearing HTML.
//  4. WriteHeader (Status — defaults to 200 inside ServerStream_stream).
//  5. Flush to commit the head BEFORE invoking the user's handler;
//     this is what gets the head onto the wire so the client can
//     react to status + Content-Type before any chunk arrives
//     (critical for SSE — the EventSource spec requires the
//     "text/event-stream" Content-Type to be visible before the
//     first dispatch).
//  6. Register a serverStreamHandle, invoke the handler via SkyCall
//     with the StreamWriter ADT, drive its returned Task to
//     completion (anyTaskInvoke), sweep the handle.
//
// Panics inside the handler are caught by the outer mux closure's
// defer-recover (logPanicFrame + 500); since headers are already on
// the wire the 500 would be a no-op on the response side — the
// recover still neutralises the panic so the server stays up.
func serveStreamingResponse(w http.ResponseWriter, _ *http.Request, resp SkyResponse) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Reverse-proxy / middleware wrapping that doesn't
		// pass-through Flusher.  Reject cleanly — buffered output
		// would defeat the whole streaming contract.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "Streaming response requires http.Flusher support (Sky.Http.Server.Stream)")
		return
	}

	// Apply Content-Type from the response builder first; the user-
	// supplied Headers map overrides if needed (parity with the
	// buffered path).
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	setSecurityHeaders(w.Header())

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	// Commit the head BEFORE the handler runs — the client should
	// see the response code + Content-Type immediately, even if
	// the first chunk takes seconds to compute.
	flusher.Flush()

	sh := &serverStreamHandle{
		id:      nextServerStreamID(),
		w:       w,
		flusher: flusher,
	}
	// Pre-mark headerSent: from the user's POV the headers are
	// committed (this flush wrote them).  Any withContentType
	// call after this point is correctly a no-op.
	sh.headerSent.Store(true)

	serverStreamHandles.Store(sh.id, sh)
	defer func() {
		serverStreamHandles.Delete(sh.id)
		sh.closed.Store(true)
	}()

	// Construct the Sky-side StreamWriter ADT value — single-
	// constructor `StreamWriter Int`.  Pass it to the user's
	// handler closure via SkyCall (reflect-backed; accepts any
	// callable shape that typed codegen / `func(any) any`
	// kernel-fallback may emit).
	writerADT := SkyADT{
		Tag:     0,
		SkyName: "StreamWriter",
		Fields:  []any{sh.id},
	}
	task := SkyCall(resp.StreamHandler, writerADT)
	// Drive the handler's returned Task to completion. The result
	// is ignored — the handler's effect IS the chunk-writes; if it
	// errored we've already written some chunks + headers, so we
	// can't meaningfully signal the error to the client.  The
	// dispatcher-level connection close serves as end-of-stream.
	_ = anyTaskInvoke(task)
}
