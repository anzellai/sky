//go:build !js

// Stateful terminal key decoder shared by Tui.program and Tui.app.
//
// A terminal delivers keys as a byte stream, and a read() boundary can
// fall ANYWHERE in it: inside a multi-byte UTF-8 rune, inside an escape
// sequence (`ESC [` in one read, `A` in the next), or inside the
// bracketed-paste end marker `ESC [ 2 0 1 ~`. A stateless per-read
// decoder turns each of those into garbage — a lost character, an Escape
// followed by `[` and `A`, or a paste that never ends (swallowing every
// later key, Ctrl-C included).
//
// tuiKeyDecoder keeps the undecoded tail between reads:
//
//   - a sequence is decoded only once it is complete (tuiSeqLen);
//   - an incomplete tail is held; if no further byte arrives within
//     tuiEscTimeout the tail is flushed as final input (a lone ESC is the
//     Escape key, ESC+x is Alt+x);
//   - in bracketed-paste mode the raw bytes are collected until the end
//     marker, which is recognised even when split across reads. A paste
//     that stalls for tuiPasteTimeout is flushed so the loop never hangs.

package rt

import (
	"bytes"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	tuiEscTimeout   = 50 * time.Millisecond
	tuiPasteTimeout = 1 * time.Second
	tuiPasteMax     = 1 << 20 // bytes
	tuiReadBufSize  = 4096
)

var tuiPasteEnd = []byte("\x1b[201~")

type tuiKeyDecoder struct {
	pend    []byte
	pasting bool
	paste   []byte
}

// tuiSeqLen returns the byte length of the complete key sequence at the
// start of buf, or 0 when more bytes are needed to decide.
func tuiSeqLen(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	b := buf[0]
	if b == 0x1b {
		if len(buf) == 1 {
			return 0
		}
		switch buf[1] {
		case '[':
			if len(buf) == 2 {
				return 0
			}
			if buf[2] == '<' {
				for i := 3; i < len(buf); i++ {
					if buf[i] == 'M' || buf[i] == 'm' {
						return i + 1
					}
					if buf[i] == 0x1b {
						return i // malformed — cut before the next ESC
					}
				}
				return 0
			}
			for i := 2; i < len(buf); i++ {
				c := buf[i]
				switch {
				case c >= 0x40 && c <= 0x7e:
					return i + 1
				case c >= 0x20 && c <= 0x3f:
					continue
				default:
					return i // malformed CSI — cut before the stray byte
				}
			}
			return 0
		case 'O':
			if len(buf) < 3 {
				return 0
			}
			return 3
		case 0x1b:
			return 1 // ESC ESC — the first is a lone Escape
		}
		// Alt+<key>: ESC followed by one complete key.
		n := tuiSeqLen(buf[1:])
		if n == 0 {
			return 0
		}
		return 1 + n
	}
	if b >= 0x80 {
		if !utf8.FullRune(buf) {
			return 0
		}
		_, size := utf8.DecodeRune(buf)
		return size
	}
	return 1
}

// feed appends data and returns every key that is now complete.
func (d *tuiKeyDecoder) feed(data []byte) []keyEvent {
	d.pend = append(d.pend, data...)
	return d.drain(false)
}

// waiting reports that undecoded bytes are held for more input.
func (d *tuiKeyDecoder) waiting() bool { return d.pasting || len(d.pend) > 0 }

// timeout is how long the reader waits for more bytes before flushing.
func (d *tuiKeyDecoder) timeout() time.Duration {
	if d.pasting {
		return tuiPasteTimeout
	}
	return tuiEscTimeout
}

// flush decodes everything held as final input (no more bytes follow).
func (d *tuiKeyDecoder) flush() []keyEvent {
	return d.drain(true)
}

func (d *tuiKeyDecoder) endPaste() keyEvent {
	body := strings.ReplaceAll(string(d.paste), "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	d.paste = d.paste[:0]
	d.pasting = false
	return keyEvent{kind: "paste", value: body}
}

func (d *tuiKeyDecoder) drain(final bool) []keyEvent {
	var out []keyEvent
	for {
		if d.pasting {
			if idx := bytes.Index(d.pend, tuiPasteEnd); idx >= 0 {
				d.paste = append(d.paste, d.pend[:idx]...)
				d.pend = d.pend[idx+len(tuiPasteEnd):]
				out = append(out, d.endPaste())
				continue
			}
			// Keep the longest suffix that could still grow into the
			// end marker; everything before it is paste content.
			keep := 0
			for k := len(tuiPasteEnd) - 1; k > 0; k-- {
				if k <= len(d.pend) && bytes.HasPrefix(tuiPasteEnd, d.pend[len(d.pend)-k:]) {
					keep = k
					break
				}
			}
			d.paste = append(d.paste, d.pend[:len(d.pend)-keep]...)
			d.pend = d.pend[len(d.pend)-keep:]
			if final || len(d.paste) > tuiPasteMax {
				d.paste = append(d.paste, d.pend...)
				d.pend = d.pend[:0]
				out = append(out, d.endPaste())
				continue
			}
			return out
		}
		if len(d.pend) == 0 {
			return out
		}
		n := tuiSeqLen(d.pend)
		if n == 0 {
			if !final {
				return out
			}
			n = len(d.pend)
		}
		ev, consumed := tuiDecodeKey(d.pend[:n])
		if consumed <= 0 {
			consumed = 1
		}
		d.pend = d.pend[consumed:]
		switch ev.kind {
		case "paste-start":
			d.pasting = true
		case "paste-end":
			// A stray end marker outside a paste carries no key.
		default:
			out = append(out, ev)
		}
	}
}

// tuiRunKeyReader reads raw bytes from r, decodes them with a
// tuiKeyDecoder and calls emit for each key. It returns when r reaches
// EOF / fails (after flushing any held bytes) or when emit returns false.
func tuiRunKeyReader(r io.Reader, emit func(keyEvent) bool) {
	chunks := make(chan []byte, 16)
	safeGo("Tui raw reader", func() {
		buf := make([]byte, tuiReadBufSize)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				c := make([]byte, n)
				copy(c, buf[:n])
				chunks <- c
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	})
	dec := &tuiKeyDecoder{}
	var timer *time.Timer
	var timeout <-chan time.Time
	for {
		var evs []keyEvent
		select {
		case c, ok := <-chunks:
			if !ok {
				for _, ev := range dec.flush() {
					if !emit(ev) {
						return
					}
				}
				return
			}
			evs = dec.feed(c)
		case <-timeout:
			evs = dec.flush()
		}
		for _, ev := range evs {
			if !emit(ev) {
				return
			}
		}
		if timer != nil {
			timer.Stop()
			timer, timeout = nil, nil
		}
		if dec.waiting() {
			timer = time.NewTimer(dec.timeout())
			timeout = timer.C
		}
	}
}
