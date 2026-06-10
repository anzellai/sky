package rt

import (
	"strings"
	"testing"
)

// TestSafeViewCall_RecoversFromPanic asserts that liveApp.safeViewCall
// catches a panic in the user's view function and returns a fallback
// VNode instead of propagating the panic up the dispatch goroutine.
//
// Regression for the v0.16.20 production crash where a single
// rt.Coerce panic inside Sky's reflect-based typed-record narrowing
// dropped the whole Sky.Live session on every tab click. The fix
// landed in v0.16.21 — the view function is wrapped in defer/recover,
// and a "Render error" notice renders in place of the panicked view.
//
// See memory/sky_navigation_panic_class.md for the structural class.
func TestSafeViewCall_RecoversFromPanic(t *testing.T) {
	app := &liveApp{
		view: func(any) any {
			panic("simulated rt.Coerce mismatch on State_Model_R")
		},
	}
	vn, panicked := app.safeViewCall(map[string]any{"tab": "Logs"})
	if !panicked {
		t.Fatalf("safeViewCall should have caught the panic; got panicked=false")
	}
	if vn.Kind != "element" || vn.Tag != "div" {
		t.Fatalf("fallback VNode should be a <div>; got Kind=%q Tag=%q",
			vn.Kind, vn.Tag)
	}
	rendered := renderVNode(vn, map[string]any{})
	if !strings.Contains(rendered, "Render error") {
		t.Errorf("fallback should contain user-visible 'Render error' "+
			"banner; got %q", rendered)
	}
	if !strings.Contains(rendered, "simulated rt.Coerce mismatch") {
		t.Errorf("fallback should embed the panic reason; got %q",
			rendered)
	}
	if !strings.Contains(rendered, "session survived") {
		t.Errorf("fallback should reassure the operator that the "+
			"session survived; got %q", rendered)
	}
}

// TestSafeViewCall_NoPanic asserts the wrapper is a transparent
// pass-through when the view function returns cleanly — no fallback,
// no panicked flag. Catches a regression where the defer/recover or
// closure scoping accidentally always set panicked=true.
func TestSafeViewCall_NoPanic(t *testing.T) {
	cleanVN := VNode{Kind: "element", Tag: "p"}
	app := &liveApp{
		view: func(any) any {
			// Return a Sky-Html-shaped value via the same path
			// HtmlToVNode consumes — for the simple <p> case the
			// kernel emits a SkyADT but a typed VNode reaches the
			// caller after HtmlToVNode. For this test the simpler
			// repro is to return a VNode shape that HtmlToVNode
			// passes through.
			_ = cleanVN
			return any(struct {
				Tag  string
				Kind string
			}{Tag: "p", Kind: "element"})
		},
	}
	_, panicked := app.safeViewCall(nil)
	if panicked {
		t.Fatalf("safeViewCall reported panicked=true on a clean view call")
	}
}

// TestRenderViewPanicFallback_TruncatesLongReason asserts the fallback
// VNode caps the embedded panic reason at 200 chars so a long stack
// frame can't break the UI layout.
func TestRenderViewPanicFallback_TruncatesLongReason(t *testing.T) {
	long := strings.Repeat("x", 500)
	vn := renderViewPanicFallback(long)
	rendered := renderVNode(vn, map[string]any{})
	if strings.Contains(rendered, strings.Repeat("x", 250)) {
		t.Errorf("fallback should truncate the reason; rendered "+
			"contains 250 chars of x in a row: %q", rendered[:300])
	}
	if !strings.Contains(rendered, "…") {
		t.Errorf("fallback should show truncation marker; got %q",
			rendered)
	}
}

// TestFirstLines_HappyPath asserts the structured-log helper trims a
// long stack trace to the first N lines.
func TestFirstLines_HappyPath(t *testing.T) {
	in := "line1\nline2\nline3\nline4\nline5"
	got := firstLines(in, 3)
	if got != "line1\nline2\nline3" {
		t.Errorf("firstLines(_, 3) = %q; want first three lines", got)
	}
}

func TestFirstLines_FewerThanN(t *testing.T) {
	in := "only-one-line"
	got := firstLines(in, 5)
	if got != in {
		t.Errorf("firstLines should return input unchanged when "+
			"fewer than N lines; got %q want %q", got, in)
	}
}
