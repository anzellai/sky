package rt

import (
	"strings"
	"testing"
)

// Regression for the textarea diff path. When the server's model
// updates a textarea's value, the diff must emit an `attrs.value`
// patch (NOT a `text` patch that would replace the whole element's
// textContent and not a `html` patch that would re-mount the
// textarea and blow away focus / cursor position).
//
// Two parts:
//
//  1. Server-side: `diffNodes` must produce `Patch{Attrs:
//     {"value": newText}}` for a textarea whose value attr changed.
//     The `isFormInputTag` check at the top of `diffNodes`
//     additionally consults `clientState` to skip the patch when
//     the user's in-flight typing matches the server's intended
//     value — a useful no-op when reconciliation rounds line up.
//
//  2. Client-side: `__skyApplyPatches` must apply the `value` attr
//     by setting both the attribute (so server-rendered HTML reads
//     back correctly) AND the `.value` DOM property (so the
//     textarea's displayed content updates). The dirty-aware filter
//     suppresses the value attr when the user is mid-keystroke,
//     preserving their typing.
//
// User-facing concern (raised during the v0.13.4 review): textarea
// does NOT take a `value` HTML attribute — the displayed text lives
// in the element's text content. The runtime works around this by
// stripping the `value` attr at render time and splicing it as the
// textarea's text content (`live.go`'s renderVNode special case),
// but the DIFF path operates on the VNode's `Attrs` map directly,
// so a textarea VNode whose value changes still flows through the
// attrs-diff path uniformly with `<input>` / `<select>`.

func TestTextareaDiff_ValueChangeEmitsAttrsPatch(t *testing.T) {
	old := &VNode{
		Kind:  "element",
		Tag:   "textarea",
		SkyID: "r.0#textarea",
		Attrs: map[string]string{
			"value":       "hello",
			"sky-id":      "r.0#textarea",
			"placeholder": "type",
		},
		Children: []VNode{},
	}
	new_ := &VNode{
		Kind:  "element",
		Tag:   "textarea",
		SkyID: "r.0#textarea",
		Attrs: map[string]string{
			"value":       "new content",
			"sky-id":      "r.0#textarea",
			"placeholder": "type",
		},
		Children: []VNode{},
	}

	var patches []Patch
	diffNodes(old, new_, nil, &patches)

	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d: %+v", len(patches), patches)
	}
	p := patches[0]
	if p.HTML != nil {
		t.Fatalf("textarea value change emitted HTML patch (would re-mount + blow focus); want attrs patch: %+v", p)
	}
	if p.Text != nil {
		t.Fatalf("textarea value change emitted text patch (would replace textContent); want attrs patch: %+v", p)
	}
	if p.Attrs == nil {
		t.Fatalf("expected Attrs patch, got nil")
	}
	if got := p.Attrs["value"]; got != "new content" {
		t.Fatalf("Attrs[value]: got %q, want %q", got, "new content")
	}
	if p.ID != "r.0#textarea" {
		t.Fatalf("Patch.ID: got %q, want %q", p.ID, "r.0#textarea")
	}
}

func TestTextareaDiff_DirtyClientValueSkipsPatch(t *testing.T) {
	// When the client's in-flight typing already matches the server's
	// intended next value, the diff suppresses the value attr — the
	// DOM is already correct and emitting the patch would needlessly
	// stomp on the user's selection / cursor position.
	old := &VNode{
		Kind:  "element",
		Tag:   "textarea",
		SkyID: "r.0#textarea",
		Attrs: map[string]string{"value": "hello"},
	}
	new_ := &VNode{
		Kind:  "element",
		Tag:   "textarea",
		SkyID: "r.0#textarea",
		Attrs: map[string]string{"value": "user typed this"},
	}
	clientState := map[string]string{"r.0#textarea": "user typed this"}

	var patches []Patch
	diffNodes(old, new_, clientState, &patches)

	if len(patches) != 0 {
		t.Fatalf("expected 0 patches (client value matches new value); got %d: %+v",
			len(patches), patches)
	}
}

func TestTextareaDiff_DiffMatchesInputAndSelect(t *testing.T) {
	// Textarea must be treated uniformly with input/select by
	// `isFormInputTag`. A regression where `textarea` got dropped
	// from the form-input tag set would silently stomp on user
	// typing — the diff would fall back to emitting value patches
	// without consulting clientState.
	if !isFormInputTag("textarea") {
		t.Fatalf("textarea must be a form-input tag (uniform client-value alignment)")
	}
	if !isFormInputTag("input") {
		t.Fatalf("input must be a form-input tag")
	}
	if !isFormInputTag("select") {
		t.Fatalf("select must be a form-input tag")
	}
	if isFormInputTag("div") {
		t.Fatalf("div must NOT be a form-input tag")
	}
}

