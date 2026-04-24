# CLAUDE.md — Sky Language Project

This is a [Sky](https://github.com/anzellai/sky) project. Sky is a pure
functional language inspired by Elm, compiling to Go. The compiler is
written in Haskell (GHC 9.4+) and ships as a single `sky` binary. Users
only need the `sky` binary and Go 1.21+ — no Haskell toolchain required
to use Sky.

**Core principle: if it compiles, it works.** Every side effect flows
through `Task`, every fallible value returns `Result Error a`, and
`sky check` invokes `go build` on the emitted Go so any shape mismatch
surfaces at check time. No runtime panics from well-typed Sky code, no
nil leakage, no silent numeric coercion.

**Typed Go output.** Since v0.9, generated Go functions have concrete
signatures (`func f(name string, age int) rt.SkyResult[Error, Profile]`)
rather than the old `any`-boxed shape. Type annotations on your functions
are load-bearing — if you write `f : String -> Int -> Result Error Profile`
and the body would otherwise infer to something wider, the compiler rejects
the body; if inference is narrower, the annotation still wins at the call
site. Inline records in function annotations are not supported — use a
`type alias` for any record you want in a signature.

## Reading order for AI assistants

If you're an AI code assistant building Sky applications on behalf of a
user, read the following sections IN ORDER before producing any code.
Skipping ahead leads to code that type-checks but panics at runtime —
the "if it compiles it works" guarantee depends on you respecting a
handful of idioms the Elm-derived syntax makes non-obvious.

1. **Cardinal Rules** (below) — 10 rules that, if you follow them, keep
   you out of 90% of the pitfalls real Sky projects have hit.
2. **Common Pitfalls & Fixes** — error → cause → fix table. Skim this so
   the messages users report are instantly mappable to action.
3. **Real-World App Skeletons** — complete runnable starter code for the
   most common app shapes (CRUD, auth, chat/LLM, dashboard, REST API).
   Copy + adapt; do not invent from scratch.
4. **Standard Library — Complete API** — authoritative signatures. If a
   claim here conflicts with something you saw elsewhere, the stdlib
   section wins.
5. **Troubleshooting Cookbook** — when the user reports a specific error
   message, start here.
6. **Known Limitations** — work-arounds for things the compiler can't do.

Everything else (FFI details, sky.toml, formatter rules) is reference,
consult on-demand.

---

## Cardinal Rules (AI assistants: internalise before writing code)

These are non-negotiable. Violating them either fails to compile, panics at
runtime, or silently breaks a user-visible feature. Each rule has a concrete
"do this, not that" pattern to follow.

**1. Every fallible value returns `Result Error a` — never `Result String a`,
never a bare `a` that panics on error.** Sky's `Error` ADT
(from `Sky.Core.Error`) carries a structured kind (`Io`, `Network`, `Ffi`,
`InvalidInput`, `NotFound`, `PermissionDenied`, etc.) plus a message. Handlers
pattern-match on kind to decide retry behaviour and UI severity. Stringy error
types are forbidden at public surfaces and enforced by `sky verify`.

```elm
-- ✓ Do this
readConfig : String -> Result Error Config
readConfig path =
    case File.readFile path of
        Ok contents -> parseConfig contents
        Err e -> Err e   -- propagate the typed Error unchanged

-- ✗ Not this — stringy error loses the kind, breaks pattern-matching upstream
readConfig : String -> Result String Config
readConfig path = ...
```

**2. Match the annotation to the body.** When you annotate a function, the
annotation becomes the function's scheme — the body must produce that exact
type. `initSchema : Db -> Task Error ()` over a body that actually returns
`Task Error Int` (because `Db.exec` returns affected-row count) compiles
but panics at runtime when the `Int` is coerced to `()`. Either remove the
annotation (and let HM infer) or change it to match:

```elm
-- ✓ Correct
initSchema : Db -> Task Error Int    -- execRaw returns affected rows
initSchema db = Db.execRaw db "CREATE TABLE ..."

-- Or discard the count deliberately:
initSchema : Db -> Task Error ()
initSchema db =
    Db.execRaw db "CREATE TABLE ..." |> Task.map (\_ -> ())
```

**3. `Sky.Core.Prelude exposing (..)` does NOT re-export `Error`.** You must
explicitly import it. A project using `Result Error _` anywhere needs:

```elm
import Sky.Core.Error as Error exposing (Error(..), ErrorKind(..))
```

The `Error(..)` form exposes the constructor, `ErrorKind(..)` exposes the
eleven error kinds you pattern-match on (see Cardinal Rule 1).

**4. Store DB connections at the top level, not in `Model`.** Sky.Live
persists the Model to the session store (memory, SQLite, Redis, Postgres).
`*sql.DB` and similar opaque FFI handles have internal pointer cycles that
break gob serialization and add dead weight to every session blob. Open
once at module load via `Task.run`, fail-fast on error.

```elm
-- ✓ Do this — singleton conn forced eagerly at module init
dbConn =
    case Task.run (Db.open "sqlite" "app.db") of
        Ok conn -> conn
        Err e ->
            let _ = println ("[FATAL] " ++ Error.toString e) in
            System.exit 1

init _ = ({ messages = [], input = "" }, Cmd.none)

update msg model =
    case msg of
        SaveStuff payload ->
            ( model
            , Cmd.perform (Db.exec dbConn "INSERT ..." [payload]) Saved
            )
        Saved (Ok _) -> ({ model | notice = "Saved" }, Cmd.none)
        Saved (Err e) -> ({ model | notice = errMsg e }, Cmd.none)

-- ✗ Not this — Model can't serialise a live DB handle
type alias Model = { db : Maybe Db, messages : List Msg }
```

For long-running queries inside `update`, do not call `Db.query` directly
(it returns Task — you'd discard it). Instead `Cmd.perform (Db.query …) ResultMsg`
and handle `ResultMsg (Ok rows)` / `ResultMsg (Err e)` in the next branch.

**5. Each record type alias auto-generates a constructor.** Writing
`type alias Profile = { name : String, age : Int }` gives you
`Profile : String -> Int -> Profile` for free. Field declaration order is
positional argument order. Use it in `Result.map3` / `Decode.succeed`
pipelines without a `makeProfile` wrapper:

```elm
type alias Profile = { name : String, age : Int, email : String }

decodeProfile =
    Decode.succeed Profile
        |> Pipeline.required "name"  Decode.string
        |> Pipeline.required "age"   Decode.int
        |> Pipeline.required "email" Decode.string
```

**6. Records inside function annotations must be named.** Typed codegen
needs a struct name for emission. `f : { x : Int } -> Int` is rejected
because HM can't backfill a name. Always use a `type alias`:

```elm
-- ✓ Do this
type alias Point = { x : Int, y : Int }
distance : Point -> Float
distance p = Math.sqrt (toFloat (p.x * p.x + p.y * p.y))

-- ✗ Not this — anonymous inline record type in annotation
distance : { x : Int, y : Int } -> Float
```

**7. Every FFI call returns `Result Error T`.** The FFI boundary is a
trust boundary — Go can panic, return nil, or fail in ways HM can't see.
This applies UNIFORMLY to every FFI shape: method calls, constructors,
field getters, field setters, and package-level var reads. The generated
wrappers recover panics and surface them as `Err(ErrFfi _)`.

```elm
-- ✓ Every Go call is a Result
case Uuid.newString of
    Ok id  -> useId id
    Err _  -> fallbackId

-- ✓ Field getters return Result too — unwrap explicitly
custName =
    Result.withDefault ""
        (Stripe.checkoutSessionCustomerDetails sess
             |> Result.andThen Stripe.checkoutSessionCustomerDetailsName)

-- ✗ Don't treat getter output as bare T — will fail type check
customerDetails = Stripe.checkoutSessionCustomerDetails sess
name = Stripe.checkoutSessionCustomerDetailsName customerDetails  -- Result, not String
if name != "" then ...  -- type error
```

Pipeline-friendly setters compose naturally because each stage is a
Result and `Result.andThen` threads the receiver through:

```elm
-- ✓ Setter pipeline — each stage wraps/passes Result
OpenAi.newChatCompletionMessage ()
    |> Result.andThen (\msg -> OpenAi.chatCompletionMessageSetRole "user" msg)
    |> Result.andThen (\msg -> OpenAi.chatCompletionMessageSetContent body msg)
```

**8. Zero-arg FFI functions are values, not calls.** `Uuid.newString` is a
value (a `Result Error String`), not a function to call. Calling it as
`Uuid.newString ()` is a type error.

**9. Zero-arity Sky top-level declarations are memoised — add a `_` arg if
they read env vars.** `openDb = Db.connect ()` evaluates ONCE at first access
and caches forever. That's fine for DB handles. But a zero-arity function
reading `System.getenv` evaluates at Go `init()` time — BEFORE `.env` is loaded.
Workaround: add a dummy `_` parameter to defer the evaluation.

```elm
-- ✓ Reads env vars at each call (correct)
apiKey _ = System.getenv "OPENAI_API_KEY" |> Result.withDefault ""

-- ✗ Evaluates at init time, before .env loads
apiKey = System.getenv "OPENAI_API_KEY" |> Result.withDefault ""
```

**10. Never write FFI code by hand.** `sky add <package>` generates all
bindings with panic recovery and typed wrappers. Hand-written FFI loses the
safety net.

---

## Common Pitfalls & Fixes

Reference table for the specific errors real Sky projects hit. Scan this
before writing non-trivial code.

| Symptom | Cause | Fix |
|---|---|---|
| `TYPE ERROR: Type mismatch: ( { ... }, Cmd Msg ) vs ( Model, Cmd Msg )` | `init` returns an anonymous record but annotation expects named alias | Make sure field set matches `Model` exactly; if it does and still fails, annotate `init : a -> ( Model, Cmd Msg )` to force the expectation |
| `interface conversion: interface {} is int, not struct {}` at runtime | Annotation says `Result Error ()` but body returns `Result Error Int` | Change annotation to match actual return type, or use `Result.map (\_ -> ())` to discard |
| `undefined: Error` in generated Go | Missing `import Sky.Core.Error as Error exposing (Error)` | Add the import — Prelude does not re-export it |
| Input/button renders as always-disabled | (fixed in v0.9-dev) boolean attrs now honour their value — regenerate sky-out after `sky upgrade` | — |
| `case xs of [] -> _` panics with `[]main.T, not []interface {}` | (fixed in v0.9-dev) typed list patterns | — |
| Add-a-row updates DB but page doesn't refresh | (fixed in v0.9-dev) `rt.RecordUpdate` now narrows `[]any → []T` | — |
| `stack overflow` in `walkGob` at server start | Model stores an opaque FFI handle (DB, HTTP client, Firestore client) with internal pointer cycles | Move the handle out of Model — keep it at top level (Cardinal Rule 4) |
| `not enough arguments in call to rt.Http_request` | (fixed in v0.9-dev) `Http.request` now takes a single record arg | Regenerate sky-out; use `Http.request { method, url, headers, body }` |
| `cannot use generic function init_ without instantiation` | Entry-module TypedDef referenced before its generic params were resolved | (fixed in v0.9-dev) — regenerate sky-out |
| `undefined: Sky_Core_Json_Decode_index` | (fixed in v0.9-dev) `Decode.index` now registered as a kernel function | — |
| `(\row -> { role = ..., content = ... })` parse error | Parser misaligns a multi-line lambda at a tight column | Hoist the lambda body to a top-level function, or use `\row -> { role = field row "role", content = field row "content" }` on ONE line |
| Endpoint card doesn't show when added | Model's list field typed via alias but `loadEndpoints` returns `[]any`; record-update drops the new list | (fixed in v0.9-dev) — regenerate sky-out; if still broken, check the field type matches exactly |
| `Http.request` expects 4 args instead of record | Using an old sky binary from before v0.9 record-style fix | `sky upgrade` and rebuild |
| Compiler silently accepts wrong annotation then panics at runtime | HM edge case — annotation says `Result Error ()` but body returns typed other | Remove the annotation to get the real inferred type in an error, or run with stricter annotations |
| `reflect: Call with too few input arguments` from `List.indexedMap` / `List.foldl` / `Task.andThen` | (fixed in v0.9.10) higher-order combinators now curry top-level multi-arg functions correctly. Same fix covers `Task.andThen (insertRow db)` (let-bound partial app) and `\|> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")` (FFI-setter pipeline) — no more inline-lambda workaround needed | — |
| `data-sky-ev-sky-image` / `data-sky-ev-sky-file` events do nothing | (fixed in v0.9.10) `onImage` / `onFile` now wire end-to-end: server emits `data-sky-ev-…`, JS file driver wraps the data URL in `[…]` to match the wire `Args` shape, `Event_onFile` runtime kernel + `fileMaxSize` attribute helper added | — |

---

## Quick Reference

```bash
sky init [name]           # Create a new Sky project (sky.toml, src/Main.sky, .gitignore, CLAUDE.md)
sky build src/Main.sky    # Compile to Go binary (output: sky-out/app)
sky run src/Main.sky      # Build and run
sky check src/Main.sky    # Type-check without compiling (cross-module ADT + alias resolution)
sky fmt src/Main.sky      # Format code (Elm-style: 4-space indent, leading commas)
sky test tests/MyTest.sky # Run a test module (exposes `tests : List Test`)
sky add <package>         # Add dependency + generate bindings + update sky.toml
sky remove <package>      # Remove dependency from sky.toml + clean cache
sky install               # Install all deps + auto-generate missing bindings
sky update                # Update sky.toml dependencies to latest
sky upgrade               # Self-upgrade Sky compiler to latest release
sky lsp                   # Start Language Server
sky clean                 # Remove build artifacts
sky --version             # Show version
```

## Testing (Sky.Test)

A test module exposes a single `tests : List Test` value. `sky test <path>` compiles it alongside your project and runs every test, failing with exit 1 on any assertion failure.

```elm
module StringTest exposing (tests)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Test as Test exposing (Test)


tests : List Test
tests =
    [ Test.test "trim removes outer spaces" (\_ ->
        Test.equal "hi" (String.trim "  hi  "))
    , Test.test "toInt rejects junk" (\_ ->
        Test.err (String.toInt "abc"))
    , Test.test "contains finds substring" (\_ ->
        Test.isTrue (String.contains "ell" "hello"))
    ]
```

Assertions: `equal`, `notEqual`, `ok`, `err`, `expectErrorKind`, `isTrue`, `isFalse`, `fail`, `pass`.

Non-regression rules (enforced by `sky verify`):

- No `Result String a` or `Task String a` in any public surface — use `Result Error a` / `Task Error a`.
- No `Std.IoError` (deleted), no `RemoteData` (deleted).
- Every bug you fix must land with a regression test in `tests/`.
- `sky check` is a full soundness gate (runs `go build` on the generated Go). Don't work around check failures by disabling it.
- Secrets (`Auth.signToken` / `Auth.verifyToken`) take `String` and reject short keys (< 32 bytes) — don't stringify a `Maybe` or `Dict` into an auth secret.


## Language Syntax

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)    -- auto-imported: Result, Maybe, identity, errorToString
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.Dict as Dict
import Std.Log exposing (println)

-- Type annotations are optional (Hindley-Milner inference)
greet : String -> String
greet name =
    "Hello, " ++ name

-- Algebraic data types
type Shape
    = Circle Float
    | Rectangle Float Float

-- Records (type aliases)
type alias Point = { x : Int, y : Int }

-- Pattern matching (exhaustiveness checked by compiler)
area : Shape -> Float
area shape =
    case shape of
        Circle r -> 3.14 * r * r
        Rectangle w h -> w * h

-- Let-in expressions
main =
    let
        p = { x = 10, y = 20 }
        updated = { p | x = 99 }     -- immutable record update
        items = [1, 2, 3]
            |> List.map (\x -> x * 2)  -- pipeline operator
            |> List.filter (\x -> x > 3)
    in
    println ("Result: " ++ String.fromInt updated.x)
```

### Types

`Int`, `Float`, `String`, `Bool`, `Char`, `Unit` (`()`), `List a`, `Maybe a` (`Just a | Nothing`), `Result err ok` (`Ok ok | Err err`), `Dict k v`, tuples `(a, b)`, records `{ field : Type }`

### Operators

`++` (concat), `|>` `<|` (pipe), `>>` `<<` (compose), `==` `!=` `/=` `<` `>` `<=` `>=`, `&&` `||`, `+` `-` `*` `/` `//` `%`, `::` (cons)

Note: `/=` is Elm-compatible not-equal (alias for `!=`). `//` is integer division (always returns `Int`). Both forms are supported.

### Multiline Strings

Triple-quoted strings preserve newlines. Interpolation uses double braces `{{expr}}`:

```elm
html =
    """<div class="card">
    <h1>{{title}}</h1>
    <p>{{description}}</p>
</div>"""

sql =
    """CREATE TABLE IF NOT EXISTS users (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL
    )"""
```

Single braces `{` are literal — safe for JavaScript, CSS, JSON, SQL. Interpolation expressions support identifiers, field access, qualified names, and function calls.

### Patterns

Literals, constructors (`Just x`, `Ok v`, `Err e`), tuples `(a, b)`, lists `[]`, `[x]`, `x :: xs`, wildcards `_`, as-patterns `Just x as original`, nested `Ok (Just x)`

### Record Patterns

```elm
-- Record patterns (destructuring)
case user of
    { name, age } -> name ++ " is " ++ String.fromInt age

-- In function params
greet { name } = "Hello, " ++ name

-- In let bindings
let { x, y } = point in x + y
```

## Task — Effect Boundary

**Single rule (v1.0+):** every observable side effect returns `Task Error a`. That includes the previously-eager kernels: `println`, `Slog.*`, `Time.now`, `Time.unixMillis`, `System.getenv`, `System.cwd`, `System.args`. The previous "two-tier" doctrine that carved them out as sync convenience effects is gone — see "Auto-force `let _ = TaskExpr`" below for why this is now ergonomic.

Tasks are lazy — they only execute when `Task.run` / `Task.perform` is called, when consumed by `Cmd.perform`, or auto-forced via `let _ =` discard.

The narrow exception is **`System.exit`** which stays `Int -> a` (polymorphic) because it never returns — Task-wrapping it would force every case branch using it as a fatal-error escape to also be Task, with no compensating type information.

```elm
import Sky.Core.Task as Task

-- Create and compose Tasks
pipeline =
    Task.succeed "Sky"
        |> Task.andThen (\name -> Task.succeed ("Hello, " ++ name ++ "!"))
        |> Task.map (\msg -> msg ++ " Pure and reliable.")

-- Execute at the boundary
result = Task.perform pipeline
-- result : Result Error String
```

**Task API:**
- `Task.succeed : a -> Task err a`
- `Task.fail : err -> Task err a`
- `Task.map : (a -> b) -> Task err a -> Task err b`
- `Task.andThen : (a -> Task err b) -> Task err a -> Task err b`
- `Task.perform : Task err a -> Result err a`
- `Task.sequence : List (Task err a) -> Task err (List a)` -- run sequentially
- `Task.parallel : List (Task err a) -> Task err (List a)` -- run concurrently (goroutines)
- `Task.lazy : (() -> a) -> Task err a` -- defer computation until executed
- `Task.map2/3/4/5` -- combine N independent Tasks (sequential, like Result.mapN) (v0.7.25+)
- `Task.andMap : Task e a -> Task e (a -> b) -> Task e b` -- pipeline-style applicative (v0.7.25+)
- `Task.fromResult : Result e a -> Task e a` -- lift a Result-returning step (FFI call, parser) into a Task pipeline
- `Task.andThenResult : (a -> Result e b) -> Task e a -> Task e b` -- chain a Result-returning step after a Task
- `Result.andThenTask : (a -> Task e b) -> Result e a -> Task e b` -- chain a Task-returning step after a Result
- `Task.mapError : (e -> e2) -> Task e a -> Task e2 a` -- transform error type without touching the success path
- `Task.onError : (e -> Task e2 a) -> Task e a -> Task e2 a` -- recover from error to a new Task (HTTP error response, retry, fall back to default)

**Auto-force `let _ = TaskExpr`** (v1.0+). The compiler special-cases `let _ = X in Y` discards: when X has type `Task e a`, the lowerer emits `_ = rt.AnyTaskRun(X)` so the Task thunk is forced and the side effect fires. Without this, every kernel-Task call would silently no-op when discarded. With this, the pervasive debug-trace pattern keeps working unchanged:

```elm
let
    _ = println "step 1"        -- Task auto-forced, print fires
    _ = println "step 2"        -- same
    _ = Log.infoWith "saving" [ "id", id ]
in
    continue
```

**Top-level bindings stay explicit.** Auto-force only applies to `let _ =` discards. Reading a Task-typed value at module scope requires explicit `Task.run`:

```elm
-- Module top-level
apiKey =
    System.getenv "OPENAI_KEY"
        |> Task.run
        |> Result.withDefault ""
```

**Db.* returns Task.** `Db.connect` / `Db.open` / `Db.exec` / `Db.execRaw` / `Db.query` / `Db.queryDecode` / `Db.insertRow` / `Db.{getById, updateById, deleteById}` / `Db.{findOneByField, findManyByField, findByConditions, withTransaction}` all return `Task Error a` and compose directly with `Task.andThen` / `Task.parallel` / `Cmd.perform`. Pure dict accessors (`Db.getField` / `getString` / `getInt` / `getBool`) stay bare — they read from a row dict, no I/O. Auth side effects (`Auth.register` / `login` / `setRole`) are Task; pure CPU ones (`Auth.hashPassword` / `verifyPassword` / `signToken` / `verifyToken`) are Result.

**Db chain (clean Task composition):**

```elm
loadUserNotes : String -> Task Error (List Note)
loadUserNotes userId =
    Db.open "sqlite" "app.db"
        |> Task.andThen (\db ->
            Db.query db "SELECT id, title, body FROM notes WHERE user_id = ?" [userId])
        |> Task.map (List.map parseNote)
```

**Two-level error handling (canonical pattern for production):**

```elm
import Sky.Core.Crypto as Crypto
import Std.Log as Log

-- Server-side: Slog with errId for ops to grep.
-- Client-side: Task.fail with same errId in user-friendly message.
withErrorReporting : String -> Task Error a -> Task Error a
withErrorReporting opName task =
    task |> Task.onError (\e ->
        Crypto.randomToken 4
            |> Task.andThen (\errId ->
                let _ = Log.errorWith opName [ "errId", errId, "error", Error.toString e ]
                in  Task.fail (Error.unexpected
                        ("Operation failed (ref " ++ errId ++ ")"))))

-- Apply at the source of effectful pipelines:
saveOrder : Order -> Task Error String
saveOrder order =
    Db.exec ... |> withErrorReporting "order.save"
```

**Bridges (when you mix Result-returning FFI with Task pipelines):**

The bridges still earn their keep for genuinely Result-returning FFI (Stripe builder pattern, JSON parsers, validators) — they're no longer needed for Db (which is now Task) but still useful when:

```elm
-- Stripe builder returns Result through |> Result.andThen chains.
-- Lift the final Result into a Task pipeline that does HTTP afterwards.
createCheckout : String -> Task Error CheckoutSession
createCheckout url =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetSuccessURL url)
        |> Result.andThenTask (\params ->
            Stripe.newCheckoutSession params       -- Result Error CheckoutSession
                |> Task.fromResult
                |> Task.andThen registerWebhook)   -- Task Error CheckoutSession
```

There is no `Result.fromTask` / `Task -> Result` bridge by design. `Task.run` exists for the runtime entry boundary (CLI `main`, test fixtures, Lib wrappers that need a sync result for `case`-pattern matching), but user code should keep effectful pipelines in `Task` and let the boundary (`Cmd.perform`, HTTP handler return) execute them.

**Per-app shape error handling:**
- **CLI** (`07-todo-cli`): `main = let _ = Task.run (chain |> Task.onError reportError) in ()` where `reportError` Slogs + prints to stderr + `System.exit 1`.
- **Sky.Http.Server**: handler returns `Task Error Response`; `|> Task.onError (\e -> Task.succeed (Server.withStatus 500 (Server.json (errorJson errId))))` recovers errors to a Response.
- **Sky.Live** (`18-job-queue`, etc.): `Cmd.perform task ResultMsg` dispatches; the `ResultMsg` handler updates a `notification` / `error` field in Model that the `view` renders as a banner. The errId surfaces in the banner so users can quote it in support requests.

**Concurrency:**

`Task.parallel` runs tasks concurrently using Go goroutines. Results are collected in order; the first error short-circuits.

```elm
-- Parallel HTTP requests (total time = slowest request, not sum)
results = Task.perform (Task.parallel [ Http.get url1, Http.get url2, Http.get url3 ])

-- Sequential for comparison (total time = sum of all requests)
results = Task.perform (Task.sequence [ Http.get url1, Http.get url2, Http.get url3 ])
```

`List.parallelMap` maps a function over a list using goroutines (pure, no Task wrapping):

```elm
-- Process items concurrently
squares = List.parallelMap (\n -> n * n) [ 1, 2, 3, 4, 5 ]
-- [1, 4, 9, 16, 25]
```

## Go Interop (FFI)

Sky can import any Go package. The compiler auto-generates type-safe
**Result-wrapped** bindings at build time. Every FFI call returns
`Result Error T` because the FFI boundary is a trust boundary —
Go can panic, return nil, or fail in ways Sky's type checker can't
see. The Result wrapping forces explicit handling at every call site
(like Rust's `?`). When in doubt, prefer Sky's stdlib.

```elm
import Net.Http as Http                    -- net/http
import Github.Com.Google.Uuid as Uuid      -- github.com/google/uuid
import Database.Sql as Sql                 -- database/sql
import Drivers.Sqlite as _ exposing (..)   -- side-effect import (Go driver)
import Sky.Core.Result as Result
import Sky.Core.Error as Error
import Std.Log exposing (println)


-- Typical pattern: case-match every FFI call
example =
    case Uuid.newString of
        Ok id ->
            println id

        Err e ->
            println ("uuid failed: " ++ Error.toString e)
```

### Naming Convention

| Go | Sky | Pattern |
|----|-----|---------|
| `uuid.NewString()` | `Uuid.newString ()` | Package function |
| `router.HandleFunc(p, h)` | `Mux.routerHandleFunc router p h` | Method: `{Type}{Method}` |
| `db.Query(q, args...)` | `Sql.dbQuery db q args` | Method on `*sql.DB` |
| `req.URL` (field) | `Http.requestUrl req` | Field: `{Type}{Field}` |
| `http.StatusOK` (const) | `Http.statusOK ()` | Constant: zero-arg function |

### Return Type Mapping

Every FFI call returns `Result Error T`. The Ok side's shape depends
on what Go returns:

| Go Return | Sky Return |
|-----------|------------|
| `T` (single, no error) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error T` (opaque; nil-deref → Err via recover) |
| `(T, error)` | `Result Error T` |
| `(T, *NamedErr)` where NamedErr implements error | `Result Error T` |
| `(T1, T2, error)` | `Result Error (Tuple2 T1 T2)` |
| `error` | `Result Error Unit` |
| `(T, bool)` (comma-ok) | `Result Error (Maybe T)` |
| `*sql.DB` | `Result Error Db` (opaque handle) |
| `[]string` | `Result Error (List String)` |
| `map[string]int` | `Result Error (Dict String Int)` |
| void | `Result Error Unit` |

For `(T, bool)` comma-ok returns you handle two layers: the Result
captures boundary failure (panic, type mismatch); the Maybe captures
Go's "nothing here" signal (comma-ok false).

Bare `*T` returns aren't auto-wrapped in `Maybe` — many Go SDKs
chain pointer returns through builders/getters and wrapping every
hop in `Maybe` would break the chain. Genuine "may be nil" is
expressed via `(T, error)` or `(T, bool)` in idiomatic Go.

Method calls on a nil opaque return `Err(ErrFfi "nil receiver: ...")`
instead of panicking — every method/getter/setter wrapper has a
nil-receiver guard.

### Opaque Structs (Builder Pattern)

All Go structs are opaque types in Sky. Use constructors + pipeline setters:

```elm
-- Constructor: Pkg.newTypeName ()
-- Getter:     Pkg.typeNameFieldName receiver
-- Setter:     Pkg.typeNameSetFieldName value receiver  (pipeline-friendly)

-- Example: build a Stripe checkout session
-- Each setter returns Result Error TypeName, so chain via Result.andThen.
result =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetSuccessURL url)
        |> Result.andThen (Stripe.checkoutSessionParamsSetCustomer customerId)
        |> Result.andThen Session.new
```

Pointer fields (`*string`, `*int64`, `*bool`) are handled automatically — pass the plain value.

### Error Handling — `Sky.Core.Error` (v0.9+ canonical)

Since v0.9 every fallible operation uses `Sky.Core.Error`, a structured
ADT with eleven kinds. No more `Result String` or `Task String` in
public APIs.

```elm
import Sky.Core.Error as Error exposing (Error(..), ErrorKind(..))

-- Type: Error ErrorKind ErrorInfo
-- ErrorKind: Io | Network | Ffi | Decode | Timeout | NotFound
--         | PermissionDenied | InvalidInput | Conflict | Unavailable | Unexpected

-- Constructors:
Error.io : String -> Error
Error.network : String -> Error
Error.ffi : String -> Error
Error.decode : String -> Error
Error.timeout : String -> Error
Error.notFound : Error
Error.permissionDenied : Error
Error.invalidInput : String -> Error
Error.conflict : String -> Error
Error.unavailable : String -> Error
Error.unexpected : String -> Error

-- Builders:
Error.withMessage : String -> Error -> Error
Error.withDetails : ErrorDetails -> Error -> Error
Error.toString : Error -> String
Error.isRetryable : Error -> Bool
```

**Standard library kernels return Error directly**:

```elm
-- Db.exec : Db -> String -> List any -> Result Error Int
-- Http.get : String -> Task Error Response
-- File.readFile : String -> Task Error String

-- In your Lib wrappers, pass the error through — DO NOT re-wrap:
exec queryStr args =
    case Db.exec dbConn queryStr args of
        Ok _ ->
            Ok ()
        Err e ->
            Err e    -- already a Sky.Core.Error
```

**In `update` handlers**: pattern-match on kind. Log AND set UI state:

```elm
handleSignIn model =
    case Auth.authenticateUser model.email model.password of
        Ok user ->
            ( { model | currentUser = Just user, page = Home }, Cmd.none )

        Err (Error kind info) ->
            let
                _ = println ("[AUTH ERROR] " ++ info.message)
                msg =
                    case kind of
                        PermissionDenied -> info.message
                        InvalidInput -> info.message
                        _ -> "Service unavailable"
            in
                ( { model | error = msg }, Cmd.none )
```

See `docs/errors/error-system.md` in the Sky repo for the full reference.

See `examples/12-skyvote` for the canonical end-to-end reference.

## Standard Library — Complete API

### Sky.Core.Prelude (auto-imported)

```elm
-- Types
type Result err ok = Ok ok | Err err
type Maybe a = Just a | Nothing    -- (defined in Sky.Core.Maybe)

-- Functions
identity : a -> a
always : a -> b -> a
fst : (a, b) -> a
snd : (a, b) -> b
clamp : comparable -> comparable -> comparable -> comparable
modBy : Int -> Int -> Int
errorToString : a -> String        -- converts Go error to String
```

### Sky.Core.Maybe

```elm
type Maybe a = Just a | Nothing

withDefault : a -> Maybe a -> a
map : (a -> b) -> Maybe a -> Maybe b
map2 : (a -> b -> c) -> Maybe a -> Maybe b -> Maybe c
map3 : (a -> b -> c -> d) -> Maybe a -> Maybe b -> Maybe c -> Maybe d
andThen : (a -> Maybe b) -> Maybe a -> Maybe b
```

### Sky.Core.Result

```elm
map : (a -> b) -> Result e a -> Result e b
andThen : (a -> Result e b) -> Result e a -> Result e b
withDefault : a -> Result e a -> a
fromMaybe : e -> Maybe a -> Result e a
mapError : (e -> f) -> Result e a -> Result f a

-- Applicative combinators (v0.7.25+)
map2 : (a -> b -> c) -> Result e a -> Result e b -> Result e c
map3 : (a -> b -> c -> d) -> Result e a -> Result e b -> Result e c -> Result e d
map4 : (a -> b -> c -> d -> f) -> Result e a -> Result e b -> Result e c -> Result e d -> Result e f
map5 : (a -> b -> c -> d -> f -> g) -> Result e a -> Result e b -> Result e c -> Result e d -> Result e f -> Result e g
andMap : Result e a -> Result e (a -> b) -> Result e b      -- pipeline-style for arity > 5
combine : List (Result e a) -> Result e (List a)             -- collect a homogeneous list
traverse : (a -> Result e b) -> List a -> Result e (List b)  -- map then combine
```

**When to use which:**
- `map2..5` — combine N **independent** Results of **different types** into a record. Each parser fails-fast with the first Err. Perfect for form validation, JSON-style record building.
- `andMap` — same idea but pipeline-style; use for arity > 5 fields. `Ok make |> andMap a |> andMap b |> ...`
- `combine` — collect a **homogeneous list** of Results. `[Ok 1, Ok 2, Ok 3]` → `Ok [1, 2, 3]`. First Err short-circuits.
- `traverse` — `combine << List.map f`. Map a fallible function over a list.
- `andThen` — for **dependent** computations where each step needs the previous result. Sequential by nature.

### Auto record constructors (v0.7.26+)

Every record type alias **automatically generates a constructor function** with the same name. Field declaration order in the type alias is the positional argument order of the constructor.

```elm
type alias Profile =
    { name : String
    , age : Int
    , active : Bool
    }

-- Sky auto-generates:
--   Profile : String -> Int -> Bool -> Profile
--   Profile name age active = { name = name, age = age, active = active }

-- Use it directly:
alice = Profile "Alice" 30 True

-- Or with applicative combinators (no makeProfile helper needed):
result =
    Result.map3 Profile
        (parseString "name" formData.name)
        (parseInt "age" formData.age)
        (parseBool "active" formData.active)
```

This is exactly Elm's behavior. Notes:

- Only **record** type aliases generate constructors. Aliases like `type alias Name = String` don't.
- If you define a function with the same name as the type alias, **your definition wins** — Sky skips the auto-generation. This lets you provide a custom constructor with validation, defaults, etc.
- Adding a field in the middle of a type alias is a **breaking change** for any code that uses the constructor positionally. Same trade-off Elm made.
- Constructors are exported from a module the same way the type alias is. `module Foo exposing (Profile)` exposes both the type and the constructor.

### Sky.Core.List

```elm
map : (a -> b) -> List a -> List b
filter : (a -> Bool) -> List a -> List a
foldl : (a -> b -> b) -> b -> List a -> b
foldr : (a -> b -> b) -> b -> List a -> b
head : List a -> Maybe a
tail : List a -> Maybe (List a)
length : List a -> Int
append : List a -> List a -> List a
reverse : List a -> List a
member : a -> List a -> Bool
range : Int -> Int -> List Int            -- inclusive range
isEmpty : List a -> Bool
take : Int -> List a -> List a
drop : Int -> List a -> List a
sort : List comparable -> List comparable
intersperse : a -> List a -> List a
concat : List (List a) -> List a
concatMap : (a -> List b) -> List a -> List b
indexedMap : (Int -> a -> b) -> List a -> List b
singleton : a -> List a
all : (a -> Bool) -> List a -> Bool
any : (a -> Bool) -> List a -> Bool
sum : List Int -> Int
product : List Int -> Int
maximum : List comparable -> Maybe comparable
minimum : List comparable -> Maybe comparable
partition : (a -> Bool) -> List a -> (List a, List a)
find : (a -> Bool) -> List a -> Maybe a
filterMap : (a -> Maybe b) -> List a -> List b
sortBy : (a -> comparable) -> List a -> List a
zip : List a -> List b -> List (a, b)
unzip : List (a, b) -> (List a, List b)
map2 : (a -> b -> c) -> List a -> List b -> List c
parallelMap : (a -> b) -> List a -> List b  -- goroutine-backed concurrent map
```

### Sky.Core.String

```elm
fromInt : Int -> String
fromFloat : Float -> String
toInt : String -> Maybe Int        -- Just n on success, Nothing on parse fail
toFloat : String -> Maybe Float    -- Just f on success, Nothing on parse fail
split : String -> String -> List String   -- split sep str
join : String -> List String -> String    -- join sep parts
contains : String -> String -> Bool       -- contains sub str
replace : String -> String -> String -> String  -- replace old new str
trim : String -> String
length : String -> Int
toLower : String -> String
toUpper : String -> String
startsWith : String -> String -> Bool
endsWith : String -> String -> Bool
slice : Int -> Int -> String -> String    -- slice start end str
isEmpty : String -> Bool
lines : String -> List String
words : String -> List String
repeat : Int -> String -> String
padLeft : Int -> String -> String -> String
padRight : Int -> String -> String -> String
left : Int -> String -> String
right : Int -> String -> String
reverse : String -> String
indexes : String -> String -> List Int
concat : List String -> String
fromChar : Char -> String
toBytes : String -> Bytes             -- String to []byte
fromBytes : Bytes -> String           -- []byte to String
```

### Sky.Core.Dict

```elm
empty : Dict k v
singleton : k -> v -> Dict k v
insert : k -> v -> Dict k v -> Dict k v
get : k -> Dict k v -> Maybe v
remove : k -> Dict k v -> Dict k v
keys : Dict k v -> List k
values : Dict k v -> List v
map : (k -> v -> b) -> Dict k v -> Dict k b
foldl : (k -> v -> b -> b) -> b -> Dict k v -> b
fromList : List (k, v) -> Dict k v
toList : Dict k v -> List (k, v)
isEmpty : Dict k v -> Bool
size : Dict k v -> Int
member : k -> Dict k v -> Bool
update : k -> (Maybe v -> Maybe v) -> Dict k v -> Dict k v
filter : (k -> v -> Bool) -> Dict k v -> Dict k v
union : Dict k v -> Dict k v -> Dict k v
intersect : Dict k v -> Dict k v -> Dict k v
diff : Dict k v -> Dict k v -> Dict k v
partition : (k -> v -> Bool) -> Dict k v -> (Dict k v, Dict k v)
foldr : (k -> v -> b -> b) -> b -> Dict k v -> b
```

### Sky.Core.Char

Unicode-aware character classification (backed by Go's `unicode` package):

```elm
isUpper : Char -> Bool      -- unicode.IsUpper (supports accented chars)
isLower : Char -> Bool      -- unicode.IsLower
isAlpha : Char -> Bool      -- unicode.IsLetter (all Unicode letters)
isDigit : Char -> Bool      -- unicode.IsDigit (all Unicode digits)
isAlphaNum : Char -> Bool   -- IsLetter || IsDigit
toUpper : Char -> Char
toLower : Char -> Char
toCode : Char -> Int
fromCode : Int -> Char
```

### Sky.Core.Tuple

```elm
first : (a, b) -> a
second : (a, b) -> b
mapFirst : (a -> c) -> (a, b) -> (c, b)
mapSecond : (b -> c) -> (a, b) -> (a, c)
mapBoth : (a -> c) -> (b -> d) -> (a, b) -> (c, d)
pair : a -> b -> (a, b)
```

### Sky.Core.Bitwise

```elm
and : Int -> Int -> Int
or : Int -> Int -> Int
xor : Int -> Int -> Int
complement : Int -> Int
shiftLeftBy : Int -> Int -> Int
shiftRightBy : Int -> Int -> Int
shiftRightZfBy : Int -> Int -> Int
```

### Sky.Core.Set

```elm
empty : Set a
singleton : a -> Set a
insert : a -> Set a -> Set a
remove : a -> Set a -> Set a
member : a -> Set a -> Bool
size : Set a -> Int
isEmpty : Set a -> Bool
toList : Set a -> List a
fromList : List a -> Set a
union : Set a -> Set a -> Set a
intersect : Set a -> Set a -> Set a
diff : Set a -> Set a -> Set a
map : (a -> b) -> Set a -> Set b
filter : (a -> Bool) -> Set a -> Set a
foldl : (a -> b -> b) -> b -> Set a -> b
```

### Sky.Core.Array

```elm
empty : Array a
fromList : List a -> Array a
toList : Array a -> List a
get : Int -> Array a -> Maybe a
set : Int -> a -> Array a -> Array a
push : a -> Array a -> Array a
length : Array a -> Int
slice : Int -> Int -> Array a -> Array a
map : (a -> b) -> Array a -> Array b
foldl : (a -> b -> b) -> b -> Array a -> b
foldr : (a -> b -> b) -> b -> Array a -> b
append : Array a -> Array a -> Array a
indexedMap : (Int -> a -> b) -> Array a -> Array b
```

### Sky.Core.File

All fallible IO lives in `Task`. Execute with `Task.run` for a synchronous
`Result Error a`, or pass through `Cmd.perform` in a Sky.Live app.

```elm
readFile : String -> Task Error String
readFileLimit : String -> Int -> Task Error String       -- bounded read (default 100 MiB)
readFileBytes : String -> Task Error Bytes               -- binary
writeFile : String -> String -> Task Error ()
append : String -> String -> Task Error ()               -- creates if missing
exists : String -> Bool                                  -- pure, no Task wrapping
isDir : String -> Bool                                   -- pure
remove : String -> Task Error ()
mkdirAll : String -> Task Error ()
readDir : String -> Task Error (List String)
tempFile : String -> Task Error String                   -- returns path
tempDir : String -> Task Error String                    -- returns path
copy : String -> String -> Task Error ()
rename : String -> String -> Task Error ()
```

Usage:
```elm
loadConfig : Task Error Config
loadConfig =
    File.readFile "config.toml"
        |> Task.andThen parseConfig

-- Synchronous extraction (at a main-ish boundary, or when you know
-- you can block):
main =
    case Task.run loadConfig of
        Ok config -> runApp config
        Err e -> println ("Failed to load config: " ++ Error.toString e)
```

### Sky.Core.System

The single OS-interaction kernel (v0.10.0+ — replaces `Os`, `Args`,
`Std.Env`, and the env/exit/cwd half of `Process`). All Task-wrapped
per the Task-everywhere doctrine; `let _ = …` discards auto-force.

```elm
args        : ()  -> Task Error (List String)         -- os.Args[1:] minus program name
getArg      : Int -> Task Error (Maybe String)        -- nth element of os.Args (0-indexed)
getenv      : String -> Task Error String             -- Err if unset
getenvOr    : String -> String -> Task Error String   -- env var or default; never errs
getenvInt   : String -> Task Error Int                -- typed; Err on missing or unparseable
getenvBool  : String -> Task Error Bool               -- accepts true/yes/1/on or false/no/0/off
cwd         : ()  -> Task Error String                -- current working directory
exit        : Int -> a                                -- diverging — process terminates
loadEnv     : ()  -> Task Error ()                    -- load .env from cwd into the process env
```

Production deployments override env via the process environment (Docker
`ENV`, k8s, CI vars). `loadEnv ()` is for local dev — never overrides
existing vars.

### Sky.Core.Process

```elm
run : String -> List String -> Task Error String      -- subprocess: combined stdout+stderr
```

That's the whole module. `exit` / `getEnv` / `getCwd` / `loadEnv`
moved to `System.*` in v0.10.0 — there's no longer a `Process.exit`.

### Sky.Core.Debug

```elm
log : String -> a -> a          -- prints tag + value, returns value unchanged
toString : a -> String          -- convert any value to string representation
```

### Sky.Core.Json.Encode

```elm
encode : Int -> Value -> String       -- serialise with indentation
string : String -> Value
int : Int -> Value
float : Float -> Value
bool : Bool -> Value
null : Value
list : (a -> Value) -> List a -> Value
object : List (String, Value) -> Value
```

### Sky.Core.Json.Decode

```elm
decodeString : Decoder a -> String -> Result Error a
decodeValue : Decoder a -> Value -> Result Error a
string : Decoder String
int : Decoder Int
float : Decoder Float
bool : Decoder Bool
null : a -> Decoder a
nullable : Decoder a -> Decoder (Maybe a)
value : Decoder Value
list : Decoder a -> Decoder (List a)
dict : Decoder a -> Decoder (Dict String a)
field : String -> Decoder a -> Decoder a
at : List String -> Decoder a -> Decoder a
index : Int -> Decoder a -> Decoder a
map : (a -> b) -> Decoder a -> Decoder b
map2 : (a -> b -> c) -> Decoder a -> Decoder b -> Decoder c
map3 .. map8 : combine up to 8 decoders
succeed : a -> Decoder a
fail : String -> Decoder a
andThen : (a -> Decoder b) -> Decoder a -> Decoder b
oneOf : List (Decoder a) -> Decoder a
maybe : Decoder a -> Decoder (Maybe a)
lazy : (() -> Decoder a) -> Decoder a
```

### Sky.Core.Json.Decode.Pipeline

```elm
-- Usage: Decode.succeed MyType |> required "field" Decode.string |> required "age" Decode.int
required : String -> Decoder a -> Decoder (a -> b) -> Decoder b
requiredAt : List String -> Decoder a -> Decoder (a -> b) -> Decoder b
optional : String -> Decoder a -> a -> Decoder (a -> b) -> Decoder b
optionalAt : List String -> Decoder a -> a -> Decoder (a -> b) -> Decoder b
hardcoded : a -> Decoder (a -> b) -> Decoder b
custom : Decoder a -> Decoder (a -> b) -> Decoder b
```

### Std.Log

The single logging surface (v0.10.0+ — absorbed `Slog`).

```elm
println : String -> Task Error ()
debug   : String -> Task Error ()
info    : String -> Task Error ()
warn    : String -> Task Error ()
error   : String -> Task Error ()

-- structured variants — second arg is `[ "k1", "v1", "k2", "v2", … ]`
debugWith : String -> List a -> Task Error ()
infoWith  : String -> List a -> Task Error ()
warnWith  : String -> List a -> Task Error ()
errorWith : String -> List a -> Task Error ()
```

Configure at runtime via env vars (or sky.toml `[log]` defaults):
- `SKY_LOG_FORMAT` = `plain` (default) | `json`
- `SKY_LOG_LEVEL`  = `debug` | `info` (default) | `warn` | `error`

### Std.Cmd

```elm
type Cmd msg = Cmd Foreign

none : Cmd msg
perform : Task err a -> (Result err a -> msg) -> Cmd msg
batch : List (Cmd msg) -> Cmd msg
```

`Cmd.perform` runs a Task in a background goroutine. When it completes, the result is dispatched as a Msg through the full update/view/diff/SSE cycle:

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

Use `Cmd.batch` to run multiple commands concurrently:
```elm
Cmd.batch
    [ Cmd.perform task1 Msg1
    , Cmd.perform task2 Msg2
    ]
```

### Std.Sub

```elm
type Sub msg = SubNone | SubTimer Int msg | SubBatch (List (Sub msg))

none : Sub msg
batch : List (Sub msg) -> Sub msg
```

### Std.Time

```elm
every : Int -> msg -> Sub msg    -- timer subscription, fires msg every N milliseconds
```

### Sky.Core.Time

```elm
sleep : Int -> Task Error ()    -- sleep for N milliseconds (use with Cmd.perform for async delays)
now : () -> Task Error Int      -- current Unix time in milliseconds
```

### Sky.Core.Random

```elm
int : Int -> Int -> Task Error Int       -- random int in [lo, hi] range
float : Float -> Float -> Task Error Float
choice : List a -> Task Error a          -- random element from list
shuffle : List a -> Task Error (List a)  -- Fisher-Yates shuffle
```

### Std.Html

Html functions return VNode records (not strings). For non-Live apps, use `render` to convert to HTML string.

```elm
-- Core
text : String -> VNode                                    -- escaped text
raw : String -> VNode                                     -- raw HTML (trusted only)
node : String -> List (String, String) -> List VNode -> VNode
render : VNode -> String                                  -- VNode → HTML string
toString : VNode -> String                                -- alias for render

-- Document: htmlNode, headNode, body, doctype
-- Sectioning: div, section, article, aside, headerNode, footerNode, nav, mainNode
-- Headings: h1, h2, h3, h4, h5, h6
-- Text: p, span, strong, em, small, pre, codeNode, blockquote, a
-- Lists: ul, ol, li
-- Forms: form, label, button, textarea, select, option, fieldset, legend
-- Tables: table, thead, tbody, tfoot, tr, th, td
-- Void (no children): input, br, hr, img, meta, linkNode
-- Special: script (raw JS), styleNode (raw CSS), titleNode
```

All element functions have signature: `List (String, String) -> List VNode -> VNode`
Void elements: `List (String, String) -> VNode`

**Important naming**: HTML5 elements that clash with common identifiers use suffixed names: `headerNode` (not `header`), `footerNode` (not `footer`), `mainNode`, `codeNode`, `linkNode`, `styleNode`, `titleNode`. The `textarea` function takes **2 arguments**: `textarea attrs children` (not just attrs).

### Std.Html.Attributes

All return `(String, String)` tuples.

```elm
attribute : String -> String -> (String, String)    -- generic key-value
boolAttribute : String -> (String, String)          -- boolean (no value)

-- Global: class, id, style, title, hidden, tabindex, lang, dir, role
-- Links: href, target, rel, download
-- Forms: type_, name, value, placeholder, action, method, for, enctype
--   required, disabled, checked, readonly, autofocus, multiple, selected
--   autocomplete, minlength, maxlength, min, max, step, pattern, rows, cols
-- Media: src, alt, width, height
-- Meta: charset, content, httpEquiv
-- Tables: colspan, rowspan, scope
-- ARIA: ariaLabel, ariaHidden, ariaDescribedby, ariaExpanded
-- Data: dataAttribute key value
```

### Std.Css

CSS functions return `String`. Use with `styleNode [] (stylesheet [...])`.

```elm
-- Composition
stylesheet : List String -> String    -- join rules
rule : String -> List String -> String    -- selector { props }
media : String -> List String -> String   -- @media query { rules }

-- Units: px, rem, em, pct, vh, vw, ch, fr, sec, ms, deg
-- Keywords: zero, auto, none, inherit
-- Colors: hex, rgb, rgba, hsl, hsla, transparent

-- Layout: display, position, top, right_, bottom, left, zIndex, overflow, float
-- Flexbox: flexDirection, flexWrap, justifyContent, alignItems, alignContent, flex, gap
-- Grid: gridTemplateColumns, gridTemplateRows, gridColumn, gridRow
-- Spacing: margin, margin2, margin4, marginTop, padding, padding2, padding4, paddingTop
-- Sizing: width, height, maxWidth, minWidth, maxHeight, minHeight
-- Typography: fontFamily, fontSize, fontWeight, fontStyle, lineHeight, textAlign,
--   textDecoration, textTransform, letterSpacing, wordSpacing, color
-- Background: backgroundColor, backgroundImage, backgroundSize, backgroundPosition
-- Border: border, borderTop, borderBottom, borderLeft, borderRight, borderRadius,
--   borderColor, borderWidth, borderStyle
-- Effects: boxShadow, opacity, transition, transform
-- Misc: cursor, property (for any CSS property not covered above)
```

### Std.Live

```elm
app : config -> config     -- marks as Sky.Live app (compiler detects this)
route : String -> page -> (String, page)   -- route "/" MyPage (supports :param)
```

### Std.Live.Events

All return `(String, String)` attribute tuples.

```elm
onClick : msg -> (String, String)          -- typed Msg constructor
onInput : (String -> msg) -> (String, String)  -- sends input value with msg
onSubmit : msg -> (String, String)         -- sends form data with msg
onChange : (String -> msg) -> (String, String)  -- for select, checkbox
onDblClick : msg -> (String, String)
onFocus : msg -> (String, String)
onBlur : msg -> (String, String)
onImage : (String -> msg) -> (String, String)  -- image input: resize + compress + base64
onFile : (String -> msg) -> (String, String)   -- file input: base64 data URL (no compress)
fileMaxWidth : Int -> (String, String)         -- max image width in px (onImage, default 1200)
fileMaxHeight : Int -> (String, String)        -- max image height in px (onImage, default 1200)
fileMaxSize : Int -> (String, String)          -- max file size in bytes; over-limit files are dropped client-side (no dispatch) + console.warn

-- Usage:
--     button [ onClick Increment ] [ text "+" ]
--     input [ onInput UpdateDraft, value model.draft ] []
--     form [ onSubmit AddTodo ] [ ... ]
--     input [ type_ "file", attribute "accept" "image/*"
--           , onImage UpdateImage, fileMaxWidth 1200 ] []
```

**Sensitive inputs (passwords, API keys, card details): collect via `onSubmit` form data, not `onInput` per keystroke.** This is the recommended pattern as of v0.9.8:

```elm
type alias AuthCreds =
    { email : String, password : String }

type Msg
    = UpdateEmail String
    | DoSignIn AuthCreds

view model =
    form [ onSubmit DoSignIn ]
        [ input
            [ type_ "email"
            , name "email"            -- required: name is the formData key
            , value model.email       -- email is fine to round-trip via Model
            , onInput UpdateEmail
            ] []
        , input
            [ type_ "password"
            , name "password"
            -- no `value` attr (don't round-trip the secret through DOM)
            -- no `onInput`     (don't dispatch per keystroke)
            ] []
        , button [ type_ "submit" ] [ text "Sign in" ]
        ]

update msg model =
    case msg of
        UpdateEmail e ->
            ( { model | email = e }, Cmd.none )

        DoSignIn creds ->
            -- creds.email and creds.password come straight from a typed
            -- record decode at the dispatch boundary (v0.9.8+).
            ( model, Cmd.perform (signIn creds) GotAuth )
```

Why:

1. **No password-manager extension churn.** 1Password / Bitwarden / browser autofill watch DOM mutations on password inputs. Every server-driven re-render that includes `value="…"` on the password input looks like the form changed and triggers a re-prompt / re-fill cycle (focus juddering, autofill mid-word, selection lost). With no `value` attr and no `onInput`, the password input's DOM stays untouched between user keystrokes and submit.

2. **Secret never lives in Model.** Without a `UpdateAuthPassword` Msg you can't store the password in your Model, so it's never serialised into the session store (Redis, Postgres, etc.). It exists only in the browser DOM until form submit, then briefly in the `DoSignIn` Msg's record argument until `update` consumes it. Per-keystroke handlers carry a partial password through every store round-trip — avoid them for secrets.

3. **Race-free submit.** Per-keystroke `onInput` debounces (150ms) can drop the last keystroke if the user hits Enter before the debounce settles — the auth attempt then sees the wrong password and the user retries blind. Form submit reads the live DOM value, so whatever's in the input at submit time is what gets sent.

The `DoSignIn AuthCreds` constructor takes a typed record alias — the dispatch boundary in v0.9.8+ JSON-decodes the wire form data directly into `State_AuthCreds_R{Email, Password}` via Go's case-insensitive struct field matching. No runtime guessing, no per-Msg decoder boilerplate. Same pattern works for any sensitive multi-field form (API keys, addresses, card details).

The older `onChange` pattern (fires on blur) is still acceptable when you need the password in Model for validation feedback before submit, but prefer `onSubmit` + typed record for normal sign-in / sign-up flows.

### Escape Hatch & View Types

```elm
-- `js` is a Prelude function for embedding raw JS/Go expressions (use sparingly)
js : String -> a

-- View functions should annotate their return type as VNode:
view : Model -> VNode
view model =
    div [] [ text "hello" ]
```

### Sky.Core.Math (pure)

```elm
Math.sqrt 16.0        -- 4.0
Math.pow 2.0 10.0     -- 1024.0
Math.abs -5            -- 5
Math.floor 3.7         -- 3
Math.ceil 3.2          -- 4
Math.round 3.5         -- 4
Math.pi                -- 3.14159...
Math.sin, Math.cos, Math.tan, Math.atan2
Math.min 3 7           -- 3
Math.max 3 7           -- 7
```

### Sky.Core.Time (mixed pure + Task)

```elm
Time.now ()            -- Task Error Int (Unix millis)
Time.format "2006-01-02" millis  -- pure: "2025-03-25"
Time.parse "2006-01-02" "2025-03-25"  -- Result Error Int
Time.year millis, Time.month, Time.day, Time.hour, Time.minute, Time.second
Time.sleep 1000        -- Task Error () (sleep 1 second)
```

### Sky.Core.Http (Task)

```elm
Http.get "https://api.example.com/data"     -- Task Error Response
Http.post url body                            -- Task Error Response
Http.request { method, url, headers, body }   -- Task Error Response

-- Response = { status : Int, body : String, headers : List (String, String) }
```

### Sky.Core.Encoding (pure)

```elm
Encoding.base64Encode "Hello"   -- "SGVsbG8="
Encoding.base64Decode "SGVsbG8="  -- Ok "Hello"
Encoding.urlEncode "hello world"  -- "hello+world"
Encoding.hexEncode "Hi"           -- "4869"
```

### Sky.Core.Regex (pure)

```elm
Regex.match "[0-9]+" "abc123"        -- True
Regex.find "[0-9]+" "abc123def"      -- Just "123"
Regex.findAll "[0-9]+" "a1b2c3"      -- ["1", "2", "3"]
Regex.replace "[0-9]" "#" "abc123"   -- "abc###"
Regex.split "[,;]" "a,b;c"           -- ["a", "b", "c"]
```

### Sky.Core.Crypto (pure)

```elm
Crypto.sha256 "hello"      -- "2cf24dba..."
Crypto.hmacSha256 "key" "msg"  -- HMAC signature
```

### Sky.Core.Random (Task)

```elm
Random.int 1 100           -- Task Error Int
Random.float ()             -- Task Error Float (0.0 to 1.0)
Random.choice ["a","b","c"] -- Task Error (Maybe String)
Random.shuffle [1,2,3,4]    -- Task Error (List Int)
```

### Sky.Http.Server

```elm
import Sky.Http.Server as Server

main =
    Server.listen 8000
        [ Server.get "/" (\_ -> Task.succeed (Server.text "Hello!"))
        , Server.get "/api/users/:id" getUser
        , Server.post "/api/data" handlePost
        , Server.static "/assets" "./public"
        ]

-- Request = { method, path, body, headers, params, query, cookies, ... }
-- Response builders: text, json, html, withStatus, withHeader, withCookie, redirect
-- Cookie: Server.cookie "name" "value", Server.secureCookie, Server.sessionCookie
```

## Sky.Live — Server-Driven UI

For interactive web apps, Sky.Live generates an HTTP server with DOM diffing (like Phoenix LiveView):

```elm
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Css exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick, onInput, onSubmit)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Time as Time

type Page = HomePage | AboutPage
type alias Model = { page : Page, count : Int }
type Msg = Navigate Page | Increment | Tick

init _ = ({ page = HomePage, count = 0 }, Cmd.none)

update msg model =
    case msg of
        Navigate p -> ({ model | page = p }, Cmd.none)
        Increment -> ({ model | count = model.count + 1 }, Cmd.none)
        Tick -> ({ model | count = model.count + 1 }, Cmd.none)

subscriptions model =
    case model.page of
        HomePage -> Time.every 1000 Tick    -- server-push via SSE
        _ -> Sub.none

view model =
    div []
        [ styleNode [] (stylesheet [ rule "body" [ fontFamily "sans-serif" ] ])
        , h1 [] [ text (String.fromInt model.count) ]
        , button [ onClick Increment ] [ text "+" ]
        ]

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/about" AboutPage ]
        , notFound = HomePage
        }
```

**Navigation**: `a [ href "/about", attribute "sky-nav" "" ] [ text "About" ]`
**Styling**: Use `Std.Css` with `stylesheet`/`rule` — not inline style strings.

### Event binding — radio groups

Sky.Live's `input`/`change` event on a `<input type="radio">` reports the radio's `checked` state (always `True` at selection), NOT its `value`. Binding a typed constructor like `UpdateRole : String -> Msg` to `onInput` gets a `Bool` at runtime, which the server drops as a Msg decode error.

**Use `onClick` with a fully-applied Msg per radio** (same pattern for per-choice checkboxes):

```elm
-- One <label> per choice, each carrying a zero-arg Msg.
choiceRow =
    label [ for "role-guardian", onClick (UpdateRole "guardian") ]
        [ input [ type "radio", name "role", value "guardian", id "role-guardian" ] []
        , text "Guardian"
        ]
```

The browser toggles the radio natively via the `for`/`id` pairing; the Msg ADT arrives on the server already applied (no wire-level type coercion). This is the recommended TEA pattern for radio groups.

### Dispatch error handling

Sky.Live's dispatcher is wrapped in `defer/recover`. If your `update`/`view`/`guard` panics — or a wire-level Msg decode mismatches a constructor type — the event is dropped cleanly, the session model is NOT mutated, and a diagnostic is written to stderr (`[sky.live] dispatch panic recovered …` or `[sky.live] Msg decode error …`). The client sees an empty patch list and the DOM stays as-is. Check server logs when an event appears to "do nothing".

### Sky.Live Component Protocol

Components are separate modules with their own `Model`/`Msg`/`update`/`view`. The compiler auto-wires message routing.

```elm
-- Counter.sky
module Counter exposing (..)

type alias Counter = { count : Int, label : String }

type Msg = Increment | Decrement | Reset

initWith : String -> Counter
initWith label = { count = 0, label = label }

update : Msg -> Counter -> (Counter, Cmd Msg)
update msg counter =
    case msg of
        Increment -> ({ counter | count = counter.count + 1 }, Cmd.none)
        _ -> (counter, Cmd.none)

-- View takes a Msg wrapper function from parent
view : (Msg -> parentMsg) -> Counter -> VNode
view toMsg counter =
    div []
        [ text (String.fromInt counter.count)
        , button [ onClick (toMsg Increment) ] [ text "+" ]
        ]
```

```elm
-- Main.sky (parent)
type alias Model = { myCounter : Counter.Counter }
type Msg = CounterMsg Counter.Msg | ...

-- In view:
Counter.view CounterMsg model.myCounter
```

### Subscriptions & Time (SSE Server-Push)

`Sub msg` drives server-sent events. The Go runtime walks the subscription tree to set up SSE.

```elm
-- Timer: fires Tick every 1000ms via SSE
subscriptions model = Time.every 1000 Tick

-- Conditional subscription
subscriptions model =
    if model.autoRefresh then
        Time.every 5000 RefreshData
    else
        Sub.none

-- Multiple subscriptions
subscriptions model =
    Sub.batch
        [ Time.every 1000 Tick
        , Time.every 5000 RefreshData
        ]
```

The runtime uses per-session locking and optimistic concurrency (version field) to prevent race conditions between SSE ticks and user events, even across multiple server instances sharing a database.

### Cmd (Side Effects)

`Cmd.none` is used in most cases. `Cmd.batch` combines multiple commands.

```elm
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        MultipleSideEffects ->
            ( model, Cmd.batch [ cmd1, cmd2 ] )
```

## Application Patterns — When to Use What

### 1. Simple CLI App

No `[live]` in sky.toml. Just `main` calling functions directly.

```elm
module Main exposing (main)
import Std.Log exposing (println)
import Sky.Core.Platform as Platform

main =
    let
        args = Platform.getArgs ()
    in
    case args of
        _ :: "add" :: title :: _ -> addItem title
        _ :: "list" :: _ -> listItems
        _ -> println "Usage: app [add|list]"
```

### 2. HTTP Server (non-Live, Go-style)

Uses gorilla/mux or net/http directly. Server renders HTML with `Std.Html.render`. Use `System.loadEnv` to load `.env` files for configuration.

```elm
import Net.Http as Http
import Github.Com.Gorilla.Mux as Mux
import Sky.Core.Process as Process exposing (getEnv, loadEnv)
import Sky.Core.Maybe as Maybe

main =
    let
        _ = loadEnv ""   -- load .env file
        port = Maybe.withDefault "8000" (getEnv "PORT")
    in
    -- Each FFI call returns Result; chain through Result.andThen
    -- so the first failure short-circuits the rest.
    Mux.newRouter ()
        |> Result.andThen (\r ->
            Mux.routerHandleFunc r "/" indexHandler
                |> Result.andThen (\_ -> Http.listenAndServe (":" ++ port) r))
        |> Result.withDefault ()

indexHandler w req =
    case Http.responseWriterHeader w of
        Ok header ->
            Http.headerSet header "Content-Type" "text/html"
                |> Result.andThen (\_ ->
                    Io.writeString w (render (div [] [ text "Hello" ])))
                |> Result.withDefault ()

        Err _ ->
            ()
```

### 3. Sky.Live App (Server-Driven UI with SSE)

Uses TEA architecture. Server holds state, pushes DOM diffs via SSE. Add `[live]` to sky.toml.

Use when: interactive web UIs, real-time dashboards, forms, admin panels.

### 4. Database App

Prefer `Std.Db` (ships with Sky) over raw `database/sql` FFI — it already
handles connection pooling, parameterised queries, identifier quoting, and
transactions.

```elm
import Std.Db as Db

-- Top-level: open once, memoised for the lifetime of the process.
openDb = Db.connect ()

main =
    case openDb of
        Err e -> println ("Database unavailable: " ++ Error.toString e)
        Ok db ->
            case Db.execRaw db "CREATE TABLE IF NOT EXISTS ..." of
                Ok _  -> serve db
                Err e -> println ("Schema init failed: " ++ Error.toString e)
```

sky.toml:
```toml
[database]
driver = "sqlite"
path   = "app.db"

# OR
# url  = "postgres://user:pass@host/db?sslmode=disable"
```

---

## Real-World App Skeletons

Every skeleton in this section is a complete, working starting point. Copy
it into `src/Main.sky`, adjust the types, and `sky run`. Each one is tested
against the v0.9 Sky compiler.

### Recipe 1: Todo CRUD with Sky.Live

The canonical "interactive web app with database" flow. Covers: open DB
once at top level, Model holds only serialisable state, update handlers
reload from DB after mutations, VNode diff pushes changes via SSE.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Dict as Dict
import Sky.Core.Maybe as Maybe
import Sky.Core.Error as Error exposing (Error)
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Css exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick, onInput, onSubmit)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Db as Db
import Std.Log exposing (println)


type Page = HomePage

type alias Todo =
    { id : String
    , title : String
    , done : Bool
    }

type alias Model =
    { page : Page
    , todos : List Todo
    , draft : String
    , notice : String
    }

type Msg
    = AddTodo
    | ToggleDone String
    | RemoveTodo String
    | DraftChanged String


-- Top-level: memoised, shared by every request handler.
openDb = Db.connect ()


initSchema db =
    Db.execRaw db
        """CREATE TABLE IF NOT EXISTS todos (
            id TEXT PRIMARY KEY,
            title TEXT NOT NULL,
            done INTEGER NOT NULL DEFAULT 0
        )"""


getTodos db =
    case Db.query db "SELECT id, title, done FROM todos ORDER BY id" [] of
        Err _  -> []
        Ok rows -> List.map rowToTodo rows


rowToTodo row =
    { id = Db.getField "id" row
    , title = Db.getField "title" row
    , done = Db.getField "done" row == "1"
    }


init _ =
    case openDb of
        Err e ->
            ( errorModel ("DB unavailable: " ++ Error.toString e), Cmd.none )

        Ok db ->
            case initSchema db of
                Err e ->
                    ( errorModel ("Schema: " ++ Error.toString e), Cmd.none )

                Ok _ ->
                    ( { page = HomePage
                      , todos = getTodos db
                      , draft = ""
                      , notice = ""
                      }
                    , Cmd.none
                    )


errorModel msg =
    { page = HomePage, todos = [], draft = "", notice = msg }


update msg model =
    case openDb of
        Err _ -> ( model, Cmd.none )
        Ok db -> updateWithDb db msg model


updateWithDb db msg model =
    case msg of
        DraftChanged v ->
            ( { model | draft = v }, Cmd.none )

        AddTodo ->
            if String.trim model.draft == "" then
                ( model, Cmd.none )

            else
                let
                    id = Result.withDefault "" (Uuid.newString)
                    _ =
                        Db.exec db
                            "INSERT INTO todos (id, title, done) VALUES (?, ?, 0)"
                            [ id, model.draft ]
                in
                    ( { model
                        | todos = getTodos db
                        , draft = ""
                        , notice = "Added."
                      }
                    , Cmd.none
                    )

        ToggleDone id ->
            let
                _ = Db.exec db
                    "UPDATE todos SET done = 1 - done WHERE id = ?"
                    [id]
            in
                ( { model | todos = getTodos db }, Cmd.none )

        RemoveTodo id ->
            let
                _ = Db.exec db "DELETE FROM todos WHERE id = ?" [id]
            in
                ( { model | todos = getTodos db }, Cmd.none )


view model =
    div [ class "app" ]
        [ h1 [] [ text "Todos" ]
        , form [ onSubmit AddTodo, class "row" ]
            [ input
                [ type_ "text"
                , placeholder "What needs doing?"
                , value model.draft
                , onInput DraftChanged
                ]
                []
            , button [ type_ "submit" ] [ text "Add" ]
            ]
        , div [ class "list" ] (List.map viewTodo model.todos)
        , if model.notice == "" then text "" else p [ class "notice" ] [ text model.notice ]
        ]


viewTodo todo =
    div [ class (if todo.done then "todo done" else "todo") ]
        [ input
            [ type_ "checkbox"
            , checked todo.done
            , onClick (ToggleDone todo.id)
            ]
            []
        , span [] [ text todo.title ]
        , button [ onClick (RemoveTodo todo.id) ] [ text "×" ]
        ]


main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" HomePage ]
        , notFound = HomePage
        }
```

Key idioms demonstrated:
- DB connection at top level (`openDb = Db.connect ()`), not in Model.
- `update` wraps with `case openDb of` so handler code is always holding a
  live DB — no `Maybe Db` in Model.
- After any mutation, reload the relevant list (`todos = getTodos db`)
  rather than trying to patch the Model manually.
- Boolean attribute (`checked todo.done`) now correctly renders only when
  `True` (v0.9-dev fix).

sky.toml:
```toml
name = "todo-app"
entry = "src/Main.sky"

[live]
port = 3000

[database]
driver = "sqlite"
path = "todos.db"

["go.dependencies"]
"github.com/google/uuid" = "latest"
```

### Recipe 2: Auth'd Web App (Std.Auth + Sky.Live)

Sign up → email verify → sign in → protected pages. Std.Auth handles
bcrypt, sessions, and email verification tokens.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error, ErrorKind(..))
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick, onInput, onSubmit)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Auth as Auth
import Std.Log exposing (println)


