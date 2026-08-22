# Sky.Spa — client-side TEA, statically partitioned (design)

> **Status: EXPERIMENTAL (`exp/spa`) — NOT in any release** (shipped line is
> v0.21.x). A desktop/mobile-first, **explicit-boundary** Sky.Spa v1 is now
> **built and green on `exp/spa`** (phases P1–P5 — see the [staged plan §8](#8-staged-plan)
> and the tracker [v1-progress.md](v1-progress.md); the user-facing guide is
> [overview.md](overview.md)). Two things this document opened with remain the
> record and are **honoured, not shipped**: an adversarial grill found two
> BLOCKING realities the first spike dodged (§0) — the production-**web** path
> and the **auto-derived** split. Both were resolved *by scoping them to v2*, not
> by building them: v1 ships the client renderer + explicit boundary on
> desktop/mobile-embed weight (~2.5 MB gzip); web (TinyGo/Sky→JS) and the
> compiler-derived auto-split are v2 ([auto-split.md](auto-split.md)). Read §0
> first for why the *original* thesis and web pillar are not yet earned; every
> load-bearing claim is grounded in *actual* Sky surfaces (file:line), verified
> against the code.

## 0. Phase-1 grill findings — the two blocking realities (READ FIRST)

The Phase-1 spike (`docs/skyspa/spike/`) proved a *hand-written, reflection-free*
Go→wasm TEA loop runs client-side. That was necessary but it **dodged what real
Sky-emitted code actually is**. An adversarial grill, verified against the code,
found two walls:

1. **The TinyGo web-bundle lever is dead (G2 — verified).** Real Sky dispatch is
   reflection-native: `msg_dispatch.go`'s own header says *"every TEA-shaped
   backend routes every user event through a reflection-driven adapter (`sky_call`
   / `adaptFuncValue` / `reflect.MakeFunc`)"*; `Std.Codec.auto` is reflection-driven
   (`runtime-go/rt/codec_auto.go`), and `reflect` appears in **41 of the rt
   files**. TinyGo implements neither `reflect.MakeFunc` nor `reflect.Value.Call`.
   So the only named lever to get the 579 KB wasm down to web-viable size (~30 KB)
   **cannot compile the real core**. Production web therefore requires *either* a
   reflection-free rewrite of dispatch+codec+ADT (large, touches the shipping
   runtime) *or* a from-scratch Sky→JS backend (enormous). **Standard Go→wasm
   works but is desktop/mobile-embed weight; web is unproven.**

2. **The "compile-time-sound split" thesis is not computable as written (G1 —
   verified).** It depends on the compiler knowing, per `update` branch, the
   *effect target* + read-set + write-set. **None of that exists:** every effect
   is an opaque `Task Error a` (`sky-stdlib/Sky/Core/Task.sky`) with no
   server/client distinction, and a grep of `ty`/`hir` for any effect
   classification returns empty. Building it is greenfield whole-program dataflow,
   and it is **undecidable** through `Task.andThen`/`Cmd.batch` (one branch can be
   pure-write *and* server *and* client at once — `Cmd.batch` is used across 6
   examples), inter-procedural `update` helpers, and **row-polymorphic
   record-update** (`{ m | x = … }` lowers to a reflective result typed `any` —
   `lower.rs`; 19-skyforum's `Update.sky` alone has 17 record-update sites), where
   the write-set becomes unrecoverable → conservative fallback "writes the whole
   Model" → every server branch conflicts with every client branch → the check
   degenerates to "reject everything." Worse, the disjoint-`ui`/`data` rule
   **bans the optimistic update** (a pure branch appending to `data.comments`
   before the server confirms) — the single most important SPA pattern — or forces
   the per-field versioning/merge machinery §4.1 claimed to avoid.

**Consequence for the plan:** Phase 2 (`live.go` surgery) is the *low-risk* part
and is **deferred** — doing it first is motion, not progress. The real next work
is de-risking G1 and G2 for real and restating the thesis honestly (see the
revised [§8](#8-staged-plan)). The **direction** — a client renderer over the
already-renderer-agnostic `Element`, desktop/mobile-first, with an *explicit* (not
auto-derived) server boundary for v1 — is sound and worth building. The **thesis
as first written, and the production-web pillar, are not yet earned.**

## 0.1 The auto-split measurement — FALSIFIED for real apps

The G1 prototype ran (a reproducible classifier, `spike/spa_classify.py`, over
**111 `update` branches across 8 real TEA apps**, 5 with real persistence). The
result kills the *automatic* split as a transparent, no-API-routes mechanism:

- **Ceiling: 47% of branches (52/111) classify cleanly; it does not improve under
  the real-persistent-app projection.** The clean 47% is almost entirely pure-ui
  setters (form fields, nav, toggles); the collapsing 53% *is the entire
  persistence surface* of the apps.
- **The design's own §4 mechanism — "classify by the returned Cmd's effect target"
  — is structurally blind.** Because Sky.Live runs `update` *server-side*, real
  apps do blocking effects **inline in the model expression and return
  `Cmd.none`** (verified: `examples/13-skyshop/src/Main.sky:248,251,295` call
  `refreshProducts` — which reads the DB — then return `Cmd.none`;
  `12-skyvote`/`27-multi-session-chat` do the same). A per-branch classifier keyed
  on the `Cmd` would see **98% "pure"** and ship the DB read path to the client.
  **The effects are not in the AST's `Cmd` at all** — so "the AST already knows
  which update is effectful" does **not** hold for real Sky code.
- **Write-sets are inter-procedural where it matters** — `skyshop` delegates 14
  branches, `skyvote` 12, `job-queue` 6, to `handle*` helpers → whole-model
  write-set → "reject everything."
- **0 of 8 apps have a `{ui, data}`-separated model** — all flat; the disjointness
  the thesis needs is retrofit into every one.
- **Mixed `Cmd.batch` (the case §0 led with) is rare (2 branches)** — the real,
  common killer is effects-hidden-from-the-`Cmd`, not batches.

**Verdict: the auto-split is not "still Sky.Live with smart logic" *as it applies
to today's apps* — but it is not dead either.** What the measurement falsified is
the *weak* mechanism (classify a branch by its returned `Cmd`) on apps written in
the *inline-effect* idiom. A **stronger mechanism survives it** — trace `Task` in
the branch **body** (the effect is visible at the `Task.run` site, even though the
`Cmd` looks pure) — and it becomes clean and sound once the app is written to a
**mandated dialect**: `Model = { ui, data }` **and** effects only via `Cmd`/`Task`
(the Elm discipline, no inline `Task.run` in `update`). That full mechanism — the
body-`Task`-trace, the kernel client/server table, the dialect compile-gates, the
generated RPC, and the honest residuals (optimistic-write reconciliation, authz) —
is specified in **[auto-split.md](auto-split.md)**.

So the accurate framing: **v1 uses an explicit boundary** (author-declared server
calls); **v2 is the dialect + auto-split** of `auto-split.md`, and **v1 apps
written in the dialect are forward-compatible** with it. The auto-RPC is a **v2
target reached by the stronger mechanism**, not struck. Sky.Spa's near-term win is
one language, one type system, one `Element`, and a shared `Codec` across the wire;
its v2 win is the compiler-derived split on top of that.

## 1. Thesis (as originally stated — see §0 for the correction)

> The original headline below is retained for the record; §0 documents why it does
> not hold for real apps as written. A defensible restatement is at the end of this
> section.

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

**Honest restatement (post-grill).** The auto-derived, compile-time-sound split
is a *research goal*, not a v1 feature, and it holds — if ever — only for a narrow
subset (closed-record models, disjoint `ui`/`data`, no optimistic writes to
`data`, no boundary-crossing `Cmd.batch`). For a shippable Sky.Spa the boundary is
**explicit**: the author declares which effects are server calls (as they would in
Sky.Live), the client owns `ui`, and concurrent `data` writes are reconciled with
**per-field versioning** (not "trivially"). The compiler's contribution to
soundness is what it *already* gives — one type system, one `Element`, one shared
`Codec` across the wire — not a new effect-partition oracle. Whether the
auto-split is ever reachable is decided by the G1 feasibility prototype
([§8](#8-staged-plan)), not asserted here.

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

Reordered post-grill: the two blocking unknowns (§0) are de-risked **before** any
`live.go` surgery, because the surgery is the low-risk part and the thesis + web
pillar are what actually gate the direction.

The phase numbers below are the *design* numbering; the built work is tracked as
P1–P6 in [v1-progress.md](v1-progress.md) and the mapping is called out per row.

| Phase | Deliverable | Status |
|---|---|---|
| **1. De-risk (loop)** | wasm feasibility + bundle number + headless renderer/loop proof + arch map | ✅ done ([§9](#9-evidence)) |
| **1b. De-risk G2 (web bundle)** | confirm the reflection-based core cannot TinyGo-compile; **record the web decision** | ✅ **decided — web out of scope for v1.** v1 targets desktop/mobile-embed on standard Go→wasm (real app measured ~9.5 MB raw / ~2.5 MB gzip, `examples/60-spa-todos`). TinyGo (can't compile `reflect.MakeFunc`) / Sky→JS = **v2** ([§9](#9-evidence)) |
| **1c. De-risk G1 (thesis)** | measure whether the auto-derived split is reachable on real apps | ✅ **decided — v1 uses the explicit boundary; auto-split = v2.** The weak classify-by-`Cmd` mechanism was falsified (§0.1); the stronger body-`Task`-trace mechanism under a mandated dialect is reachable and specified in **[auto-split.md](auto-split.md)** |
| **2. Emit path (explicit boundary)** | `Spa.app` config + a `spa` emit target importing a portable core; author-declared server calls (explicit `Http`, shared `Codec`) | ✅ **done** (P1 land + P4 `Std.Spa` boundary — `getJson`/`postJson`) |
| **3. Runtime-partition** | split `live.go` core out; build-tag `rt` for `js`; single-threaded wasm effect interpreter over `cmdT`. Extraction only, no behavior change; gated per CLAUDE.md §0.2.1 | ✅ **done** (P1 — `live_core.go`/`spa_core.go`/`live_wasm.go`; Sky.Live server path byte-identical; P3 `interpretCmd` real effects) |
| **4. Client renderer** | `Element→DOM` renderer reusing `__skyApplyPatches` focus/cursor logic; client-side `diffTrees` | ✅ **done** (P2 — `spaApplyPatches`, focus/caret authority; `Std.Ui` `Element` paints to the DOM, P5) |
| **5. Reconciliation** | per-`data`-field versioning / optimistic-concurrency tokens; a typed `Conflict` variant (concurrent `data` writes are **not** trivially mergeable — G4) | ⏳ **not built** — v1 is explicit-boundary; per-field versioning is a documented future residual ([auto-split.md §8](auto-split.md)) |
| **6. Dialect + auto-split (v2)** | the `{ui,data}` + effects-via-`Cmd` dialect (compile-gated) + the body-`Task`-trace partition + generated RPC — full mechanism in **[auto-split.md](auto-split.md)** | ⏳ **v2 target** |

Phases 2–4 (design numbering) are the bounded, **built** explicit-boundary
Sky.Spa v1 (desktop/mobile first), demonstrated end-to-end by
[`examples/60-spa-todos`](../../examples/60-spa-todos) and tracked as P1–P5 in
[v1-progress.md](v1-progress.md); P6 is the docs/templates sync (this doc set).
Phase 5 (reconciliation) and Phase 6 (auto-split) are **not built** — the
classify-by-`Cmd` mechanism was falsified (§0.1), but the body-`Task`-trace
mechanism under a mandated dialect is reachable (`auto-split.md`), and v1-dialect
apps are forward-compatible with it. Sky.Spa's near-term value is the client
renderer + explicit boundary + shared types; its v2 value is the compiler-derived
split. **None of this is in a release; it is experimental on `exp/spa`.**

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
