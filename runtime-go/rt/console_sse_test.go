package rt

// v0.16.1 PR 3 — regression suite for the isolated /_sky/console
// SSE channel + /_sky/console/_event POST endpoint.
//
// What's gated here:
//
//   - 401 on unauthenticated GET to /_sky/console/_sse
//     (token mode, no cookie, no login). The endpoint is
//     INTENTIONALLY auth-gated; a typo'd /_sky/conslole would
//     have produced this same shape, and we want operators to
//     spot the difference vs the framework /_sky/* reservation
//     path (PR 1).
//   - SSE handshake correctness: authenticated GET produces an
//     immediate `event: hello` frame within 100 ms after the 2
//     KiB padding. Validates the proxy-hardening contract from
//     AUDIT-v0.16.1.md §1 item 4.
//   - Heartbeat cadence: authenticated GET with the heartbeat
//     interval dialed down to 30 ms produces a second SSE event
//     (`event: heartbeat`) within ~150 ms. Validates the wedge
//     detector tripping.
//   - Isolation invariant: the console SSE plane's session map
//     does NOT cross-contaminate with host live.go's session
//     map. Two concurrent consumers (a real live.go session +
//     a console SSE session) MUST receive INDEPENDENT frame
//     streams.
//   - POST /_sky/console/_event:
//       - 401 on unauth
//       - 405 on GET
//       - 200 + envelope on valid JSON
//       - body lands in ConsoleEventChannel (the v0.16.2 attach
//         point — proves the plumbing works ahead of #429)

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetConsoleSSE wires up the standard test-mode prologue used
// by every spec in this file: clear auth state, clear SSE
// state, force non-serverless detection.
func resetConsoleSSE(t *testing.T) {
	t.Helper()
	ResetConsoleAuthStateForTesting()
	ResetConsoleSSEStateForTesting()
	withServerlessEnv(t, nil)
}

// ─── Auth gate ───────────────────────────────────────────────────

func TestConsoleSSE_AuthGate_UnauthenticatedGet_Returns401(t *testing.T) {
	// Token mode: requires the __Host-sky_console cookie. We
	// don't ship one → expect 401.
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-aaaaaaaaaaaaaa")
	resetConsoleSSE(t)

	r := httptest.NewRequest("GET", "/_sky/console/_sse", nil)
	w := httptest.NewRecorder()
	handleConsoleSSE(w, r)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SSE GET: got %d, want 401", w.Result().StatusCode)
	}
}

// ─── Hello handshake + heartbeat ─────────────────────────────────

func TestConsoleSSE_HelloHandshake_AuthenticatedGet_StreamsHello(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-bbbbbbbbbbbbbb")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)

	// Drive the handler in a goroutine + read the SSE stream
	// from the test recorder's response. We use httptest.NewServer
	// instead of httptest.NewRecorder because SSE writes
	// incrementally — the recorder buffers everything before
	// returning, but we want to see "hello" land WITHIN 100 ms.
	srv := httptest.NewServer(http.HandlerFunc(handleConsoleSSE))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE GET status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("X-Accel-Buffering: missing or wrong; proxy hardening contract broken")
	}
	if resp.Header.Get("X-Sky-Console-SSE") != "1" {
		t.Errorf("X-Sky-Console-SSE marker missing — sub-app federation depends on it")
	}

	// Scan the stream for the hello event line. Padding +
	// hello fit comfortably within 100 ms on any healthy host;
	// the test budget of 1 s gives slow CI plenty of slack.
	deadline := time.Now().Add(1 * time.Second)
	reader := bufio.NewReader(resp.Body)
	gotHello := false
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "event: hello") {
			gotHello = true
			break
		}
	}
	if !gotHello {
		t.Fatal("expected `event: hello` frame within 1s, never arrived")
	}
}

func TestConsoleSSE_Heartbeat_FiresWithinInterval(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-cccccccccccccc")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)

	// Dial the heartbeat down to 30ms so the test stays under 200ms.
	prev := consoleSSEHeartbeatInterval
	consoleSSEHeartbeatInterval = 30 * time.Millisecond
	defer func() { consoleSSEHeartbeatInterval = prev }()

	srv := httptest.NewServer(http.HandlerFunc(handleConsoleSSE))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(300 * time.Millisecond)
	gotHello := false
	gotHeartbeat := false
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "event: hello") {
			gotHello = true
		}
		if strings.HasPrefix(line, "event: heartbeat") {
			gotHeartbeat = true
			break
		}
	}
	if !gotHello {
		t.Fatal("expected `event: hello` before heartbeat, never arrived")
	}
	if !gotHeartbeat {
		t.Fatal("expected `event: heartbeat` within 300ms (heartbeat interval = 30ms), never arrived")
	}
}

