# CLAUDE.md

## Language Convention

All documentation, comments, variable names, function names, and user-facing strings in the Sky project **must use British English spelling**. Examples: `optimise` not `optimize`, `behaviour` not `behavior`, `colour` not `color`, `initialise` not `initialize`, `serialise` not `serialize`, `catalogue` not `catalog`.

Exceptions: protocol identifiers (LSP `initialize`), CSS/HTML property names (`color`, `text-align: center`), and Go standard library names which follow American conventions.

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** No runtime surprises from FFI. No panic leakage. No nil leakage. No partial bindings. All edge cases represented in types.
2. **Dev experience is top priority.** Clear errors, predictable behaviour, no user-written FFI, no confusing hidden behaviour.
3. **Root-cause fixes only.** Never patch over bugs. Fix at the correct abstraction layer (lexer, parser, type system, lowering, or interop generator).
4. **Production-grade architecture.** Must scale to large Go packages (Stripe SDK). Must support real backend systems. Must remain maintainable.

## Effect Boundary: Task

ALL effectful operations flow through `Task`. This is the fundamental guarantee that makes Sky pure and reliable.

- **Pure functions** (`String.length`, `List.map`, `Math.sqrt`) — no wrapping needed
- **Fallible functions** (`String.toInt`, `Dict.get`) — `Result` or `Maybe`
- **Effectful functions** (`File.readFile`, `Http.get`, `println`) — `Task String a`
- **Platform entry** (`main`) — may return `Task`; the runtime auto-executes it

```elm
-- Task composition with pipeline operators
main =
    Task.succeed "Sky"
        |> Task.andThen (\name -> Task.succeed ("Hello, " ++ name ++ "!"))
        |> Task.map (\msg -> msg ++ " Pure and reliable.")
        |> Task.perform
```

### Error mapping at FFI boundary:
- Go `(T, error)` → `Result String T`
- Go `error` → `Result String ()`
- Go panics → caught by `sky_runTask`, converted to `Err`
- Go nil → `Maybe` or `Result`

## Project Overview

Sky is a pure functional language inspired by Elm, compiling to Go. The compiler, CLI, formatter, LSP, and FFI generator are all self-hosted — written in Sky itself, compiled to a ~4MB native Go binary. Zero Node.js/TypeScript/npm dependencies.

## Architecture

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
                                                                     ↑ binding index (.idx)
                                                                     ↑ lazy symbol resolution
```

```
src/                              -- Sky compiler (self-hosted, 34 modules)
  Main.sky                        -- CLI entry point (build/run/check/fmt/add/install/update/upgrade/lsp/clean)
  Compiler/                       -- 21 modules: lexer, parser, type checker, lowerer, emitter
  Ffi/                            -- 4 modules: Go package inspector, binding/wrapper generator, type mapper
  Formatter/                      -- 2 modules: pretty-printer + Elm-style formatter
  Lsp/                            -- 2 modules: JSON-RPC + LSP server

ts-compiler/                      -- Legacy TypeScript bootstrap (reference only, not used)
stdlib-go/                        -- Go runtime implementations for stdlib modules
examples/                         -- 15 example projects
```

## Shell Commands

Always use `-f` flag with `rm` and `cp` commands (e.g. `rm -f`, `rm -rf`, `cp -f`) to avoid interactive confirmation prompts that block execution.

## Build & Test

```bash
sky build src/Main.sky            # Compile Sky → Go binary (sky-out/app)
sky build examples/01-hello-world/src/Main.sky   # Compile any project
sky run src/Main.sky              # Build and run
sky check src/Main.sky            # Type-check without compiling
sky fmt src/Main.sky              # Format (Elm-style: 4-space, leading commas)
sky add github.com/some/package   # Add Go or Sky dependency + generate bindings
sky install                       # Install all deps + auto-generate missing bindings
sky update                        # Update sky.toml dependencies to latest
sky upgrade                       # Self-upgrade to latest release
sky lsp                           # Start Language Server (JSON-RPC over stdio)
sky clean                         # Remove sky-out/ dist/
sky --version                     # sky v0.6.0
```

## Standard Library

### Pure Functions (no Task)
| Module | Key Functions |
|--------|--------------|
| `Sky.Core.String` | split, join, replace, trim, contains, startsWith, toInt, fromInt, slice, length |
| `Sky.Core.List` | map, filter, foldl, foldr, head, take, drop, sort, zip, concat, filterMap |
| `Sky.Core.Dict` | empty, insert, get, remove, keys, values, map, foldl, union, member |
| `Sky.Core.Set` | empty, insert, remove, member, union, diff, intersect, fromList |
| `Sky.Core.Maybe` | withDefault, map, andThen |
| `Sky.Core.Result` | withDefault, map, andThen, mapError |
| `Sky.Core.Math` | sqrt, pow, abs, floor, ceil, round, sin, cos, pi, min, max |
| `Sky.Core.Regex` | match, find, findAll, replace, split |
| `Sky.Core.Crypto` | sha256, sha512, md5, hmacSha256 |
| `Sky.Core.Encoding` | base64Encode/Decode, urlEncode/Decode, hexEncode/Decode |
| `Sky.Core.Char` | isUpper, isLower, isDigit, isAlpha, toUpper, toLower |
| `Sky.Core.Path` | join, dir, base, ext, isAbsolute |
| `Sky.Core.Json.Decode` | decodeString, string, int, float, bool, list, field, map, andThen |
| `Sky.Core.Json.Encode` | encode, string, int, float, bool, list, object |

### Task-Wrapped Effects
| Module | Key Functions | Returns |
|--------|--------------|---------|
| `Sky.Core.Task` | succeed, fail, map, andThen, perform, sequence | Task err a |
| `Sky.Core.File` | readFile, writeFile, mkdirAll, readDir, exists | Task String a |
| `Sky.Core.Process` | run, exit, getCwd, loadEnv | Task String a |
| `Sky.Core.Io` | readLine, readBytes, writeStdout, writeStderr | Task String a |
| `Sky.Core.Time` | now, unixMillis, sleep | Task String Int |
| `Sky.Core.Http` | get, post, request | Task String Response |
| `Sky.Core.Random` | int, float, choice, shuffle | Task String a |
| `Sky.Http.Server` | listen, get/post/put/delete routes, middleware | Task String () |

### Prelude (implicitly imported everywhere)
`Result (Ok/Err)`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`

