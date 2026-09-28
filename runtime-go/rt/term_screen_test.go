package rt

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// The server-side terminal screen (term_screen.go) against known
// sequences, and the frame stream (term_frame.go) against a model of the
// widget that applies the ops: after every frame the model must show
// exactly the screen, its scrollback, cursor and styles.

func vtOf(cols, rows int, in ...string) *vtScreen {
	s := newVTScreen(cols, rows)
	for _, p := range in {
		s.feedString(p)
	}
	return s
}

func eqT(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", name, got, want)
	}
}

func TestVTScreen_CursorMovement(t *testing.T) {
	s := vtOf(10, 5, "\x1b[3;4HX")
	eqT(t, "CUP writes at row 3 col 4", s.text()[2], "   X")
	eqT(t, "the cursor is after X", [2]int{s.cx, s.cy}, [2]int{4, 2})
	s.feedString("\x1b[99;99H")
	eqT(t, "CUP clamps", [2]int{s.cx, s.cy}, [2]int{9, 4})
	s.feedString("\x1b[100A")
	eqT(t, "CUU clamps at the top", s.cy, 0)
	s.feedString("\x1b[100D")
	eqT(t, "CUB clamps at the left", s.cx, 0)
	s.feedString("\x1b[3B\x1b[2C")
	eqT(t, "CUD and CUF", [2]int{s.cx, s.cy}, [2]int{2, 3})
	s.feedString("\x1b[A\x1b[D")
	eqT(t, "CUU / CUB default to 1", [2]int{s.cx, s.cy}, [2]int{1, 2})
	s.feedString("\x1b[5G")
	eqT(t, "CHA", s.cx, 4)
	s.feedString("\x1b[7`")
	eqT(t, "HPA", s.cx, 6)
	s.feedString("\x1b[2d")
	eqT(t, "VPA", s.cy, 1)
	s.feedString("\x1b[2E")
	eqT(t, "CNL", [2]int{s.cx, s.cy}, [2]int{0, 3})
	s.feedString("\x1b[F")
	eqT(t, "CPL", [2]int{s.cx, s.cy}, [2]int{0, 2})
	s.feedString("\x1b[H")
	eqT(t, "CUP defaults to home", [2]int{s.cx, s.cy}, [2]int{0, 0})
	s.feedString("\x1b[2;2H\x1b7\x1b[5;5H\x1b8")
	eqT(t, "DECSC / DECRC", [2]int{s.cx, s.cy}, [2]int{1, 1})
	s.feedString("\x1b[3;3H\x1b[s\x1b[1;1H\x1b[u")
	eqT(t, "CSI s / u", [2]int{s.cx, s.cy}, [2]int{2, 2})
	s.feedString("\x1b[1;1H\x1b[I")
	eqT(t, "CHT", s.cx, 8)
	s.feedString("\x1b[Z")
	eqT(t, "CBT", s.cx, 0)
	s = vtOf(10, 5, "ab\bX\tY")
	eqT(t, "BS and TAB", s.text()[0], "aX      Y")
}

