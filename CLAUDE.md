# CLAUDE.md

## Language Convention

All documentation, comments, variable names, function names, and user-facing strings in the Sky project **must use British English spelling**. Examples: `optimise` not `optimize`, `behaviour` not `behavior`, `colour` not `color`, `initialise` not `initialize`, `serialise` not `serialize`, `catalogue` not `catalog`.

Exceptions: protocol identifiers (LSP `initialize`), CSS/HTML property names (`color`, `text-align: center`), and Go standard library names which follow American conventions.

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** No runtime surprises from FFI. No panic leakage. No nil leakage. No partial bindings. All edge cases represented in types.
2. **Dev experience is top priority.** Clear errors, predictable behaviour, no user-written FFI, no confusing hidden behaviour.
3. **Root-cause fixes only.** Never patch over bugs. Fix at the correct abstraction layer (lexer, parser, type system, lowering, or interop generator). **Never suppress, hide, or work around type errors or warnings.** If the type checker reports an issue, either the code is wrong or the type checker is wrong — fix whichever is the root cause.
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
templates/CLAUDE.md               -- Template copied into new projects by `sky init`
examples/                         -- 15 example projects
```

## Template Sync (Non-Negotiable)

When stdlib functions, language syntax, Sky.Live APIs, or CLI commands change, **`templates/CLAUDE.md` MUST be updated** to reflect the changes. This template is the primary context file that AI assistants (Claude Code) use to write Sky code in user projects. If the template is out of date, AI assistants will generate incorrect code. Always verify the template covers:

- All CLI commands and their current behaviour
- All standard library modules with correct type signatures
- Sky.Live component protocol, events, subscriptions
- Sky.Http.Server routing and middleware
- Go FFI naming conventions and type mapping
- Code formatting rules

## Building Examples

**NEVER run `sky build` for examples from the repo root.** Always `cd` into the example directory first:

```bash
# CORRECT — builds into examples/01-hello-world/sky-out/
cd examples/01-hello-world && sky build src/Main.sky

# WRONG — overwrites the compiler in sky-out/
sky build examples/01-hello-world/src/Main.sky
```

The repo root `sky-out/` directory contains the **compiler binary**. Running `sky build` from the root for an example will overwrite it with the example's binary (e.g. an HTTP server), breaking the compiler.

## Git Push / Release Checklist

Before pushing to main or creating a release tag:

1. **Rebuild the compiler**: `rm -rf .skycache && sky build src/Main.sky`
2. **Verify**: `sky-out/app --version` must print `sky dev` or a version, NOT start a server
3. **Bootstrap**: Run `sky build src/Main.sky` twice to verify self-hosting
4. **Test examples**: `cd examples/01-hello-world && sky build src/Main.sky` (from the example dir)
5. **Test sky check**: `cd examples/12-skyvote && sky check` — must pass with 0 errors (validates cross-module ADT + type alias resolution)
6. **Test CLI commands**: Verify these in a temp directory:
   - `sky init mytest` — creates sky.toml, src/Main.sky, .gitignore
   - `sky build && sky run` — prints "Hello from Sky!"
   - `sky add fmt` — adds `fmt = "latest"` to `[go.dependencies]` in sky.toml
   - `sky remove fmt` — removes from sky.toml, reports "Removed fmt"
   - `sky upgrade` — fetches latest release, compares semver, downloads correct platform binary
7. **CI check**: Ensure `.github/workflows/ci.yml` matches the current build steps

## Shell Commands

Always use `-f` flag with `rm` and `cp` commands (e.g. `rm -f`, `rm -rf`, `cp -f`) to avoid interactive confirmation prompts that block execution.

## Build & Test

```bash
sky init [name]                   # Create new Sky project (sky.toml, src/Main.sky, .gitignore)
sky build src/Main.sky            # Compile Sky → Go binary (sky-out/app)
sky build examples/01-hello-world/src/Main.sky   # Compile any project
sky run src/Main.sky              # Build and run
sky check src/Main.sky            # Type-check without compiling (with full ADT + alias resolution)
sky fmt src/Main.sky              # Format (Elm-style: 4-space, leading commas)
sky add github.com/some/package   # Add Go or Sky dependency + generate bindings + update sky.toml
sky remove <package>              # Remove dependency from sky.toml + clean cache
sky install                       # Install all deps + auto-generate missing bindings
sky update                        # Update sky.toml dependencies to latest
sky upgrade                       # Self-upgrade to latest release (semver comparison, platform detection)
sky lsp                           # Start Language Server (JSON-RPC over stdio)
sky clean                         # Remove sky-out/ dist/
sky --version                     # sky v0.7.7
```

## Code Formatting (`sky fmt`)

The formatter follows **elm-format** style. It is opinionated and deterministic — there are no configuration options.

### Rules

- **4-space indentation** throughout (never tabs)
- **No max line width** wrapping — short expressions stay on one line, long ones break
- **"One line or each on its own line"** — function arguments, list items, and record fields either all fit on one line or each gets its own line
- **Leading commas** for multi-line lists, records, and record types
- **Trailing newline** at end of file
- **Blank line between declarations** (two blank lines), one blank line between type annotation and function

### Examples

```elm
-- Function calls: one line or each arg on its own line
div [ class "container" ] [ text "hello" ]

