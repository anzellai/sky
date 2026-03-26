# Sky.Live — HTTP-First Server-Driven UI

## Design Exploration

Developer writes standard TEA. The compiler generates:
1. A Go HTTP server with routing, sessions, and static assets
2. A diff engine that sends minimal HTML patches
3. A tiny JS client (~3KB) that applies patches and sends events
4. An event-sourcing layer for state reconstruction

No WebSocket required. Works on Lambda, Cloud Run, any HTTP host.

---

## 1. Application Structure

### Project Layout

```
my-app/
  sky.toml
  src/
    Main.sky              -- entry point: defines routes + app config
    Pages/
      Home.sky            -- live page (TEA)
      Todos.sky           -- live page (TEA)
      About.sky           -- static page (no state)
      Users/
        List.sky          -- live page
        Detail.sky        -- live page with route params
    Layout.sky            -- shared layout (nav, footer)
    Components/
      Navbar.sky          -- reusable view components
      Footer.sky
  static/
    style.css             -- served at /static/style.css
    logo.svg
```

### sky.toml

```toml
[package]
name = "my-app"
version = "0.1.0"

[live]
port = 4000
ttl = "30m"

[live.session]
store = "memory"

[live.static]
dir = "static"            # served at /static/*
```

---

## 2. Routing

### Main.sky — The Router

```elm
module Main exposing (main)

import Std.Live exposing (app, live, static, route, scope, redirect)
import Std.Live.Middleware exposing (logger, recover)
import Pages.Home as Home
import Pages.Todos as Todos
import Pages.About as About
import Pages.Users.List as UserList
import Pages.Users.Detail as UserDetail
import Layout

main =
    app
        { layout = Layout.view
        , middleware = [ logger, recover ]
        , routes =
            [ route "/" (live Home.page)
            , route "/todos" (live Todos.page)
            , route "/about" (static About.page)
            , scope "/users"
                [ route "/" (live UserList.page)
                , route "/:id" (live UserDetail.page)
                ]
            , route "/old-home" (redirect "/")
            ]
        , notFound = static About.notFoundPage
        }
```

### Route Types

| Helper     | What it does                                          |
|------------|-------------------------------------------------------|
| `live`     | Mounts a TEA page — has `init`, `update`, `view`, server-managed state |
| `static`   | Renders once, no state, no event handling. Pure `Request -> Html`. Fast. |
| `redirect` | HTTP 301/302 redirect to another path                 |
| `scope`    | Groups routes under a prefix. Composes with middleware. |

### How It Compiles

The compiler reads the route table and generates a Go HTTP mux:

```go
// Generated from Main.sky routes
func setupRoutes(mux *http.ServeMux, store SessionStore) {
    mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
    mux.HandleFunc("/_sky/live.js", serveLiveJS)
    mux.HandleFunc("/_sky/event", handleEvent(store))

    mux.HandleFunc("GET /", withMiddleware(liveHandler(homePage, store)))
    mux.HandleFunc("GET /todos", withMiddleware(liveHandler(todosPage, store)))
    mux.HandleFunc("GET /about", withMiddleware(staticHandler(aboutPage)))
    mux.HandleFunc("GET /users/", withMiddleware(liveHandler(userListPage, store)))
    mux.HandleFunc("GET /users/{id}", withMiddleware(liveHandler(userDetailPage, store)))
    mux.HandleFunc("GET /old-home", redirectHandler("/"))
}
```

---

## 3. Pages

### Live Page (Stateful TEA)

