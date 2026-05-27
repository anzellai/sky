package rt

// Wire-level regression tests for the SSE handshake + heartbeat that
// guard against reverse-proxy wedges. The client side (JS) is asserted
// in live_status_test.go as substring presence; here we drive
// handleSSE directly and verify the server emits the protocol the
// client now relies on.
//
// Failure modes these tests prevent:
//   - Removing X-Accel-Buffering would let some proxies (Nginx default,
//     Cloudflare without the right page rule) buffer the response and
//     trip the client's helloTimeout, causing infinite reconnect loops.
//   - Removing X-Sky-Live would let proxy-rewritten 200 OK responses
//     look identical to real Sky.Live SSE streams from the client's
//     viewpoint — the very wedge bug this work fixes.
//   - Dropping the hello event would make every fresh connection look
//     wedged to the client until the first patch arrived (which on a
//     dashboard might be never).
//   - Dropping the heartbeat ticker would let silently-wedged
//     connections (proxy holds the socket open with no data) sit
//     undetected indefinitely.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEHandshakeHeaders(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	// Pre-seed a session so handleSSE doesn't 404 on lookup.
	app.store.Set("sid-handshake", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
	})

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-handshake"})
	// Cancel after a short grace window so the handler returns and
	// httputil-style scanning can observe the bytes it emitted.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSE GET failed: %v", err)
	}
	if resp != nil {
		defer resp.Body.Close()
	}
	if resp == nil {
		t.Fatal("expected response from SSE handler before context cancel")
	}

	wantHeaders := map[string]string{
		"Content-Type":      "text/event-stream",
		"X-Accel-Buffering": "no",
		"X-Sky-Live":        "1",
	}
	for k, v := range wantHeaders {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("SSE header %s: got %q want %q", k, got, v)
		}
	}

	// Read the bytes emitted before the context cancelled the request.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var paddingLen int
	sawHello := false
	helloHasV := false
	helloHasSid := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ": ") && !sawHello {
			// Padding line accumulates until we hit a non-comment.
			paddingLen += len(line)
			continue
		}
		if strings.HasPrefix(line, "event: hello") {
			sawHello = true
			continue
		}
		if sawHello && strings.HasPrefix(line, "data: ") {
			if strings.Contains(line, `"v":1`) {
				helloHasV = true
			}
			if strings.Contains(line, `"sid":"sid-handshake"`) {
				helloHasSid = true
			}
			break
		}
	}
	if paddingLen < 2000 {
		t.Errorf("SSE padding line %d bytes < 2000 — proxy buffers may not flush", paddingLen)
	}
	if !sawHello {
		t.Errorf("hello event missing in SSE response")
	}
	if !helloHasV {
		t.Errorf("hello event missing protocol version")
	}
	if !helloHasSid {
		t.Errorf("hello event missing sid echo")
	}
}

// TestSSEReconnectResyncPushesCurrentView is the regression fence for
// the watch-restart-with-stale-view bug. After a Sky.Live binary
// restart with a persistent session store, the browser's SSE
// reconnects and gets a hello frame — but used to sit on stale HTML
// rendered by the OLD binary, because nothing pushed to sess.sseCh
// post-restart (subscriptions are goroutines launched in the old
// process). The fix re-renders the view inside handleSSE and emits a
// full-body patch frame as the next event after hello, guaranteeing
// a fresh DOM on every fresh SSE connection. Critical for `sky
// watch` hot-reload UX, but the same fix benefits prod deploys with
// persistent storage.
func TestSSEReconnectResyncPushesCurrentView(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
		view: func(model any) any {
			return velement("div", []any{attrPair{key: "id", val: "marker"}},
				[]any{vtext("FRESH-RENDER-MARKER")})
		},
	}
	// Pre-seed: session has model, but no prevTree/prevBody (the
	// post-restart shape — these are deliberately not gob-encoded).
	app.store.Set("sid-resync", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		model:     "model-state",
		handlers:  map[string]any{},
	})

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-resync"})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSE GET failed: %v", err)
	}
	if resp == nil {
		t.Fatal("no response from SSE handler")
	}
	defer resp.Body.Close()

	// Read the stream until we see the resync patch frame OR the
	// context cancels. The frame is the first `event: patch` event
	// (after padding + hello).
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	sawHello := false
	sawPatchAfterHello := false
	patchHasMarker := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: hello") {
			sawHello = true
			continue
		}
		if sawHello && strings.HasPrefix(line, "event: patch") {
			sawPatchAfterHello = true
			continue
		}
		if sawPatchAfterHello && strings.HasPrefix(line, "data: ") {
			if strings.Contains(line, "FRESH-RENDER-MARKER") {
				patchHasMarker = true
			}
			break
		}
	}
	if !sawHello {
		t.Errorf("hello event missing")
	}
	if !sawPatchAfterHello {
		t.Errorf("no patch event after hello — reconnect-resync didn't fire")
	}
	if !patchHasMarker {
		t.Errorf("patch frame body missing FRESH-RENDER-MARKER — view() wasn't re-evaluated")
	}
}

