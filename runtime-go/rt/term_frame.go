package rt

// term_frame.go — the screen-diff frames the "sky-terminal" widget draws.
//
// A termShadow is the server's copy of what one widget shows. frame diffs
// the screen (term_screen.go) against it, repeats the journalled scrolls
// and resizes first, and returns the ops that bring the widget to the
// screen; applying them to the shadow keeps the two equal. The widget
// applies the same ops the same way (island_terminal.go), so it never
// parses terminal output itself.
//
// The frame is the payload of the widget's "frame" command:
//
//	{ "seq": Int,          this frame's number (per widget, from 1)
//	  "base": Int,         the seq it applies on top of; -1 for a repaint
//	  "st": [[fg, bg, fl]] the styles the runs below index
//	  "ops": [op] }
//
// A colour is -1 (the default), 0-255 (the xterm palette) or 256 + 0xRRGGBB
// (a true colour). fl is bold 1, dim 2, italic 4, underline 8, inverse 16,
// strike 32, wide 64 (each character of the run is two cells), cluster 128
// (the whole text is one cell: a character and its combining marks).
//
// A line is [style, text, style, text, ...] from column 0: each character
// of a text is one cell (two when the style is wide), a space is a blank
// cell. The ops, applied in order:
//
//	["z", cols, rows]        resize: every row is blank at the new size
//	["x"]                    clear the scrollback
//	["p", line, ...]         append lines to the scrollback (oldest first)
//	["u", top, bottom, n, p] scroll rows top..bottom up by n; blank rows enter
//	                         at the bottom. p: 0 the rows that leave are lost,
//	                         1 they go to the scrollback, or [line, ...] these
//	                         lines go to the scrollback instead
//	["d", top, bottom, n]    scroll rows top..bottom down by n
//	["r", y, x, style, text, ...]  row y from column x: the runs, then blank
//	                         cells to the end of the row
//	["c", x, y, visible]     the cursor (visible 0 or 1)
//	["t", title]             the window title (OSC 0 / 2)
//	["m", modes]             1 cursor keys send ESC O, 2 bracketed paste,
//	                         4 the alternate screen shows
//	["b", n]                 the bell rang n times
//
// The scrollback holds at most vtScrollbackMax lines at both ends.

import "unicode/utf8"

// termShadow is what one widget shows, as far as the server knows.
type termShadow struct {
	gen      int64
	seq      int64
	pos      int64 // the journal position it has replayed to
	valid    bool  // it has had a repaint
	cols     int
	rows     int
	lines    [][]vtCell
	cx, cy   int
	cursorOn bool
	title    string
	modes    int
	bells    int
	version  int64 // the screen version the shadow reflects
}

type termStyles struct {
	idx  map[uint64]int
	list []any
}

func newTermStyles() *termStyles { return &termStyles{idx: map[uint64]int{}, list: []any{}} }

func wireColour(c uint32) int {
	switch {
	case c == 0:
		return -1
	case c&vtRGB != 0:
		return 256 + int(c&0xFFFFFF)
	default:
		return int(c & 0xFF)
	}
}

// of is the index of the style of cell c with the geometry bits kind.
func (t *termStyles) of(c vtCell, kind uint8) int {
	fl := (c.fl & vtStyleMask) | kind
	key := uint64(c.fg)<<34 | uint64(c.bg)<<8 | uint64(fl)
	if i, ok := t.idx[key]; ok {
		return i
	}
	i := len(t.list)
	t.idx[key] = i
	t.list = append(t.list, []any{wireColour(c.fg), wireColour(c.bg), int(fl)})
	return i
}

const termCluster uint8 = 128

// runs encodes line[x0:] as [style, text, ...], without the trailing blanks.
func (t *termStyles) runs(out []any, line []vtCell, x0 int) []any {
	end := len(line)
	for end > x0 && line[end-1].blank() {
		end--
	}
	var buf []byte
	cur := -1
	flush := func() {
		if cur >= 0 {
			out = append(out, cur, string(buf))
			buf = buf[:0]
			cur = -1
		}
	}
	for x := x0; x < end; x++ {
		c := line[x]
		if c.fl&vtTail != 0 {
			if x > x0 && line[x-1].fl&vtWide != 0 {
				continue
			}
			c = vtCell{fg: c.fg, bg: c.bg, fl: c.fl & vtStyleMask} // a tail without its head: a blank
		}
		var kind uint8
		if c.fl&vtWide != 0 && x+1 < len(line) && line[x+1].fl&vtTail != 0 {
			kind = vtWide
		}
		r := c.r
		if r == 0 {
			r = ' '
		}
		if c.comb != "" {
			flush()
			out = append(out, t.of(c, kind|termCluster), string(r)+c.comb)
			continue
		}
		if i := t.of(c, kind); i != cur {
			flush()
			cur = i
		}
		buf = utf8.AppendRune(buf, r)
	}
	flush()
	return out
}

func (t *termStyles) line(l []vtCell) []any { return t.runs([]any{}, l, 0) }

