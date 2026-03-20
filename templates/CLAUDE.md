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
    println "Result:" (String.fromInt updated.x)
```

### Types

`Int`, `Float`, `String`, `Bool`, `Char`, `Unit` (`()`), `List a`, `Maybe a` (`Just a | Nothing`), `Result err ok` (`Ok ok | Err err`), `Dict k v`, tuples `(a, b)`, records `{ field : Type }`

### Operators

`++` (concat), `|>` `<|` (pipe), `>>` `<<` (compose), `==` `!=` `<` `>` `<=` `>=`, `&&` `||`, `+` `-` `*` `/` `%`, `::` (cons)

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

## Go Interop (FFI)

Sky can import any Go package. The compiler auto-generates type-safe bindings at build time:

```elm
import Net.Http as Http                    -- net/http
import Github.Com.Google.Uuid as Uuid      -- github.com/google/uuid
import Database.Sql as Sql                 -- database/sql
import Drivers.Sqlite as _ exposing (..)   -- side-effect import (Go driver)
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

| Go Return | Sky Return |
|-----------|------------|
| `(T, error)` | `Result Error T` |
| `error` | `Result Error Unit` |
| `*string`, `*int` | `Maybe String`, `Maybe Int` |
| `*sql.DB` | `Db` (opaque handle) |
| `[]string` | `List String` |
| `map[string]int` | `Map String Int` |

### Error Handling

```elm
case Http.listenAndServe ":8080" handler of
    Ok _ -> println "Started"
    Err e -> println "Failed:" (errorToString e)
```

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
map2 : (a -> b -> c) -> Result e a -> Result e b -> Result e c
map3 : (a -> b -> c -> d) -> Result e a -> Result e b -> Result e c -> Result e d
andThen : (a -> Result e b) -> Result e a -> Result e b
withDefault : a -> Result e a -> a
fromMaybe : e -> Maybe a -> Result e a
mapError : (e -> f) -> Result e a -> Result f a
```

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
```

### Sky.Core.String

```elm
fromInt : Int -> String
fromFloat : Float -> String
toInt : String -> Result Error Int
toFloat : String -> Result Error Float
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

```elm
isUpper : Char -> Bool
isLower : Char -> Bool
isAlpha : Char -> Bool
isDigit : Char -> Bool
isAlphaNum : Char -> Bool
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

```elm
readFile : String -> Result Error String
writeFile : String -> String -> Result Error Unit
exists : String -> Bool
remove : String -> Result Error Unit
mkdirAll : String -> Result Error Unit
readDir : String -> Result Error (List String)
isDir : String -> Bool
```

### Sky.Core.Process

```elm
run : String -> List String -> Result Error String
exit : Int -> Unit
getEnv : String -> Maybe String
getCwd : Result Error String
```

### Sky.Core.Debug

```elm
log : String -> a -> a          -- prints tag + value, returns value unchanged
toString : a -> String          -- convert any value to string representation
```

### Sky.Core.Platform

```elm
getArgs : () -> List String     -- command-line arguments
```

### Sky.Core.Json.Encode

```elm
encode : Int -> Value -> String       -- serialize with indentation
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
decodeString : Decoder a -> String -> Result String a
decodeValue : Decoder a -> Value -> Result String a
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

```elm
println : a -> a -> ()     -- println tag value (variadic, uses Go fmt.Println)
```

### Std.Cmd

```elm
type Cmd msg = Cmd Foreign

none : Cmd msg
batch : List (Cmd msg) -> Cmd msg
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
onClick : String -> (String, String)      -- "MsgName" or "MsgName arg"
onInput : String -> (String, String)      -- sends input value with msg
onSubmit : String -> (String, String)     -- sends form data with msg
onChange : String -> (String, String)      -- for select, checkbox
onDblClick : String -> (String, String)
onFocus : String -> (String, String)
onBlur : String -> (String, String)
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

**Navigation**: `a [ href "/about", attribute "sky-nav" "" ] [ text "About" ]`
**Styling**: Use `Std.Css` with `stylesheet`/`rule` — not inline style strings.

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

[live]                          # only for Sky.Live apps
port = 4000
input = "debounce"              # "debounce" | "blur"

[live.session]
store = "memory"                # memory | sqlite | redis | postgresql
```

## Coding Conventions

- **Module names** are PascalCase, match file paths: `Lib.Utils` → `src/Lib/Utils.sky`
- **No semicolons**, no curly braces — indentation-sensitive like Elm/Haskell
- Use **`Std.Css`** for styling (not inline style strings)
- Use **`errorToString`** to convert Go errors to strings
- Pattern match on **`Result`** (`Ok val` / `Err e`) for Go functions returning errors
- Pattern match on **`Maybe`** (`Just val` / `Nothing`) for Go `*primitive` pointer returns

## Code Formatting

**Always run `sky fmt <file>.sky` (or `sky fmt <file>.skyi`) after any changes to `.sky` or `.skyi` files.** The formatter is opinionated and canonical — all Sky code must be formatted before committing.

### Rules

- **Line width**: 80 characters max
- **Indentation**: 4 spaces (no tabs)
- **Leading commas**: multi-line lists, records, tuples, and type variants use leading comma/pipe style

### Declarations

```elm
-- Type annotation on its own line, function body indented 4 spaces
greet : String -> String
greet name =
    "Hello, " ++ name


-- Two blank lines between top-level declarations
add : Int -> Int -> Int
add a b =
    a + b
```

### Let-In (always multiline)

```elm
main =
    let
        a = 10
        b = 20
    in
    println (a + b)
```

`let` and `in` are aligned; bindings indented 4 spaces under `let`; body indented 4 spaces under `in`.

### Case Expressions

```elm
case msg of
    Navigate page ->
        ( { model | page = page }, Cmd.none )

    Increment ->
        ( { model | count = model.count + 1 }, Cmd.none )
```

Branches indented 4 spaces under `case`; branch body indented 4 spaces under the arrow. Blank line between branches.

### If-Then-Else

```elm
if condition then
    trueValue
else
    falseValue
```

### ADT Variants (leading pipe)

```elm
type Shape
    = Circle Float
    | Rectangle Float Float
```

First variant prefixed with `= `, subsequent with `| `, all indented 4 spaces.

### Records & Lists (leading comma when multi-line)

```elm
-- Short form stays on one line
{ name = "Alice", age = 30 }
[ 1, 2, 3 ]

-- Multi-line uses leading commas
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
{ model | count = model.count + 1 }
```

### Pipeline Operators (always break to new lines)

```elm
items
    |> List.map (\x -> x * 2)
    |> List.filter (\x -> x > 3)
```

### Module Header & Imports

```elm
module Main exposing (main)


import Std.Log exposing (println)
import Sky.Core.String as String
```

Two blank lines between module declaration and imports. One blank line after imports before declarations.

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

result = Decode.decodeString userDecoder jsonString
```
