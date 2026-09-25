package rt

// Sky.Spa SSR hydration — the portable walk over the server-painted DOM.
//
// The wasm client (dom_render_wasm.go) hands the server DOM to these functions
// through the small spaDOM interface, so the same code runs in the browser
// (a js.Value adapter) and in a host Go test (an HTML5 parse of the SSR bytes,
// spa_hydrate_test.go). The host test is therefore a real SSR ↔ hydrate
// pairing check: the server's renderer writes the HTML, a spec-conformant
// parser builds the DOM a browser would build, and the client's own decision
// code runs against it.

import "strings"

// spaDOM is the part of a DOM node the hydration walk reads or changes. A nil
// interface value means "no node" (the end of a child list).
type spaDOM interface {
	// NodeType is the DOM nodeType: 1 for an element, 3 for a text node.
	NodeType() int
	// Tag is the lower-case tag name of an element.
	Tag() string
	// Attr reads one attribute of an element.
	Attr(k string) (string, bool)
	FirstChild() spaDOM
	NextSibling() spaDOM
	// Data is the character data of a text node.
	Data() string
	// SplitText splits a text node at a UTF-16 offset (DOM Text.splitText):
	// the receiver keeps the first part and the returned node, inserted as
	// its next sibling, holds the rest.
	SplitText(offsetUTF16 int) spaDOM
}

// spaCanHydrate is the whole hydrate-or-rebuild decision for an SSR first
// paint: `dom` is the server-painted root element (the first element child of
// the mount) and `root` the client's freshly-computed first tree. It returns
// false with a reason when the client must rebuild instead. A true result
// means spaHydrateTextRuns can make the server DOM match the client tree node
// for node.
func spaCanHydrate(dom spaDOM, root *VNode) (bool, string) {
	return spaHydrationParity(dom, root)
}

// spaSlot is one DOM child the server's HTML parses to, in order: either the
// element child `child` of the VNode, or a text RUN. The server writes
// consecutive text children back to back (renderVNodeInto), so the HTML
// parser makes the whole run ONE text node; `texts` holds, per non-empty text
// child of the run, the text the parser produces for it. The client tree keeps
// one text node per text child, so hydration splits the run at those
// boundaries (spaHydrateTextRuns).
type spaSlot struct {
	child int
	texts []string
}

// spaSSRSlots lists the DOM children the server HTML of v's child list parses
// to. The caller has excluded a child list holding raw HTML (a raw string
// parses to any number of nodes).
func spaSSRSlots(v *VNode) []spaSlot {
	var slots []spaSlot
	// The parser drops ONE newline that starts the content of these elements
	// (HTML "in body": pre, listing, textarea).
	dropLF := v.Tag == "pre" || v.Tag == "listing" || v.Tag == "textarea"
	for i := range v.Children {
		c := &v.Children[i]
		if c.Kind != "text" {
			slots = append(slots, spaSlot{child: i})
			dropLF = false
			continue
		}
		t := spaParsedText(v.Tag, c.Text)
		if dropLF && t != "" {
			t = strings.TrimPrefix(t, "\n")
			dropLF = false
		}
		if t == "" {
			continue // no characters: no DOM node, and the run is not broken
		}
		if n := len(slots); n > 0 && slots[n-1].texts != nil {
			slots[n-1].texts = append(slots[n-1].texts, t)
		} else {
			slots = append(slots, spaSlot{texts: []string{t}})
		}
	}
	return slots
}

// spaParsedText is the character data the HTML parser produces for a text
// child the server wrote under a `parent` element: input preprocessing turns
// CR LF and a lone CR into LF, and a NUL is dropped in normal content and
// becomes U+FFFD in the raw-text and RCDATA elements.
func spaParsedText(parent, s string) string {
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	if strings.IndexByte(s, 0) >= 0 {
		switch parent {
		case "style", "script", "textarea", "title", "xmp", "iframe", "noembed", "noframes":
			s = strings.ReplaceAll(s, "\x00", "�")
		default:
			s = strings.ReplaceAll(s, "\x00", "")
		}
	}
	return s
}

// spaValuedTextareaLeaf: a <textarea> whose value is the `value` attribute
// (Std.Ui Input.multiline). The server writes the value as the element's text
// (a textarea has no value attribute in HTML); the client models it as the
// .value property, which hydrateVNode sets. So the text child the server DOM
// holds is the element's default value, not a node of the client tree.
func spaValuedTextareaLeaf(v *VNode) bool {
	return v.Tag == "textarea" && len(v.Children) == 0
}

