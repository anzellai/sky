//go:build js

package rt

import (
	"strconv"
	"strings"
	"syscall/js"
)

// dom_render_wasm.go — the Sky.Spa per-platform DOM renderer.
//
// Two paths:
//
//   - spaMount: the FIRST render builds the whole tree into real DOM nodes
//     (buildDOM), attaching event listeners per node. Ported from the spike's
//     toDOM/renderInto but over the runtime's VNode.
//
//   - spaApplyPatches: EVERY subsequent render diffs the previous VNode tree
//     against the new one (diffTrees, shared with Sky.Live's server renderer)
//     and applies the resulting []Patch to the live DOM by sky-id — NOT a full
//     rebuild. This keeps DOM node identity stable, so a focused text input
//     keeps its focus, its caret/selection, and its uncommitted value while the
//     user is typing. The focus/cursor/dirty-input AUTHORITY logic is ported
//     from live.go's battle-tested __skyApplyPatches (which applies HTML-string
//     patches in JS); here we apply the Patch VALUE model in Go over syscall/js.

// spaDispatch is the single event entry point; live_wasm.go installs it before
// the first render so event closures built here can call back into the loop.
var spaDispatch func(msg any)

// spaNodeFns maps a DOM element's sky-id to the listeners bound on it. A
// rebuild, removal, or handler change releases them before dropping the node:
// an unreleased js.Func leaks its Go closure for the life of the process.
//
// Each entry keeps the NODE and the event it was added for, because releasing a
// js.Func does not detach it. A handler change rebinds a KEPT node in place
// (applyAttrs), and the old listener used to stay attached as a released
// function: every later click logged "call to released function" (W1). A
// release now removes the listener from its node first, and only the listeners
// of the node being released are touched, never those of another node that
// carries the same sky-id for the moment.
var spaNodeFns = map[string][]spaListener{}

// spaListener is one event listener the client added to a DOM node.
type spaListener struct {
	node js.Value
	evt  string
	fn   js.Func
}

// spaListen adds f as n's listener for evt and records it under id.
func spaListen(n js.Value, id, evt string, f js.Func) {
	n.Call("addEventListener", evt, f)
	spaNodeFns[id] = append(spaNodeFns[id], spaListener{node: n, evt: evt, fn: f})
}

// spaNodeHandlers holds each element's CURRENT handlers (see spa_handlers.go). A
// listener reads its message from here when it fires rather than capturing it at
// bind time, and renderCurrent refreshes the table after every patch, so a
// payload-only change (`Pick "a1"` -> `Pick "b2"`, which the shared diff does not
// patch) still dispatches the new payload.
var spaNodeHandlers = spaHandlerSlots{}

// spaMount replaces mount's content with a freshly-built DOM tree for root and
// records the event listeners it attaches. Used for the initial render only.
func spaMount(mount js.Value, root VNode) {
	for id := range spaNodeFns {
		releaseNodeFns(id, js.Value{})
	}
	mount.Set("innerHTML", "")
	mount.Call("appendChild", buildDOM(root))
}

// spaShouldHydrate decides whether the DOM already under `mount` was
// server-rendered (SSR, design §4.4) and can be HYDRATED in place, or whether
// the client must fall back to today's spaMount wipe-and-rebuild. Three gates,
// all required:
//
//   - the mount carries the `data-sky-ssr` marker the SSR backend stamps (so a
//     non-SSR / static-shell deploy still boots via spaMount, unchanged);
//   - the mount actually has a server-painted element child (an empty `#app`
//     from a stale/failed render is rebuilt, not hydrated onto nothing);
//   - the freshly-computed VNode tree passes the POSITIVE structural parity
//     check (spa_ssr.go). A `sky-id`-presence check alone is NOT fail-safe: raw
//     nodes, adjacent text, and valued <textarea> line up by sky-id yet diverge
//     structurally from the server DOM and corrupt on the first diff. When the
//     tree is not provably hydratable we rebuild (correct today's behaviour)
//     rather than adopt a mismatched tree.
func spaShouldHydrate(mount js.Value, root VNode) bool {
	if !mount.Truthy() {
		return false
	}
	if !mount.Call("getAttribute", spaSSRMarker).Truthy() {
		return false
	}
	if !mount.Get("firstElementChild").Truthy() {
		return false
	}
	ok, reason := spaHydratableVNode(root)
	if ok {
		ok, reason = spaHydrationParity(mount.Get("firstElementChild"), &root)
	}
	if !ok {
		if c := js.Global().Get("console"); c.Truthy() {
			c.Call("warn", "[sky.spa] SSR hydrate skipped, full rebuild:", reason)
		}
		return false
	}
	return true
}

