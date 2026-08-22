# 60 · Sky.Spa Todos

The culminating **Sky.Spa** example: a real, non-trivial single-page app whose
TEA loop (`Model / Msg / update / view`) runs **entirely in the browser** as
WebAssembly, talking to a **stateless `Sky.Http.Server` backend** over an
**explicit, typed, shared-codec boundary**. One language for the whole stack;
one `Element` view; one wire contract.

It exercises the complete Sky.Spa v1 stack end to end:

- **Client-owned UI state (zero round-trip).** The new-todo input text, the
  active filter, and the in-place edit buffer are pure `update` branches that run
  on the client and **never touch the network**. Typing and filtering are
  instant. This is a *measured* property, not a claim — `run_headless.cjs` counts
  every `fetch` and asserts that typing + three filter navigations make **zero**
  network calls.
- **Durable data via the explicit boundary.** Todos persist through
  `Std.Spa.getJson` / `Std.Spa.postJson` (HTTP + a **shared `Std.Codec`**) to the
  backend, which stores them in **SQLite** via `Std.Db.Store`. Add / toggle /
  rename / delete each round-trip and persist; the list loads on start and
  rehydrates when the client reloads.
- **Client-side routing.** The filters are real routes (`/`, `/active`,
  `/completed`) registered with `Std.Spa.withRoutes`. Internal `<a href>` clicks
  are intercepted (History `pushState`, no page reload); Back/Forward work.
- **`Std.Ui` view (cross-platform `Element`).** The view is written in `Std.Ui`,
  the renderer-agnostic layout DSL — the *same* `Element` view could render on
  Sky.Live (web), Sky.Tui (terminal), and Sky.Webview (desktop). No
  `Std.Html`-only lock-in. (The Sky.Spa client renderer paints the `Element`
  tree straight to the DOM — verified here headlessly.)

## Layout

```
shared/Shared.sky      the ONE wire contract (Todo + codecs), symlinked into
client/                the Sky.Spa wasm client (Model/Msg/update/view, Std.Ui)
  src/Main.sky           - Model = { page, ui, data }
  src/Shared.sky         → ../../shared/Shared.sky (symlink)
server/                the STATELESS Sky.Http.Server backend (SQLite store)
  src/Main.sky           - Server.api routes + Server.static for same-origin
  src/Shared.sky         → ../../shared/Shared.sky (symlink)
public/index.html      the Go/wasm bootstrap page (served by the backend)
run_roundtrip.sh       reproducible headless full-loop acceptance (below)
run_e2e_db.sh          DB-backed boundary acceptance — curl as a hostile client (below)
run.sh                 serve for a real browser (prints the URL)
```

`Shared.sky` is **literally one file** symlinked into both projects, so the type
and codec flow both ways: change a field there and **both** the client and the
server stop compiling. That is the whole point — no OpenAPI/TS drift.

## Model = `{ page, ui, data }`

Per the Sky.Spa design, the model declares **source of truth**, not "where it
lives" (the whole model is client-owned — the loop runs on the client):

- `page` — the routed filter (`All` / `Active` / `Completed`); the router owns it.
- `ui` — ephemeral client state (input text, edit buffer, fetch status). No
  codec: it never leaves the browser.
- `data` — a cached projection of server truth (the todo list). It **has a
  `Std.Codec`** (in `Shared.sky`) because it crosses the wire and hits the DB.
  "Has a codec ⇒ server-backed" *is* the boundary.

v1 does not auto-enforce the `{ui, data}` split (that is the v2 auto-split); this
app demonstrates the discipline by hand.

## Security — the untrusted client

The client runs on the user's machine and is **untrusted**. It only *proposes*
mutations; the backend is authoritative:

- Every handler **re-validates** input (trims + length-clamps the title, rejects
  an empty title, ignores an unknown id) and **re-reads** the authoritative list
  from its own store before answering.
- Ids are assigned by the database (a serial primary key); a client-sent id or
  `done` flag on create is discarded.
- The endpoints are `Server.api` routes: a Sky.Spa client uses a **stateless**
  JSON API with no cookie session, so browser-form **CSRF guards nothing here**
  and is correctly bypassed — security rests on re-validation, not CSRF. A real
  app adds an `Authorization` header the backend verifies with `Std.Auth`.

## Run it

Headless full-loop acceptance (no browser needed) — builds everything, starts
the backend on a clean DB, drives the real wasm client against it, and asserts
persistence, the zero-round-trip property, routing, and reload rehydration:

```bash
./run_roundtrip.sh                 # defaults to port 8951
TODOS_PORT=8971 ./run_roundtrip.sh # pick another port
```

DB-backed boundary acceptance — `curl` as a **hostile** client (raw requests
that bypass the wasm UI), proving the untrusted-boundary + durable-store
properties: server-side re-validation (trim / 200-char clamp / reject-empty),
server-owned identity (DB serial id; client-sent `id`/`done` ignored), unknown-id
no-op, malformed→400, full-authoritative-list responses, cross-client shared
state, durability across a **backend restart**, and concurrent parallel writes
with no lost updates (18 assertions):

```bash
./run_e2e_db.sh                    # defaults to port 8952
```

In a real browser (same-origin, one binary serves the client and the API):

```bash
./run.sh                           # then open http://localhost:8951/
```

Open DevTools' Network tab: typing and switching filters make **no** requests;
only add/toggle/rename/delete hit `/api/...`. Data survives a page reload and a
server restart (SQLite at `server/app.db`).

## Bundle size

`client/main.wasm` (standard Go→wasm): **≈9.05 MB raw / ≈2.40 MB gzip**. This is
**desktop/mobile-embed weight** — exactly the Sky.Spa v1 target. It is heavier
than a trivial counter because it pulls `Std.Ui`, `Std.Codec`, HTTP, and the
routing runtime. It is **too heavy for production web**; the web-bundle lever
(a reflection-free core or a Sky→JS backend) is a documented v2 open decision,
not solved here. See `docs/skyspa/design.md` §0/§9.
