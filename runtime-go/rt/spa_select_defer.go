package rt

import "strings"

// The open-<select> defence of the Sky.Spa client, as a pure planner.
//
// A native dropdown closes on ANY DOM mutation inside the open select or an
// ancestor that would re-mount it, and the DOM has no "is the picker open"
// API. So while a select may be open, the client holds back every patch that
// would touch it: a patch on the select, inside it, or on an ancestor of it.
//
// Holding a patch back must never LOSE it. The client diffs each render
// against the tree it recorded as applied (spaPrev). It used to record the
// whole new tree even when it had skipped patches, so the next diff ran
// against a tree the DOM never got, found no change in the skipped region,
// and the region stayed wrong until a reload (a real app hit this on a phone:
// a filter select that keeps focus after the picker closes, whose pick changed
// a sibling's tag, so the diff patched the common parent).
//
// Here the recorded tree is the tree the DOM really holds: the new tree,
// except that each held-back region keeps its OLD subtree. The next render
// that runs with the select closed diffs that tree against the view and
// re-sends exactly what was held back. Nothing is skipped for good.
//
// And a select is not defended against its own pick: once its input or
// change event fired, the picker is closed (both fire after the choice), so
// the render that follows applies in full (Settled).

// spaSelectState is what the Sky.Spa client knows about a focused <select>
// when it renders: ID is the sky-id of the focused select (or of its nearest
// ancestor with one, for a select a widget owns), "" when no select has focus.
// Settled is true when that select's own input or change event fired since it
// last opened: the native picker is closed then.
type spaSelectState struct {
	ID      string
	Settled bool
}

// guarded is the sky-id the defence protects this render, or "".
func (s spaSelectState) guarded() string {
	if s.Settled {
		return ""
	}
	return s.ID
}

// spaRenderPlan is what one Sky.Spa render does to the DOM.
type spaRenderPlan struct {
	Apply []Patch // applied now
	// Held counts the patches held back for an open select; Regions are the
	// sky-ids of the held regions (each keeps its old subtree in DOM).
	Held    int
	Regions []string
	DOM     *VNode // the tree the DOM holds once Apply ran: the next spaPrev
}

// spaPlanRender diffs prev into next and decides which patches go to the DOM
// now. prev must be the tree the DOM holds (the previous plan's DOM).
func spaPlanRender(prev, next *VNode, clientState map[string]string, sel spaSelectState) spaRenderPlan {
	patches := diffTrees(prev, next, clientState)
	guard := sel.guarded()
	if guard == "" || len(patches) == 0 {
		return spaRenderPlan{Apply: patches, DOM: next}
	}
	// touches: the patch targets the select, an ancestor of it, or a node
	// inside it. Sky-ids are paths, so this is a prefix test on ids; a key
	// containing a dot can make it answer yes for a node that is neither,
	// which only holds back more than needed (the region is still kept whole).
	touches := func(id string) bool {
		return id == guard || hasPrefixDot(guard, id) || hasPrefixDot(id, guard)
	}
	var (
		apply   []Patch
		regions []string
		held    int
		inside  = map[string]bool{} // every id under a held region, old and new
	)
	addRegion := func(id string) bool {
		o, n := spaFindVNode(prev, id), spaFindVNode(next, id)
		if o == nil || n == nil {
			return false
		}
		regions = append(regions, id)
		collectSkyIDs(o, inside)
		collectSkyIDs(n, inside)
		return true
	}
	for _, p := range patches {
		switch {
		case inside[p.ID]:
			// Under a held region: a later patch of the same region,
			// possibly addressed by a renamed id. Held with it.
			held++
		case touches(p.ID):
			if !addRegion(p.ID) {
				return spaHoldAll(prev, patches)
			}
			held++
		default:
			apply = append(apply, p)
		}
	}
	if held == 0 {
		return spaRenderPlan{Apply: patches, DOM: next}
	}
	// A region must sit at the same id in both trees, under ancestors no
	// applied patch changes; the diff emits a parent's patches before its
	// children's, so that holds. Check it rather than trust it: an applied
	// patch on an ancestor of a region would make the recorded tree wrong, so
	// then nothing is applied this render.
	for _, p := range apply {
		for _, r := range regions {
			if hasPrefixDot(r, p.ID) || p.ID == r {
				return spaHoldAll(prev, patches)
			}
		}
	}
	dom := spaKeepRegions(prev, next, regions)
	return spaRenderPlan{Apply: apply, Held: held, Regions: regions, DOM: &dom}
}

// spaHoldAll holds back the whole render: the DOM keeps prev, so prev stays
// the recorded tree.
func spaHoldAll(prev *VNode, patches []Patch) spaRenderPlan {
	return spaRenderPlan{Held: len(patches), Regions: []string{prev.SkyID}, DOM: prev}
}

// spaKeepRegions returns next with the subtree at each region id replaced by
// prev's subtree at that id. Only the path down to a region is copied; every
// other subtree is shared with next (trees are not mutated once built).
func spaKeepRegions(prev, next *VNode, regions []string) VNode {
	keep := make(map[string]bool, len(regions))
	for _, r := range regions {
		keep[r] = true
	}
	var walk func(n *VNode) VNode
	walk = func(n *VNode) VNode {
		if n.SkyID != "" && keep[n.SkyID] {
			if o := spaFindVNode(prev, n.SkyID); o != nil {
				return *o
			}
		}
		below := false
		for _, r := range regions {
			if n.SkyID != "" && hasPrefixDot(r, n.SkyID) {
				below = true
				break
			}
		}
		if !below {
			return *n
		}
		out := *n
		out.Children = make([]VNode, len(n.Children))
		for i := range n.Children {
			out.Children[i] = walk(&n.Children[i])
		}
		return out
	}
	return walk(next)
}

func collectSkyIDs(n *VNode, into map[string]bool) {
	if n.SkyID != "" {
		into[n.SkyID] = true
	}
	for i := range n.Children {
		collectSkyIDs(&n.Children[i], into)
	}
}

// hasPrefixDot reports whether s lies under the sky-id prefix: s starts with
// prefix + ".".
func hasPrefixDot(s, prefix string) bool {
	return len(s) > len(prefix)+1 && s[:len(prefix)] == prefix && s[len(prefix)] == '.'
}

// spaResyncAnchor returns the nearest sky-id at or above id for which exists
// is true, or "" when there is none. A patch whose target is missing from the
// DOM means the DOM drifted from the recorded tree; the client rebuilds that
// anchor's children from the recorded tree so the two agree again.
func spaResyncAnchor(id string, exists func(string) bool) string {
	for cur := id; cur != ""; {
		if exists(cur) {
			return cur
		}
		i := strings.LastIndexByte(cur, '.')
		if i < 0 {
			return ""
		}
		cur = cur[:i]
	}
	return ""
}

// spaFindVNode returns the node with the given sky-id under root, or nil.
func spaFindVNode(root *VNode, id string) *VNode {
	if root == nil {
		return nil
	}
	if root.SkyID == id {
		return root
	}
	for i := range root.Children {
		if r := spaFindVNode(&root.Children[i], id); r != nil {
			return r
		}
	}
	return nil
}
