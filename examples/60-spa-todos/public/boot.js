// Standard Go wasm bootstrap, as a same-origin file (not an inline <script>) so
// the page runs under a strict Content-Security-Policy
// (script-src 'self' 'wasm-unsafe-eval'). Same-origin: the client's
// fetch("/api/...") hits this same backend, so no CORS is involved.
const go = new Go();
WebAssembly.instantiateStreaming(fetch("/main.wasm"), go.importObject).then((res) => {
  go.run(res.instance);
});
