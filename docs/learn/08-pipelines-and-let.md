# Pipelines & let

Two small tools do a lot for readability: the pipe operator and `let … in`.

## The pipe `|>`

`x |> f` is just `f x`. On its own that's not much, but chained it turns
inside-out nesting into a top-to-bottom recipe:

```elm
-- without pipes — read it right-to-left, inside-out
result1 =
    String.toUpper (String.trim (String.toLower name))

-- with pipes — read it top-to-bottom
result2 =
    name
        |> String.toLower
        |> String.trim
        |> String.toUpper
```

Both compute the same thing. The piped version reads like the steps happen in
order, because they do: the value on the left flows into each function in turn.

There's also `<|` (apply to the right), handy for dropping a pair of parentheses:

```elm
println <| String.fromInt (1 + 2)
-- same as: println (String.fromInt (1 + 2))
```

## `let … in` for naming steps

When a computation has intermediate values worth naming, `let … in` makes it
legible:

```elm
priceWithTax : Float -> Float
priceWithTax base =
    let
        taxRate = 0.2
        tax = base * taxRate
    in
    base + tax
```

Bindings in a `let` can refer to each other regardless of order — Sky sorts out
the dependencies — so you can write them in whatever order reads best:

```elm
let
    total = subtotal + shipping
    subtotal = 100
    shipping = 5
in
total          -- 105
```

Pipelines and `let` compose: a common shape is a `let` that names a couple of
inputs, then a piped expression that transforms them. You'll see this everywhere
in real Sky code.

**[Next → Effects & Task](09-effects-and-task.md)**
