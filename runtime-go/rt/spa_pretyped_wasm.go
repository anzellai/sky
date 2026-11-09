//go:build js

package rt

import "syscall/js"

// The DOM half of spa_pretyped.go: read the controls the user changed before
// the client was live, put them back after the first paint, and send their
// events once the client is live.

// spaPretypedControl is one control the user changed before the first paint.
type spaPretypedControl struct {
	id       string
	kind     string // "text", "check" or "select"
	value    string
	checked  bool
	selected []bool // per <option>, for a select
}

// spaPretyped holds what spaCapturePretyped read, until spaReplayPretyped
// sends it. The client reads it once, at its first paint.
var spaPretyped []spaPretypedControl

// spaCapturePretyped reads every form control under root whose state differs
// from what the server rendered. It runs before the first paint changes the
// DOM. A control with no sky-id cannot be found again and is left out.
func spaCapturePretyped(root js.Value) {
	spaPretyped = nil
	if !root.Truthy() {
		return
	}
	all := root.Call("querySelectorAll", "input[sky-id], textarea[sky-id], select[sky-id]")
	for i := 0; i < all.Length(); i++ {
		el := all.Index(i)
		kind := spaPretypedKind(el.Get("tagName").String(), jsString(el.Get("type")))
		c := spaPretypedControl{id: el.Call("getAttribute", "sky-id").String(), kind: kind}
		switch kind {
		case "text":
			v := jsString(el.Get("value"))
			if v == jsString(el.Get("defaultValue")) {
				continue
			}
			c.value = v
		case "check":
			ch := el.Get("checked").Truthy()
			if ch == el.Get("defaultChecked").Truthy() {
				continue
			}
			c.checked = ch
		case "select":
			opts := el.Get("options")
			states := make([]spaOptState, opts.Length())
			c.selected = make([]bool, opts.Length())
			for j := range states {
				o := opts.Index(j)
				states[j] = spaOptState{
					Selected:        o.Get("selected").Truthy(),
					DefaultSelected: o.Get("defaultSelected").Truthy(),
					Disabled:        o.Get("disabled").Truthy(),
				}
				c.selected[j] = states[j].Selected
			}
			if !spaSelectDirty(el.Get("multiple").Truthy(), states) {
				continue
			}
		default:
			continue
		}
		spaPretyped = append(spaPretyped, c)
	}
}

// spaRestorePretyped puts the captured values back after the first paint, which
// set every control from the model.
func spaRestorePretyped(root js.Value) {
	for _, c := range spaPretyped {
		el := spaPretypedNode(root, c.id)
		if !el.Truthy() {
			continue
		}
		switch c.kind {
		case "text":
			el.Set("value", c.value)
		case "check":
			el.Set("checked", c.checked)
		case "select":
			opts := el.Get("options")
			for j := 0; j < opts.Length() && j < len(c.selected); j++ {
				opts.Index(j).Set("selected", c.selected[j])
			}
		}
	}
}

// spaReplayPretyped gives the app each captured control's change, in document
// order, now that every handler is attached. The app's update then sees the
// input as if the person made it now.
//
// A text field or a select gets its input and change events. A checkbox or a
// radio is clicked, as the person did: Std.Ui binds a check control's Msg to
// `click`, and a real click also fires input and change, so a handler on any
// of the three runs. The control is first set back to its state before the
// person's change, so the click leaves it where they put it. A radio that the
// person unchecked gets nothing: the radio they checked carries the change.
func spaReplayPretyped(root js.Value) {
	list := spaPretyped
	spaPretyped = nil
	ev := js.Global().Get("Event")
	opts := js.ValueOf(map[string]any{"bubbles": true})
	for _, c := range list {
		el := spaPretypedNode(root, c.id)
		if !el.Truthy() {
			continue
		}
		if c.kind == "check" {
			if !c.checked && jsString(el.Get("type")) == "radio" {
				continue
			}
			el.Set("checked", !c.checked)
			el.Call("click")
			continue
		}
		el.Call("dispatchEvent", ev.New("input", opts))
		el.Call("dispatchEvent", ev.New("change", opts))
	}
}

func spaPretypedNode(root js.Value, id string) js.Value {
	if !root.Truthy() {
		return js.Undefined()
	}
	return root.Call("querySelector", `[sky-id="`+escAttr(id)+`"]`)
}

func jsString(v js.Value) string {
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}
