# CLAUDE.md

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** No runtime surprises from FFI. No panic leakage. No nil leakage. No partial bindings. All edge cases represented in types.
2. **Dev experience is top priority.** Clear errors, predictable behavior, no user-written FFI, no confusing hidden behavior.
3. **Root-cause fixes only.** Never patch over bugs. Fix at the correct abstraction layer (lexer, parser, type system, lowering, or interop generator).
4. **Production-grade architecture.** Must scale to large Go packages (Stripe SDK). Must support real backend systems. Must remain maintainable.

## Effect Boundary: Task (Result E A)

ALL Go interop and effectful operations MUST be exposed as `Task (Result E A)`. This is the fundamental guarantee that makes Sky pure and reliable.

- **Pure functions** (`String.length`, `List.map`) — no wrapping needed
- **Fallible functions** (`String.toInt`, `Dict.get`) — `Result` or `Maybe`
- **Effectful functions** (`File.readFile`, `Process.run`, `println`) — `Task (Result E A)`
- **Platform entry** (`main`) — returns `Task`, the runtime executes it

### Error mapping rules:
- `(T, error)` → `Result E T`
- `error` → `Result E ()`
- All panics → `Result PanicError`
- nil → `Result NilError` or `Maybe` (explicit only)

No silent fallback values. No effect leakage. No goroutines/channels/mutation exposed to Sky surface.

## Project Overview

Sky is a pure functional language inspired by Elm, compiling to Go. The compiler, CLI, formatter, LSP, and FFI generator are all written in Sky itself (self-hosted). A legacy TypeScript bootstrap compiler is preserved in `ts-compiler/` for reference only.

## Architecture

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
                                                                     ↑ binding index (.idx)
                                                                     ↑ lazy symbol resolution
```

```
src/                              -- Sky compiler (self-hosted)
  Main.sky                        -- Entry point (CLI arg handling)
  Cli.sky                         -- CLI command dispatch
  Compiler/
    Token.sky                     -- Token types and source positions
    Lexer.sky                     -- Indentation-aware tokenizer
    ParserCore.sky                -- Shared parser state, helpers, layout filtering
    Parser.sky                    -- Module/import/declaration/type parsing
    ParserExpr.sky                -- Expression parsing (Pratt-style precedence)
    ParserPattern.sky             -- Pattern parsing
    Ast.sky                       -- AST node definitions
    GoIr.sky                      -- Go Intermediate Representation types
    Emit.sky                      -- Go source code emitter
    Types.sky                     -- HM type system core (Type, Scheme, Substitution)
    Env.sky                       -- Type environment with lexical scoping
    Unify.sky                     -- Robinson unification with occurs check
    Adt.sky                       -- ADT registration and constructor scheme generation
    PatternCheck.sky              -- Pattern type checking and binding extraction
    Infer.sky                     -- Algorithm W type inference
    Exhaustive.sky                -- Exhaustiveness checking for case expressions
    Checker.sky                   -- Module-level type checking orchestration
    Lower.sky                     -- AST → GoIR lowering
    Resolver.sky                  -- Module resolution and stdlib type environment
    Pipeline.sky                  -- Full compilation pipeline orchestration
  Ffi/
    Inspector.sky                 -- Go package inspection (calls go/packages subprocess)
    TypeMapper.sky                -- Go type → Sky type mapping
    BindingGen.sky                -- .skyi binding file generation
    WrapperGen.sky                -- Go wrapper code generation (panic-safe, nil-safe)
  Formatter/
    Doc.sky                       -- Pretty-printer document algebra
    Format.sky                    -- Elm-style formatter
  Lsp/
    JsonRpc.sky                   -- JSON-RPC framing and JSON construction/parsing
    Server.sky                    -- LSP message dispatch and feature handlers
  LspMain.sky                    -- LSP entry point

ts-compiler/                     -- Legacy TypeScript bootstrap compiler (reference only)
examples/                        -- Example projects (01-hello-world through 13-skyshop)
```

## Build

```bash
# Self-compile: the Sky compiler compiles itself
sky build src/Main.sky            # Produces dist/sky
./dist/sky build src/Main.sky     # Self-compiled compiler compiles itself again

# Format
sky fmt src/Main.sky              # Format .sky files (Elm-style)

# Type check
sky check src/Main.sky            # Type-check without compiling

# Clean
sky clean                         # Remove dist/, .skycache/, .skydeps/
```

## Critical Rules

1. **Sky only** — The compiler, LSP, formatter, and FFI generator are written in Sky. No TypeScript/JavaScript in the compilation pipeline.
2. **Indentation parser** — Column of first token = minimum indentation reference. Do not tighten rules that break slightly unaligned input.
3. **Formatter (Elm-style)** — 4-space indent, leading commas, `let`/`in` always multiline, 80-char line width.
4. **Prelude** — `Sky.Core.Prelude` implicitly imported everywhere. Provides `Result`, `Maybe`, `Task`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`.
5. **Go FFI** — All Go interop wrapped in `Task (Result E A)`. Compiler generates panic-safe, nil-safe wrappers. Binding index enables lazy resolution for massive packages. Users never write FFI code.
6. **Type constraints** — `comparable`, `number`, `appendable` enforced during unification.
7. **Pointer safety** — `*primitive` → `Maybe T`. Opaque struct pointers stay as type name. `(T, bool)` → `Maybe T`. `(T, error)` → `Result Error T`.
8. **Pipeline operators** — `|>` and `<|` (Elm-style). `::` (cons). `/=` (not-equal). `//` (integer division).
9. **Task execution** — `main` returns `Task`. The compiler generates the Go executor. All IO goes through `Task`.
10. **Go reserved words** — `sanitizeGoIdent` appends `_` to clashing identifiers.

## Interop Model

### Golden Rule: ALL Go interop → Task (Result E A)

The compiler owns the entire boundary layer:

1. **Inspect** — `go/packages` + `go/types` via Go subprocess
2. **Classify** — pure / fallible / effectful
3. **Generate Sky API** — always `Task (Result E A)` for effectful
4. **Generate Go wrapper** — panic-safe (`recover`), nil-safe, type-safe

### Forbidden:
- Raw Go types exposed to Sky surface (`[]T`, `map`, pointers, interfaces)
- Functions returning plain values if they are effectful
- Silent fallback values
- Runtime-only fixes for compile-time problems
- Escape hatches that bypass the type system

## Package Management

### sky.toml
```toml
name = "my-project"
version = "0.1.0"
entry = "src/Main.sky"
bin = "dist/app"

[source]
root = "src"

[dependencies]
"github.com/someone/sky-utils" = "latest"

[go.dependencies]
"github.com/google/uuid" = "latest"
```

## Language Syntax (Elm-like)

```elm
module Main exposing (main)

import Sky.Core.Task as Task
import Sky.Core.File as File
import Std.Log exposing (println)

main : Task (Result String ())
main =
    File.readFile "hello.txt"
        |> Task.andThen (\content ->
            println content
        )
```

## Sky.Live

Sky.Live is the server-driven UI framework. HTTP-first with SSE/polling for live updates. Session stores: memory, SQLite, Redis, PostgreSQL, Firestore. Config via `sky.toml [live]` section, overridable by env vars.