func sameCells(a, b []vtCell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneLine(l []vtCell) []vtCell { return append([]vtCell(nil), l...) }

func blankLines(cols, rows int) [][]vtCell {
	out := make([][]vtCell, rows)
	for y := range out {
		out[y] = make([]vtCell, cols)
	}
	return out
}

// replay repeats one journal entry on the shadow and returns its op.
func (sh *termShadow) replay(ev vtEvent, st *termStyles) []any {
	switch ev.kind {
	case 'u':
		top, bottom := ev.top, min(ev.bottom, sh.rows-1)
		if top > bottom {
			return nil
		}
		h := bottom - top + 1
		k := min(ev.n, h)
		var p any = 0
		if ev.push {
			pushed := ev.pushed
			if len(pushed) > vtScrollbackMax {
				pushed = pushed[len(pushed)-vtScrollbackMax:]
			}
			own := ev.n <= h && len(pushed) == ev.n
			for i := 0; own && i < ev.n; i++ {
				own = sameCells(trimmed(sh.lines[top+i]), pushed[i])
			}
			if own {
				p = 1
			} else {
				lines := make([]any, len(pushed))
				for i, l := range pushed {
					lines[i] = st.line(l)
				}
				p = lines
			}
		}
		gone := append([][]vtCell(nil), sh.lines[top:top+k]...)
		copy(sh.lines[top:], sh.lines[top+k:bottom+1])
		for i, l := range gone {
			clear(l)
			sh.lines[bottom-k+1+i] = l
		}
		return []any{"u", top, bottom, ev.n, p}
	case 'd':
		top, bottom := ev.top, min(ev.bottom, sh.rows-1)
		if top > bottom {
			return nil
		}
		k := min(ev.n, bottom-top+1)
		gone := append([][]vtCell(nil), sh.lines[bottom-k+1:bottom+1]...)
		copy(sh.lines[top+k:bottom+1], sh.lines[top:bottom-k+1])
		for i, l := range gone {
			clear(l)
			sh.lines[top+i] = l
		}
		return []any{"d", top, bottom, ev.n}
	case 'z':
		sh.cols, sh.rows = ev.cols, ev.rows
		sh.lines = blankLines(ev.cols, ev.rows)
		return []any{"z", ev.cols, ev.rows}
	case 'x':
		return []any{"x"}
	}
	return nil
}

func (s *vtScreen) modes() int {
	m := 0
	if s.appCursor {
		m |= 1
	}
	if s.bracketed {
		m |= 2
	}
	if s.altOn {
		m |= 4
	}
	return m
}

// frame brings sh to the screen and returns the frame, or false when there
// is nothing to send. full (or a shadow that cannot be brought up to date
// from the journal) makes a repaint: size, scrollback, every row.
func (s *vtScreen) frame(sh *termShadow, full bool) (map[string]any, bool) {
	st := newTermStyles()
	ops := []any{}
	if full || !sh.valid || sh.pos < s.jrBase || sh.pos > s.journalEnd() {
		full = true
		ops = append(ops, []any{"z", s.cols, s.rows}, []any{"x"})
		if sb := s.scrollback(); len(sb) > 0 {
			p := make([]any, 0, len(sb)+1)
			p = append(p, "p")
			for _, l := range sb {
				p = append(p, st.line(l))
			}
			ops = append(ops, p)
		}
		sh.cols, sh.rows = s.cols, s.rows
		sh.lines = blankLines(s.cols, s.rows)
		sh.bells = s.bells // a repaint does not ring
	} else {
		for _, ev := range s.jr[sh.pos-s.jrBase:] {
			if op := sh.replay(ev, st); op != nil {
				ops = append(ops, op)
			}
			if ev.kind == 'z' && len(ev.pushed) > 0 {
				p := make([]any, 0, len(ev.pushed)+1)
				p = append(p, "p")
				for _, l := range ev.pushed {
					p = append(p, st.line(l))
				}
				ops = append(ops, p)
			}
		}
	}
	sh.pos = s.journalEnd()
	s.jrSeal = sh.pos
	if sh.cols != s.cols || sh.rows != s.rows {
		// Cannot happen (every resize is journalled); repaint the grid anyway.
		ops = append(ops, []any{"z", s.cols, s.rows})
		sh.cols, sh.rows = s.cols, s.rows
		sh.lines = blankLines(s.cols, s.rows)
	}
	for y := 0; y < s.rows; y++ {
		a, b := sh.lines[y], s.lines[y]
		x0 := -1
		for x := range b {
			if a[x] != b[x] {
				x0 = x
				break
			}
		}
		if x0 < 0 {
			continue
		}
		if x0 > 0 && (a[x0].fl&vtTail != 0 || b[x0].fl&vtTail != 0) {
			x0--
		}
		ops = append(ops, st.runs([]any{"r", y, x0}, b, x0))
		sh.lines[y] = cloneLine(b)
	}
	if full || sh.cx != s.cx || sh.cy != s.cy || sh.cursorOn != s.cursorOn {
		on := 0
		if s.cursorOn {
			on = 1
		}
		ops = append(ops, []any{"c", s.cx, s.cy, on})
		sh.cx, sh.cy, sh.cursorOn = s.cx, s.cy, s.cursorOn
	}
	if full || sh.title != s.title {
		ops = append(ops, []any{"t", s.title})
		sh.title = s.title
	}
	if m := s.modes(); full || sh.modes != m {
		ops = append(ops, []any{"m", m})
		sh.modes = m
	}
	if s.bells != sh.bells {
		ops = append(ops, []any{"b", s.bells - sh.bells})
		sh.bells = s.bells
	}
	sh.version = s.version
	if !full && len(ops) == 0 {
		return nil, false
	}
	base := sh.seq
	if full {
		base = -1
	}
	sh.seq++
	sh.valid = true
	return map[string]any{"seq": sh.seq, "base": base, "st": st.list, "ops": ops}, true
}

// checkFrame is a frame with no ops that confirms the widget has sh.seq: a
// widget that missed the last frame (a command dropped on the way) sees the
// gap and asks for a repaint.
func (sh *termShadow) checkFrame() map[string]any {
	return map[string]any{"seq": sh.seq, "base": sh.seq, "st": []any{}, "ops": []any{}}
}
