package rt

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An already-open Sky Console stream ends when its console cookie ends.
//
// Regression: the console gate ran only when the SSE stream opened. The
// console's `Sub.every` tick then kept pushing metrics, logs and traces down
// that stream for as long as the tab stayed open: after an app sign-out, a
// demotion, or a `_logout` that revoked the cookie, new console requests got
// 403 but the open tab kept receiving live telemetry.
//
// Now an open gated stream re-runs its gate (the same check a new request
// gets, including the app-mode 60 s re-check) every subAppStreamRegateEvery,
// and a revocation of its cookie id wakes it at once. A failed gate ends the
// stream with the `session-lost` / `auth-required` event, so the client stops
// and reloads into the refusal.

// consoleStream is one open SSE stream through the console gate.
type consoleStream struct {
	lines chan string // every line the server sent; closed at end of stream
	resp  *http.Response
}

// fakeConsoleSSE stands in for the console sub-app's SSE handler: it sends a
// data frame every 10 ms (the console's live tick) until its request context
// ends.
func fakeConsoleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			_, _ = w.Write([]byte("data: metrics\n\n"))
			if f != nil {
				f.Flush()
			}
		}
	}
}

func newConsoleStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mountConsoleAuthRoutes(mux)
	mux.HandleFunc("/_sky/console/_sky/sse", gateSSE(ConsoleGate, fakeConsoleSSE))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func openConsoleStream(t *testing.T, srv *httptest.Server, cookies ...*http.Cookie) *consoleStream {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/_sky/console/_sky/sse?sl=1", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	s := &consoleStream{lines: make(chan string, 4096), resp: resp}
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			select {
			case s.lines <- sc.Text():
			default: // the test reads what it needs; drop the rest
			}
		}
	}()
	return s
}

// waitData reports whether a data frame arrives within d.
func (s *consoleStream) waitData(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return false
			}
			if strings.HasPrefix(l, "data: metrics") {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// waitEnd waits up to d for the end of the stream. It returns whether the
// stream ended and whether it carried the auth-required session-lost event.
func (s *consoleStream) waitEnd(d time.Duration) (ended, authRequired bool) {
	deadline := time.After(d)
	sawEvent := false
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return true, authRequired
			}
			if l == "event: session-lost" {
				sawEvent = true
			}
			if sawEvent && strings.Contains(l, sseLostAuthRequired) {
				authRequired = true
			}
		case <-deadline:
			return false, authRequired
		}
	}
}

func withStreamRegateEvery(t *testing.T, d time.Duration) {
	t.Helper()
	prev := subAppStreamRegateEvery
	subAppStreamRegateEvery = d
	t.Cleanup(func() { subAppStreamRegateEvery = prev })
}

// An open console stream ends within the re-check window after the app's
// check stops admitting the admin, with no other console request.
func TestConsoleStream_EndsWhenAppCheckFlips(t *testing.T) {
	clock := withConsoleTestClock(t)
	withStreamRegateEvery(t, 20*time.Millisecond)
	cb := &switchableConsoleCallback{}
	cb.admin.Store(true)
	setupConsoleAppMode(t, cb)
	srv := newConsoleStreamServer(t)

	ok, _, issued := consoleGet(t)
	if !ok || issued == "" || issued == "-" {
		t.Fatalf("admin: allowed=%v cookie=%q", ok, issued)
	}
	s := openConsoleStream(t, srv, consoleCookie(issued))
	if !s.waitData(2 * time.Second) {
		t.Fatalf("an admitted console stream sent no live data")
	}
	cb.admin.Store(false) // the admin signs out of the app

	// Inside the window the stream may still run; past it, it must end.
	clock.Advance(consoleAppRecheckInterval + time.Second)
	ended, auth := s.waitEnd(2 * time.Second)
	if !ended {
		t.Fatalf("%v after the app sign-out the open console stream still runs (live data keeps flowing)", consoleAppRecheckInterval+time.Second)
	}
	if !auth {
		t.Fatalf("the open stream ended without the session-lost auth-required event, so the client cannot show it is signed out")
	}
	// The failed re-check revoked the cookie: a new request is refused too.
	if ok, status, _ := consoleGet(t, consoleCookie(issued)); ok || status != http.StatusForbidden {
		t.Fatalf("after the stream ended: allowed=%v status=%d, want 403", ok, status)
	}
}