type Page = HomePage | SignInPage | SignUpPage | DashboardPage


type alias Session = { token : String, email : String, role : String }

type alias Model =
    { page : Page
    , session : Maybe Session
    , signInEmail : String
    , signInPassword : String
    , signUpEmail : String
    , signUpPassword : String
    , notice : String
    }


type Msg
    = Navigate Page
    | SignInEmail String
    | SignInPassword String
    | SignUpEmail String
    | SignUpPassword String
    | SubmitSignIn
    | SubmitSignUp
    | SignOut


init _ =
    ( { page = HomePage
      , session = Nothing
      , signInEmail = ""
      , signInPassword = ""
      , signUpEmail = ""
      , signUpPassword = ""
      , notice = ""
      }
    , Cmd.none
    )


update msg model =
    case msg of
        Navigate p ->
            ( { model | page = p, notice = "" }, Cmd.none )

        SignInEmail v -> ( { model | signInEmail = v }, Cmd.none )
        SignInPassword v -> ( { model | signInPassword = v }, Cmd.none )
        SignUpEmail v -> ( { model | signUpEmail = v }, Cmd.none )
        SignUpPassword v -> ( { model | signUpPassword = v }, Cmd.none )

        SubmitSignIn ->
            case Auth.login model.signInEmail model.signInPassword of
                Ok record ->
                    let
                        token = Maybe.withDefault "" (Dict.get "token" record)
                        user = Maybe.withDefault Dict.empty (Dict.get "user" record)
                        email = Maybe.withDefault "" (Dict.get "email" user)
                        role = Maybe.withDefault "user" (Dict.get "role" user)
                    in
                        ( { model
                            | session = Just { token = token, email = email, role = role }
                            , page = DashboardPage
                            , signInPassword = ""
                          }
                        , Cmd.none
                        )

                Err (Error kind info) ->
                    let
                        userMsg =
                            case kind of
                                PermissionDenied -> "Wrong email or password."
                                InvalidInput -> info.message
                                _ -> "Sign-in unavailable right now."

                        _ = println ("[AUTH] sign-in: " ++ info.message)
                    in
                        ( { model | notice = userMsg, signInPassword = "" }
                        , Cmd.none
                        )

        SubmitSignUp ->
            case Auth.register model.signUpEmail model.signUpPassword of
                Ok _ ->
                    ( { model
                        | notice = "Check your email for the verification link."
                        , page = SignInPage
                        , signUpEmail = ""
                        , signUpPassword = ""
                      }
                    , Cmd.none
                    )

                Err e ->
                    ( { model | notice = Error.toString e }, Cmd.none )

        SignOut ->
            ( { model | session = Nothing, page = HomePage }, Cmd.none )


