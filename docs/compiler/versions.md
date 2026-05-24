# Version history

> **v0.15 state**: type-directed lowering throughout (lambdas,
> record-field inits, list elements, call args all carry the slot's
> typed Go form); Go generics on parametric record aliases; same-module
> polymorphic re-instantiation; wildcard-`any` soundness gate. Builds
> on Layer-3 stdlib + whole-program DCE + auto-TCO from v0.14 and
> earlier. Runtime verification across all 27 examples (120 stdlib
> assertions + 306 cabal specs).

This is a feature-level changelog covering major architectural shifts.
For the line-level history see `git log` or `CHANGELOG.md` at the repo
root.

## v0.15 — type-directed lowering (2026-05-24)

End-to-end type-directed lowering pass: every sub-expression at a
lambda body, record-field init, list element, or call arg carries the
slot's typed Go form. Closes the long-standing parametric-record-alias
bug class (Surfaces 1, 2, 3 all shipped).

**Type system.**

- **`globalRegionTypes` + `LowerCtx`.** The solver writes a per-region
  type map; `LowerCtx` threads the expected type down through
  `exprToGoExpectGo`.
- **Go generics on parametric record aliases.** `type alias Cfg msg =
  { onSubmit : msg, ... }` emits `type Cfg_R[T1 any] struct {
  OnSubmit T1; ... }` with per-instance type args (`Cfg_R[Msg]`,
  `Cfg_R[Int]`).
- **Inline lambdas keep their typed shape at record-field slots.**
  `{ onSubmit = \s -> Tag ("L:" ++ s), ... }` against `Cfg Msg` now
  emits `func(string) Msg` for the lambda.
- **Cross-alias call without the alias-chain workaround.** Structurally-
  equal records pass across module boundaries without the
  `type alias State.FileForm = Editor.Form` redirect.
- **Same-module polymorphic re-instantiation.** Annotated `f : Cfg msg
  -> msg` called with `msg=Int` AND `msg=Bool` in the SAME module both
  type-check.
- **Wildcard-`any` soundness gate.** `view : Model -> any` returning a
  String against `Model -> Html msg` now surfaces as a clean type
  error.

**Limitations closed.**

- Let bindings with parameters after a multi-line case
- Zero-arity functions reading env vars memoised at init()
- `exposing (Type(..))` for user-module ADT constructors
- `import X as Alias` leaks the alias into codegen
- `let` bindings don't support forward references
- Parametric record alias bugs (Surfaces 1, 2, 3)
- Same-module polymorphic call pinned by first instantiation
- The "use alias chain" workaround for parametric records is no longer
  required

**Verification — green.**

- 27/27 examples clean-build from a wiped slate
- 120/120 stdlib `Sky.Test` assertions (`examples/00-standard-libs`)
- 306/306 cabal tests (0 failures, 1 pending)
- `scripts/verify-all-web.sh` 10/10 Playwright runs + console-e2e
- `scripts/verify-cli.sh` 13/13 (Fyne X11 skipped)
- Skydeploy clean rebuild + runtime probe

## v0.14 — Layer 3 stdlib + observability + Std.Jobs + Std.Decimal/Money

Headline shifts between v0.13 and v0.15:

- **Layer 3 stdlib** — every kernel module surfaced as Sky source under
  `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky`. Each binding is either
  pure Sky or a `Ffi.kernel "Name"` alias whose Stage-4 rewrite routes
  call sites directly to the existing typed kernel dispatch. `sky doc`
  surfaces every entry.
- **Auto-TCO** for tail-recursive Sky functions. The lowerer detects
  tail-position self-calls (`Sky.Build.TailCallOpt.isTailRecursive`)
  and emits `[GoForever <stmts>]` with each tail call becoming
  `<param reassignment + coercion>; continue` — constant stack on
  `foldl`, `find`, `any`, `all`, `member`, `drop` etc.
- **`sky doc`** — terminal renderer + browsable HTTP server (with
  fuzzy search) + Sky.Tui interactive browser. Auto-opens the system
  browser when run with `--serve`.