## Go FFI / Interop Model

### Golden Rule: Users never write FFI code

The compiler owns the entire boundary:
1. `sky add github.com/some/package` — auto-detect Go vs Sky package
2. Inspector subprocess runs `go/packages` + `go/types` to extract ALL exported types, fields, methods
3. Compiler classifies each function: pure / fallible / effectful
4. Generates `.skyi` binding file + Go wrapper with panic recovery
5. Dead code elimination strips unused wrappers from final build
6. `sky install` auto-scans source for FFI imports and generates missing bindings

### Type Mapping
| Go | Sky |
|----|-----|
| `string` | `String` |
| `int`, `int64` | `Int` |
| `float64` | `Float` |
| `bool` | `Bool` |
| `error` | `Result String a` |
| `(T, error)` | `Result String T` |
| `(T, bool)` | `Maybe T` |
| `*string`, `*int` | `Maybe String`, `Maybe Int` |
| `*sql.DB` | `Db` (opaque) |
| `[]T` | `List T` |

## Sky.Live

Server-driven UI framework with Elm TEA architecture:

```elm
main =
    Live.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/about" AboutPage ]
        , notFound = HomePage
        }
```

- **HTTP-first** — full HTML on first load, patches on events
- **SSE subscriptions** — real-time updates via `Time.every`
- **Session stores** — memory, SQLite, Redis, PostgreSQL, Firestore
- **Type-safe events** — `onClick Increment`, `onInput SetName`
- **Automatic VNode diffing** — only changed attributes/text sent as patches
- **Security** — cookie validation, rate limiting, body size limits, CORS

### Sky.Http.Server (Sky.Live foundation)

```elm
main =
    Server.listen 8080
        [ Server.get "/" (\_ -> Task.succeed (Server.text "Hello!"))
        , Server.get "/api/users/:id" getUser
        , Server.post "/api/data" handlePost
        , Server.static "/assets" "./public"
        ]
```

- Composable routes with `get/post/put/delete/any`
- Route groups with shared prefix
- Cookie support (HttpOnly, Secure, SameSite)
- Request extractors: `param`, `queryParam`, `header`, `getCookie`
- Response builders: `text`, `json`, `html`, `withStatus`, `redirect`
- Middleware: `Handler -> Handler` function composition

## Language Syntax (Elm-compatible)

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Log exposing (println)

type Msg = Increment | Decrement

update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment -> count + 1
        Decrement -> count - 1

main =
    let
        result = update Increment 0
    in
    println (String.fromInt result)
