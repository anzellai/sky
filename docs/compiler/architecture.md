# Compiler architecture

The Sky compiler is a Haskell program that reads Sky source and emits Go. It has no external dependencies beyond GHC 9.4+ and `cabal`.

## Source tree

```
src/
    Sky/
        Parse/              -- lexer, layout filter, parser
            Lexer.hs
            Token.hs
            Module.hs       -- module/import/export parsing
            Pattern.hs
            Expr.hs
        Canonicalise/       -- name resolution, AST enrichment
            Module.hs
            Environment.hs
            FreeVars.hs
        Type/               -- Hindley-Milner type system
            Type.hs         -- types, schemes, substitutions
            Constraint.hs   -- constraint tree
            Solve.hs        -- constraint solver (UF-based)
            Unify.hs
            Exhaustiveness.hs
        Build/              -- orchestration
            Compile.hs      -- top-level pipeline
            ModuleGraph.hs  -- dep resolution
            FfiRegistry.hs  -- reads .skycache/ffi/*.kernel.json
            FfiGen.hs       -- emits typed FFI wrappers
            SkyDeps.hs      -- Sky-source dep resolution
        Generate/
            Go/             -- Go IR + printer
                Ir.hs
                Record.hs
        Lsp/                -- LSP server
            Server.hs
            Index.hs
            Handlers.hs
        Format/             -- opinionated formatter (Elm-compatible output)
            Format.hs
app/
    Main.hs                 -- CLI entry point
runtime-go/rt/              -- Go runtime (embedded via Template Haskell)
sky-stdlib/                 -- Sky-side stdlib (embedded)
tools/sky-ffi-inspect/      -- Go package introspector
```

## Module phase data

Each source module passes through four representations, each a distinct Haskell type:

1. **Parsed** — surface AST, qualified names unresolved.
2. **Canonical** — names resolved to their defining module, imports validated against `exposing` lists.
3. **Typed** — every binding has an inferred scheme; patterns are exhaustiveness-checked.
4. **Lowered** — Sky AST → Go IR. Closures, pattern matches, ADTs, and tuples are elaborated.

Each transformation is a pure Haskell function. Phase data types are distinct so a canonical expression cannot accidentally flow into a lowering function.

## Pipeline orchestrator

`Sky.Build.Compile.compile` is the top-level function. It:

1. Reads `sky.toml`.
2. Loads the FFI registry (`.skycache/ffi/*.kernel.json` into `FfiRegistry`).
3. Scans `.skycache/go/*.go` for typed wrapper names (`Go_X_yT`) — used for call-site dispatch.
4. Installs Sky source deps (`SkyDeps.installDeps`).
5. Builds the module graph from the entry file.
6. Parses, canonicalises, and type-checks each module in dependency order. Each module's lowered Go is cached in `.skycache/lowered/` keyed by source hash.
7. Emits the root module's Go to `sky-out/main.go`.
8. Copies the embedded runtime (`runtime-go/rt/*.go`) and user FFI wrappers (`.skycache/go/*.go`) into `sky-out/rt/`.
9. Runs build-time DCE (`dceFfiWrappers`) — strips unused wrapper bodies from the copied bindings files.

See [pipeline.md](pipeline.md) for a step-by-step trace.

## Runtime model

The Sky runtime (`runtime-go/rt/`) lives entirely in Go. The compiler embeds it via `Template Haskell` (`Sky.Build.EmbeddedRuntime`) and copies the files into every project's `sky-out/rt/` at build time. Contributors updating runtime files don't need to re-run `cabal build` unless they add or remove files — `EmbeddedRuntime.hs` carries a version comment used to force TH rebuilds.

Key runtime pieces:

- `SkyResult[E, A]`, `SkyMaybe[A]`, `SkyTask[E, A]` — the three effect types.
- `SkyADT` — untyped ADT carrier (tag + named constructor + field slice).
- `SkyTuple2` (`{V0, V1}`), `SkyTuple3` (`{V0, V1, V2}`), `SkyTupleN` (slice-backed, arity ≥ 4) — the three tuple runtime structs `Sky.Generate.Go.Type.typeToGo` lowers to.
- `SkyFfiRecover` / `SkyFfiRecoverT` — panic recovery at the FFI boundary.
- `SkyCall`, `sky_call2/3/4/5` — apply closures with variable arity.
- `ResultCoerce` / `MaybeCoerce` — reflect-fallback type conversion (used only when a case subject's concrete type can't be determined at lower time).

## FFI pipeline

1. User runs `sky add <package>`.
2. `FfiGen.runInspector` spawns `tools/sky-ffi-inspect` on the fetched Go module.
3. Inspector emits JSON describing every public function, type, and struct field.
4. `FfiGen.generateBindings` classifies each binding (`DirectCall` / `ReflectTopLevel` / `ReflectGeneric` / `ReflectMethod` / field accessor / field setter / pkg-level var) and emits:
   - `.skycache/ffi/<slug>.skyi` — Sky interface signatures.
   - `.skycache/ffi/<slug>.kernel.json` — lightweight registry used by the canonicaliser.
   - `.skycache/go/<slug>_bindings.go` — typed Go wrappers (`Go_X_y` + `Go_X_yT` pairs).

See [../ffi/ffi-design.md](../ffi/ffi-design.md) for the classification logic and wrapper shape.
