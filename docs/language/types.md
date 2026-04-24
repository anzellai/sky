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

Fixed-arity product types. Arity 2 emits `rt.SkyTuple2` (`{V0, V1}`); arity 3 emits `rt.SkyTuple3` (`{V0, V1, V2}`); arity 4+ falls back to the slice-backed `rt.SkyTupleN`. The mapping is performed in `Sky.Generate.Go.Type.typeToGo` and is invisible at the source level — destructuring patterns work the same way at every arity.

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

Since v0.9, every public fallible surface uses `Result Error a` (not `Result String a`). See [../errors/error-system.md](../errors/error-system.md).

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

Intentionally unsupported. Sky's HM is unconstrained; typeclass-style operations are provided implicitly via runtime helpers:

- **Equality** — the `==` and `/=` operators dispatch through `rt.sky_equal`, which type-switches on the runtime tag and recurses into ADTs / records / lists / dicts. Works for any value without a constraint.
- **Ordering** — `<`, `>`, `<=`, `>=`, and `Basics.compare` dispatch through `rt.sky_compare` for primitives and lexicographic ordering on collections.
- **Display / debug** — `Basics.toString` (alias `Debug.toString`) renders any value to a readable string for debugging. For production formatting use the typed helpers (`String.fromInt`, `String.fromFloat`).

There are no `Eq` / `Ord` / `Show` constraints to opt into — every operator just works on every type.
