//go:build linux || darwin

package rt

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// App.stop / Live.stop ends every session (releaseSessionsAndStore), and a
// session's end closes the child processes and watchers it started: an
// embedded app that is stopped leaves nothing running.
func TestAppStopKillsSessionChildrenAndWatchers(t *testing.T) {
	store := newMemoryStore(time.Hour)
	app := &liveApp{store: store}
	sess := &liveSession{done: make(chan struct{})}
	store.Set("sid-stop", sess)
	var pid, wid int
	runWithLiveSession(sess, func() {
		id := procOk(t, procTask(t, Subprocess_spawn(procCmd{program: "sleep", args: []string{"60"}}.record()))).(int)
		pid = handleOf(t, id).pid
		wid = procOk(t, procTask(t, Watch_watch([]any{watchDir(t)}, watchOpts(false, 20)))).(int)
	})
	app.releaseSessionsAndStore()
	if pidAlive(pid) {
		t.Fatalf("a child of a stopped app's session is still running (pid %d)", pid)
	}
	if _, e := lookupWatcher(wid); e == nil {
		t.Fatal("a watcher of a stopped app's session is still open")
	}
}

// A Sky.Live session delivers Process.events through update (the same
// dispatch + frame path WebSocket and Http.Stream subscriptions take), and
// when `subscriptions` stops asking for it the runner stops and releases
// its claim.
//
// The app's `subscriptions` keeps asking for the leaf, as a real app does:
// every delivery re-runs setupSubscriptions, and an app with NO
// subscriptions function drops the leaf inside its first delivery — on the
// runner's own goroutine, which cannot wait for itself — so the drop below
// would find no runner to wait on and read the claim while it is still
// being released (the flake this test used to have).
func TestLiveSourceSubDeliversAndTearsDown(t *testing.T) {
	id := spawnT(t, shCmd("echo live-one; sleep 30"))
	var seen []string
	ident := func(ev any) any { return ev }
	leaf := Subprocess_events(id, ident)
	app := &liveApp{
		update: func(msg, model any) any {
			if adt, ok := msg.(SkyADT); ok && adt.SkyName == "Output" {
				seen = append(seen, adt.Fields[1].(map[string]any)["data"].(string))
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:          func(model any) any { return velement("div", nil, []any{vtext("v")}) },
		subscriptions: func(model any) any { return leaf },
		msgTags:       map[string]int{},
	}
	sess := &liveSession{done: make(chan struct{}), cancelSub: make(chan struct{}),
		sseCh: make(chan sseFrame, 16), model: "m"}
	app.applySourceSubsDiff(sess, map[string]subT{leaf.sourceKey: leaf})
	deadline := time.Now().Add(5 * time.Second)
	for {
		sess.mu.Lock()
		got := strings.Join(seen, "")
		sess.mu.Unlock()
		if strings.Contains(got, "live-one") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("update never saw the output; saw %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	sess.activeSourceSubsMu.Lock()
	var runners []*sourceRunner
	for _, r := range sess.activeSourceSubs {
		runners = append(runners, r)
	}
	sess.activeSourceSubsMu.Unlock()
	if len(runners) != 1 {
		t.Fatalf("the requested subscription has %d runners before the drop, want 1", len(runners))
	}
	app.applySourceSubsDiff(sess, nil)
	for _, r := range runners {
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Fatal("a dropped Live source runner did not stop")
		}
	}
	h := handleOf(t, id)
	h.mu.Lock()
	active := h.subActive
	h.mu.Unlock()
	if active {
		t.Fatal("the claim outlived the dropped Live subscription")
	}
	sess.markDone()
}

// A Sky.Live update that drops its own Process.events subscription runs on
// the runner's goroutine, so the runner releases its claim only after that
// dispatch returns. A dispatch that asks for the same source again inside
// that window must hand the claim over, not refuse it as a second consumer:
// before the fix the re-request was logged "already has an events Sub" and
// ignored, and the session stopped receiving the process's output.
//
// testHookSourceBeforeRelease holds the dropped runner in the window (it
// stopped reading, it still holds the claim) so the interleaving is forced,
// not waited for.
func TestLiveSourceSubReRequestedWhileReleasingIsHandedOver(t *testing.T) {
	id := spawnT(t, shCmd("echo first; read x; echo second; sleep 30"))
	ident := func(ev any) any { return ev }
	leaf := Subprocess_events(id, ident)

	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	hook := func(key string) {
		if key != leaf.sourceKey {
			return
		}
		once.Do(func() {
			close(entered)
			<-gate
		})
	}
	testHookSourceBeforeRelease.Store(&hook)
	defer testHookSourceBeforeRelease.Store(nil)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	var seen []string
	app := &liveApp{
		update: func(msg, model any) any {
			adt, _ := msg.(SkyADT)
			switch adt.SkyName {
			case "Output":
				seen = append(seen, adt.Fields[1].(map[string]any)["data"].(string))
				// The first output turns the subscription off, from inside
				// update: the runner is dropped by its own delivery.
				return SkyTuple2{V0: "off", V1: cmdT{kind: "none"}}
			case "On":
				return SkyTuple2{V0: "on", V1: cmdT{kind: "none"}}
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any { return velement("div", nil, []any{vtext("v")}) },
		subscriptions: func(model any) any {
			if model == "on" {
				return leaf
			}
			return nil
		},
		msgTags: map[string]int{},
	}
	sess := &liveSession{done: make(chan struct{}), cancelSub: make(chan struct{}),
		sseCh: make(chan sseFrame, 64), model: "on"}
	defer sess.markDone()
	app.applySourceSubsDiff(sess, map[string]subT{leaf.sourceKey: leaf})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the self-dropped runner never reached its release")
	}
	// The dropped runner has stopped reading and still holds the claim.
	// The next dispatch asks for the source again.
	app.deliverSubMsg(sess, SkyADT{Tag: 0, SkyName: "On"})
	sess.activeSourceSubsMu.Lock()
	next := sess.activeSourceSubs[leaf.sourceKey]
	sess.activeSourceSubsMu.Unlock()
	if next == nil {
		t.Fatal("a source re-requested while its dropped runner was releasing was refused")
	}

	openGate()
	procOk(t, procTask(t, Subprocess_write(id, "go\n")))
	deadline := time.Now().Add(5 * time.Second)
	for {
		sess.mu.Lock()
		got := strings.Join(seen, "")
		sess.mu.Unlock()
		if strings.Contains(got, "second") {
			if strings.Count(got, "first") != 1 {
				t.Fatalf("the handed-over runner re-delivered output: %q", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the handed-over subscription never delivered; saw %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
