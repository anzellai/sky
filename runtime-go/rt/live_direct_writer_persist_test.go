package rt

// Phase-5d / G2 (grill A1 — acked-then-lost, direct-writer arm).
//
// persistAndShipFrame is documented as "the SINGLE durability funnel": any path
// that mutates sess.model, advances localSeq and ACKs a frame to the client must
// persist the session BEFORE the ack. Otherwise the client's
// __skyLastAppliedSeq advances past the persisted OutSeq and, after a restart,
// the client SILENTLY DISCARDS every replayed frame (live_store.go — a frame
// whose seq <= __skyLastAppliedSeq is dropped). The page freezes permanently,
// with no error, until a hard reload.
//
// The funnel only ever covered the sess.sseCh arm. THREE ack paths write the
// frame DIRECTLY to the http.ResponseWriter and so slipped past both the funnel
// and the structural tripwire (which greps for `sseCh <-` sends only):
//
//	G-1  handleSSE reconnect-resync — mutates sess.model (route reconcile),
//	     commitRender, advances localSeq, writes `event: patch` to w … and
//	     THEN calls app.store.Set. The exact inversion the funnel exists to
//	     prevent (and un-nil-guarded, unlike the funnel).
//	G-2  handleSSE drop-resync (renderResyncFrame) — commitRender, advances
//	     localSeq, writes `event: patch` to w, and NEVER persists. It fires
//	     precisely when the client is already diverged.
//	G-3  handleEvent desync arm — commitRender, nextLocalSeq, writeEventHTML,
//	     then `return` — BEFORE handleEvent's store.Set. Seq advanced and a
//	     full body acked, unpersisted.
//
// Each test below proves ORDER, not just presence: the store spy records how
// much of the SSE/HTTP response had already been written at the moment the
// persist happened. A persist that lands AFTER the first byte of the ack is a
// crash window in which the client has seen a change the server can lose.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── harness ──────────────────────────────────────────────────────────────

// recordingStore wraps a real store and records every Set, plus an onSet hook
// that runs BEFORE the inner Set so a test can observe what the client had
// already been sent at persist time.
type recordingStore struct {
	inner SessionStore
	mu    sync.Mutex
	sets  int
	onSet func()
}

func newRecordingStore() *recordingStore {
	return &recordingStore{inner: newMemoryStore(30 * time.Minute)}
}

func (s *recordingStore) Get(sid string) (*liveSession, bool) { return s.inner.Get(sid) }

func (s *recordingStore) Set(sid string, sess *liveSession) {
	s.mu.Lock()
	s.sets++
	hook := s.onSet
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	s.inner.Set(sid, sess)
}

func (s *recordingStore) Delete(sid string) { s.inner.Delete(sid) }
func (s *recordingStore) NewID() string     { return s.inner.NewID() }
func (s *recordingStore) Close() error      { return s.inner.Close() }
func (s *recordingStore) Broker() Broker    { return s.inner.Broker() }
func (s *recordingStore) Ping() error       { return s.inner.Ping() }

func (s *recordingStore) setCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sets
}

func (s *recordingStore) setHook(f func()) {
	s.mu.Lock()
	s.onSet = f
	s.mu.Unlock()
}

// sseRecorder is a goroutine-safe http.ResponseWriter + http.Flusher. The
// stdlib httptest.ResponseRecorder is NOT safe to read while handleSSE writes
// from another goroutine, which is exactly what an SSE test must do.
type sseRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
	hdr http.Header
}

func newSSERecorder() *sseRecorder { return &sseRecorder{hdr: http.Header{}} }

func (r *sseRecorder) Header() http.Header { return r.hdr }

func (r *sseRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}

func (r *sseRecorder) WriteHeader(int) {}
func (r *sseRecorder) Flush()          {}

func (r *sseRecorder) patchFrames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Count(r.buf.String(), "event: patch")
}

