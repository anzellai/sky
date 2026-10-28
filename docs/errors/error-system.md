# Error system

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../history/compiler/versions.md`](../history/compiler/versions.md) for the changelog.


Since v0.9, **every fallible operation in Sky returns a value whose error slot is `Sky.Core.Error`** — a structured ADT with eleven kinds and typed details, one of which (`Domain`) is open to every domain. There is no more `Result String` or `Task String` on any public surface.

## The shape

```elm
module Sky.Core.Error exposing
    ( Error(..), ErrorKind(..), ErrorDetails(..), DomainFailure, ErrorInfo, PanicInfo, TypeInfo, … )


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
    , details : Maybe ErrorDetails
    }


type ErrorDetails
    = FfiPanic PanicInfo                -- { message : String, stack : List String }
    | TypeMismatch TypeInfo             -- { expected : String, actual : String }
    | HttpStatus Int
    | JsonDecode String
    | Custom String
    | Domain DomainFailure              -- v0.28.0: a domain's own failure (below)


type DomainFailure                      -- opaque
```

## Constructors

```elm
io : String -> Error
network : String -> Error
ffi : String -> Error
decode : String -> Error
timeout : Error
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

## Domain failures

*(v0.28.0)* `Domain DomainFailure` is the one open-ended detail. A stdlib
domain (`"ai"` first; later `"db"`, `"durable"`, …) and an app record their
own failures in it, and the core type does not change when a domain adds a
failure. A domain failure holds:

- the **domain**, a lower-case dotted name (`"ai"`, `"myapp.billing"`),
- a stable **code** inside that domain, by convention the name of the typed
  constructor (`"Overloaded"`),
- whether a retry of the same call can succeed (**retryable**),
- an optional **retry delay** in milliseconds (a server's `Retry-After`),
- string **attributes**, in the order they were added. They go to logs and
  across the Sky.Spa wire, so they never hold a secret.

```elm
-- build
failure : String -> String -> DomainFailure          -- domain, code
withRetryable : Bool -> DomainFailure -> DomainFailure
withRetryAfter : Int -> DomainFailure -> DomainFailure
withAttr : String -> String -> DomainFailure -> DomainFailure
fromDomain : ErrorKind -> String -> DomainFailure -> Error   -- the kind is required

-- read
failureOf : Error -> Maybe DomainFailure
failureDomain : DomainFailure -> String
failureCode : DomainFailure -> String
failureRetryable : DomainFailure -> Bool
failureRetryAfter : DomainFailure -> Maybe Int
failureAttr : String -> DomainFailure -> Maybe String
failureAttrs : DomainFailure -> List ( String, String )

-- generic helpers, for every domain
domain : Error -> Maybe String        -- Just "ai"
isRetryable : Error -> Bool           -- the domain's flag wins over the kind
retryAfter : Error -> Maybe Int       -- the domain's retry delay
```

A domain failure always comes with an existing `ErrorKind`, so code that
matches only on the kind keeps working for every domain. A domain module gives
the typed view: `Std.Ai.Failure.fromError : Error -> Maybe Failure`
([`../ai/local.md`](../ai/local.md#failures)).

An app can define its own domain the same way:

```elm
cardDeclined : String -> Error
cardDeclined reason =
    Error.failure "myapp.billing" "CardDeclined"
        |> Error.withAttr "reason" reason
        |> Error.fromDomain Error.InvalidInput "the card was declined"
```

**Write code that does not break when a domain grows.** Prefer the generic
helpers, or a domain's typed view with a `_ ->` branch. A `case` over
`ErrorDetails` with one arm per variant and no `_ ->` is refused with
`[E3001]` when a variant is added, and the diagnostic names this section. The
`Domain` variant is the last one the core type needs: a new domain adds codes,
not variants.

**On the wire.** `Codec.error` (the Sky.Spa RPC codec) encodes a domain
failure as one object:
`["Domain", {"domain": "ai", "code": "Overloaded", "retryable": true, "retryAfterMs": 1500, "attrs": [["status", "429"]]}]`.
It decodes forward-compatibly: `domain` and `code` are required, a missing
`retryable` is `false`, a missing or `null` `retryAfterMs` is no delay, a
missing `attrs` is none, and keys a later release adds are ignored. Any domain
and any code decode, so a failure from a newer peer keeps its kind, message,
code and retry flag. A details variant this release does not know decodes as
no details.

**In logs.** An `Error` given as a structured-log attribute
(`Log.errorWith "call failed" [ "error", err ]`) logs as `<Kind>: <message>`,
and a domain failure adds `error.domain`, `error.code`, `error.retryable`,
`error.retryAfterMs` and `error.attr.<name>` fields.

## Usage pattern

```elm
import Sky.Core.Error as Error exposing (Error(..), ErrorKind(..))


loadUser : Db -> String -> Task Error (List User)
loadUser db id =
    Db.queryDecode db "SELECT * FROM users WHERE id = ?" [ id ] userDecoder
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
ErrTimeout() any
ErrNotFound() any
ErrPermissionDenied(msg string) any
ErrInvalidInput(msg string) any
ErrConflict(msg string) any
ErrUnavailable(msg string) any
ErrUnexpected(msg string) any

// v0.28.0, runtime-go/rt/error_domain.go
ErrDomain(kindTag int, msg string, f DomainFailure) any
DomainOf(v any) (DomainFailure, bool)
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

`isRetryable` returns `True` for `Timeout`, `Network`, and `Unavailable`, unless
the error carries a domain failure: then the domain's own retryable flag decides
(v0.28.0). A crashed AI engine is `Unavailable` and not retryable, and a
database deadlock can be a `Conflict` that a retry resolves. Use it, with
`retryAfter`, in retry and backoff policies.

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
