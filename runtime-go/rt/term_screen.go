package rt

// term_screen.go — the server-side terminal screen behind Std.Ui.Terminal.
//
// A vtScreen is a VT100 / xterm emulator with no I/O: bytes go in with feed,
// and the result is a grid of cells, a bounded scrollback and a journal of
// the scrolls and resizes that happened. process_screen.go feeds it the
// output of a PTY process as the process writes it and turns it into a
// stream of screen-diff frames (termFrame, below) for the widget, which only
// draws (island_terminal.go). The screen is the source of truth, so:
//
//   - a remounted widget (a reload, a navigation back) is repainted from the
//     current screen and the scrollback, not by replaying the output bytes;
//   - a flood of output (`yes`, `cat` of a big file) costs the page one
//     frame per paint at most: the frames between two reads coalesce;
//   - a slow client never corrupts the screen: the emulator consumes every
//     byte at the speed the process writes, whatever the page does.
//
// What it emulates: cursor movement (CUU/CUD/CUF/CUB/CNL/CPL/CHA/HPA/VPA/
// CUP/HVP/HPR/VPR, CHT/CBT), erase (ED 0-3, EL 0-2, ECH), insert and delete
// (ICH/DCH/IL/DL, IRM), scroll regions (DECSTBM) and scrolling (IND/RI/NEL,
// SU/SD), autowrap with the deferred wrap, SGR (bold, dim, italic,
// underline, inverse, strike; 16, 256 and true colours in ; and : forms),
// the alternate screen (?47, ?1047, ?1049), cursor visibility (?25), cursor
// keys (?1) and bracketed paste (?2004) modes, save / restore (DECSC/DECRC,
// CSI s/u), REP, DEC line drawing (ESC ( 0), OSC 0/2 titles, BEL, UTF-8
// (streamed: a character split across two writes decodes), wide characters
// (two cells) and combining marks (joined to the cell before), and the
// device reports DSR 5n/6n and DA (the reply goes back to the process).
// Other sequences are consumed without effect.

import (
	"strconv"
	"unicode"
	"unicode/utf8"
)

// procPtyMaxCols / procPtyMaxRows: the one terminal size bound (A-5 / D-4).
// A PTY (Process.withPty, Process.resize) and the screen emulator never go
// past it; it is the Terminal widget's own limit.
const (
	procPtyMaxCols = 500
	procPtyMaxRows = 200
)

const (
	vtScrollbackMax = 1000 // lines kept above the screen (and sent on a repaint)
	// The largest screen: the same bound Process.resize and withPty enforce
	// and the Terminal widget sends (A-5 / D-4). 500 x 200 cells.
	vtMaxCols      = procPtyMaxCols
	vtMaxRows      = procPtyMaxRows
	vtJournalMax   = 512 // journal entries kept before the oldest are dropped
	vtJournalLines = 4 * vtScrollbackMax
	vtOSCMax       = 4096
)

// Cell flags. vtWide marks the left half of a wide character; vtTail the
// right half, which draws nothing of its own.
const (
	vtBold uint8 = 1 << iota
	vtDim
	vtItalic
	vtUnderline
	vtInverse
	vtStrike
	vtWide
	vtTail
)

// vtStyleMask is the flags a style carries (the rest are cell geometry).
const vtStyleMask = vtBold | vtDim | vtItalic | vtUnderline | vtInverse | vtStrike

// Colours: 0 is the default colour, vtPal|n is palette entry n (0-255),
// vtRGB|0xRRGGBB is a true colour.
const (
	vtPal uint32 = 1 << 24
	vtRGB uint32 = 2 << 24
)

type vtCell struct {
	r    rune   // 0: blank
	comb string // combining marks drawn with r
	fg   uint32
	bg   uint32
	fl   uint8
}

// blank reports a cell that draws nothing and has no background: the cells
// a row op leaves out at the end of a row.
func (c vtCell) blank() bool {
	if c.bg != 0 || c.fl&(vtInverse|vtUnderline|vtStrike|vtWide|vtTail) != 0 {
		return false
	}
	return (c.r == 0 || c.r == ' ') && len(c.comb) == 0
}

// vtEvent is one journal entry: a scroll or a resize the frames must repeat
// on the widget's copy of the screen (row contents are diffed, not
// journalled).
type vtEvent struct {
	kind   byte // 'u' scroll up, 'd' scroll down, 'z' resize, 'x' clear scrollback
	top    int
	bottom int
	n      int
	push   bool       // a scroll up whose lines went to the scrollback
	pushed [][]vtCell // those lines (at least the last vtScrollbackMax), or the lines a resize pushed
	cols   int
	rows   int
}

type vtSaved struct {
	x, y     int
	attr     vtCell
	charset  byte
	autowrap bool
}

