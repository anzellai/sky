//go:build unix

package rt

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// The terminal screen under concurrency: the PTY reader feeding the
// emulator, widget reads building frames and repaints, and resizes, all at
// once. Run these under -race (scripts: go test -race -run TestTerminalRace).
//
// The defect these pin: the first Process.screen made the screen at the PTY
// size it read, while a Process.resize from the widget (Std.Ui.Terminal
// sends the two in one Cmd.batch on attach) found no screen yet and resized
// only the PTY. The screen then stayed at the spawn size (80x24) while the
// program drew for the widget's size: vim's "^[" at column 100 of a 110-column
// PTY wrapped on the 80-column screen and scrolled row 0 away, and a reload
// repainted the same wrong screen.

// ptyWinsizeOf is the kernel's size of the PTY.
func ptyWinsizeOf(t *testing.T, f *os.File) (cols, rows int) {
	t.Helper()
	var ws ptyWinsize
	if err := ptyIoctl(f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); err != nil {
		t.Fatal(err)
	}
	return int(ws.Col), int(ws.Row)
}

func screenSize(sc *procScreen) (int, int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.vt.cols, sc.vt.rows
}

// A resize that arrives while the first screen read is making the screen
// reaches the screen: the screen, the PTY and the program agree on the size.
func TestTerminalRace_ScreenMadeDuringAResizeTakesTheResize(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 80, rows: 24}
	id := spawnT(t, c)
	h := handleOf(t, id)
	done := make(chan SkyResult[any, any], 1)
	termScreenMadeHook = func(hh *procHandle) {
		if hh != h {
			return
		}
		// The widget's resize runs while this read makes the screen.
		go func() { done <- Subprocess_resize(id, 110, 18).(func() any)().(SkyResult[any, any]) }()
		time.Sleep(200 * time.Millisecond)
	}
	t.Cleanup(func() { termScreenMadeHook = nil })
	cl := &termClient{}
	r := readScreen(t, id, "t", 1, true, 1000)
	termScreenMadeHook = nil
	if err := cl.apply(r.frame); err != nil {
		t.Fatal(err)
	}
	procOk(t, <-done)
	sc := h.scr.Load()
	if c, r := screenSize(sc); c != 110 || r != 18 {
		t.Fatalf("the screen is %dx%d after a resize to 110x18 (the PTY is at the new size)", c, r)
	}
	if c, r := ptyWinsizeOf(t, h.pty); c != 110 || r != 18 {
		t.Fatalf("the PTY is %dx%d", c, r)
	}
	procOk(t, procTask(t, Subprocess_write(id, "stty size\n")))
	follow(t, id, "t", 1, cl, hasLine("18 110"))
	eqT(t, "the widget size", []int{cl.cols, cl.rows}, []int{110, 18})
}

// Concurrent resizes end with the screen, the recorded size and the PTY at
// the same size, whatever order they ran in.
func TestTerminalRace_ConcurrentResizesAgree(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 80, rows: 24}
	id := spawnT(t, c)
	h := handleOf(t, id)
	readScreen(t, id, "t", 1, true, 1000)
	sc := h.scr.Load()
	for round := 0; round < 200; round++ {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(cols, rows int) {
				defer wg.Done()
				<-start
				_ = h.resize(cols, rows)
			}(40+11*i+round%7, 10+i+round%5)
		}
		close(start)
		wg.Wait()
		sc1, sr1 := screenSize(sc)
		h.mu.Lock()
		pc, pr := h.ptyCols, h.ptyRows
		h.mu.Unlock()
		kc, kr := ptyWinsizeOf(t, h.pty)
		if sc1 != pc || sr1 != pr || kc != pc || kr != pr {
			t.Fatalf("round %d: screen %dx%d, recorded %dx%d, PTY %dx%d", round, sc1, sr1, pc, pr, kc, kr)
		}
	}
}

