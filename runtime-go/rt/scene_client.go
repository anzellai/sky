package rt

// sceneClientJS is the pointer runtime of Std.Ui.Canvas scenes. Both clients
// carry it right after the widget-island runtime (islandClientJS): the Sky.Live
// client (liveClientJS) and the Sky.Spa boot loader (SpaBootJS). The Rust
// build reads the Sky.Spa copy from this file (main.rs spa_boot_js), so this
// literal must hold no backquote.
//
// A scene is an <svg data-sky-scene> (Std/Ui/Canvas.sky). A shape with pointer
// handlers carries data-sky-scene-ev="pointermove pointerdown ..." and a typed
// island-event handler for "skyisland-scene-<type>". This runtime listens for
// the native pointer events once, on the document, maps the pointer to scene
// units with the scene's screen transform (getScreenCTM, so a scene scaled
// down to fit its parent still reports scene units), and dispatches the
// CustomEvent "skyisland-scene-<type>" with detail {x, y} on the shape. The
// event bubbles, so a handler on a group or on the scene backdrop receives it
// too. The Live client and the Spa wasm client then decode the detail with the
// Sky decoder exactly like a widget-island event (island_core.go): a payload
// that does not decode is logged and dropped.
//
// pointermove is coalesced to one event per animation frame (the last
// position wins), so a moving mouse costs at most one message a frame.
const sceneClientJS = `// Sky scene pointer events (runtime-go/rt/scene_client.go): Std.Ui.Canvas.
(function () {
  "use strict";
  var w = typeof window !== "undefined" ? window : this;
  var Sky = w.Sky = w.Sky || {};
  if (Sky.__scene) return;
  Sky.__scene = true;
  var doc = w.document;
  var PREFIX = "skyisland-scene-";
  var EV = "data-sky-scene-ev";
  function sceneOf(n) {
    for (; n && n.nodeType === 1; n = n.parentNode) {
      if (n.getAttribute && n.getAttribute("data-sky-scene") !== null &&
          String(n.tagName).toLowerCase() === "svg") return n;
    }
    return null;
  }
  function wants(n, type, svg) {
    for (; n && n.nodeType === 1; n = n.parentNode) {
      var v = n.getAttribute ? n.getAttribute(EV) : null;
      if (v && (" " + v + " ").indexOf(" " + type + " ") >= 0) return true;
      if (n === svg) break;
    }
    return false;
  }
  function round(v) { return Math.round(v * 100) / 100; }
  // toScene maps a viewport point to the scene's own units.
  function toScene(svg, cx, cy) {
    var m = svg.getScreenCTM ? svg.getScreenCTM() : null;
    if (m && svg.createSVGPoint) {
      var p = svg.createSVGPoint();
      p.x = cx; p.y = cy;
      var q = p.matrixTransform(m.inverse());
      return { x: round(q.x), y: round(q.y) };
    }
    var r = svg.getBoundingClientRect();
    var vb = svg.viewBox && svg.viewBox.baseVal;
    var sx = vb && r.width ? vb.width / r.width : 1;
    var sy = vb && r.height ? vb.height / r.height : 1;
    return { x: round((cx - r.left) * sx + (vb ? vb.x : 0)), y: round((cy - r.top) * sy + (vb ? vb.y : 0)) };
  }
  function fire(target, type, pt) {
    var ev;
    try { ev = new w.CustomEvent(PREFIX + type, { detail: pt, bubbles: true }); } catch (_) { return; }
    target.dispatchEvent(ev);
  }
  var pending = null, scheduled = false;
  function flushMove() {
    scheduled = false;
    var p = pending;
    pending = null;
    if (p && p.t.isConnected) fire(p.t, "pointermove", p.pt);
  }
  function listen(type) {
    doc.addEventListener(type, function (ev) {
      var t = ev.target;
      var svg = sceneOf(t);
      if (!svg || !wants(t, type, svg)) return;
      var pt = toScene(svg, ev.clientX, ev.clientY);
      if (type !== "pointermove") {
        fire(t, type, pt);
        return;
      }
      pending = { t: t, pt: pt };
      if (scheduled) return;
      scheduled = true;
      if (w.requestAnimationFrame) w.requestAnimationFrame(flushMove); else w.setTimeout(flushMove, 16);
    }, true);
  }
  if (doc && doc.addEventListener) {
    listen("pointerdown");
    listen("pointermove");
    listen("pointerup");
  }
  Sky.__sceneToPoint = toScene;
})();
`
