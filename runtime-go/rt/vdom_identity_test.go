package rt

// vdom_identity_test.go — targeted regressions for node identity across a
// diff, on the simulated browser in vdom_converge_test.go. Each test names the
// audit finding it pins.

import (
	"strings"
	"testing"
)

func vel(tag string, attrs map[string]string, kids ...VNode) VNode {
	if attrs == nil {
		attrs = map[string]string{}
	}
	return VNode{Kind: "element", Tag: tag, Attrs: attrs, Events: map[string]any{}, Children: kids}
}

func vtxt(s string) VNode { return VNode{Kind: "text", Text: s} }

// roundTrip diffs old→new, applies the patches to old's DOM with both
// appliers, checks convergence, and returns the two resulting documents.
func roundTrip(t *testing.T, oldV, newV VNode) (live, spa *dnode, patches []Patch) {
	t.Helper()
	o, n := prep(oldV), prep(newV)
	patches = diffTrees(&o, &n, nil)
	docs := [2]*dnode{}
	for k, isSpa := range []bool{false, true} {
		doc := docFor(o, isSpa)
		docs[k] = doc
		before := map[string]int{}
		doc.walk(func(d *dnode) {
			if d.kind == "element" {
				before[d.attrs["sky-id"]] = d.serial
			}
		})
		if missed := applyPatches(doc, patches, &n, isSpa); missed != 0 {
			t.Fatalf("spa=%v: %d patch targets missing\n%s", isSpa, missed, fmtPatches(patches))
		}
		if got, want := canon(doc.kids, isSpa), canon(docFor(n, isSpa).kids, isSpa); got != want {
			t.Fatalf("spa=%v: DOM did not converge\nGOT  %s\nWANT %s\n%s", isSpa, got, want, fmtPatches(patches))
		}
	}
	return docs[0], docs[1], patches
}

func serialOf(t *testing.T, doc *dnode, pred func(*dnode) bool) int {
	t.Helper()
	found := 0
	doc.walk(func(d *dnode) {
		if found == 0 && d.kind == "element" && pred(d) {
			found = d.serial
		}
	})
	if found == 0 {
		t.Fatalf("node not found")
	}
	return found
}

func byAttr(k, v string) func(*dnode) bool {
	return func(d *dnode) bool { return d.attrs[k] == v }
}

// K2 / F8 (keyed): a uniquely keyed child's id does not contain its index,
// so inserting a sibling before it does not change it (or its subtree's).
func TestKeyedChildIDIgnoresSiblingInsert(t *testing.T) {
	mk := func(withMsg bool) VNode {
		root := vel("div", nil)
		if withMsg {
			root.Children = append(root.Children, vel("div", map[string]string{"sky-key": "msg"}))
		}
		root.Children = append(root.Children, vel("div", map[string]string{"sky-key": "in"},
			vel("input", map[string]string{"name": "email"})))
		assignSkyIDs(&root, "r")
		return root
	}
	a, b := mk(false), mk(true)
	before := a.Children[0].Children[0].SkyID
	after := b.Children[1].Children[0].SkyID
	if before != after {
		t.Fatalf("keyed+named input changed identity on sibling insert: %s -> %s", before, after)
	}
	if strings.Contains(before, ".0#") || strings.Contains(before, ".1#") {
		t.Fatalf("keyed id still carries an index: %s", before)
	}
}

// A key shared by several siblings (a radio group's common name) cannot
// identify one of them: those ids keep the index and stay unique.
func TestDuplicateKeysKeepIndexAndStayUnique(t *testing.T) {
	root := vel("div", nil,
		vel("input", map[string]string{"type": "radio", "name": "colour", "value": "a"}),
		vel("input", map[string]string{"type": "radio", "name": "colour", "value": "b"}))
	assignSkyIDs(&root, "r")
	a, b := root.Children[0].SkyID, root.Children[1].SkyID
	if a == b {
		t.Fatalf("radio siblings share sky-id %q", a)
	}
	if a != "r.0#input:colour" || b != "r.1#input:colour" {
		t.Fatalf("duplicate-key ids = %q, %q", a, b)
	}
}