func TestVTScreen_SGR(t *testing.T) {
	s := vtOf(20, 2, "\x1b[31;42mA\x1b[0mB\x1b[91;104mC\x1b[38;5;196mD\x1b[38;2;1;2;3mE\x1b[48:2::4:5:6mF\x1b[38:5:21mG\x1b[39;49mH")
	c := s.lines[0]
	eqT(t, "SGR 31 / 42", [2]int{wireColour(c[0].fg), wireColour(c[0].bg)}, [2]int{1, 2})
	eqT(t, "SGR 0", [2]int{wireColour(c[1].fg), wireColour(c[1].bg)}, [2]int{-1, -1})
	eqT(t, "SGR 91 / 104 bright", [2]int{wireColour(c[2].fg), wireColour(c[2].bg)}, [2]int{9, 12})
	eqT(t, "SGR 38;5;196", wireColour(c[3].fg), 196)
	eqT(t, "SGR 38;2;r;g;b true colour", wireColour(c[4].fg), 256+0x010203)
	eqT(t, "SGR 48:2::r:g:b (colon form with a colour space)", wireColour(c[5].bg), 256+0x040506)
	eqT(t, "SGR 38:5:n (colon form)", wireColour(c[6].fg), 21)
	eqT(t, "SGR 39 / 49", [2]int{wireColour(c[7].fg), wireColour(c[7].bg)}, [2]int{-1, -1})
	s = vtOf(10, 1, "\x1b[1;2;3;4;7;9mX\x1b[22;23;24;27;29mY\x1b[1mZ\x1b[mW")
	d := s.lines[0]
	eqT(t, "SGR 1;2;3;4;7;9", d[0].fl, vtBold|vtDim|vtItalic|vtUnderline|vtInverse|vtStrike)
	eqT(t, "SGR 22;23;24;27;29", d[1].fl, uint8(0))
	eqT(t, "SGR with no params resets", [2]uint8{d[2].fl, d[3].fl}, [2]uint8{vtBold, 0})
	s = vtOf(10, 1, "\x1b[4:0mX\x1b[4:3mY")
	eqT(t, "SGR 4:0 is no underline, 4:3 an underline", [2]uint8{s.lines[0][0].fl, s.lines[0][1].fl}, [2]uint8{0, vtUnderline})
	s = vtOf(10, 1, "\x1b[41m\x1b[2K")
	eqT(t, "erase uses the current background", wireColour(s.lines[0][5].bg), 1)
}

func TestVTScreen_Erase(t *testing.T) {
	fill := func() *vtScreen { return vtOf(5, 3, "abcde\r\nfghij\r\nklmno") }
	eqT(t, "deferred wrap: cols chars then CR LF adds no empty line", fill().text(), []string{"abcde", "fghij", "klmno"})
	cases := []struct {
		seq, name string
		want      []string
	}{
		{"\x1b[2;3H\x1b[0J", "ED 0", []string{"abcde", "fg", ""}},
		{"\x1b[2;3H\x1b[1J", "ED 1", []string{"", "   ij", "klmno"}},
		{"\x1b[2J", "ED 2", []string{"", "", ""}},
		{"\x1b[2;3H\x1b[K", "EL 0", []string{"abcde", "fg", "klmno"}},
		{"\x1b[2;3H\x1b[1K", "EL 1", []string{"abcde", "   ij", "klmno"}},
		{"\x1b[2;3H\x1b[2K", "EL 2", []string{"abcde", "", "klmno"}},
		{"\x1b[1;2H\x1b[2X", "ECH", []string{"a  de", "fghij", "klmno"}},
		{"\x1b[1;2H\x1b[2P", "DCH", []string{"ade", "fghij", "klmno"}},
		{"\x1b[1;1H\x1b[2@", "ICH", []string{"  abc", "fghij", "klmno"}},
		{"\x1b[2;1H\x1b[L", "IL", []string{"abcde", "", "fghij"}},
		{"\x1b[1;1H\x1b[M", "DL", []string{"fghij", "klmno", ""}},
	}
	for _, c := range cases {
		s := fill()
		s.feedString(c.seq)
		eqT(t, c.name, s.text(), c.want)
	}
	s := fill()
	s.feedString("\x1b[1;1H\x1b[M")
	eqT(t, "DL at the top row does not fill the scrollback", s.scrollbackText(), []string{})
	s = fill()
	s.feedString("\x1b[2J\x1b[3J")
	eqT(t, "ED 3 clears the scrollback", len(s.scrollback()), 0)
	s = vtOf(5, 1, "abc\x1b[1;1H\x1b[4hXY\x1b[4lZ")
	eqT(t, "IRM inserts, then replaces", s.text()[0], "XYZbc")
}

