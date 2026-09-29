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
sky check                        # checks the target a bare build builds (check ≡ build)
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
- `App.withWindow title width height` — desktop window. The `desktop` target
  opens the window on the port Sky.Live actually binds (a `SKY_LIVE_PORT`
  override included), once the server answers; when the server never answers,
  the run fails with an `Unavailable` error and no blank window opens. When the
  server fails to start (a listen error, a bad config), the process logs the
  cause and exits 1 at once, before any window opens.
- `App.withInput onLine` — a terminal line/text input handler: stdin lines on
  `terminal:cli`, a one-line prompt under the view on `terminal:tui`. Without it
  a `terminal:cli` app reads no input and exits 0 once its Cmds and timers are
  done. See the terminal loop contract in `docs/skytui/overview.md`.

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
            case Dict.get "auth" req.cookies of
                Just token -> ( { model | authToken = Just token }, Cmd.none )
                Nothing    -> ( model, Cmd.none )
        )
    |> App.withNotFound NotFound
```

`withRequest : (Request -> model -> ( model, Cmd msg )) -> App … -> App …` runs
**after `init` but before the first render**, so an auth-dependent view is
correct on first paint — no logged-out flash, no `Cmd.perform` round-trip. It
returns the same `( model, Cmd msg )` shape, so it can also fire a startup
command.

Do not key data by the Sky.Live session cookie here. Its value changes every
time the signed-in user changes (session-id rotation, see
[Sky.Live sessions](../skylive/overview.md#session-ids-change-at-sign-in)), and
over HTTPS its name is `__Host-sky_sid`. For a per-session key that stays the
same for the session's whole life, use `Cmd.perform (Live.sessionKey ()) GotKey`.

The point is portability. `init` takes `()` on every target (`App.app`, `web`,
`cli` and `tui` all type it `init : () -> ( model, Cmd msg )`), so the same
source builds for Tui / Cli / Webview (which have no HTTP request and skip this
hook). The request arrives *only* through this web-only channel. An `init` that
takes anything else (`init : Page -> …`) is one type error at the `init` field
of `App.app`, on every target. Only the Live/web runner consumes the request.

The URL fragment (the text after `#`) never reaches the server, so no request
hook can read it. Subscribe to it instead: `subscriptions = \_ -> Sub.onFragment
FragmentChanged` delivers it when the page loads with one and on every change,
on `web`, `web:app`, desktop and mobile. To move the address bar from `update`
(go to a page after a save, drop a one-time fragment), return a `Std.Nav`
command: `Nav.pushUrl "/orders/7"`, `Nav.replaceUrl "/"` or
`Nav.clearFragment`. It routes like an in-app link on every web target, and a
terminal target ignores it.

At session init the `Sky.Http.Server.Request` carries `method` / `path` /
`headers` / `params` / `query` / `cookies`. `body` and `remoteAddr` are empty —
init is a GET-time hook; read a POST body in a route handler or an `update`
command instead.

## Handling a failed RPC — `App.withRpcError`

On the `web:app` (Sky.Spa) target each server branch runs as a `POST
/_rpc/<Msg>` round-trip. When one fails — a 5xx the backend answered, a response
the shared codec cannot decode, or a network drop — the client keeps the model
(the write-set never applied) and reports the failure loudly to the console, and
a network drop arms a retry overlay. That is safe, but silent to your UI.

`App.withRpcError (\err -> RpcFailed err)` routes the error **into your own
`update`** instead, so your view can show it — parity with Sky.Live's
`Cmd.perform task ToMsg` error arm:

```elm
type Msg
    = LoadPosts
    | RpcFailed Error
    | ...

App.app { init = init, update = update, view = view, subscriptions = subscriptions }
    |> App.withNotFound NotFound
    |> App.withRpcError (\err -> RpcFailed err)
```

`withRpcError : (Error -> msg) -> App … -> App …`. It is opt-in: without it the
loud-log floor stands. Web (Sky.Live) and terminal targets have no RPC boundary
and ignore the hook — there a failed task surfaces through its own `ToMsg`.

## The Sky Console for your own admins — `App.withConsoleAuth`

In production the Sky Console (`/_sky/console`) needs a login. With
`SKY_CONSOLE_AUTH=token` that login is a shared console token. With
`SKY_CONSOLE_AUTH=app`, your app decides instead: a user already signed in to
the app, whom your code recognises as an admin, opens the console with no
second password.

