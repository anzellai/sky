# Sky.Webview — design plan (v0.15.x)

> Status: planning draft (2026-05-28). No code shipped yet. Reviewed against `docs/archive/tea-backends.md` + `docs/archive/std-ui-cross-platform.md` + `runtime-go/rt/tui.go` + `runtime-go/rt/live.go`.

## Part 1 — Recommendation

**Yes, add `Sky.Webview` as a first-class third backend.** Build it on **`github.com/webview/webview_go`** (the tiny C library with Go bindings — WebKit on macOS, WebKitGTK on Linux, WebView2/Edge on Windows). Use **one rendering mode: HTML-in-webview, reusing the existing Sky.Live VNode → HTML pipeline**, but **drop the HTTP/SSE transport** — instead bind a `__skyDispatch(eventId, payload)` JS function via webview's `Bind()` and call `Eval()` to push HTML patches. **Single backend, single rendering mode.** Native-widget rendering is the wrong tool for the driver use case (transparent floating window with custom WebGL content) and adding it would double the Std.Ui translator surface — punt to a future Sky.Gui (gio) when there's a real user demand.

**Window chrome (always-on-top, transparent, borderless, hotkeys, tray) is the gap webview_go can't fully fill** — its API is mostly title/size/min/max. For v0.1 ship only what `webview_go` natively supports (size, title, min/max size). For v0.2 add the harder fields (`alwaysOnTop`, `transparent`, `decorated`, `tray`, `hotkeys`) via per-platform Go shims (NSWindow on macOS via `purego`, SetWindowLong/SetWindowPos on Windows, `gtk_window_set_*` on Linux) called against the `Window()` pointer webview_go exposes. **Effort: MVP boot-window-render-Msg in ~3-5 person-days; full cross-backend `examples/22-tui-stopwatch-ui` triad in ~1 week; production window-chrome v0.2 in ~2-3 weeks; Std.Voice + hardening pushed to v0.3+.**

## Part 2 — Architecture

### Data flow end-to-end

```
Sky source (Main.sky)
    main = Webview.app { init, update, view, subscriptions, window, hotkeys }
        |> Task.run
        │
        ▼
Sky type checker (src/Sky/Type/Constrain/Expression.hs)
    new entry ("Webview", "app") — record-shaped, OPEN row, same model/msg
    vars as Tui.app/Live.app, plus required `window : WindowCfg` field.
        │
        ▼
Go codegen (src/Sky/Generate/Go/Kernel.hs / Canonicalise/Environment.hs)
    Std.Webview / Sky.Webview → kernel module "Webview"
    Webview.app → rt.Webview_app(cfg)
        │
        ▼
Generated main.go calls rt.Webview_app(cfg) |> rt.Task_run
        │
        ▼
runtime-go/rt/webview.go : Webview_app
    1. Re-uses live.go HtmlToVNode + renderVNode for first paint
    2. Spawns webview_go via webview.New(debug=false)
    3. SetTitle / SetSize from cfg.window
    4. webview_go.Bind("__skyDispatch", goCallback)   ← IPC inbound
    5. webview_go.Init(skyWebviewJS)                  ← embedded JS shim
    6. webview_go.SetHtml(initialShellHTML)           ← first paint
    7. setupSubscriptions (shared tea_subs.go)
    8. webview_go.Run() blocks until window closes
        │
   ┌────┴────────────────────────────────────────────────┐
   ▼                                                     ▼
Click in webview                                  Sub tick (Sub.every)
JS: __skyDispatch("ev_3", payload)                tea_subs goroutine
    │                                                     │
    └─────────────► msgCh chan any ◄──────────────────────┘
                        │
                        ▼
            update(msg, model) → (model', cmd)
                        │
            view(model') = Element / VNode
            diffTrees(prev, new) → patches
                        │
                        ▼
            webview.Dispatch(func() {
                webview.Eval("__skyApplyPatches("+json+")")
            })
```

### Reusing the Sky.Live HTML renderer — concretely

`runtime-go/rt/live.go` exposes (already exported, no new visibility needed):

