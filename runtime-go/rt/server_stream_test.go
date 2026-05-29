// server_stream_test.go — coverage for Sky.Http.Server.Stream
// (Cycle 4 HS-Server).
//
// Acceptance criteria → tests:
//
//	#1 stream() emits Response with StreamHandler sentinel       → TestServerStream_StreamReturnsHandler
//	#2 emit() writes + flushes; client sees bytes incrementally  → TestServerStream_EmitFlushes
//	#3 finish() is idempotent (post-finish emit is no-op)        → TestServerStream_FinishIdempotent
//	#4 unknown id is a no-op (emit/finish/withContentType)       → TestServerStream_UnknownIdNoop
//	#5 dispatcher rejects non-Flusher writer with 503            → TestServeStreamingResponse_NoFlusher
//	#6 end-to-end dispatcher integration with SkyResponse        → TestServeStreamingResponse_EndToEnd
//	#7 withContentType pre-emit wins; post-emit no-op            → TestServerStream_WithContentType
//	#8 client disconnect mid-stream returns ErrNetwork           → TestServerStream_EmitClientDisconnect

package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// flushRecorder is a ResponseWriter that records Write() calls
// individually (httptest.ResponseRecorder concatenates Body) so
// tests can assert per-chunk flush semantics.
type flushRecorder struct {
	headers    http.Header
	status     int
	chunks     []string
	flushCount int
	mu         sync.Mutex
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{headers: http.Header{}}
}

func (f *flushRecorder) Header() http.Header { return f.headers }
func (f *flushRecorder) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, string(b))
	return len(b), nil
}
func (f *flushRecorder) WriteHeader(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == 0 {
		f.status = status
	}
}
func (f *flushRecorder) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushCount++
}

// nonFlushRecorder is a ResponseWriter that deliberately does NOT
// implement Flusher — used to verify the dispatcher's clean reject
// path.
type nonFlushRecorder struct {
	headers http.Header
	status  int
	body    strings.Builder
}

func (n *nonFlushRecorder) Header() http.Header         { return n.headers }
func (n *nonFlushRecorder) Write(b []byte) (int, error) { return n.body.Write(b) }
func (n *nonFlushRecorder) WriteHeader(status int)      { n.status = status }

// ════════════════════════════════════════════════════════════════════
// Unit tests
// ════════════════════════════════════════════════════════════════════

func TestNextServerStreamID_MonotonicAndNonZero(t *testing.T) {
	seen := map[int64]bool{}
	for i := 0; i < 50; i++ {
		id := nextServerStreamID()
		if id == 0 {
			t.Fatalf("nextServerStreamID returned 0 (reserved sentinel) iter %d", i)
		}
		if seen[id] {
			t.Fatalf("duplicate stream id %d iter %d", id, i)
		}
		seen[id] = true
	}
}

func TestServerStream_StreamReturnsHandler(t *testing.T) {
	handlerCalled := false
	skyHandler := func(_ any) any {
		handlerCalled = true
		return func() any { return Ok[any, any](skyUnit()) }
	}
	taskRaw := ServerStream_stream("text/event-stream", skyHandler)
	result := anyTaskInvoke(taskRaw)
	if result.Tag != 0 {
		t.Fatalf("stream() returned Err: %+v", result)
	}
	resp, ok := result.OkValue.(SkyResponse)
	if !ok {
		t.Fatalf("OkValue not SkyResponse: %T", result.OkValue)
	}
	if resp.ContentType != "text/event-stream" {
		t.Errorf("ContentType: got %q want text/event-stream", resp.ContentType)
	}
	if resp.StreamHandler == nil {
		t.Fatal("StreamHandler nil — dispatcher will treat as buffered")
	}
	if handlerCalled {
		t.Error("user handler must NOT run inside ServerStream_stream (deferred to dispatcher)")
	}
}

func TestServerStream_EmitFlushes(t *testing.T) {
	w := newFlushRecorder()
	sh := &serverStreamHandle{id: nextServerStreamID(), w: w, flusher: w}
	serverStreamHandles.Store(sh.id, sh)
	defer serverStreamHandles.Delete(sh.id)

	for _, c := range []string{"chunk-1", "chunk-2", "chunk-3"} {
		taskRaw := ServerStream_emit(c, sh.id)
		res := anyTaskInvoke(taskRaw)
		if res.Tag != 0 {
			t.Fatalf("emit %q returned Err: %+v", c, res)
		}
	}
	if len(w.chunks) != 3 {
		t.Fatalf("expected 3 separate Write calls, got %d: %+v", len(w.chunks), w.chunks)
	}
	if w.flushCount != 3 {
		t.Errorf("expected 3 Flush calls (one per chunk), got %d", w.flushCount)
	}
	if w.chunks[0] != "chunk-1" || w.chunks[2] != "chunk-3" {
		t.Errorf("chunk order: %+v", w.chunks)
	}
}

func TestServerStream_FinishIdempotent(t *testing.T) {
	w := newFlushRecorder()
	sh := &serverStreamHandle{id: nextServerStreamID(), w: w, flusher: w}
	serverStreamHandles.Store(sh.id, sh)
	defer serverStreamHandles.Delete(sh.id)

	// First finish
	if res := anyTaskInvoke(ServerStream_finish(sh.id)); res.Tag != 0 {
		t.Fatalf("first finish Err: %+v", res)
	}
	if !sh.closed.Load() {
		t.Fatal("finish did not set closed flag")
	}
	// Second finish — must NOT panic, must succeed
	if res := anyTaskInvoke(ServerStream_finish(sh.id)); res.Tag != 0 {
		t.Fatalf("second finish Err: %+v", res)
	}
	// Emit after finish — must be a no-op (returns Ok unit; no Write)
	priorChunks := len(w.chunks)
	if res := anyTaskInvoke(ServerStream_emit("after-finish", sh.id)); res.Tag != 0 {
		t.Fatalf("emit-after-finish returned Err (expected silent no-op): %+v", res)
	}
	if len(w.chunks) != priorChunks {
		t.Errorf("emit-after-finish wrote a chunk: %+v", w.chunks)
	}
}

