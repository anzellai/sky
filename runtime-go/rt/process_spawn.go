//go:build !js

package rt

// process_spawn.go — Sky.Core.Process streaming child processes.
//
//	Sky side                              Runtime side
//	────────                              ────────────
//	Process.spawn cmd                   → Subprocess_spawn: start the child in
//	                                      its own process group, one pump
//	                                      goroutine per output stream into an
//	                                      offset-addressed ring
//	                                      (process_ring.go), one reaper
//	                                      goroutine that always Waits.
//	Process.readFrom p s offset         → Subprocess_readFrom (Task consumer)
//	Process.events p toMsg              → Subprocess_events (Sub consumer,
//	                                      sub_source.go)
//	Process.write / closeStdin / resize / kill / wait / close
//
// # One consumer mode
//
// A process's output has ONE consumer mode for its whole life: the first
// `readFrom` makes it Task-read, the first `events` Sub makes it Sub-read.
// `readFrom` on a Sub-read process returns Err InvalidInput; an `events` Sub
// on a Task-read process is refused (logged once; no Msg arrives). Mixing the
// two would give each an arbitrary share of the output. `wait` is not a
// consumer and works in both modes.
//
// # Lifecycle
//
//   - The child is always reaped: the reaper goroutine calls Wait as soon as
//     the child starts, so an exited child never lingers as a zombie, whether
//     or not anyone calls `wait`.
//   - `kill` signals the child's whole process group, so grandchildren (the
//     workers of a shell pipeline) receive it too.
//   - `close` kills a running child (SIGKILL to the group), waits for the
//     reaper, closes every descriptor and forgets the handle.
//   - A process spawned from a Sky.Live session belongs to that session: when
//     the session ends — evicted, deleted, or its app stopped with App.stop /
//     Live.stop — the process is closed.
//   - Every live child is killed when this program exits through the runtime
//     (main returns or panics, System.exit, a signal handled by an app shape,
//     the shutdown drain).

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Consumer modes (procHandle.owner).
const (
	procOwnerNone int32 = iota
	procOwnerSub
	procOwnerTask
)

// procExitGrace: after the child exits, how long the runtime waits for its
// output pipes to reach EOF before it reports the exit anyway. A grandchild
// that inherited stdout and keeps running holds the pipe open; the exit must
// not wait for it.
const procExitGrace = 250 * time.Millisecond

// procStreamStdout / procStreamStderr are the `Stream` tags.
const (
	procStreamStdout = 0
	procStreamStderr = 1
)

type procSpec struct {
	program  string
	args     []string
	env      [][2]string
	clearEnv bool
	cwd      string
	pty      bool
	cols     int
	rows     int
	ringSize int
}

type procHandle struct {
	id  int64
	cmd *exec.Cmd
	pid int
	pty *os.File // PTY master, nil without a PTY

	stdinMu     sync.Mutex
	stdin       io.WriteCloser // pipe write end, or the PTY master
	stdinClosed bool

	readers []io.Closer // our read ends (pipes), closed on close()
	out     [2]*outRing

	exited   chan struct{} // closed after the reaper's Wait returned
	drained  chan struct{} // closed once exited and output reached EOF (or grace)
	exitCode int
	exitSig  int
	signaled bool

	owner atomic.Int32

	mu          sync.Mutex
	subCursor   [2]int64 // where the events Sub continues from
	subExitSent bool     // the Sub already delivered Exited
	subActive   bool
	closed      bool
	sess        *liveSession
	closeOnce   sync.Once

	// The terminal screen (process_screen.go): made by the first
	// Subprocess_screen, then fed every stdout byte as the pump stores it.
	scr     atomic.Pointer[procScreen]
	ptyCols int // the PTY size (guarded by mu)
	ptyRows int
	// sizeMu makes a size change one step: the making of the screen (which
	// takes ptyCols x ptyRows) and each resize (the screen, ptyCols x
	// ptyRows and the PTY's window size) run one at a time, so the screen,
	// the recorded size and the PTY always end at the same size.
	sizeMu sync.Mutex
}

