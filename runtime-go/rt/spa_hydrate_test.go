package rt

// SSR ↔ hydrate pairing for Sky.Spa. The server renderer (renderVNode, the
// same code the SSR backend route serves) writes the HTML, the HTML5 parser in
// golang.org/x/net/html builds the DOM a browser builds from it, and the
// client's own decision code (spaCanHydrate, spa_hydrate.go) runs against that
// DOM through the spaDOM interface. A tree a normal Std.Ui page produces must
// hydrate: a refusal is a full rebuild that throws the server DOM away on
// every page load.
//
// Pinned defect (v0.25.17 register M): every page of a real web:app app
// logged "[sky.spa] SSR hydrate skipped, full rebuild: adjacent text". Two
// text children side by side (`text "Hello, "` next to `text name`) are
// written back to back by the server, and the parser turns the run into ONE
// text node, while the client tree holds two. The client refused the whole
// page instead of splitting the node.

import (
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// xDOM adapts an x/net/html node to spaDOM.
type xDOM struct{ n *html.Node }

func xdom(n *html.Node) spaDOM {
	if n == nil {
		return nil
	}
	return xDOM{n}
}

func (d xDOM) NodeType() int {
	switch d.n.Type {
	case html.ElementNode:
		return 1
	case html.TextNode:
		return 3
	case html.CommentNode:
		return 8
	}
	return 0
}
func (d xDOM) Tag() string { return d.n.Data }
func (d xDOM) Attr(k string) (string, bool) {
	for _, a := range d.n.Attr {
		if a.Namespace == "" && a.Key == k {
			return a.Val, true
		}
	}
	return "", false
}
func (d xDOM) FirstChild() spaDOM  { return xdom(d.n.FirstChild) }
func (d xDOM) NextSibling() spaDOM { return xdom(d.n.NextSibling) }
func (d xDOM) Data() string        { return d.n.Data }
func (d xDOM) SplitText(off int) spaDOM {
	u := utf16.Encode([]rune(d.n.Data))
	rest := &html.Node{Type: html.TextNode, Data: string(utf16.Decode(u[off:]))}
	d.n.Data = string(utf16.Decode(u[:off]))
	d.n.Parent.InsertBefore(rest, d.n.NextSibling)
	return xdom(rest)
}

// ssrParse renders `root` the way the SSR backend does and parses it the way a
// browser does (as the content of the #app <div>). It returns the parsed root
// element.
func ssrParse(t *testing.T, root *VNode) *html.Node {
	t.Helper()
	body := renderVNode(*root, map[string]any{})
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(body), ctx)
	if err != nil {
		t.Fatalf("parse SSR html: %v", err)
	}
	for _, n := range nodes {
		if n.Type == html.ElementNode {
			return n
		}
	}
	t.Fatalf("SSR html has no root element: %q", body)
	return nil
}

func withIDs(v VNode) *VNode {
	assignSkyIDs(&v, "r")
	return &v
}

