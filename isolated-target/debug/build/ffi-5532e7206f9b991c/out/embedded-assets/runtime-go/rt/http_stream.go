// http_stream.go — Sky.Core.Http.Stream streaming HTTP response runtime
// (Cycle 4 HS).
//
// Lets Sky code read an HTTP response body incrementally as bytes
// arrive — each chunk surfacing as a Msg via a `Sub` subscription
// (mirrors the shape of `Time.every` / `Sub.subscribeTopic`).
//
// Architecture parallel: Cycle 3 P46-P48 pub/sub. The shape is:
//
//   Sky side:               Runtime side:
//   ─────────               ─────────────
//   Http.Stream.open req → HttpStream_open: kick off http.Request,
//                          create per-session *streamHandle, spool
//                          goroutine on body.Read → ch.
//
//   Http.Stream.chunks sid → Sub.subscribeStream sid toMsg leaf.
//                          setupSubscriptions opens a drain goroutine
//                          that reads ch + dispatches msgs through
//                          app.dispatch (same path Cmd.perform takes).
//
//   Http.Stream.close sid → HttpStream_close: close body, set closed
//                          flag, unregister from session map. Idempotent.
//
// Concurrency contract:
//
//   - Per-session registry. *streamHandle lives on liveSession.streams
//     (a map[int64]*streamHandle), guarded by liveSession.streamsMu.
//     Cleanup is local to the session — markDone walks every stream
//     and closes it (matching the pub/sub markDone pattern).
//
//   - Bounded channel (cap 16) per stream; spool goroutine drops via
//     default: if the consumer can't keep up. A 30s consumer-timeout
//     abandons the stream if the dispatch loop is wedged.
//
//   - 4096-byte read buffer; chunks emit as SkyString (UTF-8 text).
//     Bytes overload deferred to a future kernel signature.
//
//   - Header timeout (30s, mirrors Http.request). NO body timeout —
//     LLM streams may legitimately run for minutes; the per-stream
//     consumer-stall timeout is the upper bound, not the request
//     duration.
//
// Locked defaults (from the proposal, all five preserved):
//
//   1. String chunks (UTF-8 text).
//   2. Per-session registry (NOT global).
//   3. Drain rate: up to 8 events per drain pass per stream — enforced
//      in the subscriber loop; not env-tunable in v0.15.x.
//   4. Header timeout 30s; no body timeout.
//   5. StreamId is `type StreamId = StreamId Int` (single-constructor ADT).

package rt

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ═════════════════════════════════════════════════════════════════════
// Per-stream constants (locked defaults — NOT env-tunable in v0.15.x)
// ═════════════════════════════════════════════════════════════════════

const (
	// streamChanBuffer — bounded channel capacity per stream. The
	// spool goroutine drops via `default:` if the drain consumer
	// falls this far behind. Chosen to match SKY_LIVE_SSE_BUFFER's
	// default so the broadcast + stream paths have symmetric
	// backpressure semantics.
	streamChanBuffer = 16

	// streamReadBuffer — bytes per body.Read call. 4 KiB matches
	// the kernel's default page size; SSE chunks from typical
	// upstream LLM APIs run smaller (one delta token = ~30-100 B).
	streamReadBuffer = 4096

	// streamConsumerTimeout — if the spool goroutine cannot push an
	// event onto the channel within this window (channel full +
	// consumer wedged), it logs + abandons the stream. Bounds the
	// runaway-goroutine class — a session whose subscriber loop has
	// crashed must not pin its body connection forever.
	streamConsumerTimeout = 30 * time.Second

	// streamHeaderTimeout — applied to the initial HTTP transaction
	// (DNS + connect + TLS + header read). Mirrors the existing
	// SKY_HTTP_CLIENT_TIMEOUT default. Body read is UN-timed at the
	// client level — long-lived streams are the use case.
	streamHeaderTimeout = 30 * time.Second

	// streamDrainBatchMax — locked default #3. Up to N events per
	// drain pass per stream. Caps the dispatch + render burst per
	// subscriber iteration so one fast stream can't starve the
	// session's other Subs.
	streamDrainBatchMax = 8
)