var (
	procRegistry sync.Map // map[int64]*procHandle
	procIDs      atomic.Int64
	procExitOnce sync.Once
)

func lookupProc(idArg any) (*procHandle, any) {
	id := int64(AsInt(idArg))
	if v, ok := procRegistry.Load(id); ok {
		return v.(*procHandle), nil
	}
	return nil, ErrInvalidInput(fmt.Sprintf("Process: no process %d (it was closed, or its session ended)", id))
}

// parseProcSpec reads the `Command` record the Sky side builds.
func parseProcSpec(cfg any) procSpec {
	s := procSpec{
		program:  asBytesString(recordField(cfg, "Program", "program")),
		clearEnv: AsBool(recordField(cfg, "ClearEnv", "clearEnv")),
		cwd:      asBytesString(recordField(cfg, "Cwd", "cwd")),
		cols:     AsInt(recordField(cfg, "PtyCols", "ptyCols")),
		rows:     AsInt(recordField(cfg, "PtyRows", "ptyRows")),
		ringSize: AsInt(recordField(cfg, "BufferSize", "bufferSize")),
	}
	s.pty = AsBool(recordField(cfg, "Pty", "pty"))
	for _, a := range AsList(recordField(cfg, "Args", "args")) {
		s.args = append(s.args, asBytesString(a))
	}
	for _, kv := range AsList(recordField(cfg, "Env", "env")) {
		k, v := wsExtractStringPair(kv)
		if k != "" {
			s.env = append(s.env, [2]string{k, v})
		}
	}
	return s
}

// procEnviron builds the child's environment: the parent's (unless cleared),
// with each added pair replacing an existing entry of the same name.
func procEnviron(s procSpec) []string {
	var base []string
	if !s.clearEnv {
		base = os.Environ()
	}
	idx := map[string]int{}
	out := make([]string, 0, len(base)+len(s.env))
	for _, e := range base {
		k := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			k = e[:i]
		}
		idx[k] = len(out)
		out = append(out, e)
	}
	for _, kv := range s.env {
		e := kv[0] + "=" + kv[1]
		if i, ok := idx[kv[0]]; ok {
			out[i] = e
			continue
		}
		idx[kv[0]] = len(out)
		out = append(out, e)
	}
	return out
}

func procStartError(program string, err error) any {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return makeError(5, "NotFound", fmt.Sprintf("Process.spawn: %s: %v", program, err))
	}
	if errors.Is(err, fs.ErrPermission) {
		return ErrPermissionDenied(fmt.Sprintf("Process.spawn: %s: %v", program, err))
	}
	return ErrIo(fmt.Sprintf("Process.spawn: %s: %v", program, err))
}