// TestSSEHeartbeatFires verifies that the heartbeat ticker actually
// emits frames (not just that it's wired up — the JS-side test asserts
// the listener exists, but a server that never sends would still trip
// the client's watchdog). Uses the test-only sseHeartbeatInterval
// override to keep the test fast.
func TestSSEHeartbeatFires(t *testing.T) {
	prev := sseHeartbeatInterval
	sseHeartbeatInterval = 100 * time.Millisecond
	defer func() { sseHeartbeatInterval = prev }()

	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	app.store.Set("sid-hb", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
	})

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-hb"})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSE GET failed: %v", err)
	}
	if resp == nil {
		t.Fatal("no response")
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	sawHeartbeat := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: heartbeat") {
			sawHeartbeat = true
			break
		}
	}
	if !sawHeartbeat {
		t.Errorf("no heartbeat event observed within 1s window (interval=100ms)")
	}
}

// TestPostEventHasSkyLiveHeader: the JSON envelope path on
// /_sky/event must carry X-Sky-Live: 1 so the client can distinguish
// a real Sky.Live response from a reverse-proxy-rewritten 200 OK.
// Without this header the new client refuses the response and falls
// through to the retry path — which is correct for a wedge but
// catastrophic for a healthy server, hence this test.
// TestI18nBannerStringsReachClientPage drives the full page-render
// path with a Live.app cfg that supplies non-English banner strings,
// and asserts the rendered HTML embeds them in the inline script. This
// is the only test that proves the resolveBannerStrings overlay
// actually flows from cfg → bannerCfg → liveJSWithCfg → page response;
// the unit-level tests only check each layer in isolation.
func TestI18nBannerStringsReachClientPage(t *testing.T) {
	app := &liveApp{
		init: func(req any) any {
			return SkyTuple2{V0: "model", V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("hi")})
		},
		subscriptions: func(model any) any { return nil },
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
	}
	// Inline-record-shaped cfg — exactly what the typed-codegen emits
	// when a user writes `status = { reconnecting = "...", offline = "..." }`
	// inside `Live.app`'s record literal.
	type statusRec struct {
		Reconnecting string
		Offline      string
	}
	type cfgShape struct {
		Status statusRec
	}
	app.bannerCfg = resolveBannerStrings(loadLiveBannerConfig(), cfgShape{
		Status: statusRec{
			Reconnecting: "Reconnexion en cours…",
			Offline:      "Connexion perdue. Veuillez actualiser.",
		},
	})

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)

	if rr.Code != 200 {
		t.Fatalf("page render failed: status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `Reconnexion en cours…`) {
		t.Errorf("rendered page missing user-supplied reconnecting string")
	}
	if !strings.Contains(body, `Connexion perdue. Veuillez actualiser.`) {
		t.Errorf("rendered page missing user-supplied offline string")
	}
}

// TestSSESessionNotFoundCarriesSkyLiveHeader: when the SSE handler
// returns 404 because the session is gone (memory-store restart,
// sky.toml [live] store change, TTL expiry), the response MUST carry
// X-Sky-Live: 1 so the client's __skyProbeSessionLost can distinguish
// a real Sky.Live "session gone" from a proxy-rewritten 404. Without
// this marker, the page would loop on reconnect forever — the user-
// reported bug.
func TestSSESessionNotFoundCarriesSkyLiveHeader(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	// Deliberately do NOT seed the session — we want the 404 path.
	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "ghost-session"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 for unknown session, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Sky-Live"); got != "1" {
		t.Errorf("404 response missing X-Sky-Live: 1 (got %q) — client probe can't recover", got)
	}
}

// TestEventSessionNotFoundCarriesSkyLiveHeader: same contract for
// the POST /_sky/event 404 path. The client probe in
// __skyProbeSessionLost specifically targets this endpoint, so its
// header must be set or the reload-on-session-loss never fires.
func TestEventSessionNotFoundCarriesSkyLiveHeader(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	body := strings.NewReader(`{"sessionId":"ghost","msg":"X","args":[]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.handleEvent(rr, req)

	if rr.Code != 404 {
		t.Fatalf("expected 404, got %d body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Sky-Live"); got != "1" {
		t.Errorf("404 missing X-Sky-Live: 1 (got %q)", got)
	}
	if !strings.Contains(rr.Body.String(), "session not found") {
		t.Errorf("404 body should say 'session not found' so the probe can distinguish from handler-not-found, got %q",
			rr.Body.String())
	}
}

func TestPostEventHasSkyLiveHeader(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
		init: func(req any) any {
			return SkyTuple2{V0: "model", V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("hi")})
		},
		subscriptions: func(model any) any { return nil },
		msgTags:       map[string]int{},
	}
	// Seed a session.
	app.store.Set("sid-post", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		model:     "model",
		handlers:  map[string]any{},
	})

	body := strings.NewReader(`{"sessionId":"sid-post","msg":"Noop","args":[]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.handleEvent(rr, req)

	if got := rr.Header().Get("X-Sky-Live"); got != "1" {
		t.Errorf("/_sky/event response missing X-Sky-Live: 1 (got %q)", got)
	}
}