```elm
module Pages.Todos exposing (page)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (Page, onEvent)
import Std.Live.Events exposing (onClick, onSubmit, onInput)
import Std.Cmd as Cmd

type alias Todo =
    { id : Int
    , text : String
    , done : Bool
    }

type alias Model =
    { todos : List Todo
    , draft : String
    , nextId : Int
    }

type Msg
    = UpdateDraft String
    | AddTodo
    | ToggleTodo Int
    | DeleteTodo Int

init : Request -> ( Model, Cmd Msg )
init _req =
    ( { todos = [], draft = "", nextId = 1 }
    , Cmd.none
    )

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        UpdateDraft text ->
            ( { model | draft = text }, Cmd.none )

        AddTodo ->
            if model.draft == "" then
                ( model, Cmd.none )
            else
                ( { model
                    | todos = model.todos ++ [ { id = model.nextId, text = model.draft, done = False } ]
                    , draft = ""
                    , nextId = model.nextId + 1
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

view : Model -> Html Msg
view model =
    div [ class "todo-app" ]
        [ h1 [] [ text "Todos" ]
        , form [ onSubmit AddTodo ]
            [ input [ type_ "text", value model.draft, onInput UpdateDraft, placeholder "What needs to be done?" ]
            , button [ type_ "submit" ] [ text "Add" ]
            ]
        , ul [ class "todo-list" ]
            (List.map viewTodo model.todos)
        , footer []
            [ span [] [ text (String.fromInt (List.length (List.filter (\t -> not t.done) model.todos)) ++ " items left") ]
            ]
        ]

viewTodo : Todo -> Html Msg
viewTodo todo =
    li [ class (if todo.done then "completed" else "") ]
        [ span [ onClick (ToggleTodo todo.id) ] [ text (if todo.done then "✓ " else "○ ") ]
        , span [] [ text todo.text ]
        , button [ onClick (DeleteTodo todo.id), class "delete" ] [ text "×" ]
        ]

page : Page
page =
    { init = init
    , update = update
    , view = view
    , subscriptions = \_ -> Sub.none
    , title = "Todos"
    }
```

### Static Page (No State)

```elm
module Pages.About exposing (page, notFoundPage)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (Request)

page : Request -> Html msg
page _req =
    div [ class "about" ]
        [ h1 [] [ text "About" ]
        , p [] [ text "Built with Sky.Live" ]
        ]

notFoundPage : Request -> Html msg
notFoundPage req =
    div [ class "not-found" ]
        [ h1 [] [ text "404" ]
        , p [] [ text ("Page not found: " ++ req.path) ]
        ]
```

### Live Page with Route Params

```elm
module Pages.Users.Detail exposing (page)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (Page, Request)
import Std.Cmd as Cmd

type alias Model =
    { userId : String
    , user : Maybe User
    , loading : Bool
    }

type alias User =
    { name : String
    , email : String
    }

type Msg
    = GotUser (Result String User)

init : Request -> ( Model, Cmd Msg )
init req =
    let
        userId =
            req.params.id           -- extracted from "/:id" in route
    in
    ( { userId = userId, user = Nothing, loading = True }
    , fetchUser userId              -- Cmd that fires GotUser on completion
    )

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        GotUser (Ok user) ->
            ( { model | user = Just user, loading = False }, Cmd.none )

        GotUser (Err _) ->
            ( { model | loading = False }, Cmd.none )

view : Model -> Html Msg
view model =
    case model.user of
        Nothing ->
            if model.loading then
                div [] [ text "Loading..." ]
            else
                div [] [ text "User not found" ]

        Just user ->
            div [ class "user-detail" ]
                [ h1 [] [ text user.name ]
                , p [] [ text user.email ]
                ]

page : Page
page =
    { init = init
    , update = update
    , view = view
    , subscriptions = \_ -> Sub.none
    , title = "User Detail"
    }
```

---

## 4. Layouts — Shared Shell

```elm
module Layout exposing (view)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (Request, PageContent)
import Components.Navbar as Navbar
import Components.Footer as Footer

view : Request -> PageContent -> Html msg
view req content =
    div [ class "app-shell" ]
        [ Navbar.view req.path
        , mainNode [ class "content" ]
            [ content.body ]
        , Footer.view
        ]
```

```elm
module Components.Navbar exposing (view)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live.Navigation exposing (link)

view : String -> Html msg
view currentPath =
    nav [ class "navbar" ]
        [ link "/" [ classList [ ("active", currentPath == "/") ] ] [ text "Home" ]
        , link "/todos" [ classList [ ("active", currentPath == "/todos") ] ] [ text "Todos" ]
        , link "/users" [ classList [ ("active", String.startsWith "/users" currentPath) ] ] [ text "Users" ]
        , link "/about" [ classList [ ("active", currentPath == "/about") ] ] [ text "About" ]
        ]
```

### How Layout Wraps Pages

