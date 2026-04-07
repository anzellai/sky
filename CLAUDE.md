# CLAUDE.md

## Language Convention

All documentation, comments, variable names, function names, and user-facing strings **must use British English spelling** (`optimise`, `behaviour`, `colour`, `initialise`, `serialise`, `catalogue`). Exceptions: protocol identifiers (LSP `initialize`), CSS/HTML properties (`color`), Go stdlib names.

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** No runtime surprises from FFI. No panic/nil leakage. All edge cases in types.
2. **Dev experience is top priority.** Clear errors, predictable behaviour, no user-written FFI.
3. **Root-cause fixes only.** Fix at the correct abstraction layer. **Never suppress type errors or warnings.**
4. **Production-grade architecture.** Must scale to large Go packages (Stripe SDK). Must remain maintainable.

## Effect Boundary: Task

ALL effectful operations flow through `Task`:
- **Pure** (`String.length`, `List.map`) — no wrapping
- **Fallible** (`String.toInt`, `Dict.get`) — `Result` or `Maybe`
- **Effectful** (`File.readFile`, `Http.get`, `println`) — `Task String a`
- **Entry** (`main`) — may return `Task`; runtime auto-executes

FFI boundary mapping: Go `(T, error)` → `Result String T` | Go `error` → `Result String ()` | panics → `Err` | nil → `Maybe`/`Result`

## Project Overview

Sky is a pure functional language (Elm-inspired) compiling to Go. Self-hosted compiler, CLI, formatter, LSP, FFI generator — ~4MB native binary. Zero Node.js/TypeScript dependencies.

## Architecture

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
```

```
src/                              -- Sky compiler (self-hosted, 34 modules)
  Main.sky                        -- CLI entry point
  Compiler/                       -- 21 modules: lexer, parser, type checker, lowerer, emitter
  Ffi/                            -- 4 modules: inspector, binding/wrapper gen, type mapper
  Formatter/                      -- 2 modules: pretty-printer + formatter
  Lsp/                            -- 2 modules: JSON-RPC + LSP server
