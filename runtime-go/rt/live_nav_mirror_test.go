package rt

// Phase 1 mirror — navigation fan-out (v0.18). A session's tabs mirror
// ONE shared view, so handleInitial (sky-nav / popstate / a new tab
// landing on a URL) must fan the rendered page out to the session's OTHER
// tabs, excluding the requesting tab (X-Sky-Tab). Without this, tabs drift
// onto different pages than the shared Model and a later action's diff
// mis-targets the stale tab's DOM.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sidFromSetCookie(t *testing.T, setCookie string) string {
	t.Helper()
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "sky_sid=") {
			return strings.TrimPrefix(part, "sky_sid=")
		}
	}
	t.Fatalf("no sky_sid in Set-Cookie: %q", setCookie)
	return ""
}

func newMirrorTestApp() *liveApp {
	return &liveApp{
		init: func(req any) any {
			return SkyTuple2{V0: "model", V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("page")})
		},
		subscriptions: func(model any) any { return nil },
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
	}
}

func TestHandleInitial_MirrorsNavigationToOtherTabs(t *testing.T) {
	app := newMirrorTestApp()

	// First GET / creates the session.
	rr1 := httptest.NewRecorder()
	app.handleInitial(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first GET /: %d", rr1.Code)
	}
	cookie := rr1.Header().Get("Set-Cookie")
	sid := sidFromSetCookie(t, cookie)

	sess, ok := app.store.Get(sid)
	if !ok {
		t.Fatal("session not created")
	}
	// Two live tabs of the session.
	_, chA, _ := sess.registerSSEConn("tabA")
	_, chB, _ := sess.registerSSEConn("tabB")

	// Tab A navigates (sky-nav carries X-Sky-Tab so the originator is
	// excluded from its own mirror).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Sky-Nav", "1")
	req.Header.Set("X-Sky-Tab", "tabA")
	app.handleInitial(httptest.NewRecorder(), req)

	// Tab B mirrors the navigation via a full-body frame.
	select {
	case fr := <-chB:
		if fr.event != "patch" {
			t.Fatalf("tabB mirror frame event=%q, want \"patch\" (full-body)", fr.event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tabB did not receive the navigation mirror frame")
	}

	// Tab A (the navigator) is excluded — its own HTTP response already
	// carried the page; a mirror frame would double-apply.
	select {
	case fr := <-chA:
		t.Fatalf("tabA (originator) must be excluded from its own mirror, got %q", fr.data)
	default:
	}
}

func TestHandleInitial_LoneTab_NoMirror(t *testing.T) {
	app := newMirrorTestApp()
	rr1 := httptest.NewRecorder()
	app.handleInitial(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	sid := sidFromSetCookie(t, rr1.Header().Get("Set-Cookie"))
	sess, _ := app.store.Get(sid)

	// Only one tab connected — its own navigation must fan out to nobody
	// (no wasted full-body encode/broadcast for the common single-tab case).
	_, chA, _ := sess.registerSSEConn("tabA")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", rr1.Header().Get("Set-Cookie"))
	req.Header.Set("X-Sky-Nav", "1")
	req.Header.Set("X-Sky-Tab", "tabA")
	app.handleInitial(httptest.NewRecorder(), req)

	select {
	case fr := <-chA:
		t.Fatalf("lone tab must not receive a mirror of its own nav, got %q", fr.data)
	case <-time.After(300 * time.Millisecond):
	}
}
