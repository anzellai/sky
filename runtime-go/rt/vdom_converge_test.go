package rt

// vdom_converge_test.go — the shared diff (diffTrees) is correct only if
// applying its patches to the DOM the browser holds for the OLD tree yields the
// DOM the browser would hold for the NEW tree, on every applier.
//
// This file is a small simulated browser:
//
//   - parseHTML turns the bytes renderVNode emits into a node tree, the way the
//     HTML parser does (duplicate attributes: first wins; <style>/<script>
//     bodies are raw text; void tags have no children).
//   - applyLive applies patches with the Sky.Live / webview semantics: every
//     HTML payload (html / kids[].html / replace) is PARSED, exactly as
//     __skyApplyPatches does, so a payload that is missing a `selected` or a
//     sky-id is caught.
//   - applySpa applies patches with the Sky.Spa semantics: new nodes are built
//     from the NEW VNode tree (buildDOM), addressed by sky-id.
//
// TestVDOMRandomTransitionsConverge drives 20,000 random (old, new) pairs
// through both appliers and requires byte-equal DOMs and zero missed patch
// targets. The generator covers keyed + named children, sibling inserts and
// removals, raw nodes, <select value> with changing options, style-injection
// markers and a root tag change — the classes the 2026-09 audit found diverging
// (keyed id drift, stale injected <style>, lost select value, root tag change).

import (
	"fmt"
	"html"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// ---- simulated DOM -------------------------------------------------------

type dnode struct {
	kind   string // element | text
	tag    string
	text   string
	attrs  map[string]string
	kids   []*dnode
	parent *dnode
	// serial is the node's identity. A node the applier KEEPS keeps its serial;
	// a node it rebuilds gets a fresh one. Tests use it to prove a focused input
	// survived a patch as the SAME node (focus, caret and IME live on the node).
	serial int
}

var dnodeSerial int

func newDnode(kind, tag string) *dnode {
	dnodeSerial++
	return &dnode{kind: kind, tag: tag, attrs: map[string]string{}, serial: dnodeSerial}
}

func (d *dnode) setKids(kids []*dnode) {
	d.kids = kids
	for _, k := range kids {
		k.parent = d
	}
}

// parseHTML parses the subset of HTML renderVNodeInto emits (plus the simple
// raw fragments the generator uses) into nodes.
func parseHTML(s string) []*dnode {
	p := &miniParser{s: s}
	root := newDnode("element", "#fragment")
	p.parseInto(root, "")
	return root.kids
}

type miniParser struct {
	s string
	i int
}

func (p *miniParser) parseInto(parent *dnode, closeTag string) {
	for p.i < len(p.s) {
		if strings.HasPrefix(p.s[p.i:], "<!DOCTYPE") {
			end := strings.IndexByte(p.s[p.i:], '>')
			p.i += end + 1
			continue
		}
		if strings.HasPrefix(p.s[p.i:], "</") {
			end := strings.IndexByte(p.s[p.i:], '>')
			p.i += end + 1
			return
		}
		if p.s[p.i] == '<' {
			parent.kids = append(parent.kids, p.parseElement())
			parent.kids[len(parent.kids)-1].parent = parent
			continue
		}
		end := strings.IndexByte(p.s[p.i:], '<')
		if end < 0 {
			end = len(p.s) - p.i
		}
		t := newDnode("text", "")
		t.text = html.UnescapeString(p.s[p.i : p.i+end])
		t.parent = parent
		parent.kids = append(parent.kids, t)
		p.i += end
	}
}

func (p *miniParser) parseElement() *dnode {
	p.i++ // '<'
	start := p.i
	for p.i < len(p.s) && p.s[p.i] != ' ' && p.s[p.i] != '>' && p.s[p.i] != '/' {
		p.i++
	}
	el := newDnode("element", p.s[start:p.i])
	selfClosed := false
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ' ' {
			p.i++
			continue
		}
		if c == '/' {
			selfClosed = true
			p.i++
			continue
		}
		if c == '>' {
			p.i++
			break
		}
		ks := p.i
		for p.i < len(p.s) && p.s[p.i] != '=' && p.s[p.i] != ' ' && p.s[p.i] != '>' {
			p.i++
		}
		name := p.s[ks:p.i]
		val := ""
		if p.i < len(p.s) && p.s[p.i] == '=' {
			p.i += 2 // ="
			ve := strings.IndexByte(p.s[p.i:], '"')
			val = html.UnescapeString(p.s[p.i : p.i+ve])
			p.i += ve + 1
		}
		if _, dup := el.attrs[name]; !dup {
			el.attrs[name] = val
		}
	}
	if selfClosed || isVoidTag(el.tag) {
		return el
	}
	if el.tag == "style" || el.tag == "script" {
		end := strings.Index(p.s[p.i:], "</"+el.tag+">")
		if end > 0 {
			t := newDnode("text", "")
			t.text = p.s[p.i : p.i+end]
			el.setKids([]*dnode{t})
		}
		p.i += end + len("</"+el.tag+">")
		return el
	}
	p.parseInto(el, el.tag)
	return el
}