type vtScreen struct {
	cols, rows int
	lines      [][]vtCell
	altOn      bool
	mainLines  [][]vtCell // the main screen while the alternate one shows
	mainSaved  vtSaved    // the cursor ?1049 saved
	cx, cy     int
	wrap       bool // the deferred wrap: the next character goes to the next line
	autowrap   bool
	cursorOn   bool
	appCursor  bool
	bracketed  bool
	insert     bool
	top        int
	bottom     int
	attr       vtCell
	saved      vtSaved
	hasSaved   bool
	charset    byte // G0: 'B' ASCII, '0' DEC line drawing
	last       rune // the last printed character (REP)

	sb      [][]vtCell // scrollback, oldest first, at most vtScrollbackMax
	title   string
	bells   int
	version int64 // bumps on every change

	// The journal: events with absolute numbers jrBase, jrBase+1, ...
	jr      []vtEvent
	jrBase  int64
	jrLines int
	jrSeal  int64 // entries before this position were sent: never merged into

	// The parser.
	st     int // 0 ground, 1 ESC, 2 CSI, 3 OSC, 4 string (DCS/SOS/PM/APC), 5 ESC in a string, 6 skip one, 7 charset G0
	params []byte
	priv   byte
	inter  bool
	osc    []byte
	oscESC bool
	u8     [4]byte
	u8n    int
	u8need int

	tmpRows [][]vtCell // scratch for scrollRegionUp
	tmpPush [][]vtCell

	// replies collects what the terminal answers the program (DSR, DA);
	// the owner writes it to the process input.
	replies []byte
}

func newVTScreen(cols, rows int) *vtScreen {
	s := &vtScreen{cols: vtClamp(cols, 1, vtMaxCols), rows: vtClamp(rows, 1, vtMaxRows)}
	s.reset()
	return s
}

func vtClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *vtScreen) blankLine() []vtCell {
	l := make([]vtCell, s.cols)
	if s.attr.bg != 0 {
		for i := range l {
			l[i].bg = s.attr.bg
		}
	}
	return l
}

func (s *vtScreen) clearLine(l []vtCell) {
	if s.attr.bg == 0 {
		clear(l)
		return
	}
	for i := range l {
		l[i] = vtCell{bg: s.attr.bg}
	}
}

// reset is RIS: a clear screen, the default modes. The scrollback stays.
func (s *vtScreen) reset() {
	s.attr = vtCell{}
	s.lines = make([][]vtCell, s.rows)
	for y := range s.lines {
		s.lines[y] = make([]vtCell, s.cols)
	}
	s.altOn = false
	s.mainLines = nil
	s.cx, s.cy = 0, 0
	s.wrap = false
	s.autowrap = true
	s.cursorOn = true
	s.appCursor = false
	s.bracketed = false
	s.insert = false
	s.top, s.bottom = 0, s.rows-1
	s.hasSaved = false
	s.charset = 'B'
	s.version++
}

// ── The journal ──────────────────────────────────────────────────────

func (s *vtScreen) journal(ev vtEvent) {
	if n := len(s.jr); n > 0 && ev.kind == 'u' && s.jrBase+int64(n-1) >= s.jrSeal {
		p := &s.jr[n-1]
		if p.kind == 'u' && p.top == ev.top && p.bottom == ev.bottom && p.push == ev.push {
			p.n += ev.n
			if ev.push {
				s.jrLines -= len(p.pushed)
				p.pushed = append(p.pushed, ev.pushed...)
				if len(p.pushed) > 2*vtScrollbackMax {
					// Amortised: keep up to twice the scrollback, cut to once.
					p.pushed = append([][]vtCell(nil), p.pushed[len(p.pushed)-vtScrollbackMax:]...)
				}
				s.jrLines += len(p.pushed)
			}
			s.trimJournal()
			return
		}
	}
	if len(ev.pushed) > 0 {
		ev.pushed = append([][]vtCell(nil), ev.pushed...)
	} else {
		ev.pushed = nil
	}
	s.jr = append(s.jr, ev)
	s.jrLines += len(ev.pushed)
	s.trimJournal()
}

// trimJournal keeps the journal bounded. A reader whose position falls
// before jrBase gets a full repaint instead.
func (s *vtScreen) trimJournal() {
	for len(s.jr) > 1 && (len(s.jr) > vtJournalMax || s.jrLines > vtJournalLines) {
		s.jrLines -= len(s.jr[0].pushed)
		s.jr[0] = vtEvent{}
		s.jr = s.jr[1:]
		s.jrBase++
	}
}

// dropJournalBefore forgets the entries before pos (every reader is past them).
func (s *vtScreen) dropJournalBefore(pos int64) {
	for s.jrBase < pos && len(s.jr) > 0 {
		s.jrLines -= len(s.jr[0].pushed)
		s.jr[0] = vtEvent{}
		s.jr = s.jr[1:]
		s.jrBase++
	}
	if len(s.jr) == 0 {
		s.jr = nil
	}
}

func (s *vtScreen) journalEnd() int64 { return s.jrBase + int64(len(s.jr)) }

// ── Scrollback ───────────────────────────────────────────────────────

