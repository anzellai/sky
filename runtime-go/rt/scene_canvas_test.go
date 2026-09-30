//go:build !js

package rt

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The canvas backend of Std.Ui.Canvas on the Sky.Spa client: the draw list
// Go makes from a scene (scene_canvas.go) and the painter that draws it
// (scenePainterJS), run for real in node against a recording 2D context.

// canvasTestScene is a 200 x 100 scene shaped the way Std/Ui/Canvas.sky
// builds one: a title, a backdrop with the scene's pointer move, a clickable
// rectangle, a translated clickable group (a circle and a text), a line with
// its current-colour stroke, a translucent group and a stroked path.
func canvasTestScene() VNode {
	el := func(tag string, attrs map[string]string, events map[string]any, kids ...VNode) VNode {
		return VNode{Kind: "element", Tag: tag, Attrs: attrs, Events: events, Children: kids}
	}
	svg := el("svg", map[string]string{
		"data-sky-scene": "1", "xmlns": "http://www.w3.org/2000/svg", "viewBox": "0 0 200 100",
		"width": "200", "height": "100", "role": "img", "aria-label": "Test scene",
		"style": "display: block; max-width: 100%; height: auto; touch-action: none;",
	}, nil,
		el("title", nil, nil, vtext("Test scene")),
		el("rect", map[string]string{"x": "0", "y": "0", "width": "200", "height": "100", "fill": "transparent"},
			map[string]any{islandEventPrefix + "scene-pointermove": "Moved"}),
		el("rect", map[string]string{"x": "10", "y": "10", "width": "40", "height": "20", "fill": "rgb(255,0,0)"},
			map[string]any{"click": "Square"}),
		el("g", map[string]string{"transform": "translate(100 0)"}, map[string]any{"click": "Group"},
			el("circle", map[string]string{"cx": "20", "cy": "50", "r": "10", "fill": "blue"}, nil),
			el("text", map[string]string{"x": "20", "y": "90", "font-size": "10", "text-anchor": "middle"}, nil, vtext("hello")),
		),
		el("line", map[string]string{"x1": "0", "y1": "95", "x2": "200", "y2": "95", "stroke": "currentColor", "stroke-width": "2"}, nil),
		el("g", map[string]string{"opacity": "0.5"}, nil,
			el("rect", map[string]string{"x": "150", "y": "10", "width": "20", "height": "20", "fill": "green"}, nil),
		),
		el("path", map[string]string{"d": "M 60 60 L 80 60 L 70 80 Z", "fill": "black", "stroke": "red", "stroke-width": "1"}, nil),
	)
	assignSkyIDs(&svg, "r")
	return svg
}

func readable(s string) string {
	return strings.NewReplacer(sceneRS, "\n", sceneUS, "|").Replace(s)
}

// The draw list is the scene's shapes in draw order, a record per shape, a
// "g" and a "/" around each group, the title left out; the event flags are
// click 1, pointerdown 2, pointermove 4, pointerup 8.
func TestSceneCanvas_DrawListGolden(t *testing.T) {
	scene := canvasTestScene()
	want := strings.Join([]string{
		"rect|4|x|0|y|0|width|200|height|100|fill|transparent",
		"rect|1|x|10|y|10|width|40|height|20|fill|rgb(255,0,0)",
		"g|1|transform|translate(100 0)",
		"circle|0|cx|20|cy|50|r|10|fill|blue",
		"text|0|x|20|y|90|font-size|10|text-anchor|middle|#|hello",
		"/",
		"line|0|x1|0|y1|95|x2|200|y2|95|stroke|currentColor|stroke-width|2",
		"g|0|opacity|0.5",
		"rect|0|x|150|y|10|width|20|height|20|fill|green",
		"/",
		"path|0|d|M 60 60 L 80 60 L 70 80 Z|fill|black|stroke|red|stroke-width|1",
	}, "\n")
	if got := readable(encodeScene(&scene)); got != want {
		t.Fatalf("draw list:\n%s\nwant:\n%s", got, want)
	}
	// A value holding a separator cannot split a record.
	scene.Children[3].Children[1].Children[0].Text = "a\x1eb\x1fc"
	if got := readable(encodeScene(&scene)); !strings.Contains(got, "|#|a b c\n") {
		t.Fatalf("the separators in a text were not cleaned:\n%s", got)
	}
}