// loadTermSession reads a logged PTY session (one JSON object per chunk,
// the bytes Latin-1 in "out"): the output chunks, in order.
func loadTermSession(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r struct {
			Out *string `json:"out"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		if r.Out == nil {
			continue
		}
		chunk := make([]byte, 0, len(*r.Out))
		for _, c := range *r.Out {
			chunk = append(chunk, byte(c))
		}
		out = append(out, chunk)
	}
	return out
}

func sameScreens(a, b *vtScreen) error {
	switch {
	case a.cols != b.cols || a.rows != b.rows:
		return fmt.Errorf("size %dx%d, want %dx%d", a.cols, a.rows, b.cols, b.rows)
	case !reflect.DeepEqual(a.lines, b.lines):
		return fmt.Errorf("rows differ:\n got %q\nwant %q", a.text(), b.text())
	case !reflect.DeepEqual(a.scrollbackText(), b.scrollbackText()):
		return fmt.Errorf("scrollback differs:\n got %q\nwant %q", a.scrollbackText(), b.scrollbackText())
	case a.cx != b.cx || a.cy != b.cy || a.cursorOn != b.cursorOn || a.modes() != b.modes() || a.title != b.title:
		return fmt.Errorf("cursor/modes differ: got %v, want %v",
			[]any{a.cx, a.cy, a.cursorOn, a.modes(), a.title}, []any{b.cx, b.cy, b.cursorOn, b.modes(), b.title})
	}
	return nil
}

// runSessionConcurrently plays chunks into a process handle with no process
// behind it (the pump's path: the ring, then feedScreen), cut at random
// points and with random pauses, while a widget reads frames, another view
// repaints, catch-ups run, and the screen is made and resized to the
// program's size concurrently with the start. It returns the screen, the
// widget model after every frame and a final repaint.
func runSessionConcurrently(t *testing.T, seed int64, chunks [][]byte, cols, rows int) (*vtScreen, *termClient, *termClient) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	h := &procHandle{drained: make(chan struct{}), ptyCols: 80, ptyRows: 24}
	h.out[procStreamStdout] = newOutRing(1 << 20)
	h.out[procStreamStderr] = newOutRing(1 << 20)
	delays := make([]time.Duration, 4)
	for i := range delays {
		delays[i] = time.Duration(rng.Intn(3000)) * time.Microsecond
	}
	var errMu sync.Mutex
	var errs []string
	fail := func(f string, a ...any) {
		errMu.Lock()
		errs = append(errs, fmt.Sprintf(f, a...))
		errMu.Unlock()
	}

	// The spawn size is 80x24; the widget resizes the PTY to the program's
	// size while the first read makes the screen (Terminal.attach batches the
	// two), and the program writes only once it has the new size.
	var startWG sync.WaitGroup
	startWG.Add(2)
	go func() { defer startWG.Done(); time.Sleep(delays[0]); h.screen() }()
	go func() { defer startWG.Done(); time.Sleep(delays[1]); _ = h.resize(cols, rows) }()

	writerDone := make(chan struct{})
	pieces := make([][]byte, 0, 4*len(chunks))
	pauses := make([]time.Duration, 0, cap(pieces))
	for _, c := range chunks {
		for len(c) > 0 {
			n := 1 + rng.Intn(len(c))
			if rng.Intn(3) == 0 {
				n = len(c)
			}
			pieces = append(pieces, c[:n])
			c = c[n:]
			var p time.Duration
			if rng.Intn(4) == 0 {
				p = time.Duration(rng.Intn(2000)) * time.Microsecond
			}
			pauses = append(pauses, p)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(writerDone)
		startWG.Wait()
		ring := h.out[procStreamStdout]
		for i, p := range pieces {
			ring.write(p)
			h.feedScreen()
			if pauses[i] > 0 {
				time.Sleep(pauses[i])
			}
		}
	}()

	// Catch-ups and same-size resizes from other goroutines (a screen read
	// and a widget re-sending its size catch up under the lock too).
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := rand.New(rand.NewSource(seed + 1))
		for {
			select {
			case <-writerDone:
				return
			default:
			}
			if sc := h.scr.Load(); sc != nil {
				if r.Intn(2) == 0 {
					sc.catchUp()
				} else {
					startWG.Wait()
					_ = h.resize(cols, rows)
				}
			}
			time.Sleep(time.Duration(r.Intn(1500)) * time.Microsecond)
		}
	}()

	// Another view repaints now and then (a second tab, a reload).
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := rand.New(rand.NewSource(seed + 2))
		for gen := int64(1); ; gen++ {
			select {
			case <-writerDone:
				return
			default:
			}
			res := h.screen().next("repaint", gen, true, 0).(map[string]any)
			if f, ok := res["frame"].(JsonValue).raw.(map[string]any); ok {
				if err := (&termClient{}).apply(f); err != nil {
					fail("repaint gen %d: %v", gen, err)
					return
				}
			}
			time.Sleep(time.Duration(r.Intn(3000)) * time.Microsecond)
		}
	}()

	// The widget: every frame, applied in order.
	widget := &termClient{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		full := true
		for {
			done := chanClosed(writerDone)
			res := h.screen().next("w", 1, full, 5*time.Millisecond).(map[string]any)
			full = false
			if res["changed"].(bool) {
				if f, ok := res["frame"].(JsonValue).raw.(map[string]any); ok {
					if err := widget.apply(f); err != nil {
						fail("widget: %v", err)
						return
					}
				}
			} else if done {
				return
			}
		}
	}()
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("seed %d: %s", seed, strings.Join(errs, "; "))
	}
	sc := h.screen()
	final := &termClient{}
	res := sc.next("final", 1, true, 0).(map[string]any)
	if err := final.apply(res["frame"].(JsonValue).raw.(map[string]any)); err != nil {
		t.Fatal(err)
	}
	return sc.vt, widget, final
}

// A logged vim session (open a file, type "ihello from vim", Escape, Ctrl-L,
// :q!) fed with random cuts and timings while frames, repaints, catch-ups
// and resizes run concurrently: the screen equals a single-threaded feed of
// the same bytes, and the widget that applied every frame, and a repaint,
// equal the screen. Up to the Ctrl-L the first row is the typed line.
func TestTerminalRace_LoggedSessionMatchesASingleThreadedFeed(t *testing.T) {
	chunks := loadTermSession(t, "testdata/term-vim-escape-session.jsonl")
	const cols, rows = 110, 18
	ctrlL := -1
	for i, c := range chunks {
		if strings.HasPrefix(string(c), "\x1b[?25l\x1b[18;100H^L") {
			ctrlL = i
			break
		}
	}
	if ctrlL < 0 {
		t.Fatal("the session fixture has no Ctrl-L chunk")
	}
	for _, part := range []struct {
		name   string
		chunks [][]byte
	}{{"to the Escape", chunks[:ctrlL]}, {"the whole session", chunks}} {
		ref := newVTScreen(cols, rows)
		for _, c := range part.chunks {
			ref.feed(c)
		}
		if part.name == "to the Escape" {
			if got := ref.text()[0]; got != "hello from vim" {
				t.Fatalf("the reference row 0 after the Escape is %q", got)
			}
		}
		seeds := int64(60)
		if testing.Short() {
			seeds = 15
		}
		for seed := int64(1); seed <= seeds; seed++ {
			vt, widget, final := runSessionConcurrently(t, seed, part.chunks, cols, rows)
			if err := sameScreens(vt, ref); err != nil {
				t.Fatalf("%s, seed %d: the screen differs from a single-threaded feed: %v", part.name, seed, err)
			}
			if !widget.matches(t, vt, fmt.Sprintf("%s, seed %d, the widget", part.name, seed)) {
				t.FailNow()
			}
			if !final.matches(t, vt, fmt.Sprintf("%s, seed %d, a repaint", part.name, seed)) {
				t.FailNow()
			}
		}
	}
}
