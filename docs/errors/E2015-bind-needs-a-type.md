# [E2015] BIND NEEDS A TYPE

A bind line `p <- e` in a `let` block chooses its `andThen` from the type of
`e`, and the checker could not find that type.

```text
-- BIND NEEDS A TYPE ---------------------------- src/Main.sky:13:11 [E2015]

11 | step t =
12 |     let
13 |         n <- t
   |           ^^

[step] The bind line `n <- …` chooses `andThen` from the type of the expression
after `<-`, and that type is still `a`. Annotate it, for example
`fetch : String -> Task Error User`. See docs/errors/E2015-bind-needs-a-type.md
```

Added in v0.32.0.

## What it covers

A bind line is a line of a `let` block. The rest of the block is the
continuation:

```elm
let
    n <- t
in
Task.succeed (n + 1)
```

is `t |> M.andThen (\n -> Task.succeed (n + 1))`, where `M` is the module that
declares the type of `t`: `Sky.Core.Task` for a `Task`, `Sky.Core.Maybe` for a
`Maybe`, `Sky.Core.Result` for a `Result`, `Sky.Core.Json.Decode` for the JSON
`Decoder`, and the declaring module of any other type (its exposed `andThen`).

The checker picks `M` once the type of `e` has a known head. It tries when it
meets the line, and again at the end of the definition, so a later use can fix
the type. When the type is still a variable at the end, the checker cannot
choose, and the line is refused. It never guesses.

A name in `e` that does not exist (`b <- fetchh a`, a misspelling) is
`[E1001]` Undefined name, and the line gives no `[E2015]`: fix the name first.

## How to fix it

Give `e` a type. The usual place is the annotation of the definition:

```elm
step : Task Error Int -> Task Error Int
step t =
    let
        n <- t
    in
    Task.succeed (n + 1)
```

Or annotate the function that `e` calls (`fetch : String -> Task Error User`),
or a `let` line that names `e`.

## A type variable of the annotation

When the type of `e` is a type variable of the annotation, an annotation of
`e` does not help: the variable can be any type, and any type has no
`andThen`. The message names the variable and a type that would do:

```text
[work] The bind line `a <- …` chooses `andThen` from the type of the
expression after `<-`. `f x` has the type variable `m` from the annotation,
which can be any type; a bind line needs a type with an `andThen` such as
`Result Error m`.
```

Change the annotation so that `e` has such a type
(`work : (Int -> Result Error m) -> Int -> Result Error m`).
