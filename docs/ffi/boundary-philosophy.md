# FFI boundary philosophy

## The trust boundary

Sky's Go FFI is a **trust boundary**, not a transparent function call.
Even when Go's signature looks safe (`func F() string`), the call
crosses into code Sky's type checker can't see — the Go compiler is
the only gatekeeper, and Go's type system permits panics, nil
pointers, interface-nil values, OOM, goroutine leaks, and runtime
errors that Sky's HM types can't model.

This is the same problem Elm solves with **ports**: typed airlocks to
JavaScript that decode incoming values and reject what doesn't fit.
Sky applies the same principle to Go: every FFI call returns
`Result Error T`, forcing the user to acknowledge the boundary at
each call site.

## Why Result, not Task

| Sky type | Meaning | Use for |
|---|---|---|
| `Result Error T` | "this crossed a boundary, here's the outcome" | Synchronous FFI calls (already executed) |
| `Task Error T` | "this will cross a boundary when you say go" | Deferred Sky effects (`File.readFile`, `Time.sleep`) |

FFI calls execute immediately — wrapping them in `Task` would imply
"hasn't run yet" which is misleading. By the time Sky code sees the
value, the Go function has already returned. `Result` accurately
describes that state: the call happened, here's what came back.

If a user wants to defer an FFI call (compose it with `Task.parallel`
or hold it lazily), they can wrap it explicitly:

```elm
deferred : Task Error String
deferred =
    Task.lazy (\_ ->
        case Uuid.newString of
            Ok id -> Task.succeed id
            Err e -> Task.fail e
    )
```

## Why Result on every FFI call (even pure-looking ones)

Three reasons:

1. **Go can panic anywhere.** Even functions Go authors mark as pure
   can fail — third-party packages have bugs, nil pointers sneak in,
   `init()` in some imported package can leave global state broken,
   the runtime can hit resource limits. The Result wrapping turns
   every panic into a typed `Err` instead of a process crash. Sky's
   defer-recover layer (`SkyFfiRecoverT`) catches the panic at the
   wrapper boundary and surfaces it as `Err(ErrFfi(...))`.

2. **Honest types.** A function that *might* fail returning bare `T`
   is dishonest. `Result Error T` matches what can actually happen at
   runtime. The user reads the type and knows: "this is across the
   boundary; check the outcome."

3. **Intentional friction.** Discouraging Go FFI is a design feature.
   Sky's stdlib should grow to cover most use cases. The Result tax
   is a signal: "you're leaving Sky's safety guarantees, consider
   whether you really need to." When a Sky-side equivalent exists
   (`Std.File`, `Std.Http`, `Std.Db`, `Std.Crypto`, etc.) prefer it.

## Comparison: Rust's `?` and `unwrap`

Rust forces explicit handling of `Result` via the `?` operator
(propagate up the call stack) or `.unwrap()` (panic on `Err` for
known-safe cases). Sky's pattern matching, `Result.withDefault`,
`Result.map`, and `Result.andThen` serve the same role: every call
site explicitly acknowledges the fallibility. There's no implicit
unwrap — the compiler won't let you accidentally use a `Result T` as
if it were `T`.

The difference: Rust's boundary is `unsafe` (memory safety). Sky's
boundary is "untyped from Sky's perspective" — Go isn't unsafe in the
memory-safety sense, but its type system doesn't surface enough
information for Sky's HM to reason about every failure mode.

## Comparison: Elm ports

| Property | Elm ports | Sky FFI |
|---|---|---|
| Typed boundary | Declared types both sides | Sky declares; inspector extracts Go types |
| Failure containment | Bad JS data → decode error | Bad Go return → `Result Error T` |
| Async | Yes (Cmd outbound, Sub inbound) | No (synchronous; sky compiles to same Go binary) |
| Decoder | `Json.Decode` for incoming | `rt.Coerce[T]` for shape mismatches |
| Crash safety | JS errors can't reach Elm | Go panics caught by `SkyFfiRecoverT` |

Sky doesn't need Elm's async ports because it's not crossing a
runtime boundary — Sky compiles to Go and they share a process.
Synchronous calls fit Go's model. The other Elm-port properties
(typed, contained, decoded, crash-safe) all apply.

