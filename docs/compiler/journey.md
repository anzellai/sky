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

## What's next

No compiler rewrite is planned. The Haskell implementation is the long-term home. Future work is:

- Port runtime kernels (`Dict_*`, `List_*`, `Html_*`) to Go generics so the typed surface no longer narrows through reflect at the boundary.
- Record-struct emission for `update` / `view` tuples in TEA apps.
- Formal exhaustiveness for nested patterns.
- Smarter cache invalidation (source-hash that covers transitive annotations).
- Selective import emission (currently emits all 18 runtime subpackages).

See [versions.md](versions.md) for the feature-level changelog.
