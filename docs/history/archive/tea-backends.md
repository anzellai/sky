# Sky's TEA Backends — Architecture & GUI Roadmap

Status: Exploratory (branch `exp/tea-core`)
Author notes from session 2026-05-08

## Where we are

Sky's TEA shape (`init / update / view / subscriptions` + `Cmd`/`Sub`)
was originally specialised for HTML+SSE in `Sky.Live`. The `exp/tea-core`
branch demonstrates the same shape generalising to non-HTML targets.

Two new backends shipped on this branch:

| Backend | View shape | Input source | Status |
|---|---|---|---|
| **Sky.Cli** | `Model -> String` (prompt) | `onLine : String -> Msg` | Working, tested |
| **Sky.Tui** | `Model -> String` (full frame) | `onKey : KeyEvent -> Msg` | Working, tested |
| **Sky.Live** | `Model -> H.VNode` | DOM events via `data-sky-ev` | Existing, unchanged |

Both new backends share `runtime-go/rt/tea_subs.go` for subscription
management — `Sub.every` and `Sub.batch` finally drive ticker
goroutines into the same Msg pipe used for keypress/line input. All
three backends use the same `Std.Cmd` / `Std.Sub` kernel modules.

The architecture extracted naturally without needing a new
`Sky.Core.Tea` abstraction layer: kernel modules ARE the abstraction.
The shared types live in the runtime; each backend just brings its
own view shape + main loop.

## What this proves about callbacks

The `exp/tea-core` work demonstrates that TEA's shape eliminates the
"callback API" problem entirely for the wide class of stateful event
loops:

- **Keypresses become Msgs.** `onKey "space"` = `Toggle`, no closure.
- **Timers become Subs.** `Sub.every 100 Tick` = ticker goroutine
  that pushes a Msg, no `time.Ticker` user code.
- **Self-reference doesn't arise.** State lives in `model`. The view
  reads from it. There are no widget identities for the user to
  manage.

The Fyne stopwatch case (toggle button updating its own label) — the
original motivating example for `*Self` variants and γ-codegen — is
trivially expressed in `Sky.Tui` without any of that machinery. The
"Pause" / "Start" string is just `if model.running then ... else ...`
in the view function. See `examples/21-tui-stopwatch/src/Main.sky`.

This collapses the earlier "γ-Self codegen variant" proposal: TEA
solves it cleanly enough that we don't need it.

---

## The GUI question

A native GUI backend (`Sky.Gui`) is the obvious next slice. The user's
flagged scepticism about Fyne (and openness to `golang.org/x/exp/shiny`)
prompted a deeper look at the landscape.

### Survey

| Library | Last meaningful commit | Architecture | Production-ready | TEA fit |
|---|---|---|---|---|
| **Fyne** | Mar 2026 | Retained-mode, Material-inspired widgets | Yes | OK |
| **gio** | May 2026 | Immediate-mode, GPU-rendered | Yes (Tailscale ships it) | **Best** |
| **shiny** | Real shiny code: ~2024 | Low-level toolkit (driver, screen) | **No** — effectively abandoned | Poor |
| **Wails** (webview) | Active | HTML/JS in native webview, Go backend | v3 ALPHA, v2 stable | **Excellent** (reuses Sky.Live) |
| **webview/webview_go** | Aug 2024 | Tiny C library + Go binding | Yes, but quiet | Excellent |

#### Fyne

Pros: mature, big widget set, easy API, cross-platform (desktop +
mobile). Decent activity — committed under
`github.com/fyne-io/fyne` regularly.

Cons: retained-mode means each `widget.NewButton` is a long-lived
object with internal state. To fit TEA you'd have to diff a Sky-side
widget tree against a parallel cache of Fyne widgets and mutate
fields. Doable but architecturally awkward — Sky.Live's VDom diff has
this problem and it's the most complex part of `live.go`.

#### gio

Pros: **Immediate-mode is the natural fit for TEA's pure-view
function.** Each frame, the user's `view model` produces a description
of widgets to draw; gio renders them; no retained tree, no diffing.
Direct mental model: "given this model, what should appear on screen."
Used in production by Tailscale's mobile app. Active development
(commits May 2026). GPU-rendered for performance.

Cons: Lower-level than Fyne. The dev (in this case, the Sky.Gui
runtime author — not user code) handles layout computation, hit
testing, rendering. Bigger upfront cost in the Sky.Gui implementation.

#### shiny

The earlier discussion considered `golang.org/x/exp/shiny` as future-
proof because it's in the official `golang.org/x` namespace.
**Investigation says no.** The `exp/shiny` directory's last
*substantive* commits are from 2024-2025; recent activity is just
dependency bumps. The project is explicitly experimental and has been
in maintenance-only mode for years. Building Sky.Gui on shiny would
mean adopting a stale foundation. **Skip.**

