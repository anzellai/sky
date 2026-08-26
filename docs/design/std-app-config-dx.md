# Design deep-dive: `Std.App` config syntax + DX (the sole-front-door model)

> **Status: DESIGN.** Works out the *optimal* config syntax for `Std.App` once it
> becomes the SOLE public front door and `Sky.Live` / `Sky.Tui` / `Sky.Cli` /
> `Sky.Webview` / `Sky.Spa` are deprecated into internal plumbing. Companion to
> `docs/design/std-app-config-architecture.md` (the precedence/env/secret model)
> and `docs/design/unified-app-builder.md` (the builder itself). Locks the shape
> the example migrations target. @anzel, 2026-08-26.

## 0. Requirements (from the design conversation)

1. **Flag-consistent.** The config a user writes maps 1:1 to the `--target`
   flag family, so seeing `--target desktop:mac` tells you to write
   `DesktopConfig`. No mapping to memorise.
2. **No `withX` soup.** Not a flat pile of `App.withPort |> App.withHead |> …`.
3. **`withX` preserved.** The granular per-item override stays available; we add
   an easier home for it, we don't remove it.
4. **Cross-platform base.** One write of the config shared by *every* target
   (`BaseConfig`), with per-target overlays on top — a cross-platform build must
   not repeat shared settings per variant.
5. **Extensible in three axes** without breaking user code: add a *knob* to a
   family, add a *target family*, add a *backend* under a family.
6. **Deprecation-complete.** `Std.App` must subsume 100% of the per-shape
   modules' public config surface — deprecating them loses nothing.
7. **App-owned.** Config types are `App.*`, never re-exports of `Live.*` — you
   cannot reference a module you're deprecating.

## 1. Four concerns, deliberately separated

The config problem is not one thing. It splits into four, and each has a
*different* optimal shape — conflating them is what produces soup.

| # | Concern | Carries | Optimal shape | Why |
|---|---|---|---|---|
| 1 | **Logic** — `init/update/view/subscriptions` | `model`/`msg` type params | a record arg to `App.app`/`App.web` | it's the app; type-param-carrying |
| 2 | **Structure** — routes, notFound | `page` param + the *fallback phantom* | **builders** (`withRoutes`, `withNotFound`) | must flip a phantom type param (web-requires-fallback) — a record can't |
| 3 | **Shared config** — log, telemetry, db, env | nothing (plain data) | **`BaseConfig` record-update** | one write, all targets; data-only |
| 4 | **Per-target config** — port/head/window/onKey | nothing (plain data) | **`Config` ADT + record-update** | data-only, flag-named variants |

**The principle that decides builder-vs-record: builders where the TYPE changes
(a phantom flip or a type param), record-update where only DATA changes.** That
is the whole rule, and it explains why `withNotFound` is a builder while
`WebConfig {…}` is a record.

## 2. The syntax decision — record-update from exposed defaults

For concerns 3 and 4 (plain-data config), three candidate syntaxes were weighed:

**(A) Record-update from an exposed default** — *chosen*:
```elm
|> App.withConfig (WebConfig { webDefaults | port = 8080, guard = Just requireAuth })
```
**(B) Per-family builder pipeline:**
```elm
|> App.withConfig (App.web |> App.webPort 8080 |> App.webGuard requireAuth)
```
**(C) Polymorphic cross-family setters:**
```elm
|> App.withPort 8080 |> App.withGuard requireAuth      -- one setter set, all families
```

### Why (A) wins

| Axis | (A) record-update | (B) builders | (C) poly setters |
|---|---|---|---|
| **API surface** | 1 default + 1 ctor per family | **N setters × M families** (soup) | N setters, but ill-typed cross-family |
| **Add a knob** | add field + default — **existing code unaffected** | new public setter fn | new setter fn |
| **Discoverability** | `sky doc` lists fields; LSP completes `{ d │ █ }` | each setter documented | each setter documented |
| **Wrong-family knob** | impossible (field only on that Opts) | impossible | `withPort` on `TerminalConfig` = nonsense |
| **Matches user ask** | **yes** (`WebConfig {…}`) | no | no |
| **Phantom flips** | n/a (data only) | possible | possible |

