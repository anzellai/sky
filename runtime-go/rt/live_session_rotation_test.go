//go:build !js

package rt

// Session-id rotation + revocation-snapshot regression suite.
//
// THE DEFECTS (pre-fix):
//
//  A. Session fixation. sessionIDNamed adopted ANY presented sky_sid, and
//     Live.bindSessionUser stamped the user onto that SAME sid. An attacker
//     who planted a sid in the victim's browser (a sid they got by visiting
//     the site themselves, via a sibling subdomain, plain HTTP or XSS) shared
//     the victim's signed-in session after the victim signed in.
//
//  B. Revocation bypass. evictForAccess deleted only the session-store
//     entry. The durable-TEA snapshot keyed by the same sid survived, so the
//     next GET with the old cookie restored the signed-in Model as an
//     UNBOUND session, which the revocation gate lets through.
//
// The contract these tests lock:
//   - Every change of bound user (first bind, account switch) moves the
//     session to a NEW sid. The same *liveSession object is re-keyed, so open
//     SSE connections of the acting tab stay attached.
//   - The OLD sid never again grants access to the session. Inside a short
//     grace window a request that presents only the old cookie gets
//     "session-rotating" (retry, no reload); after it, "session-lost".
//   - The new sid reaches only the tab that performed the sign-in: its SSE
//     connection gets a `rotate` frame with a one-time ticket that
//     /_sky/rotate exchanges for the new cookie, and its own event POSTs get
//     the new cookie directly. Every other SSE connection of the session is
//     closed, so a connection the attacker opened before the sign-in cannot
//     keep watching the signed-in view.
//   - The durable snapshot moves to the new sid; the old key is gone.
//   - Eviction and Live.endSession delete the snapshot, so a revoked or
//     ended sid restores nothing.
//   - A presented sid that is not 32 lowercase hex is replaced, not adopted.
//   - The cookie is `__Host-sky_sid` when it is Secure and `sky_sid` over
//     plain HTTP; both names are read.
//   - A stable per-session key survives rotation.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── helpers ─────────────────────────────────────────────────────────

// fakeDurableStore is an in-memory stand-in for the Std.Durable snapshot
// table, driven through the same wiring record shape Std.App builds.
type fakeDurableStore struct {
	mu   sync.Mutex
	rows map[string]any
}

func newFakeDurable() (*fakeDurableStore, *durableCtx) {
	f := &fakeDurableStore{rows: map[string]any{}}
	wiring := map[string]any{
		"Enabled": true,
		"RunId":   "",
		"Setup":   func() any { return Ok[any, any](struct{}{}) },
		"Restore": func(runId any) any {
			return func() any {
				f.mu.Lock()
				defer f.mu.Unlock()
				if m, ok := f.rows[runId.(string)]; ok {
					return Ok[any, any](Just[any](m))
				}
				return Ok[any, any](Nothing[any]())
			}
		},
		"ApplyRestore": func(res any, initModel any) any {
			if r, ok := res.(SkyResult[any, any]); ok && r.Tag == 0 {
				if mb, ok := r.OkValue.(SkyMaybe[any]); ok && mb.Tag == 0 {
					return mb.JustValue
				}
			}
			return initModel
		},
		"Persist": func(runId any, model any) any {
			return func() any {
				f.mu.Lock()
				f.rows[runId.(string)] = model
				f.mu.Unlock()
				return Ok[any, any](struct{}{})
			}
		},
		"Discard": func(runId any) any {
			return func() any {
				f.mu.Lock()
				delete(f.rows, runId.(string))
				f.mu.Unlock()
				return Ok[any, any](struct{}{})
			}
		},
	}
	return f, durableCtxOf(wiring)
}

func (f *fakeDurableStore) has(runId string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[runId]
	return ok
}

// waitFor polls cond for up to 2 s (the durable persist is fire-and-forget).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func newRotationTestApp(t *testing.T) *liveApp {
	t.Helper()
	app := newBindingTestApp("sky_sid")
	t.Cleanup(func() { ResetRevocationGate() })
	return app
}

// mustGet returns the live session stored under sid.
func mustGet(t *testing.T, app *liveApp, sid string) *liveSession {
	t.Helper()
	sess, ok := app.store.Get(sid)
	if !ok || sess == nil {
		t.Fatalf("session %q not in store", sid)
	}
	return sess
}