// trimmed copies a line without its trailing blank cells.
func trimmed(l []vtCell) []vtCell {
	end := len(l)
	for end > 0 && l[end-1].blank() {
		end--
	}
	out := make([]vtCell, end)
	copy(out, l[:end])
	return out
}

func (s *vtScreen) pushScrollback(l []vtCell) {
	s.sb = append(s.sb, l)
	if len(s.sb) > 2*vtScrollbackMax {
		s.sb = append([][]vtCell(nil), s.sb[len(s.sb)-vtScrollbackMax:]...)
	}
}

// scrollback returns the kept lines, oldest first.
func (s *vtScreen) scrollback() [][]vtCell {
	if len(s.sb) > vtScrollbackMax {
		return s.sb[len(s.sb)-vtScrollbackMax:]
	}
	return s.sb
}

// ── Scrolling ────────────────────────────────────────────────────────

func (s *vtScreen) scrollUp(n int) {
	s.scrollRegionUp(s.top, s.bottom, n, s.top == 0 && !s.altOn)
}

func (s *vtScreen) scrollDown(n int) { s.scrollRegionDown(s.top, s.bottom, n) }

// scrollRegionUp moves rows top..bottom up by n, blank rows entering at the
// bottom; with push the rows that leave go to the scrollback.
func (s *vtScreen) scrollRegionUp(top, bottom, n int, push bool) {
	n = vtClamp(n, 1, bottom-top+1)
	gone := append(s.tmpRows[:0], s.lines[top:top+n]...)
	pushed := s.tmpPush[:0]
	if push {
		for _, l := range gone {
			t := trimmed(l)
			s.pushScrollback(t)
			pushed = append(pushed, t)
		}
	}
	copy(s.lines[top:], s.lines[top+n:bottom+1])
	for i, l := range gone {
		s.clearLine(l) // reuse the row buffer: the scrollback holds a trimmed copy
		s.lines[bottom-n+1+i] = l
	}
	s.tmpRows, s.tmpPush = gone, pushed
	// journal copies what it keeps of pushed (s.tmpPush is reused).
	s.journal(vtEvent{kind: 'u', top: top, bottom: bottom, n: n, push: push, pushed: pushed})
}

// scrollRegionDown moves rows top..bottom down by n, blank rows entering at
// the top.
func (s *vtScreen) scrollRegionDown(top, bottom, n int) {
	n = vtClamp(n, 1, bottom-top+1)
	gone := make([][]vtCell, n)
	copy(gone, s.lines[bottom-n+1:bottom+1])
	copy(s.lines[top+n:bottom+1], s.lines[top:bottom-n+1])
	for i, l := range gone {
		s.clearLine(l)
		s.lines[top+i] = l
	}
	s.journal(vtEvent{kind: 'd', top: top, bottom: bottom, n: n})
}

func (s *vtScreen) index() {
	s.wrap = false
	if s.cy == s.bottom {
		s.scrollUp(1)
	} else if s.cy < s.rows-1 {
		s.cy++
	}
}

func (s *vtScreen) reverseIndex() {
	s.wrap = false
	if s.cy == s.top {
		s.scrollDown(1)
	} else if s.cy > 0 {
		s.cy--
	}
}

// ── Feeding bytes ────────────────────────────────────────────────────

// feed runs bytes through the emulator. A UTF-8 character split across two
// calls decodes as one.
func (s *vtScreen) feed(p []byte) {
	if len(p) == 0 {
		return
	}
	s.version++
	for i := 0; i < len(p); i++ {
		b := p[i]
		if s.u8need > 0 {
			if b&0xC0 == 0x80 {
				s.u8[s.u8n] = b
				s.u8n++
				if s.u8n == s.u8need {
					r, _ := utf8.DecodeRune(s.u8[:s.u8n])
					s.u8need, s.u8n = 0, 0
					s.input(r)
				}
				continue
			}
			s.u8need, s.u8n = 0, 0
			s.input(utf8.RuneError)
		}
		switch {
		case b < 0x80:
			s.input(rune(b))
		case b >= 0xC2 && b < 0xE0:
			s.u8[0], s.u8n, s.u8need = b, 1, 2
		case b >= 0xE0 && b < 0xF0:
			s.u8[0], s.u8n, s.u8need = b, 1, 3
		case b >= 0xF0 && b < 0xF5:
			s.u8[0], s.u8n, s.u8need = b, 1, 4
		default:
			s.input(utf8.RuneError)
		}
	}
}

// feedString is feed for text written by the runtime itself.
func (s *vtScreen) feedString(t string) { s.feed([]byte(t)) }

// lostBytes tells the parser that bytes before the next feed are missing:
// a sequence it was in the middle of is abandoned.
func (s *vtScreen) lostBytes() {
	s.st = 0
	s.u8need, s.u8n = 0, 0
}

