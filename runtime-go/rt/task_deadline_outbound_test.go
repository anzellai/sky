//go:build !js

package rt

// The Task.timeout deadline on every outbound network path a Sky program
// reaches, beyond Http.get / post / request (task_deadline_test.go).
//
// Found while adding Task.timeout: only the three Http kernels read the
// deadline. Http.Stream.open, WebSocket.connect and its Task reads,
// Email.send's HTTP providers, the Std.Ai slot and provider calls and the
// Std.Ai.Store transfers ran on until their own timeouts (30 s to 10 min), so
// a `Task.timeout 2000` around any of them gave up waiting and left the call
// running. Worse, Http.Stream.open sent its request with the Task.timeout
// context itself, so a stream opened inside a Task.timeout was cut the moment
// the open returned. Each test below stalls a real server and checks that the
// call stops at the deadline, that the server sees the request cancelled, and
// that a call is refused, unsent, once the deadline has passed.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stallServer answers nothing until the client goes away (or `wait`), and
// reports on `cancelled` when the client gave up first. With `headFirst` it
// sends the status line and one chunk at once, then stalls the body.
func stallServer(t *testing.T, headFirst bool, cancelled chan<- struct{}, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		buf := make([]byte, 4096)
		for {
			if _, err := r.Body.Read(buf); err != nil {
				break
			}
		}
		if headFirst {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"))
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
			select {
			case cancelled <- struct{}{}:
			default:
			}
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitCancelled(t *testing.T, what string, cancelled <-chan struct{}) {
	t.Helper()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: the server never saw the request cancelled", what)
	}
}

// within runs a Task on a goroutine whose deadline is `ms` away, and fails
// when it took longer than `limit`. It stamps the deadline directly rather
// than through Task.timeout: Task.timeout would give up waiting at the
// deadline whether or not the kernel stopped, and its own error would hide
// the kernel's. Here only the kernel can end the call.
func within(t *testing.T, what string, ms int, limit time.Duration, task any) SkyResult[any, any] {
	t.Helper()
	start := time.Now()
	var r SkyResult[any, any]
	withDeadlineIn(time.Duration(ms)*time.Millisecond, func() { r = anyTaskInvoke(task) })
	if el := time.Since(start); el > limit {
		t.Fatalf("%s under Task.timeout %d returned after %v", what, ms, el)
	}
	return r
}

// refusedUnsent runs task with the deadline already passed and asserts the
// deadline's error and that nothing reached the server.
func refusedUnsent(t *testing.T, what string, hits *atomic.Int32, task func() any) {
	t.Helper()
	before := hits.Load()
	var r SkyResult[any, any]
	withDeadlineIn(-time.Millisecond, func() { r = anyTaskInvoke(task()) })
	deadlineFailure(t, r)
	time.Sleep(20 * time.Millisecond)
	if hits.Load() != before {
		t.Fatalf("%s: a call refused at the deadline reached the server", what)
	}
}

func streamReq(url string, extra ...[2]any) map[string]any {
	m := map[string]any{
		"method": "POST", "url": url, "body": "{}", "headers": []any{},
		"timeout": 30000, "followRedirects": true, "maxRedirects": 10,
	}
	for _, kv := range extra {
		m[kv[0].(string)] = kv[1]
	}
	return m
}

func TestHttpStream_OpenStopsAtTheDeadline(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, false, cancelled, &hits)
	r := within(t, "Http.Stream.open", 150, 2*time.Second, HttpStream_open(streamReq(srv.URL)))
	f := deadlineFailure(t, r)
	if v, _ := f.Attr("op"); v != "Http.Stream.open" {
		t.Fatalf("op attribute = %q", v)
	}
	waitCancelled(t, "Http.Stream.open", cancelled)

	refusedUnsent(t, "Http.Stream.open", &hits, func() any { return HttpStream_open(streamReq(srv.URL)) })

	// Http.withMinRemaining applies to a stream's open too.
	r = anyTaskInvoke(Task_timeout(1000, HttpStream_open(streamReq(srv.URL, [2]any{"minRemaining", 5000}))))
	if fd, ok := DomainOf(r.ErrValue); r.Tag == 0 || !ok || fd.Code != "DeadlineTooShort" {
		t.Fatalf("want http/DeadlineTooShort, got %+v", r)
	}
}

