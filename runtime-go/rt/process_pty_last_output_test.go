//go:build unix

package rt

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestProcessPty_OutputOfAChildThatExitsAtOnceIsKept is the regression for
// the macOS conformance failure "process: a PTY child is on a tty — expected
// True, got False" (rust-ci run 38797890228). `tty` writes one line and exits
// within microseconds. The parent closed its copy of the PTY slave right after
// Start, so the child's exit was the slave's last close, and on macOS that can
// discard output the master has not read yet: under load the pump read EOF
// with nothing before it, and the ring held "". The parent now holds the slave
// until the child has exited and the master has read every queued byte
// (TIOCOUTQ is 0), then closes it.
//
// Many short children run at once, so the pump is late for some of them, as
// it is on a loaded CI runner. Measured on an 8-core Mac with 16 busy `yes`
// loops: before the fix, 3 of 15 runs lost one child's output (""); after,
// 0 of 15.
func TestProcessPty_OutputOfAChildThatExitsAtOnceIsKept(t *testing.T) {
	tty, err := exec.LookPath("tty")
	if err != nil {
		t.Fatalf("this test needs the `tty` program on PATH: %v", err)
	}
	const workers, each = 8, 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	var lost []string
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				out := ptyOutputOf(t, procSpec{program: tty, pty: true, cols: 80, rows: 24, ringSize: 64 << 10})
				if !strings.Contains(out, "/dev/") {
					mu.Lock()
					lost = append(lost, out)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if len(lost) > 0 {
		t.Fatalf("%d of %d PTY children lost their output (first: %q)", len(lost), workers*each, lost[0])
	}
}

// ptyOutputOf spawns s, waits until its output is drained, and returns it.
func ptyOutputOf(t *testing.T, s procSpec) string {
	t.Helper()
	h, perr := spawnProcess(s)
	if perr != nil {
		t.Errorf("spawn %s: %v", s.program, perr)
		return ""
	}
	defer h.shutdown()
	select {
	case <-h.drained:
	case <-time.After(20 * time.Second):
		t.Errorf("%s: output not drained after 20s", s.program)
		return ""
	}
	return string(h.out[procStreamStdout].readFrom(0, 1<<20).data)
}