func TestVTScreen_WrapAndScroll(t *testing.T) {
	eqT(t, "auto-wrap", vtOf(5, 3, "abcdefg").text(), []string{"abcde", "fg", ""})
	s := vtOf(5, 3, "abcde")
	eqT(t, "the wrap is deferred", []any{s.cx, s.cy, s.wrap}, []any{4, 0, true})
	eqT(t, "?7l disables wrap", vtOf(5, 3, "\x1b[?7labcdefg").text(), []string{"abcdg", "", ""})
	s = vtOf(5, 2, "1\r\n2\r\n3\r\n4")
	eqT(t, "LF at the bottom scrolls", s.text(), []string{"3", "4"})
	eqT(t, "the lines go to the scrollback", s.scrollbackText(), []string{"1", "2"})
	s = vtOf(5, 4, "a\r\nb\r\nc\r\nd\x1b[2;3r")
	eqT(t, "DECSTBM homes the cursor", [2]int{s.cx, s.cy}, [2]int{0, 0})
	s.feedString("\x1b[3;1H\n")
	eqT(t, "LF at the region bottom scrolls only the region", s.text(), []string{"a", "c", "", "d"})
	eqT(t, "a region scroll keeps the scrollback", s.scrollbackText(), []string{})
	eqT(t, "SU", vtOf(5, 3, "a\r\nb\r\nc\x1b[S").text(), []string{"b", "c", ""})
	eqT(t, "SD", vtOf(5, 3, "a\r\nb\r\nc\x1b[T").text(), []string{"", "a", "b"})
	eqT(t, "RI at the top scrolls down", vtOf(5, 3, "a\x1bMb").text(), []string{" b", "a", ""})
	eqT(t, "NEL", vtOf(5, 3, "ab\x1bEc").text(), []string{"ab", "c", ""})
	s = vtOf(10, 2)
	for i := 0; i < 2500; i++ {
		s.feedString(fmt.Sprintf("%d\r\n", i))
	}
	sb := s.scrollbackText()
	eqT(t, "the scrollback is bounded", []any{len(sb), sb[0], sb[len(sb)-1]}, []any{vtScrollbackMax, "1499", "2498"})
}

func TestVTScreen_AlternateScreenAndModes(t *testing.T) {
	s := vtOf(5, 2, "main\x1b[?1049h")
	eqT(t, "?1049h shows a clear screen", s.text(), []string{"", ""})
	s.feedString("\x1b[Halt\r\n1\r\n2\r\n3")
	eqT(t, "writes go to the alternate screen", s.text(), []string{"2", "3"})
	eqT(t, "the alternate screen keeps no scrollback", s.scrollbackText(), []string{})
	s.feedString("\x1b[?1049l")
	eqT(t, "?1049l restores the main screen", s.text(), []string{"main", ""})
	eqT(t, "?1049l restores the cursor", [2]int{s.cx, s.cy}, [2]int{4, 0})
	eqT(t, "?47 round trip", vtOf(5, 2, "x\x1b[?47hy\x1b[?47l").text(), []string{"x", ""})
	s = vtOf(5, 2, "\x1b[?25l\x1b[?1h\x1b[?2004h")
	eqT(t, "?25l ?1h ?2004h", []any{s.cursorOn, s.modes()}, []any{false, 3})
	s.feedString("\x1b[?25h\x1b[?1l\x1b[?2004l\x1b[?1049h")
	eqT(t, "?25h ?1l ?2004l, alternate", []any{s.cursorOn, s.modes()}, []any{true, 4})
}

