package rt

// Idle-survival regression suite — the "idle 20-30min → disconnected →
// refresh fixes it" incident, on the sky_sid SESSION cookie.
//
// THE DEFECT (pre-fix): writeSessionCookie set the browser cookie's Max-Age to
// the session store TTL:
//
//	live.go: maxAge := int(ttl.Seconds())
//
// but the server-side session TTL SLIDES without bound — every store Get/Set
// touches lastSeen, and so does the SSE heartbeat (handleSSE, every
// sseHeartbeatInterval). The cookie, by contrast, is only re-issued by
// sessionIDNamed (a full page GET) or handleEvent (an event POST). An SSE
// stream's headers are written once at connect, so the heartbeat physically
// cannot re-issue a cookie mid-stream.
//
// A tab left IDLE under a live SSE therefore ends up in a state the runtime
// never intended: the server session is demonstrably ALIVE (heartbeat-slid,
// survives every cleanup tick) while the BROWSER has silently dropped sky_sid
// at the original TTL window. The next click POSTs with no session cookie,
// boundSessionID fails, writeSessionLost replies X-Sky-Status: session-lost,
// the client hard-reloads, and the user's Model is gone.
//
// This is the same class already fixed once for the __sky_csrf cookie
// (bug #11, TestCsrfCookieMaxAgeOutlivesShortIdleTTL) and left unfixed on the
// sky_sid cookie __sky_csrf exists to guard — so fixing the CSRF layer simply
// moved the failure one layer down, from a 403 to a session-lost.
//
// Why these live in Go and not only in scripts/verify-live-resilience.mjs: the
// Playwright gate runs only in scripts/preflight-tag.sh, never in rust-ci.yml,
// so this whole class could regress unseen between tags. These run in the
// codegen-build job on every push.

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newIdleTestApp — a Live app whose update() mutates the model observably, so
// a dispatch that actually landed is distinguishable from one that was
// rejected. sessionTTL is explicit: it is what writeSessionCookie is handed.
func newIdleTestApp(ttl time.Duration) *liveApp {
	app := newBindingTestApp("sky_sid")
	app.sessionTTL = ttl
	app.store.Close()
	app.store = newMemoryStore(ttl)
	return app
}

// sessionCookieFrom returns the sky_sid cookie the handler set, parsed.
func sessionCookieFrom(t *testing.T, rr *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rr.Result().Cookies() {
		if c.Name == "sky_sid" {
			return c
		}
	}
	t.Fatalf("no sky_sid cookie in response headers: %v", rr.Header())
	return nil
}

// ── 1. The invariant, asserted directly on the emitted header ─────────

// TestSessionCookieMaxAgeOutlivesShortIdleTTL pins the contract that the
// session cookie must outlive the session it identifies. The server-side reap
// is the sole authority on when a session ends; a cookie that expires FIRST
// destroys a session that is still alive.
//
// The mirror of TestCsrfCookieMaxAgeOutlivesShortIdleTTL, one layer down.
func TestSessionCookieMaxAgeOutlivesShortIdleTTL(t *testing.T) {
	const day = 24 * 3600

	// A deliberately short TTL — the shape an operator uses for a
	// fast-expiring session, and the shape the resilience gate uses (25s).
	app := newIdleTestApp(25 * time.Second)
	defer app.store.Close()

	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET /: status %d", rr.Code)
	}

	c := sessionCookieFrom(t, rr)
	if c.MaxAge < 7*day {
		t.Fatalf("session cookie Max-Age (%ds) must outlive an idle-sliding "+
			"session, not track the %s store TTL: the SSE heartbeat slides the "+
			"server session indefinitely but cannot re-issue a cookie into an "+
			"open stream, so a TTL-keyed Max-Age expires the cookie out from "+
			"under a live session (X-Sky-Status: session-lost on the next click)",
			c.MaxAge, app.sessionTTL)
	}
	// Still a PERSISTENT cookie — a session cookie (MaxAge 0, no Expires)
	// is dropped on last-tab-close by some browsers, which is the separate
	// regression cd9546ec fixed.
	if c.MaxAge <= 0 {
		t.Fatalf("session cookie must stay persistent (Max-Age > 0), got %d", c.MaxAge)
	}
}