On `GET /todos`, the server:
1. Runs `Todos.init req` → Model
2. Runs `Todos.view model` → page HTML
3. Runs `Layout.view req { body = pageHtml, title = "Todos" }` → full HTML
4. Wraps in `<!DOCTYPE html>` shell with `<script src="/_sky/live.js">`

The layout is **static** — it renders once. Only the `content.body` region
is live-patched. The compiler knows the layout boundary and scopes `sky-id`
attributes to the page content area.

---

## 5. Navigation Between Pages

### Client-Side Navigation (No Full Reload)

```elm
-- Std.Live.Navigation
link : String -> List Attribute -> List (Html msg) -> Html msg
```

`link` renders an `<a>` tag with `sky-nav` attribute:

```html
<a href="/todos" sky-nav>Todos</a>
```

The JS client intercepts `sky-nav` clicks:

```javascript
// In live.js
document.addEventListener('click', (e) => {
  const link = e.target.closest('[sky-nav]');
  if (!link) return;
  e.preventDefault();

  const href = link.getAttribute('href');
  history.pushState({}, '', href);
  navigateTo(href);
});

async function navigateTo(path) {
  const res = await fetch('/_sky/navigate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, sid })
  });
  const { html, title, newSid } = await res.json();

  // Replace the live content region
  document.querySelector('[sky-root]').innerHTML = html;
  document.querySelector('[sky-root]').setAttribute('sky-root', newSid);
  if (title) document.title = title;
  sid = newSid;

  // Re-bind events for new page
  bindEvents();

  // Update active nav links
  document.querySelectorAll('[sky-nav]').forEach(a => {
    a.classList.toggle('active', a.getAttribute('href') === path);
  });
}

// Handle browser back/forward
window.addEventListener('popstate', () => navigateTo(location.pathname));
```

### What `/_sky/navigate` Does Server-Side

```
POST /_sky/navigate  { "path": "/todos", "sid": "s_abc123" }

→ Server:
  1. Expire old session (s_abc123 was for /home)
  2. Match /todos → Todos.page (live)
  3. Run Todos.init req → (model, cmd)
  4. Run Todos.view model → page HTML
  5. Create new session s_def456 with model
  6. Return { html: "<div class=\"todo-app\">...", title: "Todos", newSid: "s_def456" }
```

The layout shell stays in place. Only the inner content swaps.
This gives SPA-like navigation without a client-side router.

### Navigation for Static Pages

Static pages skip session creation:

```
POST /_sky/navigate  { "path": "/about", "sid": "s_abc123" }

→ { html: "<div class=\"about\">...", title: "About", newSid: null }
```

`newSid: null` tells the client there's no live session for this page.
Events are disabled until navigating to a live page.

---

## 6. The Request Object

Every `init` and static page receives a `Request`:

```elm
type alias Request =
    { method : String             -- "GET", "POST", etc.
    , path : String               -- "/users/42"
    , params : Params             -- { id = "42" } from route pattern
    , query : Dict String String  -- ?search=foo → Dict.singleton "search" "foo"
    , headers : Dict String String
    , cookies : Dict String String
    }
```

The compiler generates `Params` as a record type matching the route pattern:

```elm
-- Route: "/users/:id"  →  Params = { id : String }
-- Route: "/posts/:year/:slug"  →  Params = { year : String, slug : String }
-- Route: "/"  →  Params = {}
```

This is type-safe — accessing `req.params.id` on a route without `:id`
is a compile error.

---

## 7. Middleware

```elm
module Std.Live.Middleware exposing (Middleware, logger, recover, cors, basicAuth)

-- Middleware wraps a handler: Request → Request (or short-circuit with Response)
type alias Middleware =
    Request -> Result Response Request
```

### Built-in Middleware

```elm
-- Logs method, path, status, duration to stdout
logger : Middleware

-- Catches panics, returns 500 with stack trace in dev mode
recover : Middleware

-- CORS headers
cors : CorsConfig -> Middleware
cors config = ...

-- Basic auth (checks against hardcoded or env-var credentials)
basicAuth : String -> String -> Middleware
basicAuth user pass = ...
```

### Custom Middleware

