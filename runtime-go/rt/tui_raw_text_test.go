//go:build !js

package rt

import "testing"

// A Std.Ui `Raw` node (Ui.html wrapping a Std.Html node) used to render as
// the literal placeholder "[raw]" in the terminal, hiding its content. The
// terminal cannot draw markup, but it can draw the node's text.
func TestTuiLayout_RawRendersItsTextContent(t *testing.T) {
	html := SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{
		"table", []any{},
		[]any{
			SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{"td", []any{}, []any{
				SkyADT{Tag: 1, SkyName: "HText", Fields: []any{"Total"}},
			}}},
			SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{"td", []any{}, []any{
				SkyADT{Tag: 2, SkyName: "HRaw", Fields: []any{"<b>42</b> &amp; up"}},
			}}},
		},
	}}
	raw := SkyADT{Tag: 4, SkyName: "Raw", Fields: []any{html}}
	box := layoutElement(raw, tuiLayoutCtx{cols: 80, rows: 24}, 80, 24, 0)
	if box.text != "Total 42 & up" {
		t.Fatalf("Raw node rendered %q, want its text content %q", box.text, "Total 42 & up")
	}
	if box.width != runeLen(box.text) || box.height != 1 {
		t.Fatalf("Raw text box has size %dx%d for %q", box.width, box.height, box.text)
	}
	empty := SkyADT{Tag: 4, SkyName: "Raw", Fields: []any{
		SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{"canvas", []any{}, []any{}}},
	}}
	if b := layoutElement(empty, tuiLayoutCtx{cols: 80, rows: 24}, 80, 24, 0); b.kind != "empty" {
		t.Fatalf("a Raw node with no text rendered %q (kind %s), want nothing", b.text, b.kind)
	}
}