(A)'s decisive advantages: **the smallest API surface** (one default record + one
ADT ctor per family — *not* one function per knob), and **non-breaking growth** —
because a config is built by updating from `webDefaults`, adding a new optional
field to `WebOpts` is invisible to every existing call site (they inherit the new
default automatically). For a surface that will *grow* as `App` absorbs
`Live`/`Tui`/`Spa`, that extensibility is the requirement, not a nicety.

### Empirically verified (not assumed)

The pattern — an ADT ctor wrapping a record-update from a default — type-checks
**and** `go build`s with **zero call-site annotations** under Sky's HM inference
(probe run 2026-08-26, `sky check` green):
```elm
type Config = WebConfig WebOpts | TerminalConfig { onKey : Maybe String }
webDefaults : WebOpts
webDefaults = { port = 8080, guard = Nothing, head = Nothing }

myWeb = WebConfig { webDefaults | port = 9000, guard = Just "requireAuth" }   -- infers as Config
```
The default record is monomorphic, so `{ webDefaults | port = 9000 }` is
unambiguously `WebOpts` and the ctor fixes the `Config` — no annotation needed,
even at a bare `let`.

### Why not builders here (the v0.19 lesson, correctly scoped)

v0.19 moved the *app config* from a record to a builder
(`Live.config {…} |> Live.withHead …`) — but that was for concern **2**
(structure + the fallback phantom), where a builder is genuinely required to flip
the type. It does **not** argue for builders on concerns 3/4 (plain data), where
record-update from a default is strictly less surface with better growth. We keep
builders exactly where the type changes, and nowhere else.

### The scalar/function split — forced by BOTH the type principle AND a codegen floor

The per-shape survey (§6) sorts every knob into two shapes, and two independent
facts put the boundary in the SAME place:

- **Scalar / ADT knobs** (`port`, `ttl`, `maxBodyBytes`, `static`, `csrf`,
  `liveBroker`, `canvasWidth`, `title`/`size`, `withLog`'s format+level,
  `Database`/`Sessions`/`JobStore`/`Telemetry` ADTs, the telemetry windows) —
  plain data → **record-update fields**.
- **Function-valued knobs** (`withGuard : msg -> model -> Result …`,
  `withHead : model -> List Html`, `withOnKey : key -> msg`,
  `withOnLine : String -> msg`, `withOnNavigate : page -> msg`,
  `withConsoleAuth`, `withAnalyticsIdentify`, `withAuthSliding` (holds a fn),
  `withRevocation`) → **stay builders**.

Why the function knobs stay builders — two reasons, either sufficient:

1. **Type principle:** they thread `model`/`msg`/`page`, so a builder on the
   `App`-value (which already carries those params) is the natural home; a plain
   record field would pin the type vars early and muddy inference.
2. **A codegen edge (root-caused 2026-08-26):** a *bare constructor* wrapped by a
   builtin container ctor — `Just Goto` / `Ok Goto` where the slot is
   `Maybe (String -> Msg)` and `Msg` is concrete — **type-checks but fails
   `go build`** today (the ctor is emitted erased as `func(any) any`, but
   `rt.Just[func(string) Msg]` wants the concrete signature). It is *narrow*
   (`Just up` with a named fn, lambdas, and list literals all work) and *latent*
   (no shipped example hits it). The builder design **sidesteps it entirely**:
   builders store the handler as a *bound polymorphic parameter* (`Just fn` with
   `fn : String -> msg`, `msg` erased to `any`), which is exactly why
   `Std/App.sky`'s `onInput` and `Std/Live.sky`'s `revokedCheck` fields compile
   today. So this is not a blocker for the config design — but it IS a real
   soundness hole for user code like `onInput = Just SetName`, so it's fixed on a
   no-deferral track (regression-test-first; `codegen_maybe_of_function_erasure`).
   Even were it fixed, the ordering/validation/overlap knobs below still want
   builder semantics.

**Builders that stay builders for semantics, not just type** (survey §"HARD"):
`withRoutes`/`route`/`routeInt`/`api` (declaration ORDER is significant — literals
before `:param`), `withAuthSliding` (owns the `sameSite` setter to prevent
attribute drift; interacts with `Auth.signSlidingToken`), `withRevocation` (an
enabling signal with an ordering relation to `bindSessionUser`), and the
`Sessions`/`withStore` **overlap** (the more-specific per-target value beats the
base value — cross-builder precedence, not a naive field merge).