- **`sky doctor`** — project / environment health checks (stale
  `.skycache/`, port-in-use, missing FFI deps, mem-guard alive). The
  `--fix` flag applies the suggested action.
- **Sky Console + sub-app mount + observability federation.** Every
  Sky.Live / Sky.Http.Server app auto-mounts a Std.Ui dev console at
  `/_sky/console` in dev mode; structured logging + Prometheus
  metrics + W3C-traceparent distributed tracing built in. Sub-apps
  pushed via `rt.MountSubApp` federate their telemetry to the parent.
- **Std.Decimal + Std.Money** — arbitrary-precision Decimal (Banker's
  round, percent helpers), currency-typed Money on Decimal + ISO 4217
  enum, `Money.allocate` (fair split), conversion rates.
- **Std.Time** — IANA zones, calendar-aware `addMonths`/`addYears`
  (month-end clamped), ISO `dayOfWeek` + `weekOfYear`, `startOfDay`
  / `startOfWeek` / `startOfMonth` / `startOfYear` /
  `diffDays`/`Hours`/`Minutes`/`Seconds`.
- **Std.Jobs** — kernel module with memory / SQLite / Postgres
  backends, exponential backoff + dead-letter table + `/_sky/jobs`
  admin UI.
- **CSRF default-on** for Sky.Live `POST /_sky/event`.
- **JSON path-aware decode errors** carry a `[PathSeg]` and pretty-
  print to `.user.email[3]`.
- **`sky watch`** — file-watch-driven rebuild + respawn with a strict
  allowlist; failed rebuilds keep the previously-running binary alive.

The v0.13 typed-codegen contract carries forward. The full per-PR
log lives at the repo root `CHANGELOG.md`.

## v0.13 — typed-codegen completion (perf/v0.13, 2026-05-15)

All seven workstreams A→G landed. The agreed contract — "all USED Sky
code → fully-typed Go; ONLY genuinely-generic OR genuinely-unused may
use Go-generic / `any`; Sky-side DCE pre-lowering; LSP 100 %" — is
satisfied end-to-end.

**Commits** (most recent first):

```
8ac27a8  docs — CLAUDE.md + templates + journey.md + LSP + Known-Limitations
584d2e1  verifier scripts + reflect-adapter arg narrowing fix
80dcdf9  G goto-def coverage + F3 orphan FfiT pruning + Unicode-aware ident matching
316accc  regression tests — AnonLambda + AnonRecord specs
5d2d8df  E anon-record struct decls — remove sanitiseTypedDeep cover-up
041bd70  B0 prefer Sky-defined union over runtimeTypedMap any-alias
5b59e68  G LSP 100% — every USED symbol class
ecf024f  F whole-program Sky DCE — per-dep decl pruning
3757025  D-Lambda-Lowerer + D1 typed HOF return
22ee8e0  A2/A1/B1/C — pre-register + superset records + container TVars + parametric ADTs
```

**Skyshop benchmark** (the most expensive real example — Stripe SDK +
Firebase + Tailwind + Std.Ui):

| Metric | Pre-v0.13 | Post-v0.13 |
|---|---|---|
| `main.go` lines | 14,398 | **4,178** (−71 %) |
| Total funcs in main.go | 3,518 | **975** (−72 %) |
| Emitted `Stripe_*` user refs | (full) | **0** (all DCE-pruned) |
| `stripe_bindings.go` lines | 326,327 | **58,059** (−82 %) |
| `type FfiT_*` aliases | 80,847 | **29** |
| Wrapper funcs | 124,312 | **57** |

**Runtime verification** added: `scripts/verify-all-web.sh` (Playwright
headless Chromium for 10 Sky.Live + Sky.Http.Server apps) +
`scripts/verify-cli.sh` (CLI / Sky.Cli / Sky.Tui). 25/26 examples PASS
end-to-end (Fyne `11-fyne-stopwatch` skip — needs X11).

