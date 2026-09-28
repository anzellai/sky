//go:build unix

package rt

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Process.screen (process_screen.go) on a real shell on a PTY: the frames
// Std.Ui.Terminal sends its widget, applied to the widget model of
// term_screen_test.go.

type screenRead struct {
	changed bool
	frame   map[string]any
	eof     bool
}

func readScreen(t *testing.T, id int, view string, gen int, full bool, ms int) screenRead {
	t.Helper()
	m := procOk(t, procTask(t, Subprocess_screen(id, view, gen, full, ms))).(map[string]any)
	r := screenRead{changed: m["changed"].(bool), eof: m["eof"].(bool)}
	if f, ok := m["frame"].(JsonValue).raw.(map[string]any); ok {
		r.frame = f
	}
	return r
}

// follow applies frames to cl until the widget's text satisfies ok.
func follow(t *testing.T, id int, view string, gen int, cl *termClient, ok func([]string) bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		text := make([]string, len(cl.grid))
		for y, r := range cl.grid {
			text[y] = cellsText(r)
		}
		if ok(text) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen never showed what was expected; the widget shows %q", text)
		}
		r := readScreen(t, id, view, gen, false, 2000)
		if r.changed {
			if err := cl.apply(r.frame); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func hasLine(want string) func([]string) bool {
	return func(text []string) bool {
		for _, l := range text {
			if strings.TrimRight(l, " ") == want {
				return true
			}
		}
		return false
	}
}

func screenOf(t *testing.T, id int) *vtScreen {
	t.Helper()
	sc := handleOf(t, id).scr.Load()
	if sc == nil {
		t.Fatal("the process has no screen")
	}
	return sc.vt
}

// A shell's output reaches the widget as frames; a remount (a new
// generation) repaints from the screen: the same rows and scrollback, the
// line printed once; the read of the old generation returns at once.
func TestTerminalScreenFollowsAShellAndRepaintsARemount(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 40, rows: 6}
	id := spawnT(t, c)
	cl := &termClient{}
	r := readScreen(t, id, "t", 1, true, 1000)
	if !r.changed || num(jsonRound(r.frame)["base"]) != -1 {
		t.Fatalf("the first read must be a repaint: %+v", r)
	}
	if err := cl.apply(r.frame); err != nil {
		t.Fatal(err)
	}
	procOk(t, procTask(t, Subprocess_write(id, "for i in 1 2 3 4 5 6 7 8; do echo line$i; done; echo hi\n")))
	follow(t, id, "t", 1, cl, hasLine("hi"))
	sc := handleOf(t, id).scr.Load()
	sc.mu.Lock()
	cl.matches(t, sc.vt, "the live widget")
	sc.mu.Unlock()
	if len(cl.sb) == 0 {
		t.Fatal("eight lines on a six-row screen must reach the scrollback")
	}

	// A read of generation 1 in flight ends when generation 2 starts.
	var wg sync.WaitGroup
	wg.Add(1)
	var old screenRead
	start := time.Now()
	go func() {
		defer wg.Done()
		old = readScreen(t, id, "t", 1, false, 15000)
	}()
	time.Sleep(200 * time.Millisecond)
	fresh := &termClient{}
	r = readScreen(t, id, "t", 2, true, 1000)
	wg.Wait()
	if old.changed || time.Since(start) > 5*time.Second {
		t.Fatalf("the old generation's read must return at once with nothing: %+v after %v", old, time.Since(start))
	}
	if err := fresh.apply(r.frame); err != nil {
		t.Fatal(err)
	}
	sc.mu.Lock()
	fresh.matches(t, sc.vt, "the remounted widget")
	sc.mu.Unlock()
	all := append(append([]string{}, textOf(fresh.sb)...), textOf(fresh.grid)...)
	if n := strings.Count(strings.Join(all, "\n")+"\n", "\nhi\n"); n != 1 {
		t.Fatalf("the repaint shows the hi line %d times:\n%s", n, strings.Join(all, "\n"))
	}
	if stale := readScreen(t, id, "t", 1, false, 1000); stale.changed {
		t.Fatal("a read of a retired generation must not produce a frame")
	}
}