- `HtmlToVNode(view-output any) vnode` — line ~520-ish, normalizes Std.Ui Element / Std.Html VNode into the runtime `vnode` struct.
- `assignSkyIDs(&vn, "r")` — stamps path IDs (line ~474 of live.go) so events can route back.
- `renderVNode(vn, sess.handlers) string` — line ~683-ish, produces HTML with `data-sky-ev-<event>` and `data-sky-hid` attributes, populating `sess.handlers` as a map[eventId]→handler Msg constructor.
- `diffTrees(prev, new)` (used in `handleEvent` patch path) — produces a JSON patch array consumable by `__skyApplyPatches` in the embedded JS.

Sky.Webview will **reuse all four** and bypass:

- The HTTP mux (no `mux.HandleFunc("/_sky/event", …)` — events arrive via `Bind()`).
- The SSE handler `handleSSE` (no streaming — patches go directly via `Eval()`).
- The CSRF middleware (no network boundary; the JS shim runs in the local webview).
- The session store (single session, single process, lives in a local `webviewSession` struct).

### Source tree layout

```
runtime-go/rt/
  webview.go          ← Webview_app, webviewSession, lifecycle
                        ~250-300 LOC (mostly delegation to live.go helpers)
  webview_chrome.go   ← v0.2: per-OS window-chrome shims
                        (build-tag split: webview_chrome_darwin.go,
                         webview_chrome_windows.go, webview_chrome_linux.go)
  webview_js.go       ← //go:embed of a ~150-line JS shim
                        (parallel to liveJS in live.go but smaller —
                         no HTTP fetch, just postMessage to Go).
  webview_safety.go   ← v0.2: clean shutdown, panic recovery analogue
                        of tui_safety.go (~80 LOC).
  webview_test.go     ← unit tests with a stub IPC; no real webview spawn

runtime-go/rt/embed/  ← new dir if we want assets out-of-line
  webview_shim.js     ← if we prefer embed.FS over inline string

sky-stdlib/Std/
  Webview.sky         ← Layer-3 Sky-source surface: type aliases for
                        WindowCfg, HotkeyCfg, the Webview.app facade
                        (no kernel call inside — just type re-exports
                        + helpers, delegating to "Std.Webview" kernel).

src/Sky/Canonicalise/Environment.hs
  Add: ("Std.Webview", "Webview")  and  ("Sky.Webview", "Webview")
       Line ~406 area (right after the Tui entries).

src/Sky/Type/Constrain/Expression.hs
  Add  ("Webview", "app")  arm with record signature.
  Add  ("Webview", "windowCfg") helper if we want a smart constructor.
  Pattern matches the Tui.app arm at line ~2104.
```

### Window-chrome API surface (Sky-side)

```elm
-- sky-stdlib/Std/Webview.sky (Layer-3, Sky source)

type alias WindowCfg =
    { title         : String
    , size          : (Int, Int)        -- (width, height) logical px
    , minSize       : Maybe (Int, Int)
    , maxSize       : Maybe (Int, Int)
    , position      : Maybe (Int, Int)  -- (x, y); Nothing = OS default
    , resizable     : Bool              -- default True
    -- v0.2 fields (NoOp on backends that don't support them yet):
    , alwaysOnTop   : Bool              -- macOS NSWindow.level, Win HWND_TOPMOST, GTK keep-above
    , transparent   : Bool              -- requires `decorated = False` on Win/Linux
    , decorated     : Bool              -- title-bar / borders; default True
    , minimisable   : Bool              -- default True (ignored on Linux WMs that don't honour)
    , closable      : Bool              -- default True
    , clickThrough  : Bool              -- default False; macOS NSWindow.ignoresMouseEvents,
                                        -- Windows WS_EX_TRANSPARENT, Linux input shape
    , skipTaskbar   : Bool              -- default False; useful for tray-only apps
    }


type alias HotkeyCfg msg =
    { combo : String      -- "Cmd+Shift+S", "Ctrl+Alt+H", platform-normalized
    , msg   : msg         -- dispatched on press; runtime suppresses the OS default
    }


type alias TrayCfg msg =
    { icon  : Bytes               -- PNG bytes (read via Sky.Core.File at init)
    , title : String              -- macOS menu-bar title (empty for icon-only)
    , menu  : List (TrayItem msg)
    }


type TrayItem msg
    = TrayLabel String              -- non-interactive label
    | TrayItem  String msg          -- "Quit" → QuitMsg
    | TraySeparator


type alias AppCfg model msg =
    { init           : () -> (model, Cmd msg)
    , update         : msg -> model -> (model, Cmd msg)
    , view           : model -> Element msg
    , subscriptions  : model -> Sub msg
    , window         : WindowCfg
    , hotkeys        : List (HotkeyCfg msg)  -- v0.2; empty list ok
    , tray           : Maybe (TrayCfg msg)   -- v0.2
    }


app : AppCfg model msg -> Task Error ()
app cfg = Ffi.kernel "Webview_app"
```

