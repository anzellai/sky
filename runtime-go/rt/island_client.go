package rt

// islandClientJS is the widget-island runtime (island_core.go has the model).
// Both clients carry the same bytes: it opens the Sky.Live client
// (liveClientJS) and the Sky.Spa boot loader (SpaBootJS), each a same-origin
// file, so a strict Content-Security-Policy (script-src 'self') runs it and a
// widget file loaded with <script src defer> finds window.Sky.island defined.
// The Rust build reads the Sky.Spa copy from this file (main.rs spa_boot_js),
// so this literal must hold no backquote.
//
// The contract a widget file implements:
//
//	window.Sky.island("editor", {
//	  mount(el, props, send) { ... },  // first render, and after a remount
//	  update(props) { ... },           // the server changed the props
//	  command(name, payload) { ... },  // Cmd.toIsland id name payload
//	  destroy() { ... },               // the island left the page
//	});
//
// Each island gets its own object made with Object.create(definition), so
// `this` holds per-instance state; this.el is the element and this.send the
// sender. send(type, data) dispatches the CustomEvent "skyisland-<type>" (type
// lower-cased: HTML attribute names are) with detail data, which must be
// JSON-serialisable; Std.Ui.onIslandEvent decodes it into a typed Msg.
//
// A MutationObserver drives the lifecycle, so the same code serves the Live
// client (HTML patches) and the Spa wasm client (DOM calls): an island that
// appears is mounted (or waits for its name to register), one whose props
// attribute changes is updated, one that leaves the document is destroyed.
// A node that a patch moves out and back in the same task is still connected
// when the observer runs, so it is not remounted. Commands for an island that
// is not mounted yet wait in a bounded queue and are delivered on mount.
//
// Delivery contract: a command reaches the widget once and in order, or the
// island is resynced: the runtime destroys the widget, empties its element,
// mounts it again from the current props, and dispatches the island event
// "resync" with {"reason": "lost" | "restart" | "overflow"} (the app can
// decode it with Std.Ui.onIslandEvent "resync"; the name is reserved, a
// widget cannot send it). On Sky.Live every command carries a per-island seq
// and the server writes "islandsync" maps (live_island_delivery.go): a
// command that skips a seq, or a map above the last seq received, is a lost
// command. After a resync, commands at or below the resync point are stale
// and ignored. A command pushed out of the bounded wait queue (an island that
// is not mounted) resyncs that island when it mounts.
const islandClientJS = `// Sky widget islands (runtime-go/rt/island_client.go): window.Sky.island.
(function () {
  "use strict";
  var w = typeof window !== "undefined" ? window : this;
  var Sky = w.Sky = w.Sky || {};
  if (Sky.__islands) return;
  var NAME = "data-sky-island", ID = "data-sky-island-id", PROPS = "data-sky-props";
  var PREFIX = "skyisland-";
  var QMAX = 256;
  var defs = {};
  var live = [];
  var queues = {};
  var seqs = {}, epoch = null, owed = {};
  function warn() {
    try {
      if (w.console && w.console.warn) {
        w.console.warn.apply(w.console, ["[sky.island]"].concat(Array.prototype.slice.call(arguments)));
      }
    } catch (_) {}
  }
  function isIsland(n) {
    return !!(n && n.nodeType === 1 && n.getAttribute && n.getAttribute(NAME));
  }
  function identity(n) {
    return n.getAttribute(NAME) + "\u0000" + (n.getAttribute(ID) || "");
  }
  function props(n) {
    var s = n.getAttribute(PROPS);
    if (s === null || s === "") return null;
    try { return JSON.parse(s); } catch (e) {
      warn("the props of island", n.getAttribute(ID), "are not JSON", e);
      return null;
    }
  }
  function call(inst, fn, args, what) {
    if (!inst || typeof inst[fn] !== "function") return;
    try { inst[fn].apply(inst, args); } catch (e) {
      warn(what, "failed for island", inst.el && inst.el.getAttribute(ID), e);
    }
  }
  function sender(el) {
    return function (type, data) {
      var t = String(type).toLowerCase();
      var detail = data === undefined ? null : data;
      try { JSON.stringify(detail); } catch (e) {
        warn("send(" + t + "): the data is not JSON; the event was dropped", e);
        return false;
      }
      if (t === "resync") {
        warn("send(resync): the event name resync is reserved for the runtime; the event was dropped");
        return false;
      }
      return emit(el, t, detail);
    };
  }
  // emit dispatches a widget event, holding it until the page's client is
  // ready (see hostReady).
  function emit(el, t, detail) {
    if (!el.isConnected || !el.__skyIsland) return false;
    if (!hostReady) {
      // The page's client has not bound its listeners yet (see hostReady):
      // hold the event and dispatch it once it has.
      if (held.length >= HMAX) {
        held.shift();
        warn("the page's client is not ready and", HMAX, "widget events are waiting; the oldest was dropped");
      }
      held.push([el, t, detail]);
      return true;
    }
    return fire(el, t, detail);
  }
  function fire(el, t, detail) {
    var ev;
    try { ev = new w.CustomEvent(PREFIX + t, { detail: detail }); } catch (_) { return false; }
    return el.dispatchEvent(ev);
  }
  // hostReady: the page's client (the Sky.Live client, or the Sky.Spa wasm
  // client) has bound its event listeners and calls Sky.__islandHostReady.
  // Until then a widget's send() is held: this runtime runs first, so a
  // widget that registers while the page is still loading mounts, and sends
  // from mount(), before the client binds the element's listener at
  // DOMContentLoaded (or, on Sky.Spa, when the wasm hydrates); such an
  // event reached no listener and was lost. A terminal widget's "ready" (the
  // request to repaint its scrollback) was the first to need it.
  var hostReady = false, held = [], HMAX = 1024;
  function markHostReady() {
    if (hostReady) return;
    hostReady = true;
    var h = held;
    held = [];
    for (var i = 0; i < h.length; i++) {
      if (h[i][0].isConnected && h[i][0].__skyIsland) fire(h[i][0], h[i][1], h[i][2]);
    }
  }
  function deliver(el, name, payload) {
    call(el.__skyIsland.inst, "command", [name, payload], "command " + name);
  }
  function flush(el) {
    var id = el.getAttribute(ID), q = queues[id];
    if (!q) return;
    delete queues[id];
    for (var i = 0; i < q.length && el.__skyIsland; i++) deliver(el, q[i][0], q[i][1]);
  }
  function mount(el) {
    if (el.__skyIsland || !el.isConnected) return;
    var def = defs[el.getAttribute(NAME)];
    if (!def) return;
    var inst = Object.create(def);
    inst.el = el;
    inst.send = sender(el);
    el.__skyIsland = { inst: inst, key: identity(el), props: el.getAttribute(PROPS) };
    live.push(el);
    call(inst, "mount", [el, props(el), inst.send], "mount");
    flush(el);
    var id = el.getAttribute(ID) || "";
    if (owed[id]) {
      var why = owed[id];
      delete owed[id];
      emit(el, "resync", { reason: why });
    }
  }
  // resync remounts island id from its current props and tells the app, or,
  // when it is not mounted, does so when it mounts.
  function resync(id, why) {
    var el = find(id);
    if (!el || !el.__skyIsland) { owed[id] = why; return; }
    warn("island", id, "missed a command (" + why + "); it is mounted again from its current props");
    destroy(el);
    while (el.firstChild) el.removeChild(el.firstChild);
    owed[id] = why;
    mount(el);
  }
  // sync takes an "islandsync" map from the server: {e: epoch, s: {id: seq}}.
  function sync(m) {
    if (!m || typeof m !== "object") return;
    var s = m.s && typeof m.s === "object" ? m.s : {}, id;
    if (typeof m.e === "string") {
      if (epoch !== null && m.e !== epoch) {
        // A new server process: its predecessor's buffers are gone.
        var had = seqs;
        seqs = {};
        for (id in had) if (had[id] > 0) resync(id, "restart");
      }
      epoch = m.e;
    }
    for (id in s) {
      var g = s[id];
      if (typeof g !== "number") continue;
      if (seqs[id] === undefined) seqs[id] = g;
      else if (g > seqs[id]) { seqs[id] = g; resync(id, "lost"); }
    }
  }
  function destroy(el) {
    var st = el.__skyIsland;
    if (!st) return;
    el.__skyIsland = null;
    var i = live.indexOf(el);
    if (i >= 0) live.splice(i, 1);
    call(st.inst, "destroy", [], "destroy");
  }
  function update(el) {
    var st = el.__skyIsland;
    if (!st) {
      if (isIsland(el)) mount(el);
      return;
    }
    if (!isIsland(el) || st.key !== identity(el)) {
      destroy(el);
      if (isIsland(el)) mount(el);
      return;
    }
    var p = el.getAttribute(PROPS);
    if (p === st.props) return;
    st.props = p;
    call(st.inst, "update", [props(el)], "update");
  }
  function scan(root) {
    if (!root) return;
    if (isIsland(root)) mount(root);
    if (!root.querySelectorAll) return;
    var list = root.querySelectorAll("[" + NAME + "]");
    for (var i = 0; i < list.length; i++) mount(list[i]);
  }
  function sweep() {
    for (var i = live.length - 1; i >= 0; i--) {
      var el = live[i];
      if (!el.isConnected) destroy(el); else update(el);
    }
  }
  function find(id) {
    for (var i = 0; i < live.length; i++) {
      if (live[i].isConnected && live[i].getAttribute(ID) === id) return live[i];
    }
    return null;
  }
  function command(id, name, payload, seq) {
    id = String(id);
    if (typeof seq === "number" && seq > 0) {
      var last = seqs[id];
      if (last !== undefined) {
        if (seq <= last) return;
        if (seq > last + 1) { seqs[id] = seq; resync(id, "lost"); }
      }
      seqs[id] = seq;
    }
    var el = find(id);
    if (el) { deliver(el, name, payload); return; }
    var q = queues[id] || (queues[id] = []);
    if (q.length >= QMAX) {
      q.shift();
      owed[id] = "overflow";
      warn("island", id, "is not mounted and has", QMAX, "commands waiting; the oldest was dropped, and the island is resynced when it mounts");
    }
    q.push([name, payload]);
  }
  // pool / adopt keep islands across a Sky.Live HTML swap: pool the islands
  // under the subtree about to be replaced, then put each back in place of the
  // parsed element with the same identity. The kept element takes the fresh
  // attributes (the new props included); an attribute outside the server's
  // namespace (sky-*, data-sky-*) that the widget set stays.
  function pool(scope) {
    var p = null;
    function add(n) { if (isIsland(n)) (p || (p = {}))[identity(n)] = n; }
    if (!scope) return p;
    add(scope);
    if (scope.querySelectorAll) {
      var l = scope.querySelectorAll("[" + NAME + "]");
      for (var i = 0; i < l.length; i++) add(l[i]);
    }
    return p;
  }
  function copyAttrs(src, dst) {
    var i, a, drop = [];
    for (i = 0; i < src.attributes.length; i++) {
      a = src.attributes[i];
      if (dst.getAttribute(a.name) !== a.value) dst.setAttribute(a.name, a.value);
    }
    for (i = 0; i < dst.attributes.length; i++) {
      a = dst.attributes[i].name;
      if (!src.hasAttribute(a) && (a.lastIndexOf("sky-", 0) === 0 || a.lastIndexOf("data-sky-", 0) === 0)) drop.push(a);
    }
    for (i = 0; i < drop.length; i++) dst.removeAttribute(drop[i]);
  }
  function adopt(p, frag) {
    if (!p || !frag) return frag;
    var fresh = [], i;
    if (isIsland(frag)) fresh.push(frag);
    if (frag.querySelectorAll) {
      var l = frag.querySelectorAll("[" + NAME + "]");
      for (i = 0; i < l.length; i++) fresh.push(l[i]);
    }
    var out = frag;
    for (i = 0; i < fresh.length; i++) {
      var n = fresh[i], k = identity(n), kept = p[k];
      if (!kept || kept === n) continue;
      delete p[k];
      copyAttrs(n, kept);
      if (n.parentNode) n.parentNode.replaceChild(kept, n);
      if (n === frag) out = kept;
    }
    return out;
  }
  // saveFocus / restoreFocus: moving an island's element blurs whatever the
  // widget had focused and resets the selection; put both back. Offsets are
  // saved as values (a live Range would follow the removal).
  function saveFocus(scope) {
    var d = w.document, a = d && d.activeElement;
    if (!a || a === d.body || !scope || !scope.contains || !scope.contains(a)) return null;
    if (!a.closest || !a.closest("[" + NAME + "]")) return null;
    var f = { el: a, input: null, ranges: [] };
    try {
      if (typeof a.selectionStart === "number") f.input = [a.selectionStart, a.selectionEnd];
    } catch (_) {}
    var sel = w.getSelection && w.getSelection();
    if (sel) {
      for (var i = 0; i < sel.rangeCount; i++) {
        var r = sel.getRangeAt(i);
        f.ranges.push([r.startContainer, r.startOffset, r.endContainer, r.endOffset]);
      }
    }
    return f;
  }
  function restoreFocus(f) {
    if (!f || !f.el.isConnected) return;
    var d = w.document;
    if (d.activeElement !== f.el) {
      try { f.el.focus({ preventScroll: true }); } catch (_) { try { f.el.focus(); } catch (_) {} }
    }
    if (f.input) {
      try { f.el.setSelectionRange(f.input[0], f.input[1]); } catch (_) {}
    }
    if (!f.ranges.length || !w.getSelection) return;
    var sel = w.getSelection();
    try {
      sel.removeAllRanges();
      for (var i = 0; i < f.ranges.length; i++) {
        var r = f.ranges[i], rg = d.createRange();
        rg.setStart(r[0], r[1]);
        rg.setEnd(r[2], r[3]);
        sel.addRange(rg);
      }
    } catch (_) {}
  }
  Sky.island = function (name, def) {
    if (typeof name !== "string" || name === "" || !def || typeof def !== "object") {
      warn("Sky.island(name, definition) needs a name and a definition object");
      return;
    }
    if (defs[name]) warn("island", name, "is registered twice; islands mounted from now on use the new definition");
    defs[name] = def;
    if (w.document) scan(w.document);
  };
  Sky.__islandCommand = command;
  Sky.__islandSync = sync;
  Sky.__islandHostReady = markHostReady;
  Sky.__islands = {
    prefix: PREFIX, scan: scan, sweep: sweep, update: update, command: command,
    pool: pool, adopt: adopt, saveFocus: saveFocus, restoreFocus: restoreFocus,
    sync: sync, resync: resync
  };
  function start() {
    var d = w.document;
    if (!d) return;
    scan(d);
    if (typeof w.MutationObserver !== "function") return;
    new w.MutationObserver(function (records) {
      var removed = false;
      for (var i = 0; i < records.length; i++) {
        var r = records[i];
        if (r.type === "attributes") {
          if (r.target.__skyIsland || isIsland(r.target)) update(r.target);
          continue;
        }
        if (r.removedNodes && r.removedNodes.length) removed = true;
        var add = r.addedNodes || [];
        for (var j = 0; j < add.length; j++) {
          if (add[j].nodeType === 1) scan(add[j]);
        }
      }
      if (removed) sweep();
    }).observe(d.documentElement || d, {
      subtree: true, childList: true, attributes: true, attributeFilter: [NAME, ID, PROPS]
    });
  }
  if (w.document && w.document.readyState === "loading") {
    w.document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
`
