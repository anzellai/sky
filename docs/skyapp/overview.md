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
        |> App.withRoutes [ App.route "/" Home ]
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

- `App.withRoutes [ App.route path page ]` + `App.withNotFound page` — routing
  (`App.route` / `App.routeParam` / `App.api` build the `Route` values; you never
  pass a raw `( path, page )` tuple).
- `App.withWindow title width height` — desktop window.
- `App.withInput onLine` — a terminal line/text input handler.

**`notFound` is mandatory for `web`, enforced by the type.** `App.withNotFound`
flips a phantom capability flag on the `App` (`NoFallback` → `HasFallback`), and
`App.runLive` (the web backend) requires `HasFallback` — so building a `web` app
without a fallback page is a **compile error**, reprinted as *"target 'web'
requires a fallback page — add `|> App.withNotFound <page>`"*. A terminal-only app
(`NoFallback`) is never forced to add one; it just can't target `web` until it
does. Everything else (`routes`, `window`, `input`) stays optional.

## Configuration — `withBase` (shared) + `withConfig` (per-target)

Two layers, both plain data you record-update from an exposed default:

- **`App.withBase (BaseConfig)`** — cross-target settings applied at boot: the
  structured log (`logFormat`/`logLevel`), an optional `database`, and optional
  `telemetry`. The fields are the typed `Sky.Config` values, so `import Sky.Config`
  for the constructors:

  ```elm
  import Sky.Config as Config

  App.app { init = init, update = update, view = view, subscriptions = subscriptions }
      |> App.withBase
          { App.baseDefaults
              | database = Just (Config.Sqlite "app.db")
              , logLevel = Config.Debug
          }
      |> App.withNotFound NotFound
  ```

  It applies through the same `Sky.Config`/`ApplyConfig` path a top-level
  `config` binding uses, so an operator's `SKY_*` env var still wins. One caveat:
  a `database` set here is read lazily on first connect, so it works for a normal
  DSN but NOT for `--embed` (the embedded cluster is decided at process start,
  before the app runs) — with `--embed` the cluster provides the database, so
  leave `database` unset.

- **`App.withConfig (Config)`** — per-target settings whose variant name matches
  the `--target` family (`WebConfig`, `DesktopConfig`, `TerminalConfig`, …), each
  wrapping an `*Opts` record you record-update from `webDefaults` / `desktopDefaults`
  / … (port, window size, canvas, static dir, …). Targets ignore a config that
  isn't theirs.

## Reading the request — `App.withRequest`

A web app often needs the incoming HTTP request at session start: an auth cookie
to render the logged-in view on **first paint**, the path/query to seed initial
state, a header to pick a locale. `App.withRequest` delivers it **portably**:

```elm
App.app { init = init, update = update, view = view, subscriptions = subs }
    |> App.withRequest
        (\req model ->
            case Dict.get "sky_sid" req.cookies of
                Just sid -> ( { model | session = Just sid }, Cmd.none )
                Nothing  -> ( model, Cmd.none )
        )
    |> App.withNotFound NotFound
```

`withRequest : (Request -> model -> ( model, Cmd msg )) -> App … -> App …` runs
**after `init` but before the first render**, so an auth-dependent view is
correct on first paint — no logged-out flash, no `Cmd.perform` round-trip. It
returns the same `( model, Cmd msg )` shape, so it can also fire a startup
command.

The point is portability. `init` stays `seed -> …` and **ignores its seed**, so
the same source still builds for Tui / Cli / Webview (which have no HTTP request
and skip this hook). The request arrives *only* through this web-only channel —
which is why using `withRequest` fixes the app's `seed` to `()` (write
`init : a -> …` or `init : () -> …`; a concrete non-unit seed is rejected). Only
the Live/web runner consumes it.

At session init the `Sky.Http.Server.Request` carries `method` / `path` /
`headers` / `params` / `query` / `cookies`. `body` and `remoteAddr` are empty —
init is a GET-time hook; read a POST body in a route handler or an `update`
command instead.

## View adapter

You write one `view : model -> Element msg`. `Std.App` adapts it per backend:
`Ui.layout []` for the HTML family (Live/Spa/Webview), the `Element` directly for
Tui, and a best-effort `Element`→text flatten for `terminal:cli` (2-D layout has
no lossless text form).

## String views — `App.cli` / `App.tui`

When you want to hand-author the terminal output yourself (`view : model ->
String`), reach for `App.cli` (line-oriented, printed verbatim) or `App.tui`
(drawn full-screen) instead of `App.app`. They are siblings of `App.app` /
`App.web`, refined by the same `with…` builders and run by the same `App.run`:

```elm
main : Task Error ()
main =
    App.run
        (App.cli { init = init, update = update, view = view, subscriptions = subscriptions }
            |> App.withInput onLine)   -- stdin lines → Msg


view : Model -> String
view model =
    "count=" ++ String.fromInt model.count ++ " > "
```

A `String` view is **terminal-only** — it cannot render on the web, so the
`web` / `desktop` / `mobile` runners refuse it at boot (use `App.app` for a
`Std.Ui` view that renders full-screen ANSI *and* the web). These builders are
the first-class successors to the old `Std.Cli.program` / `Std.Tui.program`;
nothing in user code imports `Std.Cli` / `Std.Tui` directly any more.

Because a String-view app can't fall back to the `web` default, pin its backend
in `sky.toml` so a bare `sky build` / `sky run` picks the terminal:

```toml
[app]
target = "terminal:cli"   # or "terminal:tui"
```

An explicit `--target` on the command line always overrides the persisted one.

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