// TestSessionCookieMaxAgeHonoursLongerConfiguredTTL — the floor must never
// SHORTEN a deliberately long session. An operator who configures ttl = "90d"
// gets a 90-day cookie, not a 30-day one.
func TestSessionCookieMaxAgeHonoursLongerConfiguredTTL(t *testing.T) {
	longTTL := 90 * 24 * time.Hour
	app := newIdleTestApp(longTTL)
	defer app.store.Close()

	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	c := sessionCookieFrom(t, rr)
	if want := int(longTTL.Seconds()); c.MaxAge < want {
		t.Fatalf("a configured TTL longer than the floor must win: "+
			"Max-Age=%d, want >= %d", c.MaxAge, want)
	}
}

// ── 2. The end-to-end reproduction, through a real cookie jar ─────────

// TestIdleUnderLiveSSE_NextEventStillDispatches is the incident itself,
// reproduced against the real handlers through a real http.Client whose
// cookiejar honours Max-Age exactly as a browser does.
//
// The configuration deliberately isolates the COOKIE from server eviction: the
// memory store's cleanup ticker is 60s, so within this test's ~2s the server
// session cannot have been reaped. Any failure here is therefore the browser
// having dropped a cookie for a session that is provably still alive — which
// is precisely the bug, and precisely what the gate's error message
// misattributed to touchLastSeen.
func TestIdleUnderLiveSSE_NextEventStillDispatches(t *testing.T) {
	prevHB := sseHeartbeatInterval
	sseHeartbeatInterval = 50 * time.Millisecond
	defer func() { sseHeartbeatInterval = prevHB }()

	const ttl = 1 * time.Second
	app := newIdleTestApp(ttl)
	defer app.store.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/_sky/sse", app.handleSSE)
	mux.HandleFunc("/", app.handleInitial)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}

	// The browser's first page load — mints + stores sky_sid.
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()

	var sid string
	for _, c := range jar.Cookies(mustParseURL(t, srv.URL)) {
		if c.Name == "sky_sid" {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatal("no sky_sid in the jar after the initial GET")
	}
	hid := clickHandlerID(t, app, sid)

	// The tab's live SSE. It is the ONLY thing touching the server during
	// the idle window, and it slides lastSeen on every heartbeat.
	sseCtx, cancelSSE := context.WithCancel(context.Background())
	defer cancelSSE()
	go func() {
		req, _ := http.NewRequestWithContext(sseCtx, http.MethodGet,
			srv.URL+"/_sky/sse?tab=t1&path=/", nil)
		if r, err := client.Do(req); err == nil {
			defer r.Body.Close()
			<-sseCtx.Done()
		}
	}()

	// Idle PAST the TTL, with no GET and no POST to re-issue the cookie.
	time.Sleep(ttl + 500*time.Millisecond)

	// Pre-condition: the server session must still be alive, or this test is
	// measuring eviction rather than cookie expiry.
	if _, ok := app.store.Get(sid); !ok {
		t.Fatalf("precondition failed: session %q was evicted server-side; "+
			"this test can only speak to cookie lifetime", sid)
	}

	// The decisive interaction — the user's next click after the idle.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/_sky/event",
		strings.NewReader(eventBody(sid, hid)))
	req.Header.Set("Content-Type", "application/json")
	evResp, err := client.Do(req) // jar attaches sky_sid IF it still holds it
	if err != nil {
		t.Fatalf("POST /_sky/event: %v", err)
	}
	defer evResp.Body.Close()

	if got := evResp.Header.Get("X-Sky-Status"); got == "session-lost" {
		t.Fatalf("post-idle event returned X-Sky-Status: session-lost while the "+
			"server session %q was still ALIVE — the browser dropped sky_sid "+
			"because its Max-Age tracked the %s TTL instead of outliving the "+
			"sliding session. The client hard-reloads here and the user's Model "+
			"is lost: the darraghstudio idle-disconnect incident.", sid, ttl)
	}
	if evResp.StatusCode != http.StatusOK {
		t.Fatalf("post-idle event: status %d (want 200)", evResp.StatusCode)
	}
	// The dispatch actually landed on the SAME session (model advanced),
	// rather than silently starting a new one.
	if got := modelOf(t, app, sid); got != "seed!" {
		t.Fatalf("post-idle dispatch did not mutate the original session: "+
			"model = %q, want %q", got, "seed!")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// ── 3. The other half of the mechanism: heartbeat vs cleanup ─────────

// TestLiveSSEConnectionSurvivesTTLCleanupTick pins the server-side invariant
// the gate's failure message names: a session with a LIVE SSE connection must
// not be evicted when it passes its TTL, because the heartbeat slides
// lastSeen.
//
// This half was already correct — it passes before and after the cookie fix —
// but it had no Go-level test at all, so nothing in CI would have caught it
// breaking. The negative control in the same test is what keeps this from
// being vacuous: a session with NO connection, under the identical cleanup
// pass, MUST be evicted. If the cleanup predicate ever stops firing, the
// control fails and the test goes red rather than silently passing.
func TestLiveSSEConnectionSurvivesTTLCleanupTick(t *testing.T) {
	prevHB := sseHeartbeatInterval
	sseHeartbeatInterval = 20 * time.Millisecond
	defer func() { sseHeartbeatInterval = prevHB }()

	const ttl = 150 * time.Millisecond
	app := &liveApp{
		store:  newMemoryStore(ttl),
		locker: newSessionLocker(),
	}
	defer app.store.Close()

	// The connected session — a live SSE holds it open.
	app.store.Set("sid-connected", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
	})
	// The negative control — same store, same TTL, no connection.
	app.store.Set("sid-idle-noconn", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
	})

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	sseCtx, cancelSSE := context.WithCancel(context.Background())
	defer cancelSSE()
	connected := make(chan struct{})
	go func() {
		req, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, srv.URL, nil)
		req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-connected"})
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer r.Body.Close()
		close(connected)
		<-sseCtx.Done()
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE never connected")
	}

	// Idle well past the TTL. Heartbeats (20ms) are the only activity.
	time.Sleep(4 * ttl)

	// Drive the memoryStore cleanup body inline — the same code the 60s
	// ticker runs (live_store.go cleanupLoop), so the test does not have to
	// wait a minute for a real tick.
	mem := app.store.(*memoryStore)
	mem.mu.Lock()
	now := time.Now()
	var expired []*liveSession
	for id, s := range mem.sessions {
		if now.Sub(s.lastSeenTime()) > mem.ttl {
			expired = append(expired, s)
			delete(mem.sessions, id)
		}
	}
	mem.mu.Unlock()
	for _, s := range expired {
		s.markDone()
	}

	// Negative control FIRST: if the unconnected session survived, the
	// cleanup predicate never fired and the positive assertion below would
	// be meaningless.
	if _, ok := mem.sessions["sid-idle-noconn"]; ok {
		t.Fatal("control: an unconnected session past its TTL was NOT evicted — " +
			"the cleanup pass did not fire, so this test cannot prove anything " +
			"about the connected session")
	}

	if _, ok := mem.sessions["sid-connected"]; !ok {
		t.Fatalf("a session with a LIVE SSE connection was evicted after %s "+
			"(TTL %s): the heartbeat's touchLastSeen is not sliding the session. "+
			"Every subsequent click on that tab 404s under a connection the "+
			"client still shows as green.", 4*ttl, ttl)
	}
}