// An update carries each changed shape with its index in the draw list, the
// same index encodeScene gives it; a pointer event's index maps back to the
// shape and its groups, innermost first.
func TestSceneCanvas_UpdateIndexAndChain(t *testing.T) {
	scene := canvasTestScene()
	circle := scene.Children[3].Children[0].SkyID
	path := scene.Children[6].SkyID
	upd, found := encodeSceneUpdate(&scene, map[string]bool{circle: true, path: true})
	if found != 2 {
		t.Fatalf("found %d of 2", found)
	}
	want := "3|circle|0|cx|20|cy|50|r|10|fill|blue\n10|path|0|d|M 60 60 L 80 60 L 70 80 Z|fill|black|stroke|red|stroke-width|1"
	if got := readable(upd); got != want {
		t.Fatalf("update:\n%s\nwant:\n%s", got, want)
	}
	if _, found := encodeSceneUpdate(&scene, map[string]bool{"r.9#nope": true}); found != 0 {
		t.Fatal("an id that is not in the scene was found")
	}
	chain := sceneChainAt(&scene, 3)
	if len(chain) != 2 || chain[0].Tag != "circle" || chain[1].Tag != "g" {
		t.Fatalf("chain at 3 = %v", chain)
	}
	if c := sceneChainAt(&scene, 1); len(c) != 1 || c[0].Tag != "rect" || c[0].Events["click"] != "Square" {
		t.Fatalf("chain at 1 = %v", c)
	}
	for _, idx := range []int{2, 5, 99, -1} {
		if c := sceneChainAt(&scene, idx); c != nil {
			t.Fatalf("index %d (a group, a close or out of range) gave %v", idx, c)
		}
	}
	if sceneDetailJSON(12.5, 40) != `{"x":12.5,"y":40}` {
		t.Fatal(sceneDetailJSON(12.5, 40))
	}
}

// A patch belongs to the scene whose sky-id is its own or a path prefix at a
// segment boundary, and is routed by what it changes.
func TestSceneCanvas_PatchRouting(t *testing.T) {
	root := VNode{Kind: "element", Tag: "div", Children: []VNode{
		{Kind: "element", Tag: "p"},
		canvasTestScene(),
	}}
	assignSkyIDs(&root, "r")
	scenes := sceneIndex(&root)
	if len(scenes) != 1 {
		t.Fatalf("scenes = %v", scenes)
	}
	var sid string
	for id := range scenes {
		sid = id
	}
	scene := scenes[sid]
	rect := scene.Children[2].SkyID
	text := scene.Children[3].Children[1].SkyID
	group := scene.Children[3].SkyID
	if sceneOwner(rect, scenes) != sid || sceneOwner(sid, scenes) != sid || sceneOwner(text, scenes) != sid {
		t.Fatal("a node in the scene is not owned by it")
	}
	if sceneOwner(root.Children[0].SkyID, scenes) != "" || sceneOwner(sid+"x", scenes) != "" || sceneOwner("r", scenes) != "" {
		t.Fatal("a node outside the scene is owned by it")
	}
	nodes := sceneNodes(scene)
	s := "x"
	cases := []struct {
		name string
		p    Patch
		want sceneRouteKind
	}{
		{"a shape's attributes", Patch{ID: rect, Attrs: map[string]string{"x": "11"}}, sceneRouteShape},
		{"a text's content", Patch{ID: text, Text: &s}, sceneRouteShape},
		{"a text's attributes", Patch{ID: text, Attrs: map[string]string{"x": "1"}}, sceneRouteShape},
		{"a group's attributes", Patch{ID: group, Attrs: map[string]string{"transform": ""}}, sceneRouteFull},
		{"a group's children", Patch{ID: group, Kids: []KidOp{{Keep: text}}}, sceneRouteFull},
		{"the scene's children", Patch{ID: sid, Kids: []KidOp{{Keep: rect}}}, sceneRouteFull},
		{"a removed shape", Patch{ID: rect, Remove: true}, sceneRouteFull},
		{"a replaced shape", Patch{ID: rect, Replace: &s}, sceneRouteFull},
		{"an unknown id", Patch{ID: sid + ".99#rect", Attrs: map[string]string{"x": "1"}}, sceneRouteFull},
		{"the scene's own attributes", Patch{ID: sid, Attrs: map[string]string{"aria-label": "B"}}, sceneRouteHost},
	}
	for _, c := range cases {
		c := c
		if got := sceneRoute(&c.p, sid, nodes); got != c.want {
			t.Errorf("%s: route %d, want %d", c.name, got, c.want)
		}
	}
}

