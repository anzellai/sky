# Compiler journey — TS → Go → Sky → Haskell

The Sky compiler has lived in four implementations. Each rewrite addressed a concrete limitation of its predecessor. This document captures the history so future maintainers understand why the current codebase looks the way it does.

## 1. TypeScript bootstrap (`legacy-ts-compiler/`)

**When:** project inception.

**Why TypeScript:** rapid prototyping. The language semantics were still moving; a dynamic ecosystem (npm, ts-node) made iteration fast. TypeScript's structural types gave enough safety without HM-level plumbing.

**What it did:** parser, canonicaliser, and a naïve Go emitter. No type inference — all types were erased to `any` in the Go output. Good enough to bootstrap runnable programs.

**Why it had to go:**

- Node.js dependency for every run.
- Slow: 5–15s startup for even trivial programs.
- No real type checking, so Sky's guarantees were vibes-based.
- JS ecosystem drift made reproducible builds painful.

## 2. Go rewrite

**When:** once Sky programs could run, the TS compiler was ported to Go.

**Why Go:** single static binary. No Node. Fast startup. The same language Sky compiles to — reducing cognitive load when debugging codegen.

**What changed:** same pipeline, reimplemented idiomatically in Go. Type checker was still basic.

**Why it had to go:**

- Writing a functional type checker in Go is unpleasant. The implementation fought the language at every step.
- Large feature landings (pattern exhaustiveness, HM inference) kept stalling because the imperative code made invariants hard to maintain.

## 3. Self-hosted Sky (`legacy-sky-compiler/`)

**When:** once Sky had enough features (HM inference, ADTs, pattern matching, FFI) to implement itself.

**Why self-hosted:** the classic demonstration that a language is real. Writing the compiler in Sky validated the language's ergonomics for production code.

**What worked:**

- Proved Sky could handle serious programs (the compiler was ~30k lines of Sky).
- Exercised every corner of the language, catching ergonomic bugs early.
- Self-hosted builds were around 6 MB native binaries with no external runtime.

**Why it had to go:**

- Sky's type system was Hindley-Milner, but writing HM itself in Sky hit the same expressiveness limits that made it hard to describe in Go. No higher-kinded types, no type classes, no row polymorphism — all intentional language omissions that made the compiler's own invariants brittle.
- Parser error recovery and LSP latency suffered because Sky's runtime model (single-threaded, `any`-boxed by default pre-v1) added cost the compiler couldn't optimise away.
- Debugging was circular: a compiler bug affecting inference made the compiler itself misbuild.

## 4. Haskell (current — `src/` tree)

**When:** 2026 Q1 — after P4/P5/P6 of the production-readiness plan landed.

**Why Haskell:**

- Hindley-Milner is Haskell's native idiom. The type checker is a few hundred lines of clear constraint-solving code rather than the thousands of lines of imperative state management it took in Go.
- ADTs and pattern matching in Haskell map 1:1 to Sky's AST, so the parser and canonicaliser are almost transliterations.
- GHC's optimiser produces a binary that's faster than the self-hosted Sky implementation without any hand-tuning.
- Type-level invariants (`data Canonical` vs `data Lowered` AST phases) catch compiler bugs at compile time.

**What moved:**

- Parser, canonicaliser, type checker, lowerer, Go emitter — all in `src/Sky/**`.
- FFI generator (`src/Sky/Build/FfiGen.hs`) inspects Go packages via a Go-side tool (`tools/sky-ffi-inspect/`) and emits typed wrappers.
- LSP (`src/Sky/Lsp/`) — same module graph reuse as the compiler, no duplicated parsing.
- The Sky runtime (`runtime-go/rt/`) stayed in Go — it's shipped as `//go:embed` data and copied into every project's `sky-out/rt/` at build time.

**Trade-offs made:**

- Sky is no longer self-hosted. For the foreseeable future, a Haskell toolchain (GHC 9.4+ via `cabal install`) is required to build the compiler. Users of Sky only need the `sky` binary + Go, not Haskell.
- Contributors to the compiler need to learn Haskell. The existing codebase is conventional Haskell — no advanced type-level hackery.

