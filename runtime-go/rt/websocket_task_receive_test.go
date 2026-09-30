//go:build !js

package rt

// Task-based WebSocket client receive (v0.27): `WebSocket.receive`,
// `receiveWithin` and `forEachMessage` for programs that are not a TEA loop.
//
// Before these existed the client could only be read through a Sub, so a Task
// `main` had no way to read a frame at all, and a socket nobody drained
// filled its 64-slot queue, waited 30 s and closed itself. The tests below
// drive a real client against a real server (coder/websocket on httptest).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsScriptServer runs `script` against every accepted connection.
func wsScriptServer(t *testing.T, script func(ctx context.Context, conn *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "test cleanup")
		script(r.Context(), conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// wsTaskConnect dials through the runtime and returns the socket id.
func wsTaskConnect(t *testing.T, url string, ping time.Duration) int64 {
	t.Helper()
	res := doWebSocketConnect(url, nil, nil, 5*time.Second, ping).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("connect failed: %v", res.ErrValue)
	}
	id := res.OkValue.(int64)
	t.Cleanup(func() { runWsTask(WebSocket_close(id)) })
	return id
}

// wsTaskConnectIn connects inside a Sky.Live session, so the socket is that
// session's (a session reaches only its own sockets).
func wsTaskConnectIn(t *testing.T, sess *liveSession, url string) int64 {
	t.Helper()
	var res SkyResult[any, any]
	runWithLiveSession(sess, func() {
		res = doWebSocketConnect(url, nil, nil, 5*time.Second, 0).(SkyResult[any, any])
	})
	if res.Tag != 0 {
		t.Fatalf("connect failed: %v", res.ErrValue)
	}
	id := res.OkValue.(int64)
	t.Cleanup(func() { runWithLiveSession(sess, func() { runWsTask(WebSocket_close(id)) }) })
	return id
}

func runWsTask(task any) SkyResult[any, any] {
	return task.(func() any)().(SkyResult[any, any])
}

// wsReceived decodes one `receive` result into a readable form:
// "Text:<s>" / "Binary:<s>" / "Nothing" / "Err:<kind>".
func wsReceived(res SkyResult[any, any]) string {
	if res.Tag != 0 {
		return "Err:" + errorKindName(res.ErrValue)
	}
	m := res.OkValue.(SkyMaybe[any])
	if m.Tag != 0 {
		return "Nothing"
	}
	adt := m.JustValue.(SkyADT)
	return adt.SkyName + ":" + adt.Fields[0].(string)
}

// errorKindName reads the kind constructor out of a Sky Error value. The
// kind is carried as its enum tag (errorKindAdt).
func errorKindName(e any) string {
	adt, ok := e.(skyErrorAdt)
	if !ok || len(adt.Fields) == 0 {
		return fmt.Sprintf("%+v", e)
	}
	names := map[int]string{1: "Network", 4: "Timeout", 7: "InvalidInput", 9: "Unavailable"}
	if tag, isInt := adt.Fields[0].(int); isInt {
		if n, known := names[tag]; known {
			return n
		}
	}
	return fmt.Sprintf("%+v", e)
}

// withWsTestKnobs shrinks the queue and the timeouts for one test.
func withWsTestKnobs(t *testing.T, queue int, stall, pingTimeout time.Duration) {
	t.Helper()
	oldQ, oldStall, oldPing := wsReadChanCap, wsConsumerStallTimeout, wsPingTimeout
	wsReadChanCap, wsConsumerStallTimeout, wsPingTimeout = queue, stall, pingTimeout
	t.Cleanup(func() {
		wsReadChanCap, wsConsumerStallTimeout, wsPingTimeout = oldQ, oldStall, oldPing
	})
}

// TestWsReceive_NothingAfterServerClose — a frame arrives as Just; after the
// server closes cleanly every later receive is Nothing (not an error, not a
// hang), however many times it is called.
func TestWsReceive_NothingAfterServerClose(t *testing.T) {
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Write(ctx, websocket.MessageText, []byte("a"))
		_ = conn.Write(ctx, websocket.MessageBinary, []byte{0x00, 0xff})
		conn.Close(websocket.StatusNormalClosure, "bye")
	})
	id := wsTaskConnect(t, url, 0)
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Text:a" {
		t.Fatalf("first receive = %q, want Text:a", got)
	}
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Binary:"+string([]byte{0x00, 0xff}) {
		t.Fatalf("second receive = %q, want Binary 00 ff", got)
	}
	for i := 0; i < 3; i++ {
		if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Nothing" {
			t.Fatalf("receive #%d after close = %q, want Nothing", i+1, got)
		}
	}
	// After an explicit close (which unregisters the id) it is still Nothing.
	runWsTask(WebSocket_close(id))
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Nothing" {
		t.Fatalf("receive after close = %q, want Nothing", got)
	}
}

