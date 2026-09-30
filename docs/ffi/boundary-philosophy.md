# FFI boundary philosophy

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../history/compiler/versions.md`](../history/compiler/versions.md) for the changelog.


## The trust boundary

Sky's Go FFI is a **trust boundary**, not a transparent function call.
Even when Go's signature looks safe (`func F() string`), the call
crosses into code Sky's type checker can't see — the Go compiler is
the only gatekeeper, and Go's type system permits panics, nil
pointers, interface-nil values, OOM, goroutine leaks, and runtime
errors that Sky's HM types can't model.

This is the same problem typed-airlock FFI designs (such as Elm's **ports** or PureScript's foreign-import boundaries) solve: typed airlocks to
JavaScript that decode incoming values and reject what doesn't fit.
Sky applies the same principle to Go: every FFI call returns
`Result Error T`, forcing the user to acknowledge the boundary at
each call site.

## The checker enforces it

Since v0.27.0 the type checker types every Go-FFI call from the binding's
pinned signature (`sky-ffi/<pkg>.kernel.json`). A program that uses the
`Result` as its bare payload does not compile:

```text
probe : Int
probe =
    Hex.encodedLen 3     -- [E2001] type mismatch: `Result Error Int` vs `Int`
```

The wrapper, the arity and the argument and payload types are checked. Since
surface format 3 (v0.27.0, written by `sky install`) every Go type has a Sky
type, computed from the Go signature:

| Go | Sky |
|---|---|
| `string`, `int*` / `uint*`, `float*`, `bool` | `String`, `Int`, `Float`, `Bool` (an integer out of range for the Go or the Sky side is an `Err`, never a wrapped or truncated number) |
| `[]byte`, `[N]byte` | `Bytes` |
| `[]T`, `map[K]V` with a `string` / `int` / `float` / `bool` key | `List T`, `Dict K V` (other keys: opaque) |
| `*T` where `T` has a Sky form | `Maybe T` (nil is `Nothing`), in results, parameters, lists, fields and callbacks |
| a struct, `*Struct` and any other type with no Sky form | its own nominal type, `Pkg.Name` (`Mux.Router`) |
| `func(A) R` as a parameter | a typed callback `A -> R`; a zero-parameter one is `() -> R` |
| `interface{}` / `any` as a parameter | any Sky value |
| a non-empty interface as a parameter | a Go value that implements it; a Sky value there is `[E2013]` |

A Go value with its own nominal type is not usable at another type, not even
at the kernel `Value` type or an app type of the same name: annotate it with
its Go type (`router : Mux.Router`) and pass it to the package's bindings. A
Go value passed where Go wants an interface is checked at run time (an `Err`,
never a crash). A binding of an older surface whose wrapper converted a value
unsoundly is refused with the fix, `sky install`. A partially applied FFI
function (`Strings.repeat "ab"`) is a normal Sky function value.

`Sky.Ffi` is stdlib-only (`[E1011]`): `Ffi.kernel`, `Ffi.call`, `Ffi.callPure`
and `Ffi.callTask`. `Ffi.call*` reach a Go binding by name with an unchecked
type. `Ffi.kernel "Sym"` binds a runtime kernel and trusts the annotation it
is given, and the checker cannot compare that annotation with the kernel's
real signature, so a wrong one compiles and fails at run time. The stdlib uses
them behind declared, reviewed signatures. An application calls the typed
stdlib function (the `[E1011]` hint names it) or the `sky add` binding. A
module is never exempt by its name. The compiler grants two exceptions, both
to its own code: a project the Sky.Spa split generated may bind the split's
`Spa_*` kernels, and a module whose text is the compiler's bundled-app source
(the Sky Console, the doc server) keeps full `Sky.Ffi`.

The four are members of `Sky.Ffi` only. Spelled through any other kernel
module (`Webview.kernel`, `import Webview exposing (kernel)`) they are
`[E1011]` in every module, the stdlib included, so no qualifier reaches a
kernel by name except `Ffi`. A fetched registry package (`.skydeps/`) is
checked like application code and gets no grant: a package is pure Sky and
ships no Go kernel.

What keeps the stdlib's own `Ffi.kernel` bindings honest is a set of gates,
not the checker. `xtask kernel-members` and
`rust/crates/project/tests/kernel_surface.rs` prove every bound symbol is a
real `rt` function; `kernel_signature_coverage.rs` proves every advertised
kernel member has a signature; `kernel_signature_runtime_arity.rs` proves the
signature's arity matches the Go function. The argument and result TYPES of a
stdlib binding are its reviewed annotation, exercised by the conformance
suites; no gate derives them from the Go source.

## Why Result, not Task

| Sky type | Meaning | Use for |
|---|---|---|
| `Result Error T` | "this crossed a boundary, here's the outcome" | Synchronous FFI calls (already executed) |
| `Task Error T` | "this will cross a boundary when you say go" | Deferred Sky effects (`File.readFile`, `Time.sleep`) |

FFI calls execute immediately — wrapping them in `Task` would imply
"hasn't run yet" which is misleading. By the time Sky code sees the
value, the Go function has already returned. `Result` accurately
describes that state: the call happened, here's what came back.

If a user wants to defer an FFI call (run it off `update` with
`Cmd.perform`, compose it with `Task.parallel`, or hold it lazily), they
wrap it explicitly. `Task.lazy` delays the call and `Task.fromResult`
turns its `Result` into the Task's outcome:

```elm
deferred : Task Error String
deferred =
    Task.lazy (\_ -> Uuid.newString ()) |> Task.andThen Task.fromResult
