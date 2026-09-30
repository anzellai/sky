//go:build js

package rt

import "syscall/js"

// scene_canvas_wasm.go — the Sky.Spa client's side of the canvas backend of
// Std.Ui.Canvas (scene_canvas.go has the draw list and the routing,
// scenePainterJS in scene_client.go the painter).

// spaSceneBackendCache is the page's backend override, read once:
// window.Sky.sceneBackend = "svg" | "canvas" (a measurement sets it before the
// client boots); "" is automatic.
var (
	spaSceneBackendRead  bool
	spaSceneBackendCache string
	spaSceneDispatchFn   js.Func
	spaSceneDispatchSet  bool
)

func spaSceneBackend() string {
	if !spaSceneBackendRead {
		spaSceneBackendRead = true
		if sky := js.Global().Get("Sky"); sky.Truthy() {
			if b := sky.Get("sceneBackend"); b.Type() == js.TypeString {
				spaSceneBackendCache = b.String()
			}
		}
	}
	return spaSceneBackendCache
}

// spaScenePainter is window.Sky.sceneCanvas, or undefined when the boot file
// does not carry it (then every scene stays SVG).
func spaScenePainter() js.Value {
	sky := js.Global().Get("Sky")
	if !sky.Truthy() {
		return js.Undefined()
	}
	return sky.Get("sceneCanvas")
}

// spaSceneCanvas reports whether the client draws the scene el on a canvas.
func spaSceneCanvas(el *VNode) bool {
	if !isSceneVNode(el) || !spaScenePainter().Truthy() {
		return false
	}
	return sceneWantsCanvas(el, spaSceneBackend())
}

// spaIsSceneCanvas reports whether the DOM node n is a canvas-drawn scene.
func spaIsSceneCanvas(n js.Value) bool {
	return n.Truthy() && tagName(n) == "CANVAS" && n.Call("getAttribute", "data-sky-scene").Truthy()
}

// spaBuildSceneCanvas builds the <canvas> of a scene: the scene's sky-id, the
// accessible name (role="img", aria-label; the painter adds the text
// alternative), the SVG's box, and the draw list handed to the painter.
func spaBuildSceneCanvas(el VNode) js.Value {
	spaSceneHookDispatch()
	doc := js.Global().Get("document")
	cv := doc.Call("createElement", "canvas")
	if el.SkyID != "" {
		cv.Call("setAttribute", "sky-id", el.SkyID)
	}
	cv.Call("setAttribute", "data-sky-scene", el.Attrs["data-sky-scene"])
	cv.Call("setAttribute", "data-sky-scene-backend", "canvas")
	cv.Call("setAttribute", "role", "img")
	cv.Call("setAttribute", "aria-label", el.Attrs["aria-label"])
	cv.Call("setAttribute", "style", sceneCanvasStyle(el.Attrs["style"], el.Attrs["width"]))
	w, h := sceneSize(&el)
	spaScenePainter().Call("mount", cv, w, h, el.Attrs["aria-label"], encodeScene(&el))
	return cv
}

// spaSceneHookDispatch installs window.Sky.__sceneDispatch, which the painter
// calls with (scene sky-id, record index, event type, x, y) when a pointer
// event hits a shape that listens for it.
func spaSceneHookDispatch() {
	if spaSceneDispatchSet {
		return
	}
	spaSceneDispatchSet = true
	spaSceneDispatchFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 5 || spaPrev == nil {
			return nil
		}
		scene := findVNode(spaPrev, args[0].String())
		if !isSceneVNode(scene) {
			return nil
		}
		typ := args[2].String()
		key := sceneEventKey(typ)
		detail := sceneDetailJSON(args[3].Float(), args[4].Float())
		// The shape, then each group around it: the order the SVG's DOM
		// bubbling gives. The handlers are taken before the first dispatch
		// re-renders.
		var hs []any
		for _, n := range sceneChainAt(scene, args[1].Int()) {
			if h, ok := n.Events[key]; ok {
				hs = append(hs, h)
			}
		}
		for _, h := range hs {
			if typ == "click" {
				dispatchEvent(h, "")
			} else {
				dispatchEvent(h, detail)
			}
		}
		return nil
	})
	if sky := js.Global().Get("Sky"); sky.Truthy() {
		sky.Set("__sceneDispatch", spaSceneDispatchFn)
	}
}