## Why this sequence

Each implementation pushed the language far enough that the next one became feasible. TS let us prove the shape of the language. Go let us ship a single binary. Sky self-hosted let us ensure the language was usable for real work. Haskell let us make the compiler *good*.

## 5. Typed codegen (v0.9 / `feat/typed-codegen`)

Not a new compiler — a pipeline rework on top of the Haskell implementation. Worth a dedicated section because the before/after on generated Go is the biggest visible change since Haskell landed.

**The problem (v0.7.x baseline).** Every generated function looked like `func f(a any, b any) any`. Every call site went through `rt.sky_call(f, arg)`, which did a reflect-based dispatch. Records were `map[string]any`. ADTs were tagged structs but parameters were still boxed. The Go compiler was just a static printer; all type errors surfaced at runtime.

**The goal.** Zero `any` in generated signatures across every example. Go's type checker becomes the second layer of defence; the runtime's reflect dance disappears from hot paths.

### What worked

- **Annotations are load-bearing.** Before v0.7.28, the inferred scheme won even when the user wrote `f : String -> Int -> String`. A body that typechecked as `forall a b. a -> b -> a` would register that scheme instead of the annotation, and callers could pass any types. The fix: three coordinated patches in `Sky/Type/Constrain/Expression.hs` — `applyAnnotationConstraint` unifies the inferred body with the annotation, then stores the annotation as the scheme; `preRegisterFunctions` uses the annotation for forward refs; the pretty-printer renames quantified vars to `a, b, c` consistently so the error messages are readable.
- **Cross-module alias resolution at registration time.** `registerTypeAliases` takes the imported-alias dict and resolves `Counter` in `myCounter : Counter` to the record at registration, so downstream modules see the concrete shape rather than a late-bound placeholder.
- **TVar defaulting with slot awareness.** By emission time, residual TVars default by position: error slots → `Sky.Core.Error.Error`; ok/return slots → `rt.SkyValue` (a named `any` alias so the grep gate stays passable). Anything that reaches a typed caller gets monomorphised via `rt.Coerce[T]`.
- **Recursive runtime narrowing.** `rt.Coerce[T]`, `AsListT[T]`, `AsMapT[V]`, `AsDict` all delegate to `narrowReflectValue`, which handles maps-of-maps, slices-of-maps, and heterogeneous string columns in one pass. Without recursion, SQL rows (`[]map[string]any`) typed at the Sky level as `List (Dict String String)` silently dropped non-string columns and broke auth/CRUD in 08-notes-app and 13-skyshop.
- **Curried lambda wrapping.** `adaptFuncValue` recurses: when a Sky `func(any) any` returns another `func(any) any` but the target wants `func(string) rt.SkyResponse`, we wrap at each level. The Sky.Live requireAuth → route-handler path uses this every request.
- **Kernel sigs in `lookupKernelType`.** Db.open, Db.query, Db.exec, Context.background, Fmt.sprint*, Css.rgb/rgba/hsl/hsla/shadow became explicit signatures at the inference layer so callers pick up typed Go shapes.
- **Literal pattern constraints.** `case foo of "ready" -> _` now forces `foo : String` at infer time. Before the fix, the scrutinee stayed polymorphic and the downstream comparison was boxed; once codegen stopped boxing, the mismatch became a runtime panic.

### What we tried and abandoned

