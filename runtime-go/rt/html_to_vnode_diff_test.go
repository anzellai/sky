package rt

// html_to_vnode_diff_test.go — byte-identical differential gate for the
// Element→Html→VNode lowering optimization.
//
// The render pipeline's Html→VNode pass (HtmlToVNode) was rewritten to
// stop routing typed `[]SkyADT` attribute/child slices through
// `asList` — which reflect-boxes every element (`reflect.Value.Interface`
// → `reflect.unsafe_New`), the single largest allocation site in the
// render profile (≈22% of all render-path objects, see
// docs/perf/runs/render-opt-htmltovnode-20260818/). The rewrite MUST be
// byte-for-byte output-preserving.
//
// This gate freezes the PRE-optimization HtmlToVNode as htmlToVNodeRef and
// asserts, over a corpus covering every ADT shape and both slice
// representations, that:
//   1. the live HtmlToVNode produces a VNode tree DeepEqual to the frozen
//      reference, and
//   2. renderVNode of each is byte-identical.
//
// FALSIFIABLE: flip any behavioural detail in HtmlToVNode (drop an attr,
// change child order, skip class/style joining) and TestHtmlToVNodeDiff
// goes red. TestHtmlToVNodeDiffGateIsFalsifiable proves the gate itself
// can fail by diffing the reference against a deliberately-broken variant.

import (
	"reflect"
	"testing"
)

// ─── frozen reference: the HtmlToVNode / applyHtmlAttr behaviour as it
// stood before the typed-slice fast path was introduced. Do NOT edit to
// track the live implementation — its whole purpose is to be the
// independent oracle. ───────────────────────────────────────────────

func htmlToVNodeRef(node any) VNode {
	node = unwrapAny(node)
	if vn, ok := node.(VNode); ok {
		return vn
	}
	name, _, fields, ok := unwrapADTShape(node)
	if !ok {
		return vtext(sprintfRef(node))
	}
	switch name {
	case "HText":
		if len(fields) > 0 {
			return vtext(AsString(fields[0]))
		}
		return vtext("")
	case "HRaw":
		if len(fields) > 0 {
			return VNode{Kind: "raw", Text: AsString(fields[0])}
		}
		return VNode{Kind: "raw"}
	case "HElement":
		if len(fields) < 3 {
			return vtext("")
		}
		vn := VNode{
			Kind: "element",
			Tag:  AsString(fields[0]),
		}
		for _, a := range asList(fields[1]) {
			applyHtmlAttrRef(&vn, a)
		}
		for _, c := range asList(fields[2]) {
			vn.Children = append(vn.Children, htmlToVNodeRef(c))
		}
		return vn
	default:
		return vtext("")
	}
}

func sprintfRef(node any) string {
	// mirror fmt.Sprintf("%v", node) closely enough for the non-ADT
	// fallback corpus (which uses plain strings).
	return AsString(node)
}

func applyHtmlAttrRef(vn *VNode, a any) {
	a = unwrapAny(a)
	name, _, fields, ok := unwrapADTShape(a)
	if !ok {
		return
	}
	switch name {
	case "Attr":
		if len(fields) >= 2 {
			k := AsString(fields[0])
			v := SafeAttrURL(k, AsString(fields[1]))
			if existing, ok := vn.Attrs[k]; ok && existing != "" {
				switch k {
				case "class":
					vn.setAttr(k, existing+" "+v)
					return
				case "style":
					sep := "; "
					if hasSuffixRef(existing, ";") {
						sep = " "
					}
					vn.setAttr(k, existing+sep+v)
					return
				}
			}
			vn.setAttr(k, v)
		}
	case "BoolAttr":
		if len(fields) >= 2 && AsBool(fields[1]) {
			k := AsString(fields[0])
			vn.setAttr(k, k)
		}
	case "EventAttr":
		if len(fields) >= 1 {
			ev := unwrapAny(fields[0])
			if _, _, evFields, ok := unwrapADTShape(ev); ok && len(evFields) >= 2 {
				vn.setEvent(AsString(evFields[0]), evFields[1])
			}
		}
	case "NoAttr":
	}
}

