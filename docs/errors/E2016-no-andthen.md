# [E2016] NO ANDTHEN

The type of `e` in a bind line `p <- e` has no `andThen` that a bind line can
chain with.

```text
-- NO ANDTHEN ----------------------------------- src/Main.sky:14:11 [E2016]

12 | step x =
13 |     let
14 |         n <- Just x |> Maybe.withDefault 0
   |           ^^

[step] The bind line `n <- …` needs a type with an `andThen`, but the
expression after `<-` is `Int`, which has none. Use `case` or a plain `let`
line. See docs/errors/E2016-no-andthen.md
```

Added in v0.32.0.

## What it covers

A bind line chooses `andThen` from the head type of `e` (see
[E2015](E2015-bind-needs-a-type.md)). The module that declares that type must
expose

```elm
andThen : (a -> T b) -> T a -> T b
```

where `T` is the type with its other arguments (`Task e`, `Result e`,
`Decoder`). The line is refused when:

| Case | Example |
|---|---|
| The type has no named head | an `Int`, a function, a record, a tuple |
| The module has no `andThen` of that shape | `List Int` (`Sky.Core.List` has none) |
| The module declares `andThen` but does not expose it | `module Lib.Box exposing (Box)` |
| The exposed `andThen` has another shape | `andThen : Box a -> (a -> Box b) -> Box b` |
| The bind line is inside that `andThen` itself | a bind line on `Box` in `Box`'s own `andThen` |
| The type has no module that could give an `andThen` | `Cmd`, `Sub` |

A function after `<-` is usually a missing argument (`a <- Task.succeed`).
The message then names the function, its type and how many arguments it
still needs before `<-` can run it.

In the module that declares the type, a bind line uses the module's own
`andThen`, exposed or not, the same one a bare `andThen` names there.

A bind line is not a name lookup: a local definition named `andThen` is never
used.

## How to fix it

A `Cmd` or a `Sub` cannot be chained: chain the Tasks in one bind-line block
and send the result with `Cmd.perform task ToMsg`, send several commands with
`Cmd.batch`, and listen to several subscriptions with `Sub.batch`.

Use `case` for a value with no `andThen`, or a plain `let` line (`n = e`) when
`e` is not a computation to chain. For your own type, add (or expose) an
`andThen` of the shape above. An `andThen` of another shape does not fit a
bind line, and calling it by name does not fit the chain either: use `case`,
or add one of the shape above. Inside `andThen` itself, write the body with `case`.