func TestVTScreen_ParserEdges(t *testing.T) {
	s := vtOf(10, 2, "\x1b[3", "1mR")
	eqT(t, "a CSI split across writes", []any{s.text()[0], wireColour(s.lines[0][0].fg)}, []any{"R", 1})
	eqT(t, "ESC split from its CSI", vtOf(10, 2, "\x1b", "[2;2HZ").text(), []string{"", " Z"})
	s = vtOf(10, 1, "\x1b]0;hello\x07ok\x1b]2;x y\x1b\\!")
	eqT(t, "OSC 0 / 2 set the title and are consumed", []any{s.text()[0], s.title}, []any{"ok!", "x y"})
	eqT(t, "DCS and other strings are consumed", vtOf(10, 1, "a\x1bPq#0;2;0\x1b\\b").text()[0], "ab")
	eqT(t, "charset and private-other sequences are consumed", vtOf(10, 1, "\x1b(Bq\x1b[>c\x1b[ qz").text()[0], "qz")
	eqT(t, "DEC line drawing", vtOf(10, 1, "\x1b(0lqk\x1b(Bq").text()[0], "┌─┐q")
	s = vtOf(10, 1, "abc\x1bc")
	eqT(t, "RIS clears the screen", []any{s.text()[0], s.cx}, []any{"", 0})
	eqT(t, "REP repeats the last character", vtOf(10, 1, "x\x1b[3b").text()[0], "xxxx")
	s = vtOf(10, 1, "a\x07b\x07")
	eqT(t, "BEL rings and prints nothing", []any{s.text()[0], s.bells}, []any{"ab", 2})
	s = vtOf(10, 3, "\x1b[2;3H\x1b[6n\x1b[5n\x1b[c")
	eqT(t, "DSR and DA reply to the program", string(s.replies), "\x1b[2;3R\x1b[0n\x1b[?1;2c")
}

func TestVTScreen_UTF8WideAndCombining(t *testing.T) {
	b := []byte("é日😀x")
	s := newVTScreen(10, 2)
	for i := range b {
		s.feed(b[i : i+1]) // one byte at a time: every character is split
	}
	eqT(t, "UTF-8 split across writes", s.text()[0], "é日😀x")
	eqT(t, "wide characters take two cells", []any{s.cx, s.lines[0][1].fl & vtWide, s.lines[0][2].fl & vtTail}, []any{6, vtWide, vtTail})
	s = vtOf(5, 2, "abcd日")
	eqT(t, "a wide character that does not fit wraps", s.text(), []string{"abcd", "日"})
	s = vtOf(6, 1, "日本\x1b[1;2Hx")
	eqT(t, "overwriting a wide character's tail blanks its head", s.text()[0], " x本")
	s = vtOf(6, 1, "日本\x1b[1;3Hx")
	eqT(t, "overwriting a head blanks its tail", s.text()[0], "日x")
	s = vtOf(6, 1, "éx")
	eqT(t, "a combining mark joins the cell before", []any{s.text()[0], s.lines[0][0].comb, s.cx}, []any{"éx", "́", 2})
	s = vtOf(4, 1, "\xff\xc3(")
	eqT(t, "invalid UTF-8 is U+FFFD", s.text()[0], "��(")
	eqT(t, "vtRuneWidth", []int{vtRuneWidth('a'), vtRuneWidth('日'), vtRuneWidth('😀'), vtRuneWidth(0x301), vtRuneWidth('─')}, []int{1, 2, 2, 0, 1})
}

func TestVTScreen_Resize(t *testing.T) {
	s := vtOf(5, 3, "a\r\nb\r\nc")
	s.resize(3, 2)
	eqT(t, "shrinking the rows keeps the cursor row", s.text(), []string{"b", "c"})
	eqT(t, "the row that left goes to the scrollback", s.scrollbackText(), []string{"a"})
	s.resize(6, 4)
	eqT(t, "growing pads", []any{s.text(), len(s.lines[0])}, []any{[]string{"b", "c", "", ""}, 6})
	s = vtOf(4, 2, "ab日")
	s.resize(3, 2)
	eqT(t, "a wide character cut by the new edge is blanked", s.text()[0], "ab")
	s = vtOf(5, 2, "main\x1b[?1049halt")
	s.resize(3, 3)
	s.feedString("\x1b[?1049l")
	eqT(t, "the main screen follows a resize made on the alternate one", s.text(), []string{"mai", "", ""})
}

// ── The widget model ────────────────────────────────────────────────

type tcCell struct {
	t    string
	st   [3]int
	tail bool
}

type termClient struct {
	cols, rows int
	grid       [][]tcCell
	sb         [][]tcCell
	seq        int64
	cx, cy     int
	cursorOn   bool
	title      string
	modes      int
	bells      int
}