// The backend: canvas for a scene Std.Ui.Canvas made (from sceneCanvasMin
// shapes), SVG under the "svg" override, and SVG for a scene holding an
// element the painter cannot draw.
func TestSceneCanvas_Backend(t *testing.T) {
	scene := canvasTestScene()
	if n, ok := sceneCanvasShapes(&scene); n != 7 || !ok {
		t.Fatalf("shapes = %d, drawable = %v", n, ok)
	}
	if !sceneWantsCanvas(&scene, "") || !sceneWantsCanvas(&scene, "canvas") {
		t.Fatal("a Std.Ui.Canvas scene is not drawn on a canvas")
	}
	if sceneWantsCanvas(&scene, "svg") {
		t.Fatal("the svg override was ignored")
	}
	withImage := canvasTestScene()
	withImage.Children = append(withImage.Children, VNode{Kind: "element", Tag: "image"})
	if sceneWantsCanvas(&withImage, "canvas") {
		t.Fatal("a scene with an <image> went to the canvas")
	}
	plain := VNode{Kind: "element", Tag: "svg", Attrs: map[string]string{"viewBox": "0 0 10 10"}}
	if sceneWantsCanvas(&plain, "canvas") {
		t.Fatal("an <svg> that is not a scene went to the canvas")
	}
	if w, h := sceneSize(&scene); w != 200 || h != 100 {
		t.Fatalf("size %v x %v", w, h)
	}
	if got := sceneCanvasStyle("display: block; max-width: 100%; height: auto", "200"); got != "display: block; max-width: 100%; height: auto; width: 200px;" {
		t.Fatal(got)
	}
}