func textOf(rows [][]tcCell) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = cellsText(r)
	}
	return out
}

// The screen consumes every byte as the process writes it, so a ring far
// smaller than the output (the reader fell behind by more than the ring)
// still ends with the exact screen and scrollback, and the exit line. The
// byte replay this replaced could only start from the ring's oldest byte:
// its screen and scrollback would be those of the tail alone.
func TestTerminalScreenIsExactWhenTheRingOverflows(t *testing.T) {
	c := shCmd("sleep 0.3; i=1; while [ $i -le 3000 ]; do echo \"n$i\"; i=$((i+1)); done")
	c.pty, c.cols, c.rows = true, 30, 8
	c.ring = 4096
	id := spawnT(t, c)
	if r := readScreen(t, id, "t", 1, true, 0); !r.changed {
		t.Fatal("the first read must be a repaint")
	}
	cl := &termClient{}
	var eof bool
	deadline := time.Now().Add(20 * time.Second)
	for first := true; !eof; first = false {
		if time.Now().After(deadline) {
			t.Fatal("no eof")
		}
		r := readScreen(t, id, "t", 1, first, 5000)
		if r.changed {
			if err := cl.apply(r.frame); err != nil {
				t.Fatal(err)
			}
		}
		eof = r.eof
	}
	if ch := readChunk(t, id, procStreamStdout, 0); !ch.dropped {
		t.Fatal("the test needs the ring to have overwritten the start")
	}
	var want strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&want, "n%d\r\n", i)
	}
	want.WriteString("\r\n[process exited with code 0]\r\n")
	ref := newVTScreen(30, 8)
	ref.feedString(want.String())
	eqT(t, "the screen", textOf(cl.grid), ref.text())
	eqT(t, "the scrollback", textOf(cl.sb), ref.scrollbackText())
	eqT(t, "the exit status", exitStatus(t, id), "ExitCode 0")
}

// After the last frame of a burst, a check frame restates the seq, so a
// widget that lost that frame on the way finds out without new output.
func TestTerminalScreenSendsACheckFrameAfterABurst(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 40, rows: 6}
	id := spawnT(t, c)
	cl := &termClient{}
	r := readScreen(t, id, "t", 1, true, 1000)
	_ = cl.apply(r.frame)
	procOk(t, procTask(t, Subprocess_write(id, "echo x\n")))
	follow(t, id, "t", 1, cl, hasLine("x"))
	// Let the shell print its prompt, then read until the check frame.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no check frame")
		}
		r = readScreen(t, id, "t", 1, false, 3000)
		if !r.changed {
			continue
		}
		f := jsonRound(r.frame)
		if len(f["ops"].([]any)) == 0 {
			eqT(t, "the check frame restates the seq", num(f["base"]), num(f["seq"]))
			if err := cl.apply(r.frame); err != nil {
				t.Fatalf("a widget that has every frame accepts the check: %v", err)
			}
			break
		}
		_ = cl.apply(r.frame)
	}
	if r = readScreen(t, id, "t", 1, false, 1200); r.changed {
		t.Fatalf("one check frame per burst, then nothing: %v", r.frame)
	}
}

// A resize reaches the screen before the process sees it, and the next
// frame carries it.
func TestTerminalScreenFollowsAResize(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 40, rows: 6}
	id := spawnT(t, c)
	cl := &termClient{}
	r := readScreen(t, id, "t", 1, true, 1000)
	_ = cl.apply(r.frame)
	procOk(t, procTask(t, Subprocess_resize(id, 50, 9)))
	procOk(t, procTask(t, Subprocess_write(id, "stty size\n")))
	follow(t, id, "t", 1, cl, hasLine("9 50"))
	eqT(t, "the widget size", []int{cl.cols, cl.rows}, []int{50, 9})
}
