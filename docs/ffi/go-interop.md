# Go interop

Sky imports any Go package and uses it with full type safety.

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


newId : String
newId =
    Uuid.newString    -- zero-arg Go functions take no () in Sky
```

Module name mapping:

| Go path | Sky module |
|---------|------------|
| `github.com/google/uuid` | `Github.Com.Google.Uuid` |
| `github.com/stripe/stripe-go/v84` | `Github.Com.Stripe.StripeGo.V84` |
| `net/http` | `Net.Http` |
| `fyne.io/fyne/v2/app` | `Fyne.Io.Fyne.V2.App` |

Hyphens are dropped, next character upper-cased. Non-alphanumerics become `_`.

## Return type mapping

| Go | Sky |
|----|-----|
| `string` | `String` |
| `int`, `int64`, `int32` | `Int` |
| `float64` | `Float` |
| `bool` | `Bool` |
| `[]T` | `List T` |
| `error` | `Result Error ()` |
| `(T, error)` | `Result Error T` |
| `(T, bool)` | `Maybe T` |
| `*T` (method-returning) | `Maybe T` |
| `*pkg.Struct` | opaque type with generated getters/setters |
| `map[string]V` | `Dict String V` |
| `interface{}` / `any` | Sky-level `any` (boxed) |

## Opaque struct pattern (Sky's builder convention)

Go structs are opaque — you build them via generated constructors and pipeline setters:

```elm
params =
    Stripe.newCheckoutSessionParams ()
        |> Stripe.checkoutSessionParamsSetMode "payment"
        |> Stripe.checkoutSessionParamsSetSuccessURL "https://example.com/success"
        |> Stripe.checkoutSessionParamsSetLineItems [ lineItem ]
```

Naming rules:

- Constructor: `new<TypeName> : () -> TypeName`
- Getter: `<typeName><FieldName> : TypeName -> FieldType`
- Setter: `<typeName>Set<FieldName> : FieldType -> TypeName -> TypeName`

Setters take the value first and the struct second — so they pipe naturally via `|>`.

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