// waitForPatchFrames spins until the recorder has seen n `event: patch` frames.
func (r *sseRecorder) waitForPatchFrames(t *testing.T, n int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.patchFrames() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d %s frame(s); got %d", n, what, r.patchFrames())
}

// counterApp builds a minimal Sky.Live app whose view changes with the model.
func counterApp(store SessionStore) *liveApp {
	return &liveApp{
		store: store,
		update: func(_ any, model any) any {
			n, _ := model.(int)
			return SkyTuple2{V0: n + 1, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			n, _ := model.(int)
			return velement("div", nil, []any{
				velement("button",
					[]any{eventPair{name: "click", msg: "Bump"}},
					[]any{vtext("n" + itoa(n))}),
			})
		},
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
}

func newTestSession(sid string) *liveSession {
	return &liveSession{
		sid:       sid,
		model:     0,
		handlers:  map[string]any{},
		sseCh:     make(chan sseFrame, 8),
		cancelSub: make(chan struct{}),
	}
}

// ── G-1: handleSSE reconnect-resync ──────────────────────────────────────

// TestSSEReconnectResync_PersistsBeforeAck. Every fresh SSE connection
// re-renders the current view (after reconciling sess.model with the
// connection's ?path) and pushes it as a full-body frame with a FRESH seq. The
// pre-fix code wrote that frame to w and only THEN called app.store.Set — so a
// crash in that window leaves the store at the old OutSeq while the browser has
// already advanced __skyLastAppliedSeq. On restart every replayed frame is
// silently dropped and the page is frozen until a hard reload.
func TestSSEReconnectResync_PersistsBeforeAck(t *testing.T) {
	store := newRecordingStore()
	app := counterApp(store)
	sess := newTestSession("sid-resync")
	// Bootstrap a render so the session has a prevTree/handler map, exactly as
	// a live session that has already served a page would.
	_ = app.dispatch(sess, 0)
	store.Set("sid-resync", sess)

	w := newSSERecorder()
	var patchesAtFirstPersist = -1
	var once sync.Once
	store.setHook(func() {
		once.Do(func() { patchesAtFirstPersist = w.patchFrames() })
	})
	before := store.setCount()

	req := httptest.NewRequest(http.MethodGet, "/_sky/live?tab=t1", nil)
	req.Header.Set("Cookie", "sky_sid=sid-resync")
	ctx, cancel := context.WithCancel(req.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.handleSSE(w, req.WithContext(ctx))
	}()
	w.waitForPatchFrames(t, 1, "reconnect-resync")
	cancel()
	<-done

	if store.setCount() <= before {
		t.Fatal("G-1 acked-then-lost: the reconnect-resync advanced localSeq and shipped a " +
			"full-body frame but never persisted the session")
	}
	if patchesAtFirstPersist != 0 {
		t.Fatalf("G-1 ACK BEFORE PERSIST: the resync frame was already on the wire (%d patch "+
			"frame(s) written) when store.Set finally ran. A crash in that window leaves the "+
			"persisted OutSeq behind the client's __skyLastAppliedSeq; after restart the client "+
			"silently discards every replayed frame and the page freezes with no error. Route "+
			"the write through the durability funnel (persist BEFORE the ack).",
			patchesAtFirstPersist)
	}
}

// ── G-2: handleSSE drop-resync ───────────────────────────────────────────

// TestSSEDropResync_PersistsBeforeAck. When a frame is dropped by a full
// per-connection buffer the connection is flagged out-of-sync and the loop
// ships a fresh full-body frame straight to w (bypassing sess.sseCh so a full
// buffer cannot block the correction). That path re-renders, advances localSeq
// and NEVER persisted — and it fires exactly when the client is already
// diverged, so the store's OutSeq lags what the client actually applied.
func TestSSEDropResync_PersistsBeforeAck(t *testing.T) {
	store := newRecordingStore()
	app := counterApp(store)
	sess := newTestSession("sid-drop")
	_ = app.dispatch(sess, 0)
	store.Set("sid-drop", sess)

	w := newSSERecorder()
	req := httptest.NewRequest(http.MethodGet, "/_sky/live?tab=t1", nil)
	req.Header.Set("Cookie", "sky_sid=sid-drop")
	ctx, cancel := context.WithCancel(req.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.handleSSE(w, req.WithContext(ctx))
	}()
	// Let the reconnect-resync (G-1's path) settle first so the counters below
	// isolate the DROP-resync.
	w.waitForPatchFrames(t, 1, "reconnect-resync")

	beforeDrop := store.setCount()
	patchesAtDropPersist := -1
	var once sync.Once
	store.setHook(func() {
		once.Do(func() { patchesAtDropPersist = w.patchFrames() })
	})

	// Simulate the ingress drop that flags every connection out-of-sync.
	sess.markAllConnsOutOfSync()
	w.waitForPatchFrames(t, 2, "drop-resync")
	cancel()
	<-done

	if store.setCount() <= beforeDrop {
		t.Fatal("G-2 acked-then-lost: the drop-resync re-rendered, advanced localSeq and wrote a " +
			"full-body frame directly to the SSE stream but NEVER persisted. The client's " +
			"__skyLastAppliedSeq now leads the persisted OutSeq; after a restart every replayed " +
			"frame is silently dropped and the page freezes. Route it through the durability funnel.")
	}
	if patchesAtDropPersist != 1 {
		t.Fatalf("G-2 ACK BEFORE PERSIST: %d patch frame(s) had been written when the persist ran "+
			"(want 1 — i.e. the persist landed BEFORE the drop-resync frame reached the wire)",
			patchesAtDropPersist)
	}
}

// ── G-3: handleEvent desync arm ──────────────────────────────────────────

// TestHandleEventDesync_PersistsBeforeAck. A handler-id miss (deploy changed
// the view, or the DOM went stale after an SSE drop) re-renders the CURRENT
// view, advances localSeq and returns the whole body with X-Sky-Status: desync
// — then `return`s, BEFORE handleEvent's own store.Set. The client applies the
// body and advances its seq; the server persisted nothing.
func TestHandleEventDesync_PersistsBeforeAck(t *testing.T) {
	store := newRecordingStore()
	app := counterApp(store)
	sess := newTestSession("sid-desync")
	_ = app.dispatch(sess, 0)
	store.Set("sid-desync", sess)

	rr := httptest.NewRecorder()
	bytesAtPersist := -1
	var once sync.Once
	store.setHook(func() {
		once.Do(func() { bytesAtPersist = rr.Body.Len() })
	})
	before := store.setCount()

	// A STALE handler id: the session is valid, the handler map has no such id.
	body := `{"sessionId":"sid-desync","seq":1,"msg":"Bump","args":[],"handlerId":"r_gone_9_button_0.click"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "sky_sid=sid-desync")
	app.handleEvent(rr, req)

	if got := rr.Header().Get("X-Sky-Status"); got != "desync" {
		t.Fatalf("fixture broken: X-Sky-Status = %q, want \"desync\" (the assertions below would be vacuous)", got)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("fixture broken: no body acked, so there is no acked-then-lost hazard to assert")
	}
	if store.setCount() <= before {
		t.Fatal("G-3 acked-then-lost: the handleEvent desync arm re-rendered, advanced localSeq via " +
			"nextLocalSeq and acked a FULL BODY to the client, then returned before handleEvent's " +
			"store.Set. The client's __skyLastAppliedSeq advances past the persisted OutSeq; after a " +
			"restart the client silently discards every replayed frame. Route the reply through the " +
			"durability funnel (persist BEFORE writeEventHTML).")
	}
	if bytesAtPersist != 0 {
		t.Fatalf("G-3 ACK BEFORE PERSIST: %d response byte(s) had already been written when the "+
			"persist ran (want 0)", bytesAtPersist)
	}
}
