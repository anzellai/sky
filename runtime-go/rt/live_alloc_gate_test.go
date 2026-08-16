package rt

// Allocation ratchets for the server-side interaction path.
//
// WHY AN ALLOCATION GATE AND NOT A WALL-CLOCK ONE
//
// The attribution run (`docs/perf/skylive-interaction-cost.md`) found the
// interaction cost is allocation, not computation: 42-46% of self-time is Go
// runtime and GC and ~2% is compiled Sky logic. It also found the allocation
// COUNT is the stable observable — across three runs it varied 0.3% while
// CPU-per-interaction varied 1.4% on the same machine, and the machine here is
// shared with other agents. A wall-clock gate on this path would be a coin
// toss; an allocation gate is a ratchet.
//
// WHAT THESE GATES OBSERVE
//
// Every number asserted below is READ OFF the running system --
// `testing.AllocsPerRun` around the real functions, and field inspection of
// the tree `HtmlToVNode` actually produced. None of them is recomputed with
// the expression the production site uses, which would make the assertion an
// identity that holds regardless of correctness. The element census the
// budgets are divided by comes from `htmlPageCensus`, which counts what the
// FIXTURE BUILDER was told to build -- arithmetic over its own parameters,
// independent of anything in live.go -- and `TestAllocGateFixtureIsFaithful`
// asserts the production lowering reproduces that census. If the fixture and
// the census ever disagree, the census is what fails, and every budget below
// it is void rather than quietly rescaled.
//
// The budgets are deliberately loose (headroom over the measured value, stated
// per constant). They are regression ratchets, not targets: they catch a
// change that puts a per-element allocation back, not a 3% drift.
//
// WHAT THESE GATES DO NOT CATCH -- read this before trusting them
//
//  1. ANYTHING IN Std.Ui, which is the largest of the three fixes these were
//     written alongside. The marker-scan fusion lives in compiled Sky, above
//     the boundary these fixtures start at; a change putting the six
//     `hasMarker` scans back would leave every assertion in this file green.
//     That regression is currently caught by nothing in CI. The only
//     instrument that sees it is the attribution harness in
//     `docs/perf/runs/attribution-20260815/`, which is a manual run, not a
//     gate. This is the biggest known hole and it is not a small one.
//  2. BYTES. These count allocations, not bytes allocated. The two move
//     independently and did during this work: the first cut of the Std.Ui
//     fusion lowered the allocation count 6.7% while raising
//     bytes-per-interaction 8%. A gate shaped like this one would have
//     called that a win.
//  3. RETENTION. These measure the churn of ONE interaction. What a session
//     HOLDS is a different quantity, measured by a different harness
//     (`memrun.sh`), and it moved the wrong way by 0.3% during this work
//     without any assertion here noticing.
//  4. ATTRIBUTE-DENSITY-PROPORTIONAL REGRESSIONS. The census pins element and
//     text counts, but attributes per element are a fixture choice (~2). A
//     regression whose cost scales with attributes per element shows up here
//     attenuated against a real app carrying more.
//  5. ANYTHING ABOVE THE SKY BOUNDARY -- the user's `update` and `view`,
//     which the attribution measured at 84% of the handler.

