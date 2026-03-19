# Sky

Sky is an experimental programming language inspired by [Elm](https://elm-lang.org/), compiling to [Go](https://go.dev/). It combines Elm's syntax, type safety, and architecture with Go's performance and ecosystem.

The compiler, CLI, formatter, LSP, and editor integrations are all written in TypeScript.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

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

---

## Quick Start

### Prerequisites

- [Node.js](https://nodejs.org/) (v18+)
- [Go](https://go.dev/) (for running compiled output)

### Install and Build

```bash
# Install dependencies
npm install

# Build the compiler
npm run build
```

### Create a Project

```bash
node dist/bin/sky.js init my-app
cd my-app
node dist/bin/sky.js run
```

This creates:

```
my-app/
  sky.toml          -- project manifest
  src/
    Main.sky        -- entry point
```

### Building a Self-Contained Binary

To produce a standalone native binary that requires no Node.js runtime:

```bash
npm run bundle
```

This bundles the compiler with esbuild, embeds the standard library, and uses [pkg](https://github.com/vercel/pkg) to produce native executables in `bin/`:

- `bin/sky` -- the compiler CLI
- `bin/sky-lsp` -- the language server

Copy these anywhere on your `PATH`:

```bash
cp bin/sky /usr/local/bin/sky
cp bin/sky-lsp /usr/local/bin/sky-lsp
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

`Sky.Core.Prelude` is implicitly imported into every module (provides `Result`, `Maybe`, etc.).

### Types

Sky uses Hindley-Milner type inference. Type annotations are optional but recommended for top-level definitions.

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

Sky can import and use any Go package -- both standard library and third-party.

#### Importing Go Packages

Go packages are mapped to PascalCase module names:

| Go Package               | Sky Import               |
| ------------------------ | ------------------------ |
| `net/http`               | `Net.Http`               |
| `crypto/sha256`          | `Crypto.Sha256`          |
| `time`                   | `Time`                   |
| `os`                     | `Os`                     |
| `github.com/google/uuid` | `Github.Com.Google.Uuid` |
| `github.com/gorilla/mux` | `Github.Com.Gorilla.Mux` |

```elm
import Net.Http as Http
import Time
import Github.Com.Google.Uuid as Uuid

main =
    let
        now = Time.now ()
        uuid = Uuid.newString ()
        resp = Http.get "https://example.com"
    in
    case resp of
        Ok r -> println "Status:" (Http.responseStatusCode r)
        Err e -> println "Error:" e
```

#### Foreign Import Declarations

For low-level control, use `foreign import`:

```elm
foreign import "fmt" exposing (Sprintf, println)
foreign import "sky_wrappers" exposing (Sky_list_Map, Sky_list_Filter)
foreign import "@sky/runtime/cmd" exposing (none, batch, perform)
```

#### Side-Effect Imports

Some Go packages need to be imported for side effects only (e.g., database drivers):

```elm
import Drivers.Sqlite as _ exposing (..)
```

The `as _` syntax generates a Go blank import (`import _ "package"`).

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
| `Sky.Core.Prelude`  | `Result`, `Maybe`, `identity` (auto-imported)                                                                                                                                                                                          |
| `Sky.Core.Maybe`    | `withDefault`, `map`, `andThen`                                                                                                                                                                                                        |
| `Sky.Core.Result`   | `withDefault`, `map`, `andThen`, `mapError`, `toMaybe`                                                                                                                                                                                 |
| `Sky.Core.List`     | `map`, `filter`, `foldl`, `foldr`, `head`, `tail`, `length`, `append`, `reverse`, `sort`, `range`, `member`, `concat`, `concatMap`, `indexedMap`, `take`, `drop`, `intersperse`, `isEmpty`                                             |
| `Sky.Core.String`   | `split`, `join`, `contains`, `replace`, `trim`, `length`, `toLower`, `toUpper`, `startsWith`, `endsWith`, `slice`, `fromInt`, `toInt`, `fromFloat`, `toFloat`, `lines`, `words`, `repeat`, `padLeft`, `padRight`, `reverse`, `indexes` |
| `Sky.Core.Dict`     | `empty`, `singleton`, `insert`, `get`, `remove`, `keys`, `values`, `map`, `foldl`, `fromList`, `toList`, `isEmpty`, `size`, `member`, `update`                                                                                         |
| `Sky.Core.Debug`    | `log`, `toString`                                                                                                                                                                                                                      |
| `Sky.Core.Platform` | `getArgs`                                                                                                                                                                                                                              |

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

| Module        | Purpose                             |
| ------------- | ----------------------------------- |
| `Std.Log`     | `println` for output                |
| `Std.Cmd`     | `none`, `batch`, `perform`          |
| `Std.Sub`     | `none`, `batch`                     |
| `Std.Task`    | `succeed`, `fail`, `map`, `andThen` |
| `Std.Program` | `Program` type alias, `makeProgram` |
| `Std.Time`    | `every` (subscription timer)        |
| `Std.Uuid`    | `v4` (UUID generation)              |

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

Sky.Live is an HTTP-first, server-driven UI framework. Write standard TEA code; the compiler generates a Go HTTP server with DOM diffing, session management, and a tiny (~3KB) JS client.

No WebSocket required. Works on Lambda, Cloud Run, any HTTP host.

```elm
module Main exposing (main)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick)
import Std.Cmd as Cmd

type Page = CounterPage | AboutPage

type alias Model = { page : Page, count : Int }

type Msg = Navigate Page | Increment | Decrement

init _ = ({ page = CounterPage, count = 0 }, Cmd.none)

update msg model =
    case msg of
        Navigate page -> ({ model | page = page }, Cmd.none)
        Increment -> ({ model | count = model.count + 1 }, Cmd.none)
        Decrement -> ({ model | count = model.count - 1 }, Cmd.none)

view model =
    div []
        [ nav []
            [ button [ onClick (Navigate CounterPage) ] [ text "Counter" ]
            , button [ onClick (Navigate AboutPage) ] [ text "About" ]
            ]
        , case model.page of
            CounterPage ->
                div []
                    [ h1 [] [ text (String.fromInt model.count) ]
                    , button [ onClick Increment ] [ text "+" ]
                    , button [ onClick Decrement ] [ text "-" ]
                    ]
            AboutPage ->
                div [] [ text "Built with Sky.Live" ]
        ]

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" CounterPage, route "/about" AboutPage ]
        , notFound = CounterPage
        }
```

### How It Works

1. The compiler detects `Std.Live.app` and generates a Go HTTP server
2. On `GET /`, the server runs `init` + `view`, stores the model in a session, and returns full HTML
3. User interactions (`onClick`, `onInput`, etc.) send events to `POST /_sky/event`
4. The server runs `update`, diffs the old and new views, and returns minimal DOM patches
5. A tiny JS client applies the patches -- no full page reload

### Key Features

- **No WebSocket required** -- pure HTTP with optional SSE for subscriptions
- **Unified Model/Msg** -- one TEA loop for the whole app, navigation is just a `Msg`
- **Automatic component wiring** -- components following the protocol get auto-wired
- **Session stores** -- memory (default), sqlite, postgresql, redis, dynamodb
- **Subscriptions** -- `Std.Time.every 1000 Tick` auto-creates SSE streams
- **Static analysis diffing** -- compiler traces field dependencies, only patches what changed

### Component Protocol

Sky.Live components follow the Elm convention: module name = type name. A component exports `Foo`, `Msg`, `init`, `update`, and `view`. The compiler auto-wires component messages when the naming convention is followed:

```elm
import Counter exposing (Counter)

type alias Model = { myCounter : Counter }
type Msg = CounterMsg Counter.Msg    -- compiler auto-wires this

-- No manual forwarding needed in update!
```

See [docs/design/sky-live-components.md](docs/design/sky-live-components.md) for the full protocol.

### Sky.Live Configuration

```toml
[live]
port = 4000
ttl = "30m"

[live.session]
store = "memory"

[live.static]
dir = "static"
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
ttl = "30m"                        # session time-to-live

[live.session]
store = "memory"                   # memory | sqlite | postgresql | redis | dynamodb
path = "./data/sessions.db"        # for sqlite
url = "$DATABASE_URL"              # for postgresql/redis
snapshot_interval = 50             # snapshot model every N messages

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

After `sky add github.com/someone/sky-utils` (assuming it exposes `Utils.String`):

```elm
import Utils.String exposing (capitalize)

main =
    println (capitalize "hello")
```

The module resolver searches `.skydeps/` and respects each package's `[lib].exposing` list -- only publicly exposed modules are importable.

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
sky init [name]              # Create a new project
sky add <package>            # Add a dependency (auto-detects Sky vs Go)
sky remove <package>         # Remove a dependency
sky install                  # Install all dependencies from sky.toml
sky update                   # Update lockfile
sky build [file.sky]         # Compile to Go and build binary
sky run [file.sky]           # Build and run (detects Sky.Live apps)
sky dev [file.sky]           # Watch mode: auto-rebuild + restart on changes
sky fmt <file-or-dir>        # Format code (Elm-style)
sky lsp                      # Start LSP server for editor integration
```

If `file.sky` is omitted, the CLI reads `entry` from `sky.toml`.

### Build Pipeline

`sky build` performs:

1. Compile Sky source to Go (`dist/`)
2. Copy Go wrappers and helpers
3. Run `go mod init` + `go mod tidy`
4. Run `go build` -> output binary at `bin` path (default `dist/app`)

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

Start the LSP:

```bash
node dist/bin/sky-lsp.js
# or if built as binary:
sky lsp
```

### Helix

Sky includes Helix editor integration. Configure in your Helix `languages.toml`:

```toml
[[language]]
name = "sky"
scope = "source.sky"
file-types = ["sky"]
auto-format = true
formatter = { command = "sky", args = ["fmt", "-"] }
language-servers = ["sky-lsp"]
indent = { tab-width = 4, unit = " " }

[language-server.sky-lsp]
command = "sky-lsp"
args = ["--stdio"]
```

---

## Examples

| Example             | Description                | Key Features                                                   |
| ------------------- | -------------------------- | -------------------------------------------------------------- |
| `01-hello-world`    | Basic hello world          | `println`, modules                                             |
| `02-go-stdlib`      | Go standard library        | `net/http`, `crypto/sha256`, `time`, `encoding/hex`            |
| `03-tea-external`   | TEA with external packages | `Model`/`Msg`/`update`, `uuid`, `godotenv`                     |
| `04-local-pkg`      | Multi-module project       | Local package imports (`Lib.Utils`)                            |
| `05-mux-server`     | HTTP server                | `gorilla/mux`, `godotenv`, request handling                    |
| `06-json`           | JSON encode/decode         | Elm-compatible `Json.Encode`, `Json.Decode`, pipeline decoding |
| `07-todo-cli`       | CLI with SQLite            | Command-line args, `database/sql`, `modernc.org/sqlite`        |
| `08-notes-app`      | Full CRUD web app          | HTTP server, database, auth, HTML templates                    |
| `09-live-counter`   | Sky.Live counter           | Server-driven UI, routing, events                              |
| `10-live-component` | Sky.Live components        | Component protocol, auto-wiring                                |

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

### Source Layout

```
src/
  compiler.ts            -- Core compilation pipeline
  ast/ast.ts             -- AST node definitions
  lexer/lexer.ts         -- Indentation-aware lexer
  parser/                -- Pratt-style parser with layout filtering
  modules/resolver.ts    -- Module resolution & dependency graph
  types/                 -- HM type system (infer, unify, checker, adt, exhaustiveness)
  core-ir/core-ir.ts     -- Core Intermediate Representation
  go-ir/go-ir.ts         -- Go Intermediate Representation
  lower/                 -- AST -> CoreIR -> GoIR lowering
  emit/go-emitter.ts     -- Go code generation
  interop/go/            -- Go FFI (bindings, wrappers, package inspection)
  pkg/                   -- Package manager (manifest, installer, resolver, lockfile, registry)
  live/                  -- Sky.Live compiler support
  runtime/               -- Sky.Live Go runtime files
  lsp/                   -- Language Server (completion, definition, hover, signature)
  stdlib/                -- Standard library .sky files
  cli/                   -- CLI commands
  bin/                   -- Entry points (sky.ts, sky-lsp.ts, build-binary.js)
  utils/                 -- Helpers (assets, paths)
```

### Key Design Decisions

- **Indentation-sensitive parsing** -- like Elm/Haskell, whitespace determines block structure
- **Hindley-Milner type inference** -- full inference with unification, explicit annotations optional
- **Go as backend** -- compiles to readable Go code, leverages Go's toolchain and ecosystem
- **Universal unifiers** -- `JsValue`, `Foreign` types bridge Sky and Go type systems
- **Prelude auto-import** -- `Sky.Core.Prelude` available everywhere without explicit import
- **Virtual assets** -- stdlib files are bundled into the binary via `build-binary.js`

---

## License

This project is experimental and under active development.