func (s *vtScreen) input(r rune) {
	switch s.st {
	case 1:
		s.esc(r)
		return
	case 2:
		switch {
		case r == 0x1b:
			s.st = 1
		case r == 0x18 || r == 0x1a:
			s.st = 0
		case r < 0x20:
			s.control(r)
		case r >= 0x40 && r <= 0x7e:
			s.st = 0
			s.csi(byte(r))
		case r == '?' || r == '>' || r == '=' || r == '<':
			s.priv = byte(r)
		case r >= 0x20 && r <= 0x2f:
			s.inter = true
		default:
			if len(s.params) < 256 {
				s.params = append(s.params, byte(r))
			}
		}
		return
	case 3:
		switch {
		case r == 7:
			s.st = 0
			s.oscDone()
		case r == 0x1b:
			s.st = 5
			s.oscESC = true
		case len(s.osc) < vtOSCMax:
			s.osc = utf8.AppendRune(s.osc, r)
		}
		return
	case 4:
		switch r {
		case 7:
			s.st = 0
		case 0x1b:
			s.st = 5
			s.oscESC = false
		}
		return
	case 5:
		s.st = 0
		if s.oscESC {
			s.oscDone()
		}
		if r != '\\' {
			s.st = 1
			s.esc(r)
		}
		return
	case 6:
		s.st = 0
		return
	case 7:
		s.st = 0
		if r == '0' {
			s.charset = '0'
		} else {
			s.charset = 'B'
		}
		return
	}
	if r == 0x1b {
		s.st = 1
		return
	}
	if r < 0x20 || r == 0x7f {
		s.control(r)
		return
	}
	if r >= 0x80 && r < 0xa0 {
		return // C1 controls are not supported as bytes; drop them
	}
	s.print(r)
}

func (s *vtScreen) esc(r rune) {
	s.st = 0
	switch r {
	case '[':
		s.st = 2
		s.params = s.params[:0]
		s.priv = 0
		s.inter = false
	case ']':
		s.st = 3
		s.osc = s.osc[:0]
	case 'P', 'X', '^', '_':
		s.st = 4
	case '7':
		s.save()
	case '8':
		s.restore()
	case 'c':
		s.reset()
	case 'D':
		s.index()
	case 'M':
		s.reverseIndex()
	case 'E':
		s.cx = 0
		s.index()
	case '(':
		s.st = 7
	case ')', '*', '+', '-', '.', '/', '#', '%', ' ':
		s.st = 6
	}
}

func (s *vtScreen) control(r rune) {
	switch r {
	case 7:
		s.bells++
	case 8:
		if s.cx > 0 {
			s.cx--
		}
		s.wrap = false
	case 9:
		s.cx = min(s.cols-1, (s.cx/8+1)*8)
		s.wrap = false
	case 10, 11, 12:
		s.index()
	case 13:
		s.cx = 0
		s.wrap = false
	}
}

func (s *vtScreen) oscDone() {
	body := string(s.osc)
	if i := indexByte(body, ';'); i > 0 {
		switch body[:i] {
		case "0", "2":
			t := body[i+1:]
			if len(t) > 256 {
				t = t[:256]
			}
			s.title = t
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// ── Printing ─────────────────────────────────────────────────────────

// decGraphics maps the DEC special graphics set (ESC ( 0) to Unicode.
var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'f': '°', 'g': '±', 'j': '┘', 'k': '┐', 'l': '┌', 'm': '└',
	'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽', 't': '├', 'u': '┤',
	'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£', '~': '·',
}

// vtWideRanges are the East Asian Wide and Fullwidth ranges and the emoji
// presentation blocks: characters a terminal draws two cells wide.
var vtWideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x231A, 0x231B}, {0x2329, 0x232A}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0},
	{0x23F3, 0x23F3}, {0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F},
	{0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE}, {0x26C4, 0x26C5},
	{0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA}, {0x26F2, 0x26F3}, {0x26F5, 0x26F5},
	{0x26FA, 0x26FA}, {0x26FD, 0x26FD}, {0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728},
	{0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50}, {0x2B55, 0x2B55},
	{0x2E80, 0x303E}, {0x3041, 0x33FF}, {0x3400, 0x4DBF}, {0x4E00, 0x9FFF}, {0xA000, 0xA4CF},
	{0xA960, 0xA97F}, {0xAC00, 0xD7A3}, {0xF900, 0xFAFF}, {0xFE10, 0xFE19}, {0xFE30, 0xFE6F},
	{0xFF00, 0xFF60}, {0xFFE0, 0xFFE6}, {0x16FE0, 0x16FE4}, {0x17000, 0x18AFF}, {0x1B000, 0x1B2FF},
	{0x1F004, 0x1F004}, {0x1F0CF, 0x1F0CF}, {0x1F18E, 0x1F18E}, {0x1F191, 0x1F19A}, {0x1F200, 0x1F202},
	{0x1F210, 0x1F23B}, {0x1F240, 0x1F248}, {0x1F250, 0x1F251}, {0x1F260, 0x1F265}, {0x1F300, 0x1F64F},
	{0x1F680, 0x1F6FF}, {0x1F7E0, 0x1F7EB}, {0x1F90C, 0x1F9FF}, {0x1FA70, 0x1FAFF}, {0x20000, 0x2FFFD},
	{0x30000, 0x3FFFD},
}

