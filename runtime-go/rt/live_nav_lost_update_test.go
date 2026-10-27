package rt

// live_nav_lost_update_test.go — a page GET on an existing session must not
// lose an update that commits while it routes.
//
// Release run 40381185989 (nav-e2e, Sky.Live): `Save` returns
// `Cmd.batch [ Cmd.perform (sleep 10) Saved, Nav.pushUrl "/about" ]`. The
// client fetches /about while the perform finishes. handleInitial read
// sess.model WITHOUT sess.mu, ran applyRoute and onNavigate's update on that
// snapshot, then wrote it back under sess.mu. A `Saved` dispatched in
// between (it holds sess.mu) was overwritten: the server model, its tree and
// the page all ended with SAVED=0, and nothing ever sent the 7 again.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHandleInitial_DoesNotLoseAConcurrentUpdate(t *testing.T) {
	var (
		armed   bool
		entered = make(chan struct{}, 1)
		release = make(chan struct{})
		mu      sync.Mutex
	)
	copyModel := func(m any) map[string]any {
		out := map[string]any{}
		for k, v := range m.(map[string]any) {
			out[k] = v
		}
		return out
	}
	app := newMirrorTestApp()
	app.init = func(req any) any {
		return SkyTuple2{V0: map[string]any{"Page": "home", "Saved": 0, "Navs": 0}, V1: cmdT{kind: "none"}}
	}
	app.onNavigate = func(page any) any { return "Nav" }
	app.update = func(msg, model any) any {
		m := copyModel(model)
		switch msg {
		case "Nav":
			mu.Lock()
			block := armed
			mu.Unlock()
			if block {
				entered <- struct{}{}
				<-release
			}
			m["Navs"] = m["Navs"].(int) + 1
		case "Saved":
			m["Saved"] = 7
		}
		return SkyTuple2{V0: m, V1: cmdT{kind: "none"}}
	}

	rr1 := httptest.NewRecorder()
	app.handleInitial(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	cookie := rr1.Header().Get("Set-Cookie")
	sess, ok := app.store.Get(sidFromSetCookie(t, cookie))
	if !ok {
		t.Fatal("session not created")
	}

	// The navigation's GET: it blocks inside onNavigate's update.
	mu.Lock()
	armed = true
	mu.Unlock()
	navDone := make(chan struct{})
	go func() {
		defer close(navDone)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Cookie", cookie)
		req.Header.Set("X-Sky-Nav", "1")
		app.handleInitial(httptest.NewRecorder(), req)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the navigation never reached onNavigate")
	}
	mu.Lock()
	armed = false
	mu.Unlock()

	// The perform's result arrives meanwhile, dispatched the way
	// runPerformBody does it: under sess.mu.
	savedDone := make(chan struct{})
	go func() {
		defer close(savedDone)
		sess.mu.Lock()
		app.dispatch(sess, "Saved")
		sess.mu.Unlock()
	}()
	// Give the dispatch every chance to commit before the GET resumes.
	select {
	case <-savedDone:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	<-navDone
	<-savedDone

	sess.mu.Lock()
	m := sess.model.(map[string]any)
	sess.mu.Unlock()
	if m["Saved"] != 7 {
		t.Fatalf("the navigation overwrote a concurrent update: model=%v, want Saved=7", m)
	}
	if m["Navs"].(int) < 2 {
		t.Fatalf("onNavigate's update was lost: model=%v", m)
	}
}
