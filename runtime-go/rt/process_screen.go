//go:build !js

package rt

// process_screen.go — the terminal screen of a process, for Std.Ui.Terminal.
//
// The first Subprocess_screen call on a process gives it a vtScreen
// (term_screen.go) at the PTY's size. From then on the pump that stores the
// process's stdout in its ring also feeds the screen, so the screen keeps up
// with the process whatever the page does: a slow or disconnected client
// only gets fewer, bigger frames, never a wrong screen. (The screen reads
// the ring by offset like any reader; if the screen is made after the ring
// already overwrote the start, the parser restarts where the ring begins.)
//
// Each widget showing the screen has a shadow (term_frame.go), keyed by the
// widget's id. Subprocess_screen waits for a change, then returns the frame
// that brings that widget's shadow to the screen:
//
//   - frames are at least termFrameInterval apart, so a flood of output
//     costs one frame per paint at most (the bytes in between coalesce into
//     one diff), and an interactive echo still goes out at once;
//   - gen is the widget's mount generation (Std.Ui.Terminal counts it): a
//     higher gen starts a new shadow and wakes the read of the old one,
//     which returns at once with nothing, so a remount never races an old
//     read for the shadow;
//   - termCheckAfter after the last frame, with nothing new, the read
//     returns a frame with no ops that restates the seq: a widget whose last
//     frame was lost on the way (a full SSE buffer drops a command) sees the
//     gap and asks for a repaint, instead of showing a stale screen until
//     the next output;
//   - once the process has exited and its output is drained, the screen
//     prints "[process exited with code N]" (or "terminated by signal N"),
//     and the read reports eof after the frame that shows it.

import (
	"strconv"
	"sync"
	"time"
)

const (
	termFrameInterval = 16 * time.Millisecond
	termCheckAfter    = 750 * time.Millisecond
	termShadowsMax    = 8
)

type procScreen struct {
	h       *procHandle
	mu      sync.Mutex
	vt      *vtScreen
	off     int64 // the ring offset fed so far
	changed chan struct{}
	shadows map[string]*termView
	exited  bool // the exit line is on the screen
}

type termView struct {
	sh    termShadow
	last  time.Time // when its last frame was made
	check bool      // a frame went out since the last check frame
	used  time.Time
}

func chanClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// termScreenMadeHook, when set (tests only), runs after a new screen took
// the PTY size and before it is published.
var termScreenMadeHook func(h *procHandle)

// screen returns the process's screen, making it on first use.
//
// It is made under sizeMu, the lock every resize holds: a resize either
// finishes before the screen reads the size, or waits until the screen is
// published and then resizes it. (Without the lock, Terminal.attach's first
// read and its resize, which run at once, could leave the screen at the
// spawn size while the program drew for the widget's size.)
func (h *procHandle) screen() *procScreen {
	if sc := h.scr.Load(); sc != nil {
		return sc
	}
	h.sizeMu.Lock()
	if sc := h.scr.Load(); sc != nil {
		h.sizeMu.Unlock()
		return sc
	}
	h.mu.Lock()
	cols, rows := h.ptyCols, h.ptyRows
	h.mu.Unlock()
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	sc := &procScreen{h: h, vt: newVTScreen(cols, rows), changed: make(chan struct{}), shadows: map[string]*termView{}}
	if termScreenMadeHook != nil {
		termScreenMadeHook(h)
	}
	h.scr.Store(sc)
	h.sizeMu.Unlock()
	sc.catchUp()
	return sc
}

// resize sets the PTY size: the screen's (if it is made), the size a new
// screen takes, and the PTY's window size, as one step under sizeMu, so
// concurrent resizes and the making of the screen cannot leave them apart.
func (h *procHandle) resize(cols, rows int) error {
	h.sizeMu.Lock()
	defer h.sizeMu.Unlock()
	// The screen takes the new size first: the output the process
	// writes after it learns the size is laid out at that size.
	if sc := h.scr.Load(); sc != nil {
		sc.resize(cols, rows)
	}
	h.mu.Lock()
	h.ptyCols, h.ptyRows = cols, rows
	h.mu.Unlock()
	if h.pty == nil {
		return nil
	}
	return procSetWinsize(h.pty, cols, rows)
}

// feedScreen runs the new stdout bytes through the screen, if there is one.
func (h *procHandle) feedScreen() {
	if sc := h.scr.Load(); sc != nil {
		sc.catchUp()
	}
}

func (sc *procScreen) signalLocked() {
	close(sc.changed)
	sc.changed = make(chan struct{})
}

func (sc *procScreen) wake() {
	sc.mu.Lock()
	sc.signalLocked()
	sc.mu.Unlock()
}

func (sc *procScreen) catchUp() {
	sc.mu.Lock()
	replies := sc.catchUpLocked()
	sc.mu.Unlock()
	sc.reply(replies)
}

// catchUpLocked feeds the ring's bytes past sc.off and returns the replies
// the terminal owes the process (device reports).
func (sc *procScreen) catchUpLocked() []byte {
	ring := sc.h.out[procStreamStdout]
	moved := false
	for {
		c := ring.readFrom(sc.off, maxProcessChunkBytes)
		if c.dropped {
			sc.vt.lostBytes()
		}
		sc.off = c.next
		if len(c.data) == 0 {
			break
		}
		sc.vt.feed(c.data)
		moved = true
	}
	if moved {
		sc.signalLocked()
	}
	r := sc.vt.replies
	sc.vt.replies = nil
	return r
}