```

(`Task.lazy : (() -> a) -> Task e a` takes a plain value. An earlier
version of this example returned a Task from the lambda, which does not
type-check: it yields `Task e (Task Error String)`.)

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

## Comparison: typed-airlock FFI (e.g. Elm's ports)

| Property | Typed-airlock FFI (Elm ports as a familiar example) | Sky FFI |
|---|---|---|
| Typed boundary | Declared types both sides | Sky declares; inspector extracts Go types |
| Failure containment | Bad foreign data → decode error | Bad Go return → `Result Error T` |
| Async | Often async (Cmd outbound, Sub inbound) | Synchronous (Sky compiles to Go and they share a process) |
| Decoder | `Json.Decode`-style for incoming | `rt.Coerce[T]` for shape mismatches |
| Crash safety | Foreign-runtime errors can't reach the host | Go panics caught by `SkyFfiRecoverT` |

Sky doesn't need a runtime-asynchronous airlock because it's not
crossing a runtime boundary — Sky compiles to Go and they share a
process. Synchronous calls fit Go's model. The other airlock
properties (typed, contained, decoded, crash-safe) all apply.

## What this means in practice

### Prefer Sky stdlib

| Task | Sky stdlib (no Result tax for pure ops) | Go FFI fallback |
|---|---|---|
| Generate UUID | `Sky.Core.Uuid.v4` / `v7` | `Uuid.newString` (`github.com/google/uuid`) |
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
let id = Uuid.newString () in ...   -- type error: id : Result Error String

-- Good — pattern match
case Uuid.newString () of
    Ok id ->
        ...
    Err e ->
        ...

-- Good — bail to a default
let id = Result.withDefault "anonymous" (Uuid.newString ()) in ...

-- Good — chain across multiple FFI calls
result =
    Uuid.newString ()
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

### A pointer to an opaque type is NOT wrapped in Maybe

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

Sky's design: a pointer to an opaque type (`*Builder`, `*sql.DB`) flows
through as `Result Error Builder`. If the Go SDK genuinely returns nil and the
user calls a method on it, the nil-receiver guard surfaces an `Err` instead of
a crash. A pointer to a type that has a Sky form (`*string`, `*int`, `*[]T`) is
different: nil is a normal value there (an optional field), so it is a
`Maybe` (v0.27.0, surface format 3). Go authors who explicitly mean "this can
be nothing" use `(T, error)` or `(T, bool)` — those map cleanly to
`Result Error T` / `Result Error (Maybe T)`.

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