// ═════════════════════════════════════════════════════════════════════
// streamEvent — the value that flows from the spool goroutine to the
// drain goroutine (which dispatches it through the user's toMsg
// decoder as a ChunkEvent ADT value).
// ═════════════════════════════════════════════════════════════════════

type streamEventKind int

const (
	streamChunkEv streamEventKind = iota
	streamDoneEv
	streamErrEv
)

type streamEvent struct {
	kind streamEventKind
	data string // streamChunkEv only — UTF-8 chunk text
	err  any    // streamErrEv only — Sky-shaped Error ADT (from ErrNetwork/etc.)
}

// ═════════════════════════════════════════════════════════════════════
// streamHandle — one live HTTP-streaming response.
// ═════════════════════════════════════════════════════════════════════

// streamHandle owns one open HTTP body + its spool goroutine. The
// drain side reads `ch` via the session's subscriber loop. closed
// is a one-way flag — Close marks it true under closeOnce so a
// concurrent body-EOF and an explicit Http.Stream.close don't both
// race to close ch / body.
type streamHandle struct {
	id   int64
	body io.ReadCloser
	ch   chan streamEvent

	// closed — set true the first time Close fires (idempotent via
	// closeOnce). Read by the spool goroutine on a non-blocking-send
	// timeout so it can decide whether to drop or abandon.
	closed atomic.Bool

	// done — closed by Close exactly once; the spool goroutine
	// selects on it as a teardown signal so a sleeper inside the
	// channel-push timeout exits promptly.
	done chan struct{}

	closeOnce sync.Once
}

// ═════════════════════════════════════════════════════════════════════
// Per-session registry helpers
// ═════════════════════════════════════════════════════════════════════

// streamIDCounter — monotonic source of unique stream ids. atomic
// so concurrent HttpStream_open calls don't have to sync on a mutex
// for ID generation. Process-wide (NOT per-session); having ids
// unique across the process makes correlating logs / metrics
// trivial. The Sky-side StreamId Int wraps this.
var streamIDCounter atomic.Int64

// sessionlessStreams — fallback registry for streams opened OUTSIDE
// a live session (plain Sky.Http.Server handlers, direct-Task
// callers, tests). Without this, `Http.Stream.close` /
// `Http.Stream.forEachChunk` can't recover the handle from the
// returned StreamId (the per-session map is unreachable).
//
// Driving use case: the synchronous-relay shape (#373) — an HTTP
// handler opens an upstream stream + forEachChunk-drains it
// chunk-for-chunk back to its own client. The handler goroutine
// has NO live session, so the open path needs a global home for
// the handle.
//
// Lifetime: entries are removed on explicit Close / forEachChunk
// exit. A handler that drops the StreamId without closing leaks
// one entry forever (mirrors the Sky-side promise that close is
// the caller's responsibility outside session-managed flows).
var sessionlessStreams sync.Map // map[int64]*streamHandle

// nextStreamID — assigns a fresh non-zero stream id. We skip 0 so a
// zero-valued StreamId (uninitialised model field) can't accidentally
// resolve to a real stream.
func nextStreamID() int64 {
	for {
		id := streamIDCounter.Add(1)
		if id != 0 {
			return id
		}
	}
}

// registerStream attaches a handle to the session map under
// sess.streamsMu. Called from HttpStream_open under the current-session
// resolution path. Pulled out into a helper so the lock discipline
// stays in one place.
//
// sess MAY be nil (HttpStream_open invoked outside a live session
// — direct-Task callers, Sky.Http.Server handler goroutines, tests).
// In that case the handle goes into the process-global
// sessionlessStreams map so close / forEachChunk can still find it
// by id. markDone never reaches it; the caller owns the lifecycle.
func registerStream(sess *liveSession, sh *streamHandle) {
	if sess == nil {
		sessionlessStreams.Store(sh.id, sh)
		return
	}
	sess.streamsMu.Lock()
	if sess.streams == nil {
		sess.streams = map[int64]*streamHandle{}
	}
	sess.streams[sh.id] = sh
	sess.streamsMu.Unlock()
}

