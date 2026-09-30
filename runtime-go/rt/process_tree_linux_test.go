//go:build linux

package rt

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// procStopped reports whether pid is stopped (state T).
func procStopped(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'T'
}

func openFDCount() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(es)
}

func pumpGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "rt.spawnProcess.func")
}

// TestProcessClose_PtyPumpEndsWhileAnEscapedJobHoldsTheSlave is the D-5
// regression (Linux only: the master was a blocking descriptor outside the Go
// poller, and on Linux a master read blocks while ANY process holds the
// slave, so os.File.Close could not interrupt the pump's read). A job that
// escapes every sweep (it clears its environment, leaves the session and
// double-forks) holds the slave; after close the pump goroutine, its thread
// and the master descriptor must be gone at once, not when the job exits.
func TestProcessClose_PtyPumpEndsWhileAnEscapedJobHoldsTheSlave(t *testing.T) {
	requirePerl(t)
	mark := filepath.Join(t.TempDir(), "pid")
	runtime.GC()
	fdsBefore := openFDCount()
	c := procCmd{program: "/bin/sh", pty: true, cols: 80, rows: 24, args: []string{"-c",
		daemonHoldingStdin(mark, 20) + " exec sleep 100"}}
	id := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	daemon := readPidMark(t, mark)
	killOnCleanup(t, daemon)
	procOk(t, procTask(t, Subprocess_close(id)))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pumpGoroutines() > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := pumpGoroutines(); n > 0 {
		t.Fatalf("%d pump goroutine(s) still blocked on the PTY master after close", n)
	}
	if !pidAlive(daemon) {
		t.Fatal("the escaped job did not survive close, so this test proved nothing about a held slave")
	}
	if n := openFDCount(); n > fdsBefore {
		t.Fatalf("%d descriptor(s) still open after close", n-fdsBefore)
	}
}

// TestTerminalReplies_BlockedPtyWriteDoesNotPileUp is the Linux leg of D-6:
// a program that asks for device reports and never reads its input fills the
// PTY input queue, and on Linux the master write then blocks. The replies
// must stay bounded.
func TestTerminalReplies_BlockedPtyWriteDoesNotPileUp(t *testing.T) {
	c := procCmd{program: "/bin/sh", pty: true, cols: 80, rows: 24, args: []string{"-c",
		`stty -icanon -echo; i=0; while [ $i -lt 3000 ]; do printf '\033[6n'; i=$((i+1)); done; sleep 30`}}
	id := spawnT(t, c)
	h := handleOf(t, id)
	h.screen() // the screen answers device reports
	before := runtime.NumGoroutine()
	time.Sleep(1500 * time.Millisecond)
	if grew := runtime.NumGoroutine() - before; grew > 4 {
		t.Fatalf("terminal replies behind a full PTY left %d extra goroutines", grew)
	}
}
