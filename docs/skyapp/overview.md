# Std.App — one builder, one `--target`

`Std.App` describes an app **once** with a single `Std.Ui.Element` view and builds
it for many targets. You write the shared TEA core (`init` / `update` / `view` /
`subscriptions`), and a build-time `--target family[:variant]` picks the backend —
you never import or choose `Std.Live` / `Std.Spa` / `Std.Tui` / `Std.Cli` /
`Std.Webview` yourself. `Std.App` *composes* them.

> **`Std.Ui` vs `Std.Html`.** `Std.App` is for the **cross-platform, `Std.Ui`**
> world — one `Element` view that renders to web, native (wasm), and terminal. If
> you build views with **raw `Std.Html`** for a server-rendered web (or a desktop
> app that is just that web app in a window), use **`Std.Live`** directly — that's
> the `web` / `desktop` bare targets below.

## The target axis

One extendible axis, `family[:variant]`: the bare family is the simplest delivery;
naming a platform opts into a true native build. Invalid combinations are rejected
at parse time (`web:ios` → *"did you mean `mobile:ios`?"*):

Bare family = a Sky.Live delivery; a named platform = a native (wasm) build:

| `--target` | delivery | backend |
|---|---|---|
| `web` · `tablet` | server-driven HTML + SSE (responsive) | **Sky.Live** |
| `desktop` | Sky.Live in a native window (server + webview) | **Sky.Live** + webview |
| `terminal:tui` (or bare `terminal`) · `terminal:cli` | full-screen ANSI · line text | **Sky.Tui / Sky.Cli** |
| `web:app` · `desktop:mac\|windows\|linux` · `tablet:ipad\|android` · `mobile:ios\|android` | client wasm (auto-split) + native shell | **Sky.Spa** — see [client targets](#client-targets) |

## The entry — `main = App.run app`

Describe `app` once, run it with `App.run`; `--target` (optional, defaults to
`web`) picks the backend:

```elm
-- doc-example: skip  (illustrative fragment; Page/Model/Msg/init/update/view/subscriptions elided)
module Main exposing (main)

import Std.App as App
import Std.Ui as Ui


app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withRoutes [ ( "/", Home ) ]
        |> App.withNotFound NotFound


main : Task Error ()
main =
    App.run app
```

(No `App`-type annotation needed — inference handles it, including the capability
flag below.)

```bash
sky run                          # defaults to web (Sky.Live)
sky build --target terminal:tui  # a TUI
sky build --target desktop       # Sky.Live in a native window
sky check                        # type-checks the core (target-scoped for a backend)
```

The build resolves `--target`, rewrites `App.run` → the target's `run<Backend>`
under
`.skyapp/<target>/` and builds it; dead-code elimination prunes the four unused
backends, so a `terminal:cli` binary never links the web or desktop runtimes.

**Runner-direct** — pick the backend in source (no `--target` needed):

```elm
-- doc-example: skip  (illustrative fragment; Model/Msg/init/update/view/subscriptions elided)
module Main exposing (main)

import Std.App as App


main : Task Error ()
main =
    App.runLive (App.app { init = init, update = update, view = view, subscriptions = subscriptions } |> App.withNotFound NotFound)
```

`sky build src/Main.sky` builds it like any other app. The runners are `runLive`,
`runTui`, `runCli`, `runWebview`, and `runSpa`.

## Capability builders

Each `with…` builder adds a capability a target may require; targets that don't
use it ignore it. The builders are uniform (`… -> App … -> App …`), so you
mix-and-match — pre-inject whatever your targets need:

- `App.withRoutes [ ( path, page ) ]` + `App.withNotFound page` — routing.
- `App.withWindow title width height` — desktop window.
- `App.withInput onLine` — a terminal line/text input handler.

**`notFound` is mandatory for `web`, enforced by the type.** `App.withNotFound`
flips a phantom capability flag on the `App` (`NoFallback` → `HasFallback`), and
`App.runLive` (the web backend) requires `HasFallback` — so building a `web` app
without a fallback page is a **compile error**, reprinted as *"target 'web'
requires a fallback page — add `|> App.withNotFound <page>`"*. A terminal-only app
(`NoFallback`) is never forced to add one; it just can't target `web` until it
does. Everything else (`routes`, `window`, `input`) stays optional.

## View adapter

You write one `view : model -> Element msg`. `Std.App` adapts it per backend:
`Ui.layout []` for the HTML family (Live/Spa/Webview), the `Element` directly for
Tui, and a best-effort `Element`→text flatten for `terminal:cli` (2-D layout has
no lossless text form — `Std.Cli.program` stays the escape hatch for a
hand-authored `String` view).

## Client targets — same source, no `Std.Spa` entry

`web:app` / `mobile:*` / `tablet:*` deliver a **client wasm** build that
auto-splits your effects to a backend. From the **same `Std.App` source** — the
build synthesises a `Spa.app` from your `App.app` value (referencing your
`update`/`view`/… directly so the *existing, unchanged* auto-split can partition
it), then splits + builds it:

```bash
sky build --target web:app     src/Main.sky   # wasm client + backend (PWA / offline)
sky run   --target web:app     src/Main.sky   # serves the wasm shell + /_rpc
sky build --target mobile:ios  src/Main.sky   # native app (needs a Mac to sign)
```

So `Std.App` covers **every** target from one source — you never write or import
`Std.Spa`. (The derivation reads the standard `sky fmt`'d `App.app { init = …,
update = …, view = …, subscriptions = … }` form; if it can't, it says so and you
can drop to a `Std.Spa` entry.)

See also: `sky doc Std.App`, `docs/skylive/overview.md`, `docs/skyspa/overview.md`,
and the design rationale in `docs/design/unified-app-builder.md`.