// vtRuneWidth is the number of cells r takes: 0 for a combining mark, 2 for
// a wide character, else 1.
func vtRuneWidth(r rune) int {
	if r < 0x300 {
		return 1
	}
	if unicode.In(r, unicode.Mn, unicode.Me) || r == 0x200B || r == 0x200C || r == 0x200D || (r >= 0xFE00 && r <= 0xFE0F) {
		return 0
	}
	if r < 0x1100 {
		return 1
	}
	lo, hi := 0, len(vtWideRanges)
	for lo < hi {
		m := (lo + hi) / 2
		switch {
		case r < vtWideRanges[m][0]:
			hi = m
		case r > vtWideRanges[m][1]:
			lo = m + 1
		default:
			return 2
		}
	}
	return 1
}

// unpair blanks both halves of a wide character whose half at x is about
// to be overwritten or moved, so no half is ever left without the other.
func (s *vtScreen) unpair(line []vtCell, x int) {
	if x < 0 || x >= len(line) {
		return
	}
	c := line[x]
	if c.fl&vtTail != 0 {
		if x > 0 && line[x-1].fl&vtWide != 0 {
			line[x-1] = vtCell{bg: line[x-1].bg}
		}
		line[x] = vtCell{bg: c.bg}
	}
	if c.fl&vtWide != 0 {
		if x+1 < len(line) && line[x+1].fl&vtTail != 0 {
			line[x+1] = vtCell{bg: line[x+1].bg}
		}
		line[x] = vtCell{bg: c.bg}
	}
}

func (s *vtScreen) print(r rune) {
	w := vtRuneWidth(r)
	if w == 0 {
		s.combine(r)
		return
	}
	if s.charset == '0' {
		if g, ok := decGraphics[r]; ok {
			r = g
		}
	}
	if s.wrap {
		s.wrap = false
		if s.autowrap {
			s.cx = 0
			s.index()
		}
	}
	if w == 2 && s.cx == s.cols-1 {
		if s.cols < 2 {
			w = 1
		} else if s.autowrap {
			line := s.lines[s.cy]
			s.unpair(line, s.cx)
			line[s.cx] = vtCell{bg: s.attr.bg}
			s.cx = 0
			s.index()
		} else {
			w = 1
		}
	}
	line := s.lines[s.cy]
	if s.insert {
		s.insertCells(w)
	}
	s.unpair(line, s.cx)
	if w == 2 {
		s.unpair(line, s.cx+1)
	}
	c := s.attr
	c.r = r
	c.comb = ""
	c.fl &= vtStyleMask
	if w == 2 {
		c.fl |= vtWide
		t := s.attr
		t.r = 0
		t.comb = ""
		t.fl = (t.fl & vtStyleMask) | vtTail
		line[s.cx+1] = t
	}
	line[s.cx] = c
	s.last = r
	if s.cx+w >= s.cols {
		s.cx = s.cols - 1
		if s.autowrap {
			s.wrap = true
		}
	} else {
		s.cx += w
	}
}

// combine joins a combining mark to the character before the cursor.
func (s *vtScreen) combine(r rune) {
	x := s.cx - 1
	if s.wrap {
		x = s.cx
	}
	if x < 0 {
		return
	}
	line := s.lines[s.cy]
	if line[x].fl&vtTail != 0 && x > 0 {
		x--
	}
	if line[x].r == 0 || len(line[x].comb) >= 16 {
		return
	}
	line[x].comb += string(r)
}

func (s *vtScreen) insertCells(n int) {
	line := s.lines[s.cy]
	n = vtClamp(n, 1, s.cols-s.cx)
	s.unpair(line, s.cx)
	copy(line[s.cx+n:], line[s.cx:s.cols-n])
	for i := s.cx; i < s.cx+n; i++ {
		line[i] = vtCell{bg: s.attr.bg}
	}
	if last := line[s.cols-1]; last.fl&vtWide != 0 {
		line[s.cols-1] = vtCell{bg: last.bg}
	}
}

func (s *vtScreen) save() {
	s.saved = vtSaved{x: s.cx, y: s.cy, attr: s.attr, charset: s.charset, autowrap: s.autowrap}
	s.hasSaved = true
}

func (s *vtScreen) restore() {
	s.wrap = false
	if !s.hasSaved {
		s.cx, s.cy = 0, 0
		return
	}
	s.cx = vtClamp(s.saved.x, 0, s.cols-1)
	s.cy = vtClamp(s.saved.y, 0, s.rows-1)
	s.attr = s.saved.attr
	s.charset = s.saved.charset
}

