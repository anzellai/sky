//go:build linux || darwin

package rt

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// waitDead polls until pid is not running (or the bound passes).
func waitDead(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !pidAlive(pid)
}

func killOnCleanup(t *testing.T, pid int) {
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

// TestProcessClose_EndsTheShellsBackgroundJobs is the D-1 regression. With a
// PTY the child is a session leader, and an interactive shell puts each job
// in a process group of its own; close signalled only the child's group, and
// the kernel's hang-up reaches only the foreground group, so a `sleep &`
// typed into a Std.Ui.Terminal outlived close, the session and the program.
func TestProcessClose_EndsTheShellsBackgroundJobs(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "pid")
	c := procCmd{program: "/bin/bash", args: []string{"--norc", "--noprofile", "-i"}, pty: true, cols: 80, rows: 24}
	id := spawnT(t, c)
	time.Sleep(300 * time.Millisecond)
	procOk(t, procTask(t, Subprocess_write(id, "sleep 30 & echo $! > "+mark+"\n")))
	job := readPidMark(t, mark)
	killOnCleanup(t, job)
	if !pidAlive(job) {
		t.Fatal("the background job did not start")
	}
	procOk(t, procTask(t, Subprocess_close(id)))
	if !waitDead(job, 3*time.Second) {
		t.Fatalf("background job %d outlived Process.close", job)
	}
}

// TestProcessClose_EndsADescendantThatLeftTheSession: a descendant that
// called setsid is in no group or session of the child's. Close still finds
// it: in the first snapshot through its parent link, and after a double fork
// (re-parented away) through the environment cookie. This is the cookie
// path; on macOS it proves kern.procargs2 is readable (the macos-behaviour
// job runs it). The daemon execs a program, as a real one does: measured on
// macOS, a perl process that forks and never execs shows no environment to
// kern.procargs2 (perl reuses that area).
func TestProcessClose_EndsADescendantThatLeftTheSession(t *testing.T) {
	requirePerl(t)
	dir := t.TempDir()
	direct := filepath.Join(dir, "direct")
	daemon := filepath.Join(dir, "daemon")
	script := `perl -MPOSIX -e 'POSIX::setsid(); open(F,">","` + direct + `"); print F $$; close F; sleep 30' &` +
		` perl -MPOSIX -e 'exit if fork; POSIX::setsid(); exit if fork; open(F,">","` + daemon + `"); print F $$; close F; exec "sleep", "30"' &` +
		` exec sleep 100`
	id := spawnT(t, shCmd(script))
	p1 := readPidMark(t, direct)
	p2 := readPidMark(t, daemon)
	killOnCleanup(t, p1)
	killOnCleanup(t, p2)
	procOk(t, procTask(t, Subprocess_close(id)))
	if !waitDead(p1, 3*time.Second) {
		t.Errorf("a setsid descendant (%d) outlived Process.close", p1)
	}
	if !waitDead(p2, 3*time.Second) {
		t.Errorf("a double-forked setsid descendant (%d) outlived Process.close: the cookie was not found", p2)
	}
}

// TestProcessClose_LeavesSiblingsAndOtherChildrenAlone: the sweep ends one
// child's tree and nothing else: another spawned process, its children, and
// a child the runtime started some other way (the embedded PostgreSQL
// postmaster is one) survive, and that child's exit status still reaches its
// own Wait: there is no subreaper and no wait on any other pid.
func TestProcessClose_LeavesSiblingsAndOtherChildrenAlone(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "pid")
	sibling := spawnT(t, shCmd("sleep 30 & echo $! > "+mark+"; wait"))
	siblingChild := readPidMark(t, mark)
	killOnCleanup(t, siblingChild)
	siblingPid := handleOf(t, sibling).pid

	direct := exec.Command("/bin/sh", "-c", "sleep 1; exit 7")
	if err := direct.Start(); err != nil {
		t.Fatal(err)
	}

	victim := spawnT(t, shCmd("sleep 30 & sleep 30 & wait"))
	procOk(t, procTask(t, Subprocess_close(victim)))

	if !pidAlive(siblingPid) || !pidAlive(siblingChild) {
		t.Fatalf("closing one process killed another (sibling %v, its child %v)", pidAlive(siblingPid), pidAlive(siblingChild))
	}
	err := direct.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("a direct child's exit status did not reach its Wait: %v", err)
	}
}

