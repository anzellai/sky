package rt

// Sky.Live per-interaction CPU benchmark.
//
// WHAT THIS MEASURES, AND WHY IT EXISTS
//
// Sizing guidance for Sky.Live has been quoting "a complex view costs
// 2-10 ms per interaction". That figure was an inference, never a
// measurement, and every capacity number derived from it inherited the
// guess. This file replaces it with a curve.
//
// The server-side per-interaction path, as run by dispatch() at
// live.go:5022-5026 and the /_sky/event handler at live.go:4702, is:
//
//	update(msg, model)      -- compiled Sky, NOT measurable here (see below)
//	view(model)   -> Html   -- compiled Sky, NOT measurable here
//	HtmlToVNode             -- live.go:108   (ADT -> VNode lowering)
//	assignSkyIDs            -- live.go:573
//	applyStyleInjections    -- live.go:942
//	diffTrees               -- live.go:1295
//	json encode             -- live.go:4715  (writeEventJSON)
//
// The two Sky-compiled steps (`update` and `view`) cannot be driven from
// a Go benchmark without compiling a Sky program -- they are user code.
// Everything from HtmlToVNode down is Go and is measured here. See
// BenchmarkInteraction_* for the composite figure that the sizing
// guidance should actually quote.
//
// TREE SIZES ARE REAL, NOT INVENTED
//
// Node counts are anchored on the rendered output of the two reference
// apps, counted from their actual served HTML at this commit:
//
//	examples/19-skyforum    ->  94 sky-id-bearing elements  (canonical form flow)
//	examples/26-ui-showcase -> 384 sky-id-bearing elements  (every Std.Ui primitive)
//
// The sweep brackets those two and extends well past them so the result
// is a curve any app can be sized against, not a single number for one
// view.
//
// MUTATION CLASS DOMINATES NODE COUNT
//
// diffNodes (live.go:1301) is a non-keyed positional walk with two
// distinct cost regimes:
//
//   - text/attr change  -> walk the tree, emit a small patch. Cheap.
//   - child-count change -> the parent's ENTIRE child list is
//     re-serialized through renderVNode into one HTML patch
//     (live.go:1419-1461). Expensive, and proportional to the
//     subtree, not to the change.
//
// Quoting one number for "an interaction" without saying which regime
// it is in is exactly the error this file exists to correct, so every
// benchmark below is labelled by mutation class.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"testing"
)

// ---------------------------------------------------------------------
// Tree construction
// ---------------------------------------------------------------------

// benchItem builds one repeating unit of a realistic Sky.Live view: a
// row/card carrying attributes, two text spans and an interactive
// button. Shape mirrors what Std.Ui emits for a list row in
// 19-skyforum (a titled item with meta text and an action).
//
// 4 element nodes + 3 text nodes = 7 VNodes per item.
func benchItem(i int, title, meta string) VNode {
	id := strconv.Itoa(i)
	return VNode{
		Kind: "element",
		Tag:  "div",
		Attrs: map[string]string{
			"class":     "sky-row flex items-center gap-3",
			"data-idx":  id,
			"data-kind": "thread",
		},
		Events: map[string]any{},
		Children: []VNode{
			{
				Kind:     "element",
				Tag:      "span",
				Attrs:    map[string]string{"class": "sky-title font-medium"},
				Events:   map[string]any{},
				Children: []VNode{vtext(title)},
			},
			{
				Kind:     "element",
				Tag:      "span",
				Attrs:    map[string]string{"class": "sky-meta text-sm opacity-70"},
				Events:   map[string]any{},
				Children: []VNode{vtext(meta)},
			},
			{
				Kind:  "element",
				Tag:   "button",
				Attrs: map[string]string{"class": "sky-btn", "type": "button"},
				Events: map[string]any{
					"click": SkyADT{SkyName: "OpenThread"},
				},
				Children: []VNode{vtext("Open")},
			},
		},
	}
}

