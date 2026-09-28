//go:build js

package rt

import (
	"strings"
	"syscall/js"
)

// island_wasm.go — widget islands in the Sky.Spa wasm client (island_core.go
// has the model). The island runtime itself (mount / update / destroy through
// a MutationObserver, window.Sky.island) is the same JS the Sky.Live client
// carries (island_client.go); the boot loader installs it. This file keeps the
// widget's element through the client's own DOM rebuilds, hands widget events
// to their Sky decoder, and delivers Cmd.toIsland.

// spaIslandPool collects the island elements under scope (and scope itself
// when it is one), by identity, before a rebuild discards them.
func spaIslandPool(scope js.Value) map[string]js.Value {
	if scope.Type() != js.TypeObject || scope.Get("nodeType").Int() != 1 {
		return nil
	}
	var pool map[string]js.Value
	add := func(n js.Value) {
		if k := spaIslandKey(n); k != "" {
			if pool == nil {
				pool = map[string]js.Value{}
			}
			pool[k] = n
		}
	}
	add(scope)
	list := scope.Call("querySelectorAll", "["+islandNameAttr+"]")
	for i := 0; i < list.Length(); i++ {
		add(list.Index(i))
	}
	return pool
}

// spaIslandKey is the identity of an island element, or "".
func spaIslandKey(n js.Value) string {
	name := n.Call("getAttribute", islandNameAttr)
	if name.Type() != js.TypeString || name.String() == "" {
		return ""
	}
	id := n.Call("getAttribute", islandIDAttr)
	if id.Type() != js.TypeString {
		return name.String() + "\x00"
	}
	return name.String() + "\x00" + id.String()
}

// spaAdoptIslands puts each pooled island back in place of the freshly built
// element with the same identity under scope (or scope itself), so the widget
// keeps its element, its DOM and its state. The fresh element's attributes
// (new props included) are copied onto the kept one, and its listeners move
// with it. It returns scope, or the kept island when scope itself was adopted.
func spaAdoptIslands(pool map[string]js.Value, scope js.Value, newRoot *VNode) js.Value {
	if len(pool) == 0 || scope.Type() != js.TypeObject || scope.Get("nodeType").Int() != 1 {
		return scope
	}
	var fresh []js.Value
	if spaIslandKey(scope) != "" {
		fresh = append(fresh, scope)
	}
	list := scope.Call("querySelectorAll", "["+islandNameAttr+"]")
	for i := 0; i < list.Length(); i++ {
		fresh = append(fresh, list.Index(i))
	}
	out := scope
	for _, n := range fresh {
		k := spaIslandKey(n)
		kept, ok := pool[k]
		if !ok || kept.Equal(n) {
			continue
		}
		delete(pool, k)
		spaCopyIslandAttrs(n, kept)
		sid := ""
		if s := n.Call("getAttribute", "sky-id"); s.Type() == js.TypeString {
			sid = s.String()
		}
		if sid != "" {
			releaseNodeFns(sid, n)
			releaseNodeFns(sid, kept)
		}
		if p := n.Get("parentNode"); p.Truthy() {
			p.Call("replaceChild", kept, n)
		}
		if sid != "" {
			if nv := findVNode(newRoot, sid); nv != nil {
				bindNodeEvents(kept, *nv)
			}
		}
		if n.Equal(scope) {
			out = kept
		}
	}
	return out
}

// spaCopyIslandAttrs makes the kept island carry the fresh render's
// attributes. An attribute the fresh element lacks is removed only when it is
// in the server's namespace (sky-*, data-sky-*): anything else was set by the
// widget or the browser and stays.
func spaCopyIslandAttrs(src, dst js.Value) {
	attrs := src.Get("attributes")
	want := map[string]bool{}
	for i := 0; i < attrs.Length(); i++ {
		a := attrs.Index(i)
		name, val := a.Get("name").String(), a.Get("value").String()
		want[name] = true
		if cur := dst.Call("getAttribute", name); cur.Type() != js.TypeString || cur.String() != val {
			dst.Call("setAttribute", name, val)
		}
	}
	dattrs := dst.Get("attributes")
	var drop []string
	for i := 0; i < dattrs.Length(); i++ {
		name := dattrs.Index(i).Get("name").String()
		if !want[name] && (strings.HasPrefix(name, "sky-") || strings.HasPrefix(name, "data-sky-")) {
			drop = append(drop, name)
		}
	}
	for _, name := range drop {
		dst.Call("removeAttribute", name)
	}
}