view model =
    case model.session of
        Just sess ->
            viewAuthenticated sess model

        Nothing ->
            case model.page of
                SignInPage -> viewSignIn model
                SignUpPage -> viewSignUp model
                _ -> viewLanding model


viewLanding model =
    div []
        [ h1 [] [ text "Welcome" ]
        , button [ onClick (Navigate SignInPage) ] [ text "Sign in" ]
        , button [ onClick (Navigate SignUpPage) ] [ text "Sign up" ]
        , if model.notice == "" then text "" else p [] [ text model.notice ]
        ]


viewSignIn model =
    form [ onSubmit SubmitSignIn ]
        [ h2 [] [ text "Sign in" ]
        , input [ type_ "email", placeholder "Email", onInput SignInEmail, value model.signInEmail ] []
        , input [ type_ "password", placeholder "Password", onInput SignInPassword, value model.signInPassword ] []
        , button [ type_ "submit" ] [ text "Sign in" ]
        , if model.notice == "" then text "" else p [ class "err" ] [ text model.notice ]
        ]


viewSignUp model =
    form [ onSubmit SubmitSignUp ]
        [ h2 [] [ text "Sign up" ]
        , input [ type_ "email", placeholder "Email", onInput SignUpEmail, value model.signUpEmail ] []
        , input [ type_ "password", placeholder "Password", onInput SignUpPassword, value model.signUpPassword ] []
        , button [ type_ "submit" ] [ text "Create account" ]
        ]


