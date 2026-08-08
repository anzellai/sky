# Coming from another language

Sky's surface is **Elm**: whitespace-significant, expression-based, no `return`,
no loops, no null. If you've written JavaScript, Python, Go, or Rust, here are
the shifts that matter — and the Sky idiom for each.

## The five big shifts

| From imperative languages | In Sky |
|---|---|
| Statements, `return`, loops | **Everything is an expression.** `if`/`case`/`let` return values. Iterate with `List.map`/`foldl`, not `for`. |
| `null` / `nil` / `None` sprinkled everywhere | **No null.** Absence is `Maybe a` (`Just x` / `Nothing`); you must handle both. |
| `try/catch`, exceptions | **Errors are values.** Fallible code returns `Result Error a`; side effects return `Task Error a`. No hidden throws. |
| Mutable variables | **Immutable bindings.** `{ user | age = 41 }` makes a *new* record; the old one is untouched. |
| Classes / methods / inheritance | **Records + functions + tagged unions.** `type Shape = Circle Float \| Rect Float Float`, then `case shape of …`. |

## Side by side

**A function.** No `function`/`def`/`func` keyword — just `name args = body`. The
type annotation above it is optional but preferred (it's checked).

```elm
greet : String -> String
greet name =
    "Hello, " ++ name
```

**No null — use `Maybe`.** Where JS returns `undefined` or Python `None`, Sky
returns `Maybe` and the compiler makes you handle the empty case:

```elm
case List.head users of
    Just first -> first.name
    Nothing    -> "no users"
```

**No exceptions — use `Result` / `Task`.** `String.toInt` can't throw; it returns
`Result Error Int`. Anything touching the outside world (files, HTTP, DB, time)
returns `Task Error a`:

```elm
-- pure:          List.map, String.length, Crypto.sha256
-- can fail:      String.toInt : String -> Result Error Int
-- side effect:   Http.get, Db.query, File.read : … -> Task Error a
```

**Loops become folds.** There is no `for`. Build and transform with list
functions:

```elm
total =
    List.foldl (\item acc -> acc + item.price) 0 cart
```

**Pattern match instead of `switch`/`if-else` chains.** `case` is
exhaustiveness-checked — forget a variant and it won't compile:

```elm
describe : Shape -> String
describe shape =
    case shape of
        Circle r    -> "circle r=" ++ String.fromInt r
        Rect w h    -> "rect " ++ String.fromInt w ++ "x" ++ String.fromInt h
```

## Notes per language

- **From JavaScript/TypeScript:** think Elm/ReasonML. `|>` is like a pipe;
  records are structural like TS interfaces but immutable. No `async/await` — the
  runtime runs your `Task` at the entry point (`main`, a request handler, or
  `Cmd.perform`).
- **From Python:** significant whitespace will feel familiar; `let … in` replaces
  local assignments; comprehensions become `List.map`/`List.filter`.
- **From Go:** you already know the deployment story — Sky *compiles to Go* and
  ships one static binary. The difference is upstream: sum types + exhaustive
  matching + `Result`/`Task` instead of `if err != nil`.
- **From Rust:** Hindley–Milner inference (no lifetimes, no borrow checker),
  `Result`/`Maybe` you already know, but no traits/generics beyond parametric
  polymorphism — it's a smaller, simpler type system aimed at DX.

## What trips people up (and the fix)

- **Inline records in signatures aren't allowed** — give any record a
  `type alias` and use the name.
- **No `where`** — use `let … in`.
- **A top-level zero-arg binding is memoised** (computed once). Great for a shared
  DB pool (`db = Task.run (Db.connect ())`); wrong for a fresh value per call —
  make those a function (`newId _ = …`; call `newId ()`).

Next: **[your first app](01-first-app.md)**, then a real
**[web app with Sky.Live](11-first-web-app.md)**.
