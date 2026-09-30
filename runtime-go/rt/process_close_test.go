//go:build unix

package rt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// requirePerl: the escape tests need a real double-forking, session-leaving
// daemon, which perl's POSIX module makes in one line on every CI runner.
func requirePerl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		if os.Getenv("SKY_LIVE_TESTS") == "skip" {
			t.Skip("perl not installed (SKY_LIVE_TESTS=skip)")
		}
		t.Fatal("this test needs perl on PATH (install perl, or set SKY_LIVE_TESTS=skip)")
	}
}

// daemonHoldingStdin is a shell line that starts a daemon which escapes every
// way the runtime can find a descendant: it double-forks (its parent exits,
// so it is re-parented away from the child's tree), calls setsid (a new
// session and group) and clears its environment. It inherits the child's
// stdin and holds it for `secs` seconds, and writes its pid to `mark`.
func daemonHoldingStdin(mark string, secs int) string {
	return fmt.Sprintf(`perl -MPOSIX -e 'exit if fork; POSIX::setsid(); exit if fork; open(F,">","%s"); print F $$; close F; %%ENV=(); exec "sleep", "%d"' <&0 &`, mark, secs)
}

func readPidMark(t *testing.T, mark string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(mark); err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the daemon did not start")
	return 0
}

// TestProcessClose_NotBlockedByABlockedWrite is the D-2 regression.
// `Subprocess_write` held stdinMu across the write, and `shutdown` took the
// same mutex BEFORE it closed any descriptor. A write to a child whose stdin
// nobody reads (here a daemon that escaped the group kill holds it) blocks
// once the pipe is full, so close waited on the write and the write waited
// on the daemon: close returned only when the daemon exited. Session
// teardown runs close inline, so one stuck write stopped every later session
// expiry in the app. Close must close the descriptor without the mutex,
// which unblocks the write.
func TestProcessClose_NotBlockedByABlockedWrite(t *testing.T) {
	requirePerl(t)
	mark := filepath.Join(t.TempDir(), "pid")
	id := spawnT(t, shCmd(daemonHoldingStdin(mark, 30)+" exec sleep 100"))
	daemon := readPidMark(t, mark)
	t.Cleanup(func() { _ = syscall.Kill(daemon, syscall.SIGKILL) })

	wrote := make(chan SkyResult[any, any], 1)
	go func() { wrote <- procTask(t, Subprocess_write(id, strings.Repeat("x", 1<<20))) }()
	time.Sleep(300 * time.Millisecond) // let the write fill the pipe and block

	closed := make(chan struct{})
	start := time.Now()
	go func() {
		procTask(t, Subprocess_close(id))
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("Process.close blocked %v behind a blocked Process.write", time.Since(start))
	}
	select {
	case r := <-wrote:
		if r.Tag == 0 {
			t.Error("the interrupted write reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the blocked write was not released by close")
	}
}

// TestSessionRelease_OneStuckResourceDoesNotStallTheRest: releaseOwned ran
// every release inline, one after another, on the goroutine that ended the
// session (the store's cleanup loop, App.stop). A release that blocks must
// not stop the others or hold that goroutine past a bound.
func TestSessionRelease_OneStuckResourceDoesNotStallTheRest(t *testing.T) {
	sess := &liveSession{done: make(chan struct{})}
	stuck := make(chan struct{})
	defer close(stuck)
	released := make(chan string, 2)
	sess.addOwned("stuck", func() { <-stuck })
	sess.addOwned("quick", func() { released <- "quick" })
	returned := make(chan struct{})
	go func() {
		sess.releaseOwned()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(ownedReleaseBound + 2*time.Second):
		t.Fatal("releaseOwned did not return with one stuck release")
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the quick release never ran behind the stuck one")
	}
}
