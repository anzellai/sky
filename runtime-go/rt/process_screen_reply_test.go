//go:build !js

package rt

import (
	"runtime"
	"testing"
	"time"
)

// TestTerminalReplies_BoundedWhileStdinIsBlocked is the D-6 regression. Each
// terminal reply (a device answer the emulator owes the program, ESC[6n and
// the like) started its own goroutine that waited on stdinMu. A program that
// asks and never reads its input fills the PTY; on Linux a master write then
// blocks with stdinMu held, and every later pump read added one more blocked
// goroutine, without bound. The test holds stdinMu the way a blocked write
// does (portable: macOS discards a full tty's input instead of blocking) and
// sends a thousand replies.
func TestTerminalReplies_BoundedWhileStdinIsBlocked(t *testing.T) {
	h := &procHandle{exited: make(chan struct{})}
	sc := &procScreen{h: h}
	h.stdinMu.Lock() // a write blocked on a full PTY holds this
	before := runtime.NumGoroutine()
	for i := 0; i < 1000; i++ {
		sc.reply([]byte("\x1b[1;1R"))
	}
	time.Sleep(50 * time.Millisecond)
	grew := runtime.NumGoroutine() - before
	h.stdinMu.Unlock()
	close(h.exited)
	if grew > 2 {
		t.Fatalf("1000 replies behind a blocked stdin left %d goroutines, want at most 2", grew)
	}
}
