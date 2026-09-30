# [E1016] RECURSIVE TYPE ALIAS

A `type alias` refers to itself, directly or through other aliases of the same
module.

```text
-- RECURSIVE TYPE ALIAS ---------------------------- src/Tree.sky:3:12 [E1016]

3 | type alias Node =
  |            ^^^^ this type alias refers to itself

The type alias `Node` refers to itself. A type alias is only another name for
its body, so a recursive one never ends. Make it a custom type instead, which
gives the recursion a name to stop at: `type Node = Node { … }`, and wrap and
unwrap the record with the `Node` constructor. See
docs/migration/v0.27.md#recursive-type-alias
```

Added in v0.27.0.

## What it covers

| You wrote | It is an error because |
|---|---|
| `type alias Node = { value : Int, next : Maybe Node }` | `Node` names itself |
| `type alias A = { b : List B }` and `type alias B = { a : A }` | `A -> B -> A` is a cycle (the message names the path) |

A cycle cannot cross modules: that would need an import cycle, which `[E1010]`
already refuses.

## Why it is an error

An alias is expanded wherever it is used, never named. A recursive alias has no
finite expansion. Before v0.27.0 the checker either printed a confusing
`record vs Node` type mismatch or accepted the declaration, and the Go it
emitted was an invalid recursive type that `go build` refused. `sky check`
passed and `sky build` failed, which breaks "if it compiles, it works". Elm
refuses the same declaration for the same reason.

When a module has an `[E1016]`, the type checker does not run on it, so this is
the only error you see for that module until you fix it.

## How to fix it

Make the alias a custom type with one constructor. The constructor is the name
the recursion stops at:

```elm
-- before
type alias Node =
    { value : Int, next : Maybe Node }


-- after
type Node
    = Node { value : Int, next : Maybe Node }


value : Node -> Int
value (Node n) =
    n.value
```

Build a value with the constructor (`Node { value = 1, next = Nothing }`) and
read it by matching on it, as `value` does above.
