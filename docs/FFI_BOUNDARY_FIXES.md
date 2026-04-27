# FFI boundary fixes — implementation prompt

**Context for the implementing session:** Sky's Go FFI wraps every
call in `Result Error T` via `SkyFfiRecoverT` panic recovery. This
is intentional — the FFI boundary is a trust boundary (typed-airlock
discipline: you don't trust the other side). But four gaps exist where
Go's return patterns slip through without proper Sky-side typing.
This prompt addresses all four, plus the docs that overstate
coverage.

**Guiding principle:** discourage unnecessary FFI. Sky's stdlib
should cover most use cases. When users do reach for Go FFI, the
`Result` wrapping is intentional friction — it makes the boundary
visible and forces explicit error handling (like Rust's `?`).

---

## P0 — Named error types (inspector bug) — landed e1faa21

**Problem.** `sky-ffi-inspect/main.go:classifyEffect` checks
`r.Type == "error"` — a string literal match. Go functions
returning `(*T, *os.PathError)` or `(int, *url.Error)` are
classified as `pure` because `*os.PathError` ≠ `"error"`. The
error is silently dropped into a tuple slot instead of producing
`Result Error T`.

**Fix.** In `sky-ffi-inspect/main.go`, change `classifyEffect` to
use Go's `types.Implements` to check if the last result type
implements the `error` interface, not string-match:

```go
import "go/types"

var errorInterface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func implementsError(t types.Type) bool {
    return types.Implements(t, errorInterface) ||
           types.Implements(types.NewPointer(t), errorInterface)
}
```

Then in `classifyEffect`, replace:
```go
if r.Type == "error" {
```
with a check that uses the `types.Type` object (you'll need to
thread it through — currently only the string representation is
stored in `Param`). Add a `GoType types.Type` field to `Param`
or pass the `*types.Signature` to `classifyEffect`.

**Acceptance.** A Go function `func ReadConfig() (*Config, *ConfigError)`
where `*ConfigError` implements `error` should generate a
`Result Error Config` wrapper, not a `SkyTuple2`.

**Test.** Add a fixture Go package under `test-ffi-fixtures/` with
a named error type and verify `sky-ffi-inspect` classifies it as
`fallible`.

---

## P1 — Nil pointer returns → Maybe — landed e1faa21

**Problem.** `func F() *T` can return `nil`. Sky wraps it as an
opaque value. If the user calls a method on a nil opaque, Go panics.
No `Maybe` wrapping — the nil is invisible at the Sky type level.

**Fix.** In `FfiGen.hs`, when generating the typed wrapper for a
function whose single (non-error) return type is a Go pointer
(`*pkg.Foo`):

```go
func Go_Pkg_fooT() (out SkyResult[any, SkyMaybe[*pkg.Foo]]) {
    defer SkyFfiRecoverT(&out)()
    r0 := pkg.Foo()
    if r0 == nil {
        out = Ok[any, SkyMaybe[*pkg.Foo]](Nothing[*pkg.Foo]())
    } else {
        out = Ok[any, SkyMaybe[*pkg.Foo]](Just[*pkg.Foo](r0))
    }
    return
}
```

Sky-side type becomes `Result Error (Maybe Foo)`. User must handle
both the Result (boundary failure) and the Maybe (Go returned nil).

**Scope.** Only applies to functions returning a bare `*T` without
an `error` companion. `(*T, error)` keeps the current shape
(`Result Error T`) — the error already covers the nil case (Go
convention: if err is nil, the pointer is valid).

**Acceptance.** A Go function `func MaybeUser() *User` generates a
wrapper returning `SkyMaybe[*User]` inside the Result. The `.skyi`
signature shows `Maybe User`.

---

## P2 — (T, bool) comma-ok → Maybe — landed e1faa21

**Problem.** Go's `map[K]V` lookup, type assertions, and
`sync.Map.Load` return `(T, bool)`. The docs claim this maps to
`Maybe T` but `classifyTypedResult` treats it as `SkyTuple2`.

**Fix.** In `FfiGen.hs:classifyTypedResult`, add a case before
the generic two-return handler:

```haskell
-- (T, bool) comma-ok pattern → Maybe T
[(_, t), (_, "bool")] | t /= "error" && t /= "bool" ->
    Just (maybeTy t, False, \call -> "commaOkToMaybe(" ++ call ++ ")")
```

In `runtime-go/rt/rt.go`, add a helper:

```go
func CommaOkToMaybe[T any](v T, ok bool) SkyMaybe[T] {
    if ok { return Just[T](v) }
    return Nothing[T]()
}
```

The generated wrapper calls `CommaOkToMaybe(pkg.MapGet(key))`.

**Edge case.** Some Go functions return `(bool, bool)` — both
are booleans but the second is the ok-flag. The pattern `(T, bool)`
where T==bool is ambiguous. Resolve by checking Go convention:
if the function name contains `Load`, `Get`, `Lookup`, `Check`,
or the second param is named `ok`, treat as comma-ok. Otherwise
fall back to SkyTuple2.

**Acceptance.** A Go function `func Lookup(k string) (string, bool)`
generates a wrapper returning `SkyMaybe[string]` inside Result.

---

## P3 — Interface-nil wrapping — landed e1faa21

**Problem.** `func F() io.Reader` can return a non-nil interface
value whose underlying pointer is nil (`(*os.File)(nil)` satisfies
`io.Reader`). Calling methods on it panics. Go pointer returns have
the same issue when accessed through interface wrappers.

**Fix — method/getter/setter level.** Rather than wrapping every
interface return (too many false positives), wrap the *call sites*
— every generated getter, setter, and method wrapper for opaque
types adds a nil-receiver check:

```go
func Go_Pkg_fooBarT(self *pkg.Foo) (out SkyResult[any, string]) {
    defer SkyFfiRecoverT(&out)()
    if self == nil {
        out = Err[any, string](ErrFfi("nil receiver: Foo.Bar"))
        return
    }
    out = Ok[any, string](self.Bar())
    return
}
```

This catches:
- User passing a nil opaque from a previous FFI call
- Interface-nil values flowing through opaque boundaries
- Method calls on expired/closed resources

**Scope.** Apply to every method/getter/setter wrapper where the
receiver is a pointer type. Struct-value receivers can't be nil.

**Acceptance.** Calling a method on a nil opaque returns
`Err (ErrFfi "nil receiver: ...")` instead of panicking.

**Test.** Go test in `runtime-go/rt/` that creates a nil opaque
pointer, calls a method wrapper, and asserts the Err.

---

## Docs update

After implementing all four, update:

1. `docs/ffi/go-interop.md` — return-type-mapping table must match
   reality. Add the interface-nil and named-error-type rows. Remove
   claims about `(T, bool) → Maybe T` if not yet implemented; add
   them once landed.

2. `CLAUDE.md` — FFI boundary mapping table. Same corrections.

3. `templates/CLAUDE.md` — user-facing template. Same.

4. `docs/ffi/ffi-design.md` — add a "Trust boundary" section
   explaining why all FFI returns Result: the boundary is untrusted
   (typed-airlock discipline), and the Result wrapping is intentional
   friction to discourage unnecessary FFI and surface failures explicitly.

5. Add a `docs/ffi/boundary-philosophy.md` explaining:
   - Sky's stdlib is the preferred path (no Result tax)
   - Go FFI is the escape hatch (Result tax)
   - The analogy: a typed-airlock FFI (e.g. Elm's ports — typed
     airlock to untrusted JS) = Sky FFI (typed airlock to unchecked Go)
   - Why this is different from Rust's `unsafe` (Go isn't unsafe,
     it's just untyped from Sky's perspective)

---

## Implementation order

1. P0 (named errors) — highest impact, inspector-only change
2. P2 (comma-ok → Maybe) — FfiGen + small rt helper
3. P1 (nil pointer → Maybe) — FfiGen wrapper shape change
4. P3 (nil-receiver checks) — FfiGen method/getter/setter change
5. Docs — after all four land

Each fix should follow the audit pattern: failing test first,
implement, verify 18/18 example sweep, commit with descriptive
message.

---

## Non-goal for this prompt

Changing FFI from Result to Task. The current `Result Error T`
return is correct for synchronous FFI calls. Task is for deferred
effects (File.readFile, Http.get) where the operation hasn't
happened yet. FFI calls execute immediately — they're across a
trust boundary but not deferred. The Result wrapping captures the
trust boundary; Task would add ceremony without benefit for
synchronous operations. If a user wants to defer an FFI call, they
can use `Task.lazy (\_ -> Ffi.call ...)` explicitly.

---

## Status

All four items landed in commit `e1faa21` (2026-04-16).

- P0 (named error types) — `sky-ffi-inspect` uses `types.Implements`.
- P1 (`*T` → `Maybe`) — codegen wraps via `rt.NilToMaybe`.
- P2 (`(T, bool)` → `Maybe`) — codegen wraps via `rt.CommaOkToMaybe`.
- P3 (nil-receiver checks) — every method wrapper guards before call.

Docs + samples updated in the follow-up commit. New
`docs/ffi/boundary-philosophy.md` explains the trust-boundary design
(typed-airlock analogy, Result vs Task, when to prefer stdlib).

Verification (run before each commit in this work):
- `bash scripts/example-sweep.sh --build-only` → 18/18
- `cd runtime-go && go test ./rt/` → all pass
- self-tests across `test-files/*.sky` → 67/67
- `cabal test` → all specs pass
- `sky verify <key examples>` → runtime ok
