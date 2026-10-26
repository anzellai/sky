package rt

// spa_select_defer_test.go — a focused <select> in Sky.Spa (spaPlanRender).
//
// A real app reported it: on a phone the select keeps focus after the native
// picker closes. The user picks a filter, the pick changes a SIBLING's tag, so
// the diff patches the common parent. The open-select defence skipped that
// patch because the parent contains the focused select, and renderCurrent then
// recorded the new tree as applied (spaPrev = &vn). The next diff ran against a
// tree the DOM never got, found no change, and the region stayed wrong until a
// reload.
//
// These tests drive spaPlanRender (the planner renderCurrent runs) against the
// simulated browser of vdom_converge_test.go, with the Sky.Spa applier.

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// spaSim is one Sky.Spa client: the DOM it holds and the tree it last
// recorded as applied (spaPrev).
type spaSim struct {
	t    *testing.T
	doc  *dnode
	prev VNode
}

func newSpaSim(t *testing.T, first VNode) *spaSim {
	t.Helper()
	v := prep(first)
	return &spaSim{t: t, doc: docFor(v, true), prev: v}
}

// render runs one renderCurrent: plan, apply, record. It fails the test when
// the DOM no longer matches the recorded tree (the drift this file is about)
// or when a patch missed its target.
func (s *spaSim) render(next VNode, sel spaSelectState) spaRenderPlan {
	s.t.Helper()
	n := prep(next)
	prev := s.prev
	plan := spaPlanRender(&prev, &n, nil, sel)
	if missed := applyPatches(s.doc, plan.Apply, &n, true); missed != 0 {
		s.t.Fatalf("%d patch targets missing\n%s", missed, fmtPatches(plan.Apply))
	}
	s.prev = *plan.DOM
	if got, want := canon(s.doc.kids, true), canon(docFor(s.prev, true).kids, true); got != want {
		s.t.Fatalf("spaPrev drifted from the DOM:\nDOM    %s\nspaPrev %s", got, want)
	}
	return plan
}

// shows reports whether the DOM is the DOM of v.
func (s *spaSim) shows(v VNode) bool {
	return canon(s.doc.kids, true) == canon(docFor(prep(v), true).kids, true)
}

// filterView is the report's reproduction: a select and, as its SIBLING, a
// list that becomes a paragraph (a tag change) when the filter is "none".
func filterView(filter string, tick int) VNode {
	opt := func(v string) VNode { return vel("option", map[string]string{"value": v}, vtxt(v)) }
	sel := vel("select", map[string]string{"value": filter}, opt("all"), opt("none"))
	sel.Events["input"] = zzMsg{SkyName: "Pick"}
	var body VNode
	if filter == "none" {
		body = vel("p", nil, vtxt("Nothing matches."))
	} else {
		body = vel("ul", nil, vel("li", nil, vtxt("row 1")), vel("li", nil, vtxt("row 2")))
	}
	// A Sub tick that ALSO changes a sibling's tag, so a tick render patches
	// the common parent too.
	tickEl := vel("span", nil, vtxt(fmt.Sprintf("tick=%d", tick)))
	if tick%2 == 1 {
		tickEl = vel("b", nil, vtxt(fmt.Sprintf("tick=%d", tick)))
	}
	return vel("div", nil, sel, body, tickEl)
}

const filterSelID = "r.0#select"

// The report's first test: focus the select, pick (its input and change
// events fire, so the picker is closed), and the sibling must follow. Then
// pick back, and it must follow again.
func TestSpaFocusedSelectPickUpdatesSibling(t *testing.T) {
	s := newSpaSim(t, filterView("all", 0))
	picked := spaSelectState{ID: filterSelID, Settled: true}

	s.render(filterView("none", 0), picked)
	if !s.shows(filterView("none", 0)) {
		t.Fatalf("pick \"none\" on the focused select: the list did not become the paragraph")
	}
	s.render(filterView("all", 0), picked)
	if !s.shows(filterView("all", 0)) {
		t.Fatalf("pick \"all\" back: the paragraph did not become the list")
	}
	s.render(filterView("none", 0), picked)
	if !s.shows(filterView("none", 0)) {
		t.Fatalf("pick \"none\" again: the list did not become the paragraph")
	}
}

