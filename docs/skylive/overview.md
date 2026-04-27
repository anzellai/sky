# Sky.Live overview

**Server-driven UI with the TEA architecture** (`init` / `update` / `view` / `subscriptions`). Sky.Live lets you build interactive web apps where all state, logic, and rendering live on the server. The browser runs no client-side framework — just minimal JavaScript for DOM patching and SSE reconnection.

```elm
module Main exposing (main)

import Sky.Live as Live
import Html exposing (..)
import Html.Events exposing (onClick)


type Msg
    = Increment
    | Decrement


type alias Model =
    { count : Int }


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


view : Model -> Html Msg
view model =
    div []
        [ button [ onClick Increment ] [ text "+" ]
        , span [] [ text (String.fromInt model.count) ]
        , button [ onClick Decrement ] [ text "-" ]
        ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main =
    Live.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes =
            [ Live.route "/" HomePage
            ]
        , notFound = HomePage
        }
```

## How it works

1. **Initial page load:** Server renders `view model` as complete HTML. The browser receives a full static page, not a JS bundle.
2. **Event subscription:** Browser opens a Server-Sent Events (SSE) stream to receive updates.
3. **User interaction:** Click / input / submit triggers a minimal fetch to `/_sky/event` with a message payload.
4. **Server update:** `update msg model` runs on the server. The result is `(newModel, cmd)`.
5. **Diff:** Server diffs `view oldModel` against `view newModel` producing a VNode patch.
6. **Patch:** Patch is sent over SSE. Client-side Sky.js applies it to the DOM (< 2 KB gzipped).
7. **Command dispatch:** If `cmd` included `Cmd.perform task msgWrapper`, the task runs in a goroutine and its result is dispatched as a new `Msg` through the same loop.

See [architecture.md](architecture.md) for the detailed flow and session management.

## Advantages vs traditional SPAs

- **No client-side state.** No Redux, no React hooks, no "where does this state live" debate.
- **No JSON API layer.** You write Sky types once, not duplicated client + server contracts.
- **No bundler.** No Vite, no webpack, no npm audit alerts.
- **No fetch boilerplate.** Events are just messages.
- **Single binary deploy.** `sky build` produces one executable.

## When not to use Sky.Live

- **Offline-first apps.** Sky.Live requires a live server connection.
- **Heavy client-side computation.** The server is authoritative for all state; round-trips add latency for purely-local work (canvas animation, drag interactions).
- **Public-facing static content.** A plain `Sky.Http.Server` serving pre-rendered HTML is lighter if no interactivity is needed.

## Patterns

- Auth-gated pages: check `session` in `update` or in the route handler.
- Async work: `Cmd.perform (Http.get url) GotResponse` dispatches a task, the result comes back as `GotResponse (Result Error Response)`.
- Scheduled updates: `Sub.interval 1000 Tick` emits `Tick` every second.
- Multi-page: `routes` maps URL paths to route messages; `update` responds to navigation.

See [`examples/09-live-counter`](../../examples/09-live-counter/), [`examples/12-skyvote`](../../examples/12-skyvote/), [`examples/16-skychess`](../../examples/16-skychess/) for worked examples.

## Session stores

Sky.Live supports multiple backends for session state:

| Store | Configured via | Use case |
|-------|----------------|----------|
| `memory` | default | Single-instance dev / testing |
| `sqlite` | `[live] store = "sqlite", storePath = "./data.db"` | Single-instance prod |
| `redis` | `[live] store = "redis", storePath = "redis://..."` | Multi-instance deployments |
| `postgres` | `[live] store = "postgres", storePath = "postgres://..."` | Shared SQL backend |
| `firestore` | `[live] store = "firestore"` | Serverless GCP |

Configure in `sky.toml`:

```toml
[live]
port = 8000
store = "sqlite"
storePath = "./data.db"
ttl = 1800
```