// scenePainterHarnessJS runs scenePainterJS in node with a stub DOM, a
// recording 2D context and a Path2D that knows rectangles, circles and
// polygons (enough to hit-test the test scene). argv: painter file, draw
// list file.
const scenePainterHarnessJS = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const list = fs.readFileSync(process.argv[3], "utf8");
const fails = [];
let ran = 0;
const ok = (c, m) => { ran++; if (!c) fails.push(m); };
let frames = [];
const flush = () => { const f = frames; frames = []; f.forEach((fn) => fn(0)); };
class Path2D {
  constructor(d) {
    this.ops = [];
    if (typeof d === "string") {
      const t = d.trim().split(/[\s,]+/);
      for (let i = 0; i < t.length;) {
        const c = t[i++];
        if (c === "M") { this.moveTo(+t[i], +t[i + 1]); i += 2; }
        else if (c === "L") { this.lineTo(+t[i], +t[i + 1]); i += 2; }
        else if (c === "Z") this.closePath();
        else throw new Error("stub Path2D: " + c);
      }
    }
  }
  rect(x, y, w, h) { this.ops.push(["rect", x, y, w, h]); }
  arc(x, y, r) { this.ops.push(["arc", x, y, r]); }
  ellipse(x, y, rx, ry) { this.ops.push(["ellipse", x, y, rx, ry]); }
  moveTo(x, y) { this.ops.push(["M", x, y]); }
  lineTo(x, y) { this.ops.push(["L", x, y]); }
  closePath() { this.ops.push(["Z"]); }
}
function local(m, x, y) {
  const det = m[0] * m[3] - m[1] * m[2];
  const dx = x - m[4], dy = y - m[5];
  return [(m[3] * dx - m[2] * dy) / det, (-m[1] * dx + m[0] * dy) / det];
}
function poly(ops) { return ops.filter((o) => o[0] === "M" || o[0] === "L").map((o) => [o[1], o[2]]); }
function inPath(p, m, x, y) {
  const [lx, ly] = local(m, x, y);
  for (const o of p.ops) {
    if (o[0] === "rect" && lx >= o[1] && lx <= o[1] + o[3] && ly >= o[2] && ly <= o[2] + o[4]) return true;
    if (o[0] === "arc" && Math.hypot(lx - o[1], ly - o[2]) <= o[3]) return true;
  }
  const pts = poly(p.ops);
  if (pts.length < 3) return false;
  let inside = false;
  for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
    const [xi, yi] = pts[i], [xj, yj] = pts[j];
    if ((yi > ly) !== (yj > ly) && lx < ((xj - xi) * (ly - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}
function onStroke(p, m, lw, x, y) {
  const [lx, ly] = local(m, x, y);
  const pts = poly(p.ops);
  for (let i = 1; i < pts.length; i++) {
    const [ax, ay] = pts[i - 1], [bx, by] = pts[i];
    const l2 = (bx - ax) ** 2 + (by - ay) ** 2;
    const t = l2 ? Math.max(0, Math.min(1, ((lx - ax) * (bx - ax) + (ly - ay) * (by - ay)) / l2)) : 0;
    if (Math.hypot(lx - (ax + t * (bx - ax)), ly - (ay + t * (by - ay))) <= lw / 2) return true;
  }
  return false;
}
let ids = 0;
class Ctx {
  constructor(cv) { this.canvas = cv; this.log = []; this.m = [1, 0, 0, 1, 0, 0]; this.font = "10px sans-serif"; this.globalAlpha = 1; this.lineWidth = 1; }
  setTransform(a, b, c, d, e, f) { this.m = [a, b, c, d, e, f]; this.log.push(["setTransform", a, b, c, d, e, f]); }
  save() { this.log.push(["save"]); }
  restore() { this.log.push(["restore"]); }
  beginPath() { this.log.push(["beginPath"]); }
  rect(x, y, w, h) { this.log.push(["rect", x, y, w, h]); }
  clip() { this.log.push(["clip"]); }
  clearRect(x, y, w, h) { this.log.push(["clearRect", x, y, w, h]); }
  fill(p) { this.log.push(["fill", this.fillStyle, this.globalAlpha, JSON.stringify(p.ops)]); }
  stroke(p) { this.log.push(["stroke", this.strokeStyle, this.lineWidth, JSON.stringify(p.ops)]); }
  fillText(t, x, y) { this.log.push(["fillText", t, x, y, this.font, this.textAlign, this.fillStyle]); }
  strokeText(t, x, y) { this.log.push(["strokeText", t, x, y]); }
  measureText(t) { return { width: t.length * parseFloat(this.font) * 0.5 }; }
  drawImage(img) { this.log.push(["drawImage", img.id, this.globalAlpha]); }
  isPointInPath(p, x, y) { return inPath(p, this.m, x, y); }
  isPointInStroke(p, x, y) { return onStroke(p, this.m, this.lineWidth, x, y); }
}
class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.attrs = {}; this.children = []; this.listeners = {}; this.isConnected = true; this.width = 300; this.height = 150; this.textContent = ""; this.id = "el" + (++ids); this.box = { left: 0, top: 0, width: 0, height: 0 }; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  appendChild(c) { this.children.push(c); c.parentNode = this; return c; }
  addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); }
  getContext() { return this.ctx || (this.ctx = new Ctx(this)); }
  getBoundingClientRect() { return this.box; }
  fire(t, cx, cy) { (this.listeners[t] || []).forEach((f) => f({ type: t, clientX: cx, clientY: cy })); }
}
const dispatched = [];
const sandbox = {
  document: { createElement: (t) => new El(t) },
  requestAnimationFrame(fn) { frames.push(fn); return frames.length; },
  setTimeout(fn) { frames.push(fn); return 1; },
  getComputedStyle: () => ({ fontFamily: "TestSans", color: "rgb(1, 2, 3)" }),
  devicePixelRatio: 2,
  Path2D, Math, String, JSON, parseFloat, isFinite,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const P = sandbox.Sky.sceneCanvas;
sandbox.Sky.__sceneDispatch = (id, i, type, x, y) => dispatched.push([id, i, type, x, y].join(" "));
ok(P && typeof P.mount === "function", "window.Sky.sceneCanvas.mount is defined");

const cv = new El("canvas");
cv.setAttribute("sky-id", "r.1#svg");
ok(P.mount(cv, 200, 100, "Test scene", list), "mount returns true");
// Crispness: the backing store is the scene's CSS size in device pixels.
ok(cv.width === 400 && cv.height === 200, "backing store 400x200 at devicePixelRatio 2, got " + cv.width + "x" + cv.height);
// The text alternative.
const desc = cv.children[0];
ok(desc && desc.tagName === "P" && cv.getAttribute("aria-describedby") === desc.attrs.id, "aria-describedby names the description");
ok(desc && desc.textContent === "Test scene. Text in the scene: hello.", "description: " + (desc && desc.textContent));

// Nothing is drawn until the animation frame, then one full pass.
const ctx = cv.getContext("2d");
ok(ctx.log.length === 0, "mount drew before the frame");
flush();
let st = P.stats(cv);
ok(st.paints === 1 && st.full === 1 && st.drawn === 7, "first frame: one full pass over 7 shapes, got " + JSON.stringify(st));
const log = ctx.log.map((e) => JSON.stringify(e));
const has = (e) => log.indexOf(JSON.stringify(e)) >= 0;
ok(has(["clearRect", 0, 0, 400, 200]), "the full pass clears the canvas");
ok(has(["setTransform", 2, 0, 0, 2, 0, 0]) && has(["fill", "rgb(255,0,0)", 1, JSON.stringify([["rect", 10, 10, 40, 20]])]), "the red rectangle, scaled by the device pixel ratio");
ok(has(["setTransform", 2, 0, 0, 2, 200, 0]) && has(["fill", "blue", 1, JSON.stringify([["arc", 20, 50, 10]])]), "the circle, under its group's translate(100 0)");
ok(has(["fillText", "hello", 20, 90, "10px TestSans", "center", "black"]), "the text: size, family of the page, middle anchor, inherited black fill");
ok(has(["stroke", "rgb(1, 2, 3)", 2, JSON.stringify([["M", 0, 95], ["L", 200, 95]])]), "the line: current colour stroke, width 2");
ok(log.some((e) => e.startsWith('["drawImage"') && e.endsWith(",0.5]")), "the translucent group is composited from a layer at opacity 0.5");
ok(has(["stroke", "red", 1, JSON.stringify([["M", 60, 60], ["L", 80, 60], ["L", 70, 80], ["Z"]])]), "the path's stroke over its fill");
ok(log.filter((e) => e.startsWith('["fill"')).length === 4 && !log.some((e) => e.startsWith('["fill"') && e.includes("95")), "four fills on the canvas (backdrop, red, circle, path; the green one is in the layer), and none for the line, which has no area");

// One pass per frame: several updates before a frame cost one paint.
ctx.log = [];
P.set(cv, list);
P.update(cv, "3\u001fcircle\u001f0\u001fcx\u001f30\u001fcy\u001f50\u001fr\u001f10\u001ffill\u001fblue");
P.update(cv, "3\u001fcircle\u001f0\u001fcx\u001f25\u001fcy\u001f50\u001fr\u001f10\u001ffill\u001fblue");
ok(ctx.log.length === 0, "set and update drew before the frame");
ok(frames.length === 1, "three calls in a frame asked for " + frames.length + " animation frames, want 1");
flush();
st = P.stats(cv);
ok(st.paints === 2 && ctx.log.filter((e) => e[0] === "clearRect").length === 1, "three calls in a frame: one draw pass, got " + JSON.stringify(st));
flush();
ok(P.stats(cv).paints === 2, "an idle frame drew");

// A changed shape redraws only its region: old and new box, clipped.
ctx.log = [];
P.update(cv, "3\u001fcircle\u001f0\u001fcx\u001f30\u001fcy\u001f50\u001fr\u001f10\u001ffill\u001fblue");
flush();
st = P.stats(cv);
const clip = ctx.log.find((e) => e[0] === "rect");
ok(st.partial === 1 && st.drawn === 2, "one partial pass drawing the backdrop and the circle, got " + JSON.stringify(st));
ok(clip && ctx.log.some((e) => e[0] === "clip"), "the partial pass is clipped");
// Old circle x 115..135, new 120..140 (scene), y 40..60, grown by 1 unit, in device pixels.
ok(clip && clip[1] <= 228 && clip[1] + clip[3] >= 282 && clip[2] <= 78 && clip[2] + clip[4] >= 122 && clip[3] < 80, "the clip covers the old and new circle and little more: " + JSON.stringify(clip));
ok(!ctx.log.some((e) => e[0] === "fillText"), "the text outside the region was not redrawn");

// Hit tests in scene units: the topmost shape, its transform, its stroke.
const hit = (x, y) => P.hitTest(cv, x, y);
ok(hit(20, 20) === 1, "(20,20) is the red rectangle: " + hit(20, 20));
ok(hit(130, 50) === 3, "(130,50) is the moved circle: " + hit(130, 50));
ok(hit(5, 5) === 0, "(5,5) is the backdrop: " + hit(5, 5));
ok(hit(120, 87) === 4, "(120,87) is the text: " + hit(120, 87));
ok(hit(100, 95.5) === 6, "(100,95.5) is on the line's stroke: " + hit(100, 95.5));
ok(hit(160, 20) === 8, "(160,20) is the translucent group's rectangle: " + hit(160, 20));
ok(hit(70, 65) === 10, "(70,65) is inside the path: " + hit(70, 65));
ok(hit(300, 300) === -1, "a point off the scene hits nothing: " + hit(300, 300));

// Pointer events: the canvas drawn at half size, 100x50 at (10,20).
cv.box = { left: 10, top: 20, width: 100, height: 50 };
cv.fire("click", 20, 25);
ok(dispatched.join(";") === "r.1#svg 1 click 20 10", "a click on the rectangle, in scene units: " + dispatched.join(";"));
dispatched.length = 0;
cv.fire("click", 75, 45);
ok(dispatched.join(";") === "r.1#svg 3 click 130 50", "a click on the circle reaches its group's handler: " + dispatched.join(";"));
dispatched.length = 0;
cv.fire("click", 12, 22);
ok(dispatched.length === 0, "a click on the backdrop, which listens only for moves, dispatched " + dispatched.join(";"));
cv.fire("pointermove", 12, 22);
cv.fire("pointermove", 14, 24);
ok(dispatched.length === 0, "a move dispatched before the frame");
flush();
ok(dispatched.join(";") === "r.1#svg 0 pointermove 8 8", "two moves in a frame: one pointermove at the last position: " + dispatched.join(";"));
dispatched.length = 0;
cv.fire("pointerdown", 20, 25);
ok(dispatched.length === 0, "a pointerdown on a shape that does not listen for it dispatched " + dispatched.join(";"));

// A text change updates the text alternative.
P.update(cv, "4\u001ftext\u001f0\u001fx\u001f20\u001fy\u001f90\u001ffont-size\u001f10\u001ftext-anchor\u001fmiddle\u001f#\u001fbye");
ok(desc.textContent === "Test scene. Text in the scene: bye.", "description after a text change: " + desc.textContent);
flush();

// A devicePixelRatio change (a zoom, another screen) resizes and redraws all.
sandbox.devicePixelRatio = 3;
P.update(cv, "3\u001fcircle\u001f0\u001fcx\u001f20\u001fcy\u001f50\u001fr\u001f10\u001ffill\u001fblue");
ctx.log = [];
flush();
st = P.stats(cv);
ok(cv.width === 600 && cv.height === 300 && ctx.log.some((e) => e[0] === "clearRect" && e[3] === 600), "resized to 600x300 and fully redrawn at ratio 3, " + cv.width + "x" + cv.height);

// Many changed shapes: one full pass, not a clip.
sandbox.devicePixelRatio = 2;
P.set(cv, list);
flush();
const before = P.stats(cv).full;
let many = [];
for (const i of [0, 1, 3, 8]) many.push(i + "\u001frect\u001f0\u001fx\u001f0\u001fy\u001f0\u001fwidth\u001f190\u001fheight\u001f90\u001ffill\u001fred");
P.update(cv, many.join("\u001e"));
flush();
ok(P.stats(cv).full === before + 1, "an update over most of the scene is a full pass");
process.stdout.write(JSON.stringify({ ran, fails }));
`

// TestScenePainterJS runs the painter on the draw list Go makes for the test
// scene: the list decodes to the shapes, drawn once per animation frame, a
// changed shape redraws only its region, hit tests follow transforms and
// strokes, pointer events map to scene units and reach the shape's groups,
// the text alternative follows the texts, and the backing store follows the
// device pixel ratio.
func TestScenePainterJS(t *testing.T) {
	node := requireNode(t)
	if strings.Contains(scenePainterJS, "`") {
		t.Fatal("scenePainterJS must hold no backquote (the Rust build reads it as a raw literal)")
	}
	dir := t.TempDir()
	scene := canvasTestScene()
	files := map[string]string{
		"painter.js": scenePainterJS,
		"list.txt":   encodeScene(&scene),
		"harness.js": scenePainterHarnessJS,
	}
	for name, body := range files {
		if err := os.WriteFile(dir+"/"+name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(node, dir+"/harness.js", dir+"/painter.js", dir+"/list.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	var res struct {
		Ran   int      `json:"ran"`
		Fails []string `json:"fails"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("harness output is not JSON: %v\n%s", err, out)
	}
	if res.Ran < 30 {
		t.Fatalf("the painter harness ran %d assertions, want at least 30", res.Ran)
	}
	for _, f := range res.Fails {
		t.Error(f)
	}
}
