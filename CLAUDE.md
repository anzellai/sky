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

3. **Native DCE tool** (`bin/sky-dce` + `Pipeline.sky:runNativeDce`) — compiled Go tool performs both wrapper DCE and main.go DCE in a single pass. Uses native `strings.Contains` (no `any` boxing) for O(n²) reachability analysis. Replaces the Sky-based DCE which took 27s with a 1s native implementation. Wrapper DCE: extracts function names from wrapper files, checks references in main.go, removes unused functions. Main.go DCE: BFS reachability from `main()` + wrapper seeds + var/header seeds. Falls back to Sky-based DCE if `sky-dce` not on PATH.

4. **Var declaration preservation** — DCE separates `var` declarations from `func` blocks before analysis. All vars are preserved (they may be type constructors or FFI aliases). Only unreachable functions are eliminated.

5. **Import-keeper removal** (`Lower.sky`) — removed all `var _ = pkg.Symbol` declarations that forced unused Go imports. `goimports` now handles import management.

6. **Large .skyi filtering** (`bin/skyi-filter` + `Pipeline.sky:loadOneFfiBinding`) — for binding files >10KB, runs external Go tool that precomputes used `Alias.funcName` set via regex, then streams the .skyi keeping only header + types + used declarations. Stripe SDK: 147K→9K lines in 90ms.

7. **Wrapper goimports** (`Pipeline.sky:eliminateDeadCode`) — runs `goimports -w` on all remaining wrapper files after DCE to fix unused imports from partial function elimination. Now handled by `bin/sky-dce` native tool.

### Known Issues (to fix)

1. **Formatter↔compiler compat** — FIXED. All 30 modules format, compile, and self-host. Formatting is idempotent (running `sky fmt` twice produces identical output). Six fixes in Format.sky: (a) `getLexemeAt1` field access, (b) annotation-function pairing via `formatDeclPairs`, (c) flat `else if` chains via `isExprIf`, (d) record field layout on indented new line, (e) `formatCall` with `align` instead of `indent`, (f) `quoteString` identity (AST stores raw escaped strings, no re-escaping needed).

2. **Lowerer limitation with new functions** — PARTIALLY FIXED. (a) Nested `case` inside `case` inside `let` — uses IIFEs (anonymous function literals), no blank function names observed. Works correctly for all 30 compiler modules. (b) ADT constructor sub-pattern matching — FIXED. `patternToCondition` now recursively checks sub-patterns of constructors via `subPatternConditions`. e.g. `Cons head Nil` generates `SkyName == "Cons" && V1.SkyName == "Nil"` instead of just `SkyName == "Cons"`.

12. **Parser: `getLexemeAt1` field access** — FIXED. `(peekAt 1 state).lexeme` returned the full token object instead of `.lexeme` string because the lowerer dropped field access on parenthesised expressions. Fixed by using a let-binding. This caused `type alias` declarations to be silently skipped during parsing.

13. **Lowerer: cons pattern `x :: []` matching** — FIXED. The lowerer was generating `len > 0` for ALL `PCons` patterns, causing `single :: []` to match any non-empty list instead of exactly 1-element lists. This broke `classifyFunc` in WrapperGen (fallible functions misclassified as effectful). Fix: recursively check tail pattern via `patternToCondition` and generate `len == N` for fixed-length cons patterns.

3. **FFI .skycache path resolution** — FIXED. `resolveBindingPath` now accepts a `projectRoot` parameter derived from `dirOfPath srcRoot`. Binding files and wrappers are resolved relative to the project root, not CWD. `copyOneFfiWrapper` falls back to `.skycache/go/wrappers/` (combined wrapper directory). Removed stale `dist/sky_wrappers` lookup. `sky install` reads `sky.toml` `[go.dependencies]` for correct Go import paths. FFI binding declarations are filtered to remove functions without matching wrappers via `filterFfiModule`/`declHasWrapper`.