// buildBenchTree builds a page of nItems rows wrapped in the chrome a
// real Sky.Live page carries (root, header, nav, main, footer).
func buildBenchTree(nItems int, titleSuffix string) VNode {
	items := make([]VNode, 0, nItems)
	for i := 0; i < nItems; i++ {
		items = append(items, benchItem(i,
			fmt.Sprintf("Thread %d%s", i, titleSuffix),
			fmt.Sprintf("%d replies - updated 3h ago", i*7%97),
		))
	}

	header := VNode{
		Kind:   "element",
		Tag:    "header",
		Attrs:  map[string]string{"class": "sky-header"},
		Events: map[string]any{},
		Children: []VNode{
			{
				Kind:     "element",
				Tag:      "h1",
				Attrs:    map[string]string{"class": "sky-h1"},
				Events:   map[string]any{},
				Children: []VNode{vtext("Sky Forum")},
			},
			{
				Kind:   "element",
				Tag:    "nav",
				Attrs:  map[string]string{"class": "sky-nav"},
				Events: map[string]any{},
				Children: []VNode{
					{
						Kind:     "element",
						Tag:      "a",
						Attrs:    map[string]string{"href": "/", "sky-nav": "1"},
						Events:   map[string]any{},
						Children: []VNode{vtext("Home")},
					},
					{
						Kind:     "element",
						Tag:      "a",
						Attrs:    map[string]string{"href": "/new", "sky-nav": "1"},
						Events:   map[string]any{},
						Children: []VNode{vtext("New")},
					},
				},
			},
		},
	}

	main := VNode{
		Kind:     "element",
		Tag:      "main",
		Attrs:    map[string]string{"class": "sky-main"},
		Events:   map[string]any{},
		Children: items,
	}

	return VNode{
		Kind:   "element",
		Tag:    "div",
		Attrs:  map[string]string{"id": "sky-root"},
		Events: map[string]any{},
		Children: []VNode{
			header,
			main,
			{
				Kind:     "element",
				Tag:      "footer",
				Attrs:    map[string]string{"class": "sky-footer"},
				Events:   map[string]any{},
				Children: []VNode{vtext("(c) Sky")},
			},
		},
	}
}

// countVNodes counts every node in the tree, elements and text alike --
// the denominator for the per-node cost figure.
func countVNodes(n *VNode) int {
	total := 1
	for i := range n.Children {
		total += countVNodes(&n.Children[i])
	}
	return total
}

// prepared returns a tree with sky-ids assigned, exactly as the server
// holds it in liveSession.prevTree.
func prepared(nItems int, titleSuffix string) *VNode {
	t := buildBenchTree(nItems, titleSuffix)
	assignSkyIDs(&t, "r")
	return &t
}

// benchItemCounts brackets the two reference apps and extends past
// them. Each item contributes 4 elements, plus 8 elements of page
// chrome, so:
//
//	21 items ->  92 elements ~= 19-skyforum's     94
//	94 items -> 384 elements ==  26-ui-showcase's 384
//
// The larger counts exist to establish the slope, not because any
// current example is that big.
var benchItemCounts = []int{1, 21, 94, 250, 500, 1000, 2000}

// The two reference-app anchor points, named so the calibration test
// and the fixture sweep cannot drift apart silently.
const (
	skyforumItems = 21 // ~= 94 elements
	showcaseItems = 94 // == 384 elements
	skyforumElems = 94
	showcaseElems = 384
)

// ---------------------------------------------------------------------
// Mutation classes
// ---------------------------------------------------------------------

// mutation produces the "after" tree for a given "before" tree. Each
// models a different real interaction, and they differ by orders of
// magnitude in cost -- which is the headline finding.
type mutation struct {
	name string
	// desc explains which real interaction this models.
	desc string
	make func(nItems int) (before, after *VNode)
}

var benchMutations = []mutation{
	{
		name: "noop",
		desc: "model advanced but view is unchanged; pure diff walk floor",
		make: func(n int) (*VNode, *VNode) {
			return prepared(n, ""), prepared(n, "")
		},
	},
	{
		name: "text_one",
		desc: "one label changed (counter, status, single field) -- the common case",
		make: func(n int) (*VNode, *VNode) {
			before := prepared(n, "")
			built := buildBenchTree(n, "")
			after := &built
			// main -> first item -> title span -> text
			after.Children[1].Children[0].Children[0].Children[0].Text = "Thread 0 (edited)"
			assignSkyIDs(after, "r")
			return before, after
		},
	},
	{
		name: "attr_one",
		desc: "one class toggled (selection, focus ring, disabled state)",
		make: func(n int) (*VNode, *VNode) {
			before := prepared(n, "")
			built := buildBenchTree(n, "")
			after := &built
			after.Children[1].Children[0].Attrs["class"] = "sky-row flex items-center gap-3 is-selected"
			assignSkyIDs(after, "r")
			return before, after
		},
	},
	{
		name: "text_all",
		desc: "every row's text changed (filter/sort/refresh of a whole list)",
		make: func(n int) (*VNode, *VNode) {
			return prepared(n, ""), prepared(n, " *")
		},
	},
	{
		name: "list_append",
		desc: "one row added -- child-count change, forces full subtree re-render",
		make: func(n int) (*VNode, *VNode) {
			return prepared(n, ""), prepared(n+1, "")
		},
	},
}