// cookieFromResponse returns the value of the named cookie a response set,
// or "" when it set none.
func cookieFromResponse(rr *httptest.ResponseRecorder, name string) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c.Value
		}
	}
	return ""
}

func postEventTab(app *liveApp, cookie, sid, hid, tab string) *httptest.ResponseRecorder {
	body := `{"sessionId":"` + sid + `","seq":1,"msg":"","args":[],"handlerId":"` + hid + `","tab":"` + tab + `"}`
	return postEvent(app, cookie, body)
}

func postRotate(app *liveApp, cookie, tab, ticket string) *httptest.ResponseRecorder {
	body := `{"tab":"` + tab + `","ticket":"` + ticket + `"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/rotate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	app.handleRotate(rr, req)
	return rr
}

// bindFromTab binds uid the way a Cmd.perform spawned by tab's event does.
func bindFromTab(app *liveApp, sess *liveSession, uid, tab string) {
	withLiveOriginTab(tab, func() {
		app.bindSessionUserTo(sess, uid, time.Now().Unix())
	})
}

// ── A: rotation on every change of bound user ───────────────────────

func TestSessionRotation_FirstBindMovesSessionToNewSid(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, _ := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)

	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())

	newSid := sess.currentSID()
	if newSid == oldSid {
		t.Fatalf("FIXATION: binding a user kept the pre-login sid %q", oldSid)
	}
	if !validSessionID(newSid) {
		t.Fatalf("rotated sid %q is not 32 lowercase hex", newSid)
	}
	if _, ok := app.store.Get(oldSid); ok {
		t.Fatalf("FIXATION: the pre-login sid %q still resolves in the store", oldSid)
	}
	if got := mustGet(t, app, newSid); got != sess {
		t.Fatalf("rotation must re-key the SAME session object, got a different one")
	}
}

func TestSessionRotation_AccountSwitchRotatesAgain_SameUserDoesNot(t *testing.T) {
	app := newRotationTestApp(t)
	sid0, _ := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, sid0)

	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	sidA := sess.currentSID()
	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	if sess.currentSID() != sidA {
		t.Fatalf("re-binding the SAME user must not rotate (%q -> %q)", sidA, sess.currentSID())
	}
	app.bindSessionUserTo(sess, "user-2", time.Now().Unix())
	sidB := sess.currentSID()
	if sidB == sidA || sidB == sid0 {
		t.Fatalf("account switch must rotate to a fresh sid: sid0=%q A=%q B=%q", sid0, sidA, sidB)
	}
	if _, ok := app.store.Get(sidA); ok {
		t.Fatalf("the previous user's sid %q still resolves after an account switch", sidA)
	}
}

// The fixation attacker keeps the planted (pre-login) cookie. It must never
// dispatch into the signed-in session: inside the grace window it gets
// session-rotating, after it session-lost.
func TestSessionRotation_OldCookieCannotDriveSignedInSession(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, oldSid)
	sess := mustGet(t, app, oldSid)
	bindFromTab(app, sess, "user-1", "victim-tab")
	newSid := sess.currentSID()
	before := modelOf(t, app, newSid)

	rr := postEventTab(app, oldCookie, oldSid, hid, "attacker-tab")
	if rr.Code == http.StatusOK {
		t.Fatalf("FIXATION: the pre-login cookie dispatched into the signed-in session (body %s)", rr.Body.String())
	}
	if got := rr.Header().Get("X-Sky-Status"); got != "session-rotating" {
		t.Fatalf("inside the grace window the old cookie must get session-rotating, got %q (code %d)", got, rr.Code)
	}
	if c := cookieFromResponse(rr, "sky_sid"); c == newSid {
		t.Fatalf("FIXATION: the old cookie was handed the new sid")
	}
	if strings.Contains(rr.Body.String(), newSid) || rr.Header().Get("X-Sky-Sid") == newSid ||
		rr.Header().Get("X-Sky-Sid") == sidTag(newSid) {
		t.Fatalf("FIXATION: the response to the old cookie leaked the new sid")
	}
	if got := modelOf(t, app, newSid); got != before {
		t.Fatalf("FIXATION: model mutated through the old cookie: %q -> %q", before, got)
	}

	// After the grace window the old sid is dead: session-lost.
	expireRotationGrace(app, oldSid)
	rr = postEventTab(app, oldCookie, oldSid, hid, "attacker-tab")
	if got := rr.Header().Get("X-Sky-Status"); got != "session-lost" {
		t.Fatalf("after the grace window the old cookie must get session-lost, got %q (code %d)", got, rr.Code)
	}
}

// A page GET with the dead old sid mints a FRESH sid rather than creating a
// new session under the old value.
func TestSessionRotation_DeadOldSidIsNotReadopted(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	expireRotationGrace(app, oldSid)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", oldCookie)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	got := cookieFromResponse(rr, "sky_sid")
	if got == "" || got == oldSid {
		t.Fatalf("a GET with a rotated-away sid must mint a fresh sid, got %q (old %q)", got, oldSid)
	}
	if got == sess.currentSID() {
		t.Fatalf("FIXATION: the GET with the old sid was given the signed-in sid")
	}
}

// Inside the grace window a page GET with only the old cookie is told to
// retry: it neither gets the signed-in session nor a replacement cookie
// (which would overwrite the new one in the victim's jar).
func TestSessionRotation_PageGetInsideGraceRetries(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", oldCookie)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with the old cookie inside the grace window: status %d, want 503", rr.Code)
	}
	if rr.Header().Get("Set-Cookie") != "" {
		t.Fatalf("the retry answer must set no cookie: %q", rr.Header().Get("Set-Cookie"))
	}
	if strings.Contains(rr.Body.String(), sess.currentSID()) {
		t.Fatalf("FIXATION: the retry page leaked the new sid")
	}
}

// Another tab of the victim's browser: the jar already holds the NEW cookie
// (the acting tab redeemed it), but the tab's JS still echoes the old sid in
// the body. The cookie is the authority, the body sid is an alias of it, so
// the event dispatches and the response tells the tab its new sid.
func TestSessionRotation_OtherTabWithNewCookieAndOldBodySid(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, _ := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, oldSid)
	sess := mustGet(t, app, oldSid)
	bindFromTab(app, sess, "user-1", "tab-a")
	newSid := sess.currentSID()

	rr := postEventTab(app, "sky_sid="+newSid, oldSid, hid, "tab-b")
	if rr.Code != http.StatusOK {
		t.Fatalf("new cookie + old body sid must dispatch, got %d: %s", rr.Code, rr.Body.String())
	}
	// E-13: cookie mode names the session by its tag, never by the id (a
	// response header is logged by proxies; the id is the credential).
	if got := rr.Header().Get("X-Sky-Sid"); got != sidTag(newSid) {
		t.Fatalf("the response must tell the tab its new session tag: X-Sky-Sid=%q, want %q", got, sidTag(newSid))
	}
	if got := modelOf(t, app, newSid); got != "seed!" {
		t.Fatalf("event did not dispatch into the rotated session: model %q", got)
	}
	// The tab echoes the tag from now on, and that dispatches.
	rr = postEventTab(app, "sky_sid="+newSid, sidTag(newSid), clickHandlerID(t, app, newSid), "tab-b")
	if rr.Code != http.StatusOK {
		t.Fatalf("an event echoing the session tag must dispatch, got %d: %s", rr.Code, rr.Body.String())
	}
	// A tag of ANOTHER session does not.
	rr = postEventTab(app, "sky_sid="+newSid, sidTag(newLiveSessionID()), clickHandlerID(t, app, newSid), "tab-b")
	if rr.Code == http.StatusOK {
		t.Fatal("an event echoing another session's tag dispatched")
	}
}

// The acting tab's own event POST, still carrying the old cookie, gets the
// new cookie on the response and dispatches.
func TestSessionRotation_ActingTabEventGetsNewCookie(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	hid := clickHandlerID(t, app, oldSid)
	sess := mustGet(t, app, oldSid)
	bindFromTab(app, sess, "user-1", "tab-a")
	newSid := sess.currentSID()

	rr := postEventTab(app, oldCookie, oldSid, hid, "tab-a")
	if rr.Code != http.StatusOK {
		t.Fatalf("the acting tab's event must dispatch, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := cookieFromResponse(rr, "sky_sid"); got != newSid {
		t.Fatalf("the acting tab must get the new cookie, got %q want %q", got, newSid)
	}
	if got := rr.Header().Get("X-Sky-Sid"); got != sidTag(newSid) {
		t.Fatalf("X-Sky-Sid = %q, want the tag %q", got, sidTag(newSid))
	}
}

// The SSE path: the acting tab's open connection gets a `rotate` frame with a
// one-time ticket; /_sky/rotate exchanges (old cookie, tab, ticket) for the
// new cookie. Every other connection of the session is closed, and neither a
// wrong ticket nor a wrong tab gets the cookie.
func TestSessionRotation_SSETicketRedeemAndOtherConnsClosed(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	actID, actCh, _ := sess.registerSSEConn("tab-a")
	otherID, _, _ := sess.registerSSEConn("attacker-tab")

	bindFromTab(app, sess, "user-1", "tab-a")
	newSid := sess.currentSID()

	var ticket string
	select {
	case fr := <-actCh:
		if fr.event != "rotate" {
			t.Fatalf("acting tab's first frame = %q, want rotate", fr.event)
		}
		var p struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(fr.data), &p); err != nil || p.Ticket == "" {
			t.Fatalf("rotate frame data %q: %v", fr.data, err)
		}
		if strings.Contains(fr.data, newSid) {
			t.Fatalf("the rotate frame must carry a ticket, never the sid itself")
		}
		ticket = p.Ticket
	case <-time.After(time.Second):
		t.Fatal("acting tab received no rotate frame")
	}

	select {
	case <-sess.sseConnKicked(otherID):
	default:
		t.Fatal("FIXATION: a non-acting SSE connection stayed attached to the signed-in session")
	}
	select {
	case <-sess.sseConnKicked(actID):
		t.Fatal("the acting tab's SSE connection must stay attached")
	default:
	}

	if rr := postRotate(app, oldCookie, "tab-a", "wrong-ticket"); cookieFromResponse(rr, "sky_sid") == newSid || rr.Code == http.StatusOK {
		t.Fatalf("a wrong ticket must not redeem: %d", rr.Code)
	}
	if rr := postRotate(app, oldCookie, "attacker-tab", ticket); cookieFromResponse(rr, "sky_sid") == newSid || rr.Code == http.StatusOK {
		t.Fatalf("FIXATION: a different tab redeemed the ticket: %d", rr.Code)
	}
	rr := postRotate(app, oldCookie, "tab-a", ticket)
	if rr.Code != http.StatusOK {
		t.Fatalf("redeem: status %d body %s", rr.Code, rr.Body.String())
	}
	if got := cookieFromResponse(rr, "sky_sid"); got != newSid {
		t.Fatalf("redeem must set the new cookie, got %q want %q", got, newSid)
	}
	var out struct {
		Sid string `json:"sid"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Sid != newSid {
		t.Fatalf("redeem body %q: want sid %q", rr.Body.String(), newSid)
	}

	// The re-keyed session keeps delivering to the acting tab's connection.
	sess.fanOutFrame(sseFrame{event: "patch", data: "after-rotate"}, "")
	select {
	case fr := <-actCh:
		if fr.data != "after-rotate" {
			t.Fatalf("unexpected frame %+v", fr)
		}
	case <-time.After(time.Second):
		t.Fatal("the acting tab's SSE connection stopped receiving frames after rotation")
	}
}

