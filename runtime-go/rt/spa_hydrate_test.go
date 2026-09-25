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
	"net/http"
	"net/http/httptest"
	"net/url"
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

// ssrParseHTML parses SSR bytes the way a browser does (as the content of the
// #app <div>) and returns the first root element.
func ssrParseHTML(t *testing.T, body string) *html.Node {
	t.Helper()
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

// Pinned defect (a real web:app app, v0.25.17): a returning visitor's sign-in
// page logged "[sky.spa] SSR hydrate skipped, full rebuild: tag differs at
// …#form.0#div: server <input>, client <div>". The SSR page is served as a
// Sky.Http.Server HTML response, and for a request that carries the CSRF
// cookie the server adds `<input type="hidden" name="__sky_csrf">` as the
// first child of every `<form method="post">` (injectCsrfIntoForms). That
// input is deliberate: it is what lets a native POST (before the wasm client
// runs, or with JS off) pass the CSRF check. The hydration walk must step over
// it, not refuse the page.
func signInPage() *VNode {
	signIn := el("div", nil,
		el("form", nil,
			el("div", nil,
				el("div", nil, txt("Email")),
				VNode{Kind: "element", Tag: "input", Attrs: attrs("name", "email", "type", "email", "value", "")},
			),
			el("div", nil,
				el("button", attrs("type", "submit"), txt("Sign in")),
			),
		),
	)
	signIn.Children[0].Events = map[string]any{"submit": "DoSignIn"}
	return withIDs(signIn)
}

func TestSpaHydrate_csrfCookieRequestStillHydratesForms(t *testing.T) {
	root := signInPage()
	body := renderVNode(*root, map[string]any{})
	if !strings.Contains(body, `method="post"`) {
		t.Fatalf("precondition: the renderer marks a submit form method=post: %s", body)
	}
	served := injectCsrfIntoForms(body, "tok123")
	if !strings.Contains(served, `<form sky-id="r.0#form"`) || !strings.Contains(served, `<input type="hidden" name="__sky_csrf" value="tok123">`) {
		t.Fatalf("the served SSR form must carry the CSRF token for a native POST: %s", served)
	}
	dom := ssrParseHTML(t, served)
	if ok, reason := spaCanHydrate(xdom(dom), root); !ok {
		t.Fatalf("the SSR page served to a request with a CSRF cookie must hydrate, refused: %s\nhtml: %s", reason, served)
	}
	// The text-run split walks the same slots and must not trip on it either.
	spaHydrateTextRuns(xdom(dom), root)
	form := dom.FirstChild
	if form == nil || form.FirstChild == nil || !spaIsServerCsrfInput(xdom(form.FirstChild)) {
		t.Fatalf("hydration must leave the token as the form's first child")
	}
}

// A text-run form (text children right after the token) hydrates too.
func TestSpaHydrate_csrfTokenBeforeATextRun(t *testing.T) {
	v := el("div", nil, el("form", nil, txt("Hello, "), txt("world"), el("button", nil, txt("Go"))))
	v.Children[0].Events = map[string]any{"submit": "Go"}
	root := withIDs(v)
	served := injectCsrfIntoForms(renderVNode(*root, map[string]any{}), "tok")
	dom := ssrParseHTML(t, served)
	if ok, reason := spaCanHydrate(xdom(dom), root); !ok {
		t.Fatalf("refused: %s\nhtml: %s", reason, served)
	}
	spaHydrateTextRuns(xdom(dom), root)
	if why := clientShapeSkippingToken(dom, root); why != "" {
		t.Fatalf("after hydration the DOM must have spaMount's structure (plus the token): %s", why)
	}
}

// clientShapeSkippingToken is clientShape with the form's token stepped over.
func clientShapeSkippingToken(n *html.Node, v *VNode) string {
	form := n.FirstChild
	if form != nil && form.FirstChild != nil && spaIsServerCsrfInput(xdom(form.FirstChild)) {
		tok := form.FirstChild
		form.RemoveChild(tok)
		defer form.InsertBefore(tok, form.FirstChild)
	}
	return clientShape(n, v)
}

// The skip is exactly the server's token as a form's first child. Anything
// broader would let parity accept a server DOM the client tree does not show.
func TestSpaHydrate_csrfSkipIsNarrow(t *testing.T) {
	root := signInPage()
	body := renderVNode(*root, map[string]any{})
	open := `<form sky-id="r.0#form" sky-submit="_" data-sky-hid="r.0#form.submit" method="post">`
	if !strings.Contains(body, open) {
		t.Fatalf("precondition: form opener %q not in %s", open, body)
	}
	cases := map[string]string{
		"another name":            `<input type="hidden" name="__other" value="t">`,
		"not hidden":              `<input type="text" name="__sky_csrf" value="t">`,
		"a div":                   `<div>t</div>`,
		"carries a sky-id":        `<input type="hidden" name="__sky_csrf" value="t" sky-id="x">`,
		"two tokens (second one)": `<input type="hidden" name="__sky_csrf" value="t"><input type="hidden" name="__sky_csrf" value="t">`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			served := strings.Replace(body, open, open+extra, 1)
			if ok, _ := spaCanHydrate(xdom(ssrParseHTML(t, served)), root); ok {
				t.Fatalf("a server form holding %s must be refused", extra)
			}
		})
	}
	// Outside a form the token is not skipped either.
	div := withIDs(el("div", nil, el("div", nil, txt("x"))))
	served := strings.Replace(renderVNode(*div, map[string]any{}), `<div sky-id="r.0#div">`,
		`<div sky-id="r.0#div"><input type="hidden" name="__sky_csrf" value="t">`, 1)
	if ok, _ := spaCanHydrate(xdom(ssrParseHTML(t, served)), div); ok {
		t.Fatalf("a token input outside a form must be refused")
	}
}

