# Values & types

Sky has the value types you'd expect, and a type checker that never guesses.

## The primitives

```elm
count   = 42            -- Int
price   = 9.99          -- Float
name    = "Ada"         -- String
grade   = 'A'           -- Char
ok      = True          -- Bool
```

Comments start with `--`. That's the whole set of literals you'll use day to day
(plus lists and records, which get their own lessons).

## Type annotations are optional but preferred

Sky infers types, so you rarely *have* to write them. But a top-level annotation
is checked against the definition and doubles as documentation:

```elm
greeting : String
greeting =
    "Hello, Sky!"

double : Int -> Int
double n =
    n * 2
```

If the body doesn't match the annotation, that's a compile error — the annotation
is a contract, not a comment.

## No silent conversions

This is the one that catches newcomers. Sky will **not** turn an `Int` into a
`String` for you:

```elm
-- ✗ won't compile — you can't ++ an Int onto a String
-- message = "Count: " ++ 42

-- ✓ convert explicitly
message =
    "Count: " ++ String.fromInt 42
```

The same goes for `Int` vs `Float`: `String.fromInt` for whole numbers,
`String.fromFloat` for decimals. This strictness is deliberate — it's a big part
of how "if it compiles, it works" holds up. When a type mismatch would be a bug,
you hear about it at compile time, not at 2 a.m. in production.

## Converting the other way can fail

Turning a `String` into a number might not work, so those functions return a
`Result` (a value that's either success or failure) — you'll meet `Result` in the
[Maybe & Result](07-maybe-and-result.md) lesson:

```elm
-- String.toInt "42"  →  Ok 42
-- String.toInt "oops" →  Err …
```

**[Next → Functions](03-functions.md)**
