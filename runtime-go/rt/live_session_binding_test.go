package rt

// Session-binding regression suite for /_sky/event.
//
// THE VULNERABILITY (pre-fix): handleEvent resolved the Live session
// from `req.SessionID` — a plain JSON field in the attacker-controlled
// request body — and never compared it to the session cookie the caller
// actually presented:
//
//	live.go: var req struct { SessionID string `json:"sessionId"`; … }
//	live.go: sess, ok := app.store.Get(req.SessionID)
//
// So ANY client that learns another user's sid can POST /_sky/event and
// drive that session: dispatch Msgs into it, mutate its Model, fire its
// handlers, and read back whatever the resulting view renders. The sid is
// not confidential in practice — the cookie is HttpOnly, but the same sid
// is ALSO templated into the page JS (`var __skySid = %q`) and echoed in
// every request body, so it leaks through XSS, extensions, screenshots,
// proxy access logs and referrers.
//
// handleSSE has always required the cookie (live.go handleSSE reads
// r.Cookie(cookieName) and 400s when absent), so the cookie is already a
// hard requirement for any functioning Sky.Live client. These tests pin
// the same requirement on the event channel.
//
// The contract these tests lock:
//   - The SESSION COOKIE is the sole authority for which session an event
//     dispatches into. A body `sessionId` is advisory: it may agree, or be
//     omitted, but it may never override.
//   - A request with no session cookie cannot dispatch at all.
//   - The rejection is INDISTINGUISHABLE from "unknown session" so the
//     endpoint is not an oracle for which sids exist.
//   - The cookie NAME is the one the session was minted with
//     (app.cookieName — sub-apps use "sky_<name>_sid", see
//     subapp_inprocess.go), never a hardcoded "sky_sid".

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newBindingTestApp builds a minimal Live app whose update() mutates the
// model observably ("seed" -> "seed!" -> "seed!!"), so a hijacked
// dispatch is detectable by inspecting the victim's model afterwards.
func newBindingTestApp(cookieName string) *liveApp {
	return &liveApp{
		init: func(req any) any {
			return SkyTuple2{V0: "seed", V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			next := model
			if s, ok := model.(string); ok {
				next = s + "!"
			}
			return SkyTuple2{V0: next, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			label := ""
			if s, ok := model.(string); ok {
				label = s
			}
			return velement("div", nil, []any{
				velement("button",
					[]any{eventPair{name: "click", msg: "ClickMsg"}},
					[]any{vtext(label)}),
			})
		},
		subscriptions: func(model any) any { return nil },
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
		cookieName:    cookieName,
	}
}

// sidFromNamedSetCookie extracts a session id from a Set-Cookie header for
// an arbitrary cookie name (sub-apps do not use "sky_sid").
func sidFromNamedSetCookie(t *testing.T, setCookie, cookieName string) string {
	t.Helper()
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, cookieName+"=") {
			return strings.TrimPrefix(part, cookieName+"=")
		}
	}
	t.Fatalf("no %s in Set-Cookie: %q", cookieName, setCookie)
	return ""
}

// mintSession performs the initial GET that a browser does, returning the
// session id and the Cookie header value the browser would send back.
func mintSession(t *testing.T, app *liveApp, cookieName string) (sid, cookie string) {
	t.Helper()
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET /: status %d, body %s", rr.Code, rr.Body.String())
	}
	setCookie := rr.Header().Get("Set-Cookie")
	sid = sidFromNamedSetCookie(t, setCookie, cookieName)
	return sid, cookieName + "=" + sid
}