```elm
module Middleware.Auth exposing (requireAuth)

import Std.Live exposing (Middleware, Request, Response)

requireAuth : Middleware
requireAuth req =
    case Dict.get "Authorization" req.headers of
        Just token ->
            if validateToken token then
                Ok req
            else
                Err (Response.unauthorized "Invalid token")

        Nothing ->
            Err (Response.redirect "/login")
```

### Scoped Middleware

```elm
main =
    app
        { layout = Layout.view
        , middleware = [ logger, recover ]    -- global: applies to all routes
        , routes =
            [ route "/" (live Home.page)
            , route "/login" (live Login.page)
            , scope "/admin"                  -- scoped: only /admin/* routes
                [ route "/" (live Admin.Dashboard.page)
                , route "/users" (live Admin.Users.page)
                ]
                |> withMiddleware [ requireAuth ]
            , scope "/api"
                [ route "/health" (static healthCheck)
                , route "/users" (live Api.Users.page)
                ]
                |> withMiddleware [ cors defaultCors ]
            ]
        , notFound = static notFoundPage
        }
```

---

## 8. API Routes (JSON Endpoints)

Not everything is HTML. Sky.Live handles JSON API routes too:

```elm
module Api.Users exposing (page)

import Std.Live exposing (Request, Response, json)
import Std.Json.Encode as E

-- Static route that returns JSON, not HTML
page : Request -> Response
page req =
    case req.method of
        "GET" ->
            let
                users =
                    fetchAllUsers ()
            in
            json 200
                (E.list encodeUser users)

        "POST" ->
            case decodeBody req.body userDecoder of
                Ok user ->
                    let
                        created = insertUser user
                    in
                    json 201 (encodeUser created)

                Err err ->
                    json 400
                        (E.object [ ("error", E.string err) ])

        _ ->
            json 405
                (E.object [ ("error", E.string "Method not allowed") ])

encodeUser user =
    E.object
        [ ("id", E.int user.id)
        , ("name", E.string user.name)
        , ("email", E.string user.email)
        ]
```

### Route Registration

```elm
routes =
    [ route "/" (live Home.page)
    , scope "/api"
        [ route "/users" (api Api.Users.page)     -- returns Response, not Html
        , route "/users/:id" (api Api.UserDetail.page)
        , route "/health" (api healthCheck)
        ]
        |> withMiddleware [ cors defaultCors ]
    ]
```

`api` routes bypass the layout, session store, and live.js entirely.
They're plain HTTP handlers compiled to Go.

---

## 9. Compiler: Events & Msg Serialization

### onClick takes a Msg, not a JS string

```elm
button [ onClick Increment ] [ text "+" ]
-- Produces: <button sky-click="Increment" sky-id="c3">+</button>

button [ onClick (DeleteTodo 42) ] [ text "×" ]
-- Produces: <button sky-click="DeleteTodo" sky-args="[42]" sky-id="c7">×</button>

input [ onInput UpdateDraft, value model.draft ]
-- Produces: <input sky-input="UpdateDraft" value="buy milk" sky-id="c4">
```

The compiler sees the `Msg` type definition and generates:

```go
func decodeMsg(name string, args []json.RawMessage) Msg {
    switch name {
    case "Increment":   return MsgIncrement{}
    case "Decrement":   return MsgDecrement{}
    case "AddTodo":     return MsgAddTodo{}
    case "UpdateDraft": return MsgUpdateDraft{Text: decodeString(args[0])}
    case "ToggleTodo":  return MsgToggleTodo{Id: decodeInt(args[0])}
    case "DeleteTodo":  return MsgDeleteTodo{Id: decodeInt(args[0])}
    default:            return nil  // reject unknown — security
    }
}
```

No runtime reflection. Strict validation. Unknown Msg names are rejected.

---

## 10. Compiler: Smart Diffing via Static Analysis

### The Key Insight

Given `update` and `view`, the compiler can trace data flow:

```elm
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

view model =
    div [ class "counter" ]
        [ h1 [] [ text (String.fromInt model.count) ]     -- reads model.count
        , button [ onClick Decrement ] [ text "-" ]         -- static
        , button [ onClick Increment ] [ text "+" ]         -- static
        ]
```

The compiler determines:
1. `Increment` changes `model.count`
2. Only `h1` reads `model.count`
3. Buttons are static

Generated optimised patcher:

```go
func patchIncrement(oldModel, newModel Model) []Patch {
    if oldModel.Count != newModel.Count {
        return []Patch{{ID: "0", Text: strconv.Itoa(newModel.Count)}}
    }
    return nil
}
```

**No runtime tree diff.** The compiler pre-computes the dependency graph.

### sky-id Assignment

Only dynamic nodes get `sky-id`:

```
div [ class "counter" ]                          -- static, no sky-id
    [ h1 [] [ text (String.fromInt model.count)] -- DYNAMIC → sky-id="0"
    , button [...] [ text "-" ]                  -- static, no sky-id
    , button [...] [ text "+" ]                  -- static, no sky-id
    ]
```

In a typical app, ~80% of the DOM is static. Those nodes are invisible
to the diff engine.

---

## 11. Event Sourcing

### TEA Is Already Event Sourcing

```
Model = foldl update (fst (init ())) messages
```

Model is fully determined by the ordered sequence of Msg values.

### Session Flow

```
┌──────────┐     ┌──────────────────────────────┐
│ JS Client│────→│ POST /_sky/event              │
│          │←────│                               │
└──────────┘     │  1. Load session (log/snap)   │
                 │  2. Replay if needed           │
                 │  3. Apply new msg              │
                 │  4. Append msg to log          │
                 │  5. Snapshot if interval hit   │
                 │  6. Diff view, return patches  │
                 └──────────────┬────────────────┘
                                │
                     ┌──────────▼──────────┐
                     │   Session Store      │
                     │                      │
                     │  s_abc123:           │
                     │    snapshot@50: {...} │
                     │    msgs@51+: [...]   │
                     └─────────────────────┘
```

### Compiler Optimizations

**Snapshots** — every N messages, serialise Model:

```
Session s_abc123:
  snapshot@50:  { count: 47 }
  messages@51+: [Increment, Increment, Decrement]

Replay cost: 3 messages, not 53.
```

**Message compaction** — the compiler detects overwrite semantics:

```elm
-- UpdateDraft sets draft = text (overwrite)
-- Consecutive UpdateDraft msgs collapse to just the last one
UpdateDraft "h"
UpdateDraft "he"        -- skipped during replay
UpdateDraft "hel"       -- skipped during replay
UpdateDraft "hello"     -- only this one runs
AddTodo                 -- boundary: previous UpdateDraft kept
```

**Arithmetic folding** — pure numeric updates batch:

```go
// Compiler detects: Increment is count + 1, Decrement is count - 1
// Three Increments = count + 3
delta := 0
for _, msg := range msgs {
    switch msg.(type) {
    case MsgIncrement: delta++
    case MsgDecrement: delta--
    default:
        model.Count += delta
        delta = 0
        model = update(msg, model)
    }
}
model.Count += delta
```

---

## 12. Session Store

Default is in-memory. Swap the store in sky.toml — zero code changes.

```toml
[live.session]
store = "memory"         # default
```

| Store        | Config                           | Best For                           |
|--------------|----------------------------------|------------------------------------|
| `memory`     | `store = "memory"`               | Development, single instance. Zero deps. **Default.** |
| `sqlite`     | `store = "sqlite"`               | Single-server production. Persistent, no external deps. |
|              | `path = "./data/sessions.db"`    |                                    |
| `postgresql` | `store = "postgresql"`           | Multi-instance. Horizontal scaling. |
|              | `url = "$DATABASE_URL"`          |                                    |
| `redis`      | `store = "redis"`                | Low-latency. Cloud Run / ECS.     |
|              | `url = "$REDIS_URL"`             |                                    |
| `valkey`     | `store = "valkey"`               | Open-source Redis. Wire-compatible. |
|              | `url = "$VALKEY_URL"`            |                                    |
| `dynamodb`   | `store = "dynamodb"`             | AWS serverless. Scales to zero.    |
|              | `table = "sky_sessions"`         |                                    |

All stores implement:

```go
type SessionStore interface {
    Load(sid string) ([]Msg, *ModelSnapshot, error)
    Append(sid string, msg Msg) error
    Snapshot(sid string, model Model, atIndex int) error
    Expire(sid string) error
}
```

Progression: `memory` → `sqlite` → `postgresql`/`redis` → `dynamodb`.
One line in sky.toml. The compiler generates the right Go driver imports.

