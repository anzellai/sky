# Exploration: one `App`, `target × renderer` — unifying Sky.Live / Sky.Spa / Sky.Tui / Sky.Webview

> Status: **exploration / RFC**, not shipped. Written 2026-08-25 against v0.22.1.
> The question: can the app-shape variants become ONE app builder whose target
> and renderer are a *build* choice (`--target`, `--html`/`--wasm`), instead of a
> *code* choice (which module you import)?

## 1. The problem — too many front doors

Today a user picks the shape at the **code** level, at the entry point:

| Shape | Entry | Delivery |
|---|---|---|
| Sky.Live | `Live.app cfg` | server-driven web |
| Sky.Spa | `Spa.app cfg` | client-wasm web (+ desktop/iOS/Android) |
| Sky.Tui | `Tui.app cfg` | terminal |
| Sky.Cli | `Cli.program cfg` | stdin/stdout |
| Sky.Webview | `Webview.app cfg` | desktop |

Five imports, five entry points, five mental models to choose between *before you
write a line*. Switching target means rewriting `main` and re-reading a different
module's docs. New users bounce off the choice ("Live or Spa? what's the
difference?") before they've built anything. The variants read as *five
frameworks*; they are really **one framework with five deployment shapes**.

## 2. The observation — they are already the same program

The configs are near-identical (checked in the stdlib source):

```
-- all five, core fields IDENTICAL:
{ init          : flags -> ( model, Cmd msg )
, update        : msg -> model -> ( model, Cmd msg )
, view          : model -> Element msg      -- Std.Ui Element, renders everywhere
, subscriptions : model -> Sub msg
}
```

Everything else is an **additive, target-relevant** field:

- `routes` + `notFound` — routed targets (web).
- `window` — windowed targets (desktop).
- `onKey` / `onLine` — input for terminal targets.

And `Std.Ui.Element` **already** renders to web (Sky.Live), terminal (Sky.Tui) and
desktop (Sky.Webview) — the pinned cross-platform view layer. So the view is
already target-agnostic. The variants are not different *programming models*; they
are the same `Model / Msg / update / view` with different **runtimes**.

## 3. The three real axes

What actually varies is a 3-tuple, and the five variants are just named points in
it:

1. **Execution location** — where the loop + effects run:
   *server* (Live) · *client-wasm* (Spa) · *local process* (Tui / Cli / Webview).
2. **Render backend** — how `view` becomes pixels/cells/bytes:
   *server-HTML-over-SSE* · *client-wasm-DOM* · *native-webview* · *ANSI* · *text*.
3. **Delivery target** — where it ships:
   *browser · terminal · desktop · iOS · Android · tablet*.

Most of the 3-tuple space is invalid or collapses to one obvious choice, which is
exactly why it should be **defaults + a couple of flags**, not five modules.

## 4. Proposal — ONE extendible `--target`; everything else is derived

Decision (2026-08-25, @anzel): **drop `--html` / `--wasm`.** There is exactly ONE
axis the user chooses — the **target** — a small, extendible, hierarchical enum.
Execution model and renderer are *derived* from the target, so there is no second
flag to get wrong and no invalid combination to reject. One entry point:

```
import Std.App as App

main =
    App.app { init = init, update = update, view = view, subscriptions = subs }
        |> App.withRoutes [ App.route "/" Home ]    -- capability: routing (web)
        |> App.withWindow (App.window "My App")      -- capability: window (desktop)
        |> App.withInput onEvent                     -- capability: input (terminal)
```

```
sky build --target web             # server-driven HTML + SSE   (safe web default)
sky build --target desktop         # native window
sky build --target mobile          # native app, on-device
sky build --target tablet
sky build --target terminal        # TUI
sky run   --target terminal:cli    # text / stdin loop
```

Targets form a **shallow hierarchy** — a family, and an optional platform under it
via `:`:

| `--target` | platform (`family:platform`) | execution model (derived) | = today |
|---|---|---|---|
| `web` | — | server-driven HTML / SSE | Sky.Live |
| `desktop` | `mac` · `windows` · `linux` | native window (webview) | Sky.Webview |
| `tablet` | `ipad` · `android` · `windows` | on-device wasm | Sky.Spa shell |
| `mobile` | `ios` · `android` | on-device wasm | Sky.Spa shell |
| `terminal` | `tui` · `cli` | local process | Sky.Tui / Sky.Cli |

