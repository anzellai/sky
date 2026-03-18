# Sky.Live — Unified Model/Msg Design

## The Core Idea

One `Model`. One `Msg`. One `update`. The whole app is a single TEA loop.

Navigation is just a `Msg` that changes `model.page`. No session swapping,
no state reconstruction on page change, no mental juggling between isolated
page states.

---

## 1. What It Looks Like

### A Complete Multi-Page App

```elm
module Main exposing (main)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route, scope)
import Std.Live.Events exposing (onClick, onSubmit, onInput)
import Std.Live.Navigation exposing (link)
import Std.Cmd as Cmd

-- ══════════════════════════════════════════════════════
-- MODEL — single source of truth
-- ══════════════════════════════════════════════════════

type Page
    = HomePage
    | TodosPage
    | UserDetailPage String
    | AboutPage
    | NotFoundPage

type alias Todo =
    { id : Int
    , text : String
    , done : Bool
    }

type alias Model =
    { page : Page

    -- Global state (persists across pages)
    , currentUser : Maybe User
    , notifications : List String

    -- Todos state
    , todos : List Todo
    , todoDraft : String
    , todoNextId : Int

    -- User detail state
    , viewedUser : Maybe User
    , userLoading : Bool
    }

-- ══════════════════════════════════════════════════════
-- MSG — one flat union
-- ══════════════════════════════════════════════════════

type Msg
    = Navigate Page
    -- Todos
    | UpdateTodoDraft String
    | AddTodo
    | ToggleTodo Int
    | DeleteTodo
    -- User detail
    | GotUser (Result String User)
    -- Global
    | DismissNotification Int
    | GotCurrentUser (Result String User)

-- ══════════════════════════════════════════════════════
-- INIT
-- ══════════════════════════════════════════════════════

init : Request -> ( Model, Cmd Msg )
init req =
    ( { page = HomePage
      , currentUser = Nothing
      , notifications = []
      , todos = []
      , todoDraft = ""
      , todoNextId = 1
      , viewedUser = Nothing
      , userLoading = False
      }
    , fetchCurrentUser GotCurrentUser
    )

-- ══════════════════════════════════════════════════════
-- UPDATE — one function, pattern match on Msg
-- ══════════════════════════════════════════════════════

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Navigate page ->
            navigateTo page model

        -- Todos
        UpdateTodoDraft text ->
            ( { model | todoDraft = text }, Cmd.none )

        AddTodo ->
            if model.todoDraft == "" then
                ( model, Cmd.none )
            else
                ( { model
                    | todos = model.todos ++ [ { id = model.todoNextId, text = model.todoDraft, done = False } ]
                    , todoDraft = ""
                    , todoNextId = model.todoNextId + 1
                  }
                , Cmd.none
                )

        ToggleTodo id ->
            ( { model
                | todos = List.map (\t -> if t.id == id then { t | done = not t.done } else t) model.todos
              }
            , Cmd.none
            )

        DeleteTodo id ->
            ( { model | todos = List.filter (\t -> t.id /= id) model.todos }
            , Cmd.none
            )

        -- User detail
        GotUser (Ok user) ->
            ( { model | viewedUser = Just user, userLoading = False }, Cmd.none )

        GotUser (Err _) ->
            ( { model | viewedUser = Nothing, userLoading = False }, Cmd.none )

        -- Global
        DismissNotification idx ->
            ( { model | notifications = List.removeAt idx model.notifications }, Cmd.none )

        GotCurrentUser (Ok user) ->
            ( { model | currentUser = Just user }, Cmd.none )

        GotCurrentUser (Err _) ->
            ( model, Cmd.none )


navigateTo : Page -> Model -> ( Model, Cmd Msg )
navigateTo page model =
    case page of
        UserDetailPage userId ->
            ( { model | page = page, viewedUser = Nothing, userLoading = True }
            , fetchUser userId GotUser
            )

        _ ->
            ( { model | page = page }, Cmd.none )

-- ══════════════════════════════════════════════════════
-- VIEW — one function, branches on model.page
-- ══════════════════════════════════════════════════════

view : Model -> Html Msg
view model =
    div [ class "app" ]
        [ viewNavbar model
        , viewNotifications model
        , mainNode [ class "content" ]
            [ viewPage model ]
        , viewFooter
        ]

viewPage : Model -> Html Msg
viewPage model =
    case model.page of
        HomePage ->
            viewHome model

        TodosPage ->
            viewTodos model

        UserDetailPage _ ->
            viewUserDetail model

        AboutPage ->
            viewAbout

        NotFoundPage ->
            viewNotFound

-- ── Layout (always visible) ──────────────────────────

viewNavbar : Model -> Html Msg
viewNavbar model =
    nav [ class "navbar" ]
        [ link "/" (Navigate HomePage) [ text "Home" ]
        , link "/todos" (Navigate TodosPage) [ text "Todos" ]
        , link "/about" (Navigate AboutPage) [ text "About" ]
        , case model.currentUser of
            Just user ->
                span [ class "user" ] [ text user.name ]
            Nothing ->
                text ""
        ]

viewNotifications : Model -> Html Msg
viewNotifications model =
    if List.isEmpty model.notifications then
        text ""
    else
        div [ class "notifications" ]
            (List.indexedMap viewNotification model.notifications)

viewNotification : Int -> String -> Html Msg
viewNotification idx message =
    div [ class "notification" ]
        [ text message
        , button [ onClick (DismissNotification idx) ] [ text "×" ]
        ]

viewFooter : Html Msg
viewFooter =
    footer [ class "footer" ]
        [ text "Built with Sky.Live" ]

-- ── Pages ────────────────────────────────────────────

viewHome : Model -> Html Msg
viewHome model =
    div []
        [ h1 [] [ text "Welcome" ]
        , case model.currentUser of
            Just user ->
                p [] [ text ("Hello, " ++ user.name) ]
            Nothing ->
                p [] [ text "Loading..." ]
        ]

viewTodos : Model -> Html Msg
viewTodos model =
    div [ class "todo-app" ]
        [ h1 [] [ text "Todos" ]
        , form [ onSubmit AddTodo ]
            [ input [ type_ "text", value model.todoDraft, onInput UpdateTodoDraft, placeholder "What needs to be done?" ]
            , button [ type_ "submit" ] [ text "Add" ]
            ]
        , ul [ class "todo-list" ]
            (List.map viewTodo model.todos)
        , p [] [ text (String.fromInt (List.length (List.filter (\t -> not t.done) model.todos)) ++ " items left") ]
        ]

viewTodo : Todo -> Html Msg
viewTodo todo =
    li [ class (if todo.done then "completed" else "") ]
        [ span [ onClick (ToggleTodo todo.id) ] [ text (if todo.done then "✓ " else "○ ") ]
        , span [] [ text todo.text ]
        , button [ onClick (DeleteTodo todo.id) ] [ text "×" ]
        ]

viewUserDetail : Model -> Html Msg
viewUserDetail model =
    if model.userLoading then
        div [] [ text "Loading..." ]
    else
        case model.viewedUser of
            Just user ->
                div [ class "user-detail" ]
                    [ h1 [] [ text user.name ]
                    , p [] [ text user.email ]
                    ]
            Nothing ->
                div [] [ text "User not found" ]

viewAbout : Html Msg
viewAbout =
    div [ class "about" ]
        [ h1 [] [ text "About" ]
        , p [] [ text "A Sky.Live app." ]
        ]

viewNotFound : Html Msg
viewNotFound =
    div [] [ h1 [] [ text "404 — Not Found" ] ]

-- ══════════════════════════════════════════════════════
-- APP — routes map URLs to Navigate msgs
-- ══════════════════════════════════════════════════════

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes =
            [ route "/" HomePage
            , route "/todos" TodosPage
            , route "/users/:id" (\params -> UserDetailPage params.id)
            , route "/about" AboutPage
            ]
        , notFound = NotFoundPage
        }
```