// clickHandlerID returns the sole click handler id registered for a session.
func clickHandlerID(t *testing.T, app *liveApp, sid string) string {
	t.Helper()
	sess, ok := app.store.Get(sid)
	if !ok {
		t.Fatalf("session %q not in store", sid)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for hid := range sess.handlers {
		if strings.HasSuffix(hid, ".click") {
			return hid
		}
	}
	t.Fatalf("no .click handler registered for %q: %+v", sid, sess.handlers)
	return ""
}

func modelOf(t *testing.T, app *liveApp, sid string) string {
	t.Helper()
	sess, ok := app.store.Get(sid)
	if !ok {
		t.Fatalf("session %q not in store", sid)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	s, _ := sess.model.(string)
	return s
}

// postEvent POSTs an /_sky/event body, optionally with a Cookie header.
func postEvent(app *liveApp, cookie, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	return rr
}

func eventBody(sid, hid string) string {
	return `{"sessionId":"` + sid + `","seq":1,"msg":"","args":[],"handlerId":"` + hid + `"}`
}

// ── The exploit ──────────────────────────────────────────────────────

// TestHandleEvent_ForeignSessionIDWithAttackersOwnCookie is the hijack.
// Victim A and attacker B each hold their own legitimate session. B POSTs
// an event naming A's sessionId while presenting B's OWN cookie. Pre-fix
// this dispatched into A's session; it must be rejected.
func TestHandleEvent_ForeignSessionIDWithAttackersOwnCookie(t *testing.T) {
	app := newBindingTestApp("sky_sid")

	victimSid, victimCookie := mintSession(t, app, "sky_sid")
	attackerSid, attackerCookie := mintSession(t, app, "sky_sid")
	if victimSid == attackerSid {
		t.Fatalf("sessions collided: %q", victimSid)
	}
	_ = victimCookie
	hid := clickHandlerID(t, app, victimSid)
	before := modelOf(t, app, victimSid)

	rr := postEvent(app, attackerCookie, eventBody(victimSid, hid))

	if rr.Code == http.StatusOK {
		t.Errorf("HIJACK: attacker (cookie %s) drove victim session %s — status 200, body %s",
			attackerSid, victimSid, rr.Body.String())
	}
	if got := modelOf(t, app, victimSid); got != before {
		t.Errorf("HIJACK: victim model mutated by attacker: %q -> %q", before, got)
	}
	if got := modelOf(t, app, attackerSid); got != "seed" {
		t.Errorf("attacker's own session must be untouched, got %q", got)
	}
}

// TestHandleEvent_ForeignSessionIDWithNoCookie — the cheapest form of the
// same attack: a bare curl/script that knows the sid and simply sends no
// cookie at all. A fix that only rejects on MISMATCH (and falls back to the
// body sid when no cookie is present) would leave this wide open, so it is
// pinned separately.
func TestHandleEvent_ForeignSessionIDWithNoCookie(t *testing.T) {
	app := newBindingTestApp("sky_sid")

	victimSid, _ := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, victimSid)
	before := modelOf(t, app, victimSid)

	rr := postEvent(app, "", eventBody(victimSid, hid))

	if rr.Code == http.StatusOK {
		t.Errorf("HIJACK: cookie-less POST drove session %s — status 200, body %s",
			victimSid, rr.Body.String())
	}
	if got := modelOf(t, app, victimSid); got != before {
		t.Errorf("HIJACK: victim model mutated by cookie-less caller: %q -> %q", before, got)
	}
}

// TestHandleEvent_BatchHijack — the sendBeacon batch path takes a separate
// early-return branch inside handleEvent, so it needs its own pin.
func TestHandleEvent_BatchHijack(t *testing.T) {
	app := newBindingTestApp("sky_sid")

	victimSid, _ := mintSession(t, app, "sky_sid")
	_, attackerCookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, victimSid)
	before := modelOf(t, app, victimSid)

	body := `{"sessionId":"` + victimSid + `","batch":[{"msg":"","args":[],"handlerId":"` + hid + `"}]}`
	rr := postEvent(app, attackerCookie, body)

	if rr.Code == http.StatusNoContent || rr.Code == http.StatusOK {
		t.Errorf("HIJACK (batch): attacker drove victim session %s — status %d",
			victimSid, rr.Code)
	}
	if got := modelOf(t, app, victimSid); got != before {
		t.Errorf("HIJACK (batch): victim model mutated: %q -> %q", before, got)
	}
}

// TestHandleEvent_CustomCookieNameHijack — the console sub-app mounts with
// cookieName "sky_<name>_sid" (subapp_inprocess.go). A fix that hardcodes
// "sky_sid" would read no cookie for a sub-app request and either break every
// sub-app event or fall back to trusting the body. Pin both directions.
func TestHandleEvent_CustomCookieNameHijack(t *testing.T) {
	const name = "sky_console_sid"
	app := newBindingTestApp(name)

	victimSid, _ := mintSession(t, app, name)
	_, attackerCookie := mintSession(t, app, name)
	hid := clickHandlerID(t, app, victimSid)
	before := modelOf(t, app, victimSid)

	rr := postEvent(app, attackerCookie, eventBody(victimSid, hid))
	if rr.Code == http.StatusOK {
		t.Errorf("HIJACK (sub-app cookie name): status 200, body %s", rr.Body.String())
	}
	if got := modelOf(t, app, victimSid); got != before {
		t.Errorf("HIJACK (sub-app cookie name): victim model mutated: %q -> %q", before, got)
	}

	// A DECOY cookie under the DEFAULT name must not satisfy the check
	// either — the binding must use the name the session was minted with.
	rr2 := postEvent(app, "sky_sid="+victimSid, eventBody(victimSid, hid))
	if rr2.Code == http.StatusOK {
		t.Errorf("HIJACK (decoy sky_sid cookie on a sub-app): status 200, body %s", rr2.Body.String())
	}
}