[shiny on pkg.go.dev](https://pkg.go.dev/golang.org/x/exp/shiny) /
[golang-nuts: is shiny dead](https://groups.google.com/g/golang-nuts/c/Gazd5rzJM_s)

#### Wails / webview

This is the surprise of the survey. Wails (and the underlying
[webview](https://github.com/webview/webview) C library) embed a
**native webview** (WebKit on macOS, WebView2/Edge on Windows, GTK on
Linux) and serve a local HTTP frontend to it. **Sky already has all
the infrastructure for this** — Sky.Live is exactly an HTML server
with SSE-driven updates. To produce a "desktop app", you'd:

1. Run the existing Sky.Live program on a localhost random port
2. Spawn a webview pointing at it
3. Hide the server from the network (bind 127.0.0.1 only)

Effectively zero new abstraction. Reuses every Std.Html / Std.Ui /
Sky.Live primitive that already exists. The user writes the same code
they'd write for Sky.Live; the runtime decides "do I bind a public
port and serve real users, or do I bind localhost and pop a webview?"

The downsides:
- **Bundle size.** ~10-50 MB binary depending on platform (the
  webview library + its dependencies). Compared to a gio binary
  (~5-10 MB) or a Fyne binary (~15-25 MB), it's competitive on macOS
  and Linux (uses system WebKit/GTK), heavier on Windows (bundles
  WebView2 runtime if not present).
- **Distribution.** Mac App Store / iOS App Store policies are
  picky about webviews. For broad distribution this is a real
  consideration.

For most "internal tools, dashboards, configuration UIs, dev
utilities" the bundle-size / distribution concerns don't apply, and
the development time saved is enormous.

[wails.io](https://wails.io/) /
[webview/webview_go](https://github.com/webview/webview_go)

---

## Recommendation

Build **two** GUI backends, in this order:

### 1. Sky.Webview — fast win, ~1-2 days

A thin wrapper that:
- Imports a Go webview library (`github.com/webview/webview_go` or
  similar)
- Starts the existing Sky.Live HTTP server on localhost:rand-port
- Spawns a webview pointed at `http://127.0.0.1:port`
- Closes the server when the webview window closes

User code is **identical to a Sky.Live program**. Different `main`:

```elm
main =
    Webview.app
        { init = init, update = update, view = view
        , subscriptions = subs
        , routes = [ route "/" Counter ], notFound = Counter
        , title = "My App", size = (400, 300) }
```

Internally `Webview.app` calls `Live_app` machinery + spawns the
webview. Maybe 100 lines of new Go code, zero new Sky kernel
functions beyond `Webview.app` (which delegates 90% to `Live_app`).

This unblocks ~80% of "I need a desktop app" use cases on day one.

### 2. Sky.Gui (gio-based) — bigger investment, ~3-5 weeks

For the cases where webview isn't acceptable: hardware-constrained
environments, platforms without a system webview, App Store
distribution concerns, GPU-driven UIs (charts, games, 3D).

Architecture:
- View shape: `Model -> Gui.Widget` — a Sky-side ADT of layout
  primitives (`Gui.column`, `Gui.row`, `Gui.label`, `Gui.button`,
  `Gui.textInput`, `Gui.image`, ...)
- Runtime translates each frame to gio operations.
- Immediate-mode is the natural match — no retained widget tree, no
  diff, just `view model` -> draw -> `view model` -> draw.
- Subscriptions for window resize, mouse, keyboard, animation
  frames.

Path: same shape as Sky.Tui but with a gio renderer instead of ANSI.
The TEA loop in `tui.go` is the template; replace `tuiRender` with
`giouiRender`, replace `tuiReadKeys` with `giouiPollEvents`.

[gioui.org](https://gioui.org/)

### 3. Sky.Fyne — leave existing, don't extend

Keep `examples/11-fyne-stopwatch` working as a curiosity / direct-FFI
demo, but de-prioritise Fyne as the primary native backend. The
retained-mode mismatch with TEA + the Wails-via-Sky.Live alternative
covering most use cases mean Fyne earns less attention than originally
planned.

**Don't** invest in `*Self`-variant codegen or `mfix`-style fixed-point
combinators. TEA + Webview/gio cover the use cases those were
designed to solve.

---

## Branch summary

`exp/tea-core` currently has **two commits**:

1. **`Sky.Cli`** — line-oriented TEA backend, validated with a
   counter example. ~150 lines of runtime + 5 Sky-side bindings.
2. **`Sky.Tui` + `Sub.batch`** — full-screen terminal backend, raw
   mode, alt-screen, Sub.every, validated with a stopwatch. ~280
   lines of new runtime + shared sub manager (~110 lines).

Total cost of the two backends: ~550 LoC of Haskell+Go. Validates
the core architectural claim: **TEA's abstraction layer is
load-bearing enough that adding a backend is a small, contained
piece of work**, mostly main-loop plumbing.

Suggested next merges (separate PRs):

- **PR 1**: Sky.Cli alone — small, clean, useful for scripts.
- **PR 2**: Sub.batch + tea_subs.go + Sky.Tui — needs `golang.org/x/term`
  added to runtime go.mod, slightly bigger surface.
- **PR 3**: Sky.Webview — independent of the above two; adds webview
  binding, reuses `Live_app`.

---

## Sources

- [pkg.go.dev — golang.org/x/exp/shiny](https://pkg.go.dev/golang.org/x/exp/shiny)
- [golang-nuts: Is x/exp/shiny dead?](https://groups.google.com/g/golang-nuts/c/Gazd5rzJM_s)
- [github.com/fyne-io/fyne](https://github.com/fyne-io/fyne)
- [gioui.org](https://gioui.org/)
- [wails.io](https://wails.io/)
- [github.com/webview/webview_go](https://github.com/webview/webview_go)
- [LogRocket — Best GUI frameworks for Go](https://blog.logrocket.com/best-gui-frameworks-go/)