viewAuthenticated sess model =
    div []
        [ h1 [] [ text ("Hello, " ++ sess.email) ]
        , button [ onClick SignOut ] [ text "Sign out" ]
        -- ...protected content here...
        ]


main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes =
            [ route "/" HomePage
            , route "/signin" SignInPage
            , route "/signup" SignUpPage
            , route "/dashboard" DashboardPage
            ]
        , notFound = HomePage
        }
```

sky.toml:
```toml
[auth]
method = "password"
secret = "change-me-to-a-32-byte-hex-string"
session_ttl = "24h"
email_verification = true
```

Key idioms:
- Session stored in Model as `Maybe Session` — NOT the bcrypt hash or raw
  credentials (those live only in Std.Auth's table).
- Pattern-match on `ErrorKind` (`PermissionDenied`, `InvalidInput`) to
  render specific UI copy while logging the real error message for ops.
- `signInPassword` / `signUpPassword` are cleared from the Model once the
  form is submitted — don't persist credentials across SSE patches.

### Recipe 3: LLM Chat App

Calls an external LLM API via `Http.request`, streams results into the UI
via `Cmd.perform`. Mirrors the sky-chat structure.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.Task as Task
import Sky.Core.Http as Http
import Sky.Core.Json.Encode as Encode
import Sky.Core.Json.Decode as Decode
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.System as System
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick, onInput, onSubmit)
import Std.Cmd as Cmd
import Std.Sub as Sub


type Page = ChatPage

type alias ChatMessage =
    { role : String    -- "user" | "assistant" | "system"
    , content : String
    }

type alias Model =
    { page : Page
    , messages : List ChatMessage
    , draft : String
    , loading : Bool
    , error : String
    }

type Msg
    = DraftChanged String
    | SendMessage
    | AiResponseReceived (Result Error String)


init _ =
    ( { page = ChatPage
      , messages = []
      , draft = ""
      , loading = False
      , error = ""
      }
    , Cmd.none
    )


-- Zero-arity with `_` param — forces System.getenv to evaluate per call,
-- AFTER .env loads (Cardinal Rule 9).
apiKey _ = System.getenvOr "OPENAI_API_KEY" ""


callOpenAi : List ChatMessage -> Task Error String
callOpenAi history =
    let
        body =
            Encode.object
                [ ( "model", Encode.string "gpt-4o-mini" )
                , ( "messages", Encode.list encodeMessage history )
                , ( "max_tokens", Encode.int 1024 )
                ]
    in
        Http.request
            { method = "POST"
            , url = "https://api.openai.com/v1/chat/completions"
            , headers =
                [ ( "Authorization", "Bearer " ++ apiKey () )
                , ( "Content-Type", "application/json" )
                ]
            , body = Encode.encode 0 body
            }
            |> Task.andThen parseResponse


encodeMessage m =
    Encode.object
        [ ( "role", Encode.string m.role )
        , ( "content", Encode.string m.content )
        ]


parseResponse resp =
    if resp.status >= 400 then
        Task.fail (Error.network ("HTTP " ++ String.fromInt resp.status))

    else
        let
            decoder =
                Decode.field "choices"
                    (Decode.index 0
                        (Decode.field "message"
                            (Decode.field "content" Decode.string)))
        in
            case Decode.decodeString decoder resp.body of
                Ok content -> Task.succeed content
                Err e -> Task.fail (Error.decode (Error.toString e))


update msg model =
    case msg of
        DraftChanged v ->
            ( { model | draft = v }, Cmd.none )

        SendMessage ->
            if String.trim model.draft == "" || model.loading then
                ( model, Cmd.none )

            else
                let
                    userMsg = { role = "user", content = model.draft }
                    newHistory = model.messages ++ [ userMsg ]
                in
                    ( { model
                        | messages = newHistory
                        , draft = ""
                        , loading = True
                        , error = ""
                      }
                    , Cmd.perform (callOpenAi newHistory) AiResponseReceived
                    )

        AiResponseReceived (Ok content) ->
            let
                assistantMsg = { role = "assistant", content = content }
            in
                ( { model
                    | messages = model.messages ++ [ assistantMsg ]
                    , loading = False
                  }
                , Cmd.none
                )

        AiResponseReceived (Err e) ->
            ( { model | loading = False, error = Error.toString e }, Cmd.none )


view model =
    div [ class "chat" ]
        [ div [ class "messages" ] (List.map viewMessage model.messages)
        , viewComposer model
        , if model.error == "" then text "" else p [ class "err" ] [ text model.error ]
        ]


viewMessage m =
    div [ class ("msg " ++ m.role) ]
        [ text m.content ]


viewComposer model =
    form [ onSubmit SendMessage, class "composer" ]
        [ input
            [ type_ "text"
            , placeholder "Say something..."
            , value model.draft
            , onInput DraftChanged
            , disabled model.loading
            ]
            []
        , button
            [ type_ "submit"
            , disabled (model.loading || String.trim model.draft == "")
            ]
            [ text (if model.loading then "..." else "Send") ]
        ]


main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" ChatPage ]
        , notFound = ChatPage
        }
```