// TestHandleEvent_RejectionIsNotAnExistenceOracle — a rejected request for a
// sid that EXISTS must be byte-identical to one for a sid that does not, so
// the endpoint cannot be used to enumerate live sessions.
func TestHandleEvent_RejectionIsNotAnExistenceOracle(t *testing.T) {
	app := newBindingTestApp("sky_sid")

	victimSid, _ := mintSession(t, app, "sky_sid")
	_, attackerCookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, victimSid)

	real := postEvent(app, attackerCookie, eventBody(victimSid, hid))
	fake := postEvent(app, attackerCookie, eventBody("0000000000000000000000000000dead", hid))

	if real.Code != fake.Code {
		t.Errorf("existence oracle: real sid -> %d, unknown sid -> %d", real.Code, fake.Code)
	}
	if real.Body.String() != fake.Body.String() {
		t.Errorf("existence oracle via body: real %q vs unknown %q",
			real.Body.String(), fake.Body.String())
	}
	if real.Header().Get("X-Sky-Status") != fake.Header().Get("X-Sky-Status") {
		t.Errorf("existence oracle via X-Sky-Status: real %q vs unknown %q",
			real.Header().Get("X-Sky-Status"), fake.Header().Get("X-Sky-Status"))
	}
}

// ── Compatibility guards (these must pass BEFORE and AFTER the fix) ──

// TestHandleEvent_OwnCookieStillWorks — the normal in-page POST
// (fetch(..., credentials:"same-origin")) carries the cookie and its body
// sid equals it. This is the path every real click takes; it must keep
// working.
func TestHandleEvent_OwnCookieStillWorks(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	rr := postEvent(app, cookie, eventBody(sid, hid))
	if rr.Code != http.StatusOK {
		t.Fatalf("legitimate event rejected: status %d, body %s", rr.Code, rr.Body.String())
	}
	if got := modelOf(t, app, sid); got != "seed!" {
		t.Errorf("legitimate dispatch did not run: model %q, want %q", got, "seed!")
	}
}

// TestHandleEvent_OwnCookieBatchStillWorks — the sendBeacon unload flush.
func TestHandleEvent_OwnCookieBatchStillWorks(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	body := `{"sessionId":"` + sid + `","batch":[{"msg":"","args":[],"handlerId":"` + hid + `"}]}`
	rr := postEvent(app, cookie, body)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("legitimate beacon batch rejected: status %d, body %s", rr.Code, rr.Body.String())
	}
	if got := modelOf(t, app, sid); got != "seed!" {
		t.Errorf("beacon batch did not dispatch: model %q", got)
	}
}

// TestHandleEvent_OmittedBodySessionIDStillWorks — the body sid is advisory.
// A client that sends the cookie but omits sessionId must still dispatch;
// this keeps the wire format forward-compatible with dropping the field.
func TestHandleEvent_OmittedBodySessionIDStillWorks(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	body := `{"seq":1,"msg":"","args":[],"handlerId":"` + hid + `"}`
	rr := postEvent(app, cookie, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("cookie-only event rejected: status %d, body %s", rr.Code, rr.Body.String())
	}
	if got := modelOf(t, app, sid); got != "seed!" {
		t.Errorf("cookie-only dispatch did not run: model %q", got)
	}
}

// TestHandleEvent_MultiTabSharesOneCookie — the v0.18 "session's tabs mirror
// ONE shared view" design: a second tab reuses the SAME cookie, so both tabs'
// events bind to the same session. Confirms the fix does not split tabs.
func TestHandleEvent_MultiTabSharesOneCookie(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")

	// Second tab: a GET carrying the existing cookie must NOT mint a new sid.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", cookie)
	app.handleInitial(rr, req)
	if sid2 := sidFromNamedSetCookie(t, rr.Header().Get("Set-Cookie"), "sky_sid"); sid2 != sid {
		t.Fatalf("second tab minted a different session: %q vs %q", sid2, sid)
	}

	hid := clickHandlerID(t, app, sid)
	for i, tab := range []string{"tabA", "tabB"} {
		body := `{"sessionId":"` + sid + `","seq":1,"msg":"","args":[],"handlerId":"` +
			hid + `","tab":"` + tab + `"}`
		if got := postEvent(app, cookie, body); got.Code != http.StatusOK {
			t.Fatalf("tab %s (event %d) rejected: status %d, body %s",
				tab, i, got.Code, got.Body.String())
		}
	}
	if got := modelOf(t, app, sid); got != "seed!!" {
		t.Errorf("both tabs should have dispatched into one session: model %q, want %q",
			got, "seed!!")
	}
}

// TestHandleEvent_CookieSlidesOnDispatch — binding the event to the cookie
// makes the BROWSER's cookie lifetime load-bearing for the event channel, not
// just for page loads. sessionIDNamed re-issues a fresh MaxAge on every GET
// (live.go, "L2" comment); a long-lived page that never navigates would
// otherwise let the cookie lapse while the server session slid on activity,
// and every subsequent click would 404. Re-issue on dispatch closes that.
func TestHandleEvent_CookieSlidesOnDispatch(t *testing.T) {
	app := newBindingTestApp("sky_sid")
	sid, cookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, sid)

	rr := postEvent(app, cookie, eventBody(sid, hid))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	set := rr.Header().Get("Set-Cookie")
	if !strings.Contains(set, "sky_sid="+sid) {
		t.Errorf("dispatch did not re-issue the session cookie: %q", set)
	}
	if !strings.Contains(set, "Max-Age=") {
		t.Errorf("re-issued cookie has no sliding Max-Age: %q", set)
	}
}
