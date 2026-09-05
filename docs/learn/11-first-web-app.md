# Your first web app

Sky's sweet spot is full-stack web apps. You write one program with **`Std.App`**;
the server holds the state, renders the view, and streams minimal patches to the
browser. No separate front-end language, no API to hand-roll, no client state to
sync. `Std.App` is the single front door — a build-time `--target` (default
`web`) picks the backend, and the `web` target is **Sky.Live**, a server-driven
UI in the Elm architecture.

## The Elm architecture

A `Std.App` web app is four things:

- a **Model** — all your state, in one value;
- a **Msg** — the things that can happen;
- an **update** — `Msg -> Model -> (Model, Cmd Msg)`, how state changes;
- a **view** — `Model -> Element Msg`, what to draw.

The runtime loops: render the view, a user event produces a `Msg`, `update`
returns a new `Model`, the view is re-rendered, and only the difference is sent to
the browser.

## A complete counter

Here's the whole app — save it as `src/Main.sky` in a project and `sky run` it:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Error exposing (Error)
import Sky.Core.Task exposing (Task)
import Std.App as App
import Std.Ui as Ui exposing (Element)
import Std.Ui.Font as Font
import Std.Cmd as Cmd
import Std.Sub as Sub


type alias Model =
    { count : Int }


type Msg
    = Increment
    | Decrement


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Decrement ->
            ( { model | count = model.count - 1 }, Cmd.none )


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


view : Model -> Element Msg
view model =
    Ui.row [ Ui.spacing 16, Ui.padding 24 ]
        [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
        , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
        , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
        ]


appDef =
    App.app
        { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound ()


main : Task Error ()
main =
    App.run appDef
```

`sky run` (with no `--target`) defaults to `web`. Open `http://localhost:8000`
and the buttons work — clicks go to the server, `update` runs, and the changed
number is patched into the page.

## What to notice

- **`App.run appDef` is the only entry point.** The `appDef` value composes your
  `init`/`update`/`view`/`subscriptions` with `App.app`, then `App.withNotFound`
  supplies the fallback page (mandatory for the `web` target — the compiler
  enforces it). A build-time `--target family[:variant]` chooses the backend;
  bare `sky run`/`sky build` means `web`.
- **`init` runs once per session**, not per page reload. It takes `()`; to read
  the incoming request (path, cookies, headers) add `App.withRequest`, which runs
  after `init` and before the first paint.
- **A button's `onPress` is a `Msg`**, not a callback. All logic lives in
  `update`, which is pure and easy to test.
- **The view is `Std.Ui`, not HTML.** We'll dig into `Ui` next — because `view`
  returns an `Element`, the same view code also renders in a terminal
  (`--target terminal:tui`) and a desktop window (`--target desktop`).
- **One persistent connection.** On the `web` target the browser keeps a single
  SSE connection for the whole session; you don't manage it. When you add links
  between pages, make them `sky-nav` links so they reuse that one connection (the
  [routing](14-routing.md) lesson shows how).

More depth — sessions, the request object, connection handling, and the other
`--target` backends — is in the [Sky.Live guide](../skylive/overview.md) and the
[Std.App overview](../skyapp/overview.md).

**[Next → UI with Std.Ui](12-ui.md)**
