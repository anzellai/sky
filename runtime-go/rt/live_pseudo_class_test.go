package rt

// Regression tests for injectPseudoClassStyles — the Sky.Live runtime
// half of the Std.Ui pseudo-class primitive (Background.hoverColor /
// Font.focusColor / Border.activeColor / Ui.onPseudo, issue #377).
// After assignSkyIDs stamps every element with a structural id, this
// pass parses the `data-sky-pc-rules` marker into one or more CSS
// rules scoped to the element's sky-id; `:hover` rules are auto-
// gated behind `@media (hover: hover)` so they don't fire as
// sticky-hover on touch devices.

import (
	"strings"
	"testing"
)

// TestInjectPseudoClassStyles_HoverIsHoverGated — `:hover` rules
// must be wrapped in `@media (hover: hover)`. The mobile-tap-
// counts-as-hover bug is the entire reason we made `hoverColor` a
// typed helper instead of a raw CSS string.
func TestInjectPseudoClassStyles_HoverIsHoverGated(t *testing.T) {
	tree := el("button", map[string]string{
		"data-sky-pc-rules": "h| background-color: rgba(0, 92, 215, 1);",
	}, txt("Save"))
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	// Marker stripped from wire output.
	if _, ok := tree.Attrs["data-sky-pc-rules"]; ok {
		t.Errorf("data-sky-pc-rules leaked to wire: %v", tree.Attrs)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children (style + original), got %d", len(tree.Children))
	}
	style := tree.Children[0]
	if style.Tag != "style" {
		t.Fatalf("first child not <style>: %+v", style)
	}
	text := style.Children[0].Text
	if !strings.Contains(text, "@media (hover: hover)") {
		t.Errorf(":hover rule missing @media (hover: hover) gate: %q", text)
	}
	if !strings.Contains(text, `[sky-id="r"]:hover`) {
		t.Errorf("missing sky-id-scoped :hover selector: %q", text)
	}
	if !strings.Contains(text, "background-color: rgba(0, 92, 215, 1)") {
		t.Errorf("user rule lost: %q", text)
	}
}

// TestInjectPseudoClassStyles_FocusNotHoverGated — `:focus`,
// `:focus-visible`, `:active`, `:disabled` are NOT gated behind
// `(hover: hover)` — they apply on every device (keyboards exist
// on tablets, accessibility tools simulate disabled state).
func TestInjectPseudoClassStyles_FocusNotHoverGated(t *testing.T) {
	tree := el("input", map[string]string{
		"data-sky-pc-rules": "v| border-color: rgba(0, 122, 255, 1);",
	})
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) < 1 || tree.Children[0].Tag != "style" {
		t.Fatalf("expected first child to be <style>: %+v", tree.Children)
	}
	text := tree.Children[0].Children[0].Text
	if strings.Contains(text, "@media (hover: hover)") {
		t.Errorf(":focus-visible MUST NOT be hover-gated: %q", text)
	}
	if !strings.Contains(text, `[sky-id="r"]:focus-visible`) {
		t.Errorf("missing :focus-visible selector: %q", text)
	}
}

