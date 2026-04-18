# CLAUDE.md

## Language Convention

All documentation, comments, variable names, function names, and user-facing strings **must use British English spelling** (`optimise`, `behaviour`, `colour`, `initialise`, `serialise`, `catalogue`). Exceptions: protocol identifiers (LSP `initialize`), CSS/HTML properties (`color`), Go stdlib names.

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** The 2026-04-15 adversarial audit
   documented 23 counterexamples across P0 (soundness floor), P1
   (security), P2 (soundness cleanup), and P3 (tooling). All 23 are
   remediated with regression tests (see `docs/AUDIT_REMEDIATION.md`
   — completion marker landed 2026-04-16). The principle now holds
   for every path exercised by `cabal test`, `scripts/example-sweep.sh`,
   and `runtime-go/rt/*_test.go`. Residual P4 items (fully-typed
   codegen to eliminate `any` in emitted Go, and Sky-test harness
   port) remain as future-work tracked in `docs/PRODUCTION_READINESS.md`.
   Defence in depth (panic recovery + `Err` return at Task boundaries)
   remains the reliability floor under the v1.0 milestone.
2. **Dev experience is top priority.** Clear errors, predictable behaviour, no user-written FFI.
3. **Root-cause fixes only.** Fix at the correct abstraction layer. **Never suppress type errors or warnings.**
4. **Production-grade architecture.** Must scale to large Go packages (Stripe SDK). Must remain maintainable.

## Non-Regression Rules

These constraints are enforced by `sky verify`, `test/Sky/ErrorUnificationSpec.hs`, and the audit-remediation specs under `test/Sky/**`. Violating them breaks the repo:

- **No `Result String a`** in any public surface. Use `Result Error a`.
- **No `Task String a`** in any public surface. Use `Task Error a`.
- **No `Std.IoError`** — the pre-v1 error ADT was deleted.
- **No `RemoteData`** — the pre-v1 async-state type was deleted.
- **No runtime panic from well-typed Sky code.** Every known panic class has a regression test in `runtime-go/rt/*_test.go` or `test/Sky/**Spec.hs`.
- **No silent numeric coercion.** `AsInt` / `AsBool` / `AsFloat` returning zero on type mismatch is a violation of P0-2 (see `docs/AUDIT_REMEDIATION.md`). New code uses the fallible `AsIntChecked` variants; lenient display-only helpers carry the suffix `OrZero` so the intent is visible at every call site.
- **No raw `.(T)` assertions on generated any-typed thunks.** Typed codegen must route through a runtime `Coerce` helper (see P0-3). Grep gate: `sky-out/main.go` files contain no `any(body).(T)` patterns outside the `Coerce*` helpers.
- **Record field enumeration sorts by `_fieldIndex`.** Any `Map.toList fields` in codegen that feeds field order (struct decl, auto-ctor, destructure) sorts by declaration index before emission. Violating this swaps auto-ctor parameters (P0-4).
- **Secrets are typed.** `Auth.signToken` / `Auth.verifyToken` take `String`, not `any`. `fmt.Sprintf("%v", secret)` is forbidden — explicit `.(string)` assertion on a typed boundary, with minimum-length validation (P1-4).
- **`sky check` is a superset of `sky build`.** `sky check` runs the Go emitter and invokes `go build` on the output. If `sky build` would fail, `sky check` fails (P0-1). Regression test: for every fixture in `test-files/`, both commands agree on accept/reject.

## Testing Rules

- **Every new language feature or runtime helper needs a test.** Cabal specs for compile-time behaviour; `runtime-go/rt/*_test.go` for runtime helpers; `tests/**/*Test.sky` for stdlib semantics.
- **Every bug becomes a regression test** *before* landing the fix. The failing test is the discovery artefact; without it, the class comes back. Audit items specifically require the test to fail against HEAD~1 and pass at HEAD.
- **`sky test <file>` is the user-facing runner.** See `sky-stdlib/Sky/Test.sky` for the API.
- **Runtime verification on every push.** `sky verify` builds and runs each example, catching panics and HTTP failures that `--build-only` misses.

## Tooling Rules

