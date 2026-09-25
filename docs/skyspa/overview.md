# Sky.Spa — client-side TEA (overview)

> **Status: supported feature.** Sky.Spa is the **client-wasm backend of
> [`Std.App`](../skyapp/overview.md)** — you write an `App.app` and select it
> with a client `--target` (`web:app` / `mobile:*` / `tablet:*`); `Std.Spa` is
> the low-level runtime the build drives, not a module you import. The runtime
> partition, the auto-split, and the `Std.Bundle` packaging story are built,
> tested, and stable. It targets **desktop / mobile-embed webview first**; the
> constraints below (e.g. web-as-a-first-class-target) are real current scope
> boundaries, not instability. This page documents what the Sky.Spa target *is*
> and what is **not yet** in scope.

Sky.Spa runs the Sky TEA loop **on the client**. You write the *same*
`Model / Msg / update / view` you would write for [Sky.Live](../skylive/overview.md),
over the *same* renderer-agnostic `Std.Ui.Element` — but instead of the loop
running server-side and streaming HTML patches over SSE, the whole loop compiles
to `GOOS=js GOARCH=wasm` and runs in the browser. Pure `update` branches run
client-side with **zero round-trip**; there is no per-user server `Model`, no
session, no SSE.

- **What it is:** one language for the whole stack, a client renderer over the
  cross-platform `Element`, and an *explicit, typed* server boundary that shares
  one `Std.Codec` with the backend.