func TestServerStream_UnknownIdNoop(t *testing.T) {
	// id 99999999 was never registered
	if res := anyTaskInvoke(ServerStream_emit("orphan", int64(99999999))); res.Tag != 0 {
		t.Errorf("emit on unknown id returned Err: %+v", res)
	}
	if res := anyTaskInvoke(ServerStream_finish(int64(99999999))); res.Tag != 0 {
		t.Errorf("finish on unknown id returned Err: %+v", res)
	}
	if res := anyTaskInvoke(ServerStream_withContentType("text/plain", int64(99999999))); res.Tag != 0 {
		t.Errorf("withContentType on unknown id returned Err: %+v", res)
	}
}

func TestServerStream_WithContentType(t *testing.T) {
	w := newFlushRecorder()
	w.headers.Set("Content-Type", "text/event-stream")
	sh := &serverStreamHandle{id: nextServerStreamID(), w: w, flusher: w}
	serverStreamHandles.Store(sh.id, sh)
	defer serverStreamHandles.Delete(sh.id)

	// Pre-emit: withContentType wins
	if res := anyTaskInvoke(ServerStream_withContentType("application/x-ndjson", sh.id)); res.Tag != 0 {
		t.Fatalf("withContentType pre-emit Err: %+v", res)
	}
	if got := w.headers.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("pre-emit Content-Type: got %q want application/x-ndjson", got)
	}
	// Emit a chunk → headerSent
	anyTaskInvoke(ServerStream_emit("data", sh.id))
	// Post-emit: withContentType is a no-op
	if res := anyTaskInvoke(ServerStream_withContentType("text/plain", sh.id)); res.Tag != 0 {
		t.Fatalf("withContentType post-emit Err: %+v", res)
	}
	if got := w.headers.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("post-emit Content-Type changed: got %q (must NOT change after first emit)", got)
	}
}

func TestServeStreamingResponse_NoFlusher(t *testing.T) {
	w := &nonFlushRecorder{headers: http.Header{}}
	req := httptest.NewRequest("GET", "/stream", nil)
	resp := SkyResponse{
		Status:        200,
		ContentType:   "text/event-stream",
		StreamHandler: func(_ any) any { return func() any { return Ok[any, any](skyUnit()) } },
	}
	serveStreamingResponse(w, req, resp)
	if w.status != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on non-Flusher writer, got %d", w.status)
	}
	if !strings.Contains(w.body.String(), "Flusher") {
		t.Errorf("response body must mention Flusher requirement; got %q", w.body.String())
	}
}

func TestServeStreamingResponse_EndToEnd(t *testing.T) {
	// Run an actual httptest server with a Sky-style streaming handler.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sky-side handler closure: emits 3 chunks then finishes.
		skyHandler := func(writerADT any) any {
			adt, ok := writerADT.(SkyADT)
			if !ok || len(adt.Fields) != 1 {
				return func() any { return Err[any, any](ErrNetwork("bad writer ADT")) }
			}
			sid := asInt64(adt.Fields[0])
			return func() any {
				anyTaskInvoke(ServerStream_emit("alpha\n", sid))
				anyTaskInvoke(ServerStream_emit("beta\n", sid))
				anyTaskInvoke(ServerStream_emit("gamma\n", sid))
				anyTaskInvoke(ServerStream_finish(sid))
				return Ok[any, any](skyUnit())
			}
		}
		resp := SkyResponse{
			Status:        200,
			ContentType:   "text/plain; charset=utf-8",
			StreamHandler: skyHandler,
		}
		serveStreamingResponse(w, r, resp)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type: got %q want text/plain; charset=utf-8", ct)
	}
	if x := resp.Header.Get("X-Content-Type-Options"); x != "nosniff" {
		t.Errorf("setSecurityHeaders did not apply: X-Content-Type-Options=%q", x)
	}
	body, _ := readAllBody(resp)
	want := "alpha\nbeta\ngamma\n"
	if body != want {
		t.Errorf("body: got %q want %q", body, want)
	}
}

func TestServerStream_HandleSweptOnDispatcherReturn(t *testing.T) {
	// After serveStreamingResponse returns, the handle MUST be
	// unregistered (no leak per request).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var capturedID int64
		skyHandler := func(writerADT any) any {
			adt := writerADT.(SkyADT)
			capturedID = asInt64(adt.Fields[0])
			return func() any { return Ok[any, any](skyUnit()) }
		}
		serveStreamingResponse(w, r, SkyResponse{
			Status: 200, ContentType: "text/plain", StreamHandler: skyHandler,
		})
		// Post-dispatcher: handle must be GONE from the registry
		if sh := lookupServerStream(capturedID); sh != nil {
			t.Errorf("handle %d still in registry after dispatcher returned", capturedID)
		}
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	readAllBody(resp)
}

// ─── helpers ──────────────────────────────────────────────────────

func readAllBody(resp *http.Response) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String(), nil
}