// ── durable snapshot moves / is deleted ─────────────────────────────

func TestSessionRotation_DurableSnapshotMovesToNewSid(t *testing.T) {
	app := newRotationTestApp(t)
	f, d := newFakeDurable()
	app.durable = d
	oldSid, _ := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	d.persist(oldSid, "seed")
	waitFor(t, "initial snapshot under the old sid", func() bool { return f.has(oldSid) })

	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	newSid := sess.currentSID()

	if !f.has(newSid) {
		t.Fatalf("the snapshot was not written under the new sid %q", newSid)
	}
	if f.has(oldSid) {
		t.Fatalf("the snapshot under the old sid %q survived rotation", oldSid)
	}
	// A persist for the old sid that was queued before rotation must not
	// bring the snapshot back.
	d.persist(oldSid, "late-write")
	time.Sleep(50 * time.Millisecond)
	if f.has(oldSid) {
		t.Fatalf("a late persist recreated the snapshot under the retired sid %q", oldSid)
	}
}

// B: a revoked session's sid restores nothing.
func TestRevocationEviction_DeletesDurableSnapshot(t *testing.T) {
	app := newRotationTestApp(t)
	f, d := newFakeDurable()
	app.durable = d
	path := filepath.Join(t.TempDir(), "rev.db")
	db := openFileAuthDb(t, path)
	setRevocationGate(db, 0)

	sid0, _ := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, sid0)
	app.bindSessionUserTo(sess, "42", time.Now().Unix()-10)
	sid := sess.currentSID()
	d.persist(sid, "signed-in-model")
	waitFor(t, "snapshot under the bound sid", func() bool { return f.has(sid) })

	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "42")), "revokeUser")
	sess.mu.Lock()
	blocked := app.accessGateBlocks(sess)
	sess.mu.Unlock()
	if !blocked {
		t.Fatal("the gate did not block a revoked session")
	}
	waitFor(t, "eviction to delete the durable snapshot", func() bool { return !f.has(sid) })

	// The old cookie's next GET must boot from init, not the signed-in model.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "sky_sid="+sid)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if strings.Contains(rr.Body.String(), "signed-in-model") {
		t.Fatalf("REVOCATION BYPASS: the revoked sid restored the signed-in model")
	}
}

