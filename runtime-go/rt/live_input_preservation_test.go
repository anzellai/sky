package rt

// Regression tests for the three coordinated input-preservation bugs
// described in docs/skylive/architecture.md §Input preservation.
//
// Bug 1 — empty patches must produce a JSON ack, not an HTML full-body
// fallback. Triggered every time input-authority alignment drops a
// patch list to zero (the model advanced but the client already has
// the typed value). Before the fix, empty patches fell through to
// writeEventHTML and the client's __skyPatch swap recreated every
// input — blanking uncontrolled fields like password.
//
// Bug 2 — __skyReplaceHTMLPreservingFocus must preserve every
// uncontrolled input across the swap, not just the focused one.
//
// Bug 3 — __skyApplyPatches must skip patches that touch the focused
// SELECT's subtree, otherwise an open native dropdown collapses on
// scheduled re-renders.

import (
	"strings"
	"testing"
)

// ── Bug 1 ─────────────────────────────────────────────────────────

// TestPatchesAreFullReplaceRejectsEmpty pins the contract the dispatch
// branch relies on: an empty patch list MUST NOT be classified as a
// full-replace, otherwise the dispatch fallthrough would route empty
// patches to writeEventHTML (the regression — full-body swap blanks
// uncontrolled inputs).
func TestPatchesAreFullReplaceRejectsEmpty(t *testing.T) {
	if patchesAreFullReplace(nil) {
		t.Error("patchesAreFullReplace(nil) must be false; otherwise empty patches downgrade to HTML")
	}
	if patchesAreFullReplace([]Patch{}) {
		t.Error("patchesAreFullReplace([]) must be false")
	}
}

// TestEmptyAlignedDiffStaysJson is the load-bearing integration test
// for Bug 1. It drives diffTrees with a prev/new pair that differs ONLY
// in a controlled input's value attr, plus a clientState advertising
// the new value as already-present on the client (the input-authority
// alignment scenario). Expected: empty patch list — confirming
// dispatch's post-fix branch will route to writeEventJSON, not
// writeEventHTML.
//
// Without this fence, regressing the alignment OR the
// patchesAreFullReplace(nil) check would silently let the HTML
// fallback path re-engage on every keystroke, blanking passwords and
// other uncontrolled fields.
func TestEmptyAlignedDiffStaysJson(t *testing.T) {
	// Prev: form with empty email (the user's last seen render).
	prev := el("form", nil,
		el("input", map[string]string{
			"type":  "email",
			"name":  "email",
			"value": "",
		}),
	)
	assignSkyIDs(&prev, "r")
	emailID := prev.Children[0].SkyID

	// New: same shape, but server now wants value="alice@example.com"
	// (the model advanced — body2 != "" upstream). The client however
	// has already typed this exact value, so its inputState advertises
	// it. Alignment should drop the patch.
	newTree := el("form", nil,
		el("input", map[string]string{
			"type":  "email",
			"name":  "email",
			"value": "alice@example.com",
		}),
	)
	assignSkyIDs(&newTree, "r")

	clientState := map[string]string{
		emailID: "alice@example.com",
	}
	patches := diffTrees(&prev, &newTree, clientState)
	if len(patches) != 0 {
		t.Fatalf("aligned diff must be empty; got %d patches: %+v", len(patches), patches)
	}
	// And the empty result must NOT classify as full-replace, so
	// dispatch routes to writeEventJSON.
	if patchesAreFullReplace(patches) {
		t.Error("empty aligned patches misclassified as full-replace; HTML fallback would engage")
	}
}

// TestNonEmptyDiffStillJson is the inverse fence: a real diff should
// also stay on the JSON path (single attr patch, not full-replace).
// Without this, an over-eager refactor that downgrades small patches
// to HTML would slip through.
func TestNonEmptyDiffStillJson(t *testing.T) {
	prev := el("form", nil,
		el("input", map[string]string{"type": "email", "value": ""}),
	)
	assignSkyIDs(&prev, "r")
	newTree := el("form", nil,
		el("input", map[string]string{"type": "email", "value": "x"}),
	)
	assignSkyIDs(&newTree, "r")
	patches := diffTrees(&prev, &newTree, nil) // no alignment
	if len(patches) == 0 {
		t.Fatalf("real diff was unexpectedly aligned away")
	}
	if patchesAreFullReplace(patches) {
		t.Error("small attr patch misclassified as full-replace")
	}
}

// ── Bug 2 ─────────────────────────────────────────────────────────

// TestLiveJS_PreserveAllUncontrolledInputs guards the substring shape
// of the rewritten __skyReplaceHTMLPreservingFocus + helpers.
// Substring tests are the right tool here: actual DOM behaviour needs
// a browser harness (Playwright lives downstream in user repos), but
// a refactor that drops the helpers or changes their names would
// silently regress every Sky.Live signup form. These markers are the
// regression fence.
func TestLiveJS_PreserveAllUncontrolledInputs(t *testing.T) {
	js := liveClientJS
	required := []string{
		// Helper that decides "is this server-rendered placeholder
		// uncontrolled, i.e. should we splice the live node across".
		`function __skyPlaceholderUncontrolled(placeholder)`,
		// Authority-attr checks — without these, a partially-rendered
		// controlled field would be wrongly preserved.
		`if (placeholder.hasAttribute("value")) return false;`,
		`if (placeholder.hasAttribute("checked")) return false;`,
		`if (placeholder.hasAttribute("selected")) return false;`,
		// TEXTAREA + SELECT specialisations.
		`if (tag === "TEXTAREA")`,
		`if (tag === "SELECT")`,
		// Placeholder lookup with sky-id preferred + name-collision
		// guard (only fall back when exactly one match).
		`function __skyFindPlaceholder(tmp, live)`,
		`if (matches.length === 1) return matches[0];`,
		// Walks ALL inputs, not just activeElement.
		`var liveNodes = container.querySelectorAll("input, textarea, select");`,
		// Focused element is unconditionally preserved.
		`var isFocused = (live === focused);`,
		// Uncontrolled placeholders also get preserved.
		`if (!isFocused && !__skyPlaceholderUncontrolled(placeholder))`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing input-preservation marker: %q", want)
		}
	}
}

// ── Bug 3 ─────────────────────────────────────────────────────────

// TestLiveJS_OpenSelectDefence guards the open-dropdown protection in
// __skyApplyPatches. Without this, a Tick subscription firing while
// the user has a <select> open would close the dropdown mid-pick.
func TestLiveJS_OpenSelectDefence(t *testing.T) {
	js := liveClientJS
	required := []string{
		// Detection: focused SELECT is the proxy for "dropdown open".
		`document.activeElement.tagName === "SELECT"`,
		// Skip rule covers the SELECT itself, ancestors that would
		// re-mount it, and descendants (option attribute changes
		// also collapse the dropdown in some browsers).
		`el === openSel || el.contains(openSel) || openSel.contains(el)`,
		// Comment marker so the intent isn't obscured by a refactor.
		`Open <select> defence`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing open-select defence marker: %q", want)
		}
	}
}
