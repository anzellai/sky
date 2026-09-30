//go:build unix

package rt

import (
	"testing"
	"time"
)

func procRegistered(id int) bool {
	_, ok := procRegistry.Load(int64(id))
	return ok
}

func waitUnregistered(t *testing.T, id int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !procRegistered(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still registered %v after it exited", id, within)
}

// TestProcess_UnownedExitedProcessIsReleased is the D-3 regression. A
// process no Sky.Live session owns (an HTTP handler, a Task program, a
// durable worker) stayed registered with its two rings until someone called
// Process.close; `wait` and reading to the end did not release it. An HTTP
// handler running `spawn |> andThen wait` per request grew the registry by
// one handle and up to 2 MiB of ring per request, for the life of the
// program.
func TestProcess_UnownedExitedProcessIsReleased(t *testing.T) {
	defer procUnownedExitGrace.Store(procUnownedExitGrace.Load())
	procUnownedExitGrace.Store(int64(300 * time.Millisecond))

	// A consumer that read everything and took the exit status: released
	// at once.
	id := procOk(t, procTask(t, Subprocess_spawn(shCmd("echo out; echo err >&2").record()))).(int)
	if got := readAll(t, id, procStreamStdout); got != "out\n" {
		t.Fatalf("stdout %q", got)
	}
	readAll(t, id, procStreamStderr)
	if s := exitStatus(t, id); s != "ExitCode 0" {
		t.Fatalf("exit %q", s)
	}
	waitUnregistered(t, id, 250*time.Millisecond)

	// Reading the output and THEN waiting still works: the process stays
	// until the exit status is taken.
	id2 := procOk(t, procTask(t, Subprocess_spawn(shCmd("echo x").record()))).(int)
	readAll(t, id2, procStreamStdout)
	readAll(t, id2, procStreamStderr)
	if s := exitStatus(t, id2); s != "ExitCode 0" {
		t.Fatalf("wait after the reads: %q", s)
	}

	// spawn |> andThen wait, never read: released after the grace time.
	before := len(procLiveHandles())
	var ids []int
	for i := 0; i < 20; i++ {
		id := procOk(t, procTask(t, Subprocess_spawn(shCmd("true").record()))).(int)
		exitStatus(t, id)
		ids = append(ids, id)
	}
	for _, id := range ids {
		waitUnregistered(t, id, 5*time.Second)
	}
	if n := len(procLiveHandles()); n > before {
		t.Fatalf("%d handles before, %d after 20 spawn+wait", before, n)
	}
}

// TestProcess_OwnedExitedProcessStaysUntilTheSessionEnds: a session-owned
// process keeps its handle after it exits (a terminal repaints its last
// screen from it), and is released with the session.
func TestProcess_OwnedExitedProcessStaysUntilTheSessionEnds(t *testing.T) {
	defer procUnownedExitGrace.Store(procUnownedExitGrace.Load())
	procUnownedExitGrace.Store(int64(100 * time.Millisecond))
	sess := &liveSession{done: make(chan struct{})}
	var id int
	runWithLiveSession(sess, func() {
		id = procOk(t, procTask(t, Subprocess_spawn(shCmd("true").record()))).(int)
		exitStatus(t, id)
	})
	time.Sleep(400 * time.Millisecond)
	if !procRegistered(id) {
		t.Fatal("a session-owned process was released before its session ended")
	}
	sess.markDone()
	if procRegistered(id) {
		t.Fatal("a session-owned process outlived its session")
	}
}
