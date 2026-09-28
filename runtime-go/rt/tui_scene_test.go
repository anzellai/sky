//go:build !js

package rt

import (
	"strings"
	"testing"
)

func sceneEl(tag string, attrs map[string]string, kids ...VNode) VNode {
	return VNode{Kind: "element", Tag: tag, Attrs: attrs, Children: kids}
}

func sceneText(cells [][]tuiSceneCell) []string {
	out := make([]string, len(cells))
	for r, row := range cells {
		var sb strings.Builder
		for _, c := range row {
			if c.ch == "" {
				sb.WriteByte('.')
			} else {
				sb.WriteString(c.ch)
			}
		}
		out[r] = sb.String()
	}
	return out
}

func checkScene(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\ngot\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A 20×10 scene in 10×5 cells: 20×20 dots, one dot per unit across, two
// down. The golden pins fill, stroke, a transform, text and the colours.
func TestTuiScene_CellGolden(t *testing.T) {
	svg := sceneEl("svg", map[string]string{"data-sky-scene": "1", "viewBox": "0 0 20 10", "width": "20", "height": "10"},
		sceneEl("title", nil, vtext("golden")),
		// A filled 4×2 square at the origin: the dots 0..3 × 0..3, two full cells.
		sceneEl("rect", map[string]string{"x": "0", "y": "0", "width": "4", "height": "2", "fill": "rgba(255, 0, 0, 1)"}),
		// A horizontal line along the bottom: the dot row 18 (y 9 × 2).
		sceneEl("line", map[string]string{"x1": "0", "y1": "9", "x2": "19", "y2": "9", "stroke": "#00ff00"}),
		// The same square, translated right by 16: cells 8 and 9 of row 0.
		sceneEl("g", map[string]string{"transform": "translate(16 0)", "fill": "#0000ff"},
			sceneEl("rect", map[string]string{"x": "0", "y": "0", "width": "4", "height": "2"})),
		// Text with its baseline at y 6 (dot row 12): cell row 2, from column 3.
		sceneEl("text", map[string]string{"x": "6", "y": "6"}, vtext("Hi")),
		// A transparent backdrop draws nothing.
		sceneEl("rect", map[string]string{"x": "0", "y": "0", "width": "20", "height": "10", "fill": "transparent"}),
	)
	cells := rasterScene(svg, 10, 5)
	checkScene(t, "golden", sceneText(cells), []string{
		"⣿⣿......⣿⣿",
		"..........",
		"...Hi.....",
		"..........",
		"⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤",
	})
	if c := cells[0][0].fg; !c.set || c.r != 255 || c.g != 0 || c.b != 0 {
		t.Fatalf("the red square's cell has colour %+v", c)
	}
	if c := cells[0][9].fg; !c.set || c.b != 255 || c.r != 0 {
		t.Fatalf("the group's fill did not reach its rect: %+v", c)
	}
	if c := cells[4][5].fg; !c.set || c.g != 255 {
		t.Fatalf("the line's cell has colour %+v", c)
	}
}

// Circles and paths (curves, arcs, relative commands, close) rasterise to
// the dots inside them; rotation and scale transform the points.
func TestTuiScene_ShapesAndTransforms(t *testing.T) {
	svg := sceneEl("svg", map[string]string{"data-sky-scene": "1", "viewBox": "0 0 20 20", "width": "20", "height": "20"},
		sceneEl("circle", map[string]string{"cx": "10", "cy": "10", "r": "8"}))
	cells := rasterScene(svg, 10, 5)
	got := sceneText(cells)
	if !strings.Contains(got[2], "⣿⣿⣿⣿") {
		t.Fatalf("the circle's middle row is not filled:\n%s", strings.Join(got, "\n"))
	}
	if got[0][0] != '.' || got[4][0] != '.' {
		t.Fatalf("the circle filled its corners:\n%s", strings.Join(got, "\n"))
	}

	runs, closed := parseScenePath("M 0 0 L 10 0 l 0 10 H 0 Z m 2 2 Q 4 4 6 2 C 7 3 8 3 9 2 A 2 2 0 0 1 13 2")
	if len(runs) != 2 || !closed[0] || closed[1] {
		t.Fatalf("path runs = %d, closed = %v", len(runs), closed)
	}
	if p := runs[0][2]; p[0] != 10 || p[1] != 10 {
		t.Fatalf("the relative l did not add to the current point: %v", p)
	}
	if p := runs[0][len(runs[0])-1]; p[0] != 0 || p[1] != 0 {
		t.Fatalf("Z did not return to the start: %v", p)
	}
	if p := runs[1][0]; p[0] != 2 || p[1] != 2 {
		t.Fatalf("the relative m after Z starts at %v, want (2,2)", p)
	}
	end := runs[1][len(runs[1])-1]
	if d := (end[0]-13)*(end[0]-13) + (end[1]-2)*(end[1]-2); d > 1e-9 {
		t.Fatalf("the arc ends at %v, want (13,2)", end)
	}

	m := parseSceneTransform("translate(10 0) rotate(90) scale(2)")
	x, y := m.apply(1, 0)
	if d := (x-10)*(x-10) + (y-2)*(y-2); d > 1e-9 {
		t.Fatalf("translate(10 0) rotate(90) scale(2) maps (1,0) to (%v,%v), want (10,2)", x, y)
	}
}

// A scene inside a Tui layout: a Raw <svg data-sky-scene> is a "scene" box
// sized from its CSS px and scaled to fit; a Raw without the marker is still
// its text.
func TestTuiScene_LayoutAndPaint(t *testing.T) {
	svgADT := SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{
		"svg",
		[]any{
			SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"data-sky-scene", "1"}},
			SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"viewBox", "0 0 40 20"}},
			SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"width", "40"}},
			SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"height", "20"}},
		},
		[]any{
			SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{"rect", []any{
				SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"x", "0"}},
				SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"y", "0"}},
				SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"width", "40"}},
				SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"height", "20"}},
			}, []any{}}},
		},
	}}
	raw := SkyADT{Tag: 4, SkyName: "Raw", Fields: []any{svgADT}}
	box := layoutElement(raw, tuiLayoutCtx{cols: 80, rows: 24, pxPerCellX: 4, pxPerCellY: 4}, 80, 24, layoutAxisColumn)
	if box.kind != "scene" || box.width != 10 || box.height != 5 {
		t.Fatalf("scene box = kind %q %dx%d, want scene 10x5", box.kind, box.width, box.height)
	}
	narrow := layoutElement(raw, tuiLayoutCtx{cols: 80, rows: 24, pxPerCellX: 4, pxPerCellY: 4}, 4, 24, layoutAxisColumn)
	if narrow.width != 4 || narrow.height != 2 {
		t.Fatalf("a scene in 4 cells is %dx%d, want 4x2 (aspect kept)", narrow.width, narrow.height)
	}
	grid := newCellGrid(10, 5)
	var fs []focusable
	paintBox(grid, box, 0, 0, 10, 5, -1, &fs, newInputRegistry(), textStyle{}, layoutAxisColumn, 0)
	for r := 0; r < 5; r++ {
		if got := gridRow(grid, r); got != strings.Repeat("⣿", 10) {
			t.Fatalf("row %d painted %q, want a full row of dots", r, got)
		}
	}
}
