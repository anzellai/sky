//go:build unix

package rt

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────

type procCmd struct {
	program  string
	args     []string
	env      [][2]string
	clearEnv bool
	cwd      string
	pty      bool
	cols     int
	rows     int
	ring     int
}

func (c procCmd) record() map[string]any {
	args := make([]any, len(c.args))
	for i, a := range c.args {
		args[i] = a
	}
	env := make([]any, len(c.env))
	for i, kv := range c.env {
		env[i] = SkyTuple2{V0: kv[0], V1: kv[1]}
	}
	return map[string]any{
		"program": c.program, "args": args, "env": env, "clearEnv": c.clearEnv,
		"cwd": c.cwd, "pty": c.pty, "ptyCols": c.cols, "ptyRows": c.rows,
		"bufferSize": c.ring,
	}
}

func procTask(t *testing.T, task any) SkyResult[any, any] {
	t.Helper()
	return task.(func() any)().(SkyResult[any, any])
}

func procOk(t *testing.T, res SkyResult[any, any]) any {
	t.Helper()
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %s", errorMessageOf(res.ErrValue))
	}
	return res.OkValue
}

// procErrTag is the ErrorKind tag of a Sky Error (5 = NotFound).
func procErrTag(e any) int {
	adt, ok := e.(skyErrorAdt)
	if !ok || len(adt.Fields) == 0 {
		return -1
	}
	tag, _ := adt.Fields[0].(int)
	return tag
}

func errorMessageOf(e any) string {
	adt, ok := e.(skyErrorAdt)
	if !ok || len(adt.Fields) < 2 {
		return fmt.Sprintf("%v", e)
	}
	return errorKindName(e) + ": " + fmt.Sprintf("%v", adt.Fields[1])
}

// spawnT spawns and registers cleanup that closes the process, so no test
// leaves a child behind even when it fails half way.
func spawnT(t *testing.T, c procCmd) int {
	t.Helper()
	id := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	t.Cleanup(func() { procTask(t, Subprocess_close(id)) })
	return id
}

func shCmd(script string) procCmd {
	return procCmd{program: "/bin/sh", args: []string{"-c", script}}
}

type chunkT struct {
	data    string
	from    int
	next    int
	dropped bool
	eof     bool
}

func readChunk(t *testing.T, id, stream, offset int) chunkT {
	t.Helper()
	res := procTask(t, Subprocess_readWithin(10000, id, stream, offset))
	m := procOk(t, res).(map[string]any)
	return chunkT{m["data"].(string), m["from"].(int), m["next"].(int), m["dropped"].(bool), m["eof"].(bool)}
}

// readAll reads a stream from offset 0 to EOF.
func readAll(t *testing.T, id, stream int) string {
	t.Helper()
	var b strings.Builder
	off := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		c := readChunk(t, id, stream, off)
		if c.dropped {
			t.Fatalf("unexpected dropped at offset %d", off)
		}
		b.WriteString(c.data)
		off = c.next
		if c.eof {
			return b.String()
		}
	}
	t.Fatalf("stream %d did not reach EOF; got %q", stream, b.String())
	return ""
}

func exitStatus(t *testing.T, id int) string {
	t.Helper()
	done := make(chan SkyResult[any, any], 1)
	go func() { done <- Subprocess_wait(id).(func() any)().(SkyResult[any, any]) }()
	select {
	case res := <-done:
		adt := procOk(t, res).(SkyADT)
		return fmt.Sprintf("%s %v", adt.SkyName, adt.Fields[0])
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not return")
	}
	return ""
}

func handleOf(t *testing.T, id int) *procHandle {
	t.Helper()
	h, e := lookupProc(id)
	if e != nil {
		t.Fatalf("no handle %d", id)
	}
	return h
}

// ── round trips ──────────────────────────────────────────────────────────

func TestProcessCatRoundTrip(t *testing.T) {
	id := spawnT(t, procCmd{program: "cat"})
	procOk(t, procTask(t, Subprocess_write(id, "hello\n")))
	procOk(t, procTask(t, Subprocess_write(id, "world\n")))
	procOk(t, procTask(t, Subprocess_closeStdin(id)))
	if got := readAll(t, id, procStreamStdout); got != "hello\nworld\n" {
		t.Fatalf("cat echoed %q", got)
	}
	if got := exitStatus(t, id); got != "ExitCode 0" {
		t.Fatalf("exit = %s", got)
	}
	// Writing after closeStdin is refused, not a panic.
	if res := procTask(t, Subprocess_write(id, "late")); res.Tag == 0 {
		t.Fatal("write after closeStdin must be Err")
	}
}