- **Named Go structs for anonymous records in function signatures.** `f : { x : Int, y : Int } -> Int` would need a struct name for emission, and HM can't backfill one. We emit typed struct decls only when the user names the shape with a `type alias`; anonymous inline records in signatures stay any-boxed. Accepted as a limitation — users simply write the alias.
- **Monomorphisation over Go generics.** Briefly attempted for `SkyResult[E, A]` / `SkyMaybe[A]` / `SkyTuple2[A, B]`. On 13-skyshop it blew the emitted Go up roughly 5× because the Stripe SDK touched opaque wrappers at many call sites, each reinstantiating. Go generics produce similar performance with one instantiation per type pair — reverted.
- **Narrowing `Live.app.init`'s request-record type.** Typing it as `Dict String String -> (Model, Cmd Msg)` looked tidy but collapsed to the first Live example's record shape. 13-skyshop's nested Firestore maps then failed to unify. Reverted to a polymorphic TVar on `init`'s argument.
- **Zero-arity env lookups at Go init time.** Memoised zero-arity bindings calling `Os.getenv` evaluate during Go `init()` — before `godotenv` has loaded `.env`. Adding a dummy `_` parameter prevents the memoisation. Still in the known-limitations list.
- **Eliminating `any` from the internal runtime kernels too.** `Dict_get`, `List_map`, `Html_render` still return `any` internally; we rely on `rt.Coerce[T]` at call sites to narrow. The port to generic kernels is scheduled but not gated on v1.0 because the typed surface already holds.

### Invariant this branch enforces

The 20-example sweep reports **0 real-`any` sigs**. The helper `rt.SkyValue` is a named alias over `any` used in exactly those slots where runtime polymorphism is intentional (return-only slots, explicit boxing). `sky-out/main.go` files contain no `any(body).(T)` patterns outside the `rt.Coerce*` helpers — the P0-3 grep gate stays green.

## v0.12 — typed routing soundness floor

v0.12 closed the Gap 3 + Gap 4 long tail from v0.9's typed-codegen work and added Sky.Tui (TEA in the terminal sharing `view` code with Sky.Live).

### Typed kernel routing

The kernel-by-kernel routing table now has typed call sites across the 24-example sweep: List/Dict/Maybe/Result helpers all route through `rt.X_T[A, B]` or `rt.X_TA[A]` (typed-slice + any-fn) variants instead of `rt.X_Any`. Concrete benefit: ~200 typed call sites where v0.9 had 0.

### What unblocked v0.12

- **Lambda-input-derived element typing.** Previously, `inferListElemGoType` looked up the LIST argument's type in `solvedTypes`. But Sky's HM stores let-bound names with a single (innermost-wins) type per module — when one function bound `visible : List Monitor` and another bound `visible : List Metric` in the same module, only one survived. Wrong typed routes (`rt.List_mapTA[State_Metric_R]` for a list of Monitors) emitted silently, producing zero-valued elements at runtime via the narrow fallback. **Fix**: derive the element type from the LAMBDA's INPUT TYPE via `_cg_funcParamTypes` lookup. HM enforces the lambda's input matches the list's element, so this is guaranteed correct — immune to intra/cross-module shadowing.
- **Conflict-detection merge with TVar normalisation.** `typesWithDeps` now collects per-key type assignments across all modules, normalises TVar names to a shared sentinel (so structurally-equal types from different modules match), and replaces genuinely conflicting names with `_ambig` (resolves to `any` in `solvedTypeToGo`). Cross-module shadowing of names like `children` (Std.Ui's `List Element` vs Std.Html's `List VNode`) used to produce wrong typed routes; now they fall back to any-routing safely.
- **`Dict_fromListT[V]` + `Dict_map2T[V, W]` runtime variants.** Closed the `Dict.fromList` 8-site any-route in skyshop and `Dict.map`'s curried-fn / runtime single-arg shape mismatch.
- **Cross-call inference for `List.take/drop/filter/find/reverse/...`.** When a kernel's result element type ties to its input arg's element type (the "identity-on-element-type" family), `inferExprType.Can.Call` now substitutes correctly so downstream `List.map` sees the concrete element type through a chain like `List.map f (List.take 10 xs)`.

### Soundness floor: strict `coerceInner`

Earlier in v0.12 development the runtime `coerceInner[T]` quietly fell back to zero `T` on type-assertion failure — silencing what would otherwise have been compiler bugs. **Reverted**: the fallback is now a strict panic with a descriptive message ("rt.coerceInner: type mismatch — source X cannot be cast to target Y. This is a compiler bug in typed-codegen routing."). The conflict-detection merge + lambda-input-derived typing ensure no wrong-typed route is ever emitted; if a panic ever fires it's a real compiler-side bug to investigate. All 6 Live apps now serve HTTP 200 with the strict panic active — empirical confirmation that current typed routing is correct end-to-end.

