# Version history

This is a feature-level changelog covering major architectural shifts. For the line-level history see `git log`.

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
- Formatter elm-style improvements (leading commas, 4-space args, tuple vertical break).
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

- Every `type alias Foo = { ... }` declaration auto-generates a constructor function matching Elm's convention.
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
- Elm-style syntax.
- Go backend.
- Basic Sky.Live prototype.

---

**Note on semver:** Sky's pre-v1 minor versions carried breaking changes routinely. v1.0 (when reached) will commit to semver — breaking language or CLI changes will increment the major version.