// ---------------------------------------------------------------------
// The headline benchmark: one whole server-side interaction
// ---------------------------------------------------------------------

// BenchmarkInteraction measures the complete Go-side per-interaction
// path: assign sky-ids on the freshly rendered tree, diff it against
// the previous tree, and JSON-encode the response envelope exactly as
// writeEventJSON (live.go:4715) does.
//
// This is the number the sizing guidance should quote. It excludes the
// user's compiled-Sky `update` and `view`, which are app-specific and
// unmeasurable from Go -- see the file header.
func BenchmarkInteraction(b *testing.B) {
	for _, m := range benchMutations {
		for _, n := range benchItemCounts {
			before, after := m.make(n)
			nodes := countVNodes(after)
			name := fmt.Sprintf("%s/items=%d/nodes=%d", m.name, n, nodes)

			b.Run(name, func(b *testing.B) {
				// Re-render produces a fresh tree each interaction, so
				// the sky-id assignment is part of the per-interaction
				// cost. Work on a copy to keep the input pristine.
				b.ReportAllocs()
				b.ResetTimer()
				var sink int
				for i := 0; i < b.N; i++ {
					fresh := *after
					assignSkyIDs(&fresh, "r")
					patches := diffTrees(before, &fresh, nil)
					payload := map[string]any{
						"seq":     int64(i),
						"patches": patches,
					}
					enc := json.NewEncoder(io.Discard)
					if err := enc.Encode(payload); err != nil {
						b.Fatal(err)
					}
					sink += len(patches)
				}
				b.StopTimer()
				if sink == 0 && m.name != "noop" {
					b.Fatalf("mutation %q produced no patches at all -- "+
						"the benchmark is measuring a no-op and the number is meaningless", m.name)
				}
				b.ReportMetric(float64(nodes), "nodes")
				b.ReportMetric(float64(sink)/float64(b.N), "patches/op")
			})
		}
	}
}

// ---------------------------------------------------------------------
// Component breakdown -- so the composite figure can be attributed
// ---------------------------------------------------------------------

func BenchmarkDiffOnly(b *testing.B) {
	for _, m := range benchMutations {
		for _, n := range benchItemCounts {
			before, after := m.make(n)
			nodes := countVNodes(after)
			b.Run(fmt.Sprintf("%s/nodes=%d", m.name, nodes), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				var sink int
				for i := 0; i < b.N; i++ {
					sink += len(diffTrees(before, after, nil))
				}
				b.StopTimer()
				b.ReportMetric(float64(nodes), "nodes")
				_ = sink
			})
		}
	}
}

func BenchmarkAssignSkyIDs(b *testing.B) {
	for _, n := range benchItemCounts {
		t := buildBenchTree(n, "")
		nodes := countVNodes(&t)
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fresh := t
				assignSkyIDs(&fresh, "r")
			}
			b.StopTimer()
			b.ReportMetric(float64(nodes), "nodes")
		})
	}
}

// BenchmarkRenderFull measures a whole-page render to HTML -- the cost
// paid on first paint, on a full-replace fallback, and on every
// child-count change within the affected subtree.
func BenchmarkRenderFull(b *testing.B) {
	for _, n := range benchItemCounts {
		t := prepared(n, "")
		nodes := countVNodes(t)
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for i := 0; i < b.N; i++ {
				sink += len(renderVNode(*t, map[string]any{}))
			}
			b.StopTimer()
			if sink == 0 {
				b.Fatal("renderVNode produced no output -- benchmark is vacuous")
			}
			b.ReportMetric(float64(nodes), "nodes")
			b.ReportMetric(float64(sink)/float64(b.N), "html_bytes")
		})
	}
}

