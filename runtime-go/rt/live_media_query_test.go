package rt

// Regression tests for injectMediaQueryStyles — the Sky.Live runtime
// half of the Std.Ui media-query primitive (Ui.breakpoint /
// Ui.mediaQuery, issue #376). After assignSkyIDs stamps every
// element with a structural id, this pass rewrites every element
// that carries `data-sky-mq-q` + `data-sky-mq-rules` into a base
// wrapper plus a sky-id-scoped `<style>` child. The runtime side
// is fully decoupled from the Sky source — these tests fix the wire
// shape independently from the Sky compile path.

import (
	"strings"
	"testing"
)

// TestInjectMediaQueryStyles_BasicScope — single Ui.breakpoint
// produces one scoped style child + strips marker attrs.
func TestInjectMediaQueryStyles_BasicScope(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-mq-q":     "(max-width: 767px)",
		"data-sky-mq-rules": "padding: 8px; flex-direction: column;",
	}, txt("child"))
	assignSkyIDs(&tree, "r")
	injectMediaQueryStyles(&tree)

	// Marker attrs stripped — they're internal protocol, not wire.
	if _, ok := tree.Attrs["data-sky-mq-q"]; ok {
		t.Errorf("data-sky-mq-q leaked to wire: %v", tree.Attrs)
	}
	if _, ok := tree.Attrs["data-sky-mq-rules"]; ok {
		t.Errorf("data-sky-mq-rules leaked to wire: %v", tree.Attrs)
	}

	// First child should be the injected <style> element.
	if len(tree.Children) < 2 {
		t.Fatalf("expected ≥2 children (style + original), got %d", len(tree.Children))
	}
	style := tree.Children[0]
	if style.Kind != "element" || style.Tag != "style" {
		t.Fatalf("first child is not <style>: %+v", style)
	}
	if len(style.Children) != 1 || style.Children[0].Kind != "raw" {
		t.Fatalf("style needs one raw child: %+v", style)
	}
	// The selector MUST scope by sky-id so multiple breakpoints
	// on the same page don't cross-contaminate each other.
	want := `[sky-id="r"]`
	if !strings.Contains(style.Children[0].Text, want) {
		t.Errorf("style selector missing sky-id scope: got %q, want substring %q",
			style.Children[0].Text, want)
	}
	if !strings.Contains(style.Children[0].Text, "@media (max-width: 767px)") {
		t.Errorf("style missing @media wrap: %q", style.Children[0].Text)
	}
	if !strings.Contains(style.Children[0].Text, "padding: 8px") {
		t.Errorf("style missing user rules: %q", style.Children[0].Text)
	}

	// Original child preserved at position 1.
	if tree.Children[1].Kind != "text" || tree.Children[1].Text != "child" {
		t.Errorf("original child displaced: %+v", tree.Children[1])
	}
}

// TestInjectMediaQueryStyles_NoMarker — elements without the
// marker pair pass through unchanged. (Defends against the pass
// over-eagerly mutating every element.)
func TestInjectMediaQueryStyles_NoMarker(t *testing.T) {
	tree := el("div", nil, el("span", nil, txt("ok")))
	assignSkyIDs(&tree, "r")
	before := len(tree.Children)
	injectMediaQueryStyles(&tree)
	if len(tree.Children) != before {
		t.Errorf("non-marker element mutated: children %d → %d",
			before, len(tree.Children))
	}
}

