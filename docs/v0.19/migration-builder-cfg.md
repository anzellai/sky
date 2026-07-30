# v0.19 migration — TEA app config is now a typed builder (BREAKING)

v0.19 replaces the row-open **record literal** passed to `Live.app` / `Tui.app` /
`Tui.program` / `Cli.program` with a typed **builder**: a `config { …required… }`
call produces an opaque `AppConfig`, and optional fields are attached with
`withX` builders. This makes the app config a real, checkable, hover-able type
(the row-open record was untyped — it hovered as `?`), and it is what unifies the
kernel-module docs onto a single source (`sky doc`, LSP hover, and the
type-checker now all read the module's `.sky` file).

**This is a breaking change.** Every `*.app { … }` / `*.program { … }` call must
move to the builder form. The transformation is mechanical.

## Sky.Live

**Before:**

```elm
import Std.Live exposing (app, route)

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ route "/" Home ]
        , notFound = Home
        , head = headFor              -- optional fields, row-open
        , analytics = { pageViews = True }
        }
```

**After:**

```elm
import Std.Live exposing (app, config, route, withHead, withAnalytics)

main =
    app
        (config
            { init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes = [ route "/" Home ]
            , notFound = Home
            }
            |> withHead headFor
            |> withAnalytics { pageViews = True }
        )
```

The **six required fields** stay in `config { … }`; every **optional field**
becomes a `withX` in the pipe chain, and you add the `config`/`withX` names you
use to the `exposing (…)` list. Optional-field → builder map:

| Old record field | Builder |
|---|---|
| `head` | `withHead` |
| `consoleAuth` | `withConsoleAuth` |
| `onNavigate` | `withOnNavigate` |
| `guard` | `withGuard` |
| `static` | `withStatic` |
| `staticUrl` | `withStaticUrl` |
| `port` | `withPort` |
| `store` / `storePath` / `ttl` | `withStore` / `withStorePath` / `withTtl` |
| `analytics` | `withAnalytics` |
| `status` | `withStatus` |

## Sky.Tui

Required fields: `init` / `update` / `view` / `subscriptions`. (The old
`routes` / `notFound` fields were ignored by the Tui runtime — drop them.)
`onKey` / `guard` / `canvasWidth` / `canvasHeight` become builders.

```elm
-- Before:  Tui.app { init = …, update = …, view = …, subscriptions = …, onKey = onKey }
-- After:
Tui.app
    (Tui.config { init = init, update = update, view = view, subscriptions = subscriptions }
        |> Tui.withOnKey onKey
    )
```

`Tui.program` uses the same `Tui.config`; it requires `withOnKey`.

## Sky.Cli

```elm
-- Before:  Cli.program { init = …, update = …, view = …, subscriptions = …, onLine = onLine }
-- After:
Cli.program
    (Cli.config { init = init, update = update, view = view, subscriptions = subscriptions }
        |> Cli.withOnLine onLine
    )
```

## Sky.Webview — NOT affected

`Webview.app` already took a closed record (`{ init, update, view,
subscriptions, window }` — no optional fields), so it keeps the record form and
its precise signature. No change needed.

## Why

The row-open record could not be given a precise type, so `Live.app` hovered as
`?` and its docs lived in a separate hand-maintained registry that drifted from
the type-checker. As a typed builder, `AppConfig` is a real opaque type: the
entry points get precise `AppConfig model msg -> Task Error ()` signatures, and
every binding is an `Ffi.kernel` alias declared in the module's `.sky` file — so
`sky doc`, LSP hover, and the type-checker all read that one source. See
`docs/v0.19/kernel-metadata-unification.md`.

## Raw `api` endpoints (also breaking)

The old cfg had a separate `api` field. In v0.19 there is no `api` field — an
`api "METHOD /path" handler` is a `Route`, so it goes in the **`routes`** list
next to `route`. The handler signature also changed:

```elm
-- v0.18 (old)
handleWebhook : Dict String any -> Response
handleWebhook req = ...

-- v0.19 (new): a typed record Request + a Task return
handleWebhook : Server.Request -> Task Error Server.Response
handleWebhook req =
    -- req.method / req.path / req.headers / req.params / req.query / req.cookies / req.body
    Task.succeed (Server.text "ok")     -- wrap a plain Response in Task.succeed
```

- **Request** is now a record: `{ method, path, body, headers, params, query,
  cookies, remoteAddr }` — read `req.path` (not `Dict.get "path" req`).
- **Return** is `Task Error Response` — a handler that previously returned a
  plain `Response` wraps it in `Task.succeed`; a fallible handler can
  `Task.fail (Error.…)` and let the framework map it to a 4xx/5xx.
- Register in `routes`: `Live.config { …, routes = [ route "/" Home, api "POST /webhooks/x" handleWebhook ], notFound = Home }`.
