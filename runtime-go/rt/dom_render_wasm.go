//go:build js

package rt

import (
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

// spaNodeFns maps a DOM element's sky-id to the js.Funcs bound on it. A rebuild,
// removal, or handler change Releases them before dropping the node — an
// unreleased js.Func leaks its Go closure for the life of the process.
var spaNodeFns = map[string][]js.Func{}

// spaMount replaces mount's content with a freshly-built DOM tree for root and
// records the event listeners it attaches. Used for the initial render only.
func spaMount(mount js.Value, root VNode) {
	for id := range spaNodeFns {
		releaseNodeFns(id)
	}
	mount.Set("innerHTML", "")
	mount.Call("appendChild", buildDOM(root))
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
		// Raw HTML: wrap in a <span> and set innerHTML. Std.Html.raw is the
		// only source and it is author-controlled markup.
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
			reflectInputProp(n, k, v)
		}
		bindNodeEvents(n, el)
		for i := range el.Children {
			n.Call("appendChild", buildDOM(el.Children[i]))
		}
		return n
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
	for evt, handler := range el.Events {
		if strings.HasPrefix(evt, "sky-") {
			continue
		}
		h := handler // capture per listener
		e := evt
		f := js.FuncOf(func(this js.Value, args []js.Value) any {
			dispatchEvent(h, eventPayload(e, args))
			return nil
		})
		spaNodeFns[el.SkyID] = append(spaNodeFns[el.SkyID], f)
		n.Call("addEventListener", evt, f)
	}
}

// eventPayload extracts the string argument a handler expects. Input/change hand
// in the target's current value; other events carry no payload.
func eventPayload(evt string, args []js.Value) string {
	if evt != "input" && evt != "change" {
		return ""
	}
	if len(args) == 0 {
		return ""
	}
	target := args[0].Get("target")
	if target.Truthy() {
		if v := target.Get("value"); v.Type() == js.TypeString {
			return v.String()
		}
	}
	return ""
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

// spaApplyPatches applies a []Patch (produced by diffTrees) to the live DOM,
// addressing each target by sky-id. oldRoot/newRoot are the previous and new
// VNode trees: newRoot is the source of truth for subtree rebuilds (so real
// listeners get re-attached), oldRoot lets us release listeners of removed
// subtrees. Focus/cursor/dirty-input authority is ported from
// live.go's __skyApplyPatches.
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

		if p.Text != nil {
			// textContent on a container that holds the focused input would
			// also wipe the input. Guard like the HTML path.
			if containsFocusedInput(el) {
				rebuildChildrenPreservingFocus(el, p.ID, oldRoot, newRoot)
			} else {
				el.Set("textContent", *p.Text)
			}
		}
		if p.HTML != nil {
			rebuildChildrenPreservingFocus(el, p.ID, oldRoot, newRoot)
		}
		if p.Attrs != nil {
			applyAttrs(el, p.Attrs, p.ID, newRoot)
		}
		if p.Remove {
			releaseSubtree(findVNode(oldRoot, p.ID))
			el.Call("remove")
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
		if v == "" {
			el.Call("removeAttribute", k)
			continue
		}
		// Idempotent setAttribute: some elements re-fetch/re-navigate on ANY
		// assignment (iframe/img src, link href), even to an identical value.
		if cur := el.Call("getAttribute", k); cur.Type() != js.TypeString || cur.String() != v {
			el.Call("setAttribute", k, v)
		}
		switch k {
		case "value":
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
			releaseNodeFns(id)
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
// subtree (real nodes + listeners), releasing the old subtree's listeners
// first, and preserves focus + caret of any input that was focused inside el by
// re-focusing the element with the same sky-id after the rebuild.
//
// NOTE: the focused input's DOM node identity is NOT preserved across this
// path (the subtree is rebuilt). Focus, caret and value ARE restored. In the
// minimal-patch typing case the input is never under an HTML/text-container
// patch, so its node identity is stable — see spaApplyPatches' Attrs path.
func rebuildChildrenPreservingFocus(el js.Value, id string, oldRoot, newRoot *VNode) {
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

	if oldSub := findVNode(oldRoot, id); oldSub != nil {
		for i := range oldSub.Children {
			releaseSubtree(&oldSub.Children[i])
		}
	}
	el.Set("innerHTML", "")
	for i := range newSub.Children {
		el.Call("appendChild", buildDOM(newSub.Children[i]))
	}

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

func releaseNodeFns(id string) {
	if fns, ok := spaNodeFns[id]; ok {
		for _, f := range fns {
			f.Release()
		}
		delete(spaNodeFns, id)
	}
}

func releaseSubtree(n *VNode) {
	if n == nil {
		return
	}
	if n.SkyID != "" {
		releaseNodeFns(n.SkyID)
	}
	for i := range n.Children {
		releaseSubtree(&n.Children[i])
	}
}
