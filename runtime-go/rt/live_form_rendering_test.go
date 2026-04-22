package rt

// Regression: renderVNode used to emit <textarea value="X"></textarea>
// which every browser ignores — the textarea's displayed value is its
// text content between the tags, not a value attribute. Any innerHTML
// re-parse (full-HTML fallback, ancestor patch) would then wipe the
// user's typing out of the DOM even when the server's model was
// correctly tracking it. Same class of bug for <select> which needs
// `selected` on the matching <option>, not `value=` on the select.

import (
	"strings"
	"testing"
)

func TestRenderTextareaEmitsValueAsContent(t *testing.T) {
	ta := VNode{
		Kind:  "element",
		Tag:   "textarea",
		Attrs: map[string]string{"name": "bio", "value": "hello world"},
	}
	out := renderVNode(ta, map[string]any{})

	// value attribute must NOT appear on textarea (browsers ignore it).
	if strings.Contains(out, `value="hello world"`) {
		t.Errorf("textarea must not carry value= attribute; got %q", out)
	}
	// Text content between <textarea> and </textarea> must carry the value.
	if !strings.Contains(out, `>hello world</textarea>`) {
		t.Errorf("textarea text content missing expected value; got %q", out)
	}
	// name attribute stays as normal.
	if !strings.Contains(out, `name="bio"`) {
		t.Errorf("non-value attrs must still render; got %q", out)
	}
}

func TestRenderTextareaEscapesHTMLInValue(t *testing.T) {
	ta := VNode{
		Kind:  "element",
		Tag:   "textarea",
		Attrs: map[string]string{"value": `<script>alert("xss")</script>`},
	}
	out := renderVNode(ta, map[string]any{})
	if strings.Contains(out, "<script>") {
		t.Errorf("textarea content must be HTML-escaped; got %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("textarea content missing escaped form; got %q", out)
	}
}

func TestRenderTextareaPrefersChildrenOverValueAttr(t *testing.T) {
	// If the user wrote `textarea [] [ text "child content" ]`, the
	// children win — the value attr splice only fires when children
	// are empty, preserving existing behaviour.
	ta := VNode{
		Kind:  "element",
		Tag:   "textarea",
		Attrs: map[string]string{"value": "from-attr"},
		Children: []VNode{
			{Kind: "text", Text: "from-child"},
		},
	}
	out := renderVNode(ta, map[string]any{})
	if strings.Contains(out, "from-attr") {
		t.Errorf("explicit children must override value attr; got %q", out)
	}
	if !strings.Contains(out, "from-child") {
		t.Errorf("child content missing; got %q", out)
	}
}

func TestRenderSelectMarksMatchingOption(t *testing.T) {
	sel := VNode{
		Kind:  "element",
		Tag:   "select",
		Attrs: map[string]string{"name": "role", "value": "artist"},
		Children: []VNode{
			{Kind: "element", Tag: "option",
				Attrs:    map[string]string{"value": "guardian"},
				Children: []VNode{{Kind: "text", Text: "Guardian"}}},
			{Kind: "element", Tag: "option",
				Attrs:    map[string]string{"value": "artist"},
				Children: []VNode{{Kind: "text", Text: "Artist"}}},
		},
	}
	out := renderVNode(sel, map[string]any{})

	// select itself must NOT carry value= (browsers ignore it).
	if strings.Contains(out, `<select`) && strings.Contains(out, `value="artist"`) && !strings.Contains(out, `<option`) {
		// A bit defensive — just check the first attribute block.
	}
	selectOpen := strings.Index(out, "<select")
	selectClose := strings.Index(out, ">")
	if selectOpen != -1 && selectClose != -1 && selectClose > selectOpen {
		if strings.Contains(out[selectOpen:selectClose], `value="artist"`) {
			t.Errorf("select must not carry value= attribute; got %q", out)
		}
	}
	// The matching option must be marked selected.
	if !strings.Contains(out, `selected="selected"`) {
		t.Errorf("matching option missing selected attr; got %q", out)
	}
	// Only the "artist" option should be selected; "guardian" must not.
	guardianIdx := strings.Index(out, "Guardian")
	artistIdx := strings.Index(out, "Artist")
	guardianOpt := out[:guardianIdx]
	artistOpt := out[guardianIdx:artistIdx]
	if strings.Contains(guardianOpt, `selected="selected"`) {
		t.Errorf("guardian option must not be marked selected; got %q", out)
	}
	if !strings.Contains(artistOpt, `selected="selected"`) {
		t.Errorf("artist option should be marked selected; got %q", out)
	}
}

func TestRenderSelectUnselectsStaleSelected(t *testing.T) {
	// If the user wrote an option with selected= already set but the
	// select's value points elsewhere, we should flip off the stale
	// selected so the displayed choice matches the value.
	sel := VNode{
		Kind:  "element",
		Tag:   "select",
		Attrs: map[string]string{"value": "b"},
		Children: []VNode{
			{Kind: "element", Tag: "option",
				Attrs:    map[string]string{"value": "a", "selected": "selected"},
				Children: []VNode{{Kind: "text", Text: "A"}}},
			{Kind: "element", Tag: "option",
				Attrs:    map[string]string{"value": "b"},
				Children: []VNode{{Kind: "text", Text: "B"}}},
		},
	}
	out := renderVNode(sel, map[string]any{})
	// `A` option should no longer carry selected=.
	aIdx := strings.Index(out, ">A<")
	bIdx := strings.Index(out, ">B<")
	if aIdx == -1 || bIdx == -1 {
		t.Fatalf("both options must be rendered; got %q", out)
	}
	// Grab just the <option ...> opening tag for A.
	aOptStart := strings.LastIndex(out[:aIdx], "<option")
	aOpt := out[aOptStart:aIdx]
	if strings.Contains(aOpt, "selected") {
		t.Errorf("option A must no longer be selected; got %q", aOpt)
	}
	bOptStart := strings.LastIndex(out[:bIdx], "<option")
	bOpt := out[bOptStart:bIdx]
	if !strings.Contains(bOpt, `selected="selected"`) {
		t.Errorf("option B should be marked selected; got %q", bOpt)
	}
}

func TestRenderInputValueStillAttr(t *testing.T) {
	// Regular inputs: value lives as an attribute — that's the correct
	// HTML semantics for <input>. The fix must not accidentally strip
	// it there.
	in := VNode{
		Kind:  "element",
		Tag:   "input",
		Attrs: map[string]string{"type": "text", "value": "hello"},
	}
	out := renderVNode(in, map[string]any{})
	if !strings.Contains(out, `value="hello"`) {
		t.Errorf("input must keep value= attribute; got %q", out)
	}
}
