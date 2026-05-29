# Sky.Webview

A first-class desktop UI backend. Same TEA shape as Sky.Live and
Sky.Tui (`init` / `update` / `view` / `subscriptions`), but the
runtime opens a native system webview window and renders the same
`Std.Ui.Element` tree to HTML in-process. No HTTP server, no SSE, no
session store.

## Why

The cross-backend story:

| Backend | View target | Best for |
|---|---|---|
| **Sky.Live** | Web (HTTP + SSE, multi-tenant) | Apps with a server, multiple users, public URL |
| **Sky.Tui** | Terminal (ANSI cells) | CLI tools, headless dashboards, SSH sessions |
| **Sky.Webview** | Native desktop window | Single-user desktop apps, packaged binaries, offline-first |

Write `view : Model -> Element Msg` once. Pick the backend at
`main`. The same Std.Ui tree paints under all three.

## Status (v0.1 MVP)

Shipped:

- `Webview.app cfg` — TEA entry; same shape as `Live.app` /
  `Tui.app`.
- `WindowCfg = { title : String, size : (Int, Int) }` —
  closed-record (better HM error messages).
- Reuses Sky.Live's HTML renderer + VNode diff
  (`HtmlToVNode`, `assignSkyIDs`, `renderVNode`, `diffTrees`) —
  the same Std.Ui tree renders identically.
- XSS hardening parity with Sky.Live: focus-preserving DOM
  replacer, `__skyReviveScripts` for late-injected `<script>`
  tags.
- Bounded `msgCh chan any` — drops surface in stderr; the update
  loop cannot dead-lock.
- macOS (WKWebView) is the only smoke-validated platform; the
  runtime compiles on Windows + Linux but v0.2 owns the cross-OS
  smoke + tray-icon + always-on-top work.

Out of scope for v0.1 (deferred to v0.2 / v0.3):

- `alwaysOnTop` / `transparent` / `decorated` window flags
- Tray icons + global hotkeys
- Native file / folder pickers
- `Std.Voice` intents
- Windows + Linux smoke

## Quick start

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Webview as Webview
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Ui exposing (Element)


type alias Model = { count : Int }
type Msg = Inc | Dec


init : () -> ( Model, Cmd Msg )
init _ = ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Inc -> ( { model | count = model.count + 1 }, Cmd.none )
        Dec -> ( { model | count = model.count - 1 }, Cmd.none )


subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


view : Model -> Element Msg
view model =
    Ui.row [ Ui.spacing 12 ]
        [ Ui.button [] { onPress = Just Dec, label = Ui.text "-" }
        , Ui.text (String.fromInt model.count)
        , Ui.button [] { onPress = Just Inc, label = Ui.text "+" }
        ]


main =
    Webview.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , window = { title = "Counter", size = ( 480, 360 ) }
        }
        |> Task.run
```

## Platform requirements

| OS | What you need | v0.1 |
|---|---|---|
| **macOS 12+** | WKWebView (ships with the OS) | ✅ smoke-validated |
| **Windows 11** | Edge WebView2 runtime ([evergreen distributable](https://developer.microsoft.com/en-us/microsoft-edge/webview2/)) | ⚠️ builds, untested |
| **Ubuntu 22.04+** | `libwebkit2gtk-4.0-37` (Debian/Ubuntu) or `webkit2gtk4.0` (Fedora/Arch) | ⚠️ builds, untested |

`webview_go`'s cgo bindings provide the system-webview bridge.
Builds without cgo (`CGO_ENABLED=0`) get a stub that returns an
`Err Error` from `Webview.app cfg` instead of panicking at link
time.

## How it works

Bird's-eye view:

1. **First render.** `view model` → `Std.Ui.Element` → HTML body
   via the Sky.Live renderer. The body lands inside
   `<div id="sky-root">` via `webview.SetHtml`.
2. **Event dispatch.** Every DOM event (`click`, `input`, `submit`,
   …) carries a `data-sky-hid` attribute identifying the handler
   in the renderer-built map. The JS shim's `__skyBindEvents`
   wires native listeners that forward `(handlerId, args)` to the
   Go-side `__skyDispatch` Bind callback. The bound function looks
   up the Msg ctor and pushes it onto a bounded `msgCh`.
3. **Update loop.** A goroutine drains `msgCh`, runs
   `update msg model`, computes the new VNode tree, and diffs it
   against the previous. Patches are JSON-encoded and sent over
   the bridge as `__skyApplyPatches([…])` via `webview.Eval`.
4. **Clean shutdown.** When the user closes the window,
   `webview.Run()` returns, the subscription manager stops every
   ticker, and `Task.run` resolves to `Ok ()`.

## Comparison with Sky.Live

| Concern | Sky.Live | Sky.Webview |
|---|---|---|
| Wire | HTTP + SSE | In-process `Bind` + `Eval` |
| Session store | memory / sqlite / redis / postgres / firestore | None — single-user, single-process |
| CSRF | Yes, per-session | N/A |
| Reconnect banner | Yes | N/A |
| URL routing | `routes = [ … ]`, history, `sky-nav` | N/A — desktop apps don't have an address bar |
| Multi-tenant | Yes | No — one window, one model |
| Auto-mounts dev console | Yes | No — `/_sky/*` paths only meaningful with HTTP |
| Cross-platform | Anywhere with a browser | macOS / Windows / Linux with a system webview |

The convergence point is `view : Model -> Element Msg`. Identical
across Sky.Live, Sky.Tui, and Sky.Webview.

## Environment variables

| Env | Default | Effect |
|---|---|---|
| `SKY_WEBVIEW_DEBUG` | `1` (on) | Enable webview DevTools (right-click → Inspect). Set `0` / `false` for prod-tightened builds. |

## See also

- `examples/31-webview-stopwatch-ui` — the v0.1 reference.
- `examples/22-tui-stopwatch-ui` — same `view` rendered to the
  terminal.
- `examples/29-webview-threejs-spike` — the WebGL2 + 60 fps spike
  that de-risked the choice of `webview_go`.
