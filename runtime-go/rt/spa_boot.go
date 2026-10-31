package rt

import (
	"crypto/sha256"
	"encoding/hex"
)

// The Sky.Spa wasm boot loader, as a same-origin file.
//
// Both boot paths (the static `dist/index.html` the build writes, and the SSR
// first-paint page, SpaSSRPageSeeded) used to start the wasm from an inline
// <script>, which a strict Content-Security-Policy (script-src 'self'
// 'wasm-unsafe-eval', no 'unsafe-inline') blocks: the page stayed on
// "Loading…" forever. The loader is now the file `spa-boot.<hash>.js` in the
// frontend dist, next to wasm_exec.js and main.<hash>.wasm. It reads the wasm
// URL from its own `data-wasm` attribute, so its bytes do not depend on the
// build and one hash names it everywhere.
//
// The build (rust/crates/sky/src/main.rs, spa_boot_js + stage_web_bundle)
// writes the SAME bytes: it reads the literals below (islandClientJS from
// island_client.go, sceneClientJS from scene_client.go, terminalWidgetJS from
// island_terminal.go, spaBootLoaderJS from this file) out of the Go sources at
// compile time, so there is one copy. The Rust unit test
// `spa_boot_js_matches_the_runtime` checks the extraction, because a drift
// would point the SSR page at a file name the build never wrote.

// SpaBootJS is the file: the widget-island runtime (island_client.go), so a
// widget file loaded with <script src defer> can register before the wasm
// boots, the Std.Ui.Canvas pointer runtime (scene_client.go), the built-in
// terminal widget (island_terminal.go), then the loader. `document.currentScript` is the <script> element
// that is running it (a classic, non-module script), so its data-wasm
// attribute names the wasm to instantiate.
const SpaBootJS = islandClientJS + sceneClientJS + scenePainterJS + terminalWidgetJS + spaBootLoaderJS

// spaBootLoaderJS is the loader proper. It follows the widget-island runtime
// (island_client.go), which a widget file needs before the wasm boots.
const spaBootLoaderJS = `// Sky.Spa boot loader (runtime-go/rt/spa_boot.go). An external file so a strict
// Content-Security-Policy (script-src 'self' 'wasm-unsafe-eval') runs it.
const go = new Go();
(function () {
  var me = document.currentScript;
  var wasm = (me && me.getAttribute("data-wasm")) || "/main.wasm";
  WebAssembly.instantiateStreaming(fetch(wasm), go.importObject).then(function (res) {
    go.run(res.instance);
  });
  // Safety net for the blocking hydration overlay: if the wasm never boots,
  // drop data-sky-hydrating after 12s so the page is never locked. The client
  // clears it on hydration first in the normal case.
  setTimeout(function () {
    document.documentElement.removeAttribute("data-sky-hydrating");
  }, 12000);
})();
// Hold a form submitted while the client hydrates (v0.29.1). Before the wasm
// client is live, Enter, a keyboard "Go" key or a password manager submits the
// server-rendered form natively: a POST of the fields to the page's own URL.
// While <html data-sky-hydrating> is set, a submit of a form the app handles
// (it has a sky-submit attribute) is held here instead, with the field values
// typed so far. Hydration sets each field to the model's value, so typed text
// is recorded too. Once the client is live it calls __skyHydrated (once), which
// puts the typed values back, sends them as input and change events, so the
// app's handlers see them, and submits the held form through the app. Clicks
// before hydration are not replayed: the overlay takes them, and the target
// is not known.
(function () {
  var root = document.documentElement;
  var edits = {};
  var order = [];
  var held = null;
  function hydrating() {
    return root.hasAttribute("data-sky-hydrating");
  }
  function idOf(el) {
    return el && el.getAttribute ? el.getAttribute("sky-id") : null;
  }
  var notTyped = { submit: 1, button: 1, reset: 1, image: 1, hidden: 1, file: 1 };
  function record(el) {
    var id = idOf(el);
    if (!id || typeof el.value !== "string" || notTyped[el.type]) return;
    if (!(id in edits)) order.push(id);
    edits[id] = { value: el.value, checked: !!el.checked };
  }
  function onEdit(ev) {
    if (hydrating()) record(ev.target);
  }
  document.addEventListener("input", onEdit, true);
  document.addEventListener("change", onEdit, true);
  document.addEventListener("submit", function (ev) {
    var form = ev.target;
    if (!hydrating() || !form || !form.hasAttribute || !form.hasAttribute("sky-submit")) return;
    ev.preventDefault();
    ev.stopImmediatePropagation();
    if (held) return;
    for (var i = 0; i < form.elements.length; i++) record(form.elements[i]);
    held = { form: idOf(form), by: idOf(ev.submitter) };
  }, true);
  function byIdNow() {
    var map = {};
    var all = document.querySelectorAll("[sky-id]");
    for (var i = 0; i < all.length; i++) map[all[i].getAttribute("sky-id")] = all[i];
    return map;
  }
  globalThis.__skyHydrated = function () {
    globalThis.__skyHydrated = function () {};
    var byId = byIdNow();
    for (var j = 0; j < order.length; j++) {
      var el = byId[order[j]];
      var e = edits[order[j]];
      if (!el || typeof el.value !== "string" || notTyped[el.type]) continue;
      if (el.type === "checkbox" || el.type === "radio") {
        el.checked = e.checked;
      } else if (el.value !== e.value) {
        el.value = e.value;
      }
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    }
    var spec = held;
    held = null;
    // The events above may have re-rendered: look the form up again.
    byId = byIdNow();
    var form = spec && spec.form ? byId[spec.form] : null;
    if (!form || form.tagName !== "FORM") return;
    var by = spec.by ? byId[spec.by] : null;
    if (typeof form.requestSubmit !== "function") {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    } else if (by && by.form === form) {
      form.requestSubmit(by);
    } else {
      form.requestSubmit();
    }
  };
})();
`

// SpaBootPath is the loader's root-absolute URL in the frontend dist.
var SpaBootPath = "/spa-boot." + assetHash(SpaBootJS) + ".js"

// assetHash is the content hash used in every hashed asset name (the first 12 hex
// digits of the SHA-256; the Rust build uses the same rule for dist files).
func assetHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