// The binding survives a memory-store restart through the gate's Db, so a
// durable restore of a bound session is re-bound (and gated), not unbound.
func TestRevocation_DurableRestoreIsRebound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rebind.db")
	db := openFileAuthDb(t, path)
	setRevocationGate(db, 0)
	t.Cleanup(func() { ResetRevocationGate() })

	f, d := newFakeDurable()
	app1 := newBindingTestApp("sky_sid")
	app1.durable = d
	sid0, _ := mintSession(t, app1, "sky_sid")
	sess := mustGet(t, app1, sid0)
	app1.bindSessionUserTo(sess, "77", time.Now().Unix()-10)
	sid := sess.currentSID()
	d.persist(sid, "signed-in-model")
	waitFor(t, "snapshot", func() bool { return f.has(sid) })

	// "Restart": a fresh app + fresh memory store, same durable table + Db.
	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "77")), "revokeUser")
	app2 := newBindingTestApp("sky_sid")
	app2.durable = d
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "sky_sid="+sid)
	rr := httptest.NewRecorder()
	app2.handleInitial(rr, req)
	if strings.Contains(rr.Body.String(), "signed-in-model") {
		t.Fatalf("REVOCATION BYPASS: a restart restored a revoked user's model unbound")
	}
	if f.has(sid) {
		t.Fatalf("the revoked session's snapshot survived the restore-time eviction")
	}
}

