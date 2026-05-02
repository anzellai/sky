# JSON, Result Combinators & Auto Record Constructors

JSON encoding and decoding using `Sky.Core.Json.Encode` and `Sky.Core.Json.Decode` (combinator-style decoders, same API shape as Elm's `Json.Decode`). Also demonstrates `Sky.Core.Result` applicative combinators (`map2`/`map3`, `combine`, `traverse`) added in v0.7.25, and **auto-generated record constructors** from type aliases (v0.7.26+).

## Build & Run

```bash
sky build src/Main.sky
./sky-out/app
```

## What it shows

1. **Simple encoding** — `Encode.object`, `Encode.string`, `Encode.int`, `Encode.bool`
2. **Complex encoding** — nested objects, lists
3. **Decoder `map2`** — combine two fields with a function
4. **Pipeline decoding** — `|> Pipeline.required` for record fields
5. **Optional fields** — `Pipeline.optional` with defaults
6. **Nested field access** — `Decode.at [...]`
7. **List of objects** — `Decode.list`
8. **Roundtrip** — encode then decode
9. **`Decode.oneOf`** — handle multiple possible formats
10. **`Result.map3` + auto record constructor** — combine three independent parsers into a `Profile` record using the type alias as a constructor (no `makeProfile` helper needed)
11. **`Result.combine`** — collect a list of Results into a Result of list (homogeneous)
12. **`Result.traverse`** — map a function over a list and collect into one Result

Sections 10–12 demonstrate the **applicative combinators** added in v0.7.25 — useful for form validation, multi-field parsing, and any case where you have several Results to combine without writing nested case expressions.

Section 10 also showcases v0.7.26's **auto record constructors**: every `type alias Foo = { ... }` automatically generates a positional constructor function `Foo : ... -> Foo` (matches the convention Elm uses for the same construct), so you can pass the type alias name directly into `Result.map3` instead of writing a `makeFoo` helper.

<img width="463" height="907" alt="06-json" src="https://github.com/user-attachments/assets/bfb18d80-d23d-4ae4-88a8-e7c22aaf9f15" />
