# Std.App — one builder, one `--target`

`Std.App` describes an app **once** and builds it for many targets. You write the
shared TEA core (`init` / `update` / `view` / `subscriptions`) with a single
`view : model -> Element msg`, and a build-time `--target family[:variant]` picks
the backend. It *composes* the existing shape frameworks (Sky.Live / Sky.Tui /
Sky.Cli / Sky.Webview) — it does not replace them; each stays available for the
"I know exactly which shape I want" case.

## The target axis

One extendible axis, `family[:variant]`, where the variant is the single
irreducible choice a family can't infer for you. Invalid combinations are rejected
at parse time (`web:ios` → *"did you mean `mobile:ios`?"*):

| `--target` | runs as | = framework |
|---|---|---|
| `web` | server-driven HTML + SSE | Sky.Live |
| `terminal:tui` (or bare `terminal`) | full-screen ANSI | Sky.Tui |
| `terminal:cli` | line-based text | Sky.Cli |
| `desktop[:mac\|windows\|linux]` | native window | Sky.Webview |
| `web:app` · `mobile:ios\|android` · `tablet:*` | client wasm (auto-split) | **Sky.Spa** — see [client targets](#client-targets) |

## Two entry forms

**Dispatched** — expose `app` (no `main`); `--target` picks the backend:

```elm
-- doc-example: skip  (illustrative fragment; Page/Model/Msg/init/update/view/subscriptions elided)
module Main exposing (app)

import Std.App as App
import Std.Ui as Ui


app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withRoutes [ ( "/", Home ) ]
        |> App.withNotFound NotFound
```

(No type annotation needed — inference handles the `App` type, including the
capability flag below.)

```bash
sky run   --target web           # server-driven web (Sky.Live)
sky build --target terminal:tui  # a TUI
sky build --target desktop       # a native window
sky check                        # type-checks the core (target-scoped for a backend)
```

The build stages a derived entry `main = App.run<Backend> Main.app` under
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

## Client targets

`web:app` / `mobile:*` / `tablet:*` deliver a **client wasm** build, which
auto-splits your effects to a backend — and the splitter needs your `update`
visible to it. That is exactly what a **Sky.Spa** entry provides (`import
Std.Spa`, `main = Spa.app …`), so client targets use a Sky.Spa entry and the same
`--target` grammar:

```bash
sky build --target web:app  src/Main.sky   # on a Std.Spa entry
sky build --target mobile:ios src/Main.sky
```

`Std.App` covers the four non-split families natively (`web` · `terminal:tui` ·
`terminal:cli` · `desktop`); `Sky.Spa` covers the client-wasm families. The
target axis is shared across both.

See also: `sky doc Std.App`, `docs/skylive/overview.md`, `docs/skyspa/overview.md`,
and the design rationale in `docs/design/unified-app-builder.md`.