someFunction
    arg1
    arg2
    arg3

-- Pipelines: each |> on its own line, indented 4
value
    |> transform1
    |> transform2 arg1
    |> finalStep

-- Boolean chains: operators at start of line, indented 4
if condition1
    || condition2
    || condition3 then
    body

else
    fallback

-- Records: leading commas when multi-line
{ name = "Alice" , age = 30 }

{ firstName = "Alice"
, lastName = "Smith"
, email = "alice@example.com"
}

-- Record update
{ model | name = newName , age = newAge }

-- Case: branches indented 4, body indented 4 from pattern
case msg of

    Increment ->
        count + 1

    Decrement ->
        count - 1

-- Let/in: bindings indented 4, body indented 4 from in
let
    x = compute
    y = transform x
in
    result

-- else if: flat chains
if x > 0 then
    positive

else if x < 0 then
    negative

else
    zero
```

### Safety

The formatter refuses to write if the output loses more than 1/3 of code lines compared to the input. This prevents silent code deletion when the parser recovers from syntax errors with a partial AST.

## Standard Library

### Pure Functions (no Task)
| Module | Key Functions |
|--------|--------------|
| `Sky.Core.String` | split, join, replace, trim, contains, startsWith, toInt, fromInt, slice, length |
| `Sky.Core.List` | map, filter, foldl, foldr, head, take, drop, sort, zip, concat, filterMap, parallelMap |
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
| `Sky.Core.Task` | succeed, fail, map, andThen, perform, sequence, parallel, lazy | Task err a |
| `Sky.Core.File` | readFile, writeFile, mkdirAll, readDir, exists | Task String a |
| `Sky.Core.Process` | run, exit, getCwd, loadEnv | Task String a |
| `Sky.Core.Io` | readLine, readBytes, writeStdout, writeStderr | Task String a |
| `Sky.Core.Time` | now, unixMillis, sleep | Task String Int |
| `Sky.Core.Http` | get, post, request | Task String Response |
| `Sky.Core.Random` | int, float, choice, shuffle | Task String a |
| `Sky.Http.Server` | listen, get/post/put/delete routes, middleware | Task String () |

### Prelude (implicitly imported everywhere)
`Result (Ok/Err)`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`

### Concurrency

Sky provides goroutine-backed concurrency through Task and List:

```elm
-- Run tasks concurrently, collect results in order (first error short-circuits)
Task.parallel : List (Task err a) -> Task err (List a)

-- Defer computation until task is executed
Task.lazy : (() -> a) -> Task err a

-- Map function over list using goroutines (pure, no Task wrapping)
List.parallelMap : (a -> b) -> List a -> List b
```

Usage:
```elm
-- Parallel HTTP requests
results = Task.perform (Task.parallel [ Http.get url1, Http.get url2, Http.get url3 ])

-- Parallel computation (no Task needed)
squares = List.parallelMap (\n -> n * n) [ 1, 2, 3, 4, 5 ]
```

## Go FFI / Interop Model

### Golden Rule: Users never write FFI code

The compiler owns the entire boundary:
1. `sky add github.com/some/package` — auto-detect Go vs Sky package
2. Inspector subprocess runs `go/packages` + `go/types` to extract ALL exported types, fields, methods
3. Compiler classifies each function: all FFI calls are effectful (Task-wrapped with panic recovery)
4. Generates `.skyi` binding file + Go wrapper with panic recovery
5. For large packages (>50KB inspect JSON), `sky-ffi-gen` native tool generates usage-driven bindings — only symbols referenced in source get bindings
6. Dead code elimination strips unused wrappers from final build
7. `sky install` auto-scans source for FFI imports and generates missing bindings

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
| Go struct | Opaque type (constructor + field getters + setters) |
| Go interface | Opaque type (method bindings) |

### Opaque Struct Pattern (Builder)

All Go structs are opaque in Sky — never constructed as Sky records. Use generated constructors and pipeline-friendly setters:

```elm
-- Constructor: newTypeName () -> TypeName
-- Getter: typeNameFieldName : TypeName -> FieldType
-- Setter: typeNameSetFieldName : FieldType -> TypeName -> TypeName

-- Example: Stripe CheckoutSessionParams
import Github.Com.Stripe.StripeGo.V84 as Stripe
import Github.Com.Stripe.StripeGo.V84.Checkout.Session as Session

params =
    Stripe.newCheckoutSessionParams ()
        |> Stripe.checkoutSessionParamsSetMode "payment"
        |> Stripe.checkoutSessionParamsSetSuccessURL successUrl
        |> Stripe.checkoutSessionParamsSetCustomer customerId
        |> Stripe.checkoutSessionParamsSetLineItems lineItems

result = Session.new params
```

Setters take **value first, struct second** for `|>` pipeline compatibility. Pointer fields (`*string`, `*int64`, `*bool`) are automatically wrapped — pass the plain value, the wrapper handles pointer creation.

For nested structs, build inner structs first:
```elm
productData =
    Stripe.newCheckoutSessionLineItemPriceDataProductDataParams ()
        |> Stripe.checkoutSessionLineItemPriceDataProductDataParamsSetName title

priceData =
    Stripe.newCheckoutSessionLineItemPriceDataParams ()
        |> Stripe.checkoutSessionLineItemPriceDataParamsSetProductData productData
        |> Stripe.checkoutSessionLineItemPriceDataParamsSetUnitAmount 1000
        |> Stripe.checkoutSessionLineItemPriceDataParamsSetCurrency "gbp"
```

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

1. **Stale file cleanup** (`Pipeline.sky:compile`) — `rm -f sky-out/sky_ffi_*.go sky-out/sky_*.go sky-out/live_init.go` at start of every build prevents cross-project pollution when `sky-out/` is reused.

2. **Empty wrapper deletion** (`Pipeline.sky:trimWrapperFile`) — when wrapper DCE eliminates ALL functions from an FFI wrapper file, the file is deleted entirely instead of leaving import-only stubs that cause Go build failures.

3. **Native DCE tool** (`bin/sky-dce` + `Pipeline.sky:runNativeDce`) — compiled Go tool performs both wrapper DCE and main.go DCE in a single pass. Uses native `strings.Contains` (no `any` boxing) for O(n²) reachability analysis. Replaces the Sky-based DCE which took 27s with a 1s native implementation.

