package rt

import "testing"

// Std_App_htmlDocOrDefault must route on the CONSTRUCTOR NAME, because
// Std.Ui.Element and Std.Html.Html share one Go representation (SkyADT) AND
// collide on Tag (Html's HElement and Element's Empty are both Tag 0). A
// Tag-based check would misclassify an Html document as an empty Element — the
// exact bug this kernel closes.
func TestStdAppHtmlDocOrDefault_Routing(t *testing.T) {
	// Sentinels: `deflt` stands in for the caller's `Ui.layout [] el`.
	deflt := SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{"div", nil, nil}}

	// An Html DOCUMENT (what `Ui.layout` returns) — every Html constructor,
	// including the Tag-0 HElement that collides with Element's Empty — must be
	// passed through UNCHANGED (returned, not replaced by deflt).
	htmlDocs := []SkyADT{
		{Tag: 0, SkyName: "HElement", Fields: []any{"div", nil, nil}},
		{Tag: 1, SkyName: "HText", Fields: []any{"hi"}},
		{Tag: 2, SkyName: "HRaw", Fields: []any{"<b>x</b>"}},
	}
	for _, doc := range htmlDocs {
		got := Std_App_htmlDocOrDefault(doc, deflt)
		g, ok := got.(SkyADT)
		if !ok || g.SkyName != doc.SkyName {
			t.Fatalf("Html doc %q must pass through unchanged, got %#v", doc.SkyName, got)
		}
	}

	// A genuine Element — including the Tag-0 Empty that shares HElement's Tag —
	// must fall through to `deflt` (the caller's Ui.layout wrapping).
	elements := []SkyADT{
		{Tag: 0, SkyName: "Empty", Fields: []any{}},
		{Tag: 1, SkyName: "Text", Fields: []any{"hi"}},
		{Tag: 2, SkyName: "Node", Fields: []any{nil, nil, nil}},
		{Tag: 3, SkyName: "TaggedNode", Fields: []any{"div", nil, nil, nil}},
		{Tag: 4, SkyName: "Raw", Fields: []any{nil}},
	}
	for _, el := range elements {
		got := Std_App_htmlDocOrDefault(el, deflt)
		g, ok := got.(SkyADT)
		if !ok || g.SkyName != "HElement" { // deflt's SkyName
			t.Fatalf("Element %q must fall through to deflt, got %#v", el.SkyName, got)
		}
	}

	// A non-SkyADT value (defensive): fall through to deflt.
	if got := Std_App_htmlDocOrDefault("not-an-adt", deflt); got.(SkyADT).SkyName != "HElement" {
		t.Fatalf("non-ADT value must fall through to deflt, got %#v", got)
	}
}
