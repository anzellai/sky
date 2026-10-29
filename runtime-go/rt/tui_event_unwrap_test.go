package rt

import (
	"testing"
)

// Regression fence for the v0.13.3 → v0.13.4 TUI event-emission bug.
//
// The bug: walkAttrs's tag-11 (AttrEvent) handler appended
// adt.Fields[0] verbatim to the focusable's events list, expecting
// a Go-side `eventPair{name, msg}` struct directly. That matched
// the OLD kernel-emitted shape (Std.Html.Events was a Go runtime
// kernel pre-Layer-3). v0.13 Layer 3 rewrote Std.Html.Events to
// Sky source, so AttrEvent's Fields[0] is now a Sky `EventAttr
// (OnMsg "click" msg)` SkyADT — NOT an eventPair. The downstream
// focusableEvent type-assertion `ev.(eventPair)` silently rejects
// the SkyADT shape and returns nil — so EVERY key press in a TUI
// app using Std.Ui inputs / buttons drops silently.
//
// Symptom in user-facing TUI repro (a downstream app, --tui): typing in
// any input field, pressing Enter on any button, clicking any
// link — all no-ops. The TUI renders correctly but is dead to
// keyboard / mouse events.
//
// Fix: unwrap the Sky EventAttr SkyADT in walkAttrs's tag-11 case
// before appending. Construct an eventPair from
// (EventAttr.Fields[0]=name, EventAttr.Fields[1]=msg). The legacy
// eventPair-direct shape is still accepted (for any callers that
// continue to emit it pre-Layer-3-style).

func TestTui_AttrEvent_UnwrapsSkyADT(t *testing.T) {
	// Build the v0.13 Sky-source shape directly:
	//   AttrEvent (EventAttr (OnMsg "click" "Increment"))
	innerEvent := SkyADT{
		SkyName: "OnMsg",
		Fields:  []any{"click", "Increment"},
	}
	eventAttr := SkyADT{
		SkyName: "EventAttr",
		Fields:  []any{innerEvent},
	}
	attrEvent := SkyADT{
		Tag:     11,
		SkyName: "AttrEvent",
		Fields:  []any{eventAttr},
	}

	// Run the attr walker on a node with this one event attr.
	wa := walkAttrs([]any{attrEvent}, tuiLayoutCtx{cols: 80, rows: 24})

	if len(wa.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(wa.events))
	}
	ep, ok := wa.events[0].(eventPair)
	if !ok {
		t.Fatalf("event 0 is not eventPair (raw Sky.ADT leaked through): %T %+v",
			wa.events[0], wa.events[0])
	}
	if ep.name != "click" {
		t.Fatalf("event name: got %q want %q", ep.name, "click")
	}
	if ep.msg != "Increment" {
		t.Fatalf("event msg: got %+v want %q", ep.msg, "Increment")
	}
}

func TestTui_AttrEvent_AcceptsLegacyEventPair(t *testing.T) {
	// Legacy shape: AttrEvent with a Go eventPair directly in
	// Fields[0]. Should still work — kept for any caller that
	// emits the old shape.
	ep := eventPair{name: "input", msg: "UpdateName"}
	attrEvent := SkyADT{
		Tag:     11,
		SkyName: "AttrEvent",
		Fields:  []any{ep},
	}

	wa := walkAttrs([]any{attrEvent}, tuiLayoutCtx{cols: 80, rows: 24})

	if len(wa.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(wa.events))
	}
	got, ok := wa.events[0].(eventPair)
	if !ok {
		t.Fatalf("event 0 not eventPair: %T", wa.events[0])
	}
	if got.name != "input" || got.msg != "UpdateName" {
		t.Fatalf("event mismatch: %+v", got)
	}
}
