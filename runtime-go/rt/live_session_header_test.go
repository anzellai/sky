//go:build !js

package rt

// Header session transport (live_session_header.go): Sky.Live sessions for
// hosts that cannot keep cookies.
//
// The contract these tests lock:
//   - a header-mode page load mints a token, hands it over in the boot config
//     and the X-Sky-Session header, and sets NO session cookie;
//   - the store is keyed by the token's hash, never by the token;
//   - X-Sky-Session is accepted on the event, SSE and rotate endpoints, and a
//     session cookie is ignored (even one naming a live session);
//   - a state-changing request without the header is refused (the header is
//     the CSRF defence), and a cross-origin one is refused even with it; no
//     CSRF cookie is issued;
//   - the SSE fallback ticket is single-use, bound to its session and tab,
//     and expires;
//   - session-id rotation works: the rotate exchange returns the new token,
//     whose hash is the new session id, and the rotating tab's next request
//     with the old token is handed the new one; any other request with the
//     old token gets session-rotating, then session-lost;
//   - the transport resolves through the shared precedence rule, and an
//     unknown value keeps cookies.

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

func newHeaderTestApp(t *testing.T) *liveApp {
	t.Helper()
	app := newBindingTestApp("sky_sid")
	app.headerSessions = true
	t.Cleanup(func() { ResetRevocationGate() })
	return app
}

// mintHeaderSession performs a header-mode page load and returns the token
// and the session id it stands for.
func mintHeaderSession(t *testing.T, app *liveApp) (token, sid string, rr *httptest.ResponseRecorder) {
	t.Helper()
	rr = httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET /: status %d, body %s", rr.Code, rr.Body.String())
	}
	token = rr.Header().Get(skySessionHeader)
	if !validSessionID(token) {
		t.Fatalf("page load did not hand out a token in %s: %q", skySessionHeader, token)
	}
	return token, sessionTokenSID(token), rr
}