// spaHydrationParity checks that the server-painted DOM SHOWS what the
// client's first tree says — tags, sky-ids, the tree's attributes and every
// text node — before hydration adopts it (SPA-5). Hydration only binds
// listeners; it writes no text. So when the server and the client computed a
// different first view (a route param the server decoded and the client did
// not, a model field only one side had), the page kept showing the server's
// text while the client's model and every later diff assumed its own. A
// mismatch rebuilds from the client tree instead, which is always correct.
//
// Extra DOM attributes are allowed (the server stamps sky-<event> and
// data-sky-hid, which the client does not model). A child list holding raw
// HTML is not compared node by node (a raw string parses to any number of
// nodes).
func spaHydrationParity(node js.Value, v *VNode) (bool, string) {
	if !node.Truthy() || node.Get("nodeType").Int() != 1 {
		return false, "server DOM has no element for " + v.SkyID
	}
	if strings.ToLower(tagName(node)) != v.Tag {
		return false, "tag differs at " + v.SkyID + ": server <" + strings.ToLower(tagName(node)) + ">, client <" + v.Tag + ">"
	}
	if v.SkyID != "" {
		if s := node.Call("getAttribute", "sky-id"); s.Type() != js.TypeString || s.String() != v.SkyID {
			return false, "sky-id differs at " + v.SkyID
		}
	}
	for k, want := range v.Attrs {
		if k == "value" && (v.Tag == "select" || v.Tag == "textarea") {
			continue
		}
		if got := node.Call("getAttribute", k); got.Type() != js.TypeString || got.String() != want {
			return false, "attribute " + k + " differs at " + v.SkyID
		}
	}
	for i := range v.Children {
		if v.Children[i].Kind == "raw" {
			return true, ""
		}
	}
	dom := node.Get("firstChild")
	for i := range v.Children {
		c := &v.Children[i]
		if c.Kind == "text" && c.Text == "" {
			continue
		}
		if !dom.Truthy() {
			return false, "server DOM has fewer children at " + v.SkyID
		}
		switch c.Kind {
		case "text":
			if dom.Get("nodeType").Int() != 3 {
				return false, "text expected at " + v.SkyID
			}
			if d := dom.Get("data"); d.Type() != js.TypeString || d.String() != c.Text {
				return false, "text differs at " + v.SkyID
			}
		default:
			if ok, why := spaHydrationParity(dom, c); !ok {
				return false, why
			}
		}
		dom = dom.Get("nextSibling")
	}
	if dom.Truthy() {
		return false, "server DOM has more children at " + v.SkyID
	}
	return true, ""
}

// spaHydrate attaches the client's real event closures (and input-prop
// reflections) to the EXISTING server-rendered DOM nodes — matched by the
// `sky-id` both sides compute identically — WITHOUT any createElement /
// innerHTML="" / appendChild. It is the non-destructive replacement for spaMount
// on an SSR first paint (design §4.4). The server's inert sky-<event> /
// data-sky-hid attributes are left in place: the client neither reads nor
// manages them (it dispatches from the in-memory VNode.Events map bound here),
// so they are harmless bytes a later pass may strip. renderCurrent sets spaPrev
// to this tree afterwards, so every subsequent render takes the diff path.
func spaHydrate(mount js.Value, root VNode) {
	hydrateVNode(mount, root)
}

// hydrateVNode walks the VNode tree, binding events + reflecting input props onto
// the matching existing DOM node. Text/raw nodes carry no sky-id (assignSkyIDs
// skips non-elements) so they bind nothing; spaShouldHydrate has already proven
// the child structure matches the server DOM, so positional text is aligned.
func hydrateVNode(scope js.Value, el VNode) {
	if el.SkyID != "" {
		node := scope.Call("querySelector", `[sky-id="`+escAttr(el.SkyID)+`"]`)
		if node.Truthy() {
			bindNodeEvents(node, el)
			for k, v := range el.Attrs {
				reflectInputProp(node, k, v)
			}
		}
	}
	for i := range el.Children {
		hydrateVNode(scope, el.Children[i])
	}
}

// spaInjectBaseCSS appends a <style id="sky-base-reset"> carrying liveBaseCSS
// (the shared server/client reset) into the document <head>, once. Idempotent —
// a page that already ships the reset (or a hot reload) is a no-op.
func spaInjectBaseCSS(doc js.Value) {
	head := doc.Get("head")
	if !head.Truthy() {
		return
	}
	if doc.Call("getElementById", "sky-base-reset").Truthy() {
		return
	}
	style := doc.Call("createElement", "style")
	style.Call("setAttribute", "id", "sky-base-reset")
	style.Set("textContent", liveBaseCSS)
	head.Call("appendChild", style)
}

// buildDOM interprets a VNode into a real DOM node, attaching real event
// listeners (recorded under the node's sky-id for later release) and reflecting
// user-facing input properties (.value/.checked/.disabled) so the live DOM
// tracks the model from the first mount.
func buildDOM(el VNode) js.Value {
	doc := js.Global().Get("document")
	switch el.Kind {
	case "text":
		return doc.Call("createTextNode", el.Text)
	case "raw":
		// A raw node's content is its PARENT's innerHTML (Std.Html.raw), exactly
		// as SSR serialises it inline (live_core.go renderVNodeInto). It is never
		// a standalone DOM node, so buildDOM is not called on a raw child — the
		// element case below routes children through spaSetChildren, which
		// innerHTMLs a raw-containing child list verbatim. This case is reached
		// only if a raw node is a render ROOT (no parent to inject into); we then
		// isolate it in a <span> as a last resort. Wrapping a raw CHILD in a
		// <span> was the hydration bug: a <style>'s CSS wrapped in a <span> is
		// never applied, so a server-rendered stylesheet vanished on first paint.
		span := doc.Call("createElement", "span")
		span.Set("innerHTML", el.Text)
		return span
	default: // "element"
		n := doc.Call("createElement", el.Tag)
		// Stamp the sky-id so diff patches can address this node by
		// querySelector('[sky-id="..."]') — the same addressing the server diff
		// uses. Without it the applier below could not find its targets.
		if el.SkyID != "" {
			n.Call("setAttribute", "sky-id", el.SkyID)
		}
		for k, v := range el.Attrs {
			n.Call("setAttribute", k, v)
			// A <select>'s value can only be reflected once its options
			// exist; before that `.value = v` is a no-op and the first
			// option wins the first paint (F5). It is reflected below,
			// after spaSetChildren.
			if el.Tag == "select" && k == "value" {
				continue
			}
			reflectInputProp(n, k, v)
		}
		// A submit-handled <form> with no explicit method gets method="post", so a
		// native submit in the JS-off / pre-hydration window is a POST, not a GET
		// that leaks fields into the URL. Mirrors the SSR path (live_core.go
		// renderVNodeInto); the client also preventDefaults the submit once
		// hydrated (bindNodeEvents), so this is the belt to that braces.
		if el.Tag == "form" {
			if _, hasSubmit := el.Events["submit"]; hasSubmit {
				if _, hasMethod := el.Attrs["method"]; !hasMethod {
					n.Call("setAttribute", "method", "post")
				}
			}
		}
		bindNodeEvents(n, el)
		spaSetChildren(n, el.Children)
		spaSyncSelectValue(n, &el)
		return n
	}
}

