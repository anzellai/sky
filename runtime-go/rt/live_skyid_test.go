package rt

// Regression tests for structural sky-ids (step 1 of the input-authority
// protocol — docs/skylive/input-authority-protocol.md). The old scheme
// emitted purely positional ids (`r.2.0.0.3`) which collided between
// structurally-different subtrees at the same positional depth, letting
// the diff walker merge e.g. a `<input>` and a `<fieldset>` as if they
// were the same element. Under the new scheme every segment embeds the
// tag and (when present) a key, so colliding siblings are impossible.

import (
	"strings"
	"testing"
)

// Element VNode helper — matches the shape assignSkyIDs walks without
// pulling in the full velement builder (which requires the Sky runtime
// boot, unnecessary for pure id assignment tests).
func el(tag string, attrs map[string]string, children ...VNode) VNode {
	if attrs == nil {
		attrs = map[string]string{}
	}
	return VNode{
		Kind:     "element",
		Tag:      tag,
		Attrs:    attrs,
		Events:   map[string]any{},
		Children: children,
	}
}

func txt(s string) VNode { return VNode{Kind: "text", Text: s} }

// TestSkyIDCollisionFree — the signIn/signUp duplication repro. Two
// trees share an outer `<div class="sc-container-sm">` wrapper but the
// form inside diverges: signIn has a 2-field form, signUp wraps a role
// `<fieldset>` in front. Under positional-only ids the email input on
// signIn and the fieldset on signUp would share `r.2.0.0.3`. After the
// fix, their sky-ids must differ.
func TestSkyIDCollisionFree(t *testing.T) {
	signIn := el("div", nil,
		el("form", nil,
			el("div", nil,
				el("label", nil, txt("Email")),
				el("input", map[string]string{"name": "email", "type": "email"}),
				el("label", nil, txt("Password")),
				el("input", map[string]string{"name": "password", "type": "password"}),
				el("button", map[string]string{"type": "submit"}, txt("Sign in")),
			),
		),
	)
	signUp := el("div", nil,
		el("form", nil,
			el("div", nil,
				el("fieldset", map[string]string{"name": "role"},
					el("input", map[string]string{"name": "role", "type": "radio"}),
					el("input", map[string]string{"name": "role", "type": "radio"}),
				),
				el("label", nil, txt("Email")),
				el("input", map[string]string{"name": "email", "type": "email"}),
				el("label", nil, txt("Password")),
				el("input", map[string]string{"name": "password", "type": "password"}),
				el("button", map[string]string{"type": "submit"}, txt("Sign up")),
			),
		),
	)
	assignSkyIDs(&signIn, "r")
	assignSkyIDs(&signUp, "r")

	// Walk to the inner div (r.0#form.0#div) on each side and compare
	// the child ids. Under the old scheme signIn[1] (email input) and
	// signUp[1] (label) shared `r.0.0.1`; that must not recur.
	inCtr := &signIn.Children[0].Children[0]
	upCtr := &signUp.Children[0].Children[0]
	if len(inCtr.Children) == 0 || len(upCtr.Children) == 0 {
		t.Fatalf("container empty: in=%d up=%d", len(inCtr.Children), len(upCtr.Children))
	}
	seen := map[string]bool{}
	for _, c := range inCtr.Children {
		if c.SkyID != "" {
			seen[c.SkyID] = true
		}
	}
	for _, c := range upCtr.Children {
		if c.SkyID == "" {
			continue
		}
		if seen[c.SkyID] {
			t.Errorf("collision: signIn and signUp share sky-id %q", c.SkyID)
		}
	}
}

// TestSkyIDStableAcrossRenders — two identical renders of the same
// tree must assign identical sky-ids. Without this the diff can't
// correlate old→new at all.
func TestSkyIDStableAcrossRenders(t *testing.T) {
	mk := func() VNode {
		return el("div", nil,
			el("header", nil, txt("hello")),
			el("main", nil,
				el("form", nil,
					el("input", map[string]string{"name": "q"}),
				),
			),
		)
	}
	a, b := mk(), mk()
	assignSkyIDs(&a, "r")
	assignSkyIDs(&b, "r")

	var collect func(n *VNode, into *[]string)
	collect = func(n *VNode, into *[]string) {
		if n.SkyID != "" {
			*into = append(*into, n.SkyID)
		}
		for i := range n.Children {
			collect(&n.Children[i], into)
		}
	}
	var sa, sb []string
	collect(&a, &sa)
	collect(&b, &sb)
	if strings.Join(sa, "|") != strings.Join(sb, "|") {
		t.Errorf("identical trees produced different sky-ids:\n  a: %v\n  b: %v", sa, sb)
	}
}

