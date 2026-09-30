package rt

import "testing"

// The H-2 shape: a click on a card inside a list column. The card's Msg
// re-renders inside the dispatch; the patch keeps the list column and renames
// it into the "Back" element, which has a click handler. The click, still
// bubbling, reaches the column: it must run nothing.
func TestDomEventViewARebindMidEventRunsNothing(t *testing.T) {
	slots := spaHandlerSlots{
		"r.0.0":   {"click": "Navigate Post"}, // the card
		"r.0":     nil,                        // the list column: no handler
		"r.0.0.1": nil,                        // the card's inner text
	}
	// Nodes are identities; a string stands for a DOM node.
	nodes := map[string]string{"span": "r.0.0.1", "card": "r.0.0", "column": "r.0"}
	idOf := func(n string) (string, bool) { id, ok := nodes[n]; return id, ok }
	eq := func(a, b string) bool { return a == b }
	views := newDomEventViews[string](16)

	tok := views.open([]string{"span", "card", "column", "document"}, idOf, slots)

	// The card's listener runs first: its recorded handler.
	if h, ok, known := views.lookup(tok, "card", eq, "click"); !known || !ok || h != "Navigate Post" {
		t.Fatalf("card: got %v %v %v", h, ok, known)
	}
	// The render renames the column into the Back element and binds a click.
	nodes["column"] = "r.0.back"
	slots["r.0.back"] = map[string]any{"click": "Navigate List"}
	delete(slots, "r.0")
	if _, ok, known := views.lookup(tok, "column", eq, "click"); !known || ok {
		t.Fatalf("column: a listener bound during the event must run nothing (ok=%v known=%v)", ok, known)
	}
}

// Nested handlers: both run, the outer one with the Msg of the clicked view
// even when the inner Msg changed it (Outer 0, not Outer 1).
func TestDomEventViewNestedHandlersUseTheClickedView(t *testing.T) {
	slots := spaHandlerSlots{
		"r.1":   {"click": "Outer 0"},
		"r.1.0": {"click": "Inner"},
	}
	idOf := func(n string) (string, bool) { return n, n != "document" }
	eq := func(a, b string) bool { return a == b }
	views := newDomEventViews[string](16)
	tok := views.open([]string{"r.1.0", "r.1", "document"}, idOf, slots)
	slots.refresh(&VNode{SkyID: "r.1", Events: map[string]any{"click": "Outer 1"}})
	if h, ok, _ := views.lookup(tok, "r.1.0", eq, "click"); !ok || h != "Inner" {
		t.Fatalf("inner: %v %v", h, ok)
	}
	if h, ok, _ := views.lookup(tok, "r.1", eq, "click"); !ok || h != "Outer 0" {
		t.Fatalf("outer: want the clicked view's Outer 0, got %v %v", h, ok)
	}
	if _, ok, _ := views.lookup(tok, "r.1", eq, "input"); ok {
		t.Fatal("a node with no handler for the event type runs nothing")
	}
}

// The views are bounded: the oldest is dropped, and a dropped token reports
// known=false so the caller falls back to the current handler.
func TestDomEventViewsAreBounded(t *testing.T) {
	idOf := func(n string) (string, bool) { return n, true }
	eq := func(a, b string) bool { return a == b }
	views := newDomEventViews[string](2)
	slots := spaHandlerSlots{"a": {"click": 1}}
	first := views.open([]string{"a"}, idOf, slots)
	views.open([]string{"a"}, idOf, slots)
	last := views.open([]string{"a"}, idOf, slots)
	if len(views.views) != 2 {
		t.Fatalf("want 2 views held, got %d", len(views.views))
	}
	if _, _, known := views.lookup(first, "a", eq, "click"); known {
		t.Fatal("the oldest view must be dropped")
	}
	if h, ok, known := views.lookup(last, "a", eq, "click"); !known || !ok || h != 1 {
		t.Fatalf("the newest view: %v %v %v", h, ok, known)
	}
}