// TestInjectPseudoClassStyles_MultipleStatesOnSameElement —
// `hoverColor` + `focusColor` + `activeColor` on the same element
// produce ONE <style> child with three independent rules. Hover is
// the only one gated.
func TestInjectPseudoClassStyles_MultipleStatesOnSameElement(t *testing.T) {
	tree := el("button", map[string]string{
		"data-sky-pc-rules": "h| background-color: rgba(0, 92, 215, 1);" +
			"||v| border-color: rgba(255, 102, 0, 1);" +
			"||a| background-color: rgba(0, 62, 175, 1);",
	}, txt("Submit"))
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) != 2 {
		t.Fatalf("expected 1 style + 1 original child, got %d", len(tree.Children))
	}
	text := tree.Children[0].Children[0].Text
	// Three rules emitted.
	for _, want := range []string{
		`[sky-id="r"]:hover`,
		`[sky-id="r"]:focus-visible`,
		`[sky-id="r"]:active`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing selector %q in style: %q", want, text)
		}
	}
	// Hover gated; focus-visible + active NOT gated. Easiest way to
	// pin: the focus + active selectors must appear OUTSIDE the
	// `@media (hover: hover)` block. Split on the @media block and
	// confirm the non-hover selectors land after the closing brace.
	mediaIdx := strings.Index(text, "@media (hover: hover)")
	if mediaIdx < 0 {
		t.Fatalf("missing @media block: %q", text)
	}
	// Find the matching closing `} }` of the @media block.
	closingIdx := strings.Index(text[mediaIdx:], "} }")
	if closingIdx < 0 {
		t.Fatalf("@media block has no closing `} }`: %q", text)
	}
	postMedia := text[mediaIdx+closingIdx:]
	if !strings.Contains(postMedia, ":focus-visible") {
		t.Errorf(":focus-visible MUST live outside @media (hover: hover): %q", text)
	}
	if !strings.Contains(postMedia, ":active") {
		t.Errorf(":active MUST live outside @media (hover: hover): %q", text)
	}
}

// TestInjectPseudoClassStyles_NoMarker — elements without the
// marker attr pass through unchanged. (Defends against the pass
// over-eagerly mutating every element.)
func TestInjectPseudoClassStyles_NoMarker(t *testing.T) {
	tree := el("div", nil, el("span", nil, txt("ok")))
	assignSkyIDs(&tree, "r")
	before := len(tree.Children)
	injectPseudoClassStyles(&tree)
	if len(tree.Children) != before {
		t.Errorf("non-marker element mutated: children %d → %d",
			before, len(tree.Children))
	}
}

// TestInjectPseudoClassStyles_NestedScopes — two elements each with
// their own pseudo-class rules get independent sky-id selectors.
// Cross-contamination guard.
func TestInjectPseudoClassStyles_NestedScopes(t *testing.T) {
	tree := el("div", nil,
		el("button", map[string]string{
			"data-sky-pc-rules": "h| color: red;",
		}, txt("A")),
		el("button", map[string]string{
			"data-sky-pc-rules": "h| color: blue;",
		}, txt("B")),
	)
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(tree.Children))
	}
	for i, btn := range tree.Children {
		if len(btn.Children) < 2 || btn.Children[0].Tag != "style" {
			t.Fatalf("button %d missing injected style child: %+v", i, btn.Children)
		}
		text := btn.Children[0].Children[0].Text
		want := `[sky-id="` + btn.SkyID + `"]:hover`
		if !strings.Contains(text, want) {
			t.Errorf("button %d style missing own sky-id scope %q in %q",
				i, want, text)
		}
		other := tree.Children[1-i].SkyID
		bad := `[sky-id="` + other + `"]`
		if strings.Contains(text, bad) {
			t.Errorf("button %d cross-contamination: contains sibling's selector %q",
				i, bad)
		}
	}
}

// TestInjectPseudoClassStyles_StyleEscape — runtime defends against
// a malicious `</style>` substring in the user-supplied CSS (browser
// would otherwise terminate the style block prematurely and dump
// the rest as inline HTML — low impact, trivial to close).
func TestInjectPseudoClassStyles_StyleEscape(t *testing.T) {
	tree := el("button", map[string]string{
		"data-sky-pc-rules": "h| color: red;</style><script>alert('x')</script>",
	}, txt("x"))
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	style := tree.Children[0]
	text := style.Children[0].Text
	if strings.Contains(text, "</style") {
		t.Errorf("unstripped </style> in injected text: %q", text)
	}
	if strings.Contains(text, "</STYLE") {
		t.Errorf("unstripped </STYLE in injected text: %q", text)
	}
}

