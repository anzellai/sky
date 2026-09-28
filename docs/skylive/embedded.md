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
  settings. Two Live apps in one process need two ports and would share those
  settings.
- The start-up report (`Sky.Live listening on :PORT`) is still printed.
