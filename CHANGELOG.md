# Changelog

Notable user-visible changes. Keep this file additive — never rewrite history.

## v0.17.0 — typed-emit soundness floor (2026-06-28)

Release plan: [`docs/v0.17/release-plan.md`](docs/v0.17/release-plan.md).
Judge re-verdict: REFRAMED 100% ACHIEVED + VERIFIED.

### Compiler

- **Typed-emit wrap-target gate.** `resolveWrapParams` +
  `resolveWrapParamsCtx` now gate HM-override on enclosing-scope T-var
  presence. Closes the wrong-typed wrap class (8 go-build errors on
  `examples/00-standard-libs` → 0; 131/131 runtime). Symbol-level
  diagnosis at `docs/v0.17/session-2026-06-28-diagnosis.md`.

- **rt.Coerce residual surface — documented sound.** All Coerce-family
  sites on the canonical `examples/26-ui-showcase` benchmark enumerated
  across 8 safety classes with explicit soundness proofs at
  `docs/v0.17/rt-coerce-residual-surface.md`. Zero "unknown / unsafe"
  remainders. Closes the rock-solid soundness claim under the reframed
  v0.17.0 goal.

- **`scopeStateRef` IORef contract + audit spec.** Per CLAUDE.md §0.3
  criterion #3 locked wording. Compile.hs:496-595 documents the
  bracket-scoped (Class A) + monotonic-accumulating (Class B) write
  semantics; `Sky.Build.ScopeStateRefAuditSpec` machine-verifies the
  writer counts (25 + 17) + the layering invariant. Pattern mirrors
  `Sky.Build.AnonRecordWriterAuditSpec`.

- **Per-panic-class emission-time regression locks.**
  `Sky.Build.PanicClassGateSpec` adds the emission leg of the
  three-leg soundness stool (runtime classification at
  `runtime-go/rt/panic_recover_test.go` + example sweep / verify-cli /
  WellTypedFuzzer real-world leg + this emission-time leg). 11 tests
  covering C1-C7 panic classes.

### Limitations closed in v0.17

See [`docs/KNOWN_LIMITATIONS.md`](docs/KNOWN_LIMITATIONS.md) for the
full current-state catalog. Items closed since v0.16.x:

- Negative literal arguments (`f -1` parses as `f (-1)`)
- Multi-line function signatures (both `: T` and `-> T` continuation)
- Zero-arg call shape arity gate (`[E2007]` StrictHmArityGate)
- `Css.*` keyword constants are bare values (`Css.zero`)
- `Dict.toList` typed-key inference works inline AND let-bound
- `sky check` validates Go interface satisfaction empirically
- All list ops on constant Go stack (CPS / accumulator rewrites)
- 3-tuple literals at top-level
- Sky.Live `init` receives full `Request`
- URL-driven route matches fire `Navigate` Msg

### Docs

- **Cleanup pass.** 22 v0.17 design notes + 2 v0.16.13 handoffs moved
  to `docs/archive/`. 39 MB of build artifacts under
  `docs/v0.16.x-console/parametric-cfg-repro/sky-out/` deleted.
- **`docs/session-protocol.md` folded into `CLAUDE.md` §0.4** as a
  durable Session Methodology section (phase pattern, agent +
  grilling, three-leg soundness stool, N-strikes circuit-breaker,
  reframed-vs-literal goal handling, push discipline, context
  discipline).
- **`docs/KNOWN_LIMITATIONS.md` refreshed** to v0.17.0 state.

## v0.15.3 — typed let-binding RHS + sibling-helper call sites (2026-05-25)

### Codegen