func TestLiveEndSession_DeletesSnapshotAndRetiresSid(t *testing.T) {
	app := newRotationTestApp(t)
	f, d := newFakeDurable()
	app.durable = d
	sid, cookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, sid)
	sess.app.Store(app)
	d.persist(sid, "state")
	waitFor(t, "snapshot", func() bool { return f.has(sid) })

	var res any
	runWithLiveSession(sess, func() { res = Live_endSession(nil).(func() any)() })
	if isErrResult(res) {
		t.Fatalf("endSession: %v", res)
	}
	if f.has(sid) {
		t.Fatal("endSession left the durable snapshot")
	}
	if _, ok := app.store.Get(sid); ok {
		t.Fatal("endSession left the store entry")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", cookie)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if got := cookieFromResponse(rr, "sky_sid"); got == "" || got == sid {
		t.Fatalf("the ended sid must not be re-adopted, got %q", got)
	}
}

// ── hardening: format + cookie names ────────────────────────────────

func TestSessionID_MalformedPresentedSidIsReplaced(t *testing.T) {
	app := newRotationTestApp(t)
	for _, bad := range []string{"attacker-chosen", "ABCDEF0123456789ABCDEF0123456789", strings.Repeat("a", 31), strings.Repeat("a", 33), "../../etc"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "sky_sid", Value: bad})
		rr := httptest.NewRecorder()
		app.handleInitial(rr, req)
		got := cookieFromResponse(rr, "sky_sid")
		if got == bad || !validSessionID(got) {
			t.Fatalf("presented %q: got cookie %q, want a fresh 32-hex sid", bad, got)
		}
	}
}

