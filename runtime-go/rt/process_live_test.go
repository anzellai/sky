//go:build linux || darwin

package rt

import (
	"strings"
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
func TestLiveSourceSubDeliversAndTearsDown(t *testing.T) {
	id := spawnT(t, shCmd("echo live-one; sleep 30"))
	var seen []string
	app := &liveApp{
		update: func(msg, model any) any {
			if adt, ok := msg.(SkyADT); ok && adt.SkyName == "Output" {
				seen = append(seen, adt.Fields[1].(map[string]any)["data"].(string))
			}
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view:    func(model any) any { return velement("div", nil, []any{vtext("v")}) },
		msgTags: map[string]int{},
	}
	sess := &liveSession{done: make(chan struct{}), cancelSub: make(chan struct{}),
		sseCh: make(chan sseFrame, 16), model: "m"}
	ident := func(ev any) any { return ev }
	leaf := Subprocess_events(id, ident)
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