```elm
import Std.Live.Console as Console

-- The sign-in lives in the model (the usual TEA app):
adminsOnly : Request -> Model -> Task Error (Maybe Console.Identity)
adminsOnly _ model =
    case model.session of
        Just s ->
            if s.role == "admin" then
                Task.succeed (Just (Console.defaultIdentity s.userId |> Console.withEmail s.email))

            else
                Task.succeed Nothing

        Nothing ->
            Task.succeed Nothing

App.app { init = init, update = update, view = view, subscriptions = subscriptions }
    |> App.withNotFound NotFound
    |> App.withConsoleAuth adminsOnly
```

`withConsoleAuth : (Request -> model -> Task Error (Maybe Console.Identity)) ->
App … -> App …`. The check gets two things:

- `model`, the visitor's signed-in model. On Sky.Live it is the model of the
  live session their session cookie names. On a `web:app` backend it is
  `init`'s model (seeded by `withRequest`, when present) with each session
  field taken from the verified `sky_sid` cookie, exactly as an RPC handler
  sees it. A visitor with no session gets `init`'s model: signed out.
- `req`, the console request as a `Sky.Http.Server` handler receives it. An
  app with its own session cookie reads it there and ignores `model`:
  `adminsOnly req _ = case Dict.get "session" req.cookies of …`.

The gate fails closed:

- `Just identity` with a non-empty `subject` lets the request in and sets a
  signed console session cookie (`__Host-sky_console`, `Secure`, `HttpOnly`,
  `SameSite=Strict`), so later console requests do not call the check again.
  The login is logged as `console.auth.allowed` with the subject.
- `Nothing`, `Err`, a panic, or an empty `subject` refuses with 403 and logs
  `console.auth.denied`. No console session is set.

It runs on the server on every web target: Sky.Live (`web`, `desktop`) and the
backend of a `web:app` split, which registers it with `Server.setConsoleAuth`
before it listens. The wasm client never sees it. A hand-written
`Sky.Http.Server` app calls `Server.setConsoleAuth check` itself, before
`Server.listen`; there the check takes the request only (`Request -> Task Error
(Maybe identity)`), as such an app has no model.

`SKY_CONSOLE_TOKEN` is optional in this mode. Without it the console session
cookie is signed with a per-host key, so after a restart on a new host, or on
another replica, the check simply runs again for the next console request.
Set it to keep console sessions valid across replicas.

With `SKY_CONSOLE_AUTH=app` and no `withConsoleAuth`, every console request
gets 403. Use `SKY_CONSOLE_AUTH=token` in that case.

## Surviving a restart — `App.withDurable`

Add durability to any `App.app` app with **one line** and NO change to `model` /
`msg` / `update`:

```elm
mkApp db =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound NotFound
        |> App.withDurable db modelCodec
```

`withDurable : Db -> Codec model -> App … -> App …`. The backend loop restores the
Model on start and snapshots it to the database after each update. So the state up
to the last completed `update` survives a process restart:

- **Web (Sky.Live):** the snapshot is keyed by the session id. With the default
  in-process memory session store, a restart normally loses the session; the
  durable snapshot restores it on the next mount as long as the session cookie
  survives. When the session id changes at sign-in the snapshot moves with it,
  and a revoked or ended session (`Live.endSession`) deletes its snapshot, so a
  retired id restores nothing. (A shared session store — postgres / redis — already carries the Model
  across replicas; `withDurable` is the layer that also covers a memory store.)
- **Terminal (`App.cli` / `App.tui`):** the snapshot is keyed by a fixed run id
  (`"default"`), so a re-launched program picks up where it stopped. Use
  `App.withDurableId "<id>" db modelCodec` to key several runs separately, or to
  share one durable state between two runs that use the same id + database.

The Model must be `Codec`-serialisable data (no function fields — the same rule as
an Elm port). Build the codec with `Codec.auto <blank model>`.

**A snapshot that no longer decodes is never overwritten.** When the Model shape
changes without a migration, the stored snapshot fails to decode
(`Durable.loadSnapshot` returns a `Decode` error, not `Nothing`). The backend logs
a classified `DurableRestoreFailed` error, boots that run (terminal) or session
(web) from `init`, and writes NO snapshot for it, so the old state stays in the
database until you migrate or remove it.

