package rt

import (
	"strings"
	"testing"
)

// L10c — the view-determinism signature must (1) be identical for identical
// trees, (2) DIFFER when a same-model render produces different text (a Time.now
// / Random in view — the nondeterminism we're catching), and (3) NOT be
// perturbed by event-handler closure values (different pointers per render),
// which would false-positive on every render.
func TestVnodeShapeSigCatchesNondeterminismNotClosures(t *testing.T) {
	sig := func(vn VNode) string { var s strings.Builder; vnodeShapeSig(vn, &s); return s.String() }

	base := VNode{Kind: "element", Tag: "div", Attrs: map[string]string{"class": "x"},
		Children: []VNode{{Kind: "text", Text: "hi"}}}
	same := VNode{Kind: "element", Tag: "div", Attrs: map[string]string{"class": "x"},
		Children: []VNode{{Kind: "text", Text: "hi"}}}
	// Same model, different text — as if view formatted Time.now().
	drift := VNode{Kind: "element", Tag: "div", Attrs: map[string]string{"class": "x"},
		Children: []VNode{{Kind: "text", Text: "2026-08-01T18:40:07"}}}

	if sig(base) != sig(same) {
		t.Fatal("identical trees must have identical signatures")
	}
	if sig(base) == sig(drift) {
		t.Fatal("a same-model render with different text must produce a different signature (catches Time.now/Random in view)")
	}

	// Different click closures under the SAME event name must NOT change the sig.
	d1 := base
	d1.Events = map[string]any{"click": func() {}}
	d2 := base
	d2.Events = map[string]any{"click": func() {}}
	if sig(d1) != sig(d2) {
		t.Fatal("different handler closures (same event name) must NOT change the signature — would false-positive every render")
	}
	// But the PRESENCE of a handler is part of the shape.
	if sig(base) == sig(d1) {
		t.Fatal("adding an event handler should change the signature (handler presence is shape)")
	}
}

// L10c gating — the check is OPT-IN (default off) so it never doubles an impure
// view's side effects during normal dev/test runs.
func TestViewDeterminismCheckIsOptIn(t *testing.T) {
	t.Setenv("SKY_LIVE_VIEW_DETERMINISM_CHECK", "")
	if viewDeterminismCheckEnabled() {
		t.Fatal("determinism check must be OFF by default")
	}
	t.Setenv("SKY_LIVE_VIEW_DETERMINISM_CHECK", "1")
	if !viewDeterminismCheckEnabled() {
		t.Fatal("determinism check must be ON when explicitly enabled")
	}
	t.Setenv("ENV", "production")
	if viewDeterminismCheckEnabled() {
		t.Fatal("determinism check must never run in production")
	}
}