That's it. **The entire app is one file.** For larger apps, split the views
into separate modules — but Model, Msg, and update stay unified.

---

## 2. Why This Works Better Than Per-Page

### State sharing is free

With per-page models, sharing state is painful:

```elm
-- PER-PAGE: Need to thread shared state through every page transition
-- Who owns currentUser? How does TodosPage access it?
-- What happens to notifications when you navigate?
```

With unified model:

```elm
-- UNIFIED: Just read it. It's right there.
viewNavbar model =
    case model.currentUser of
        Just user -> span [] [ text user.name ]
        Nothing -> text ""
```

No prop drilling. No context providers. No shared state modules.
Every view function takes `Model` and reads what it needs.

### Navigation is just a Msg

Per-page approach:
```
Click "Todos" → POST /_sky/navigate → expire old session → init new page
→ create new session → return HTML → swap DOM → re-bind events
```

Unified approach:
```
Click "Todos" → POST /_sky/event { msg: "Navigate", args: ["TodosPage"] }
→ update model.page → diff view → return patches
```

Same as any other user interaction. No special navigation endpoint.
No session management complexity. The URL changes via pushState on the client.

### Todos state persists across navigation

Go to /todos, add some items, navigate to /about, navigate back to /todos.

Per-page: Todos state is gone (session expired on navigate away).
Unified: Todos are still there. Same model.

