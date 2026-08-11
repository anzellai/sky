# Maybe & Result

Sky has no `null`, no `nil`, no `None` floating through your code, and no
exceptions. Absence and failure are ordinary values with ordinary types — and the
compiler makes sure you handle them.

## Maybe — a value, or none

```elm
type Maybe a
    = Just a
    | Nothing
```

Anything that might not have an answer returns a `Maybe`. `List.head` on a
possibly-empty list is the classic:

```elm
firstName : List String -> String
firstName names =
    case List.head names of
        Just n  -> n
        Nothing -> "(no one)"
```

You can't accidentally use the "empty" case as if it held a value — there's no
value there, and the `case` forces you to say what happens.

When you just want a fallback, `Maybe.withDefault` collapses it:

```elm
name =
    Maybe.withDefault "(no one)" (List.head names)
```

## Result — success, or an error

`Result Error a` is either `Ok value` or `Err error`. Use it when the failure
carries information — a parse error, a validation failure:

```elm
-- Encoding.base64Decode : String -> Result Error String

decodeName : String -> String
decodeName raw =
    case Encoding.base64Decode raw of
        Ok name -> name
        Err _   -> "anonymous"
```

**The error type is always `Error`, never `String`.** `Result Error a` and
`Task Error a` are the two error-carrying shapes across all of Sky; a bare
`String` message loses structure and is disallowed in public APIs.

## Chaining without a pyramid of cases

`map` transforms the success case; `andThen` sequences steps that can each fail —
the first `Err` short-circuits the rest:

```elm
-- map: apply a function to the Ok value, leave Err alone
decoded =
    Result.map String.toUpper (Encoding.base64Decode "aGk=")   -- Ok "HI"

-- `Maybe` maps the same way: String.toInt is Maybe-valued, so it takes Maybe.map
doubled =
    Maybe.map (\n -> n * 2) (String.toInt "21")      -- Just 42

-- andThen: feed the Ok value into another fallible step
-- (Result.andThen : (a -> Result e b) -> Result e a -> Result e b)
```

`Maybe` has the same `map` / `andThen` / `withDefault`. Between them you rarely
write a nested `case` — you build a pipeline and handle failure once, at the end.

**[Next → Pipelines & let](08-pipelines-and-let.md)**