`--target mobile:ios`, `--target desktop:mac`, `--target terminal:cli`. The
**family carries the execution model**; the **platform picks the concrete SDK /
shell**. Adding a platform (`desktop:bsd`, a new mobile OS) is one enum entry —
extendible by construction. Adding a *family* (`watch`, `embedded`) is a new
runtime + one entry; the flag surface never grows.

**Mode is derived, not chosen.** `web` is server-driven; the native families
(`desktop` / `tablet` / `mobile`) run on-device; `terminal` is a local process. The
one case a user might want a *wasm client in a browser* (offline PWA) is
web-delivery + on-device execution — model it as a **platform**, `--target
web:offline` (or `web:pwa`), not a resurrected `--wasm` flag. It stays inside the
single axis.

Because there is exactly ONE axis over a validated hierarchy, **you cannot mix the
wrong flags** — there is no second flag, and `--target web:ios` (a platform that
belongs to a different family) is rejected at parse time with
*"did you mean `mobile:ios`?"*.

## 5. Mandatory per-target config — one capability vocabulary, checked at build

The DX wart @anzel named: `terminal` needs an input handler, `web` needs routes,
`desktop` needs a window — mandatory config that DIFFERS per target, and today is
three different `onKey`/`onLine`/`routes` shapes. The fix is a **capability model**:
builders add capabilities, each target *requires* a set, and the build refuses a
missing one — with a copy-pasteable fix.

Rules that make it consistent + mixing-proof:

- **One `with…` builder per capability, not one per target.** `App.withRoutes`,
  `App.withWindow`, `App.withInput`. There is exactly one name for "routing", one
  for "a window", one for "direct input" — you learn the vocabulary once.
- **Unify the input surface.** Instead of Tui's `onKey`, Cli's `onLine` and web's
  ad-hoc events being three mandatory fields, ONE `App.withInput onEvent` whose
  payload the target interprets (key / line for terminal, DOM event for web). Fewer
  mandatory builders, one name.
- **Targets declare their required capabilities** (`terminal` ⇒ input; `web` ⇒
  routes; `desktop` ⇒ window). `sky build --target terminal` validates the App
  carries the input capability; missing ⇒ a build error that says exactly what to
  add, e.g. `error: target 'terminal' requires an input handler — add \`|>
  App.withInput <fn>\``. Same shape of error for every target, so it's learnable.
- **Optional, strongest guarantee: declare supported targets in the source.**
  `App.app core |> App.targets [ Web, Terminal ]` lets the compiler check the
  mandatory config for BOTH web and terminal at `sky check` time — before any build
  flag exists — and makes `sky build --target mobile` on that app a clear *"this
  app does not declare the `mobile` target"*. The source states what it supports,
  the compiler enforces each declared target's requirements, and the build must
  pick a declared target. That is the "can't mix the wrong flags" guarantee moved
  all the way to compile time.

The four things fall out of this: one axis (no bad combos), a validated hierarchy
(no bad platforms), a capability check (no missing mandatory config), and — with
`App.targets` — all of it verified before you even build.

## 6. The one genuine fork: web-html vs web-wasm (= "split or not")

The hardest question (@anzel): for `web`, html or wasm — split or not? State it
sharply: **"html vs wasm" and "split or not" are the same question — where the
update loop runs — and it forks for *exactly one* target: web.** Everywhere else
the answer is forced, so it is never a user choice:

| target | loop runs | split? | why it's forced |
|---|---|---|---|
| `mobile` / `tablet` / `desktop` | on device (wasm) | **always** | a phone can't run your `Db.query`; effects MUST route to a backend |
| `terminal` | local process | **never** | the process holds the filesystem / DB; nothing to split |
| `web` | **server OR client** | **derived from that** | a browser is the one place both models are legitimate |

So `split` is **derived, never a flag**: server-driven ⇒ no split (the server runs
everything), client ⇒ split (loop on the client, effects to a backend via the
existing `spa-split`). You pick a target; the split follows.

That leaves web as the sole family with a real two-way choice — so give it two
values under the single axis, not two flags:

- **`web` — server-driven (html), NO split.** Loop + effects on the server, thin
  HTML client over SSE. SEO, first paint on a cold link, works with JS disabled.
  **The default**, because "I'm building a website" is the common case and this is
  the safe answer. (= Sky.Live.)
- **`web:app` — client (wasm), split.** Loop in the browser, effects auto-split to
  a stateless backend. Offline, native-latency, installable PWA. The opt-in for
  "I'm building a web *app*". (= Sky.Spa.)