// TestWsReceiveWithin_TimeoutKeepsFrameAndSocket — receiveWithin returns
// Err Timeout when no frame arrives in time, consumes nothing, and leaves the
// socket open: the next frame is still delivered and a send still works.
func TestWsReceiveWithin_TimeoutKeepsFrameAndSocket(t *testing.T) {
	release := make(chan struct{})
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		<-release
		_ = conn.Write(ctx, websocket.MessageText, []byte("late"))
		_, msg, err := conn.Read(ctx)
		if err == nil {
			_ = conn.Write(ctx, websocket.MessageText, append([]byte("echo:"), msg...))
		}
		_, _, _ = conn.Read(ctx)
	})
	id := wsTaskConnect(t, url, 0)
	start := time.Now()
	if got := wsReceived(runWsTask(WebSocket_receiveWithin(50, id))); got != "Err:Timeout" {
		t.Fatalf("receiveWithin with nothing sent = %q, want Err:Timeout", got)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("receiveWithin 50 ms took %s", el)
	}
	close(release)
	if got := wsReceived(runWsTask(WebSocket_receiveWithin(5000, id))); got != "Text:late" {
		t.Fatalf("receive after timeout = %q, want Text:late", got)
	}
	if res := runWsTask(WebSocket_send(id, "ping")); res.Tag != 0 {
		t.Fatalf("send after a timeout failed: %v", res.ErrValue)
	}
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Text:echo:ping" {
		t.Fatalf("echo = %q, want Text:echo:ping", got)
	}
}

// TestWsReceive_SlowTaskConsumerDropsNothing — far more frames than the
// queue holds, to a consumer that stops reading for longer than the stall
// timeout and longer than a ping timeout. In Task mode the reader waits (TCP
// backpressure slows the peer) instead of closing, and the heartbeat treats
// the parked reader as alive. Every frame arrives, in order.
func TestWsReceive_SlowTaskConsumerDropsNothing(t *testing.T) {
	// G-3: a 60 ms pong deadline under -race on a loaded runner closed the
	// socket for a slow pong and failed as "frame lost". The pong deadline
	// is 500 ms; the consumer still stops reading for longer than it.
	withWsTestKnobs(t, 4, 30*time.Millisecond, 500*time.Millisecond)
	const n = 200
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		for i := 0; i < n; i++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf("m%03d", i))); err != nil {
				return
			}
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	})
	id := wsTaskConnect(t, url, 20*time.Millisecond)
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Text:m000" {
		t.Fatalf("first frame = %q", got)
	}
	// Stop reading: the queue fills, the reader parks, pings come due. The
	// pause is longer than the stall timeout and the pong deadline.
	time.Sleep(1200 * time.Millisecond)
	for i := 1; i < n; i++ {
		want := fmt.Sprintf("Text:m%03d", i)
		if got := wsReceived(runWsTask(WebSocket_receiveWithin(5000, id))); got != want {
			t.Fatalf("frame %d = %q, want %q", i, got, want)
		}
	}
	if got := wsReceived(runWsTask(WebSocket_receiveWithin(5000, id))); got != "Nothing" {
		t.Fatalf("after the last frame = %q, want Nothing", got)
	}
}

// TestWsReceive_ConcurrentReceiversShareQueue — two Tasks receiving from the
// same socket each get a disjoint share; together they see every frame once,
// and both end on Nothing when the peer closes.
func TestWsReceive_ConcurrentReceiversShareQueue(t *testing.T) {
	const n = 100
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		for i := 0; i < n; i++ {
			_ = conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf("%03d", i)))
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	})
	id := wsTaskConnect(t, url, 0)
	var mu sync.Mutex
	var got []string
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				r := wsReceived(runWsTask(WebSocket_receiveWithin(5000, id)))
				if r == "Nothing" {
					return
				}
				if !strings.HasPrefix(r, "Text:") {
					t.Errorf("unexpected receive result %q", r)
					return
				}
				mu.Lock()
				got = append(got, strings.TrimPrefix(r, "Text:"))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Strings(got)
	if len(got) != n {
		t.Fatalf("receivers saw %d frames, want %d", len(got), n)
	}
	for i, s := range got {
		if s != fmt.Sprintf("%03d", i) {
			t.Fatalf("frame set mismatch at %d: %q", i, s)
		}
	}
}