Durability of the Model is separate from exactly-once EFFECTS. The snapshot
guarantees the state up to the last completed `update`; an effect in flight at the
moment of a crash is at-most-once. For an effect that must run exactly once across
a resume (a payment, an outbound message), drive it through a `Std.Durable`
step-journalled workflow (`Durable.step`) rather than a bare `Cmd.perform`. See
`docs/design/durable-execution.md`. A runnable demonstration is
`examples/67-durable-counter` (a Cli counter that keeps its count across restarts
through the one `App.withDurable` line).

## Running an app inside a Task program — `App.withEmbedded`

`main = App.run app` gives the app the whole process: on the `web` target
Sky.Live installs its own signal handler, and a port already in use or a failed
boot check exits the process. To run the app as ONE part of a larger Task
program, mark it embedded and spawn it next to the other work:

```elm
statusApp =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound NotFound
        |> App.withEmbedded

main =
    Task.spawn
        (App.run statusApp
            |> Task.onError (\e -> Log.println ("status UI: " ++ Error.toString e))
        )
        |> Task.andThen (\_ -> Task.loop workerStep 0)
```

`withEmbedded : App … -> App …`. In embedded mode the Live app installs no
signal handler and never exits the process: a taken port, the console boot check
and a production session store that is unreachable become the `Err` of its Task.
The host program owns shutdown, and the process ends when `main` returns, so keep
`main` running for as long as the app should serve.

Caveats: it applies to the `web` / `tablet` targets (Sky.Live); a `web:app`
backend is a generated server program that owns its process, and the terminal
and desktop runners ignore it. A plain Task host has no signal handler, so
SIGINT / SIGTERM end the process at once; a host that runs `Server.listen` or
`--embed` keeps its own termination sequence, and the embedded app's listener is
closed in its drain phase, before the session store is released. The full
account is `docs/skylive/embedded.md`.

### Starting and stopping — `App.serve`

`App.serve app : Task Error App.Running` starts a `web` app embedded and
succeeds once it is listening; `App.address running` is its `host:port`
(port `0` picks a free one); `App.stop running` stops it gracefully (live
streams close, in-flight requests get 5 seconds, sessions end, the store
closes, the port is free again). Stopping twice is safe.

```elm
main =
    App.serve statusApp
        |> Task.andThen (\running -> Log.println ("status UI on " ++ App.address running))
        |> Task.andThen (\_ -> Task.loop workerStep 0)
```

Two apps served in one process keep their own sessions, routes, broker and
store; the Sky Console is one per process (the first app owns it). A build for
a target that does not run Sky.Live refuses an entry that calls `App.serve`.
See `docs/skylive/embedded.md`.

## Sessions without cookies — `App.withSessionTransport`

For a host that cannot keep cookies (a native shell whose custom-scheme handler
drops `Set-Cookie`, some embedded web views):

```elm
app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound NotFound
        |> App.withSessionTransport HeaderToken
```

`SessionTransport` is `CookieSession` (the default) or `HeaderToken`. With
`HeaderToken` the session id travels in the `X-Sky-Session` header: the page
carries a token, the client sends it on every request and reads the live stream
with `fetch`, no session cookie is set, and the server stores only a hash of the
token. The header is the CSRF defence (plus the Origin / `Sec-Fetch-Site`
check). A full page reload starts a new session. `SKY_LIVE_SESSION_TRANSPORT`
(`cookie` / `header`) overrides the builder. It is Sky.Live only: a
`web:app` build refuses it, because Sky.Spa authenticates its RPC and
subscription endpoints with the session cookie. The security model is in
`docs/skylive/architecture.md` ("Sessions without cookies").

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
`Std.Spa`.

The derivation reads the app value **structurally**: the value actually passed
to `App.run` (a second `App.app` elsewhere in the file is ignored), through local
bindings, local helper functions and lambdas (`|> secured` where `secured a = a
|> App.withGuard guard`), `|>`, `<|`, direct application (`App.withGuard g app`),
the inline form `main = App.run (App.app { … } |> …)`, and any import spelling
(`import Std.App as A` + `A.run`, or `exposing (run)` + bare `run`). Every
builder step is either carried into the client build (`withRoutes`,
`withNotFound`, `withHead`, `withOnNavigate`, `withRequest`, `withGuard`,
`withRpcError`, and `withClientCrypto`, which keeps end-to-end keys on the
device: `docs/skyspa/client-crypto.md`), read by the build for the native shell
(`withAppUrl`, see below), or listed in a build warning as not applying to a client
(`withConfig`, `withInput`, `withWindow`, `withOnKey`, `withBase`,
`withDurable`, `withDurableId`). Anything else fails the build with an error
that names it: an unknown builder, a function from another module applied to the
app, or a builder argument that uses a local of the code building the app (the
client build places that argument at top level). A guard is never dropped
silently.

