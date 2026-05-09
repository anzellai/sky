package rt

import "testing"

// ensureFocusVisible should LEAVE scrollY alone when the focused
// element is already inside the visible viewport.
func TestEnsureFocusVisible_AlreadyVisible(t *testing.T) {
	focs := []focusable{
		{row: 5, h: 1},
		{row: 10, h: 3},
	}
	got := ensureFocusVisible(focs, 0, 3, 20, 100)
	if got != 3 {
		t.Errorf("scrollY=%d want 3 (no change — element at row 5 is in [3,23))", got)
	}
}

// Element below the bottom of the viewport → scroll DOWN so the
// element's bottom row is at the bottom of the viewport.
func TestEnsureFocusVisible_ScrollsDownToReach(t *testing.T) {
	focs := []focusable{
		{row: 50, h: 3}, // bottom = 52
	}
	got := ensureFocusVisible(focs, 0, 0, 20, 100)
	want := 52 - 20 + 1 // 33
	if got != want {
		t.Errorf("scrollY=%d want %d", got, want)
	}
}

// Element above the top of the viewport (scrolled past it) →
// scroll UP so the element's top row is at the top of the viewport.
func TestEnsureFocusVisible_ScrollsUpToReach(t *testing.T) {
	focs := []focusable{
		{row: 5, h: 1},
	}
	got := ensureFocusVisible(focs, 0, 50, 20, 100)
	if got != 5 {
		t.Errorf("scrollY=%d want 5", got)
	}
}

// Clamp scrollY to [0, contentH-rows] so we never scroll past the
// bottom of content.
func TestEnsureFocusVisible_ClampsAtBottom(t *testing.T) {
	focs := []focusable{
		{row: 95, h: 5}, // bottom = 99
	}
	got := ensureFocusVisible(focs, 0, 0, 20, 100)
	want := 100 - 20 // 80 — can't scroll further
	if got != want {
		t.Errorf("scrollY=%d want %d (clamped to maxScroll)", got, want)
	}
}

// When contentH <= rows there's nothing to scroll; result is 0.
func TestEnsureFocusVisible_NoOverflow(t *testing.T) {
	focs := []focusable{{row: 10, h: 1}}
	got := ensureFocusVisible(focs, 0, 5, 20, 15)
	if got != 0 {
		t.Errorf("scrollY=%d want 0 (contentH<rows so no scroll)", got)
	}
}

// Out-of-range focusIdx → leave scrollY unchanged.
func TestEnsureFocusVisible_BadIndex(t *testing.T) {
	focs := []focusable{{row: 10, h: 1}}
	if got := ensureFocusVisible(focs, -1, 7, 20, 100); got != 7 {
		t.Errorf("focusIdx=-1: scrollY=%d want 7", got)
	}
	if got := ensureFocusVisible(focs, 5, 7, 20, 100); got != 7 {
		t.Errorf("focusIdx=5 (oob): scrollY=%d want 7", got)
	}
}