Key idioms:
- `Cmd.perform task toMsg` dispatches a background task and delivers its
  result as a Msg — UI stays responsive during the API call.
- `Http.request { ... }` takes a single record argument with
  `method`, `url`, `headers`, `body` fields.
- JSON decoder uses `Decode.index 0` to pull the first element from the
  `choices` array.
- `disabled` attributes honour their bool value: the input disables only
  while `loading`, the button disables when loading OR draft is empty.

### Recipe 4: Real-Time Dashboard (SSE subscriptions)

Pulls metrics every N seconds via `Time.every` and renders them live
without the user having to click anything.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.Error as Error exposing (Error)
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Time as Time
import Std.Db as Db


type Page = DashboardPage

type alias Metric = { name : String, value : Int, checkedAt : String }

type alias Model =
    { page : Page
    , metrics : List Metric
    , paused : Bool
    }

type Msg = Tick | TogglePause


openDb = Db.connect ()


loadMetrics db =
    case Db.query db "SELECT name, value, checked_at FROM metrics ORDER BY name" [] of
        Err _ -> []
        Ok rows -> List.map rowToMetric rows


rowToMetric row =
    { name = Db.getField "name" row
    , value = Db.getInt "value" row
    , checkedAt = Db.getField "checked_at" row
    }


init _ =
    case openDb of
        Err _ -> ( { page = DashboardPage, metrics = [], paused = False }, Cmd.none )
        Ok db -> ( { page = DashboardPage, metrics = loadMetrics db, paused = False }, Cmd.none )


