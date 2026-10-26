//go:build js

package rt

import "syscall/js"

// The browser half of the open-<select> defence (spa_select_defer.go holds
// the planner). It tracks whether the focused select's picker can be open,
// and it re-renders once a select that held patches back is closed.

var (
	// spaSettledSelect is the select whose own input or change event fired
	// since it last opened: its picker is closed.
	spaSettledSelect js.Value
	// spaHeldSelect is the select patches were held back for, or a zero
	// js.Value. Its blur or pick re-renders, which re-sends them.
	spaHeldSelect      js.Value
	spaSelectTrackerOn bool
)

// spaInstallSelectTracker binds the document-level listeners once. They run
// in the CAPTURE phase, so they see an event before the select's own Sky
// listener dispatches its Msg and renders.
func spaInstallSelectTracker() {
	if spaSelectTrackerOn {
		return
	}
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return
	}
	spaSelectTrackerOn = true
	isSelect := func(args []js.Value) (js.Value, bool) {
		if len(args) == 0 || !args[0].Truthy() {
			return js.Value{}, false
		}
		t := args[0].Get("target")
		return t, t.Truthy() && tagName(t) == "SELECT"
	}
	// input / change fire after the choice is made: the picker is closed.
	settle := js.FuncOf(func(this js.Value, args []js.Value) any {
		if t, ok := isSelect(args); ok {
			spaSettledSelect = t
			spaCatchUpHeld(t)
		}
		return nil
	})
	// A press or a key on the select can open the picker again; focus
	// arriving on a select starts it unsettled.
	unsettle := js.FuncOf(func(this js.Value, args []js.Value) any {
		if t, ok := isSelect(args); ok && (t.Equal(spaSettledSelect) || args[0].Get("type").String() == "focusin") {
			spaSettledSelect = js.Value{}
		}
		return nil
	})
	// Focus leaving the select closes the picker: re-send what was held.
	leave := js.FuncOf(func(this js.Value, args []js.Value) any {
		if t, ok := isSelect(args); ok {
			if t.Equal(spaSettledSelect) {
				spaSettledSelect = js.Value{}
			}
			spaCatchUpHeld(t)
		}
		return nil
	})
	opts := map[string]any{"capture": true}
	for _, e := range []string{"input", "change"} {
		doc.Call("addEventListener", e, settle, opts)
	}
	for _, e := range []string{"pointerdown", "mousedown", "keydown", "focusin"} {
		doc.Call("addEventListener", e, unsettle, opts)
	}
	doc.Call("addEventListener", "focusout", leave, opts)
}

// spaCurrentSelect reports the focused select for the planner.
func spaCurrentSelect() spaSelectState {
	active := js.Global().Get("document").Get("activeElement")
	if !active.Truthy() || tagName(active) != "SELECT" {
		return spaSelectState{}
	}
	id := ""
	if n := active.Call("closest", "[sky-id]"); n.Truthy() {
		if s := n.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
			id = s.String()
		}
	}
	if id == "" {
		// A select outside every sky-id element is outside the tree: no
		// patch can touch it.
		return spaSelectState{}
	}
	return spaSelectState{ID: id, Settled: active.Equal(spaSettledSelect)}
}

// spaNoteHeld records that a render held patches back for the focused select,
// and says so on the console once per hold.
func spaNoteHeld(plan spaRenderPlan) {
	if plan.Held == 0 {
		return
	}
	active := js.Global().Get("document").Get("activeElement")
	if spaHeldSelect.Truthy() && spaHeldSelect.Equal(active) {
		return
	}
	spaHeldSelect = active
	if c := js.Global().Get("console"); c.Truthy() {
		c.Call("warn", "[sky.spa] patches deferred while a <select> is open; they apply when it closes:",
			plan.Held, "patch(es) under", js.ValueOf(plan.Regions[0]))
	}
}

// spaCatchUpHeld re-renders after sel closes when patches were held for it.
// It runs on the next task, after the event's own listeners (a pick
// dispatches and renders first, and a blur has moved activeElement).
func spaCatchUpHeld(sel js.Value) {
	if !spaHeldSelect.Truthy() || !spaHeldSelect.Equal(sel) {
		return
	}
	spaHeldSelect = js.Value{}
	var f js.Func
	f = js.FuncOf(func(this js.Value, args []js.Value) any {
		f.Release()
		if spaPrev != nil {
			spaRenderGuard(spaModel, renderCurrent, nil, func(_ string, r any) { spaReportPanic("render", r) })
		}
		return nil
	})
	js.Global().Call("setTimeout", f, 0)
}