// unregisterStream removes the handle from the session map. Safe to
// call with sess==nil OR with an id that's no longer present (the
// idempotent close path on a session whose markDone already swept).
func unregisterStream(sess *liveSession, id int64) {
	if sess == nil {
		sessionlessStreams.Delete(id)
		return
	}
	sess.streamsMu.Lock()
	delete(sess.streams, id)
	sess.streamsMu.Unlock()
}

// lookupStream resolves an id back to its *streamHandle. Returns
// nil if the id was never registered OR the stream was already
// closed + unregistered. Called by:
//
//   - HttpStream_close to find the handle to close.
//   - HttpStream_forEachChunk to find the handle to drain.
//   - setupSubscriptions / drainStreamSub to find the handle whose
//     channel a subscriber should read from.
//
// Resolution order: session-scoped map first (covers Sky.Live);
// falls back to the process-global sessionlessStreams map for
// handler-goroutine / direct-Task callers (covers
// Sky.Http.Server + tests).
func lookupStream(sess *liveSession, id int64) *streamHandle {
	if sess != nil {
		sess.streamsMu.Lock()
		sh := sess.streams[id]
		sess.streamsMu.Unlock()
		if sh != nil {
			return sh
		}
	}
	if v, ok := sessionlessStreams.Load(id); ok {
		return v.(*streamHandle)
	}
	return nil
}

// closeAllStreams walks every active stream on the session, closes
// it, and clears the map. Called from markDone (session terminal
// teardown — TTL eviction / Delete). Returns the count of streams
// it closed so markDone can log a summary line. Idempotent — a
// session whose streams are already swept returns 0.
func closeAllStreams(sess *liveSession) int {
	if sess == nil {
		return 0
	}
	sess.streamsMu.Lock()
	handles := make([]*streamHandle, 0, len(sess.streams))
	for _, sh := range sess.streams {
		if sh != nil {
			handles = append(handles, sh)
		}
	}
	sess.streams = nil
	sess.streamsMu.Unlock()

	for _, sh := range handles {
		sh.Close()
	}
	return len(handles)
}

// ═════════════════════════════════════════════════════════════════════
// streamHandle methods
// ═════════════════════════════════════════════════════════════════════

// Close marks the handle closed, closes the response body, and
// signals the spool goroutine to exit. Idempotent — both
// HttpStream_close from Sky code AND the spool goroutine's
// body-EOF path call it.
//
// We deliberately DO NOT close ch — the drain goroutine selects on
// the handle's `done` channel for teardown; closing ch would race
// with an in-flight spool push and panic (send-on-closed-channel
// is a Go runtime panic, NOT a recoverable). Leaving ch alive +
// signalling teardown via `done` mirrors the pub/sub Broker pattern.
func (sh *streamHandle) Close() {
	sh.closeOnce.Do(func() {
		sh.closed.Store(true)
		if sh.body != nil {
			_ = sh.body.Close()
		}
		close(sh.done)
	})
}

// IsClosed reports whether Close has fired. Used by:
//
//   - The spool goroutine to short-circuit a partial-read after
//     explicit close.
//   - The drain goroutine to drop straggler events whose stream
//     was already torn down.
func (sh *streamHandle) IsClosed() bool {
	return sh.closed.Load()
}

// streamDebug — set true via SKY_STREAM_DEBUG=1 env to print
// per-event timing on the spool + drain paths. Useful for
// diagnosing latency when the dispatch loop falls behind the
// upstream's chunk cadence. Off by default — adds two stderr
// lines per chunk when on.
var streamDebug = os.Getenv("SKY_STREAM_DEBUG") == "1"