// erase blanks cells [a, b) of row y with the current background.
func (s *vtScreen) erase(y, a, b int) {
	line := s.lines[y]
	a = max(a, 0)
	b = min(b, s.cols)
	if a >= b {
		return
	}
	s.unpair(line, a)
	s.unpair(line, b-1)
	for i := a; i < b; i++ {
		line[i] = vtCell{bg: s.attr.bg}
	}
}

// ── CSI ──────────────────────────────────────────────────────────────

// csiParams parses the parameters: one entry per ';' field, each a list of
// its ':' sub-parameters (-1 for an empty one).
func (s *vtScreen) csiParams() [][]int {
	var out [][]int
	cur := []int{-1}
	for _, b := range s.params {
		switch {
		case b >= '0' && b <= '9':
			k := len(cur) - 1
			if cur[k] < 0 {
				cur[k] = 0
			}
			if cur[k] < 100000 {
				cur[k] = cur[k]*10 + int(b-'0')
			}
		case b == ':':
			cur = append(cur, -1)
		case b == ';':
			out = append(out, cur)
			cur = []int{-1}
		}
	}
	return append(out, cur)
}

func (s *vtScreen) csi(fin byte) {
	ps := s.csiParams()
	num := func(k, d int) int {
		if k < len(ps) && ps[k][0] >= 0 {
			return ps[k][0]
		}
		return d
	}
	cnt := func(k int) int { return max(num(k, 1), 1) }
	if s.inter {
		return // CSI with intermediates (DECSCUSR, DECSTR, ...): not emulated
	}
	switch s.priv {
	case '?':
		if fin == 'h' || fin == 'l' {
			for i := range ps {
				s.privMode(ps[i][0], fin == 'h')
			}
		}
		return
	case '>':
		if fin == 'c' {
			s.replies = append(s.replies, "\x1b[>0;276;0c"...)
		}
		return
	case 0:
	default:
		return
	}
	switch fin {
	case 'm':
		s.sgr(ps)
		return
	case 'n':
		switch num(0, 0) {
		case 5:
			s.replies = append(s.replies, "\x1b[0n"...)
		case 6:
			s.replies = append(s.replies, "\x1b["+strconv.Itoa(s.cy+1)+";"+strconv.Itoa(s.cx+1)+"R"...)
		}
		return
	case 'c':
		if num(0, 0) == 0 {
			s.replies = append(s.replies, "\x1b[?1;2c"...)
		}
		return
	case 'h', 'l':
		for i := range ps {
			if ps[i][0] == 4 {
				s.insert = fin == 'h'
			}
		}
		return
	case 'b':
		if s.last != 0 {
			for k := min(cnt(0), s.cols*s.rows); k > 0; k-- {
				s.print(s.last)
			}
		}
		return
	}
	s.wrap = false
	switch fin {
	case 'A':
		lim := 0
		if s.cy >= s.top {
			lim = s.top
		}
		s.cy = max(s.cy-cnt(0), lim)
	case 'B', 'e':
		lim := s.rows - 1
		if s.cy <= s.bottom {
			lim = s.bottom
		}
		s.cy = min(s.cy+cnt(0), lim)
	case 'C', 'a':
		s.cx = min(s.cx+cnt(0), s.cols-1)
	case 'D':
		s.cx = max(s.cx-cnt(0), 0)
	case 'E':
		s.cy = min(s.cy+cnt(0), s.rows-1)
		s.cx = 0
	case 'F':
		s.cy = max(s.cy-cnt(0), 0)
		s.cx = 0
	case 'G', '`':
		s.cx = vtClamp(cnt(0)-1, 0, s.cols-1)
	case 'd':
		s.cy = vtClamp(cnt(0)-1, 0, s.rows-1)
	case 'H', 'f':
		s.cy = vtClamp(cnt(0)-1, 0, s.rows-1)
		s.cx = vtClamp(cnt(1)-1, 0, s.cols-1)
	case 'I':
		for k := cnt(0); k > 0; k-- {
			s.cx = min(s.cols-1, (s.cx/8+1)*8)
		}
	case 'Z':
		for k := cnt(0); k > 0; k-- {
			s.cx = max(0, (s.cx-1)/8*8)
		}
	case 'J':
		switch num(0, 0) {
		case 0:
			s.erase(s.cy, s.cx, s.cols)
			for y := s.cy + 1; y < s.rows; y++ {
				s.erase(y, 0, s.cols)
			}
		case 1:
			for y := 0; y < s.cy; y++ {
				s.erase(y, 0, s.cols)
			}
			s.erase(s.cy, 0, s.cx+1)
		case 2:
			for y := 0; y < s.rows; y++ {
				s.erase(y, 0, s.cols)
			}
		case 3:
			s.sb = nil
			s.journal(vtEvent{kind: 'x'})
		}
	case 'K':
		switch num(0, 0) {
		case 0:
			s.erase(s.cy, s.cx, s.cols)
		case 1:
			s.erase(s.cy, 0, s.cx+1)
		case 2:
			s.erase(s.cy, 0, s.cols)
		}
	case 'L', 'M':
		if s.cy < s.top || s.cy > s.bottom {
			return
		}
		if fin == 'L' {
			s.scrollRegionDown(s.cy, s.bottom, cnt(0))
		} else {
			s.scrollRegionUp(s.cy, s.bottom, cnt(0), false)
		}
		s.cx = 0
	case 'P':
		line := s.lines[s.cy]
		n := min(cnt(0), s.cols-s.cx)
		s.unpair(line, s.cx)
		s.unpair(line, s.cx+n-1)
		copy(line[s.cx:], line[s.cx+n:])
		for i := s.cols - n; i < s.cols; i++ {
			line[i] = vtCell{bg: s.attr.bg}
		}
	case '@':
		s.insertCells(cnt(0))
	case 'X':
		s.erase(s.cy, s.cx, s.cx+cnt(0))
	case 'S':
		s.scrollUp(cnt(0))
	case 'T':
		s.scrollDown(cnt(0))
	case 'r':
		t, b := num(0, 1)-1, num(1, s.rows)-1
		t = max(t, 0)
		if b >= s.rows || b < 0 {
			b = s.rows - 1
		}
		if t < b {
			s.top, s.bottom = t, b
		}
		s.cx, s.cy = 0, 0
	case 's':
		s.save()
	case 'u':
		s.restore()
	}
}

