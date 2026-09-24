//go:build !js

package rt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression gates for the Sky.Live event / session lifecycle fixes that
// have a Go-observable half. The browser half (client applier, binding,
// debounce, IME, gap buffering) is gated by scripts/live-client-e2e.sh.
//
// Every test here drives the public request path (handleInitial,
// handleEvent, handleSSE) or the production dispatch loops, so each one
// failed against the runtime before the fix it names.

// ── helpers ─────────────────────────────────────────────────────

var viewVarRe = regexp.MustCompile(`var __skyView = "([^"]*)";`)

// listApp is a Sky.Live app whose model is a list of item names and whose
// view renders one delete button per item, in order. update("Delete:x")
// removes x. It is the shape of the L2 data-loss report.
func listApp(store SessionStore) *liveApp {
	view := func(model any) any {
		items := model.([]string)
		kids := []any{}
		for _, it := range items {
			kids = append(kids, velement("button",
				[]any{eventPair{name: "click", msg: "Delete:" + it}},
				[]any{vtext("x " + it)}))
		}
		return velement("div", nil, kids)
	}
	return &liveApp{
		init: func(req any) any {
			return SkyTuple2{V0: []string{"a", "b", "c"}, V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			name := strings.TrimPrefix(msg.(string), "Delete:")
			out := []string{}
			for _, it := range model.([]string) {
				if it != name {
					out = append(out, it)
				}
			}
			return SkyTuple2{V0: out, V1: cmdT{kind: "none"}}
		},
		view:    view,
		store:   store,
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
}

// getPage runs handleInitial and returns the body, the session cookie
// and the view id the page embeds for its client ("" if none).
func getPage(t *testing.T, app *liveApp, path, cookie string) (string, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if cookie == "" {
		for _, c := range rr.Result().Cookies() {
			if c.Name == "sky_sid" {
				cookie = "sky_sid=" + c.Value
			}
		}
	}
	view := ""
	if m := viewVarRe.FindStringSubmatch(rr.Body.String()); m != nil {
		view = m[1]
	}
	return rr.Body.String(), cookie, view
}

func postLiveEvent(t *testing.T, app *liveApp, cookie string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	sid := strings.TrimPrefix(cookie, "sky_sid=")
	payload["sessionId"] = sid
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	return rr
}

func sessionModel(t *testing.T, app *liveApp, cookie string) any {
	t.Helper()
	sess, ok := app.store.Get(strings.TrimPrefix(cookie, "sky_sid="))
	if !ok {
		t.Fatalf("session missing")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.model
}

// ── L2: a click resolves against the render the user clicked on ─────

// TestEvent_ResolvesAgainstTheRenderTheUserClicked: the page shows
// [a, b, c]. The user taps "x a" and, before the reply arrives, "x b".
// Both clicks were made on the SAME render. The second click's handler
// id (position 1) must resolve to "Delete:b" — not to whatever sits at
// position 1 after the first delete ("Delete:c"), which deleted the
// wrong row.
func TestEvent_ResolvesAgainstTheRenderTheUserClicked(t *testing.T) {
	app := listApp(newMemoryStore(30 * time.Minute))
	_, cookie, view := getPage(t, app, "/", "")

	pos0 := "r.0#button.click"
	pos1 := "r.1#button.click"
	if rr := postLiveEvent(t, app, cookie, map[string]any{"seq": 1, "msg": "_", "args": []any{}, "handlerId": pos0, "view": view}); rr.Code != 200 {
		t.Fatalf("first click: %d %s", rr.Code, rr.Body.String())
	}
	rr := postLiveEvent(t, app, cookie, map[string]any{"seq": 2, "msg": "_", "args": []any{}, "handlerId": pos1, "view": view})
	if rr.Code != 200 {
		t.Fatalf("second click: %d %s", rr.Code, rr.Body.String())
	}
	got := sessionModel(t, app, cookie).([]string)
	if len(got) != 1 || got[0] != "c" {
		t.Fatalf("after tapping x on a then x on b (both on the first render) the list must be [c]; got %v "+
			"(the second click resolved against the NEWER render and deleted the wrong row)", got)
	}
}

// TestEvent_UnknownViewIsDesyncNotAnotherMsg: a click stamped with a
// render the session no longer holds must never dispatch a Msg.
func TestEvent_UnknownViewIsDesyncNotAnotherMsg(t *testing.T) {
	app := listApp(newMemoryStore(30 * time.Minute))
	_, cookie, view := getPage(t, app, "/", "")
	if view == "" {
		t.Fatalf("the page must embed its view id for the client (var __skyView)")
	}
	rr := postLiveEvent(t, app, cookie, map[string]any{"seq": 1, "msg": "_", "args": []any{}, "handlerId": "r.0#button.click", "view": "gone-render"})
	if got := rr.Header().Get("X-Sky-Status"); got != "desync" {
		t.Fatalf("X-Sky-Status = %q, want desync", got)
	}
	if got := sessionModel(t, app, cookie).([]string); len(got) != 3 {
		t.Fatalf("an expired view dispatched a Msg: model %v", got)
	}
}

// TestBeaconBatch_ResolvesAgainstEntryView: the unload beacon replays
// debounced entries later; each carries the view it was captured on.
func TestBeaconBatch_ResolvesAgainstEntryView(t *testing.T) {
	app := listApp(newMemoryStore(30 * time.Minute))
	_, cookie, view := getPage(t, app, "/", "")
	postLiveEvent(t, app, cookie, map[string]any{"seq": 1, "msg": "_", "args": []any{}, "handlerId": "r.0#button.click", "view": view})
	rr := postLiveEvent(t, app, cookie, map[string]any{"batch": []any{
		map[string]any{"seq": 2, "msg": "_", "args": []any{}, "handlerId": "r.1#button.click", "view": view},
	}})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("beacon: %d", rr.Code)
	}
	got := sessionModel(t, app, cookie).([]string)
	if len(got) != 1 || got[0] != "c" {
		t.Fatalf("beacon entry resolved against the wrong render: %v", got)
	}
}

// ── F1: one handler-id attribute per element ────────────────────────

// TestRender_OneHandlerIDAttributePerElement: an element with two
// events used to carry `data-sky-hid` twice; the parser keeps the first
// and every event dispatched the first handler. Both handler ids must be
// registered, and the element must carry the attribute once.
func TestRender_OneHandlerIDAttributePerElement(t *testing.T) {
	vn := velement("textarea", []any{
		eventPair{name: "input", msg: "Draft"},
		eventPair{name: "enter", msg: "Send"},
	}, nil)
	assignSkyIDs(&vn, "r")
	handlers := map[string]any{}
	html := renderVNode(vn, handlers)
	if n := strings.Count(html, "data-sky-hid="); n != 1 {
		t.Fatalf("data-sky-hid appears %d times in %s; duplicate attributes make every event dispatch the first handler", n, html)
	}
	if handlers["r.enter"] != "Send" || handlers["r.input"] != "Draft" {
		t.Fatalf("both handler ids must be registered: %v", handlers)
	}
}

// ── F7: an element gaining an unnamed handler binds it ──────────────

func TestDiff_GainingUnnamedHandlerIsNotARemoval(t *testing.T) {
	closure := func(s any) any { return s }
	old := velement("input", nil, nil)
	nw := velement("input", []any{eventPair{name: "input", msg: closure}}, nil)
	assignSkyIDs(&old, "r")
	assignSkyIDs(&nw, "r")
	patches := diffTrees(&old, &nw, nil)
	for _, p := range patches {
		if v, ok := p.Attrs["sky-input"]; ok {
			if v == "" {
				t.Fatalf("gaining an unnamed handler emitted sky-input=\"\", which the client applies as REMOVE: %+v", p)
			}
			return
		}
	}
	t.Fatalf("no sky-input attribute patch for a gained handler: %+v", patches)
}

// ── Elm rule: the DOM is written only when the rendered value changes ──
//
// docs/skylive/input-authority-protocol.md, "The DOM is written only when the
// render changes". A real app bound an input to one model field and wrote
// its onInput to another; in Elm and on v0.25.16 the field kept what the user
// typed, because the render did not change. A "user-event reconcile" that
// wrote the unchanged model value back into the field erased every keystroke.

func elmRuleApp(update func(msg, model any) any) *liveApp {
	return &liveApp{
		init:   func(req any) any { return SkyTuple2{V0: "abcde", V1: cmdT{kind: "none"}} },
		update: update,
		view: func(model any) any {
			return velement("div", nil, []any{
				velement("input", []any{
					attrPair{"value", model.(string)},
					eventPair{name: "input", msg: func(s any) any { return s }},
				}, nil),
			})
		},
		store: newMemoryStore(30 * time.Minute), locker: newSessionLocker(), msgTags: map[string]int{},
	}
}

func postTypedValue(t *testing.T, app *liveApp, typed string) []Patch {
	t.Helper()
	_, cookie, view := getPage(t, app, "/", "")
	rr := postLiveEvent(t, app, cookie, map[string]any{
		"seq": 1, "msg": "_", "args": []any{typed}, "handlerId": "r.0#input.input", "view": view,
		"inputState": map[string]any{"r.0#input": map[string]any{"value": typed, "seq": 1}},
	})
	var resp struct {
		Patches []Patch `json:"patches"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("reply is not JSON: %v %s", err, rr.Body.String())
	}
	return resp.Patches
}

func TestEvent_RejectedEditLeavesTheDOMAsTyped(t *testing.T) {
	app := elmRuleApp(func(msg, model any) any {
		// The app ignores the edit: the model, and so the render, stay "abcde".
		return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
	})
	for _, p := range postTypedValue(t, app, "abcdefg") {
		if _, ok := p.Attrs["value"]; ok && p.ID == "r.0#input" {
			t.Fatalf("the render did not change but the reply writes value=%q over what the user typed "+
				"(Elm semantics: the DOM is written only when the rendered value changes): %+v", p.Attrs["value"], p)
		}
	}
}

func TestEvent_ChangedModelValueIsApplied(t *testing.T) {
	app := elmRuleApp(func(msg, model any) any {
		// The app normalises the edit to a value the render has not shown yet.
		return SkyTuple2{V0: strings.ToUpper(msg.(string)), V1: cmdT{kind: "none"}}
	})
	for _, p := range postTypedValue(t, app, "abcdef") {
		if p.ID == "r.0#input" && p.Attrs["value"] == "ABCDEF" {
			return
		}
	}
	t.Fatalf("the rendered value changed to ABCDEF but the reply carries no value patch")
}

// ── F4: select keeps its value when its options re-render ───────────

func TestEvent_SelectKeepsValueAcrossOptionsRerender(t *testing.T) {
	store := newMemoryStore(30 * time.Minute)
	app := &liveApp{
		init: func(req any) any { return SkyTuple2{V0: 1, V1: cmdT{kind: "none"}} },
		update: func(msg, model any) any {
			return SkyTuple2{V0: model.(int) + 1, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			n := model.(int)
			opts := []any{}
			for i := 0; i <= n+1; i++ {
				v := string(rune('a' + i))
				opts = append(opts, velement("option", []any{attrPair{"value", v}}, []any{vtext(v)}))
			}
			return velement("div", nil, []any{
				velement("select", []any{attrPair{"value", "b"}}, opts),
				velement("button", []any{eventPair{name: "click", msg: "More"}}, []any{vtext("more")}),
			})
		},
		store: store, locker: newSessionLocker(), msgTags: map[string]int{},
	}
	_, cookie, view := getPage(t, app, "/", "")
	rr := postLiveEvent(t, app, cookie, map[string]any{"seq": 1, "msg": "_", "args": []any{}, "handlerId": "r.1#button.click", "view": view})
	body := rr.Body.String()
	var resp struct {
		Patches []Patch `json:"patches"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("reply: %v %s", err, body)
	}
	for _, p := range resp.Patches {
		if p.ID == "r.0#select" && p.HTML != nil {
			if !strings.Contains(*p.HTML, `selected="selected"`) || !strings.Contains(*p.HTML, `value="b"`) {
				t.Fatalf("options re-render lost the select's value \"b\": %s", *p.HTML)
			}
			return
		}
		// A children reconcile keeps the existing option nodes, so the
		// selected option "b" must be kept (not rebuilt) and no new option
		// may claim the selection.
		if p.ID == "r.0#select" && p.Kids != nil {
			keptB := false
			for _, k := range p.Kids {
				if k.Keep == "r.0#select.1#option" {
					keptB = true
				}
				if k.HTML != nil && strings.Contains(*k.HTML, "selected") {
					t.Fatalf("a new option claimed the selection: %s", body)
				}
			}
			if !keptB {
				t.Fatalf("options reconcile dropped the selected option \"b\": %s", body)
			}
			return
		}
	}
	t.Fatalf("expected a children patch on the select: %s", body)
}

// ── L10: withNotFound on every request ──────────────────────────────

func TestInitial_NotFoundPageOnEveryRequest(t *testing.T) {
	app := listApp(newMemoryStore(30 * time.Minute))
	app.routes = []liveRoute{{path: "/", page: "Home"}}
	app.notFound = "Missing"
	app.view = func(model any) any { return velement("p", nil, []any{vtext("page")}) }
	app.init = func(req any) any {
		return SkyTuple2{V0: map[string]any{"Page": "Home"}, V1: cmdT{kind: "none"}}
	}
	_, cookie, _ := getPage(t, app, "/", "")
	req := httptest.NewRequest(http.MethodGet, "/no/such/page", nil)
	req.Header.Set("Cookie", cookie)
	rr := httptest.NewRecorder()
	app.handleInitial(rr, req)
	if rr.Code == http.StatusNotFound || !strings.Contains(rr.Body.String(), "sky-root") {
		t.Fatalf("an app with withNotFound must render its not-found page on a later request too; got %d %q",
			rr.Code, rr.Body.String())
	}
}

// ── L12: a classified update panic reaches the user ─────────────────

func TestEvent_UpdatePanicSurfacesToTheUser(t *testing.T) {
	app := listApp(newMemoryStore(30 * time.Minute))
	app.update = func(msg, model any) any { panic("boom in update") }
	_, cookie, view := getPage(t, app, "/", "")
	sess, _ := app.store.Get(strings.TrimPrefix(cookie, "sky_sid="))
	rr := postLiveEvent(t, app, cookie, map[string]any{"seq": 1, "msg": "_", "args": []any{}, "handlerId": "r.0#button.click", "view": view})
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if ref, _ := resp["error"].(string); ref == "" {
		t.Fatalf("a panicking update replied with no error for the client to show: %s", rr.Body.String())
	}
	select {
	case fr := <-sess.sseCh:
		if fr.event != "skyerror" {
			t.Fatalf("expected a skyerror frame for the other tabs, got %q", fr.event)
		}
	default:
		t.Fatalf("no skyerror frame queued for the session's tabs")
	}
}

// ── L7: every dispatch path persists ────────────────────────────────

type countingStore struct {
	SessionStore
	sets atomic.Int64
}

func (c *countingStore) Set(sid string, s *liveSession) {
	c.sets.Add(1)
	c.SessionStore.Set(sid, s)
}

func TestPerformCompletion_PersistsTheSession(t *testing.T) {
	cs := &countingStore{SessionStore: newMemoryStore(30 * time.Minute)}
	app := listApp(cs)
	_, cookie, _ := getPage(t, app, "/", "")
	sess, _ := app.store.Get(strings.TrimPrefix(cookie, "sky_sid="))
	before := cs.sets.Load()
	task := func() any { return ResultOk("x") }
	toMsg := func(r any) any { return "Delete:a" }
	app.runPerformBody(sess, task, toMsg)
	if cs.sets.Load() == before {
		t.Fatalf("a Cmd.perform completion changed the model but was never written to the session store; " +
			"a restart loses it")
	}
}

func TestTimeEveryTick_PersistsTheSession(t *testing.T) {
	cs := &countingStore{SessionStore: newMemoryStore(30 * time.Minute)}
	app := listApp(cs)
	_, cookie, _ := getPage(t, app, "/", "")
	sess, _ := app.store.Get(strings.TrimPrefix(cookie, "sky_sid="))
	before := cs.sets.Load()
	app.timeEveryTick(sess, "Delete:b", time.Now())
	if cs.sets.Load() == before {
		t.Fatalf("a Sub.every tick changed the model but was never written to the session store")
	}
}

// ── SA-4 / K4: every Sub.every runs, and keeps running ──────────────

func TestSubEvery_AllTimersRunAcrossDispatches(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	app := &liveApp{
		update: func(msg, model any) any {
			mu.Lock()
			seen[msg.(string)]++
			mu.Unlock()
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any { return velement("div", nil, nil) },
		subscriptions: func(model any) any {
			return Sub_batch([]any{Sub_every(10, "Fast"), Sub_every(60, "Slow")})
		},
	}
	sess := &liveSession{
		sid: "s", model: 0, handlers: map[string]any{},
		sseCh: make(chan sseFrame, 64), cancelSub: make(chan struct{}), done: make(chan struct{}),
	}
	defer sess.markDone()
	sess.mu.Lock()
	app.setupSubscriptions(sess)
	sess.mu.Unlock()
	go func() {
		for range sess.sseCh {
		}
	}()
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	fast, slow := seen["Fast"], seen["Slow"]
	mu.Unlock()
	if fast == 0 {
		t.Fatalf("the first Sub.every never fired")
	}
	if slow == 0 {
		t.Fatalf("the second Sub.every never fired (fast=%d): every Sub.every leaf must run, and a "+
			"faster timer's dispatch must not restart it", fast)
	}
}

// ── L8: subscriptions come back on SSE connect, own echo delivered ───

func TestSSEConnect_ReestablishesSubscriptionsOfRestoredSession(t *testing.T) {
	var ticks atomic.Int64
	store := newMemoryStore(30 * time.Minute)
	app := &liveApp{
		update: func(msg, model any) any {
			ticks.Add(1)
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:          func(model any) any { return velement("div", nil, nil) },
		subscriptions: func(model any) any { return Sub_every(10, "Tick") },
		store:         store, locker: newSessionLocker(), msgTags: map[string]int{},
	}
	// A session as a persistent store hands it back after a restart: a
	// model, no running timers.
	sess := &liveSession{
		sid: "sid-r", model: 0, handlers: map[string]any{},
		sseCh: make(chan sseFrame, 64), cancelSub: make(chan struct{}), done: make(chan struct{}),
	}
	store.Set("sid-r", sess)
	defer sess.markDone()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/_sky/sse?tab=t1", nil).WithContext(ctx)
	req.Header.Set("Cookie", "sky_sid=sid-r")
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { app.handleSSE(rr, req); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if ticks.Load() == 0 {
		t.Fatalf("the restored session's Sub.every never ran after the SSE reconnect")
	}
}

func TestDispatch_PublishReachesSubscriptionOpenedByTheSameUpdate(t *testing.T) {
	var mu sync.Mutex
	var got []string
	app := &liveApp{
		update: func(msg, model any) any {
			mu.Lock()
			got = append(got, msg.(string))
			mu.Unlock()
			if msg.(string) == "Join" {
				return SkyTuple2{V0: "in", V1: cmdT{kind: "publish", topic: "room", payload: "hello"}}
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any { return velement("div", nil, nil) },
		subscriptions: func(model any) any {
			if model == "in" {
				return Sub_subscribeTopic("room", func(p any) any { return "Got:" + p.(string) })
			}
			return Sub_none()
		},
		topics: newTopicRegistry(16),
	}
	sess := &liveSession{
		sid: "s", model: "out", handlers: map[string]any{},
		sseCh: make(chan sseFrame, 64), cancelSub: make(chan struct{}), done: make(chan struct{}),
	}
	defer sess.markDone()
	go func() {
		for range sess.sseCh {
		}
	}()
	sess.mu.Lock()
	app.dispatch(sess, "Join")
	sess.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, m := range got {
		if m == "Got:hello" {
			return
		}
	}
	t.Fatalf("a Cmd.publish from the update that opened the subscription never reached it: %v", got)
}
