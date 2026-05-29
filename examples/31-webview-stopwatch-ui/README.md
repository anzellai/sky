# 31-webview-stopwatch-ui

Desktop stopwatch via **Sky.Webview** — the cross-backend mirror
of Sky.Live (web) and Sky.Tui (terminal).

This example pairs with two siblings:

| Example | Backend | `main` |
|---|---|---|
| `22-tui-stopwatch-ui` | Terminal (Std.Ui → ANSI cells) | `Tui.app` |
| `31-webview-stopwatch-ui` | **Native desktop window** | `Webview.app` |

The Model, `update`, `subscriptions`, and `view` functions are
identical across all three; only the entry call differs. That's
the "write once, render anywhere" cross-backend story.

## Running it (macOS)

```bash
cd examples/31-webview-stopwatch-ui
sky build src/Main.sky
./sky-out/app
```

A native window opens (~800×500). Click **Start** to begin the
stopwatch; **Pause** to halt; **Reset** to zero. Close the window
to exit cleanly.

## Platform notes (v0.1)

Sky.Webview v0.1 is **macOS only** — the runtime compiles on
Windows + Linux but smoke validation lands in v0.2.

| OS | v0.1 status | v0.2 plan |
|---|---|---|
| macOS 12+ (WKWebView) | ✅ shipped | — |
| Windows 11 (WebView2) | ⚠️ builds, untested | Smoke-validated + alwaysOnTop / transparent |
| Ubuntu 22.04+ (WebKitGTK) | ⚠️ builds, untested | Smoke-validated + tray-icon shim |

On Windows you'll need the Edge WebView2 runtime (Microsoft's
evergreen distributable). On Linux you'll need
`libwebkit2gtk-4.0-37` (Ubuntu/Debian) or `webkit2gtk4.0`
(Fedora/Arch). On macOS WKWebView ships with the OS.

## What's in scope for v0.1

- `Webview.app cfg` — TEA entry mirroring `Live.app` / `Tui.app`.
- Minimal `WindowCfg = { title : String, size : (Int, Int) }`.
- Reuses Sky.Live's HTML renderer + VNode diff (`HtmlToVNode`,
  `assignSkyIDs`, `renderVNode`, `diffTrees`) so the same `view`
  function paints identically.
- `Cmd.perform` / `Sub.every` work as in Sky.Live and Sky.Tui.
- XSS hardening parity — focus-preserving DOM replacer,
  `__skyReviveScripts` for late-injected `<script>` tags.
- No HTTP server, no SSE, no session store. The bridge is
  `webview_go`'s `Bind()` + `Eval()` running in-process.

## What's out of scope for v0.1 (deferred to v0.2 / v0.3)

- `alwaysOnTop` / `transparent` / `decorated` window flags
- Tray icons + global hotkeys
- Native file / folder pickers
- `Std.Voice` intents
- Windows + Linux smoke validation

The closed-record `WindowCfg` type means a missing field surfaces
as a clean Sky TYPE ERROR at compile time instead of a runtime
panic. When v0.2 adds optional fields the record will open and
they'll absorb cleanly without breaking v0.1 callers.

## Comparison with the spike at `examples/29-webview-threejs-spike`

29 is a **spike** — proves WebGL2 + Three.js + 60 fps animation
run end-to-end inside `webview_go` on every target OS. It uses
Sky.Http.Server (HTTP) + a hand-written Go shim (no Sky.Webview
binding existed yet).

30 is the **MVP** — `Webview.app cfg` is now a first-class Sky
backend; the spike's "two terminals, two binaries" choreography
is replaced by a single `sky build` that produces a self-contained
desktop binary.

After v0.2 (Windows + Linux smoke + tray icons + always-on-top),
29 becomes redundant and gets retired.