// spaSyncSelectValue sets a <select>'s .value from its VNode once the
// options are in place. The options already carry `selected` for the
// model's value (markSelectedOptions), so this is the belt to those
// braces — and the only thing that moves the selection when the options
// were rebuilt under a select the user had already touched.
func spaSyncSelectValue(n js.Value, el *VNode) {
	if el == nil || el.Tag != "select" {
		return
	}
	if v, ok := el.Attrs["value"]; ok && v != "" {
		if cur := n.Get("value"); cur.Type() != js.TypeString || cur.String() != v {
			n.Set("value", v)
		}
	}
}

// spaChildrenContainRaw reports whether any direct child is a raw-HTML node.
func spaChildrenContainRaw(children []VNode) bool {
	for i := range children {
		if children[i].Kind == "raw" {
			return true
		}
	}
	return false
}

// spaSetChildren populates parent's children from a VNode list.
//
// The default path builds a real DOM node per child (createElement + real event
// listeners), preserving node identity for focus/caret handling.
//
// When ANY child is a raw-HTML node, that per-child path is wrong: a raw node's
// content is the PARENT's innerHTML (Std.Html.raw — the trusted-raw-HTML escape
// hatch), which cannot be modelled as a standalone child DOM node. So the
// raw-present case serialises the ENTIRE child list with the SSR renderer
// (renderChildrenHTML) and assigns parent.innerHTML once — byte-identical to
// what SSR produced and parsed in the parent's own element context (so a
// <style>'s CSS becomes the style element's text and is applied, not wrapped in
// a spurious <span>). It then walks the children binding real event listeners +
// reflecting input props onto the freshly-parsed descendant elements by sky-id
// (hydrateVNode), so interactive siblings of a raw node stay wired. Raw content
// itself carries no sky-id and is left verbatim.
func spaSetChildren(parent js.Value, children []VNode) {
	if spaChildrenContainRaw(children) {
		parent.Set("innerHTML", renderChildrenHTML(children))
		for i := range children {
			hydrateVNode(parent, children[i])
		}
		return
	}
	for i := range children {
		parent.Call("appendChild", buildDOM(children[i]))
	}
}

// reflectInputProp mirrors the value/checked/disabled ATTRIBUTES onto the live
// DOM PROPERTIES. setAttribute("value", …) only sets the default value; the
// live .value property is what the user sees and edits.
func reflectInputProp(n js.Value, k, v string) {
	switch k {
	case "value":
		n.Set("value", v)
	case "checked":
		n.Set("checked", boolAttr(v))
	case "selected":
		n.Set("selected", boolAttr(v))
	case "disabled":
		n.Set("disabled", boolAttr(v))
	}
}

func boolAttr(v string) bool { return v != "" && v != "false" }

