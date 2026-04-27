# Go interop

Sky imports any Go package and uses it with full type safety.

> **See also:** [boundary-philosophy.md](boundary-philosophy.md) — why every FFI call returns `Result Error T`, when to reach for FFI vs Sky's stdlib, and the Result-vs-Task distinction.

## The promise

- You never write FFI wrappers by hand.
- Sky generates typed bindings from Go's type information via `tools/sky-ffi-inspect`.
- Every FFI call is wrapped in panic recovery — `ErrFfi(<panic-message>)` comes back as a Sky `Error` if the Go side panics.
- Nil dereferences, type assertions, and other runtime hazards on the Go side become `Result Error a` rejections on the Sky side.

## Adding a package

```bash
sky add github.com/google/uuid
```

This:

1. Runs `go get` inside `sky-out/` to fetch the module.
2. Runs `tools/sky-ffi-inspect` to emit a JSON description of every public function, type, and struct field.
3. Generates `.skycache/ffi/<slug>.{skyi,kernel.json}` and `.skycache/go/<slug>_bindings.go`.
4. Adds the dep to `sky.toml` under `[go.dependencies]`.

`sky install` (or any subsequent `sky build`) regenerates missing bindings idempotently.

## Using a package

```elm
import Github.Com.Google.Uuid as Uuid
import Sky.Core.Result as Result
import Sky.Core.Error as Error
import Std.Log exposing (println)


main =
    -- Every FFI call returns Result Error T — pattern match or
    -- use Result.withDefault to get the value out.
    case Uuid.newString () of
        Ok id ->
            println id

        Err e ->
            println ("uuid failed: " ++ Error.toString e)
```

Zero-arg Go FFI functions are called with `()` in Sky — the inspector emits a `() -> R` Sky signature for every zero-param Go function so the call site stays explicit. (Sky-side **kernel** zero-arity bindings like `Uuid.v4` are different — those are kernel-registered and called as bare values; the `()` rule is only for FFI-generated wrappers.)

Module name mapping:

| Go path | Sky module |
|---------|------------|
| `github.com/google/uuid` | `Github.Com.Google.Uuid` |
| `github.com/stripe/stripe-go/v84` | `Github.Com.Stripe.StripeGo.V84` |
| `net/http` | `Net.Http` |
| `fyne.io/fyne/v2/app` | `Fyne.Io.Fyne.V2.App` |

Hyphens are dropped, next character upper-cased. Non-alphanumerics become `_`.

## Return type mapping

**Every Go FFI call returns `Result Error T`.** This is intentional —
the FFI boundary is a trust boundary — same discipline as a typed-airlock FFI. See
[boundary-philosophy.md](boundary-philosophy.md) for the full reasoning.

The wrapping shape depends on what the Go function returns:

| Go return | Sky type |
|---|---|
| `T` (single, no error) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error T` (opaque; nil-deref panic → Err via recover) |
| `(T, error)` | `Result Error T` |
| `error` | `Result Error ()` |
| `(T, bool)` (comma-ok) | `Result Error (Maybe T)` |
| `(T, *NamedErr)` where `NamedErr` implements `error` | `Result Error T` |
| `(T, U)` (neither error nor bool) | `Result Error (T, U)` |
| `(T, U, error)` | `Result Error (T, U)` |
| `(T, U, V)` | `Result Error (T, U, V)` |
| `[]T` | `Result Error (List T)` |
| `map[string]V` | `Result Error (Dict String V)` |
| `*pkg.Struct` (opaque) | `Result Error Struct` (with generated getters/setters) |
| `interface{}` / `any` | `Result Error any` (boxed) |
| void (no return) | `Result Error ()` |

Element-type mapping (used inside the wrappers above):

| Go | Sky |
|---|---|
| `string` | `String` |
| `int`, `int64`, `int32` | `Int` |
| `float64` | `Float` |
| `bool` | `Bool` |

Notes:

- **`Result Error (Maybe T)`** for `(T, bool)` comma-ok returns
  means you handle two layers: the Result captures boundary failure
  (panic, type mismatch, runtime error); the Maybe captures Go's
  "nothing here" signal (comma-ok false).
- **Bare `*T` returns** are NOT auto-wrapped in `Maybe` because Go
  SDK builder/getter chains (`Firestore.client.Collection(x).Doc(y)`,
  `Stripe.params.SetMode(x).SetCustomer(y)`) rely on chaining
  pointer returns. If a Go function genuinely returns nil, the
  defer-recover catches the downstream nil-deref and surfaces an
  `Err(ErrFfi("nil pointer..."))`. Go authors who mean "nothing
  here" use `(T, error)` or `(T, bool)` — those map cleanly.
- **Named error types** (`*os.PathError`, `*url.Error`,
  `*json.SyntaxError`, etc.) are detected via Go's
  `types.Implements` — they map to `Error` even though the type
  string isn't literally `"error"`.
- **Nil-receiver checks** are added to every method/getter/setter
  wrapper with a pointer receiver. A method call on a nil opaque
  returns `Err(ErrFfi "nil receiver: Type.Method")` instead of
  panicking.

## Opaque struct pattern (Sky's builder convention)

Go structs are opaque — you build them via generated constructors and pipeline setters. **Every step returns `Result Error T`** (constructor, every setter, every getter), so chains use `Result.andThen`:

```elm
params =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetSuccessURL "https://example.com/success")
        |> Result.andThen (Stripe.checkoutSessionParamsSetLineItems [ lineItem ])
```

Naming rules:

- Constructor: `new<TypeName> : () -> Result Error TypeName`
- Getter: `<typeName><FieldName> : TypeName -> Result Error FieldType`
- Setter: `<typeName>Set<FieldName> : FieldType -> TypeName -> Result Error TypeName`

Setters take the value first and the struct second — so they pipe naturally via `|>` + `Result.andThen`. The Result wrap covers the boundary failure modes (nil receiver, panic, type mismatch); a successful chain returns `Ok params`.

Pointer fields are auto-wrapped. For `Mode *string`, you pass a plain `String` and Sky wraps `&v` on the Go side.

## Callbacks (Go function values)

```elm
import Net.Http as Http
import Github.Com.Gorilla.Mux as Mux


handler : Http.ResponseWriter -> Http.Request -> Task Error ()
handler w req =
    Http.writeString w "Hello!"


main =
    let
        router = Mux.newRouter ()
        _ = Mux.routerHandleFunc router "/" handler
    in
        Http.listenAndServe ":8000" router
```

Sky handles the `func(ResponseWriter, *Request)` signature by wrapping the Sky closure in a Go adapter.

## Large packages (Stripe SDK, Fyne, Firestore)

The FFI generator emits typed and reflect-typed variants per function. Unused bindings are stripped at build time by `dceFfiWrappers`:

- Stripe: 8,896 types, ~81k wrapper bodies → a few hundred bytes of actually-used wrappers per project.
- Firestore: 835 functions → same story.

You pay for what you use.

## When Sky can't type a Go symbol

Some Go shapes don't have a compile-time-expressible type:

- Unexported return types (`*pkg.internalTransform`).
- Generic types with unknown constraints (`V2List[T]`).
- Channel returns.
- Inspector couldn't see the package (rare — usually a build-tag issue).

For these, Sky emits a reflect-typed wrapper (`Go_X_y(arg0 any) any`) that works at runtime via `reflect.Value.Call`. You lose Go-side static type checking but the code still compiles and runs.

See [ffi-design.md](ffi-design.md) for the classification algorithm.
