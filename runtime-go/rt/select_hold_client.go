package rt

// selectHoldJS is the open-<select> defence shared by the two browser
// appliers of server-diffed patches: the Sky.Live client (liveClientJS) and
// the Sky.Webview shim (webviewSharedJS). The Sky.Spa client has the same
// rule in Go (spa_select_defer.go).
//
// A native dropdown closes on ANY DOM mutation inside the open select or an
// ancestor that would re-mount it, and there is no JS API for "is the
// dropdown open", so focus is the conservative proxy. While a select may be
// open, a patch on it, inside it or on an ancestor of it is HELD, never
// dropped. Both appliers get patches the server diffed against its own
// previous tree, which has already moved on, so a dropped patch was lost
// until a reload. A real app hit this: a phone keeps focus on a filter select
// after the picker closes, the pick changed a sibling's tag, and the patch on
// the common parent was skipped for good. Held patches, and every later patch
// for the same region (they may address renamed ids, or be an ancestor's),
// apply in order once the select closes: on its pick (input or change) or
// when focus leaves it.
//
// A select is not defended against its own pick: input and change fire after
// the choice, so the picker is closed. __skySettledSelect is the select whose
// input or change fired since it last opened; a press, a key or a new focus on
// it may open it again.
//
// The host defines __skyApplyPatches(patches); a host with a full-body path
// defines __skyPatch(body), and a body that arrives while a select is open is
// held the same way (it supersedes every patch held before it).
const selectHoldJS = `
var __skySettledSelect = null;
var __skyHeld = null; // { sel, roots: [sky-id], patches: [], body, all, closing }
function __skyIdUnder(id, root) {
  return id.length > root.length + 1 && id.charAt(root.length) === "." &&
      id.slice(0, root.length) === root;
}
function __skyIdRelated(id, root) {
  return id === root || __skyIdUnder(id, root) || __skyIdUnder(root, id);
}
function __skyOpenSelect() {
  var a = document.activeElement;
  if (!a || a.tagName !== "SELECT" || a === __skySettledSelect) return null;
  return { el: a };
}
function __skyStartHold(sel, what) {
  if (__skyHeld) return;
  __skyHeld = { sel: sel, roots: [], patches: [], body: null, all: false, closing: false };
  if (window.console && console.warn) {
    console.warn("[sky.live] patches deferred while a <select> is open; they apply when it closes:", what);
  }
}
// __skyHold holds patch p for the open select sel.
function __skyHold(p, sel) {
  __skyStartHold(sel, p.id);
  __skyHeld.roots.push(p.id);
  __skyHeld.patches.push(p);
}
// __skyHoldBody holds a full body for the open select sel. It replaces the
// whole page, so it supersedes the patches held before it and every later
// patch queues behind it.
function __skyHoldBody(body, sel) {
  __skyStartHold(sel, "full body");
  __skyHeld.body = body;
  __skyHeld.patches = [];
  __skyHeld.all = true;
}
// __skyHeldTakes holds p when it addresses a held region: the region itself,
// a node under it (perhaps by a renamed id), or an ancestor of it (which must
// not run before the held patches). Patches elsewhere apply now.
function __skyHeldTakes(p) {
  var h = __skyHeld, roots = h.roots;
  if (h.all) { h.patches.push(p); return true; }
  for (var i = 0; i < roots.length; i++) {
    if (__skyIdRelated(p.id, roots[i])) {
      if (__skyIdUnder(roots[i], p.id)) roots.push(p.id);
      h.patches.push(p);
      return true;
    }
  }
  return false;
}
// __skyPatchOrHold applies an SSE-pushed full body, or holds it while a
// select is open (or patches are already held, which it must follow).
function __skyPatchOrHold(body) {
  var o = __skyOpenSelect();
  if (o || __skyHeld) { __skyHoldBody(body, __skyHeld ? __skyHeld.sel : o.el); return; }
  __skyPatch(body);
}
// __skyFlushHeld applies what was held for sel, in order, on the next task:
// after the event's own listeners, once a blur has moved activeElement. Until
// then later patches for the same region still queue behind the held ones.
function __skyFlushHeld(sel) {
  if (!__skyHeld || __skyHeld.sel !== sel || __skyHeld.closing) return;
  var h = __skyHeld;
  h.closing = true;
  setTimeout(function() {
    if (__skyHeld !== h) return; // a full body replaced the page meanwhile
    __skyHeld = null;
    if (h.body !== null && typeof __skyPatch === "function") __skyPatch(h.body);
    __skyApplyPatches(h.patches);
  }, 0);
}
(function() {
  if (typeof document === "undefined" || !document.addEventListener) return;
  function sel(e) { return e && e.target && e.target.tagName === "SELECT" ? e.target : null; }
  var settle = function(e) {
    var s = sel(e);
    if (s) { __skySettledSelect = s; __skyFlushHeld(s); }
  };
  var unsettle = function(e) {
    var s = sel(e);
    if (s && (s === __skySettledSelect || e.type === "focusin")) __skySettledSelect = null;
  };
  document.addEventListener("input", settle, true);
  document.addEventListener("change", settle, true);
  ["pointerdown", "mousedown", "keydown", "focusin"].forEach(function(t) {
    document.addEventListener(t, unsettle, true);
  });
  document.addEventListener("focusout", function(e) {
    var s = sel(e);
    if (!s) return;
    if (s === __skySettledSelect) __skySettledSelect = null;
    __skyFlushHeld(s);
  }, true);
})();
`