import (
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// Fixture: Sky `Html` ADT values, so HtmlToVNode is on the measured path
// ---------------------------------------------------------------------
//
// The Phase 1 benchmark builds `VNode`s directly and therefore cannot see
// HtmlToVNode at all -- a stated gap in the measurement doc ("HtmlToVNode is
// not in the measured path"). These fixtures start one level up, at the ADT
// the compiled Sky `view` actually returns, so the ADT -> VNode lowering and
// its per-element map allocation are inside the gate.

func hAttr(k, v string) any {
	return SkyADT{SkyName: "Attr", Fields: []any{k, v}}
}

func hEvent(name string, msg any) any {
	return SkyADT{SkyName: "EventAttr", Fields: []any{
		SkyADT{SkyName: "OnMsg", Fields: []any{name, msg}},
	}}
}

func hText(s string) any {
	return SkyADT{SkyName: "HText", Fields: []any{s}}
}

func hElem(tag string, attrs []any, kids []any) any {
	return SkyADT{SkyName: "HElement", Fields: []any{tag, attrs, kids}}
}

// htmlItem mirrors benchItem's shape (the unit calibrated against the
// reference apps by TestBenchTreeSizesMatchReferenceApps) but expressed as the
// Html ADT: 4 elements + 3 text nodes, one of which carries an event and one
// of which carries nothing at all.
//
// The bare `span` is load-bearing for this gate: it is the element with NO
// attributes and NO events, the case eager map allocation charged two maps
// for. A fixture where every element has attributes could not observe it.
func htmlItem(i int) any {
	id := strconv.Itoa(i)
	return hElem("div",
		[]any{
			hAttr("class", "sky-row flex items-center gap-3"),
			hAttr("data-idx", id),
			hAttr("data-kind", "thread"),
		},
		[]any{
			hElem("span", []any{hAttr("class", "sky-title font-medium")},
				[]any{hText("Thread " + id)}),
			// No attributes, no events.
			hElem("span", nil, []any{hText(id + " replies")}),
			hElem("button",
				[]any{
					hAttr("class", "sky-btn"),
					hEvent("click", SkyADT{SkyName: "OpenThread"}),
				},
				[]any{hText("Open")}),
		})
}

// buildHtmlPage wraps nItems rows in page chrome. One element in the chrome
// carries a media-query marker so the style-injection pass has real work: a
// gate over a pass that never fires would measure nothing.
func buildHtmlPage(nItems int) any {
	items := make([]any, 0, nItems)
	for i := 0; i < nItems; i++ {
		items = append(items, htmlItem(i))
	}
	header := hElem("header",
		[]any{
			hAttr("class", "sky-header"),
			hAttr("data-sky-mq-q", "(min-width: 40em)"),
			hAttr("data-sky-mq-rules", "padding: 2rem;"),
		},
		[]any{hElem("h1", []any{hAttr("class", "sky-h1")}, []any{hText("Sky Forum")})})
	main := hElem("main", []any{hAttr("class", "sky-main")}, items)
	footer := hElem("footer", []any{hAttr("class", "sky-footer")}, []any{hText("(c) Sky")})
	return hElem("div", []any{hAttr("id", "sky-root")}, []any{header, main, footer})
}

// htmlPageCensus is the SECOND, independent implementation: it states what
// buildHtmlPage was asked to build by counting its own parameters, touching no
// production code. Every budget below is per-element against this census, and
// TestAllocGateFixtureIsFaithful makes the production lowering prove it
// reproduces these numbers.
type htmlCensus struct {
	elements    int // element nodes
	texts       int // text nodes
	attrless    int // element nodes with neither attributes nor events
	withEvents  int // element nodes carrying at least one event
	styleMarked int // element nodes carrying a style-injection marker
}

func htmlPageCensus(nItems int) htmlCensus {
	// per item: div + span(titled) + span(bare) + button = 4 elements,
	//           3 texts, 1 attrless element, 1 element with an event.
	// chrome:   root div + header + h1 + main + footer = 5 elements,
	//           2 texts (h1 title, footer), 0 attrless, 0 with events,
	//           1 style-marked (header).
	return htmlCensus{
		elements:    4*nItems + 5,
		texts:       3*nItems + 2,
		attrless:    1 * nItems,
		withEvents:  1 * nItems,
		styleMarked: 1,
	}
}

// walkCensus counts the tree the PRODUCTION lowering produced.
func walkCensus(n *VNode, c *htmlCensus) {
	switch n.Kind {
	case "text":
		c.texts++
	case "element":
		c.elements++
		if len(n.Attrs) == 0 && len(n.Events) == 0 {
			c.attrless++
		}
		if len(n.Events) > 0 {
			c.withEvents++
		}
		if n.Attrs["data-sky-mq-rules"] != "" {
			c.styleMarked++
		}
	}
	for i := range n.Children {
		walkCensus(&n.Children[i], c)
	}
}

const gateItems = 96 // 389 elements -- the 384-element reference app's scale

// TestAllocGateFixtureIsFaithful is the non-vacuity guard for every budget in
// this file. It proves the fixture really lowers to the shape the census
// claims, so a budget stated "per element" is divided by a number that means
// something. Without it, a fixture that silently stopped producing elements
// would make every allocation budget below trivially pass.
func TestAllocGateFixtureIsFaithful(t *testing.T) {
	want := htmlPageCensus(gateItems)
	vn := HtmlToVNode(buildHtmlPage(gateItems))
	var got htmlCensus
	walkCensus(&vn, &got)
	if got != want {
		t.Fatalf("fixture census mismatch:\n  got  %+v\n  want %+v", got, want)
	}
	if want.attrless == 0 || want.withEvents == 0 || want.styleMarked == 0 {
		t.Fatalf("fixture is vacuous for the properties under test: %+v", want)
	}
}

// TestHtmlToVNodeAllocatesNoMapsForUnusedAttrsAndEvents observes the tree the
// running lowering produced. Reverting the lazy allocation in HtmlToVNode
// makes every element carry two non-nil maps and this fails immediately.
//
// It asserts nil, not len()==0, precisely because len() cannot tell the two
// apart -- and len() is what every reader in live.go uses, which is why the
// change is safe and also why only a nil check can detect the regression.
func TestHtmlToVNodeAllocatesNoMapsForUnusedAttrsAndEvents(t *testing.T) {
	bare := HtmlToVNode(hElem("span", nil, []any{hText("x")}))
	if bare.Attrs != nil {
		t.Errorf("element with no attributes allocated an Attrs map: %#v", bare.Attrs)
	}
	if bare.Events != nil {
		t.Errorf("element with no events allocated an Events map: %#v", bare.Events)
	}

	// An element with attributes but no events allocates exactly one map.
	attrOnly := HtmlToVNode(hElem("span", []any{hAttr("class", "c")}, nil))
	if attrOnly.Attrs == nil || attrOnly.Attrs["class"] != "c" {
		t.Errorf("attribute was dropped: %#v", attrOnly.Attrs)
	}
	if attrOnly.Events != nil {
		t.Errorf("element with no events allocated an Events map: %#v", attrOnly.Events)
	}

	// And an element with an event allocates the Events map but not Attrs.
	evOnly := HtmlToVNode(hElem("button", []any{hEvent("click", SkyADT{SkyName: "M"})}, nil))
	if evOnly.Events == nil || evOnly.Events["click"] == nil {
		t.Errorf("event was dropped: %#v", evOnly.Events)
	}
	if evOnly.Attrs != nil {
		t.Errorf("element with no attributes allocated an Attrs map: %#v", evOnly.Attrs)
	}
}

// styleInjectionAllocBudget is allocations per ELEMENT for one
// applyStyleInjections call over a tree with a single style marker.
//
// Observed 0.0154/element (6 allocations over 389 elements) with the four
// marker specs hoisted to package level. With them rebuilt per node -- the
// state this gate exists to keep out -- the same fixture measures
// 11.03/element (4,290 allocations); that number is not an estimate, it is
// what this gate printed when the fix was reverted to prove it can fail.
//
// The budget sits at 0.5: 32x headroom over the passing value, and 22x below
// the failing one. A gate this far from both is not measuring drift, which is
// the intent -- it fires on a per-element allocation coming back, not on a
// few percent of movement.
const styleInjectionAllocBudget = 0.5

func TestStyleInjectionAllocationBudget(t *testing.T) {
	census := htmlPageCensus(gateItems)
	// A fresh tree per run: the passes strip their markers, so re-running over
	// the same tree would measure an already-processed no-op walk.
	trees := make([]VNode, 0, 64)
	for i := 0; i < 64; i++ {
		vn := HtmlToVNode(buildHtmlPage(gateItems))
		assignSkyIDs(&vn, "r")
		trees = append(trees, vn)
	}
	i := 0
	got := testing.AllocsPerRun(len(trees)-1, func() {
		applyStyleInjections(&trees[i])
		i++
	})
	per := got / float64(census.elements)
	t.Logf("applyStyleInjections: %.0f allocs over %d elements = %.4f/element",
		got, census.elements, per)
	if per > styleInjectionAllocBudget {
		t.Errorf("style injection allocates %.4f/element, budget %.4f "+
			"(%.0f allocs over %d elements) -- a per-element, per-pass "+
			"allocation has come back",
			per, styleInjectionAllocBudget, got, census.elements)
	}
}

// ---------------------------------------------------------------------
// Style-injection guard: the scan must not be able to skip a live pass
// ---------------------------------------------------------------------
//
// applyStyleInjections used to run all four passes unconditionally. It now
// runs one scan and then only the passes whose marker attrs the scan found.
// The failure that buys is silent: a pass that should have run does not,
// its markers survive into the wire output as inert `data-*`, and the
// styling it existed to emit is simply missing. Nothing else notices.
//
// These two tests are the guard. They are driven off `styleMarkerPasses`
// and each spec's own `markerAttrs`, so a pass or a marker added later is
// covered without anyone remembering to extend them.

// markerTree builds a minimal tree whose inner element carries `attr`.
func markerTree(attr, val string) VNode {
	vn := HtmlToVNode(hElem("div", []any{hAttr("id", "root")},
		[]any{hElem("section", []any{hAttr(attr, val)}, []any{hText("body")})}))
	assignSkyIDs(&vn, "r")
	return vn
}

// TestStyleInjectionGuardRunsEveryPassItsMarkerNeeds proves the guarded
// funnel is indistinguishable from running all four passes unconditionally,
// for every marker attr every spec declares, at an empty AND a non-empty
// value. The empty case is the one that catches a value-keyed scan: an
// empty marker is still stripped by its pass so it cannot leak onto the
// wire, so a scan testing `v != ""` rather than key presence would leave it
// in the output and this test would see the difference.
//
// Proven able to fail: deleting an entry from `styleMarkerBits`, or keying
// the scan on the value, turns the corresponding cases red.
func TestStyleInjectionGuardRunsEveryPassItsMarkerNeeds(t *testing.T) {
	if len(styleMarkerPasses) == 0 {
		t.Fatal("styleMarkerPasses is empty -- this gate is vacuous")
	}
	cases := 0
	for _, p := range styleMarkerPasses {
		if len(p.spec.markerAttrs) == 0 {
			t.Errorf("pass with bit %d declares no markerAttrs", p.bit)
		}
		for _, attr := range p.spec.markerAttrs {
			for _, val := range []string{"", "(min-width: 40em)"} {
				cases++
				guarded := markerTree(attr, val)
				applyStyleInjections(&guarded)

				unguarded := markerTree(attr, val)
				for _, q := range styleMarkerPasses {
					q.run(&unguarded)
				}

				got := renderVNode(guarded, nil)
				want := renderVNode(unguarded, nil)
				if got != want {
					t.Errorf("marker %q=%q: guarded funnel diverged from the "+
						"unconditional passes\n  guarded   %s\n  unguarded %s",
						attr, val, got, want)
				}
				if strings.Contains(got, attr) {
					t.Errorf("marker %q=%q survived into the rendered output "+
						"-- its pass did not run: %s", attr, val, got)
				}
			}
		}
	}
	if cases == 0 {
		t.Fatal("no marker attrs were exercised -- this gate is vacuous")
	}
	t.Logf("%d marker cases across %d passes", cases, len(styleMarkerPasses))
}

// TestStyleMarkerScanIsDerivedFromTheSpecs pins the two properties the
// scan's correctness rests on, neither of which is visible at its call
// site: every declared marker maps to its own pass's bit, and no marker
// collides with the `styleAttr` a pass stamps on the <style> elements it
// emits. A collision there would let one pass read another pass's output
// as its own input.
func TestStyleMarkerScanIsDerivedFromTheSpecs(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range styleMarkerPasses {
		for _, attr := range p.spec.markerAttrs {
			if styleMarkerBits[attr]&p.bit == 0 {
				t.Errorf("marker %q does not map to its pass's bit %d", attr, p.bit)
			}
			if seen[attr] {
				t.Errorf("marker %q is claimed by two passes", attr)
			}
			seen[attr] = true
		}
		if _, isMarker := styleMarkerBits[p.spec.styleAttr]; isMarker {
			t.Errorf("styleAttr %q is also a marker attr -- a pass would see "+
				"another pass's emitted <style> as work to do", p.spec.styleAttr)
		}
		// The scan filters on this prefix before consulting the map, so a
		// marker that lost it would be invisible rather than merely wrong.
		for _, attr := range p.spec.markerAttrs {
			if !strings.HasPrefix(attr, "data-sky-") {
				t.Errorf("marker %q lacks the data-sky- prefix the scan "+
					"filters on -- the scan can never see it", attr)
			}
		}
	}
	// A tree with no markers at all must report no work.
	clean := HtmlToVNode(hElem("div", []any{hAttr("class", "c")}, []any{hText("x")}))
	if got := scanStyleMarkers(&clean); got != 0 {
		t.Errorf("marker-free tree reported work: %d", got)
	}
}

// interactionAllocBudget is allocations per ELEMENT for the whole server-side
// interaction below the Sky boundary: lower the view ADT, assign ids, run the
// style passes, diff against the previous tree.
//
// Observed 16.50/element (6,418 allocations over 389 elements). Reverting the
// style-injection spec hoist takes it to 27.51 and this gate goes red, which
// is how it was proven able to fail.
//
// This budget is deliberately the LOOSER of the two: it covers a wide path
// and would only catch a large regression. The per-pass gate above is the
// sharp instrument; this one exists so that a regression somewhere else on
// the interaction path -- one that no single sharp gate is watching -- still
// has something in its way.
const interactionAllocBudget = 20.0

func TestInteractionAllocationBudget(t *testing.T) {
	census := htmlPageCensus(gateItems)
	prev := HtmlToVNode(buildHtmlPage(gateItems))
	assignSkyIDs(&prev, "r")
	applyStyleInjections(&prev)

	got := testing.AllocsPerRun(50, func() {
		next := HtmlToVNode(buildHtmlPage(gateItems))
		assignSkyIDs(&next, "r")
		applyStyleInjections(&next)
		diffTrees(&prev, &next, nil)
	})
	per := got / float64(census.elements)
	t.Logf("interaction: %.0f allocs over %d elements = %.2f/element",
		got, census.elements, per)
	if per > interactionAllocBudget {
		t.Errorf("interaction allocates %.2f/element, budget %.2f "+
			"(%.0f allocs over %d elements)",
			per, interactionAllocBudget, got, census.elements)
	}
}
