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

func makeTestGrid(cols, rows int) [][]tuiCell {
	g := make([][]tuiCell, rows)
	for r := range g {
		g[r] = make([]tuiCell, cols)
	}
	return g
}
