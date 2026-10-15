//go:build !js

package rt

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
)

// TestProcessPty_ReleaseClosesTheSlaveWhenADrainCyclePanics — after the child
// exits, releasePtySlave polls the slave's queued output (TIOCOUTQ) until the
// master has read it, then closes the slave. That poll runs on the reaper
// goroutine, which nobody joins. A panic in one poll cycle must be recovered
// and reported for that cycle (periodic.Guard), and the slave must still be
// closed: a leaked slave keeps the child's terminal open and the master never
// reaches EOF.
//
// Two shapes: the probe panics on the first cycle, and the probe reports
// queued bytes once and then panics.
func TestProcessPty_ReleaseClosesTheSlaveWhenADrainCyclePanics(t *testing.T) {
	cases := []struct {
		name      string
		queuedFor int32 // cycles that report queued bytes before the panic
	}{
		{"panics on the first cycle", 0},
		{"panics after one queued cycle", 1},
	}
	reportAssertions(t, 2*len(cases))
	for _, tc := range cases {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer w.Close()
		var cycles atomic.Int32
		h := &procHandle{
			ptySlave: r,
			closedCh: make(chan struct{}),
			ptyQueued: func(*os.File) int {
				if cycles.Add(1) > tc.queuedFor {
					panic("injected drain-cycle panic")
				}
				return 1
			},
		}

		escaped := func() (v any) {
			defer func() { v = recover() }()
			h.releasePtySlave()
			return nil
		}()
		if escaped != nil {
			t.Errorf("%s: the panic escaped releasePtySlave (%v); on the reaper goroutine "+
				"it would end the process, and the slave stays open", tc.name, escaped)
		}
		// The slave is closed: a second Close reports os.ErrClosed.
		if cerr := r.Close(); !errors.Is(cerr, os.ErrClosed) {
			t.Errorf("%s: the PTY slave was not closed after the drain cycle panicked "+
				"(Close returned %v, want os.ErrClosed)", tc.name, cerr)
		}
	}
}
