//go:build !js

package rt

import (
	"reflect"
	"strings"
	"testing"
)

func tuiTestText(s string) SkyADT {
	return SkyADT{Tag: 1, SkyName: "Text", Fields: []any{s}}
}

func tuiTestRow(children ...any) SkyADT {
	marker := SkyADT{Tag: 8, SkyName: "AttrStyle", Fields: []any{"__row", "true"}}
	return SkyADT{Tag: 2, SkyName: "Node", Fields: []any{
		SkyADT{Tag: 0, SkyName: "NoDescription"}, []any{marker}, children,
	}}
}

// gridRow is the text of one painted row, trailing blanks trimmed.
func gridRow(grid [][]tuiCell, r int) string {
	var sb strings.Builder
	for _, c := range grid[r] {
		if c.ch == "" {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteString(c.ch)
	}
	return strings.TrimRight(sb.String(), " ")
}

// A Ui.text wider than its box wraps at word boundaries (v0.27.0). It used
// to be one row cut at the edge of the box, so the end of the text was lost.
func TestTuiText_WrapsAtTheCellWidth(t *testing.T) {
	box := layoutElement(tuiTestText("the quick brown fox jumps"), tuiLayoutCtx{cols: 80, rows: 24}, 10, 24, layoutAxisColumn)
	want := []string{"the quick", "brown fox", "jumps"}
	if !reflect.DeepEqual(box.lines, want) {
		t.Fatalf("lines = %#v, want %#v", box.lines, want)
	}
	if box.height != 3 || box.width != 9 {
		t.Fatalf("box is %dx%d, want 9x3", box.width, box.height)
	}
	grid := newCellGrid(10, 4)
	var fs []focusable
	paintBox(grid, box, 0, 0, 10, 4, -1, &fs, newInputRegistry(), textStyle{}, layoutAxisColumn, 0)
	for i, w := range want {
		if got := gridRow(grid, i); got != w {
			t.Fatalf("row %d painted %q, want %q", i, got, w)
		}
	}
}

// A text that fits keeps the one-line box, and a newline breaks the line.
func TestTuiText_FitsOrBreaksOnNewline(t *testing.T) {
	box := layoutElement(tuiTestText("short"), tuiLayoutCtx{cols: 80, rows: 24}, 10, 24, layoutAxisColumn)
	if box.height != 1 || len(box.lines) != 0 || box.text != "short" {
		t.Fatalf("a fitting text became %+v", box)
	}
	nl := layoutElement(tuiTestText("a\nb"), tuiLayoutCtx{cols: 80, rows: 24}, 10, 24, layoutAxisColumn)
	if !reflect.DeepEqual(nl.lines, []string{"a", "b"}) {
		t.Fatalf("a newline gave lines %#v", nl.lines)
	}
	long := layoutElement(tuiTestText("abcdefghijkl"), tuiLayoutCtx{cols: 80, rows: 24}, 5, 24, layoutAxisColumn)
	if !reflect.DeepEqual(long.lines, []string{"abcde", "fghij", "kl"}) {
		t.Fatalf("a word longer than the line gave %#v", long.lines)
	}
}

// Ui.textNoWrap (a Raw span) stays one row.
func TestTuiText_NoWrapStaysOneRow(t *testing.T) {
	span := SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{
		"span", []any{}, []any{SkyADT{Tag: 1, SkyName: "HText", Fields: []any{"the quick brown fox"}}},
	}}
	box := layoutElement(SkyADT{Tag: 4, SkyName: "Raw", Fields: []any{span}}, tuiLayoutCtx{cols: 80, rows: 24}, 10, 24, layoutAxisColumn)
	if box.height != 1 || len(box.lines) != 0 {
		t.Fatalf("textNoWrap wrapped: %+v", box)
	}
}

// In a row the texts share the width the other children leave, so each
// wraps in its own column instead of the first one taking the whole row.
func TestTuiText_RowTextsShareTheWidth(t *testing.T) {
	boxes := layoutChildren([]any{tuiTestText("aaa bbb ccc"), tuiTestText("ddd eee fff")},
		tuiLayoutCtx{cols: 80, rows: 24}, 16, 24, layoutAxisRow, 0)
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes", len(boxes))
	}
	for i, b := range boxes {
		if b.width > 8 || b.height != 2 {
			t.Fatalf("text %d is %dx%d (lines %#v), want at most 8 wide and 2 rows", i, b.width, b.height, b.lines)
		}
	}
	row := layoutElement(tuiTestRow(tuiTestText("left side text"), tuiTestText("right")), tuiLayoutCtx{cols: 80, rows: 24}, 12, 24, layoutAxisColumn)
	if len(row.children) != 2 || row.children[0].height < 2 {
		t.Fatalf("row children did not wrap: %+v", row.children)
	}
}