```

### Key Syntax
- `|>` `<|` pipeline operators
- `::` cons (patterns + expressions)
- `\x -> x + 1` lambdas
- `let ... in ...` local bindings
- `case x of ...` pattern matching with exhaustiveness checking
- `{ record | field = value }` record update
- `module M exposing (..)` / `import M as Alias exposing (func)`

## Examples

| # | Name | Description |
|---|------|-------------|
| 01 | hello-world | Basic println |
| 02 | go-stdlib | Go stdlib usage (crypto, encoding, time, http) |
| 03 | tea-external | TEA with external packages (UUID, godotenv) |
| 04 | local-pkg | Multi-module project with local imports |
| 05 | mux-server | HTTP server with gorilla/mux |
| 06 | json | JSON encoding/decoding (Elm-compatible API) |
| 07 | todo-cli | SQLite CLI todo app |
| 08 | notes-app | Full CRUD web app with database |
| 09 | live-counter | Sky.Live counter with SSE subscriptions |
| 10 | live-component | Sky.Live component protocol |
| 11 | fyne-stopwatch | Desktop GUI with Fyne toolkit |
| 12 | skyvote | Full Sky.Live voting app with auth |
| 13 | skyshop | E-commerce: Stripe, Firebase, i18n |
| 14 | task-demo | Task effect boundary demonstration |
| 15 | http-server | Sky.Http.Server with routing + cookies |

## Compiler Optimisation Strategy (keep up to date)

**This section must be kept current.** Any session that changes the compiler pipeline, codegen, or build system must update this section to reflect the new state. This prevents regressions and gives future sessions full context.

### Current Optimisations (implemented)

1. **Stale file cleanup** (`Pipeline.sky:compile`) — `rm -f sky-out/sky_ffi_*.go sky-out/sky_*.go` at start of every build prevents cross-project pollution when `sky-out/` is reused.

2. **Empty wrapper deletion** (`Pipeline.sky:trimWrapperFile`) — when wrapper DCE eliminates ALL functions from an FFI wrapper file, the file is deleted entirely instead of leaving import-only stubs that cause Go build failures.

3. **Main.go function-level DCE** (`Pipeline.sky:dceMainGo`) — after emitting Go code, performs transitive reachability analysis from `main()` plus FFI wrapper references plus var/header references. Unreachable `sky_*` helper functions are removed. `goimports` cleans unused imports. Result: hello-world 613→111 lines, Go build 3.4x faster.

4. **Var declaration preservation** — DCE separates `var` declarations from `func` blocks before analysis. All vars are preserved (they may be type constructors or FFI aliases). Only unreachable functions are eliminated.

5. **Import-keeper removal** (`Lower.sky`) — removed all `var _ = pkg.Symbol` declarations that forced unused Go imports. `goimports` now handles import management.

6. **Large .skyi filtering** (`bin/skyi-filter` + `Pipeline.sky:loadOneFfiBinding`) — for binding files >10KB, runs external Go tool that precomputes used `Alias.funcName` set via regex, then streams the .skyi keeping only header + types + used declarations. Stripe SDK: 147K→9K lines in 90ms.

7. **Wrapper goimports** (`Pipeline.sky:eliminateDeadCode`) — runs `goimports -w` on all remaining wrapper files after DCE to fix unused imports from partial function elimination.

### Known Issues (to fix)

1. **Formatter↔compiler compat** — `sky fmt` on `Pipeline.sky` produces code the self-hosted compiler cannot re-compile. Root cause: the lowerer generates incorrect Go for certain reformatted patterns. Do NOT format `Pipeline.sky` until this is fixed.

2. **Lowerer limitation with new functions** — adding functions with nested `case` inside `case` inside `let` in Pipeline.sky can cause the lowerer to generate blank function names (`Compiler_Pipeline__`). Workaround: keep complex logic in external tools or inline it.

3. **Skyshop duplicate wrappers** — both `sky_wrappers/` and `sky_ffi_*.go` generate overlapping `Sky_*` functions for the same package. Needs deduplication logic.

4. **JSON decoding runtime errors** — examples/06-json decoding tests (map2, pipeline, optional fields, nested at, list of objects, oneOf) produce runtime errors. Likely a codegen or runtime issue in the JSON decoder helpers.

5. **FFI Task boundary** — pure Go functions (`time.Now`, `os.Getenv`, `fmt.Sprint`) return plain values, not `Task`. Violates Sky's effect boundary principle. Classification is in `Ffi/WrapperGen.sky` lines 50-110.

### Techniques from TS Compiler (to port)

The TypeScript compiler (`ts-compiler/`) achieved fast builds through techniques not yet ported to the self-hosted compiler:

1. **Symbol-level tree-shaking** — wrapper generation is pre-filtered to only symbols referenced during lowering (`collectedWrapperSymbols` set). The self-hosted compiler generates all wrappers then DCEs.

2. **Selective import emission** — the TS lowerer scans all Go declarations and only emits imports for detected modules. The self-hosted compiler emits all 17 stdlib imports unconditionally (`makeGoPackage`).

3. **.skyi modules not lowered** — TS compiler uses .skyi for type info only; wrappers are generated separately from InspectResult. The self-hosted compiler parses .skyi into full AST modules.

4. **`-gcflags="all=-l"`** — disables Go inlining for faster compilation. Not yet used in self-hosted build step.

5. **Disk caching** — `.skydeps/.sky_export_cache.json` persists type exports across runs. No equivalent in self-hosted compiler.
