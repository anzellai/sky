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

// scenePainterJS is the canvas backend of Std.Ui.Canvas scenes on the Sky.Spa
// client (scene_canvas.go has the design and the Go side). Only the Sky.Spa
// boot loader carries it (SpaBootJS): Sky.Live draws scenes as SVG. The Rust
// build reads it from this file too (main.rs spa_boot_js), so this literal
// must hold no backquote.
//
// window.Sky.sceneCanvas:
//
//   - mount(canvas, w, h, label, list) takes a scene w x h scene units and its
//     draw list, sets the backing store to w x h device pixels
//     (devicePixelRatio, re-checked every paint), adds the text alternative
//     (aria-describedby: the label and the scene's texts) and listens for
//     pointer events;
//   - set(canvas, list) replaces the draw list: the next frame redraws all;
//   - update(canvas, entries) replaces records by index: the next frame
//     redraws only the region their old and new boxes cover, clipped;
//   - hitTest(canvas, x, y) is the index of the topmost shape at a scene
//     point: a box test, then the shape's path (fill and stroke, the way SVG
//     hit-tests a painted shape), or -1;
//   - stats(canvas) counts paints, for the tests and the benchmark.
//
// Every call between two animation frames costs one draw pass on the next.
// A pointer event on a shape that listens for it (or has a group that does)
// calls Sky.__sceneDispatch(scene sky-id, index, type, x, y), installed by
// the wasm client; pointermove is coalesced to one per animation frame.
const scenePainterJS = `// Sky scene canvas painter (runtime-go/rt/scene_client.go): Std.Ui.Canvas on the Sky.Spa client.
(function () {
  "use strict";
  var w = typeof window !== "undefined" ? window : this;
  var Sky = w.Sky = w.Sky || {};
  if (Sky.sceneCanvas) return;
  var doc = w.document;
  var RS = "\u001e", US = "\u001f";
  var FLAGS = { click: 1, pointerdown: 2, pointermove: 4, pointerup: 8 };
  var ROOT = { ctm: [1, 0, 0, 1, 0, 0], fill: "black", stroke: "none", sw: 1, fs: 16, anchor: "start", wants: 0 };
  var DESC_TEXTS = 100;
  var seq = 0, hitCtx = null;
  function raf(f) { return w.requestAnimationFrame ? w.requestAnimationFrame(f) : w.setTimeout(f, 16); }
  function num(v, d) { var n = parseFloat(v); return isFinite(n) ? n : d; }
  function mul(m, n) {
    return [m[0] * n[0] + m[2] * n[1], m[1] * n[0] + m[3] * n[1],
      m[0] * n[2] + m[2] * n[3], m[1] * n[2] + m[3] * n[3],
      m[0] * n[4] + m[2] * n[5] + m[4], m[1] * n[4] + m[3] * n[5] + m[5]];
  }
  function apply(m, x, y) { return [m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]]; }
  function invert(m) {
    var det = m[0] * m[3] - m[1] * m[2];
    if (!det) return null;
    return [m[3] / det, -m[1] / det, -m[2] / det, m[0] / det,
      (m[2] * m[5] - m[3] * m[4]) / det, (m[1] * m[4] - m[0] * m[5]) / det];
  }
  function numbers(s) {
    var out = [], re = /[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?/g, r;
    while ((r = re.exec(s))) out.push(parseFloat(r[0]));
    return out;
  }
  // An SVG transform list, left to right: "translate(a b) rotate(c) scale(d e)".
  function parseTransform(s) {
    var m = [1, 0, 0, 1, 0, 0], re = /(translate|rotate|scale|matrix)\s*\(([^)]*)\)/g, r;
    while ((r = re.exec(s))) {
      var a = numbers(r[2]), t;
      if (r[1] === "translate") t = [1, 0, 0, 1, a[0] || 0, a[1] || 0];
      else if (r[1] === "scale") t = [a.length ? a[0] : 1, 0, 0, a.length > 1 ? a[1] : (a.length ? a[0] : 1), 0, 0];
      else if (r[1] === "rotate") {
        var rad = (a[0] || 0) * Math.PI / 180, c = Math.cos(rad), sn = Math.sin(rad);
        t = [c, sn, -sn, c, 0, 0];
        if (a.length >= 3) t = mul(mul([1, 0, 0, 1, a[1], a[2]], t), [1, 0, 0, 1, -a[1], -a[2]]);
      } else if (a.length >= 6) t = a.slice(0, 6);
      else continue;
      m = mul(m, t);
    }
    return m;
  }
  // The box of a path's absolute commands (every point and control point, an
  // arc grown by its radii), or null when it has a relative command.
  function pathBox(d) {
    var re = /([A-Za-z])|([-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?)/g, r, cmds = [], cur = null;
    while ((r = re.exec(d))) {
      if (r[1]) { cur = { c: r[1], a: [] }; cmds.push(cur); } else if (cur) cur.a.push(parseFloat(r[2]));
    }
    var box = null, px = 0, py = 0;
    function add(x, y, g) {
      g = g || 0;
      if (!box) box = [x - g, y - g, x + g, y + g];
      else { box[0] = Math.min(box[0], x - g); box[1] = Math.min(box[1], y - g); box[2] = Math.max(box[2], x + g); box[3] = Math.max(box[3], y + g); }
    }
    for (var i = 0; i < cmds.length; i++) {
      var c = cmds[i].c, a = cmds[i].a, j;
      if (c === "Z") continue;
      if (c === "M" || c === "L") { for (j = 0; j + 1 < a.length; j += 2) { add(a[j], a[j + 1]); px = a[j]; py = a[j + 1]; } }
      else if (c === "Q" || c === "C") { for (j = 0; j + 1 < a.length; j += 2) add(a[j], a[j + 1]); if (a.length >= 2) { px = a[a.length - 2]; py = a[a.length - 1]; } }
      else if (c === "A") {
        for (j = 0; j + 6 < a.length; j += 7) {
          var ex = a[j + 5], ey = a[j + 6], g = Math.max(Math.abs(a[j]), Math.abs(a[j + 1]), Math.sqrt((ex - px) * (ex - px) + (ey - py) * (ey - py)));
          add(px, py, g); add(ex, ey, g); px = ex; py = ey;
        }
      } else return null;
    }
    return box;
  }
  function scaleOf(m) { return Math.max(Math.sqrt(m[0] * m[0] + m[1] * m[1]), Math.sqrt(m[2] * m[2] + m[3] * m[3])); }
  function font(s, r) { return r.fs + "px " + s.family; }
  // derive fills in a record from its attributes and its group (parent):
  // the transform, inherited paint, the path and the scene box.
  function derive(s, r, parent) {
    var a = r.a;
    r.ctm = a.transform !== undefined ? mul(parent.ctm, parseTransform(a.transform)) : parent.ctm;
    r.fill = a.fill !== undefined ? a.fill : parent.fill;
    r.stroke = a.stroke !== undefined ? a.stroke : parent.stroke;
    r.sw = a["stroke-width"] !== undefined ? num(a["stroke-width"], 1) : parent.sw;
    r.fs = a["font-size"] !== undefined ? num(a["font-size"], 16) : parent.fs;
    r.anchor = a["text-anchor"] !== undefined ? a["text-anchor"] : parent.anchor;
    r.op = a.opacity !== undefined ? Math.max(0, Math.min(1, num(a.opacity, 1))) : 1;
    r.wants = r.flags | parent.wants;
    r.path = null; r.box = null; r.local = null; r.draw = false;
    if (r.tag === "g" || r.tag === "/") return;
    geometry(s, r);
  }
  function geometry(s, r) {
    var a = r.a, t = r.tag, P = w.Path2D, p = null, box = null, known = true;
    if (t === "rect") {
      var x = num(a.x, 0), y = num(a.y, 0), wd = num(a.width, 0), ht = num(a.height, 0);
      if (wd > 0 && ht > 0) { p = new P(); p.rect(x, y, wd, ht); box = [x, y, x + wd, y + ht]; }
    } else if (t === "circle") {
      var cx = num(a.cx, 0), cy = num(a.cy, 0), rr = num(a.r, 0);
      if (rr > 0) { p = new P(); p.arc(cx, cy, rr, 0, 2 * Math.PI); box = [cx - rr, cy - rr, cx + rr, cy + rr]; }
    } else if (t === "ellipse") {
      var ex = num(a.cx, 0), ey = num(a.cy, 0), rx = num(a.rx, 0), ry = num(a.ry, 0);
      if (rx > 0 && ry > 0) { p = new P(); p.ellipse(ex, ey, rx, ry, 0, 0, 2 * Math.PI); box = [ex - rx, ey - ry, ex + rx, ey + ry]; }
    } else if (t === "line") {
      var x1 = num(a.x1, 0), y1 = num(a.y1, 0), x2 = num(a.x2, 0), y2 = num(a.y2, 0);
      p = new P(); p.moveTo(x1, y1); p.lineTo(x2, y2);
      box = [Math.min(x1, x2), Math.min(y1, y2), Math.max(x1, x2), Math.max(y1, y2)];
    } else if (t === "polyline" || t === "polygon") {
      var n = numbers(a.points || "");
      if (n.length >= 4) {
        p = new P(); p.moveTo(n[0], n[1]); box = [n[0], n[1], n[0], n[1]];
        for (var i = 2; i + 1 < n.length; i += 2) {
          p.lineTo(n[i], n[i + 1]);
          box[0] = Math.min(box[0], n[i]); box[1] = Math.min(box[1], n[i + 1]);
          box[2] = Math.max(box[2], n[i]); box[3] = Math.max(box[3], n[i + 1]);
        }
        if (t === "polygon") p.closePath();
      }
    } else if (t === "path") {
      var d = a.d || "";
      try { p = new P(d); } catch (_) { p = null; }
      if (p) { box = pathBox(d); known = !!box; }
    } else if (t === "text") {
      r.text = a["#"] || "";
      if (r.text) {
        s.ctx.font = font(s, r);
        var tw = s.ctx.measureText(r.text).width, tx = num(a.x, 0), ty = num(a.y, 0);
        var off = r.anchor === "middle" ? tw / 2 : (r.anchor === "end" ? tw : 0);
        box = [tx - off, ty - r.fs, tx - off + tw, ty + r.fs * 0.3];
        r.local = box;
      }
    }
    r.path = p;
    r.draw = !!(p || (t === "text" && r.text));
    if (!r.draw) return;
    if (!known) { r.box = null; return; }
    var m = r.ctm, c1 = apply(m, box[0], box[1]), c2 = apply(m, box[2], box[1]), c3 = apply(m, box[0], box[3]), c4 = apply(m, box[2], box[3]);
    var g = (r.stroke !== "none" ? r.sw * 2 : 0) * scaleOf(m) + 1;
    r.box = [Math.min(c1[0], c2[0], c3[0], c4[0]) - g, Math.min(c1[1], c2[1], c3[1], c4[1]) - g,
      Math.max(c1[0], c2[0], c3[0], c4[0]) + g, Math.max(c1[1], c2[1], c3[1], c4[1]) + g];
  }
  function decode(f, at) {
    var r = { tag: f[at], flags: +f[at + 1] || 0, a: {} };
    for (var i = at + 2; i + 1 < f.length; i += 2) r.a[f[i]] = f[i + 1];
    return r;
  }
  function decodeAll(s, list) {
    var out = [], stack = [], parts = list ? list.split(RS) : [];
    for (var i = 0; i < parts.length; i++) {
      var f = parts[i].split(US), r;
      if (f[0] === "/") { r = { tag: "/", flags: 0, a: {}, pi: stack.length ? stack[stack.length - 1] : -1 }; stack.pop(); out.push(r); continue; }
      r = decode(f, 0);
      r.pi = stack.length ? stack[stack.length - 1] : -1;
      derive(s, r, r.pi >= 0 ? out[r.pi] : ROOT);
      out.push(r);
      if (r.tag === "g") stack.push(out.length - 1);
    }
    return out;
  }
  function unite(s, b) {
    if (!b) { s.full = true; return; }
    if (!s.dirty) s.dirty = b.slice();
    else { s.dirty[0] = Math.min(s.dirty[0], b[0]); s.dirty[1] = Math.min(s.dirty[1], b[1]); s.dirty[2] = Math.max(s.dirty[2], b[2]); s.dirty[3] = Math.max(s.dirty[3], b[3]); }
  }
  function describe(s) {
    var texts = [], more = 0;
    for (var i = 0; i < s.recs.length; i++) {
      var r = s.recs[i];
      if (r.tag !== "text" || !r.text) continue;
      if (texts.length < DESC_TEXTS) texts.push(r.text); else more++;
    }
    var t = s.label + ".";
    if (texts.length) t += " Text in the scene: " + texts.join(", ") + (more ? ", and " + more + " more" : "") + ".";
    if (s.desc.textContent !== t) s.desc.textContent = t;
  }
  function schedule(s) {
    if (s.raf) return;
    s.raf = true;
    raf(function () { s.raf = false; paint(s); });
  }
  function layer(s, depth) {
    var L = s.layers[depth];
    if (!L) { var c = doc.createElement("canvas"); L = s.layers[depth] = { cv: c, ctx: c.getContext("2d") }; }
    if (L.cv.width !== s.cv.width || L.cv.height !== s.cv.height) { L.cv.width = s.cv.width; L.cv.height = s.cv.height; }
    L.ctx.setTransform(1, 0, 0, 1, 0, 0);
    if (s.clip) L.ctx.clearRect(s.clip[0], s.clip[1], s.clip[2], s.clip[3]);
    else L.ctx.clearRect(0, 0, L.cv.width, L.cv.height);
    return L;
  }
  function composite(onto, L, alpha) {
    onto.save();
    onto.setTransform(1, 0, 0, 1, 0, 0);
    onto.globalAlpha = alpha;
    onto.drawImage(L.cv, 0, 0);
    onto.restore();
  }
  function colour(s, c) { return c === "currentColor" ? s.current : c; }
  function drawShape(s, ctx, r, depth) {
    // A line has no area: SVG paints only its stroke.
    var fill = r.fill !== "none" && r.tag !== "line" ? colour(s, r.fill) : null;
    var stroke = r.stroke !== "none" && r.sw > 0 ? colour(s, r.stroke) : null;
    if (!fill && !stroke) return false;
    var target = ctx, L = null, m = r.ctm, kx = s.kx, ky = s.ky;
    // A translucent shape with both a fill and a stroke is composited as one
    // (SVG opacity), so the fill does not show through its stroke.
    if (r.op < 1 && fill && stroke) { L = layer(s, depth); target = L.ctx; }
    target.setTransform(kx * m[0], ky * m[1], kx * m[2], ky * m[3], kx * m[4], ky * m[5]);
    target.globalAlpha = r.op < 1 && !L ? r.op : 1;
    if (r.tag === "text") {
      target.font = font(s, r);
      target.textAlign = r.anchor === "middle" ? "center" : (r.anchor === "end" ? "end" : "start");
      target.textBaseline = "alphabetic";
      var x = num(r.a.x, 0), y = num(r.a.y, 0);
      if (fill) { target.fillStyle = fill; target.fillText(r.text, x, y); }
      if (stroke) { target.lineWidth = r.sw; target.strokeStyle = stroke; target.strokeText(r.text, x, y); }
    } else {
      if (fill) { target.fillStyle = fill; target.fill(r.path); }
      if (stroke) { target.lineWidth = r.sw; target.miterLimit = 4; target.strokeStyle = stroke; target.stroke(r.path); }
    }
    target.globalAlpha = 1;
    if (L) composite(ctx, L, r.op);
    return true;
  }
  function overlaps(b, c) { return !(b[2] < c[0] || b[0] > c[2] || b[3] < c[1] || b[1] > c[3]); }
  // paint is the frame's one draw pass: every record over a cleared canvas,
  // or, when only some shapes changed, the records over their region, clipped.
  function paint(s) {
    var cv = s.cv;
    if (!cv.isConnected) return;
    if (!s.full && !s.dirty) return;
    var t0 = w.performance ? w.performance.now() : 0;
    var cs = w.getComputedStyle ? w.getComputedStyle(cv) : null;
    var fam = (cs && cs.fontFamily) || "sans-serif";
    s.current = (cs && cs.color) || "black";
    if (fam !== s.family) {
      s.family = fam;
      for (var i = 0; i < s.recs.length; i++) if (s.recs[i].tag === "text") derive(s, s.recs[i], s.recs[i].pi >= 0 ? s.recs[s.recs[i].pi] : ROOT);
      s.full = true;
    }
    if (size(s)) s.full = true;
    var ctx = s.ctx, clipS = null;
    s.clip = null;
    if (!s.full) {
      var d = s.dirty;
      var x0 = Math.max(0, Math.floor(d[0] * s.kx) - 1), y0 = Math.max(0, Math.floor(d[1] * s.ky) - 1);
      var x1 = Math.min(cv.width, Math.ceil(d[2] * s.kx) + 1), y1 = Math.min(cv.height, Math.ceil(d[3] * s.ky) + 1);
      if (x1 <= x0 || y1 <= y0) { s.dirty = null; return; }
      if ((x1 - x0) * (y1 - y0) > 0.5 * cv.width * cv.height) s.full = true;
      else { s.clip = [x0, y0, x1 - x0, y1 - y0]; clipS = [x0 / s.kx, y0 / s.ky, x1 / s.kx, y1 / s.ky]; }
    }
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    if (s.clip) {
      ctx.beginPath(); ctx.rect(s.clip[0], s.clip[1], s.clip[2], s.clip[3]); ctx.clip();
      ctx.clearRect(s.clip[0], s.clip[1], s.clip[2], s.clip[3]);
    } else ctx.clearRect(0, 0, cv.width, cv.height);
    var stack = [], cur = ctx, drawn = 0;
    for (var j = 0; j < s.recs.length; j++) {
      var r = s.recs[j];
      if (r.tag === "g") {
        var L = r.op < 1 ? layer(s, stack.length) : null;
        stack.push({ r: r, ctx: cur, L: L });
        if (L) cur = L.ctx;
        continue;
      }
      if (r.tag === "/") {
        var top = stack.pop();
        if (top) { if (top.L) composite(top.ctx, top.L, top.r.op); cur = top.ctx; }
        continue;
      }
      if (!r.draw || (clipS && r.box && !overlaps(r.box, clipS))) continue;
      if (drawShape(s, cur, r, stack.length)) drawn++;
    }
    ctx.restore();
    var st = s.stats;
    st.paints++;
    if (s.clip) st.partial++; else st.full++;
    st.drawn = drawn;
    st.lastMs = w.performance ? w.performance.now() - t0 : 0;
    s.full = false; s.dirty = null; s.clip = null;
  }
  // size sets the backing store to the scene's CSS size in device pixels; it
  // reports a change (a devicePixelRatio change: a zoom, another screen).
  function size(s) {
    var dpr = w.devicePixelRatio || 1;
    var bw = Math.max(1, Math.round(s.w * dpr)), bh = Math.max(1, Math.round(s.h * dpr));
    s.kx = bw / s.w; s.ky = bh / s.h;
    if (s.cv.width === bw && s.cv.height === bh) return false;
    s.cv.width = bw; s.cv.height = bh;
    return true;
  }
  function hctx() {
    if (!hitCtx) hitCtx = doc.createElement("canvas").getContext("2d");
    return hitCtx;
  }
  function hitShape(r, x, y) {
    var m = r.ctm;
    if (r.tag === "text") {
      var inv = invert(m);
      if (!inv || !r.local) return false;
      var p = apply(inv, x, y), b = r.local;
      return p[0] >= b[0] && p[0] <= b[2] && p[1] >= b[1] && p[1] <= b[3];
    }
    if (!r.path) return false;
    var h = hctx();
    h.setTransform(m[0], m[1], m[2], m[3], m[4], m[5]);
    if (r.fill !== "none" && r.tag !== "line" && h.isPointInPath(r.path, x, y)) return true;
    if (r.stroke !== "none" && r.sw > 0) {
      h.lineWidth = r.sw; h.miterLimit = 4;
      if (h.isPointInStroke(r.path, x, y)) return true;
    }
    return false;
  }
  function hitTest(cv, x, y) {
    var s = cv.__skyScene;
    if (!s) return -1;
    for (var i = s.recs.length - 1; i >= 0; i--) {
      var r = s.recs[i];
      if (!r.draw) continue;
      if (r.box && (x < r.box[0] || x > r.box[2] || y < r.box[1] || y > r.box[3])) continue;
      if (hitShape(r, x, y)) return i;
    }
    return -1;
  }
  function round(v) { return Math.round(v * 100) / 100; }
  function toScene(s, cx, cy) {
    var b = s.cv.getBoundingClientRect();
    return { x: round(b.width ? (cx - b.left) * s.w / b.width : 0), y: round(b.height ? (cy - b.top) * s.h / b.height : 0) };
  }
  function fire(s, type, pt) {
    if (!s.cv.isConnected) return;
    var i = hitTest(s.cv, pt.x, pt.y);
    if (i < 0 || !(s.recs[i].wants & FLAGS[type])) return;
    var go = Sky.__sceneDispatch;
    if (typeof go === "function") go(s.cv.getAttribute("sky-id") || "", i, type, pt.x, pt.y);
  }
  function listen(s) {
    ["pointerdown", "pointermove", "pointerup", "click"].forEach(function (type) {
      s.cv.addEventListener(type, function (ev) {
        var pt = toScene(s, ev.clientX, ev.clientY);
        if (type !== "pointermove") { fire(s, type, pt); return; }
        s.move = pt;
        if (s.moveRaf) return;
        s.moveRaf = true;
        raf(function () { s.moveRaf = false; var p = s.move; s.move = null; if (p) fire(s, "pointermove", p); });
      });
    });
  }
  function mount(cv, sw, sh, label, list) {
    var ctx = cv.getContext ? cv.getContext("2d") : null;
    if (!ctx) return false;
    var s = { cv: cv, ctx: ctx, w: sw, h: sh, label: label || "", recs: [], full: true, dirty: null, raf: false,
      layers: [], family: "sans-serif", current: "black", kx: 1, ky: 1, clip: null,
      stats: { paints: 0, full: 0, partial: 0, drawn: 0, lastMs: 0 } };
    cv.__skyScene = s;
    size(s);
    var id = "sky-scene-desc-" + (++seq);
    var p = doc.createElement("p");
    p.setAttribute("id", id);
    cv.appendChild(p);
    s.desc = p;
    cv.setAttribute("aria-describedby", id);
    s.recs = decodeAll(s, list);
    describe(s);
    listen(s);
    schedule(s);
    return true;
  }
  function set(cv, list) {
    var s = cv.__skyScene;
    if (!s) return;
    s.recs = decodeAll(s, list);
    s.full = true;
    describe(s);
    schedule(s);
  }
  function update(cv, entries) {
    var s = cv.__skyScene;
    if (!s) return;
    var parts = entries ? entries.split(RS) : [], texts = false;
    for (var i = 0; i < parts.length; i++) {
      var f = parts[i].split(US), idx = +f[0], old = s.recs[idx];
      if (!old || old.tag === "g" || old.tag === "/") { s.full = true; continue; }
      var r = decode(f, 1);
      r.pi = old.pi;
      derive(s, r, r.pi >= 0 ? s.recs[r.pi] : ROOT);
      s.recs[idx] = r;
      if (old.draw) unite(s, old.box);
      if (r.draw) unite(s, r.box);
      if (old.tag === "text" || r.tag === "text") texts = true;
    }
    if (texts) describe(s);
    schedule(s);
  }
  Sky.sceneCanvas = {
    mount: mount, set: set, update: update, hitTest: hitTest,
    stats: function (cv) { return cv.__skyScene ? cv.__skyScene.stats : null; }
  };
})();
`