func (c *termClient) blankRow() []tcCell {
	r := make([]tcCell, c.cols)
	for i := range r {
		r[i] = tcCell{t: " ", st: [3]int{-1, -1, 0}}
	}
	return r
}

func num(v any) int { return int(v.(float64)) }

// decodeLine turns [style, text, ...] into cells.
func decodeLine(styles []any, runs []any) []tcCell {
	var out []tcCell
	for i := 0; i+1 < len(runs); i += 2 {
		st := styles[num(runs[i])].([]any)
		s3 := [3]int{num(st[0]), num(st[1]), num(st[2])}
		fl := s3[2]
		plain := [3]int{s3[0], s3[1], fl & int(vtStyleMask)}
		text := runs[i+1].(string)
		if fl&int(termCluster) != 0 {
			out = append(out, tcCell{t: text, st: plain})
			if fl&int(vtWide) != 0 {
				out = append(out, tcCell{tail: true, st: plain})
			}
			continue
		}
		for _, r := range text {
			out = append(out, tcCell{t: string(r), st: plain})
			if fl&int(vtWide) != 0 {
				out = append(out, tcCell{tail: true, st: plain})
			}
		}
	}
	return out
}

func (c *termClient) push(l []tcCell) {
	c.sb = append(c.sb, l)
	if len(c.sb) > vtScrollbackMax {
		c.sb = c.sb[len(c.sb)-vtScrollbackMax:]
	}
}

// apply is the widget's frame handling, op for op.
func (c *termClient) apply(frame map[string]any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	base := int64(num(f["base"]))
	if base != -1 && base != c.seq {
		return fmt.Errorf("gap: frame on %d, widget at %d", base, c.seq)
	}
	c.seq = int64(num(f["seq"]))
	styles := f["st"].([]any)
	for _, o := range f["ops"].([]any) {
		op := o.([]any)
		switch op[0].(string) {
		case "z":
			c.cols, c.rows = num(op[1]), num(op[2])
			c.grid = make([][]tcCell, c.rows)
			for y := range c.grid {
				c.grid[y] = c.blankRow()
			}
		case "x":
			c.sb = nil
		case "p":
			for _, l := range op[1:] {
				c.push(decodeLine(styles, l.([]any)))
			}
		case "u", "d":
			top, bottom, n := num(op[1]), num(op[2]), num(op[3])
			k := min(n, bottom-top+1)
			if op[0] == "u" {
				switch p := op[4].(type) {
				case float64:
					if p == 1 {
						for i := 0; i < k; i++ {
							c.push(c.grid[top+i])
						}
					}
				case []any:
					for _, l := range p {
						c.push(decodeLine(styles, l.([]any)))
					}
				}
				copy(c.grid[top:], c.grid[top+k:bottom+1])
				for y := bottom - k + 1; y <= bottom; y++ {
					c.grid[y] = c.blankRow()
				}
			} else {
				copy(c.grid[top+k:bottom+1], c.grid[top:bottom-k+1])
				for y := top; y < top+k; y++ {
					c.grid[y] = c.blankRow()
				}
			}
		case "r":
			y, x := num(op[1]), num(op[2])
			row := append([]tcCell(nil), c.grid[y]...)
			cells := decodeLine(styles, op[3:])
			for i := x; i < c.cols; i++ {
				if i-x < len(cells) {
					row[i] = cells[i-x]
				} else {
					row[i] = tcCell{t: " ", st: [3]int{-1, -1, 0}}
				}
			}
			c.grid[y] = row
		case "c":
			c.cx, c.cy, c.cursorOn = num(op[1]), num(op[2]), num(op[3]) == 1
		case "t":
			c.title = op[1].(string)
		case "m":
			c.modes = num(op[1])
		case "b":
			c.bells += num(op[1])
		default:
			return fmt.Errorf("unknown op %v", op[0])
		}
	}
	return nil
}

