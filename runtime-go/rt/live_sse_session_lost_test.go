//go:build !js

package rt

// The SSE endpoint's contract when it will not stream, and the stream's
// survival under a Server.listen host's per-request deadlines.
//
// Field report behind these tests: a Sky Console behind Caddy (HTTP/2,
// `encode zstd gzip`, flush_interval -1) logged "aborting with incomplete
// response … unexpected EOF" on /_sky/console/_sky/sse about every 33 s, the
// browser showed net::ERR_HTTP2_PROTOCOL_ERROR on a 200, and the page sat on
// "Reconnecting" for ever. The cause: Server.listen's 30 s WriteTimeout cut
// every stream mid-body (stream_deadline.go). The same investigation showed an
// EventSource cannot read the 404 an unknown session got, so recovery hung on
// a side probe that a gate's 401 defeated (live_sse_session_lost.go).

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sseLostTestApp() *liveApp {
	return &liveApp{store: newMemoryStore(30 * time.Minute), locker: newSessionLocker()}
}

func TestSSE_UnknownSession_AnswersClassifiedEvent(t *testing.T) {
	app := sseLostTestApp()
	for _, tc := range []struct {
		name, cookie, reason string
	}{
		{"unknown session (restart with a memory store, other replica, expiry)", "sid-from-a-previous-process", sseLostUnknownSession},
		{"no session cookie", "", sseLostNoCookie},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=t&sl=1", nil)
			req.Header.Set("Accept", "text/event-stream")
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "sky_sid", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { app.handleSSE(rec, req); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not end the stream: a lost session must get one answer, not a held connection")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200 (an EventSource cannot read a non-200 body)", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
				t.Fatalf("Content-Type %q, want text/event-stream", ct)
			}
			if rec.Header().Get("X-Sky-Status") != "session-lost" {
				t.Errorf("X-Sky-Status %q, want session-lost", rec.Header().Get("X-Sky-Status"))
			}
			want := "event: session-lost\ndata: {\"reason\":\"" + tc.reason + "\"}\n\n"
			if got := rec.Body.String(); got != want {
				t.Fatalf("body %q, want exactly %q", got, want)
			}
		})
	}
}

// A page loaded from a server that predates the event (no `sl=1`) keeps the
// answer its client already handles.
func TestSSE_UnknownSession_LegacyClientKeeps404(t *testing.T) {
	app := sseLostTestApp()
	req := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=t", nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "gone"})
	rec := httptest.NewRecorder()
	app.handleSSE(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "session not found") ||
		rec.Header().Get("X-Sky-Status") != "session-lost" {
		t.Fatalf("legacy answer changed: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

// An in-process sub-app's gate (the console login) refusing the stream is a
// classified `auth-required`, not a 401 the EventSource retries for ever.
func TestSSE_SubAppGateDenied_AnswersAuthRequired(t *testing.T) {
	denied := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<html>login</html>"))
		return false
	}
	called := false
	h := gateSSE(denied, func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/_sky/console/_sky/sse?tab=t&sl=1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if called {
		t.Fatal("the stream handler ran behind a denied gate")
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(rec.Body.String(), `event: session-lost`) ||
		!strings.Contains(rec.Body.String(), `"reason":"auth-required"`) ||
		strings.Contains(rec.Body.String(), "login") {
		t.Fatalf("denied gate: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	// Without sl=1 the gate's own answer stands.
	req = httptest.NewRequest(http.MethodGet, "/_sky/console/_sky/sse?tab=t", nil)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy client behind a denied gate: %d, want the gate's 401", rec.Code)
	}

	// An admitting gate's headers reach the stream's response.
	allow := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Add("Set-Cookie", "k=v")
		return true
	}
	req = httptest.NewRequest(http.MethodGet, "/_sky/console/_sky/sse?tab=t&sl=1", nil)
	rec = httptest.NewRecorder()
	gateSSE(allow, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })(rec, req)
	if rec.Code != 299 || rec.Header().Get("Set-Cookie") != "k=v" {
		t.Fatalf("admitting gate: %d %v", rec.Code, rec.Header())
	}
}

// The Live SSE stream, served behind the Server.listen middleware chain by a
// server with Server.listen's read/write timeouts, outlives those timeouts.
// Before the fix the write deadline cut it mid-body: the client read an
// unexpected EOF at the timeout.
func TestSSE_OutlivesServerListenDeadlines(t *testing.T) {
	prevHB := sseHeartbeatInterval
	sseHeartbeatInterval = 50 * time.Millisecond
	defer func() { sseHeartbeatInterval = prevHB }()

	app := sseLostTestApp()
	app.store.Set("sid-deadline", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/sse", app.handleSSE)
	// The same chain Server_listen builds (rt_server.go).
	srv := httptest.NewUnstartedServer(ObservabilityMiddleware(CSRFMiddleware(spaRpcDedupeMiddleware(mux))))
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/_sky/sse?tab=t&sl=1", nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-deadline"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	start := time.Now()
	heartbeats := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: heartbeat") {
			heartbeats++
		}
	}
	lived := time.Since(start)
	scanErr := sc.Err()
	if scanErr != nil && !strings.Contains(scanErr.Error(), "context deadline exceeded") &&
		!strings.Contains(scanErr.Error(), "context canceled") {
		t.Fatalf("stream ended with %v after %v (%d heartbeats): the server's deadline cut it", scanErr, lived, heartbeats)
	}
	if scanErr == nil || lived < time.Second {
		t.Fatalf("stream ended after %v (%d heartbeats, err=%v); want it open until the client left at 1.5 s",
			lived, heartbeats, scanErr)
	}
	if heartbeats < 10 {
		t.Fatalf("%d heartbeats in %v; want the stream to keep flowing past the 300 ms deadlines", heartbeats, lived)
	}
}

// A Sky.Http.Server.Stream response (the Sky.Spa push topic rides on it) under
// the same deadlines.
func TestServerStream_OutlivesServerListenDeadlines(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveStreamingResponse(w, r, SkyResponse{
			Status:      http.StatusOK,
			ContentType: "text/event-stream",
			StreamHandler: func(writer any) any {
				id := writer.(SkyADT).Fields[0]
				return func() any {
					for i := 0; i < 20; i++ {
						res := anyTaskInvoke(ServerStream_emit("data: x\n\n", id))
						if res.Tag != 0 {
							return res
						}
						time.Sleep(50 * time.Millisecond)
					}
					return Ok[any, any](skyUnit())
				}
			},
		})
	})
	srv := httptest.NewUnstartedServer(ObservabilityMiddleware(CSRFMiddleware(spaRpcDedupeMiddleware(h))))
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("stream cut after %d bytes: %v", len(body), err)
	}
	if n := strings.Count(string(body), "data: x"); n != 20 {
		t.Fatalf("%d of 20 frames arrived", n)
	}
}
