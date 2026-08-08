# Unions & case

Records say "this **and** that". Tagged unions say "this **or** that" — and
they're where Sky's type system really earns its keep.

## Defining a union

```elm
type Shape
    = Circle Float
    | Rect Float Float
    | Dot
```

`Shape` is a type with three **variants**. `Circle` carries one `Float` (a
radius), `Rect` carries two (width and height), and `Dot` carries nothing. The
variant names are also the constructors:

```elm
a = Circle 5.0
b = Rect 3.0 4.0
c = Dot
```

## Taking them apart with `case`

To use a union value you pattern-match on it with `case … of`, binding the payload
to names:

```elm
area : Shape -> Float
area shape =
    case shape of
        Circle r ->
            3.14159 * r * r

        Rect w h ->
            w * h

        Dot ->
            0.0
```

## Exhaustiveness is checked

Here's the payoff. If you forget a variant, the program **does not compile**:

```elm
-- ✗ compile error: missing the Dot case
-- area shape =
--     case shape of
--         Circle r -> 3.14159 * r * r
--         Rect w h -> w * h
```

Add a variant to `Shape` a year from now, and the compiler walks you to every
`case` that needs updating. No silent fall-through, no forgotten branch.

## Patterns nest

Patterns can go deeper than one level and use `_` to ignore what you don't need:

```elm
describe : Maybe Shape -> String
describe maybeShape =
    case maybeShape of
        Just (Circle _) -> "a circle"
        Just _          -> "some shape"
        Nothing         -> "nothing"
```

(`Maybe` is the next lesson — it's just a union that means "a value, or none".)

**[Next → Lists](06-lists.md)**
