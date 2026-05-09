package rt

import "testing"

func TestTuiDecodeKey_NamedKeys(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string // kind
	}{
		{"enter (CR)", []byte{0x0d}, "enter"},
		{"enter (LF)", []byte{0x0a}, "enter"},
		{"tab", []byte{0x09}, "tab"},
		{"space", []byte{0x20}, "space"},
		{"backspace (DEL)", []byte{0x7f}, "backspace"},
		{"backspace (BS)", []byte{0x08}, "backspace"},
		{"solo escape", []byte{0x1b}, "escape"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, _ := tuiDecodeKey(tt.input)
			if ev.kind != tt.want {
				t.Errorf("got kind=%q, want %q", ev.kind, tt.want)
			}
		})
	}
}

func TestTuiDecodeKey_ArrowKeys(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"up", []byte{0x1b, '[', 'A'}, "up"},
		{"down", []byte{0x1b, '[', 'B'}, "down"},
		{"right", []byte{0x1b, '[', 'C'}, "right"},
		{"left", []byte{0x1b, '[', 'D'}, "left"},
		{"home (CSI H)", []byte{0x1b, '[', 'H'}, "home"},
		{"end (CSI F)", []byte{0x1b, '[', 'F'}, "end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, n := tuiDecodeKey(tt.input)
			if ev.kind != tt.want {
				t.Errorf("got kind=%q, want %q", ev.kind, tt.want)
			}
			if n != 3 {
				t.Errorf("expected to consume 3 bytes, got %d", n)
			}
		})
	}
}

func TestTuiDecodeKey_CsiTilde(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"home", "\x1b[1~", "home"},
		{"end", "\x1b[4~", "end"},
		{"delete", "\x1b[3~", "delete"},
		{"pageup", "\x1b[5~", "pageup"},
		{"pagedown", "\x1b[6~", "pagedown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, _ := tuiDecodeKey([]byte(tt.input))
			if ev.kind != tt.want {
				t.Errorf("input %q: got kind=%q, want %q", tt.input, ev.kind, tt.want)
			}
		})
	}
}

func TestTuiDecodeKey_CtrlLetters(t *testing.T) {
	for i := 1; i <= 26; i++ {
		// Skip control codes the decoder treats as named keys at
		// higher priority: Tab (9), LF (10), CR (13), and Ctrl-H/BS (8).
		if i == 8 || i == 9 || i == 10 || i == 13 {
			continue
		}
		ev, n := tuiDecodeKey([]byte{byte(i)})
		if ev.kind != "ctrl" {
			t.Errorf("byte 0x%02x: got kind=%q, want ctrl", i, ev.kind)
			continue
		}
		expectedLetter := string(rune('a' + i - 1))
		if ev.value != expectedLetter {
			t.Errorf("byte 0x%02x: got value=%q, want %q", i, ev.value, expectedLetter)
		}
		if n != 1 {
			t.Errorf("byte 0x%02x: consumed %d, want 1", i, n)
		}
	}
}

func TestTuiDecodeKey_PlainChar(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"a", "a"},
		{"Z", "Z"},
		{"5", "5"},
		{"é", "é"},
		{"日", "日"},
	}
	for _, tt := range tests {
		ev, _ := tuiDecodeKey([]byte(tt.input))
		if ev.kind != "char" || ev.value != tt.want {
			t.Errorf("input %q: got kind=%q value=%q; want char/%q", tt.input, ev.kind, ev.value, tt.want)
		}
	}
}

func TestTuiDecodeKey_ShiftTab(t *testing.T) {
	// Shift-Tab is reported as ESC [ Z by xterm-likes. Our decoder
	// emits it as kind="other" so the main loop can recognise it.
	ev, n := tuiDecodeKey([]byte{0x1b, '[', 'Z'})
	if ev.kind != "other" {
		t.Errorf("got kind=%q, want other", ev.kind)
	}
	if ev.value != "\x1b[Z" {
		t.Errorf("got value=%q, want \\x1b[Z", ev.value)
	}
	if n != 3 {
		t.Errorf("consumed %d bytes, want 3", n)
	}
}

func TestTuiDecodeKey_SgrMouse(t *testing.T) {
	// SGR mouse press: ESC [ < button ; col ; row M
	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantValue string
	}{
		{
			name:      "left press at 5,3",
			input:     "\x1b[<0;5;3M",
			wantKind:  "mouse",
			wantValue: "0;5;3:M",
		},
		{
			name:      "left release at 5,3",
			input:     "\x1b[<0;5;3m",
			wantKind:  "mouse",
			wantValue: "0;5;3:m",
		},
		{
			name:      "right press at 100,40",
			input:     "\x1b[<2;100;40M",
			wantKind:  "mouse",
			wantValue: "2;100;40:M",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, _ := tuiDecodeKey([]byte(tt.input))
			if ev.kind != tt.wantKind {
				t.Errorf("got kind=%q, want %q", ev.kind, tt.wantKind)
			}
			if ev.value != tt.wantValue {
				t.Errorf("got value=%q, want %q", ev.value, tt.wantValue)
			}
		})
	}
}

func TestParseMouseEvent(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantBtn    int
		wantCol    int
		wantRow    int
		wantPress  bool
		wantOk     bool
	}{
		{"basic press", "0;5;3:M", 0, 5, 3, true, true},
		{"basic release", "0;5;3:m", 0, 5, 3, false, true},
		{"right press", "2;10;20:M", 2, 10, 20, true, true},
		{"malformed no parts", "5", 0, 0, 0, false, false},
		{"malformed no suffix", "0;5;3", 0, 0, 0, false, false},
		{"malformed bad nums", "x;y;z:M", 0, 0, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, c, r, isPress, ok := parseMouseEvent(tt.input)
			if ok != tt.wantOk {
				t.Errorf("ok=%v, want %v", ok, tt.wantOk)
				return
			}
			if !ok {
				return
			}
			if b != tt.wantBtn {
				t.Errorf("btn=%d, want %d", b, tt.wantBtn)
			}
			if c != tt.wantCol {
				t.Errorf("col=%d, want %d", c, tt.wantCol)
			}
			if r != tt.wantRow {
				t.Errorf("row=%d, want %d", r, tt.wantRow)
			}
			if isPress != tt.wantPress {
				t.Errorf("press=%v, want %v", isPress, tt.wantPress)
			}
		})
	}
}

func TestHitTestFocusables(t *testing.T) {
	focusables := []focusable{
		{col: 0, row: 0, w: 10, h: 1},  // top bar
		{col: 0, row: 2, w: 5, h: 1},   // left button
		{col: 6, row: 2, w: 5, h: 1},   // right button
	}
	tests := []struct {
		name     string
		col, row int
		want     int
	}{
		{"hit top bar at start", 0, 0, 0},
		{"hit top bar at end", 9, 0, 0},
		{"miss top bar (just past)", 10, 0, -1},
		{"hit left button", 2, 2, 1},
		{"hit right button", 8, 2, 2},
		{"miss between buttons", 5, 2, -1},
		{"miss above all", 5, -1, -1},
		{"miss below all", 5, 100, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hitTestFocusables(focusables, tt.col, tt.row)
			if got != tt.want {
				t.Errorf("hitTest(%d,%d) = %d, want %d", tt.col, tt.row, got, tt.want)
			}
		})
	}
}
