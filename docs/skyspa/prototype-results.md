# Sky.Spa emit-path prototype — results (VERIFIED)

> **Status:** the end-to-end emit path is **proven and independently verified**. A
> real Sky program compiles to `GOOS=js GOARCH=wasm` and renders client-side, with
> the shipping Sky.Live runtime unbroken. The implementation lives on branch
> **`exp/spa-prototype`** (commits `2f84cc63` partition, `6c6c7f70` render) — a
> prototype, not production-hardened. This note is the durable record on `exp/spa`.

## What was proven

A real Sky-emitted TEA app — `Model = Int`, `Msg = Increment | Decrement | Reset`,
pure `update`, `Std.Html` `view`, entry `App.web { init, update, view,
subscriptions }` + `main = App.run appDef` built `sky build --target web:app`
— goes Sky source → emitted Go → wasm → **renders in the browser DOM**, with the
client TEA loop running entirely client-side. **DX note:** the app is written as
`Std.App`, identical to a web (`Sky.Live`) app; the client build is purely a
`--target` choice — the same source targets both.

## Gates (all independently re-run, not agent-claimed)

| Gate | Command | Result |
|---|---|---|
| `rt` compiles to wasm | `cd runtime-go && GOOS=js GOARCH=wasm go build ./rt/...` | **exit 0** |
| normal build (Sky.Live core) | `cd runtime-go && go build ./...` | **exit 0** |
| real app → wasm | `sky build` → `GOOS=js GOARCH=wasm go build` | **exit 0** |
| **render proof** (headless, DOM shim) | `node run_headless.cjs` | **ALL PASS** — init→0, +1×3→3, Reset→0, −1→−1, zero server |
| Sky.Live unbroken | `examples/09-live-counter` clean-slate build | **exit 0** |
| `rt` core tests | `go test ./rt/` (init-order, os-exit, coerce, dispatch) | **ok** |

## The number (real-app wasm bundle)

**7,877,681 B raw (7.51 MB) / 2,140,870 B gzip (2.04 MB)** + `wasm_exec.js` 16.5 KB
→ **~2.06 MB over the wire.**

Far heavier than the hand-written Phase-1 spike (579 KB gzip) because **real Sky
dispatch is reflection-native** (`sky_call`/`reflect.MakeFunc`), which keeps most
of `rt` reachable. This is **desktop / mobile-embed weight** — it confirms the
design's web-bundle wall: production web needs TinyGo (which can't compile
`reflect.MakeFunc` — a known wall) or a Sky→JS backend. Wasm proves the
*architecture*; it is not the production-web *bundle*.

## Key structural finding (narrows future work)

The **only hard `js/wasm` blocker is `modernc.org/sqlite` → `libc`** — `net/http`,
`net/url`, `os`, `os/exec`, `syscall` all *do* compile under `js/wasm`. So the
partition is **minimal**: server code that happens to compile under `js` stayed
untagged and is dead-code-eliminated at link. The runtime is far less entangled
than feared — the `rt.go`/`live.go` welding was the real work, not a net/db swamp.

## What the prototype contains (on `exp/spa-prototype`)

- **Partition:** `live_core.go` (93 portable-TEA decls extracted from `live.go`:
  VNode/HtmlToVNode/renderVNode*/diffTrees/cmdT/Cmd_*/msg-decode glue/`sky_call`/
  style-markers), `rt_server.go` (7 server funcs out of `rt.go`), several
  `*_core.go` shims, `rt_core_shims_{js,notjs}.go`; ~70 net/db/os/observability/
  TUI/CLI files tagged `//go:build !js`; `console_app`+`hub` subpackages tagged
  with js placeholders.
- **Client runtime (js):** `live_wasm.go` (`interpretCmd` + single-threaded
  `spaRun`/`step` driver), `dom_render_wasm.go` (VNode→DOM over `syscall/js`),
  `spa_core.go` (`Spa_config`/`Spa_app`), `main_preamble_js.go` (js no-ops for the
  server-lifecycle code codegen emits into `main()`).
- **Stdlib:** `sky-stdlib/Std/Spa.sky` (minimal `config`/`app`).
- **App:** `spa-counter/` + its headless harness.

## Incomplete — the concrete next steps (a prototype, not v1)

1. `interpretCmd`: `perform` runs tasks inline (prototype); `publish`/
   `publishNoEcho` are TODO no-ops. Real client Task-running + in-process pub/sub
   is the next driver work.
2. Renderer does a **full re-render** per dispatch; client-side diff (reuse
   `diffTrees` + `__skyApplyPatches` focus/cursor logic) is design Phase 4.
3. `Std.Spa` is **minimal** (init/update/view only) — routing, subscriptions, and
   the explicit author-declared server boundary are design Phases 2–5.
4. The partition is prototype-tagged; a production landing needs the census/gate
   review + CLAUDE.md §0.2.1 full sweep before it merges to `exp/spa`.

## To recover / continue

```bash
git checkout exp/spa-prototype     # the full verified implementation
```

The auto-split (v2) mechanism this feeds is specified in
[auto-split.md](auto-split.md); the overall plan is [design.md](design.md) §8.