## 3. The type architecture

```elm
module Std.App exposing
    ( App, Config(..), BaseConfig
    , app, web                       -- view constructors (Std.Ui / Std.Html)
    , withRoutes, withNotFound        -- structure (phantom-carrying builders)
    , withBase, withConfig            -- config (record-update carriers)
    , run
    , baseDefaults                    -- the exposed defaults you record-update
    , webDefaults, desktopDefaults, tabletDefaults, mobileDefaults, terminalDefaults
    , … Opts type aliases …
    )

type alias BaseConfig = { log : LogLevel, telemetry : TelemetryConfig, database : Maybe DbConfig, env : EnvMode }

type Config
    = WebConfig      WebOpts        -- --target web[:app]
    | DesktopConfig  DesktopOpts     -- --target desktop[:mac|windows|linux]
    | TabletConfig   TabletOpts      -- --target tablet[:ipad|android]
    | MobileConfig   MobileOpts       -- --target mobile:ios|android
    | TerminalConfig TerminalOpts     -- --target terminal:tui|cli

withBase   : BaseConfig -> App f s p m g -> App f s p m g
withConfig : Config     -> App f s p m g -> App f s p m g   -- call once per customised target
```

**Flag family = Config variant name** (requirement 1). The `:variant` (native
platform / renderer) only selects the internal backend + which fields apply; the
config you reach for is the family. See the ↔ table in
`std-app-config-architecture.md` §… (Config↔flag).

## 4. Precedence + merge

One resolution, central at `App.run` (boot):

```
SKY_* env  >  withConfig (per-target)  >  withBase  >  built-in default
```