// spaIslandFocus is the focus and selection inside an island, saved by the
// island runtime (Sky.__islands.saveFocus) before a rebuild moves the island's
// element (a move blurs it and resets the selection).
type spaIslandFocus struct {
	saved js.Value
}

func spaIslandRuntime() js.Value {
	sky := js.Global().Get("Sky")
	if !sky.Truthy() {
		return js.Value{}
	}
	return sky.Get("__islands")
}

func spaSaveIslandFocus(scope js.Value) *spaIslandFocus {
	rt := spaIslandRuntime()
	if !rt.Truthy() {
		return nil
	}
	f := rt.Call("saveFocus", scope)
	if !f.Truthy() {
		return nil
	}
	return &spaIslandFocus{saved: f}
}

func (f *spaIslandFocus) restore() {
	if f == nil {
		return
	}
	if rt := spaIslandRuntime(); rt.Truthy() {
		rt.Call("restoreFocus", f.saved)
	}
}

// spaEventDetailJSON is the detail of a CustomEvent as JSON text, or ok=false
// when the event is not a CustomEvent or carries no detail.
func spaEventDetailJSON(args []js.Value, island bool) (string, bool) {
	if len(args) == 0 || !args[0].Truthy() {
		return "", false
	}
	ev := args[0]
	ce := js.Global().Get("CustomEvent")
	if !ce.Truthy() || !ev.InstanceOf(ce) {
		return "", false
	}
	d := ev.Get("detail")
	if !island && (d.IsUndefined() || d.IsNull()) {
		return "", false
	}
	if d.IsUndefined() {
		return "null", true
	}
	s := js.Global().Get("JSON").Call("stringify", d)
	if s.Type() != js.TypeString {
		return "null", true
	}
	return s.String(), true
}

// spaIslandPending holds the Cmd.toIsland commands of the current update, in
// order, until spaIslandFlush delivers them.
var (
	spaIslandPending []islandCmd
	spaIslandFlushFn js.Func
	spaIslandFlushOn bool
)

// spaIslandCommand queues a Cmd.toIsland for delivery once the current update
// has finished (a microtask), never inside it. A widget's command handler may
// send an event straight back; delivered synchronously, that event's update
// ran INSIDE the update that sent the command, and the outer update then
// wrote its own model over the inner one (step assigns the model after its
// commands run), so the widget's event was lost. The microtask also runs
// after the island runtime's MutationObserver callback for this render, so a
// command for an island this update created finds it mounted. Sky.Live
// delivers commands after the update too (the SSE event "island").
func spaIslandCommand(ic islandCmd) {
	spaIslandPending = append(spaIslandPending, ic)
	if spaIslandFlushOn {
		return
	}
	spaIslandFlushOn = true
	if spaIslandFlushFn.IsUndefined() {
		spaIslandFlushFn = js.FuncOf(func(this js.Value, args []js.Value) any {
			spaIslandFlush()
			return nil
		})
	}
	js.Global().Call("queueMicrotask", spaIslandFlushFn)
}

// spaIslandFlush hands the queued commands to the island runtime, which
// delivers each to the mounted widget or holds it until the island mounts.
func spaIslandFlush() {
	cmds := spaIslandPending
	spaIslandPending = nil
	spaIslandFlushOn = false
	sky := js.Global().Get("Sky")
	if !sky.Truthy() || sky.Get("__islandCommand").Type() != js.TypeFunction {
		if c := js.Global().Get("console"); c.Truthy() && len(cmds) > 0 {
			c.Call("error", "[sky.spa] Cmd.toIsland: the island runtime is not loaded")
		}
		return
	}
	for _, ic := range cmds {
		payload := js.Null()
		if len(ic.Payload) > 0 {
			payload = js.Global().Get("JSON").Call("parse", string(ic.Payload))
		}
		sky.Call("__islandCommand", ic.ID, ic.Name, payload)
	}
}
