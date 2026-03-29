# Sky

> **Experimental** -- Sky is under active development. APIs and internals will change.

Sky is an experimental programming language that combines **Go's pragmatism** with **Elm's elegance** to create a simple, fullstack language where you write FP code and ship a single portable binary.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

**What Sky brings together:**

- **Go** -- fast compilation, single static binary, battle-tested ecosystem covering databases, HTTP servers, cloud SDKs, and everything in between
- **Elm** -- Hindley-Milner type inference, algebraic data types, exhaustive pattern matching, pure functions, The Elm Architecture
- **Phoenix LiveView** -- server-driven UI with DOM diffing, session management, and SSE subscriptions. No client-side framework. No WebSocket required

Sky compiles to Go. You get a single binary that runs your fullstack app -- API server, database access, and server-rendered interactive UI -- all from one codebase, one language, one deployment artifact.

The compiler, CLI, formatter, and LSP are all **self-hosted** — written in Sky itself, compiled to a ~4MB native Go binary. Zero Node.js/TypeScript/npm dependencies. The compiler bootstraps through 3+ generations of self-compilation.

### Why Sky exists

I've worked professionally with Go, Elm, TypeScript, Python, Dart, Java, and others for years. Each has strengths, but none gave me everything I wanted: **simplicity, strong guarantees, functional programming, fullstack capability, and portability** -- all in one language.

The pain point that kept coming back: startups and scale-ups building React/TypeScript frontends talking to a separate backend, creating friction at every boundary -- different type systems, duplicated models, complex build pipelines, and the constant uncertainty of "does this actually work?" that comes with the JS ecosystem. Maintenance becomes the real cost, not the initial build.

I always wanted to combine Go's tooling (fast builds, single binary, real concurrency, massive ecosystem) with Elm's developer experience (if it compiles, it works; refactoring is fearless; the architecture scales). Then, inspired by Phoenix LiveView, I saw how a server-driven UI could eliminate the frontend/backend split entirely -- one language, one model, one deployment.

The first attempt compiled Sky to JavaScript with the React ecosystem as the runtime. It worked, but Sky would have inherited all the problems I was trying to escape -- npm dependency chaos, bundle configuration, and the fundamental uncertainty of a dynamically-typed runtime. So I started over with Go as the compilation target: Elm's syntax and type system on the frontend, Go's ecosystem and binary output on the backend, with auto-generated FFI bindings that let you `import` any Go package and use it with full type safety.

Building a programming language is typically a years-long effort. What made Sky possible in weeks was AI-assisted development -- first with Gemini CLI, then settling on Claude Code, which fits my workflow and let me iterate on the compiler architecture rapidly. I designed the language semantics, the pipeline, the FFI strategy, and the Live architecture; AI tooling helped me execute at a pace that would have been impossible alone.

Sky is named for having no limits. It's experimental, opinionated, and built for one developer's ideal workflow -- but if it resonates with yours, I'd love to hear about it.

## Table of Contents