See [`journey.md`](journey.md) for the full per-workstream technical
write-up (frozen at v0.13).

## v0.11.x post-0 — DX (`sky watch`) + Sky.Live hot-reload + install perf (2026-05-07)

A coordinated DX + perf release. None of the v0.11.0 surface changes; everything below is additive on top of it.

### `sky watch` — file-watch-driven hot rebuild + restart

A new top-level command: `sky watch [PATH]` polls a strict allowlist of paths (sky.toml + the entry point's directory walked recursively for `.sky` + `tests/` if it exists), debounces save bursts (default 150 ms), and re-runs the same compile pipeline as `sky build`. On success: `SIGTERM` the previously-running child, wait `--kill-timeout` ms (default 3000), `SIGKILL` if it ignores. On failure: print the build error and **keep the previously-running binary alive** — a typo halfway through a save doesn't tear down the dev session. Compile-pipeline panics (parser raising Haskell `error`) are caught at the watch boundary and treated as build failures so a single bad save can't crash the loop. SIGINT and SIGTERM both flow through a UserInterrupt async-exception handler that cleanly tears down the child without leaving zombies. Knobs: `--no-run`, `--clear`, `--interval`, `--debounce`, `--kill-timeout`, repeated `--watch=PATH` for extra paths. Spec: `test/Sky/Cli/WatchSpec.hs` (3 tests).

### Sky.Live hot-reload story

Four coordinated runtime fixes that make a `sky watch` rebuild actually update the browser DOM in production-like settings:

1. **SSE reconnect-resync.** `handleSSE` only pushed via `sess.sseCh` (populated by `dispatch`). After a restart with persistent storage, sessions reload with `Model` but no `prevTree`/`prevBody`, and no `Msg` is firing post-restart, so the browser sat on stale OLD-view HTML even though SSE handshake completed and the banner cleared. Fix: handleSSE now re-renders the current view after `hello` and pushes the result as a full-body SSE frame on every fresh connection. Cost: one HTML body per SSE connect (~10–50 KB typical). The existing `__skyReplaceHTMLPreservingFocus` keeps the swap UX-neutral (uncontrolled inputs preserved, focused state survives).
2. **Persist `outSeq`.** Client tracks `__skyLastAppliedSeq` and silently drops any frame with `seq <= lastAppliedSeq`. After restart the new process's `outSeq` reset to 0, so the resync push went out with `seq=1` while the client's `lastAppliedSeq` was whatever the old process climbed to (e.g. 47). Fix: persist `outSeq` on the session via `storableSession`; the new process continues monotonically. Frames stay newer across restarts.
3. **Session-loss probe + hard reload.** With memory store (or after `sky.toml [live] store`-kind change), all sessions are gone post-restart. SSE reconnect 404s on session-not-found, force-reopen loops on the same 404 forever, page sits at "Reconnecting…" indefinitely. Fix: `handleSSE` and `handleEvent` now set `X-Sky-Live: 1` on their 404 paths so the client can distinguish a real "session gone" from a proxy-rewritten 404. The client's `__skyForceReopenSSE` fires `__skyProbeSessionLost` on every retry. The probe POSTs a fake Msg name and inspects the 404 body — `"session not found"` specifically (not "handler not found") triggers `window.location.reload()`. One-shot guard prevents burst-reload from a flurry of retries.
4. **Open `<select>` defence in the SSE patch handler.** Native dropdowns close on any DOM mutation while open; the SSE full-body push path inherits the `__skyApplyPatches` open-select guard so a Tick subscription firing while a dropdown is open doesn't collapse it. Active user paths (sky-nav, popstate, POST text fallback) intentionally stay un-defended to avoid freezing navigation.

### `sky install` — chunked-multi inspector + trimmed loader modes

Three coordinated optimisations on top of the v0.11.0 install pipeline:

- **Trimmed `packages.Config`** in `tools/sky-ffi-inspect/main.go`. Audited every helper (methodsOf, addPointerMethods, addInterfaceMethods, addFieldGetters, addZeroConstructor, describe, paramsOf, resultsOf, classifyEffect, implementsError) — none reads `pkg.Syntax` or `pkg.TypesInfo`. The inspector consumes go/types objects (Scope, Lookup, *Func, *Signature, *Named) that NeedTypes alone provides. Dropping `NeedSyntax | NeedTypesInfo` cuts loader work without changing output. Verified BYTE-IDENTICAL against the previous mode set on skyshop's 18-dep set (Stripe SDK + Firebase + Firestore + Google APIs + stdlib chunks).
- **Multi-package inspector mode.** The inspector accepts N argv paths and emits a JSON ARRAY of PackageInfo objects. A single `packages.Load(roots…)` call lets Go's loader dedupe shared transitive deps across roots — when Stripe and Firestore both pull `golang.org/x/oauth2`, that package's type-checking happens once across the whole load instead of N times across N invocations. Single-arg invocation still emits a bare object for backwards compat. Per-package errors travel in the array entry's `errors` field.
- **Chunked-multi parallelism in `regenMissingBindings`.** Split missing deps into K chunks (`K = SKY_INSTALL_PARALLEL`, default `min(numProcessors, 4)`). Run K inspector subprocesses in parallel, each in multi-mode over its chunk. Each chunk benefits from cross-pkg dedup; chunks themselves run in parallel for wall-clock speedup. Sweet spot when K matches `numProcessors / 2` (each inspector internally uses ~2 threads from Go's loader).
- **Forward-compat fallback.** A stale in-tree `bin/sky-ffi-inspect` predating the multi-mode upgrade returns a single-pkg JSON object even when given multiple argv args. `runInspectorMulti` detects this (array decode fails + single-object decode succeeds) and falls back to a per-pkg loop. Stale dev binaries don't break `sky install`.

**Skyshop benchmark** (18 Go deps including Stripe SDK + Firebase + Firestore + Google APIs, M1 Mac 8-core, warm Go module cache, cold `.skycache/ffi`):

| | Wall | User CPU | Sys |
|---|---|---|---|
| Baseline (3-sample mean) | 67.5 s | 136.3 s | 9.6 s |
| Optimised (3-sample mean) | 58.5 s | 112.5 s | 4.3 s |
| Speedup | **13 %** | **17 %** | **55 %** |

The hard floor: Stripe SDK master package alone takes ~53 s to type-check inside Go's loader (per-dep timing measured via inspector standalone). Below that needs Tier 2 work (usage-driven FFI generation — only emit bindings for symbols user code references), tracked separately.

### Test coverage added

- `test/Sky/Cli/WatchSpec.hs` — 3 specs (initial banner, edit-triggers-rebuild, broken-save-keeps-old-binary).
- `test/Sky/Build/FfiGenMultiSpec.hs` — 5 specs (multi-mode array decode, input-order preservation, per-pkg error envelopes, single-mode object backwards compat, forward-compat fallback probe).
- `runtime-go/rt/live_sse_handshake_test.go` — adds reconnect-resync wire test, X-Sky-Live header on the two 404 paths, session-not-found body contract.
- `runtime-go/rt/live_status_test.go` — substring fences for the new client-side handlers (resync push consumer, session-loss probe, open-select defence in SSE).
- `runtime-go/rt/live_store_roundtrip_test.go` — `outSeq` round-trip across the gob-encoded session boundary.

## v0.11.0 — Std.Ui (typed no-CSS layout DSL) + 5 root-cause compiler fixes + Apache 2.0 (2026-04-27)

- **`Std.Ui`** — a typed, no-CSS layout DSL inspired by `mdgriffith/elm-ui`. Build a UI from typed primitives (`el` / `row` / `column` / `paragraph` / `textColumn` / `link` / `image` / `button` / `input` / `form` / `html`) and typed attributes from focused sub-modules: `Std.Ui.Background` (`color` / `image` / `linearGradient`), `Std.Ui.Border` (`color` / `width` / `widthEach` / `rounded` / `solid` / `dashed` / `dotted` / `shadow` / `glow` / `innerShadow`), `Std.Ui.Font` (`color` / `family` / `size` / `weight` / `bold` / `italic` / `underline` / `letterSpacing` / `wordSpacing`), `Std.Ui.Region` (semantic landmarks routed to `<h1..h6>` / `<main>` / `<nav>` / `<aside>` / `<footer>` + ARIA), `Std.Ui.Input` (typed form controls including `currentPassword` / `newPassword` / `radio` / `slider`), `Std.Ui.Lazy` / `Std.Ui.Keyed` / `Std.Ui.Responsive`. Renders to inline-styled HTML on the server side; Sky.Live ferries diffs to the browser. See [`docs/skyui/overview.md`](../skyui/overview.md) and [`examples/19-skyforum`](../../examples/19-skyforum) (~500 LOC, 8-module Reddit/HN-style demo).
- **5 root-cause compiler fixes** (each with a dedicated regression spec):
  1. **Multi-line `module/import exposing (…)` silently dropped exports** — `Sky.Parse.Module` used `spaces` (no newlines) inside parens; module-graph stage downgraded parse failures to warnings. Fix: `freshLine` inside parens + parse-errors-at-module-graph-stage are FATAL. Spec: `test/Sky/Parse/MultiLineExposingSpec.hs`.
  2. **Cons-with-constructor pattern (`(Ctor x) :: rest -> body`) didn't check head's tag** — runtime panic when head was a sister variant. Fix: new `consHeadCondition` / `patternConditionForExpr` helpers emit a head-discriminator check joined to the length test. Spec: `test/Sky/Build/ConsCtorPatternSpec.hs`.
  3. **Cross-branch HM with `any`-typed ADT payload pinned the wrong type** — distinct occurrences of `T.TVar "any"` shared a single fresh unification variable via the solver's `_varCache`. Fix: treat `T.TVar "any"` as a wildcard — every occurrence creates a fresh unification variable. Spec: `test/Sky/Type/AnyWildcardSpec.hs`.
  4. **Tuple-pattern in lambda arg shared element types across siblings** — `patternBindings` for `Can.PTuple` bound element types to STATIC names (`_tup_0`, `_tup_1`) which collapsed via `_varCache`. Fix: new `patternBindingsIO` mints fresh per-occurrence type-var names + emits structural `T.CEqual` constraints. Spec: `test/Sky/Type/TupleLambdaSpec.hs`.
  5. **`/=` operator panicked on polymorphic generic params** — lowered to Go-native `!=`, which fails with `incomparable types in type set` for `func[T any](…)`. Fix: lower `/=` to the new `rt.NotEq` runtime helper (mirrors `rt.Eq`).
- **`sky fmt`** auto-breaks long imports + module exposing past ~100 chars (matches `elm-format` convention). Idempotent. Was: collapsed user-written multi-line imports back to single-line.
- **`sky test`** exits 0 for passing test modules (was xfail; combined fix from #4 + #5).
- **LSP false-positive on TEA + `Live.app`** heuristically suppressed via `isLikelyExternalsFalsePositive` in `Sky/Lsp/Server.hs`. Proper fix (extract `loadProjectExternals` from `Sky.Build.Compile`) tracked separately.
- **Sky.Live runtime fixes**: refresh-404 path on single-page apps with `routes = []`; `Event.onFile` / `Event.onImage` exposed in the kernel registry.
- **Compiler reliability**: closed CLAUDE.md Limitations #16 (kernel-sig coverage), #17 (HM heap exhaustion + defensive `SKY_SOLVER_BUDGET` bound), #18 (typed-codegen ctor narrowing).
- **Apache 2.0 relicense** (was MIT). Brings patent grant + retaliation, trademark clause, and the NOTICE-file mechanism. Full prior-art attribution lives in [`NOTICE.md`](../../NOTICE.md): Std.Ui inspiration (mdgriffith/elm-ui), Sky.Live's architectural style (Phoenix LiveView), and the elm/compiler-derived files in `src/Sky/Type/` + `src/Sky/AST/` + `src/Sky/Reporting/` + `src/Sky/Parse/Primitives.hs`. See [`CONTRIBUTING.md`](../../CONTRIBUTING.md) for the inbound = outbound model. Existing v0.10.x-and-earlier releases keep their MIT terms.
- **Release pipeline hardening** (uncovered in flight, fixed before tag): release workflow now writes `app/VERSION` before `cabal build` + smoke-tests that `--version` matches the tag (was a 4-release latent bug — every prior shipped binary reported `sky dev`); CI replaced unbounded `sky verify` step with bounded `scripts/example-sweep.sh` (10s/CLI + 1s/HTTP probe); cross-platform `run_with_timeout` shim handles macOS runners that don't ship GNU coreutils.

## v0.10 — stdlib consolidation + soundness gaps closed (April 2026, BREAKING)

- Single canonical module per concern. Dropped `Args`, `Env`, `Sha256`, `Hex`, `Slog` (folded into `System`, `Crypto`, `Encoding`, `Log.*With`); renamed `Os` → `System` to free the `Os` qualifier for the Go FFI `os` package; shrank `Process` to `run` only.
- `System.getenvOr` returns bare `String` (default supplied → can't fail).
- New `Log.{debugWith, infoWith, warnWith, errorWith}` for structured logging; `sky.toml [log] format / level` configures defaults (`SKY_LOG_FORMAT` / `SKY_LOG_LEVEL` env vars override).
- Auto-force `let _ = TaskExpr` discard semantics formalised in the lowerer; `main`'s body wrapped in `rt.AnyTaskRun` so `main = println X` actually prints under Task-everywhere.
- Foreign-call mismatches (Go arity / type errors at FFI call sites) and dep-module HM errors are FATAL — silent degradation to `any`-typed bindings is gone. Regression test: `test/Sky/Build/DepHmFatalSpec.hs`.
- Bare-name aliases for every kernel module (`Log.error`, `Crypto.sha256`, `Encoding.base64Encode` work without explicit `import Std.X`).
- Sky.Live: configurable `/_sky/event` body cap via `[live] maxBodyBytes` / `SKY_LIVE_MAX_BODY_BYTES` (default 5 MiB; previously hardcoded 1 MiB).

See [V0.10.0_PR_SUMMARY.md](../V0.10.0_PR_SUMMARY.md) for the full migration guide.

## v0.9 — Haskell compiler rewrite (April 2026)

**Branch:** `feat/sky-haskell-compiler` (pre-merge).

Production readiness plan (P0-P13) fully complete:

- **P0** — `cabal test` harness + `scripts/example-sweep.sh` regression fence.
- **P1** — parser gaps (negative patterns, let-after-case, selective `exposing (Type(Ctor1, Ctor2))`).
- **P2** — `exposing` clause enforcement; imports of unexposed names are rejected.
- **P3** — pattern exhaustiveness checker. Missing ADT ctors / missing True/False / literal-without-wildcard are build errors.
- **P4** — typed record codegen. `TRecord` no longer falls through to `any`.
- **P5** — typed tuples (`rt.SkyTuple2` / `rt.SkyTuple3` / `rt.SkyTupleN`; arity 2 → struct with `V0,V1`, arity 3 → `V0,V1,V2`, arity ≥ 4 → slice-backed).
- **P6** — typed unresolved type variables via Go generics.
- **P7** — typed FFI wrappers. 35,775 → 0 `(p0 any)` residuals across examples.
- **P8** — typed kernel stdlib dispatch. ~900 new typed call sites. `ResultCoerce`/`MaybeCoerce` sites 213 → 58 (72.8% drop).
- **P9** — generic FFI via reflection (`SkyFfiReflectCall`). Zero `// SKIPPED` wrappers.
- **P10a-e** — stdlib wiring: Random, Time, Http.Server, Sky.Live, Std.Db, Std.Auth.
- **P11a-b** — `sky upgrade` self-update + `[dependencies]` resolution via `SkyDeps.installDeps`.
- **P12** — reflection audit. 99 reflect occurrences classified; no new reflection added.
- **P13** — error unification. `Sky.Core.Error` is the single canonical error type. `Std.IoError` and `RemoteData` removed.

Post-v1 cleanup:

- `ffi/` → `.skycache/` migration. Auto-regeneration of FFI bindings on `sky build / run / check`.
- README split into `docs/` tree.

## v0.8.x — async commands, multiline strings, Sky.Live maturity

- Async `Cmd.perform` for Sky.Live. `update` returns `(Model, Cmd Msg)`.
- `Cmd.batch` runs commands concurrently.
- Multiline strings (`"""..."""`) with `{{expr}}` interpolation. Preserves newlines and indentation.
- Formatter style improvements (leading commas, 4-space args, tuple vertical break).
- Constructor partial application via `checkPartialIdent`.
- `MultilineStringExpr` AST node (previously desugared at parse time).

## v0.7.30 — zero-arity memoisation + embedded CLAUDE.md

- Top-level zero-parameter declarations (`counter = Ref.new 0`) are now memoised. Singletons work correctly.
- `sky init` CLAUDE.md template embedded via `//go:embed runtime/*` — installed binaries no longer require a `templates/` directory on disk.
- `Task.perform` returns `Result` uniformly; both `Ok` and `Err` branches pattern-match.

## v0.7.28 — type annotation enforcement

- Pretty-printer renames quantified type variables to `a, b, c` in error messages.
- `inferFunctionSelfUnify` uses the annotation as the scheme when present and the body validates against it.
- `preRegisterFunctions` uses the annotation for forward references and mutual recursion.
- Cross-module type alias resolution in `registerTypeAliases` and `Resolver.typeExprToScheme`.
- Polymorphic annotations like `f : a -> b -> a` get distinct TVar IDs.

## v0.7.26 — auto record constructors

- Every `type alias Foo = { ... }` declaration auto-generates a positional constructor function (factually: matches Elm's convention for the same construct).
- Eliminates `makeFoo` boilerplate.
- `Result.map3 Foo (parseA ...) (parseB ...) (parseC ...)` works directly.

## v0.7.25 — applicative combinators

- `Result.map2/3/4/5`, `Result.andMap`, `Result.combine`, `Result.traverse`.
- Matching `Task.map2/3/4/5`, `Task.andMap`.
- `sky_call2/3/4/5` upgraded to handle both curried and uncurried multi-arg Sky functions.

## v0.7.21 — nested case + FFI callback wrapping

- Nested `case...of` compiles and runs correctly (`caseDepth` counter generates unique `__subject_N` variables per nesting level).
- FFI callback wrapping: `mapGoFuncType` parses arbitrary Go callback signatures.
- `sky check` handles `func(...)` types in FFI boundaries properly.
- Non-exhaustive case expressions are compile errors (was a dead binding in Infer.sky).

## v0.7.10 — ADT structs

- `SkyADT{Tag: N, SkyName: "Name", V0: val}` struct shape.
- Integer tag matching (O(1)).
- Struct field access in case bodies.

## v0.7.x — Haskell rewrite

- Compiler ported from self-hosted Sky to Haskell.
- HM type inference consolidated, exhaustiveness checker landed.
- Typed FFI wrappers alongside any/any variants.
- Build-time FFI DCE strips unreferenced wrapper bodies.

## v0.3.0 — reliability baseline

- Self-hosted Sky compiler stabilised.
- Stripe SDK (~9k types) became the stress test for FFI generation.
- Incremental compilation via `.skycache/lowered/`.

## v0.1 — initial release

- TypeScript bootstrap compiler.
- Elm-compatible surface syntax.
- Go backend.
- Basic Sky.Live prototype.

---

**Note on semver:** Sky's pre-v1 minor versions carried breaking changes routinely. v1.0 (when reached) will commit to semver — breaking language or CLI changes will increment the major version.