### Per-OS support matrix (honest)

| Field           | macOS (Cocoa via webview_go.Window() → NSWindow*) | Windows (WebView2 HWND)              | Linux (GTK3 GtkWindow)                  |
|-----------------|---------------------------------------------------|--------------------------------------|-----------------------------------------|
| size/title      | yes — webview_go.SetTitle/SetSize                 | yes                                  | yes                                     |
| alwaysOnTop     | yes — `setLevel: NSFloatingWindowLevel`           | yes — `SetWindowPos HWND_TOPMOST`    | yes — `gtk_window_set_keep_above`       |
| transparent     | yes — `isOpaque:NO` + `backgroundColor:clearColor` | yes — `DwmEnableBlurBehindWindow`/CompositionAttr | yes — RGBA visual via `gtk_widget_set_app_paintable` + compositor required |
| decorated=false | yes — `styleMask: borderless`                     | yes — strip `WS_CAPTION`/`WS_THICKFRAME` | yes — `gtk_window_set_decorated`     |
| clickThrough    | yes — `ignoresMouseEvents:YES`                    | yes — `WS_EX_TRANSPARENT` + LAYERED  | partial — input shape regions, WM dependent |
| tray            | yes — `NSStatusItem`                              | yes — `Shell_NotifyIcon`             | partial — `AppIndicator3` (Ubuntu) / `StatusNotifierItem` (KDE); GNOME pure-Wayland often won't show |
| global hotkeys  | yes — Carbon `RegisterEventHotKey`                | yes — `RegisterHotKey`               | partial — X11 `XGrabKey`; Wayland needs portal — broken on stock GNOME |

**Document the "partial" cells clearly** so users targeting Linux know the v0.2 ceiling.

### IPC (Go ↔ embedded JS shim)

webview_go gives us `w.Bind(name string, fn any)` (JS → Go) and `w.Eval(js string)` (Go → JS). No HTTP, no WebSocket.

```go
// runtime-go/rt/webview.go (sketch)

w.Bind("__skyDispatch", func(eventId string, payloadJSON string) string {
    // eventId is one of the handlers map keys we stamped during renderVNode.
    handlerCtor := sess.handlers[eventId]
    if handlerCtor == nil {
        return "" // silently drop — could be a stale click during patch
    }
    var payload any
    json.Unmarshal([]byte(payloadJSON), &payload)
    msg := sky_call(handlerCtor, payload)
    select {
    case msgCh <- msg:
    default:
        // bounded queue — emit a drop counter (parallels P42 SSE drop)
        atomic.AddInt64(&webviewMsgDropped, 1)
    }
    return ""
})

w.Bind("__skyLog", func(level, message string) string { … }) // bridge console.log → host log
```

```javascript
// runtime-go/rt/webview_shim.js (embedded via go:embed)
//
// Mirrors the click/submit/input wiring in liveJS, but instead of
// fetch("/_sky/event") it calls window.__skyDispatch — which webview_go
// has bound to a Go function. No fetch, no CSRF, no SSE.
document.addEventListener("click", function (e) {
    var el = e.target.closest("[data-sky-ev-click]");
    if (!el) return;
    e.preventDefault();
    window.__skyDispatch(el.getAttribute("data-sky-ev-click"), "");
});
// onSubmit, onInput, onChange follow the same shape.

window.__skyApplyPatches = function (patchesJson) {
    // Reuses the patch-applier logic from live.go's liveJS string
    // (factored out so both backends share it via a helper file).
};
```

