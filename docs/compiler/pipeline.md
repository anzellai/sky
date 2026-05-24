# Compilation pipeline

> **v0.15 state**: type-directed lowering throughout — solver writes
> per-region types and `LowerCtx` threads the expected Go type through
> the lowerer for lambdas, record-field inits, list elements, and call
> args. Go generics on parametric record aliases. Same-module
> polymorphic call re-instantiation. Layer-3 stdlib, whole-program DCE
> (Stripe-SDK scale: −82 % source), LSP 100 % coverage; runtime
> verification across all 27 examples (120 stdlib assertions + 306
> cabal specs). See [`versions.md`](versions.md) for the changelog.


Step-by-step trace of what happens when you run `sky build src/Main.sky`.

```
source (*.sky)
   │
   ▼  Sky.Parse.Lexer + layout filter
tokens
   │
   ▼  Sky.Parse.{Module, Expr, Pattern}
parsed AST
   │
   ▼  Sky.Canonicalise.Module
canonical AST        ◄── exposing / import validation
   │
   ▼  Sky.Type.Constraint + Sky.Type.Solve
typed AST            ◄── HM inference, substitutions applied
   │                 ◄── Sky.Type.Exhaustiveness (pattern checker)
   │
   ▼  Sky.Build.Compile.exprToGo* family
Go IR
   │
   ▼  Sky.Generate.Go.Ir printer
sky-out/main.go
   │
   ▼  copyFfiDir + copyRuntime
sky-out/rt/*.go
   │
   ▼  dceFfiWrappers
sky-out/rt/*_bindings.go (pruned)
   │
   ▼  go build -o sky-out/app
binary
```

## Phase 0 — registry load

`Sky.Build.Compile.compile` first loads the FFI registry:

- `.skycache/ffi/*.kernel.json` → `FfiRegistry` (function names + arities per kernel).
- `.skycache/go/*.go` is scanned for `^func Go_X_yT(` definitions → populates `ffiTypedWrapperNamesRef`. Call-site codegen consults this set to pick the typed variant when available.

## Phase 1 — module graph

`Sky.Build.ModuleGraph.build` walks the entry module's imports, recursively resolving each to a source path under `src/`, `sky-stdlib/` (embedded), or `.skydeps/<pkg>/src/`.

Every module gets a `ModuleInfo` record with:
- `_mi_path` — file location
- `_mi_deps` — dependency names
- `_mi_hash` — source content hash (for incremental caching)

## Phase 2 — per-module parse + canonicalise

For each module in topological order:

1. `readFile` the source.
2. `Sky.Parse.Lexer.tokenise` runs the lexer, then a layout filter inserts virtual `{` `;` `}` tokens based on indentation.
3. `Sky.Parse.Module.parseModule` produces a parsed AST.
4. `Sky.Canonicalise.Module.canonicalise` resolves names, validates imports against the exporter's `exposing` clause, records ADT constructors in the environment, and elaborates record-type constructors.

## Phase 3 — type check

`Sky.Type.Constraint.generate` produces a constraint tree for the module's declarations.

`Sky.Type.Solve.solve` unifies constraints via Union-Find, producing `SolvedTypes :: Map String Type`.

`Sky.Type.Exhaustiveness.check` walks every `case` expression and every top-level pattern. Missing ADT ctors, missing `True`/`False` in boolean matches, or literal-only patterns without a wildcard are build errors.

## Phase 4 — lowering to Go IR

`Sky.Build.Compile.exprToGo` and its typed variant `exprToGoTyped` walk the canonical expression tree, producing `GoIr.GoExpr` nodes. As of v0.15 the lowerer is **type-directed**: the solver writes a per-region type map (`globalRegionTypes`) and `LowerCtx` threads the expected Go type down through `exprToGoExpectGo`, so sub-expressions at lambda bodies, record-field inits, list elements, and call args lower with the slot's typed Go form propagated.

Key transformations:
- **ADT construction** — `Some x` → `rt.SkyADT{Tag: 0, SkyName: "Some", V0: x}` (or typed struct when `P4` record alias applies).
- **Pattern match** — nested case expressions generate `__subject_N` temporaries per nesting level; each arm becomes a chain of tag checks + field binds.
- **Pipeline** — `x |> f y` lowers through `Can.Call` so it participates in typed FFI dispatch.
- **Kernel call** — `String.toUpper s` routes through the typed companion when `String_toUpperT(s string) string` is known and the arg type is primitive. See `typedKernelArgCoerce` in `Compile.hs`.
- **FFI call** — `Uuid.newString ()` routes to `rt.Go_Uuid_newStringT()` when the wrapper is in the typed set.
- **Parametric record aliases** — `type alias Cfg msg = { onSubmit : msg, ... }` emits `type Cfg_R[T1 any] struct { OnSubmit T1; ... }` with per-instance type args (`Cfg_R[Msg]`, `Cfg_R[Int]`). Callback fields keep their typed callee parameter — no more `func(any) any` fallback at parametric-record slots.
- **Same-module polymorphic re-instantiation** — sibling refs to a polymorphic annotated TypedDef alpha-rename per call site, so `f : Cfg msg -> msg` called with `msg=Int` AND `msg=Bool` in the SAME module both type-check.

## Phase 5 — Go emission

`Sky.Generate.Go.Ir.printGo` walks the Go IR and prints valid Go source to `sky-out/main.go`.

Record types referenced anywhere in the module are emitted as top-level `type Foo_R struct { ... }` declarations.

## Phase 6 — runtime + wrapper copy

`copyEmbeddedRuntime` writes the embedded `runtime-go/rt/*.go` files into `sky-out/rt/`. `copyFfiDir` copies `.skycache/go/*.go` into the same directory. Both are gitignored — regenerable on every build.

## Phase 7 — FFI DCE

`dceFfiWrappers` scans `sky-out/main.go` + every non-rt Go file under `sky-out/` for `rt.Go_X_y(` references. It then rewrites each `sky-out/rt/*_bindings.go` keeping only the reachable wrapper bodies. Stripe's ~74k wrappers shrink to a few dozen per project.

## Phase 8 — `go build`

The Haskell entry point shells out to `go build -o sky-out/app .` inside `sky-out/`. If Go reports an error, the whole command fails — this is the final correctness gate.

## Incremental compilation

Each module's lowered output is cached in `.skycache/lowered/<module-hash>.cbor`. On subsequent builds, `Compile` compares the current source hash to the cached one and reuses the lowered output if they match, skipping parse, canonicalise, and type-check for unchanged modules.

Invalidation is per-module — changes to a dependency invalidate dependents transitively.
