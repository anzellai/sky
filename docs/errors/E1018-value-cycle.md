# [E1018] BAD RECURSION

A value is defined in terms of itself, directly or through other values.

```text
-- BAD RECURSION ------------------------------------------ src/Main.sky:7:1 [E1018]

6 | a : Int
7 | a =
  | ^

The value `a` is defined in terms of itself through a -> b -> a. A value is
computed once, the first time it is read, so a cycle of values never finishes.
Break the cycle: compute one of them from other values, or give one a parameter
(`a _ = …`, called as `a ()`) when it really is a loop.
```

Added in v0.29.0.

## What it covers

A *value* is a definition with no parameters whose body is not a lambda
(`total = price * 2`). A *function* has parameters (`double n = n * 2`) or is a
lambda (`double = \n -> n * 2`).

| You wrote | It is an error because |
|---|---|
| `xs = 1 :: xs` | `xs` reads itself while it is computed |
| `a = b + 1` and `b = a * 2` | `a -> b -> a` is a cycle of values |
| `a = f 1` and `f x = a + x` | computing `a` calls `f`, and `f` reads `a` |
| the same shapes between the bindings of one `let` | the same cycles, local |

These are accepted, because nothing is read before it is computed:

| You wrote | Why it is fine |
|---|---|
| `count n = … count (n - 1)` | a function that calls itself is ordinary recursion |
| `isEven` and `isOdd` calling each other | functions that call each other, too |
| `loop = \n -> … loop (n - 1)` | the reference is inside a lambda, read only when it is called |
| `handlers = { run = f }` and `f x = handlers.run x` | `f` is named, not called, while `handlers` is computed |
| `a = g 1` with `g x y = a x y` | a partial application runs nothing |

A cycle cannot cross modules: that would need an import cycle, which `[E1010]`
already refuses.

## Why it is an error

A top-level value is computed once, the first time it is read, and kept. A value
that reads itself while it is being computed waits for itself. Before v0.29.0
such a program passed `sky check`: a top-level cycle deadlocked on its first
read at run time, and a cycle inside a `let` failed `go build`. Elm refuses the
same programs, with the same name.

## How to fix it

Decide what the value means. A list that goes on for ever, a counter that needs
its own last value, or two settings that refer to each other usually have a
base case that was left out:

```elm
-- before
ones : List Int
ones =
    1 :: ones


-- after: say how long it is
ones : Int -> List Int
ones n =
    List.repeat n 1
```

When one value really is a loop, give it a parameter. A function runs each time
it is called, so it may call itself:

```elm
-- before
a : Int
a =
    b + 1


b : Int
b =
    a * 2


-- after: compute one from the other only
a : Int
a =
    b + 1


b : Int
b =
    2
```