// bindNodeEvents attaches a real listener per DOM event on el.Events and records
// the js.Funcs under el.SkyID. `sky-`-prefixed meta events (onImage/onFile) are
// side-channel data attributes, not DOM events, and are skipped.
func bindNodeEvents(n js.Value, el VNode) {
	spaNodeHandlers.set(el.SkyID, el.Events)
	bound := el.SkyID
	// The listener resolves its node's sky-id when it FIRES, not when it is
	// bound: a kept node can be renamed by a children-reconcile patch (see
	// KidOp), and a listener holding the old id would look up the old
	// handler slot and dispatch the old message.
	idOf := func(this js.Value) string {
		if this.Type() == js.TypeObject {
			if s := this.Call("getAttribute", "sky-id"); s.Type() == js.TypeString && s.String() != "" {
				return s.String()
			}
		}
		return bound
	}
	for evt, handler := range el.Events {
		// File / image inputs. `Ui.onFile` / `Ui.onImage` lower to the meta
		// events "sky-file" / "sky-image" (Std/Html/Events.sky). Sky.Live wires
		// these in its client JS (and resizes an image via a canvas); the Sky.Spa
		// wasm client had NO handler, so an admin upload never reached the app
		// under --target web:app. Wire a change listener that reads the selected
		// file as a data URL and dispatches the handler with it. The file is
		// delivered RAW (no client-side resize) — the backend Std.Image resizes,
		// so the wasm binary carries no image codec and the resize is server-side.
		if evt == "sky-image" || evt == "sky-file" {
			h := handler
			ek := evt
			node := n
			f := js.FuncOf(func(this js.Value, args []js.Value) any {
				spaReadFileAndDispatch(node, spaNodeHandlers.lookup(idOf(this), ek, h))
				return nil
			})
			spaListen(n, el.SkyID, "change", f)
			continue
		}
		// Synthetic "enter" event (Ui.onEnter). The DOM has no "enter" event, so
		// bind keydown and fire only on a plain Enter (no Shift), then
		// preventDefault so a <textarea> does not also insert a newline for the
		// sending keystroke. Shift-Enter is left untouched, so it inserts a
		// newline as normal. Sky.Live does the same in its client JS. The handler
		// is a bare Msg (no payload), so dispatch it with an empty string. An
		// Enter that CONFIRMS an IME composition is not a send.
		if evt == "enter" {
			h := handler
			f := js.FuncOf(func(this js.Value, args []js.Value) any {
				if len(args) > 0 && args[0].Truthy() {
					ev := args[0]
					if ev.Get("key").String() != "Enter" || ev.Get("shiftKey").Truthy() || ev.Get("isComposing").Truthy() {
						return nil
					}
					ev.Call("preventDefault")
				}
				dispatchEvent(spaNodeHandlers.lookup(idOf(this), "enter", h), "")
				return nil
			})
			spaListen(n, el.SkyID, "keydown", f)
			continue
		}
		if strings.HasPrefix(evt, "sky-") {
			continue
		}
		h := handler // fallback only; the live handler is read at dispatch time
		e := evt
		f := js.FuncOf(func(this js.Value, args []js.Value) any {
			cur := spaNodeHandlers.lookup(idOf(this), e, h)
			// A form submit is the one event that carries structured data (the
			// field values), and it MUST preventDefault or the browser does a
			// native submit — which, on a form with no method, is a GET that
			// leaks the fields (a password) into the URL. Sky.Live intercepts
			// this in its client JS; the Sky.Spa wasm client must do the same.
			// The handler is `\p -> SomeMsg p` (onSubmit takes the form record),
			// so hand it the field map — update's rt.Coerce narrows it to the
			// declared record type (Credentials, a ProductForm, …), exactly as
			// the Live path narrows form data to the record.
			if e == "submit" {
				if len(args) > 0 && args[0].Truthy() {
					args[0].Call("preventDefault")
					dispatchSubmit(cur, spaFormData(args[0].Get("target")))
					return nil
				}
			}
			// IME (UF-11): while a composition is open the input events carry
			// the PRE-EDIT text (romaji, a half-built syllable). Dispatching them
			// ran update on text the user never committed and re-rendered the
			// field under the IME. Wait for compositionend (bound below), which
			// dispatches the committed text once.
			if e == "input" && len(args) > 0 && args[0].Truthy() {
				if args[0].Get("isComposing").Truthy() || this.Get("__skyComposing").Truthy() {
					return nil
				}
				// Firefox fires one more input AFTER compositionend with the
				// committed value: that one was already dispatched.
				if done := this.Get("__skyComposed"); done.Type() == js.TypeString {
					this.Set("__skyComposed", js.Undefined())
					if v := this.Get("value"); v.Type() == js.TypeString && v.String() == done.String() {
						return nil
					}
				}
			}
			dispatchEvent(cur, eventPayload(e, args))
			return nil
		})
		spaListen(n, el.SkyID, evt, f)
		if evt == "input" {
			spaBindComposition(n, el.SkyID, h, idOf)
		}
	}
}

// spaBindComposition wires compositionstart / compositionend on a node with
// an input handler: the start marks the node composing (so input events and
// value patches leave it alone), the end dispatches the committed text once.
func spaBindComposition(n js.Value, id string, h any, idOf func(js.Value) string) {
	start := js.FuncOf(func(this js.Value, args []js.Value) any {
		this.Set("__skyComposing", true)
		return nil
	})
	end := js.FuncOf(func(this js.Value, args []js.Value) any {
		this.Set("__skyComposing", false)
		val := ""
		if v := this.Get("value"); v.Type() == js.TypeString {
			val = v.String()
		}
		this.Set("__skyComposed", val)
		dispatchEvent(spaNodeHandlers.lookup(idOf(this), "input", h), val)
		return nil
	})
	spaListen(n, id, "compositionstart", start)
	spaListen(n, id, "compositionend", end)
}

// spaFormData reads a submitted <form>'s named controls into a map[string]any
// (control name -> its string value), the shape rt.Coerce narrows to a record
// (e.g. Credentials { email, password }) in update. Unnamed controls and an
// unchecked checkbox/radio are skipped, mirroring the browser's own FormData.
func spaFormData(form js.Value) FormFields {
	out := FormFields{}
	if !form.Truthy() {
		return out
	}
	els := form.Get("elements")
	if !els.Truthy() {
		return out
	}
	n := els.Get("length").Int()
	for i := 0; i < n; i++ {
		el := els.Index(i)
		name := el.Get("name")
		if name.Type() != js.TypeString || name.String() == "" {
			continue
		}
		if t := el.Get("type"); t.Type() == js.TypeString {
			if t.String() == "checkbox" || t.String() == "radio" {
				if !el.Get("checked").Truthy() {
					continue
				}
			}
		}
		if v := el.Get("value"); v.Type() == js.TypeString {
			out[name.String()] = v.String()
		}
	}
	return out
}

