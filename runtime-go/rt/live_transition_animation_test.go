package rt

// Regression tests for injectTransitionStyles + injectAnimationStyles —
// the Sky.Live runtime half of the Std.Ui transition + animation
// primitives (Transition.attribute / Animation.attribute, issue
// #378). Both passes run after assignSkyIDs stamps every element
// with a structural id; both wrap their CSS in
// `@media (prefers-reduced-motion: no-preference)` by default for
// a11y compliance (opt out via the `respect`-flag wire markers).

import (
	"strings"
	"testing"
)

// ─── Transition tests ────────────────────────────────────────────

// TestInjectTransitionStyles_RespectsReducedMotion — the default
// (`respect = "1"`) wraps the transition rule in
// `@media (prefers-reduced-motion: no-preference)`. Users who've
// opted out of motion in their OS see a static UI.
func TestInjectTransitionStyles_RespectsReducedMotion(t *testing.T) {
	tree := el("button", map[string]string{
		"data-sky-tr-rules":   "background-color 200ms ease-out",
		"data-sky-tr-respect": "1",
	}, txt("hover me"))
	assignSkyIDs(&tree, "r")
	injectTransitionStyles(&tree)

	// Markers stripped from wire output.
	if _, ok := tree.Attrs["data-sky-tr-rules"]; ok {
		t.Errorf("data-sky-tr-rules leaked: %v", tree.Attrs)
	}
	if _, ok := tree.Attrs["data-sky-tr-respect"]; ok {
		t.Errorf("data-sky-tr-respect leaked: %v", tree.Attrs)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children (style + original), got %d", len(tree.Children))
	}
	style := tree.Children[0]
	if style.Tag != "style" {
		t.Fatalf("first child not <style>: %+v", style)
	}
	text := style.Children[0].Text
	if !strings.Contains(text, "@media (prefers-reduced-motion: no-preference)") {
		t.Errorf("missing reduced-motion gate: %q", text)
	}
	if !strings.Contains(text, `[sky-id="r"]`) {
		t.Errorf("missing sky-id scope: %q", text)
	}
	if !strings.Contains(text, "transition: background-color 200ms ease-out") {
		t.Errorf("user rule lost: %q", text)
	}
}

// TestInjectTransitionStyles_OptOutReducedMotion — `respect = "0"`
// emits the rule unwrapped, for semantically-required motion
// (loading spinners etc.).
func TestInjectTransitionStyles_OptOutReducedMotion(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-tr-rules":   "transform 1000ms linear",
		"data-sky-tr-respect": "0",
	})
	assignSkyIDs(&tree, "r")
	injectTransitionStyles(&tree)
	if len(tree.Children) < 1 || tree.Children[0].Tag != "style" {
		t.Fatalf("expected <style> child, got: %+v", tree.Children)
	}
	text := tree.Children[0].Children[0].Text
	if strings.Contains(text, "@media (prefers-reduced-motion") {
		t.Errorf("opt-out transition MUST NOT be reduced-motion-gated: %q", text)
	}
	if !strings.Contains(text, "transition: transform 1000ms linear") {
		t.Errorf("transition rule lost: %q", text)
	}
}

// TestInjectTransitionStyles_NoMarker — elements without the
// marker pass through unchanged.
func TestInjectTransitionStyles_NoMarker(t *testing.T) {
	tree := el("div", nil, el("span", nil, txt("ok")))
	assignSkyIDs(&tree, "r")
	before := len(tree.Children)
	injectTransitionStyles(&tree)
	if len(tree.Children) != before {
		t.Errorf("non-marker element mutated: %d → %d", before, len(tree.Children))
	}
}

// TestInjectTransitionStyles_StyleEscape — defensive `</style>` strip
// prevents premature termination of the injected style block.
func TestInjectTransitionStyles_StyleEscape(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-tr-rules":   "color 200ms ease-out;</style><script>alert(1)</script>",
		"data-sky-tr-respect": "1",
	}, txt("x"))
	assignSkyIDs(&tree, "r")
	injectTransitionStyles(&tree)
	text := tree.Children[0].Children[0].Text
	if strings.Contains(text, "</style") {
		t.Errorf("unstripped </style>: %q", text)
	}
	if strings.Contains(text, "</STYLE") {
		t.Errorf("unstripped </STYLE: %q", text)
	}
}

