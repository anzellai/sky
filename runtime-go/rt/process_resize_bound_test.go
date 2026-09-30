//go:build unix

package rt

import (
	"strings"
	"testing"
)

// TestProcessResize_OneBoundForPtyAndScreen is the A-5 / D-4 regression.
// Process.resize accepted 1 to 65535 and passed it to the PTY, while the
// screen emulator clamped itself to 1000 x 1000: after `resize 65535 65535`
// the program laid out for 65535 columns on a 1000-column screen, and one
// crafted resize from a client (the Terminal widget forwards `resize` events
// unchecked) cost about 100 MB of server heap per terminal. There is now one
// bound, the widget's own (500 x 200): the PTY, the screen and the spawn all
// use it, and a size above it is refused.
func TestProcessResize_OneBoundForPtyAndScreen(t *testing.T) {
	c := shCmd("sleep 30")
	c.pty, c.cols, c.rows = true, 80, 24
	id := spawnT(t, c)
	for _, sz := range [][2]int{{65535, 65535}, {1000, 1000}, {procPtyMaxCols + 1, 24}, {80, procPtyMaxRows + 1}} {
		res := procTask(t, Subprocess_resize(id, sz[0], sz[1]))
		if procErrKind(res) != "InvalidInput" {
			t.Errorf("resize %dx%d = %+v, want InvalidInput", sz[0], sz[1], res)
		}
	}
	procOk(t, procTask(t, Subprocess_resize(id, procPtyMaxCols, procPtyMaxRows)))
	h := handleOf(t, id)
	sc := h.screen()
	sc.mu.Lock()
	cols, rows := sc.vt.cols, sc.vt.rows
	sc.mu.Unlock()
	if cols != procPtyMaxCols || rows != procPtyMaxRows {
		t.Errorf("screen is %dx%d after resizing to the bound, want %dx%d", cols, rows, procPtyMaxCols, procPtyMaxRows)
	}
	// The emulator itself never goes past the bound either.
	vt := newVTScreen(65535, 65535)
	if vt.cols > procPtyMaxCols || vt.rows > procPtyMaxRows {
		t.Errorf("a vtScreen accepted %dx%d", vt.cols, vt.rows)
	}
	// And a spawn above it is refused.
	big := shCmd("true")
	big.pty, big.cols, big.rows = true, 4000, 4000
	res := procTask(t, Subprocess_spawn(big.record()))
	if procErrKind(res) != "InvalidInput" || !strings.Contains(errorMessageOf(res.ErrValue), "500") {
		t.Errorf("spawn withPty 4000x4000 = %+v, want InvalidInput naming the bound", res)
	}
	if !strings.Contains(errorMessageOf(res.ErrValue), "docs/migration/v0.27.md#pty-size-bound") {
		t.Errorf("the refusal does not name the migration note: %s", errorMessageOf(res.ErrValue))
	}
}