---

## 13. The JS Client (~3KB)

```javascript
// /_sky/live.js — auto-served by Sky runtime
(function() {
  const root = document.querySelector('[sky-root]');
  let sid = root ? root.getAttribute('sky-root') : null;

  // ── Event binding ──────────────────────────────────
  function bindEvents() {
    // Click events
    root.querySelectorAll('[sky-click]').forEach(el => {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('click', () => {
        send(el.getAttribute('sky-click'), jsonArgs(el));
      });
    });

    // Input events (debounced)
    root.querySelectorAll('[sky-input]').forEach(el => {
      if (el._skyBound) return;
      el._skyBound = true;
      let timer;
      el.addEventListener('input', (e) => {
        clearTimeout(timer);
        timer = setTimeout(() => {
          send(el.getAttribute('sky-input'), [e.target.value]);
        }, 150);
      });
    });

    // Form submit
    root.querySelectorAll('[sky-submit]').forEach(el => {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('submit', (e) => {
        e.preventDefault();
        const data = Object.fromEntries(new FormData(e.target));
        send(el.getAttribute('sky-submit'), [data]);
      });
    });
  }

  function jsonArgs(el) {
    const raw = el.getAttribute('sky-args');
    return raw ? JSON.parse(raw) : [];
  }

  // ── Event dispatch ─────────────────────────────────
  async function send(msg, args) {
    if (!sid) return;
    const res = await fetch('/_sky/event', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ msg, args: args || [], sid })
    });
    if (!res.ok) {
      if (res.status === 410) return location.reload(); // session expired
      return;
    }
    const { patches } = await res.json();
    applyPatches(patches);
  }

  // ── DOM patching ───────────────────────────────────
  function applyPatches(patches) {
    for (const p of patches) {
      const el = root.querySelector(`[sky-id="${p.id}"]`);
      if (!el) continue;
      if (p.text !== undefined) el.textContent = p.text;
      if (p.html !== undefined) { el.innerHTML = p.html; }
      if (p.attrs) {
        for (const [k, v] of Object.entries(p.attrs)) {
          v === null ? el.removeAttribute(k) : el.setAttribute(k, v);
        }
      }
      if (p.remove) el.remove();
      if (p.append) el.insertAdjacentHTML('beforeend', p.append);
    }
    bindEvents();  // re-bind for new nodes
  }

  // ── Client-side navigation ─────────────────────────
  document.addEventListener('click', (e) => {
    const link = e.target.closest('[sky-nav]');
    if (!link) return;
    e.preventDefault();
    const href = link.getAttribute('href');
    history.pushState({}, '', href);
    navigateTo(href);
  });

  async function navigateTo(path) {
    const res = await fetch('/_sky/navigate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, sid })
    });
    const { html, title, newSid } = await res.json();
    root.innerHTML = html;
    if (newSid) {
      root.setAttribute('sky-root', newSid);
      sid = newSid;
    } else {
      sid = null;  // static page, no session
    }
    if (title) document.title = title;
    bindEvents();
    // Update active nav states
    document.querySelectorAll('[sky-nav]').forEach(a => {
      const isActive = a.getAttribute('href') === path
        || (path !== '/' && a.getAttribute('href') !== '/' && path.startsWith(a.getAttribute('href')));
      a.classList.toggle('active', isActive);
    });
  }

  window.addEventListener('popstate', () => navigateTo(location.pathname));

  // ── Init ───────────────────────────────────────────
  if (root) bindEvents();
})();
```

---

## 14. Full Generated Server (Simplified)

What the compiler produces for a full app:

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"
    "sync"
)

// ── Session store ────────────────────────────────────
type MemoryStore struct {
    mu       sync.RWMutex
    models   map[string]interface{}
    logs     map[string][]MsgEnvelope
    pages    map[string]string       // sid → page identifier
}

type MsgEnvelope struct {
    Name string            `json:"msg"`
    Args []json.RawMessage `json:"args"`
}

// ── Route table ──────────────────────────────────────
type PageDef struct {
    Init   func(req Request) (interface{}, []Cmd)
    Update func(msg interface{}, model interface{}) (interface{}, []Cmd)
    View   func(model interface{}) VNode
    Decode func(name string, args []json.RawMessage) interface{}
    Title  string
}

