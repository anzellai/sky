package rt

import "testing"

// CJK chars take 2 display cells each. A row of "日本語" should
// measure 6 cells, not 3.
func TestDisplayWidth_CJK(t *testing.T) {
	if w := displayWidth("日本語"); w != 6 {
		t.Errorf("displayWidth(\"日本語\") = %d, want 6", w)
	}
}

// Common emoji are width=2.
func TestDisplayWidth_Emoji(t *testing.T) {
	if w := displayWidth("🚀"); w != 2 {
		t.Errorf("displayWidth(\"🚀\") = %d, want 2", w)
	}
}

// ZWJ sequences (family emoji) collapse to a single cluster of
// width 2 visually. uniseg + the rendering libraries we target
// agree on this.
func TestDisplayWidth_ZWJ(t *testing.T) {
	// 👨‍👩‍👧‍👦 — man + ZWJ + woman + ZWJ + girl + ZWJ + boy.
	w := displayWidth("👨‍👩‍👧‍👦")
	if w != 2 {
		t.Errorf("displayWidth family-emoji = %d, want 2", w)
	}
}

// Combining marks contribute 0 width — the base char already
// owns its cell and the mark stacks on top.
func TestDisplayWidthRune_CombiningMark(t *testing.T) {
	if w := displayWidthRune(0x0301); w != 0 {
		t.Errorf("displayWidthRune(combining acute) = %d, want 0", w)
	}
}

// iterGraphemes treats a ZWJ-joined cluster as ONE iteration with
// width 2 — caller plays one cell, advances 2 columns.
func TestIterGraphemes_ZWJOneCluster(t *testing.T) {
	count := 0
	totalW := 0
	iterGraphemes("👨‍👩‍👧", func(cluster string, w int) bool {
		count++
		totalW += w
		return true
	})
	if count != 1 {
		t.Errorf("ZWJ family yielded %d clusters, want 1", count)
	}
	if totalW != 2 {
		t.Errorf("ZWJ family total width %d, want 2", totalW)
	}
}

// Cursor at the end of "日本" lands on column 4 (each char takes 2
// cells), not column 2.
func TestCursorLocate_WideChars(t *testing.T) {
	line, col := cursorLocate("日本", 2)
	if line != 0 || col != 4 {
		t.Errorf("cursorLocate(\"日本\", 2) = (%d, %d), want (0, 4)", line, col)
	}
}

// Cursor in the middle of a wide-char line: after "日" but before
// "本" → column 2.
func TestCursorLocate_WideMidLine(t *testing.T) {
	_, col := cursorLocate("日本", 1)
	if col != 2 {
		t.Errorf("cursorLocate(\"日本\", 1) col = %d, want 2", col)
	}
}

// paintText writing "日" at col=0 should fill cell 0 with the
// cluster and cell 1 with empty (continuation) so paintDiff doesn't
// double-emit.
func TestPaintText_WideCharLeavesContinuation(t *testing.T) {
	grid := makeTestGrid(10, 1)
	paintText(grid, "日", 0, 0, 10, textStyle{})
	if grid[0][0].ch != "日" {
		t.Errorf("col 0 ch=%q want 日", grid[0][0].ch)
	}
	if grid[0][1].ch != "" {
		t.Errorf("col 1 ch=%q want \"\" (continuation)", grid[0][1].ch)
	}
	// Subsequent cells untouched.
	if grid[0][2].ch != "" || grid[0][3].ch != "" {
		t.Error("cells past the wide char should be untouched")
	}
}

// "日a" → col0 wide, col1 continuation, col2 narrow.
func TestPaintText_WideThenNarrow(t *testing.T) {
	grid := makeTestGrid(10, 1)
	paintText(grid, "日a", 0, 0, 10, textStyle{})
	if grid[0][0].ch != "日" || grid[0][1].ch != "" || grid[0][2].ch != "a" {
		t.Errorf("got [%q,%q,%q] want [日, \"\", a]",
			grid[0][0].ch, grid[0][1].ch, grid[0][2].ch)
	}
}

// Wide char that would overflow the slot → substitute single space
// rather than truncating mid-glyph (would paint the wide char's
// first half with no second half — visually broken).
func TestPaintText_WideAtRightEdgeSubstitutes(t *testing.T) {
	grid := makeTestGrid(10, 1)
	// Slot is col 0 width 1 — only 1 cell available, but 日 is 2 wide.
	paintText(grid, "日", 0, 0, 1, textStyle{})
	if grid[0][0].ch != " " {
		t.Errorf("got %q, want space (substitution)", grid[0][0].ch)
	}
}
