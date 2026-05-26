package rt

import (
	"strings"
	"testing"
)

// TestRenderVNode_AttrOrderDeterministic — locks the alphabetical
// attribute-order invariant introduced when the renderVNode `for k,
// v := range n.Attrs` loop was replaced with a sorted iteration.
//
// Go map iteration is randomised per `runtime.mapiterinit`.  Without
// sorting, the same VNode emits attrs in a different order across
// renders.  That doesn't break the diff (key-lookup is order-
// independent), but it does:
//   - flake golden / snapshot tests that compare HTML byte-for-byte,
//   - inflate log noise (server-rendered HTML appears to change
//     every request),
//   - and bloat sub-tree innerHTML patches when a downstream consumer
//     compares emitted HTML strings before deciding to skip a write.
//
// Lock test: render the same VNode 50× and assert the emitted bytes
// are byte-identical.  Statistical floor — 50 iterations against
// Go's hash-randomised map is more than enough to surface any
// remaining non-sorted iteration site (probability of all 50
// runs accidentally agreeing on the same iteration order for a
// 4-attr map is vanishingly small).
func TestRenderVNode_AttrOrderDeterministic(t *testing.T) {
	n := VNode{
		Kind:  "node",
		Tag:   "div",
		SkyID: "root.0#div",
		Attrs: map[string]string{
			"class":          "foo",
			"data-sky-path":  "/apps",
			"data-test":      "bar",
			"id":             "root",
			"role":           "main",
			"aria-label":     "Dashboard",
			"data-content":   "some content",
			"data-something": "x",
		},
		Children: []VNode{
			{Kind: "text", Text: "hello"},
		},
	}
	handlers := map[string]any{}
	first := renderVNode(n, handlers)
	for i := 0; i < 50; i++ {
		got := renderVNode(n, handlers)
		if got != first {
			t.Errorf("render iteration %d differs from first render:\nfirst:  %s\niter %d: %s",
				i, first, i, got)
			return
		}
	}
	// Spot-check the order: alphabetical, so "aria-label" precedes "class".
	ariaIdx := strings.Index(first, " aria-label=")
	classIdx := strings.Index(first, " class=")
	if ariaIdx < 0 || classIdx < 0 || ariaIdx >= classIdx {
		t.Errorf("expected aria-label before class in sorted order; got: %s", first)
	}
}

// TestRenderVNode_EventOrderDeterministic — same invariant for event
// attributes (sky-click, sky-input, …).  The Events map is iterated
// alongside Attrs; both must be alphabetically sorted for stable
// output.
func TestRenderVNode_EventOrderDeterministic(t *testing.T) {
	n := VNode{
		Kind:  "node",
		Tag:   "button",
		SkyID: "btn.0#button",
		Events: map[string]any{
			"click":     "OnClick",
			"focus":     "OnFocus",
			"blur":      "OnBlur",
			"mouseover": "OnHover",
		},
	}
	handlers := map[string]any{}
	first := renderVNode(n, handlers)
	for i := 0; i < 50; i++ {
		got := renderVNode(n, handlers)
		if got != first {
			t.Errorf("render iteration %d differs (event order non-deterministic):\nfirst:  %s\niter %d: %s",
				i, first, i, got)
			return
		}
	}
	// Alphabetical: blur < click < focus < mouseover.
	idxBlur := strings.Index(first, " sky-blur=")
	idxClick := strings.Index(first, " sky-click=")
	idxFocus := strings.Index(first, " sky-focus=")
	idxMouseover := strings.Index(first, " sky-mouseover=")
	if idxBlur < 0 || idxClick < 0 || idxFocus < 0 || idxMouseover < 0 {
		t.Errorf("missing one or more event attrs: %s", first)
		return
	}
	if !(idxBlur < idxClick && idxClick < idxFocus && idxFocus < idxMouseover) {
		t.Errorf("event attrs not alphabetically ordered: %s", first)
	}
}
