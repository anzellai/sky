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

The bindings of one `let` may refer to each other in any order, and local
functions may call each other (mutual recursion):

```elm
parity n =
    let
        isEven k =
            if k == 0 then
                True

            else
                isOdd (k - 1)

        isOdd k =
            if k == 0 then
                False

            else
                isEven (k - 1)
    in
    isEven n
```

A cycle through a plain value (`a = f 1` where `f` reads `a`) is not allowed:
it has no value to start from.

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
each line that begins at column 1 with a token that can begin a top-level
declaration: a lower-case name, `type`, `import` or `module`. Any other
token at column 1 continues the line before: a keyword that only continues a
construct (`then`, `else`, `of`, `in`, `as`, `alias`, `exposing`), a `,`, a
bracket, an operator, a literal or an upper-case name. A definition and its
annotation are one unit.

One shape of such a keyword line also begins a unit: the line reads as a
declaration named by the keyword. That is the keyword, then zero or more
lower-case names or `_` on the same line, then `=` or `:` (`else = 2`,
`of x = 2`, `in : Int`). The grammar never puts `=` or `:` there, so the
line cannot continue anything. It is its own declaration, and it gets its
own error that names the keyword, even after a broken declaration. The one
exception is `as name =`, which can end a `let` destructuring
(`( a, b )` on one line and `as p = ( 1, 2 )` on the next). That line
continues the unit before it when the two parse together past its `=` with
no error. `alias R = Int` after a `type` line also continues, because a
type alias name is upper-case. The parser never reads past the end of a unit, and
it reports at most one syntax error for each unit. After the first error it
keeps parsing, so the editor still sees the whole file, but it reports
nothing more in that unit. Two broken declarations always give two errors,
one in each.

So a line inside a definition that starts at column 1 with a lower-case name
begins a new declaration. When the definition above needs that line, this is
one error:

```text
This line starts at column 1, so it begins a new declaration. Indent it to continue the definition above.
```

Indent the line (`sky fmt` always does). A line at column 1 that starts with
anything else (`42`, `Just x`, `Task.run …`, `[`, `(`, `+`, `then`) stays in
the unit of the definition. The parser reads it as part of the definition
only where the definition still needs more: after `=`, after an operator,
after `then`, `else`, `of`, `->` or `in`, or inside an open bracket. After a
complete expression such a line is an error, as in v0.27.11: `answer =`,
`    1` and then `+ 2` at column 1 gives `expected a declaration`. Indent
it.

A reserved word is named only when the plain parse fails at the word, or at
the token directly after it (the construct the word starts could not even
begin: `g = case` with nothing after it, `if of then …`), or when the token
directly after the word starts a later line at a column less than or equal
to the indentation (first-token column) of the word's own line. That token
belongs to an enclosing block (a new arm, binding or declaration), so the
word's construct could not begin: a bare `if`, `case` or `let` as a `case`
arm body before the next arm, or as a `let` binding's value before the next
binding. When the parse
accepted the word and read more of its construct before it failed, the word
is in its valid place and is never named, even though renaming it would make
the unit parse as a function call: `if y` with no `then` gives
`expected ThenKw`, `case y` with no `of` gives `expected OfKw`, and a `let`
cut off after its first binding keeps the parser's message.

On top of that, a trial must prove the word is the cause. Read as a
fresh name, the word (and its later uses in the unit) must make the whole
unit parse with no error. Or, when one declaration misuses two or more words,
renaming the word must move the first error past it, and a later reserved
word must be eligible on that new parse and qualify by the same rule
(`f type =` with the body `of` names `type`). A chain may start at any
eligible word, before or after the place where the plain parse first fails:
`1 -> if` and then `2 -> then` as two `case` arms names `if`, and so do
`z = if` and then `w = then` as two `let` bindings, and `[ if` and then
`, then` as two list items. Each later word must be eligible on the parse with
the earlier words renamed. A word whose only eligibility is a reserved word
directly after it starts no chain: in `if then 1 else 2` the `if` began
validly and only its condition is missing, so the parser's own message stays.
A keyword in its valid place is never eligible, so a chain never passes
through it: `1 -> if y then a else b` and then `2 -> then` names only `then`.
The earliest word that qualifies is named. This finds the word even where the
parse fails a few tokens later (`x ++ if` fails at the end of the line) or at
a valid keyword (`if if then …` names the second `if`, not `then`). When no
word qualifies, the parser's own message stays, so a keyword in its valid
place is never named: `module Main exposing main` gives `expected LParen`,
and `if x 1 else 2` gives `expected ThenKw`.

A `case` needs at least one branch. `case x of` with no branch is
`expected a case branch`. A `let` needs at least one binding. `let in x` is
`expected a let binding`.

`True` and `False` are literals, not names. Before v0.27.12 the compiler also
reserved `foreign` (a leftover rule with no meaning), so `let foreign = …`
failed with `expected a pattern`; it is an ordinary name now.

## Known limitations

- No anonymous record types in function signatures. Define a `type alias` first.
- No higher-kinded types / type classes / row polymorphism — intentional.
- No custom operators.
- Negative literal arguments need parentheses: `f (-1)` not `f -1` (matches Elm's parser disambiguation).
