package rt

import "testing"

func TestTuiEditInput_CharInsert(t *testing.T) {
	st := &tuiInput{buffer: "", cursor: 0}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "char", value: "h"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "h" {
		t.Errorf("buffer=%q, want %q", st.buffer, "h")
	}
	if st.cursor != 1 {
		t.Errorf("cursor=%d, want 1", st.cursor)
	}
}

func TestTuiEditInput_CharInsertMidBuffer(t *testing.T) {
	st := &tuiInput{buffer: "helo", cursor: 3}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "char", value: "l"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "hello" {
		t.Errorf("buffer=%q, want %q", st.buffer, "hello")
	}
	if st.cursor != 4 {
		t.Errorf("cursor=%d, want 4", st.cursor)
	}
}

func TestTuiEditInput_Backspace(t *testing.T) {
	st := &tuiInput{buffer: "hello", cursor: 5}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "backspace"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "hell" {
		t.Errorf("buffer=%q, want %q", st.buffer, "hell")
	}
	if st.cursor != 4 {
		t.Errorf("cursor=%d, want 4", st.cursor)
	}
}

func TestTuiEditInput_BackspaceAtZero(t *testing.T) {
	st := &tuiInput{buffer: "hello", cursor: 0}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "backspace"}, f)
	if changed {
		t.Error("backspace at cursor=0 shouldn't change buffer")
	}
	if st.buffer != "hello" {
		t.Errorf("buffer changed: %q", st.buffer)
	}
}

func TestTuiEditInput_Delete(t *testing.T) {
	st := &tuiInput{buffer: "hello", cursor: 2}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "delete"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "helo" {
		t.Errorf("buffer=%q, want %q", st.buffer, "helo")
	}
	if st.cursor != 2 {
		t.Errorf("cursor=%d, want 2 (unchanged)", st.cursor)
	}
}

func TestTuiEditInput_LeftRight(t *testing.T) {
	st := &tuiInput{buffer: "hello", cursor: 2}
	f := focusable{isInput: true, inputType: "text"}
	tuiEditInput(st, keyEvent{kind: "left"}, f)
	if st.cursor != 1 {
		t.Errorf("after left: cursor=%d, want 1", st.cursor)
	}
	tuiEditInput(st, keyEvent{kind: "right"}, f)
	tuiEditInput(st, keyEvent{kind: "right"}, f)
	if st.cursor != 3 {
		t.Errorf("after right×2: cursor=%d, want 3", st.cursor)
	}
}

func TestTuiEditInput_HomeEnd(t *testing.T) {
	st := &tuiInput{buffer: "hello", cursor: 2}
	f := focusable{isInput: true, inputType: "text"}
	tuiEditInput(st, keyEvent{kind: "home"}, f)
	if st.cursor != 0 {
		t.Errorf("after home: cursor=%d, want 0", st.cursor)
	}
	tuiEditInput(st, keyEvent{kind: "end"}, f)
	if st.cursor != 5 {
		t.Errorf("after end: cursor=%d, want 5", st.cursor)
	}
}

func TestTuiEditInput_Multibyte(t *testing.T) {
	// Insert é (rune 0xe9) at start of empty buffer.
	st := &tuiInput{buffer: "", cursor: 0}
	f := focusable{isInput: true, inputType: "text"}
	tuiEditInput(st, keyEvent{kind: "char", value: "é"}, f)
	if st.buffer != "é" {
		t.Errorf("buffer=%q, want é", st.buffer)
	}
	if st.cursor != 1 { // cursor counts runes, not bytes
		t.Errorf("cursor=%d, want 1", st.cursor)
	}
	// Insert another rune.
	tuiEditInput(st, keyEvent{kind: "char", value: "你"}, f)
	if st.buffer != "é你" {
		t.Errorf("buffer=%q, want é你", st.buffer)
	}
	if st.cursor != 2 {
		t.Errorf("cursor=%d, want 2", st.cursor)
	}
	// Backspace removes one rune (3 bytes for 你).
	tuiEditInput(st, keyEvent{kind: "backspace"}, f)
	if st.buffer != "é" {
		t.Errorf("after backspace: buffer=%q, want é", st.buffer)
	}
}