// TestInjectTransitionStyles_NestedScopes — two elements with
// independent transitions get independent sky-id selectors. No
// cross-contamination.
func TestInjectTransitionStyles_NestedScopes(t *testing.T) {
	tree := el("div", nil,
		el("button", map[string]string{
			"data-sky-tr-rules":   "color 100ms ease-in",
			"data-sky-tr-respect": "1",
		}, txt("A")),
		el("button", map[string]string{
			"data-sky-tr-rules":   "background-color 300ms linear",
			"data-sky-tr-respect": "1",
		}, txt("B")),
	)
	assignSkyIDs(&tree, "r")
	injectTransitionStyles(&tree)
	for i, btn := range tree.Children {
		if len(btn.Children) < 2 || btn.Children[0].Tag != "style" {
			t.Fatalf("button %d missing style child: %+v", i, btn.Children)
		}
		text := btn.Children[0].Children[0].Text
		want := `[sky-id="` + btn.SkyID + `"]`
		if !strings.Contains(text, want) {
			t.Errorf("button %d missing own sky-id %q in: %q", i, want, text)
		}
		other := tree.Children[1-i].SkyID
		bad := `[sky-id="` + other + `"]`
		if strings.Contains(text, bad) {
			t.Errorf("button %d leaked sibling's selector %q", i, bad)
		}
	}
}

// ─── Animation tests ─────────────────────────────────────────────

// TestInjectAnimationStyles_BasicFade — single keyframe animation
// emits a @keyframes block + a sky-id-scoped animation rule with
// the reduced-motion gate.
func TestInjectAnimationStyles_BasicFade(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-anim-rules": "fadeIn||300ms ease-out 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 1; }||1",
	})
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)

	if _, ok := tree.Attrs["data-sky-anim-rules"]; ok {
		t.Errorf("data-sky-anim-rules leaked: %v", tree.Attrs)
	}
	if len(tree.Children) != 1 || tree.Children[0].Tag != "style" {
		t.Fatalf("expected single <style> child, got: %+v", tree.Children)
	}
	text := tree.Children[0].Children[0].Text
	if !strings.Contains(text, "@keyframes fadeIn__r") {
		t.Errorf("missing auto-suffixed @keyframes name: %q", text)
	}
	if !strings.Contains(text, "0% { opacity: 0; }") {
		t.Errorf("missing 0%% keyframe: %q", text)
	}
	if !strings.Contains(text, "100% { opacity: 1; }") {
		t.Errorf("missing 100%% keyframe: %q", text)
	}
	if !strings.Contains(text, "@media (prefers-reduced-motion: no-preference)") {
		t.Errorf("missing reduced-motion gate: %q", text)
	}
	if !strings.Contains(text, `[sky-id="r"]`) {
		t.Errorf("missing sky-id scope: %q", text)
	}
	if !strings.Contains(text, "animation: fadeIn__r") {
		t.Errorf("missing animation property: %q", text)
	}
	if !strings.Contains(text, "300ms ease-out 0ms 1 forwards") {
		t.Errorf("missing shorthand tail: %q", text)
	}
}