func TestTextareaRender_ValueAttrSplicedAsTextContent(t *testing.T) {
	// End-to-end render check: textarea VNode with `value` attr
	// produces `<textarea ...>value</textarea>` — the value attr is
	// stripped from the open tag and spliced as text content. This
	// is the on-wire contract Std.Ui.Input.multiline depends on
	// (the Sky-source wrapper sets `value=cfg.text` on the
	// TaggedNode and trusts the runtime to do the splice).
	n := VNode{
		Kind:  "element",
		Tag:   "textarea",
		SkyID: "r.0#textarea",
		Attrs: map[string]string{
			"value":       "line 1\nline 2",
			"placeholder": "type",
		},
	}
	out := renderVNode(n, map[string]any{})

	if strings.Contains(out, `value="`) {
		t.Fatalf("textarea must NOT carry value= attribute (browser ignores): %q", out)
	}
	if !strings.Contains(out, `>line 1
line 2</textarea>`) {
		t.Fatalf("textarea text content missing or wrong: %q", out)
	}
	if !strings.Contains(out, `placeholder="type"`) {
		t.Fatalf("placeholder attr missing: %q", out)
	}
}

func TestTextareaJS_ApplyPatchesSetsValueProperty(t *testing.T) {
	// Belt-and-braces: the JS apply-patches code must include the
	// `el.value = v` sync for the textarea / input value attr,
	// because <textarea>'s displayed text is its DOM `.value` (the
	// `value` attribute would have NO visual effect even if set).
	// Without this sync, the diff would set the attribute but the
	// textarea on screen would still show the old text.
	js := liveClientJS
	// `if (k === "value" && ("value" in el)) el.value = v;` — must
	// appear verbatim in the inlined JS. Whitespace tolerant.
	if !strings.Contains(js, `el.value = v`) {
		t.Fatalf("__skyApplyPatches must sync el.value when value attr changes; JS body missing this line")
	}
	if !strings.Contains(js, `"value" in el`) {
		t.Fatalf("expected the `\"value\" in el` guard; missing")
	}
	// Dirty-aware: the user's in-flight typing must not be stomped.
	if !strings.Contains(js, `__skyIsDirty`) {
		t.Fatalf("__skyApplyPatches must consult __skyIsDirty before applying authority attrs")
	}
}

// Cursor / selection preservation when a value patch lands on a
// focused input/textarea. The user-visible scenario: they click into
// a multiline editor, place the cursor mid-text (e.g. position 5 in
// "hello world"), pause briefly so their dirty flag clears, and the
// server pushes a fresh value via SSE (background tick, collab edit
// from another tab, scheduled re-render). Without the explicit
// selection snapshot + restore in `__skyApplyPatches`, the naive
// `el.value = v` assignment resets the cursor to the END of the new
// string — losing the user's edit position mid-task.
//
// The fix: snapshot `selectionStart` / `selectionEnd` / `scrollTop`
// BEFORE setting `el.value`, then call `setSelectionRange` AFTER,
// clamped to the new length so a shorter server value doesn't throw
// `RangeError`. Scroll restoration matters for multi-line textareas
// where the cursor sat below the visible area.
func TestTextareaJS_PreservesCursorOnValueApply(t *testing.T) {
	js := liveClientJS
	// Snapshot path: must read selectionStart / selectionEnd BEFORE
	// setting el.value.
	for _, needle := range []string{
		"savedSelStart",
		"savedSelEnd",
		"el.selectionStart",
		"el.selectionEnd",
		"el === document.activeElement",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("cursor-snapshot path missing %q in liveJS; cursor will jump to end on value updates", needle)
		}
	}
	// Restore path: must call setSelectionRange AFTER value applied,
	// clamped to new length to avoid RangeError on shorter values.
	for _, needle := range []string{
		"setSelectionRange",
		"Math.min(savedSelStart",
		"valueChanged",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("cursor-restore path missing %q in liveJS", needle)
		}
	}
	// Tag scope: only INPUT / TEXTAREA need cursor preservation
	// (SELECT / BUTTON / etc. have no caret). The check must NOT
	// fire on arbitrary elements — otherwise it would throw on
	// elements without setSelectionRange.
	if !strings.Contains(js, `el.tagName === "INPUT" || el.tagName === "TEXTAREA"`) {
		t.Fatalf("cursor preservation must be scoped to INPUT/TEXTAREA tags")
	}
}

// End-to-end documentation of the textarea cursor-preservation
// contract — captured in one test so future regressions surface
// with the design intent visible at the failure line.
//
//	BEFORE value patch:
//	  +---------------------+
//	  | hello[cursor] world |  selectionStart=5, selectionEnd=5
//	  +---------------------+
//
//	Server pushes: attrs.value = "hello, world"  (10 → 12 chars)
//
//	AFTER value patch:
//	  +---------------------+
//	  | hello[cursor], world|  selectionStart=5, selectionEnd=5
//	  +---------------------+    (cursor stayed at offset 5)
//
// If the cursor-preservation path regresses, the JS body would
// degrade to bare `el.value = v` which sets cursor to end (=12),
// inconvenient mid-edit.
func TestTextareaJS_CursorPreservationContract(t *testing.T) {
	js := liveClientJS
	// The two halves of the contract must both be present.
	if !strings.Contains(js, "hadFocus") {
		t.Fatalf("cursor preservation predicates missing — value-apply path no longer remembers focus state")
	}
	// Without the `valueChanged` flag the restore path would fire
	// on every attrs patch (even ones that didn't touch value),
	// uselessly thrashing the cursor.
	if !strings.Contains(js, "valueChanged = true") {
		t.Fatalf("cursor restore must only fire when value actually changed")
	}
}