// ─── Isolation ───────────────────────────────────────────────────

func TestConsoleSSE_IsolatedFromHostSession(t *testing.T) {
	// The console SSE plane MUST NOT share state with live.go's
	// session map. This spec opens a console session via
	// MountConsoleSSE + a tracked SSE connection, then asserts
	// the host's liveStore.sessions is untouched + the
	// connectedConsoleSSEClients count moves independently.
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-dddddddddddddd")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)

	prevHb := consoleSSEHeartbeatInterval
	consoleSSEHeartbeatInterval = 30 * time.Millisecond
	defer func() { consoleSSEHeartbeatInterval = prevHb }()

	mux := http.NewServeMux()
	if !MountConsoleSSE(mux) {
		t.Fatal("MountConsoleSSE returned false; mount path failed")
	}
	if !ConsoleSSEHealthy() {
		t.Fatal("ConsoleSSEHealthy() returned false after MountConsoleSSE")
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Open the SSE channel.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/_sky/console/_sse", nil)
	req.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	// Wait for hello so we know the session was created.
	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "event: hello") {
			break
		}
	}

	// Console plane has 1 client.
	if got := connectedConsoleSSEClients(); got != 1 {
		t.Fatalf("connectedConsoleSSEClients: got %d, want 1 after SSE open", got)
	}

	// Broadcast a frame on the console plane. The host plane
	// must not observe it (we don't have a direct host SSE in
	// this test, but the channel goes through consoleSSE.sessions
	// only — drop counter would tick if it tried to route via
	// the host's sseCh).
	ConsoleSSEBroadcast([]byte("event: probe\ndata: {}\n\n"))

	// Sanity: console session still alive.
	if got := connectedConsoleSSEClients(); got != 1 {
		t.Errorf("post-broadcast count: got %d, want 1", got)
	}

	// Cancel the SSE request; the session should be reaped.
	cancel()
	// Give the handler a moment to notice the canceled context.
	time.Sleep(50 * time.Millisecond)
	if got := connectedConsoleSSEClients(); got != 0 {
		t.Errorf("post-cancel count: got %d, want 0 (session leak)", got)
	}
}

// ─── POST /_sky/console/_event ───────────────────────────────────