func hasSuffixRef(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ─── corpus ─────────────────────────────────────────────────────────

func adtHText(s string) SkyADT {
	return SkyADT{Tag: 1, SkyName: "HText", Fields: []any{s}}
}
func adtHRaw(s string) SkyADT {
	return SkyADT{Tag: 2, SkyName: "HRaw", Fields: []any{s}}
}
func adtHElem(tag string, attrs, kids any) SkyADT {
	return SkyADT{Tag: 0, SkyName: "HElement", Fields: []any{tag, attrs, kids}}
}
func adtAttr(k, v string) SkyADT {
	return SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{k, v}}
}
func adtBoolAttr(k string, b bool) SkyADT {
	return SkyADT{Tag: 1, SkyName: "BoolAttr", Fields: []any{k, b}}
}
func adtNoAttr() SkyADT {
	return SkyADT{Tag: 3, SkyName: "NoAttr", Fields: []any{}}
}
func adtOnMsg(name string, msg any) SkyADT {
	return SkyADT{Tag: 0, SkyName: "OnMsg", Fields: []any{name, msg}}
}
func adtEventAttr(ev SkyADT) SkyADT {
	return SkyADT{Tag: 2, SkyName: "EventAttr", Fields: []any{ev}}
}

// htmlCorpus returns Html ADT values exercising every shape and BOTH
// slice representations ([]SkyADT — the typed fast path — and []any —
// the erased/mixed path incl. a VNode passthrough child).
func htmlCorpus() []any {
	msg := SkyADT{Tag: 0, SkyName: "OpenThread", Fields: []any{}}

	// typed-slice element: attrs and kids are []SkyADT
	typedRow := adtHElem("div",
		[]SkyADT{
			adtAttr("class", "row"),
			adtAttr("class", "hi"),              // class join
			adtAttr("style", "color:red;"),      // style join (existing ends ";")
			adtAttr("style", "font-weight:700"), // style join
			adtBoolAttr("disabled", true),
			adtBoolAttr("hidden", false), // dropped (false)
			adtNoAttr(),
			adtAttr("href", "javascript:alert(1)"), // URL neutralised
			adtAttr("data-x", "y"),
		},
		[]SkyADT{
			adtHText("hello & <world>"),
			adtHElem("span", []SkyADT{adtAttr("class", "meta")}, []SkyADT{adtHText("m")}),
			adtHElem("button",
				[]SkyADT{adtEventAttr(adtOnMsg("click", msg)), adtAttr("type", "button")},
				[]SkyADT{adtHText("Open")}),
			adtHRaw("<b>raw</b>"),
		},
	)

	// []any element: attrs and kids as []any, with a VNode passthrough child
	anyEl := adtHElem("section",
		[]any{adtAttr("id", "s"), adtBoolAttr("open", true)},
		[]any{
			adtHText("a"),
			VNode{Kind: "element", Tag: "custom", Attrs: map[string]string{"k": "v"}},
			adtHText("b"),
		},
	)

	// deeply nested typed
	nested := adtHElem("main", []SkyADT{},
		[]SkyADT{
			adtHElem("ul", []SkyADT{adtAttr("class", "list")},
				[]SkyADT{
					adtHElem("li", []SkyADT{}, []SkyADT{adtHText("1")}),
					adtHElem("li", []SkyADT{}, []SkyADT{adtHText("2")}),
					adtHElem("li", []SkyADT{}, []SkyADT{
						adtHElem("a", []SkyADT{adtAttr("href", "/x")}, []SkyADT{adtHText("link")}),
					}),
				}),
		})

	return []any{
		adtHText(""),
		adtHText("plain text & entities <>"),
		adtHRaw("<div>raw</div>"),
		adtHElem("div", []SkyADT{}, []SkyADT{}), // empty
		adtHElem("img", []SkyADT{adtAttr("src", "data:text/html,<script>")}, []SkyADT{}),
		typedRow,
		anyEl,
		nested,
		// mixed: typed attrs, []any kids
		adtHElem("p", []SkyADT{adtAttr("class", "x")}, []any{adtHText("mixed")}),
		// mixed: []any attrs, typed kids
		adtHElem("p", []any{adtAttr("class", "y")}, []SkyADT{adtHText("mixed2")}),
	}
}