// reply writes the terminal's answers to the process input. Not under the
// screen lock, and not on the pump: a process that does not read its input
// must not stall its own output.
func (sc *procScreen) reply(b []byte) {
	if len(b) == 0 {
		return
	}
	h := sc.h
	go func() {
		h.stdinMu.Lock()
		defer h.stdinMu.Unlock()
		if h.stdin != nil && !h.stdinClosed {
			_, _ = h.stdin.Write(b)
		}
	}()
}

func (sc *procScreen) resize(cols, rows int) {
	sc.mu.Lock()
	replies := sc.catchUpLocked()
	sc.vt.resize(cols, rows)
	sc.signalLocked()
	sc.mu.Unlock()
	sc.reply(replies)
}

// view returns the shadow of widget id, making it (and dropping the least
// recently used one past termShadowsMax).
func (sc *procScreen) view(id string) *termView {
	v := sc.shadows[id]
	if v == nil {
		if len(sc.shadows) >= termShadowsMax {
			oldest := ""
			for k, o := range sc.shadows {
				if oldest == "" || o.used.Before(sc.shadows[oldest].used) {
					oldest = k
				}
			}
			delete(sc.shadows, oldest)
		}
		v = &termView{}
		sc.shadows[id] = v
	}
	v.used = time.Now()
	return v
}

// trimJournalLocked drops the journal entries every valid shadow replayed.
func (sc *procScreen) trimJournalLocked() {
	pos := sc.vt.journalEnd()
	for _, v := range sc.shadows {
		if v.sh.valid && v.sh.pos < pos {
			pos = v.sh.pos
		}
	}
	sc.vt.dropJournalBefore(pos)
}

func (sc *procScreen) noteExitLocked() {
	h := sc.h
	h.mu.Lock()
	signaled, sig, code := h.signaled, h.exitSig, h.exitCode
	h.mu.Unlock()
	how := "exited with code " + strconv.Itoa(code)
	if signaled {
		how = "terminated by signal " + strconv.Itoa(sig)
	}
	sc.vt.feedString("\r\n[process " + how + "]\r\n")
	sc.exited = true
	sc.signalLocked()
}

func screenResult(changed bool, frame map[string]any, eof bool) any {
	var raw any
	if frame != nil {
		raw = frame
	}
	return map[string]any{"changed": changed, "frame": JsonValue{raw: raw}, "eof": eof}
}

// next is one Subprocess_screen read: the frame for widget `id` at mount
// generation gen (a repaint when full), waiting up to `wait` for a change.
func (sc *procScreen) next(id string, gen int64, full bool, wait time.Duration) any {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	first := true
	for {
		sc.mu.Lock()
		v := sc.view(id)
		if gen < v.sh.gen {
			sc.mu.Unlock()
			return screenResult(false, nil, false)
		}
		if gen > v.sh.gen {
			*v = termView{sh: termShadow{gen: gen}, used: v.used}
			sc.signalLocked() // the read of the old generation returns
		}
		if full && first {
			v.sh.valid = false
		}
		first = false
		if !sc.exited && chanClosed(sc.h.drained) {
			replies := sc.catchUpLocked()
			sc.noteExitLocked()
			sc.reply(replies)
		}
		if !v.sh.valid || v.sh.version != sc.vt.version {
			if since := time.Since(v.last); v.sh.valid && since < termFrameInterval {
				sc.mu.Unlock()
				time.Sleep(termFrameInterval - since)
				continue
			}
			frame, ok := sc.vt.frame(&v.sh, false)
			sc.trimJournalLocked()
			if ok {
				v.last = time.Now()
				v.check = true
				eof := sc.exited
				sc.mu.Unlock()
				return screenResult(true, frame, eof)
			}
		}
		if sc.exited {
			sc.mu.Unlock()
			return screenResult(false, nil, true)
		}
		changed := sc.changed
		var checkC <-chan time.Time
		if v.check {
			d := termCheckAfter - time.Since(v.last)
			if d < 0 {
				d = 0
			}
			checkC = time.After(d)
		}
		sc.mu.Unlock()
		select {
		case <-changed:
		case <-sc.h.drained:
		case <-checkC:
			sc.mu.Lock()
			if cur := sc.shadows[id]; cur == v && v.sh.gen == gen && v.check && v.sh.version == sc.vt.version {
				v.check = false
				frame := v.sh.checkFrame()
				sc.mu.Unlock()
				return screenResult(true, frame, false)
			}
			sc.mu.Unlock()
		case <-timer.C:
			return screenResult(false, nil, false)
		}
	}
}

// Subprocess_screen : Int -> String -> Int -> Bool -> Int -> Task Error Screen
//
// The next frame of the process's terminal screen for the widget `id` at
// mount generation `gen` (a repaint when `full`), waiting up to `ms`
// milliseconds for a change. Sky side: Process.screen.
func Subprocess_screen(idArg, viewArg, genArg, fullArg, msArg any) any {
	return func() any {
		h, e := lookupProc(idArg)
		if e != nil {
			return Err[any, any](e)
		}
		ms := AsInt(msArg)
		if ms < 0 {
			ms = 0
		}
		sc := h.screen()
		return Ok[any, any](sc.next(AsString(viewArg), int64(AsInt(genArg)), AsBool(fullArg), time.Duration(ms)*time.Millisecond))
	}
}
