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
// The build (rust/crates/sky/src/main.rs, SPA_BOOT_JS + stage_web_bundle)
// writes the SAME bytes; the Rust unit test `spa_boot_js_matches_the_runtime`
// fails if the two copies drift, because the SSR page would then reference a
// file name the build never wrote.

// SpaBootJS is the loader. `document.currentScript` is the <script> element
// that is running it (a classic, non-module script), so its data-wasm
// attribute names the wasm to instantiate.
const SpaBootJS = `// Sky.Spa boot loader (runtime-go/rt/spa_boot.go). An external file so a strict
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