// dispatchSubmit turns a form-submit handler into a Msg and dispatches it. The
// onSubmit value is a `func(any) any` wrapping the form record in a Msg
// constructor (`Ui.onSubmit DoSignIn`, DoSignIn taking a record built from the
// fields), so hand it the field map. A plain nullary Msg value (onSubmit with a
// no-arg Msg) is dispatched as-is. A panic (an rt.Coerce mismatch) is recovered
// so the instance stays alive, like dispatchEvent.
func dispatchSubmit(handler any, data FormFields) {
	if spaDispatch == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// A field the handler's record cannot take (form_decode.go): the
			// submit is dropped and reported, never zero-filled.
			if fe, ok := asFormDecodeError(r); ok {
				spaReportFormDecodeError(fe)
				return
			}
			spaReportPanic("submit", r)
		}
	}()
	switch h := handler.(type) {
	case func(any) any:
		spaDispatch(h(data))
	case func(string) any:
		// A form submit is a record of fields, never one String; the compiler
		// rejects a String handler ([E2010]). Report it rather than dispatch a
		// made-up value.
		spaReportFormDecodeError(&FormDecodeError{Record: "String", Field: "", Reason: "cannot receive a form submit: the handler takes a String"})
	default:
		if isFunc(handler) {
			spaDispatch(sky_call(handler, data))
		} else {
			spaDispatch(handler)
		}
	}
}

// spaReadFileAndDispatch reads the file chosen in a file input as a data URL and
// dispatches the onFile/onImage handler with it (a `String -> Msg`). The file is
// delivered raw — no client-side resize (the backend Std.Image resizes). An
// optional data-sky-ev-sky-file-max-size (bytes) rejects an over-large file with
// an alert, matching Sky.Live + Std.Ui.maxFileSize. The input is reset after so
// the same file can be re-picked. FileReader js.Funcs are released one-shot.
func spaReadFileAndDispatch(input js.Value, handler any) {
	files := input.Get("files")
	if !files.Truthy() || files.Get("length").Int() == 0 {
		return
	}
	file := files.Index(0)
	if maxAttr := input.Call("getAttribute", "data-sky-ev-sky-file-max-size"); maxAttr.Type() == js.TypeString && maxAttr.String() != "" {
		if max, err := strconv.Atoi(maxAttr.String()); err == nil && max > 0 {
			if sz := file.Get("size"); sz.Type() == js.TypeNumber && sz.Int() > max {
				if alert := js.Global().Get("alert"); alert.Type() == js.TypeFunction {
					alert.Invoke(spaFileTooLargeMessage(max))
				}
				input.Set("value", "")
				return
			}
		}
	}
	reader := js.Global().Get("FileReader").New()
	var onload, onerr js.Func
	release := func() {
		onload.Release()
		onerr.Release()
	}
	onload = js.FuncOf(func(this js.Value, a []js.Value) any {
		defer release()
		if res := reader.Get("result"); res.Type() == js.TypeString {
			dispatchEvent(handler, res.String())
		}
		input.Set("value", "")
		return nil
	})
	onerr = js.FuncOf(func(this js.Value, a []js.Value) any {
		defer release()
		input.Set("value", "")
		return nil
	})
	reader.Set("onload", onload)
	reader.Set("onerror", onerr)
	reader.Call("readAsDataURL", file)
}

// eventPayload extracts the argument a handler expects, with the same
// convention as Sky.Live's __skyExtractArgs (live.go):
//
//   - input / change on a checkbox or radio → its .checked (a Bool), so
//     Html.Events.onCheck gets the Bool it is typed for (F11: it used to get
//     the string "" and the dispatch panicked);
//   - input / change otherwise → the target's .value;
//   - keydown / keyup / keypress → event.key (F10: it used to be "");
//   - anything else → no payload.
func eventPayload(evt string, args []js.Value) any {
	if len(args) == 0 || !args[0].Truthy() {
		return ""
	}
	ev := args[0]
	switch evt {
	case "keydown", "keyup", "keypress":
		if k := ev.Get("key"); k.Type() == js.TypeString {
			return k.String()
		}
		return ""
	case "input", "change":
		target := ev.Get("target")
		if !target.Truthy() {
			return ""
		}
		if t := target.Get("type"); t.Type() == js.TypeString && (t.String() == "checkbox" || t.String() == "radio") {
			return target.Get("checked").Truthy()
		}
		if v := target.Get("value"); v.Type() == js.TypeString {
			return v.String()
		}
	}
	return ""
}

// dispatchEvent turns an event handler value into a Msg and dispatches it.
// A plain Msg value (onClick Increment) is dispatched as-is; a handler
// function (onInput toMsg) is applied to the event payload first.
func dispatchEvent(handler any, payload any) {
	if spaDispatch == nil {
		return
	}
	// The handler application (turning an onInput/onChange toMsg into a Msg)
	// runs BEFORE step, so a panic here (e.g. an rt.Coerce in the toMsg) is
	// outside step's guard. Recover it too: log loudly and drop this event,
	// leaving the model untouched and the instance alive for the next event.
	// step itself is guarded separately (spaTransition), so update/view panics
	// are handled there.
	defer func() {
		if r := recover(); r != nil {
			spaReportPanic("event", r)
		}
	}()
	// Reflection-free (Sky.Spa client): a payload handler emits as
	// `func(string) any` (onInput/onChange), `func(bool) any` (onCheck) or
	// `func(any) any`; apply by TYPED ASSERTION rather than
	// `reflect.Value.Call` (TinyGo cannot compile it). A plain Msg value
	// (onClick Increment) is not a func → dispatched as-is.
	switch h := handler.(type) {
	case func(string) any:
		spaDispatch(h(payloadString(payload)))
	case func(bool) any:
		spaDispatch(h(payloadBool(payload)))
	case func(any) any:
		spaDispatch(h(payload))
	default:
		if isFunc(handler) {
			spaDispatch(sky_call(handler, payload))
		} else {
			spaDispatch(handler)
		}
	}
}