### The compiler still optimizes perfectly

```
Navigate TodosPage
  → changes: model.page
  → view branches on model.page
  → compiler knows: swap the entire content area
  → one patch: { id: "content", html: "<div class=\"todo-app\">..." }

AddTodo
  → changes: model.todos, model.todoDraft, model.todoNextId
  → only viewTodos reads these fields
  → but only when model.page == TodosPage
  → compiler generates: if page != TodosPage, no patches needed
```

The static analysis traces through the `case model.page of` branch.
It knows that `AddTodo` only affects the DOM when `model.page == TodosPage`.

---

## 3. Routing: URLs Map to Msg

Routes are just a mapping from URL patterns to `Page` values:

```elm
routes =
    [ route "/" HomePage
    , route "/todos" TodosPage
    , route "/users/:id" (\params -> UserDetailPage params.id)
    , route "/about" AboutPage
    ]
```

### What happens on initial page load (GET /todos)

1. Server receives `GET /todos`
2. Matches route → `TodosPage`
3. Runs `init req` → base model
4. Runs `update (Navigate TodosPage) model` → model with page set
5. Runs `view model` → full HTML
6. Creates session, stores model
7. Returns HTML with layout

### What happens on client-side navigation

1. User clicks `link "/todos" (Navigate TodosPage)`
2. JS client sends `POST /_sky/event { msg: "Navigate", args: ["TodosPage"] }`
3. Server runs `update (Navigate TodosPage) model`
4. Diffs view → patches
5. Client applies patches + `pushState("/todos")`

### URL sync

The `Navigate` msg is special — the compiler knows it changes `model.page`,
and generates URL-sync logic:

```go
// Generated: after processing Navigate msg, sync URL
func urlForPage(page Page) string {
    switch p := page.(type) {
    case PageHomePage:           return "/"
    case PageTodosPage:          return "/todos"
    case PageUserDetailPage:     return "/users/" + p.UserId
    case PageAboutPage:          return "/about"
    default:                     return "/"
    }
}
```

The response includes a `url` field when the page changes:

```json
{
  "patches": [{ "id": "content", "html": "..." }],
  "url": "/todos"
}
```

The JS client does `history.pushState({}, '', url)` when present.

### Browser back/forward

```javascript
window.addEventListener('popstate', () => {
  // POST current URL to server, server maps it back to Navigate msg
  send('Navigate', [pathToPage(location.pathname)]);
});
```

Or more simply — the server exposes a lookup:

```javascript
window.addEventListener('popstate', async () => {
  const res = await fetch('/_sky/resolve?path=' + location.pathname);
  const { msg, args } = await res.json();
  send(msg, args);
});
```

---

## 4. Scaling Up: Modules for Views, Not State

For a large app, you don't want 500 lines of view code in one file.
Split the **views** into modules. Keep Model/Msg/update unified.

```
src/
  Main.sky              -- Model, Msg, update, routes, app
  Views/
    Home.sky            -- viewHome : Model -> Html Msg
    Todos.sky           -- viewTodos : Model -> Html Msg
    UserDetail.sky      -- viewUserDetail : Model -> Html Msg
    Layout.sky          -- viewNavbar, viewFooter
```