- **Closed `interface conversion: Cfg_R[Msg] vs Cfg_R[any]` panic
  in the REVERSE direction from v0.15.2.** Surface: a library
  module (sky-editor's `Editor.view`) defines `view : Cfg msg ->
  Element msg` whose body forwards `cfg` to sibling polymorphic
  helpers (`editorBody cfg`, `toolbar cfg onCheck`, `diagnostics
  cfg onDismissCheck`). v0.15.2 closed the *literal*-into-typed-
  slot direction (`Cfg_R[any]{...}` → `Cfg_R[Msg]`); v0.15.3
  closes the *typed-source*-into-erased-slot direction
  (`Cfg_R[Msg]` arg → `Cfg_R[any]` callee param at the sibling
  call site, plus `Cfg_R[T1]` arg → `Cfg_R[any]` sibling call
  inside the generic body itself).
  - **Symptom in production:** clicking the Source tab in the
    skydeploy file editor panicked at every render with
    `Cfg_R[State_Msg] vs Cfg_R[any]`.
  - **Fix mechanism (4 surgical changes, all in `Sky.Build.Compile`):**
    1. `letBindingType` — types the RHS of a zero-param let-
       binding from the source region or HM solver, gated on
       `canRouteTyped` (only record literals, lambdas, and
       control flow get typed routing — Can.Call/Access pass
       through untyped so FFI return wrappers like `rt.AsListT`
       don't strip Result-Ok wrappers).
    2. `Can.Access` typed-field-access path — now also fires
       when `inferExprType` returns an ambig TVar but
       `lookupLambdaType` carries the concrete `TAlias`
       (function param via `withScopedLambdaTypes` from the
       dep-emission registration). Includes a secondary check
       via `lookupLambdaGoStr` to catch the lazy-rendering race
       where the Go-string registry is active but the Sky-type
       registry isn't yet populated.
    3. `coerceArg` — short-circuits `any(arg).(Foo_R[any])`
       nominal cast when source's static Go type is the SAME
       parametric record alias base. Lets Go's call-site type
       inference pin the callee's T from the source's
       instantiation, which is the only correct behaviour
       across Go's nominal generic typing.
    4. Param registration in dep-emission (`goStringBindings` +
       `inferredArgTys`) now includes parametric record alias
       params, not just func-typed ones — so the call-arg short-
       circuit has the info to fire.
  - **Regression test:** `test-files/v0.15-stress/src/Widget/
    Form.sky` is a synthetic library mirroring sky-editor's
    `Editor.sky` shape (top-level polymorphic `view cfg`,
    sibling helpers, mixed `_ -> msg` + bare `msg` fields,
    Std.Ui body, let-extracted polymorphic fields). The L1-L7
    assertion in `examples/00-standard-libs`-style `Main.sky`
    fails on v0.15.2, passes on v0.15.3.

### `defToStmts` zero-param let-binding

- `Can.Def name [] body` now consults the same `letBindingType`
  helper before lowering, so `main`'s top-level let-bindings of
  record literals emit as `Setup_R[Msg]{...}` instead of the
  type-erased `Setup_R[any]{...}` shape that propagated the
  panic at downstream call sites.

### Known gap (documented in regression test)

- Passing a let-bound func-typed field-access (`let submit =
  cfg.wfSubmit in submitProbe cfg submit`) to a SAME-MODULE
  generic helper still emits `rt.Coerce[func(P) any](submit)`,
  which fails Go's call-site inference against the callee's
  `func(P) T1` slot. Workaround in user code: pass the field
  directly (`submitProbe cfg cfg.wfSubmit`). Sky-editor's
  actual code does NOT hit this — it passes such fields to
  Std.Ui kernels (`Ui.onSubmit cfg.onSubmit`) where the kernel's
  reflect-adapter handles the conversion. The synthetic
  `submitProbe` case is commented out in the regression test
  with a forward-looking note for the next iteration.

### Verification gates (all green pre-merge)

- Cabal test: 306 examples, 0 failures, 1 pending (matches v0.15.2).
- 27/27 examples build clean from wiped slate.
- `examples/00-standard-libs` stdlib smoke test: 120/120 assertions pass.
- `sky check` clean on `examples/{12-skyvote, 13-skyshop,
  19-skyforum, 26-ui-showcase, 00-standard-libs}` + synthetic
  stress test + skydeploy control plane.
- `scripts/verify-cli.sh`: 13 pass / 0 fail / 1 skip.
- `scripts/verify-all-web.sh`: 10 pass / 0 fail + console-e2e green.
- `scripts/lsp-test-nvim.sh`: 17/17 LSP requests pass (hover,
  completion, goto-def across kernel calls, field access, let-
  bindings, lambda params, case patterns).
- Skydeploy control plane: generated Go for `Editor_view` /
  `Editor_view__Msg_...` no longer emits the panic-causing
  `any(cfg).(Editor_Cfg_R[any])` cast at sibling helper calls.


## v0.15.2 — Cfg_R[any] panic fix + version propagation (2026-05-24)

### Codegen

- **Closed `interface conversion: Cfg_R[any] vs Cfg_R[Msg]` runtime
  panic** at every place a `Can.Record` literal sits in a typed
  call-arg slot whose Go target is a parametric record alias
  instantiation. Surfaced by skydeploy's Editor (`Editor.view
  editorCfg` at AppDetail.sky:Source tab) on every mount — Go
  generic types are nominal, so `any(Cfg_R[any]{...}).(Cfg_R[Msg])`
  fails at runtime even though Go's type checker accepts it.
  - **Fix:** call-arg lowering at every site (`zipWithDefault
    coerceArg exprToGo`, `coerceCallArgsAt`'s `coerceOne`,
    `kernelCoerceArg`, bare ctor-call zip) now routes
    `Can.Record` literals targeting parametric record slots
    through `exprToGoExpectGo` → `lowerRecordLiteralTo`, which
    emits the literal with the target's concrete type args
    directly (no nominal-type-assert wrapper).
  - **Symmetry:** the same pipeline also routes `Can.Lambda` at
    typed `func(...) ...` slots through `lowerTypedLambda` (was
    already happening at some call sites; now uniform across all
    five).
  - **Edge cases handled:** the new arms are uniformly gated on
    `not (containsGenericTypeParam ty)` so call sites where σ
    hasn't pinned the callee's TVar (`Cfg_R[T1]`) fall back to
    the legacy `coerceArg` path — emitting `Cfg_R[T1]{...}` at
    the caller would trigger `undefined: T1` since T1 names the
    callee's type variable, not in scope here. The existing
    `exprToGoExpectGo` arms (record-field-init, list-elem) are
    unchanged because they're only reached from contexts where σ
    is already concrete.
  - Stage E shipped the parametric record alias struct generation
    + Stage E.2 routed the record-field-init context; v0.15.2 closes
    the call-arg context that Stage E missed.

### `sky build`

- **`sky build` now injects `-ldflags "-X sky-app/rt.skyVersion=<compiler version>"`** into the underlying `go build`. Every Sky-built app's `/_sky/buildinfo` now reports the actual Sky version that built it instead of the default `"dev"`. No deploy-script ceremony — a tagged Sky binary built with `cabal install -ldflags="-X main.skyBuildVersion=0.15.2"` propagates that string to every app it compiles.
  - **Why:** pre-v0.15.2, the `rt.skyVersion` package-level var defaulted to `"dev"` and was only populated by the Sky compiler's own release CI (`-X main.skyBuildVersion=...`). The compiler's own version never reached the apps it built — every deployed Sky app reported `"skyVersion":"dev"` regardless of which tagged compiler had built it.
  - **Migration:** none. Existing apps rebuild → buildinfo flips from `"dev"` to the real version on next `sky build`. Deploy scripts that previously injected the ldflag manually (none in the public examples) can remove that step.

## v0.15.1 — Docs: `SKY_ADMIN_TOKEN` canonical (2026-05-24)

- **Docs: `SKY_ADMIN_TOKEN` is the canonical env var** for gating
  `/_sky/metrics` and `/_sky/console` in production. The v0.15.0 doc
  refresh accidentally kept `SKY_METRICS_TOKEN` (a v0.14.21 legacy
  alias) as the recommended name in `README.md` + `CLAUDE.md` +
  `templates/CLAUDE.md`. Runtime behaviour unchanged — both
  `SKY_METRICS_TOKEN` (v0.14.21) and `SKY_CONSOLE_TOKEN_SECRET`
  (v0.14.20) are still honoured by `adminTokenSecret()` in
  `runtime-go/rt/subapp.go`.

## v0.15.0 — Type-directed lowering (2026-05-24)

### Type system

- **Type-directed lowering throughout.** Sub-expressions at lambda
  bodies, record-field inits, list elements, and call args lower with
  the slot's typed Go form propagated. The solver writes a per-region
  type map (`globalRegionTypes`); `LowerCtx` threads the expected
  type down through `exprToGoExpectGo`. Closes the long-standing
  parametric-record-alias bug class (every Surface 1/2/3 is now
  shipped). Architecture: [`docs/v1-rfc/type-soundness-deep-analysis.md`](docs/v1-rfc/type-soundness-deep-analysis.md).
- **Go generics on parametric record aliases.** `type alias Cfg msg
  = { onSubmit : msg, label : String, ... }` now emits
  `type Cfg_R[T1 any] struct { OnSubmit T1; Label string; ... }`
  with per-instance type args (`Cfg_R[Msg]`, `Cfg_R[Int]`). Callback
  fields keep their typed callee parameter — no more `func(any) any`
  fallback at parametric-record slots.
- **Inline lambdas keep their typed shape at record-field slots.**
  `{ onSubmit = \s -> Tag ("L:" ++ s), ... }` against `Cfg Msg` now
  emits `func(string) Msg` for the lambda, not `func(any) any`.
- **Cross-alias call without the alias-chain workaround.** Structurally-
  equal records can be passed across module boundaries without the
  `type alias State.FileForm = Editor.Form` redirect. The redirect
  remains a valid idiom but is no longer required.
- **Same-module polymorphic call re-instantiation.** Annotated `f :
  Cfg msg -> msg` called with `msg=Int` AND `msg=Bool` in the SAME
  module both work — sibling references alpha-rename per call site.
  Previously the first call pinned `msg`.
- **Wildcard-`any` soundness gate.** `view : Model -> any` returning
  a String against an expected `Model -> Html msg` slot now correctly
  surfaces as a type error. Mid-development the v0.15 same-mod
  CForeign change wrongly treated wildcard-only sigs as polymorphic;
  the final gate requires at least one non-`any` freeVar before
  routing through CForeign. The pair `Canonicalise.Type.freeTypeVars`
  (collects wildcards) + `Instantiate.fromAnnotation` (filters them
  + per-occurrence fresh UF var) is documented in CLAUDE.md as
  load-bearing.

### Type errors / diagnostics

- **TAlias type-args propagate through readback + showType +
  typeStructEq.** Errors like `Cfg Msg vs Cfg Int` are now shown
  with their type args instead of the unhelpful `Cfg vs Cfg`.
- **Unify.hs App1 ↔ Alias same-name bridge.** Recursive parametric
  alias bodies (`type alias Tree a = { value : a, kids : List (Tree
  a) }`) unify with external `TAlias` references correctly.
- **Canonicaliser parametric-alias var substitution (Surface 1).**
  Sky source can now access fields on `Cfg msg`-typed function
  parameters without dropping to structural inference.

### Limitations closed in v0.15 (with the older list trimmed)

- ~~Let bindings with parameters after multi-line case~~
- ~~Zero-arity functions reading env vars memoised at init()~~
- ~~`exposing (Type(..))` for user-module ADT constructors~~
- ~~`import X as Alias` leaks the alias into codegen~~
- ~~`let` bindings don't support forward references~~
- ~~Parametric record alias bugs (Surfaces 1, 2, 3)~~

### Verification

- 27/27 examples clean-build from a wiped slate
- 120/120 stdlib Sky.Test assertions (`examples/00-standard-libs`)
- 21/21 v0.15 parametric-record-alias stress test sections
- 306/306 cabal tests (0 failures, 1 pending) — including the LSP
  `DiagnosticsSpec` "TEA with Live.app: wrong view return type
  surfaces as a real diagnostic" case
- `scripts/verify-all-web.sh` — 10/10 Sky.Live + Sky.Http.Server
  Playwright runs + console-e2e
- `scripts/verify-cli.sh` — 13/13 CLI / Tui / Cli (Fyne X11 skipped)
- Skydeploy clean rebuild + runtime probe (`/`, `/_sky/healthz`,
  `/_sky/buildinfo`, console mounted)

## Unreleased

### Std.Ui — surface complete

- **Background**: `image url`, `linearGradient angle stops`, `gradient css` (raw CSS escape).
- **Border**: `widthEach { top, right, bottom, left }`, `solid` / `dashed` / `dotted`, `shadow { offsetX, offsetY, blur, spread, color }`, `glow blur color`, `innerShadow {…}` (rendered with CSS `inset`).
- **Font**: `italic`, `underline`, `letterSpacing em`, `wordSpacing em`, plus weight helpers `semiBold` / `extraBold` / `black`.
- **Region** (new + wired through): semantic landmarks now route to real HTML tags via the renderer — `mainContent` → `<main>`, `navigation` → `<nav>`, `footer` → `<footer>`, `aside` → `<aside>`, `heading n` → `<h1>`..`<h6>`. Plus `label text` → `aria-label="..."`, `announce` → `aria-live="polite"`, `announceUrgently` → `aria-live="assertive"`. Previously these helpers existed but the renderer didn't dispatch — they all rendered as `<div>`.
- **Nearby positioning**: `above` / `below` / `onLeft` / `onRight` / `inFront` / `behind` — wraps the parent with `position: relative` and the nearby element with `position: absolute` + matching offsets. Use for tooltips, popovers, dropdown menus, badges.
- **Input**: typed wrappers for `email`, `username`, `search`, `currentPassword {show: Bool}`, `newPassword {show: Bool}`. New `radio` / `radioRow` / `slider` controls (radio uses string-valued `RadioOption` to sidestep deeply-polymorphic-record HM friction). `placeholder` text now actually renders as the HTML `placeholder=` attribute. `LabelHidden` emits `aria-label` for screen-reader access.
- **Overflow** (new): `clip` / `clipX` / `clipY` / `scrollbars` / `scrollbarX` / `scrollbarY`.
- **`Ui.html` escape hatch**: now wraps an arbitrary Std.Html VNode via the new `Raw any` Element variant. Previously collapsed to `Text ""` (placeholder).
- **Compiler-side**: `Html.aside` registered in the kernel registry so the renderer's `<aside>` dispatch resolves to `rt.Html_aside`. `Html.main` was already registered.
- **Limitation #14 doc clarification**: the documented "use `Ui.text ""` instead of `Ui.none`" workaround was misleading. `Ui.none` works fine when annotations use bare `Element Msg` (via `import Std.Ui exposing (Element)`) rather than the qualified `Ui.Element Msg`. Updated `docs/skyui/overview.md` accordingly.

### Licence + attribution

- **Relicensed to Apache License 2.0** (was MIT). Existing MIT releases (v0.10.0 and earlier) keep their original MIT terms; v0.10.1 onwards ships under Apache 2.0. The relicense brings:
  - **Patent grant** from contributors (Apache 2.0 §3) — perpetual, irrevocable patent licence for what their contribution covers.
  - **Patent-retaliation clause** — anyone initiating patent litigation against Sky users for the contribution loses their grant.
  - **Trademark clause** (§6) — the licence does not grant rights to use the "Sky" name / trademarks.
  - **NOTICE file mechanism** (§4(d)) — a structured way to propagate prior-art attribution through forks. `NOTICE.md` at the repo root.
  Same permissive philosophy as MIT (commercial use, modify, fork, sublicense all allowed). See [CONTRIBUTING.md](CONTRIBUTING.md) for what this means for contributors. Same week, the [Std.Ui — Sky.Live polish + 4 compiler reliability fixes](https://github.com/anzellai/sky/pull/36) PR also lands.
- **Per-file derivative-work attribution** strengthened on the ten files in `src/Sky/` adapted from elm/compiler (BSD-3-Clause, © Evan Czaplicki). Each file's header now names the upstream module + licence + copyright, and `NOTICE.md` lists every adapted file with its origin and reproduces the full BSD-3-Clause licence text. This satisfies BSD-3-Clause clauses 1 + 2 (source-form + binary-form attribution).
- **Defensive endorsement-clause cleanup**: removed promotional uses of "Elm" (and the prior promotional uses of "elm-ui") from user-facing docs / READMEs / runtime comments. Factual technical references — "Elm-compatible syntax", "matches Elm's behaviour", "Elm convention", per-file derivative-work attribution — stay because they are descriptive, not promotional.

### Effect boundary (stdlib)

- **Breaking — `Std.Db.*` migrated from `Result Error a` to `Task Error a`.** `Db.connect`, `Db.open`, `Db.exec`, `Db.execRaw`, and `Db.query` now return `Task Error a`. Their runtime helpers (`runtime-go/rt/db_auth.go`) wrap their bodies in `func() any { ... }` thunks so the actual SQL defers to the goroutine spawned by `Cmd.perform` instead of blocking Sky.Live's `update()`.
  - **Why:** DB ops can take hundreds of milliseconds, can fail meaningfully, and compose naturally with `Task.parallel` / `Task.andThen` / `Cmd.perform`. Typing them as Result was a pre-Sky.Live legacy that forced every effectful pipeline to either bridge through `Task.fromResult` or block the dispatcher.
  - **Migration in this branch:** every `Lib/Db.sky` (08-notes-app, 12-skyvote, 17-skymon) and `Lib/Games.sky` (16-skychess) wrapper kept its Result-shaped public API by bridging through `Task.run` internally — consumers (Main.sky, Page/*.sky) need no changes. `examples/07-todo-cli/src/Main.sky` was rewritten as a proper Task-chained CLI demonstrating the canonical error-propagation pattern. `examples/18-job-queue/src/Main.sky` was simplified to drop the now-unnecessary bridge helpers in `saveSnapshot`/`loadHistory`. `examples/13-skyshop` is unaffected (it uses Firestore, not Std.Db).
  - **For new app code:** prefer composing Task-returning Db calls directly (`Db.exec db "INSERT..." [...] |> Task.andThen ...`) and dispatch via `Cmd.perform`. Use the Lib-layer `Task.run` bridge only when wrapping a singleton conn for synchronous case-pattern matching inside an existing update branch.

- **Added — `Task.onError` and `Task.mapError`.** Mirror their Result counterparts. `Task.onError : (e -> Task e2 a) -> Task e a -> Task e2 a` recovers from a Task error by producing a new Task — the canonical primitive for converting DB / FFI errors into 4xx/5xx HTTP responses, Sky.Live notifications, or CLI exit codes. `Task.mapError : (e -> e2) -> Task e a -> Task e2 a` adds context to an error before propagation.

- **Added — kernel sigs for `File.*`, `Process.*`, `Io.*`, `Crypto.randomBytes`, `Crypto.randomToken`** (Bucket A2 of the audit). Type-only addition: the runtime helpers already returned Task thunks, the docs/stdlib tables already promised Task; HM now enforces what the runtime had silently delivered. Net-zero migration.

- **Codegen fix — `coerceArg` now handles `SkyTask` params.** Previously, passing a value to a function expecting a typed `rt.SkyTask[E, A]` param emitted `any(arg).(rt.SkyTask[E, A])` direct assertion, which panicked at runtime against `func() any` from runtime helpers and against `SkyTask[any, any]` from cross-instantiation pass-through (Go generics are nominal). Fixed by routing parametric SkyTask param targets through `rt.TaskCoerceT`, mirroring the existing `SkyResult`/`SkyMaybe` handling. Also extended the same wrap to the `VarLocal` call-result path. This unblocked the entire Db.* migration.

- **Doctrine clarification in CLAUDE.md ("Effect Boundary: Task — two-tier in practice").** The audit considered migrating *every* effectful op to Task (println / Slog / Os.getenv / Os.getcwd / Time.now / Time.unixMillis) and concluded these stay sync. Reasons documented in CLAUDE.md under "Why theory ≠ practical here" — `let _ = println …` discard pattern, module-level `apiKey = Os.getenv "X" |> Result.withDefault ""` config reads, "stamp this row" timestamp use sites. Sky picks the Elm-pragmatic position over the Haskell-purist one: real I/O that benefits from composition goes through Task; sync convenience effects that don't benefit stay sync.

### Sky.Live

- **Breaking — default HTML template no longer loads Inter from Google Fonts.** The shell document emitted by `Live.app` previously preconnected to `fonts.googleapis.com` / `fonts.gstatic.com`, fetched the Inter family, and forced `font-family: 'Inter' … !important` on `body` and `.font-sans`. All four lines have been removed.
  - **Why:** third-party request on every cold page load (offline dev, GDPR, every visitor's IP logged with Google), plus an `!important` rule that fought app-level typography. There was no opt-out.
  - **Behaviour now:** the `<head>` ships only `<meta charset>` and `<meta viewport>`. Headings and body inherit the browser default (Times/Arial) until the app sets typography itself.
  - **Migration:** apps that want a webfont add it explicitly — e.g. a `Html.styleNode` in the view's head fragment, a self-hosted `@font-face` in a `Css.stylesheet`, or a `<link>` served from `Server.static`. Apps that were silently relying on the default Inter will look unstyled until they set their own font.
  - **Privacy/a11y wins:** no third-party network request from the runtime, and no `!important` override blocking accessibility-first apps that self-host (e.g. Atkinson Hyperlegible).