4. **FFI Task boundary** — FIXED. All Go FFI calls wrapped in Task with panic recovery.

5. **Go generics** — FIXED. Inspector detects `hasTypeParams`, generic functions/methods/types filtered out.

6. **FFI keyword conflicts** — FIXED. BindingGen skips Go functions named `type`, `module`, `import` etc.

7. **`sky fmt --stdin`** — DONE. Formatter reads stdin, writes to stdout. Helix auto-format enabled.

8. **Missing stdlib functions** — FIXED. Added `List.sortBy`, `modBy` (prelude), `clamp`, `Io.readAllStdin`.

9. **Lowerer: tuple pattern destructuring in lambdas** — FIXED. `lambdaTupleBindings` extracts `V0`/`V1` fields from `SkyTuple2`/`SkyTuple3` before the lambda body.

10. **FFI binding gaps** — PARTIALLY FIXED. Removed blanket `[` + `]` type rejection that blocked `[]` slice types from reaching the proper slice handler. Slice-of-pointer patterns like `[]*pkg.Type` now pass through to the existing slice handler in both BindingGen and WrapperGen. Some complex Firestore/Stripe methods may still be filtered by other type checks.

11. **Skyshop build time ~1:35** — MOSTLY FIXED. Native DCE tool (`bin/sky-dce`) reduced DCE from 27s to 1s. Warm build: 50s → 15.5s. Cold build: 2:56 → 17s. Remaining time: local module loading (3.8s), dep lowering (5.9s), entry emit (2.9s), Go build (~3s). Full symbol-level tree-shaking during lowering not yet ported from TS compiler.

14. **Lowerer: string pattern matching double-quoting** — FIXED. `literalCondition` in Lower.sky called `goQuote` on `LitString` values that already include surrounding quotes from the lexer. This double-quoted strings in pattern match conditions, causing ALL string `case` branches to fail silently and fall through to wildcards. Fix: use the `LitString` value directly since it's already a valid Go string literal. Impact: 253 string pattern matches in skyshop (translations) now work correctly.

15. **Lowerer: local variable shadowing by exposedStdlib** — FIXED. `lowerIdentifier` checked `ctx.exposedStdlib` BEFORE checking if a name was a local variable/parameter. Variables named `title`, `lang`, `content`, `body` were resolved to HTML attribute functions from `Std.Html.Attributes` instead of local bindings. Fix: check `ctx.paramNames` before `exposedStdlib` lookup. Impact: product titles, language selections, translations, and page content now render correctly instead of showing Go function pointer addresses.

17. **Lowerer: hardcoded `Css.` prefix intercepts import aliases** — FIXED. `lowerQualified` checked `String.startsWith "Css." qualName` before checking `importAliases`. When a project imports `Tailwind.Internal.Css as Css`, calls like `Css.allRules` were lowered to `sky_cssPropFn("all-rules")` (Std.Css property) instead of `Tailwind_Internal_Css_AllRules()`. Fix: skip the hardcoded `Css.` check when `Css` is in `importAliases`. Impact: Tailwind CSS `<style>` tag now renders CSS rules instead of Go function pointer addresses.

16. **Lowerer: let-binding hoisting (bootstrapping)** — RESOLVED after 3-round bootstrap from v0.6.9. The `paramNames` tracking changes in Lower.sky (adding bound names from `lowerLet` and pattern vars from `emitBranchCode`) are now fully propagated. New functions with complex let bindings compile correctly. Remaining limitation: never write nested `case` inside a `case` branch — the parser's layout rules nest subsequent branches inside inner case expressions. Always extract inner cases to helper functions.

18. **Type checker — working** (v0.7.2). Root cause of non-working type system: parser layout rules nest case branches inside inner case expressions at same indentation. Fixed by extracting inner cases to helpers across Types.sky (`applySub`, `formatType`), Unify.sky (`unifyFun`, `unifyApp`), Infer.sky (`inferExpr` — all 13 expression branches), Adt.sky (`resolveTypeExpr`). Type errors now caught at compile time: `sky check` reports errors, `sky build` stops on errors, LSP shows red errors in editors.

