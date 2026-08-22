# Sky.Spa v1 — client routing + the explicit typed server boundary (P4)

> **Status:** P4 design of record. This is the DX-defining surface: `Std.Spa`
> client-side routing (History API) and the *explicit, author-declared* server
> boundary (client `Http` → a stateless Sky backend, sharing a `Std.Codec`).
> Every claim below is grounded in real Sky surfaces (file:line), verified
> against the code.

## Phase-0 architecture consult (findings, file:line)

### Sky.Live routing — the shape P4 mirrors

- Config carries `routes : List Route` + `notFound : page`
  (`sky-stdlib/Std/Live.sky:72-80`); `route : String -> page -> Route`
  (`:91-93`); optional `withOnNavigate : (page -> msg) -> …` (`:131-133`).
- `Live_route path page` returns `liveRoute{path, page}`
  (`runtime-go/rt/live.go:1177-1179`).
- On every URL-driven navigation the server resolves the path to a route and
  **writes the model's `Page` field directly**:
  `applyRouteWithParams` → `RecordUpdate(model, {"Page": page})`
  (`runtime-go/rt/live.go:1500-1514`), after `fillRoutePage` fills a
  param-taking page constructor (`:1628-1664`). Matching is
  `matchRoute`/`splitPath` (`:1600-1624`).
- If `onNavigate` is set, `dispatchOnNavigate` calls `onNavigate page` → a `msg`
  → runs it through `update` for per-navigation effects
  (`runtime-go/rt/live.go:1577-1596`). No-op when unset.
- **Convention (inherited):** the model has a `page` field the runtime owns.
- Client JS interception lives in `live.go` JS blobs — `<a sky-nav>` click →
  `fetch` + `history.pushState` (`:6921-6966`), `popstate` re-fetch (`:6968+`).
  Sky.Spa needs no fetch on navigation (the loop is client-side already), so it
  reimplements only the *interception + pushState + popstate* in the wasm driver.

### `Std.Codec` — the shared wire contract

- `Codec a` bundles encode + decode + shape (`sky-stdlib/Std/Codec.sky:1-6`).
- `Codec.toJson : Codec a -> a -> String`, `Codec.fromJson : Codec a -> String
  -> Result Error a` (module header example). `Codec.auto` derives one from a
  blank value. This is the *one* definition compiled into **both** client and
  backend — the shared type flows across the wire from a single source.

### The client transport (P3, already landed)

- `Sky.Core.Http.get/post : … -> Task Error HttpResponse`
  (`sky-stdlib/Sky/Core/Http.sky:37-47`), which on `GOOS=js` calls browser
  `fetch` and returns a real settled `Result` (`runtime-go/rt/http_wasm.go`).
- `Cmd.perform : Task err a -> (Result err a -> msg) -> Cmd msg`
  (`sky-stdlib/Std/Cmd.sky:43`) — the toMsg receives `Result err a`, so a failed
  task surfaces as `Err`, never a silent drop.
- `Task.andThenResult : (a -> Result e b) -> Task e a -> Task e b`
  (`sky-stdlib/Sky/Core/Task.sky:112-114`) — folds a decode step into the task.
- All of `Task_*` / `Cmd_perform` / `RecordUpdate` / `asList` / `Field` /
  `isFunc` / `Dict_*` live in `rt.go` / `live_core.go` (no build tag → portable,
  available under `//go:build js`).

### The server side of the boundary

- `Sky.Http.Server` (`sky-stdlib/Sky/Http/Server.sky`): `listen : Int -> List
  Route -> Task Error ()`, `get`/`post`/`json`/`text`, typed `Request`/
  `Response`. A stateless backend is `Server.listen port [ Server.get "/things"
  handler ]` where the handler encodes a `Std.Codec` value with `Codec.toJson`
  and returns `Server.json`.

## Proposed API (implemented in P4)

### Routing — opt-in builders (config stays the 4 TEA fields)

```elm
type Route  -- opaque, produced by `route`

route : String -> page -> Route

withRoutes     : List Route -> AppConfig model msg -> AppConfig model msg
withNotFound   : page -> AppConfig model msg -> AppConfig model msg
withOnNavigate : (page -> msg) -> AppConfig model msg -> AppConfig model msg
```

```elm
main =
    Spa.app
        (Spa.config
            { init = init, update = update, view = view
            , subscriptions = subscriptions
            }
            |> Spa.withRoutes
                [ Spa.route "/"      Home
                , Spa.route "/about" About
                ]
            |> Spa.withNotFound NotFound
            |> Spa.withOnNavigate (\_ -> NavHappened)
        )
```