// spaApplyPatches applies a []Patch (produced by diffTrees) to the live DOM,
// addressing each target by sky-id. newRoot is the source of truth for every
// node the patches create (so real listeners get attached); listeners of
// removed nodes are released by walking the removed DOM. Focus/cursor/
// dirty-input authority is ported from live.go's __skyApplyPatches.
func spaApplyPatches(patches []Patch, oldRoot, newRoot *VNode) {
	if len(patches) == 0 {
		return
	}
	doc := js.Global().Get("document")

	// Open-<select> defence: a native dropdown closes on ANY DOM mutation
	// inside the open select or an ancestor that would re-mount it. There is no
	// API for "is the dropdown open", so use focus as the conservative proxy —
	// if a SELECT is the active element, treat its subtree (and its ancestors)
	// as off-limits this cycle. The next interaction ships a fresh render.
	active := doc.Get("activeElement")
	var openSel js.Value
	if active.Truthy() && tagName(active) == "SELECT" {
		openSel = active
	}

	for i := range patches {
		p := patches[i]
		el := doc.Call("querySelector", `[sky-id="`+escAttr(p.ID)+`"]`)
		if !el.Truthy() {
			if c := js.Global().Get("console"); c.Truthy() {
				c.Call("warn", "[sky.spa] patch target not found:", p.ID)
			}
			continue
		}
		if openSel.Truthy() &&
			(el.Equal(openSel) || el.Call("contains", openSel).Bool() || openSel.Call("contains", el).Bool()) {
			continue
		}

		if p.Replace != nil {
			spaReplaceNode(el, p.ID, newRoot)
			continue
		}
		if p.Text != nil {
			// textContent on a container that holds the focused input would
			// also wipe the input. Guard like the HTML path.
			if containsFocusedInput(el) {
				rebuildChildrenPreservingFocus(el, p.ID, newRoot)
			} else {
				releaseDOMChildren(el)
				el.Set("textContent", *p.Text)
			}
		}
		if p.HTML != nil {
			rebuildChildrenPreservingFocus(el, p.ID, newRoot)
		}
		if p.Kids != nil {
			spaApplyKids(el, p, newRoot)
		}
		if p.Attrs != nil {
			applyAttrs(el, p.Attrs, p.ID, newRoot)
		}
		if p.Remove {
			releaseDOMSubtree(el)
			el.Call("remove")
		}
	}
}

// spaReplaceNode replaces el itself with a node built from the new tree (a
// root that changed tag, K3). It used to rebuild only el's children, so the
// old root tag survived.
func spaReplaceNode(el js.Value, id string, newRoot *VNode) {
	nv := findVNode(newRoot, id)
	if nv == nil {
		return
	}
	releaseDOMSubtree(el)
	el.Call("replaceWith", buildDOM(*nv))
}

// spaApplyKids applies a children-reconcile patch (see KidOp): kept children
// stay the SAME DOM node (moved only on a real reorder), new children are
// built from the new tree, everything else is removed, and a kept child whose
// id changed is renamed. A focused input inside a kept child keeps its focus,
// caret, IME state and uncommitted value.
func spaApplyKids(el js.Value, p Patch, newRoot *VNode) {
	nv := findVNode(newRoot, p.ID)
	byID := map[string]js.Value{}
	for c := el.Get("firstElementChild"); c.Truthy(); c = c.Get("nextElementSibling") {
		if s := c.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
			byID[s.String()] = c
		}
	}
	doc := js.Global().Get("document")
	active := doc.Get("activeElement")
	focusInside := active.Truthy() && el.Call("contains", active).Bool() && !el.Equal(active)
	selStart, selEnd := -1, -1
	if focusInside {
		selStart, selEnd = selectionRange(active)
	}

	// 1. Which existing children stay.
	kept := map[string]bool{}
	var renames []spaRename
	for _, k := range p.Kids {
		if k.Keep == "" || kept[k.Keep] {
			continue
		}
		n, ok := byID[k.Keep]
		if !ok {
			// The node to keep is missing (the DOM drifted): the slot is
			// built fresh from the new tree below rather than left a hole.
			if c := js.Global().Get("console"); c.Truthy() {
				c.Call("warn", "[sky.spa] kept child not found, rebuilding:", k.Keep)
			}
			continue
		}
		kept[k.Keep] = true
		if k.ID != "" && k.ID != k.Keep {
			renames = append(renames, spaRename{n, k.Keep, k.ID})
		}
	}
	// 2. Remove (and release) everything else — BEFORE any new node binds
	// listeners, since a removed node may carry an id a new node reuses.
	for c := el.Get("firstChild"); c.Truthy(); {
		next := c.Get("nextSibling")
		keep := false
		if c.Get("nodeType").Int() == 1 {
			if s := c.Call("getAttribute", "sky-id"); s.Type() == js.TypeString && kept[s.String()] {
				keep = true
			}
		}
		if !keep {
			releaseDOMSubtree(c)
			el.Call("removeChild", c)
		}
		c = next
	}
	// 3. Rename kept nodes — also before new nodes bind, since a kept node's
	// OLD id may be a new node's id (a shift).
	spaRenameKept(renames)
	// 4. Build the new nodes and put every slot in order. Kept nodes already
	// sit in relative order unless the list was reordered; only then is a
	// kept node moved.
	cursor := el.Get("firstChild")
	for i, k := range p.Kids {
		var n js.Value
		if k.Keep != "" && kept[k.Keep] {
			n = byID[k.Keep]
		} else if nv != nil && i < len(nv.Children) {
			n = buildDOM(nv.Children[i])
		} else {
			continue
		}
		if cursor.Truthy() && n.Equal(cursor) {
			cursor = cursor.Get("nextSibling")
			continue
		}
		if cursor.Truthy() {
			el.Call("insertBefore", n, cursor)
		} else {
			el.Call("appendChild", n)
		}
	}
	if nv != nil {
		spaSyncSelectValue(el, nv)
	}
	// A moved node loses focus (removal blurs); put it back.
	if focusInside && active.Get("isConnected").Truthy() && !active.Equal(doc.Get("activeElement")) {
		active.Call("focus")
		if selStart >= 0 && hasFn(active, "setSelectionRange") {
			l := valueLen(active)
			active.Call("setSelectionRange", min(selStart, l), min(selEnd, l))
		}
	}
}

