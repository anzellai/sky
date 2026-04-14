# Pattern matching

Pattern matching is exhaustive — missing variants cause build errors, not runtime panics.

## Case expressions

```elm
describe : Shape -> String
describe shape =
    case shape of
        Circle radius ->
            "circle of radius " ++ String.fromFloat radius

        Rect w h ->
            "rect " ++ String.fromFloat w ++ "x" ++ String.fromFloat h

        Polygon points ->
            "polygon with " ++ String.fromInt (List.length points) ++ " vertices"
```

## Pattern types

| Pattern | Example |
|---------|---------|
| Literal | `0`, `"hello"`, `True` |
| Variable | `x` (binds anything) |
| Wildcard | `_` (binds nothing) |
| Constructor | `Just x`, `Ok value`, `Nothing` |
| Tuple | `( a, b )`, `( _, y, _ )` |
| Record | `{ name, age }` (destructure) |
| Cons | `head :: tail` |
| List | `[]`, `[ x ]`, `[ x, y, z ]` |
| As pattern | not supported |

## Destructuring records

```elm
greet : User -> String
greet { name, age } =
    name ++ " (" ++ String.fromInt age ++ ")"
```

Records can be destructured in function parameters as well as case branches.

## Nested patterns

```elm
result =
    case maybeResult of
        Just (Ok value) ->
            value

        Just (Err _) ->
            fallback

        Nothing ->
            fallback
```

Supported arbitrarily deep — `caseDepth` in the lowerer generates unique `__subject_N` temporaries per level.

## Exhaustiveness

`Sky.Type.Exhaustiveness` runs after type-check. It reports:

- Missing ADT constructors in case expressions.
- Missing `True` / `False` in boolean matches.
- Literal-only patterns (e.g. matching `0`, `1`, `2` without a wildcard).

These are build errors. If you genuinely want to panic on an unmatched case, pattern-match a wildcard:

```elm
    case n of
        0 -> "zero"
        _ -> "anything else"
```

## Guards

Not supported — use `if` inside the branch:

```elm
describe : Int -> String
describe n =
    case n of
        0 ->
            "zero"

        _ ->
            if n > 0 then
                "positive"
            else
                "negative"
```

## Match expressions and codegen

A case on an ADT subject lowers to a chain of tag checks + typed field access:

```go
func() any {
    __subject := subjectExpr
    if __subject.Tag == 0 {
        radius := __subject.V0
        return /* Circle body */
    }
    if __subject.Tag == 1 {
        w := __subject.V0; h := __subject.V1
        return /* Rect body */
    }
    panic("sky: internal — codegen reached unreachable case arm (compiler bug)")
}()
```

The `panic` is unreachable when exhaustiveness checking is enabled — it's a compiler-bug trap, not a runtime hazard.