// A stream opened inside a Task.timeout outlives the open: the deadline
// bounds the wait for the answer to start, not the stream.
func TestHttpStream_OpenedUnderADeadlineKeepsStreaming(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("one;"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("two;"))
	}))
	t.Cleanup(srv.Close)
	r := anyTaskInvoke(Task_timeout(2000, HttpStream_open(streamReq(srv.URL))))
	if r.Tag != 0 {
		t.Fatalf("open failed: %+v", r)
	}
	id := r.OkValue
	time.Sleep(100 * time.Millisecond) // the Task.timeout has returned
	close(release)
	var got strings.Builder
	body := func(chunk any) any {
		return func() any { got.WriteString(AsString(chunk)); return Ok[any, any](skyUnit()) }
	}
	done := anyTaskInvoke(HttpStream_forEachChunk(id, body))
	if done.Tag != 0 || got.String() != "one;two;" {
		t.Fatalf("the stream did not outlive its open: %+v, read %q", done, got.String())
	}
}

func TestHttpStream_ForEachChunkStopsAtTheDeadline(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, true, cancelled, &hits)
	open := anyTaskInvoke(HttpStream_open(streamReq(srv.URL)))
	if open.Tag != 0 {
		t.Fatalf("open failed: %+v", open)
	}
	var chunks atomic.Int32
	body := func(any) any { return func() any { chunks.Add(1); return Ok[any, any](skyUnit()) } }
	r := within(t, "Http.Stream.forEachChunk", 200, 2*time.Second, HttpStream_forEachChunk(open.OkValue, body))
	f := deadlineFailure(t, r)
	if v, _ := f.Attr("op"); v != "Http.Stream.forEachChunk" {
		t.Fatalf("op attribute = %q", v)
	}
	if chunks.Load() != 1 {
		t.Fatalf("read %d chunks before the deadline, want 1", chunks.Load())
	}
	waitCancelled(t, "Http.Stream.forEachChunk", cancelled)
}

// silentListener accepts TCP connections and never answers, so a WebSocket
// handshake waits until the client gives up.
func silentListener(t *testing.T, accepted *atomic.Int32, closed chan<- struct{}) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						select {
						case closed <- struct{}{}:
						default:
						}
						return
					}
				}
			}()
		}
	}()
	return "ws://" + ln.Addr().String() + "/"
}

func TestWebSocket_ConnectStopsAtTheDeadline(t *testing.T) {
	closed := make(chan struct{}, 1)
	var accepted atomic.Int32
	url := silentListener(t, &accepted, closed)
	r := within(t, "WebSocket.connect", 150, 2*time.Second, WebSocket_connect(url))
	f := deadlineFailure(t, r)
	if v, _ := f.Attr("op"); v != "WebSocket.connect" {
		t.Fatalf("op attribute = %q", v)
	}
	waitCancelled(t, "WebSocket.connect", closed)

	cfg := map[string]any{"url": url, "headers": []any{}, "timeout": 30000, "pingInterval": 0}
	r = within(t, "WebSocket.connectWith", 150, 2*time.Second, WebSocket_connectWith(cfg))
	deadlineFailure(t, r)

	refusedUnsent(t, "WebSocket.connect", &accepted, func() any { return WebSocket_connect(url) })
}

func TestWebSocket_TaskReadsStopAtTheDeadline(t *testing.T) {
	send := make(chan string, 1)
	url := wsScriptServer(t, func(ctx context.Context, conn *websocket.Conn) {
		// Read, so the server answers the client's close handshake.
		ctx = conn.CloseRead(ctx)
		for {
			select {
			case m := <-send:
				_ = conn.Write(ctx, websocket.MessageText, []byte(m))
			case <-ctx.Done():
				return
			}
		}
	})
	id := wsTaskConnect(t, url, 0)
	for name, read := range map[string]func() any{
		"WebSocket.receive":       func() any { return WebSocket_receive(id) },
		"WebSocket.receiveWithin": func() any { return WebSocket_receiveWithin(10000, id) },
	} {
		r := within(t, name, 100, 2*time.Second, read())
		f := deadlineFailure(t, r)
		if v, _ := f.Attr("op"); v != name {
			t.Fatalf("op attribute = %q, want %q", v, name)
		}
	}
	// The deadline consumed no frame and left the socket open.
	send <- "after"
	if got := wsReceived(runWsTask(WebSocket_receiveWithin(2000, id))); got != "Text:after" {
		t.Fatalf("after the deadline: %s", got)
	}

	// forEachMessage stops at the deadline and closes the socket.
	body := func(any) any { return func() any { return Ok[any, any](skyUnit()) } }
	r := within(t, "WebSocket.forEachMessage", 100, 2*time.Second, WebSocket_forEachMessage(id, body))
	deadlineFailure(t, r)
	if got := wsReceived(runWsTask(WebSocket_receive(id))); got != "Nothing" {
		t.Fatalf("forEachMessage left the socket open: %s", got)
	}
}