The cleanest factoring: extract `__skyApplyPatches`, `__skyReviveScripts` (the C9-hardened version), and the focus-preserving DOM replacer into a separate Go const (e.g. `liveJSShared` in `live.go`), have both `liveJSWithCfgAndCsrfWithBase` AND `webview_shim.js` concatenate it in. Single source of truth for the XSS hardening.

## Part 3 — Library decision

I picked **webview_go**. Justification table:

| Criterion                     | webview_go                                            | Wails v3 (alpha)                                    | Tauri (Rust)                                                     |
|-------------------------------|-------------------------------------------------------|------------------------------------------------------|------------------------------------------------------------------|
| Toolchain on user dev box     | Go (already required) + system WebKit/WebView2       | Go + npm/Bun for asset bundler + Wails CLI          | **Rust toolchain + Node** — completely new toolchain on user box |
| Toolchain when Sky builds it  | cgo only (already used by `modernc.org/sqlite`)      | cgo + node build step inside our `sky build`        | Cross-language pipeline (cargo + node + sky) — infeasible        |
| Binary size (Hello world)     | ~3-5 MB on macOS (links system WebKit), ~5-8 MB Win, ~4-6 MB Linux | ~8-15 MB (bundles JS runtime)              | ~6-10 MB (bundles tao + wry)                                     |
| Cross-platform window-chrome  | Bare — title/size/min/max only. Anything else we shim per-OS. | Comprehensive — alwaysOnTop, translucent, frameless, tray all first-class | Best in class (tao crate)                                |
| Active maintenance            | Quiet but alive — releases on the Go binding through 2025-2026; underlying C library actively patched | Very active; v3 alpha ongoing through 2026 | Very active                                                      |
| "Single static binary" pitch  | Preserved — one Go binary + cgo to system webview     | Preserved on macOS/Linux; Windows can require WebView2 bootstrapper | Broken — needs `tauri build` shell + Rust toolchain         |
| Risk: macOS WebKit surface    | Uses system WKWebView — same as everything else      | Same                                                | Same                                                             |
| Risk: Windows WebView2 runtime | Required on pre-Win11. Bootstrapper download path. | Same; Wails provides installer hook                 | Same                                                             |
| Risk: Linux WebKitGTK         | Single shared system lib; works                       | Same                                                 | Same                                                             |
| Sky's existing precedent      | Sky.Tui already takes a cgo dep (`golang.org/x/term`) | Would require adding npm/Bun to our build           | Would require a parallel non-Go toolchain                        |

**Decision**: webview_go. We accept that we'll build our own per-OS window-chrome shims (v0.2). The reason: it's the only choice that preserves the **"`sky build` → one binary, no external toolchain"** pitch the user's CLAUDE.md (and the existing Sky.Tui/Sky.Live story) demand. Wails forces us to integrate a JS bundler into our build pipeline; Tauri forces a polyglot toolchain. Both kill the single-binary story.

### Risks specific to webview_go

1. **macOS WKWebView is the modern API** — no risk of the older "WebView" deprecation. Confirmed.
2. **Windows requires WebView2 runtime** — `evergreen` bootstrapper is automatic on Win11 and most updated Win10. Document a runtime-presence check that points users to Microsoft's distributable.
3. **Linux requires WebKitGTK 4.0 (libwebkit2gtk-4.0)** — on Ubuntu 22.04+/Fedora 38+ this is `apt install libwebkit2gtk-4.0-dev` (build) and `libwebkit2gtk-4.0-37` (runtime). The Sky build doc must list it.
4. **Quiet maintenance cadence on the Go binding** — the C library itself is actively patched; the Go shim is thin enough that we can fork-and-patch if necessary. Track upstream.
5. **No native dialogs out of the box** — file/open save dialogs need a per-OS shim. Punt to v0.3.

## Part 4 — MVP scope (v0.1, ~1 week)

### Single working example: `examples/29-webview-stopwatch-ui`

Why not extend `22-tui-stopwatch-ui` in-place? Because each example currently has one `Main.sky` + one `sky.toml` and the build outputs one binary. The cleanest demo of the cross-backend story is **three sibling example dirs** that all `import Examples.Stopwatch.View exposing (view, update, init, subscriptions, Model, Msg)` from a shared `examples/_shared/StopwatchCore/` package. That gives readers a one-glance "the same view function compiles for three backends" proof:

```
examples/_shared/StopwatchCore/
    src/StopwatchCore.sky          ← Model, Msg, init, update, view, subscriptions

examples/21-tui-stopwatch-ui/      ← renames 22; existing
    src/Main.sky                   ← Tui.app { … wiring StopwatchCore }

examples/22-live-stopwatch-ui/     ← NEW
    src/Main.sky                   ← Live.app { … wiring StopwatchCore }

examples/29-webview-stopwatch-ui/  ← NEW (this PR's deliverable)
    src/Main.sky                   ← Webview.app { … wiring StopwatchCore }
```

If reshuffling existing example numbers is undesirable (CI / docs may pin them), an alternative: keep 22 as-is, add `examples/29-webview-stopwatch-ui/src/Main.sky` that **copies** the StopwatchCore-shaped Model/update/view inline (a pragmatic mirror — not as elegant but doesn't churn existing examples).

### Smallest possible `Std.Webview` surface for v0.1

```elm
module Std.Webview exposing (app, WindowCfg, defaultWindow)

type alias WindowCfg = { title : String, size : (Int, Int) }

defaultWindow : WindowCfg
defaultWindow = { title = "Sky.Webview", size = (800, 600) }

app : { init, update, view, subscriptions, window } -> Task Error ()
app cfg = Ffi.kernel "Webview_app"
```

That's it. Six fields. Everything else (alwaysOnTop, transparent, hotkeys, tray) is **out of v0.1**.

### v0.1 contract — exactly what MVP must demonstrate

1. `sky build` of `examples/29-webview-stopwatch-ui` produces a single Go binary on macOS / Windows / Linux.
2. Running the binary opens a window with the stopwatch UI rendered from the same `view : Model -> Element Msg` function used by Sky.Live and Sky.Tui.
3. Clicking the "Start/Pause" button dispatches `Toggle`, the model updates, and the button label flips on the next frame — within 1 frame (16ms) of dispatch.
4. `Sub.every 100 Tick` drives the elapsed counter without freezing the UI thread.
5. Closing the window terminates the process cleanly (no orphaned goroutines, no panic, no terminal weirdness).

### Tests + verification gates

```
tests/Webview/AppShapeTest.sky
    Validates the type signature accepts the StopwatchCore record;
    a missing required field yields a compile error.

runtime-go/rt/webview_test.go
    Unit tests with a stub webview that captures Eval() strings and
    feeds Bind() callbacks synthetically. Asserts:
      - First render produces non-empty HTML.
      - Synthetic click → __skyDispatch → msgCh receives Msg.
      - update produces a model change → next Eval() contains
        __skyApplyPatches with the expected patch shape.
      - Bounded msgCh queue: 1000 synthetic clicks while update is
        slow does NOT panic; webviewMsgDropped counter increments.
      - Clean shutdown: close() unblocks Run(), no goroutine leak
        (count goroutines before/after, like tui_safety_test).

CI gate: tests/ run under `cabal test`.
CI gate: examples sweep `--build-only` arm includes
         examples/29-webview-stopwatch-ui to catch type errors
         even without a real webview spawn.
CI gate: separate `--smoke` arm spawns the binary in headless mode
         on macOS runners only (Linux CI may lack WebKitGTK; Windows
         CI may lack WebView2). Skipped elsewhere with a clear log.
```

### Explicitly OUT of v0.1 scope

- Multi-window / multi-session (everything single-process, single-window).
- alwaysOnTop / transparent / decorated (v0.2).
- Tray icon + tray menu (v0.2).
- Global hotkeys (v0.2).
- Native file dialogs (v0.3).
- Native mic / Std.Voice (v0.3 — see Part 5).
- Auto-update / packaging (.dmg / .msi / AppImage) — out of compiler scope; user concern.
- HTTP fallback (no, the whole point is that webview_go owns the boundary).

## Part 5 — Std.Voice (sketch)

```elm
module Std.Voice exposing (..)

type alias Transcript =
    { text : String, isFinal : Bool, confidence : Float }

type alias VoiceCfg msg =
    { onTranscript : Transcript -> msg
    , onError      : String -> msg
    , language     : String              -- "en-US", "ja-JP"
    , continuous   : Bool
    }

-- listen returns a Sub: starts the platform recogniser when present
-- in subscriptions, stops it when absent. Pure-data control flow,
-- same as Sub.every.
listen : VoiceCfg msg -> Sub msg
listen cfg = Ffi.kernel "Voice_listen"


-- Optional explicit imperative form, paralleling Cmd.perform:
startListening : VoiceCfg msg -> (Result Error () -> msg) -> Cmd msg
stopListening : Cmd msg
```

**Backend selection is invisible to Sky code.** The runtime picks:

- **Sky.Live runtime** → `Voice` kernel inlines a JS shim using the browser's `SpeechRecognition` API (Chromium-only realistically; Firefox doesn't ship it). User's browser handles permission prompt.
- **Sky.Webview runtime** → `Voice` kernel routes to a Go-side wrapper. **MVP path: whisper.cpp via CGo** (`github.com/ggerganov/whisper.cpp/bindings/go`). A bundled model file (path configurable via `cfg.modelPath`, default `~/.sky/models/ggml-base.en.bin`, download on first run with prompt). Audio capture via `malgo` (PortAudio binding, cgo).
- **Sky.Tui runtime** → returns `Sub.none` (no terminal voice). Document.