func cellsText(l []tcCell) string {
	var b strings.Builder
	for _, c := range l {
		if !c.tail {
			b.WriteString(c.t)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// expectCells is what the widget must show for a screen row: the text of
// each cell and its style as the wire carries it.
func expectCells(l []vtCell) []tcCell {
	out := make([]tcCell, len(l))
	for x, c := range l {
		st := [3]int{wireColour(c.fg), wireColour(c.bg), int(c.fl & vtStyleMask)}
		switch {
		case c.fl&vtTail != 0 && x > 0 && l[x-1].fl&vtWide != 0:
			out[x] = tcCell{tail: true, st: st}
		case c.r == 0 || c.fl&vtTail != 0:
			out[x] = tcCell{t: " ", st: st}
		default:
			out[x] = tcCell{t: string(c.r) + c.comb, st: st}
		}
	}
	return out
}

// visual drops the style of a cell that draws nothing: a space with no
// background, inverse, underline or strike looks the same in any colour.
func visual(l []tcCell) []tcCell {
	out := make([]tcCell, len(l))
	for i, c := range l {
		if !c.tail && c.t == " " && c.st[1] == -1 && c.st[2]&int(vtInverse|vtUnderline|vtStrike) == 0 {
			c.st = [3]int{-1, -1, 0}
		}
		out[i] = c
	}
	return out
}

func (c *termClient) matches(t *testing.T, s *vtScreen, when string) bool {
	t.Helper()
	if c.cols != s.cols || c.rows != s.rows {
		t.Errorf("%s: widget %dx%d, screen %dx%d", when, c.cols, c.rows, s.cols, s.rows)
		return false
	}
	for y := 0; y < s.rows; y++ {
		want := visual(expectCells(s.lines[y]))
		if !reflect.DeepEqual(visual(c.grid[y]), want) {
			t.Errorf("%s: row %d differs:\n widget %q\n screen %q", when, y, cellsText(c.grid[y]), lineText(s.lines[y]))
			return false
		}
	}
	sb := s.scrollback()
	if len(c.sb) != len(sb) {
		t.Errorf("%s: scrollback %d lines, screen %d", when, len(c.sb), len(sb))
		return false
	}
	for i := range sb {
		if cellsText(c.sb[i]) != lineText(sb[i]) {
			t.Errorf("%s: scrollback line %d: widget %q, screen %q", when, i, cellsText(c.sb[i]), lineText(sb[i]))
			return false
		}
	}
	if c.cx != s.cx || c.cy != s.cy || c.cursorOn != s.cursorOn || c.title != s.title || c.modes != s.modes() {
		t.Errorf("%s: cursor/title/modes differ: widget %v, screen %v", when,
			[]any{c.cx, c.cy, c.cursorOn, c.title, c.modes}, []any{s.cx, s.cy, s.cursorOn, s.title, s.modes()})
		return false
	}
	return true
}

// streamCase feeds `in` in pieces, taking a frame after each piece, and
// checks the widget model after every frame.
func streamCase(t *testing.T, name string, cols, rows int, in []byte, rng *rand.Rand, resizes bool) (frames int, bytes int) {
	t.Helper()
	s := newVTScreen(cols, rows)
	sh := &termShadow{}
	cl := &termClient{}
	take := func(full bool) bool {
		f, ok := s.frame(sh, full)
		if !ok {
			return true
		}
		frames++
		b, _ := json.Marshal(f)
		bytes += len(b)
		if err := cl.apply(f); err != nil {
			t.Errorf("%s: %v", name, err)
			return false
		}
		return cl.matches(t, s, fmt.Sprintf("%s after frame %d", name, frames))
	}
	if !take(true) {
		return
	}
	for off := 0; off < len(in); {
		n := 1 + rng.Intn(4096)
		if off+n > len(in) {
			n = len(in) - off
		}
		s.feed(in[off : off+n])
		off += n
		if resizes && rng.Intn(20) == 0 {
			s.resize(10+rng.Intn(60), 3+rng.Intn(20))
		}
		if !take(rng.Intn(50) == 0) {
			return
		}
	}
	return
}

func randomTerminalBytes(rng *rand.Rand, n int) []byte {
	pieces := []string{
		"hello ", "world", "\r\n", "\n", "\r", "\t", "\b", "日本", "é", "é", "😀", "─│┌",
		"\x1b[H", "\x1b[2J", "\x1b[K", "\x1b[1K", "\x1b[J", "\x1b[3;5H", "\x1b[10;1H", "\x1b[A", "\x1b[5C",
		"\x1b[31m", "\x1b[1;4;7m", "\x1b[0m", "\x1b[38;5;200m", "\x1b[48;2;10;20;30m", "\x1b[m",
		"\x1b[2;8r", "\x1b[r", "\x1bM", "\x1bD", "\x1b[3S", "\x1b[2T", "\x1b[2L", "\x1b[2M", "\x1b[3P", "\x1b[2@",
		"\x1b[4X", "\x1b[?1049h", "\x1b[?1049l", "\x1b[?25l", "\x1b[?25h", "\x1b]0;title\x07", "\x07",
		"\x1b(0lqqk\x1b(B", "\x1b[?7l", "\x1b[?7h", "\x1b7", "\x1b8", "\x1b[3J", "\x1b[?2004h", "\x1b[4h", "\x1b[4l",
		"\x1b[5b", "0123456789abcdefghijklmnopqrstuvwxyz0123456789",
	}
	var b []byte
	for len(b) < n {
		b = append(b, pieces[rng.Intn(len(pieces))]...)
		if rng.Intn(6) == 0 {
			for k := rng.Intn(30); k > 0; k-- {
				b = append(b, fmt.Sprintf("line %d\r\n", rng.Intn(1000))...)
			}
		}
	}
	return b
}

// Every frame brings the widget model to the screen exactly: rows and
// styles, scrollback, cursor, title and modes; with random sequences cut at
// random points, random resizes, and random repaints in between.
func TestTermFrames_WidgetModelMatchesTheScreen(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		streamCase(t, fmt.Sprintf("random seed %d", seed), 20+rng.Intn(60), 4+rng.Intn(20), randomTerminalBytes(rng, 60000), rng, seed%2 == 0)
	}
	rng := rand.New(rand.NewSource(99))
	var seq []byte
	for i := 1; i <= 20000; i++ {
		seq = append(seq, fmt.Sprintf("%d\r\n", i)...)
	}
	streamCase(t, "seq", 80, 24, seq, rng, false)
	streamCase(t, "yes", 80, 24, []byte(strings.Repeat("y\r\n", 30000)), rng, false)
}

// A flood that scrolls the screen many times between two frames is sent as
// ONE scroll op plus the rows, and the lines that went to the scrollback
// are the exact lines.
func TestTermFrames_FloodCoalescesScrolls(t *testing.T) {
	s := newVTScreen(20, 5)
	sh := &termShadow{}
	cl := &termClient{}
	f, _ := s.frame(sh, true)
	if err := cl.apply(f); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5000; i++ {
		s.feedString(fmt.Sprintf("%d\r\n", i))
	}
	f, ok := s.frame(sh, false)
	if !ok {
		t.Fatal("no frame after a flood")
	}
	scrolls := 0
	for _, op := range f["ops"].([]any) {
		if op.([]any)[0] == "u" {
			scrolls++
		}
	}
	eqT(t, "one scroll op for 4996 scrolls", scrolls, 1)
	if err := cl.apply(f); err != nil {
		t.Fatal(err)
	}
	cl.matches(t, s, "after the flood")
	eqT(t, "the scrollback ends with the last line that left the screen", cellsText(cl.sb[len(cl.sb)-1]), "4996")
	b, _ := json.Marshal(f)
	if len(b) > 12000 {
		t.Errorf("the flood frame is %d bytes; it must be about one screen plus the %d scrollback lines", len(b), vtScrollbackMax)
	}
}

// A shadow that fell behind the journal (the journal is bounded) gets a
// repaint, and the widget still matches.
func TestTermFrames_JournalOverflowRepaints(t *testing.T) {
	s := newVTScreen(10, 6)
	sh := &termShadow{}
	cl := &termClient{}
	f, _ := s.frame(sh, true)
	_ = cl.apply(f)
	for i := 0; i < vtJournalMax+50; i++ {
		s.feedString("\x1b[2;4r\x1b[4;1H\x1bD\x1b[3;5r\x1b[3;1H\x1bM\x1b[r")
	}
	f, ok := s.frame(sh, false)
	if !ok || num(jsonRound(f)["base"]) != -1 {
		t.Fatalf("a shadow behind the journal must get a repaint (base -1), got %v", f["base"])
	}
	if err := cl.apply(f); err != nil {
		t.Fatal(err)
	}
	cl.matches(t, s, "after the repaint")
}

func jsonRound(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// A widget that missed a frame sees the gap on the next frame (or on the
// check frame), and a repaint brings it back.
func TestTermFrames_MissedFrameIsDetected(t *testing.T) {
	s := newVTScreen(10, 3)
	sh := &termShadow{}
	cl := &termClient{}
	f, _ := s.frame(sh, true)
	_ = cl.apply(f)
	s.feedString("one")
	_, _ = s.frame(sh, false) // lost on the way
	if err := cl.apply(sh.checkFrame()); err == nil {
		t.Fatal("the check frame after a lost frame must show a gap")
	}
	s.feedString(" two")
	f, _ = s.frame(sh, false)
	if err := cl.apply(f); err == nil {
		t.Fatal("a frame after a lost one must show a gap")
	}
	f, _ = s.frame(sh, true)
	if err := cl.apply(f); err != nil {
		t.Fatal(err)
	}
	cl.matches(t, s, "after the repaint")
	if err := cl.apply(sh.checkFrame()); err != nil {
		t.Fatalf("a check frame on an up-to-date widget: %v", err)
	}
}

func TestTermFrames_RowOpsAreMinimal(t *testing.T) {
	s := vtOf(40, 5, "\x1b[31mred\x1b[0m plain \x1b[1mbold")
	sh := &termShadow{}
	f, _ := s.frame(sh, true)
	eqT(t, "the first row is three runs", jsonRound(f)["ops"].([]any)[2], []any{"r", 0.0, 0.0, 0.0, "red", 1.0, " plain ", 2.0, "bold"})
	s.feedString("\x1b[3;5HX")
	f, _ = s.frame(sh, false)
	ops := jsonRound(f)["ops"].([]any)
	eqT(t, "one changed cell is one row op from its column, and the cursor", ops, []any{[]any{"r", 2.0, 4.0, 0.0, "X"}, []any{"c", 5.0, 2.0, 1.0}})
	s.feedString("\x1b[3;1H\x1b[K")
	f, _ = s.frame(sh, false)
	eqT(t, "a cleared cell at the end of a row is an op with no runs", jsonRound(f)["ops"].([]any)[0], []any{"r", 2.0, 4.0})
	if _, ok := s.frame(sh, false); ok {
		t.Error("no change: no frame")
	}
	s = vtOf(10, 2, "日x")
	f, _ = s.frame(&termShadow{}, true)
	m := jsonRound(f)
	eqT(t, "a wide character is a run with the wide style", m["ops"].([]any)[2], []any{"r", 0.0, 0.0, 0.0, "日", 1.0, "x"})
	eqT(t, "the wide style has flag 64", m["st"].([]any)[0], []any{-1.0, -1.0, 64.0})
}

func TestTermFrames_Bell(t *testing.T) {
	s := newVTScreen(10, 2)
	sh := &termShadow{}
	_, _ = s.frame(sh, true)
	s.feedString("\x07\x07")
	f, _ := s.frame(sh, false)
	eqT(t, "the bell count", jsonRound(f)["ops"].([]any), []any{[]any{"b", 2.0}})
	s.feedString("\x07")
	f, _ = s.frame(sh, true)
	for _, op := range jsonRound(f)["ops"].([]any) {
		if op.([]any)[0] == "b" {
			t.Error("a repaint does not ring the bell")
		}
	}
}
