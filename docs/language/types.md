# Types

Sky's type system is Hindley-Milner with algebraic data types, records, and concrete Go interop types. There are no type classes, no higher-kinded types, no row polymorphism.

## Primitives

| Sky | Go |
|-----|----|
| `Int` | `int` |
| `Float` | `float64` |
| `String` | `string` |
| `Bool` | `bool` |
| `Char` | `rune` (int32) |
| `Bytes` | `[]byte` |

## Type aliases

```elm
type alias Point =
    { x : Int
    , y : Int
    }

type alias UserId = String
type alias Tags = List String
```

Every record type alias auto-generates a positional constructor:

```elm
origin : Point
origin =
    Point 0 0     -- constructor args in field-declaration order
```

## Algebraic data types

```elm
type Shape
    = Circle Float
    | Rect Float Float
    | Polygon (List Point)


area : Shape -> Float
area shape =
    case shape of
        Circle r ->
            3.14159 * r * r

        Rect w h ->
            w * h

        Polygon points ->
            -- exhaustiveness-checked at compile time
            polygonArea points
```

Pattern matches are exhaustive — missing variants are build errors.

## Tuples

Fixed-arity product types. Arities 2-5 compile to typed Go structs (`rt.T2[A, B]`, `rt.T3[A, B, C]`, ...). Larger tuples fall back to `SkyTupleN`.

```elm
pair : ( Int, String )
pair =
    ( 42, "answer" )
```

## Lists & dicts

```elm
numbers : List Int
numbers =
    [ 1, 2, 3 ]

usersByEmail : Dict String User
usersByEmail =
    Dict.empty
        |> Dict.insert "alice@example.com" alice
```

> `Dict` is `map[string]any` at runtime. Non-`String` keys are stringified. Arithmetic on `Dict Int v` keys returned by `Dict.toList` silently produces strings — iterate via `Dict.get` over known key ranges instead.

## Maybe & Result

```elm
Maybe a
    = Just a
    | Nothing

Result e a
    = Ok a
    | Err e
```

Use `Maybe` for optional values, `Result` for fallible pure computations. Both are generic in their payload type.

In v1+, every public fallible surface uses `Result Error a` (not `Result String a`). See [../errors/error-system.md](../errors/error-system.md).

## Task

`Task e a` is the Sky effect type. Every effectful operation — file I/O, HTTP, DB, println — returns `Task Error a`. Run one with `Task.perform`.

```elm
readConfig : Task Error String
readConfig =
    File.readFile "./config.json"
        |> Task.onError (\_ -> Task.succeed "{}")
```

## Type annotations

Annotations are load-bearing:

- If a function is annotated, the annotation *is* the scheme used by callers. The body is checked against it, not just inferred and cross-referenced.
- Missing annotations fall back to inferred types (full HM, including generalisation).
- Type variables in annotations are distinct: `f : a -> b -> a` gets fresh TVars for `a` and `b`.

## Generics

Polymorphic HM-inferred functions lower to Go generics:

```elm
identity : a -> a
identity x = x
```

```go
func Identity[T1 any](x T1) T1 { return x }
```

`solvedTypeToGo TVar` falls back to `any` at expression positions (Go's type parameters can't appear outside enclosing function signatures). This is by design, not an escape hatch.

## Type variables with constraints

Intentionally unsupported. Sky's HM is unconstrained; typeclass-style operations (`Eq`, `Ord`, `Show`) are provided as concrete module-level functions instead (`Basics.eqT`, `Basics.ordT`, `Debug.toString`).