**When the user must know the backend**: `cfg.modelPath` is desktop-only (Sky.Live ignores it). The runtime can either ignore unknown fields silently (current Std.Live.app convention via row-variable extension) or emit a runtime warning if the user passes `modelPath` while running under Sky.Live.

**Recommend**: punt the full Std.Voice plan to a separate `docs/skyvoice/PLAN.md`. For Sky.Webview v0.1, ship `Webview.app` with no voice support; the user can wire a Web Speech API path themselves via `Std.Html` until Std.Voice ships.

## Part 6 — Security + reliability

This is the **CLAUDE.md non-negotiable** section.

### XSS surface in embedded content (parity with C9)

The same `__skyReviveScripts` allowlist + event-handler-drop hardening from `runtime-go/rt/live.go:5105` must apply to Sky.Webview. The cleanest path is to factor `__skyReviveScripts`, `__skyApplyPatches`, and `__skyReplaceHTMLPreservingFocus` into a shared `liveJSShared` const (new top-of-file in live.go) and have both:

- `liveJSWithCfgAndCsrfWithBase` (line 4676) concatenate it.
- The new `webview_js.go` embed it via `//go:embed` into the shim.

Single source of truth. New regression test in `webview_test.go` mirrors the C9 hardening test.

### Native API exposure

Default-deny. **Apps cannot call Go from JS** except through:

- `__skyDispatch(eventId, payload)` — only resolves event IDs the runtime stamped during renderVNode. An attacker injecting `window.__skyDispatch("notarealid", "")` gets a no-op (no handler matches, msgCh stays clean).
- `__skyLog(level, message)` — log-only, no side effects.

**No `bind` of file system / shell exec / process spawn.** Adding new native APIs in future versions must go through an explicit `Std.Webview.Native.allow [Fs, Shell]` opt-in record on the cfg, validated by the kernel before any binding happens. Document this principle in the module header comment of `Std/Webview.sky`.

### Mic privacy