update msg model =
    case msg of
        TogglePause ->
            ( { model | paused = not model.paused }, Cmd.none )

        Tick ->
            case openDb of
                Err _ -> ( model, Cmd.none )
                Ok db -> ( { model | metrics = loadMetrics db }, Cmd.none )


subscriptions model =
    if model.paused then
        Sub.none

    else
        Time.every 5000 Tick   -- fire Tick every 5s via SSE


view model =
    div [ class "dash" ]
        [ h1 [] [ text "Dashboard" ]
        , button [ onClick TogglePause ] [ text (if model.paused then "Resume" else "Pause") ]
        , ul [] (List.map viewMetric model.metrics)
        ]


viewMetric m =
    li []
        [ strong [] [ text m.name ]
        , text (": " ++ String.fromInt m.value ++ " (" ++ m.checkedAt ++ ")")
        ]


main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ route "/" DashboardPage ]
        , notFound = DashboardPage
        }
```

Key idioms:
- Subscriptions are CONDITIONAL — turn `Time.every` off when the user
  pauses. Returning `Sub.none` cancels the timer.
- `Tick` handler reloads the whole list, not incremental diffs. The
  VNode diff + SSE patch path sends only the changed rows to the client.

### Recipe 5: JSON API Server (Sky.Http.Server, no TEA)

For REST endpoints you don't need TEA — use `Sky.Http.Server` directly.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Json.Encode as Encode
import Sky.Core.Json.Decode as Decode
import Sky.Core.Error as Error exposing (Error)
import Sky.Http.Server as Server


type alias User = { id : Int, name : String, email : String }


getUser : Server.Request -> Task Error Server.Response
getUser req =
    let
        id = Server.param "id" req |> Maybe.withDefault "0" |> String.toInt |> Result.withDefault 0
        user = { id = id, name = "Demo", email = "demo@example.com" }

        json =
            Encode.object
                [ ( "id", Encode.int user.id )
                , ( "name", Encode.string user.name )
                , ( "email", Encode.string user.email )
                ]
    in
        Task.succeed (Server.json (Encode.encode 2 json))


health _ = Task.succeed (Server.text "ok")


main =
    Server.listen 8000
        [ Server.get "/health" health
        , Server.get "/api/users/:id" getUser
        ]
```

Use when: you need a classic JSON API with routing and no server-driven UI.

---

## Go FFI — Detailed Semantics

### Adding Go Dependencies

```bash
sky add github.com/google/uuid    # external Go package
sky add database/sql               # Go stdlib
sky install                        # install all from sky.toml
```

This auto-generates `.skycache/go/<package>/bindings.skyi` with type-safe Sky bindings and `sky_wrappers/<package>.go` with Go wrapper functions (including panic recovery). **Never write FFI code manually** — the compiler generates everything. The inspector extracts ALL struct fields, methods, functions, and constants. Dead code elimination strips unused wrappers from the final build.

`sky install` auto-scans your source files for FFI imports and generates any missing bindings.

### Import Path Mapping

Go package paths map to PascalCase Sky module names. **The Sky kernel
already covers the common cases — only reach for the Go FFI binding
when you need a Go-only API the kernel doesn't surface.** For example,
`Crypto.sha256 s` (kernel) returns the hex digest directly; you only
need `import Crypto.Sha256 as Sha256` if you want raw bytes for
further processing.

| Go Package | Sky Import | Notes |
|-----------|-----------|-------|
| `net/http` | `import Net.Http as Http` | for HTTP client beyond `Sky.Core.Http`'s `get`/`post`/`request` |
| `database/sql` | `import Database.Sql as Sql` | for raw `sql.DB` access; `Std.Db` covers most usage |
| `crypto/sha256` | `import Crypto.Sha256 as Sha256` | kernel `Crypto.sha256 s` returns hex string directly — use this for plain hashing |
| `encoding/hex` | `import Encoding.Hex as Hex` | kernel `Encoding.hexEncode s` / `Encoding.hexDecode s` cover string ↔ hex |
| `os` | `import Os` | Go's process-state package (stdin/stderr/file ops). Sky kernel is `System.*` (no collision since v0.10.0) |
| `os/exec` | `import Os.Exec as Exec` | for richer subprocess control than `Process.run` |
| `log/slog` | `import Log.Slog as Slog` | Go's structured logger. Sky kernel `Log.*With` is the preferred surface |
| `bufio` | `import Bufio` | line/byte scanning, e.g. for stdin pipelines |
| `io` | `import Io` | low-level Reader/Writer; kernel `Sky.Core.Io` covers stdin/stdout/stderr |
| `github.com/google/uuid` | `import Github.Com.Google.Uuid as Uuid` |
| `github.com/gorilla/mux` | `import Github.Com.Gorilla.Mux as Mux` |
| `modernc.org/sqlite` | `import Modernc.Org.Sqlite as _` |
| `fyne.io/fyne/v2` | `import Fyne.Io.Fyne.V2 as Fyne` |
| `github.com/stripe/stripe-go/v84` | `import Github.Com.Stripe.StripeGo.V84 as Stripe` |
| `github.com/stripe/stripe-go/v84/checkout/session` | `import Github.Com.Stripe.StripeGo.V84.Checkout.Session as Session` |

### Calling Conventions

Every FFI call returns `Result Error T`. Pattern-match or chain via
`Result.andThen` / `Result.withDefault` at every call site.

```elm
-- Zero-arg Go functions/variables: no () in Sky (returns Result)
case Uuid.newString of
    Ok id -> useId id
    Err e -> handleError e

-- Or with Result.withDefault for "bail to a default"
id = Result.withDefault "anonymous" Uuid.newString

-- Go methods: first arg is receiver. Returns Result.
case Mux.routerHandleFunc router "/path" handler of
    Ok _ -> ...
    Err e -> ...

-- Go struct fields: accessor function returns Result
case Http.requestUrl req of
    Ok url -> ...
    Err e -> ...

-- Go constants: accessed as values (still Result-wrapped)
status = Result.withDefault 200 Http.statusOK

-- Go package variables: getter + setter (both Result)
key = Result.withDefault "" (Stripe.key ())
case Stripe.setKey "sk_test_..." of
    Ok _ -> ...
    Err e -> ...

-- Go struct construction: chain setters via Result.andThen
-- Each setter returns Result Error TypeName.
result =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetCustomer id)

-- Nested structs: build inner first, then set on outer
priceDataResult =
    Stripe.newCheckoutSessionLineItemPriceDataParams ()
        |> Result.andThen (Stripe.checkoutSessionLineItemPriceDataParamsSetCurrency "gbp")
        |> Result.andThen (Stripe.checkoutSessionLineItemPriceDataParamsSetUnitAmount 1000)

lineItemResult =
    priceDataResult
        |> Result.andThen (\priceData ->
            Stripe.newCheckoutSessionLineItemParams ()
                |> Result.andThen (Stripe.checkoutSessionLineItemParamsSetPriceData priceData))
```

**Why all the Results?** The FFI boundary can fail in ways Sky's
type system can't see (Go panics, nil pointers, missing config,
goroutine leaks). Every wrapper catches these via panic recovery
and returns `Err(ErrFfi(...))` instead of crashing. See
`docs/ffi/boundary-philosophy.md` for the full reasoning. **Prefer
Sky's stdlib** (`Std.Crypto`, `Std.Time`, `Std.Http`, etc.) where
available — those don't pay the Result tax for genuinely-pure ops.

**Important**: Never pass Sky records `{ field = value }` as Go struct parameters. Always use the `newTypeName ()` constructor + `typeNameSetField value` setters. Sky records are `map[string]any` at runtime; Go functions expect typed struct pointers.

### Side-Effect Imports (Database Drivers)

Some Go packages are drivers that register themselves via `init()`. Import with `_`:

```elm
import Modernc.Org.Sqlite as _    -- registers "sqlite" driver for database/sql
```

### Handler Functions (HTTP)

Go HTTP handlers take `(http.ResponseWriter, *http.Request)`. Each
FFI call is a Result; chain via `Result.andThen` and let the first
`Err` short-circuit:

```elm
handler : ResponseWriter -> Request -> Unit
handler w req =
    Http.responseWriterHeader w
        |> Result.andThen (\header -> Http.headerSet header "Content-Type" "text/html")
        |> Result.andThen (\_ -> Io.writeString w "Hello")
        |> Result.withDefault ()

-- Cookie management
token =
    case Http.requestCookie req "session" of
        Ok cookie ->
            Result.withDefault "" (Http.cookieValue cookie)
        Err _ ->
            ""

-- Form values
email = Result.withDefault "" (Http.requestFormValue req "email")

-- Redirect
Http.redirect w req "/login" 302
```

## Project Structure

```
my-project/
  sky.toml              -- project manifest
  src/
    Main.sky            -- entry point (module Main exposing (main))
    Lib/
      Utils.sky         -- module Lib.Utils exposing (..)
```

### sky.toml

```toml
name = "my-project"
version = "0.1.0"
entry = "src/Main.sky"
bin = "dist/app"

[source]
root = "src"

[go.dependencies]
"github.com/google/uuid" = "latest"
"modernc.org/sqlite" = "latest"

[database]                      # only for Std.Db apps
driver = "sqlite"               # "sqlite" | "postgres"
path = "myapp.db"               # for sqlite
# url = "postgres://user:pass@host/db"  # for postgres

[auth]                          # only for Std.Auth apps
method = "password"             # "password" (more planned)
secret = "your-secret-key"     # required: session signing key
previous_secrets = "old-key"   # optional: previous keys for rotation
bcrypt_cost = 12                # optional (default 12)
session_ttl = "24h"             # optional: "24h", "30m", or seconds
email_verification = false      # optional (default false)

[live]                          # only for Sky.Live apps
port = 8000
input = "debounce"              # "debounce" | "blur"
store = "memory"                # memory | sqlite | redis | postgres
# storePath = "./sessions.db"   # sqlite file
# storePath = "localhost:6379"  # redis host:port (or "redis://…")
# storePath = "postgres://user:pass@host/db"
```

Sky.Live config is embedded at compile time but can be overridden at runtime. Env vars and the corresponding `sky.toml` keys (under the single `[live]` table — there is NO `[live.session]` section):

| Env var | sky.toml `[live]` key | Notes |
|---|---|---|
| `SKY_LIVE_PORT` | `port` | Server port (default 8000) |
| `SKY_LIVE_INPUT` | `input` | `debounce` or `blur` |
| `SKY_LIVE_POLL_INTERVAL` | `poll_interval` | ms (0 = SSE only) |
| `SKY_LIVE_STORE` | `store` | Session store: `memory` (default), `sqlite`, `redis` / `valkey`, `postgres` |
| `SKY_LIVE_STORE_PATH` | `storePath` | sqlite file path, or redis `host:port` / `redis://…`, or postgres URL |
| `DATABASE_URL` | -- | Postgres URL fallback if `SKY_LIVE_STORE_PATH` is unset |
| `REDIS_URL` | -- | Redis URL fallback if `SKY_LIVE_STORE_PATH` is unset (defaults to `localhost:6379`) |
| `SKY_LIVE_STATIC_DIR` | `static` | Path to static assets |
| `SKY_LIVE_TTL` | `ttl` | Session TTL (Go duration format, e.g. `30m`) |

