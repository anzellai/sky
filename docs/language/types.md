# Types

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the Sky compiler (the earlier compilers were removed in v0.29.0).
> Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../history/compiler/versions.md`](../history/compiler/versions.md) for the changelog.


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
- `let` bindings generalise too, under ML's value restriction. A let-bound function, lambda or syntactic value (`[]`, a literal, a constructor applied to values) is polymorphic: `twice x = ( x, x )` can be used at `String` and at `Int` in the same body. An application (`r = Task.run t`) stays monomorphic. Type variables the binding shares with the enclosing scope (an outer parameter it captures) are never generalised.
- Type variables in annotations are distinct: `f : a -> b -> a` gets fresh TVars for `a` and `b`.
- Every type variable in an annotation is rigid in its definition (since v0.29.2, for argument-only variables too): it stands for every type a caller may choose, so the body must work for all of them. The body may keep it polymorphic (`apply : (a -> b) -> a -> b`, `getName : { r | name : String } -> String`), but a body that uses it as a type, or makes two variables one type, is `[E2001]`, with the type to write. `show : a -> String` with `show v = "<" ++ v ++ ">"` says "the type annotation says `a` can be any type, but this code uses it as `String` … Fix: write `String` instead of `a`", and `coerce : a -> b` with `coerce x = x` says `a` and `b` are used as one type. A bound the body needs and the name does not spell asks for `comparable`, `number` or `appendable` (`largest : a -> a -> a` with `<` is refused; `comparable -> comparable -> comparable` is not). Before v0.29.2 a variable that appeared only in argument position was flexible, so `coerce "hello" + 1` compiled and crashed at run time.
- A ROW variable is rigid the same way (v0.29.2): `{ r | name : String }` stands for whatever other fields a caller's record has. The body may read `.name`, update it (`{ rec | name = … }`) and return the record, but it may not read a field the annotation does not list (`rec.age`), return a record with only the listed fields where `{ r | … }` is promised (that would lose the caller's other fields), pass the record to a function that takes a closed record, or make two row variables one (`f : { r | name : String } -> { s | name : String }` with `f rec = rec`). Each is `[E2001]` naming the row variable and the fix. Before v0.29.2 each of these compiled and then failed at run time with a `CoerceFailure`.
- Arithmetic needs a number, as in Elm: `+`, `-`, `*`, `negate` and `abs` take an `Int` or a `Float` (`number -> number -> number`), so `f : a -> a` with `f x = x + x` asks for the `number` bound, and an unannotated `add a b = a + b` is `number -> number -> number`: `add 1.5 2.0` checks and `add "s" "t"` is refused (since v0.29.2; before, both compiled and the String call crashed at run time).
- An integer literal stands for a `number` variable (since v0.29.2): `isPositive : number -> Bool` with `isPositive x = x > 0`, and `double : number -> number` with `double x = x * 2`, check and give the right answer for a `Float` (`double 2.5` is `5.0`). Everywhere else a literal is an `Int`: a `Float` slot still needs `1.0`, and `1 + 2.5` is refused.
- Each numeric instantiation selects its representation before the body runs.
  For `delta : number -> number` with `delta _ = (9007199254740993 + 1) - 9007199254740993`,
  an `Int` call returns `1`, while a `Float` call returns `0.0` because the
  literals and operations use Float precision. This also holds through local
  functions, containers, partial application and recursion. A numeric top-level
  value is memoised separately for each numeric instantiation.
- An unannotated recursive function uses one type within its own definition.
  Its finished type may then generalise for separate callers. An explicit
  annotation can support polymorphic recursive calls that each satisfy it.
- Applying one function stage evaluates that stage before returning its next
  function. A partial application evaluates supplied arguments once, when the
  partial value is made. Captured values keep their values when a tail-recursive
  caller advances to its next iteration.
- The fix the error suggests is a type you can write: an unknown part is a fresh variable (`List x1`, not `List _`), and a record shows its fields (`{ r1 | count : x1 }`).
- A `let` binding's annotation is checked too (since v0.27.14), as a top-level one is: `n : Int` above `n = "x"` is `[E2001]`. Its type variables are rigid in the definition, so the body must be as general as the annotation (`f : a -> a` with `f x = x + 1` is refused). A `let` annotation may name a variable of the enclosing annotation (`wrap : a -> List a` with `one : a` in its `let`): it is the same variable (scoped type variables, as in Elm). In the `let` body the binder has the annotated type, generalised under the same value restriction.

## Generics

Polymorphic Sky functions erase their type variables to Go `any`:

```elm
identity : a -> a
identity x = x
```

```go
func Identity(x any) any { return x }
```

The checker proves each instantiation. The lowerer inserts typed adapters at
concrete boundaries. A quantified `number` also passes hidden Int or Float
evidence, so its literals and arithmetic run in the caller's domain.
Unknown function values preserve one application stage per arrow. A direct
call to a known function can use its source parameter group as one Go call.

**Parametric record aliases** (v0.15+) lower to Go-generic structs with one type parameter per HM type variable:

```elm
type alias Cfg msg =
    { onSubmit : msg
    , label : String
    }
```

```go
type Cfg_R[T1 any] struct {
    OnSubmit T1
    Label    string
}
```

Per-instance construction takes the type args: `Cfg_R[Msg]`, `Cfg_R[Int]`. Callback fields keep their typed callee parameter — no `func(any) any` fallback at parametric-record slots. The same-module polymorphic re-instantiation rule lets `f : Cfg msg -> msg` be called with `msg=Int` AND `msg=Bool` in the same module without pinning.

## Type variables with constraints

Intentionally unsupported. Sky's HM is unconstrained; typeclass-style operations are provided implicitly via runtime helpers:

- **Equality** — `==` and `/=` are structural. Two primitives of the same type compare natively; anything else goes through the runtime (`rt.Eq` / `rt.NotEq`), which recurses into records, lists, tuples, `Dict`s and custom types. Two values of a custom type are equal only when they have the **same constructor** and equal fields, at any depth: with `type T = A | B | C Int`, `A == B` is `False`, `C 1 == C 1` is `True` and `C 1 == C 2` is `False`. Works for any value without a constraint. `Set`, `List.member` and `Test.equal` use the same equality. (Before v0.27.7, two different constructors without fields of a type that also had a constructor with fields compared equal.)
- **Ordering** — `<`, `>`, `<=`, `>=`, and `Basics.compare` dispatch through `rt.sky_compare` for primitives and lexicographic ordering on collections.
- **Display / debug** — `Basics.toString` (alias `Debug.toString`) renders any value to a readable string for debugging. For production formatting use the typed helpers (`String.fromInt`, `String.fromFloat`).

There are no `Eq` / `Ord` / `Show` constraints to opt into — every operator just works on every type.
