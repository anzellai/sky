package rt

import (
	"strconv"
	"strings"
)

// scene_canvas.go — the batched <canvas> backend of Std.Ui.Canvas scenes on
// the Sky.Spa client (Sky.Spa targets: web:app, the desktop:<os>,
// tablet:<os> and mobile:<os> native shells). Sky.Live keeps the server-rendered
// SVG; a terminal keeps the Braille cells (tui_scene.go).
//
// A scene reaches the client as the <svg data-sky-scene> VNode tree that
// Std/Ui/Canvas.sky builds, so the API and the server's first paint are the
// same on every backend. The Sky.Spa DOM renderer (dom_render_wasm.go) draws a
// scene of at least sceneCanvasMin shapes on a <canvas> instead:
//
//   - encodeScene flattens the scene into a draw list (one record per shape,
//     a "g" record opening and a "/" record closing each group), which the
//     painter (scenePainterJS in scene_client.go) decodes in one call and
//     draws in one pass on the next animation frame;
//   - a later render routes the diff patches that fall inside the scene
//     (sceneRoute): a changed shape is re-sent alone
//     (encodeSceneUpdate) and the painter redraws only the region the old and
//     new shape cover; a structural change re-sends the whole list;
//   - the painter hit-tests a pointer event against its scene index (the
//     records' boxes, then the shape's path) and hands the record's index
//     back; sceneChainAt maps it to the shape and its groups, whose handlers
//     get the event exactly as the SVG's DOM bubbling would give it.
//
// The measurements that set the boundary are in
// docs/perf/runs/canvas-20260930/README.md.

// sceneCanvasMin is the shape count from which the Sky.Spa client draws a scene
// on a canvas; a smaller scene would stay SVG. It is 0: no size was measured
// where SVG is faster (canvas-20260930: 10 and 100 shapes tie within a
// millisecond or a frame, and from 1,000 the canvas wins the static render,
// the every-shape-moves frame and the hit test in Chrome and WebKit).
const sceneCanvasMin = 0

// sceneCanvasTags are the elements Std.Ui.Canvas emits inside a scene. A scene
// holding anything else (a hand-written <svg data-sky-scene> with an <image>,
// say) stays SVG: the painter would not draw it.
var sceneCanvasTags = map[string]bool{
	"rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "path": true, "text": true,
	"g": true, "title": true,
}

// sceneCanvasAttrs are the attributes the painter reads, in the order they are
// written to a record.
var sceneCanvasAttrs = []string{
	"x", "y", "width", "height", "cx", "cy", "r", "rx", "ry",
	"x1", "y1", "x2", "y2", "points", "d",
	"fill", "stroke", "stroke-width", "opacity", "font-size", "text-anchor",
	"transform",
}

// Record and field separators of the draw list (ASCII RS and US). Attribute
// values that hold them are cleaned (sceneClean): a colour, a number or a path
// never does, and text content loses nothing a scene can show.
const (
	sceneRS = "\x1e"
	sceneUS = "\x1f"
)

// Event flags of a record, the same bits the painter tests (FLAGS).
const (
	sceneEvClick = 1 << iota
	sceneEvDown
	sceneEvMove
	sceneEvUp
)

// isSceneVNode reports whether n is a Std.Ui.Canvas scene root.
func isSceneVNode(n *VNode) bool {
	return n != nil && n.Kind == "element" && n.Tag == "svg" && n.Attrs["data-sky-scene"] != ""
}

// sceneCanvasShapes counts the drawable shapes under a scene root and reports
// whether the painter can draw every element in it.
func sceneCanvasShapes(n *VNode) (int, bool) {
	count := 0
	ok := true
	var walk func(c *VNode)
	walk = func(c *VNode) {
		for i := range c.Children {
			k := &c.Children[i]
			if k.Kind != "element" {
				continue
			}
			if !sceneCanvasTags[k.Tag] {
				ok = false
				return
			}
			switch k.Tag {
			case "title":
			case "g":
				walk(k)
			default:
				count++
			}
		}
	}
	walk(n)
	return count, ok
}

// sceneWantsCanvas decides a scene's backend on the Sky.Spa client. backend is
// the page's override (Sky.sceneBackend, set by a measurement before the
// client boots): "svg" or "canvas"; anything else is automatic.
func sceneWantsCanvas(n *VNode, backend string) bool {
	if !isSceneVNode(n) || backend == "svg" {
		return false
	}
	count, ok := sceneCanvasShapes(n)
	if !ok {
		return false
	}
	return backend == "canvas" || count >= sceneCanvasMin
}