Connection-status banner (v0.9.9+): `SKY_LIVE_BANNER` (default `on`; `off` / `0` / `false` to suppress the chrome but keep the retry queue active), `SKY_LIVE_RETRY_BASE_MS` (default `500`), `SKY_LIVE_RETRY_MAX_MS` (default `16000`), `SKY_LIVE_RETRY_MAX_ATTEMPTS` (default `10`), `SKY_LIVE_QUEUE_MAX` (default `50`).

**Connection status banner**: the runtime injects a bottom-pinned banner that shows `Reconnecting…` (amber) when the SSE connection drops or a POST `/_sky/event` fails, and `Connection lost — refresh to retry` (red) after the retry attempts are exhausted. POST failures during the outage land in a FIFO queue; the SSE re-open or a successful retry drains them, so clicks during a brief outage replay automatically. Use a persistent session store (Redis / Postgres / SQLite / Firestore) for production deployments — the memory store loses Model state on every server restart, so reconnect re-initialises from `init`. The banner is opt-out via `SKY_LIVE_BANNER=off`; styling can be overridden by `#__sky-status { ... !important }` in the user's stylesheet.

**Priority (highest wins):** system env vars > `.env` file > `sky.toml` defaults. System env vars always win so production deployments can override without editing files. `.env` is for local dev convenience.

### Importing Sky Dependencies

Three import syntaxes are supported for `.skydeps/` packages (all resolve to the same file):

```elm
-- Stripped (cleanest, recommended)
import Tailwind as Tw

-- Prefixed (PascalCase package name + module)
import SkyTailwind.Tailwind as Tw

-- Full path (mirrors the dependency URL)
import Github.Com.Anzellai.SkyTailwind.Tailwind as Tw
```

Resolution precedence: local `src/` > `.skydeps/` > stdlib. Local modules shadow dependencies; use full/prefixed path to disambiguate. Only modules listed in the package's `[lib].exposing` are importable.

## Std.Db — Database Abstraction

```elm
import Std.Db as Db
import Modernc.Org.Sqlite as _   -- driver import needed for SQLite

-- Open connection
db = Db.connect ()  -- reads [database] from sky.toml
    Ok conn -> ...
    Err e -> ...

-- Parameterised queries (injection-safe)
Db.exec conn "INSERT INTO t (name) VALUES (?)" ["val"]
Db.query conn "SELECT * FROM t WHERE x = ?" ["val"]
Db.execRaw conn "CREATE TABLE IF NOT EXISTS t (...)"

-- Typed queries via Json.Decode
Db.queryDecode conn "SELECT * FROM t" [] myDecoder
Db.queryOneDecode conn "SELECT * FROM t WHERE id = ?" [id] myDecoder

-- Convenience
Db.insertRow conn "table" (Dict.fromList [("col", "val")])
Db.getById conn "table" "123"
Db.updateById conn "table" "123" (Dict.fromList [("col", "new")])
Db.deleteById conn "table" "123"
Db.findWhere conn "table" "column" "value"

-- Row helpers (for untyped Dict queries)
Db.getField "name" row   -- String
Db.getInt "count" row     -- Int
Db.getBool "done" row     -- Bool

-- Transactions
Db.withTransaction conn (\tx ->
    let _ = Db.txExec tx "..." []
    in Ok ()
)
```

## Std.Auth — Authentication

```elm
import Std.Auth as Auth

-- Register (auto-creates sky_users + sky_sessions tables)
Auth.register "alice@example.com" "password123"
-- Ok { id, email, role, verified }

-- Login (returns session token + user)
Auth.login "alice@example.com" "password123"
-- Ok { token, user: { id, email, role, name, avatarUrl, verified } }

-- Verify session token
Auth.verify sessionToken
-- Ok { id, email, role, ... }

-- Logout
Auth.logout sessionToken

-- Email verification (when email_verification = true in sky.toml)
Auth.verifyEmail verificationToken

-- Low-level: bcrypt hash/verify
Auth.hashPassword "password"        -- Ok "bcrypt-hash"
Auth.verifyPassword "pw" "hash"     -- True/False
Auth.setRole userId "admin"
Auth.signToken "payload"            -- Ok "hmac-signature"
```

Configure in sky.toml:
```toml
[auth]
method = "password"
secret = "your-secret-key"          # required
previous_secrets = "old-key-1"      # optional: for key rotation
bcrypt_cost = 12                    # optional (default 12)
session_ttl = "24h"                 # optional (default 24h)
email_verification = false          # optional (default false)
```

Env var overrides: `SKY_AUTH_SECRET`, `SKY_AUTH_PREVIOUS_SECRETS`, `SKY_AUTH_METHOD`, `SKY_AUTH_BCRYPT_COST`, `SKY_AUTH_SESSION_TTL`, `SKY_AUTH_EMAIL_VERIFICATION`.

Key rotation: move current `secret` to `previous_secrets`, set new `secret`, restart. `signToken` uses current key; `verifyToken` checks current + previous keys.

When `email_verification = true`, `Auth.register` returns a `verificationToken`. Your app delivers it:
```elm
case Auth.register email password of
    Ok user ->
        case Dict.get "verificationToken" user of
            Just token -> sendVerificationEmail email token  -- your email provider
            Nothing -> ...
```

For apps with custom user fields (username, avatar), use `Auth.hashPassword`/`Auth.verifyPassword` for the crypto while keeping your own users table.

---

## Troubleshooting Cookbook

Error-message → root cause → fix. This is the single-file reference for
"my code won't compile / my app crashed at runtime". Check here before
spelunking through the runtime or compiler source.

### Compile errors (`sky build` / `sky check`)

**`TYPE ERROR: Type mismatch: ( { ... }, Cmd Msg ) vs ( Model, Cmd Msg ) (from: a vs ( Model, Cmd Msg ))`**

Your `init` or `update` returns an anonymous record literal, and HM can't
unify it with the `Model` alias. Usually means:

- A field name is misspelled (`tokensUsed` vs `tokenUsed`).
- A field has the wrong type (`Nothing` vs `Just 0` when the alias says `Int`).
- `db : Maybe Db.Db` is in the Model and you're on an old Sky binary — run
  `sky upgrade`.

Make the expectation explicit by annotating:
`init : a -> ( Model, Cmd Msg )`. The error will then pinpoint which field
differs.

**`Type mismatch: Int vs ()` at a function call**

Annotation promises one return type, body returns another. Common cause:
`initSchema : Db -> Result Error ()` over a body that's just `Db.execRaw ...`
— `execRaw` returns `Result Error Int`. Fix either the annotation or map
the body: `|> Result.map (\_ -> ())`.

**`Import error: module M does not expose X`**

Either the symbol isn't exported from `M`, or `M` hasn't generated an
interface file yet. Run `sky install` to regenerate FFI bindings. For Sky
modules, check the module's `module X exposing (..)` line.

**`DeclarationError <line> <col>` during parsing**

Parser got confused by a lambda / record layout at a tight column. Most
often: a multi-line lambda inside `List.map` that starts with an open
paren. Hoist the lambda to a top-level function, or fit the whole lambda
on ONE line. Annotations do not affect this — it's a pure parser issue.

**`Names resolved` but then no further output and a non-zero exit**

Canonicaliser failed silently — usually a name you used isn't in scope.
Add `-v` style logging isn't available; easiest fix is to bisect your
imports (comment half, re-run, narrow).

### Runtime panics

**`interface conversion: interface {} is X, not Y`**

Somewhere the typed runtime got an X but expected a Y. Common culprits:

- `int` vs `struct{}` — wrong annotation (Cardinal Rule 2).
- `rt.HttpResponse` vs `rt.SkyResponse` — usually a stale build; run
  `sky clean && sky build` (v0.9 fixed the mapping).
- `string` vs `[]map[string]string` — `rt.Concat` fell through to
  string-concat on typed slices (fixed v0.9-dev — `sky upgrade`).

**`rt.Coerce: expected []main.T_R, got string`**

`++` on a list produced a stringified blob because Concat didn't recognise
the typed slice. Fixed v0.9-dev — upgrade.

**`assignment to entry in nil map` inside `renderVNode`**

A form with `onSubmit`/`onClick` is being rendered through
`Html_render` with a nil handler map. Fixed v0.9-dev — upgrade.

**`reflect.Value.Call: call of nil function`**

A curried lambda passed to a Go-typed callback slot got its inner function
zero'd. Fixed v0.9-dev — upgrade.

**`stack overflow` in `walkGob` at server startup**

Your Model stores an opaque FFI handle (DB connection, HTTP client,
Firestore client) with internal pointer cycles. Fix: move the handle to a
top-level value, not in Model (Cardinal Rule 4).

**`Invalid email or password` on every sign-in even though the user exists**

`Db.getField` was reading from a `map[string]any` instead of the typed
`map[string]string` HM narrowed it to. Fixed v0.9-dev — upgrade.

**`No notes yet` / empty list rendered after a successful insert**

The record-update path silently dropped the `[]any → []T` widening.
Fixed v0.9-dev — upgrade.

**Chat input / button renders as permanently disabled**

Boolean HTML attrs (`disabled`, `checked`, etc.) always emitted regardless
of their value. Fixed v0.9-dev — upgrade.

**`session not found` on every event**

Either the client sent the wrong `sessionId`, or the server just restarted
and the memory store lost its sessions. For production, use a persistent
store:

```toml
[live]
store = "sqlite"
storePath = "sessions.db"
```

**`stack overflow` when subscribing to Time.every + handler does expensive work**