// TestInjectAnimationStyles_NameAutoSuffix — two animations with
// the SAME user-supplied name on different elements produce
// DIFFERENT effective @keyframes names. The user can copy-paste
// the same `name = "fadeIn"` spec across elements without manually
// disambiguating.
func TestInjectAnimationStyles_NameAutoSuffix(t *testing.T) {
	tree := el("div", nil,
		el("p", map[string]string{
			"data-sky-anim-rules": "fadeIn||200ms ease-out 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 1; }||1",
		}, txt("A")),
		el("p", map[string]string{
			"data-sky-anim-rules": "fadeIn||500ms linear 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 0.5; }||1",
		}, txt("B")),
	)
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(tree.Children))
	}
	textA := tree.Children[0].Children[0].Children[0].Text
	textB := tree.Children[1].Children[0].Children[0].Text
	// Extract the @keyframes names — they must differ.
	nameA := extractKeyframesName(textA, t)
	nameB := extractKeyframesName(textB, t)
	if nameA == nameB {
		t.Errorf("@keyframes names collide: both = %q (text A: %q, text B: %q)", nameA, textA, textB)
	}
	// Both should start with the user prefix.
	if !strings.HasPrefix(nameA, "fadeIn__") {
		t.Errorf("name A missing user prefix: %q", nameA)
	}
	if !strings.HasPrefix(nameB, "fadeIn__") {
		t.Errorf("name B missing user prefix: %q", nameB)
	}
}

func extractKeyframesName(text string, t *testing.T) string {
	t.Helper()
	idx := strings.Index(text, "@keyframes ")
	if idx < 0 {
		return ""
	}
	rest := text[idx+len("@keyframes "):]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// TestInjectAnimationStyles_OptOutReducedMotion — `respect = "0"`
// emits the animation rule unwrapped (for spinners that MUST move).
func TestInjectAnimationStyles_OptOutReducedMotion(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-anim-rules": "spin||1000ms linear 0ms infinite none||0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); }||0",
	})
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)
	text := tree.Children[0].Children[0].Text
	if strings.Contains(text, "@media (prefers-reduced-motion") {
		t.Errorf("opt-out animation MUST NOT be reduced-motion-gated: %q", text)
	}
	if !strings.Contains(text, "@keyframes spin__r") {
		t.Errorf("missing @keyframes block: %q", text)
	}
	if !strings.Contains(text, "animation: spin__r") {
		t.Errorf("missing animation rule: %q", text)
	}
}

// TestInjectAnimationStyles_MultipleAnimations — stacking two
// animations on one element joins them in the CSS `animation:`
// shorthand with a comma.
func TestInjectAnimationStyles_MultipleAnimations(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-anim-rules": "fadeIn||200ms ease-out 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 1; }||1" +
			"@@slideUp||300ms ease-out 0ms 1 forwards||0% { transform: translateY(20px); } 100% { transform: translateY(0); }||1",
	})
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)
	text := tree.Children[0].Children[0].Text
	if !strings.Contains(text, "@keyframes fadeIn__r") {
		t.Errorf("missing fadeIn @keyframes: %q", text)
	}
	if !strings.Contains(text, "@keyframes slideUp__r") {
		t.Errorf("missing slideUp @keyframes: %q", text)
	}
	// Animation rule should reference BOTH animations comma-joined.
	if !strings.Contains(text, "fadeIn__r") || !strings.Contains(text, "slideUp__r") ||
		!strings.Contains(text, ", ") {
		t.Errorf("stacked animations not comma-joined: %q", text)
	}
}

// TestInjectAnimationStyles_NoMarker — elements without the marker
// pass through unchanged.
func TestInjectAnimationStyles_NoMarker(t *testing.T) {
	tree := el("div", nil, el("span", nil, txt("ok")))
	assignSkyIDs(&tree, "r")
	before := len(tree.Children)
	injectAnimationStyles(&tree)
	if len(tree.Children) != before {
		t.Errorf("non-marker element mutated: %d → %d", before, len(tree.Children))
	}
}

// TestInjectAnimationStyles_StyleEscape — defensive </style> strip
// in the keyframes body.
func TestInjectAnimationStyles_StyleEscape(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-anim-rules": "evil||200ms ease-out 0ms 1 forwards||0% { opacity: 0; }</style><script>alert(1)</script> 100% { opacity: 1; }||1",
	})
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)
	text := tree.Children[0].Children[0].Text
	if strings.Contains(text, "</style") {
		t.Errorf("unstripped </style>: %q", text)
	}
}

