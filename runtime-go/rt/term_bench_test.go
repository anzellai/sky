package rt

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wire cost of the terminal, measured on the four workloads of
// docs/perf/runs/terminal-20260928 (yes, seq, redraw, colour; see
// workloads.mjs there). For each, the output is cut into 64 KiB reads (the
// most one Process.readWithin returns) and sent three ways:
//
//	bytes    the previous protocol: each read as a base64 "output" command
//	frames   a screen-diff frame after each read, as JSON (what ships)
//	binary   the same frames in a compact binary encoding, base64 for SSE
//
// each as the SSE island message data, raw and through one gzip stream
// flushed after every message (what a compressing proxy does to SSE).
//
// With TERM_BENCH_DIR set to a directory holding <name>.bin (node
// workloads.mjs <dir>), it reads those files, writes <name>.frames.json for
// the browser bench (bench-widget.mjs) and prints the table. Without it, it
// runs the same pipeline on small built-in workloads. Either way it asserts
// what the design relies on: the frames reproduce the screen, and a flood's
// frames are far smaller than its bytes.

type wireTally struct{ raw, gz int }

type sseGzip struct {
	buf bytes.Buffer
	zw  *gzip.Writer
	raw int
}

func newSSEGzip() *sseGzip {
	g := &sseGzip{}
	g.zw, _ = gzip.NewWriterLevel(&g.buf, gzip.DefaultCompression)
	return g
}

// msg writes one SSE island event and flushes the gzip stream.
func (g *sseGzip) msg(data string) {
	ev := "event: island\ndata: " + data + "\n\n"
	g.raw += len(ev)
	_, _ = g.zw.Write([]byte(ev))
	_ = g.zw.Flush()
}

func (g *sseGzip) tally() wireTally { return wireTally{raw: g.raw, gz: g.buf.Len()} }

// binFrame is a compact binary encoding of a frame, to measure what binary
// frames would save: varints, zigzag colours, a tag byte per op, strings
// as length + UTF-8.
func binFrame(f map[string]any) []byte {
	var b []byte
	uv := func(v int) { b = binary.AppendUvarint(b, uint64(v)) }
	sv := func(v int) { b = binary.AppendVarint(b, int64(v)) }
	str := func(s string) { uv(len(s)); b = append(b, s...) }
	var val func(v any)
	val = func(v any) {
		switch x := v.(type) {
		case int:
			sv(x)
		case string:
			b = append(b, 's')
			str(x)
		case []any:
			b = append(b, 'l')
			uv(len(x))
			for _, e := range x {
				val(e)
			}
		}
	}
	sv(int(f["seq"].(int64)))
	sv(int(f["base"].(int64)))
	st := f["st"].([]any)
	uv(len(st))
	for _, s := range st {
		t := s.([]any)
		sv(t[0].(int))
		sv(t[1].(int))
		b = append(b, byte(t[2].(int)))
	}
	ops := f["ops"].([]any)
	uv(len(ops))
	for _, o := range ops {
		op := o.([]any)
		b = append(b, op[0].(string)[0])
		uv(len(op) - 1)
		for _, e := range op[1:] {
			val(e)
		}
	}
	return b
}

type benchWorkload struct {
	name string
	in   []byte
}

func builtInWorkloads() []benchWorkload {
	var seq strings.Builder
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&seq, "%d\r\n", i)
	}
	var colour strings.Builder
	for f := 0; f < 5; f++ {
		colour.WriteString("\x1b[H")
		for y := 0; y < 40; y++ {
			for x := 0; x < 120; x++ {
				fmt.Fprintf(&colour, "\x1b[38;5;%d;48;5;%dm%c", (x+y+f)%256, (x*y+f*7)%256, 'a'+rune((x+y*3+f)%26))
			}
			colour.WriteString("\x1b[0m")
			if y < 39 {
				colour.WriteString("\r\n")
			}
		}
	}
	return []benchWorkload{
		{"yes", []byte(strings.Repeat("y\r\n", 200000))},
		{"seq", []byte(seq.String())},
		{"colour", []byte(colour.String())},
	}
}

func TestTerminalWireCost(t *testing.T) {
	dir := os.Getenv("TERM_BENCH_DIR")
	works := builtInWorkloads()
	if dir != "" {
		works = nil
		for _, n := range []string{"yes", "seq", "redraw", "colour"} {
			b, err := os.ReadFile(filepath.Join(dir, n+".bin"))
			if err != nil {
				t.Fatalf("TERM_BENCH_DIR is set but %v", err)
			}
			works = append(works, benchWorkload{n, b})
		}
	}
	const chunk = maxProcessChunkBytes
	for _, wk := range works {
		old, fr, bin := newSSEGzip(), newSSEGzip(), newSSEGzip()
		s := newVTScreen(120, 40)
		sh := &termShadow{}
		cl := &termClient{}
		var frames []map[string]any
		emit := func(full bool) {
			f, ok := s.frame(sh, full)
			if !ok {
				return
			}
			j, _ := json.Marshal(f)
			var round map[string]any
			_ = json.Unmarshal(j, &round)
			frames = append(frames, round)
			data, _ := json.Marshal(islandCmd{ID: "t", Name: "frame", Payload: j})
			fr.msg(string(data))
			bf := f
			bf["seq"], bf["base"] = int64(f["seq"].(int64)), int64(f["base"].(int64))
			bdata, _ := json.Marshal(map[string]any{"id": "t", "name": "frame", "payload": base64.StdEncoding.EncodeToString(binFrame(bf))})
			bin.msg(string(bdata))
			if err := cl.apply(round); err != nil {
				t.Fatalf("%s: %v", wk.name, err)
			}
		}
		emit(true)
		for off := 0; off < len(wk.in); off += chunk {
			part := wk.in[off:min(off+chunk, len(wk.in))]
			payload, _ := json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(part), "from": off, "next": off + len(part), "dropped": false})
			data, _ := json.Marshal(islandCmd{ID: "t", Name: "output", Payload: payload})
			old.msg(string(data))
			s.feed(part)
			emit(false)
		}
		if !cl.matches(t, s, wk.name) {
			continue
		}
		o, f, b := old.tally(), fr.tally(), bin.tally()
		t.Logf("%-7s input %9d B | bytes (base64) raw %9d gz %8d | frames (JSON) raw %8d gz %7d | binary+base64 raw %8d gz %7d | frames %d",
			wk.name, len(wk.in), o.raw, o.gz, f.raw, f.gz, b.raw, b.gz, len(frames))
		if wk.name == "yes" || wk.name == "seq" {
			if f.raw*5 > o.raw {
				t.Errorf("%s: a scrolling flood must cost far less as frames (%d B) than as bytes (%d B)", wk.name, f.raw, o.raw)
			}
		}
		if dir != "" {
			js, _ := json.Marshal(frames)
			if err := os.WriteFile(filepath.Join(dir, wk.name+".frames.json"), js, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// BenchmarkVTScreenFeed is the server's cost of the emulation: the screen
// consuming each built-in workload (reported as MB/s of terminal output).
func BenchmarkVTScreenFeed(b *testing.B) {
	for _, wk := range builtInWorkloads() {
		b.Run(wk.name, func(b *testing.B) {
			b.SetBytes(int64(len(wk.in)))
			for i := 0; i < b.N; i++ {
				s := newVTScreen(120, 40)
				for off := 0; off < len(wk.in); off += 32 << 10 {
					s.feed(wk.in[off:min(off+32<<10, len(wk.in))])
				}
			}
		})
	}
}