- A field present in both `BaseConfig` and a per-target Opts (e.g. `database`):
  the per-target value wins *for that target* (it's the more specific overlay).
- `SKY_*` env always overrides (the deploy layer), per
  `std-app-config-architecture.md` §2.
- Missing config → the exposed default. The zero-config app
  (`App.app {…} |> App.run`) is `baseDefaults` + the target's `*Defaults`.

## 5. Extensibility — the three axes, all non-breaking

1. **New knob on a family** — add a field to `WebOpts` + a default value in
   `webDefaults`. Every existing `WebConfig { webDefaults | … }` inherits it;
   **no call site changes.**
2. **New target family** (e.g. `watch`) — add a `WatchConfig WatchOpts` variant +
   `watchDefaults`, a `--target watch` in `target.rs`, and a `run` arm. Existing
   code never matched the variant → **non-breaking.**
3. **New backend under a family** (e.g. `web:pwa`) — internal: the build maps the
   new flag variant to a runtime; config only changes if the backend needs new
   knobs, which is axis 1 (non-breaking).

The `Config` ADT is *closed* (backends are a compiler-provided set), so
extensibility is the Sky team adding variants/fields — always additive for users.

## 6. Deprecation mapping — `Std.App` subsumes the per-shape surface

Every public knob of `Sky.Config` + `Std.Live`/`Tui`/`Cli`/`Webview`/`Spa` has a
home. **Record-field** = a scalar/ADT field on `BaseConfig` or a `*Opts` record;
**builder** = a type/fn/ordering-carrying `App.withX`.

### BaseConfig (shared by all targets) — from `Sky.Config`
| source knob | App home | shape |
|---|---|---|
| `withLog LogFormat LogLevel` | `BaseConfig.logFormat`, `.logLevel` | record (2 fields) |
| `withDatabase Database` | `BaseConfig.database : Maybe Database` | record (ADT) |
| `withJobs JobStore` | `BaseConfig.jobs : Maybe JobStore` | record (ADT) |
| `withTelemetry Telemetry` | `BaseConfig.telemetry : Maybe Telemetry` | record (ADT) |
| `withTelemetryAggregationWindow`/`HistogramWindow` | `BaseConfig.telemetryAggWindow`/`HistWindow` | record (Int) |
| `withTelemetryDbCapacity Capacity` | `BaseConfig.telemetryDbCapacity` | record (ADT) |
| `withTelemetrySynchronousCommit Bool` | `BaseConfig.telemetrySyncCommit` | record (Bool) |
| *(env mode)* | `BaseConfig.env : EnvMode` | record |

### WebConfig (`--target web[:app]`, tablet Live) — from `Std.Live` + web-ish `Sky.Config`
Record fields: `port`, `ttl`, `idleEvict`, `maxBodyBytes`, `static`, `staticUrl`,
`inputMode` (← `Live.withInput`, **renamed** to end the clash with the terminal
line handler), `csrf` (← `Config.withCsrf`), `liveBroker` (← `Config.withLiveBroker`),
`sessions : Maybe Sessions` (← `Config.withSessions` ⊕ `Live.withStore`/`withStorePath`,
overlap resolved: this per-target value beats `BaseConfig`), `status` (← `withStatus`
`{reconnecting,offline}`), `analytics` (← `withAnalytics` `{pageViews}`).
Builders: `withHead`, `withGuard`, `withOnNavigate`, `withConsoleAuth`,
`withAnalyticsIdentify`, `withAuthSliding`, `withRevocation` (all fn/ordering).

### DesktopConfig (`--target desktop[:os]`) — from `Std.Webview` (+ Live when bare)
Record fields: `title`, `width`, `height` (← `Webview.WindowCfg` / `withTitle` /
`withSize`). Bare `desktop` = Live-in-window, so it *also* accepts the WebConfig
knobs for the served app (composition, not duplication — see §note).

### TerminalConfig (`--target terminal:tui|cli`) — from `Std.Tui` + `Std.Cli`
Record fields: `canvasWidth`, `canvasHeight` (← Tui). Builders: `withOnKey` (Tui
fn), `withOnLine` (Cli fn), `withGuard` (shared).

### MobileConfig (`--target mobile:ios|android`)
No stdlib surface exists yet (survey: no signing/bundle module). `MobileOpts` is
seeded minimal (bundle id / icon / permissions) as future work — a placeholder
that the flag already routes to.

### Non-config public surface (constructors / runtime helpers) → App homes
`route`/`routeInt`/`api`/`lifecycle` → `App.route`/`App.routeInt`/`App.api`/`App.lifecycle`;
`bindSessionUser`, `readPassword`, `getJson`/`postJson` → re-exported as `App.*`
runtime helpers. These are not config; they must survive deprecation as App
functions.

**Internalisation:** the per-shape modules keep their runtime code but their
public `module … exposing` shrinks to kernel-internal, hand-curated in
`rust/crates/project/src/kernel_api.rs` (the `kernel_api_covers_registered_kernel_functions`
gate guards drift). Users import only `Std.App`.

## 7. What the user writes (before → after)

**A Live example (19-skyforum-style, single-page Std.Ui):**
```elm
-- before
main = Live.app (Live.config { init = init, update = update, view = view, subscriptions = subs, routes = [], notFound = () })
-- after
app = App.app { init = init, update = update, view = view, subscriptions = subs }
main = App.run app                                   -- zero config; port/store from defaults + env
```

**A Live example needing port + head + guard (37/38-style):**
```elm
app =
    App.app { init, update, view, subscriptions }
        |> App.withRoutes routes
        |> App.withNotFound HomePage
        |> App.withConfig (WebConfig { webDefaults | port = 8005, inputMode = "debounce" })
        |> App.withHead Head.headFor            -- fn → builder (name implies web/desktop target)
        |> App.withGuard requireAuth            -- fn → builder (shared; DCE per target)
main = App.run app
```

**The multi-backend example (38, one source → Tui / Webview / Live via `--target`):**
```elm
app =
    App.app { init, update, view, subscriptions }
        |> App.withRoutes routes
        |> App.withNotFound HomePage
        |> App.withBase   { baseDefaults | logLevel = Info }
        |> App.withConfig (WebConfig     { webDefaults     | port = 8006 })
        |> App.withHead   Head.headFor                      -- web fn knob
        |> App.withConfig (TerminalConfig { terminalDefaults | canvasWidth = 100 })
        |> App.withOnKey  KeyPressed                         -- terminal fn knob
        |> App.withConfig (DesktopConfig { desktopDefaults | title = "Multi", width = 900, height = 600 })
main = App.run app
-- sky build … --target terminal:tui   → only TerminalConfig + withOnKey survive DCE
```

The argv-branching those examples do today (`case argv of "--tui" -> Tui.app …`)
collapses into ONE `App.app` value + a build-time `--target`. That is the unified
builder's headline win, now with a config surface to match.