// runSpool reads from body in streamReadBuffer-sized chunks and
// pushes streamEvents onto ch. Exits on:
//
//   - body.Read EOF — push streamDoneEv (best-effort; dropped if
//     the channel is full + consumer wedged AND streamConsumerTimeout
//     elapses).
//   - body.Read error — push streamErrEv with the Sky-shaped error
//     wrapped as ErrNetwork.
//   - explicit Close() (sh.done fires) — exit silently; the
//     consumer-facing Done event isn't sent because the caller asked
//     to stop.
//
// Lifecycle: started by HttpStream_open after the response headers
// arrive. One goroutine per active stream.
func (sh *streamHandle) runSpool() {
	defer func() {
		// Defensive recover: a malformed http.Response.Body could
		// panic during Close inside a deferred call; we don't want
		// a spool panic to take down the whole runtime. The error
		// goes to stderr (mirrors the pub/sub decoder-panic path)
		// and the stream is marked closed.
		if r := recover(); r != nil {
			fmt.Printf("[sky.stream] spool panic on stream %d: %v\n", sh.id, r)
			sh.Close()
		}
	}()

	buf := make([]byte, streamReadBuffer)
	startNs := time.Now()
	for {
		if sh.IsClosed() {
			return
		}
		n, err := sh.body.Read(buf)
		if streamDebug {
			fmt.Fprintf(os.Stderr, "[sky.stream/%d] %dms Read n=%d err=%v\n",
				sh.id, time.Since(startNs).Milliseconds(), n, err)
		}
		if n > 0 {
			chunk := string(buf[:n])
			deliveredAt := time.Now()
			if !sh.deliver(streamEvent{kind: streamChunkEv, data: chunk}) {
				// Consumer-stall timeout fired; abandon the stream.
				sh.Close()
				return
			}
			if streamDebug {
				fmt.Fprintf(os.Stderr, "[sky.stream/%d] %dms delivered chunk (took %dms)\n",
					sh.id, time.Since(startNs).Milliseconds(), time.Since(deliveredAt).Milliseconds())
			}
		}
		if err == io.EOF {
			doneAt := time.Now()
			sh.deliver(streamEvent{kind: streamDoneEv})
			if streamDebug {
				fmt.Fprintf(os.Stderr, "[sky.stream/%d] %dms delivered Done (took %dms)\n",
					sh.id, time.Since(startNs).Milliseconds(), time.Since(doneAt).Milliseconds())
			}
			return
		}
		if err != nil {
			// Network read error. Surface to Sky as ErrNetwork; the
			// consumer dispatches Errored Error and decides what to do.
			sh.deliver(streamEvent{kind: streamErrEv, err: ErrNetwork("http.stream read: " + err.Error())})
			sh.Close()
			return
		}
	}
}

// deliver pushes one event onto sh.ch with the consumer-stall
// timeout. Returns true if the event landed (or sh was closed
// mid-push — at which point the caller should exit too); false if
// the consumer stalled past streamConsumerTimeout. False is the
// signal to the spool goroutine to abandon.
func (sh *streamHandle) deliver(ev streamEvent) bool {
	// Fast path: channel has capacity — non-blocking send.
	select {
	case sh.ch <- ev:
		return true
	default:
	}
	// Slow path: channel full. Wait up to streamConsumerTimeout for
	// the consumer to drain, OR for an explicit Close.
	t := time.NewTimer(streamConsumerTimeout)
	defer t.Stop()
	select {
	case sh.ch <- ev:
		return true
	case <-sh.done:
		// Close fired mid-push; treat as delivered so the caller
		// exits cleanly without flagging a stall.
		return true
	case <-t.C:
		fmt.Printf("[sky.stream] consumer stall on stream %d (event kind=%d dropped after %s)\n",
			sh.id, ev.kind, streamConsumerTimeout)
		return false
	}
}