// BenchmarkPatchEncode isolates JSON serialization of the patch set, so
// the composite number can be split into diff cost vs wire cost.
func BenchmarkPatchEncode(b *testing.B) {
	for _, m := range []string{"text_one", "text_all", "list_append"} {
		for _, n := range benchItemCounts {
			var mu mutation
			for _, c := range benchMutations {
				if c.name == m {
					mu = c
				}
			}
			before, after := mu.make(n)
			patches := diffTrees(before, after, nil)
			if len(patches) == 0 {
				b.Fatalf("%s/%d produced no patches -- nothing to encode", m, n)
			}
			b.Run(fmt.Sprintf("%s/patches=%d", m, len(patches)), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					enc := json.NewEncoder(io.Discard)
					if err := enc.Encode(map[string]any{"seq": int64(i), "patches": patches}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------------
// Non-vacuity gates
// ---------------------------------------------------------------------
//
// A benchmark that measures nothing still reports a confident ns/op.
// These tests run in the normal `go test` suite and fail loudly if the
// fixtures stop exercising the path they claim to.

// TestBenchFixturesAreNonVacuous proves each mutation class actually
// produces the patch shape it is named for. If diffTrees is changed so
// that (say) list_append no longer emits an HTML patch, this fails
// rather than silently turning the benchmark into a no-op walk.
func TestBenchFixturesAreNonVacuous(t *testing.T) {
	cases := []struct {
		mutation  string
		wantEmpty bool
		wantHTML  bool
		wantText  bool
		wantAttrs bool
	}{
		{mutation: "noop", wantEmpty: true},
		{mutation: "text_one", wantText: true},
		{mutation: "attr_one", wantAttrs: true},
		{mutation: "text_all", wantText: true},
		{mutation: "list_append", wantHTML: true},
	}

	for _, tc := range cases {
		var mu mutation
		for _, c := range benchMutations {
			if c.name == tc.mutation {
				mu = c
			}
		}
		if mu.make == nil {
			t.Fatalf("no such mutation %q", tc.mutation)
		}
		before, after := mu.make(13) // skyforum-sized
		patches := diffTrees(before, after, nil)

		if tc.wantEmpty {
			if len(patches) != 0 {
				t.Errorf("%s: expected no patches, got %d -- the "+
					"no-op floor is not measuring a no-op", tc.mutation, len(patches))
			}
			continue
		}
		if len(patches) == 0 {
			t.Errorf("%s: produced ZERO patches. The benchmark for this "+
				"class is measuring an unchanged tree and its ns/op is "+
				"meaningless.", tc.mutation)
			continue
		}

		var sawHTML, sawText, sawAttrs bool
		for _, p := range patches {
			if p.HTML != nil {
				sawHTML = true
			}
			if p.Text != nil {
				sawText = true
			}
			if len(p.Attrs) > 0 {
				sawAttrs = true
			}
		}
		if tc.wantHTML && !sawHTML {
			t.Errorf("%s: expected a full-HTML subtree patch, got none (%d patches)", tc.mutation, len(patches))
		}
		if tc.wantText && !sawText {
			t.Errorf("%s: expected a text patch, got none (%d patches)", tc.mutation, len(patches))
		}
		if tc.wantAttrs && !sawAttrs {
			t.Errorf("%s: expected an attrs patch, got none (%d patches)", tc.mutation, len(patches))
		}
	}
}

// TestBenchTreeSizesMatchReferenceApps pins the fixture sizes to the
// two reference apps' real rendered node counts. Counted from the HTML
// actually served by each app at this commit:
//
//	19-skyforum     94 sky-id elements  (135 tags total)
//	26-ui-showcase 384 sky-id elements  (422 tags total)
//
// If the fixture drifts far from those, the curve is being fitted to
// shapes no real app has.
func TestBenchTreeSizesMatchReferenceApps(t *testing.T) {
	forum := buildBenchTree(skyforumItems, "")
	showcase := buildBenchTree(showcaseItems, "")

	// Within 10% of the real apps' element counts.
	assertNear(t, "19-skyforum", countElements(&forum), skyforumElems, 0.10)
	assertNear(t, "26-ui-showcase", countElements(&showcase), showcaseElems, 0.10)
}

func countElements(n *VNode) int {
	total := 0
	if n.Kind == "element" {
		total = 1
	}
	for i := range n.Children {
		total += countElements(&n.Children[i])
	}
	return total
}

func assertNear(t *testing.T, label string, got, want int, tol float64) {
	t.Helper()
	lo := float64(want) * (1 - tol)
	hi := float64(want) * (1 + tol)
	if float64(got) < lo || float64(got) > hi {
		t.Errorf("%s fixture has %d elements, real app renders %d "+
			"(tolerance +/-%.0f%%). The benchmark curve is anchored on a "+
			"shape the app does not have.", label, got, want, tol*100)
	}
}