The naming does real work: **`web` = a website, `web:app` = a web app.** Nobody is
surprised that `web:app` is the richer, client-side, installable one, and it keeps
the fork inside the one axis. No `--html`, no `--wasm`, no `--split` — the single
most-confusing pair of flags is gone, replaced by one sub-target on the one target
where the choice is real.

The payoff of auto-split: the **same source** compiles both ways. An app with a
`Db.query` in an `update` branch builds as `web` (that branch runs server-side) or
`web:app` (that branch becomes an RPC) with **no code change** — so a team can
start server-driven for SEO and add an offline `web:app` build later without a
rewrite. "Split or not" stops being an architecture you commit to in code and
becomes a build target you pick per deploy.

## 7. Why terminal doesn't fork like web — split is a *sandbox*, not a backend

Natural question (@anzel): a TUI could call a remote backend too — an AI TUI
hitting a remote API — so shouldn't terminal have a splitting `terminal:app` like
`web:app`? No, and the reason pins down what auto-split actually *is*. Three things
get conflated; only one is a target choice:

1. **Renderer / delivery** — HTML, wasm-DOM, native window, ANSI, line-text. Set by
   the *family*.
2. **Execution location (split or not)** — forced by whether the family runs in a
   *sandbox that forbids direct effects*. Browser wasm has no DB driver and no
   filesystem, so a `Db.query` in your `update` MUST be lifted to a backend — that
   lifting is auto-split. The server and a local process hold the DB driver and the
   filesystem already; nothing to lift.
3. **Calling a remote API** — `Http.post` to some service. ANY app on ANY target
   can do this. A normal effect, not a target choice, not auto-split.

The AI-TUI-calls-a-backend case is **#3, not #2.** The TUI's whole loop runs
locally in the terminal; it makes an HTTP call the same way a web server or a
mobile app does. Nothing is split, because nothing is sandboxed away — a terminal
can already do everything.

**Auto-split is forced by a sandbox, never chosen because "I have a backend."** Only
the browser / native-wasm families are sandboxed, so only they split. That is why
the fork lives on `web` (and is implicit for `mobile` / `tablet` / `desktop`, which
are *always* client-sandboxed) and never on `terminal`. Terminal's variant is a
**renderer**, its one genuine axis:

- `terminal:tui` — full-screen ANSI, live redraw (= Sky.Tui).
- `terminal:cli` — line-based text / stdin (= Sky.Cli).

(A pure one-shot `main = Task.run cmd` isn't a TEA loop and doesn't go through
`App.app` at all — `sky build` handles it directly. The target model is for
interactive apps.)

## 8. Backend-or-not is adaptive across *every* client family — no `web:spa`

Does `web:app` become "just an SPA" when it needs no backend? Do mobile/desktop
apps need a backend at all? **One target per family; whether a backend exists is
derived, not declared** — and it's uniform across `web:app`, `mobile`, `tablet`,
`desktop`. Auto-split inspects the effects your `update` actually uses and routes
each to the *nearest place it can run*:

- **Pure logic** → on the client, always.
- **Device-local effects** (files, local storage, on-device SQLite) → on the
  device via the native bridge. **No backend.** A browser wasm sandbox has almost
  none of these, so a `web:app` splits more; a native mobile/desktop shell has
  filesystem + local DB, so it splits *less* — a client-only native app (note app,
  calculator, offline game) generates **zero backend** and ships as a standalone
  `.app` / `.ipa` / `.apk`.
- **Genuinely remote / shared effects** (a multi-user DB, a secret, server state)
  → lifted to the **minimal** backend, served over typed RPC.

So the generated backend is the minimal set of effects that *cannot* run
client-side — and what "cannot" means widens as the sandbox tightens (browser ≫
native device). If that set is empty, there is no backend at all. `--embed` is the
opposite end (bundle PostgreSQL into a self-contained server) for when you *do*
want one.

Forcing `web:spa` (client-only) vs `web:app` (client+backend) — or the equivalent
for mobile — would make the user commit up front to whether their effects need a
server, which they often don't know and which changes the moment they add a
feature. The compiler decides and says so:
`web:app: no server-side effects — deployable as static files`, or
`mobile:ios: 2 effects run on-device, 1 backend endpoint generated`.

*(Refinement, later: a project that MUST stay backend-free — a CDN static site, a
fully-offline native app — can opt into a guard that fails the build if any effect
would introduce a backend, e.g. `[web] backend = "forbidden"`. An assertion on the
derived result, not a separate target.)*

## 8b. Cross-compilation — nearly free, because Sky → CGO-free Go