func TestHtmlToVNodeDiff(t *testing.T) {
	for i, c := range htmlCorpus() {
		gotNew := HtmlToVNode(c)
		gotRef := htmlToVNodeRef(c)
		if !reflect.DeepEqual(gotNew, gotRef) {
			t.Errorf("case %d: VNode tree differs from frozen reference\nnew: %#v\nref: %#v", i, gotNew, gotRef)
			continue
		}
		hNew := renderVNode(gotNew, map[string]any{})
		hRef := renderVNode(gotRef, map[string]any{})
		if hNew != hRef {
			t.Errorf("case %d: rendered HTML differs\nnew: %s\nref: %s", i, hNew, hRef)
		}
	}
}

// TestHtmlToVNodeDiffGateIsFalsifiable proves the gate can fail: a
// deliberately-broken converter (drops the first attribute of every
// element) must diverge from the reference on the corpus.
func TestHtmlToVNodeDiffGateIsFalsifiable(t *testing.T) {
	diverged := false
	for _, c := range htmlCorpus() {
		broken := htmlToVNodeBroken(c)
		ref := htmlToVNodeRef(c)
		if !reflect.DeepEqual(broken, ref) || renderVNode(broken, map[string]any{}) != renderVNode(ref, map[string]any{}) {
			diverged = true
			break
		}
	}
	if !diverged {
		t.Fatal("gate is vacuous: a converter that drops attributes was NOT detected as divergent")
	}
}

// ─── before/after allocation benchmark ─────────────────────────────
//
// Builds a forum-shaped Html ADT (typed []SkyADT attrs+children, exactly
// what Std.Ui/Std.Html emit) at the render re-baseline's view sizes and
// measures the Html→VNode pass BEFORE (htmlToVNodeRef, the frozen
// pre-optimization converter) and AFTER (HtmlToVNode) in one binary, so
// the numbers are directly comparable and reproducible without a compiler
// rebuild. Run:
//
//	go test ./rt/ -run=XXX -bench=BenchmarkHtmlToVNode_ -benchmem
//
// ~16 elements/post matches the re-baseline (forum-rebaseline-20260816):
// 6 posts ≈ 94 sky-id elements, 60 posts ≈ 974.

func forumPostRowADT(i int) SkyADT {
	id := AsString(i)
	return adtHElem("div",
		[]SkyADT{
			adtAttr("class", "post-row flex items-center gap-3"),
			adtAttr("style", "display: flex; padding: 8px 0px 8px 0px;"),
			adtAttr("data-idx", id),
		},
		[]SkyADT{
			adtHElem("div", []SkyADT{adtAttr("class", "votes"), adtAttr("style", "display: flex;")},
				[]SkyADT{
					adtHElem("button",
						[]SkyADT{adtEventAttr(adtOnMsg("click", SkyADT{Tag: 0, SkyName: "UpvotePost", Fields: []any{i}})), adtAttr("type", "button"), adtAttr("class", "up")},
						[]SkyADT{adtHText("▲")}),
					adtHElem("span", []SkyADT{adtAttr("class", "score")}, []SkyADT{adtHText(id)}),
					adtHElem("button",
						[]SkyADT{adtEventAttr(adtOnMsg("click", SkyADT{Tag: 0, SkyName: "DownvotePost", Fields: []any{i}})), adtAttr("type", "button"), adtAttr("class", "down")},
						[]SkyADT{adtHText("▼")}),
				}),
			adtHElem("div", []SkyADT{adtAttr("class", "body"), adtAttr("style", "display: flex;")},
				[]SkyADT{
					adtHElem("a", []SkyADT{adtAttr("href", "https://example.com/"+id), adtAttr("class", "title")},
						[]SkyADT{adtHText("Post title number " + id)}),
					adtHElem("div", []SkyADT{adtAttr("class", "meta")},
						[]SkyADT{
							adtHElem("span", []SkyADT{}, []SkyADT{adtHText("by user" + id)}),
							adtHElem("span", []SkyADT{}, []SkyADT{adtHText("• 3h ago")}),
						}),
				}),
		})
}