var routes = map[string]PageDef{
    "/":      homePageDef,
    "/todos": todosPageDef,
    "/users/{id}": userDetailPageDef,
}

var staticRoutes = map[string]func(Request) string{
    "/about": aboutPage,
}

// ── HTTP handlers ────────────────────────────────────
func main() {
    store := &MemoryStore{
        models: make(map[string]interface{}),
        logs:   make(map[string][]MsgEnvelope),
        pages:  make(map[string]string),
    }

    mux := http.NewServeMux()

    // Static assets
    mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
    mux.HandleFunc("/_sky/live.js", serveLiveJS)

    // Live event handler (all pages share this endpoint)
    mux.HandleFunc("POST /_sky/event", func(w http.ResponseWriter, r *http.Request) {
        var env struct {
            Msg  string            `json:"msg"`
            Args []json.RawMessage `json:"args"`
            SID  string            `json:"sid"`
        }
        json.NewDecoder(r.Body).Decode(&env)

        store.mu.Lock()
        defer store.mu.Unlock()

        pageId, ok := store.pages[env.SID]
        if !ok {
            http.Error(w, "session expired", 410)
            return
        }

        pageDef := routes[pageId]
        model := store.models[env.SID]
        oldView := pageDef.View(model)

        msg := pageDef.Decode(env.Msg, env.Args)
        newModel, cmds := pageDef.Update(msg, model)
        processCmds(cmds) // execute side effects

        newView := pageDef.View(newModel)
        patches := diff(oldView, newView)

        store.models[env.SID] = newModel
        store.logs[env.SID] = append(store.logs[env.SID], MsgEnvelope{
            Name: env.Msg, Args: env.Args,
        })

        json.NewEncoder(w).Encode(map[string]interface{}{
            "patches": patches,
        })
    })

    // Navigation handler
    mux.HandleFunc("POST /_sky/navigate", func(w http.ResponseWriter, r *http.Request) {
        var nav struct {
            Path string `json:"path"`
            SID  string `json:"sid"`
        }
        json.NewDecoder(r.Body).Decode(&nav)

        // Expire old session
        if nav.SID != "" {
            store.mu.Lock()
            delete(store.models, nav.SID)
            delete(store.logs, nav.SID)
            delete(store.pages, nav.SID)
            store.mu.Unlock()
        }

        req := buildRequest(r, nav.Path)

        // Check static routes first
        if handler, ok := staticRoutes[nav.Path]; ok {
            html := handler(req)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "html": html, "title": "About", "newSid": nil,
            })
            return
        }

        // Live route
        pageId, pageDef := matchRoute(nav.Path)
        model, cmds := pageDef.Init(req)
        processCmds(cmds)

        newSid := generateSID()
        store.mu.Lock()
        store.models[newSid] = model
        store.pages[newSid] = pageId
        store.mu.Unlock()

        html := renderToString(pageDef.View(model))
        json.NewEncoder(w).Encode(map[string]interface{}{
            "html": html, "title": pageDef.Title, "newSid": newSid,
        })
    })

    // Page routes (initial GET requests)
    for path, pageDef := range routes {
        pd := pageDef
        mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
            req := buildRequest(r, r.URL.Path)
            model, cmds := pd.Init(req)
            processCmds(cmds)

            sid := generateSID()
            store.mu.Lock()
            store.models[sid] = model
            store.pages[sid] = r.URL.Path
            store.mu.Unlock()

            pageHtml := renderToString(pd.View(model))
            fullHtml := wrapLayout(req, pageHtml, pd.Title, sid)
            w.Header().Set("Content-Type", "text/html")
            w.Write([]byte(fullHtml))
        })
    }

    log.Println("Sky.Live server running on :4000")
    log.Fatal(http.ListenAndServe(":4000", mux))
}
```

---

## 15. Architecture Summary

```
                    ┌───────────────────────────────────────┐
                    │          Developer writes:             │
                    │  routes, pages (TEA), layout, static   │
                    └──────────────────┬────────────────────┘
                                       │
                        ┌──────────────▼──────────────┐
                        │       Sky Compiler           │
                        │                              │
                        │  • Route table generation     │
                        │  • Msg codec per page         │
                        │  • Static analysis / sky-id   │
                        │  • Per-Msg patch functions    │
                        │  • Model serialiser           │
                        │  • Replay optimiser            │
                        │  • Session store driver        │
                        └──────────────┬──────────────┘
                                       │
         ┌─────────────────────────────┼─────────────────────────────┐
         │                             │                             │