func headerEvent(app *liveApp, token, sid, hid, tab string) *httptest.ResponseRecorder {
	body := `{"sessionId":"` + sid + `","seq":1,"msg":"","args":[],"handlerId":"` + hid + `","tab":"` + tab + `"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(skySessionHeader, token)
	}
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	return rr
}

func noSessionCookie(t *testing.T, what string, h http.Header) {
	t.Helper()
	for _, line := range h.Values("Set-Cookie") {
		if strings.Contains(line, "sky_sid") {
			t.Fatalf("%s set a session cookie in header mode: %q", what, line)
		}
	}
}

func TestHeaderSession_PageLoadIssuesATokenAndNoCookie(t *testing.T) {
	app := newHeaderTestApp(t)
	token, sid, rr := mintHeaderSession(t, app)
	noSessionCookie(t, "page GET", rr.Header())
	// The page carries the token: no shared cache may keep it.
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("a header-mode page must be Cache-Control: no-store, got %q", cc)
	}
	if !strings.Contains(rr.Body.String(), `"tok":"`+token+`"`) {
		t.Fatalf("the boot config does not carry the token:\n%s", rr.Body.String())
	}
	if _, ok := app.store.Get(sid); !ok {
		t.Fatal("no session stored under the token's hash")
	}
	if _, ok := app.store.Get(token); ok {
		t.Fatal("the session store is keyed by the raw token")
	}
	if sid == token || !validSessionID(sid) {
		t.Fatalf("session id %q must be a hash of the token, of the minted shape", sid)
	}
}

func TestHeaderSession_HeaderAcceptedCookieIgnored(t *testing.T) {
	app := newHeaderTestApp(t)
	token, sid, _ := mintHeaderSession(t, app)
	hid := clickHandlerID(t, app, sid)

	// A cookie naming the live session is not a credential in header mode.
	for _, cookie := range []string{"sky_sid=" + sid, "sky_sid=" + token, "__Host-sky_sid=" + sid} {
		body := eventBody(sid, hid)
		rr := postEvent(app, cookie, body)
		if got := rr.Header().Get("X-Sky-Status"); got != "session-lost" {
			t.Fatalf("cookie %q dispatched in header mode (status %d, X-Sky-Status %q)", cookie, rr.Code, got)
		}
	}
	if got := modelOf(t, app, sid); got != "seed" {
		t.Fatalf("a cookie-only request changed the model to %q", got)
	}

	rr := headerEvent(app, token, sid, hid, "tab-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("event with the header: status %d body %s", rr.Code, rr.Body.String())
	}
	noSessionCookie(t, "event POST", rr.Header())
	if got := modelOf(t, app, sid); got != "seed!" {
		t.Fatalf("model after a header event = %q, want seed!", got)
	}
	// A token of the wrong shape, or of another session, is not accepted.
	if rr := headerEvent(app, "not-a-token", sid, hid, "tab-1"); rr.Code == http.StatusOK {
		t.Fatal("a malformed token dispatched")
	}
	if rr := headerEvent(app, newLiveSessionID(), sid, hid, "tab-1"); rr.Code == http.StatusOK {
		t.Fatal("another token dispatched into this session")
	}
}

func TestHeaderSession_CSRFIsTheHeaderPlusOrigin(t *testing.T) {
	app := newHeaderTestApp(t)
	SetCsrfEnabled(true)
	t.Cleanup(func() { refreshCsrfEnabled() })
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/", app.dispatchRoot)
	h := liveListenerHandlerFor(app, mux, "")

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("page GET through the listener chain: %d", get.Code)
	}
	for _, line := range get.Header().Values("Set-Cookie") {
		t.Fatalf("header mode set a cookie on the page load: %q", line)
	}
	token := get.Header().Get(skySessionHeader)
	sid := sessionTokenSID(token)
	hid := clickHandlerID(t, app, sid)

	post := func(withToken bool, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(eventBody(sid, hid)))
		req.Header.Set("Content-Type", "application/json")
		if withToken {
			req.Header.Set(skySessionHeader, token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := post(false, map[string]string{"Sec-Fetch-Site": "same-origin"}); rr.Code != http.StatusForbidden ||
		!strings.Contains(rr.Body.String(), "csrf_missing") {
		t.Fatalf("a POST without X-Sky-Session: %d %s, want 403 csrf_missing", rr.Code, rr.Body.String())
	}
	if rr := post(true, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); rr.Code != http.StatusForbidden {
		t.Fatalf("a cross-site POST with the header: %d, want 403", rr.Code)
	}
	if rr := post(true, map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}); rr.Code != http.StatusForbidden {
		t.Fatalf("Origin null: %d, want 403", rr.Code)
	}
	if rr := post(true, map[string]string{"Sec-Fetch-Site": "same-origin"}); rr.Code != http.StatusOK {
		t.Fatalf("a same-origin POST with the header: %d %s", rr.Code, rr.Body.String())
	}
	if rr := post(true, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://example.com"}); rr.Code != http.StatusOK {
		t.Fatalf("an Origin that is the app's own: %d %s", rr.Code, rr.Body.String())
	}
}

// readSSEUntil reads an SSE body until an event of the given name.
func readSSEUntil(t *testing.T, body string, event string) bool {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if sc.Text() == "event: "+event {
			return true
		}
	}
	return false
}

// sseOnce runs handleSSE until its first frames are written, then ends the
// request.
func sseOnce(app *liveApp, req *http.Request) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()
	rr := httptest.NewRecorder()
	app.handleSSE(rr, req.WithContext(ctx))
	return rr
}

func TestHeaderSession_SSEByHeaderAndByOneTimeTicket(t *testing.T) {
	app := newHeaderTestApp(t)
	token, sid, _ := mintHeaderSession(t, app)

	// The fetch-stream client: the header on the SSE request.
	req := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=t1&sl=1", nil)
	req.Header.Set(skySessionHeader, token)
	if rr := sseOnce(app, req); !readSSEUntil(t, rr.Body.String(), "hello") {
		t.Fatalf("SSE with the header got no hello:\n%s", rr.Body.String())
	}
	// A cookie alone opens nothing.
	req = httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=t1&sl=1", nil)
	req.Header.Set("Cookie", "sky_sid="+sid)
	if rr := sseOnce(app, req); readSSEUntil(t, rr.Body.String(), "hello") {
		t.Fatal("SSE opened with a session cookie in header mode")
	}

	issue := func(tab string) string {
		req := httptest.NewRequest(http.MethodPost, "/_sky/sse-ticket", strings.NewReader(`{"tab":"`+tab+`"}`))
		req.Header.Set(skySessionHeader, token)
		rr := httptest.NewRecorder()
		app.handleSSETicket(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("sse-ticket: %d %s", rr.Code, rr.Body.String())
		}
		noSessionCookie(t, "sse-ticket", rr.Header())
		var out struct {
			Ticket string `json:"ticket"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		if out.Ticket == "" || out.Ticket == token || out.Ticket == sid {
			t.Fatalf("bad ticket %q", out.Ticket)
		}
		return out.Ticket
	}
	open := func(ticket, tab string) bool {
		req := httptest.NewRequest(http.MethodGet, "/_sky/sse?sl=1&tab="+tab+"&tk="+ticket, nil)
		return readSSEUntil(t, sseOnce(app, req).Body.String(), "hello")
	}

	tk := issue("t1")
	if !open(tk, "t1") {
		t.Fatal("a fresh ticket did not open the stream")
	}
	if open(tk, "t1") {
		t.Fatal("a ticket opened the stream twice")
	}
	if open(issue("t1"), "other-tab") {
		t.Fatal("a ticket opened the stream for another tab")
	}
	old := sseTicketTTL
	sseTicketTTL = 20 * time.Millisecond
	t.Cleanup(func() { sseTicketTTL = old })
	tk = issue("t1")
	time.Sleep(40 * time.Millisecond)
	if open(tk, "t1") {
		t.Fatal("an expired ticket opened the stream")
	}
	// Without a token there is no ticket.
	req = httptest.NewRequest(http.MethodPost, "/_sky/sse-ticket", strings.NewReader(`{"tab":"t1"}`))
	rr := httptest.NewRecorder()
	app.handleSSETicket(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatal("sse-ticket issued a ticket without a session token")
	}
	// The ticket endpoint does not exist in cookie mode.
	cookieApp := newBindingTestApp("sky_sid")
	rr = httptest.NewRecorder()
	cookieApp.handleSSETicket(rr, httptest.NewRequest(http.MethodPost, "/_sky/sse-ticket", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cookie mode /_sky/sse-ticket: %d, want 404", rr.Code)
	}
}