func (s *vtScreen) privMode(mode int, on bool) {
	switch mode {
	case 1:
		s.appCursor = on
	case 7:
		s.autowrap = on
		if !on {
			s.wrap = false
		}
	case 25:
		s.cursorOn = on
	case 2004:
		s.bracketed = on
	case 1048:
		if on {
			s.save()
		} else {
			s.restore()
		}
	case 47, 1047:
		if on {
			s.enterAlt(false)
		} else {
			s.leaveAlt(false)
		}
	case 1049:
		if on {
			s.enterAlt(true)
		} else {
			s.leaveAlt(true)
		}
	}
}

func (s *vtScreen) enterAlt(saveCursor bool) {
	if s.altOn {
		return
	}
	if saveCursor {
		s.mainSaved = vtSaved{x: s.cx, y: s.cy, attr: s.attr, charset: s.charset}
	}
	s.mainLines = s.lines
	s.lines = make([][]vtCell, s.rows)
	for y := range s.lines {
		s.lines[y] = s.blankLine()
	}
	s.altOn = true
	s.wrap = false
}

func (s *vtScreen) leaveAlt(restoreCursor bool) {
	if !s.altOn {
		return
	}
	s.lines = s.mainLines
	s.mainLines = nil
	s.altOn = false
	s.wrap = false
	if restoreCursor {
		s.cx = vtClamp(s.mainSaved.x, 0, s.cols-1)
		s.cy = vtClamp(s.mainSaved.y, 0, s.rows-1)
		s.attr = s.mainSaved.attr
		s.charset = s.mainSaved.charset
	}
}

func vtPalette(n int) uint32 { return vtPal | uint32(vtClamp(n, 0, 255)) }

// extColour reads a 38 / 48 colour: the ':' form in the parameter's own
// sub-parameters, else the ';' form in the parameters after it. It returns
// the colour (ok false when malformed) and how many extra ';' parameters
// it used.
func extColour(ps [][]int, i int) (uint32, bool, int) {
	sub := ps[i]
	at := func(k int) int {
		if k < len(sub) && sub[k] >= 0 {
			return sub[k]
		}
		return 0
	}
	if len(sub) > 1 {
		switch sub[1] {
		case 5:
			return vtPalette(at(2)), len(sub) > 2, 0
		case 2:
			if len(sub) >= 6 { // 38:2:<colour space>:r:g:b
				return vtRGB | uint32(vtClamp(at(3), 0, 255))<<16 | uint32(vtClamp(at(4), 0, 255))<<8 | uint32(vtClamp(at(5), 0, 255)), true, 0
			}
			return vtRGB | uint32(vtClamp(at(2), 0, 255))<<16 | uint32(vtClamp(at(3), 0, 255))<<8 | uint32(vtClamp(at(4), 0, 255)), len(sub) >= 5, 0
		}
		return 0, false, 0
	}
	p := func(k int) int {
		if i+k < len(ps) && ps[i+k][0] >= 0 {
			return ps[i+k][0]
		}
		return 0
	}
	if i+1 >= len(ps) {
		return 0, false, 0
	}
	switch p(1) {
	case 5:
		return vtPalette(p(2)), i+2 < len(ps), 2
	case 2:
		return vtRGB | uint32(vtClamp(p(2), 0, 255))<<16 | uint32(vtClamp(p(3), 0, 255))<<8 | uint32(vtClamp(p(4), 0, 255)), i+4 < len(ps), 4
	}
	return 0, false, len(ps)
}

