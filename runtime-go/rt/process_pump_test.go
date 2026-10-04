//go:build unix

package rt

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// A single read from the PTY can return more bytes than the output ring
// holds: Linux n_tty keeps copying while the slave writes, so a pump slowed
// down (by the race detector, or a loaded machine) reads 4096 + k bytes at
// once into its 32 KiB buffer. The ring then keeps only the last `limit`
// bytes of that write, and the screen, which reads the ring after the
// write, lost the first k bytes for good: CI saw the scrollback skip 18
// lines (126 bytes) in TestTerminalScreenIsExactWhenTheRingOverflows. The
// screen must see every byte the process wrote, whatever the read size.
func TestPumpFeedsTheScreenEveryByteOfAReadLargerThanTheRing(t *testing.T) {
	h := &procHandle{drained: make(chan struct{}), ptyCols: 30, ptyRows: 8}
	h.out[procStreamStdout] = newOutRing(4096)
	h.out[procStreamStderr] = newOutRing(4096)
	sc := h.screen()

	var out strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&out, "n%d\r\n", i)
	}
	// bytes.Reader answers one Read with as much as the buffer takes: the
	// first read is 32 KiB, eight times the ring.
	h.pumpStream(bytes.NewReader([]byte(out.String())), h.out[procStreamStdout], true)

	ref := newVTScreen(30, 8)
	ref.feedString(out.String())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	eqT(t, "the screen", sc.vt.text(), ref.text())
	eqT(t, "the scrollback", sc.vt.scrollbackText(), ref.scrollbackText())
	if _, end, eof := h.out[procStreamStdout].snapshot(); end != int64(out.Len()) || !eof {
		t.Fatalf("the ring must count every byte and reach eof: end=%d eof=%v, want %d", end, eof, out.Len())
	}
}
