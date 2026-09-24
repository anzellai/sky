package rt

import "testing"

// radioGroup builds a Std.Ui-shaped radio group: a marked container holding one
// <label><input type="radio"></label> per option.
func radioGroup(containerAttrs map[string]string, values ...string) VNode {
	attrs := map[string]string{"data-sky-radio-group": "1"}
	for k, v := range containerAttrs {
		attrs[k] = v
	}
	g := VNode{Kind: "element", Tag: "div", Attrs: attrs}
	for _, v := range values {
		g.Children = append(g.Children, VNode{Kind: "element", Tag: "label", Children: []VNode{
			{Kind: "element", Tag: "input", Attrs: map[string]string{"type": "radio", "value": v}},
		}})
	}
	return g
}

func radioNames(g *VNode) []string {
	var out []string
	var walk func(n *VNode)
	walk = func(n *VNode) {
		if n.Tag == "input" && n.Attrs["type"] == "radio" {
			out = append(out, n.Attrs["name"])
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(g)
	return out
}

// Two Std.Ui radio groups on one page: each radio gets its group's name, the
// two groups get DIFFERENT names, and server and client (two independent
// renders of the same view) agree on them.
func TestRadioGroupsAreNamedPerGroup(t *testing.T) {
	render := func() VNode {
		root := VNode{Kind: "element", Tag: "div", Children: []VNode{
			radioGroup(nil, "s", "m", "l"),
			radioGroup(nil, "red", "blue"),
		}}
		assignSkyIDs(&root, "r")
		applyStyleInjections(&root)
		return root
	}
	a, b := render(), render()
	g1, g2 := radioNames(&a.Children[0]), radioNames(&a.Children[1])
	for _, n := range append(append([]string{}, g1...), g2...) {
		if n == "" {
			t.Fatalf("a Std.Ui radio has no name, so the browser does not group it: %v %v", g1, g2)
		}
	}
	if g1[0] != g1[1] || g1[1] != g1[2] || g2[0] != g2[1] {
		t.Fatalf("radios of one group must share one name: %v %v", g1, g2)
	}
	if g1[0] == g2[0] {
		t.Fatalf("two groups on a page must not share a name (%q)", g1[0])
	}
	if b1 := radioNames(&b.Children[0]); b1[0] != g1[0] {
		t.Fatalf("two renders of the same view must name the group the same: %q vs %q", g1[0], b1[0])
	}
}

// A name the app set wins: on the group (Ui.name on radio's attrs) or on a radio.
func TestRadioGroupKeepsAnAppName(t *testing.T) {
	root := VNode{Kind: "element", Tag: "div", Children: []VNode{radioGroup(map[string]string{"name": "size"}, "s", "m")}}
	root.Children[0].Children[1].Children[0].Attrs["name"] = "own"
	assignSkyIDs(&root, "r")
	applyStyleInjections(&root)
	got := radioNames(&root.Children[0])
	if got[0] != "size" || got[1] != "own" {
		t.Fatalf("want [size own] (group name, then the radio's own), got %v", got)
	}
}
