# Embedded Sky.Live — a Live app inside a Task program

A Sky.Live app normally **owns its process**. When it starts it:

- installs a SIGINT / SIGTERM / SIGHUP handler whose shutdown tears down
  process-wide state (readiness goes to 503, tracing is flushed, the
  `Std.Jobs` worker stops, every shutdown hook runs, every registered resource
  is released);
- exits the process on a second signal (`ExitProcess(130)`);
- exits the process when its port is already in use (`ExitProcess(1)`);
- exits the process when a boot check fails: the console invariant
  (`SKY_CONSOLE_AUTH=token|app` with no console mounted) or a session store
  that is configured but unreachable in production.

That is correct for `main = App.run app`. It is wrong when the Live app is one
part of a larger program, for example a worker loop that also serves a small
status UI, or a program that runs a Live app next to a `Sky.Http.Server`.

**Embedded mode** (v0.27) runs the app as a guest. Turn it on with
`App.withEmbedded` (`Std.App`) or `Live.withEmbedded` (`Std.Live`). In
embedded mode the app:

- installs **no** signal handler, and tears down **no** process-wide state;
- **never** exits the process. A port already in use, the console invariant
  and a production session store that is unreachable all become the `Err` of
  the app's Task, for `main` to handle.

The host program owns shutdown.

## The pattern

Spawn the app, then do the other work in the same `main`:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Http as Http
import Sky.Core.String as String
import Sky.Core.Task as Task exposing (Step(..))
import Sky.Core.Time as Time
import Std.App as App exposing (Config(..), webDefaults)
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


type alias Model =
    { ticks : Int }


type Msg
    = Tick