// ═════════════════════════════════════════════════════════════════════
// HTTP client for streaming (deliberately NOT skyHttpClient)
// ═════════════════════════════════════════════════════════════════════

// streamHttpClient — purpose-built client for streaming. Distinguished
// from skyHttpClient by `Timeout: 0` — the whole-request deadline
// would cap a long-lived stream (LLM completions routinely run 30s+).
// The header-stage timeout is enforced separately on the Transport.
var streamHttpClient = newStreamHttpClient()

func newStreamHttpClient() *http.Client {
	return &http.Client{
		Timeout: 0, // NO whole-request timeout — body may stream for minutes
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: streamHeaderTimeout,
			// DisableKeepAlives: prevents the chunked-transfer EOF
			// from being deferred to keep-alive idle timeout. Without
			// this, Go's http.Transport may hold the body Reader
			// open after the upstream closes the chunked stream
			// because the underlying TCP conn is being recycled into
			// the keep-alive pool — body.Read blocks for the conn's
			// idle timeout (~30 s with default settings) instead of
			// returning io.EOF immediately on server close.
			//
			// Streaming responses are one-shot by definition; reusing
			// the conn buys us nothing and costs us EOF latency.
			DisableKeepAlives:     true,
			// IdleConnTimeout, ExpectContinueTimeout etc. inherit
			// from the http stdlib defaults.
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// ═════════════════════════════════════════════════════════════════════
// Sky-facing kernel entries
// ═════════════════════════════════════════════════════════════════════

// HttpStream_open implements:
//
//	Sky.Core.Http.Stream.open : HttpRequest -> Task Error Int
//
// The Sky-side wrapper wraps the returned Int in `StreamId Int`.
//
// Builds the *http.Request from the Sky record (method / url / body /
// headers — same shape Http_request reads), kicks off the request
// (header-stage only), registers the *streamHandle on the current
// session, starts the spool goroutine, and returns the stream id.
//
// Returns a Task — Sky's effect boundary requires Task for every
// observable side-effect. The Task is the standard `func() any`
// shape; on success returns Ok[any, any](int64), on failure
// Err[any, any](ErrNetwork(...)).
func HttpStream_open(reqArg any) any {
	method := strings.ToUpper(fmt.Sprintf("%v", recordField(reqArg, "Method", "method")))
	if method == "" {
		method = "GET"
	}
	url := fmt.Sprintf("%v", recordField(reqArg, "Url", "url"))
	body := fmt.Sprintf("%v", recordField(reqArg, "Body", "body"))
	headers := recordField(reqArg, "Headers", "headers")

	return func() any {
		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, url, bodyReader)
		if err != nil {
			return Err[any, any](ErrNetwork("http.stream.open: " + err.Error()))
		}
		applyHttpHeaders(req, headers)
		// Carry the request's trace context onto the downstream call
		// so spans nest correctly under the originating session.
		req = req.WithContext(CurrentTraceContext())
		InjectTraceHeaders(req)

		resp, err := streamHttpClient.Do(req)
		if err != nil {
			return Err[any, any](ErrNetwork("http.stream.open do: " + err.Error()))
		}
		// HTTP error statuses (4xx/5xx) still surface as a stream —
		// the body might carry the error JSON the user wants to
		// dispatch. The consumer can guard on `Done` and inspect
		// accumulated text; an upstream that returns a body-less
		// 4xx will see one `Done` immediately.
		// (Equivalent to Http.get returning Ok with Status=4xx.)

		sh := &streamHandle{
			id:   nextStreamID(),
			body: resp.Body,
			ch:   make(chan streamEvent, streamChanBuffer),
			done: make(chan struct{}),
		}

		sess := currentLiveSession()
		registerStream(sess, sh)

		go sh.runSpool()
		return Ok[any, any](sh.id)
	}
}

// HttpStream_forEachChunk implements:
//
//	Sky.Core.Http.Stream.forEachChunk : Int -> (String -> Task Error ()) -> Task Error ()
//
// Synchronous-iterator counterpart to the Sub-based `chunks` driver.
// Drains the spool channel for the stream id from the calling
// goroutine, calling the user-supplied `body` Task per chunk.
//
// Bridges Sky.Core.Http.Stream (consumer) to Sky.Http.Server.Stream
// (producer) inside the same Sky.Http.Server handler goroutine —
// `Sub.*` only fires inside Sky.Live update loops, so plain HTTP
// handlers had no way to relay an upstream chunked response
// chunk-for-chunk before this primitive.
//
// Semantics:
//
//   - Clean Done from upstream → return Ok unit.
//   - Upstream Errored e        → return Err e.
//   - body returns Err e        → abort, close the stream, return Err e.
//   - On any exit path the underlying handle is closed (idempotent
//     — safe if the caller also calls close).
//
// Backpressure: body runs synchronously per chunk; if it blocks
// (e.g. on Server.Stream.emit waiting for a slow downstream
// consumer's write buffer), the spool goroutine's bounded
// streamChanBuffer fills + the upstream HTTP client naturally
// throttles. The existing streamConsumerTimeout (30s) is the
// runaway-stall safety net.
func HttpStream_forEachChunk(sidArg any, bodyArg any) any {
	id := asInt64(sidArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupStream(sess, id)
		if sh == nil {
			// Unknown / already-closed stream id — nothing to drain.
			// Match HttpStream_close's idempotent-no-op contract.
			return Ok[any, any](skyUnit())
		}
		// Always close + unregister on exit. Idempotent — the spool
		// goroutine's own close is also safe. unregisterStream covers
		// BOTH the per-session map AND the sessionless global map
		// (lookupStream may have found the handle in either).
		defer func() {
			sh.Close()
			unregisterStream(sess, id)
			// Defense-in-depth: if the handle was opened outside a
			// session but later a session emerged (rare — Sky.Live
			// state-restore path), still scrub the global map.
			sessionlessStreams.Delete(id)
		}()
		for {
			select {
			case ev, open := <-sh.ch:
				if !open {
					// Channel closed without a Done/Errored sentinel —
					// treat as clean EOF (shouldn't happen with the
					// current spool, but be defensive).
					return Ok[any, any](skyUnit())
				}
				switch ev.kind {
				case streamChunkEv:
					// Call user's `body chunk` Task synchronously.
					// SkyCall returns the body's Task value
					// (typically func() any); anyTaskInvoke drives
					// it to a SkyResult.
					taskVal := SkyCall(bodyArg, ev.data)
					res := anyTaskInvoke(taskVal)
					if res.Tag != 0 {
						// body errored — fail-fast: abort iteration
						// and propagate the body's Err upward.
						return Err[any, any](res.ErrValue)
					}
				case streamDoneEv:
					return Ok[any, any](skyUnit())
				case streamErrEv:
					return Err[any, any](ev.err)
				}
			case <-sh.done:
				// Stream was closed out-of-band (e.g. session
				// teardown, explicit Http.Stream.close from a
				// concurrent goroutine). Treat as clean EOF —
				// the caller asked to stop.
				return Ok[any, any](skyUnit())
			}
		}
	}
}

// HttpStream_close implements:
//
//	Sky.Core.Http.Stream.close : Int -> Task Error ()
//
// (The Sky-side wrapper passes the inner Int from StreamId.)
//
// Idempotent — calling on an already-closed / unknown id is a no-op
// that returns Ok[(){}]. Safe from update handlers that receive a
// Done event AND from a teardown branch that always closes regardless
// of the prior message — both paths land cleanly.
func HttpStream_close(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupStream(sess, id)
		if sh != nil {
			sh.Close()
			unregisterStream(sess, id)
		}
		return Ok[any, any](skyUnit())
	}
}

