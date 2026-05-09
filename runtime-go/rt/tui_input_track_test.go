package rt

import "testing"

// paintInputBufferAdvanced should paint a light ░ track across the
// input's range so empty cells are visible. Real characters paint
// over the track; cursor inverts the cell.
func TestPaintInputTrack_Empty(t *testing.T) {
	grid := makeTestGrid(20, 1)
	st := &tuiInput{buffer: "", cursor: 0}
	paintInputBufferAdvanced(grid, st, 2, 0, 10, 1, textStyle{}, "", true, false, false)

	// Cells 2..11 should be track + cursor at col 2.
	if grid[0][2].ch == "░" && grid[0][2].reverse {
		// cursor cell should be reversed (not necessarily ░ — could be space)
	} else if grid[0][2].ch != "░" && grid[0][2].ch != " " {
		t.Errorf("col=2 ch=%q want ░ or reversed cursor", grid[0][2].ch)
	}
	tracks := 0
	for c := 2; c < 12; c++ {
		if grid[0][c].ch == "░" {
			tracks++
		}
	}
	if tracks < 8 {
		t.Errorf("expected ~9 ░ track cells (one cell is the cursor), got %d", tracks)
	}
}

// Track should NOT show on a non-input cell — outside the input's
// `col..col+w` range cells stay empty.
func TestPaintInputTrack_OutsideRangeUntouched(t *testing.T) {
	grid := makeTestGrid(20, 1)
	st := &tuiInput{buffer: "", cursor: 0}
	paintInputBufferAdvanced(grid, st, 5, 0, 10, 1, textStyle{}, "", false, false, false)

	if grid[0][0].ch == "░" || grid[0][4].ch == "░" {
		t.Error("track painted outside the input range (col<5)")
	}
	if grid[0][15].ch == "░" || grid[0][19].ch == "░" {
		t.Error("track painted outside the input range (col>=15)")
	}
}

// Real characters should paint OVER the track — typed text shows
// in the terminal's default fg, not the dim track grey.
func TestPaintInputTrack_RealCharsOverridesTrack(t *testing.T) {
	grid := makeTestGrid(20, 1)
	st := &tuiInput{buffer: "hi", cursor: 2}
	paintInputBufferAdvanced(grid, st, 0, 0, 10, 1, textStyle{}, "", true, false, false)

	if grid[0][0].ch != "h" {
		t.Errorf("col=0 ch=%q want h", grid[0][0].ch)
	}
	if grid[0][1].ch != "i" {
		t.Errorf("col=1 ch=%q want i", grid[0][1].ch)
	}
	// Tracks beyond typed chars.
	tracks := 0
	for c := 2; c < 10; c++ {
		if grid[0][c].ch == "░" {
			tracks++
		}
	}
	if tracks < 7 {
		t.Errorf("expected track from col=2..9 (one is cursor), got %d ░ cells", tracks)
	}
	// 'h' should NOT have the dim track fg leaking through. Track
	// fg is grey 110/110/110; default fg is unset (zero value).
	if grid[0][0].fg.set && grid[0][0].fg.r == 110 {
		t.Error("typed 'h' inherited the track fg colour — paintInputLine didn't reset")
	}
}

// Track adapts to the input's bg when set: 15%-lighter shade so it
// has subtle contrast against the input's solid bg.
func TestPaintInputTrack_AdaptsToBgColor(t *testing.T) {
	grid := makeTestGrid(20, 1)
	st := &tuiInput{buffer: "", cursor: 0}
	bgStyle := textStyle{bg: tuiColor{set: true, r: 30, g: 36, b: 60}}
	paintInputBufferAdvanced(grid, st, 0, 0, 5, 1, bgStyle, "", false, false, false)

	if grid[0][1].ch != "░" {
		t.Errorf("col=1 ch=%q want ░", grid[0][1].ch)
	}
	// Track fg should be lightened bg, NOT the default grey 110.
	if grid[0][1].fg.r == 110 && grid[0][1].fg.g == 110 && grid[0][1].fg.b == 110 {
		t.Error("track used default grey when bg was set; expected lightened bg")
	}
	// Track bg should match the input's bg (not pass through to terminal).
	if !grid[0][1].bg.set || grid[0][1].bg.r != 30 {
		t.Error("track did not inherit input's bg colour")
	}
}