// TestInjectPseudoClassStyles_UnknownTagSkipped — forward-compat
// guard: a future Sky compiler emitting an unknown tag (e.g. `c`
// for `:checked`) shouldn't break the older runtime. Unknown
// entries are silently dropped.
func TestInjectPseudoClassStyles_UnknownTagSkipped(t *testing.T) {
	tree := el("input", map[string]string{
		"data-sky-pc-rules": "h| color: red;||z| background: blue;",
	})
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) < 1 || tree.Children[0].Tag != "style" {
		t.Fatalf("expected style child: %+v", tree.Children)
	}
	text := tree.Children[0].Children[0].Text
	if !strings.Contains(text, ":hover") {
		t.Errorf("known :hover rule lost: %q", text)
	}
	if strings.Contains(text, "background: blue") {
		t.Errorf("unknown 'z' tag should be dropped: %q", text)
	}
}

// TestInjectPseudoClassStyles_ComposesWithMediaQuery — a
// `Ui.breakpoint Ui.mobile` wrapper around an element that carries
// `Background.hoverColor` produces a NESTED structure: outer
// wrapper gets a media-query <style>, inner element gets its own
// pseudo-class <style>. Both rules are scoped to their respective
// sky-ids and don't conflict.
func TestInjectPseudoClassStyles_ComposesWithMediaQuery(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-mq-q":     "(max-width: 767px)",
		"data-sky-mq-rules": "padding: 8px;",
	},
		el("button", map[string]string{
			"data-sky-pc-rules": "h| background-color: rgba(0, 92, 215, 1);",
		}, txt("Save")),
	)
	assignSkyIDs(&tree, "r")
	applyStyleInjections(&tree)

	// Outer wrapper: <style data-sky-mq=...> + button
	if len(tree.Children) != 2 {
		t.Fatalf("outer wrapping wrong: %+v", tree.Children)
	}
	if tree.Children[0].Tag != "style" {
		t.Fatalf("outer first child not <style>: %+v", tree.Children[0])
	}
	outerStyleText := tree.Children[0].Children[0].Text
	if !strings.Contains(outerStyleText, "@media (max-width: 767px)") {
		t.Errorf("outer missing breakpoint: %q", outerStyleText)
	}

	// Inner button: <style data-sky-pc=...> + "Save" text
	btn := tree.Children[1]
	if btn.Tag != "button" {
		t.Fatalf("inner not button: %+v", btn)
	}
	if len(btn.Children) != 2 || btn.Children[0].Tag != "style" {
		t.Fatalf("inner wrapping wrong: %+v", btn.Children)
	}
	innerStyleText := btn.Children[0].Children[0].Text
	if !strings.Contains(innerStyleText, "@media (hover: hover)") {
		t.Errorf("inner :hover missing (hover: hover) gate: %q", innerStyleText)
	}
	if !strings.Contains(innerStyleText, ":hover") {
		t.Errorf("inner missing :hover: %q", innerStyleText)
	}
	// Cross-contamination guard.
	if strings.Contains(outerStyleText, ":hover") {
		t.Errorf("outer leaked inner's pseudo rule: %q", outerStyleText)
	}
	if strings.Contains(innerStyleText, "max-width") {
		t.Errorf("inner leaked outer's media query: %q", innerStyleText)
	}
}

// TestInjectPseudoClassStyles_EmptyCSSDropped — a single-entry
// marker whose CSS portion is empty emits no <style> child, but
// the marker attr is still stripped (we've consumed it). The Sky
// emitter (`pseudoRuleEntry` in Std.Ui.sky) already filters
// empty-CSS entries upstream — this test pins the runtime's
// downstream defence so an older / mis-emitted client wire shape
// can't pollute the rendered tree.
func TestInjectPseudoClassStyles_EmptyCSSDropped(t *testing.T) {
	tree := el("button", map[string]string{
		"data-sky-pc-rules": "h|",
	}, txt("x"))
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)
	if _, ok := tree.Attrs["data-sky-pc-rules"]; ok {
		t.Errorf("marker should be stripped even when no rules emit: %v",
			tree.Attrs)
	}
	for _, c := range tree.Children {
		if c.Tag == "style" {
			t.Errorf("empty rule emitted a style child: %+v", c)
		}
	}
}
