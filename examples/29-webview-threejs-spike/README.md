# 29-webview-threejs-spike

Spike to validate that **Three.js + WebGL2 + animation** works
end-to-end through `webview_go`'s system webview engine on every
target OS we'd ship `Sky.Webview` for (macOS / Windows / Linux).

This example exists to de-risk **PLAN.md risk #2 + #3** before we
commit to building the real `Sky.Webview` backend. See
`docs/skywebview/PLAN.md` for the full design.

> **Important**: this is a spike, NOT the Sky.Webview backend
> itself. It deliberately uses Sky.Http.Server (not Sky.Live) and a
> hand-written `webview_go` shim in `webview_shim/` to keep the test
> minimal. When Sky.Webview ships for real you won't need any of
> this glue — `Webview.app cfg` will own both sides.

## What it tests

| Aspect | How |
|---|---|
| WebGL2 availability in the embedded engine | HUD shows "WebGL 2" or falls back to "WebGL 1" |
| Hardware acceleration | HUD shows GL renderer string (e.g. "Apple M2", "ANGLE Direct3D11", "Mesa …", or the bad case "Mesa Off-screen") |
| Continuous animation under requestAnimationFrame | Torus knot rotates + 12 cubes orbit + starfield spins + per-frame light colour cycling |
| FPS the engine sustains | Live FPS in HUD (target: 60) |
| JS event dispatch inside webview | Two HUD buttons toggle rotation + wireframe |
| Window resize → canvas reflow | Drag the window edges |

## Running it

### Side 1 — Sky.Http.Server (serves the page + static assets)

```bash
cd examples/29-webview-threejs-spike
sky run src/Main.sky
# → listens on http://localhost:8765
```

Open `http://localhost:8765` in a regular browser as a baseline —
the scene should run at ~60 FPS in Chrome / Firefox / Safari. Note
the renderer string + WebGL version for comparison.

### Side 2 — webview_go (validates the system webview engine)

In a second shell, with the server running:

```bash
cd examples/29-webview-threejs-spike/webview_shim
go mod download
go run main.go
```

Compare HUD readings (FPS, renderer string, WebGL version) to the
browser baseline.

## Per-OS expectations

| OS | Expected | Bad path |
|---|---|---|
| **macOS 12+** | WebGL 2 · "Apple M…" / "AMD Radeon …" / "Intel UHD …" renderer · 60 fps | If you see "Software" renderer, your Mac has GPU acceleration disabled — unusual |
| **Windows 11** | WebGL 2 · "ANGLE (… Direct3D11 …)" or similar · 60 fps | Missing Edge WebView2 runtime → install from Microsoft's evergreen distributable |
| **Ubuntu 22.04+** | WebGL 2 · "Mesa …" + your GPU model · 60 fps | "Mesa Off-screen" renderer → missing DRI drivers; install `libgl1-mesa-dri` + ensure compositor is running |

If any platform falls back to WebGL 1, the design plan's risk #2
just escalated — flag in the PR.

## Files

```
sky.toml                 ← Sky.Http.Server config (port 8765)
src/Main.sky             ← Server that returns the HTML shell + static files
static/index.html        ← (implicit — served as the root by Main.sky)
static/three.min.js      ← Three.js r158 UMD build (vendored, ~636 KB)
static/scene.js          ← Animated 3D scene + HUD probes
static/style.css         ← HUD + canvas chrome
webview_shim/main.go     ← webview_go program pointed at localhost:8765
webview_shim/go.mod      ← Just the webview_go dependency
```

## A note on the vendored Three.js version

Three.js r150+ deprecated the UMD `three.min.js` build in favour of
ES modules. The vendored r158 is the last UMD release that still
exposes a global `THREE` — perfect for a zero-build spike. When
Sky.Webview ships, the real plan will likely vendor a current
release via an import map.

## Smoke-test results captured during development

| Check | Result |
|---|---|
| `sky build src/Main.sky` produces a binary | ✅ |
| Server returns the shell HTML at GET / | ✅ |
| `/static/three.min.js` content-type | ✅ `application/javascript` (after fixing a Sky bug — see commit "fix(http-server): withHeader Content-Type override") |
| `/static/scene.js` content-type | ✅ `application/javascript` |
| `/static/style.css` content-type | ✅ `text/css; charset=utf-8` |
| webview_go binary builds | ✅ ~3 MB (cgo to system WebKit) |
| webview_go opens a window pointed at the server | ✅ — confirmed by process surviving 4 s smoke |
| Visual rendering of the Three.js scene | ⏳ requires interactive run; report what you see |

When you run the interactive version, please paste back:
- The HUD's **Renderer** string
- The HUD's **WebGL** version (1 or 2)
- The HUD's **FPS** value (target 60)
- Whether the two HUD buttons respond

That's the data the design plan's risk #2 needs to close.

## Sky bugs surfaced + fixed during the spike

This spike's PR also carries two Sky-side fixes that were
necessary to make the spike work:

1. **`Server.withHeader "Content-Type"` was silently overridden**
   by `Server.text` / `Server.json` / `Server.html`'s default. The
   response-write order applied `Headers` map first and the default
   `ContentType` second. Swapped (`runtime-go/rt/rt.go` ~line 6800).
2. **`Server.static` is a STUB** that returns the literal string
   `"static:<dir>"` as the body instead of serving files from
   disk. Workaround in this example: three hand-rolled `Server.get`
   handlers that read each file via `File.readFile`. Real fix:
   filed as a follow-up Sky compiler task.

## What's next after this spike passes

The plan promises that `Sky.Webview` will reuse Sky.Live's HTML
renderer. That means: take the same Sky source you'd write for a
Sky.Live page, replace `Live.app` with `Webview.app`, get a desktop
binary. This spike doesn't demonstrate that yet — it only proves
that webview_go's underlying engine handles the kind of content
your target use case wants. The compiler glue is the next step.