func TestSessionCookie_HostPrefixOnlyWhenSecure(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()
	app := newRotationTestApp(t)

	plain := httptest.NewRecorder()
	app.handleInitial(plain, httptest.NewRequest(http.MethodGet, "/", nil))
	if cookieFromResponse(plain, "sky_sid") == "" || cookieFromResponse(plain, "__Host-sky_sid") != "" {
		t.Fatalf("plain HTTP must use sky_sid: %v", plain.Header().Values("Set-Cookie"))
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	secure := httptest.NewRecorder()
	app.handleInitial(secure, req)
	hostSid := cookieFromResponse(secure, "__Host-sky_sid")
	if hostSid == "" {
		t.Fatalf("HTTPS must use __Host-sky_sid: %v", secure.Header().Values("Set-Cookie"))
	}
	for _, c := range secure.Result().Cookies() {
		if c.Name == "__Host-sky_sid" && (!c.Secure || c.Path != "/" || c.Domain != "") {
			t.Fatalf("__Host- cookie must be Secure, Path=/, no Domain: %+v", c)
		}
	}

	// Both names are read: an existing sky_sid over HTTPS keeps its session
	// and is upgraded to the __Host- name (the legacy cookie is expired).
	legacySid := cookieFromResponse(plain, "sky_sid")
	up := httptest.NewRequest(http.MethodGet, "/", nil)
	up.Header.Set("X-Forwarded-Proto", "https")
	up.AddCookie(&http.Cookie{Name: "sky_sid", Value: legacySid})
	upRR := httptest.NewRecorder()
	app.handleInitial(upRR, up)
	if got := cookieFromResponse(upRR, "__Host-sky_sid"); got != legacySid {
		t.Fatalf("legacy sky_sid over HTTPS must keep its session under __Host-: got %q want %q", got, legacySid)
	}
	expired := false
	for _, c := range upRR.Result().Cookies() {
		if c.Name == "sky_sid" && c.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("the legacy sky_sid cookie must be expired once upgraded: %v", upRR.Header().Values("Set-Cookie"))
	}

	// The event channel reads the __Host- name too.
	hid := clickHandlerID(t, app, hostSid)
	rr := postEvent(app, "__Host-sky_sid="+hostSid, eventBody(hostSid, hid))
	if rr.Code != http.StatusOK {
		t.Fatalf("event with __Host-sky_sid: status %d %s", rr.Code, rr.Body.String())
	}
	// And the __Host- cookie wins over a planted legacy one.
	rr = postEvent(app, "sky_sid="+legacySid+"; __Host-sky_sid="+hostSid, eventBody(hostSid, hid))
	if rr.Code != http.StatusOK {
		t.Fatalf("__Host- cookie must win over a legacy cookie: status %d", rr.Code)
	}
}

func TestSessionCookie_SubAppNameFollowsTheSameRule(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()
	app := newRotationTestApp(t)
	app.cookieName = "sky_billing_sid"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if cookieFromResponse(rr, "__Host-sky_billing_sid") == "" {
		t.Fatalf("sub-app cookie over HTTPS must be __Host-sky_billing_sid: %v", rr.Header().Values("Set-Cookie"))
	}
}

// ── stable per-session key ──────────────────────────────────────────

func TestSessionKey_StableAcrossRotation(t *testing.T) {
	app := newRotationTestApp(t)
	var seedKey string
	app.init = func(req any) any {
		if s, ok := fieldOrNil(req, "sessionKey").(string); ok {
			seedKey = s
		}
		return SkyTuple2{V0: "seed", V1: cmdT{kind: "none"}}
	}
	sid, _ := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, sid)
	if seedKey == "" || !validSessionID(seedKey) {
		t.Fatalf("init's request must carry a 32-hex sessionKey, got %q", seedKey)
	}
	if seedKey == sid {
		t.Fatalf("the session key must not be the sid (the sid changes and leaks)")
	}
	app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
	var res any
	runWithLiveSession(sess, func() { res = Live_sessionKey(nil).(func() any)() })
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 || r.OkValue != seedKey {
		t.Fatalf("Live.sessionKey after rotation = %#v, want Ok %q", res, seedKey)
	}
}