- **CLI commands must be correct end-to-end.** `sky build` / `sky run` / `sky check` / `sky fmt` / `sky test` all auto-regen missing FFI bindings and propagate exit codes.
- **LSP capabilities must match `docs/tooling/lsp.md`.** If you add a capability, document it. If a feature is incomplete, narrow the claim — don't lie in docs.
- **Formatter must be idempotent.** Two passes produce byte-identical output. Fixtures in `test/Sky/Format/FormatSpec.hs` guard this.

## Effect Boundary: Task

ALL effectful operations flow through `Task`:
- **Pure** (`String.length`, `List.map`) — no wrapping
- **Fallible** (`String.toInt`, `Dict.get`) — `Result` or `Maybe`
- **Effectful** (`File.readFile`, `Http.get`, `println`) — `Task Error a`
- **Entry** (`main`) — may return `Task`; runtime auto-executes

FFI boundary mapping: Go `(T, error)` → `Result Error T` | Go `error` → `Result Error ()` | panics → `Err` | nil → `Maybe`/`Result`

## Environment Variable Precedence

Configuration values resolve in this order (highest priority first):

1. **System environment variables** (`export PORT=8080`, Docker `ENV`, CI vars)
2. **`.env` file** in the working directory (auto-loaded at startup, never overrides existing env vars)
3. **`sky.toml`** defaults (compiled into the binary via `init()`, only set when not already present)

This follows the standard convention (godotenv, Docker): system env vars always win so production deployments can override `.env` defaults without editing files. The `.env` file is for local development convenience.

Sky.Live-specific env vars: `SKY_LIVE_PORT`, `SKY_LIVE_TTL`, `SKY_LIVE_STORE`, `SKY_AUTH_TOKEN_TTL`, `SKY_AUTH_COOKIE`.

## Project Overview

Sky is a pure functional language (Elm-inspired) compiling to Go. The compiler is written in Haskell (GHC 9.4+) and ships as a single `sky` binary. Runtime binaries are Go output — single-file, statically-linked, no external runtime needed. See `docs/compiler/journey.md` for why the compiler moved TS → Go → Sky → Haskell.

## Architecture

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
```

```
src/                              -- Sky compiler (Haskell, GHC 9.4+)
  Sky/Parse/                      -- lexer, layout filter, parser
  Sky/Canonicalise/               -- name resolution, import validation
  Sky/Type/                       -- HM inference, exhaustiveness
  Sky/Build/                      -- orchestration + FFI generator
  Sky/Generate/Go/                -- Go IR + printer
  Sky/Lsp/                        -- language server
  Sky/Format/                     -- elm-format-style formatter
app/Main.hs                       -- CLI entry point
runtime-go/rt/                    -- Go runtime (embedded via Template Haskell)
sky-stdlib/                       -- Sky-side stdlib (embedded)
tools/sky-ffi-inspect/            -- Go package introspector (embedded via TH;
                                     self-provisions to XDG cache on first use
                                     so releases ship a single `sky` binary)