// F8 (unkeyed): a message inserted above an input's wrapper — both plain
// divs — must not rebuild the input. The input's DOM node survives (so its
// focus, caret and keystrokes do), on the Live and the Spa applier.
func TestUnkeyedSiblingInsertKeepsInputNode(t *testing.T) {
	field := func(v string) VNode {
		return vel("div", map[string]string{"aria-label": "vv"},
			vel("input", map[string]string{"type": "text", "value": v, "id": "vv"}))
	}
	oldV := vel("div", nil, vel("div", nil, field("abc")))
	newV := vel("div", nil, vel("div", nil, vel("div", nil, vtxt("too long")), field("abcd")))

	o, n := prep(oldV), prep(newV)
	patches := diffTrees(&o, &n, nil)
	for _, isSpa := range []bool{false, true} {
		doc := docFor(o, isSpa)
		inputBefore := serialOf(t, doc, byAttr("id", "vv"))
		if missed := applyPatches(doc, patches, &n, isSpa); missed != 0 {
			t.Fatalf("spa=%v: %d targets missed\n%s", isSpa, missed, fmtPatches(patches))
		}
		if got, want := canon(doc.kids, isSpa), canon(docFor(n, isSpa).kids, isSpa); got != want {
			t.Fatalf("spa=%v: not converged\nGOT  %s\nWANT %s", isSpa, got, want)
		}
		if inputAfter := serialOf(t, doc, byAttr("id", "vv")); inputAfter != inputBefore {
			t.Fatalf("spa=%v: the input was rebuilt by a sibling insert (focus and keystrokes lost)\n%s",
				isSpa, fmtPatches(patches))
		}
	}
}

// K2: a keyed child whose key changes at the same position must not keep
// its OLD sky-id in the DOM. The diff used to patch its attributes in place
// and never the id, so the DOM held an id the server/client no longer knew:
// a later patch at the new id found nothing and a click dispatched the old
// handler.
func TestKeyedKeyChangeLeavesNoStaleID(t *testing.T) {
	row := func(key, label string) VNode {
		return vel("div", map[string]string{"sky-key": key}, vel("button", nil, vtxt(label)))
	}
	live, spa, _ := roundTrip(t,
		vel("div", nil, row("a", "A")),
		vel("div", nil, row("b", "B")))
	for _, doc := range []*dnode{live, spa} {
		if doc.find("r.#div:a") != nil {
			t.Fatalf("stale keyed id r.#div:a still in the DOM: %s", canon(doc.kids, false))
		}
		if doc.find("r.#div:b.0#button") == nil {
			t.Fatalf("new keyed id missing from the DOM: %s", canon(doc.kids, false))
		}
	}
}

// Keyed reorder keeps every node (a move, not a rebuild).
func TestKeyedReorderMovesNodes(t *testing.T) {
	item := func(k string) VNode {
		return vel("li", map[string]string{"sky-key": k}, vel("input", map[string]string{"id": "in-" + k}))
	}
	oldV := vel("ul", nil, item("a"), item("b"), item("c"))
	newV := vel("ul", nil, item("c"), item("a"), item("b"))
	o, n := prep(oldV), prep(newV)
	patches := diffTrees(&o, &n, nil)
	for _, isSpa := range []bool{false, true} {
		doc := docFor(o, isSpa)
		s := map[string]int{}
		for _, k := range []string{"a", "b", "c"} {
			s[k] = serialOf(t, doc, byAttr("id", "in-"+k))
		}
		if missed := applyPatches(doc, patches, &n, isSpa); missed != 0 {
			t.Fatalf("missed %d", missed)
		}
		if got, want := canon(doc.kids, isSpa), canon(docFor(n, isSpa).kids, isSpa); got != want {
			t.Fatalf("not converged\nGOT  %s\nWANT %s", got, want)
		}
		for _, k := range []string{"a", "b", "c"} {
			if serialOf(t, doc, byAttr("id", "in-"+k)) != s[k] {
				t.Fatalf("spa=%v: keyed item %s was rebuilt on reorder\n%s", isSpa, k, fmtPatches(patches))
			}
		}
	}
}

// F6: an injected <style> (Ui hover colour) has a sky-id, so a CSS change
// following the model is patched.
func TestInjectedStyleFollowsModel(t *testing.T) {
	hov := func(c string) VNode {
		return vel("div", nil, vel("div", map[string]string{"data-sky-pc-rules": "h|background-color: " + c}, vtxt("hover me")))
	}
	live, _, patches := roundTrip(t, hov("red"), hov("blue"))
	if len(patches) == 0 {
		t.Fatalf("a hover colour change produced no patch")
	}
	var css string
	live.walk(func(d *dnode) {
		if d.tag == "style" && len(d.kids) > 0 {
			css = d.kids[0].text
		}
	})
	if !strings.Contains(css, "blue") {
		t.Fatalf("injected style still says %q", css)
	}
}

// K3: a root tag change replaces the root itself on every applier (it used
// to nest the new root inside the old one).
func TestRootTagChangeReplacesRoot(t *testing.T) {
	live, spa, patches := roundTrip(t,
		vel("div", nil, vtxt("a")),
		vel("main", nil, vtxt("b")))
	if len(patches) != 1 || patches[0].Replace == nil {
		t.Fatalf("root tag change patches = %s", fmtPatches(patches))
	}
	for _, doc := range []*dnode{live, spa} {
		if len(doc.kids) != 1 || doc.kids[0].tag != "main" {
			t.Fatalf("root after replace: %s", canon(doc.kids, false))
		}
	}
}

