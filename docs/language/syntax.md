# Language syntax

Sky is Elm-compatible at the surface: if an expression parses in `elm-format`, it parses in Sky.

## A module

```elm
module Lib.Counter exposing (Counter, init, increment)

import Sky.Core.Prelude exposing (..)


type alias Counter =
    { value : Int
    , step : Int
    }


init : Counter
init =
    { value = 0, step = 1 }


increment : Counter -> Counter
increment c =
    { c | value = c.value + c.step }
```

## Types

- Primitives: `Int`, `Float`, `String`, `Bool`, `Char`, `Bytes`.
- Records: `{ name : String, age : Int }` (must be aliased; inline record types in annotations are not supported).
- Tuples: `( Int, String )` — arities 2-5 are typed structs; larger are `SkyTupleN`.
- Lists: `List a`.
- Dicts: `Dict k v` (runtime `map[string]any`; see [types.md](types.md) for caveats).
- ADTs: `type Shape = Circle Float | Rect Float Float`.
- Functions: `Int -> Int -> Int` (right-associative).
- Type variables: `a`, `b`, `c` — lowercase identifiers are HM polymorphic.

See [types.md](types.md) for the full type story.

## Functions

```elm
-- Top-level function with annotation
add : Int -> Int -> Int
add x y =
    x + y

-- Anonymous function (lambda)
doubler =
    \x -> x * 2

-- Partial application
addFive =
    add 5
```

## Let / in

```elm
area radius =
    let
        pi = 3.14159
        square x = x * x
    in
        pi * square radius
```

## Case / of

```elm
describe n =
    case n of
        0 ->
            "zero"

        _ ->
            if n > 0 then
                "positive"
            else
                "negative"
```

Pattern matching is exhaustive — missing ADT variants or missing `True`/`False` in boolean matches are compile errors. See [pattern-matching.md](pattern-matching.md).

## Pipelines

```elm
result =
    input
        |> String.trim
        |> String.toLower
        |> String.split ","
        |> List.map String.trim
        |> List.filter (not << String.isEmpty)
```

`|>` is left-to-right function application. `<|` is the reverse.

## Record update

```elm
updated =
    { user | email = "new@example.com", age = user.age + 1 }
```

## Multiline strings

```elm
html =
    """<div class="card">
    <h1>{{title}}</h1>
    <p>{{description}}</p>
</div>"""
```

- Preserves newlines and indentation.
- `{{expr}}` for interpolation (identifier, qualified name, field access, or function call).
- Single `{` is literal — safe for CSS, JSON, SQL, JS.

## Operators

| Operator | Meaning |
|----------|---------|
| `|>` `<\|` | Pipeline |
| `::` | Cons onto list |
| `++` | Concatenate string / list |
| `<<` `>>` | Function composition |
| `+` `-` `*` `/` `//` | Numeric — `//` is integer division |
| `==` `/=` `<` `>` `<=` `>=` | Comparison |
| `&&` `\|\|` | Boolean |

No custom operators — language constraint.

## Comments

```elm
-- line comment

{-
    block comment
    can span lines
-}
```

## Reserved words

`module`, `exposing`, `import`, `as`, `type`, `alias`, `if`, `then`, `else`, `case`, `of`, `let`, `in`.

Non-reserved identifiers frequently used in Sky but which are *not* keywords: `from`, `where`.

## Known limitations

- No anonymous record types in function signatures. Define a `type alias` first.
- No higher-kinded types / type classes / row polymorphism — intentional.
- No custom operators.
- Negative literal arguments need parentheses: `f (-1)` not `f -1` (Elm behaviour).
