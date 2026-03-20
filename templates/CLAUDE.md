# CLAUDE.md — Sky Language Project

This is a [Sky](https://github.com/anzellai/sky) project. Sky is an Elm-inspired programming language that compiles to Go.

## Quick Reference

```bash
sky build src/Main.sky    # Compile to Go binary (output: dist/app)
sky run src/Main.sky      # Build and run
sky dev src/Main.sky      # Watch mode: auto-rebuild on changes
sky fmt src/Main.sky      # Format code (Elm-style: 4-space indent, leading commas)
sky add <package>         # Add dependency (auto-detects Sky vs Go package)
sky install               # Install all dependencies from sky.toml
```

## Language Syntax (Elm-like)

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)    -- Result, Maybe, identity, errorToString (auto-imported)
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

-- Pattern matching (exhaustiveness checked)
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
    println "Result:" (String.fromInt updated.x)
```

## Key Language Features

- **Types**: `Int`, `Float`, `String`, `Bool`, `List a`, `Maybe a`, `Result err ok`, `Dict k v`, `(a, b)` tuples
- **Operators**: `++` (concat), `|>` `<|` (pipe), `>>` `<<` (compose), `==` `!=` `<` `>` `<=` `>=`, `&&` `||`, `+` `-` `*` `/` `%`
- **Patterns**: literals, constructors (`Just x`, `Ok v`), tuples `(a, b)`, lists `x :: xs`, `[]`, `[x]`, wildcards `_`, as-patterns `Just x as original`
- **Records**: `{ field = value }`, access `record.field`, update `{ record | field = newValue }`
- **Lambda**: `\x -> x + 1`, `\x y -> x + y`
- **If/else**: `if cond then a else b` (expression, both branches same type)
- **Let/in**: `let x = 1 in x + 2` (always multiline in formatted code)

## Go Interop (FFI)

Sky can import any Go package. The compiler auto-generates type-safe bindings:

```elm
import Net.Http as Http                    -- net/http
import Github.Com.Google.Uuid as Uuid      -- github.com/google/uuid
import Database.Sql as Sql                 -- database/sql
import Drivers.Sqlite as _ exposing (..)   -- side-effect import (Go driver)
```

**Naming convention**: Go methods become `{type}{Method}` in Sky:
- `db.Query(q)` → `Sql.dbQuery db q`
- `router.HandleFunc(p, h)` → `Mux.routerHandleFunc router p h`
- `req.URL` (field) → `Http.requestUrl req`

**Return types**: `(T, error)` → `Result Error T`, `*string` → `Maybe String`, `*sql.DB` → `Db` (opaque handle)

**Error handling**: Use `errorToString` from Prelude to convert Go errors to strings.

## Sky.Live (Server-Driven UI)

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

-- Subscriptions: server-push via SSE
subscriptions model =
    case model.page of
        HomePage -> Time.every 1000 Tick
        _ -> Sub.none

view model =
    div []
        [ styleNode [] (stylesheet [ rule "body" [ fontFamily "sans-serif" ] ])
        , h1 [] [ text (String.fromInt model.count) ]
        , button [ onClick "Increment" ] [ text "+" ]
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

**Events**: `onClick "MsgName"`, `onInput "MsgName"` (sends value), `onSubmit "MsgName"` (sends form data)
**Navigation**: `a [ href "/about", attribute "sky-nav" "" ] [ text "About" ]` (client-side nav)
**Styling**: Use `Std.Css` with `stylesheet`, `rule`, `px`, `rem`, `hex`, `rgb`, etc. — not inline style strings.
**Html functions** return VNode records (not strings). Use `render vnode` to convert to HTML string for non-Live apps.

## Standard Library

| Module | Key Functions |
|--------|---------------|
| `Sky.Core.Prelude` | `Result (Ok/Err)`, `Maybe (Just/Nothing)`, `identity`, `errorToString` |
| `Sky.Core.List` | `map`, `filter`, `foldl`, `foldr`, `head`, `tail`, `length`, `append`, `reverse`, `sort`, `range`, `concat`, `concatMap`, `indexedMap`, `take`, `drop`, `intersperse`, `isEmpty`, `member` |
| `Sky.Core.String` | `split`, `join`, `contains`, `replace`, `trim`, `length`, `toLower`, `toUpper`, `startsWith`, `endsWith`, `slice`, `fromInt`, `toInt`, `fromFloat`, `toFloat`, `lines`, `words`, `repeat`, `reverse`, `indexes` |
| `Sky.Core.Dict` | `empty`, `singleton`, `insert`, `get`, `remove`, `keys`, `values`, `map`, `foldl`, `fromList`, `toList`, `isEmpty`, `size`, `member`, `update` |
| `Sky.Core.Maybe` | `withDefault`, `map`, `andThen` |
| `Sky.Core.Result` | `withDefault`, `map`, `andThen`, `mapError`, `toMaybe` |
| `Sky.Core.Json.Encode` | `string`, `int`, `float`, `bool`, `null`, `list`, `object`, `encode` |
| `Sky.Core.Json.Decode` | `string`, `int`, `float`, `bool`, `field`, `at`, `list`, `map`, `map2`..`map8`, `succeed`, `fail`, `andThen`, `oneOf`, `nullable`, `decodeString` |
| `Sky.Core.Debug` | `log`, `toString` |
| `Std.Log` | `println` |
| `Std.Cmd` | `none`, `batch` |
| `Std.Sub` | `none`, `batch` (Sub type: `SubNone`, `SubTimer Int msg`, `SubBatch`) |
| `Std.Time` | `every` (timer subscription, e.g., `every 1000 Tick`) |
| `Std.Html` | `div`, `span`, `h1`-`h6`, `p`, `a`, `button`, `input`, `form`, `text`, `node`, `render`, `styleNode`, etc. |
| `Std.Html.Attributes` | `class`, `id`, `style`, `href`, `src`, `type_`, `value`, `placeholder`, `attribute`, etc. |
| `Std.Css` | `stylesheet`, `rule`, `media`, `px`, `rem`, `em`, `pct`, `hex`, `rgb`, `rgba`, `display`, `flexDirection`, `justifyContent`, `alignItems`, `padding`, `margin`, `color`, `backgroundColor`, `fontSize`, `borderRadius`, `transition`, `transform`, and 100+ more |
| `Std.Live` | `app`, `route` |
| `Std.Live.Events` | `onClick`, `onInput`, `onSubmit`, `onChange`, `onBlur`, `onFocus`, `onDblClick` |

## Project Structure

```
my-project/
  sky.toml              -- project manifest
  src/
    Main.sky            -- entry point
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

[live]                          # only for Sky.Live apps
port = 4000
input = "debounce"              # "debounce" | "blur"

[live.session]
store = "memory"                # memory | sqlite | redis | postgresql
```

## Coding Conventions

- **4-space indentation**, leading commas in lists/records
- **Module names** are PascalCase, match file paths: `Lib.Utils` → `src/Lib/Utils.sky`
- **`let`/`in`** always multiline when formatted
- **No semicolons**, no curly braces — indentation-sensitive like Elm/Haskell
- Use **`Std.Css`** for styling (not inline style strings)
- Use **`errorToString`** to convert Go errors to strings
- Pattern match on **`Result`** (`Ok val` / `Err e`) for Go functions that return errors
- Pattern match on **`Maybe`** (`Just val` / `Nothing`) for Go `*primitive` pointer returns

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

userDecoder =
    Decode.succeed (\n a -> { name = n, age = a })
        |> Pipeline.required "name" Decode.string
        |> Pipeline.required "age" Decode.int
```