// spaRename is one kept child whose sky-id changed (KidOp.ID).
type spaRename struct {
	n        js.Value
	from, to string
}

// spaRenameKept rewrites the sky-ids of renamed kept subtrees (every id equal
// to `from` or under `from.` moves to `to`), and moves the listener
// bookkeeping with them. Two phases: every renamed id is taken out of the
// table before any is put back, so a shift (a→b while b→c) cannot clobber.
func spaRenameKept(renames []spaRename) {
	type moved struct {
		n      js.Value
		to     string
		hidTo  string
		hasHid bool
		fns    []spaListener
	}
	var all []moved
	for _, r := range renames {
		root, from, to := r.n, r.from, r.to
		visit := func(n js.Value) {
			s := n.Call("getAttribute", "sky-id")
			if s.Type() != js.TypeString {
				return
			}
			old := s.String()
			if old != from && !strings.HasPrefix(old, from+".") {
				return
			}
			m := moved{n: n, to: to + old[len(from):]}
			if h := n.Call("getAttribute", "data-sky-hid"); h.Type() == js.TypeString && strings.HasPrefix(h.String(), from+".") {
				m.hasHid = true
				m.hidTo = to + h.String()[len(from):]
			}
			m.fns = spaNodeFns[old]
			delete(spaNodeFns, old)
			spaNodeHandlers.drop(old)
			all = append(all, m)
		}
		visit(root)
		list := root.Call("querySelectorAll", "[sky-id]")
		for i := 0; i < list.Length(); i++ {
			visit(list.Index(i))
		}
	}
	for _, m := range all {
		m.n.Call("setAttribute", "sky-id", m.to)
		if m.hasHid {
			m.n.Call("setAttribute", "data-sky-hid", m.hidTo)
		}
		if len(m.fns) > 0 {
			spaNodeFns[m.to] = append(spaNodeFns[m.to], m.fns...)
		}
	}
}

// applyAttrs sets/removes attributes on el with caret/selection preservation.
//
// Input-authority note (why there is no blanket "drop value on a focused
// field"): a Sky.Spa dispatch is SYNCHRONOUS and client-authoritative — the
// keystroke updates the model before the re-render, so there is never an
// unacked in-flight value the way the server model has. diffTrees' clientState
// alignment already skips a value patch precisely when the model equals what
// the DOM shows (the user's own typing), so the only value patches that reach a
// focused input here are genuine PROGRAMMATIC changes (model != DOM), which
// SHOULD apply. We snapshot and restore the caret/selection around any value
// write so even a programmatic change does not jump the cursor. Event-attribute
// changes trigger a listener re-bind from the new VNode (real js.Func listeners
// are the client's wiring; the sky-<event> attributes alone do nothing here).
func applyAttrs(el js.Value, attrs map[string]string, id string, newRoot *VNode) {
	tag := tagName(el)
	isInputLike := tag == "INPUT" || tag == "TEXTAREA"

	doc := js.Global().Get("document")
	active := doc.Get("activeElement")
	hadFocus := isInputLike && active.Truthy() && el.Equal(active)
	selStart, selEnd := -1, -1
	var savedScroll js.Value
	if hadFocus {
		selStart, selEnd = selectionRange(el)
		savedScroll = el.Get("scrollTop")
	}

	valueChanged := false
	eventChanged := false
	for k, v := range attrs {
		if strings.HasPrefix(k, "sky-") || strings.HasPrefix(k, "data-sky-ev-") || k == "data-sky-hid" {
			eventChanged = true
		}
		// Sync the ATTRIBUTE. Empty removes it; otherwise an idempotent
		// setAttribute — some elements re-fetch/re-navigate on ANY assignment
		// (iframe/img src, link href), even to an identical value.
		if v == "" {
			el.Call("removeAttribute", k)
		} else if cur := el.Call("getAttribute", k); cur.Type() != js.TypeString || cur.String() != v {
			el.Call("setAttribute", k, v)
		}
		// Sync the live DOM PROPERTY for property-backed attributes — REQUIRED
		// even when cleared to "". Removing the `value` attribute does NOT reset
		// an input's current `.value`, so a programmatic clear (a model field set
		// to "") must write the property to actually empty the box; likewise an
		// empty `checked`/`selected`/`disabled` must drive the property to false.
		switch k {
		case "value":
			// An open IME composition owns the field; writing .value under it
			// cancels the composition (UF-11). The committed text dispatches
			// on compositionend and the next render reconciles.
			if el.Get("__skyComposing").Truthy() {
				continue
			}
			el.Set("value", v)
			valueChanged = true
		case "checked":
			el.Set("checked", boolAttr(v))
		case "selected":
			el.Set("selected", boolAttr(v))
		case "disabled":
			el.Set("disabled", boolAttr(v))
		}
	}

	if eventChanged {
		if nv := findVNode(newRoot, id); nv != nil {
			releaseNodeFns(id, el)
			bindNodeEvents(el, *nv)
		}
	}

	// Restore selection after a value write on the focused input. Clamp to the
	// new length so a shorter value cannot throw. Scroll restore matters for a
	// multi-line textarea the user has scrolled.
	if hadFocus && valueChanged && selStart >= 0 && hasFn(el, "setSelectionRange") {
		newLen := valueLen(el)
		s := min(selStart, newLen)
		e := min(selEnd, newLen)
		el.Call("setSelectionRange", s, e)
		if savedScroll.Type() == js.TypeNumber {
			el.Set("scrollTop", savedScroll)
		}
	}
}