- **Where it runs today:** desktop and mobile-embed (webview). Production **web is
  a v2 bet** — the wasm bundle is ~2.5 MB gzip (see [Honest limits](#honest-limits)).
- **Design of record:** [design.md](design.md) (thesis + the two grill findings +
  the staged plan) and [auto-split.md](auto-split.md) (the v2 compiler-derived
  split). Read those for the *why*; this page is the *how-to*.
- **Worked example:** [`examples/60-spa-todos`](../../examples/60-spa-todos) — a
  full-stack Sky.Spa Todos app (wasm client + stateless SQLite backend + one
  shared wire contract).

## Build & run — one command

An `App.app` entry built to a **client `--target`** (`web:app` / `mobile:*` /
`tablet:*`) selects the Sky.Spa backend, which auto-splits into a wasm frontend
+ a native backend by the normal verbs — you do not run the generator by hand:

```bash
sky run   --target web:app  src/Main.sky  # split → build wasm frontend + native backend → run it
sky build --target web:app  src/Main.sky  # split + build both (artefacts under .split/)
sky build --embed --target mobile:ios …   # flags COMPOSE: --embed → backend PostgreSQL, --target → frontend shell
```

Pin the target once in `sky.toml` (`[app]` → `target = "web:app"`) and a bare
`sky build` / `sky run` picks it. `sky run` starts the backend, which serves the
frontend + `/_rpc` + the dev console + metrics same-origin (one binary) — open
the printed `http://localhost:<port>/`. `sky check` type-checks the shared
source without splitting. The explicit generator (`sky spa-split <entry> --out
<dir>`) is the low-level form for when you want the
`frontend/`/`backend/`/`shared/` trees kept at a chosen path; see
[`docs/tooling/cli.md`](../tooling/cli.md) and [auto-split.md](auto-split.md).

## When to use Sky.Spa vs Sky.Live

Sky.Live keeps the loop on the server: per-user `Model`, a live SSE per session,
a full server-side re-render each interaction. It scales, but the ceiling is the
stateful fleet (sticky sessions, session store, SSE fan-out). Sky.Spa moves the
loop to the client, which changes the trade:

| | Sky.Live | Sky.Spa |
|---|---|---|
| Where `update` runs | server (trusted) | client (**untrusted** — see [Security](#security)) |
| Pure UI transition | round-trips to the server | **client-local, zero round-trip** |
| Backend | stateful (session + SSE per user) | **stateless** — auth + effects + durable data only |
| Scaling axis | sticky sessions / SSE fan-out | horizontal stateless API; DB is the only shared axis |
| First-paint cost | server HTML (light) | wasm bundle (~2.5 MB gzip today) |
| Target today | web, terminal, desktop | **desktop / mobile-embed**; web = v2 |

Reach for **Sky.Spa** when pure UI transitions should be instant and local (rich
client-side interaction), the backend can be a stateless API, and the delivery
target is a desktop/mobile-embed webview where a one-time wasm download is fine.
Stay on **Sky.Live** for a browser web app today — its first paint is server HTML,
not a multi-megabyte wasm blob.

## The programming model — same as Sky.Live

An app is written exactly like a Sky.Live / web `App.app`; only the build
`--target` (a client wasm backend) and a Model-shape convention change. The four
TEA fields go in `App.app`; routing and the server boundary are attached with
`App.withX` builders (exactly like the web target's optionals):

```elm
appDef =
    App.app
        { init = Model.init
        , update = Update.update      -- pure branches run on the CLIENT
        , view = View.view            -- Std.Ui Element, painted client-side to the DOM
        , subscriptions = Subs.subs   -- Sub.every timers, reconciled after each update
        }
        |> App.withRoutes
            [ App.route "/" All
            , App.route "/active" Active
            , App.route "/completed" Completed
            ]
        |> App.withNotFound NotFound


main =
    App.run appDef       -- built --target web:app  (runner-direct: App.runSpa appDef)
```

`view` is `Std.Ui` (the default — see the pinned defaults in
[AGENTS.md](../../AGENTS.md)), not `Std.Html`: the Sky.Spa client renderer paints
any `Element` tree to the DOM, so the *same* view could target Sky.Live (web),
Sky.Tui (terminal), or Sky.Webview (desktop).

## `Model = { ui, data }` — source of truth, not "where it lives"

In Sky.Spa the *entire* Model is client-owned (the loop runs on the client), so
the useful declaration is **source of truth**, expressed structurally:

```elm
type alias Model =
    { page : Page       -- the routed page (the router sets it)
    , ui   : Ui         -- client-owned, ephemeral, NEVER serialized (no codec)
    , data : DataCache  -- a cached projection of server truth (has a Std.Codec)
    }
```

The wire boundary falls out of the types: **things in `data` have a `Std.Codec`**
(they cross the network and hit the DB); **things in `ui` are plain Sky types with
no codec** (they never leave the client). "Has a codec ⇒ server-backed" *is* the
boundary. Sky removed `RemoteData` pre-v1, so model the fetch lifecycle with an
explicit ADT (`Loading | Loaded a | Failed Error | Stale a`) rather than a magic
wrapper.

> v1 does **not** auto-enforce the `{ ui, data }` split — that is the v2
> auto-split ([auto-split.md](auto-split.md)). v1 apps follow the discipline by
> hand, which keeps them forward-compatible with the v2 mechanism.

## The server boundary — generated, with an explicit low-level form

Under a client `--target` the auto-split derives the boundary for you: an
ordinary effectful `update` branch becomes a generated `POST /_rpc/<Msg>` (pure
→ client, any effect → server; see [auto-split.md](auto-split.md)) over a
`Std.Codec` shared with the backend — one type, one codec, one wire contract, no
OpenAPI/TS drift. The **low-level, explicit** form — talk to a stateless Sky
backend by hand with `Std.Spa.getJson` / `postJson`, decoding with that **same**
codec — is what the generated code uses under the hood:

```elm
Refresh ->
    ( { model | ui = setStatus Loading model.ui }
    , Spa.getJson todosCodec "/api/todos" GotTodos )

GotTodos (Ok todos) -> ( { model | data = { todos = todos } }, Cmd.none )
GotTodos (Err e)    -> ( { model | ui = setStatus (Failed e) model.ui }, Cmd.none )
```

The idiom that makes the wire contract literally one file: put the shared types +
codecs in a single `Shared.sky` and **symlink** it into both the client and
server projects. Add a field there and *both* the client and server stop
compiling — that is the whole point.

`getJson` / `postJson` are ordinary Sky over `Cmd.perform` + `Http` + `Codec`
(no new runtime kernel). They hand `update` a `Result Error a` directly (a 2xx +
decoded body, or an `Err` — a non-2xx status, a decode failure, and a network
failure are all `Err`), so the app writes one `case`, not two.

### How the client paints and handles events

The client renders with the same diff as Sky.Live
([input-authority protocol](../skylive/input-authority-protocol.md) §Patch
operations): children are matched by key (`Std.Ui.Keyed`, a `name`d field) or
by shape, and a matched child keeps its DOM node, so a focused input keeps its
focus, caret and typing when something is inserted above it. Event payloads
follow Sky.Live's convention: `onKeyDown` / `onKeyUp` / `onKeyPress` get
`event.key`, `onCheck` gets the checkbox's Bool, input / change get the value.
A control's value is written only when the rendered value changes (Elm
semantics): when `update` refuses an edit, the field keeps what the user typed. While an IME composition is
open the field's input events are not dispatched; the committed text is, once.
A server-painted first page is adopted (hydrated) only when it shows exactly
what the client's first view says; otherwise the client builds the page itself.
Adjacent texts (`text "Hello, "` next to `text name`) arrive as one browser
text node, and hydration splits that node at the client's boundaries; the
server's nodes stay in the page.

## Routing — `App.withRoutes` (History API)

Routing is opt-in via the `App.withX` builders (a single-view app needs none).
The names read exactly like the web target:

- `App.route path page` — register a static route (`App.route "/about" About`).
  Put literal routes before `:param` patterns.
- `App.routeParam path toPage` — a route whose `path` carries a `:param` segment
  (`App.routeParam "/thing/:id" ThingPage`, `ThingPage : String -> Page`),
  captured as a **String** and passed to the page constructor. The captured
  value is percent-DECODED (`/u/J%C3%B6rg` gives `"Jörg"`), identically on the
  client, in the server-side first paint and on Sky.Live. Parse it (e.g.
  `String.toInt`) inside the constructor or `view` when you need a typed id;
  route an id your app rejects to `App.withNotFound`.
- `App.withRoutes routes` — resolves `location.pathname` on mount, on an
  intercepted internal-link click, and on Back/Forward, setting `model.page`.
- `App.withNotFound page` — the page shown when nothing matches.
- `App.withOnNavigate (page -> msg)` — fired after the route is applied, so the
  app can run an effect per navigation.

Internal `<a href>` clicks are intercepted (History `pushState`, no reload);
Back/Forward (`popstate`) is honoured; an external host, `target="_blank"`, a
`download`, a `sky-external` mark, or a modified click is left to the browser.

The full surface (typed signatures + summaries) is `sky doc Std.App`.

## Client persistence and identity

A `web:app` client writes its whole model to `localStorage` after each
`update` and restores it on a full page load, so a reload keeps client scratch
state (a form draft, a counter, a dismissed banner). `localStorage` is shared by
every tab of the origin, and it holds ONE model: the last one any tab wrote.

**A stored model restores only for the identity it was stored under.** The
identity is the value of the session field(s): the model fields of type
`Session` / `Maybe Session` that a server branch writes, which the backend signs
into the `sky_sid` cookie and the SSR seed carries. On a full load the client
compares the identity of the stored model with the identity of the seed:

| Stored model → seed | Result |
|---|---|
| same identity, or signed out → signed out | the stored model restores over the seed |
| one identity → another identity | nothing restores, the stored copy is removed |
| signed out → signed in | nothing restores, the stored copy is removed (a basket filled before sign-in is not carried over) |
| signed in → signed out | nothing restores, the stored copy is removed |

So two tabs with two identities (a practitioner in one, a patient link in the
other) never show each other's data. Each tab's full load after the other tab
wrote boots from its own seed: the tab loses its unsaved scratch state, never its
server data. When an `update` signs out (clears the session field), the client
removes the stored copy and does not write the signed-out model for the rest of
that page's life, because that model still holds the signed-in user's other
fields. A page served without an SSR seed (a static deploy, a native shell) has
no server identity to compare, and restores the stored model as it is. An app
with no session field has one identity, and restores as before.

**Shared computers.** The stored model stays in the browser until the next
change of identity, sign-out in the page, or a full load under another
identity. A user who closes the tab without signing out leaves their last model
in `localStorage`, where the next user of that browser profile can read it with
the developer tools (the app never shows it to them). Keep sensitive data in
server state, not in client scratch fields, if the app runs on shared machines.

Before v0.25.18 the model was stored under `sky:spa:model` with no identity
check. The client deletes that key on the first load and restores it only for an
app with no session field. The current key is `sky:spa:model:v2`.

**Sky.Live has no such store.** A Sky.Live model lives on the server, keyed by
the `sky_sid` session; the browser holds only the cookie. Two Sky.Live tabs with
the same cookie share one session by design, and nothing of the model is
written to the browser.

## Security — the untrusted client is a first-class rule

In Sky.Live `update` runs on the server → **trusted**. In Sky.Spa `update` runs
on the user's machine → **untrusted**. Therefore, unavoidably:

- The backend **re-validates and re-authorizes every request** and **re-reads
  authoritative data** (price, role, ownership, ids) from its own store. It may
  **never** trust a client-sent field for anything security-relevant.
- `getJson` / `postJson` are **transport, not trust** — they carry no ambient
  authority. Auth is an explicit header/cookie the author adds and the backend
  verifies with `Std.Auth` on every call; there is no session a client can spoof,
  because the backend is stateless.
- Because a Sky.Spa client uses a stateless JSON API with no cookie session,
  browser-form CSRF guards nothing — use `Server.api` routes (CSRF bypassed by
  design); security rests on re-validation, not CSRF.
- Sky's typed secrets (`Auth.signToken` takes `String`, never `any`) and the
  production gate carry over unchanged.

## Honest limits

These are real, current scope boundaries — not roadmap optimism:

- **Bundle weight → desktop/mobile-embed only.** A real Sky.Spa app compiles to a
  standard Go→wasm bundle of **~9.5 MB raw / ~2.5 MB gzip**
  (`examples/60-spa-todos`, measured). That is fine for a one-time
  desktop/mobile-embed download; it is **too heavy for production web** (Elm's
  equivalent ≈30 KB). The size is inherent to real Sky dispatch being
  reflection-native (`sky_call` / `reflect.MakeFunc`), which keeps most of the
  runtime reachable.
- **Web / TinyGo / Sky→JS = v2.** The named lever to shrink the bundle (TinyGo)
  cannot compile `reflect.MakeFunc`, so production web needs *either* a
  reflection-free core rewrite *or* a Sky→JS backend. Both are **v2 bets**, not
  done. See [design.md §0/§9](design.md).
- **Browser pixel-check pending.** The TEA loop, the client renderer, and the
  full round-trip are proven **headlessly** (Node + a DOM shim; `examples/60`'s
  `run_roundtrip.sh` asserts persistence, the zero-round-trip property, routing,
  and reload rehydration). The in-browser *visual* confirmation awaits a
  connected browser extension — confirmation, not a new risk.
- **Auto-split = v2.** v1's boundary is explicit (author-declared server calls).
  The compiler-derived client/server partition ("no hand-written API routes") is
  the v2 target, specified in [auto-split.md](auto-split.md); v1-dialect apps are
  forward-compatible with it.
- **Client effect surface is bounded in v1.** Client effects run through a
  single-threaded wasm interpreter: `Cmd.perform` (sync kernels like
  `Time.now` / `Random` inline; async `Http` via `fetch`) and `Sub.every` timers.
  `Cmd.publish` is a documented client no-op (no peer/session bus in a single
  tab); `Sub.subscribeTopic` / stream / websocket subscriptions are not wired on
  the client in v1.

## See also

- [design.md](design.md) — thesis, the two grill findings (bundle wall + thesis
  computability), the staged plan, and the measured evidence.
- [auto-split.md](auto-split.md) — the v2 compiler-derived split (`Task`-body
  tracing + the effects-via-`Cmd` dialect).
- [`examples/60-spa-todos`](../../examples/60-spa-todos) — the worked full-stack
  example.
- `sky doc Std.App` — the live front-door API (the low-level transport is
  `sky doc Std.Spa`).