A Tick handler that triggers another Tick (via Cmd.perform + a Msg that
triggers the subscription's model) can loop. Add a `paused` flag or check
the model before firing.

### Deployment & production issues

**`OPENAI_API_KEY not set` but `.env` has it**

A zero-arity function reading `System.getenv` evaluated at Go `init()` time,
before `godotenv` ran. Add a dummy `_` parameter (Cardinal Rule 9).

**`sky-out/go.mod` got wiped after `sky run`**

Fixed v0.9-dev — incremental builds now re-seed Go deps. Upgrade.

**CSS not applying in Sky.Live app**

Make sure you call `styleNode [] (stylesheet [...])` at the TOP of your
view, not inside the body. The diff protocol needs the `<style>` block
to be a top-level VNode to stay stable across renders.

### Performance issues

**First request hangs for 5-30s**

Sky.Live does index-building on first request for the session store. If
using SQLite: set `busy_timeout` in your schema init. If using Postgres:
make sure `prepare_threshold` is sensible.

**Memory usage climbing linearly in a long-running app**

Session store memory grows with inactive sessions. Set a TTL:
`SKY_LIVE_TTL=30m`. For high-traffic apps, switch to SQLite or Redis.

---

## Known Limitations (v0.9)

- **No anonymous records in type annotations** — use `type alias` for record types in signatures. Typed codegen needs a name for the struct shape; inline `{ field : Type }` in an annotation is rejected.
- **No higher-kinded types** — no `Functor`, `Monad`, etc. Use concrete types.
- **No `where` clauses** — use `let...in` instead.
- **No custom operators** — only built-in (`|>`, `<|`, `++`, `::`, etc.).
- **Negative literal arguments need parentheses** — `f (-1)` not `f -1` (`f -1` parses as subtraction).
- **`import M as A exposing (Type(..))`** — combining `as` alias with `exposing` for ADT constructors breaks module loading; use `import M exposing (..)` without `as` instead, or qualify constructors.
- **`Dict.toList` returns string keys** — `Dict` is `map[string]any` at runtime, so `Dict.toList` on `Dict Int v` gives string keys. Iterate via `Dict.get` over known ranges.
- **`sky check` doesn't fully model Go interfaces** — concrete types can't unify with Go interfaces (`Fyne.CanvasObject`), but the code compiles and runs fine.
- **Zero-arg FFI functions need no `()`** — call `Uuid.newString` (the return value), not `Uuid.newString ()`.
- **Zero-arg `Css.*` constants DO need `()`** — `Css.zero`, `Css.auto`, `Css.none`, `Css.transparent`, `Css.inherit`, `Css.initial`, `Css.borderBox`, `Css.systemFont`, `Css.monoFont`, `Css.userSelectNone`. These are exposed as `() -> String` kernels (not zero-arity values) so they don't interact with Go's `init()` ordering. Write `Css.padding (Css.zero ())`, not `Css.padding Css.zero` — the latter serialises a function pointer like `0xc00001c0a0` into the stylesheet. Pattern: any `Css.X` that names a literal CSS keyword takes `()`; value constructors like `px`, `rem`, `em`, `hex`, `rgba` take their arguments directly.
- **FFI setters in pipelines need an explicit lambda** — `|> Result.andThen (OpenAi.chatCompletionMessageSetRole m.role)` emits a call to the non-existent non-T variant and fails codegen. Wrap: `|> Result.andThen (\msg -> OpenAi.chatCompletionMessageSetRole m.role msg)`.
- **`import Lib.X as Alias` leaks the alias into codegen for exposed types** — `import Lib.Db as Chat` emits `Chat_Message_R` instead of the canonical `Lib_Db_Message_R`, breaking cross-module record sharing. **Workaround**: import types without the alias — `import Lib.Db exposing (Message, ...)`. Aliases are fine for modules that only expose functions.
- **Zero-arity functions reading env vars** — zero-arity functions are memoised; when they read `System.getenv` they evaluate during Go `init()`, before `.env` is loaded. **Workaround**: add a dummy `_` parameter: `getConfig _ = System.getenv "KEY"`.
- **Let bindings with parameters after multi-line case** — `mark j = expr` directly after a `case ... of` in the same `let` can be reparsed as a new top-level declaration. Use a lambda (`\j -> expr`) or extract to a top-level function.
- **`exposing (Type(..))` doesn't expose user-module constructors** — only stdlib/kernel modules resolve `MyType(..)` fully. For a user-defined `MyModule`, import `exposing (..)` or qualify constructors (`MyModule.MyConstructor`).
- **`let` bindings don't support forward references** — Helpers inside a `let` block must be defined *before* their consumers in source order. `let writeAll db = … insertRow db ts …; insertRow db ts = …` fails `go build` with `undefined: insertRow`. **Workaround**: reorder so dependencies come first. (Future fix — the canonicaliser already knows the full set of let names.)
- **Partial application of let-bound multi-arg functions panics at runtime** — `Task.andThen (insertRow db)` where `insertRow db ts = …` is defined in an enclosing `let` panics with `reflect: Call with too few input arguments` when invoked. **Workaround**: explicit lambda — `Task.andThen (\ts -> insertRow db ts)`. Same class as the FFI-setter limitation but for ordinary user-defined let-bound functions.

### Fixed in v0.9-dev (feat/typed-codegen)
- **Typed-map round-trips at the FFI boundary** — `[]any` containing `map[string]any` now narrows into `[]map[string]string` correctly across `rt.Coerce`, `AsListT`, `AsMapT`, `AsDict`. `List.isEmpty` / `List.map` on annotated DB result slices no longer wrongly report empty.
- **Curried lambdas passed to Go-typed callbacks** — `rt.Coerce[func(X) func(Y) Z]` over a Sky `func(any) any { return func(any) any {...} }` now wraps the inner func too; requireAuth → route-handler style no longer panics.
- **Server-rendered form events** — `Html_render` for a form with `onSubmit="..."` no longer panics on `assignment to entry in nil map`.
- **Signin on annotated auth rows** — `Db.getField` accepts both `map[string]string` (typed) and `map[string]any` (raw) sources.
- **Pattern literal inference** — `case foo of "idle" -> _` now forces `foo : String` at check time.

### Fixed earlier (historical)
- **Nested `case...of`** — fixed v0.7.21; cases at any depth compile.
- **Cross-module type alias unification** — record aliases defined in module A unify correctly in module B's type annotations.
- **Cross-module ADT exhaustiveness** — missing case branches for imported ADTs are caught at compile time.
- **`exposing (Constructor(..))` qualified call issue** — resolved; use `import M exposing (..)` for unqualified constructors on stdlib/kernel modules.
- **Type annotations are load-bearing** — since v0.7.28, the annotation wins when the body would infer a wider type.

## Coding Conventions

- **Module names** are PascalCase, match file paths: `Lib.Utils` → `src/Lib/Utils.sky`
- **No semicolons**, no curly braces — indentation-sensitive like Elm/Haskell
- Use **`Std.Css`** for styling (not inline style strings)
- Use **`errorToString`** to convert Go errors to strings
- Pattern match on **`Result`** (`Ok val` / `Err e`) for Go functions returning errors
- Pattern match on **`Maybe`** (`Just val` / `Nothing`) for Go `*primitive` pointer returns
- **Nested patterns work**: `Ok (Just x)` and `Ok Nothing` are fully supported in case expressions
- **Import conventions**: Use `exposing (..)` sparingly — when two modules export the same name (e.g., `Std.Html` and `Tailwind` both export `hidden`, `h2`, etc.), the first import wins. Prefer qualified imports (`import Foo as F`) to avoid collisions. If using `Tailwind exposing (..)` alongside `Std.Html exposing (..)`, use `hidden_` (with underscore) for the Tailwind version, and `headerNode`/`footerNode` for HTML5 semantic elements
- **`exposing (Type(..))` limitation**: `import MyModule exposing (MyType(..))` does NOT expose ADT constructors for user-defined modules. Use `import MyModule exposing (..)` instead, or qualify constructors: `MyModule.MyConstructor`
- **`//` for integer division**: Use `//` (Elm-style) or regular `/` — both work. `//` always returns `Int`, `modBy divisor n` returns `n % divisor`

## Code Formatting (`sky fmt`)

**Always run `sky fmt <file>.sky` after changes.** The formatter follows **elm-format** style — opinionated, deterministic, no configuration options.

### Rules

- **4-space indentation** throughout (never tabs)
- **"One line or each on its own line"** — arguments, list items, record fields either all fit on one line or each gets its own line indented 4 spaces
- **Leading commas** for multi-line lists, records, and record types
- **Two blank lines** between top-level declarations
- **Trailing newline** at end of file

### Function Calls

```elm
-- Short: stays on one line
div [ class "container" ] [ text "hello" ]

-- Long: each arg on its own line, indented 4
someFunction
    arg1
    arg2
    arg3
```

### Pipelines

```elm
items
    |> List.map (\x -> x * 2)
    |> List.filter (\x -> x > 3)
    |> List.sort
```

### Boolean Chains

```elm
if condition1
    || condition2
    || condition3 then
    body

else
    fallback
```

### If-Then-Else

```elm
if condition then
    trueValue

else if otherCondition then
    otherValue

else
    fallback
```

### Case Expressions

```elm
case msg of

    Increment ->
        count + 1

    Decrement ->
        count - 1
```

### Let-In

```elm
let
    x = compute
    y = transform x
in
    result
```

### Records & Lists

```elm
-- Short: one line
{ name = "Alice" , age = 30 }
[ 1 , 2 , 3 ]

-- Long: leading commas
{ name = "Alice"
, age = 30
, email = "alice@example.com"
}

[ firstItem
, secondItem
, thirdItem
]
```

### Record Updates

```elm
{ model | name = newName , age = newAge }
```

### ADT Variants

```elm
type Shape
    = Circle Float
    | Rectangle Float Float
```

### Declarations

```elm
greet : String -> String
greet name =
    "Hello, " ++ name


add : Int -> Int -> Int
add a b =
    a + b
```

## Common Patterns

```elm
-- HTTP handler (with gorilla/mux)
handler w req =
    let
        body = Io.readAll (Http.requestBody req)
    in
    case body of
        Ok data -> writeResponse w data
        Err e -> writeResponse w (errorToString e)

-- Database query
getUsers db =
    case Sql.dbQueryToMaps db "SELECT * FROM users" [] of
        Ok rows -> rows
        Err _ -> []

-- JSON decoding with pipeline
type alias User = { name : String, age : Int }

-- Sky auto-generates `User : String -> Int -> User` from the type
-- alias above (v0.7.26+), so you can use it as a constructor directly:
userDecoder =
    Decode.succeed User    -- the type alias name IS the constructor
        |> Pipeline.required "name" Decode.string
        |> Pipeline.required "age" Decode.int

result = Decode.decodeString userDecoder jsonString
```


---

## New Compiler Additions (Haskell-based Sky compiler)

The following features are available in the new Haskell-based compiler.
Everything in the sections above still applies — this appends new surface.

### Safety Guarantees (on by default)

Every Sky program gets these out of the box with zero configuration:

| Attack vector | Defence |
|---------------|---------|
| SQL injection | `Std.Db` validates identifiers (Unicode-aware allow-list) and ANSI-quotes them; values always go through parameter placeholders |
| XSS | `String.htmlEscape`, auto-escape in `Sky.Live` VNode renderer, `X-Content-Type-Options: nosniff`, `isUrl` rejects `javascript:` / `data:` |
| CSRF / Clickjacking | `X-Frame-Options: SAMEORIGIN`, `Referrer-Policy: strict-origin-when-cross-origin` sent by default |
| Path traversal | `Path.safeJoin root rel` |
| DoS (body size) | HTTP server caps request body at 32 MiB → 413; Live event body capped at 1 MiB; `File.readFile` caps at 100 MiB |
| DoS (timeouts) | ReadHeaderTimeout 10s, ReadTimeout 30s, WriteTimeout 30s, IdleTimeout 120s; HTTP client 30s |
| DoS (request rate) | `RateLimit.allow name key cap perSec` token-bucket |
| Command injection | `Process.run` uses argv; never shell interpretation |
| Timing attacks | `Crypto.constantTimeEqual` for comparing secrets; bcrypt is already constant-time |
| Weak randomness | `Crypto.randomBytes`/`randomToken`, `Uuid.v4`/`v7`, Sky.Live session IDs all use `crypto/rand` |
| Password brute force | bcrypt cost 12; `Auth.passwordStrength` validates ≥8 chars + letter + digit |
| Panic cascading | Every HTTP/Live/FFI handler wraps in `recover()` — returns 500/Err, process never crashes |
| Unicode correctness | `String.length/slice/left/right` are rune-based; `String.normalize` (NFC) + `graphemes` (UAX #29) available |

### New Standard Library Modules

#### `Std.Log` — structured logging

```elm
import Std.Log as Log

Log.info "server started"              -- human format: <timestamp> INFO server started
Log.warn "rate-limit hit"              -- goes to stderr
Log.error "db connection lost"         -- goes to stderr
Log.with "request completed"           -- with key-value context
    (Dict.fromList [("method", "GET"), ("status", 200)])
```

Configure via env vars (or sky.toml `[log] format = "json" / level = "info"`):
- `SKY_LOG_LEVEL` = `debug | info | warn | error` (default `info`)
- `SKY_LOG_FORMAT` = `plain` (default) or `json`

Three-layer precedence: env > `.env` > `sky.toml`. Set `SKY_LOG_FORMAT=json` in production to flip on JSON without a rebuild.

#### `Sky.Core.System` — typed environment + process state access

```elm
import Sky.Core.System as System

System.getenv     "DATABASE_URL"              -- Task Error String — Err on missing
System.getenvOr   "PORT"   "8080"             -- Task Error String — never errs
System.getenvInt  "WORKERS"                   -- Task Error Int    — Err on missing/parse
System.getenvBool "DEBUG"                     -- Task Error Bool   — true/yes/1/on or false/no/0/off
System.requireEnv -- removed; `getenv` already errors on missing
```

Pattern at module top-level (Task collapses with `Task.run`):
```elm
port _ =
    Task.run (System.getenvOr "PORT" "8080")
        |> Result.withDefault "8080"
```

#### `Sky.Core.Uuid`

```elm
import Sky.Core.Uuid as Uuid

Uuid.v4        -- Task Error String — RFC 4122 random (crypto/rand)
Uuid.v7        -- Task Error String — time-ordered (better for DB primary keys)
Uuid.parse s   -- Result Error String — canonicalise or reject
```

#### `Sky.Http.Middleware`

```elm
import Sky.Http.Server as Server
import Sky.Http.Middleware as M

main =
    Server.listen 8080
        [ Server.get "/" (M.withLogging (M.withCors ["*"] homeHandler))
        , Server.get "/admin" (M.withBasicAuth "alice" "secret" adminHandler)
        , Server.post "/api" (M.withRateLimit "api" 100 10 apiHandler)  -- 100 burst, 10/sec refill
        ]
```

#### `Sky.Http.RateLimit`

```elm
import Sky.Http.RateLimit as RL
RL.allow "login" userEmail 5 1   -- 5 attempts, 1/sec refill → Bool
```

#### `Std.Html` / `Std.Css` / `Std.Live` — web framework

```elm
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick)

type Msg = Increment | Decrement
type alias Model = { count : Int }

init _ = ({ count = 0 }, Cmd.none)

update msg model =
    case msg of
        Increment -> ({ model | count = model.count + 1 }, Cmd.none)
        Decrement -> ({ model | count = model.count - 1 }, Cmd.none)

view model =
    div [ class "app" ]
        [ h1 [] [ text (String.fromInt model.count) ]
        , button [ onClick Decrement ] [ text "-" ]
        , button [ onClick Increment ] [ text "+" ]
        ]

main =
    app
        { init = init, update = update, view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" () ], notFound = ()
        }
```

SSE subscriptions work out of the box:

```elm
subscriptions model =
    case model.page of
        CounterPage -> Sub.every 1000 Tick   -- tick every second via SSE
        _ -> Sub.none
```

#### `Sky.Ffi` — safe FFI to arbitrary Go packages

Sky's FFI has a strict effect boundary enforced at runtime:

```elm
import Sky.Ffi as Ffi
import Sky.Core.Task as Task

-- Effect-unknown (auto-generated): must use callTask
Task.perform (Ffi.callTask "github.com/pkg.DoThing" [arg1, arg2])

-- Hand-audited pure (only via rt.RegisterPure in hand-written ffi/*.go)
Ffi.callPure "mypkg.reverse" [str]   -- Result Error a
```

Workflow:
```bash
sky add github.com/stripe/stripe-go/v82     # auto-fetches + generates bindings
```

The generator:
- Emits `ffi/<slug>_bindings.go` with `rt.Register` calls
- Auto-discovers + imports all referenced Go packages (stdlib, parent packages)
- Every binding wraps in panic-recover → Err; never crashes process
- Generics (`Fetch[T]`) genuinely can't be realised — SKIPPED with clear comment

### New String Functions (Unicode-correct)

```elm
String.length "世界"          -- 2 (runes, not bytes)
String.graphemes "👨‍👩‍👧"    -- 1 (UAX #29 grapheme cluster, emoji family = 1 char)
String.normalize s            -- NFC canonical form (web standard)
String.normalizeNFD s         -- decomposed form (for diacritic-insensitive search)
String.equalFold a b          -- case-insensitive Unicode equality
String.casefold s             -- Unicode-aware lowercase for comparison
String.isValid s              -- True iff valid UTF-8
String.slugify s              -- URL-safe, Unicode-preserving
String.htmlEscape s           -- & < > " ' → HTML entities
String.truncate n s           -- cut at n graphemes (never mid-emoji)
String.ellipsize n s          -- truncate + "…"
String.isEmail s              -- RFC 5322 syntactic check
String.isUrl s                -- rejects javascript:/data: (XSS-safe)
String.trimStart / trimEnd    -- Unicode whitespace (NBSP, ideographic, etc.)
```

### New Time Functions

```elm
Time.formatISO8601 ms    -- "2026-04-12T14:30:00.000Z" (JSON-friendly)
Time.formatRFC3339 ms    -- like ISO but with nanos
Time.formatHTTP ms       -- HTTP-date header format
Time.parseISO8601 str    -- Result Error Int (unix millis)
Time.addMillis delta ms
Time.diffMillis later earlier
```

### New Crypto Functions

```elm
Crypto.sha256 "hello"                    -- hex digest
Crypto.sha512 "hello"
Crypto.hmacSha256 "secret" "message"     -- for signing cookies/tokens
Crypto.constantTimeEqual a b             -- use for comparing secrets, NOT ==
Crypto.randomBytes 16                    -- Task Error String, hex
Crypto.randomToken 32                    -- Task Error String, URL-safe base64
```

### New Path Safety

```elm
Path.safeJoin "/var/www" "public/index.html"    -- Ok "/var/www/public/index.html"
Path.safeJoin "/var/www" "../../etc/passwd"     -- Err "safeJoin: path escapes root"
```

### `Std.Db` — SQLite + PostgreSQL

Auto-detects driver from connection string:

```elm
import Std.Db as Db

-- SQLite
db = Db.connect ":memory:"
db = Db.connect "/tmp/app.db"

-- PostgreSQL (pgx driver)
db = Db.connect "postgres://user:pw@localhost:5432/mydb?sslmode=disable"

-- All identifiers validated + ANSI-quoted; values parameterised
Db.insertRow db "users" (Dict.fromList [("email", "alice@example.com")])
Db.query db "SELECT * FROM users WHERE email = ?" ["alice@example.com"]
Db.getById db "users" 42
Db.updateById db "users" 42 (Dict.fromList [("role", "admin")])
```

### `Std.Auth`

```elm
import Std.Auth as Auth

Auth.register db email password          -- bcrypt cost 12, creates users table
Auth.login db email password             -- returns user row on success
Auth.hashPassword pw                     -- Result Error String (bcrypt, min 8, max 72 bytes)
Auth.passwordStrength pw                 -- Result Error () — validator
Auth.signToken secret claims expirySeconds -- HS256 JWT
Auth.verifyToken secret token            -- Result Error (Dict String any)
```

### Editor Integration

VS Code / Neovim / Emacs / Zed / Helix / Sublime all work via `sky lsp`.
Configure your editor to run `sky lsp` for `.sky` files.
The server provides:
- Diagnostics on save (type errors, parse errors)
- Hover with inferred types
- Completion (top-level defs + stdlib)

### Incremental Builds

Build artifacts cached in `.skycache/`. Source-hash-based: if no source files
have changed, Sky skips parse/canonicalise/type-check and reuses main.go.
Set `SKY_DCE=0` to disable dead-code elimination for debugging.

### FFI: How Generated Bindings Stay Safe

The generator uses **two runtime registries**:
- `rt.Register(name, fn)` — effect-unknown (default); only `Ffi.callTask`
- `rt.RegisterPure(name, fn)` — hand-audited pure; allows `Ffi.callPure`

Every auto-generated binding is effect-unknown. You get `Err "use callTask"`
if you try `callPure` on it — the runtime itself enforces Sky's effect
boundary. To promote an audited Go function to pure, write a hand-crafted
`ffi/<pkg>_pure.go` that calls `rt.RegisterPure` — it shadows the
auto-generated binding.

All FFI calls go through `defer/recover`: any Go panic becomes a Sky `Err`,
never a process crash.
