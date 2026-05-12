package rt

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUnknownMsg_DirectSend_Returns400 — regression for the case
// where a wire __sky_send POSTs a Msg constructor name that's not in
// the global ADT registry AND not in the per-app cache. Previously
// the runtime built a SkyADT{Tag: -1, ...} and passed it to the
// user's update, which fell through every case branch and hit the
// codegen Unreachable, recovered as "[sky.live] dispatch panic
// recovered, dropping event". User-facing symptom: silent drop with
// a noisy panic stack in the server log. Fix: reject upfront with
// HTTP 400 + X-Sky-Live header so the client sees a real,
// recoverable error.
func TestUnknownMsg_DirectSend_Returns400(t *testing.T) {
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
	app.store.Set("sid-unknown", &liveSession{
		sseCh:     make(chan string, 4),
		cancelSub: make(chan struct{}),
		model:     "model",
		handlers:  map[string]any{},
	})

	body := strings.NewReader(`{"sessionId":"sid-unknown","msg":"DefinitelyNotARealMsg","args":[]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.handleEvent(rr, req)

	if rr.Code != 400 {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Sky-Live"); got != "1" {
		t.Errorf("expected X-Sky-Live: 1, got %q", got)
	}
	if !strings.Contains(rr.Body.String(), "DefinitelyNotARealMsg") {
		t.Errorf("expected body to mention the Msg name, got %q", rr.Body.String())
	}
}


// TestUnknownMsg_SkySentinel_Returns200 — Sky.Live's client posts
// `__skySessionPing` as a liveness probe. It's intentionally NOT a
// real Msg constructor; the client just cares about session
// existence (404 → reload, anything else → keep going). Our unknown-
// Msg defence must NOT log a panic / return 400 for these sentinels;
// they should silently no-op with 200.
func TestUnknownMsg_SkySentinel_Returns200(t *testing.T) {
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
	app.store.Set("sid-sentinel", &liveSession{
		sseCh:     make(chan string, 4),
		cancelSub: make(chan struct{}),
		model:     "model",
		handlers:  map[string]any{},
	})

	body := strings.NewReader(`{"sessionId":"sid-sentinel","msg":"__skySessionPing","args":[]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.handleEvent(rr, req)

	if rr.Code != 200 {
		t.Errorf("expected 200 for __sky sentinel, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Sky-Live"); got != "1" {
		t.Errorf("expected X-Sky-Live: 1, got %q", got)
	}
}