// sceneSize is the scene's size in scene units (the viewBox) and in CSS
// pixels (width/height); Std.Ui.Canvas writes the same pair to both.
func sceneSize(n *VNode) (w, h float64) {
	w, _ = strconv.ParseFloat(n.Attrs["width"], 64)
	h, _ = strconv.ParseFloat(n.Attrs["height"], 64)
	if vb := strings.Fields(n.Attrs["viewBox"]); len(vb) == 4 {
		if v, err := strconv.ParseFloat(vb[2], 64); err == nil && v > 0 {
			w = v
		}
		if v, err := strconv.ParseFloat(vb[3], 64); err == nil && v > 0 {
			h = v
		}
	}
	if w <= 0 {
		w = 300
	}
	if h <= 0 {
		h = 150
	}
	return w, h
}

// sceneCanvasStyle is the canvas's inline style: the SVG's own (display:
// block, max-width: 100%, height: auto, and touch-action: none on an
// interactive scene) and its CSS width, so the canvas takes the SVG's box and
// scales down the same way.
func sceneCanvasStyle(svgStyle string, cssWidth string) string {
	s := strings.TrimSpace(svgStyle)
	if s != "" && !strings.HasSuffix(s, ";") {
		s += ";"
	}
	if cssWidth != "" {
		s += " width: " + cssWidth + "px;"
	}
	return strings.TrimSpace(s)
}

// sceneEventFlags are the pointer events a scene element listens for.
func sceneEventFlags(ev map[string]any) int {
	f := 0
	if _, ok := ev["click"]; ok {
		f |= sceneEvClick
	}
	if _, ok := ev[islandEventPrefix+"scene-pointerdown"]; ok {
		f |= sceneEvDown
	}
	if _, ok := ev[islandEventPrefix+"scene-pointermove"]; ok {
		f |= sceneEvMove
	}
	if _, ok := ev[islandEventPrefix+"scene-pointerup"]; ok {
		f |= sceneEvUp
	}
	return f
}

// sceneEventKey is the VNode event name of a pointer event type.
func sceneEventKey(typ string) string {
	if typ == "click" {
		return "click"
	}
	return islandEventPrefix + "scene-" + typ
}

func sceneClean(s string) string {
	if !strings.ContainsAny(s, sceneRS+sceneUS) {
		return s
	}
	return strings.NewReplacer(sceneRS, " ", sceneUS, " ").Replace(s)
}

// sceneTextOf is a <text> element's content.
func sceneTextOf(n *VNode) string {
	var b strings.Builder
	for i := range n.Children {
		if n.Children[i].Kind == "text" {
			b.WriteString(n.Children[i].Text)
		}
	}
	return b.String()
}

// writeSceneRecord writes one element's record: tag, event flags, then the
// attributes the painter reads as key/value pairs ("#" is a text's content).
func writeSceneRecord(b *strings.Builder, n *VNode) {
	b.WriteString(n.Tag)
	b.WriteString(sceneUS)
	b.WriteString(strconv.Itoa(sceneEventFlags(n.Events)))
	for _, k := range sceneCanvasAttrs {
		if v, ok := n.Attrs[k]; ok {
			b.WriteString(sceneUS)
			b.WriteString(k)
			b.WriteString(sceneUS)
			b.WriteString(sceneClean(v))
		}
	}
	if n.Tag == "text" {
		b.WriteString(sceneUS + "#" + sceneUS)
		b.WriteString(sceneClean(sceneTextOf(n)))
	}
}

// sceneWalk visits the scene's records in draw order: a shape, or a group
// ("g") followed by its records and a close (nil node, close=true). idx is the
// record's index in the draw list.
func sceneWalk(scene *VNode, visit func(idx int, n *VNode, close bool, chain []*VNode) bool) {
	idx := 0
	var chain []*VNode
	var walk func(c *VNode) bool
	walk = func(c *VNode) bool {
		for i := range c.Children {
			k := &c.Children[i]
			if k.Kind != "element" || k.Tag == "title" {
				continue
			}
			if !visit(idx, k, false, chain) {
				return false
			}
			idx++
			if k.Tag == "g" {
				chain = append(chain, k)
				if !walk(k) {
					return false
				}
				chain = chain[:len(chain)-1]
				if !visit(idx, nil, true, chain) {
					return false
				}
				idx++
			}
		}
		return true
	}
	walk(scene)
}