// TestSkyIDNameBasedKey — inputs with a `name` attribute get that
// name appended so the id survives structural shuffling (e.g. rearranged
// form fields). Without the key, a reordered form confuses the diff.
func TestSkyIDNameBasedKey(t *testing.T) {
	f := el("form", nil,
		el("input", map[string]string{"name": "email"}),
		el("input", map[string]string{"name": "password"}),
	)
	assignSkyIDs(&f, "r")

	if !strings.Contains(f.Children[0].SkyID, ":email") {
		t.Errorf("email input missing name key: %q", f.Children[0].SkyID)
	}
	if !strings.Contains(f.Children[1].SkyID, ":password") {
		t.Errorf("password input missing name key: %q", f.Children[1].SkyID)
	}
	// Swapping order should produce different ids from the original slots
	// (positional index changes) — identity still tracked via the name.
	g := el("form", nil,
		el("input", map[string]string{"name": "password"}),
		el("input", map[string]string{"name": "email"}),
	)
	assignSkyIDs(&g, "r")
	if f.Children[0].SkyID == g.Children[0].SkyID {
		t.Errorf("unexpected match after reorder: both %q", f.Children[0].SkyID)
	}
	// But the email input is still identifiable in both trees — the diff
	// can find it by scanning for `:email` when structural position moves.
	if !strings.HasSuffix(g.Children[1].SkyID, ":email") {
		t.Errorf("reordered email lost its key: %q", g.Children[1].SkyID)
	}
}

// TestSkyIDExplicitKey — an explicit `sky-key` attribute takes
// precedence over name-based auto-keying. Consumers use this for
// list items where there's no natural `name` attr.
func TestSkyIDExplicitKey(t *testing.T) {
	ul := el("ul", nil,
		el("li", map[string]string{"sky-key": "todo-42"}, txt("first")),
		el("li", map[string]string{"sky-key": "todo-17"}, txt("second")),
	)
	assignSkyIDs(&ul, "r")
	if !strings.HasSuffix(ul.Children[0].SkyID, ":todo-42") {
		t.Errorf("first li missing explicit key: %q", ul.Children[0].SkyID)
	}
	if !strings.HasSuffix(ul.Children[1].SkyID, ":todo-17") {
		t.Errorf("second li missing explicit key: %q", ul.Children[1].SkyID)
	}
}

// TestSkyIDKeySanitisation — user-supplied keys with quote/dot/hash
// characters must be coerced to `[A-Za-z0-9_-]+` so they can't break
// the sky-id grammar or escape HTML attribute quoting.
func TestSkyIDKeySanitisation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc", "abc"},
		{"a.b", "a_b"},
		{"a#b", "a_b"},
		{`a"b`, "a_b"},
		{"a:b", "a_b"},
		{"user@example.com", "user_example_com"},
		{"foo-bar_baz", "foo-bar_baz"},
	}
	for _, tc := range cases {
		if got := sanitiseSkyIDKey(tc.in); got != tc.want {
			t.Errorf("sanitiseSkyIDKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSkyIDTextChildrenDontGetIDs — text/raw nodes must remain unstamped.
// assignSkyIDs uses positional index, so text children still occupy an
// index in the parent's Children slice, but their SkyID stays "".
func TestSkyIDTextChildrenDontGetIDs(t *testing.T) {
	tree := el("p", nil,
		txt("before"),
		el("span", nil, txt("middle")),
		txt("after"),
	)
	assignSkyIDs(&tree, "r")
	if tree.Children[0].SkyID != "" {
		t.Errorf("text child got sky-id: %q", tree.Children[0].SkyID)
	}
	if tree.Children[2].SkyID != "" {
		t.Errorf("text child got sky-id: %q", tree.Children[2].SkyID)
	}
	// Span sits at index 1 — its id must reflect that index regardless
	// of the text siblings being unstamped.
	if !strings.HasPrefix(tree.Children[1].SkyID, "r.1#span") {
		t.Errorf("span id doesn't reflect position: %q", tree.Children[1].SkyID)
	}
}