4. **Var declaration preservation** — DCE separates `var` declarations from `func` blocks before analysis. All vars are preserved (they may be type constructors or FFI aliases). Only unreachable functions are eliminated.

5. **Large .skyi filtering** (`bin/skyi-filter` + `Pipeline.sky:loadOneFfiBinding`) — for binding files >10KB, runs external Go tool that precomputes used `Alias.funcName` set via regex, then streams the .skyi keeping only header + types + used declarations. Stripe SDK: 147K→9K lines in 90ms.

6. **Combined FFI imports** (`Pipeline.sky:compileMultiModule`) — collect all dependency imports first, deduplicate, then load FFI modules once. Previously loaded per-module, causing the 8.4MB Stripe SDK to be parsed 40+ times.

7. **FFI light path** (`Pipeline.sky:compileFfiModuleLight`) — skip full type-check + lowering for `.skyi` modules. Generate only constructor declarations + wrapper variable bindings. Handles 0-arity wrappers via `sky_callZeroOrNil` (type-assertion dispatch) and literal bindings via `extractLiteralFromBody`.

8. **Parallel module lowering** (`Pipeline.sky` + `Lower.sky`) — `List.parallelMap` using Go goroutines for dependency module compilation. Parallel helpers written to separate `sky-out/sky_parallel.go` file (avoids `goimports` stripping `sync` import from single-line helper decls). ~300% CPU utilisation on multi-core.

9. **Parallel FFI loading** — `loadFfiBindings` uses `List.parallelMap` to spawn `skyi-filter` subprocesses concurrently.

10. **Parallel wrapper copying** — `copyFfiWrappers` uses `List.parallelMap` for concurrent file I/O.

11. **String.join optimisation** (`Lower.sky:emitGoExprInline`, `lowerBinary`, `emitBranchCode`, `patternToCondition`, `lowerLet`) — replaced O(n²) `++` concatenation chains with O(n) `String.join "" [parts]` in the lowerer's hottest functions. Reduced CPU time by ~5%.

12. **Incremental compilation** (`Pipeline.sky:compileDependencyModuleCached`) — cache lowered Go declarations in `.skycache/lowered/`. On subsequent builds, cached modules skip type-checking + lowering entirely. Cross-module aliases regenerated fresh each build. Invalidated by `sky clean`.

### Known Issues (to fix)

1. **Formatter↔compiler compat** — FIXED. All 32 modules format, compile, and self-host. Formatting is idempotent (running `sky fmt` twice produces identical output). Seven fixes in Format.sky: (a) `getLexemeAt1` field access, (b) annotation-function pairing via `formatDeclPairs`, (c) flat `else if` chains via `isExprIf`, (d) record field layout on indented new line, (e) `formatCall` with `align` + `indent` + `line` — long function calls break arguments onto indented new lines while keeping short calls on one line; `align` ensures argument column >= callee column so the parser's `parseApplicationArgs` column check passes, (f) `quoteString` identity (AST stores raw escaped strings, no re-escaping needed), (g) stale `live_init.go` cleanup in Pipeline.sky prevents build failures when switching between Live and non-Live projects.

2. **Lowerer limitation with new functions** — FIXED. (a) Nested `case` inside `case` inside `let` — uses IIFEs (anonymous function literals), no blank function names observed. Works correctly for all 30 compiler modules. (b) ADT constructor sub-pattern matching — FIXED. `patternToCondition` now recursively checks sub-patterns of constructors via `subPatternConditions`. (c) **New functions in dependency modules** — FIXED (v0.7.13). Root cause was parser `(expr).field` not supported. `parsePrimary` only handled field access for simple identifiers. Expressions like `(snd pair).declarations` caused parse failures that silently dropped functions. Fix: `applyFieldAccess` in `parseApplication`/`parseApplicationArgs`. (d) **FFI/local module name collision** — FIXED. When a project had both a local Sky module (e.g. `Log.Entry`) and an FFI binding with the same name (from `log/entry`), `ffiNames` included the local module name, causing it to be compiled via the light path (constructors only, no functions). Fix: exclude local module names from `ffiNames` in `compileMultiModule`.

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