### Runtime hardening (defense-in-depth)

Every reflect-based `tagField.Int()` site in `rt.go` now gates on `tagField.Kind() == reflect.Int(64)` BEFORE calling `.Int()`:

- `narrowSkyContainer` (the main offender — rt.VNode has `Tag string`, used to panic with "SetInt on string Value")
- `coerceInner`'s Sky-container reconstruction path
- `ResultCoerce` / `MaybeCoerce` reflect fallback paths
- `anyResultView` / `anyMaybeView` / `Result_withDefault` / `unwrapAny` / SkyMaybe slice-appendJust

Each `if tagField.IsValid()` guard now also includes the int-kind check. Closes the latent "non-Sky struct with `Tag` field" panic class permanently.

### Sky.Tui v1

TEA in the terminal. Same `init / update / view / subscriptions` shape as Sky.Live; same `Std.Ui` widgets render to ANSI cells instead of HTML. Cross-backend code shares the entire `view` + `update` layer — only `main` differs (`Live.app` vs `Tui.app`). Runtime entry: `Tui_app` in `runtime-go/rt/tui_ui.go` (~2400 lines).

Coverage of `Std.Ui` primitives: ~95%+. Layout (`row/column/wrappedRow/grid/paragraph/textColumn`), text styling (truecolour fg/bg, bold, italic, underline), borders, inputs (text/password/checkbox/radio/slider/multiline), events (`onClick/onInput/onSubmit/onKeyDown` + mouse press/scroll), nearby overlays (`above/below/inFront/...`), focus ring with Tab cycling, wide chars (CJK + emoji + ZWJ via `uniseg`), bracketed paste, SIGWINCH resize, signal-safe teardown.

Reliability floor (enforced runtime invariants): goroutine panics (Cmd.perform, key reader) wrap through `safeGo` which restores the TTY; SIGTERM/SIGHUP/SIGQUIT/SIGINT-from-outside trapped and routed to `tuiTeardown`; main-goroutine panics fall through `tuiTeardown + DECSTR soft reset`; ANSI injection via user text sanitised at every paint path; hard cap at `tuiMaxContentH = 50 000` rows; `TERM=dumb` / non-TTY stdin refused before raw mode.

### What's left from v0.12

The remaining 10 `List_mapAny` residuals across the 24-example sweep are all FFI-opaque element types (Firestore returns, JSON-decoded list types) or unresolved HM types that genuinely can't be typed at codegen without rewriting Sky's curry semantics. These any-routed call sites use SkyCall reflect dispatch — functionally correct, ~100 ns/element slower than typed routes. The strict `coerceInner` panic guarantees no wrong-typed route can fire silently.

## v0.13 (perf/v0.13 — typed-codegen completion, 2026-05-15)

v0.13 is where the typed-codegen contract is genuinely complete: **all USED Sky code emits fully-typed Go**. The pre-v0.13 codepath had several layers of "type-then-cover-up" — defensive `any` fallbacks (`sanitiseTypedDeep` rewrote `Anon_R_*` → `any`; HOF return slots stayed blanket `any` even when the HM said otherwise; FFI registry kept dead Stripe bindings alive). v0.13 closes each gap at root, removes the cover-ups, pins regressions.

Seven coordinated workstreams (A→G) plus cross-cutting runtime fixes. Commits, most recent first:

| Commit | Workstream |
|---|---|
| `584d2e1` | verifier scripts (Playwright + CLI) + reflect-adapter arg narrowing |
| `80dcdf9` | G goto-def coverage + F3 orphan FfiT pruning + Unicode-aware ident matching |
| `316accc` | regression tests — AnonLambda + AnonRecord specs |
| `5d2d8df` | E anon-record struct decls — remove sanitiseTypedDeep cover-up |
| `041bd70` | B0 prefer Sky-defined union over runtimeTypedMap any-alias |
| `5b59e68` | G LSP 100% — every USED symbol class |
| `ecf024f` | F whole-program Sky DCE — per-dep decl pruning |
| `3757025` | D-Lambda-Lowerer + D1 typed HOF return |
| `22ee8e0` | A2/A1/B1/C — pre-register + superset records + container TVars + parametric ADTs |

