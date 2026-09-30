//go:build linux || darwin

package rt

import (
	"testing"
	"time"
)

// TestWatchHandle_AnotherSessionIsRefused is the A-1 regression for
// `Watcher`: a watcher is resolved by its Int alone, so another session (or a
// restored model after a restart) read another session's file-change paths.
// A watcher owned by one session is refused to another; ids are random.
func TestWatchHandle_AnotherSessionIsRefused(t *testing.T) {
	dir := watchDir(t)
	a := &liveSession{done: make(chan struct{})}
	b := &liveSession{done: make(chan struct{})}
	var id int
	runWithLiveSession(a, func() {
		id = procOk(t, procTask(t, Watch_watch([]any{dir}, watchOpts(false, 10)))).(int)
	})
	t.Cleanup(func() { procTask(t, Watch_close(id)) })
	if id < 1<<32 {
		t.Errorf("watcher id %d is not a random 62-bit id", id)
	}
	var next, closeRes SkyResult[any, any]
	var sub SkySub
	runWithLiveSession(b, func() {
		// Bounded: before the fix the call was served and waited for a change.
		got := make(chan SkyResult[any, any], 1)
		go func() {
			runWithLiveSession(b, func() { got <- procTask(t, Watch_next(id)) })
		}()
		select {
		case next = <-got:
		case <-time.After(3 * time.Second):
			t.Error("next from another session was served (it waited for a change)")
		}
		closeRes = procTask(t, Watch_close(id))
		sub = Watch_changes(id, func(v any) any { return v })
	})
	if k := procErrKind(next); k != "PermissionDenied" {
		t.Errorf("next from another session: %q, want PermissionDenied", k)
	}
	if k := procErrKind(closeRes); k != "PermissionDenied" {
		t.Errorf("close from another session: %q, want PermissionDenied", k)
	}
	if _, dead := sub.source.(deadSource); !dead {
		t.Error("changes from another session reached the watcher")
	}
	if _, e := lookupWatcher(id); e != nil {
		t.Fatal("the refused close closed the watcher")
	}
	for old := 1; old <= 16; old++ {
		if _, e := lookupWatcher(old); e == nil {
			t.Fatalf("stale watcher id %d resolved", old)
		}
	}
}