Targets map almost 1:1 onto Go's `GOOS`/`GOARCH` matrix, and pure Sky emits
**`CGO_ENABLED=0`** Go, so a static binary for another OS/arch cross-compiles from
any host:

| target | cross-compiles from any host? | caveat |
|---|---|---|
| `web` (server), `terminal:*`, `desktop:*` binary | ✅ static Go binary | — |
| `web:app`, `mobile` / `tablet` wasm frontend | ✅ `GOARCH=wasm` | — |
| `--embed` (bundled PostgreSQL) | ⚠️ needs the *target's* PG bundle fetched | the Sky binary still cross-compiles; the bundle is per-OS |
| `mobile:ios` **signed** `.ipa` | ❌ needs macOS + Xcode | Apple platform reality, not a Sky limit |
| `mobile:android` `.apk` | ✅ mostly (Android SDK) | — |

Honest caveats: a **CGO-based** Go FFI dep (`sky add`) breaks the clean
cross-compile (pure-Go deps are fine), and **iOS signing** inherently needs a Mac.
Otherwise `sky build --target desktop:windows` from a Mac, or `--target
mobile:android` from Linux, Just Works — the Go backend is doing the heavy lifting.

## 8a. The unifying rule — `family[:variant]`

`variant` is **the one irreducible choice a family can't infer for you**:

| family | variant means | values | bare `family` builds |
|---|---|---|---|
| `web` | execution mode | `app` | server-driven (the website) |
| `mobile` | OS | `ios` · `android` | both stores |
| `tablet` | OS | `ipad` · `android` · `windows` | all |
| `desktop` | OS | `mac` · `windows` · `linux` | host OS |
| `terminal` | renderer | `tui` · `cli` | `tui` |

The variant is not the same *category* across families, but it is always "the thing
you must state because it's a genuine product/deploy choice the compiler cannot make
for you." At the point of use there is never ambiguity — `web:` completes to only
`app`, `mobile:` to only `ios`/`android`, `terminal:` to only `tui`/`cli` — and
`web:ios` / `terminal:mac` are rejected at parse time. Extensible (a new OS is one
enum entry; a new family is a new runtime + one entry) and invalid combinations are
impossible by construction, because a variant exists only under its family.

## 9. What each flag does to the *same* source

The value of the unification is that one source compiles every way, and the
runtime differences are mechanical:

- **Effects.** `--html` and local targets (terminal/desktop) run effects **in
  place**. `--wasm` runs the existing **auto-split**: effectful branches → backend
  RPC, pure branches → client. The programmer already writes effects as `Task
  Error a`; the split is a build concern, not a code one.
- **Routing.** Web targets consume `withRoutes`; terminal/desktop ignore them (or
  warn if set), because there is no URL bar.
- **Input.** Terminal targets wire `onKey`/`onLine`; web uses `Ui.onClick` etc.;
  the input surface is target-relevant and validated at build time.
- **Sessions.** `--html` has server sessions (`sky_sid`, the session store);
  `--wasm` is client-local state + the backend. This is a consequence of
  execution location, not a separate knob.

## 7. Outliers and tensions (the honest part)

1. **Cli is the genuine outlier — resolved by an `Element`→text adapter.** The
   wiring survey confirmed Cli/`Tui.program` consume a `String` view with no
   `Element` path, while Tui consumes `Element` directly and Live/Spa/Webview
   consume `Ui.layout [] element` (`Html`). To keep **one** user-facing view type
   (`Element msg`) across every target, `Std.App` renders the `Element` per backend:
   identity → Tui, `Ui.layout []` → the HTML family, and a small **`Element`→text**
   walk → `terminal:cli`. The user always writes `view : model -> Element msg`;
   `Std.Cli.program` (native `String` view) stays as the low-level escape hatch.
2. **Effect-location parity is the main risk.** An app authored + tested under
   `--html` (effects server-side) and then built `--wasm` gets its effects split to
   RPC. Behaviour must be identical across the two. `spa-split` already guarantees
   this and has a gate corpus, but it is the surface where a "compiles every way"
   promise is most likely to leak. Any unification must treat cross-mode parity as
   a first-class, gated invariant.
3. **Invalid combos.** `--target terminal --html` (server-driven terminal) is
   nonsense. The builder must reject invalid `target × mode` combos at build time
   with a one-line "did you mean" — not silently pick something.
4. **One config type with optional fields** risks becoming a grab-bag. The builder
   pattern (`App.app core |> App.withRoutes … |> App.withWindow …`) keeps the core
   minimal and makes each optional field self-documenting + target-checked, rather
   than a wide record with half the fields ignored per target.
