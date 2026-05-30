# 29-webview-threejs-spike

Spike validating that **Three.js + WebGL2 + animation** work
end-to-end inside a native Sky.Webview desktop window — driven
entirely by the Sky compiler, no hand-rolled glue.

This example used to be a two-process spike: a Sky.Http.Server on
one side + a hand-rolled `webview_go` shim on the other. After
bug #370 landed (Sky.Webview now spawns a 127.0.0.1 loopback http
server when `sky.toml`'s `[live].static` is set), the whole
two-process dance collapses into a single Sky source file.

> **What this proves**: the same Sky source you'd write for a
> Sky.Live page works for a desktop Sky.Webview app — relative
> paths (`/static/three.min.js`) resolve correctly because the
> webview is pointed at a real `http://127.0.0.1:<free>` origin
> instead of `about:blank`.

## What it tests

| Aspect | How |
|---|---|
| WebGL2 in the system webview | HUD shows "WebGL 2" or falls back to "WebGL 1" |
| Hardware acceleration | HUD shows GL renderer string (e.g. "Apple M2", "ANGLE Direct3D11", "Mesa …") |
| Continuous animation under `requestAnimationFrame` | Torus knot rotates + 12 cubes orbit + starfield spins |
| FPS the engine sustains | Live FPS in HUD (target: 60) |
| JS event dispatch inside the webview | Two HUD buttons toggle rotation + wireframe |
| Window resize → canvas reflow | Drag the window edges |
| `<script>` tag bootstrap | Three.js loads from `/static/three.min.js`, then `/static/scene.js` initialises the scene |
| Bug #370 (`[live].static` loopback) | Asset 404s would crash this spike; a green run pins the fix |

## Running it

```bash
cd examples/29-webview-threejs-spike
sky run src/Main.sky
```

A native window opens, the Three.js scene paints, the FPS counter
ticks. Closing the window exits the program cleanly. That's it —
no second process, no `go run`, no port to remember.

You should see the loopback startup line on stderr:

```
[sky.webview] loopback server on http://127.0.0.1:<port>/ (static="static")
```

## Per-OS expectations

| OS | Expected | Bad path |
|---|---|---|
| **macOS 12+** | WebGL 2 · "Apple M…" / "AMD Radeon …" / "Intel UHD …" renderer · 60 fps | "Software" renderer → GPU acceleration disabled |
| **Windows 11** | WebGL 2 · "ANGLE (… Direct3D11 …)" · 60 fps | Missing Edge WebView2 runtime → install Microsoft's evergreen distributable |
| **Ubuntu 22.04+** | WebGL 2 · "Mesa …" + your GPU model · 60 fps | "Mesa Off-screen" renderer → install `libgl1-mesa-dri` |

If any platform falls back to WebGL 1, the design plan's risk #2
just escalated — flag in the PR.

## Files

```
sky.toml                 ← [live].static = "static" — the bug #370 gate
src/Main.sky             ← Webview.app cfg + view that emits the HUD + canvas + script tags
static/three.min.js      ← Three.js r158 UMD build (vendored, ~636 KB)
static/scene.js          ← Animated 3D scene + HUD probes
static/style.css         ← HUD + canvas chrome
```

That's the entire footprint. Compare to the pre-bug-#370 version,
which carried an extra `webview_shim/` Go module + a Sky.Http.Server
in `Main.sky` to hand-roll three `Server.get` handlers per static
file — gone.

## A note on the vendored Three.js version

Three.js r150+ deprecated the UMD `three.min.js` build in favour
of ES modules. The vendored r158 is the last UMD release that
still exposes a global `THREE` — perfect for a zero-build spike.

## How the loopback works (bug #370)

When `sky.toml` declares `[live].static = "static"`, the Sky
compiler emits a `SetSkyDefault("LIVE_STATIC_DIR", "static")`
into the program's `init()`. At runtime, `Sky.Webview` checks
that env var:

- **Set** (this example): spawns `http.Server` on `127.0.0.1:0`
  (free port), serves `/static/*` via `http.FileServer`, returns
  the current rendered body on `/`, then `w.Navigate()`s to the
  loopback URL. The embedded browser has a real origin, so
  relative paths resolve.
- **Unset** (examples/31-webview-stopwatch-ui): falls through to
  `w.SetHtml(webviewPageWrap(body))` — the original path, no
  loopback server, no regression for pure Std.Ui apps.

The loopback binds to `127.0.0.1` only — **never** `0.0.0.0`. A
desktop app's local server must not be reachable from the LAN.