func TestHeaderSession_RotationHandsTheNewTokenToTheRotatingTab(t *testing.T) {
	app := newHeaderTestApp(t)
	oldTok, oldSid, _ := mintHeaderSession(t, app)
	hid := clickHandlerID(t, app, oldSid)
	sess := mustGet(t, app, oldSid)
	actID, actCh, _ := sess.registerSSEConn("tab-a")
	_ = actID

	// The bind runs on a goroutine stamped with the acting request's tab and
	// token, as a Cmd.perform of that request does.
	withLiveOriginTab("tab-a", func() {
		restore := stampLiveSessionToken(oldTok)
		defer restore()
		app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	})
	newSid := sess.currentSID()
	if newSid == oldSid {
		t.Fatal("binding a user did not rotate the session id")
	}

	var ticket string
	select {
	case fr := <-actCh:
		var p struct {
			Ticket string `json:"ticket"`
		}
		_ = json.Unmarshal([]byte(fr.data), &p)
		ticket = p.Ticket
	case <-time.After(time.Second):
		t.Fatal("the acting tab got no rotate frame")
	}
	if ticket == "" {
		t.Fatal("rotate frame without a ticket")
	}

	rotate := func(token, tab, tk string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/_sky/rotate",
			strings.NewReader(`{"tab":"`+tab+`","ticket":"`+tk+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(skySessionHeader, token)
		rr := httptest.NewRecorder()
		app.handleRotate(rr, req)
		return rr
	}
	if rr := rotate(oldTok, "attacker-tab", ticket); rr.Code == http.StatusOK {
		t.Fatal("another tab redeemed the rotation ticket")
	}
	rr := rotate(oldTok, "tab-a", ticket)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	noSessionCookie(t, "rotate", rr.Header())
	var out struct {
		Sid   string `json:"sid"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Token == "" || rr.Header().Get(skySessionHeader) != out.Token {
		t.Fatalf("rotate must return the new token in the body and header: %s / %q",
			rr.Body.String(), rr.Header().Get(skySessionHeader))
	}
	if sessionTokenSID(out.Token) != newSid || out.Sid != newSid {
		t.Fatalf("the new token names %q, want the rotated id %q", sessionTokenSID(out.Token), newSid)
	}
	// The new token drives the session.
	if rr := headerEvent(app, out.Token, newSid, hid, "tab-a"); rr.Code != http.StatusOK {
		t.Fatalf("event with the new token: %d %s", rr.Code, rr.Body.String())
	}
	// The rotating tab's event with the OLD token inside the grace window is
	// handed the new token on the response.
	rr = headerEvent(app, oldTok, oldSid, hid, "tab-a")
	if rr.Code != http.StatusOK || rr.Header().Get(skySessionHeader) != out.Token {
		t.Fatalf("the rotating tab's old-token event: %d, X-Sky-Session %q (want the new token)",
			rr.Code, rr.Header().Get(skySessionHeader))
	}
	// Any other tab with the old token: session-rotating, then session-lost.
	rr = headerEvent(app, oldTok, oldSid, hid, "attacker-tab")
	if got := rr.Header().Get("X-Sky-Status"); got != "session-rotating" {
		t.Fatalf("old token from another tab: X-Sky-Status %q, want session-rotating", got)
	}
	if rr.Header().Get(skySessionHeader) != "" {
		t.Fatal("the old token from another tab was handed a token")
	}
	expireRotationGrace(app, oldSid)
	rr = headerEvent(app, oldTok, oldSid, hid, "tab-a")
	if got := rr.Header().Get("X-Sky-Status"); got != "session-lost" {
		t.Fatalf("after the grace window the old token: %q, want session-lost", got)
	}
}

func TestHeaderSession_TokenlessRotationEndsAfterGrace(t *testing.T) {
	app := newHeaderTestApp(t)
	oldTok, oldSid, _ := mintHeaderSession(t, app)
	sess := mustGet(t, app, oldSid)
	// A rotation with no token in scope (a Time.every tick): nobody can
	// derive the new token.
	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	if _, ok := tokenThroughHopsFrom(app, oldTok); ok {
		t.Fatal("a tokenless rotation produced a derivable token")
	}
}

func TestHeaderSession_TransportResolution(t *testing.T) {
	t.Setenv("SKY_LIVE_SESSION_TRANSPORT", "")
	if got := resolveSessionTransport(""); got != sessionTransportCookie {
		t.Fatalf("default transport %q, want cookie", got)
	}
	if got := resolveSessionTransport("header"); got != sessionTransportHeader {
		t.Fatalf("builder header: %q", got)
	}
	if got := resolveSessionTransport("bogus"); got != sessionTransportCookie {
		t.Fatalf("an unknown value must keep cookies, got %q", got)
	}
	t.Setenv("SKY_LIVE_SESSION_TRANSPORT", "header")
	if got := resolveSessionTransport(""); got != sessionTransportHeader {
		t.Fatalf("operator env header: %q", got)
	}
	t.Setenv("SKY_LIVE_SESSION_TRANSPORT", "cookie")
	if got := resolveSessionTransport("header"); got != sessionTransportCookie {
		t.Fatalf("the operator env must beat the builder, got %q", got)
	}
	cfg := Live_withSessionTransport("header", map[string]any{}).(map[string]any)
	if cfg["SessionTransport"] != "header" {
		t.Fatalf("Live.withSessionTransport did not set the field: %v", cfg)
	}
}

// End to end over a real listener: a served header-mode app never sets a
// session cookie, and its event and SSE endpoints answer the token.
func TestHeaderSession_ServedAppEndToEnd(t *testing.T) {
	serveTestEnv(t)
	cfg := serveTestCfg(0, "hdr")
	cfg["SessionTransport"] = "header"
	ls := serveForTest(t, cfg)
	addr := Live_address(ls).(string)
	resp, err := serveTestClient.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	noSessionCookie(t, "served page GET", resp.Header)
	token := resp.Header.Get(skySessionHeader)
	if !validSessionID(token) {
		t.Fatalf("served page GET handed no token: %q", token)
	}
	sid := sessionTokenSID(token)
	hid := clickHandlerID(t, ls.app, sid)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/_sky/event", strings.NewReader(eventBody(sid, hid)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(skySessionHeader, token)
	resp, err = serveTestClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("served event: %d", resp.StatusCode)
	}
	noSessionCookie(t, "served event", resp.Header)
	if got := modelOf(t, ls.app, sid); got != "hdr!" {
		t.Fatalf("model %q, want hdr!", got)
	}
}

// A Sky.Spa backend (Server.rpc routes, cookie-authenticated) refuses to start
// under SKY_LIVE_SESSION_TRANSPORT=header instead of serving anonymous RPCs.
func TestHeaderSession_ServerRpcRefusesTheHeaderTransport(t *testing.T) {
	routes := []any{
		SkyRoute{Method: "GET", Path: "/", Handler: nil},
		SkyRoute{Method: "POST", Path: "/_rpc/Save", Handler: nil, Rpc: true},
	}
	t.Setenv("SKY_LIVE_SESSION_TRANSPORT", "")
	if err := rpcRefusesHeaderSessions(routes); err != nil {
		t.Fatalf("cookie transport refused a Server.rpc route set: %v", err)
	}
	t.Setenv("SKY_LIVE_SESSION_TRANSPORT", "header")
	err := rpcRefusesHeaderSessions(routes)
	if err == nil || !strings.Contains(err.Error(), "/_rpc/Save") || !strings.Contains(err.Error(), "Sky.Live only") {
		t.Fatalf("header transport with Server.rpc routes: %v, want a refusal naming the route", err)
	}
	res := serverListenRun(0, routes).(SkyResult[any, any])
	if res.Tag == 0 {
		t.Fatal("Server.listen started with Server.rpc routes under the header transport")
	}
	if err := rpcRefusesHeaderSessions(routes[:1]); err != nil {
		t.Fatalf("a server without Server.rpc routes was refused: %v", err)
	}
}