legacy-ts-compiler/               -- Legacy TypeScript bootstrap (reference only)
legacy-sky-compiler/              -- Legacy self-hosted Sky compiler (reference only)
templates/CLAUDE.md               -- Template for `sky init` projects
examples/                         -- 18 example projects
```

See `docs/compiler/journey.md` for the TS → Go → Sky → Haskell history.

## Template Sync (Non-Negotiable)

When stdlib, syntax, Sky.Live APIs, or CLI commands change, **`templates/CLAUDE.md` MUST be updated**. AI assistants use this template to write Sky code in user projects.

## Building Examples

**NEVER run `sky build` for examples from the repo root** — it overwrites the compiler binary in `sky-out/`. Always `cd` into the example directory first:
```bash
cd examples/01-hello-world && sky build src/Main.sky
```

## Git Push / Release Checklist

1. `cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky` — rebuild compiler
2. `sky-out/sky --version` — must print version, NOT start a server
3. `cabal test` — cabal test suite must pass (18/18 ExampleSweep + TypedFfi + ErrorUnification specs)
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
sky test tests/MyTest.sky         # Run a Sky test module (exposing `tests : List Test`)
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
| `Sky.Core.Result` | withDefault, map, andThen, mapError, **map2/3/4/5, andMap, combine, traverse** |
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
| `Sky.Core.Task` | succeed, fail, map, andThen, perform, sequence, parallel, lazy, **map2/3/4/5, andMap** | Task err a |
| `Sky.Core.File` | readFile, writeFile, append, mkdirAll, readDir, exists, remove, isDir, tempFile, tempDir, copy, rename | Task Error a |
| `Sky.Core.Process` | run, exit, getEnv, getCwd, loadEnv | Task Error a |
| `Sky.Core.Io` | readLine, readBytes, writeStdout, writeStderr | Task Error a |
| `Sky.Core.Args` | getArg, getArgs | Maybe String / List String |
| `Sky.Core.Time` | now, unixMillis, sleep | Task Error Int |
| `Sky.Core.Http` | get, post, request | Task Error Response |
| `Sky.Core.Random` | int, float, choice, shuffle | Task Error a |
| `Sky.Http.Server` | listen, get/post/put/delete routes, middleware | Task Error () |
| `Std.Db` | connect, open, exec, query, queryDecode, insertRow, getById, updateById, deleteById, findWhere, withTransaction | Result Error a |
| `Std.Auth` | register, login, verify, logout, verifyEmail, hashPassword, verifyPassword, setRole, signToken, verifyToken | Result Error a |

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

**Every FFI call returns `Result Error T`.** The boundary is a trust
boundary (Elm-ports analogy). See `docs/ffi/boundary-philosophy.md`.

| Go return | Sky type |
|---|---|
| `string` / `int`/`int64` / `float64` / `bool` (element types) | `String` / `Int` / `Float` / `Bool` |
| `T` (single, no error) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error T` (opaque; nil-deref → Err via recover) |
| `(T, error)` / `error` | `Result Error T` / `Result Error ()` |
| `(T, bool)` (comma-ok) | `Result Error (Maybe T)` |
| `(T, *NamedErr)` where NamedErr implements error | `Result Error T` |
| `(T, U)` (no error/bool) | `Result Error (T, U)` |
| `*sql.DB` / `[]T` / `map[string]V` | `Result Error Db` / `Result Error (List T)` / `Result Error (Dict String V)` |
| Go struct / Go interface | Opaque type (constructor + getters + setters / method bindings, all wrapped in Result) |
| void | `Result Error ()` |

Bare `*T` returns are NOT wrapped in Maybe — Go SDK builder chains
(Firestore, Stripe) rely on chaining pointer returns. Defer-recover
catches downstream nil-deref and surfaces `Err(ErrFfi(...))`.

Nil-receiver checks are added to every method/getter/setter wrapper —
calling on a nil opaque returns `Err(ErrFfi "nil receiver: T.M")` instead
of panicking.

### Opaque Struct Pattern (Builder)

Go structs are opaque — use generated constructors and pipeline setters (value first, struct second for `|>`). Every FFI call returns `Result Error T`; the example below shows the typical "stitch values out of Results then call" pattern using `Result.andThen`:

```elm
-- Constructor: newTypeName () -> Result Error TypeName
-- Getter: typeNameFieldName : TypeName -> Result Error FieldType
-- Setter: typeNameSetFieldName : FieldType -> TypeName -> Result Error TypeName

createSession : String -> Result Error CheckoutSession
createSession successUrl =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetSuccessURL successUrl)
        |> Result.andThen Stripe.newCheckoutSession
```

Pointer fields auto-wrapped — pass plain values. For nested structs, build inner first. Boundary failure (panic, type mismatch) surfaces as `Err`; user code chains via `Result.andThen` / `withDefault` / `case`.

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

### Async Commands (Cmd.perform)

`update` returns `(Model, Cmd Msg)`. Use `Cmd.perform` to run long-running Tasks in background goroutines — results are dispatched back to `update` via SSE:

```elm
type Msg = FetchData | DataLoaded (Result Error String)

update msg model =
    case msg of
        FetchData ->
            ( { model | loading = True }
            , Cmd.perform (Http.get "/api/data") DataLoaded
            )
        DataLoaded result ->
            ( { model | loading = False, data = Result.withDefault "" result }
            , Cmd.none
            )
```

| Function | Type | Description |
|----------|------|-------------|
| `Cmd.none` | `Cmd msg` | No-op (most update branches) |
| `Cmd.perform` | `Task err a -> (Result err a -> msg) -> Cmd msg` | Run task async, dispatch result as Msg |
| `Cmd.batch` | `List (Cmd msg) -> Cmd msg` | Run multiple commands concurrently |

Concurrency: commands run in goroutines with session locking (same as subscriptions). Model is read fresh from the session store on completion — safe for multi-instance deployments.

