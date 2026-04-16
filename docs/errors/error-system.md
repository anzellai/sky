# Error system

Since v0.9, **every fallible operation in Sky returns a value whose error slot is `Sky.Core.Error`** — a structured ADT with eleven kinds and typed details. There is no more `Result String` or `Task String` on any public surface.

## The shape

```elm
module Sky.Core.Error exposing (..)


type Error
    = Error ErrorKind ErrorInfo


type ErrorKind
    = Io
    | Network
    | Ffi
    | Decode
    | Timeout
    | NotFound
    | PermissionDenied
    | InvalidInput
    | Conflict
    | Unavailable
    | Unexpected


type alias ErrorInfo =
    { message : String
    , details : ErrorDetails
    }


type ErrorDetails
    = NoDetails
    | FfiPanic String
    | TypeMismatch String String
    | HttpStatus Int
    | JsonDecode String
    | Custom (Dict String String)
```

## Constructors

```elm
io : String -> Error
network : String -> Error
ffi : String -> Error
decode : String -> Error
timeout : String -> Error
notFound : Error
permissionDenied : Error
invalidInput : String -> Error
conflict : String -> Error
unavailable : String -> Error
unexpected : String -> Error
```

Plus builders:

```elm
withMessage : String -> Error -> Error
withDetails : ErrorDetails -> Error -> Error
toString : Error -> String
isRetryable : Error -> Bool
```

## Usage pattern

```elm
import Sky.Core.Error as Error exposing (Error(..), ErrorKind(..))


loadUser : String -> Task Error User
loadUser id =
    Db.queryDecode userDecoder "SELECT * FROM users WHERE id = ?" [ id ]
        |> Task.mapError classifyError


classifyError : Error -> Error
classifyError e =
    case e of
        Error PermissionDenied info ->
            info.message
                |> String.append "user lacks permission: "
                |> Error.invalidInput
        _ ->
            e
```

## Go side

Every runtime kernel that can fail returns `rt.SkyResult[any, any]` where the Err slot is a value produced by one of the `Err*` builders in `runtime-go/rt/rt.go`:

```go
ErrIo(msg string) any
ErrNetwork(msg string) any
ErrFfi(msg string) any
ErrDecode(msg string) any
ErrTimeout(msg string) any
ErrNotFound() any
ErrPermissionDenied() any
ErrInvalidInput(msg string) any
ErrConflict(msg string) any
ErrUnavailable(msg string) any
ErrUnexpected(msg string) any
```

These produce `skyErrorAdt` values that match the Sky ADT's runtime layout (`{Tag, SkyName, V0 (kind), V1 (info)}`), so Sky-side pattern matching works without a translation layer.

### Panic recovery

```go
func SkyFfiRecover(out *any) func()
func SkyFfiRecoverT[A any](out *SkyResult[any, A]) func()
```

Every FFI wrapper `defer`s one of these. On panic, `out` is set to `Err(ErrFfi(<panic-message>))` with an `FfiPanic` detail carrying the recovered value. Sky code never sees a Go panic.

## When to use which kind

| Kind | Use for |
|------|---------|
| `Io` | File / DB / disk failures |
| `Network` | HTTP / DNS / transport failures |
| `Ffi` | Panics caught at the FFI boundary |
| `Decode` | JSON / binary decoder failures |
| `Timeout` | Deadlines, context cancellation |
| `NotFound` | Record or resource absent |
| `PermissionDenied` | Auth / access-control failures |
| `InvalidInput` | User input validation |
| `Conflict` | Optimistic-lock / unique-constraint failures |
| `Unavailable` | Service overloaded / circuit-broken |
| `Unexpected` | Fallback when no other kind fits |

`isRetryable` returns `True` for `Timeout`, `Network`, and `Unavailable`. Use it in retry / backoff policies.

## Migration from `Result String`

If you're upgrading a pre-v1 project:

```elm
-- Before:
loadUser : String -> Task String User

-- After:
loadUser : String -> Task Error User
```

If your error branch was `Err e -> Err (Error.io e)` and the upstream now returns `Error` (not `String`), collapse to `Err e -> Err e`. Error chaining through combinators preserves the kind.

Forbidden in v0.9+ public surfaces (enforced by `Sky.ErrorUnificationSpec`):

- `Result String a`
- `Task String a`
- `Std.IoError` (deleted)
- `RemoteData` (deleted)