5. **One user-facing view type, per-backend adapter inside.** The user writes
   exactly one `view : model -> Element msg`. Internally the backends do NOT all
   consume the same slot (survey): Tui takes the raw `Element`, Live/Spa/Webview
   take `Ui.layout [] element` (`Html`), Cli takes text — so `Std.App` owns the
   adapter per backend (`§ Implementation`). `Std.Html` stays the escape hatch for
   raw markup, not a second app model.

## Namespace — the builder is `Std.App`, not `Sky.App`

The `Sky.*` / `Std.*` split is load-bearing and the new module must land on the
right side of it:

- **`Sky.*` = the language kernel + runtime/platform primitives** — `Sky.Core.*`
  (prelude, `Task`, `List`, the `Http` client…), `Sky.Http.*` (the server,
  middleware, WebSocket), `Sky.Config`, `Sky.Test`. These are imported with the
  `Sky.` prefix in real code.
- **`Std.*` = the batteries standard library built on the kernel** — the five
  app-shape frameworks (`Std.Live` / `Std.Spa` / `Std.Tui` / `Std.Cli` /
  `Std.Webview`), `Std.Ui`, `Std.Db`, `Std.Auth`, `Std.Codec`, `Std.Native`, …
- The unified builder **wraps the five `Std.*` app frameworks**, so it is
  **`Std.App`** (`import Std.App as App`) — consistent with its siblings, *not*
  `Sky.App`. `Sky.*` stays reserved for the kernel.