### Main.sky (orchestrator)

```elm
module Main exposing (main)

import Views.Home as Home
import Views.Todos as Todos
import Views.UserDetail as UserDetail
import Views.Layout as Layout

-- ... Model, Msg, update same as before ...

viewPage : Model -> Html Msg
viewPage model =
    case model.page of
        HomePage -> Home.view model
        TodosPage -> Todos.view model
        UserDetailPage _ -> UserDetail.view model
        AboutPage -> aboutView
        NotFoundPage -> notFoundView
```

### Views/Todos.sky

```elm
module Views.Todos exposing (view)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live.Events exposing (onClick, onSubmit, onInput)

-- Takes the full Model, reads only what it needs.
-- Type system ensures it can't call update or produce wrong Msg.

view : Model -> Html Msg
view model =
    div [ class "todo-app" ]
        [ h1 [] [ text "Todos" ]
        , form [ onSubmit AddTodo ]
            [ input [ type_ "text", value model.todoDraft, onInput UpdateTodoDraft ]
            , button [ type_ "submit" ] [ text "Add" ]
            ]
        , ul [] (List.map viewTodo model.todos)
        ]

viewTodo : Todo -> Html Msg
viewTodo todo =
    li [ class (if todo.done then "completed" else "") ]
        [ span [ onClick (ToggleTodo todo.id) ] [ text todo.text ]
        , button [ onClick (DeleteTodo todo.id) ] [ text "×" ]
        ]
```

### Why not split update too?

You can, with helper functions:

```elm
update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Navigate page -> navigateTo page model

        -- Delegate to domain-specific helpers
        UpdateTodoDraft _ -> updateTodos msg model
        AddTodo -> updateTodos msg model
        ToggleTodo _ -> updateTodos msg model
        DeleteTodo _ -> updateTodos msg model

        GotUser _ -> updateUserDetail msg model

        _ -> ( model, Cmd.none )
```

```elm
-- In a helper module or same file
updateTodos : Msg -> Model -> ( Model, Cmd Msg )
updateTodos msg model =
    case msg of
        UpdateTodoDraft text ->
            ( { model | todoDraft = text }, Cmd.none )
        AddTodo ->
            ...
        _ ->
            ( model, Cmd.none )
```

But the Msg type and Model type stay in one place. One source of truth.

---

## 5. Compiler Analysis: Unified Model

### Does the big Model hurt diffing? No.

The compiler traces field-level dependencies regardless of Model size.

```
Msg: AddTodo
  update analysis:
    → writes: model.todos, model.todoDraft, model.todoNextId
    → does NOT write: model.page, model.currentUser, model.viewedUser, ...

  view analysis:
    → model.todos read in: viewTodos (inside case TodosPage branch)
    → model.todoDraft read in: viewTodos input[value]
    → model.todoNextId read in: nowhere in view (internal counter)

  page guard:
    → viewTodos only renders when model.page == TodosPage
    → if model.page != TodosPage, zero patches guaranteed

  generated patcher:
    func patchAddTodo(old, new Model) []Patch {
        if old.Page != PageTodosPage { return nil }
        patches := []Patch{}
        if old.TodoDraft != new.TodoDraft {
            patches = append(patches, Patch{ID: "input-0", Attrs: map[string]any{"value": new.TodoDraft}})
        }
        if !equalTodos(old.Todos, new.Todos) {
            patches = append(patches, Patch{ID: "todo-list", Html: renderTodoList(new.Todos)})
        }
        return patches
    }
```

The page guard is the key optimization. Msgs that affect page-specific state
produce **zero patches** when the user is on a different page.

### Navigate is the most expensive Msg

`Navigate` swaps the content area — a large HTML patch. But this is the same
cost as a per-page system doing a full page init. And it only happens on
navigation, not on every interaction.

The compiler can optimize this too:

```
Navigate from TodosPage to AboutPage:
  → AboutPage view is static (no model fields read)
  → compiler pre-renders About HTML at compile time
  → patch is a constant string, not computed at runtime
```

### Event sourcing: unified model is cheaper

Per-page: navigate away = expire session + replay new page's log.
Unified: navigate = one more Msg in the log. No session churn.

The replay optimizer handles Navigate efficiently:

```go
// Compiler knows: Navigate just sets model.Page (and maybe triggers a Cmd)
// Consecutive Navigates collapse to the last one
Navigate HomePage
Navigate TodosPage      // skipped
Navigate AboutPage      // skipped
Navigate TodosPage      // only this one replays
```

---

## 6. The `link` Helper

Navigation links produce `Navigate` msgs:

```elm
-- Std.Live.Navigation

link : String -> Page -> List Attribute -> List (Html Msg) -> Html Msg
link url page attrs children =
    a ([ href url, skyNav page ] ++ attrs) children
```

Usage:
```elm
link "/todos" (Navigate TodosPage) [] [ text "Todos" ]
```

Produces:
```html
<a href="/todos" sky-nav="Navigate" sky-args='["TodosPage"]'>Todos</a>
```

The `href` ensures:
- Right-click "Open in new tab" works (full page GET)
- SEO crawlers follow the link
- No-JS fallback works (full page reload)

The `sky-nav` attribute tells live.js to intercept and use client-side navigation.

---

## 7. API Routes (Outside TEA)

REST/JSON endpoints don't go through the TEA loop. They're separate:

```elm
main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes =
            [ route "/" HomePage
            , route "/todos" TodosPage
            , route "/about" AboutPage
            ]
        , api =
            [ get "/api/health" handleHealth
            , get "/api/todos" handleGetTodos
            , post "/api/todos" handleCreateTodo
            ]
        , notFound = NotFoundPage
        }

handleHealth : Request -> Response
handleHealth _ =
    json 200 (E.object [ ("status", E.string "ok") ])

handleGetTodos : Request -> Response
handleGetTodos _ =
    let
        todos = fetchTodosFromDb ()
    in
    json 200 (E.list encodeTodo todos)
```

API routes are stateless, no session, no layout, no live.js.
The compiler generates plain Go HTTP handlers for these.

---

## 8. Full sky.toml

```toml
[package]
name = "my-app"
version = "0.1.0"

[live]
port = 4000
ttl = "30m"

[live.session]
store = "memory"
# store = "sqlite"
# path = "./data/sessions.db"
# snapshot_interval = 50

[live.static]
dir = "static"           # served at /static/*
```

---

## 9. Generated Server (Simplified)

With unified model, the server is simpler — no per-page session management:

```go
package main

import (
    "encoding/json"
    "net/http"
    "sync"
)

type Session struct {
    Model    Model
    MsgLog   []MsgEnvelope
}

type Store struct {
    mu       sync.RWMutex
    sessions map[string]*Session
}

func main() {
    store := &Store{sessions: make(map[string]*Session)}
    mux := http.NewServeMux()

    // Static assets + live.js
    mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
    mux.HandleFunc("/_sky/live.js", serveLiveJS)

    // ── Single event endpoint ────────────────────────
    mux.HandleFunc("POST /_sky/event", func(w http.ResponseWriter, r *http.Request) {
        var env MsgEnvelope
        json.NewDecoder(r.Body).Decode(&env)

        store.mu.Lock()
        defer store.mu.Unlock()

        sess, ok := store.sessions[env.SID]
        if !ok {
            http.Error(w, "session expired", 410)
            return
        }

        oldView := view(sess.Model)

        msg := decodeMsg(env.Name, env.Args)
        newModel, cmds := update(msg, sess.Model)
        processCmds(cmds)

        newView := view(newModel)
        patches := diff(oldView, newView)

        sess.Model = newModel
        sess.MsgLog = append(sess.MsgLog, env)

        resp := map[string]interface{}{"patches": patches}

        // URL sync: if page changed, tell client to pushState
        if oldModel.Page != newModel.Page {
            resp["url"] = urlForPage(newModel.Page)
            resp["title"] = titleForPage(newModel.Page)
        }

        json.NewEncoder(w).Encode(resp)
    })

    // ── URL resolution (for browser back/forward) ────
    mux.HandleFunc("GET /_sky/resolve", func(w http.ResponseWriter, r *http.Request) {
        path := r.URL.Query().Get("path")
        page := matchRoute(path)
        json.NewEncoder(w).Encode(map[string]interface{}{
            "msg": "Navigate", "args": []interface{}{encodePage(page)},
        })
    })

    // ── Initial page load (any route) ────────────────
    pageHandler := func(w http.ResponseWriter, r *http.Request) {
        req := buildRequest(r)
        page := matchRoute(r.URL.Path)

        model, cmds := init(req)
        model, cmds2 := update(Navigate(page), model)
        processCmds(append(cmds, cmds2...))

        sid := generateSID()
        store.mu.Lock()
        store.sessions[sid] = &Session{Model: model}
        store.mu.Unlock()

        html := renderFull(model, sid)
        w.Header().Set("Content-Type", "text/html")
        w.Write([]byte(html))
    }

    // Register all page routes
    mux.HandleFunc("GET /", pageHandler)
    mux.HandleFunc("GET /todos", pageHandler)
    mux.HandleFunc("GET /users/{id}", pageHandler)
    mux.HandleFunc("GET /about", pageHandler)

    // ── API routes (outside TEA) ─────────────────────
    mux.HandleFunc("GET /api/health", handleHealth)
    mux.HandleFunc("GET /api/todos", handleGetTodos)

    http.ListenAndServe(":4000", mux)
}
```