func TestProcessEchoStdoutAndStderrAreSeparate(t *testing.T) {
	id := spawnT(t, shCmd("echo out; echo err 1>&2"))
	if got := readAll(t, id, procStreamStdout); got != "out\n" {
		t.Fatalf("stdout %q", got)
	}
	if got := readAll(t, id, procStreamStderr); got != "err\n" {
		t.Fatalf("stderr %q", got)
	}
}

func TestProcessExitCodesAndSignals(t *testing.T) {
	id := spawnT(t, shCmd("exit 3"))
	if got := exitStatus(t, id); got != "ExitCode 3" {
		t.Fatalf("exit = %s", got)
	}
	id2 := spawnT(t, procCmd{program: "sleep", args: []string{"30"}})
	procOk(t, procTask(t, Subprocess_kill(id2, 1))) // Terminate
	if got := exitStatus(t, id2); got != fmt.Sprintf("Signalled %d", int(syscall.SIGTERM)) {
		t.Fatalf("exit after Terminate = %s", got)
	}
	// kill on an exited process is Ok (nothing to signal).
	procOk(t, procTask(t, Subprocess_kill(id2, 2)))
}

func TestProcessEnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	c := shCmd(`printf '%s|' "$SKY_PROC_T"; pwd`)
	c.env = [][2]string{{"SKY_PROC_T", "bar"}}
	c.cwd = dir
	id := spawnT(t, c)
	got := strings.TrimSpace(readAll(t, id, procStreamStdout))
	real, _ := os.Getwd()
	_ = real
	if !strings.HasPrefix(got, "bar|") || !strings.HasSuffix(got, strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("env/cwd output %q (dir %s)", got, dir)
	}
	// clearEnv: only the added variables reach the child.
	c2 := procCmd{program: "/usr/bin/env", clearEnv: true, env: [][2]string{{"ONLY", "1"}}}
	id2 := spawnT(t, c2)
	if got := readAll(t, id2, procStreamStdout); got != "ONLY=1\n" {
		t.Fatalf("clearEnv env output %q", got)
	}
}

func TestProcessSpawnErrors(t *testing.T) {
	res := procTask(t, Subprocess_spawn(procCmd{program: "/definitely/not/a/program"}.record()))
	if res.Tag == 0 || procErrTag(res.ErrValue) != 5 { // NotFound
		t.Fatalf("missing program: want Err NotFound, got %+v", res)
	}
	res = procTask(t, Subprocess_spawn(procCmd{program: ""}.record()))
	if res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
		t.Fatalf("empty program: want Err InvalidInput")
	}
	res = procTask(t, Subprocess_readFrom(987654, 0, 0))
	if res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
		t.Fatalf("unknown process: want Err InvalidInput")
	}
}

// ── the ring ─────────────────────────────────────────────────────────────

// expectedLines is what `awk 'BEGIN{for(i=0;i<n;i++) printf "%05d\n", i}'`
// prints.
func expectedLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%05d\n", i)
	}
	return b.String()
}

func awkLines(n int) procCmd {
	return procCmd{program: "awk", args: []string{fmt.Sprintf(`BEGIN{for(i=0;i<%d;i++) printf "%%05d\n", i}`, n)}}
}

// A child whose output overflows the ring is never blocked (it exits with no
// reader at all), and a reader that fell behind is told so.
func TestProcessLargeOutputBeyondTheRingSetsDropped(t *testing.T) {
	c := awkLines(50000) // 300,000 bytes
	c.ring = 4096
	id := spawnT(t, c)
	if got := exitStatus(t, id); got != "ExitCode 0" {
		t.Fatalf("a child writing past the ring must exit without a reader: %s", got)
	}
	h := handleOf(t, id)
	select {
	case <-h.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("output never drained")
	}
	want := expectedLines(50000)
	c0 := readChunk(t, id, procStreamStdout, 0)
	if !c0.dropped {
		t.Fatal("reading offset 0 of an overwritten ring must set dropped")
	}
	if c0.from != len(want)-4096 {
		t.Fatalf("from = %d, want %d", c0.from, len(want)-4096)
	}
	if c0.data != want[c0.from:] || c0.next != len(want) || !c0.eof {
		t.Fatalf("tail mismatch: next=%d eof=%v data[:12]=%q", c0.next, c0.eof, c0.data[:12])
	}
}