// F4: options re-rendered (one inserted before the chosen option) keep the
// model's selection on every applier. The subtree patch used to omit the
// `selected` marking, so the browser fell back to the first option.
func TestSelectKeepsValueWhenOptionsChange(t *testing.T) {
	sel := func(opts ...string) VNode {
		var kids []VNode
		for _, o := range opts {
			kids = append(kids, vel("option", map[string]string{"value": o}, vtxt(o)))
		}
		return vel("div", nil, vel("select", map[string]string{"value": "c"}, kids...))
	}
	live, spa, _ := roundTrip(t, sel("a", "b", "c"), sel("z3", "a", "b", "c"))
	for _, doc := range []*dnode{live, spa} {
		var chosen []string
		doc.walk(func(d *dnode) {
			if d.tag == "option" {
				if _, ok := d.attrs["selected"]; ok {
					chosen = append(chosen, d.attrs["value"])
				}
			}
		})
		if len(chosen) != 1 || chosen[0] != "c" {
			t.Fatalf("selected options after re-render = %v, want [c]", chosen)
		}
	}
}

// W2: a password-reset form followed, in the same slot, by a sign-in form
// whose password field has the same name. The fields are uncontrolled (no
// `value`), so nothing in the tree states what the user typed; a kept
// <input> node showed the new password in the sign-in field. A form whose
// submit handler differs is a different form: it gets fresh nodes.
func formPage(submit string, fields ...string) VNode {
	form := vel("form", nil)
	form.Events["submit"] = SkyADT{SkyName: submit}
	for _, f := range fields {
		form.Children = append(form.Children, vel("input", map[string]string{"name": f, "type": "password", "id": f}))
	}
	return vel("div", nil, vel("div", nil, form))
}

func TestFormSwapGivesFreshFieldNodes(t *testing.T) {
	oldV := formPage("ResetPassword", "password")
	newV := formPage("SignIn", "password", "email")
	o, n := prep(oldV), prep(newV)
	patches := diffTrees(&o, &n, nil)
	for _, isSpa := range []bool{false, true} {
		doc := docFor(o, isSpa)
		before := serialOf(t, doc, byAttr("id", "password"))
		if missed := applyPatches(doc, patches, &n, isSpa); missed != 0 {
			t.Fatalf("spa=%v: %d targets missed\n%s", isSpa, missed, fmtPatches(patches))
		}
		if got, want := canon(doc.kids, isSpa), canon(docFor(n, isSpa).kids, isSpa); got != want {
			t.Fatalf("spa=%v: not converged\nGOT  %s\nWANT %s", isSpa, got, want)
		}
		if after := serialOf(t, doc, byAttr("id", "password")); after == before {
			t.Fatalf("spa=%v: the sign-in form reused the reset form's password <input>, so the typed "+
				"password carries over\n%s", isSpa, fmtPatches(patches))
		}
	}
}

// The same form re-rendered (the same submit handler, a field added) keeps
// its field nodes, so focus and typing survive.
func TestFormRerenderKeepsFieldNodes(t *testing.T) {
	oldV := formPage("SignIn", "password")
	newV := formPage("SignIn", "password", "email")
	o, n := prep(oldV), prep(newV)
	patches := diffTrees(&o, &n, nil)
	for _, isSpa := range []bool{false, true} {
		doc := docFor(o, isSpa)
		before := serialOf(t, doc, byAttr("id", "password"))
		if missed := applyPatches(doc, patches, &n, isSpa); missed != 0 {
			t.Fatalf("spa=%v: %d targets missed\n%s", isSpa, missed, fmtPatches(patches))
		}
		if after := serialOf(t, doc, byAttr("id", "password")); after != before {
			t.Fatalf("spa=%v: a re-render of the same form rebuilt its field\n%s", isSpa, fmtPatches(patches))
		}
	}
}

// W2 with the handlers an app actually has: an eta-expanded `Ui.onSubmit
// ResetPw` is a closure with no constructor name, so two forms' handlers
// looked the same. Two closures from different sites are different forms; the
// same site on the next render is the same form. (The helpers stand for a
// view function; noinline keeps the one code copy a view function has.)
//
//go:noinline
func w2ResetHandler() any { return func(p any) any { return p } }

//go:noinline
func w2SignInHandler() any { return func(p any) any { return []any{p} } }

func TestFormIdentityOfClosureHandlers(t *testing.T) {
	form := func(h any) VNode {
		f := vel("form", nil)
		f.Events["submit"] = h
		return f
	}
	a, b, a2 := form(w2ResetHandler()), form(w2SignInHandler()), form(w2ResetHandler())
	if sameForm(&a, &b) {
		t.Fatalf("forms submitting through closures from different sites read as the same form (%q)", formIdentity(&a))
	}
	if !sameForm(&a, &a2) {
		t.Fatalf("the same closure site on the next render read as a different form: %q vs %q", formIdentity(&a), formIdentity(&a2))
	}
}