A client build behaves like the `web` build for the same Msg sequence
(`docs/skyspa/auto-split.md` §20). The server branches of `update` run one at a
time, in the order the Msgs were dispatched, and each request is built from the
model current when it is sent; a field the user edits while a request is on the
wire keeps the edit. `App.withGuard` runs in the client for every Msg and again
on the server for every server branch. A server branch's returned `Cmd` runs:
its server tasks run on the server and their result Msgs come back to the
client, and a `Std.Native` effect runs in the client. Retry after a lost
connection never runs a server effect twice. On a reload the client restores its
own state, and a field that only server branches write comes from the server.

### The backend address a native shell loads — `App.withAppUrl`

The `mobile:ios`, `mobile:android` and `desktop:<os>` targets wrap the client
in a native web view that loads it from the backend. `App.withAppUrl` sets that
address:

```elm
-- doc-example: skip  (fragment — init/update/view/subscriptions elided)
app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound NotFound
        |> App.withAppUrl "https://app.example.test/"
```

`withAppUrl : String -> App … -> App …`. A phone cannot read the build
machine's environment, so the build reads the value **statically** from the
source and bakes it into the shell. The argument must be a string literal or a
top-level `String` constant in the entry module (followed through local
helpers, as `withGuard` is). Anything computed at run time is a build error
that names `App.withAppUrl` and says why. On the `web`, `tablet` and terminal
targets the builder does nothing.

`SKY_APP_URL` set at build time overrides the builder, and the desktop shell
also reads it at run time. With neither set, the shell loads the development
default: `http://localhost:<PORT>/` (iOS simulator), `http://10.0.2.2:<PORT>/`
(Android emulator), `http://127.0.0.1:<PORT>/` (desktop), with `PORT` read at
build time (default 8951). The value must be an absolute `http://` or
`https://` URL. The build summary prints the address and its source, for
example `loads https://app.example.test/ (App.withAppUrl)`. A plain `http://`
address to a host that is not local gets an iOS App Transport Security
exception and an Android cleartext permission for exactly that host, plus a
warning that a production device build should use `https://`. If the shell
cannot load the address, it shows a native message with the URL and the error
instead of a blank page. Full rules: `docs/sky-toml.md` (`SKY_APP_URL`).

### Permissions, entitlements, secure storage and release — `docs/skyapp/native.md`

A native shell needs more than the web build. [`native.md`](native.md) covers:

- `Bundle.withUsage Bundle.Camera "…"`: the purpose string each permission
  prompt shows, with the matching iOS / macOS `Info.plist` key and Android
  permission. A `Native.authenticate` / `capturePhoto` / `geolocation` /
  `scanCode` call without its permission fails the iOS and Android builds,
  naming the call, the fix and the `bundle` binding.
- `Bundle.withEntitlement`: typed Apple entitlements (keychain access groups,
  app groups, associated domains, push, iCloud), merged structurally with any
  `native/ios/app.entitlements` fragment.
- `Native.secureSet` / `secureGet` / `secureRemove` (Keychain, Android Keystore)
  and `Native.authenticate` (Face ID, Touch ID, BiometricPrompt).
- `Native.scanCode`: a QR code or barcode from the device camera (VisionKit on
  iOS and iPadOS, the camera and ZXing on Android).
- `sky package --release --target mobile:ios|mobile:android|desktop:mac`: the
  signed `.ipa`, the release `.apk` / `.aab`, the `.app` / `.dmg`. A release
  refuses the development backend address.
- A recipe that scans a QR code (`Native.scanCode` in the native shells, a
  widget island on the web) and draws one with `Std.Qr`.

See also: `sky doc Std.App`, `docs/skylive/overview.md`, `docs/skyspa/overview.md`,
and the design rationale in `docs/design/unified-app-builder.md`.