// TestProcKillVerified_RefusesAReusedPid: a sweep kills only the process its
// snapshot saw. A pid whose start time no longer matches names another
// process now (PID reuse), and is left alone and running.
func TestProcKillVerified_RefusesAReusedPid(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	pid := cmd.Process.Pid
	start := procStartTime(pid)
	if start == 0 {
		t.Fatalf("no start time for a live process %d", pid)
	}
	procKillVerified(procInfo{pid: pid, start: start + 1})
	time.Sleep(100 * time.Millisecond)
	if !pidAlive(pid) {
		t.Fatal("a process with another start time was killed")
	}
	if procStopped(pid) {
		t.Fatal("a refused kill left the process stopped")
	}
	procKillVerified(procInfo{pid: pid, start: start})
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestProcessTreeCookie_IsInTheChildEnvironment: the cookie reaches the
// child (and so its descendants), and the platform reader finds it in a
// running process (/proc/<pid>/environ on Linux, kern.procargs2 on macOS).
func TestProcessTreeCookie_IsInTheChildEnvironment(t *testing.T) {
	id := spawnT(t, shCmd(`printf '%s' "$`+procTreeCookieEnv+`"; exec sleep 30`))
	h := handleOf(t, id)
	c := readChunk(t, id, procStreamStdout, 0)
	if len(c.data) != 32 || c.data != h.treeCookie {
		t.Fatalf("child cookie %q, handle cookie %q", c.data, h.treeCookie)
	}
	// Polled: while the shell execs sleep the read can be empty or cut off,
	// which procCookieOf reports as procEnvUnsure, never as a value.
	var v string
	st := procEnvUnsure
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v, st = procCookieOf(h.pid); st == procEnvFound {
			break
		}
	}
	if st != procEnvFound || v != h.treeCookie {
		t.Fatalf("the platform reader found %q (state %d) in the child, want %q", v, st, h.treeCookie)
	}
}

// TestProcEnvScan_TornReadIsUnsure: a read taken while a process is in
// execve (empty, or a cut-off last string) is never taken as a value. A CI
// run read "d4" for a 32-digit cookie this way.
func TestProcEnvScan_TornReadIsUnsure(t *testing.T) {
	const name = procTreeCookieEnv
	full := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		in   string
		want procEnvState
		val  string
	}{
		{"", procEnvUnsure, ""},
		{"\x00\x00\x00", procEnvUnsure, ""},
		{"A=1\x00" + name + "=d4", procEnvUnsure, ""},
		{"A=1\x00" + name + "=" + full + "\x00", procEnvFound, full},
		{"A=1\x00B=2\x00", procEnvAbsent, ""},
		{"/bin/sleep\x00\x00sleep\x0030\x00", procEnvUnsure, ""},
	}
	for _, c := range cases {
		v, st := procEnvScan([]byte(c.in), name)
		if st != c.want || v != c.val {
			t.Errorf("procEnvScan(%q) = %q, %d; want %q, %d", c.in, v, st, c.val, c.want)
		}
	}
}

// TestProcCookieOf_MalformedValueIsUnsure: a value without the cookie's form
// (32 lowercase hex digits) is a torn read, so the sweep reads it again.
func TestProcCookieOf_MalformedValueIsUnsure(t *testing.T) {
	saved := procEnvLookup
	defer func() { procEnvLookup = saved }()
	procEnvLookup = func(int, string) (string, procEnvState) { return "d4", procEnvFound }
	if _, st := procCookieOf(1); st != procEnvUnsure {
		t.Fatalf("a short value gave state %d, want procEnvUnsure", st)
	}
}

// TestProcTreeSweep_RereadsAProcessInExec: a descendant whose first read is
// torn is read again in a later round and killed, instead of ending the
// sweep with it alive.
func TestProcTreeSweep_RereadsAProcessInExec(t *testing.T) {
	cookie := newProcTreeCookie()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	pid := cmd.Process.Pid
	saved := procEnvLookup
	defer func() { procEnvLookup = saved }()
	reads := 0
	procEnvLookup = func(p int, name string) (string, procEnvState) {
		if p != pid {
			return "", procEnvAbsent
		}
		reads++
		if reads <= 2 {
			return "", procEnvUnsure // in execve for the first two reads
		}
		return cookie, procEnvFound
	}
	// The root is another process (this one's parent stands in: never
	// killed, since it is not a member by session, group or cookie).
	procTreeSweepNow([]procTreeRoot{{pid: 0x7ffffffe, cookie: cookie}})
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the descendant survived the sweep after %d reads", reads)
	}
}