### Sky.Http.Server
```elm
main =
    Server.listen 8000
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

### Multiline Strings

Triple-quoted strings preserve newlines and indentation. Interpolation uses `{{expr}}`:

```elm
html =
    """<div class="card">
    <h1>{{title}}</h1>
    <p>{{description}}</p>
</div>"""
```

Single braces `{` are literal — safe for JavaScript, CSS, JSON, SQL. Interpolation expressions can be identifiers, field access (`{{record.field}}`), qualified names (`{{String.fromInt n}}`), or function calls (`{{String.fromInt count}}`).

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
| 17 | skymon | Sky.Live monitoring dashboard with metrics, alerts |
| 18 | job-queue | Async Cmd.perform demo with Time.sleep, Random.int, Cmd.batch |

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
16. **Single-binary release** — `tools/sky-ffi-inspect/` Go source embedded via TH (`Sky.Build.EmbeddedInspector`); materialises + go-builds to `$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<hash>/` on first `sky add`. Cold-start ~4s, warm instant. Content-hash keys the cache dir so `sky upgrade` auto-invalidates. Dev workflow still picks up `bin/sky-ffi-inspect` via ancestor walk so contributors don't rebuild per branch.

### Historical Fixes (all resolved)

All issues below are FIXED — listed for context if debugging regressions:

- **Formatter** — 7 fixes for elm-format compat; all 32 modules format+compile; idempotent output
- **Parser** — `(expr).field` support, `parseCaseBranches` nesting fix (`branchCol` tracking), long-line splits, `getLexemeAt1` field access
- **Lowerer** — nested case IIFEs, ADT sub-pattern matching, cons pattern `len == N`, string pattern double-quoting, local variable shadowing by `exposedStdlib` (check `paramNames` first), hardcoded `Css.` prefix vs import aliases, let-binding hoisting (3-round bootstrap)
- **Type checker** — working since v0.7.2; inner case extraction across Types/Unify/Infer/Adt modules
- **FFI** — `.skycache` path resolution, Task boundary, Go generics filtered, keyword conflicts, IIFE invocation, type alias emission, interface pointer dereference, zero-arity params, callback function types, method/constant collision, slice-of-pointer types, namespace collisions
- **Lexer** — `alias` removed from keywords (contextual only)
- **Type safety audit** — 33 gaps fixed: case fallthrough panics, FFI panic recovery, float-aware arithmetic, rune-based strings, numeric sorting, typed FFI boundaries, session ADT rebuilding, exhaustiveness checking

### Known Limitations (v0.7.x)

These are current compiler limitations users must work around:

1. **No anonymous records in function signatures** — Record types must be defined as type aliases; inline `{ field : Type }` in annotations is not supported.
2. **No higher-kinded types** — No `Functor`, `Monad`, etc. Use concrete types. (Intentional — Hindley-Milner only.)
3. **No `where` clauses** — Use `let...in` instead. (Intentional.)
4. **No custom operators** — Only built-in operators (`|>`, `<|`, `++`, `::`, etc.). (Intentional.)
5. **Negative literal arguments need parentheses** — `f -1` parses as `f - 1` (subtraction). Use `f (-1)` — matches Elm's behaviour.
6. **`Dict.toList` returns string keys** — Sky's `Dict` uses `map[string]any` internally, so `Dict.toList` returns string keys even for `Dict Int v`. Arithmetic on these keys silently produces 0. **Workaround**: iterate over known key ranges with `Dict.get` instead of using `Dict.toList`.
7. **`sky check` does not fully model Go interface satisfaction** — Opaque FFI types unify with each other (v0.7.21 fix), but the checker still cannot verify that a concrete Go type (e.g. `Label`) satisfies a named Go interface (e.g. `CanvasObject`). Calls like `Fyne.windowSetContent window label` may fail `sky check` but compile and run correctly.
8. **Zero-arg FFI functions require no `()` argument** — FFI bindings for zero-arg Go functions (e.g. `Uuid.newString`, `FyneApp.new`) declare the return type directly. Calling them with `()` causes a type error. **Use**: `Uuid.newString` not `Uuid.newString ()`.
9. **Let bindings with parameters after multi-line case** — A let binding like `mark j = expr` after a `case ... of` in the same let block causes the parser to misinterpret it as a new top-level declaration. **Workaround**: use lambdas (`\j -> expr`) or extract to a top-level function.
10. **Zero-arity functions reading env vars** — Zero-arity functions are memoised and their import aliases evaluate at Go init time (before `.env` is loaded). If a zero-arity function reads `Os.getenv`, the value is cached as empty. **Workaround**: add a dummy `_` parameter: `getConfig _ = Os.getenv "KEY"`.

### Recently Fixed (v0.7.x — listed for regression context)

- **Nested `case...of`** — FIXED in v0.7.21. `caseDepth` counter in `LowerCtx` generates unique `__subject_N` variables per nesting level. Triple-nested case expressions compile and run correctly.
- **FFI callback wrapping** — FIXED in v0.7.21. `mapGoFuncType` parses arbitrary Go callback signatures (not just `func(ResponseWriter, *Request)`).
- **`sky check` Go callback function types** — FIXED in v0.7.21. Callback parsing in `TypeMapper.sky` handles `func(...)` types properly.
- **Non-exhaustive case expressions** — FIXED. Now a compile error (was a dead binding in Infer.sky). Shows missing patterns with source context.
- **Multi-module stdlib alias collision** — FIXED. `isStdlibCallee` checks `ctx.importAliases` instead of a hardcoded whitelist. `import Std.Db as Db` alongside `import Lib.Db as Db` works.
- **Lexer: `from` keyword blocked parameter names** — FIXED. Same class as the earlier `alias` bug. Removed `from` from `isKeyword` in Token.sky. Was the root cause of the cons-pattern-in-recursive-functions symptom.
- **`bin` field in sky.toml respected** — FIXED. `cmdBuild`, `cmdRun`, and the typed-build path now read `bin` from sky.toml and produce the configured binary path (defaults to `app`).
- **Cross-module zero-arg ADT constructors emitted as function calls** — FIXED. `lowerQualifiedImport` in Lower.sky now consults `ctx.importedConstructors` and emits `Piece_King` (value) for zero-arg constructors instead of `Piece_King()` (call). Multi-arg constructors retain the existing call form so `Piece.Box 42` still works.
- **Applicative combinators for Result and Task** — ADDED in v0.7.25. `Result.map2/3/4/5`, `Result.andMap`, `Result.combine`, `Result.traverse`, plus matching `Task.map2/3/4/5`, `Task.andMap`. Solves the heterogeneous-Result-combine and homogeneous-list-of-Results cases without needing nested case or `andThen` lambdas. Also upgraded `sky_call2`/`sky_call3` and added `sky_call4`/`sky_call5` to handle both curried and uncurried multi-arg Sky functions, fixing a latent issue where local-module functions passed to higher-order helpers crashed at runtime.
- **Auto record constructors from type aliases** — ADDED in v0.7.26. Every `type alias Foo = { ... }` declaration auto-generates a constructor function `Foo : field1Type -> field2Type -> ... -> Foo` (Elm convention). Eliminates `makeFoo` boilerplate and lets type aliases drop directly into `Result.map3 Foo (parseA ...) (parseB ...) (parseC ...)`. Implemented as a post-parse `elaborateModule` step in `Parser.sky` that synthesizes `TypeAnnotDecl` + `FunDecl` for each record type alias, skipping when the user has defined a value with the same name. Also extended the parser dispatcher to accept `TkUpperIdentifier` as a value-level declaration name so users can override the auto-generated constructor with their own implementation. Field declaration order in the type alias becomes positional API for the constructor.
- **Type system overhaul (annotations now load-bearing)** — FIXED in v0.7.28. Three coordinated changes restore "if it compiles, it works" for annotated functions:
  1. **Pretty-printer renames quantified vars to `a, b, c`** in `Types.sky`. `formatScheme` and `formatTypePairForError` rename TVars consistently within a single error or hover, so users see `Cannot unify a -> Int with Int` instead of `Cannot unify t108 -> Int with Int`. All unification error messages now use `cannotUnifyMsg` which calls `formatTypePairForError`. `TypedDecl.prettyType` uses `formatScheme` so LSP hovers show real types.
  2. **`inferFunctionSelfUnify` uses the annotation as the scheme** when present and the body validates against it. The new `applyAnnotationConstraint` helper unifies inferred body type with resolved annotation type, then uses the annotation type (substituted) as the function's stored scheme. Without this, `f : String -> Int -> String; f s n = s` was registered as `forall a b. a -> b -> a` (the inferred body type), and call sites could pass any types — silently ignoring the annotation.
  3. **`preRegisterFunctions` uses the annotation when present** so use sites in earlier declarations of the same module see the user's declared type instead of a polymorphic placeholder. Forward references and mutual recursion now respect annotations.
  - **Cross-module type alias resolution** in `registerTypeAliases` and `Resolver.typeExprToScheme`: both now accept the imported alias dict and combine it with the local paramMap, so an alias body like `myCounter : Counter` (where Counter is from another module) gets the resolved record substituted inline at registration time.
  - **`Adt.resolveAnnotation`** added: walks an annotation TypeExpr collecting unique TypeVar names, allocates a fresh ID per name, builds a paramMap, and resolves. This makes polymorphic annotations like `f : a -> b -> a` get distinct TVar IDs (previously all `TypeVar` references got hardcoded ID 0).
  - **Verified**: annotated `Decode.succeed makeStr |> Pipeline.required "a" Decode.string |> Pipeline.required "b" Decode.string` (where `makeStr : String -> Int -> String`) is now caught by `sky check` with `Pipeline operator: Type mismatch: String vs Int` instead of silently passing.

- **Zero-arity declaration memoisation (Ref bug fix)** — FIXED in v0.7.30. The lowerer treated top-level zero-parameter declarations like `counter = Ref.new 0` as functions, re-evaluating the body on every reference. This broke `Ref.new`, `Dict.empty` singletons, and any other values that must be created once. Fix: zero-arity declarations now emit memoised functions (`var _memo_X; var _memoOk_X bool; func X() { if !_memoOk_X { _memo_X = <body>; _memoOk_X = true }; return _memo_X }`). The calling convention is unchanged — both same-module and cross-module references call the function, but the body evaluates only once. The runtime alias registry `Ref` in `Unify.sky` now works as a true singleton.
- **`sky init` CLAUDE.md template embedded in binary** — FIXED in v0.7.30. The template is now in `bootstrap/runtime/templates/CLAUDE.md` and embedded via `//go:embed runtime/*`. Installed binaries no longer need a `templates/` directory on disk; `readEmbeddedTemplate` reads from the binary's embedded FS. Falls back to disk path lookup for repo dev builds.
- **Task.perform returns Result uniformly** — FIXED in v0.7.29. The helper used to unwrap `Ok` values while keeping `Err` as `SkyResult`. Now returns `sky_runTask` result directly so `case Task.perform t of Ok x -> ... ; Err e -> ...` works for both branches.

- **Async Cmd.perform for Sky.Live** — ADDED in v0.8.0. `update` returns `(Model, Cmd Msg)` where `Cmd.perform task toMsg` spawns a goroutine. On completion, the result is dispatched as a Msg through the full update/view/diff/SSE cycle with session locking. `Cmd.batch` runs multiple commands concurrently. Recursive: cmd-triggered updates can spawn more cmds.
- **Time.sleep + Random.int lowerer mappings** — ADDED in v0.8.0. `Time.sleep : Int -> Task Error ()` and `Random.int/float/choice/shuffle` now have Go implementations and lowerer mappings. Type signatures in Resolver for compile-time checking.
- **Constructor partial application** — FIXED in v0.8.0. `checkPartialIdent` now checks `importedConstructors` for ADT constructor arities, not just `localFunctionArity`. Fixes `JobDone jid` (partial apply of 2-arg constructor) generating invalid Go.
- **MultilineStringExpr AST node** — ADDED in v0.8.0. The parser creates `MultilineStringExpr` for `"""..."""` strings instead of desugaring at parse time. The formatter preserves triple-quoted strings. The lowerer desugars at codegen time with `{{expr}}` interpolation handling.
- **Formatter elm-style improvements** — FIXED in v0.8.0. Tuples break vertically with leading commas. Function args indent 4 spaces (not aligned to callee column). Parenthesised expressions stay compact on one line.
- **Skyshop env var race condition** — FIXED in v0.8.0. Zero-arity functions reading `Os.getenv` were memoised and evaluated at Go init time (before `.env` loaded). Fix: add `_` parameter to prevent memoisation.

**Coding constraints**: none active. (The "no nested case" rule is no longer required as of v0.7.21.)

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
