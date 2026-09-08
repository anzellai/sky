package rt

import (
	"strings"
	"testing"
)

// Regression for the Sky.Spa client-hydration bug where a Std.Html.raw node
// inside a <style> (View.Common.globalCss on sky-lang.org) lost its CSS on the
// first client paint. The wasm render fix lives in dom_render_wasm.go
// (spaSetChildren renders a raw-containing child list via the SSR serialiser
// instead of wrapping raw in a <span>); these portable checks pin the shared
// diff behaviour that backs it — a raw node has no sky-id of its own, so a
// change to its text must replace its PARENT's subtree. Pre-fix the child loop
// recursed into two raw nodes and, finding equal tag/kind and no attrs, emitted
// nothing — so dynamic raw content silently stopped updating.

func TestDiffRawTextChangeReplacesParentSubtree(t *testing.T) {
	old := el("style", nil, rawNode("body{color:red}"))
	newT := el("style", nil, rawNode("body{color:blue}"))
	assignSkyIDs(&old, "r")
	assignSkyIDs(&newT, "r")

	patches := diffTrees(&old, &newT, nil)

	var got *string
	for i := range patches {
		if patches[i].ID == old.SkyID && patches[i].HTML != nil {
			got = patches[i].HTML
		}
	}
	if got == nil {
		t.Fatalf("a changed raw child must emit an HTML patch on its parent; got %+v", patches)
	}
	if !strings.Contains(*got, "body{color:blue}") {
		t.Fatalf("the HTML patch must carry the new raw string verbatim, got %q", *got)
	}
	if strings.Contains(*got, "&lt;") || strings.Contains(*got, "<span") {
		t.Fatalf("raw content must not be escaped or <span>-wrapped, got %q", *got)
	}
}

func TestDiffRawTextUnchangedEmitsNoPatch(t *testing.T) {
	old := el("style", nil, rawNode("body{color:red}"))
	newT := el("style", nil, rawNode("body{color:red}"))
	assignSkyIDs(&old, "r")
	assignSkyIDs(&newT, "r")

	patches := diffTrees(&old, &newT, nil)
	if len(patches) != 0 {
		t.Fatalf("unchanged raw content must not re-render; got %+v", patches)
	}
}
