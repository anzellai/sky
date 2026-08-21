# Sky.Spa — client-side TEA, statically partitioned (design)

> **Status:** experimental (`exp/spa`). Phase 1 (de-risk) complete with measured
> evidence (see [§9](#9-evidence)). This document is the design of record; it is
> grounded in the *actual* Sky surfaces (file:line cited), not aspiration.

## 1. Thesis

**Sky.Spa is the Sky.Live TEA loop, statically partitioned so the pure part runs
on the client.** One Sky program; the compiler decides what runs where. The one
property that justifies building it — and that no other stack can offer — is:

> **If it compiles, the client/server state split is sound.** A Model field
> written by both a client-pure `update` branch *and* a server-effect branch is a
> *split conflict* the compiler rejects (or forces a declared merge policy). The
> sync therefore cannot clobber, by construction.

That property needs three things at once — one language, one typed whole-program
IR, and a pure `update`. Sky has all three. Elm has the loop but no backend;
TS/React has a backend but no shared type system across the wire (OpenAPI/TS
drift); server-driven frameworks (Sky.Live, LiveView) keep the loop on the server
and pay the SSE/session/sticky tax. Sky.Spa is the only point that gets the
client loop *and* a proven-sound boundary.

## 2. Why — the scalability argument

Sky.Live holds per-user `Model` + a live SSE per session + re-renders the full
view server-side each interaction; it scales, but the ceiling is the stateful
fleet (sticky sessions, session store, SSE fan-out). Sky.Spa moves the loop to
the client:

- **Pure UI transitions are client-local** — zero round-trip, no server state.
- **The backend becomes stateless** — no per-user Model, no session, no SSE. It
  authenticates, executes server effects, and owns durable `data`. It scales like
  any stateless API (horizontal, no sticky ceiling); the DB is the only shared
  axis, scaled separately.
- **The client holds its own state** — free (the user's device), and the view
  cost is the client's, not a shared fleet's.

This is the path to enterprise millions-of-concurrent: keep the stateful loop on
the client, keep the backend stateless.

## 3. Programming model — same as Sky.Live

An author writes the *same* `Model / Msg / update / view` they'd write for
Sky.Live, over the *same* renderer-agnostic `Std.Ui.Element`. Only the entry
point and the Model's shape convention change.

```elm
main =
    Spa.app
        (Spa.config
            { init          = Model.init
            , update        = Update.update   -- PURE branches run on the CLIENT
            , view          = View.view        -- Element Msg, rendered client-side to the DOM
            , subscriptions = Subs.subscriptions
            , routes        = Routes.routes     -- client-side routing (History API)
            , notFound      = NotFoundPage
            }
            |> Spa.withApi   "https://api.example.com"  -- backend base for server effects
            |> Spa.withMount "#app"
        )
```

### 3.1 Model = `{ ui, data }` — source of truth, not "where it lives"

In Sky.Spa the *entire* Model is client-owned (the loop runs on the client), so
the useful declaration is **source of truth**, expressed structurally:

```elm
type alias Model =
    { ui   : Ui         -- client-owned, ephemeral, NEVER serialized (no codec)
    , data : DataCache  -- a cached projection of server truth (has a Std.Codec)
    }
```

The wire boundary then falls out of the types for free: **things in `data` have a
`Std.Codec`** (they cross the network and hit the DB); **things in `ui` are plain
Sky types with no codec** (they never leave the client). "Has a codec ⇒
server-backed" *is* the boundary. Sky removed `RemoteData` pre-v1, so model the
fetch lifecycle with an explicit ADT (`Loading | Loaded Data | Failed Error |
Stale Data`) rather than reaching for a magic wrapper.

## 4. The client/server boundary — AST-derived, no hand-written routes

Because `update : Msg -> Model -> (Model, Cmd Msg)` is pure and the whole program
is typed, static analysis gives us, **per `Msg` branch**, three facts:

1. **Effect target** — `pure` / `client-effect` (browser `Http`, storage, `Time`,
   nav) / **`server-effect`** (`Db`, server `Task`, secrets). Not just
   "effectful," but *where*. The runtime already distinguishes `Cmd.none` from an
   effectful `Cmd` by the `cmdT.kind` string (`runtime-go/rt/live.go:1823`,
   `:5950`); Sky.Spa lifts that to a *compile-time* per-branch classification.
2. **Read-set** — which Model fields the branch reads to compute the new model +
   the effect's arguments.
3. **Write-set** — which Model fields it writes.

With those:

- **Pure branch** → runs client-side, zero round-trip.
- **Client-effect branch** → runs in the client effect interpreter (fetch,
  storage, timer).
- **Server-effect branch** → an **auto-generated RPC**: the client sends
  `Msg + read-set` (minimal, not the whole Model); the server runs the branch and
  returns the **write-set delta**; the client applies just those fields.

**No hand-written API routes** — the effectful branches *are* the routes; the
compiler derives the endpoint, payload shape, and response shape from the
branch's read/write-set. (v1 may use an explicit `Http` boundary as a
scaffold; the auto-derived boundary is the target — see [§8](#8-staged-plan).)

### 4.1 The disjointness check (the thesis, made mechanical)

If client-owned fields (`ui`) and server-owned fields (`data`) are **disjoint**,
pure branches only write `ui`, server branches only write `data`, write-sets
never overlap, and **reconciliation is trivial** — the server returns a `data`
delta, the client applies it, its concurrent `ui` edits untouched. No CRDT, no
OT. A field written by *both* a client-pure branch and a server-effect branch is
a *split conflict* the compiler rejects (or requires a declared merge policy).
That check is what makes "if it compiles it works" hold across the wire.

## 5. Architecture — WASM core + per-platform renderer

**WASM does not render.** It runs the loop and produces the `Element` tree; a thin
per-platform renderer paints it. So the shape is:

> **WASM = the portable logic core (Model + pure `update` + `view → Element`).
> The renderer is the only per-platform shim.**

| Target | Core | Renderer |
|---|---|---|
| Web | WASM in browser | `Element → DOM` via a small JS glue |
| Mobile (webview) | WASM in a webview | same DOM renderer → a webview app |
| Mobile (native) | WASM runtime embedded | `Element → SwiftUI/Compose` bridge |
| Desktop | WASM (webview/embed) | DOM or native |

This maps directly onto Sky's existing renderer-agnostic `Element`
(`sky-stdlib/Std/Ui.sky:55-69`), which lowers to the DOM-facing
`Std.Html.Html msg` (`sky-stdlib/Std/Html.sky:24-28`).

### 5.1 The runtime-partition (the real work)

The blocker, measured in Phase 1: the emitted app imports exactly one Go package,
`import rt "sky-app/rt"` (`rust/crates/codegen/src/lib.rs:60`), and `rt` is a
**single monolithic package** (109 non-test files) that Go must compile as a
unit. `rt` pulls `net/http` (28 files), `database/sql` (9), `os` (48),
`os/exec`/`os/signal`/`syscall`, `jackc/pgx`, `redis`, and `modernc.org/sqlite` —
so a full app **cannot** compile under `GOOS=js GOARCH=wasm` (build fails on
`modernc.org/libc` constraint exclusions). There is **no cgo in the core path**
(sqlite is pure-Go `modernc.org/sqlite`), so the entanglement is *mechanical, not
deep.*

The two structural walls, both in our control:

1. **The monolithic import.** Fix: build-tag-partition `rt` — `//go:build !js` on
   every net/db/os/server/exec file, plus a `//go:build js` wasm entrypoint. The
   existing `webview.go:60` (`//go:build cgo && darwin`) /
   `webview_stub.go:16` split is the working precedent. *Or* emit against a
   separate minimal SPA runtime package for the Spa target.
2. **`live.go` welds the TEA core to net/http.** The primitives Sky.Spa needs —
   `VNode`, `renderVNode`/`renderVNodeInto` (`live.go:435-489`),
   `diffTrees`/`diffNodes` (`live.go:1632`), `cmdT` (`:1823`),
   `assignSkyIDs` (`:781`) — currently live in `live.go`, which imports
   `net/http`/`os`/`os/signal`/`syscall` (`live.go:16-44`). Fix: extract them into
   a portable `live_core.go`; leave the net/http dispatch + signals in
   `live_server.go`; replace `runCmd` (coupled to `liveApp`/goroutines/OTEL,
   `:5950`) with a small **single-threaded wasm effect interpreter** over the same
   `cmdT` value.

**Reusable as-is:** the client DOM patcher `__skyApplyPatches` (`live.go:8787`) —
its focus/cursor preservation and dirty-input/open-`<select>` authority logic is
battle-tested and directly applicable to the client renderer. **Not** reusable:
today the diff runs *server-side* in Go and the client only splices HTML strings;
a true SPA runs `renderVNode`/`diffTrees` in wasm (or reimplements them client-side).

## 6. The five pillars — how each is met (none compromised)

- **DX** — an app is written like Sky.Live (`Model/Msg/update/view` over
  `Element`); the split is compiler-derived, not hand-plumbed. Same `sky` CLI,
  same type errors.
- **Scalability** — stateless backend + client-held state → horizontal; no
  sticky/SSE/session-store ceiling.
- **Maintenance** — one language, one type system, one `Element` view, one shared
  `Codec` wire contract; no TS/OpenAPI drift.
- **Performance** — pure UI transitions client-local (zero round-trip); server
  calls batched/on-demand; view cost is the client's. *Open cost:* the wasm
  bundle (see [§7](#7-security--the-untrusted-client) is security;
  [§9](#9-evidence) has the measured bundle + the mitigations).
- **Security** — see §7; enforced at the generated boundary, not documented and
  hoped.

## 7. Security — the untrusted client is a first-class rule

In Sky.Live, `update` runs on the server → **trusted**. In Sky.Spa, `update` runs
on the user's machine → **untrusted**. Therefore, unavoidably:

- The server **re-validates and re-authorizes every server-effect**, and
  **re-reads authoritative data from the DB** rather than trusting a client-sent
  read-set for anything security-relevant (price, role, ownership).
- The generated boundary makes the auth/validation hook a **required** part of a
  server-effect branch — the compiler can generate the plumbing (RPC + delta) but
  the *trust rule* is declared by the author and cannot be omitted.
- Sky's typed secrets (`Auth.signToken` takes `String`, never `any`), `Std.Auth`,
  and the prod gate carry over unchanged; the backend is a normal stateless Sky
  server subject to all of them.

## 8. Staged plan

| Phase | Deliverable | Status |
|---|---|---|
| **1. De-risk** | wasm feasibility + bundle number + headless renderer/loop proof + arch map | ✅ **done** ([§9](#9-evidence)) |
| **2. Runtime-partition** | split `live.go` → `live_core.go` (portable) + `live_server.go`; build-tag `rt` for `js`; wasm effect interpreter over `cmdT` | next |
| **3. Emit path** | `Spa.app` config surface + a `spa` emit target that imports the portable core; `sky build --target spa` → `main.wasm` + JS glue; **decide web bundle lever** (TinyGo vs Sky→JS) on evidence | |
| **4. Client renderer** | `Element→DOM` renderer reusing `__skyApplyPatches` focus/cursor logic; client-side `diffTrees` | |
| **5. AST boundary** | per-branch effect classification + read/write-set analysis in `hir`/`ty`/`lower`; the **disjointness check**; explicit `Http` boundary first | |
| **6. Auto-RPC** | auto-generated server-effect endpoints (read-set in, write-set delta out) + required per-branch trust hook | |

Each phase is additive and independently verifiable. Phase 2 is surgery on the
most critical runtime file (`live.go`) — it is **extraction only, no behavior
change** (Sky.Live keeps importing the extracted core), gated by the full example
sweep + a real Sky.Live app per CLAUDE.md §0.2.1.

## 9. Evidence (Phase 1, measured on `exp/spa`)

- **Full `rt` → `js/wasm`: FAILS** — `import rt "sky-app/rt"` forces the whole
  monolith; build errors on `modernc.org/libc` constraint exclusions. Only 9 of
  109 files import sqlite/`database/sql`, but one package compiles as a unit.
- **Bundle size (standard Go→wasm, trivial counter): 1.90 MB raw / 579 KB gzip**
  (+4 KB `wasm_exec.js`), ~583 KB over the wire. Fine for desktop/mobile-embed;
  **too heavy for production web** (Elm's equivalent ≈30 KB). **Levers:** TinyGo
  (~10–20× smaller wasm; not yet installed — must verify the portable core
  compiles under TinyGo's reflect/stdlib limits) or a future Sky→JS backend. The
  web-bundle lever is decided in Phase 3 on evidence, not theory.
- **Client TEA loop + `Element→DOM` renderer: VERIFIED** headlessly in Node (Go
  wasm + a DOM shim). All transitions pass with **zero server**: init→`0`,
  `+1`×3→`3`, `Reset`→`0`, `−1`→`−1`. Proves wasm instantiation, `syscall/js`
  interop, the renderer, and pure `update` + re-render per dispatched `Msg`.
- The spike (`docs/skyspa/spike/`) is a faithful hand-written mirror of what the
  Spa emit path will generate — `Element`/`Model`/`Msg`/pure `update`/`view` +
  the DOM renderer + the TEA driver — so Phase 3 has a concrete generation target.
- **Pending (needs the Chrome extension connected):** the pixel-level in-browser
  visual check. The loop is proven headlessly; the visual is confirmation, not a
  new risk.

## 10. Open decisions (surfaced, not blocking)

1. **Web bundle lever** — TinyGo vs Sky→JS. Decided in Phase 3 on measured
   evidence. Does not block Phases 2/4/5 (architecture proof + native/mobile-embed
   are fine on standard wasm).
2. **v1 boundary** — explicit `Http` (author writes endpoints, shared `Codec`) as
   a scaffold before the auto-derived RPC. The explicit path is secure and
   achievable first; the auto-RPC (Phase 6) is the magic on top.
