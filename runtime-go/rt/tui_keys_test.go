//go:build !js

package rt

import (
	"io"
	"strings"
	"testing"
	"time"
)

func feedAll(d *tuiKeyDecoder, chunks ...string) []keyEvent {
	var out []keyEvent
	for _, c := range chunks {
		out = append(out, d.feed([]byte(c))...)
	}
	return out
}

// An arrow key split across two reads is ONE Up key, not Escape + '[' +
// 'A' (T14).
func TestKeyDecoder_SplitEscapeSequence(t *testing.T) {
	d := &tuiKeyDecoder{}
	evs := feedAll(d, "\x1b[", "A")
	if len(evs) != 1 || evs[0].kind != "up" {
		t.Fatalf("split ESC [ A decoded as %+v, want one up key", evs)
	}
	evs = feedAll(d, "\x1b", "[1;5", "C")
	if len(evs) != 1 || evs[0].kind != "right" || !evs[0].ctrl {
		t.Fatalf("split CSI 1;5C decoded as %+v, want ctrl+right", evs)
	}
}

// A multi-byte UTF-8 rune split across reads is kept whole (T14).
func TestKeyDecoder_SplitUTF8(t *testing.T) {
	d := &tuiKeyDecoder{}
	e := "é" // 0xC3 0xA9
	evs := feedAll(d, "a"+e[:1], e[1:]+"b")
	got := []string{}
	for _, ev := range evs {
		got = append(got, ev.kind+":"+ev.value)
	}
	if strings.Join(got, ",") != "char:a,char:é,char:b" {
		t.Fatalf("split UTF-8 decoded as %v", got)
	}
	// A 4-byte rune split three ways.
	g := "😀"
	evs = feedAll(d, g[:1], g[1:3], g[3:])
	if len(evs) != 1 || evs[0].value != g {
		t.Fatalf("split 4-byte rune decoded as %+v", evs)
	}
}

// The bracketed-paste end marker split across reads ends the paste; the
// keys after it are decoded normally (T14 — pre-fix the paste never
// ended and every later key, Ctrl-C included, was swallowed).
func TestKeyDecoder_SplitPasteEndMarker(t *testing.T) {
	d := &tuiKeyDecoder{}
	evs := feedAll(d, "\x1b[200~hello\r\nwor", "ld\x1b[20", "1~", "\x03")
	if len(evs) != 2 {
		t.Fatalf("got %d events %+v, want paste + ctrl-c", len(evs), evs)
	}
	if evs[0].kind != "paste" || evs[0].value != "hello\nworld" {
		t.Fatalf("paste = %+v, want \"hello\\nworld\"", evs[0])
	}
	if evs[1].kind != "ctrl" || evs[1].value != "c" {
		t.Fatalf("key after paste = %+v, want ctrl-c", evs[1])
	}
}

// A lone ESC is held until the timeout flush, then it is the Escape key.
// ESC + a key is Alt+key (T18).
func TestKeyDecoder_LoneEscapeAndAlt(t *testing.T) {
	d := &tuiKeyDecoder{}
	if evs := d.feed([]byte{0x1b}); len(evs) != 0 {
		t.Fatalf("lone ESC decoded before the timeout: %+v", evs)
	}
	if !d.waiting() {
		t.Fatalf("decoder not waiting on a held ESC")
	}
	evs := d.flush()
	if len(evs) != 1 || evs[0].kind != "escape" {
		t.Fatalf("flushed ESC = %+v, want escape", evs)
	}
	evs = d.feed([]byte("\x1bx"))
	if len(evs) != 1 || evs[0].kind != "char" || evs[0].value != "x" || !evs[0].alt {
		t.Fatalf("ESC x = %+v, want alt+x", evs)
	}
	evs = d.feed([]byte("\x1b\x7f"))
	if len(evs) != 1 || evs[0].kind != "backspace" || !evs[0].alt {
		t.Fatalf("ESC DEL = %+v, want alt+backspace", evs)
	}
	// ESC [ alone, then the timeout: Alt+'['.
	d.feed([]byte("\x1b["))
	evs = d.flush()
	if len(evs) != 1 || evs[0].value != "[" || !evs[0].alt {
		t.Fatalf("flushed ESC [ = %+v, want alt+[", evs)
	}
}

// The reader flushes a held ESC after the short timeout even though no
// further byte arrives.
func TestKeyReader_EscapeTimeoutFlush(t *testing.T) {
	pr, pw := io.Pipe()
	got := make(chan keyEvent, 4)
	go tuiRunKeyReader(pr, func(ev keyEvent) bool { got <- ev; return true })
	if _, err := pw.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.kind != "escape" {
			t.Fatalf("got %+v, want escape", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("lone ESC never flushed")
	}
	pw.Close()
}