Notice how much simpler this is vs the per-page version:
- No `PageDef` abstraction
- No `/_sky/navigate` endpoint
- No session swap on navigation
- One `view()` call, one `update()` call, one `diff()` call

---

## 10. Trade-offs: When Unified Gets Uncomfortable

### Large apps (50+ pages)

The Msg type could have 200+ variants. The update function could be 500+ lines.

**Mitigation:** Split update into helper functions by domain:

```elm
update msg model =
    case msg of
        Navigate _ -> handleNavigation msg model
        -- Todos domain
        UpdateTodoDraft _ -> Todos.update msg model
        AddTodo -> Todos.update msg model
        ToggleTodo _ -> Todos.update msg model
        DeleteTodo _ -> Todos.update msg model
        -- User domain
        GotUser _ -> Users.update msg model
        -- Auth domain
        Login _ -> Auth.update msg model
        Logout -> Auth.update msg model
        _ -> ( model, Cmd.none )
```

The Msg type stays in Main.sky. Helper update functions live in domain modules.
This is exactly how large Elm apps work — and it scales fine.

### Memory: unused page state

The Model always contains fields for all pages. If a user only visits /about,
the todos fields are still in memory (with default values).

**In practice this doesn't matter.** A Model with 50 fields is maybe 2KB.
You'd need millions of concurrent sessions before this is an issue.
And the fields are zero-initialized, not populated with data.

### Stale page state

User visits /users/42, navigates away, navigates back. The old user data
is still in `model.viewedUser`. Is that stale?

**This is a feature, not a bug.** The `navigateTo` function controls this:

```elm
navigateTo page model =
    case page of
        UserDetailPage userId ->
            -- Always refetch: clear old data and load fresh
            ( { model | page = page, viewedUser = Nothing, userLoading = True }
            , fetchUser userId GotUser
            )

        TodosPage ->
            -- Keep existing state: todos persist across navigation
            ( { model | page = page }, Cmd.none )
```

The developer decides per-page whether to keep or clear state.

---

## 11. Summary

| Aspect                | Per-Page Model          | Unified Model               |
|-----------------------|-------------------------|-----------------------------|
| Mental model          | Multiple state machines | One state machine           |
| State sharing         | Painful (threading)     | Free (just read model)      |
| Navigation            | Session swap + init     | Just a Msg                  |
| State persistence     | Lost on navigate        | Preserved                   |
| Compiler diff         | Same perf               | Same perf (with page guard) |
| Event sourcing        | Session churn           | One continuous log           |
| Code organization     | Forced split by page    | Split views, unify state    |
| 50+ pages             | Natural isolation       | Need update helpers          |
| Dev onboarding        | Familiar (Next.js-like) | Familiar (Elm SPA-like)     |

**Recommendation: Unified model is the right default for Sky.Live.**

It's simpler, more powerful, and the compiler handles the complexity.
For the rare case where true isolation is needed (e.g., an embedded widget),
support mounting independent `live` components within a page — but that's
an escape hatch, not the primary pattern.
