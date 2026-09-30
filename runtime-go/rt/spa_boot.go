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
`

// SpaBootPath is the loader's root-absolute URL in the frontend dist.
var SpaBootPath = "/spa-boot." + assetHash(SpaBootJS) + ".js"

// assetHash is the content hash used in every hashed asset name (the first 12 hex
// digits of the SHA-256; the Rust build uses the same rule for dist files).
func assetHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