func TestTuiEditInput_SpaceInserts(t *testing.T) {
	st := &tuiInput{buffer: "hi", cursor: 2}
	f := focusable{isInput: true, inputType: "text"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "space"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "hi " {
		t.Errorf("buffer=%q, want %q", st.buffer, "hi ")
	}
}

func TestTuiEditInput_MultilineEnter(t *testing.T) {
	st := &tuiInput{buffer: "ab", cursor: 1}
	f := focusable{isInput: true, inputType: "textarea"}
	changed, _ := tuiEditInput(st, keyEvent{kind: "enter"}, f)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if st.buffer != "a\nb" {
		t.Errorf("buffer=%q, want %q", st.buffer, "a\nb")
	}
	if st.cursor != 2 {
		t.Errorf("cursor=%d, want 2", st.cursor)
	}
}

func TestTuiEditInput_MultilineUpDown(t *testing.T) {
	// Buffer:
	//   line1
	//   line2
	//   x
	st := &tuiInput{buffer: "line1\nline2\nx", cursor: 13} // end
	f := focusable{isInput: true, inputType: "textarea"}
	// Up: should move to line2's column position. We're at "x" col 0
	// (after \n) → no wait, cursor=13 is end of x → line=2 col=1.
	tuiEditInput(st, keyEvent{kind: "up"}, f)
	// Now should be at line=1 col=1 → cursor=7 (l-i)
	if st.cursor != 7 {
		t.Errorf("after up: cursor=%d, want 7", st.cursor)
	}
	tuiEditInput(st, keyEvent{kind: "up"}, f)
	// line=0 col=1 → cursor=1
	if st.cursor != 1 {
		t.Errorf("after up×2: cursor=%d, want 1", st.cursor)
	}
	// Up at first line: no-op.
	tuiEditInput(st, keyEvent{kind: "up"}, f)
	if st.cursor != 1 {
		t.Errorf("after up at top: cursor=%d, want 1 (unchanged)", st.cursor)
	}
}

func TestCursorLocate(t *testing.T) {
	tests := []struct {
		text     string
		cursor   int
		wantLine int
		wantCol  int
	}{
		{"abc", 0, 0, 0},
		{"abc", 2, 0, 2},
		{"abc", 3, 0, 3},
		{"a\nb", 0, 0, 0},
		{"a\nb", 1, 0, 1},
		{"a\nb", 2, 1, 0},
		{"a\nb", 3, 1, 1},
		{"line1\nline2", 7, 1, 1},
	}
	for _, tt := range tests {
		l, c := cursorLocate(tt.text, tt.cursor)
		if l != tt.wantLine || c != tt.wantCol {
			t.Errorf("cursorLocate(%q, %d) = (%d, %d), want (%d, %d)",
				tt.text, tt.cursor, l, c, tt.wantLine, tt.wantCol)
		}
	}
}

func TestAlignOffset(t *testing.T) {
	tests := []struct {
		align string
		slack int
		want  int
	}{
		{"", 10, 0},
		{"left", 10, 0},
		{"top", 10, 0},
		{"center", 10, 5},
		{"center", 11, 5}, // floor(11/2)
		{"right", 10, 10},
		{"bottom", 10, 10},
		{"center", 0, 0},
		{"center", -5, 0}, // negative slack clamps
	}
	for _, tt := range tests {
		got := alignOffset(tt.align, tt.slack, true)
		if got != tt.want {
			t.Errorf("alignOffset(%q, %d) = %d, want %d", tt.align, tt.slack, got, tt.want)
		}
	}
}

func TestTuiWarn_DedupesByKey(t *testing.T) {
	tuiResetWarnings()
	tuiWarn("font", "size")
	tuiWarn("font", "size") // dup
	tuiWarn("font", "size") // dup
	tuiWarn("font", "family")
	tuiWarn("background", "gradient")

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if len(tuiWarnSeen) != 3 {
		t.Errorf("expected 3 unique warnings, got %d", len(tuiWarnSeen))
	}
	if w := tuiWarnSeen["font:size"]; w == nil || w.count != 3 {
		t.Errorf("font:size count = %v, want 3", w)
	}
	if w := tuiWarnSeen["font:family"]; w == nil || w.count != 1 {
		t.Errorf("font:family count = %v, want 1", w)
	}
}
