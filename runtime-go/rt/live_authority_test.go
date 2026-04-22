package rt

// Step 3 — I1 client authority for dirty inputs. Server-side tests
// verify diffNodes honours the clientState hint: when the new model's
// intended value for a form field matches what the client says the
// DOM already shows, no value patch is emitted (the server would
// just round-trip the user's own typing and race against keystrokes).
// When the new model genuinely disagrees (form reset, admin edit),
// the patch IS emitted and Step 3's client-side filter decides at
// apply time whether to take effect.

import (
	"testing"
)

// attr on an element via helper — avoids the velement pipeline,
// which needs more ceremony for attributes.
func elWithAttrs(tag string, kv map[string]string, children ...VNode) VNode {
	return VNode{
		Kind:     "element",
		Tag:      tag,
		Attrs:    kv,
		Events:   map[string]any{},
		Children: children,
	}
}

// TestDiffAlignsToClientValue — server's new intended value matches
// client's reported DOM value. Diff must NOT emit a value patch.
func TestDiffAlignsToClientValue(t *testing.T) {
	old := elWithAttrs("form", nil,
		elWithAttrs("input", map[string]string{
			"name":  "email",
			"value": "stale@old.com",
		}),
	)
	new_ := elWithAttrs("form", nil,
		elWithAttrs("input", map[string]string{
			"name":  "email",
			"value": "a@b.com",
		}),
	)
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")
	emailID := new_.Children[0].SkyID

	// Client has already typed a@b.com — server's new value agrees.
	patches := diffTrees(&old, &new_, map[string]string{
		emailID: "a@b.com",
	})
	for _, p := range patches {
		if p.ID == emailID && p.Attrs != nil {
			if _, ok := p.Attrs["value"]; ok {
				t.Errorf("diff emitted a value patch despite client-server agreement: %+v", p.Attrs)
			}
		}
	}
}

// TestDiffOverridesClientWhenServerDisagrees — update decided the
// model's email should be "Bob" (e.g. admin edit). Diff MUST emit the
// patch so the browser hears about the change; the client-side
// filter in Step 3 decides whether to apply based on focus state.
func TestDiffOverridesClientWhenServerDisagrees(t *testing.T) {
	old := elWithAttrs("form", nil,
		elWithAttrs("input", map[string]string{
			"name":  "email",
			"value": "stale@old.com",
		}),
	)
	new_ := elWithAttrs("form", nil,
		elWithAttrs("input", map[string]string{
			"name":  "email",
			"value": "bob@example.com",
		}),
	)
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")
	emailID := new_.Children[0].SkyID

	// Client says they've typed "draft@typing.com" — server's new
	// model says "bob@example.com". Disagreement → must patch.
	patches := diffTrees(&old, &new_, map[string]string{
		emailID: "draft@typing.com",
	})
	found := false
	for _, p := range patches {
		if p.ID == emailID && p.Attrs != nil {
			if v, ok := p.Attrs["value"]; ok && v == "bob@example.com" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("diff must emit value patch when server disagrees with client; got %+v", patches)
	}
}

// TestDiffLegacyCallerNilClientState — no client state is supplied
// (legacy call path or first render). Must behave like the pre-Step 3
// diff: every changed value emits a patch.
func TestDiffLegacyCallerNilClientState(t *testing.T) {
	old := elWithAttrs("input", map[string]string{"value": "a"})
	new_ := elWithAttrs("input", map[string]string{"value": "b"})
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")
	patches := diffTrees(&old, &new_, nil)
	found := false
	for _, p := range patches {
		if p.Attrs != nil && p.Attrs["value"] == "b" {
			found = true
		}
	}
	if !found {
		t.Errorf("nil clientState must preserve legacy diff behaviour; got %+v", patches)
	}
}

// TestDiffNonInputTagIgnoresClientState — client state entries for
// non-input tags must NOT suppress their patches (would hide real
// server-driven changes like class="error" on a <div>).
func TestDiffNonInputTagIgnoresClientState(t *testing.T) {
	old := elWithAttrs("div", map[string]string{"class": "ok"})
	new_ := elWithAttrs("div", map[string]string{"class": "error"})
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")
	// Nonsense clientState entry for a div — should be ignored.
	patches := diffTrees(&old, &new_, map[string]string{
		new_.SkyID: "something",
	})
	found := false
	for _, p := range patches {
		if p.Attrs != nil && p.Attrs["class"] == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("non-input tag must still receive attr patches despite clientState; got %+v", patches)
	}
}

// TestDiffAuthorityAttrsChecked — `checked` gets the same alignment
// treatment as `value`. Client-reported "true" + server-new "true"
// → no patch. Covers checkbox/radio protection.
func TestDiffAuthorityAttrsChecked(t *testing.T) {
	old := elWithAttrs("input", map[string]string{
		"type":    "checkbox",
		"checked": "false",
	})
	new_ := elWithAttrs("input", map[string]string{
		"type":    "checkbox",
		"checked": "true",
	})
	assignSkyIDs(&old, "r")
	assignSkyIDs(&new_, "r")

	// Client says "true" — server's new value agrees.
	patches := diffTrees(&old, &new_, map[string]string{
		new_.SkyID: "true",
	})
	for _, p := range patches {
		if p.Attrs != nil {
			if _, ok := p.Attrs["checked"]; ok {
				t.Errorf("checked patch should not fire when server matches client; got %+v", p.Attrs)
			}
		}
	}
}