## What this means in practice

### Prefer Sky stdlib

| Task | Sky stdlib (no Result tax for pure ops) | Go FFI fallback |
|---|---|---|
| Generate UUID | (none — use FFI) | `Uuid.newString` |
| HTTP request | `Sky.Core.Http.get` | `Http.get` (net/http) |
| File read | `Sky.Core.File.readFile` | `Os.readFile` |
| SQL query | `Std.Db.query` | `Sql.dbQuery` (database/sql) |
| Hash | `Sky.Core.Crypto.sha256` | `Crypto.sha256.sum256` |
| Time | `Sky.Core.Time.now` | `Time.now` (time package) |
| JSON encode/decode | `Sky.Core.Json.Encode` / `Decode` | `Json.marshal` |
| Auth | `Std.Auth.signToken` | (none) |

### When you need Go FFI, expect Result at every call site

```elm
-- Bad — ignores the boundary
let id = Uuid.newString in ...   -- type error: id : Result Error String

-- Good — pattern match
case Uuid.newString of
    Ok id ->
        ...
    Err e ->
        ...

-- Good — bail to a default
let id = Result.withDefault "anonymous" Uuid.newString in ...

-- Good — chain across multiple FFI calls
result =
    Uuid.newString
        |> Result.andThen (\id -> Db.insertUser id email)
        |> Result.andThen Session.create
```

### For (T, bool) comma-ok returns, you handle two layers

Go's `func F() (T, bool)` (map lookups, type assertions,
sync.Map.Load) maps to `Result Error (Maybe T)`. The Result
captures boundary failure (panic, type mismatch); the Maybe
captures Go's "nothing here":

```elm
-- Looking up a key that may not exist
case SomeMap.get key of
    Ok (Just value) ->
        useValue value

    Ok Nothing ->
        -- Boundary call succeeded but the key isn't in the map
        useDefault

    Err e ->
        -- The FFI call itself failed (panic, etc.)
        logBoundaryFailure e
```

### Bare `*T` returns are NOT auto-wrapped in Maybe

Many Go SDKs use builder patterns:

```go
session, err := stripe.New(params).
    Customer(custID).
    LineItems(items).
    Confirm(ctx)
```

Each intermediate call returns `*Builder`. The pointer is
conventionally non-nil — wrapping every hop in `Maybe` would force
the user to unwrap at every step:

```elm
-- Hypothetical Maybe-wrapped chain (rejected design)
case Stripe.new params of
    Ok (Just s1) ->
        case Stripe.customer custID s1 of
            Ok (Just s2) ->
                case Stripe.lineItems items s2 of
                    Ok (Just s3) -> ...
                    ...
```

Sky's design: `*T` returns flow through as `Result Error T`. If
the Go SDK genuinely returns nil and the user calls a method on
it, the defer-recover catches the nil-deref panic and surfaces
`Err(ErrFfi("nil pointer..."))`. Go authors who explicitly mean
"this can be nothing" use `(T, error)` or `(T, bool)` — those map
cleanly to `Result Error T` / `Result Error (Maybe T)`.

### Method calls have nil-receiver guards

Every generated method/getter/setter wrapper checks for a nil
receiver. A method call on an expired/closed/never-initialised
opaque returns `Err(ErrFfi "nil receiver: Type.Method")` instead of
panicking. The Result wrapping makes this visible at the type level —
you can't accidentally `.method()` on a nil and crash the process.

## When to add to Sky's stdlib instead of telling users to FFI

If a Go package's API is small, stable, and broadly useful
(crypto primitives, time helpers, JSON, regex), prefer adding a
Sky-side wrapper to `sky-stdlib/` so users don't pay the Result tax
for what's effectively pure code. The `Std.*` modules under
`sky-stdlib/` cover this: they wrap Go but expose pure Sky types
for genuinely-pure operations (`Std.Crypto.sha256` returns `String`,
not `Result Error String`, because hashing can't meaningfully fail).

Reserve direct FFI exposure for:
- Large or unstable APIs (Stripe SDK, Firestore, Fyne) where wrapping
  every function in stdlib would be infeasible.
- Fundamentally-fallible operations (network, disk, exec).
- Opaque resources (DB handles, GUI windows, file descriptors).
