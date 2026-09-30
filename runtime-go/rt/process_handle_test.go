//go:build unix

package rt

import (
	"strings"
	"testing"
)

// procErrKind returns the ErrorKind name of a Task's Err, or "" for Ok.
func procErrKind(res SkyResult[any, any]) string {
	if res.Tag == 0 {
		return ""
	}
	return errKindOf(res.ErrValue)
}

// TestProcessHandle_AnotherSessionIsRefused is the A-1 regression. A
// `Process` is a plain Int in the model, and every kernel resolved it by the
// Int alone: session B could write to, read from, resize, signal or close a
// process session A owns (a restored model after a deploy did exactly that,
// with another visitor's terminal). Every kernel must refuse a caller from a
// different session, and still serve the owner and a sessionless caller.
func TestProcessHandle_AnotherSessionIsRefused(t *testing.T) {
	a := &liveSession{done: make(chan struct{})}
	b := &liveSession{done: make(chan struct{})}
	var id int
	runWithLiveSession(a, func() {
		c := procCmd{program: "cat", pty: true, cols: 80, rows: 24}
		id = procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	})
	t.Cleanup(func() { procTask(t, Subprocess_close(id)) })

	calls := map[string]func() any{
		"write":      func() any { return Subprocess_write(id, "x") },
		"readWithin": func() any { return Subprocess_readWithin(0, id, 0, 0) },
		"resize":     func() any { return Subprocess_resize(id, 90, 30) },
		"kill":       func() any { return Subprocess_kill(id, 0) },
		"pid":        func() any { return Subprocess_pid(id) },
		"closeStdin": func() any { return Subprocess_closeStdin(id) },
		"screen":     func() any { return Subprocess_screen(id, "v", 0, true, 0) },
		"close":      func() any { return Subprocess_close(id) },
	}
	for name, call := range calls {
		var res SkyResult[any, any]
		runWithLiveSession(b, func() { res = procTask(t, call()) })
		if k := procErrKind(res); k != "PermissionDenied" {
			t.Errorf("%s from another session: got %q, want PermissionDenied", name, k)
		} else if !strings.Contains(errorMessageOf(res.ErrValue), "docs/migration/v0.27.md#handles-belong-to-their-session") {
			t.Errorf("%s: the refusal does not name the migration note", name)
		}
	}
	if _, e := lookupProc(id); e != nil {
		t.Fatal("a refused close from another session closed the process")
	}
	// events from another session delivers nothing.
	var sub SkySub
	runWithLiveSession(b, func() { sub = Subprocess_events(id, func(ev any) any { return ev }) })
	if _, dead := sub.source.(deadSource); !dead {
		t.Error("events from another session reached the process")
	}
	// The owner and a sessionless caller are served.
	var own SkyResult[any, any]
	runWithLiveSession(a, func() { own = procTask(t, Subprocess_pid(id)) })
	if own.Tag != 0 {
		t.Errorf("the owner was refused: %s", errorMessageOf(own.ErrValue))
	}
	if r := procTask(t, Subprocess_pid(id)); r.Tag != 0 {
		t.Errorf("a sessionless caller was refused: %s", errorMessageOf(r.ErrValue))
	}
	// No sessionless fallback: a session cannot reach a process made outside
	// any session (a stale id in a restored model must not reach a
	// background task's process).
	shared := spawnT(t, procCmd{program: "sleep", args: []string{"30"}})
	var fromSession SkyResult[any, any]
	runWithLiveSession(b, func() { fromSession = procTask(t, Subprocess_pid(shared)) })
	if k := procErrKind(fromSession); k != "PermissionDenied" {
		t.Errorf("a session reached a sessionless process: %q", k)
	}
}

// TestProcessHandle_IdsAreUnguessable: ids are random 62-bit values, so a
// small id stored by an earlier boot (the old counter started at 1) or taken
// from another replica names nothing, and gives an Err that says so.
func TestProcessHandle_IdsAreUnguessable(t *testing.T) {
	var ids []int
	for i := 0; i < 3; i++ {
		ids = append(ids, spawnT(t, procCmd{program: "sleep", args: []string{"30"}}))
	}
	for i, id := range ids {
		if id < 1<<32 {
			t.Errorf("id %d = %d: not a random 62-bit id", i, id)
		}
		if i > 0 && (id == ids[i-1]+1 || id == ids[i-1]-1) {
			t.Errorf("ids %d and %d are sequential", ids[i-1], id)
		}
	}
	for old := 1; old <= 64; old++ {
		res := procTask(t, Subprocess_pid(old))
		if res.Tag == 0 {
			t.Fatalf("stale id %d resolved to a live process", old)
		}
		if !strings.Contains(errorMessageOf(res.ErrValue), "docs/migration/v0.27.md#handle-ids-are-random") {
			t.Fatalf("stale id %d: the Err does not name the migration note", old)
		}
		if !strings.Contains(errorMessageOf(res.ErrValue), "not live in this server") {
			t.Fatalf("stale id %d: unclear Err %q", old, errorMessageOf(res.ErrValue))
		}
	}
}

// TestProcessHandle_OwnershipSurvivesRotation: the owner is the session
// object, not its id string, so a sign-in rotation (which re-keys the same
// *liveSession) keeps the process usable by its session.
func TestProcessHandle_OwnershipSurvivesRotation(t *testing.T) {
	a := &liveSession{done: make(chan struct{})}
	a.setSID("before-sign-in")
	var id int
	runWithLiveSession(a, func() {
		id = procOk(t, procTask(t, Subprocess_spawn(procCmd{program: "sleep", args: []string{"30"}}.record()))).(int)
	})
	t.Cleanup(func() { procTask(t, Subprocess_close(id)) })
	a.setSID("after-sign-in")
	var res SkyResult[any, any]
	runWithLiveSession(a, func() { res = procTask(t, Subprocess_pid(id)) })
	if res.Tag != 0 {
		t.Fatalf("the rotated session lost its process: %s", errorMessageOf(res.ErrValue))
	}
}