// spaHydrationParity checks that the server-painted DOM SHOWS what the
// client's first tree says — tags, sky-ids, the tree's attributes and every
// text node — before hydration adopts it (SPA-5). Hydration only binds
// listeners and splits text runs; it writes no text. So when the server and
// the client computed a different first view (a route param the server
// decoded and the client did not, a model field only one side had), the page
// kept showing the server's text while the client's model and every later diff
// assumed its own. A mismatch rebuilds from the client tree instead, which is
// always correct.
//
// Extra DOM attributes are allowed (the server stamps sky-<event> and
// data-sky-hid, which the client does not model). A child list holding raw
// HTML is not compared node by node (a raw string parses to any number of
// nodes); the client builds such a list from the same serialisation
// (spaSetChildren), so the server's nodes are already the client's.
func spaHydrationParity(node spaDOM, v *VNode) (bool, string) {
	if node == nil || node.NodeType() != 1 {
		return false, "server DOM has no element for " + v.SkyID
	}
	if node.Tag() != v.Tag {
		return false, "tag differs at " + v.SkyID + ": server <" + node.Tag() + ">, client <" + v.Tag + ">"
	}
	if v.SkyID != "" {
		if s, ok := node.Attr("sky-id"); !ok || s != v.SkyID {
			return false, "sky-id differs at " + v.SkyID
		}
	}
	for k, want := range v.Attrs {
		if k == "value" && (v.Tag == "select" || v.Tag == "textarea") {
			continue
		}
		if got, ok := node.Attr(k); !ok || got != want {
			return false, "attribute " + k + " differs at " + v.SkyID
		}
	}
	if spaChildrenContainRaw(v.Children) || spaValuedTextareaLeaf(v) {
		return true, ""
	}
	dom := node.FirstChild()
	if v.Tag == "form" {
		dom = spaSkipServerCsrfInput(dom)
	}
	for _, s := range spaSSRSlots(v) {
		if dom == nil {
			return false, "server DOM has fewer children at " + v.SkyID
		}
		if s.texts == nil {
			if ok, why := spaHydrationParity(dom, &v.Children[s.child]); !ok {
				return false, why
			}
		} else {
			if dom.NodeType() != 3 {
				return false, "text expected at " + v.SkyID
			}
			if dom.Data() != strings.Join(s.texts, "") {
				return false, "text differs at " + v.SkyID
			}
		}
		dom = dom.NextSibling()
	}
	if dom != nil {
		return false, "server DOM has more children at " + v.SkyID
	}
	return true, ""
}

// spaHydrateTextRuns splits every server text node that holds a run of
// several client text children into one text node per child (DOM
// Text.splitText), so the hydrated DOM has the node structure spaMount would
// build. The first part stays the ORIGINAL server node. It runs only after
// spaCanHydrate accepted the same trees, so every slot lines up.
func spaHydrateTextRuns(node spaDOM, v *VNode) {
	if node == nil || spaChildrenContainRaw(v.Children) || spaValuedTextareaLeaf(v) {
		return
	}
	dom := node.FirstChild()
	if v.Tag == "form" {
		dom = spaSkipServerCsrfInput(dom)
	}
	for _, s := range spaSSRSlots(v) {
		if dom == nil {
			return
		}
		if s.texts == nil {
			spaHydrateTextRuns(dom, &v.Children[s.child])
		} else {
			for _, part := range s.texts[:len(s.texts)-1] {
				dom = dom.SplitText(spaUTF16Len(part))
			}
		}
		dom = dom.NextSibling()
	}
}

// spaUTF16Len is the length of s in UTF-16 code units, the unit of DOM text
// offsets.
func spaUTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// spaChildrenContainRaw reports whether any direct child is a raw-HTML node.
func spaChildrenContainRaw(children []VNode) bool {
	for i := range children {
		if children[i].Kind == "raw" {
			return true
		}
	}
	return false
}

// spaServerCsrfFieldName is the form field the server injects into every
// `<form method="post">` of an HTML response (injectCsrfIntoForms, rt.go) and
// the CSRF middleware reads back for a native form POST (csrf_middleware.go).
const spaServerCsrfFieldName = "__sky_csrf"

// spaIsServerCsrfInput reports whether n is the CSRF token input the server
// injected into a form: exactly `<input type="hidden" name="__sky_csrf">`
// with no sky-id (the renderer stamps a sky-id on every element it writes, so
// an input from the client tree always has one).
//
// The input is not part of the rendered tree, but it is part of the served
// page on purpose: before the wasm client runs (a slow load, JS disabled, the
// wasm failed) a submit is a native POST, and the token is what lets it pass
// the CSRF check instead of a 403. So the hydration walk steps over it, and
// the client keeps it in the form when it patches the form
// (spaDetachCsrfToken, dom_render_wasm.go).
func spaIsServerCsrfInput(n spaDOM) bool {
	if n == nil || n.NodeType() != 1 || n.Tag() != "input" {
		return false
	}
	if t, _ := n.Attr("type"); t != "hidden" {
		return false
	}
	if nm, _ := n.Attr("name"); nm != spaServerCsrfFieldName {
		return false
	}
	_, hasID := n.Attr("sky-id")
	return !hasID
}

// spaSkipServerCsrfInput steps over the server's CSRF token input when it is
// `first`, the first child of a form (where injectCsrfIntoForms puts it). Any
// other position, or any other input, is not skipped: parity still refuses a
// server DOM that holds a node the client tree does not.
func spaSkipServerCsrfInput(first spaDOM) spaDOM {
	if spaIsServerCsrfInput(first) {
		return first.NextSibling()
	}
	return first
}