// Offsets are absolute: a reader can stop anywhere and resume.
func TestProcessOffsetResume(t *testing.T) {
	id := spawnT(t, awkLines(30000)) // 180,000 bytes, ring 1 MiB
	want := expectedLines(30000)
	exitStatus(t, id)
	// Read in two halves from an arbitrary split offset.
	split := 77777
	var got strings.Builder
	off := split
	for {
		c := readChunk(t, id, procStreamStdout, off)
		if c.from != off {
			t.Fatalf("resume at %d returned from %d", off, c.from)
		}
		got.WriteString(c.data)
		off = c.next
		if c.eof {
			break
		}
	}
	if got.String() != want[split:] {
		t.Fatalf("resumed read mismatch (%d bytes vs %d)", got.Len(), len(want)-split)
	}
	// A negative offset means "what you still have".
	c := readChunk(t, id, procStreamStdout, -1)
	if c.from != 0 || c.dropped {
		t.Fatalf("offset -1: from=%d dropped=%v", c.from, c.dropped)
	}
}

func TestOutRingUnit(t *testing.T) {
	r := newOutRing(10)
	r.write([]byte("abcdef"))
	if c := r.readFrom(0, 100); string(c.data) != "abcdef" || c.next != 6 || c.dropped {
		t.Fatalf("simple read %+v", c)
	}
	r.write([]byte("ghijkl")) // 12 bytes written, ring keeps 10: "cdefghijkl"
	c := r.readFrom(0, 100)
	if !c.dropped || c.from != 2 || string(c.data) != "cdefghijkl" {
		t.Fatalf("wrapped read %+v %q", c, c.data)
	}
	c = r.readFrom(5, 3)
	if string(c.data) != "fgh" || c.next != 8 {
		t.Fatalf("bounded read %+v %q", c, c.data)
	}
	r.write([]byte(strings.Repeat("z", 25) + "0123456789")) // oversized write
	c = r.readFrom(-1, 100)
	if string(c.data) != "0123456789" || c.from != 37 {
		t.Fatalf("oversized write kept %q from %d", c.data, c.from)
	}
	if c := r.readFrom(1000, 10); c.from != 47 || len(c.data) != 0 {
		t.Fatalf("offset past the end: %+v", c)
	}
	r.closeEOF()
	if c := r.readFrom(47, 10); !c.eof {
		t.Fatal("eof not reported at the end")
	}
}

// ── lifecycle ────────────────────────────────────────────────────────────

// pidAlive reports whether pid is a running process. A zombie (exited, not
// yet reaped by its parent) is not running: where /proc exists its state is
// read, because signal 0 still succeeds on a zombie. That matters when the
// orphaned grandchild is re-parented to a PID 1 that does not reap (this test
// binary itself, run as a container's init).
func pidAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) {
			if st := b[i+2]; st == 'Z' || st == 'X' {
				return false
			}
		}
	}
	return true
}

