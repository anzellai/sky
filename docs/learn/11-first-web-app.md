# Your first web app

Sky's sweet spot is full-stack web apps with **Sky.Live** — a server-driven UI in
the Elm architecture. You write one program; the server holds the state, renders
the view, and streams minimal patches to the browser. No separate front-end
language, no API to hand-roll, no client state to sync.

## The Elm architecture

A Sky.Live app is four things:

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
import Std.Live exposing (app, config, route)
import Std.Ui as Ui
import Std.Ui.Font as Font
import Std.Cmd as Cmd
import Std.Sub as Sub


type alias Model =
    { count : Int }


type Msg
    = Increment
    | Decrement


init : a -> ( Model, Cmd Msg )
init _req =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Decrement ->
            ( { model | count = model.count - 1 }, Cmd.none )


subscriptions : Model -> Sub.Sub Msg
subscriptions _ =
    Sub.none


view model =
    Ui.layout []
        (Ui.row [ Ui.spacing 16, Ui.padding 24 ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ]
        )


main =
    app
        (config
            { init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes = [ route "/" () ]
            , notFound = ()
            }
        )
```

Run it, open `http://localhost:8000`, and the buttons work — clicks go to the
server, `update` runs, and the changed number is patched into the page.

## What to notice

- **`init` runs once per session**, not per page reload. The `_req` argument
  carries the incoming request (path, cookies, headers) if you need it.
- **A button's `onPress` is a `Msg`**, not a callback. All logic lives in
  `update`, which is pure and easy to test.
- **The view is `Std.Ui`, not HTML.** We'll dig into `Ui` next — the same view
  code also runs in a terminal (Sky.Tui) and a desktop window (Sky.Webview).
- **One persistent connection.** The browser keeps a single SSE connection for the
  whole session; you don't manage it. When you add links between pages, make them
  `sky-nav` links so they reuse that one connection (the [routing](14-routing.md)
  lesson shows how).

More depth — sessions, the request object, connection handling — is in the
[Sky.Live guide](../skylive/overview.md).

**[Next → UI with Std.Ui](12-ui.md)**