ts-compiler/                      -- Legacy TypeScript bootstrap (reference only)
stdlib-go/                        -- Go runtime for stdlib modules
templates/CLAUDE.md               -- Template for `sky init` projects
examples/                         -- 15 example projects
```

## Template Sync (Non-Negotiable)

When stdlib, syntax, Sky.Live APIs, or CLI commands change, **`templates/CLAUDE.md` MUST be updated**. AI assistants use this template to write Sky code in user projects.

## Building Examples

**NEVER run `sky build` for examples from the repo root** — it overwrites the compiler binary in `sky-out/`. Always `cd` into the example directory first:
```bash
cd examples/01-hello-world && sky build src/Main.sky
```

## Git Push / Release Checklist

1. `rm -rf .skycache && sky build src/Main.sky` — rebuild compiler
2. `sky-out/app --version` — must print version, NOT start a server
3. `sky build src/Main.sky` twice — verify self-hosting
4. **Clean-slate validation of ALL examples (mandatory before every push/tag):**
   ```bash
   for d in examples/*/; do
       cd "$d" && rm -rf sky-out .skycache .skydeps
       # run `sky install` first if sky.toml has [go.dependencies]
       sky build src/Main.sky   # must succeed
       ./sky-out/app            # must run (kill servers after verifying HTTP 200)
       cd ../..
   done
   ```
   Every example must build **and** run from a completely clean slate. If any example fails, fix it before pushing. No exceptions.
5. `cd examples/12-skyvote && sky check` — 0 errors
6. Test in temp dir: `sky init mytest`, `sky build && sky run`, `sky add fmt`, `sky remove fmt`, `sky upgrade`
7. Verify `.github/workflows/ci.yml` matches build steps

## Shell Commands

Always use `-f` flag with `rm` and `cp` (`rm -f`, `rm -rf`, `cp -f`).

## Build & Test

```bash
sky init [name]                   # Create new project
sky build src/Main.sky            # Compile → sky-out/app
sky run src/Main.sky              # Build and run
sky check src/Main.sky            # Type-check only
sky fmt src/Main.sky              # Format (Elm-style)
sky add github.com/some/package   # Add dependency + generate bindings
sky remove <package>              # Remove dependency
sky install                       # Install deps + generate missing bindings
sky update                        # Update deps to latest
sky upgrade                       # Self-upgrade binary
sky lsp                           # Language Server (JSON-RPC/stdio)
sky clean                         # Remove sky-out/ dist/
sky --version                     # sky v0.7.7
```

## Code Formatting (`sky fmt`)

Opinionated elm-format style, no configuration:
- 4-space indentation (never tabs)
- No max line width — short on one line, long ones break
- "One line or each on its own line" for args, list items, record fields
- Leading commas for multi-line lists/records
- Trailing newline; two blank lines between declarations

```elm
-- Pipelines
value
    |> transform1
    |> transform2 arg1

-- Records: leading commas when multi-line
{ firstName = "Alice"
, lastName = "Smith"
}

-- Case
case msg of
    Increment ->
        count + 1
    Decrement ->
        count - 1

-- Let/in
let
    x = compute
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

Safety: formatter refuses to write if output loses >1/3 of code lines (prevents silent deletion from partial AST).

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
| `Std.Db` | connect, open, exec, query, queryDecode, insertRow, getById, updateById, deleteById, findWhere, withTransaction | Result String a |

### Prelude (implicitly imported)
`Result (Ok/Err)`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`

### Concurrency
```elm
Task.parallel : List (Task err a) -> Task err (List a)  -- goroutine-backed, first error short-circuits
Task.lazy : (() -> a) -> Task err a                      -- defer computation
List.parallelMap : (a -> b) -> List a -> List b          -- pure goroutine map
```

## Go FFI / Interop Model

### Golden Rule: Users never write FFI code

Pipeline: `sky add pkg` → inspector extracts types → compiler classifies functions → generates `.skyi` + Go wrapper with panic recovery → DCE strips unused → `sky install` auto-generates missing bindings. Large packages (>50KB) use `sky-ffi-gen` for usage-driven bindings.

### Type Mapping
| Go | Sky |
|----|-----|
| `string` / `int`,`int64` / `float64` / `bool` | `String` / `Int` / `Float` / `Bool` |
| `error` / `(T, error)` / `(T, bool)` | `Result String a` / `Result String T` / `Maybe T` |
| `*string`, `*int` | `Maybe String`, `Maybe Int` |
| `*sql.DB` / `[]T` | `Db` (opaque) / `List T` |
| Go struct / Go interface | Opaque type (constructor + getters + setters / method bindings) |

### Opaque Struct Pattern (Builder)

Go structs are opaque — use generated constructors and pipeline setters (value first, struct second for `|>`):
```elm
-- Constructor: newTypeName () -> TypeName
-- Getter: typeNameFieldName : TypeName -> FieldType
-- Setter: typeNameSetFieldName : FieldType -> TypeName -> TypeName
params =
    Stripe.newCheckoutSessionParams ()
        |> Stripe.checkoutSessionParamsSetMode "payment"
        |> Stripe.checkoutSessionParamsSetSuccessURL successUrl
```
Pointer fields auto-wrapped — pass plain values. For nested structs, build inner first.

## Sky.Live

Server-driven UI with Elm TEA architecture:
```elm
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/about" AboutPage ], notFound = HomePage
        }
```
HTTP-first (full HTML on load, patches on events), SSE subscriptions, session stores (memory/SQLite/Redis/PostgreSQL/Firestore), type-safe events, VNode diffing, security (cookies, rate limiting, CORS).

### Sky.Http.Server
```elm
main =
    Server.listen 8080
        [ Server.get "/" (\_ -> Task.succeed (Server.text "Hello!"))
        , Server.get "/api/users/:id" getUser
        , Server.post "/api/data" handlePost
        , Server.static "/assets" "./public"
        ]
```
Routes: `get/post/put/delete/any` | Groups with prefix | Cookies (HttpOnly, Secure, SameSite) | Extractors: `param`, `queryParam`, `header`, `getCookie` | Responses: `text`, `json`, `html`, `withStatus`, `redirect` | Middleware: `Handler -> Handler`

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
    println (String.fromInt (update Increment 0))
```

Key syntax: `|>` `<|` pipelines | `::` cons | `\x -> x + 1` lambdas | `let...in` | `case...of` with exhaustiveness | `{ record | field = value }` update | `module M exposing (..)` / `import M as Alias exposing (func)`

## Examples

| # | Name | Description |
|---|------|-------------|
| 01 | hello-world | Basic println |
| 02 | go-stdlib | Go stdlib (crypto, encoding, time, http) |
| 03 | tea-external | TEA with external packages (UUID, godotenv) |
| 04 | local-pkg | Multi-module with local imports |
| 05 | mux-server | HTTP server with gorilla/mux |
| 06 | json | JSON encoding/decoding |
| 07 | todo-cli | SQLite CLI todo app |
| 08 | notes-app | Full CRUD web app with database |
| 09 | live-counter | Sky.Live counter with SSE |
| 10 | live-component | Sky.Live component protocol |
| 11 | fyne-stopwatch | Desktop GUI with Fyne |
| 12 | skyvote | Full Sky.Live voting app with auth |
| 13 | skyshop | E-commerce: Stripe, Firebase, i18n |
| 14 | task-demo | Task effect boundary demo |
| 15 | http-server | Sky.Http.Server with routing + cookies |
| 16 | skychess | Sky.Live chess game with AI, SQLite persistence |

## Compiler Optimisation Strategy (keep up to date)

**This section must be kept current.** Any session changing the compiler pipeline, codegen, or build system must update it.

### Current Optimisations (implemented)

1. **Stale file cleanup** — `rm -f sky-out/sky_ffi_*.go sky-out/sky_*.go sky-out/live_init.go` at build start
2. **Empty wrapper deletion** — DCE deletes FFI wrapper files with no remaining functions
3. **Native DCE** (`bin/sky-dce`) — single-pass wrapper + main.go DCE, 27s → 1s
4. **Var declaration preservation** — DCE preserves all `var` decls (type constructors, FFI aliases)
5. **Large .skyi filtering** (`bin/skyi-filter`) — Stripe SDK: 147K→9K lines in 90ms
6. **Combined FFI imports** — deduplicate before loading (was parsing 8.4MB Stripe SDK 40+ times)
7. **FFI light path** — skip type-check + lowering for `.skyi`, generate constructors + wrapper vars only
8. **Parallel module lowering** — `List.parallelMap` with goroutines, ~300% CPU
9. **Parallel FFI loading/wrapper copying** — concurrent `skyi-filter` and file I/O
10. **String.join in hot paths** — O(n²) → O(n) in lowerer
11. **Incremental compilation** — `.skycache/lowered/` cache, skip type-check + lowering on warm builds
12. **Usage-driven FFI** (`sky-ffi-gen`) — Stripe 8896 types → only referenced symbols
13. **Runtime optimisations** — `sky_equal` type-switch, `sky_asString` via `strconv`, ASCII fast paths
14. **ADT structs** (v0.7.10+) — `SkyADT{Tag: N, SkyName: "Name", V0: val}`, integer tag matching, struct field access
15. **Type annotations** — `// sky:type funcName : Type` comments on all declarations

### Historical Fixes (all resolved)

All issues below are FIXED — listed for context if debugging regressions:

- **Formatter** — 7 fixes for elm-format compat; all 32 modules format+compile; idempotent output
- **Parser** — `(expr).field` support, `parseCaseBranches` nesting fix (`branchCol` tracking), long-line splits, `getLexemeAt1` field access
- **Lowerer** — nested case IIFEs, ADT sub-pattern matching, cons pattern `len == N`, string pattern double-quoting, local variable shadowing by `exposedStdlib` (check `paramNames` first), hardcoded `Css.` prefix vs import aliases, let-binding hoisting (3-round bootstrap)
- **Type checker** — working since v0.7.2; inner case extraction across Types/Unify/Infer/Adt modules
- **FFI** — `.skycache` path resolution, Task boundary, Go generics filtered, keyword conflicts, IIFE invocation, type alias emission, interface pointer dereference, zero-arity params, callback function types, method/constant collision, slice-of-pointer types, namespace collisions
- **Lexer** — `alias` removed from keywords (contextual only)
- **Type safety audit** — 33 gaps fixed: case fallthrough panics, FFI panic recovery, float-aware arithmetic, rune-based strings, numeric sorting, typed FFI boundaries, session ADT rebuilding, exhaustiveness checking

**Coding constraints**:
- Never write nested `case` inside a `case` branch — extract to helper functions.

### Known Limitations (v0.7.x)

These are current compiler limitations users must work around:

1. **No nested `case...of`** — The lowerer generates broken Go (nested IIFEs with variable capture issues) when `case` expressions appear inside `case` branches. **Workaround**: extract the inner `case` into a separate helper function. This is the single most impactful limitation.
2. **No anonymous records in function signatures** — Record types must be defined as type aliases; inline `{ field : Type }` in annotations is not supported.
3. **No higher-kinded types** — No `Functor`, `Monad`, etc. Use concrete types.
4. **No `where` clauses** — Use `let...in` instead.
5. **No custom operators** — Only built-in operators (`|>`, `<|`, `++`, `::`, etc.).
6. **Negative literal arguments need parentheses** — `f -1` parses as subtraction; use `f (-1)`.
7. **FFI callback wrapping is limited** — Only `func(ResponseWriter, *Request)` HTTP handlers are auto-wrapped. Other Go callback signatures may require manual wrappers.
8. **`exposing (Constructor(..))` breaks cross-module qualified calls** — Importing ADT constructors via `exposing (Colour(..))` in a dependency module causes the lowerer to misresolve qualified calls like `Move.foo` as record field access (`move.foo`) instead of `Chess_Move_Foo`. **Workaround**: use `import Foo as Foo` without `exposing` constructors, and reference values via lowercase accessor functions defined in the source module.
9. **Cross-module zero-arg ADT constructors emitted as function calls** — When a zero-arg constructor like `King` is referenced cross-module as `Piece.King`, the lowerer emits `Chess_Piece_King()` (function call) instead of `Chess_Piece_King` (value). **Workaround**: define lowercase accessor functions (`king = King`) in the defining module and use `Piece.king` instead.
10. **`Dict.toList` returns string keys** — Sky's `Dict` uses `map[string]any` internally, so `Dict.toList` returns string keys even for `Dict Int v`. Arithmetic on these keys silently produces 0. **Workaround**: iterate over known key ranges with `Dict.get` instead of using `Dict.toList`.
11. **`sky check` does not understand Go interface subtyping** — The type checker cannot verify that a concrete Go type (e.g. `Label`) satisfies a Go interface (e.g. `CanvasObject`). Calls like `Fyne.windowSetContent window label` fail check but compile and run correctly. **No workaround** — this requires the checker to model Go interface satisfaction.
12. **`sky check` does not understand Go callback function types** — FFI functions expecting `func(ResponseWriter, *Request)` cannot unify with Sky function types `Writer -> Request -> Unit`. Calls like `Mux.routerHandleFunc router "/" handler` fail check but the lowerer wraps handlers correctly at runtime. **No workaround** — requires callback type mapping in the checker.
13. **Zero-arg FFI functions require no `()` argument** — FFI bindings for zero-arg Go functions (e.g. `Uuid.newString`, `FyneApp.new`) declare the return type directly. Calling them with `()` causes a type error. **Use**: `Uuid.newString` not `Uuid.newString ()`.

30. **Lexer: `from` keyword blocks parameter names** — FIXED. Same class of bug as #22 (`alias`). `isKeyword` in Token.sky listed `from` as a keyword, causing the lexer to emit `TkKeyword` instead of `TkIdentifier`. Functions with `from` as a parameter name silently failed to parse because `parseFunParams` only accepts `TkIdentifier` tokens. `parseDeclsHelper` caught the error and called `skipToNextDecl`, dropping the function entirely. Fix: remove `from` from `isKeyword` — it's not used as a keyword in any parser dispatch. Impact: ALL functions using `from` as a parameter were silently dropped in dependency modules. This was the root cause of the chess example build failures; the reported cons pattern bug (#32 below) was also a symptom.

31. **Parser: negative literals require parentheses as function arguments** — NOT A BUG (Elm convention). `f -1` is parsed as `f - 1` (subtraction). Use `f (-1)` for negative arguments — this matches Elm's behaviour. Negative literals work without parentheses in `let` bindings (`x = -1`) and as standalone expressions.

32. **Lowerer: cons pattern in recursive functions** — FIXED (was symptom of #30). Cons patterns (`x :: rest`) work correctly in both Main and dependency modules. The earlier failures were caused by functions containing `from` as a parameter being silently dropped by the `from` keyword bug.

### Techniques from TS Compiler (to port)

1. **Symbol-level tree-shaking** — collect wrapper refs during lowering, filter to referenced only (Stripe 40K→~50)
2. **Selective import emission** — only emit imports for referenced packages (currently emits all 18)
3. **go.mod/go.sum preservation** — only delete `.go` files, reuse Go compiled objects
4. **Single-pass emission** — track imports during lowering, no second pass

### Build Times

| Project | Modules | Cold | Warm |
|---|---|---|---|
| hello-world | 1 | <1s | <1s |
| skyvote | 32+2 FFI | 1.7s | 1.7s |
| **skyshop** | 43+14 FFI | **1:30** | **0:59** |
| compiler | 28 | 5.6s | 5.6s |

### TODO (v1.0 — fully typed codegen)

Current v0.7.x uses `any` for params/returns with `sky_call(f, arg)`. v1.0 goal: eliminate `any` entirely.

**Why**: Go compiler as second type checker; no map allocations/type assertions; direct Go interop.

**v0.7.x achievements**: ADT structs (no map alloc), integer tag matching (O(1)), type info flows checker→lowerer.

**v1.0 requires** (calling-convention rewrite — all callers/callees change simultaneously):
1. Direct function calls replacing `sky_call(f, arg)`
2. Concrete typed signatures replacing `func f(a any) any`
3. Polymorphism via Go generics or monomorphisation
4. Go structs for records (`{ name : String, age : Int }` → named struct)
5. Parameterised core types: `SkyMaybe[T]`, `SkyResult[E, T]`, `SkyTuple2[A, B]`

**Remaining TODO items**:
- Smarter cache invalidation (hash source content per-module)
- Selective import emission
