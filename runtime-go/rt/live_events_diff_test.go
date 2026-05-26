package rt

// v0.15.14 — diffNodes must diff VNode.Events alongside VNode.Attrs.
//
// VNode stores DOM event handlers in a separate Events map but
// renderVNode emits them as `sky-<event>` + `data-sky-hid`
// attributes in the produced HTML. The pre-fix diffNodes only
// compared Attrs, which meant an element gaining or losing an
// event handler (without any other attr change) produced no patch
// for those rendered attributes. The previously-bound JS listener
// stayed attached, but the runtime's per-event Msg lookup via
// `target.getAttribute("sky-<event>")` returned null, so user
// gestures were silently dropped.
//
// This file pins three properties:
//
//   1. New event added → diff emits `sky-<event>` and `data-sky-hid`.
//   2. Event removed → diff emits `sky-<event>=""` (and clears
//      `data-sky-hid` when no events remain).
//   3. Event's Msg constructor changed (same key, different value)
//      → diff emits an updated `sky-<event>` value.

import (
	"testing"
)

// findAttrPatch returns the first Patch in `patches` whose ID
// matches `id` and which carries Attrs (no HTML, no Text).
func findAttrPatch(patches []Patch, id string) *Patch {
	for i := range patches {
		if patches[i].ID == id && patches[i].Attrs != nil {
			return &patches[i]
		}
	}
	return nil
}

func TestDiffNodes_EventAdded_EmitsAttrPatch(t *testing.T) {
	// Two VNodes with the same attrs but different events:
	// old has no keydown handler; new has one.
	old := VNode{
		Kind:   "element",
		Tag:    "div",
		SkyID:  "r.0#div",
		Attrs:  map[string]string{"class": "canvas-wrap"},
		Events: map[string]any{},
	}
	new_ := VNode{
		Kind:   "element",
		Tag:    "div",
		SkyID:  "r.0#div",
		Attrs:  map[string]string{"class": "canvas-wrap"},
		Events: map[string]any{"keydown": SkyADT{SkyName: "KeyDown"}},
	}

	patches := diffTrees(&old, &new_, nil)
	p := findAttrPatch(patches, "r.0#div")
	if p == nil {
		t.Fatalf("expected attr patch on r.0#div, got: %+v", patches)
	}
	if p.Attrs["sky-keydown"] != "KeyDown" {
		t.Fatalf("expected sky-keydown=KeyDown, got: %v", p.Attrs)
	}
	if p.Attrs["data-sky-hid"] != "r.0#div.keydown" {
		t.Fatalf("expected data-sky-hid=r.0#div.keydown, got: %v", p.Attrs)
	}
}

func TestDiffNodes_EventRemoved_EmitsClear(t *testing.T) {
	old := VNode{
		Kind:   "element",
		Tag:    "div",
		SkyID:  "r.0#div",
		Attrs:  map[string]string{"class": "canvas-wrap"},
		Events: map[string]any{"keydown": SkyADT{SkyName: "KeyDown"}},
	}
	new_ := VNode{
		Kind:   "element",
		Tag:    "div",
		SkyID:  "r.0#div",
		Attrs:  map[string]string{"class": "canvas-wrap"},
		Events: map[string]any{},
	}

	patches := diffTrees(&old, &new_, nil)
	p := findAttrPatch(patches, "r.0#div")
	if p == nil {
		t.Fatalf("expected attr patch on r.0#div, got: %+v", patches)
	}
	if v, ok := p.Attrs["sky-keydown"]; !ok || v != "" {
		t.Fatalf("expected sky-keydown=\"\" (clear), got: %v", p.Attrs)
	}
	if v, ok := p.Attrs["data-sky-hid"]; !ok || v != "" {
		t.Fatalf("expected data-sky-hid=\"\" (clear) when no events remain, got: %v", p.Attrs)
	}
}

func TestDiffNodes_EventMsgChanged_EmitsUpdate(t *testing.T) {
	old := VNode{
		Kind:   "element",
		Tag:    "button",
		SkyID:  "r.0#button",
		Attrs:  map[string]string{},
		Events: map[string]any{"click": SkyADT{SkyName: "Save"}},
	}
	new_ := VNode{
		Kind:   "element",
		Tag:    "button",
		SkyID:  "r.0#button",
		Attrs:  map[string]string{},
		Events: map[string]any{"click": SkyADT{SkyName: "Cancel"}},
	}

	patches := diffTrees(&old, &new_, nil)
	p := findAttrPatch(patches, "r.0#button")
	if p == nil {
		t.Fatalf("expected attr patch on r.0#button, got: %+v", patches)
	}
	if p.Attrs["sky-click"] != "Cancel" {
		t.Fatalf("expected sky-click=Cancel, got: %v", p.Attrs)
	}
	if p.Attrs["data-sky-hid"] != "r.0#button.click" {
		t.Fatalf("expected data-sky-hid=r.0#button.click, got: %v", p.Attrs)
	}
}

func TestDiffNodes_EventUnchanged_NoPatch(t *testing.T) {
	old := VNode{
		Kind:   "element",
		Tag:    "button",
		SkyID:  "r.0#button",
		Attrs:  map[string]string{"class": "primary"},
		Events: map[string]any{"click": SkyADT{SkyName: "Save"}},
	}
	new_ := VNode{
		Kind:   "element",
		Tag:    "button",
		SkyID:  "r.0#button",
		Attrs:  map[string]string{"class": "primary"},
		Events: map[string]any{"click": SkyADT{SkyName: "Save"}},
	}

	patches := diffTrees(&old, &new_, nil)
	if findAttrPatch(patches, "r.0#button") != nil {
		t.Fatalf("expected no attr patch for unchanged events, got: %+v", patches)
	}
}