// TestWsReceive_SubOwnedSocketIsRefused — a socket a Sub already drains
// cannot also be read by a Task: receive returns Err InvalidInput rather
// than racing the Sub for frames.
func TestWsReceive_SubOwnedSocketIsRefused(t *testing.T) {
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx)
	})
	// The socket, the Sub and the receive all belong to one session: a
	// session reaches only its own sockets (v0.27.0, A-1).
	sess := &liveSession{done: make(chan struct{})}
	t.Cleanup(func() { close(sess.done) })
	id := wsTaskConnectIn(t, sess, url)
	app := &liveApp{}
	ignore := func(any) any { return nil }
	app.applyWsSubsDiff(sess, map[string]subT{
		"k": {kind: "subscribeWebSocket", socketID: id, wsKind: "message", toMsg: ignore},
	})
	var res SkyResult[any, any]
	runWithLiveSession(sess, func() { res = runWsTask(WebSocket_receiveWithin(5000, id)) })
	if got := wsReceived(res); got != "Err:InvalidInput" {
		t.Fatalf("receive on a Sub-owned socket = %q, want Err:InvalidInput", got)
	}
}

// TestWsSub_OnTaskOwnedSocketDoesNotStealFrames — the reverse: once a Task
// has claimed the socket with receive, a Sub for it is refused (logged, never
// registered), so the next frame still reaches the Task.
func TestWsSub_OnTaskOwnedSocketDoesNotStealFrames(t *testing.T) {
	next := make(chan struct{})
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Write(ctx, websocket.MessageText, []byte("one"))
		<-next
		_ = conn.Write(ctx, websocket.MessageText, []byte("two"))
		_, _, _ = conn.Read(ctx)
	})
	sess := &liveSession{done: make(chan struct{})}
	t.Cleanup(func() { close(sess.done) })
	id := wsTaskConnectIn(t, sess, url)
	inSess := func(task any) (r SkyResult[any, any]) {
		runWithLiveSession(sess, func() { r = runWsTask(task) })
		return r
	}
	if got := wsReceived(inSess(WebSocket_receive(id))); got != "Text:one" {
		t.Fatalf("first receive = %q", got)
	}
	app := &liveApp{}
	ignore := func(any) any { return nil }
	app.applyWsSubsDiff(sess, map[string]subT{
		"k": {kind: "subscribeWebSocket", socketID: id, wsKind: "message", toMsg: ignore},
	})
	sess.activeWsSubsMu.Lock()
	registered := len(sess.activeWsSubs)
	sess.activeWsSubsMu.Unlock()
	if registered != 0 {
		t.Fatalf("a Sub on a Task-owned socket was registered (%d subs)", registered)
	}
	close(next)
	if got := wsReceived(inSess(WebSocket_receiveWithin(5000, id))); got != "Text:two" {
		t.Fatalf("receive after the refused Sub = %q, want Text:two", got)
	}
}

// TestWsForEachMessage — forEachMessage runs the body per frame and returns
// Ok on a clean close; a body Err stops at once, closes the socket and is
// returned.
func TestWsForEachMessage(t *testing.T) {
	five := func(ctx context.Context, conn *websocket.Conn) {
		for i := 1; i <= 5; i++ {
			_ = conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf("%d", i)))
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	}
	t.Run("clean close", func(t *testing.T) {
		id := wsTaskConnect(t, wsScriptServer(t, five), 0)
		var seen []string
		body := func(m any) any {
			seen = append(seen, m.(SkyADT).Fields[0].(string))
			return func() any { return Ok[any, any](skyUnit()) }
		}
		res := runWsTask(WebSocket_forEachMessage(id, body))
		if res.Tag != 0 {
			t.Fatalf("forEachMessage = Err %v", res.ErrValue)
		}
		if strings.Join(seen, ",") != "1,2,3,4,5" {
			t.Fatalf("body saw %v", seen)
		}
	})
	t.Run("body error fails fast and closes", func(t *testing.T) {
		id := wsTaskConnect(t, wsScriptServer(t, five), 0)
		sh := lookupWs(nil, id)
		var seen []string
		body := func(m any) any {
			s := m.(SkyADT).Fields[0].(string)
			seen = append(seen, s)
			if s == "3" {
				return func() any { return Err[any, any](ErrInvalidInput("stop at 3")) }
			}
			return func() any { return Ok[any, any](skyUnit()) }
		}
		res := runWsTask(WebSocket_forEachMessage(id, body))
		if res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
			t.Fatalf("forEachMessage = %+v, want the body's Err", res)
		}
		if strings.Join(seen, ",") != "1,2,3" {
			t.Fatalf("body saw %v, want it to stop at 3", seen)
		}
		if sh == nil || !sh.IsClosed() {
			t.Fatal("socket still open after the body failed")
		}
	})
}