func attrs(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// typicalPages are client trees of the shapes a normal Std.Ui / Std.Html page
// produces. Each must hydrate.
func typicalPages() map[string]VNode {
	return map[string]VNode{
		"adjacent text": el("p", nil, txt("Hello, "), txt("world"), txt("!")),
		"text next to an interpolated text": el("div", nil,
			el("span", nil, txt("name="), txt("Jörg")),
			el("span", nil, txt("count: "), txt("3"), txt(" items")),
		),
		"paragraph with mixed text and a link": el("p", nil,
			txt("Read "),
			el("a", attrs("href", "/docs"), txt("the docs")),
			txt(" or "), txt("ask"), txt("."),
		),
		"Ui.text rows": el("div", attrs("class", "c"),
			el("div", attrs("class", "r"), el("div", nil, txt("one"))),
			el("div", attrs("class", "r"), el("div", nil, txt("two"))),
			el("div", attrs("class", "r"), el("div", nil, txt("a "), txt("b"))),
		),
		"inputs": el("form", nil,
			el("label", attrs("for", "n"), txt("Name")),
			VNode{Kind: "element", Tag: "input", Attrs: attrs("id", "n", "type", "text", "value", "Ada")},
			VNode{Kind: "element", Tag: "input", Attrs: attrs("type", "checkbox", "checked", "checked")},
			el("button", attrs("type", "submit"), txt("Save")),
		),
		"a valued textarea (Input.multiline)": el("div", nil,
			VNode{Kind: "element", Tag: "textarea", Attrs: attrs("id", "notes", "value", "line one\nline two")},
		),
		"a select with a value": el("div", nil,
			el("select", attrs("value", "b"),
				el("option", attrs("value", "a"), txt("a")),
				el("option", attrs("value", "b"), txt("b")),
			),
		),
		"empty text between texts":              el("p", nil, txt("a"), txt(""), txt("b")),
		"non-ASCII and astral text runs":        el("p", nil, txt("😀 "), txt("Jörg"), txt(" ✓")),
		"text submitted from a textarea (CRLF)": el("p", nil, txt("first\r\nsecond"), txt("\rthird")),
		"a pre block starting with a newline":   el("pre", nil, txt("\nfn main() {}"), txt("\n")),
		"a style element": el("div", nil,
			el("style", nil, txt(".a{color:red}"), txt(".b{color:blue}")),
			el("main", nil, txt("content")),
		),
		"a NUL in text and in a style": el("div", nil, el("p", nil, txt("a\x00"), txt("b")), el("style", nil, txt("x\x00"), txt("y"))),
		"raw next to text":             el("p", nil, txt("before "), rawNode("<b>x</b>"), txt(" after")),
	}
}

func TestSpaHydrate_typicalPagesHydrate(t *testing.T) {
	for name, page := range typicalPages() {
		t.Run(name, func(t *testing.T) {
			root := withIDs(page)
			dom := ssrParse(t, root)
			if ok, reason := spaCanHydrate(xdom(dom), root); !ok {
				t.Fatalf("the SSR DOM of this page must hydrate, refused: %s\nhtml: %s",
					reason, renderVNode(*root, map[string]any{}))
			}
		})
	}
}

// A server DOM that shows something the client tree does not must still be
// refused (SPA-5): the fix makes the split exact, it does not relax parity.
func TestSpaHydrate_divergentServerDOMIsRefused(t *testing.T) {
	cases := map[string][2]VNode{
		"one text part differs": {
			el("p", nil, txt("Hello, "), txt("world")),
			el("p", nil, txt("Hello, "), txt("there")),
		},
		"last part differs": {
			el("p", nil, txt("ab"), txt("c")),
			el("p", nil, txt("ab"), txt("d")),
		},
		"a text part missing": {
			el("p", nil, txt("Hello, "), txt("world")),
			el("p", nil, txt("Hello, ")),
		},
		"tag differs": {
			el("div", nil, el("span", nil, txt("x"))),
			el("div", nil, el("b", nil, txt("x"))),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			server := withIDs(c[0])
			client := withIDs(c[1])
			dom := ssrParse(t, server)
			if ok, _ := spaCanHydrate(xdom(dom), client); ok {
				t.Fatalf("a server DOM that differs from the client tree must be refused")
			}
		})
	}
}

// clientShape reports where the DOM under `n` differs from the node structure
// spaMount builds for `v`: one DOM text node per text child that has
// characters, one element per element child. "" means the same.
func clientShape(n *html.Node, v *VNode) string {
	if spaChildrenContainRaw(v.Children) || spaValuedTextareaLeaf(v) {
		return ""
	}
	dom := n.FirstChild
	lead := v.Tag == "pre" || v.Tag == "listing" || v.Tag == "textarea"
	for i := range v.Children {
		c := &v.Children[i]
		if c.Kind == "text" {
			want := spaParsedText(v.Tag, c.Text)
			if lead && want != "" {
				want = strings.TrimPrefix(want, "\n")
				lead = false
			}
			if want == "" {
				continue
			}
			if dom == nil || dom.Type != html.TextNode || dom.Data != want {
				got := "<none>"
				if dom != nil {
					got = dom.Data
				}
				return "text child " + v.SkyID + ": want " + want + ", got " + got
			}
		} else {
			lead = false
			if dom == nil || dom.Type != html.ElementNode {
				return "element child missing at " + v.SkyID
			}
			if why := clientShape(dom, c); why != "" {
				return why
			}
		}
		dom = dom.NextSibling
	}
	if dom != nil {
		return "extra DOM child at " + v.SkyID
	}
	return ""
}

func TestSpaHydrate_textRunsSplitToTheClientStructure(t *testing.T) {
	for name, page := range typicalPages() {
		t.Run(name, func(t *testing.T) {
			root := withIDs(page)
			dom := ssrParse(t, root)
			if ok, reason := spaCanHydrate(xdom(dom), root); !ok {
				t.Fatalf("refused: %s", reason)
			}
			firstText := map[*html.Node]string{}
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						firstText[c] = c.Data
					}
					walk(c)
				}
			}
			walk(dom)
			spaHydrateTextRuns(xdom(dom), root)
			if why := clientShape(dom, root); why != "" {
				t.Fatalf("after hydration the DOM must have spaMount's structure: %s", why)
			}
			// The split keeps every server text node: each still sits in the
			// tree and starts with the text it had.
			for n, was := range firstText {
				if n.Parent == nil || !strings.HasPrefix(was, n.Data) {
					t.Fatalf("server text node %q was replaced, not split (now %q)", was, n.Data)
				}
			}
		})
	}
}