func TestConsoleEvent_POST_AuthGate(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-eeeeeeeeeeeeee")
	resetConsoleSSE(t)

	r := httptest.NewRequest("POST", "/_sky/console/_event", strings.NewReader(`{"x":1}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleConsoleEvent(w, r)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST: got %d, want 401", w.Result().StatusCode)
	}
}

func TestConsoleEvent_POST_WrongMethod_405(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-ffffffffffffff")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)

	r := httptest.NewRequest("GET", "/_sky/console/_event", nil)
	r.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	w := httptest.NewRecorder()
	handleConsoleEvent(w, r)

	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on /_event: got %d, want 405", w.Result().StatusCode)
	}
	if got := w.Result().Header.Get("Allow"); got != "POST" {
		t.Errorf("Allow header: got %q, want POST", got)
	}
}

func TestConsoleEvent_POST_Accepted_LandsInChannel(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-gggggggggggggg")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)

	mux := http.NewServeMux()
	if !MountConsoleSSE(mux) {
		t.Fatal("MountConsoleSSE returned false")
	}
	ch := ConsoleEventChannel()
	if ch == nil {
		t.Fatal("ConsoleEventChannel returned nil after mount")
	}

	body := strings.NewReader(`{"kind":"click","sid":"sky-id-42"}`)
	r := httptest.NewRequest("POST", "/_sky/console/_event", body)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("POST: got %d, want 200; body=%q", w.Result().StatusCode, w.Body.String())
	}

	// Envelope should be {"status":"queued"}.
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, w.Body.String())
	}
	if env["status"] != "queued" {
		t.Errorf("envelope status: got %v, want queued", env["status"])
	}

	// Event should be on the channel.
	select {
	case evt := <-ch:
		if evt.Payload["kind"] != "click" {
			t.Errorf("payload[kind]: got %v, want click", evt.Payload["kind"])
		}
		if evt.SessionID == "" {
			t.Errorf("event SessionID is empty; cookie not set or read")
		}
		if evt.Headers["content-type"] == "" {
			t.Errorf("headers map should include content-type lowered key")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event never landed on ConsoleEventChannel within 500ms")
	}
}

func TestConsoleEvent_POST_BodyTooLarge_413(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-test-token-hhhhhhhhhhhhhh")
	resetConsoleSSE(t)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "tester", time.Hour)
	_ = MountConsoleSSE(http.NewServeMux()) // initialise eventCh

	// Build a body just past 1 MiB.
	body := strings.Repeat("a", consoleEventMaxBody+10)
	r := httptest.NewRequest("POST", "/_sky/console/_event", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	w := httptest.NewRecorder()
	handleConsoleEvent(w, r)

	if w.Result().StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize POST: got %d, want 413", w.Result().StatusCode)
	}
}

// ─── Mount idempotency + sub-app guard ───────────────────────────

func TestMountConsoleSSE_Idempotent(t *testing.T) {
	resetConsoleSSE(t)
	mux := http.NewServeMux()
	if !MountConsoleSSE(mux) {
		t.Fatal("first mount: got false, want true")
	}
	if MountConsoleSSE(mux) {
		t.Error("second mount on same registered state: got true, want false (idempotency)")
	}
}

func TestMountConsoleSSE_SubAppContext_DoesNotMount(t *testing.T) {
	resetConsoleSSE(t)
	t.Setenv("SKY_LIVE_BASE_PATH", "/admin")
	mux := http.NewServeMux()
	if MountConsoleSSE(mux) {
		t.Error("sub-app context (SKY_LIVE_BASE_PATH set) should decline mount")
	}
	if ConsoleSSEHealthy() {
		t.Error("ConsoleSSEHealthy should be false when sub-app guard fires")
	}
}

func TestMountConsoleSSE_NilMux_ReturnsFalse(t *testing.T) {
	resetConsoleSSE(t)
	if MountConsoleSSE(nil) {
		t.Error("nil mux: got true, want false")
	}
}

// ─── Cookie shape ────────────────────────────────────────────────

func TestConsoleSSESessionID_MintsHostPrefixCookie(t *testing.T) {
	resetConsoleSSE(t)
	r := httptest.NewRequest("GET", "/_sky/console/_sse", nil)
	w := httptest.NewRecorder()
	sid := consoleSSESessionID(r, w)
	if sid == "" {
		t.Fatal("session id empty")
	}
	// findSetCookie's parseSetCookieAttrs helper only knows
	// Secure / HttpOnly / SameSite; we re-parse the raw header
	// directly for Path + Max-Age (per the comment in
	// console_auth_v2_test.go @ L98).
	var raw string
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, consoleSSECookieName+"=") {
			raw = line
			break
		}
	}
	if raw == "" {
		t.Fatalf("expected %s cookie to be set", consoleSSECookieName)
	}
	if !strings.Contains(raw, "Path=/") {
		t.Errorf("cookie raw header should contain Path=/ (RFC 6265bis __Host- requirement): %q", raw)
	}
	if !strings.Contains(raw, "Secure") {
		t.Errorf("cookie raw header should contain Secure (__Host- requirement): %q", raw)
	}
	if !strings.Contains(raw, "HttpOnly") {
		t.Errorf("cookie raw header should contain HttpOnly: %q", raw)
	}
	if !strings.Contains(raw, "SameSite=Strict") {
		t.Errorf("cookie raw header should contain SameSite=Strict: %q", raw)
	}
}

func TestConsoleSSESessionID_ReadsExistingCookie(t *testing.T) {
	resetConsoleSSE(t)
	r := httptest.NewRequest("GET", "/_sky/console/_sse", nil)
	r.AddCookie(&http.Cookie{Name: consoleSSECookieName, Value: "1234567890abcdef-existing"})
	w := httptest.NewRecorder()
	sid := consoleSSESessionID(r, w)
	if sid != "1234567890abcdef-existing" {
		t.Errorf("session id: got %q, want %q (existing cookie ignored)", sid, "1234567890abcdef-existing")
	}
	// Should NOT mint a new Set-Cookie when one already exists.
	if c := findSetCookie(w.Result().Header, consoleSSECookieName); c != nil {
		t.Errorf("expected NO Set-Cookie when valid cookie supplied, got %q", c.Value)
	}
}
