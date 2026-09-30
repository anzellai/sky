//go:build !js

package rt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Std.Ui.Canvas pointer runtime (scene_client.go), run for real in node
// against a stub DOM:
//
//  1. a pointerdown on a shape that asked for it becomes the CustomEvent
//     "skyisland-scene-pointerdown" on the shape, bubbling, with the pointer
//     mapped to SCENE units through the scene's screen transform (a scene
//     drawn at half size reports doubled coordinates);
//  2. a pointer event on a shape that did not ask for it, or outside any
//     scene, dispatches nothing;
//  3. a handler on a group ancestor counts (the marker is looked up the
//     ancestor chain up to the <svg>);
//  4. pointermove is coalesced: two moves in one frame dispatch ONE event,
//     with the last position;
//  5. without getScreenCTM the fallback maps through the bounding box and the
//     viewBox.
//
// Skips when node is absent, like TestIslandJS_ClientRuntime.
func TestSceneJS_PointerRuntime(t *testing.T) {
	node := requireNode(t)
	if strings.Contains(sceneClientJS, "`") {
		t.Fatal("sceneClientJS must hold no backquote (the Rust build reads it as a raw literal)")
	}
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
class CustomEvent { constructor(type, init) { this.type = type; this.detail = init.detail; this.bubbles = !!init.bubbles; } }
class El {
  constructor(tag, attrs, parent) {
    this.nodeType = 1; this.tagName = tag; this._a = Object.assign({}, attrs || {});
    this.parentNode = parent || null; this.isConnected = true; this.events = [];
  }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k) ? this._a[k] : null; }
  dispatchEvent(ev) { this.events.push(ev); return true; }
}
const listeners = {};
let frames = [];
const sandbox = {
  document: { addEventListener(type, fn, capture) { listeners[type] = fn; } },
  requestAnimationFrame(fn) { frames.push(fn); return frames.length; },
  setTimeout(fn) { frames.push(fn); return 1; },
  CustomEvent, Math, String,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const fail = (m) => { console.log("FAIL " + m); process.exitCode = 1; };
const ok = (c, m) => { if (!c) fail(m); };

// A 200x100 scene drawn at half size (100x50 CSS px) at viewport (10, 20).
const svg = new El("svg", { "data-sky-scene": "1" });
svg.getScreenCTM = () => ({
  inverse: () => ({ a: 2, d: 2, e: -20, f: -40 }),
});
svg.createSVGPoint = () => ({
  x: 0, y: 0,
  matrixTransform(m) { return { x: this.x * m.a + m.e, y: this.y * m.d + m.f }; },
});
const hot = new El("circle", { "data-sky-scene-ev": "pointerdown pointermove" }, svg);
const cold = new El("rect", {}, svg);
const grp = new El("g", { "data-sky-scene-ev": "pointerup" }, svg);
const inGroup = new El("rect", {}, grp);
const outside = new El("div", { "data-sky-scene-ev": "pointerdown" }, null);

ok(typeof listeners.pointerdown === "function" && typeof listeners.pointermove === "function" &&
   typeof listeners.pointerup === "function", "the runtime listens for pointerdown/move/up");

listeners.pointerdown({ target: hot, clientX: 60, clientY: 45 });
ok(hot.events.length === 1, "pointerdown on a shape that asked for it dispatches once");
const e = hot.events[0] || {};
ok(e.type === "skyisland-scene-pointerdown", "the event is skyisland-scene-pointerdown, got " + e.type);
ok(e.bubbles === true, "the scene event bubbles (a group handler receives it)");
ok(e.detail && e.detail.x === 100 && e.detail.y === 50,
   "(60,45) on a half-size scene at (10,20) maps to scene (100,50), got " + JSON.stringify(e.detail));

listeners.pointerdown({ target: cold, clientX: 60, clientY: 45 });
ok(cold.events.length === 0, "a shape without the marker gets nothing");
listeners.pointerdown({ target: outside, clientX: 1, clientY: 1 });
ok(outside.events.length === 0, "an element outside any scene gets nothing");

listeners.pointerup({ target: inGroup, clientX: 20, clientY: 30 });
ok(inGroup.events.length === 1 && inGroup.events[0].type === "skyisland-scene-pointerup",
   "a pointerup inside a group that asked for it is dispatched on the target");
ok(inGroup.events[0] && inGroup.events[0].detail.x === 20 && inGroup.events[0].detail.y === 20,
   "the group case maps (20,30) to (20,20), got " + JSON.stringify(inGroup.events[0] && inGroup.events[0].detail));
listeners.pointerdown({ target: inGroup, clientX: 20, clientY: 30 });
ok(inGroup.events.length === 1, "the group asked for pointerup only, so a pointerdown is not dispatched");

listeners.pointermove({ target: hot, clientX: 11, clientY: 21 });
listeners.pointermove({ target: hot, clientX: 12, clientY: 22 });
ok(hot.events.length === 1, "moves wait for the next frame");
ok(frames.length === 1, "two moves in one frame schedule ONE flush, got " + frames.length);
frames.shift()();
ok(hot.events.length === 2 && hot.events[1].type === "skyisland-scene-pointermove",
   "the frame flush dispatches one pointermove");
ok(hot.events[1] && hot.events[1].detail.x === 4 && hot.events[1].detail.y === 4,
   "the LAST move wins: (12,22) maps to (4,4), got " + JSON.stringify(hot.events[1] && hot.events[1].detail));
listeners.pointermove({ target: hot, clientX: 13, clientY: 23 });
ok(frames.length === 1, "a move after the flush schedules a new frame");

// The fallback without getScreenCTM: the bounding box and the viewBox.
const svg2 = new El("svg", { "data-sky-scene": "1" });
svg2.getBoundingClientRect = () => ({ left: 100, top: 0, width: 50, height: 25 });
svg2.viewBox = { baseVal: { x: 0, y: 0, width: 200, height: 100 } };
const s2 = new El("rect", { "data-sky-scene-ev": "pointerdown" }, svg2);
listeners.pointerdown({ target: s2, clientX: 125, clientY: 5 });
ok(s2.events[0] && s2.events[0].detail.x === 100 && s2.events[0].detail.y === 20,
   "the fallback maps (125,5) to (100,20), got " + JSON.stringify(s2.events[0] && s2.events[0].detail));
if (!process.exitCode) console.log("ALL OK");
`
	dir := t.TempDir()
	js := filepath.Join(dir, "scene.js")
	hp := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(js, []byte(sceneClientJS), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, hp, js).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ALL OK") {
		t.Fatalf("scene client runtime failed (%v):\n%s", err, out)
	}
}

// Both clients carry the scene runtime and the terminal widget right after
// the island runtime, so a scene's pointer events and a Std.Ui.Terminal work
// on Sky.Live and Sky.Spa alike. Only the Sky.Spa boot file carries the
// canvas painter (Sky.Live draws scenes as SVG), between the two.
func TestSceneAndTerminal_ShipInBothClients(t *testing.T) {
	if !strings.HasPrefix(liveClientJS, islandClientJS+sceneClientJS+terminalWidgetJS) {
		t.Fatal("the Sky.Live client does not start with the island, scene and terminal runtimes")
	}
	if strings.Contains(liveClientJS, "Sky.sceneCanvas") {
		t.Fatal("the Sky.Live client carries the canvas painter (Sky.Live draws scenes as SVG)")
	}
	if !strings.HasPrefix(SpaBootJS, islandClientJS+sceneClientJS+scenePainterJS+terminalWidgetJS) {
		t.Fatal("the Sky.Spa boot file does not start with the island, scene, painter and terminal runtimes")
	}
}