// An open console stream ends at once when `_logout` revokes its cookie, in
// app mode and in token mode. The periodic re-check is set to an hour, so
// only the revocation can end it inside the test.
func TestConsoleStream_EndsAtOnceOnLogout(t *testing.T) {
	t.Run("app", func(t *testing.T) {
		withConsoleTestClock(t)
		withStreamRegateEvery(t, time.Hour)
		cb := &switchableConsoleCallback{}
		cb.admin.Store(true)
		setupConsoleAppMode(t, cb)
		srv := newConsoleStreamServer(t)
		_, _, issued := consoleGet(t)
		s := openConsoleStream(t, srv, consoleCookie(issued))
		if !s.waitData(2 * time.Second) {
			t.Fatalf("an admitted console stream sent no live data")
		}
		consoleLogout(t, issued)
		if ended, auth := s.waitEnd(time.Second); !ended || !auth {
			t.Fatalf("after _logout of its cookie: stream ended=%v auth-required=%v, want it ended at once with auth-required", ended, auth)
		}
	})
	t.Run("token", func(t *testing.T) {
		withConsoleTestClock(t)
		withStreamRegateEvery(t, time.Hour)
		t.Setenv("SKY_CONSOLE_AUTH", "token")
		t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-token-cccccccccccccccccccc")
		ResetConsoleAuthStateForTesting()
		t.Cleanup(ResetConsoleAuthStateForTesting)
		withServerlessEnv(t, nil)
		srv := newConsoleStreamServer(t)
		w := httptest.NewRecorder()
		setConsoleV2Cookie(w, loadConsoleAuthState().signKey, "token-auth")
		issued := w.Result().Cookies()[0].Value
		s := openConsoleStream(t, srv, consoleCookie(issued))
		if !s.waitData(2 * time.Second) {
			t.Fatalf("an admitted console stream sent no live data")
		}
		consoleLogout(t, issued)
		if ended, auth := s.waitEnd(time.Second); !ended || !auth {
			t.Fatalf("after _logout of its cookie: stream ended=%v auth-required=%v, want it ended at once with auth-required", ended, auth)
		}
	})
}

// An admin nobody signed out keeps an open stream: across many re-checks,
// past the 60 s window, and while another admin's cookie is revoked.
func TestConsoleStream_UntouchedAdminStaysOpen(t *testing.T) {
	clock := withConsoleTestClock(t)
	withStreamRegateEvery(t, 20*time.Millisecond)
	cb := &switchableConsoleCallback{}
	cb.admin.Store(true)
	setupConsoleAppMode(t, cb)
	srv := newConsoleStreamServer(t)

	_, _, mine := consoleGet(t)
	_, _, other := consoleGet(t)
	s := openConsoleStream(t, srv, consoleCookie(mine))
	o := openConsoleStream(t, srv, consoleCookie(other))
	if !s.waitData(2*time.Second) || !o.waitData(2*time.Second) {
		t.Fatalf("admitted console streams sent no live data")
	}
	consoleLogout(t, other)
	if ended, _ := o.waitEnd(time.Second); !ended {
		t.Fatalf("the signed-out admin's stream did not end")
	}
	clock.Advance(consoleAppRecheckInterval + time.Second)
	time.Sleep(200 * time.Millisecond) // several re-checks
	if !s.waitData(time.Second) {
		t.Fatalf("an untouched admin's open stream stopped after another admin's _logout and a re-check")
	}
	if ended, _ := s.waitEnd(200 * time.Millisecond); ended {
		t.Fatalf("an untouched admin's open stream ended")
	}
}
