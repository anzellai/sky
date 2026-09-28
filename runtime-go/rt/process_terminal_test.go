//go:build unix

package rt

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// The scrollback replay Std.Ui.Terminal relies on: a remounted widget holds
// nothing, so the terminal reads the PTY output again from offset 0 and sends
// it as base64 "output" commands. Asserted on a real shell on a PTY:
//
//   - the live reads and the replay from 0 return the same bytes, so the
//     repainted screen is the screen the user saw (the `hi` line is there
//     once);
//   - the replay reads offsets the widget can dedupe: a chunk starts at the
//     offset asked for and `next` is where the following read starts;
//   - the base64 payload round-trips the bytes, a UTF-8 character included;
//   - when the ring has overwritten the start, the replay reports `dropped`
//     and starts at the oldest byte it still holds (the widget paints the
//     tail).
func TestTerminalScrollbackReplay(t *testing.T) {
	c := procCmd{program: "/bin/sh", env: [][2]string{{"PS1", "$ "}}, pty: true, cols: 80, rows: 24}
	id := spawnT(t, c)
	procOk(t, procTask(t, Subprocess_write(id, "echo hi; printf 'caf\\303\\251\\n'\n")))

	var live strings.Builder
	off := 0
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(live.String(), "café\r\n") {
		if time.Now().After(deadline) {
			t.Fatalf("the shell never printed its output; got %q", live.String())
		}
		ch := readChunk(t, id, procStreamStdout, off)
		if ch.from != off {
			t.Fatalf("a live read asked for %d and started at %d", off, ch.from)
		}
		live.WriteString(ch.data)
		off = ch.next
	}
	// Count "hi\r\n", not "\nhi\r\n": the PTY echoes the typed line at once,
	// and the shell may print its prompt after that echo, so the output line
	// can read "$ hi". The echoed input holds "hi;", never "hi\r\n".
	if strings.Count(live.String(), "hi\r\n") != 1 {
		t.Fatalf("the live output holds the hi line %d times: %q", strings.Count(live.String(), "hi\r\n"), live.String())
	}

	// The replay: from 0 to where the live reads got.
	var replay strings.Builder
	var payload []string
	roff := 0
	for roff < off {
		ch := readChunk(t, id, procStreamStdout, roff)
		if ch.dropped || ch.from != roff {
			t.Fatalf("the replay read at %d gave from=%d dropped=%v", roff, ch.from, ch.dropped)
		}
		replay.WriteString(ch.data)
		payload = append(payload, Encoding_base64Encode(ch.data).(string))
		roff = ch.next
	}
	got := replay.String()
	if len(got) < off || got[:off] != live.String() {
		t.Fatalf("the replay differs from what the widget showed:\nlive   %q\nreplay %q", live.String(), got)
	}
	var decoded strings.Builder
	for _, p := range payload {
		b, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			t.Fatalf("an output payload is not base64: %v", err)
		}
		decoded.Write(b)
	}
	if decoded.String() != got {
		t.Fatalf("the base64 payloads do not round-trip the bytes")
	}
	if !strings.Contains(decoded.String(), "caf\xc3\xa9") {
		t.Fatalf("the UTF-8 bytes of é did not survive the payload: %q", decoded.String())
	}
}

func TestTerminalScrollbackReplayAfterTheRingWrapped(t *testing.T) {
	c := shCmd("i=0; while [ $i -lt 200 ]; do echo line$i; i=$((i+1)); done")
	c.pty, c.cols, c.rows = true, 80, 24
	c.ring = 256
	id := spawnT(t, c)
	exitStatus(t, id)
	ch := readChunk(t, id, procStreamStdout, 0)
	if !ch.dropped || ch.from == 0 {
		t.Fatalf("a replay from 0 after the ring wrapped: from=%d dropped=%v", ch.from, ch.dropped)
	}
	var tail strings.Builder
	tail.WriteString(ch.data)
	off := ch.next
	for !ch.eof {
		ch = readChunk(t, id, procStreamStdout, off)
		tail.WriteString(ch.data)
		off = ch.next
	}
	if !strings.HasSuffix(tail.String(), "line199\r\n") || len(tail.String()) > 256 {
		t.Fatalf("the replayed tail is %d bytes ending %q", len(tail.String()), tail.String())
	}
}