- [Quick Start](#quick-start)
- [Language Features](#language-features)
  - [Modules](#modules)
  - [Types](#types)
  - [Functions](#functions)
  - [Pattern Matching](#pattern-matching)
  - [Data Structures](#data-structures)
  - [Operators](#operators)
  - [Control Flow](#control-flow)
  - [Go Interop (FFI)](#go-interop-ffi)
  - [TEA Architecture](#tea-architecture)
- [Standard Library](#standard-library)
- [Sky.Live](#skylive)
- [Package Management](#package-management)
  - [sky.toml Reference](#skytoml-reference)
  - [Dependencies](#dependencies)
  - [Publishing Libraries](#publishing-libraries)
- [CLI Reference](#cli-reference)
- [Editor Integration](#editor-integration)
- [Examples](#examples)
- [Architecture](#architecture)
- [Compiler Optimisation Journey](#compiler-optimisation-journey)

---

## Quick Start

### Install

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh

# Custom install directory
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh -s -- --dir ~/.local/bin

# Or with Docker
docker run --rm -v $(pwd):/app -w /app anzel/sky sky --help
```

> **Prerequisite**: [Go](https://go.dev/) must be installed (Sky compiles to Go).

### Create a Project

```bash
sky init my-app
cd my-app
sky run
```

This creates:

```
my-app/
  sky.toml          -- project manifest
  CLAUDE.md         -- AI-assisted development context (Claude Code)
  src/
    Main.sky        -- entry point
```

The generated `CLAUDE.md` gives Claude Code full context about Sky syntax, stdlib, FFI, and Sky.Live — so it can write Sky code confidently from day one.

### Docker

Pre-built images are available on Docker Hub:

```bash
docker run --rm -v $(pwd)/my-app:/app -w /app anzel/sky sky build src/Main.sky
docker run --rm -v $(pwd)/my-app:/app -w /app anzel/sky sky run src/Main.sky
```

---

## Language Features

### Modules

Every Sky file declares a module with an exposing clause:

```elm
module Main exposing (main)

module Utils.String exposing (capitalize, trim)

module Sky.Core.Prelude exposing (..)     -- expose everything
```

Module names are PascalCase and hierarchical (dot-separated). The file path mirrors the module name: `Utils.String` lives at `src/Utils/String.sky`.

#### Imports

```elm
import Std.Log exposing (println)              -- selective import
import Sky.Core.String as String               -- qualified alias
import Sky.Core.Prelude exposing (..)          -- open import (all)
import Github.Com.Google.Uuid as Uuid          -- Go package via FFI
import Database.Sql as Sql                     -- Go stdlib
import Drivers.Sqlite as _ exposing (..)       -- side-effect import (Go driver)
```

`Sky.Core.Prelude` is implicitly imported into every module (provides `Result`, `Maybe`, `errorToString`, etc.).

### Types

Sky uses Hindley-Milner type inference with type class constraints. Type annotations are optional but recommended for top-level definitions. The type system enforces correctness at compile time -- if it compiles, it runs.

#### Type Annotations

```elm
add : Int -> Int -> Int
add x y = x + y

identity : a -> a
identity x = x
```

#### Built-in Types

| Type            | Description        | Examples              |
| --------------- | ------------------ | --------------------- |
| `Int`           | Integer            | `42`, `-7`            |
| `Float`         | Floating point     | `3.14`, `-0.5`        |
| `String`        | Text               | `"hello"`, `"line\n"` |
| `Bool`          | Boolean            | `True`, `False`       |
| `Char`          | Character          | `'a'`, `'Z'`          |
| `Unit`          | Empty tuple        | `()`                  |
| `List a`        | Ordered collection | `[1, 2, 3]`           |
| `Maybe a`       | Optional value     | `Just 42`, `Nothing`  |
| `Result err ok` | Success/failure    | `Ok 42`, `Err "fail"` |

#### Type Aliases

```elm
type alias Model =
    { count : Int
    , name : String
    , active : Bool
    }

type alias Point = { x : Int, y : Int }
```

#### Type Constraints

Sky enforces three built-in type constraints, checked at compile time:

| Constraint    | Allowed Types                                        | Used By                          |
| ------------- | ---------------------------------------------------- | -------------------------------- |
| `comparable`  | `Int`, `Float`, `String`, `Bool`, `Char`, tuples/lists of comparables | `List.sort`, `<`, `>`, `clamp`  |
| `number`      | `Int`, `Float`                                       | `+`, `-`, `*`, `/`, `%`         |
| `appendable`  | `String`, `List a`                                   | `++`                            |

```elm
sort : List comparable -> List comparable
clamp : comparable -> comparable -> comparable -> comparable
```

Passing the wrong type is a compile error:

```
-- sort [Just 1, Nothing]
-- Error: Type Maybe Int is not comparable.
```

#### Algebraic Data Types (Union Types)

```elm
type Maybe a
    = Just a
    | Nothing

type Result err ok
    = Ok ok
    | Err err

type Msg
    = Increment
    | Decrement
    | SetCount Int
    | Navigate Page
```

Constructors can carry zero or more typed fields. The compiler performs exhaustiveness checking on pattern matches.

#### Records

```elm
-- Creation
point = { x = 10, y = 20 }

-- Field access
point.x

-- Immutable update (creates a copy)
{ point | x = 99 }
{ model | count = model.count + 1, name = "Alice" }

-- Destructuring
let { x, y } = point in x + y
```

#### Tuples

```elm
pair = (1, "hello")
triple = (True, 42, "yes")

-- Destructuring
let (a, b) = pair in a + 1
```

### Functions

All functions are curried and support partial application.

```elm
-- Definition
add x y = x + y

-- With type annotation
greet : String -> String
greet name = "Hello, " ++ name

-- Lambda (anonymous function)
\x -> x + 1
\x y -> x + y

-- Partial application
addTen = add 10
result = addTen 5       -- 15

-- Function composition
f >> g                  -- (f >> g) x == g (f x)
f << g                  -- (f << g) x == f (g x)
```

#### Let-In Expressions

```elm
calculate x =
    let
        doubled = x * 2
        offset = 10

        helper : Int -> Int
        helper n = n + offset
    in
    helper doubled
```

Bindings in `let` can have optional type annotations. Each binding is in scope for all subsequent bindings and the body.

### Pattern Matching

#### Case Expressions

```elm
describe : Maybe Int -> String
describe value =
    case value of
        Just n ->
            "Got: " ++ String.fromInt n

        Nothing ->
            "Nothing here"
```

#### Pattern Types

```elm
-- Literal patterns
case x of
    42 -> "the answer"
    _ -> "something else"

-- Constructor patterns
case result of
    Ok value -> "success: " ++ value
    Err msg -> "error: " ++ msg

-- Tuple patterns
case pair of
    (0, 0) -> "origin"
    (x, y) -> String.fromInt x ++ ", " ++ String.fromInt y

-- List patterns
case items of
    [] -> "empty"
    [x] -> "single: " ++ x
    x :: xs -> "head: " ++ x     -- cons: head and tail

-- As patterns (bind whole + parts)
case value of
    Just x as original -> ...     -- original = Just x

-- Record patterns
case user of
    { name, age } -> name ++ " is " ++ String.fromInt age

-- Nested patterns
case value of
    Ok (Just x) -> x
    _ -> defaultValue
```

The compiler checks exhaustiveness -- it will warn if you miss a case.

### Data Structures

#### Lists

```elm
numbers = [1, 2, 3, 4, 5]
empty = []
combined = [1, 2] ++ [3, 4]     -- [1, 2, 3, 4]
withHead = 0 :: numbers          -- [0, 1, 2, 3, 4, 5]

-- Common operations (from Sky.Core.List)
List.map (\x -> x * 2) numbers
List.filter (\x -> x > 3) numbers
List.foldl (+) 0 numbers
List.head numbers                -- Just 1
List.length numbers              -- 5
```

#### Dictionaries

```elm
import Sky.Core.Dict as Dict

users = Dict.fromList [ ("alice", 1), ("bob", 2) ]
Dict.get "alice" users           -- Just 1
Dict.insert "charlie" 3 users
Dict.keys users                  -- ["alice", "bob"]
```

### Operators

| Operator                         | Description          | Precedence |
| -------------------------------- | -------------------- | ---------- |
| `\|>`                            | Pipeline (left)      | 0          |
| `<\|`                            | Application (right)  | 0          |
| `\|\|`                           | Logical OR           | 2          |
| `&&`                             | Logical AND          | 3          |
| `==`, `!=`, `<`, `>`, `<=`, `>=` | Comparison           | 4          |
| `++`                             | String/list concat   | 5          |
| `+`, `-`                         | Arithmetic           | 6          |
| `*`, `/`, `%`                    | Arithmetic           | 7          |
| `>>`, `<<`                       | Function composition | 9          |

#### Pipeline Operators

Pipelines are the idiomatic way to chain operations:

```elm
result =
    "  Hello, World!  "
        |> String.trim
        |> String.toLower
        |> String.split " "
        |> List.head
```

Equivalent to `List.head (String.split " " (String.toLower (String.trim " Hello, World! ")))`.

### Control Flow

#### If-Then-Else

```elm
status =
    if count > 10 then
        "high"
    else if count > 5 then
        "medium"
    else
        "low"
```

`if` is an expression -- both branches must return the same type.

#### Case-Of

See [Pattern Matching](#pattern-matching).

### Go Interop (FFI)

Sky can import any Go package. The compiler auto-generates type-safe, **Task-wrapped** bindings with panic recovery. Users never write FFI code.

**Principle**: all Go interop returns `Task String T` — effects are explicit, panics are caught, nil is handled.

#### Importing Go Packages

```elm
import Sky.Core.Task as Task

-- Go packages auto-generate Task-wrapped Sky bindings
import Github.Com.Google.Uuid as Uuid

main =
    Uuid.newString ()
        |> Task.map (\id -> "Generated: " ++ id)
        |> Task.perform
```

#### Return Type Mapping (Go → Sky)

| Go Return | Sky Return | Notes |
|-----------|-----------|-------|
| `T` (pure) | `T` | No wrapping for pure functions |
| `(T, error)` | `Task String T` | Error becomes `Err` in Task |
| `error` | `Task String ()` | Effectful, may fail |
| `void` (side effect) | `Task String ()` | Wrapped in lazy thunk |
| `*string`, `*int` | `Maybe String`, `Maybe Int` | Nil-safe |
| `*sql.DB` | `Db` (opaque handle) | Pointer is transparent |
| `[]string` | `List String` | Slice → List |
| `map[string]int` | `Dict String Int` | Map → Dict |

#### Panic Safety

Every Go call is wrapped with `defer recover()`. Panics become `Err`:

```elm
-- If the Go function panics, you get Err "panic: ..."
case Task.perform (riskyGoCall args) of
    Ok result -> use result
    Err msg -> handleError msg
```

#### Pointer Safety

- **Primitive pointers** (`*string`, `*int`) → `Maybe T`
- **Opaque struct pointers** (`*sql.DB`) → `Db` (type name, pointer hidden)

```elm
case getName user of
    Just name -> println name
    Nothing -> println "anonymous"
```

#### Auto-Generated Bindings

Go's `Package.Method` becomes `packageMethod` in Sky (lowerCamelCase):

| Go | Sky |
|----|-----|
| `uuid.NewString()` | `Uuid.newString ()` |
| `db.Query(q)` | `Sql.dbQuery db q` |
| `rows.Close()` | `Sql.rowsClose rows` |
| `http.StatusOK` | `Http.statusOK ()` |

#### Callback Bridging

Go callbacks are automatically bridged:

```elm
Mux.routerHandleFunc router "/api" myHandler
-- Generated Go: bridges func(any) any → func(http.ResponseWriter, *http.Request)
```

### TEA Architecture

Sky supports The Elm Architecture for stateful applications:

```elm
module Main exposing (main)

import Std.Cmd as Cmd exposing (Cmd)

type alias Model =
    { count : Int }

type Msg
    = Increment
    | Decrement

init : Unit -> (Model, Cmd Msg)
init _ =
    ({ count = 0 }, Cmd.none)

update : Msg -> Model -> (Model, Cmd Msg)
update msg model =
    case msg of
        Increment ->
            ({ model | count = model.count + 1 }, Cmd.none)

        Decrement ->
            ({ model | count = model.count - 1 }, Cmd.none)

view : Model -> String
view model =
    "Count: " ++ String.fromInt model.count
```

Key modules: `Std.Cmd`, `Std.Sub`, `Std.Task`, `Std.Program`.

---

## Standard Library

### Sky.Core (auto-imported via Prelude)

| Module              | Key Functions                                                                                                                                                                                                                          |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Sky.Core.Prelude`  | `Result`, `Maybe`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`, `js` (auto-imported)                                                                                                                     |
| `Sky.Core.Maybe`    | `withDefault`, `map`, `andThen`                                                                                                                                                                                                        |
| `Sky.Core.Result`   | `withDefault`, `map`, `andThen`, `mapError`, `toMaybe`                                                                                                                                                                                 |
| `Sky.Core.List`     | `map`, `filter`, `foldl`, `foldr`, `head`, `tail`, `length`, `append`, `reverse`, `sort`, `range`, `member`, `concat`, `concatMap`, `indexedMap`, `take`, `drop`, `intersperse`, `isEmpty`, `singleton`, `all`, `any`, `sum`, `product`, `maximum`, `minimum`, `partition`, `find`, `filterMap`, `sortBy`, `zip`, `unzip`, `map2`, `parallelMap` |
| `Sky.Core.String`   | `split`, `join`, `contains`, `replace`, `trim`, `length`, `toLower`, `toUpper`, `startsWith`, `endsWith`, `slice`, `fromInt`, `toInt`, `fromFloat`, `toFloat`, `lines`, `words`, `repeat`, `padLeft`, `padRight`, `reverse`, `indexes`, `concat`, `fromChar` |
| `Sky.Core.Dict`     | `empty`, `singleton`, `insert`, `get`, `remove`, `keys`, `values`, `map`, `foldl`, `fromList`, `toList`, `isEmpty`, `size`, `member`, `update`, `filter`, `union`, `intersect`, `diff`, `partition`, `foldr`                            |
| `Sky.Core.Debug`    | `log`, `toString`                                                                                                                                                                                                                      |
| `Sky.Core.Platform` | `getArgs`                                                                                                                                                                                                                              |
| `Sky.Core.Char`    | `isUpper`, `isLower`, `isAlpha`, `isDigit`, `isAlphaNum`, `toUpper`, `toLower`, `toCode`, `fromCode` |
| `Sky.Core.Tuple`   | `first`, `second`, `mapFirst`, `mapSecond`, `mapBoth`, `pair` |
| `Sky.Core.Bitwise` | `and`, `or`, `xor`, `complement`, `shiftLeftBy`, `shiftRightBy` |
| `Sky.Core.Set`     | `empty`, `singleton`, `insert`, `remove`, `member`, `size`, `toList`, `fromList`, `union`, `intersect`, `diff`, `map`, `filter`, `foldl` |
| `Sky.Core.Array`   | `empty`, `fromList`, `toList`, `get`, `set`, `push`, `length`, `slice`, `map`, `foldl`, `foldr`, `append` |
| `Sky.Core.File`    | `readFile`, `writeFile`, `exists`, `remove`, `mkdirAll`, `readDir`, `isDir` |
| `Sky.Core.Process` | `run`, `exit`, `getEnv`, `getCwd`, `loadEnv` |

### Sky.Core.Json

Elm-compatible JSON encoding/decoding:

```elm
import Sky.Core.Json.Encode as Encode
import Sky.Core.Json.Decode as Decode
import Sky.Core.Json.Decode.Pipeline as Pipeline

-- Encoding
json =
    Encode.object
        [ ("name", Encode.string "Alice")
        , ("age", Encode.int 30)
        , ("scores", Encode.list Encode.int [95, 87, 92])
        ]
    |> Encode.encode 2

-- Decoding with pipeline
type alias User = { name : String, age : Int }

userDecoder =
    Decode.succeed User
        |> Pipeline.required "name" Decode.string
        |> Pipeline.required "age" Decode.int

result = Decode.decodeString userDecoder jsonString
```

### Std (Application Framework)

| Module        | Purpose                                     |
| ------------- | ------------------------------------------- |
| `Std.Log`     | `println` for output                        |
| `Std.Cmd`     | `none`, `batch`, `perform`                  |
| `Std.Sub`     | `none`, `batch` -- subscription types       |
| `Std.Time`    | `every` -- timer subscriptions for Sky.Live |
| `Std.Task`    | `succeed`, `fail`, `map`, `andThen`, `sequence`, `parallel`, `lazy`, `perform` |
| `Std.Program` | `Program` type alias, `makeProgram`         |
| `Std.Uuid`    | `v4` (UUID generation)                      |

### Task and Concurrency

Sky wraps all effectful operations in `Task`. Tasks are lazy -- they only execute when `perform` is called.

```elm
import Sky.Core.Task as Task

-- Sequential: run tasks one after another
Task.sequence : List (Task err a) -> Task err (List a)

-- Parallel: run tasks concurrently using goroutines
Task.parallel : List (Task err a) -> Task err (List a)

-- Lazy: defer computation until task is executed
Task.lazy : (() -> a) -> Task err a

-- Parallel map: map a function over a list using goroutines
List.parallelMap : (a -> b) -> List a -> List b
```

**Example -- parallel HTTP requests:**

```elm
import Sky.Core.Task as Task
import Sky.Core.Http as Http

fetchAll urls =
    let
        tasks = List.map (\url -> Http.get url) urls
        results = Task.perform (Task.parallel tasks)
    in
        results
```

**Example -- sequential vs parallel:**

```elm
-- Sequential: total time = sum of individual times
seqResults = Task.perform (Task.sequence [ taskA, taskB, taskC ])

-- Parallel: total time = max of individual times
parResults = Task.perform (Task.parallel [ taskA, taskB, taskC ])
```

`Task.parallel` preserves result order -- the i-th result corresponds to the i-th task. If any task fails, the first error is returned. Under the hood, each task runs in its own goroutine with panic recovery.

`List.parallelMap` is the pure equivalent for non-Task computations:

```elm
-- Process items concurrently
results = List.parallelMap expensiveComputation items
```

### Std.Html (Server-Side Rendering)

Full HTML element and attribute support for Sky.Live and server-rendered apps:

```elm
import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Css as Css

view model =
    div [ class "container" ]
        [ h1 [ style [ Css.color (Css.hex "#333") ] ] [ text "Title" ]
        , p [] [ text "Content" ]
        , ul []
            (List.map (\item -> li [] [ text item ]) model.items)
        ]
```

Elements: `div`, `section`, `article`, `aside`, `header`, `footer`, `nav`, `main`, `h1`-`h6`, `p`, `span`, `strong`, `em`, `a`, `ul`, `ol`, `li`, `form`, `label`, `button`, `input`, `textarea`, `select`, `option`, `table`, `thead`, `tbody`, `tr`, `th`, `td`, `img`, `br`, `hr`, `pre`, `code`, `blockquote`, and more.

`Std.Css` provides typed CSS properties: `display`, `flexDirection`, `justifyContent`, `alignItems`, `padding`, `margin`, `color`, `backgroundColor`, `fontSize`, `borderRadius`, `boxShadow`, `transition`, `transform`, units (`px`, `rem`, `em`, `pct`, `vh`, `vw`), colors (`hex`, `rgb`, `rgba`, `hsl`, `hsla`), and 100+ more.

---

## Sky.Live

Sky.Live is a server-driven UI framework inspired by [Phoenix LiveView](https://hexdocs.pm/phoenix_live_view). Write standard TEA code; the compiler generates a Go HTTP server with DOM diffing, session management, SSE subscriptions, and a tiny (~3KB) JS client.

No WebSocket required. No client-side framework. Works on Lambda, Cloud Run, any HTTP host.

```elm
module Main exposing (main)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Time as Time

type Page = CounterPage | AboutPage

type alias Model = { page : Page, count : Int }

type Msg = Navigate Page | Increment | Decrement | Tick

init _ = ({ page = CounterPage, count = 0 }, Cmd.none)

update msg model =
    case msg of
        Navigate page -> ({ model | page = page }, Cmd.none)
        Increment -> ({ model | count = model.count + 1 }, Cmd.none)
        Decrement -> ({ model | count = model.count - 1 }, Cmd.none)
        Tick -> ({ model | count = model.count + 1 }, Cmd.none)

-- Subscriptions: auto-increment every second on CounterPage
subscriptions model =
    case model.page of
        CounterPage -> Time.every 1000 Tick
        _ -> Sub.none

view model =
    div []
        [ h1 [] [ text (String.fromInt model.count) ]
        , button [ onClick Increment ] [ text "+" ]
        , button [ onClick Decrement ] [ text "-" ]
        ]

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ route "/" CounterPage, route "/about" AboutPage ]
        , notFound = CounterPage
        }
```

### Event Patterns

Sky.Live events accept typed Msg constructors -- no string-based events needed:

```elm
-- Zero-arg constructors
button [ onClick Increment ] [ text "+" ]
button [ onClick DoSignOut ] [ text "Sign out" ]

-- Constructors with arguments
button [ onClick (Navigate HomePage) ] [ text "Home" ]
button [ onClick (SetFilter "bug") ] [ text "Bugs" ]

-- Input events with String-arg constructors (constructor as function reference)
input [ onInput SetSearch, value model.query ] []
input [ onInput UpdateEmail, value model.email ] []

-- Form submission
form [ onSubmit SubmitIdea ] [ ... ]
```

For non-Live server-rendered HTML, use `Std.Html.Events` which returns `(String, String)` attribute tuples with JavaScript handlers:

```elm
import Std.Html.Events as Events

button [ Events.onClick "alert('Hello!')" ] [ text "Click" ]
form [ Events.onSubmit "return confirm('Sure?')" ] [ ... ]
```

### How It Works

1. The compiler detects `Std.Live.app` and generates a Go HTTP server
2. On `GET /`, the server runs `init` + `view`, stores the model in a session, and returns full HTML
3. User interactions (`onClick`, `onInput`, etc.) send events to `POST /_sky/event`
4. The server runs `update`, diffs the old and new views, and returns minimal DOM patches
5. A tiny JS client applies the patches -- no full page reload
6. Subscriptions (e.g., `Time.every`) create SSE streams that push server updates to the browser

### Subscriptions

Subscriptions let the server push updates to the browser without user interaction. The `Sub` type is a proper ADT:

```elm
type Sub msg
    = SubNone                        -- no subscription
    | SubTimer Int msg               -- fire msg every N milliseconds
    | SubBatch (List (Sub msg))      -- combine multiple subscriptions
```

`Time.every 1000 Tick` constructs a `SubTimer 1000 Tick` value. At runtime, the Go server inspects this value, starts a timer goroutine, and pushes DOM patches via Server-Sent Events.

```elm
subscriptions : Model -> Sub Msg
subscriptions model =
    case model.page of
        DashboardPage ->
            Sub.batch
                [ Time.every 5000 RefreshData
                , Time.every 60000 CheckNotifications
                ]
        _ ->
            Sub.none
```

### Key Features

- **No WebSocket required** -- pure HTTP with SSE for subscriptions, polling fallback for serverless
- **Serverless-ready** -- polling fallback (`poll_interval`) works on Lambda, Cloud Run, any stateless environment
- **Configurable input** -- `input = "debounce"` sends on pause (default), `input = "blur"` sends only on blur/enter (fewer requests)
- **Unified Model/Msg** -- one TEA loop for the whole app, navigation is just a `Msg`
- **Direct VNode emission** -- Html functions produce VNode records, not HTML strings. No parsing overhead
- **Automatic component wiring** -- components following the protocol get auto-wired
- **Session stores** -- memory (default), sqlite, redis, postgresql
- **Concurrency-safe** -- per-session locking + optimistic concurrency (version field) prevents race conditions between SSE ticks and user events, even across multiple server instances
- **Subscriptions** -- runtime-carrying `Sub` values drive SSE server-push
- **256-bit session IDs** -- cryptographically random, base64url-encoded

### Component Protocol

Sky.Live components follow the Elm convention: module name = type name. A component exports `Foo`, `Msg`, `init`, `update`, and `view`. The compiler auto-wires component messages when the naming convention is followed:

```elm
import Counter exposing (Counter)

type alias Model = { myCounter : Counter }
type Msg = CounterMsg Counter.Msg    -- compiler auto-wires this

-- No manual forwarding needed in update!
```

See [docs/design/sky-live-components.md](docs/design/sky-live-components.md) for the full protocol.

### Shared State Module

For multi-module Sky.Live apps, define Page, Model, and Msg in a shared `State.sky` module:

```elm
-- State.sky
module State exposing (..)

type Page = BoardPage | DetailPage | SubmitPage
type Msg = Navigate Page | SetFilter String | DoSignOut | SubmitIdea

-- Sub-modules import State directly:
-- import State exposing (..)
-- button [ onClick DoSignOut ] [ text "Sign out" ]
```

This avoids circular dependencies and gives all modules access to typed Msg constructors. See `examples/12-skyvote` for a full example.

### Sky.Live Configuration

```toml
[live]
port = 4000
input = "blur"            # "debounce" | "blur"
poll_interval = 5000      # ms (0 = SSE only; >0 enables polling fallback for serverless)

[live.session]
store = "redis"           # memory | sqlite | redis | postgresql
url = "redis://localhost:6379"

[live.static]
dir = "static"
```

#### Runtime Environment Overrides

Sky.Live config values from `sky.toml` are embedded at compile time, but can be overridden at runtime via environment variables or a `.env` file. Env var names mirror the `sky.toml` structure with underscores. Priority (lowest to highest): compiled defaults < `sky.toml` < env vars < `.env` file.

| Variable | sky.toml | Default | Description |
|---|---|---|---|
| `SKY_LIVE_PORT` | `live.port` | `4000` | Server port |
| `SKY_LIVE_INPUT` | `live.input` | `debounce` | Input handling: `debounce` or `blur` |
| `SKY_LIVE_POLL_INTERVAL` | `live.poll_interval` | `0` | Polling interval in ms (0 = SSE only) |
| `SKY_LIVE_SESSION_STORE` | `live.session.store` | `memory` | Session store: `memory`, `sqlite`, `redis`, `postgresql` |
| `SKY_LIVE_SESSION_PATH` | `live.session.path` | _(empty)_ | Store file path (sqlite) |
| `SKY_LIVE_SESSION_URL` | `live.session.url` | _(empty)_ | Store connection URL (redis, postgresql) |
| `SKY_LIVE_STATIC_DIR` | `live.static.dir` | _(empty)_ | Path to static assets |
| `SKY_LIVE_TTL` | — | `30m` | Session TTL (Go duration format) |

```bash
# Override via env var
SKY_LIVE_PORT=8000 ./sky-out/app

# Or via .env file in the working directory
echo "SKY_LIVE_PORT=8000" > .env
./sky-out/app
```

See the [design docs](docs/design/) for the full architecture:

- [sky-live.md](docs/design/sky-live.md) -- HTTP-first server-driven UI design
- [sky-live-unified.md](docs/design/sky-live-unified.md) -- unified Model/Msg design
- [sky-live-components.md](docs/design/sky-live-components.md) -- component protocol & ecosystem

---

## Package Management

Sky has a built-in package manager that handles both Sky packages and Go packages.

### sky.toml Reference

The `sky.toml` file is the project manifest. Here is a complete reference:

```toml
# ---- Project Identity ----
name = "my-project"                # required: project name
version = "0.1.0"                  # required: semver version

# ---- Application Entry Point ----
entry = "src/Main.sky"             # optional: entry file for sky build/run
bin = "dist/app"                   # optional: output binary path

# ---- Source Configuration ----
[source]
root = "src"                       # source root directory (default: "src")

# ---- Library Configuration ----
# If present, this project exposes modules for other packages to import.
# Only modules listed in "exposing" are publicly importable.
# Omitting [lib] entirely means all modules are internal/private.
[lib]
exposing = ["Utils.String", "Utils.Math"]

# ---- Sky Dependencies ----
# Other Sky packages (from GitHub or a registry)
[dependencies]
"github.com/someone/sky-utils" = "latest"

# ---- Go Dependencies ----
# Go packages (standard library or third-party)
[go.dependencies]
"net/http" = "latest"
"github.com/google/uuid" = "latest"
"github.com/gorilla/mux" = "latest"

# ---- Sky.Live Configuration ----
[live]
port = 4000                        # HTTP server port
input = "debounce"                 # "debounce" (send on pause) | "blur" (send on blur/enter)
poll_interval = 0                  # polling fallback interval in ms (0 = SSE only)

[live.session]
store = "memory"                   # memory | sqlite | redis | postgresql
path = "./data/sessions.db"        # for sqlite
url = "redis://localhost:6379"     # for redis
# url = "postgres://user:pass@host/db" # for postgresql

[live.static]
dir = "static"                     # static file directory, served at /static/*
```

### Project Types

A project's role is determined by which fields are present:

| Configuration                | Role            | Description                                     |
| ---------------------------- | --------------- | ----------------------------------------------- |
| Has `entry`, no `[lib]`      | **Application** | A runnable app. `sky build` and `sky run` work. |
| Has `[lib]`, no `entry`      | **Library**     | Exposes modules for others to import.           |
| Has both `entry` and `[lib]` | **Both**        | An app that also exposes reusable modules.      |
| Neither `entry` nor `[lib]`  | **Private app** | Internal project, no public API.                |

### Dependencies

#### Adding Packages

```bash
# Auto-detects Sky vs Go package:
sky add github.com/someone/sky-utils     # Sky package (if repo has sky.toml)
sky add github.com/google/uuid           # Go package (if repo has go.mod)

# Go standard library:
sky add net/http
sky add database/sql
sky add crypto/sha256

# Remove a package:
sky remove github.com/google/uuid
```

**Auto-detection**: When you run `sky add github.com/...`, Sky checks the remote repository:

- If it has a `sky.toml` -> installs as a Sky package (cloned to `.skydeps/`)
- If it has a `go.mod` -> installs as a Go package (via `go get`)

**Transitive dependencies**: When installing a Sky package, its own dependencies (both Sky and Go) are automatically installed recursively.

#### Using Sky Dependencies

After `sky add github.com/someone/sky-utils` (assuming it exposes `Utils.String`), three import syntaxes are supported:

```elm
-- Stripped (cleanest, recommended)
import Utils.String exposing (capitalize)

-- Prefixed (PascalCase package name + module)
import SkyUtils.Utils.String exposing (capitalize)

-- Full path (mirrors the dependency URL)
import Github.Com.Someone.SkyUtils.Utils.String exposing (capitalize)
```

All three resolve to the same file in `.skydeps/`. The resolver respects each package's `[lib].exposing` list -- only publicly exposed modules are importable.

**Resolution precedence**: local `src/` modules > `.skydeps/` packages > stdlib. If a local module name conflicts with a dependency, use the full or prefixed import path to reach the dependency.

#### Using Go Dependencies

After `sky add github.com/google/uuid`:

```elm
import Github.Com.Google.Uuid as Uuid

main =
    let
        id = Uuid.newString ()
    in
    println "UUID:" id
```

### Publishing Libraries

To make a Sky package importable by others:

1. Add a `[lib]` section to `sky.toml`:

```toml
name = "sky-utils"
version = "1.0.0"

[source]
root = "src"

[lib]
exposing = ["Utils.String", "Utils.Math"]
```

2. Create the exposed modules:

```elm
-- src/Utils/String.sky
module Utils.String exposing (capitalize, kebabCase)

capitalize str = ...
kebabCase str = ...
```

3. Push to GitHub. Consumers install with:

```bash
sky add github.com/yourname/sky-utils
```

Only modules listed in `[lib].exposing` are importable. Internal modules (helpers, implementation details) remain private.

A library can also have Go dependencies. When someone installs your Sky package, its `[go.dependencies]` are transitively installed as well.

### Dependency Storage

| Type         | Location                  | Mechanism                      |
| ------------ | ------------------------- | ------------------------------ |
| Sky packages | `.skydeps/{org}/{repo}/`  | `git clone --depth 1`          |
| Go packages  | `.skycache/gomod/`        | `go get` (shared `go.mod`)     |
| Go bindings  | `.skycache/go/{package}/` | Auto-generated `.skyi` files   |
| Lock file    | `sky.lock`                | YAML, tracks resolved versions |

---

## CLI Reference

```bash
sky build [file.sky]         # Compile to Go and build binary (sky-out/app)
sky run [file.sky]           # Build and run
sky check [file.sky]         # Type-check without compiling (reports all diagnostics)
sky fmt <file-or-dir>        # Format code (Elm-style: 4-space, leading commas)
sky add <package>            # Add a Go or Sky dependency + auto-generate bindings
sky install                  # Install all deps + auto-generate missing bindings from source
sky remove <package>         # Remove a dependency
sky update                   # Update sky.toml dependencies to latest versions
sky upgrade                  # Self-upgrade to latest GitHub release
sky clean                    # Remove sky-out/ dist/
sky lsp                      # Start LSP server for editor integration
sky --version                # Show version
```

If `file.sky` is omitted, defaults to `src/Main.sky`.

### Build Pipeline

`sky build` performs:

1. Lex, parse, type-check all Sky modules (entry + local imports + FFI bindings)
2. Lower AST to Go IR, emit Go source (`sky-out/main.go`)
3. Copy FFI wrapper files from `.skycache/go/` to `sky-out/`
4. Dead code elimination: strip unused wrapper functions
5. Run `go mod init` + `go mod tidy` (if FFI wrappers present)
6. Run `go build` -> output binary at `sky-out/app`

### Type Checker

`sky check` runs the full type-checking pipeline without compiling to Go:

```bash
sky check src/Main.sky        # Check a single file and its dependencies
sky check                     # Check the entry from sky.toml
```

It reports:
- **Type mismatches** with human-readable variable names (`a`, `b`, `c` instead of `'t123`)
- **Non-exhaustive pattern matches** with missing constructors listed
- **Type annotation mismatches** when the annotation disagrees with inference
- **Type constraint violations** (e.g., sorting non-comparable types)
- **Go reserved word clashes** that will be auto-renamed

Multiple errors are reported per file (the parser recovers from syntax errors and continues).

### Formatter

`sky fmt` formats Sky code in Elm style:

- 4-space indentation
- Leading commas in lists and records
- `let`/`in` always multiline
- 80-character soft line width

---

## Editor Integration

### LSP

Sky ships with a Language Server that provides:

- **Completion** -- module names, functions, types
- **Go to Definition** -- jump to function/type definitions
- **Hover** -- show type information
- **Signature Help** -- function parameter hints
- **Formatting** -- via `sky fmt`
- **Document Symbols** -- outline view with functions, types, constructors
- **Find References** -- cross-module identifier search
- **Rename** -- workspace-wide symbol rename
- **Folding Ranges** -- collapse declarations, let/case blocks, imports

Start the LSP:

```bash
sky lsp
```

### Helix

Sky includes Helix editor integration. Configure in your Helix `languages.toml`:

```toml
[[language]]
name = "sky"
scope = "source.sky"
file-types = ["sky", "skyi"]
auto-format = true
formatter = { command = "sky", args = ["fmt", "-"] }
language-servers = ["sky-lsp"]
indent = { tab-width = 4, unit = " " }

[language-server.sky-lsp]
command = "sky"
args = ["lsp"]

[[grammar]]
name = "sky"
source = { git = "https://github.com/anzellai/tree-sitter-sky", rev = "main" }
```

---

## Examples

| Example             | Description                | Key Features                                                   |
| ------------------- | -------------------------- | -------------------------------------------------------------- |
| `01-hello-world`    | Basic hello world          | `println`, modules                                             |
| `02-go-stdlib`      | Go standard library        | `net/http`, `crypto/sha256`, `time`, `encoding/hex`            |
| `03-tea-external`   | TEA with external packages | `Model`/`Msg`/`update`, `uuid`, `godotenv`                     |
| `04-local-pkg`      | Multi-module project       | Local package imports (`Lib.Utils`)                            |
| `05-mux-server`     | HTTP server                | `gorilla/mux`, `godotenv`, request handling, `errorToString`   |
| `06-json`           | JSON encode/decode         | Elm-compatible `Json.Encode`, `Json.Decode`, pipeline decoding |
| `07-todo-cli`       | CLI with SQLite            | Command-line args, `database/sql`, `modernc.org/sqlite`        |
| `08-notes-app`      | Full CRUD web app          | HTTP server, database, auth, HTML templates                    |
| `09-live-counter`   | Sky.Live counter           | Server-driven UI, routing, SSE subscriptions (`Time.every`)    |
| `10-live-component` | Sky.Live components        | Component protocol, auto-wiring                                |
| `11-fyne-stopwatch` | Desktop GUI                | Fyne toolkit, timers, data binding                             |
| `12-skyvote`        | Full Sky.Live app          | SQLite, auth, voting, SSE auto-refresh                         |
| `13-skyshop`        | E-commerce Sky.Live app    | Firestore, Firebase Auth, Stripe checkout, admin panel, i18n, image uploads |
| `14-task-demo`      | Task effect boundary       | Task composition, error handling, sequencing                   |
| `15-http-server`    | Sky.Http.Server            | Routing, cookies, middleware, request/response builders        |

Run any example:

```bash
sky run examples/01-hello-world/src/Main.sky
```

---

## Architecture

### Compilation Pipeline

```
source.sky -> lexer -> layout filtering -> parser -> AST -> module graph -> type checker -> Go emitter -> go build
```

### Source Layout (Self-Hosted Sky Compiler)

```
src/                              -- Sky compiler (self-hosted, 34 modules, ~4MB binary)
  Main.sky                        -- CLI entry (build/run/check/fmt/add/install/update/upgrade/lsp/clean)
  Compiler/                       -- 21 modules: lexer, parser, type checker, lowerer, emitter
    Lexer.sky, Parser.sky, ParserExpr.sky, ParserPattern.sky
    Ast.sky, GoIr.sky, Types.sky, Env.sky
    Infer.sky, Unify.sky, Checker.sky, Exhaustive.sky
    Lower.sky, Emit.sky, Pipeline.sky, Resolver.sky
  Ffi/                            -- 4 modules: Go package inspector, type mapper, binding/wrapper gen
    Inspector.sky                 -- Runs go/packages to extract Go API metadata
    BindingGen.sky                -- Generates .skyi binding files (Sky type signatures)
    WrapperGen.sky                -- Generates Go wrapper functions with panic recovery
    TypeMapper.sky                -- Maps Go types to Sky types
  Formatter/                      -- Elm-style formatter (Doc algebra + Format)
  Lsp/                            -- Language Server (JSON-RPC + hover/definition/completion)

ts-compiler/                      -- Legacy TypeScript bootstrap (reference only)
stdlib-go/                        -- Go runtime implementations for stdlib modules
examples/                         -- 15 example projects
```

### Key Design Decisions

- **Self-hosted** -- the compiler compiles itself through 3+ generations (bootstrapping verified)
- **Task effect boundary** -- all IO goes through `Task`, panics caught, nil handled
- **Indentation-sensitive parsing** -- like Elm/Haskell, whitespace determines block structure
- **Hindley-Milner type inference** -- full inference with unification, explicit annotations optional
- **Go as backend** -- compiles to readable Go code, leverages Go's toolchain and ecosystem
- **Auto-generated FFI** -- Go packages introspected at build time; type-safe Task-wrapped wrappers generated automatically
- **Pointer safety** -- Go `*primitive` → `Maybe T`, opaque struct pointers are transparent handles
- **~4MB native binary** -- no Node.js, no npm, no TypeScript runtime. Just Go

---

## Compiler Optimisation Journey

The Sky compiler is self-hosted -- written in Sky, compiling to Go, then compiling itself. Building the largest example project (SkyShop: 43 local modules + 14 FFI modules including Stripe SDK, Firebase, Tailwind CSS) exposed a series of performance bottlenecks. Here's how each was identified and fixed.

### The Problem

SkyShop's build was **hanging indefinitely** -- the compiler never completed. The root cause: the `loadFfiForTypeCheck` function loaded the full Stripe SDK binding file (8.4 MB, 147K lines) once per dependency module. With 43 local modules each triggering a separate FFI loading pass, the compiler was parsing the Stripe SDK ~40 times.

### Optimisation Timeline

| # | Optimisation | Before | After | Technique |
|---|---|---|---|---|
| 1 | **Combined FFI imports** | Hanging | 2:56 | Collect all dep imports first, deduplicate, load once |
| 2 | **FFI light path** | 2:56 | 1:28 | Skip full type-check + lowering for `.skyi` modules; generate only constructors + wrapper vars |
| 3 | **Parallel module lowering** | 1:28 | 1:12 | `List.parallelMap` using goroutines for dependency module compilation |
| 4 | **Parallel FFI loading** | 1:12 | 1:06 | Parallel `skyi-filter` subprocess spawning for FFI binding files |
| 5 | **Parallel wrapper copying** | -- | -- | Concurrent file I/O for FFI wrapper `.go` files |
| 6 | **String.join optimisation** | 218s CPU | 207s CPU | Replace O(n^2) `++` chains with O(n) `String.join ""` in lowerer hot paths |
| 7 | **Incremental compilation** | 1:06 | 1:02 | Cache lowered Go declarations in `.skycache/lowered/`; skip re-lowering unchanged modules |

**Result: Hanging -> 1:02** (with ~300% CPU utilisation on multi-core machines).

### Key Technical Details

#### Combined FFI Loading (Step 1)

The original pipeline loaded FFI bindings per-module:

```sky
-- Before: O(modules * FFI) -- loaded Stripe SDK 40+ times
depFfiModules =
    List.concatMap (\pair -> loadFfiBindings srcRoot (snd pair).imports) localModules

-- After: O(FFI) -- load each FFI module once
allImports = List.append localImports (List.concatMap (\pair -> (snd pair).imports) localModules)
ffiModules = loadFfiBindings srcRoot allImports
```

#### FFI Light Path (Step 2)

FFI `.skyi` modules only need constructor declarations and wrapper variable bindings -- not full type-checking or AST-to-Go lowering. The light path generates just what's needed:

```sky
compileFfiModuleLight allModules pair =
    let
        ctorDecls = Lower.generateConstructorDecls registry mod.declarations
        wrapperVars = List.filterMap (makeFfiWrapperVar prefix) mod.declarations
    in
        deduplicateDecls (List.concat [ aliases , depImportAliases , prefixed , wrapperVars ])
```

#### Goroutine-Based Parallelism (Steps 3-5)

Sky now has `Task.parallel` and `List.parallelMap` -- pure functional interfaces backed by Go goroutines:

```elm
-- Run tasks concurrently, collect results in order
Task.parallel : List (Task err a) -> Task err (List a)

-- Map a function over a list using goroutines
List.parallelMap : (a -> b) -> List a -> List b
```

The compiler uses `List.parallelMap` for the three most expensive sequential operations:

```sky
-- Parallel module lowering (biggest win)
depDecls = List.concat (List.parallelMap (compileDependencyModule env modules ffiNames) loadedModules)

-- Parallel FFI binding loading
results = List.parallelMap (\imp -> loadOneFfiBinding srcRoot imp) deduped

-- Parallel wrapper file copying
_ = List.parallelMap (\modName -> copyOneFfiWrapper outDir projectRoot modName mainGoCode) uniqueModNames
```

The parallel helpers are written to a separate Go file (`sky-out/sky_parallel.go`) with proper multi-line formatting, avoiding the `goimports` issue where single-line function bodies cause import stripping.

#### String Concatenation (Step 6)

Sky's `++` operator compiles to Go string concatenation, which is O(n) per operation. Chained concatenation `a ++ b ++ c ++ d` creates O(n^2) intermediate strings. The fix: replace hot-path chains with `String.join "" [parts]`, which uses Go's `strings.Join` (single allocation):

```sky
-- Before: 4 intermediate strings
"sky_asInt(" ++ left ++ ") " ++ op ++ " sky_asInt(" ++ right ++ ")"

-- After: 1 allocation
String.join "" [ "sky_asInt(" , left , ") " , op , " sky_asInt(" , right , ")" ]
```

Applied to the lowerer's `emitGoExprInline` (called per AST node), `lowerBinary` (per operator), `emitBranchCode` (per case branch), and `patternToCondition` (per pattern match).

#### Incremental Compilation (Step 7)

Dependency modules that haven't changed don't need re-lowering. The compiler caches lowered Go declarations in `.skycache/lowered/`:

```
.skycache/lowered/
  Tailwind_Typography.go      -- cached lowered output
  Tailwind_Spacing.go
  Lib_Auth.go
  ...
```

On subsequent builds, cached modules skip type-checking and lowering entirely. Cross-module aliases are regenerated fresh each build to avoid duplicates. The cache is invalidated by `sky clean` or by deleting `.skycache/lowered/`.

### Current Build Times

| Project | Modules | Time | Notes |
|---|---|---|---|
| hello-world | 1 | <1s | Single module, no deps |
| skyvote | 32 local + 2 FFI | 1.7s | SQLite + Sky.Live |
| **skyshop** | 43 local + 14 FFI | **1:02** | Stripe, Firebase, Tailwind, Sky.Live |
| compiler self-build | 28 local | 5.6s | 2800 Go declarations |

### What's Next

- **Smarter cache invalidation** -- detect source changes per-module instead of invalidating everything
- **Symbol-level tree-shaking** -- collect wrapper references during lowering, skip unused FFI symbols (ported from the TS compiler)
- **Selective import emission** -- only emit Go imports for packages actually referenced
- **Multi-level caching** -- cache type-check results, inspector output, and wrapper generation separately

---

## Contributing

Sky is experimental and under active development. Contributions are welcome! Here's how you can help:

- **Try building something** -- the best feedback comes from real usage. Build a small app, hit the rough edges, and report what you find
- **Create examples** -- real-world examples (CRUD apps, API integrations, dashboards) help validate the language and show others what's possible
- **Report issues** -- compiler bugs, type checker edge cases, FFI gaps, or confusing error messages
- **Improve the stdlib** -- add missing functions to List, String, Dict, or propose new modules
- **Test Sky.Live** -- try the server-driven UI on different browsers, test SSE subscriptions, stress-test session management
- **Editor support** -- improve the LSP, add integrations for VS Code, Neovim, Zed

If you're interested, open an issue or start a discussion. PRs are welcome for bug fixes, examples, and stdlib additions.

## License

MIT License. See [LICENSE](LICENSE) for details.
