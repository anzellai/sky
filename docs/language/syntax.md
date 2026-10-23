# Language syntax

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../history/compiler/versions.md`](../history/compiler/versions.md) for the changelog.


Sky's surface syntax is Elm-compatible: most expressions that parse in Elm also parse in Sky. (See [NOTICE.md](../../NOTICE.md) for prior-art attribution; programming-language syntax is not itself copyrightable.)

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
- Records: `{ name : String, age : Int }`. The parser accepts inline record types in annotations (`f : { name : String, age : Int } -> String`), though sharing a record shape across functions still reads better as a named `type alias`.
- Tuples: `( Int, String )` — 2-tuples emit `rt.SkyTuple2`, 3-tuples emit `rt.SkyTuple3`, 4+ emit the slice-backed `rt.SkyTupleN`.
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

A let-bound function is polymorphic, like a top-level one: `square` above
could be used at `Int` and at `Float` in the same body. See
[types.md](types.md) ("Type annotations") for the value restriction.

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

## Field access

`record.field` reads a field, and chains: `model.user.name`. On a value from
another module, `Module.value.field` reads the field of the qualified value:
`Shape.origin.x` is the field `x` of `Shape.origin`. A qualified path ends at
its first lower-case segment; the rest is field access.

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
- Backslash escape: `\{{` emits a literal `{{` (no interpolation). Useful for
  shipping Mustache / Handlebars / shell-script placeholders verbatim:

  ```elm
  template =
      """Hello \{{NAME}}, welcome to {{appName}}!"""
  -- → "Hello {{NAME}}, welcome to MyApp!"
  ```

  `\\` collapses to a single literal backslash. Other `\X` sequences are
  preserved verbatim (so regex / paths like `\d+` / `\test` are unaffected).

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

No custom operators — language constraint. Any other operator is a compile
error (`[E1014] UNKNOWN OPERATOR`): "not equal" is `/=`, never `!=`.

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

Non-reserved identifiers frequently used in Sky but which are *not* keywords: `from`, `where`, `port`, `foreign`.

A reserved word where a name is expected (a binding, a parameter, a field, a
value, a type variable, a type name, an import alias) is one parse error that
names the word, with no follow-on errors:

```text
`type` is a reserved word and cannot be used as a name. Pick another name. The reserved words are: module, exposing, import, as, type, alias, if, then, else, case, of, let, in.
```

The file is cut into top-level units by its tokens alone. A unit starts at
each line that begins at column 1, except a line that begins with a `,`, a
closing bracket or an operator, which continues the line before. A definition
and its annotation are one unit. So a declaration body must be indented past
column 1. The parser never reads past the end of a unit, and it reports at
most one syntax error for each unit. After the first error it keeps parsing,
so the editor still sees the whole file, but it reports nothing more in that
unit. Two broken declarations always give two errors, one in each.

A reserved word is named only when a trial proves it is the cause: read as a
fresh name, the word (and its later uses in the unit) must make the whole
unit parse with no error. The earliest such word is named. This finds the
word even where the parse fails a few tokens later (`x ++ if` fails at the end
of the line) or at a valid keyword (`if if then …` names the second `if`, not
`then`). When no trial parses clean, the parser's own message stays, so a
keyword in its valid place is never named: `module Main exposing main` gives
`expected LParen`, and `if x 1 else 2` gives `expected ThenKw`.

A `case` needs at least one branch. `case x of` with no branch is
`expected a case branch`.

`True` and `False` are literals, not names. Before v0.27.12 the compiler also
reserved `foreign` (a leftover rule with no meaning), so `let foreign = …`
failed with `expected a pattern`; it is an ordinary name now.

## Known limitations

- No anonymous record types in function signatures. Define a `type alias` first.
- No higher-kinded types / type classes / row polymorphism — intentional.
- No custom operators.
- Negative literal arguments need parentheses: `f (-1)` not `f -1` (matches Elm's parser disambiguation).
