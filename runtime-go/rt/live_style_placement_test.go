package rt

import (
	"strings"
	"testing"
)

// Issue #326: a pass that injects a <style> for an element must never put
// it where it changes that element's content. A <textarea> shows its
// children as its text, so a <style> child became the value the user saw.
// <select>, <option> and <optgroup> hold only options (the parser drops a
// <style> child), and a <title>, <script>, <style>, <iframe>, <noscript> or
// <template> child is text or inert. For all of these the style goes after
// the element, in the nearest ancestor that can hold it.

func TestStylePlacement_TextareaStyleIsSiblingNotContent(t *testing.T) {
	tree := el("div", nil,
		el("textarea", map[string]string{
			"data-sky-pc-rules": "v| outline: 3px solid #1e6999 !important;",
		}),
	)
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) != 2 {
		t.Fatalf("want textarea + sibling style, got %d children: %+v", len(tree.Children), tree.Children)
	}
	ta, st := tree.Children[0], tree.Children[1]
	if ta.Tag != "textarea" || len(ta.Children) != 0 {
		t.Fatalf("textarea must keep no children, got tag %q children %+v", ta.Tag, ta.Children)
	}
	if st.Tag != "style" || st.Attrs["data-sky-pc"] != ta.SkyID {
		t.Fatalf("sibling must be the textarea's style, got %+v", st)
	}
	if st.SkyID != ta.SkyID+".~pc" {
		t.Errorf("style sky-id = %q, want %q", st.SkyID, ta.SkyID+".~pc")
	}
	if _, ok := ta.Attrs["data-sky-pc-rules"]; ok {
		t.Errorf("marker leaked on textarea: %v", ta.Attrs)
	}

	html := renderVNode(tree, nil)
	open := strings.Index(html, "<textarea")
	end := strings.Index(html, "</textarea>")
	if open < 0 || end < 0 {
		t.Fatalf("no textarea in %q", html)
	}
	body := html[strings.Index(html[open:], ">")+open+1 : end]
	if body != "" {
		t.Errorf("textarea content must stay empty, got %q", body)
	}
}

func TestStylePlacement_OptionStyleLeavesTheSelect(t *testing.T) {
	tree := el("div", nil,
		el("select", nil,
			el("optgroup", nil,
				el("option", map[string]string{"data-sky-pc-rules": "h| color: red;"}),
			),
		),
	)
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)

	if len(tree.Children) != 2 {
		t.Fatalf("want select + style after it, got %+v", tree.Children)
	}
	sel, st := tree.Children[0], tree.Children[1]
	opt := sel.Children[0].Children[0]
	if len(sel.Children) != 1 || len(sel.Children[0].Children) != 1 || len(opt.Children) != 0 {
		t.Fatalf("select subtree must hold no style: %+v", sel)
	}
	if st.Tag != "style" || st.Attrs["data-sky-pc"] != opt.SkyID {
		t.Fatalf("style after the select must belong to the option, got %+v", st)
	}
}

func TestStylePlacement_SelectOwnStyleIsSibling(t *testing.T) {
	tree := el("div", nil,
		el("select", map[string]string{"data-sky-pc-rules": "f| outline: 1px solid red;"},
			el("option", nil),
		),
	)
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)
	if len(tree.Children) != 2 || tree.Children[1].Tag != "style" {
		t.Fatalf("want select + sibling style, got %+v", tree.Children)
	}
	if got := tree.Children[0].Children; len(got) != 1 || got[0].Tag != "option" {
		t.Fatalf("select must hold only its option, got %+v", got)
	}
}

func TestStylePlacement_OtherPassesUseTheSameRule(t *testing.T) {
	tree := el("div", nil,
		el("textarea", map[string]string{
			"data-sky-mq-q":     "(min-width: 640px)",
			"data-sky-mq-rules": "width: 100%;",
		}),
	)
	assignSkyIDs(&tree, "r")
	injectMediaQueryStyles(&tree)
	if ta := tree.Children[0]; len(ta.Children) != 0 {
		t.Fatalf("media-query style must not go inside the textarea: %+v", ta.Children)
	}
}

func TestStylePlacement_OrdinaryElementKeepsFirstChild(t *testing.T) {
	tree := el("div", nil,
		el("button", map[string]string{"data-sky-pc-rules": "h| color: red;"},
			VNode{Kind: "text", Text: "Go"},
		),
	)
	assignSkyIDs(&tree, "r")
	injectPseudoClassStyles(&tree)
	btn := tree.Children[0]
	if len(tree.Children) != 1 || len(btn.Children) != 2 || btn.Children[0].Tag != "style" {
		t.Fatalf("a button keeps its style as the first child: %+v", tree.Children)
	}
}
