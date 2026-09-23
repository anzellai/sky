//go:build !js

package rt

import "testing"

// A focused button whose label fills its inner width keeps every label
// cell: the ▸ ◂ markers may only take blank cells, else the focus shows as
// reverse video (T5). Pre-fix the markers overwrote "O" and "K".
func TestPaintBox_FocusMarkersKeepLabel(t *testing.T) {
	grid := newCellGrid(10, 1)
	btn := layoutBox{
		kind: "node", tag: "button", width: 2, height: 1, axis: layoutAxisRow,
		events:   []any{eventPair{name: "click", msg: "ok"}},
		children: []layoutBox{{kind: "text", text: "OK", width: 2, height: 1}},
	}
	var focusables []focusable
	paintBox(grid, btn, 0, 0, 10, 1, 0, &focusables, newInputRegistry(), textStyle{}, layoutAxisColumn, 0)
	if grid[0][0].ch != "O" || grid[0][1].ch != "K" {
		t.Fatalf("focused button label = %q%q, want OK", grid[0][0].ch, grid[0][1].ch)
	}
	if !grid[0][0].reverse || !grid[0][1].reverse {
		t.Fatalf("focused button shows no focus state")
	}
	// With room for them (padding), the markers still frame the label.
	grid = newCellGrid(10, 1)
	btn.width, btn.padding = 6, [4]int{0, 2, 0, 2}
	focusables = nil
	paintBox(grid, btn, 0, 0, 10, 1, 0, &focusables, newInputRegistry(), textStyle{}, layoutAxisColumn, 0)
	if grid[0][2].ch != "O" || grid[0][3].ch != "K" || grid[0][0].ch == "O" {
		t.Fatalf("padded label moved or overwritten: %q", gridText(grid))
	}
}