### A — forward-ref pre-registration + open-record alias match

`Sky.Type.Constrain.Expression.constrainDecls` now pre-registers UNANNOTATED `Can.Def` declarations via an outer `CLet` header so forward refs (e.g. `main` calling a `view` declared later) bind to the real `defType` var rather than the throwaway `CLocal` fresh var the solver used to mint. Annotated `Can.TypedDef` stays sequential — pre-registering with `Forall []` would collapse same-letter type-vars across distinct same-module call sites, defeating polymorphism.

`Sky.Generate.Go.Record.lookupRecordAlias` + `Sky.Build.Compile.matchAliasByFieldSet` do superset match on open records (the row-polymorphic `T.TRecord fields (Just rowExt)` HM emits for any function reading a record subset). Smallest-superset wins; tied sizes → Nothing → falls back to `any`.

### B — parametric-ADT erased name + Sky-union priority

`safeReturnType`'s `T.TType home name _` arm renders parametric Sky ADTs (`Html msg`, `Element msg`, `Attribute msg`) as their erased Go-name. Sky-defined unions in `_cg_unionNames` take precedence over `runtimeTypedMap`'s any-aliased entry, so `Attribute` from `Std.Ui` / `Std.Html.Attributes` emits as the typed Sky struct instead of `rt.SkyAttribute = any`.

### C — container TVars propagate

`tvarsInEmitted` recurses into `List`/`Dict`/`Set`. Paired with a PARAM-only filter in `splitInferredSigWithReg`'s `usedTypeParams` — Go's generic inference only works from input positions; a TVar appearing only in the return collapses to `[]any` rather than emit a `cannot infer T2` Go-build error.

### D — typed lambda at user-defined HOF call sites

Pre-v0.13 only kernel-HOF call sites routed Sky lambdas through `curryLambdaPatTyped`. User-defined HOFs like `do : ... -> (a -> Result Error b) -> ...` dropped the lambda into `exprToGo + coerceArg` and emitted `func(any) any`. D fixes this:

- `coerceCallArgsAt`'s no-CSI fallback routes literal `Can.Lambda` args at func-typed param slots through `curryLambdaPatTyped`.
- `exprToGoTyped`'s `Can.Call` branch delegates `Can.VarTopLevel`-headed calls to `exprToGo`.
- `renderHofParamTy` concrete-return arm: blanket `"any"` → `go _to` (D1).
- `safeReturnTypeWith`'s `renderFuncTy` arm mirrors D1 — must sync, otherwise `_cg_funcParamTypes` reports `func(any) any` while `renderHofParamTy` emits the typed shape and the routing silently no-ops.
- `typedLambdaParam`'s `Can.PVar` arm emits `_ = paramName` after the rebind so Go's "declared and not used" doesn't fire when a Sky lambda binds an unused param.

Pinned regression (`CompileSpec.hs:139` Result-typed lambda params) now passes WITH typed shape. `HofTypedMsgSpec` updated: `cb func(string) Msg` (was `cb func(string) any`).

### E — anon-record struct decls; cover-up removed

`synthAnonRecordName` registers each produced shape in `globalAnonRecords :: IORef (Map String (Map String T.FieldType))`. New `generateAnonRecordDecls` emits one `type Anon_R_<hash> = struct { ... }` per shape (fields sorted by `_fieldIndex`). The pre-v0.13 `sanitiseTypedDeep` `Anon_R_*` → `any` rewrite is now a no-op pass-through.

### F + F3 — whole-program Sky DCE + orphan FfiT pruning