11. **Skyshop build time** — FIXED. Was hanging indefinitely due to repeated 8.4MB Stripe SDK parsing. Fixed via combined FFI imports, FFI light path, parallel goroutine lowering, String.join optimisation, and incremental caching. Warm build: **1:02** at 316% CPU. See README.md "Compiler Optimisation Journey" for full details.

14. **Lowerer: string pattern matching double-quoting** — FIXED. `literalCondition` in Lower.sky called `goQuote` on `LitString` values that already include surrounding quotes from the lexer. This double-quoted strings in pattern match conditions, causing ALL string `case` branches to fail silently and fall through to wildcards. Fix: use the `LitString` value directly since it's already a valid Go string literal. Impact: 253 string pattern matches in skyshop (translations) now work correctly.

15. **Lowerer: local variable shadowing by exposedStdlib** — FIXED. `lowerIdentifier` checked `ctx.exposedStdlib` BEFORE checking if a name was a local variable/parameter. Variables named `title`, `lang`, `content`, `body` were resolved to HTML attribute functions from `Std.Html.Attributes` instead of local bindings. Fix: check `ctx.paramNames` before `exposedStdlib` lookup. Impact: product titles, language selections, translations, and page content now render correctly instead of showing Go function pointer addresses.

17. **Lowerer: hardcoded `Css.` prefix intercepts import aliases** — FIXED. `lowerQualified` checked `String.startsWith "Css." qualName` before checking `importAliases`. When a project imports `Tailwind.Internal.Css as Css`, calls like `Css.allRules` were lowered to `sky_cssPropFn("all-rules")` (Std.Css property) instead of `Tailwind_Internal_Css_AllRules()`. Fix: skip the hardcoded `Css.` check when `Css` is in `importAliases`. Impact: Tailwind CSS `<style>` tag now renders CSS rules instead of Go function pointer addresses.

16. **Lowerer: let-binding hoisting (bootstrapping)** — RESOLVED after 3-round bootstrap from v0.6.9. The `paramNames` tracking changes in Lower.sky (adding bound names from `lowerLet` and pattern vars from `emitBranchCode`) are now fully propagated. New functions with complex let bindings compile correctly. Remaining limitation: never write nested `case` inside a `case` branch — the parser's layout rules nest subsequent branches inside inner case expressions. Always extract inner cases to helper functions. **Formatter fix**: `formatCall` must use `align` to keep argument columns >= callee column; using `indent` alone places arguments at `baseIndent+4` which can be less than the callee column at deep nesting, causing `parseApplicationArgs` to stop parsing arguments prematurely — let bindings appear as top-level declarations.

18. **Type checker — working** (v0.7.2). Root cause of non-working type system: parser layout rules nest case branches inside inner case expressions at same indentation. Fixed by extracting inner cases to helpers across Types.sky (`applySub`, `formatType`), Unify.sky (`unifyFun`, `unifyApp`), Infer.sky (`inferExpr` — all 13 expression branches), Adt.sky (`resolveTypeExpr`). Type errors now caught at compile time: `sky check` reports errors, `sky build` stops on errors, LSP shows red errors in editors.

19. **WrapperGen: IIFE missing invocation** — FIXED. `wrapFallibleReturn` and `wrapEffectfulReturn` generated `func() any { ... }` without trailing `()`. Effectful FFI calls (Os.getenv, Http.get, etc.) returned unevaluated closures instead of Result values. Fix: add `()` to invoke IIFEs. Existing projects must regenerate wrappers (`sky add <pkg>` or `sky install`).