// asInt64 narrows an `any` to int64 for stream ids. Accepts int /
// int32 / int64 / float64 (JSON-decoded number) AND a single-field
// SkyADT (StreamId Int → unwrap to the inner Int).
//
// The SkyADT unwrap covers the call from `Sub.subscribeStream sid`
// when `sid` arrives as the typed `StreamId Int` value — the
// kernel signature can't see the inner Int without it.
//
// Unknown shapes fall through to AsInt (which returns 0 on
// unrecognised types, so lookupStream then resolves to nil →
// idempotent no-op in close).
func asInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	// SkyADT wrap: type StreamId = StreamId Int — the runtime ADT
	// is `SkyADT{Tag:0, SkyName:"StreamId", Fields:[42]}`. Pull
	// Fields[0] and re-coerce.
	if adt, ok := v.(SkyADT); ok && len(adt.Fields) == 1 {
		return asInt64(adt.Fields[0])
	}
	// User-defined-ADT struct fallback. Sky's codegen emits
	// `type StreamId struct{Tag int; SkyName string; Fields []any}`
	// — same layout as SkyADT but a distinct Go type. Reflect to
	// extract Fields[0] when the type-assertion to SkyADT fails.
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Kind() == reflect.Struct {
		fieldsF := rv.FieldByName("Fields")
		if fieldsF.IsValid() && fieldsF.Kind() == reflect.Slice && fieldsF.Len() == 1 {
			return asInt64(fieldsF.Index(0).Interface())
		}
	}
	return int64(AsInt(v))
}