func (s *vtScreen) sgr(ps [][]int) {
	a := &s.attr
	for i := 0; i < len(ps); i++ {
		v := ps[i][0]
		if v < 0 {
			v = 0
		}
		switch {
		case v == 0:
			*a = vtCell{}
		case v == 1:
			a.fl |= vtBold
		case v == 2:
			a.fl |= vtDim
		case v == 3:
			a.fl |= vtItalic
		case v == 4 || v == 21:
			if len(ps[i]) > 1 && ps[i][1] == 0 {
				a.fl &^= vtUnderline
			} else {
				a.fl |= vtUnderline
			}
		case v == 7:
			a.fl |= vtInverse
		case v == 9:
			a.fl |= vtStrike
		case v == 22:
			a.fl &^= vtBold | vtDim
		case v == 23:
			a.fl &^= vtItalic
		case v == 24:
			a.fl &^= vtUnderline
		case v == 27:
			a.fl &^= vtInverse
		case v == 29:
			a.fl &^= vtStrike
		case v >= 30 && v <= 37:
			a.fg = vtPalette(v - 30)
		case v == 39:
			a.fg = 0
		case v >= 40 && v <= 47:
			a.bg = vtPalette(v - 40)
		case v == 49:
			a.bg = 0
		case v >= 90 && v <= 97:
			a.fg = vtPalette(v - 90 + 8)
		case v >= 100 && v <= 107:
			a.bg = vtPalette(v - 100 + 8)
		case v == 38 || v == 48:
			col, ok, used := extColour(ps, i)
			if ok {
				if v == 38 {
					a.fg = col
				} else {
					a.bg = col
				}
			}
			i += used
		}
	}
}

// ── Resize ───────────────────────────────────────────────────────────

func (s *vtScreen) fitCols(lines [][]vtCell, cols int) {
	for y, l := range lines {
		if len(l) > cols {
			l = l[:cols]
			if last := l[cols-1]; last.fl&vtWide != 0 {
				l[cols-1] = vtCell{bg: last.bg}
			}
		}
		for len(l) < cols {
			l = append(l, vtCell{})
		}
		lines[y] = l
	}
}

// resize changes the size like xterm: shrinking the rows keeps the cursor
// row on screen and moves the rows above it to the scrollback.
func (s *vtScreen) resize(cols, rows int) {
	cols = vtClamp(cols, 1, vtMaxCols)
	rows = vtClamp(rows, 1, vtMaxRows)
	if cols == s.cols && rows == s.rows {
		return
	}
	s.version++
	s.fitCols(s.lines, cols)
	var pushed [][]vtCell
	if rows < len(s.lines) {
		drop := max(0, min(s.cy-(rows-1), len(s.lines)-rows))
		for k := 0; k < drop; k++ {
			if !s.altOn {
				l := trimmed(s.lines[k])
				s.pushScrollback(l)
				pushed = append(pushed, l)
			}
		}
		s.lines = s.lines[drop:]
		s.cy -= drop
		s.lines = s.lines[:rows]
	}
	for len(s.lines) < rows {
		s.lines = append(s.lines, make([]vtCell, cols))
	}
	if s.altOn {
		s.fitCols(s.mainLines, cols)
		if len(s.mainLines) > rows {
			s.mainLines = s.mainLines[:rows]
		}
		for len(s.mainLines) < rows {
			s.mainLines = append(s.mainLines, make([]vtCell, cols))
		}
	}
	s.cols, s.rows = cols, rows
	s.top, s.bottom = 0, rows-1
	s.cx = vtClamp(s.cx, 0, cols-1)
	s.cy = vtClamp(s.cy, 0, rows-1)
	s.wrap = false
	if len(pushed) > vtScrollbackMax {
		pushed = pushed[len(pushed)-vtScrollbackMax:]
	}
	s.journal(vtEvent{kind: 'z', cols: cols, rows: rows, pushed: pushed})
}

// ── Text (tests and accessibility) ───────────────────────────────────

// lineText is a row's characters without trailing blanks; a wide
// character's tail adds nothing.
func lineText(l []vtCell) string {
	b := make([]byte, 0, len(l))
	for x, c := range l {
		if c.fl&vtTail != 0 && x > 0 && l[x-1].fl&vtWide != 0 {
			continue
		}
		if c.r == 0 || c.fl&vtTail != 0 {
			b = append(b, ' ')
		} else {
			b = utf8.AppendRune(b, c.r)
			b = append(b, c.comb...)
		}
	}
	end := len(b)
	for end > 0 && b[end-1] == ' ' {
		end--
	}
	return string(b[:end])
}

func (s *vtScreen) text() []string {
	out := make([]string, len(s.lines))
	for y, l := range s.lines {
		out[y] = lineText(l)
	}
	return out
}

func (s *vtScreen) scrollbackText() []string {
	sb := s.scrollback()
	out := make([]string, len(sb))
	for i, l := range sb {
		out[i] = lineText(l)
	}
	return out
}