- **Prose caveat:** "Sky.Live / Sky.Spa / Sky.Tui / Sky.Cli / Sky.Webview" are
  *framework concept* names (as in AGENTS.md's app-shape matrix); the importable
  *module* is `Std.Live` / `Std.Spa` / … There is no `Sky/Live.sky`. When writing
  CODE or naming an import, use `Std.X`; the `Sky.X` spelling is prose only. The
  one genuine `Sky.*` app surface is `Sky.Http.Server` (`import Sky.Http.Server as
  Server`) — a kernel-level HTTP primitive, correctly under `Sky.`.

## 8. Implementation architecture (grounded in the wiring survey)

The survey pinned two constraints the mechanism must respect:

- The five `app`/`program` functions are `Ffi.kernel`-backed and **freely callable
  from pure Sky** (example 38 already calls three of them from one module) — so
  `Std.App` delegates, it does not reimplement.
- A **single binary cannot link all five**: a `Std.Spa` reference triggers the
  auto-split, and a `Webview_app` reference forces cgo for the whole binary. So a
  `--target` build must compile **only the selected backend**.

### The mechanism: a per-target derived entry (generalises `spa-split`)

1. **`Std.App` (pure Sky)** exposes the unified builder plus one runner per
   backend, each applying that backend's view adapter and calling its kernel:
   ```
   App.app { init, update, view : model -> Element msg, subscriptions }
       |> App.withRoutes [...]        -- capability (web)
       |> App.withWindow (...)        -- capability (desktop)
       |> App.withInput onEvent       -- capability (terminal)
   -- runners (internal target → backend):
   App.runLive  : App model msg -> Task Error ()   -- view |> Ui.layout []  → rt.Live_app
   App.runSpa   : App model msg -> Task Error ()   -- view |> Ui.layout []  → rt.Spa_app
   App.runWebview : App model msg -> Task Error () -- view |> Ui.layout []  → rt.Webview_app
   App.runTui   : App model msg -> Task Error ()   -- view (Element direct) → rt.Tui_app
   App.runCli   : App model msg -> Task Error ()   -- view |> Ui.toText     → rt.Cli_program
   ```
2. **The build resolves `--target` → a runner**, then (for a `Std.App` entry,
   detected by an `import Std.App` scan like `is_spa_app_entry`) generates a tiny
   derived entry `main = App.run<Backend> userApp` and builds THAT — so only the
   selected backend is referenced, dodging the Spa/Webview link conflict:

   | `--target` | runner | build path (all already exist) |
   |---|---|---|
   | `web` | `runLive` | plain `go build` (server) |
   | `web:app` · `mobile:*` · `tablet:*` | `runSpa` | `spa_split_and_build` (client) |
   | `desktop[:os]` | `runWebview` | cgo `go build` (or `runSpa` native shell) |
   | `terminal` · `terminal:tui` | `runTui` | plain `go build` |
   | `terminal:cli` | `runCli` | plain `go build` |

   This is exactly where the **`web` = server / `web:app` = client** flip lives:
   two different runners off the one target axis. `terminal` is now a first-class
   build target (net-new — today Tui/Cli build with no `--target`).

### Migration — strictly non-breaking, additive only

- `Std.App` is **new**; nothing is deleted or rewritten. `Std.Live` / `Std.Spa` /
  `Std.Tui` / `Std.Cli` / `Std.Webview` and their `app`/`program` functions stay
  exactly as they are — `Std.App` *calls* them. A user who writes `Live.app cfg`
  today is unaffected; a user who wants one-source-many-targets writes `App.app`.
- `sky init` can scaffold `App.app`; `AGENTS.md`'s five-row matrix gains a unified
  row. The named modules remain the "I know exactly which shape I want" form.

### Implementation status (branch `feat/unified-app-builder`)

- **Phase 1 — the `--target family[:variant]` model** — SHIPPED. `target::Target`
  with parse + did-you-mean + legacy compat; mix-safety by construction.
- **Phase 2a — `Std.App` (module + 5 runners + view adapters)** — SHIPPED. One
  `App` value feeds all five runners (proven); runLive serves 200, runCli renders.
- **Phase 2b — build/run `--target` dispatch (direct targets)** — SHIPPED for
  `web` · `terminal:tui` · `terminal:cli` · `desktop`. Derived-entry + DCE prune,
  proven. `sky check` verifies all backends.
- **Client targets (`web:app` / `mobile:*` / `tablet:*`)** — BOUNDARY: they use a
  **Sky.Spa** entry. The auto-splitter partitions the `update` structurally and
  cannot see it through the `App.app` indirection (verified: "no resolvable `case
  msg of`"); wiring them into `Std.App` needs `spa_partition` to trace through the
  builder — a larger change to the proven split path, deliberately not attempted
  here. The `--target` grammar is shared across `Std.App` and `Std.Spa` entries.
- **Phase 3 — build-time capability validation + `App.targets`** — PARTIAL: the
  `web`-requires-`notFound` contract is enforced (runLive) and `sky check` proves
  every backend type-checks; a dedicated per-target capability gate + `App.targets`
  remains future.

### Delivery slices (each its own commit + Judge boundary)

- **2a** — `Std/App.sky`: config + builder + capability builders + view adapters +
  the five `run<Backend>` runners (+ `Ui.toText`). Proven by a hand-written
  `main = App.runTui app` / `runLive` / `runCli` building & running per backend.
  Pure Sky; no build changes; lowest risk.
- **2b** — build `--target` dispatch: detect a `Std.App` entry, generate the
  per-target derived entry, route to the right existing build path. Reuses
  `spa-split`'s derived-project pattern.
- **3** — capability validation: each target declares its required capabilities;
  the build errors with a copy-pasteable fix when one is missing; optional
  `App.targets [...]` enforces at `sky check`.
- **4** — docs + templates + `sky doc` + AGENTS.md + sky-lang, and `sky init`.

## 9. Recommendation

**Do it — incrementally, `Std.App`-first, reusing the kernels + `spa-split`.** The
hard pieces exist: the `Element` view renders everywhere (with a per-backend
adapter), the five `app` kernels are callable from Sky, and `spa-split` already
solves client-mode effect location. The net-new work is bounded: one `Std.App`
module, the `Element`→text adapter, and the per-target derived-entry build
dispatch.

Suggested first slice, provable on its own: **unify the config** — one `App.app`
taking the shared core + optional builders, with `Std.Live`/`Std.Spa` reimplemented
as thin aliases over it and no new build flags yet. That collapses the mental model
("write an App, not a Live-app-or-a-Spa-app") without touching the runtime, and
each subsequent slice (the `--target`/`--mode` resolution, the terminal fold) lands
behind it. The `if it compiles, it works` promise extends naturally: *if it
compiles, it runs on every target you build it for.*

## Open questions for @anzel

- Is `--html`/`--wasm` the right surface, or should it be explicit `--mode
  server|client` (clearer semantics) with `--html`/`--wasm` as aliases?
- Keep Cli separate, or fold it into a terminal target as the `text` renderer?
- Should bare `sky build` default to server-driven web (safe, SEO) or refuse and
  make the target explicit? (I lean: default to server web — the safe, most-common
  choice — and let `--target` opt into the rest.)
- Is there appetite to make **cross-mode effect parity** a gated invariant (an app
  built `--html` and `--wasm` must observably agree), given it's the main risk?