// TestInjectMediaQueryStyles_NestedScopes — two breakpoints in
// the same tree get independent sky-id selectors; their scoped
// rules cannot leak into each other.
func TestInjectMediaQueryStyles_NestedScopes(t *testing.T) {
	tree := el("div", nil,
		el("section", map[string]string{
			"data-sky-mq-q":     "(max-width: 767px)",
			"data-sky-mq-rules": "color: red;",
		}, txt("a")),
		el("section", map[string]string{
			"data-sky-mq-q":     "(prefers-color-scheme: dark)",
			"data-sky-mq-rules": "background: black;",
		}, txt("b")),
	)
	assignSkyIDs(&tree, "r")
	injectMediaQueryStyles(&tree)

	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(tree.Children))
	}
	for i, section := range tree.Children {
		if len(section.Children) < 2 {
			t.Fatalf("section %d missing injected style child", i)
		}
		style := section.Children[0]
		if style.Tag != "style" {
			t.Errorf("section %d: first child not <style>: %s", i, style.Tag)
			continue
		}
		// Sky-id scope must match the section's own sky-id, never
		// the sibling's.
		want := `[sky-id="` + section.SkyID + `"]`
		if !strings.Contains(style.Children[0].Text, want) {
			t.Errorf("section %d style missing own sky-id scope %q in %q",
				i, want, style.Children[0].Text)
		}
		// And must NOT contain the OTHER section's sky-id.
		other := tree.Children[1-i].SkyID
		bad := `[sky-id="` + other + `"]`
		if strings.Contains(style.Children[0].Text, bad) {
			t.Errorf("section %d cross-contamination: contains sibling's selector %q",
				i, bad)
		}
	}
}

// TestInjectMediaQueryStyles_StyleEscape — runtime defends against
// a malicious `</style>` substring in the user-supplied rules /
// query (the browser would otherwise terminate the style block
// prematurely and dump the rest as inline HTML — a low-impact but
// trivial vector to close).
func TestInjectMediaQueryStyles_StyleEscape(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-mq-q":     "(max-width: 767px)",
		"data-sky-mq-rules": "color: red;</style><script>alert('x')</script>",
	}, txt("child"))
	assignSkyIDs(&tree, "r")
	injectMediaQueryStyles(&tree)

	style := tree.Children[0]
	if strings.Contains(style.Children[0].Text, "</style") {
		t.Errorf("unstripped </style> in injected text: %q",
			style.Children[0].Text)
	}
	if strings.Contains(style.Children[0].Text, "</STYLE") {
		t.Errorf("unstripped </STYLE in injected text: %q",
			style.Children[0].Text)
	}
}

// TestInjectMediaQueryStyles_Composition — a Ui.mediaQuery wrapper
// around another Ui.mediaQuery wrapper (nested breakpoint stack)
// produces two scoped style blocks at the correct depths.
func TestInjectMediaQueryStyles_Composition(t *testing.T) {
	// Outer wrapper carries breakpoint A; inner wrapper (its
	// only child) carries breakpoint B; the actual content is at
	// depth 2.
	tree := el("div", map[string]string{
		"data-sky-mq-q":     "(max-width: 767px)",
		"data-sky-mq-rules": "padding: 8px;",
	},
		el("div", map[string]string{
			"data-sky-mq-q":     "(prefers-color-scheme: dark)",
			"data-sky-mq-rules": "background: #111;",
		}, txt("content")),
	)
	assignSkyIDs(&tree, "r")
	injectMediaQueryStyles(&tree)

	// Outer: [<style>, innerWrapper]
	if len(tree.Children) != 2 || tree.Children[0].Tag != "style" {
		t.Fatalf("outer wrapping wrong: %+v", tree.Children)
	}
	inner := tree.Children[1]
	if inner.Tag != "div" {
		t.Fatalf("inner not a div: %+v", inner)
	}
	// Inner: [<style>, original text]
	if len(inner.Children) != 2 || inner.Children[0].Tag != "style" {
		t.Fatalf("inner wrapping wrong: %+v", inner.Children)
	}
	outerStyle := tree.Children[0].Children[0].Text
	innerStyle := inner.Children[0].Children[0].Text
	if !strings.Contains(outerStyle, "@media (max-width: 767px)") {
		t.Errorf("outer style missing breakpoint: %q", outerStyle)
	}
	if !strings.Contains(innerStyle, "@media (prefers-color-scheme: dark)") {
		t.Errorf("inner style missing breakpoint: %q", innerStyle)
	}
	// Cross-contamination guard.
	if strings.Contains(outerStyle, "prefers-color-scheme") {
		t.Errorf("outer leaked inner's query: %q", outerStyle)
	}
	if strings.Contains(innerStyle, "max-width") {
		t.Errorf("inner leaked outer's query: %q", innerStyle)
	}
}