// spaBuild mirrors dom_render_wasm.go buildDOM / spaSetChildren: an element is
// built from its VNode (its Attrs + sky-id; events are listeners, not
// attributes); a child list holding a raw node is innerHTML'd from the SSR
// renderer instead.
func spaBuild(v VNode) []*dnode {
	switch v.Kind {
	case "text":
		t := newDnode("text", "")
		t.text = v.Text
		return []*dnode{t}
	case "raw":
		return parseHTML(v.Text)
	}
	el := newDnode("element", v.Tag)
	for k, val := range v.Attrs {
		el.attrs[k] = val
	}
	if v.SkyID != "" {
		el.attrs["sky-id"] = v.SkyID
	}
	el.setKids(spaBuildChildren(&v))
	return el.asList()
}

func (d *dnode) asList() []*dnode { return []*dnode{d} }

func spaBuildChildren(v *VNode) []*dnode {
	for _, c := range v.Children {
		if c.Kind == "raw" {
			return parseHTML(renderChildrenHTMLOf(v))
		}
	}
	var out []*dnode
	for _, c := range v.Children {
		out = append(out, spaBuild(c)...)
	}
	return out
}

// canon serialises a DOM for comparison: sorted attributes, adjacent text
// merged, empty text dropped (the browser shows no difference). Event wiring
// attributes are dropped for the Spa (it binds listeners, the attributes are
// inert) and data-sky-hid is dropped for both (multi-event hid is a separate
// finding owned elsewhere; this harness is about structure and identity).
func canon(nodes []*dnode, spa bool) string {
	var sb strings.Builder
	canonInto(&sb, nodes, spa)
	return sb.String()
}