// spawnProcess starts the child and its pump + reaper goroutines.
func spawnProcess(s procSpec) (*procHandle, any) {
	if s.program == "" {
		return nil, ErrInvalidInput("Process.spawn: the program name is empty")
	}
	if s.pty && (s.cols <= 0 || s.rows <= 0) {
		return nil, ErrInvalidInput("Process.spawn: a PTY needs cols and rows above 0")
	}
	cmd := exec.Command(s.program, s.args...)
	cmd.Dir = s.cwd
	cmd.Env = procEnviron(s)
	cmd.SysProcAttr = procSysAttr(s.pty)

	h := &procHandle{
		cmd:     cmd,
		exited:  make(chan struct{}),
		drained: make(chan struct{}),
		ptyCols: s.cols,
		ptyRows: s.rows,
	}
	h.out[procStreamStdout] = newOutRing(s.ringSize)
	h.out[procStreamStderr] = newOutRing(s.ringSize)

	// Descriptors the child gets; the parent closes its copies after Start.
	var childSide []*os.File
	var sources [2]*os.File
	closeAll := func(fs []*os.File) {
		for _, f := range fs {
			if f != nil {
				f.Close()
			}
		}
	}

	if s.pty {
		master, slave, err := procOpenPTY(s.cols, s.rows)
		if err != nil {
			return nil, ErrUnavailable("Process.spawn: " + err.Error())
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		childSide = []*os.File{slave}
		h.pty = master
		h.stdin = master
		sources[procStreamStdout] = master
	} else {
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, ErrIo("Process.spawn: " + err.Error())
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			closeAll([]*os.File{inR, inW})
			return nil, ErrIo("Process.spawn: " + err.Error())
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			closeAll([]*os.File{inR, inW, outR, outW})
			return nil, ErrIo("Process.spawn: " + err.Error())
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
		childSide = []*os.File{inR, outW, errW}
		h.stdin = inW
		sources[procStreamStdout] = outR
		sources[procStreamStderr] = errR
		h.readers = []io.Closer{outR, errR}
	}

	if err := cmd.Start(); err != nil {
		closeAll(childSide)
		closeAll(sources[:])
		if h.stdin != nil && !s.pty {
			h.stdin.Close()
		}
		return nil, procStartError(s.program, err)
	}
	closeAll(childSide)
	h.pid = cmd.Process.Pid
	h.id = procIDs.Add(1)

	var pumps sync.WaitGroup
	for i, src := range sources {
		ring := h.out[i]
		if src == nil {
			ring.closeEOF() // PTY: one merged stream; stderr is empty
			continue
		}
		pumps.Add(1)
		go func(src *os.File, ring *outRing, stdout bool) {
			defer pumps.Done()
			buf := make([]byte, 32<<10)
			for {
				n, err := src.Read(buf)
				if n > 0 {
					ring.write(buf[:n])
					if stdout {
						h.feedScreen()
					}
				}
				if err != nil {
					ring.closeEOF()
					if stdout {
						h.feedScreen()
					}
					return
				}
			}
		}(src, ring, i == procStreamStdout)
	}
	pumpsDone := make(chan struct{})
	go func() {
		pumps.Wait()
		close(pumpsDone)
	}()

	// The reaper: Wait at once so the child never stays a zombie.
	go func() {
		err := cmd.Wait()
		code, sig, signaled := procExitInfo(cmd.ProcessState)
		if cmd.ProcessState == nil && err != nil {
			code = -1
		}
		h.mu.Lock()
		h.exitCode, h.exitSig, h.signaled = code, sig, signaled
		h.mu.Unlock()
		close(h.exited)
		h.wakeRings()
		select {
		case <-pumpsDone:
		case <-time.After(procExitGrace):
		}
		close(h.drained)
		h.wakeRings()
	}()

	procRegistry.Store(h.id, h)
	procExitOnce.Do(func() {
		RegisterResourceCloser("process.children", killAllChildProcesses)
	})
	if sess := currentLiveSession(); sess != nil {
		h.sess = sess
		sess.addOwned(h.ownedKey(), func() { h.shutdown() })
	}
	return h, nil
}

func (h *procHandle) ownedKey() string { return fmt.Sprintf("process:%d", h.id) }

// wakeRings wakes every waiter on both rings (an exit is a state change a
// waiting Sub pump must observe).
func (h *procHandle) wakeRings() {
	for _, r := range h.out {
		r.mu.Lock()
		r.signalLocked()
		r.mu.Unlock()
	}
	if sc := h.scr.Load(); sc != nil {
		sc.wake()
	}
}

func (h *procHandle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// shutdown kills a running child, reaps it, and releases every descriptor.
// Idempotent. Bounded: a child that ignores SIGKILL cannot exist, but the
// wait is still capped so a stuck kernel cannot hang the caller.
func (h *procHandle) shutdown() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		select {
		case <-h.exited:
		default:
			_ = procSignalGroup(h.cmd.Process, syscall.SIGKILL)
			select {
			case <-h.exited:
			case <-time.After(5 * time.Second):
			}
		}
		// A grandchild that ignored the group kill (it moved to its own
		// group) may still hold a pipe: closing our read ends ends the pumps.
		h.stdinMu.Lock()
		if h.stdin != nil && !h.stdinClosed {
			h.stdinClosed = true
			h.stdin.Close()
		}
		h.stdinMu.Unlock()
		for _, c := range h.readers {
			c.Close()
		}
		if h.pty != nil {
			h.pty.Close()
		}
		for _, r := range h.out {
			r.closeEOF()
		}
		procRegistry.Delete(h.id)
		if h.sess != nil {
			h.sess.removeOwned(h.ownedKey())
		}
	})
}