┌────────▼────────┐         ┌──────────▼──────────┐       ┌─────────▼─────────┐
│  Go HTTP Server  │         │   live.js (~3KB)    │       │  Session Store    │
│                  │         │                      │       │                   │
│ GET /path →      │         │ bind sky-* events   │       │ memory (default)  │
│   full HTML page │         │ POST /_sky/event    │       │ sqlite            │
│                  │         │ apply DOM patches   │       │ postgresql        │
│ POST /_sky/event │         │ sky-nav navigation  │       │ redis / valkey    │
│   → JSON patches │         │ pushState routing   │       │ dynamodb          │
│                  │         │ back/forward        │       │                   │
│ POST /_sky/nav   │         │ session recovery    │       │ event log +       │
│   → page swap    │         │                      │       │ snapshots         │
│                  │         │                      │       │                   │
│ /static/* files  │         │                      │       │                   │
│ /api/* JSON      │         │                      │       │                   │
└─────────────────┘         └─────────────────────┘       └───────────────────┘
```

### Request Lifecycle

```
Browser                     Server                          Store
  │                           │                               │
  │  GET /todos               │                               │
  │──────────────────────────→│                               │
  │                           │  init(req) → model            │
  │                           │  view(model) → html           │
  │                           │  store session ──────────────→│
  │  full HTML page           │                               │
  │←──────────────────────────│                               │
  │                           │                               │
  │  user clicks "+"          │                               │
  │                           │                               │
  │  POST /_sky/event         │                               │
  │  { msg: "AddTodo" }       │                               │
  │──────────────────────────→│                               │
  │                           │  load session ←──────────────│
  │                           │  update(msg, model) → model'  │
  │                           │  diff(view(model), view(model'))
  │                           │  store model' ───────────────→│
  │  { patches: [...] }       │                               │
  │←──────────────────────────│                               │
  │                           │                               │
  │  user clicks "Todos" nav  │                               │
  │                           │                               │
  │  POST /_sky/navigate      │                               │
  │  { path: "/todos" }       │                               │
  │──────────────────────────→│                               │
  │                           │  expire old session ─────────→│
  │                           │  init(req) → model            │
  │                           │  view(model) → html           │
  │                           │  new session ─────────────────→│
  │  { html, newSid }         │                               │
  │←──────────────────────────│                               │
  │  (swap inner content,     │                               │
  │   pushState, re-bind)     │                               │
```

---

## 16. Upgrade Path: SSE for Subscriptions

When `subscriptions` returns non-empty, the JS client auto-opens an SSE stream:

```elm
subscriptions model =
    Time.every 1000 Tick
```

```
GET /_sky/stream?sid=s_abc123
→ Content-Type: text/event-stream

data: {"patches":[{"id":"clock","text":"14:30:05"}]}
data: {"patches":[{"id":"clock","text":"14:30:06"}]}
```

This is opt-in per page. Pages without subscriptions use pure HTTP.

---

## 17. Summary: What the Developer Writes vs What's Generated

### Developer writes:

```
Main.sky          — routes + config (~20 lines)
Layout.sky        — shared shell (~15 lines)
Pages/Todos.sky   — standard TEA (~80 lines)
Pages/About.sky   — static page (~10 lines)
sky.toml          — port + session store (~8 lines)
```

### Compiler generates:

- Go HTTP server with mux, middleware chain, static file serving
- Per-page Msg encoder/decoder (strict, no reflection)
- Per-page Model serialiser (for snapshots)
- Per-Msg optimised patch functions (static analysis)
- sky-id assignment (dynamic nodes only)
- Event log replay with compaction + snapshots
- Session store implementation matching sky.toml config
- live.js client (static asset, ~3KB)
- Layout wrapper with `<head>`, live.js injection
- API route handlers (bypass layout/sessions)

**Zero boilerplate. Zero JS to write. Deploy anywhere.**