// TestSweepSessionless_SparesSocketWhosePingsSucceed — the sessionless
// reaper closes a socket silent for 10 minutes. A socket whose heartbeat
// pings are answered is not silent: the peer is there. Before the fix only a
// delivered frame refreshed the activity time, so a healthy but quiet socket
// was closed at the 10-minute mark.
func TestSweepSessionless_SparesSocketWhosePingsSucceed(t *testing.T) {
	quiet := func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx) // answers pings while it waits
	}
	pinging := lookupWs(nil, wsTaskConnect(t, wsScriptServer(t, quiet), 20*time.Millisecond))
	deadline := time.Now().Add(5 * time.Second)
	for !pinging.pingOK.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pinging.pingOK.Load() {
		t.Fatal("no ping succeeded within 5 s")
	}
	// Control: the same quiet peer with no heartbeat IS reaped, so the sweep
	// below is a real one.
	silent := lookupWs(nil, wsTaskConnect(t, wsScriptServer(t, quiet), 0))

	sweepSessionless(time.Now().Add(11 * time.Minute))

	if pinging.IsClosed() {
		t.Fatal("the reaper closed a socket whose pings succeed")
	}
	if _, ok := sessionlessSockets.Load(pinging.id); !ok {
		t.Fatal("the reaper unmapped a socket whose pings succeed")
	}
	if !silent.IsClosed() {
		t.Fatal("control: a silent socket with no heartbeat was not reaped")
	}
}

// TestSweepSessionless_SparesTaskSocketWhoseReaderIsParked — a Task reads a
// socket when it is ready, so a Task-owned socket whose peer sent more frames
// than the queue holds parks its reader (no conn.Read, so TCP backpressure
// slows the peer). The backlog is proof of a live peer: the reaper must not
// close it however long the Task takes. Control: the same parked state on a
// socket no Task owns is reaped at the idle bound.
func TestSweepSessionless_SparesTaskSocketWhoseReaderIsParked(t *testing.T) {
	flood := func(ctx context.Context, conn *websocket.Conn) {
		for i := 0; i < wsReadChanCap+16; i++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf("%d", i))); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(ctx) // stay open
	}
	id := wsTaskConnect(t, wsScriptServer(t, flood), 0)
	sh := lookupWs(nil, id)
	// One Task receive claims the socket for a Task.
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Text:0" {
		t.Fatalf("first receive = %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !sh.parked.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !sh.parked.Load() {
		t.Fatal("the reader never parked on a full queue")
	}
	if sh.owner.Load() != wsOwnerTask {
		t.Fatalf("owner = %d, want the Task owner", sh.owner.Load())
	}

	sweepSessionless(time.Now().Add(11 * time.Minute))

	if sh.IsClosed() {
		t.Fatal("the reaper closed a Task-owned socket whose reader is parked on a backlog")
	}
	if _, ok := sessionlessSockets.Load(sh.id); !ok {
		t.Fatal("the reaper unmapped a Task-owned socket whose reader is parked")
	}
	// The backlog is still there for the Task, in order.
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Text:1" {
		t.Fatalf("receive after the sweep = %q, want the next queued frame", got)
	}

	// Control: parked with no Task owner (a Sub owner stalls out instead of
	// parking indefinitely) is not proof of life, and idle past the bound is
	// reaped.
	ctx, cancel := context.WithCancel(context.Background())
	ctl := &wsHandle{id: nextWsID(), ch: make(chan wsEvent, 1), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	ctl.owner.Store(wsOwnerSub)
	ctl.parked.Store(true)
	registerWs(nil, ctl)
	sweepSessionless(time.Now().Add(11 * time.Minute))
	if !ctl.IsClosed() {
		t.Fatal("control: a parked socket with no Task owner was not reaped at the idle bound")
	}
}
