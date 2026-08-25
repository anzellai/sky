# Sky.Spa — client-side TEA (overview)

> **Status: supported feature.** Sky.Spa is part of the Sky stdlib (`Std.Spa`);
> the runtime partition, the auto-split, and the `Std.Bundle` packaging story are
> built, tested, and stable. It targets **desktop / mobile-embed webview first**;
> the constraints below (e.g. web-as-a-first-class-target) are real current scope
> boundaries, not instability. This page documents what Sky.Spa *is* and what is
> **not yet** in scope.

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

A `Spa.app` entry (it imports `Std.Spa`) is auto-split by the normal verbs — you
do not run the generator by hand:

```bash
sky run src/Main.sky              # split → build wasm frontend + native backend → run it
sky build src/Main.sky            # split + build both (artefacts under .split/)
sky build --embed --target ios …  # flags COMPOSE: --embed → backend PostgreSQL, --target → frontend shell
```

`sky run` starts the backend, which serves the frontend + `/_rpc` + the dev
console + metrics same-origin (one binary) — open the printed
`http://localhost:<port>/`. `sky check` type-checks the shared source without
splitting. The explicit generator (`sky spa-split <entry> --out <dir>`) is for
when you want the `frontend/`/`backend/`/`shared/` trees kept at a chosen path;
see [`docs/tooling/cli.md`](../tooling/cli.md) and [auto-split.md](auto-split.md).

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

An app is written like a Sky.Live app; only the entry point and a Model-shape
convention change. The four TEA fields go in `Spa.config`; routing and the
server boundary are attached with `withX` builders (exactly like Sky.Live's
optionals):

```elm
main =
    Spa.app
        (Spa.config
            { init = Model.init
            , update = Update.update      -- pure branches run on the CLIENT
            , view = View.view            -- Std.Ui Element, painted client-side to the DOM
            , subscriptions = Subs.subs   -- Sub.every timers, reconciled after each update
            }
            |> Spa.withRoutes
                [ Spa.route "/" All
                , Spa.route "/active" Active
                , Spa.route "/completed" Completed
                ]
            |> Spa.withNotFound NotFound
        )
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

## The explicit, typed server boundary

v1's boundary is **explicit** (author-declared), not compiler-derived. Talk to a
stateless Sky backend with `Std.Spa.getJson` / `postJson`, decoding with a
`Std.Codec` that is the **same** codec compiled into the backend — one type, one
codec, one wire contract, no OpenAPI/TS drift:

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

## Routing — `withRoutes` (History API)

Routing is opt-in via the `withX` builders (a single-view app needs none). The
names read exactly like Sky.Live:

- `Spa.route path page` — register a route; `path` may contain `:param` segments
  (`/thing/:id`), captured as a **String** and passed to a page constructor
  (`ThingPage : String -> Page`). Put literal routes before `:param` patterns.
- `Spa.routeInt path toPage` — a `:param` route whose captured segment is an
  **Int** (`TodoDetail : Int -> Page`); the segment is parsed before it reaches
  the constructor, so the page carries a typed `Int`, not a `String` you
  re-parse in `view`. A non-integer segment makes the route **not match**, so
  `/todo/abc` falls through to the next route or `withNotFound` — exactly as an
  invalid id should. (The runtime can't see the constructor's parameter type
  under the erased ABI, so the Int-ness is declared at the route.)
- `Spa.withRoutes routes` — resolves `location.pathname` on mount, on an
  intercepted internal-link click, and on Back/Forward, setting `model.page`.
- `Spa.withNotFound page` — the page shown when nothing matches.
- `Spa.withOnNavigate (page -> msg)` — fired after the route is applied, so the
  app can run an effect per navigation.

Internal `<a href>` clicks are intercepted (History `pushState`, no reload);
Back/Forward (`popstate`) is honoured; an external host, `target="_blank"`, a
`download`, a `sky-external` mark, or a modified click is left to the browser.

The full surface (typed signatures + summaries) is `sky doc Std.Spa`.

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
- `sky doc Std.Spa` — the live API surface.