// With JS off (or before the wasm client runs) a submit of the SSR form is a
// native POST of its fields. The injected token must make that POST pass the
// CSRF middleware, not 403.
func TestSpaHydrate_nativePostOfSSRFormPassesCSRF(t *testing.T) {
	resetCsrf(t)
	const tok = "cookie-token-123"
	root := signInPage()
	served := injectCsrfIntoForms(renderVNode(*root, map[string]any{}), tok)
	dom := ssrParseHTML(t, served)
	// Collect the fields a browser submits: every named input of the form.
	fields := url.Values{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" {
			d := xDOM{n}
			if nm, ok := d.Attr("name"); ok {
				v, _ := d.Attr("value")
				fields.Add(nm, v)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(dom)
	fields.Set("email", "a@b.c")
	post := func(body url.Values) int {
		req := httptest.NewRequest(http.MethodPost, "/signin", strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: SkyCsrfCookieName, Value: tok})
		resp := httptest.NewRecorder()
		CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(resp, req)
		return resp.Code
	}
	if code := post(fields); code != 200 {
		t.Fatalf("a native POST of the served SSR form must pass CSRF, got %d (fields %v)", code, fields)
	}
	// Proof the token is what carries it: the same POST without it is 403.
	without := url.Values{}
	for k, v := range fields {
		if k != "__sky_csrf" {
			without[k] = v
		}
	}
	if code := post(without); code != http.StatusForbidden {
		t.Fatalf("precondition: a native POST without the token must be 403, got %d", code)
	}
}

// ── Parser-safe nesting (Std.Ui) ─────────────────────────────────────────
//
// Pinned defect (a real web:app app, v0.25.17): every page logged "[sky.spa]
// SSR hydrate skipped, full rebuild: server DOM has fewer children at
// …#p.1#a". A cookie notice put a link with an `el` label in a paragraph, and
// Std.Ui emitted `<p>…<a><div>privacy policy</div></a>.</p>`. The HTML parser
// closes the open <p> at the <div> start tag (through the <a>), so the served
// DOM was not the rendered tree.
//
// Std.Ui now renders every element under a parser-safety state
// (sky-stdlib/Std/Ui.sky `Nesting` / `parserSafeTag`). stdUiTag below is that
// rule; tests/Std/UiParserSafeNestingTest.sky proves Std.Ui's markup follows
// it (with parserRestructuresModel, the same model this test checks), and this
// matrix proves, against a spec-conformant HTML parser and the client's own
// hydrate decision, that (1) every nesting the rule emits hydrates, and (2)
// the model flags every nesting the parser really restructures, so the Sky
// check has no false negatives over the tags Std.Ui can emit.

func isHeadingTagT(t string) bool {
	switch t {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// stdUiFlowOnly mirrors Std.Ui `isFlowOnlyTag`.
func stdUiFlowOnly(t string) bool {
	switch t {
	case "div", "p", "section", "form", "main", "nav", "footer", "header", "aside":
		return true
	}
	return isHeadingTagT(t)
}

// stdUiTag mirrors Std.Ui `parserSafeTag` for a tag emitted under the
// (already emitted) ancestors `anc`, outermost first.
func stdUiTag(anc []string, tag string) string {
	last := ""
	if len(anc) > 0 {
		last = anc[len(anc)-1]
	}
	switch {
	case has(anc, "p") && stdUiFlowOnly(tag):
		return "span"
	case has(anc, "a") && tag == "a":
		return "span"
	case has(anc, "button") && tag == "button":
		return "span"
	case has(anc, "form") && tag == "form":
		return "div"
	case isHeadingTagT(last) && isHeadingTagT(tag):
		return "div"
	}
	return tag
}

// parserRestructuresModel mirrors `restructure` in
// tests/Std/UiParserSafeNestingTest.sky.
func parserRestructuresModel(anc []string, tag string) bool {
	closesP := map[string]bool{"address": true, "article": true, "aside": true, "blockquote": true, "center": true,
		"details": true, "dialog": true, "dir": true, "div": true, "dl": true, "dd": true, "dt": true, "fieldset": true,
		"figcaption": true, "figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
		"h5": true, "h6": true, "header": true, "hgroup": true, "hr": true, "li": true, "listing": true, "main": true,
		"menu": true, "nav": true, "ol": true, "p": true, "plaintext": true, "pre": true, "search": true, "section": true,
		"summary": true, "table": true, "ul": true, "xmp": true}
	last := ""
	if len(anc) > 0 {
		last = anc[len(anc)-1]
	}
	return (closesP[tag] && has(anc, "p")) ||
		(tag == "a" && has(anc, "a")) ||
		(tag == "button" && has(anc, "button")) ||
		(tag == "form" && has(anc, "form")) ||
		(isHeadingTagT(tag) && isHeadingTagT(last))
}

// nestedPage builds root > anc[0] > … > anc[n-1] > tag, with text before and
// after the innermost element at every level (a paragraph's run of text).
func nestedPage(anc []string, tag string) VNode {
	var inner VNode
	switch tag {
	case "input", "img":
		inner = VNode{Kind: "element", Tag: tag, Attrs: attrs("name", "q")}
	case "textarea":
		inner = VNode{Kind: "element", Tag: tag, Attrs: map[string]string{}}
	default:
		inner = el(tag, nil, txt("c"))
	}
	cur := inner
	for i := len(anc) - 1; i >= 0; i-- {
		cur = el(anc[i], nil, txt("a "), cur, txt(" b"))
	}
	return el("div", nil, cur)
}

func TestSpaHydrate_stdUiNestingMatrix(t *testing.T) {
	ancestors := [][]string{
		{"p"}, {"p", "a"}, {"p", "button"}, {"p", "label"}, {"p", "span"}, {"p", "a", "span"},
		{"p", "label", "span"}, {"a"}, {"a", "div"}, {"button"}, {"button", "div"}, {"form"},
		{"form", "div"}, {"label"}, {"h2"}, {"h2", "div"}, {"section"}, {"div"},
	}
	children := []string{
		"div", "span", "p", "h3", "section", "form", "main", "nav", "footer", "header", "aside",
		"a", "button", "label", "img", "input", "textarea",
	}
	for _, anc := range ancestors {
		for _, child := range children {
			name := strings.Join(anc, ">") + ">" + child
			t.Run(name, func(t *testing.T) {
				emitted := stdUiTag(anc, child)
				root := withIDs(nestedPage(anc, emitted))
				if ok, reason := spaCanHydrate(xdom(ssrParse(t, root)), root); !ok {
					t.Fatalf("Std.Ui emits <%s> here; the parser must keep it, refused: %s\nhtml: %s",
						emitted, reason, renderVNode(*root, map[string]any{}))
				}
				orig := withIDs(nestedPage(anc, child))
				if ok, reason := spaCanHydrate(xdom(ssrParse(t, orig)), orig); !ok && !parserRestructuresModel(anc, child) {
					t.Fatalf("the parser restructures <%s> under %v (%s) but the Sky-side model does not flag it",
						child, anc, reason)
				}
			})
		}
	}
}

// The exact shapes the real app shipped, before and after the fix.
func TestSpaHydrate_linkWithElLabelInParagraph(t *testing.T) {
	shape := func(labelTag string) *VNode {
		return withIDs(el("div", nil,
			el("p", attrs("style", "display: block;"),
				txt("See our "),
				el("a", attrs("href", "/privacy", "style", "display: inline;"),
					el(labelTag, attrs("style", "display: flex; flex-direction: column; text-decoration: underline;"),
						txt("privacy policy"))),
				txt("."),
			)))
	}
	before := shape("div")
	if ok, _ := spaCanHydrate(xdom(ssrParse(t, before)), before); ok {
		t.Fatalf("precondition: <p><a><div> is restructured by the parser and must be refused")
	}
	after := shape("span")
	if ok, reason := spaCanHydrate(xdom(ssrParse(t, after)), after); !ok {
		t.Fatalf("<p><a><span> (what Std.Ui emits now) must hydrate, refused: %s", reason)
	}
}

// With JS disabled the SSR page must be usable: the first-paint overlay
// (html[data-sky-hydrating]::after, pointer-events:auto) is cleared only by
// script, so it covered the page forever and no form could be submitted. The
// page carries a <noscript> style in <head> that hides it.
func TestSpaSSRPage_noScriptHidesTheHydratingOverlay(t *testing.T) {
	page := SpaSSRPage("", `<div sky-id="r"><form sky-id="r.0#form" method="post"></form></div>`, "main.wasm", "{}")
	doc, err := html.ParseWithOptions(strings.NewReader(page), html.ParseOptionEnableScripting(false))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	var walk func(n *html.Node, inHead, inNoscript bool)
	walk = func(n *html.Node, inHead, inNoscript bool) {
		if n.Type == html.ElementNode {
			inHead = inHead || n.Data == "head"
			inNoscript = inNoscript || n.Data == "noscript"
			if n.Data == "style" && inHead && inNoscript && n.FirstChild != nil &&
				strings.Contains(n.FirstChild.Data, "html[data-sky-hydrating]::after{display:none}") {
				found = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inHead, inNoscript)
		}
	}
	walk(doc, false, false)
	if !found {
		t.Fatalf("the SSR page must hide the hydrating overlay when scripts are off:\n%s", page)
	}
}