func TestEmailSend_HttpProviderStopsAtTheDeadline(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, false, cancelled, &hits)
	t.Setenv("SKY_EMAIL_DRY_RUN", "")
	t.Setenv("SKY_EMAIL_ENDPOINT_RESEND", srv.URL)
	provider := adt("Resend", "re_test")
	r := within(t, "Email.send", 150, 2*time.Second, Email_send(provider, msgNoAttachment()))
	f := deadlineFailure(t, r)
	if v, _ := f.Attr("op"); v != "Email.send" {
		t.Fatalf("op attribute = %q", v)
	}
	waitCancelled(t, "Email.send", cancelled)
	refusedUnsent(t, "Email.send", &hits, func() any { return Email_send(provider, msgNoAttachment()) })
}

func remoteSlotCfg(url string) map[string]any {
	return map[string]any{
		"slot": "chat", "mode": "", "url": url, "hasKey": false, "key": Secret{},
		"model": "", "adapter": "", "timeoutMs": 60000, "maxInFlight": -1, "queue": -1,
		"queueWaitMs": -1, "retries": 0, "hasLocal": false,
	}
}

func TestAi_CallsStopAtTheDeadline(t *testing.T) {
	aiTestEnv(t, nil)
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, false, cancelled, &hits)
	cfg := remoteSlotCfg(srv.URL)
	calls := map[string]func() any{
		"Ai.call":         func() any { return Ai_endpointPost(cfg, "/chat/completions", chatBody("x")) },
		"Ai.callMeasured": func() any { return Ai_endpointPostMeasured(cfg, "tok-1", "/chat/completions", chatBody("x")) },
		"Ai.stream":       func() any { return Ai_endpointStreamOpen(cfg, "/chat/completions", chatBody("x")) },
		"Ai.providerStream": func() any {
			return Ai_streamOpenUrl(srv.URL+"/chat/completions", Secret{}, chatBody("x"))
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			r := within(t, name, 150, 3*time.Second, call())
			deadlineFailure(t, r)
			waitCancelled(t, name, cancelled)
			refusedUnsent(t, name, &hits, call)
		})
	}
}

func TestAi_StreamReadStopsAtTheDeadline(t *testing.T) {
	aiTestEnv(t, nil)
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, true, cancelled, &hits)
	open := anyTaskInvoke(Ai_streamOpenUrl(srv.URL+"/chat/completions", Secret{}, chatBody("x")))
	if open.Tag != 0 {
		t.Fatalf("open failed: %+v", open)
	}
	id := open.OkValue
	// The first event is already there; the next read waits for the stall.
	for i := 0; i < 5; i++ {
		r := within(t, "Ai.streamNext", 200, 3*time.Second, Ai_streamNext(id))
		if r.Tag != 0 {
			f := deadlineFailure(t, r)
			if v, _ := f.Attr("op"); v != "Ai.streamNext" {
				t.Fatalf("op attribute = %q", v)
			}
			waitCancelled(t, "Ai.streamNext", cancelled)
			return
		}
	}
	t.Fatal("Ai_streamNext never reached the stalled part of the stream")
}

func TestAiStore_TransfersStopAtTheDeadline(t *testing.T) {
	t.Setenv("SKY_AI_HOME", t.TempDir())
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := stallServer(t, false, cancelled, &hits)
	data := []byte("adapter")
	get := func() any { return Ai_storeHttpGet(storeReq("GET", srv.URL+"/b/x"), sha(data)) }
	r := within(t, "Ai.Store.get", 150, 3*time.Second, get())
	deadlineFailure(t, r)
	waitCancelled(t, "Ai.Store.get", cancelled)
	refusedUnsent(t, "Ai.Store.get", &hits, get)
}
