//go:build js

package rt

import "syscall/js"

// dom_render_wasm.go — the Sky.Spa per-platform DOM renderer: interpret a
// VNode tree into real browser DOM over syscall/js. Ported from the spike's
// toDOM/renderInto (docs/skyspa/spike/main.go) but over the runtime's own
// VNode (Kind/Tag/Text/Attrs/Events/Children) instead of the spike's Element.
//
// Full re-render per dispatch: the spike proved this is correct; client-side
// diffing (reusing diffTrees/__skyApplyPatches) is a later renderer
// optimisation, not an architecture change (design §5.1).

// spaDispatch is the single event entry point; live_wasm.go installs it before
// the first render so event closures built here can call back into the loop.
var spaDispatch func(msg any)

// spaLiveFns holds the event closures for the currently-mounted tree so they
// can be released before the next render (js.Func leaks if not Released).
var spaLiveFns []js.Func

// renderVNodeToDOM replaces mount's content with the DOM for el.
func renderVNodeToDOM(mount js.Value, el VNode) {
	for _, f := range spaLiveFns {
		f.Release()
	}
	spaLiveFns = spaLiveFns[:0]
	mount.Set("innerHTML", "")
	mount.Call("appendChild", vnodeToDOM(el))
}

func vnodeToDOM(el VNode) js.Value {
	doc := js.Global().Get("document")
	switch el.Kind {
	case "text":
		return doc.Call("createTextNode", el.Text)
	case "raw":
		// Raw HTML: wrap in a <span> and set innerHTML. Std.Html.raw is the
		// only source and it is author-controlled markup.
		span := doc.Call("createElement", "span")
		span.Set("innerHTML", el.Text)
		return span
	default: // "element"
		n := doc.Call("createElement", el.Tag)
		for k, v := range el.Attrs {
			n.Call("setAttribute", k, v)
		}
		for evt, handler := range el.Events {
			h := handler // capture per listener
			f := js.FuncOf(func(this js.Value, args []js.Value) any {
				var payload string
				if len(args) > 0 {
					// For onInput-style handlers, hand the target's value in.
					target := args[0].Get("target")
					if target.Truthy() {
						if v := target.Get("value"); v.Type() == js.TypeString {
							payload = v.String()
						}
					}
				}
				dispatchEvent(h, payload)
				return nil
			})
			spaLiveFns = append(spaLiveFns, f)
			n.Call("addEventListener", evt, f)
		}
		for i := range el.Children {
			n.Call("appendChild", vnodeToDOM(el.Children[i]))
		}
		return n
	}
}

// dispatchEvent turns an event handler value into a Msg and dispatches it.
// A plain Msg value (onClick Increment) is dispatched as-is; a handler
// function (onInput toMsg) is applied to the event payload string first.
func dispatchEvent(handler any, payload string) {
	if spaDispatch == nil {
		return
	}
	if isFunc(handler) {
		spaDispatch(sky_call(handler, payload))
		return
	}
	spaDispatch(handler)
}