func canonInto(sb *strings.Builder, nodes []*dnode, spa bool) {
	pending := ""
	flush := func() {
		if pending != "" {
			sb.WriteString("'" + pending + "'")
			pending = ""
		}
	}
	for _, d := range nodes {
		if d.kind == "text" {
			pending += d.text
			continue
		}
		flush()
		keys := make([]string, 0, len(d.attrs))
		for k := range d.attrs {
			if k == "data-sky-hid" || k == "method" {
				continue
			}
			if spa && (strings.HasPrefix(k, "sky-") && k != "sky-id" && k != "sky-key" || strings.HasPrefix(k, "data-sky-ev-")) {
				continue
			}
			// A <select>/<textarea> value is a PROPERTY (the SSR renderer
			// drops the attribute; buildDOM sets both). The selection is
			// compared through the options' `selected`.
			if k == "value" && (d.tag == "select" || d.tag == "textarea") {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("<" + d.tag)
		for _, k := range keys {
			sb.WriteString(" " + k + "=" + d.attrs[k])
		}
		sb.WriteString(">")
		canonInto(sb, d.kids, spa)
		sb.WriteString("</" + d.tag + ">")
	}
	flush()
}

func (d *dnode) find(id string) *dnode {
	if d.kind == "element" && d.attrs["sky-id"] == id {
		return d
	}
	for _, c := range d.kids {
		if r := c.find(id); r != nil {
			return r
		}
	}
	return nil
}

func (d *dnode) walk(f func(*dnode)) {
	f(d)
	for _, c := range d.kids {
		c.walk(f)
	}
}

// renameSubtreeDOM is the applier-side half of a kept child whose sky-id
// changed: every sky-id (and data-sky-hid) under the old prefix moves to the
// new one.
func renameSubtreeDOM(d *dnode, from, to string) {
	d.walk(func(n *dnode) {
		if n.kind != "element" {
			return
		}
		if s, ok := n.attrs["sky-id"]; ok && (s == from || strings.HasPrefix(s, from+".")) {
			n.attrs["sky-id"] = to + s[len(from):]
		}
		if h, ok := n.attrs["data-sky-hid"]; ok && strings.HasPrefix(h, from+".") {
			n.attrs["data-sky-hid"] = to + h[len(from):]
		}
	})
}

// applyPatches applies patches to the document `doc` (a #document node whose
// kids are the mount's children). spa selects the Sky.Spa semantics.
func applyPatches(doc *dnode, patches []Patch, newT *VNode, spa bool) (missed int) {
	for _, p := range patches {
		el := doc.find(p.ID)
		if el == nil {
			missed++
			continue
		}
		if p.Replace != nil {
			var repl []*dnode
			if spa {
				if nv := zzFind(newT, p.ID); nv != nil {
					repl = spaBuild(*nv)
				}
			} else {
				repl = parseHTML(*p.Replace)
			}
			par := el.parent
			var out []*dnode
			for _, k := range par.kids {
				if k == el {
					out = append(out, repl...)
				} else {
					out = append(out, k)
				}
			}
			par.setKids(out)
			continue
		}
		if p.Text != nil {
			t := newDnode("text", "")
			t.text = *p.Text
			el.setKids([]*dnode{t})
		}
		if p.HTML != nil {
			if spa {
				if nv := zzFind(newT, p.ID); nv != nil {
					el.setKids(spaBuildChildren(nv))
				}
			} else {
				el.setKids(parseHTML(*p.HTML))
			}
		}
		if p.Kids != nil {
			byID := map[string]*dnode{}
			for _, k := range el.kids {
				if k.kind == "element" {
					if s, ok := k.attrs["sky-id"]; ok {
						byID[s] = k
					}
				}
			}
			var nv *VNode
			if spa {
				nv = zzFind(newT, p.ID)
			}
			var out []*dnode
			type ren struct {
				n        *dnode
				from, to string
			}
			var renames []ren
			for i, kop := range p.Kids {
				if kop.Keep != "" {
					n := byID[kop.Keep]
					if n == nil {
						missed++
						continue
					}
					out = append(out, n)
					if kop.ID != "" && kop.ID != kop.Keep {
						renames = append(renames, ren{n, kop.Keep, kop.ID})
					}
					continue
				}
				if spa && nv != nil && i < len(nv.Children) {
					out = append(out, spaBuild(nv.Children[i])...)
				} else if kop.HTML != nil {
					out = append(out, parseHTML(*kop.HTML)...)
				}
			}
			el.setKids(out)
			for _, r := range renames {
				renameSubtreeDOM(r.n, r.from, r.to)
			}
		}
		for k, v := range p.Attrs {
			if v == "" {
				delete(el.attrs, k)
			} else {
				el.attrs[k] = v
			}
		}
		if p.Remove {
			par := el.parent
			var out []*dnode
			for _, k := range par.kids {
				if k != el {
					out = append(out, k)
				}
			}
			par.setKids(out)
		}
	}
	return missed
}

// docFor builds the browser document for a tree under a given applier.
func docFor(v VNode, spa bool) *dnode {
	doc := newDnode("element", "#document")
	if spa {
		doc.setKids(spaBuild(v))
	} else {
		doc.setKids(parseHTML(renderVNode(v, nil)))
	}
	return doc
}

// ---- generator -----------------------------------------------------------

type zzMsg struct{ SkyName string }

var genTags = []string{"div", "span", "button", "input", "select", "p"}

func genTree(r *rand.Rand, depth int) VNode {
	x := r.Intn(10)
	if depth > 3 || x < 2 {
		return VNode{Kind: "text", Text: []string{"a", "b", ""}[r.Intn(3)]}
	}
	if x == 2 {
		return VNode{Kind: "raw", Text: []string{"<i>r1</i>", "<b>r2</b>"}[r.Intn(2)]}
	}
	n := VNode{Kind: "element", Tag: genTags[r.Intn(len(genTags))], Attrs: map[string]string{}, Events: map[string]any{}}
	if r.Intn(3) == 0 {
		n.Attrs["class"] = []string{"c1", "c2"}[r.Intn(2)]
	}
	if r.Intn(4) == 0 {
		n.Attrs["sky-key"] = []string{"k1", "k2", "k3"}[r.Intn(3)]
	}
	if (n.Tag == "input" || n.Tag == "select") && r.Intn(3) == 0 {
		n.Attrs["name"] = []string{"email", "pw"}[r.Intn(2)]
	}
	if r.Intn(4) == 0 {
		n.Attrs["data-sky-pc-rules"] = "h|color: " + []string{"red", "blue"}[r.Intn(2)]
	}
	if r.Intn(3) == 0 {
		n.Events["click"] = zzMsg{SkyName: []string{"A", "B"}[r.Intn(2)]}
	}
	if r.Intn(4) == 0 {
		n.Events["mouseover"] = zzMsg{SkyName: "H"}
	}
	switch n.Tag {
	case "input":
		if r.Intn(2) == 0 {
			n.Attrs["value"] = []string{"x", "y"}[r.Intn(2)]
		}
	case "select":
		n.Attrs["value"] = []string{"o1", "o2", "o3"}[r.Intn(3)]
		k := 1 + r.Intn(3)
		for i := 0; i < k; i++ {
			v := []string{"o1", "o2", "o3", "o4"}[r.Intn(4)]
			n.Children = append(n.Children, VNode{Kind: "element", Tag: "option", Attrs: map[string]string{"value": v},
				Children: []VNode{{Kind: "text", Text: v}}})
		}
	default:
		k := r.Intn(4)
		for i := 0; i < k; i++ {
			n.Children = append(n.Children, genTree(r, depth+1))
		}
	}
	return n
}

func cloneVNode(v VNode) VNode {
	out := v
	if v.Attrs != nil {
		out.Attrs = copyAttrs(v.Attrs)
	}
	if v.Events != nil {
		out.Events = map[string]any{}
		for k, e := range v.Events {
			out.Events[k] = e
		}
	}
	out.Children = nil
	for _, c := range v.Children {
		out.Children = append(out.Children, cloneVNode(c))
	}
	return out
}

func mutate(r *rand.Rand, v VNode, depth int) VNode {
	out := cloneVNode(v)
	switch r.Intn(10) {
	case 0:
		return genTree(r, depth)
	case 1:
		if out.Kind == "text" {
			out.Text += "x"
		}
	case 2:
		if out.Kind == "element" {
			out.Attrs["class"] = "c3"
		}
	case 3:
		if out.Kind == "element" && len(out.Children) > 0 {
			out.Children = out.Children[1:]
		}
	case 4:
		if out.Kind == "element" && out.Tag != "input" && out.Tag != "select" {
			out.Children = append([]VNode{genTree(r, depth+1)}, out.Children...)
		}
	case 5:
		if out.Kind == "element" {
			switch r.Intn(3) {
			case 0:
				delete(out.Events, "click")
			case 1:
				out.Events["mouseover"] = zzMsg{SkyName: "H"}
			case 2:
				if _, ok := out.Events["mouseover"]; ok {
					delete(out.Events, "mouseover")
				} else {
					out.Events["blur"] = zzMsg{SkyName: "Z"}
				}
			}
		}
	case 6:
		if out.Kind == "element" {
			if _, ok := out.Attrs["data-sky-pc-rules"]; ok {
				out.Attrs["data-sky-pc-rules"] = "h|color: green"
			} else {
				out.Attrs["data-sky-pc-rules"] = "h|color: teal"
			}
		}
	case 7:
		if out.Kind == "element" {
			if _, ok := out.Attrs["sky-key"]; ok {
				out.Attrs["sky-key"] = []string{"k1", "k2", "k3", "k4"}[r.Intn(4)]
			}
		}
	case 8:
		if out.Kind == "element" && len(out.Children) > 1 {
			i, j := r.Intn(len(out.Children)), r.Intn(len(out.Children))
			out.Children[i], out.Children[j] = out.Children[j], out.Children[i]
		}
	case 9:
		if out.Kind == "element" && out.Tag == "select" {
			out.Children = append([]VNode{{Kind: "element", Tag: "option", Attrs: map[string]string{"value": "o9"},
				Children: []VNode{{Kind: "text", Text: "o9"}}}}, out.Children...)
		}
	}
	for i := range out.Children {
		if out.Tag == "select" {
			continue
		}
		if r.Intn(2) == 0 {
			out.Children[i] = mutate(r, out.Children[i], depth+1)
		}
	}
	return out
}

func prep(v VNode) VNode {
	v = cloneVNode(v)
	assignSkyIDs(&v, "r")
	applyStyleInjections(&v)
	return v
}

func fmtPatches(ps []Patch) string {
	var parts []string
	for _, p := range ps {
		s := p.ID
		if p.Text != nil {
			s += " text=" + *p.Text
		}
		if p.HTML != nil {
			s += " html=" + *p.HTML
		}
		if p.Replace != nil {
			s += " replace=" + *p.Replace
		}
		if p.Kids != nil {
			var ks []string
			for _, k := range p.Kids {
				if k.Keep != "" {
					ks = append(ks, "keep:"+k.Keep+"->"+k.ID)
				} else if k.HTML != nil {
					ks = append(ks, "html:"+*k.HTML)
				}
			}
			s += " kids=[" + strings.Join(ks, " | ") + "]"
		}
		if p.Attrs != nil {
			s += fmt.Sprintf(" attrs=%v", p.Attrs)
		}
		parts = append(parts, "{"+s+"}")
	}
	return strings.Join(parts, "\n  ")
}

func zzFind(root *VNode, id string) *VNode {
	if root == nil {
		return nil
	}
	if root.SkyID == id {
		return root
	}
	for i := range root.Children {
		if r := zzFind(&root.Children[i], id); r != nil {
			return r
		}
	}
	return nil
}

func TestVDOMRandomTransitionsConverge(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	fails := map[string]int{}
	examples := map[string]string{}
	for iter := 0; iter < 20000; iter++ {
		root := VNode{Kind: "element", Tag: "div", Attrs: map[string]string{}, Events: map[string]any{}}
		for i := 0; i < 1+r.Intn(4); i++ {
			root.Children = append(root.Children, genTree(r, 1))
		}
		nroot := mutate(r, root, 0)
		if nroot.Kind != "element" {
			continue
		}
		// K3: the root keeps its tag except in a slice of the runs.
		if r.Intn(20) != 0 {
			nroot.Tag = "div"
		} else {
			nroot.Tag = []string{"main", "section"}[r.Intn(2)]
		}
		oldT, newT := prep(root), prep(nroot)
		patches := diffTrees(&oldT, &newT, nil)
		for _, spa := range []bool{false, true} {
			doc := docFor(oldT, spa)
			missed := applyPatches(doc, patches, &newT, spa)
			got := canon(doc.kids, spa)
			want := canon(docFor(newT, spa).kids, spa)
			if got == want && missed == 0 {
				continue
			}
			tgt := map[bool]string{false: "live", true: "spa"}[spa]
			fails[tgt]++
			if _, ok := examples[tgt]; !ok {
				examples[tgt] = fmt.Sprintf("OLD  %s\nNEW  %s\nPATCHES\n  %s\nGOT  %s\nWANT %s\nmissed=%d",
					canon(docFor(oldT, spa).kids, spa), want, fmtPatches(patches), got, want, missed)
			}
		}
	}
	for _, k := range []string{"live", "spa"} {
		if fails[k] > 0 {
			t.Errorf("%s applier: %d of 20000 random transitions did not converge; first:\n%s", k, fails[k], examples[k])
		}
	}
}