// Checkbox renders a single ☐ / ☑ glyph (not the legacy `[ ]`).
// Focus state inverts via `reverse` rather than a leading arrow.
func TestPaintCheckbox_GlyphAndFocus(t *testing.T) {
	grid := makeTestGrid(10, 1)
	box := layoutBox{tag: "input", inputType: "checkbox", valueAttr: "false"}
	paintCheckbox(grid, box, 3, 0, 1, textStyle{}, false)
	if grid[0][3].ch != "☐" {
		t.Errorf("unchecked: ch=%q want ☐", grid[0][3].ch)
	}
	if grid[0][3].reverse {
		t.Error("unchecked unfocused should NOT be reversed")
	}
	// Now checked + focused.
	box2 := layoutBox{tag: "input", inputType: "checkbox", valueAttr: "true"}
	paintCheckbox(grid, box2, 3, 0, 1, textStyle{}, true)
	if grid[0][3].ch != "☑" {
		t.Errorf("checked: ch=%q want ☑", grid[0][3].ch)
	}
	if !grid[0][3].reverse {
		t.Error("focused checked should be reversed")
	}
}

// Radio: ○ / ●, focus inverts.
func TestPaintRadio_GlyphAndFocus(t *testing.T) {
	grid := makeTestGrid(10, 1)
	box := layoutBox{tag: "input", inputType: "radio", valueAttr: ""}
	paintRadio(grid, box, 0, 0, 1, textStyle{}, false)
	if grid[0][0].ch != "○" {
		t.Errorf("unselected: ch=%q want ○", grid[0][0].ch)
	}
	box2 := layoutBox{tag: "input", inputType: "radio", valueAttr: "green"}
	paintRadio(grid, box2, 0, 0, 1, textStyle{}, true)
	if grid[0][0].ch != "●" {
		t.Errorf("selected: ch=%q want ●", grid[0][0].ch)
	}
	if !grid[0][0].reverse {
		t.Error("focused selected radio should be reversed")
	}
}

// `<a>` links must join the focus order even without explicit
// event handlers — `Ui.link` builds a TaggedNode "a" with just an
// `href` attribute, but it should still be reachable by Tab and
// arrow navigation, matching HTML's intrinsic link tab-stop.
func TestPaintBox_LinkIsFocusable(t *testing.T) {
	grid := makeTestGrid(20, 1)
	link := layoutBox{
		kind:   "node",
		tag:    "a",
		width:  10, height: 1,
		axis: layoutAxisRow,
		// Note: empty events slice — Ui.link sets href but no onClick.
		children: []layoutBox{
			{kind: "text", text: "click me", width: 8, height: 1},
		},
	}
	var focusables []focusable
	inputs := newInputRegistry()
	paintBox(grid, link, 0, 0, 20, 1, -1, &focusables, inputs, textStyle{}, layoutAxisColumn, 0)
	if len(focusables) != 1 {
		t.Fatalf("link with no events should still be focusable; got %d focusables", len(focusables))
	}
	if focusables[0].isInput {
		t.Error("link should not be marked as isInput")
	}
}

// When the link is focused, applyFocusIndicator should underline
// the entire content row so users see the focus state at a glance.
func TestPaintBox_FocusedLinkUnderlines(t *testing.T) {
	grid := makeTestGrid(20, 1)
	link := layoutBox{
		kind:   "node",
		tag:    "a",
		width:  10, height: 1,
		axis: layoutAxisRow,
		children: []layoutBox{
			{kind: "text", text: "go", width: 2, height: 1},
		},
	}
	var focusables []focusable
	inputs := newInputRegistry()
	// focusIdx = 0 → this link IS the focused element.
	paintBox(grid, link, 0, 0, 20, 1, 0, &focusables, inputs, textStyle{}, layoutAxisColumn, 0)
	// Some cell in the link's bounding box should have underline=true.
	underlined := 0
	for c := 0; c < 10; c++ {
		if grid[0][c].underline {
			underlined++
		}
	}
	if underlined == 0 {
		t.Error("focused link should apply underline to its content row")
	}
}

func makeTestGrid(cols, rows int) [][]tuiCell {
	g := make([][]tuiCell, rows)
	for r := range g {
		g[r] = make([]tuiCell, cols)
	}
	return g
}