20. **Parser nesting bug** — FIXED. `parseCaseBranches` used `peekColumn <= 1` to terminate branch parsing. Inner case expressions absorbed outer branches as dead code. Fix: `branchCol` parameter tracks the owning case's indentation level; terminates at `peekColumn < branchCol`. Eight compiler source files refactored to extract nested case expressions: Main.sky, Infer.sky, PatternCheck.sky, Unify.sky, Lower.sky, Pipeline.sky, Parser.sky, Lsp/Server.sky.

21. **Type safety audit** — FIXED. Comprehensive audit found 33 gaps violating "if it compiles, it works". All resolved:
    - Case fallthrough: `return nil` changed to `panic("non-exhaustive case expression")`
    - FFI panic recovery: wrappers use named returns with `SkyErr("FFI panic: ...")` instead of silently returning nil
    - Arithmetic: `+`, `-`, `*` are float-aware via `sky_numBinop`; comparisons via `sky_numCompare`
    - Strings: `String.length` counts runes not bytes
    - Sorting: `List.sort`/`max`/`min` use numeric comparison for numbers
    - Type system: unknown lowercase identifiers produce errors; Int/Float are distinct types; `JsValue` removed from universal unifiers
    - FFI: pointer fields return `Maybe`; receiver nil guards; safe opaque type casts; variadic element type checking
    - Runtime: `sky_runTask` converts panics to `SkyErr`; `sky_asList`/`sky_asMap` return empty collections not nil
    - Session store: `RebuildADT` handles custom ADTs recursively
    - Exhaustive.sky module checks pattern coverage against ADT registry

22. **Lexer: `alias` keyword blocks parameter names** — FIXED. `isKeyword` in Token.sky listed `alias` as a keyword, causing the lexer to emit `TkKeyword` instead of `TkIdentifier` for `alias`. Functions with `alias` as a parameter name (e.g. `generateDepModule modName alias mod =`) failed to parse because `parseFunParams` only accepts `TkIdentifier` tokens. Fix: remove `alias` from `isKeyword` — it's only contextual in `type alias` declarations, where `dispatchDeclaration` already uses string comparison. Impact: 5 functions in LowerTyped.sky were silently dropped.

23. **Parser: long-line formatter splits dropping functions** — FIXED. Very long single-line expressions split by the formatter across multiple lines (e.g. `String.startsWith` with `"["` on the next line at column 518) caused `parseFunDecl` to fail because `parseExpr` couldn't handle the deep-column continuation. Fix: split `isSafeType` (BindingGen.sky) and `isSupportedType` (WrapperGen.sky) into helper functions. Impact: 2 critical FFI functions were silently dropped.

24. **Parser: `(expr).field` not supported** — FIXED (v0.7.13). Field access was only handled for simple identifiers (`x.field`). Parenthesised expressions like `(snd pair).declarations` or `(fst x).name` caused parse failures that silently dropped entire functions via `parseDeclsHelper` error recovery. Fix: added `applyFieldAccess` after `parsePrimary` in `parseApplication` and `parseApplicationArgs`. Supports chained access: `(expr).a.b.c`. This was the root cause of known issue #2c — new functions in dependency modules not being emitted.

25. **FFI: type alias emission** — FIXED. Opaque FFI types (e.g. `time.Duration`) generated `var Duration = Time_Duration` where `Time_Duration` was a Go type, not a value. Fix: `generateAliasesFromModuleInner` filters type names from alias generation. Also handles capitalised name collisions (e.g. `dBStats` → `DBStats` collides with type `DBStats`).

26. **FFI: interface pointer dereference** — FIXED. `sky-ffi-gen` generated `receiver.(*Interface)` for Go interfaces, but Go doesn't allow pointers to interfaces. Fix: detect interface types via `kind` field in inspect JSON and use `receiver.(Interface)` assertion. Also fixed: interface params cast via `sky_asInt()` → proper type assertion.