// expireRotationGrace ends the grace window of oldSid's alias record, as if
// liveRotateGrace had passed.
func expireRotationGrace(app *liveApp, oldSid string) {
	a, ok := app.store.getAlias(oldSid)
	if !ok {
		return
	}
	a.GraceUntil = time.Now().Add(-time.Second).UnixNano()
	app.store.putAlias(oldSid, a)
}

// The alias record lives in the shared store, so a second replica on the
// same database refuses the old cookie too, and the retired row is gone from
// disk (the second replica cannot decode the signed-in session under it).
func TestSessionRotation_AliasSharedAcrossReplicas_SQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	storeA, err := newSQLiteStore(path, 30*time.Minute, 0)
	if err != nil {
		t.Fatalf("store A: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := newSQLiteStore(path, 30*time.Minute, 0)
	if err != nil {
		t.Fatalf("store B: %v", err)
	}
	t.Cleanup(func() { _ = storeB.Close() })

	appA := newRotationTestApp(t)
	appA.store = storeA
	appB := newBindingTestApp("sky_sid")
	appB.store = storeB

	oldSid, oldCookie := mintSession(t, appA, "sky_sid")
	hid := clickHandlerID(t, appA, oldSid)
	sess := mustGet(t, appA, oldSid)
	bindFromTab(appA, sess, "user-1", "tab-a")
	newSid := sess.currentSID()

	if _, ok := storeB.Get(oldSid); ok {
		t.Fatalf("replica B still reads the retired sid %q from the shared store", oldSid)
	}
	if _, ok := storeB.Get(newSid); !ok {
		t.Fatalf("replica B cannot read the rotated session %q", newSid)
	}
	rr := postEventTab(appB, oldCookie, oldSid, hid, "attacker-tab")
	if got := rr.Header().Get("X-Sky-Status"); got != "session-rotating" {
		t.Fatalf("replica B: old cookie got %q (code %d), want session-rotating", got, rr.Code)
	}
	expireRotationGrace(appA, oldSid)
	rr = postEventTab(appB, oldCookie, oldSid, hid, "attacker-tab")
	if got := rr.Header().Get("X-Sky-Status"); got != "session-lost" {
		t.Fatalf("replica B after grace: old cookie got %q (code %d), want session-lost", got, rr.Code)
	}
}

// An SSE connect with the old cookie: the rotating tab gets the new cookie
// on the stream's headers; any other tab gets a short "rotating" stream.
func TestSessionRotation_SSEConnectWithOldCookie(t *testing.T) {
	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	bindFromTab(app, sess, "user-1", "tab-a")
	newSid := sess.currentSID()

	other := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=attacker-tab&sl=1", nil)
	other.Header.Set("Cookie", oldCookie)
	orr := httptest.NewRecorder()
	app.handleSSE(orr, other)
	if !strings.Contains(orr.Body.String(), "event: rotating") {
		t.Fatalf("non-rotating tab: want a rotating event, got %q", orr.Body.String())
	}
	if strings.Contains(orr.Body.String(), newSid) || cookieFromResponse(orr, "sky_sid") == newSid {
		t.Fatalf("FIXATION: an SSE connect with the old cookie learned the new sid")
	}

	req := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=tab-a&sl=1", nil)
	req.Header.Set("Cookie", oldCookie)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSSE(rr, req)
	if got := cookieFromResponse(rr, "sky_sid"); got != newSid {
		t.Fatalf("rotating tab's SSE connect must set the new cookie, got %q want %q", got, newSid)
	}
	if !strings.Contains(rr.Body.String(), `"sid":"`+newSid+`"`) {
		t.Fatalf("hello must name the new sid: %q", rr.Body.String())
	}
}

// Rotation races every store write of the session (persistSession from
// dispatch paths, the SSE connect, a perform completion). None may land under
// the retired id: storeMu makes the id read and the write one step against
// the re-key. Run under -race in CI.
func TestSessionRotation_ConcurrentWritesNeverResurrectOldSid(t *testing.T) {
	for round := 0; round < 20; round++ {
		app := newRotationTestApp(t)
		oldSid, _ := mintSession(t, app, "sky_sid")
		sess := mustGet(t, app, oldSid)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						app.persistSession(sess)
						_ = sess.currentSID()
					}
				}
			}()
		}
		app.bindSessionUserTo(sess, "user-1", time.Now().Unix())
		time.Sleep(2 * time.Millisecond)
		close(stop)
		wg.Wait()
		if _, ok := app.store.Get(oldSid); ok {
			t.Fatalf("round %d: a concurrent write resurrected the retired sid %q", round, oldSid)
		}
		if _, ok := app.store.Get(sess.currentSID()); !ok {
			t.Fatalf("round %d: the rotated session is missing from the store", round)
		}
	}
}