// The guarantee the defence exists for: while the select is focused and its
// picker may be open, a render caused by something else (a Sub tick) must not
// rewrite the select or its options. And the held-back patches are not lost:
// once the select is no longer open, the DOM catches up to the model.
func TestSpaOpenSelectHoldsPatchesAndCatchesUp(t *testing.T) {
	s := newSpaSim(t, filterView("all", 0))
	selNode := s.doc.find(filterSelID)
	selBefore := canon([]*dnode{selNode}, true)
	open := spaSelectState{ID: filterSelID}

	for tick := 1; tick <= 3; tick++ {
		s.render(filterView("all", tick), open)
		if got := s.doc.find(filterSelID); got != selNode {
			t.Fatalf("tick %d: the open select was replaced (a new DOM node)", tick)
		}
		if got := canon([]*dnode{selNode}, true); got != selBefore {
			t.Fatalf("tick %d: the open select or its options were rewritten:\nbefore %s\nafter  %s", tick, selBefore, got)
		}
	}
	// The select closes (blur): the next render, with no model change, must
	// bring the DOM to the current view.
	s.render(filterView("all", 3), spaSelectState{})
	if !s.shows(filterView("all", 3)) {
		t.Fatalf("after blur the held-back tick patches were lost:\nDOM  %s", canon(s.doc.kids, true))
	}

	// The same through a pick made while the picker is still reported open
	// (focus kept, no settle yet): it is held, and the blur catches it up.
	s.render(filterView("none", 3), open)
	s.render(filterView("none", 3), spaSelectState{})
	if !s.shows(filterView("none", 3)) {
		t.Fatalf("a pick held while the select was open was lost after blur")
	}
}

// A patch addressed INSIDE a held-back region (a renamed descendant, a text
// under the common parent) is held with it, and patches elsewhere still apply
// while the select is open.
func TestSpaOpenSelectAppliesUnrelatedRegions(t *testing.T) {
	view := func(filter string, tick int, other string) VNode {
		return vel("main", nil, filterView(filter, tick), vel("section", nil, vtxt(other)))
	}
	s := newSpaSim(t, view("all", 0, "x"))
	open := spaSelectState{ID: "r.0#div.0#select"}
	s.render(view("none", 1, "y"), open)
	if got := canon([]*dnode{s.doc.find("r.1#section")}, true); !strings.Contains(got, "y") {
		t.Fatalf("an unrelated region did not update while the select was open: %s", got)
	}
	s.render(view("none", 1, "y"), spaSelectState{})
	if !s.shows(view("none", 1, "y")) {
		t.Fatalf("the held region did not catch up after blur")
	}
}

// Random trees with a select somewhere, random transitions, a random open
// select: the DOM must always equal the recorded tree, an open select is never
// touched, and one render after the select closes the DOM is the view.
func TestSpaOpenSelectRandomTransitionsNeverDrift(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	checked := 0
	for iter := 0; iter < 4000; iter++ {
		root := VNode{Kind: "element", Tag: "div", Attrs: map[string]string{}, Events: map[string]any{}}
		for i := 0; i < 1+r.Intn(4); i++ {
			root.Children = append(root.Children, genTree(r, 1))
		}
		s := newSpaSim(t, root)
		var sels []string
		s.doc.walk(func(d *dnode) {
			if d.kind == "element" && d.tag == "select" {
				sels = append(sels, d.attrs["sky-id"])
			}
		})
		if len(sels) == 0 {
			continue
		}
		checked++
		selID := sels[r.Intn(len(sels))]
		selNode := s.doc.find(selID)
		before := canon([]*dnode{selNode}, true)
		cur := root
		for step := 0; step < 3; step++ {
			cur = mutate(r, cur, 0)
			if cur.Kind != "element" || cur.Tag == "select" {
				cur = root
			}
			cur.Tag = "div"
			s.render(cur, spaSelectState{ID: selID})
			if s.doc.find(selID) != selNode || canon([]*dnode{selNode}, true) != before {
				t.Fatalf("iter %d step %d: the open select %s was rewritten", iter, step, selID)
			}
		}
		s.render(cur, spaSelectState{})
		if !s.shows(cur) {
			t.Fatalf("iter %d: after the select closed the DOM is not the view\nDOM  %s\nWANT %s",
				iter, canon(s.doc.kids, true), canon(docFor(prep(cur), true).kids, true))
		}
	}
	if checked < 500 {
		t.Fatalf("only %d runs had a select; the generator changed", checked)
	}
}

// A patch target missing from the DOM: the client rebuilds from the nearest
// id that is still there, walking up the sky-id path.
func TestSpaResyncAnchorWalksUpThePath(t *testing.T) {
	have := map[string]bool{"r": true, "r.0#div": true, "r.0#div.#input:user.email": true}
	exists := func(id string) bool { return have[id] }
	for _, c := range []struct{ id, want string }{
		{"r.0#div.1#p", "r.0#div"},
		{"r.0#div.1#p.0#span", "r.0#div"},
		{"r.0#div", "r.0#div"},
		{"r.3#ul.0#li", "r"},
		// A key with a dot: the walk passes through ids that are not there.
		{"r.0#div.#input:user.email.0#x", "r.0#div.#input:user.email"},
		{"x.1", ""},
	} {
		if got := spaResyncAnchor(c.id, exists); got != c.want {
			t.Errorf("spaResyncAnchor(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
