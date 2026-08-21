# Sky.Spa Phase-1 de-risk spike

A faithful, hand-written mirror of what the Sky.Spa emit path will generate for a
client-side TEA loop — used to de-risk three unknowns *before* the runtime-subset
carve (see `../design.md` §5.1). It is not a toy: `Element`, `Model`, `Msg`, pure
`update`, `view`, the `Element→DOM` renderer, and the TEA driver each map 1:1 onto
a Sky concept, so Phase 3 has a concrete generation target.

## What it proves

1. **Bundle size** — standard Go→wasm for a trivial counter: **1.90 MB raw /
   579 KB gzip** (+4 KB `wasm_exec.js`). Fine for desktop/mobile-embed; too heavy
   for production web (Elm ≈30 KB). Lever: TinyGo (~10–20× smaller) or a Sky→JS
   backend — decided in Phase 3 on evidence.
2. **Renderer + interop** — the `Element→DOM` renderer builds real DOM over
   `syscall/js`.
3. **The client TEA loop** — pure `update` + re-render per dispatched `Msg`,
   **zero server**.

## Reproduce

```bash
# build the wasm core
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" .
GOOS=js GOARCH=wasm go build -o main.wasm .
wc -c main.wasm && gzip -9 -c main.wasm | wc -c   # size

# headless verification of the loop (no browser needed)
node run_headless.cjs
# => ALL PASS — init→0, +1×3→3, Reset→0, −1→−1, client-local, no server

# in a browser (optional visual check)
python3 -m http.server 8791 --bind 127.0.0.1   # then open http://127.0.0.1:8791/
```

`main.wasm` and `wasm_exec.js` are build artifacts and are intentionally not
committed.