// skyUnit returns the Sky `()` (empty tuple) value. The Go runtime
// uses an empty struct as the canonical representation.
func skyUnit() any { return struct{}{} }

// ═════════════════════════════════════════════════════════════════════
// Subscriber-side wiring — drains streamHandle.ch + dispatches msgs
// ═════════════════════════════════════════════════════════════════════

// streamSubReg is one live `Http.Stream.chunks` subscription on a
// session. Mirrors subRegistration's shape — toMsg is the user's
// `ChunkEvent -> msg` decoder; cancel is the idempotent teardown
// closure that stops the drain goroutine.
type streamSubReg struct {
	streamID int64
	toMsg    any
	cancel   func()
}

// diffStreamSubs computes the (added, removed) stream-id sets between
// the CURRENT live registrations (`old`) and the DESIRED set
// (`desired`). Pure function — no I/O. Symmetric counterpart to
// diffSubscriptions (live_topics.go).
func diffStreamSubs(old map[int64]*streamSubReg, desired map[int64]any) (added, removed []int64) {
	for id := range desired {
		if _, ok := old[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range old {
		if _, ok := desired[id]; !ok {
			removed = append(removed, id)
		}
	}
	return added, removed
}

// ─── ChunkEvent ADT construction ──────────────────────────────────────

// The Sky-side type is:
//
//	type ChunkEvent
//	    = Chunk String
//	    | Done
//	    | Errored Error
//
// The runtime constructs ADT values via SkyADT (rt.go's
// canonical constructor used by every other Sky ADT). Tag indices
// follow declaration order in the Sky source.

const (
	chunkEventChunkTag   = 0
	chunkEventDoneTag    = 1
	chunkEventErroredTag = 2
)

// buildChunkEventValue constructs a ChunkEvent ADT value from a
// streamEvent. The result is the `any` value the user's toMsg
// decoder receives.
func buildChunkEventValue(ev streamEvent) any {
	switch ev.kind {
	case streamChunkEv:
		return SkyADT{
			Tag:     chunkEventChunkTag,
			SkyName: "Chunk",
			Fields:  []any{ev.data},
		}
	case streamDoneEv:
		return SkyADT{
			Tag:     chunkEventDoneTag,
			SkyName: "Done",
			Fields:  nil,
		}
	case streamErrEv:
		return SkyADT{
			Tag:     chunkEventErroredTag,
			SkyName: "Errored",
			Fields:  []any{ev.err},
		}
	}
	return nil
}

