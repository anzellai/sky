package rt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// countSyncMap returns the number of live entries in a sync.Map.
func countSyncMap(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// fakeReadCloser yields its bytes once, then io.EOF, and records Close.
type fakeReadCloser struct {
	data   []byte
	off    int
	closed atomic.Bool
}

func (f *fakeReadCloser) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}

func (f *fakeReadCloser) Close() error { f.closed.Store(true); return nil }

// ─────────────────────────────────────────────────────────────────────
// B1 — pendingWebSocketCfgs must not register until the Task RUNS, and
// asSkyResponse must drain it back to baseline.
// ─────────────────────────────────────────────────────────────────────

func TestPendingWsCfgNotRegisteredUntilTaskRuns(t *testing.T) {
	base := countSyncMap(&pendingWebSocketCfgs)

	cfgArg := map[string]any{
		"onConnect": nil, "onMessage": nil, "onClose": nil, "onError": nil,
		"maxMessageBytes": 1048576, "originPatterns": []any{},
	}
	// BUILD the Task value but do NOT run it. Before the fix this alone
	// registered a cfg (plus four closures) that nothing would ever drain.
	taskVal := ServerWebSocket_upgrade(nil, cfgArg)
	if got := countSyncMap(&pendingWebSocketCfgs); got != base {
		t.Fatalf("building an unrun upgrade Task registered a cfg: base=%d now=%d", base, got)
	}

	taskFn, ok := taskVal.(func() any)
	if !ok {
		t.Fatalf("ServerWebSocket_upgrade did not return a Task func, got %T", taskVal)
	}
	res := taskFn()
	if got := countSyncMap(&pendingWebSocketCfgs); got != base+1 {
		t.Fatalf("running the Task must register exactly one cfg: base=%d now=%d", base, got)
	}

	// asSkyResponse (what EVERY dispatcher runs the response through) must
	// resolve + drain the token, so the Live api dispatcher no longer leaks.
	skyRes, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Task did not return SkyResult, got %T", res)
	}
	resp, ok := asSkyResponse(skyRes.OkValue)
	if !ok {
		t.Fatal("asSkyResponse did not recognise the upgrade response")
	}
	if resp.WSUpgrade == nil {
		t.Fatal("asSkyResponse must resolve the ws token into WSUpgrade")
	}
	if strings.HasPrefix(resp.Body, pendingWebSocketSentinelPrefix) {
		t.Fatalf("asSkyResponse must clear the sentinel from Body, still have %q", resp.Body)
	}
	if got := countSyncMap(&pendingWebSocketCfgs); got != base {
		t.Fatalf("asSkyResponse must drain the ws cfg back to baseline: base=%d now=%d", base, got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// B2 — runSpool must Close the handle (and its body) on a CLEAN EOF, not
// only on a network error.
// ─────────────────────────────────────────────────────────────────────

func TestSpoolClosesHandleAndBodyOnCleanEOF(t *testing.T) {
	fb := &fakeReadCloser{data: []byte("hello world")}
	sh := &streamHandle{
		id:   nextStreamID(),
		body: fb,
		ch:   make(chan streamEvent, streamChanBuffer),
		done: make(chan struct{}),
	}
	sh.runSpool()

	if !sh.IsClosed() {
		t.Fatal("clean EOF must Close the handle (doc on Close claims the body-EOF path does)")
	}
	if !fb.closed.Load() {
		t.Fatal("clean EOF must close the response body — otherwise its transport goroutine + socket leak")
	}
}

// ─────────────────────────────────────────────────────────────────────
// B3 — the sessionless registries must be reaped: closed-but-mapped
// entries reclaimed, idle-expired open handles closed + evicted.
// ─────────────────────────────────────────────────────────────────────

func TestSessionlessStreamReaperReclaimsClosed(t *testing.T) {
	base := countSyncMap(&sessionlessStreams)
	sh := &streamHandle{
		id:   nextStreamID(),
		ch:   make(chan streamEvent, 1),
		done: make(chan struct{}),
	}
	registerStream(nil, sh)
	if got := countSyncMap(&sessionlessStreams); got != base+1 {
		t.Fatalf("sessionless register should add one: base=%d now=%d", base, got)
	}
	// The post-EOF-without-drain shape: handle closed, map entry orphaned.
	sh.Close()
	sweepSessionless(time.Now())
	if got := countSyncMap(&sessionlessStreams); got != base {
		t.Fatalf("reaper must reclaim a closed-but-mapped sessionless stream: base=%d now=%d", base, got)
	}
}

func TestSessionlessStreamReaperClosesIdle(t *testing.T) {
	base := countSyncMap(&sessionlessStreams)
	fb := &fakeReadCloser{}
	sh := &streamHandle{
		id:   nextStreamID(),
		body: fb,
		ch:   make(chan streamEvent, 1),
		done: make(chan struct{}),
	}
	registerStream(nil, sh)

	// A fresh handle is NOT reaped.
	sweepSessionless(time.Now())
	if got := countSyncMap(&sessionlessStreams); got != base+1 {
		t.Fatalf("a fresh open sessionless stream must not be reaped: base=%d now=%d", base, got)
	}
	// Past the idle TTL it IS reaped and closed.
	sweepSessionless(time.Now().Add(2 * sessionlessIdleTTL))
	if got := countSyncMap(&sessionlessStreams); got != base {
		t.Fatalf("reaper must reap an idle-expired open sessionless stream: base=%d now=%d", base, got)
	}
	if !sh.IsClosed() {
		t.Fatal("reaping an open handle must Close it")
	}
	if !fb.closed.Load() {
		t.Fatal("reaping an open stream must close its body")
	}
}

func TestSessionlessSocketReaperReclaimsClosedAndIdle(t *testing.T) {
	base := countSyncMap(&sessionlessSockets)

	// closed-but-mapped
	ctx1, cancel1 := context.WithCancel(context.Background())
	c1 := &wsHandle{id: nextWsID(), ch: make(chan wsEvent, 1), ctx: ctx1, cancel: cancel1, done: make(chan struct{})}
	registerWs(nil, c1)
	c1.Close()

	// idle-expired open
	ctx2, cancel2 := context.WithCancel(context.Background())
	c2 := &wsHandle{id: nextWsID(), ch: make(chan wsEvent, 1), ctx: ctx2, cancel: cancel2, done: make(chan struct{})}
	registerWs(nil, c2)

	if got := countSyncMap(&sessionlessSockets); got != base+2 {
		t.Fatalf("expected two sessionless sockets registered: base=%d now=%d", base, got)
	}
	sweepSessionless(time.Now().Add(2 * sessionlessIdleTTL))
	if got := countSyncMap(&sessionlessSockets); got != base {
		t.Fatalf("reaper must clear both a closed and an idle-expired sessionless socket: base=%d now=%d", base, got)
	}
	if !c2.IsClosed() {
		t.Fatal("reaping an open socket must Close it")
	}
}

// ─────────────────────────────────────────────────────────────────────
// B4 — serveWebSocketUpgrade must return PROMPTLY after the peer drops,
// not stall up to pingInterval waiting on the heartbeat goroutine.
// ─────────────────────────────────────────────────────────────────────

func TestServeWebSocketUpgradeReturnsPromptlyAfterPeerDrop(t *testing.T) {
	clearBindEnv(t) // ENV unset → dev → serveWebSocketUpgrade allows all origins

	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWebSocketUpgrade(w, r, webSocketUpgradeCfg{maxMessageBytes: wsDefaultMaxMessageBytes})
		close(done)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	c, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Abruptly drop the peer (no close handshake).
	c.CloseNow()

	select {
	case <-done:
		// Returned quickly: h.Close() ran (via the swapped defer) BEFORE the
		// <-hbDone wait, stopping the heartbeat at once.
	case <-time.After(10 * time.Second):
		t.Fatal("serveWebSocketUpgrade did not return within 10s of peer drop — heartbeat defer-order stall (pingInterval is 30s)")
	}
}