27. **FFI: zero-arity wrapper params** — FIXED. `generateFuncWrapper` added `_ any` dummy param for zero-arity Go functions. Fix: empty param list for zero-arity. Also fixed BindingGen to not generate dummy `_` param in `.skyi` declarations.

28. **FFI: callback function types** — FIXED. `generateTypeCast` passed `any` where Go expected concrete function types like `func(http.ResponseWriter, *http.Request)`. Fix: generate type assertion for `func(...)` types with package path aliasing.

29. **FFI: method/constant name collision** — FIXED. Go type `Kind` has method `String()` → `Sky_log_slog_KindString`. Go constant `KindString` → also `Sky_log_slog_KindString`. Fix: `sky-ffi-gen` collects method wrapper names and skips colliding constants.

### Techniques from TS Compiler (to port)

The TypeScript compiler (`ts-compiler/`) achieved fast builds (~2-3s first build, ~500ms incremental) through techniques not yet ported:

1. **Symbol-level tree-shaking during lowering** — the TS lowerer collects `Sky_*` wrapper references into a `collectedWrapperSymbols` set AS it generates Go code. Wrappers are then filtered via `filterInspectResult()` to only generate code for referenced symbols. Impact: Stripe SDK 40K symbols → ~50 wrappers. The self-hosted compiler generates ALL wrappers then DCEs 99.8% of them.

2. **Selective import emission** — the TS lowerer scans emitted GoIR for `GoSelectorExpr`/`GoRawExpr` references and only emits imports for detected packages. The self-hosted compiler emits all 18 stdlib imports unconditionally in `makeGoPackage`.

3. **.skyi for types only, not lowered** — PARTIALLY DONE. `compileFfiModuleLight` skips full lowering for .skyi modules, generating only constructors + wrapper vars. Full symbol-level approach (TS-style) not yet ported.

4. **`-gcflags="all=-l"`** — disables Go inlining for faster compilation. Not yet used in self-hosted build step (`Main.sky` line ~116).

5. **Multi-level caching** — TS compiler has 4 cache levels: (a) in-memory type-check cache, (b) disk export cache (`.skydeps/.sky_export_cache.json`), (c) inspector cache (`.skycache/go/inspect.json`), (d) wrapper generation cache. Cold LSP start: 38s → 2s.

6. **go.mod/go.sum preservation** — TS compiler preserves these across rebuilds, only deleting `.go` files. Allows Go's incremental build to reuse compiled object files. Self-hosted compiler's stale cleanup removes everything.

7. **Single-pass emission** — imports tracked during lowering (not post-hoc scanning). No second pass over generated Go needed.

### Build Times (current)

| Project | Modules | Cold | Warm | Notes |
|---|---|---|---|---|
| hello-world | 1 | <1s | <1s | Single module |
| skyvote | 32+2 FFI | 1.7s | 1.7s | SQLite + Sky.Live |
| **skyshop** | 43+14 FFI | **1:30** | **0:59** | Stripe, Firebase, Tailwind |
| compiler | 28 | 5.6s | 5.6s | Self-hosted, 3200 Go decls |

### Priority Optimisation Roadmap

**Done:**
- DONE: Native DCE tool — 27s → 1s
- DONE: Combined FFI imports — fixed hanging build
- DONE: FFI light path — skip full lowering for .skyi
- DONE: Parallel lowering/loading/copying — goroutines, 300%+ CPU
- DONE: String.join in hot paths — O(n²) → O(n) concat
- DONE: Incremental compilation — cache lowered modules
- DONE: `-gcflags="all=-l"` in go build
- DONE: Usage-driven FFI generation — `sky-ffi-gen` native tool, Stripe 8896 types → 3
- DONE: `sky_equal` type-switch — direct comparison instead of `fmt.Sprintf`
- DONE: Incremental cache reads — warm builds skip type-check + lowering for cached modules
- DONE: SkyName tag extraction — one map lookup per case, not per branch
- DONE: `sky_asString` type-switch — `strconv.Itoa` instead of `fmt.Sprintf` for ints
- DONE: ASCII fast path for `String.slice`/`length` — skip `[]rune` for ASCII strings
- DONE: Opaque struct builders — constructors + pipeline setters for Go struct params
- DONE: FFI namespace collision fix — bare aliases no longer shadow local functions
- DONE: Rune-based `String.slice` — fixes UTF-8 truncation for multi-byte characters
- DONE: Sky.Live Update wrapper — preserves model on FFI panics instead of corrupting session