// rebuildChildrenPreservingFocus replaces el's children from the new VNode
// subtree (real nodes + listeners), releasing the old children's listeners
// first, and preserves focus + caret of any input that was focused inside el by
// re-focusing the element with the same sky-id after the rebuild.
//
// NOTE: the focused input's DOM node identity is NOT preserved across this
// path (the subtree is rebuilt). It is reached only for children that cannot
// be reconciled one by one (raw HTML, <style>/<script>/<textarea> text) or
// when nothing matched; every other child change is a Kids patch, which keeps
// the nodes (spaApplyKids).
func rebuildChildrenPreservingFocus(el js.Value, id string, newRoot *VNode) {
	newSub := findVNode(newRoot, id)
	if newSub == nil {
		return
	}
	doc := js.Global().Get("document")
	active := doc.Get("activeElement")
	focSid, selStart, selEnd := "", -1, -1
	if active.Truthy() && el.Call("contains", active).Bool() {
		t := tagName(active)
		if t == "INPUT" || t == "TEXTAREA" || t == "SELECT" {
			if s := active.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
				focSid = s.String()
			}
			selStart, selEnd = selectionRange(active)
		}
	}

	releaseDOMChildren(el)
	el.Set("innerHTML", "")
	spaSetChildren(el, newSub.Children)
	spaSyncSelectValue(el, newSub)

	if focSid != "" {
		nf := doc.Call("querySelector", `[sky-id="`+escAttr(focSid)+`"]`)
		if nf.Truthy() {
			nf.Call("focus")
			if selStart >= 0 && hasFn(nf, "setSelectionRange") {
				newLen := valueLen(nf)
				nf.Call("setSelectionRange", min(selStart, newLen), min(selEnd, newLen))
			}
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func containsFocusedInput(el js.Value) bool {
	active := js.Global().Get("document").Get("activeElement")
	if !active.Truthy() {
		return false
	}
	switch tagName(active) {
	case "INPUT", "TEXTAREA", "SELECT":
	default:
		return false
	}
	return el.Equal(active) || el.Call("contains", active).Bool()
}

func tagName(el js.Value) string {
	t := el.Get("tagName")
	if t.Type() == js.TypeString {
		return t.String()
	}
	return ""
}

func selectionRange(el js.Value) (int, int) {
	s, e := -1, -1
	if ss := el.Get("selectionStart"); ss.Type() == js.TypeNumber {
		s = ss.Int()
	}
	if se := el.Get("selectionEnd"); se.Type() == js.TypeNumber {
		e = se.Int()
	}
	if e < 0 {
		e = s
	}
	return s, e
}

func valueLen(el js.Value) int {
	if v := el.Get("value"); v.Type() == js.TypeString {
		return len(v.String())
	}
	return 0
}

func hasFn(el js.Value, name string) bool {
	return el.Get(name).Type() == js.TypeFunction
}

func escAttr(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func findVNode(root *VNode, id string) *VNode {
	if root == nil {
		return nil
	}
	if root.SkyID == id {
		return root
	}
	for i := range root.Children {
		if r := findVNode(&root.Children[i], id); r != nil {
			return r
		}
	}
	return nil
}

// releaseNodeFns detaches and releases the listeners recorded under id. With
// a node, only that node's listeners go (another node may carry the same id
// while a patch set is half applied); with js.Value{} every listener under id
// goes (spaMount, which empties the whole mount).
func releaseNodeFns(id string, node js.Value) {
	all := node.Type() != js.TypeObject
	var keep []spaListener
	for _, l := range spaNodeFns[id] {
		if !all && !l.node.Equal(node) {
			keep = append(keep, l)
			continue
		}
		l.node.Call("removeEventListener", l.evt, l.fn)
		l.fn.Release()
	}
	if len(keep) > 0 {
		spaNodeFns[id] = keep
		return
	}
	delete(spaNodeFns, id)
	spaNodeHandlers.drop(id)
}

// releaseDOMSubtree releases the listeners bound on n and every element
// under it, by the sky-ids the DOM carries NOW (a kept node may have been
// renamed since it was built, so the VNode tree is not the authority).
func releaseDOMSubtree(n js.Value) {
	if n.Type() != js.TypeObject || n.Get("nodeType").Int() != 1 {
		return
	}
	if s := n.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
		releaseNodeFns(s.String(), n)
	}
	list := n.Call("querySelectorAll", "[sky-id]")
	for i := 0; i < list.Length(); i++ {
		c := list.Index(i)
		if s := c.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
			releaseNodeFns(s.String(), c)
		}
	}
}

// releaseDOMChildren releases the listeners of every element under el (not
// el's own).
func releaseDOMChildren(el js.Value) {
	for c := el.Get("firstElementChild"); c.Truthy(); c = c.Get("nextElementSibling") {
		releaseDOMSubtree(c)
	}
}
