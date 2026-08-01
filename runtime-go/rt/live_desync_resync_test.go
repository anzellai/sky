package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A1 regression — a handler-not-found (the client's DOM references a handler ID
// the server's CURRENT render no longer has: a deploy changed the view, or an
// SSE drop left the DOM stale) must NOT strand the client with a bare 404 that
// only a manual page refresh recovers from.
//
// Pre-fix: `404 "handler not found"` with no X-Sky-Live / X-Sky-Status, which
// __skySend misclassified as a proxy wedge → retried the same dead handler ID →
// "disconnected" banner until the user manually refreshed (the confirmed prod
// bug: 102/103 event-404s were this).
//
// Post-fix: the server re-renders the CURRENT view and returns it with
// X-Sky-Status: desync, so the client refreshes its DOM + data-sky-hid and its
// next click matches — self-heals in one round-trip.
func TestHandlerNotFoundSoftResyncs(t *testing.T) {
	viewFn := func(model any) any {
		return velement("form", nil, []any{
			velement("button",
				[]any{eventPair{name: "click", msg: "ClickMsg"}},
				[]any{vtext("Go")}),
		})
	}
	app := &liveApp{
		update:  func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:    viewFn,
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	init := sky_call(viewFn, "seed").(VNode)
	assignSkyIDs(&init, "r")
	handlers := map[string]any{}
	_ = renderVNode(init, handlers)
	sess := &liveSession{
		model:     "seed",
		handlers:  handlers,
		prevTree:  &init,
		sseCh:     make(chan sseFrame, 1),
		cancelSub: make(chan struct{}),
	}
	app.store.Set("sid-1", sess)

	// A STALE handler id — as if the client's DOM was rendered by an older
	// view. The server's current handler map has no such id.
	reqBody := `{"sessionId":"sid-1","seq":1,"msg":"ClickMsg","args":[],"handlerId":"r_stale_9_button_0.click"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handler-miss should soft-resync (200), got %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Sky-Status"); got != "desync" {
		t.Fatalf("X-Sky-Status = %q, want \"desync\"", got)
	}
	if got := rr.Header().Get("X-Sky-Live"); got != "1" {
		t.Fatalf("X-Sky-Live = %q, want \"1\" (client must not treat it as a proxy wedge)", got)
	}
	body := rr.Body.String()
	if strings.Contains(body, "handler not found") {
		t.Fatalf("body should be the re-rendered view, not the error string: %s", body)
	}
	if !strings.Contains(body, "<button") {
		t.Fatalf("resync body should contain the re-rendered button: %s", body)
	}
}

// A1 — session-not-found (the session cookie is unknown) is a HARD-reload
// signal: X-Sky-Status: session-lost, so the client reloads deterministically
// instead of sniffing the response body string.
func TestSessionNotFoundIsSessionLost(t *testing.T) {
	app := &liveApp{
		update:  func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:    func(model any) any { return velement("div", nil, nil) },
		store:   newMemoryStore(30 * time.Minute),
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	reqBody := `{"sessionId":"nonexistent","seq":1,"msg":"X","args":[],"handlerId":"a.click"}`
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("session-not-found status = %d, want 404", rr.Code)
	}
	if got := rr.Header().Get("X-Sky-Status"); got != "session-lost" {
		t.Fatalf("X-Sky-Status = %q, want \"session-lost\"", got)
	}
}