**Done (v0.7.10–v0.7.14):**
- DONE: Struct-based ADT values — `SkyADT{Tag: N, SkyName: "Name", V0: val}` replaces `map[string]any`
- DONE: Integer tag matching — `sky_adtTag(subject) == N` replaces `sky_getSkyName(subject) == "Name"`
- DONE: Struct field access — `sky_adtField(subject, 0)` replaces `sky_asMap(subject)["V0"]`
- DONE: Ordered variant names — `RegisteredAdt.variantNames` ensures deterministic tag index assignment
- DONE: Type plumbing — `typedDecls : Dict String Scheme` threaded from Checker to LowerCtx
- DONE: Type annotations — `// sky:type funcName : Type` comments on all function declarations
- DONE: Parser `(expr).field` — field access on any expression, not just identifiers
- DONE: FFI interface handling — non-pointer receiver for interfaces in `sky-ffi-gen`
- DONE: FFI callback types — function type assertions for `func(ResponseWriter, *Request)` etc.
- DONE: FFI method/constant collision detection — skips colliding constants
- DONE: FFI typed slices — `[]any` → `[]slog.Attr` conversion
- DONE: FFI zero-arity fix — correct wrapper signatures for `time.Now()` etc.
- DONE: Bootstrap directory — `bootstrap/main.go` for clean CI builds

**TODO (v1.0 — fully typed codegen):**
- Typed function parameters — requires replacing `sky_call(f, arg)` calling convention with direct Go function calls
- Typed function returns — requires all callers to handle concrete return types
- Go generic core types — `SkyMaybe[T]`, `SkyResult[E, T]`, `SkyTuple2[A, B]` with parameterised constructors
- Typed records — generate Go structs for each record shape instead of `map[string]any`
- Smarter cache invalidation — hash source content per-module, not just declaration counts
- Selective import emission — only emit Go imports for referenced packages

### v1.0 Typed Codegen Roadmap

The current compiler (v0.7.x) uses `any` for function parameters and returns, with `sky_call(f, arg)` for all function application. This means the Go compiler cannot validate types across function boundaries. The v1.0 goal is to eliminate `any` from generated code entirely.

**Why this matters:**
- "If it compiles, it works" — the Go compiler becomes a second type checker, catching mismatches the Sky type checker might miss
- Performance — typed code avoids map allocations, type assertions, and reflection
- Interop — typed functions can be called directly from Go without `any` casting

**What v0.7.x achieves (current):**
- ADT values are Go structs (SkyADT) — eliminates map allocation per ADT value
- Pattern matching uses integer comparison — O(1) vs O(n) string comparison
- Type information flows from checker to lowerer — inferred types available during code generation

**What v1.0 requires:**
1. Replace `sky_call(f, arg)` with direct function calls — every call site must know the callee's type
2. Replace `func f(a any) any` with `func f(a int) int` — every function signature must use concrete types
3. Handle polymorphic functions — either Go generics or monomorphisation
4. Generate Go structs for records — each unique `{ name : String, age : Int }` becomes a named Go struct
5. Parameterise core types — `SkyMaybe[T]`, `SkyResult[E, T]` with typed constructors and accessors

This is a calling-convention rewrite that affects every function in the compiler and all generated code. It cannot be done incrementally — all callers and callees must change simultaneously.
