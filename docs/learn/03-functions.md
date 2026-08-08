# Functions

There's no `function`, `def`, or `func` keyword in Sky. A function is just a name,
its arguments, an `=`, and a body.

```elm
add : Int -> Int -> Int
add x y =
    x + y
```

The annotation reads "takes an `Int` and an `Int`, returns an `Int`". The arrows
between arguments will make more sense in a second.

## Calling a function

No parentheses around the argument list, no commas — just juxtaposition:

```elm
sum =
    add 2 3        -- 5
```

Parentheses are only for grouping: `add 2 (add 3 4)`.

## Partial application

Every function is curried, which is a fancy way of saying you can hand it fewer
arguments than it wants and get back a function waiting for the rest:

```elm
addTen : Int -> Int
addTen =
    add 10          -- add, with x pinned to 10

result =
    addTen 5        -- 15
```

That's why the type is `Int -> Int -> Int`: applying one argument to
`Int -> Int -> Int` leaves `Int -> Int`.

## Lambdas

An anonymous function is `\args -> body`. Reach for one when a function is small
and only used once — most often as an argument to `List.map` and friends:

```elm
doubled =
    List.map (\x -> x * 2) [ 1, 2, 3 ]     -- [ 2, 4, 6 ]
```

## Locals with `let … in`

Name intermediate results with `let … in`. This is also where Sky puts what other
languages call `where` — Sky has no `where` clause.

```elm
areaOfCircle : Float -> Float
areaOfCircle r =
    let
        pi = 3.14159
        rSquared = r * r
    in
    pi * rSquared
```

Everything bound in a `let` is in scope in the `in` body (and in the other
bindings — they can even refer to each other).

**[Next → Records](04-records.md)**