19. **WrapperGen: IIFE missing invocation** — FIXED. `wrapFallibleReturn` and `wrapEffectfulReturn` generated `func() any { ... }` without trailing `()`. Effectful FFI calls (Os.getenv, Http.get, etc.) returned unevaluated closures instead of Result values. Fix: add `()` to invoke IIFEs. Existing projects must regenerate wrappers (`sky add <pkg>` or `sky install`).

### Techniques from TS Compiler (to port)

The TypeScript compiler (`ts-compiler/`) achieved fast builds (~2-3s first build, ~500ms incremental) through techniques not yet ported:

1. **Symbol-level tree-shaking during lowering** — the TS lowerer collects `Sky_*` wrapper references into a `collectedWrapperSymbols` set AS it generates Go code. Wrappers are then filtered via `filterInspectResult()` to only generate code for referenced symbols. Impact: Stripe SDK 40K symbols → ~50 wrappers. The self-hosted compiler generates ALL wrappers then DCEs 99.8% of them.

2. **Selective import emission** — the TS lowerer scans emitted GoIR for `GoSelectorExpr`/`GoRawExpr` references and only emits imports for detected packages. The self-hosted compiler emits all 17 stdlib imports unconditionally in `makeGoPackage`.

3. **.skyi for types only, not lowered** — TS compiler uses .skyi modules purely for type information during type-checking. They are NOT lowered to Go code. Wrappers come from a separate generation step using InspectResult. The self-hosted compiler parses .skyi into full AST modules and lowers them.

4. **`-gcflags="all=-l"`** — disables Go inlining for faster compilation. Not yet used in self-hosted build step (`Main.sky` line ~116).

5. **Multi-level caching** — TS compiler has 4 cache levels: (a) in-memory type-check cache, (b) disk export cache (`.skydeps/.sky_export_cache.json`), (c) inspector cache (`.skycache/go/inspect.json`), (d) wrapper generation cache. Cold LSP start: 38s → 2s.

6. **go.mod/go.sum preservation** — TS compiler preserves these across rebuilds, only deleting `.go` files. Allows Go's incremental build to reuse compiled object files. Self-hosted compiler's stale cleanup removes everything.

7. **Single-pass emission** — imports tracked during lowering (not post-hoc scanning). No second pass over generated Go needed.

### Priority Optimisation Roadmap

**P0 — Biggest impact for skyshop (<15s goal):**
- DONE: Native DCE tool (`bin/sky-dce`) — 27s → 1s DCE, warm build 50s → 16s
- DONE: Package-level wrapper filtering (skip entire unused FFI packages)
- DONE: Removed debug prints from `compileDependencyModule` (57 println calls per build)
- DONE: Fixed string pattern matching + exposedStdlib priority (skyshop rendering)
- DONE: Removed debug prints from `compileDependencyModule` (57 println calls per build)
- DONE: FFI import deduplication (Os loaded 5x → 1x)
- DONE: Removed `go get` loop before `go mod tidy` (cold build 1:41 → 17s)
- DONE: Working type checker — catches errors at compile time (v0.7.2)
- DONE: Wrapper IIFE fix — effectful FFI calls now return values not closures
- TODO: Skip .skyi lowering — use .skyi for types only, emit direct wrapper calls

**P1 — Moderate impact:**
- Port full symbol-level tree-shaking (collect wrapper refs during lowering, skip unused wrappers)
- Selective import emission in `makeGoPackage` (scan declarations, only emit used imports)
- DONE: `-gcflags="all=-l"` already in go build command
- Preserve go.mod/go.sum across builds

**P2 — Incremental/future:**
- Multi-level caching for type-check results
- Inspector cache for Go package introspection
- Go generics support in FFI pipeline