init : a -> ( Model, Cmd Msg )
init _ =
    ( { ticks = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update _ model =
    ( { model | ticks = model.ticks + 1 }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.text ("ticks: " ++ String.fromInt model.ticks)


statusApp =
    App.app { init = init, update = update, view = view, subscriptions = \_ -> Sub.none }
        |> App.withNotFound ()
        |> App.withConfig (WebConfig { webDefaults | port = 8090 })
        |> App.withEmbedded


-- The host's own work: here, a loop that runs forever.
work : Int -> Task Error (Step Int ())
work n =
    Time.sleep 1000
        |> Task.andThen (\_ -> Log.println ("worker tick " ++ String.fromInt n))
        |> Task.map (\_ -> Loop (n + 1))


main : Task Error ()
main =
    Task.spawn
        (App.run statusApp
            |> Task.onError (\e -> Log.println ("status UI did not start: " ++ Error.toString e))
        )
        |> Task.andThen (\_ -> Task.loop work 0)
```

`App.run` is rewritten to the target's runner wherever it appears, so it works
inside `Task.spawn`. `Task.spawn` returns at once; the app serves on its own
goroutine. Handle the app's `Err` inside the spawned Task (`Task.onError`), as
above, or run it without `Task.spawn` when the rest of `main` should wait for
it.

## Starting and stopping an app: `App.serve`

`App.run` with `withEmbedded` runs the app until the process ends; nothing
can stop it. `App.serve` starts the same app and hands back a handle:

```elm
App.serve : App HasFallback () page model msg key -> Task Error App.Running
App.address : App.Running -> String
App.stop : App.Running -> Task Error ()
```

- `App.serve` implies `withEmbedded` (no signal handler, no process exit). Its
  Task succeeds once the listener is **bound**, so a request sent right after
  it is answered. A port already in use, a session store that refuses to
  start and the console boot check are the Task's `Err`.
- Port `0` (`WebConfig { webDefaults | port = 0 }`) asks the kernel for a free
  port. `App.address` names the bound address as `host:port`
  (`127.0.0.1:54321` in development, `[::]:8080` when bound to all
  interfaces). An operator's `SKY_LIVE_PORT` still wins over the builder.
- `App.stop` is graceful and bounded: the listener stops accepting, every open
  live stream of the app closes, in-flight requests get up to **5 seconds** to
  finish (then their connections close), every session ends (its `Sub.every`
  timers, relay and topic subscriptions stop), the session store closes, and
  every process-wide registration the app made is removed (its `/_sky/readyz`
  store probe, its shutdown hook, its `Std.PubSub.publish` target, the console
  if it owned it). The port is free when the Task succeeds, so the same port
  binds again at once. A second `App.stop` is harmless.
- `Std.Live` has the same three: `Live.serve`, `Live.address`, `Live.stop`.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Http as Http
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.App as App exposing (Config(..), webDefaults)
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


type alias Model =
    { count : Int }


type Msg
    = Increment


init : a -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update _ model =
    ( { model | count = model.count + 1 }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.text ("count: " ++ String.fromInt model.count)


statusApp =
    App.app { init = init, update = update, view = view, subscriptions = \_ -> Sub.none }
        |> App.withNotFound ()
        |> App.withConfig (WebConfig { webDefaults | port = 0 })


main : Task Error ()
main =
    App.serve statusApp
        |> Task.andThen
            (\running ->
                Http.get ("http://" ++ App.address running ++ "/")
                    |> Task.andThen (\resp -> Log.println ("status UI answered " ++ String.fromInt resp.status))
                    |> Task.andThen (\_ -> App.stop running)
            )
```

`App.serve` always runs Sky.Live, whatever `--target` a build selects. A
dispatched `Std.App` build for a target that does not run Sky.Live
(`terminal:*`, `web:app`, `mobile:*`, `tablet:<os>`, `desktop:<os>`) refuses
an entry whose sources call `App.serve`, and names the file.

## Several apps in one process

Two apps served in one process on different ports keep their own state, and
stopping one does not touch the other (v0.27, `runtime-go/rt/live_serve.go`).
**Per app:**

- the listener, HTTP server, routes, `api` handlers and handler chain;
- the session store and every session in it (a session cookie of one app is
  an unknown session to the other), with their timers and subscriptions;
- the pub/sub broker (`Cmd.publish` and `Sub.subscribeTopic` stay inside the
  app);
- the revocation gate (`Live.withRevocation`) and its per-user verdict cache;
- the sliding-auth middleware (`Live.withAuthSliding`);
- the session transport (`withSessionTransport`).

**One per process, by design:**

- **The Sky Console.** The first listener that mounts `/_sky/console` owns it
  (it renders the process-wide telemetry, and its auth check is that app's).
  A second app serves no console of its own and prints no console line. When
  the owning app stops, the console is released, and the next app to start
  mounts it. (Before v0.27 the second mount panicked.)
- **`Std.PubSub.publish`** (the Task form, called outside `update`) has no app
  in scope, so it publishes to **every** running app: a topic is a
  process-wide name. A stopped app receives nothing more.
- **`Auth.setSlidingCookie`** has no app in scope either. Two apps that set
  `withAuthSliding` must use the same cookie, secret variable and SameSite, or
  the second one refuses to start.
- **Process settings.** `ENV`, `SKY_CSRF`, the bind host (`SKY_HOST`),
  `SKY_LIVE_*` values not set by a builder, `/_sky/readyz` (it checks every
  running app's store), telemetry, tracing, the `Std.Jobs` worker, and the
  shutdown sequence.
- **CSRF exemptions** (`Live.api`, `Server.api`, `Server.rpc`,
  `WithoutCsrf`) are keyed by method and path, for the whole process.

A browser keeps cookies per host, not per port, so two apps on
`localhost:8001` and `localhost:8002` receive the same `sky_sid` cookie. Each
app keeps its own session under that id (the other app's state is never
visible), but when one app rotates the id at a sign-in, the other app sees a
new id and starts a fresh session. Serve the apps on different host names, or
use the header session transport
([architecture](architecture.md#sessions-without-cookies-the-header-transport)).

## Shutdown

Because the embedded app installs no signal handler, a signal does what the
host's own setup says:

- **A host with its own termination sequence** (a `Server.listen` in the same
  program, or the `--embed` PostgreSQL supervisor) runs it as usual. The
  embedded app's listener is closed in the drain phase of that sequence, and
  its session store is released after the drain, so the app stops in the right
  order.
- **A plain Task host** has no handler. SIGINT / SIGTERM take Go's default
  action and end the process at once. The memory session store loses nothing
  it would not lose anyway; a SQLite store is already durable per write (its
  WAL is checkpointed on the next open).
- **When `main` returns**, the process ends, and the embedded app with it.
  Keep `main` running (for example with `Task.loop` or `Task.forever`) for as
  long as the app should serve.

## Caveats

- Embedded mode applies to the `web` and `tablet` targets (Sky.Live). A
  `web:app` build's backend is a generated server program that owns its own
  process, and the terminal and desktop runners ignore the flag.
- The app still reads the same process-wide settings as a normal run: the
  port (`withConfig` / `SKY_LIVE_PORT`), the session store, `ENV`, the console
  settings. Two Live apps in one process need two ports; see
  [Several apps in one process](#several-apps-in-one-process) for what they
  share.
- The start-up report (`Sky.Live listening on :PORT`) is still printed.
