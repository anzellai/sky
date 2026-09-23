package rt

import (
	"reflect"
	"testing"
)

// Regression (found 2026-09-23 in a real Sky.Spa app): a re-rendered button kept
// the message payload bound at its FIRST render. The shared diff compares event
// handlers by constructor name, so rows re-rendered with identical text but a
// different id payload (`Pick "a1"` -> `Pick "b2"`) produced no patch; the wasm
// client's listener had captured `Pick "a1"` and kept dispatching it. The client
// now reads the handler from spaHandlerSlots at dispatch time and refreshes the
// slots from each new tree. The browser-level proof is
// scripts/spa-stale-handler-e2e.sh; this pins the pure-Go half.

func pickMsg(id string) SkyADT { return SkyADT{Tag: 1, SkyName: "Pick", Fields: []any{id}} }

func buttonTree(id string) VNode {
	root := VNode{Kind: "element", Tag: "div", Children: []VNode{
		{Kind: "element", Tag: "button", Events: map[string]any{"click": pickMsg(id)},
			Children: []VNode{vtext("Edit")}},
	}}
	assignSkyIDs(&root, "r")
	return root
}

// The mechanism, pinned: a payload-only handler change emits NO patch. That is
// correct for Sky.Live and the desktop webview (they resolve the handler from the
// current render on the server), and it is why the wasm client needs the slots.
func TestDiffEmitsNoPatchForPayloadOnlyHandlerChange(t *testing.T) {
	oldT, newT := buttonTree("a1"), buttonTree("b2")
	if p := diffTrees(&oldT, &newT, nil); len(p) != 0 {
		t.Fatalf("expected no patch for a payload-only handler change, got %#v", p)
	}
}

func TestSpaHandlerSlotsFollowTheCurrentTree(t *testing.T) {
	oldT, newT := buttonTree("a1"), buttonTree("b2")
	btnID := oldT.Children[0].SkyID
	if btnID == "" || btnID != newT.Children[0].SkyID {
		t.Fatalf("button sky-id must be stable across renders, got %q / %q", btnID, newT.Children[0].SkyID)
	}

	s := spaHandlerSlots{}
	s.refresh(&oldT) // first render: what bindNodeEvents records
	captured := oldT.Children[0].Events["click"]

	if got := s.lookup(btnID, "click", captured); !reflect.DeepEqual(got, pickMsg("a1")) {
		t.Fatalf("before the re-render: want Pick a1, got %#v", got)
	}

	// Re-render with the same text, different payload; no patch is applied
	// (see the test above), only the post-patch refresh runs.
	s.refresh(&newT)
	if got := s.lookup(btnID, "click", captured); !reflect.DeepEqual(got, pickMsg("b2")) {
		t.Fatalf("after the re-render the listener must dispatch Pick b2 (the CURRENT payload), got %#v", got)
	}
}

func TestSpaHandlerSlotsDropAndFallback(t *testing.T) {
	s := spaHandlerSlots{}
	s.set("r.0", map[string]any{"click": pickMsg("x")})
	s.drop("r.0")
	if got := s.lookup("r.0", "click", "fallback"); got != "fallback" {
		t.Fatalf("a dropped slot must fall back, got %#v", got)
	}
	// An element whose handlers were all removed clears its slot.
	s.set("r.1", map[string]any{"click": pickMsg("y")})
	s.set("r.1", nil)
	if _, ok := s["r.1"]; ok {
		t.Fatal("setting nil events must clear the slot")
	}
	// No sky-id: never recorded, lookup falls back.
	s.set("", map[string]any{"click": pickMsg("z")})
	if got := s.lookup("", "click", "fallback"); got != "fallback" {
		t.Fatalf("an element without a sky-id must fall back, got %#v", got)
	}
}