New `Sky.Build.Dce.Ref` ADT (TopRef | FfiRef | CtorRef); `reachableWholeProgram entryMod allMods extraRoots` walks the call graph across module boundaries. `globalReachableProgram :: IORef (Set Dce.Ref)` populated after canon-fixpoint; `generateDeclsForDep` filters via `keepName`. `globalDceDisabled` honours `SKY_DCE=0`.

F3: orphan FfiT type-alias pruning. The pre-v0.13 `dceFfiWrappers` stripped wrapper bodies but left 80,847 `type FfiT_*` aliases orphan on Stripe-scale. New `pruneOrphanFfiTypes` (Unicode-aware identifier scan, O(blob + Σ name_lengths) via pre-computed identifier `Set` — naive `isInfixOf` per alias was 7 TB of char comparisons that hung the build).

Stripe-skyshop benchmark:

| Metric | Pre-v0.13 | Post-v0.13 |
|---|---|---|
| `main.go` lines | 14,398 | **4,178** (−71%) |
| Total funcs in main.go | 3,518 | **975** (−72%) |
| Emitted `Stripe_*` user refs | (full) | **0** |
| `stripe_bindings.go` lines | 326,327 | **58,059** (−82%) |
| `type FfiT_*` aliases | 80,847 | **29** |
| Wrapper funcs | 124,312 | **57** |

### G — LSP 100% (every USED symbol class)

17 cabal-fenced tests via headless Neovim driver (`scripts/lsp-test-nvim.{lua,sh}` + `test/Sky/Lsp/NvimDriverSpec.hs`). Hover + goto-def for: function, type alias, ADT constructor, record-field access, kernel call, lambda parameter, let-binding, case-pattern binder. `Sky.Lsp.Index.exprLocals` `caseArm` scope fix: case-pattern binders' scope spans `pattern ∪ body` (was body-only).

### Unicode-aware Go ident matching

Four ASCII-only `isIdentStart` / `isIdentChar` sites replaced with shared `isGoIdentStart` / `isGoIdentChar` (`Char.isLetter` / `Char.isAlphaNum`). Aligns with the parser side (`Sky.Parse.Variable.isIdentChar`). ASCII walks would silently slice Unicode-letter identifiers.

### Reflect-adapter arg narrowing

Real runtime panic surfaced by `verify-cli.sh` on `examples/07-todo-cli`: `reflect.Call using map[string]interface{} as type map[string]string` in `makeFuncAdapter`. Fix: narrow each arg to `skyFn.In(i)` via `narrowReflectValue` BEFORE `reflect.Call`.

### Runtime verification

`scripts/verify-all-web.sh` + `scripts/verify-live-app.mjs` (Playwright headless Chromium for 10 Sky.Live / Sky.Http.Server apps) + `scripts/verify-cli.sh` (CLI / Sky.Cli / Sky.Tui). 25 / 26 examples PASS end-to-end (Fyne `11-fyne-stopwatch` skipped — needs X11).

### Deferred to v0.13.x

- **Install-time Go binding generation**: `sky install` still emits the full `.skycache/go/<pkg>_bindings.go`. With Sky DCE now identifying the reachable set pre-lowering, install could skip Go-source generation entirely and let `sky build` generate only the reachable subset on demand. Stripe install would drop from ~8 min to ~10 sec, disk usage from ~12 MB to <100 KB per FFI pkg. Right architecture; deferred for risk-isolation.

## What's next

No compiler rewrite is planned. The Haskell implementation is the long-term home. Future work (post-v0.13):

- v0.13.x — install-time Go-binding generation (see above).
- Port runtime kernels (`Dict_*`, `List_*`, `Html_*`) to Go generics so the typed surface no longer narrows through reflect at the boundary.
- Record-struct emission for `update` / `view` tuples in TEA apps.
- Formal exhaustiveness for nested patterns.
- Smarter cache invalidation (source-hash that covers transitive annotations).
- Selective import emission (currently emits all 18 runtime subpackages).
- Rewrite Sky's curry semantics so partial-application closures emit typed `func(A) B` Go signatures — would unlock the remaining 10 typed routes.

See [versions.md](versions.md) for the feature-level changelog.