- Default-deny. `Sub.none`-equivalent unless `Voice.listen` is in the active `subscriptions` return.
- First call triggers the OS permission prompt (macOS Info.plist must include `NSMicrophoneUsageDescription`; Windows manifests must declare `microphone`; Linux PortAudio uses PulseAudio's permission flow).
- Visual indicator: the runtime injects a small red-dot DOM element when the mic stream is open; cannot be CSS-suppressed (verified server-side that the indicator is in the rendered shell).
- Stop the stream the instant `Voice.listen` drops out of `subscriptions` — same lifecycle as `Sub.every` timers (see `tea_subs.go`).

### Sandbox model

webview_go does not sandbox `window.location = 'https://malicious.com'` — by default a navigation **will** happen. **Fix**: bind a beforeNavigate-equivalent. webview_go exposes `WebView.Navigate(url)` but no navigation interceptor in its standard API. Workaround for v0.1: install a JS-side guard in our shim that intercepts `<a href>` clicks and `window.location` assignment via a Proxy. **Document the limit**: user JS that bypasses the proxy can navigate. For v0.2 add a Content-Security-Policy `<meta>` header equivalent and document `frame-src 'self'`.

### Production gate parity

Mirror Sky.Live's `productionFromEnv()` (line 2443 of live.go). Sky.Webview reads `SKY_ENV=production`; in dev mode, inject a small "[Sky.Webview dev]" badge in the corner of the window (analogue to the Sky.Live dev banner); in production, no badge, no console.warn spew. The badge is a sibling of `#sky-root`, position:fixed, opacity 0.6, click-through.

### Memory safety / bounded queue

- `msgCh chan any` is `make(chan any, 32)` like Sky.Tui (tui.go:161).
- Dispatch path uses `select { case msgCh <- msg: default: atomic.AddInt64(&webviewMsgDropped, 1) }` so a slow update doesn't block the webview thread or the IPC bridge.
- Drop counter exported via observability ingest (parallels `sseDropped` from P42).
- Clean shutdown: when `webview.Run()` returns (window closed), close `doneCh`, stop subscription goroutines (re-use `subMgr.stopAll()` from `tea_subs.go`), wait for in-flight dispatches with a 100ms drain timeout, then return `Ok ()`.
- Panic-recover wrapper around every `sky_call` site (parallels tui_safety.go's `safeGo`).

## Part 7 — Risks + unknowns (validate in MVP)

1. **Transparent windows portability** — webview_go's `Window()` pointer is `unsafe.Pointer` to a per-OS handle. Calling `NSWindow.setOpaque:NO` via purego on macOS is well-known; doing the equivalent on GTK requires an `app_paintable=TRUE` on a window before realize and an RGBA visual, which webview_go might already realize before user code gets the pointer. **Action**: spike a 50-LOC test in v0.1 (not user-visible, but build-tagged behind `experimental_chrome`) that proves we can call the right calls at the right time on each OS before committing to v0.2 scope.
2. **WebKitGTK CSS coverage for custom WebGL content** — WebKitGTK is generally 1-2 minor versions behind upstream Safari WebKit. Recent features (WebGPU) are not available; WebGL2 is. **Action**: validate by loading a representative WebGL scene in a `webview_go` Hello world on Ubuntu 22.04 + 24.04 before committing the user to the Linux path.
3. **WebGL2 + hardware acceleration in webview_go** — macOS WKWebView and Windows WebView2 both forward to the native compositor; Linux WebKitGTK can fall back to software rendering when DRI is missing. **Action**: document a `SKY_WEBVIEW_DEBUG=1` env var that logs the GL renderer string at startup so users know whether they're software-rasterising.
4. **Cold-start latency** — Hello binary cold start under webview_go on macOS: typically ~80-150ms to first paint (system WKWebView). Windows: 200-500ms (WebView2 process spawn). Linux: 150-300ms (WebKitGTK init). **Action**: log it; if Windows cold start is too slow for the always-on-top floating use case, consider an "instant-on" mode that hides the window for the first paint cycle.
5. **macOS notarisation / Gatekeeper** — out of compiler scope but document: a `sky build` binary is not codesigned, so users distributing must `codesign` + `notarytool submit`. Affects how the floating companion is delivered to non-developer users. Not a v0.1 blocker for the user's own dev box.
6. **webview_go thread affinity** — webview_go requires the main goroutine for `Run()` AND for `Dispatch()`. Our patch-emit path (Go side calling Eval after update) must always `webview.Dispatch(func() { w.Eval(…) })` from goroutines that aren't the main thread, or the call will deadlock. Mirror the pattern in `tui.go`'s render loop that already restricts writes to the main loop.

## Part 8 — Effort estimate (person-days)

| Stage | Days |
|---|---|
| MVP boot-window-render-Msg (Webview_app + webview_shim.js + handlers map reuse + one Std.Webview.sky module + ("Webview","app") kernel arm) | **3-5** |
| Cross-backend `_shared/StopwatchCore` extraction + three sibling example dirs + docs | **1-2** |
| Webview_test.go (stub-based) + sweep arm + smoke arm | **1-2** |
| v0.1 shipping (tag, changelog, sky-lang.org doc page) | **0.5** |
| **v0.1 subtotal** | **~5.5-9.5** |
| v0.2: per-OS window-chrome (alwaysOnTop, transparent, decorated, clickThrough) — 1.5d × 3 OS + integration | **5-7** |
| v0.2: tray (per-OS) | **3-5** |
| v0.2: global hotkeys (per-OS, including Wayland portal caveats) | **2-3** |
| v0.2: hardening — beforeNavigate proxy, CSP shim, prod-gate badge, dropped-msg counter + observability | **1-2** |
| **v0.2 subtotal** | **~11-17** |
| v0.3: Std.Voice (separate doc; not scoped here) | **see Std.Voice plan** |

**Headline**: ~1 week for shippable v0.1 that proves the cross-backend triad. ~3 weeks more for production-grade window chrome (the user's actual driver case). Std.Voice is a separate workstream of comparable size.

---

## Open design questions to resolve before coding begins

1. **Three sibling example dirs vs in-place extension of `22-tui-stopwatch-ui`?** Plan prefers siblings; defer to user.
2. **`Std.Webview` vs `Sky.Webview` qualifier preference?** Pattern matches Tui (both registered). Recommend both, like the existing `("Sky.Tui", "Tui"), ("Std.Tui", "Tui")` pair.
3. **JS shim location: inline string in `webview_js.go` (matches `liveJS` in live.go) or `//go:embed runtime-go/rt/embed/webview_shim.js`?** Inline is uglier but matches the existing pattern. Recommend inline for parity; refactor both to embed in a single follow-up commit.
4. **Drop the HTTP fallback entirely, or keep a "Sky.Webview also serves the same HTML on a localhost:port for inspection"?** The latter doubles maintenance for ~0 user benefit. Drop.
5. **Window record extension vs closed?** Closed for v0.1 to get good error messages. Open for v0.2 once we know which optional fields stabilise.

---

## Critical files for implementation

- `runtime-go/rt/live.go` (HtmlToVNode, renderVNode, diffTrees, the JS bootstrap string — must be extracted into a shared const before Sky.Webview can reuse it cleanly)
- `runtime-go/rt/tui.go` (the cleanest existing main-loop precedent — msgCh wiring, tea_subs integration, clean-shutdown discipline; mirror its structure in webview.go)
- `src/Sky/Type/Constrain/Expression.hs` (add `("Webview", "app")` arm around line 2104 next to `("Tui", "app")`)
- `src/Sky/Canonicalise/Environment.hs` (add `("Std.Webview", "Webview")` and `("Sky.Webview", "Webview")` entries around line 406)
- `sky-stdlib/Std/Webview.sky` (new — Layer-3 surface: WindowCfg / HotkeyCfg / TrayCfg type aliases + `app` Ffi.kernel passthrough)

---

## Executive summary

**Recommendation**: build `Sky.Webview` as a third Std.Ui backend on top of `github.com/webview/webview_go`, reusing Sky.Live's `HtmlToVNode` → `renderVNode` → `diffTrees` pipeline but replacing the HTTP/SSE transport with webview_go's `Bind()`/`Eval()` JS bridge. **One backend, one rendering mode** (HTML-in-webview) — native-widget rendering is the wrong tool for the driver use case (floating window with custom WebGL content) and would double the translator surface. **MVP v0.1 scope**: title/size-only window, single working `examples/29-webview-stopwatch-ui` mirroring the Sky.Tui sibling, hardening parity with the existing XSS guard, bounded msgCh with drop counter. **Effort: ~1 week for v0.1, ~3 more weeks for v0.2 window chrome (alwaysOnTop / transparent / clickThrough / tray / hotkeys), Std.Voice on a separate plan.** **Top 3 risks**: (1) per-OS window-chrome shims need spike validation on each platform before v0.2 estimates harden; (2) Linux WebKitGTK CSS/WebGL2 fidelity for custom Three.js / WebGL must be verified end-to-end before committing the user to Linux; (3) webview_go's main-thread affinity for `Eval()`/`Dispatch()` is easy to deadlock and must be enforced runtime-wide.

### Sources

- [Wails v3 alpha roadmap](https://v3alpha.wails.io/status/)
- [GitHub — webview/webview_go](https://github.com/webview/webview_go)
- [GitHub — webview/webview (C library)](https://github.com/webview/webview)
- [Wails Window options](https://v3.wails.io/features/windows/options/)