**Why builders, not `routes` in `config` (deviation from the literal brief —
rationale).** Sky.Live puts `routes` in `config` because a *server* must route
every URL; a Sky.Spa client can legitimately be a single-view app (the shipped
`spa-counter`/`spa-input`/`spa-perform`/`spa-sub`/`spa-http` apps have no
routes and MUST keep compiling). A required `routes`/`notFound` field would (a)
break every one of those and (b) force a meaningless `notFound` page on a
counter. Sky.Live *itself* attaches every optional through `withX` builders
(`withHead`/`withOnNavigate`/…), so a routed Spa app "reads the same" — same
`route` / `withOnNavigate` names, same mental model — while routing stays the
opt-in capability it actually is. `config` remains the four TEA fields; a routed
app adds `|> withRoutes […] |> withNotFound Page`.

**Convention (mirrors Sky.Live exactly):** a routed app's `Model` has a `page`
field. On navigation the runtime resolves the URL → page value and sets
`model.page` via `RecordUpdate(…, {"Page": …})`, re-renders, and — if
`withOnNavigate` is set — dispatches `onNavigate page` through `update`.

**Client router (wasm, `//go:build js`):**
- On mount, resolve `location.pathname` → route → set `model.page` before the
  first paint.
- A single document-level `click` listener intercepts a left-click (no
  modifier/middle) on an internal same-origin `<a href>` that is not
  `target=_blank`, not `download`, and not marked `sky-external` →
  `preventDefault` + `history.pushState` + apply-route + dispatch. Anything else
  (external host, marked escape, modified click) is a real browser navigation.
- A `popstate` listener (Back/Forward) applies the new URL's route + dispatches.
- Route matching reuses Sky.Live's exact algorithm (`/a/:id` segment match),
  reimplemented client-side (a small pure helper) so **`live.go` is not
  touched** — the Sky.Live server path stays byte-identical.

### The explicit typed server boundary — pure Sky over existing kernels

```elm
getJson  : Codec a -> String -> (Result Error a -> msg) -> Cmd msg
postJson : Codec body -> Codec a -> String -> body -> (Result Error a -> msg) -> Cmd msg
```

`getJson` issues `Http.get url`, checks the status is 2xx, decodes the body with
the shared `Codec a`, and hands `update` a `Result Error a` directly — removing
the double-nested `case` (HTTP result, then decode result) an app writes by
hand. `postJson` additionally encodes the request body with a `Codec body`.

**No new runtime kernel** — both are pure Sky over `Cmd.perform` + `Http.*` +
`Codec.fromJson`/`toJson` + `Task.andThenResult`, so they add zero runtime
surface and inherit P3's wasm transport unchanged. An app that needs headers /
auth cookies / a non-JSON shape drops to `Http.request` + `Codec` directly; the
helper is the ergonomic 90% path, not a wall.

The shared type is proved end-to-end by putting the type + its `Codec` in ONE
module imported by **both** the wasm client and the stateless `Sky.Http.Server`
backend: change the type, and both sides fail to compile — one type, one codec,
one wire contract, no OpenAPI/TS drift.

## Security — the untrusted client is first-class (not a footnote)

The Sky.Spa client runs on the user's machine, so it is **untrusted**. The
boundary's shape must not encourage trusting it, and the docs say so plainly:

- The stateless backend **re-validates and re-authorizes every request** and
  **re-reads authoritative data (price, role, ownership) from its own store** —
  it never trusts a client-sent field for anything security-relevant. A client
  can send any bytes; `getJson`/`postJson` are transport, not trust.
- `getJson`/`postJson` carry **no ambient authority**. Auth is an explicit
  header/cookie the author adds (`Http.request` + `withHeader`), and the backend
  verifies it with `Std.Auth` on every call — there is no session the client can
  spoof, because the backend is stateless.
- Sky's typed secrets (`Auth.signToken` takes `String`, never `any`) and the
  production gate carry over unchanged — the backend is a normal stateless Sky
  server subject to all of them.

## Five-pillar check

- **DX** — written like Sky.Live: same `Model/Msg/update/view`, `route` /
  `withOnNavigate` names, one `getJson` call for a typed round-trip.
- **Scalability** — the backend is stateless (no per-user Model, no session, no
  SSE); it scales horizontally, the DB is the only shared axis.
- **Maintenance** — one language, one type system, one `Element`, one `Codec`
  across the wire; the shared-type module makes drift a compile error.
- **Performance** — pure UI transitions + client-side routing are zero
  round-trip; only a `getJson`/`postJson` hits the network, on demand.
- **Security** — untrusted client is first-class (above); no ambient authority
  in the helper; typed secrets + prod gate intact.
</content>