func forumPageADT(nPosts int) SkyADT {
	rows := make([]SkyADT, 0, nPosts)
	for i := 0; i < nPosts; i++ {
		rows = append(rows, forumPostRowADT(i))
	}
	return adtHElem("div", []SkyADT{adtAttr("id", "sky-root"), adtAttr("class", "forum")},
		[]SkyADT{
			adtHElem("header", []SkyADT{adtAttr("class", "chrome")},
				[]SkyADT{adtHElem("h1", []SkyADT{}, []SkyADT{adtHText("Sky Forum")})}),
			adtHElem("main", []SkyADT{adtAttr("class", "posts")}, rows),
		})
}

// countVNodeTree counts elements+text in a VNode tree.
func countVNodeTree(n VNode) int {
	total := 1
	for i := range n.Children {
		total += countVNodeTree(n.Children[i])
	}
	return total
}

var forumPostCounts = []int{6, 60} // ≈ 94 and ≈ 974 sky-id elements

func BenchmarkHtmlToVNode_Before(b *testing.B) {
	for _, nPosts := range forumPostCounts {
		page := forumPageADT(nPosts)
		nodes := countVNodeTree(htmlToVNodeRef(page))
		b.Run(elemLabel(nodes), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for i := 0; i < b.N; i++ {
				sink += len(htmlToVNodeRef(page).Children)
			}
			_ = sink
			b.ReportMetric(float64(nodes), "vnodes")
		})
	}
}

func BenchmarkHtmlToVNode_After(b *testing.B) {
	for _, nPosts := range forumPostCounts {
		page := forumPageADT(nPosts)
		nodes := countVNodeTree(HtmlToVNode(page))
		b.Run(elemLabel(nodes), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for i := 0; i < b.N; i++ {
				sink += len(HtmlToVNode(page).Children)
			}
			_ = sink
			b.ReportMetric(float64(nodes), "vnodes")
		})
	}
}

func elemLabel(nodes int) string {
	return "vnodes=" + AsString(nodes)
}

// htmlToVNodeBroken is htmlToVNodeRef with one injected bug: it skips the
// first attribute of every element. Exists only to prove the gate bites.
func htmlToVNodeBroken(node any) VNode {
	node = unwrapAny(node)
	if vn, ok := node.(VNode); ok {
		return vn
	}
	name, _, fields, ok := unwrapADTShape(node)
	if !ok {
		return vtext(sprintfRef(node))
	}
	switch name {
	case "HText":
		if len(fields) > 0 {
			return vtext(AsString(fields[0]))
		}
		return vtext("")
	case "HRaw":
		if len(fields) > 0 {
			return VNode{Kind: "raw", Text: AsString(fields[0])}
		}
		return VNode{Kind: "raw"}
	case "HElement":
		if len(fields) < 3 {
			return vtext("")
		}
		vn := VNode{Kind: "element", Tag: AsString(fields[0])}
		attrs := asList(fields[1])
		if len(attrs) > 0 {
			attrs = attrs[1:] // THE BUG: drop first attribute
		}
		for _, a := range attrs {
			applyHtmlAttrRef(&vn, a)
		}
		for _, c := range asList(fields[2]) {
			vn.Children = append(vn.Children, htmlToVNodeBroken(c))
		}
		return vn
	default:
		return vtext("")
	}
}