// encodeScene is the scene's whole draw list.
func encodeScene(scene *VNode) string {
	var b strings.Builder
	first := true
	sceneWalk(scene, func(_ int, n *VNode, close bool, _ []*VNode) bool {
		if !first {
			b.WriteString(sceneRS)
		}
		first = false
		if close {
			b.WriteString("/")
		} else {
			writeSceneRecord(&b, n)
		}
		return true
	})
	return b.String()
}

// encodeSceneUpdate is the records of the changed shapes (by sky-id), each
// prefixed with its index in the draw list. found is how many of ids it
// found; fewer than asked means the list no longer lines up (the caller then
// sends the whole list).
func encodeSceneUpdate(scene *VNode, ids map[string]bool) (string, int) {
	var b strings.Builder
	found := 0
	sceneWalk(scene, func(idx int, n *VNode, close bool, _ []*VNode) bool {
		if close || !ids[n.SkyID] {
			return true
		}
		if found > 0 {
			b.WriteString(sceneRS)
		}
		found++
		b.WriteString(strconv.Itoa(idx))
		b.WriteString(sceneUS)
		writeSceneRecord(&b, n)
		return found < len(ids)
	})
	return b.String(), found
}

// sceneChainAt is the element at draw-list index idx followed by its groups,
// innermost first: the nodes a pointer event on it reaches, in the order the
// SVG's DOM bubbling reaches them. nil when idx is not a shape.
func sceneChainAt(scene *VNode, idx int) []*VNode {
	var out []*VNode
	sceneWalk(scene, func(i int, n *VNode, close bool, chain []*VNode) bool {
		if i != idx {
			return true
		}
		if close || n.Tag == "g" {
			return false
		}
		out = append(out, n)
		for j := len(chain) - 1; j >= 0; j-- {
			out = append(out, chain[j])
		}
		return false
	})
	return out
}

// sceneIndex maps the sky-id of every scene in a tree to its root.
func sceneIndex(root *VNode) map[string]*VNode {
	var out map[string]*VNode
	var walk func(n *VNode)
	walk = func(n *VNode) {
		if n.Kind != "element" {
			return
		}
		if isSceneVNode(n) && n.SkyID != "" {
			if out == nil {
				out = map[string]*VNode{}
			}
			out[n.SkyID] = n
			return
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	if root != nil {
		walk(root)
	}
	return out
}

// sceneOwner is the id of the scene that id is, or is inside ("" if none).
// Sky-ids are paths: a node under the scene "r.0#svg" is "r.0#svg.3#rect".
func sceneOwner(id string, scenes map[string]*VNode) string {
	for s := id; s != ""; {
		if _, ok := scenes[s]; ok {
			return s
		}
		i := strings.LastIndexByte(s, '.')
		if i < 0 {
			return ""
		}
		s = s[:i]
	}
	return ""
}

// sceneRouteKind is what a patch inside a canvas-drawn scene becomes.
type sceneRouteKind int

const (
	// sceneRouteShape re-sends one shape (its attributes or its text changed).
	sceneRouteShape sceneRouteKind = iota
	// sceneRouteFull re-sends the whole draw list (shapes added, removed,
	// reordered, or a group changed).
	sceneRouteFull
	// sceneRouteHost rebuilds the canvas element (the scene's own
	// attributes changed: its size, label or style).
	sceneRouteHost
)

// sceneRoute classifies a patch inside the canvas-drawn scene sid. nodes maps
// the scene's sky-ids to their (new) nodes.
func sceneRoute(p *Patch, sid string, nodes map[string]*VNode) sceneRouteKind {
	if p.ID == sid {
		if p.Attrs != nil || p.Replace != nil {
			return sceneRouteHost
		}
		return sceneRouteFull
	}
	n := nodes[p.ID]
	if n == nil || p.Replace != nil || p.Remove {
		return sceneRouteFull
	}
	switch n.Tag {
	case "text":
		return sceneRouteShape
	case "g", "title":
		return sceneRouteFull
	}
	if p.Kids != nil || p.HTML != nil || p.Text != nil {
		return sceneRouteFull
	}
	return sceneRouteShape
}

// sceneNodes maps every sky-id in a scene to its node.
func sceneNodes(scene *VNode) map[string]*VNode {
	out := map[string]*VNode{}
	var walk func(n *VNode)
	walk = func(n *VNode) {
		if n.SkyID != "" {
			out[n.SkyID] = n
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(scene)
	return out
}

// sceneDetailJSON is the detail of a scene pointer event, {"x":…,"y":…}.
func sceneDetailJSON(x, y float64) string {
	return `{"x":` + strconv.FormatFloat(x, 'f', -1, 64) + `,"y":` + strconv.FormatFloat(y, 'f', -1, 64) + `}`
}