// kill signals the process GROUP: a grandchild dies with its parent.
func TestProcessKillReachesGrandchild(t *testing.T) {
	id := spawnT(t, shCmd("sleep 60 & echo $!; wait"))
	c := readChunk(t, id, procStreamStdout, 0)
	gpid, err := strconv.Atoi(strings.TrimSpace(c.data))
	if err != nil || gpid <= 0 {
		t.Fatalf("grandchild pid %q", c.data)
	}
	if !pidAlive(gpid) {
		t.Fatal("grandchild not running before kill")
	}
	procOk(t, procTask(t, Subprocess_kill(id, 1)))
	exitStatus(t, id)
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(gpid) {
		if time.Now().After(deadline) {
			syscall.Kill(gpid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived kill of the group", gpid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An exited child is reaped at once, whether or not anyone calls wait: a
// zombie would still answer signal 0.
func TestProcessNoZombie(t *testing.T) {
	id := spawnT(t, procCmd{program: "true"})
	h := handleOf(t, id)
	select {
	case <-h.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("child never exited")
	}
	if err := syscall.Kill(h.pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("exited child %d is still in the process table (zombie?): %v", h.pid, err)
	}
}

func TestProcessCloseKillsAndForgets(t *testing.T) {
	c := procCmd{program: "sleep", args: []string{"60"}}
	id := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	pid := handleOf(t, id).pid
	procOk(t, procTask(t, Subprocess_close(id)))
	if pidAlive(pid) {
		t.Fatalf("closed process %d still alive", pid)
	}
	if res := procTask(t, Subprocess_wait(id)); res.Tag == 0 {
		t.Fatal("a closed handle must be forgotten (wait = Err)")
	}
	procOk(t, procTask(t, Subprocess_close(id))) // idempotent
}

func TestKillAllChildProcesses(t *testing.T) {
	c := procCmd{program: "sleep", args: []string{"60"}}
	a := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	b := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
	pa, pb := handleOf(t, a).pid, handleOf(t, b).pid
	killAllChildProcesses()
	if pidAlive(pa) || pidAlive(pb) {
		t.Fatal("killAllChildProcesses left a child running")
	}
	if len(procLiveHandles()) != 0 {
		t.Fatalf("handles left: %v", procLiveHandles())
	}
}

// A process spawned inside a Sky.Live session belongs to it: ending the
// session (eviction, App.stop) kills the child.
func TestProcessOwnedBySessionDiesWithIt(t *testing.T) {
	sess := &liveSession{done: make(chan struct{})}
	var id int
	runWithLiveSession(sess, func() {
		id = procOk(t, procTask(t, Subprocess_spawn(procCmd{program: "sleep", args: []string{"60"}}.record()))).(int)
	})
	pid := handleOf(t, id).pid
	sess.markDone()
	if pidAlive(pid) {
		t.Fatalf("session-owned child %d survived the session", pid)
	}
	if _, e := lookupProc(id); e == nil {
		t.Fatal("session-owned handle survived the session")
	}
}

// ── PTY ──────────────────────────────────────────────────────────────────

func TestProcessPtyIsATerminal(t *testing.T) {
	c := shCmd("tty; stty size")
	c.pty, c.cols, c.rows = true, 80, 24
	id := spawnT(t, c)
	out := readAll(t, id, procStreamStdout)
	if !strings.Contains(out, "/dev/") || strings.Contains(out, "not a tty") {
		t.Fatalf("tty under a PTY printed %q", out)
	}
	if !strings.Contains(out, "24 80") {
		t.Fatalf("stty size under an 80x24 PTY printed %q", out)
	}
	// With a PTY there is one merged stream: stderr is empty and at EOF.
	if c := readChunk(t, id, procStreamStderr, 0); c.data != "" || !c.eof {
		t.Fatalf("PTY stderr stream %+v", c)
	}
}

func TestProcessPtyResize(t *testing.T) {
	c := shCmd("read x; stty size")
	c.pty, c.cols, c.rows = true, 80, 24
	id := spawnT(t, c)
	procOk(t, procTask(t, Subprocess_resize(id, 132, 43)))
	procOk(t, procTask(t, Subprocess_write(id, "go\n")))
	out := readAll(t, id, procStreamStdout)
	if !strings.Contains(out, "43 132") {
		t.Fatalf("stty size after resize printed %q", out)
	}
	// resize without a PTY is an Err, not a panic.
	id2 := spawnT(t, procCmd{program: "cat"})
	if res := procTask(t, Subprocess_resize(id2, 10, 10)); res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
		t.Fatal("resize without a PTY must be Err InvalidInput")
	}
}

// ── consumer modes and the Sub ───────────────────────────────────────────

// eventString renders one Event for assertions.
func eventString(ev any) string {
	adt := ev.(SkyADT)
	switch adt.SkyName {
	case "Output":
		s := [2]string{"Stdout", "Stderr"}[adt.Fields[0].(int)]
		c := adt.Fields[1].(map[string]any)
		return fmt.Sprintf("Output %s %q", s, c["data"])
	case "Exited":
		st := adt.Fields[0].(SkyADT)
		return fmt.Sprintf("Exited %s %v", st.SkyName, st.Fields[0])
	}
	return adt.SkyName
}

func collectUntil(t *testing.T, ch chan any, pred func([]string) bool) []string {
	t.Helper()
	var got []string
	deadline := time.After(15 * time.Second)
	for !pred(got) {
		select {
		case m := <-ch:
			got = append(got, eventString(m))
		case <-deadline:
			t.Fatalf("timed out; events so far: %v", got)
		}
	}
	return got
}

func TestProcessEventsSubDeliversOutputThenExit(t *testing.T) {
	id := spawnT(t, shCmd("echo a; echo b 1>&2; exit 2"))
	msgCh := make(chan any, 64)
	m := newSubManager(msgCh)
	defer m.stopAll()
	identity := func(ev any) any { return ev }
	m.update(func(any) any { return Subprocess_events(id, identity) }, nil)
	got := collectUntil(t, msgCh, func(g []string) bool {
		return len(g) > 0 && strings.HasPrefix(g[len(g)-1], "Exited")
	})
	joined := strings.Join(got, ";")
	if !strings.Contains(joined, `Output Stdout "a\n"`) || !strings.Contains(joined, `Output Stderr "b\n"`) {
		t.Fatalf("events %v", got)
	}
	if got[len(got)-1] != "Exited ExitCode 2" {
		t.Fatalf("last event %s", got[len(got)-1])
	}
	// The source ended on its own: the program is no longer kept alive.
	deadline := time.Now().Add(5 * time.Second)
	for m.hasTimers() {
		if time.Now().After(deadline) {
			t.Fatal("a finished events Sub still counts as active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A Task read is refused: the output belongs to the Sub mode.
	if res := procTask(t, Subprocess_readFrom(id, 0, 0)); res.Tag == 0 || errorKindName(res.ErrValue) != "InvalidInput" {
		t.Fatal("readFrom on a Sub-read process must be Err InvalidInput")
	}
}

// Dropping the Sub stops the runner before the source is released: after
// update returns, the runner goroutine is gone, the claim is released and no
// further Msg arrives.
func TestProcessEventsSubTeardownStreamLeavesFirst(t *testing.T) {
	id := spawnT(t, shCmd("while true; do echo x; sleep 0.01; done"))
	msgCh := make(chan any, 1) // small: the runner is often parked on send
	m := newSubManager(msgCh)
	defer m.stopAll()
	ident := func(ev any) any { return ev }
	m.update(func(any) any { return Subprocess_events(id, ident) }, nil)
	recvOrFail(t, msgCh, 10*time.Second)
	recvOrFail(t, msgCh, 10*time.Second)
	m.mu.Lock()
	r := m.sources[fmt.Sprintf("process:%d", id)]
	m.mu.Unlock()
	if r == nil {
		t.Fatal("no runner registered")
	}
	m.update(func(any) any { return Sub_none() }, nil)
	select {
	case <-r.done:
	default:
		t.Fatal("update returned before the dropped runner stopped")
	}
	h := handleOf(t, id)
	h.mu.Lock()
	active := h.subActive
	h.mu.Unlock()
	if active {
		t.Fatal("the source claim was not released after the runner stopped")
	}
	// Drain what was queued before the drop, then nothing else arrives.
	for len(msgCh) > 0 {
		<-msgCh
	}
	select {
	case ev := <-msgCh:
		t.Fatalf("a Msg arrived after the Sub was dropped: %v", eventString(ev))
	case <-time.After(150 * time.Millisecond):
	}
	// Re-subscribing resumes (the cursor lives on the handle): the next
	// event continues the stream rather than replaying from offset 0.
	m.update(func(any) any { return Subprocess_events(id, ident) }, nil)
	ev := recvOrFail(t, msgCh, 10*time.Second).(SkyADT)
	from := ev.Fields[1].(map[string]any)["from"].(int)
	if from == 0 {
		t.Fatal("a re-added Sub replayed from offset 0 instead of resuming")
	}
}

func TestProcessEventsOnTaskReadProcessIsRefused(t *testing.T) {
	id := spawnT(t, shCmd("echo a; sleep 0.2; echo b"))
	readChunk(t, id, procStreamStdout, 0) // claims Task mode
	msgCh := make(chan any, 8)
	m := newSubManager(msgCh)
	defer m.stopAll()
	m.update(func(any) any { return Subprocess_events(id, func(ev any) any { return ev }) }, nil)
	m.mu.Lock()
	n := len(m.sources)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("an events Sub on a Task-read process must be refused")
	}
	select {
	case ev := <-msgCh:
		t.Fatalf("refused Sub delivered %v", eventString(ev))
	case <-time.After(400 * time.Millisecond):
	}
}

// No goroutine outlives the processes and subscriptions that created it.
func TestProcessNoGoroutineLeak(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		c := shCmd("echo hi; sleep 30")
		id := procOk(t, procTask(t, Subprocess_spawn(c.record()))).(int)
		msgCh := make(chan any, 8)
		m := newSubManager(msgCh)
		m.update(func(any) any { return Subprocess_events(id, func(ev any) any { return ev }) }, nil)
		<-msgCh
		m.stopAll()
		procOk(t, procTask(t, Subprocess_close(id)))
	}
	pty := shCmd("sleep 30")
	pty.pty, pty.cols, pty.rows = true, 80, 24
	id := procOk(t, procTask(t, Subprocess_spawn(pty.record()))).(int)
	procOk(t, procTask(t, Subprocess_close(id)))
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= before+1 {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutines: before %d, after %d\n%s", before, n, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