// killAllChildProcesses ends every child this program still runs. Called on
// every runtime exit path (ExitProcess, LogPanicAndExit, the shutdown drain).
func killAllChildProcesses() {
	var hs []*procHandle
	procRegistry.Range(func(_, v any) bool {
		hs = append(hs, v.(*procHandle))
		return true
	})
	var wg sync.WaitGroup
	for _, h := range hs {
		wg.Add(1)
		go func(h *procHandle) {
			defer wg.Done()
			h.shutdown()
		}(h)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// exitStatusValue builds the Sky `ExitStatus` value: `ExitCode Int` (tag 0)
// or `Signalled Int` (tag 1).
func (h *procHandle) exitStatusValue() any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.signaled {
		return SkyADT{Tag: 1, SkyName: "Signalled", Fields: []any{h.exitSig}}
	}
	return SkyADT{Tag: 0, SkyName: "ExitCode", Fields: []any{h.exitCode}}
}

// streamValue builds the Sky `Stream` value. `Stream` is a pure enum
// (Stdout | Stderr, no payloads), which typed codegen represents as its
// constructor tag — a Go int, like `ErrorKind` (errorKindAdt) — not as a
// SkyADT.
func streamValue(s int) any {
	return s
}

// chunkValue builds the Sky `Chunk` record.
func chunkValue(c ringChunk) any {
	return map[string]any{
		"data":    string(c.data),
		"from":    int(c.from),
		"next":    int(c.next),
		"dropped": c.dropped,
		"eof":     c.eof,
	}
}

// claimTask makes Task reads the output's consumer mode.
func (h *procHandle) claimTask() any {
	if h.owner.CompareAndSwap(procOwnerNone, procOwnerTask) || h.owner.Load() == procOwnerTask {
		return nil
	}
	return ErrInvalidInput(fmt.Sprintf(
		"Process.readFrom: process %d is read by an events Sub; a process has one output consumer mode", h.id))
}

// ── Kernels ──────────────────────────────────────────────────────────────

// Subprocess_spawn : Command -> Task Error Int   (Sky wraps the id)
func Subprocess_spawn(cfg any) any {
	spec := parseProcSpec(cfg)
	return func() any {
		h, err := spawnProcess(spec)
		if err != nil {
			return Err[any, any](err)
		}
		return Ok[any, any](int(h.id))
	}
}

func procStreamArg(v any) (int, any) {
	s := AsInt(v)
	if s != procStreamStdout && s != procStreamStderr {
		return 0, ErrInvalidInput(fmt.Sprintf("Process: unknown stream %d", s))
	}
	return s, nil
}

// readProc is readFrom with an optional timeout (<0: wait as long as it takes).
func readProc(idArg, streamArg, offsetArg any, timeout time.Duration) any {
	h, e := lookupProc(idArg)
	if e != nil {
		return Err[any, any](e)
	}
	s, e := procStreamArg(streamArg)
	if e != nil {
		return Err[any, any](e)
	}
	if e := h.claimTask(); e != nil {
		return Err[any, any](e)
	}
	offset := int64(AsInt(offsetArg))
	ring := h.out[s]
	var deadline <-chan time.Time
	if timeout >= 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for {
		ready, changed := ring.wait(offset)
		if ready {
			return Ok[any, any](chunkValue(ring.readFrom(offset, maxProcessChunkBytes)))
		}
		select {
		case <-changed:
		case <-h.drained:
			// Exit reported with the pipe still open (a grandchild holds
			// it): hand back what there is; a later read still sees more.
			return Ok[any, any](chunkValue(ring.readFrom(offset, maxProcessChunkBytes)))
		case <-deadline:
			return Ok[any, any](chunkValue(ring.readFrom(offset, maxProcessChunkBytes)))
		}
	}
}

// Subprocess_readFrom : Int -> Int -> Int -> Task Error Chunk
func Subprocess_readFrom(idArg, streamArg, offsetArg any) any {
	return func() any { return readProc(idArg, streamArg, offsetArg, -1) }
}

// Subprocess_readWithin : Int -> Int -> Int -> Int -> Task Error Chunk
func Subprocess_readWithin(msArg, idArg, streamArg, offsetArg any) any {
	return func() any {
		ms := AsInt(msArg)
		if ms < 0 {
			ms = 0
		}
		return readProc(idArg, streamArg, offsetArg, time.Duration(ms)*time.Millisecond)
	}
}

// Subprocess_write : Int -> String -> Task Error ()
func Subprocess_write(idArg, dataArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		data := asBytesString(dataArg)
		h.stdinMu.Lock()
		defer h.stdinMu.Unlock()
		if h.stdinClosed {
			return Err[any, any](ErrInvalidInput("Process.write: stdin is closed"))
		}
		if _, err := io.WriteString(h.stdin, data); err != nil {
			return Err[any, any](ErrIo("Process.write: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Subprocess_closeStdin : Int -> Task Error ()
//
// Without a PTY this closes the pipe (the child reads end of file). With a
// PTY there is no separate stdin to close: the runtime writes the terminal's
// end-of-file character (Ctrl-D), which a line-mode reader sees as EOF.
func Subprocess_closeStdin(idArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		h.stdinMu.Lock()
		defer h.stdinMu.Unlock()
		if h.stdinClosed {
			return Ok[any, any](struct{}{})
		}
		if h.pty != nil {
			if _, err := h.pty.Write([]byte{0x04}); err != nil {
				return Err[any, any](ErrIo("Process.closeStdin: " + err.Error()))
			}
			return Ok[any, any](struct{}{})
		}
		h.stdinClosed = true
		if err := h.stdin.Close(); err != nil {
			return Err[any, any](ErrIo("Process.closeStdin: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Subprocess_resize : Int -> Int -> Int -> Task Error ()
func Subprocess_resize(idArg, colsArg, rowsArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		if h.pty == nil {
			return Err[any, any](ErrInvalidInput("Process.resize: the process has no PTY (spawn it withPty)"))
		}
		cols, rows := AsInt(colsArg), AsInt(rowsArg)
		if cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
			return Err[any, any](ErrInvalidInput("Process.resize: cols and rows must be 1 to 65535"))
		}
		if err := h.resize(cols, rows); err != nil {
			return Err[any, any](ErrIo("Process.resize: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Subprocess_kill : Int -> Int -> Task Error ()
func Subprocess_kill(idArg, sigArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		sig, ok := procSignalNumber(AsInt(sigArg))
		if !ok {
			return Err[any, any](ErrInvalidInput("Process.kill: unknown signal"))
		}
		select {
		case <-h.exited:
			return Ok[any, any](struct{}{}) // already gone: nothing to signal
		default:
		}
		if err := procSignalGroup(h.cmd.Process, sig); err != nil {
			return Err[any, any](ErrIo("Process.kill: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Subprocess_wait : Int -> Task Error ExitStatus
func Subprocess_wait(idArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		<-h.exited
		return Ok[any, any](h.exitStatusValue())
	}
}

// Subprocess_pid : Int -> Task Error Int
func Subprocess_pid(idArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		return Ok[any, any](h.pid)
	}
}

// Subprocess_close : Int -> Task Error ()   (idempotent: an unknown id is Ok)
func Subprocess_close(idArg any) any {
	return func() any {
		if v, ok := procRegistry.Load(int64(AsInt(idArg))); ok {
			v.(*procHandle).shutdown()
		}
		return Ok[any, any](struct{}{})
	}
}

// Subprocess_events : Int -> (Event -> msg) -> Sub msg
func Subprocess_events(idArg, toMsg any) SkySub {
	id := int64(AsInt(idArg))
	key := fmt.Sprintf("process:%d", id)
	v, ok := procRegistry.Load(id)
	if !ok {
		// An unknown process delivers nothing; the leaf still reconciles.
		return subT{kind: "subscribeSource", toMsg: toMsg, sourceKey: key, source: deadSource{}}
	}
	return subT{kind: "subscribeSource", toMsg: toMsg, sourceKey: key, source: v.(*procHandle)}
}

// ── subSource ────────────────────────────────────────────────────────────

func (h *procHandle) claimSub() error {
	if !(h.owner.CompareAndSwap(procOwnerNone, procOwnerSub) || h.owner.Load() == procOwnerSub) {
		return fmt.Errorf("process %d is read by a Task (Process.readFrom); a process has one output consumer mode", h.id)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subActive {
		return fmt.Errorf("process %d already has an events Sub", h.id)
	}
	h.subActive = true
	return nil
}

func (h *procHandle) releaseSub() {
	h.mu.Lock()
	h.subActive = false
	h.mu.Unlock()
}

// pump emits Output events in order, then Exited once the child exited and
// its output is drained. Resumes from the cursor a previous Sub left.
func (h *procHandle) pump(stop <-chan struct{}, emit func(ev any) bool) {
	runSourceCycles("process.events", stop, func() bool { return h.pumpCycle(stop, emit) })
}

// pumpCycle emits what is available, or waits for a change. It reports true
// when the pump is finished (Exited delivered, or the runner is stopping).
func (h *procHandle) pumpCycle(stop <-chan struct{}, emit func(ev any) bool) bool {
	h.mu.Lock()
	if h.subExitSent {
		h.mu.Unlock()
		return true
	}
	cursor := h.subCursor
	h.mu.Unlock()
	// Sampled BEFORE the reads: Exited is emitted only after a full pass
	// that started once the output was already drained found nothing.
	drainedAtStart := false
	select {
	case <-h.drained:
		drainedAtStart = true
	default:
	}

	progressed := false
	var waits [2]<-chan struct{}
	for s := procStreamStdout; s <= procStreamStderr; s++ {
		ring := h.out[s]
		ready, changed := ring.wait(cursor[s])
		waits[s] = changed
		if !ready {
			continue
		}
		c := ring.readFrom(cursor[s], maxProcessChunkBytes)
		if len(c.data) == 0 && !c.dropped {
			continue // at EOF with nothing new
		}
		ev := SkyADT{Tag: 0, SkyName: "Output", Fields: []any{streamValue(s), chunkValue(c)}}
		if !emit(ev) {
			return true
		}
		h.mu.Lock()
		h.subCursor[s] = c.next
		h.mu.Unlock()
		progressed = true
	}
	if progressed {
		return false
	}
	if drainedAtStart {
		// Exited and drained, and this pass read nothing new.
		if !emit(SkyADT{Tag: 1, SkyName: "Exited", Fields: []any{h.exitStatusValue()}}) {
			return true
		}
		h.mu.Lock()
		h.subExitSent = true
		h.mu.Unlock()
		return true
	}
	select {
	case <-stop:
		return true
	case <-waits[0]:
	case <-waits[1]:
	case <-h.drained:
	}
	return false
}

// deadSource is the source of a Sub on an unknown (closed) handle: it claims
// fine, emits nothing and ends at once, so a Sky.Cli program is not kept
// alive by a subscription that can never deliver.
type deadSource struct{}

func (deadSource) claimSub() error                                   { return nil }
func (deadSource) releaseSub()                                       {}
func (deadSource) pump(stop <-chan struct{}, emit func(ev any) bool) {}

// procLiveHandles lists live handle ids, for tests.
func procLiveHandles() []int64 {
	var ids []int64
	procRegistry.Range(func(k, _ any) bool {
		ids = append(ids, k.(int64))
		return true
	})
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
