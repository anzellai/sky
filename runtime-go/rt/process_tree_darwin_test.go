//go:build darwin

package rt

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// procStopped reports whether pid is stopped (SSTOP).
func procStopped(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && k.Proc.P_stat == 4
}

// procWitnessSets is newProcTreeWitness for one handle's marks.
func procWitnessSets(m procFdMarks) (pipes, ttys map[uint64]bool) {
	w := newProcTreeWitness([]procTreeRoot{{marks: m}})
	return w.pipes, w.ttys
}

// TestProcFdWitness_FindsAPlatformBinaryAndNoStranger: an Apple platform
// binary (/bin/sleep) whose environment macOS hides is still found by the
// child's stdout / stderr pipe (pipes) or its terminal (PTY), and a process
// of this user outside the tree is not.
func TestProcFdWitness_FindsAPlatformBinaryAndNoStranger(t *testing.T) {
	stranger := exec.Command("/bin/sleep", "30")
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stranger.Process.Kill(); _, _ = stranger.Process.Wait() }()

	for _, c := range []procCmd{
		shCmd("exec /bin/sleep 30"),
		{program: "/bin/sh", args: []string{"-c", "exec /bin/sleep 30"}, pty: true, cols: 80, rows: 24},
	} {
		id := spawnT(t, c)
		h := handleOf(t, id)
		if c.pty && len(h.treeMarks.ttys) != 1 || !c.pty && len(h.treeMarks.pipes) != 2 {
			t.Fatalf("pty=%v: marks %+v", c.pty, h.treeMarks)
		}
		pipes, ttys := procWitnessSets(h.treeMarks)
		found := false
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !found; time.Sleep(20 * time.Millisecond) {
			found = procHoldsFdMark(h.pid, pipes, ttys)
		}
		if !found {
			t.Fatalf("pty=%v: the child %d holding its own output was not found", c.pty, h.pid)
		}
		if procHoldsFdMark(stranger.Process.Pid, pipes, ttys) {
			t.Fatalf("pty=%v: a process outside the tree matched", c.pty)
		}
	}
}

// TestProcessClose_EndsAPtyDaemonOfAPlatformBinary: through a PTY, a
// double-forked setsid descendant that runs /bin/sleep (whose environment
// macOS 27 hides) is found by the terminal it still holds, and ended.
func TestProcessClose_EndsAPtyDaemonOfAPlatformBinary(t *testing.T) {
	requirePerl(t)
	daemon := filepath.Join(t.TempDir(), "daemon")
	script := `perl -MPOSIX -e 'exit if fork; POSIX::setsid(); exit if fork; open(F,">","` + daemon + `"); print F $$; close F; exec "/bin/sleep", "30"' &` +
		` exec sleep 100`
	id := spawnT(t, procCmd{program: "/bin/sh", args: []string{"-c", script}, pty: true, cols: 80, rows: 24})
	p := readPidMark(t, daemon)
	killOnCleanup(t, p)
	procOk(t, procTask(t, Subprocess_close(id)))
	if !waitDead(p, 3*time.Second) {
		t.Fatalf("a double-forked setsid descendant on the PTY (%d) outlived Process.close", p)
	}
}
