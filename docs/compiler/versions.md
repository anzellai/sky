# Version history

This is a feature-level changelog covering major architectural shifts. For the line-level history see `git log`.

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