// /_sky/rotate is CSRF-checked through the REAL listener chain
// (liveListenerHandlerFor → CSRFMiddleware), not only by calling handleRotate
// directly: a cross-site POST that carries the old session cookie, the acting
// tab and a valid ticket, but no CSRF token, must not get the new session
// cookie. The same POST with the page's token redeems it.
func TestSessionRotation_RotateIsCSRFCheckedThroughTheListener(t *testing.T) {
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	defer csrfEnabled.Store(prev)

	app := newRotationTestApp(t)
	oldSid, oldCookie := mintSession(t, app, "sky_sid")
	sess := mustGet(t, app, oldSid)
	_, actCh, _ := sess.registerSSEConn("tab-a")
	bindFromTab(app, sess, "user-1", "tab-a")
	newSid := sess.currentSID()
	var ticket string
	select {
	case fr := <-actCh:
		var p struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(fr.data), &p); err != nil || p.Ticket == "" {
			t.Fatalf("rotate frame data %q: %v", fr.data, err)
		}
		ticket = p.Ticket
	case <-time.After(time.Second):
		t.Fatal("acting tab received no rotate frame")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/rotate", app.handleRotate)
	h := liveListenerHandlerFor(app, mux, "127.0.0.1")
	post := func(extraCookie, csrfHeader string) *httptest.ResponseRecorder {
		body := `{"tab":"tab-a","ticket":"` + ticket + `"}`
		req := httptest.NewRequest(http.MethodPost, "/_sky/rotate", strings.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		cookie := oldCookie
		if extraCookie != "" {
			cookie += "; " + extraCookie
		}
		req.Header.Set("Cookie", cookie)
		if csrfHeader != "" {
			req.Header.Set(SkyCsrfHeaderName, csrfHeader)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	forged := post("", "")
	if forged.Code != http.StatusForbidden {
		t.Fatalf("a rotate POST with no CSRF token must be refused (403), got %d: %s", forged.Code, forged.Body.String())
	}
	if got := cookieFromResponse(forged, "sky_sid"); got == newSid {
		t.Fatal("FIXATION: a CSRF-less rotate POST was handed the new session cookie")
	}

	tok := "0123456789abcdef0123456789abcdef"
	ok := post(SkyCsrfCookieName+"="+tok, tok)
	if ok.Code != http.StatusOK {
		t.Fatalf("a rotate POST with the CSRF token must redeem, got %d: %s", ok.Code, ok.Body.String())
	}
	if got := cookieFromResponse(ok, "sky_sid"); got != newSid {
		t.Fatalf("the redeemed rotate must set the new cookie, got %q want %q", got, newSid)
	}
}

// E-13: X-Sky-Sid carries the session id only in header transport (where
// the token already travels in headers); cookie mode sends the one-way tag.
func TestTellSIDNamesTheIDOnlyInHeaderMode(t *testing.T) {
	sid := newLiveSessionID()
	rr := httptest.NewRecorder()
	(&liveApp{}).tellSID(rr, sid)
	if got := rr.Header().Get("X-Sky-Sid"); got == sid || got != sidTag(sid) || strings.Contains(got, sid) {
		t.Fatalf("cookie mode X-Sky-Sid = %q, want the tag", got)
	}
	rr = httptest.NewRecorder()
	(&liveApp{headerSessions: true}).tellSID(rr, sid)
	if got := rr.Header().Get("X-Sky-Sid"); got != sid {
		t.Fatalf("header mode X-Sky-Sid = %q, want the id", got)
	}
	if !claimsSID(sidTag(sid), sid) || !claimsSID(sid, sid) || claimsSID(sidTag(sid), newLiveSessionID()) {
		t.Fatal("claimsSID must accept the id and its tag, and nothing else")
	}
}