// spaScenePass routes one render's patches that fall inside scenes: a scene
// drawn on a canvas has no DOM node per shape, so its patches become draw-list
// updates, sent once at the end (finish); a scene whose backend changes (it
// grew past or shrank under sceneCanvasMin) is rebuilt.
type spaScenePass struct {
	scenes  map[string]*VNode
	mode    map[string]int // 0 unknown, 1 canvas, 2 svg, 3 no node
	want    map[string]bool
	nodes   map[string]map[string]*VNode
	replace map[string]bool
	full    map[string]bool
	shapes  map[string]map[string]bool
}

func newSpaScenePass(newRoot *VNode) *spaScenePass {
	scenes := sceneIndex(newRoot)
	if len(scenes) == 0 {
		return nil
	}
	return &spaScenePass{
		scenes:  scenes,
		mode:    map[string]int{},
		want:    map[string]bool{},
		nodes:   map[string]map[string]*VNode{},
		replace: map[string]bool{},
		full:    map[string]bool{},
		shapes:  map[string]map[string]bool{},
	}
}

func spaQueryID(id string) js.Value {
	return js.Global().Get("document").Call("querySelector", `[sky-id="`+escAttr(id)+`"]`)
}

// take reports whether it consumed p (the DOM applier must then skip it).
func (sp *spaScenePass) take(p *Patch) bool {
	sid := sceneOwner(p.ID, sp.scenes)
	if sid == "" {
		return false
	}
	if p.ID == sid && (p.Replace != nil || p.Remove) {
		return false // the node goes or is rebuilt from the new tree
	}
	m := sp.mode[sid]
	if m == 0 {
		n := spaQueryID(sid)
		switch {
		case !n.Truthy():
			m = 3
		case spaIsSceneCanvas(n):
			m = 1
		default:
			m = 2
		}
		sp.mode[sid] = m
	}
	if m == 3 {
		return false
	}
	// The backend is decided once per scene and render (it walks the scene).
	want, seen := sp.want[sid]
	if !seen {
		want = spaSceneCanvas(sp.scenes[sid])
		sp.want[sid] = want
	}
	if (m == 1) != want {
		sp.replace[sid] = true
		return true
	}
	if m == 2 {
		return false
	}
	nodes := sp.nodes[sid]
	if nodes == nil {
		nodes = sceneNodes(sp.scenes[sid])
		sp.nodes[sid] = nodes
	}
	switch sceneRoute(p, sid, nodes) {
	case sceneRouteHost:
		sp.replace[sid] = true
	case sceneRouteFull:
		sp.full[sid] = true
	default:
		s := sp.shapes[sid]
		if s == nil {
			s = map[string]bool{}
			sp.shapes[sid] = s
		}
		s[p.ID] = true
	}
	return true
}

// finish rebuilds the scenes marked for it and sends each changed canvas scene
// its update: the changed shapes alone, or the whole list when the structure
// changed or most shapes did.
func (sp *spaScenePass) finish(newRoot *VNode) {
	for sid := range sp.replace {
		if el := spaQueryID(sid); el.Truthy() {
			spaReplaceNode(el, sid, newRoot)
		}
	}
	painter := spaScenePainter()
	send := func(sid string, full bool, ids map[string]bool) {
		if sp.replace[sid] {
			return
		}
		host := spaQueryID(sid)
		if !spaIsSceneCanvas(host) || !painter.Truthy() {
			return
		}
		scene := sp.scenes[sid]
		if !full {
			count, _ := sceneCanvasShapes(scene)
			if len(ids)*4 <= count {
				if upd, found := encodeSceneUpdate(scene, ids); found == len(ids) {
					painter.Call("update", host, upd)
					return
				}
			}
		}
		painter.Call("set", host, encodeScene(scene))
	}
	for sid := range sp.full {
		send(sid, true, nil)
	}
	for sid, ids := range sp.shapes {
		if !sp.full[sid] {
			send(sid, false, ids)
		}
	}
}