// TestInjectAnimationStyles_NameSanitisation — user-supplied
// `name = "1bad name!"` is sanitised to a CSS-safe ident.
func TestInjectAnimationStyles_NameSanitisation(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-anim-rules": "1bad name!||200ms ease-out 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 1; }||1",
	})
	assignSkyIDs(&tree, "r")
	injectAnimationStyles(&tree)
	text := tree.Children[0].Children[0].Text
	// Original name shouldn't leak as-is into the @keyframes.
	if strings.Contains(text, "@keyframes 1bad name!") {
		t.Errorf("unsafe name leaked: %q", text)
	}
	// A digit-prefixed name should get an underscore prefix.
	if !strings.Contains(text, "@keyframes _1bad_name___") {
		t.Errorf("expected sanitised name prefix `_1bad_name__`: %q", text)
	}
}

// TestInjectAnimationStyles_ComposesWithMediaQueryAndPseudoClass —
// the full chain: Ui.breakpoint(Ui.mobile, ...) wraps an element
// that has Background.hoverColor + Transition.attribute +
// Animation.attribute. All four style-injection passes coexist
// without cross-contamination.
func TestInjectAnimationStyles_ComposesWithMediaQueryAndPseudoClass(t *testing.T) {
	tree := el("div", map[string]string{
		"data-sky-mq-q":     "(max-width: 767px)",
		"data-sky-mq-rules": "padding: 8px;",
	},
		el("button", map[string]string{
			"data-sky-pc-rules":   "h| background-color: rgba(0, 92, 215, 1);",
			"data-sky-tr-rules":   "background-color 200ms ease-out",
			"data-sky-tr-respect": "1",
			"data-sky-anim-rules": "fadeIn||300ms ease-out 0ms 1 forwards||0% { opacity: 0; } 100% { opacity: 1; }||1",
		}, txt("Save")),
	)
	assignSkyIDs(&tree, "r")
	applyStyleInjections(&tree)

	// Outer wrapper has the media-query style.
	outerStyle := tree.Children[0]
	if outerStyle.Tag != "style" {
		t.Fatalf("outer first child not <style>: %+v", outerStyle)
	}
	if !strings.Contains(outerStyle.Children[0].Text, "@media (max-width: 767px)") {
		t.Errorf("outer missing breakpoint")
	}

	// Inner button has 3 <style> children (pseudo, transition,
	// animation) + the original "Save" text.
	btn := tree.Children[1]
	if btn.Tag != "button" {
		t.Fatalf("inner not button: %+v", btn)
	}
	if len(btn.Children) != 4 {
		t.Fatalf("expected 3 style children + 1 text, got %d", len(btn.Children))
	}
	// All three style tags should be present — check by data-attr.
	hasPC, hasTR, hasAnim := false, false, false
	for _, c := range btn.Children {
		if c.Tag != "style" {
			continue
		}
		if _, ok := c.Attrs["data-sky-pc"]; ok {
			hasPC = true
		}
		if _, ok := c.Attrs["data-sky-tr"]; ok {
			hasTR = true
		}
		if _, ok := c.Attrs["data-sky-anim"]; ok {
			hasAnim = true
		}
	}
	if !hasPC {
		t.Errorf("missing pseudo-class style child")
	}
	if !hasTR {
		t.Errorf("missing transition style child")
	}
	if !hasAnim {
		t.Errorf("missing animation style child")
	}
}

// TestSkyIDToCSSIdent_Examples — the sky-id → CSS-ident
// conversion preserves alphanumerics + dash/underscore; rewrites
// `.` and `#` to `_`; drops anything else.
func TestSkyIDToCSSIdent_Examples(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"r", "r"},
		{"r.0", "r_0"},
		{"r.0.2#div", "r_0_2_div"},
		{"r.0.2#button-1", "r_0_2_button-1"},
		{"r.0\x00.2", "r_0_2"}, // null byte dropped
	} {
		if got := skyIDToCSSIdent(tc.in); got != tc.want {
			t.Errorf("skyIDToCSSIdent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
