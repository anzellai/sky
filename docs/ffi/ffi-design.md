# FFI design

Technical reference for how the FFI generator classifies and emits wrappers. For user-facing usage see [go-interop.md](go-interop.md).

## Pipeline

```
Go module path
   │
   ▼  tools/sky-ffi-inspect (Go program using go/types)
ffi.kernel.json
   │
   ▼  Sky.Build.FfiGen.generateBindings
.skycache/ffi/<slug>.{skyi,kernel.json}
.skycache/go/<slug>_bindings.go
   │
   ▼  sky-out/rt/<slug>_bindings.go (copied at build time)
   │
   ▼  dceFfiWrappers (build-time dead-code elimination)
sky-out/rt/<slug>_bindings.go (pruned)
```

## Wrapper classes

Each Go function is classified into one of:

| Class | Emission |
|-------|----------|
| `DirectCall` | `func Go_X_yT(arg0 T0, arg1 T1) (out SkyResult[any, R])` — fully typed |
| `ReflectTopLevel` | `func Go_X_y(arg0 any) any` — uses `reflect.ValueOf(pkg.F)` |
| `ReflectGeneric` | Stub — generics with unknown constraints can't be instantiated |
| `ReflectMethod` | Method-by-name reflection via `recv.MethodByName(...).Call(...)` |
| Field getter | `func Go_X_yT(arg0 *pkg.Recv) FieldT { return arg0.Field }` |
| Field setter | `func Go_X_yT(value ValT, recv *pkg.Recv) *pkg.Recv` |
| Pkg-level value | `func Go_X_y(_ any) any { return pkg.Constant }` |
| Unreachable | Skipped (only a stub emitted for diagnostics) |

## Typed variants (`<Name>T`)

Every `DirectCall` that can be typed gets a `T`-suffixed companion. The any/any version is skipped entirely — call-site codegen always routes through the typed name.

Predicates (`isSimpleTypedType`):
- Accepts: primitives, `[]T`, `map[K]V`, `func(args) ret`, `pkg.X`, `*pkg.X`, `interface{}`, `[]interface{}`.
- Rejects: channel returns (`chan T`, `<-chan T`), bare type parameters (`T`), unexpressible generics.

`allPackagesKnown` ensures every `pkg.` prefix is a known import alias. Unknown prefixes force the reflect path.

## `FfiT_` re-export aliases

When a typed wrapper's parameter type references a file-local package alias, Sky emits a `type FfiT_<Wrapper>_P<N> = <goType>` alias. `main.go` can then reference the FFI-local type via `rt.FfiT_*` — this is the mechanism that lets `.skycache/go/` stay self-contained while still participating in typed call sites.

## Panic recovery

Every wrapper `defer`s either:

- `SkyFfiRecover(&out)()` — for any/any wrappers.
- `SkyFfiRecoverT[A](&out)()` — for typed wrappers.

On panic:

```go
out = Err[any, A](ErrFfi(fmt.Sprintf("%v", recovered)))
```

Sky's `Error` ADT carries the panic message with an `FfiPanic` detail.

## Multi-return mapping

**Every FFI call returns `Result Error <unpacked>`** — there are no carve-outs for "infallible" Go signatures (Go can panic anywhere, see [boundary-philosophy.md](boundary-philosophy.md)). The first column below is the Sky-side type the user writes / pattern-matches on. The second is the runtime Go shape `FfiGen.classifyTypedResult` emits in the wrapper signature.

| Go signature | Sky-side type | Go runtime emission |
|--------------|---------------|----------------------|
| `func F() T` | `Result Error T` | `SkyResult[any, T]` |
| `func F() error` | `Result Error ()` | `SkyResult[any, struct{}]` (Ok holds `struct{}{}`) |
| `func F() (T, error)` | `Result Error T` | `SkyResult[any, T]` (error extracted to Err) |
| `func F() (T, *NamedErr)` where `NamedErr` implements `error` | `Result Error T` | `SkyResult[any, T]` (named error stringified into Err) |
| `func F() (T, bool)` (comma-ok) | `Result Error (Maybe T)` | `SkyResult[any, SkyMaybe[T]]` |
| `func F() (T, U)` | `Result Error (T, U)` | `SkyResult[any, SkyTuple2]` (`{V0: any, V1: any}`) |
| `func F() (T, U, error)` | `Result Error (T, U)` | `SkyResult[any, SkyTuple2]` (error extracted) |
| `func F() (T, U, V)` | `Result Error (T, U, V)` | `SkyResult[any, SkyTuple3]` (`{V0, V1, V2}`) |
| `func F() (T, U, V, error)` | `Result Error (T, U, V)` | `SkyResult[any, SkyTuple3]` (error extracted) |
| `func F() (T0, T1, ..., Tn)` for n≥4 | `Result Error (T0, T1, ..., Tn)` | `SkyResult[any, SkyTupleN]` (slice-backed) |

`SkyTuple2` / `SkyTuple3` / `SkyTupleN` are runtime structs in `runtime-go/rt/rt.go`. Sky users access tuple slots via destructuring patterns (`( a, b ) = ...`); the tuple-shape mapping (Sky 2-tuple → SkyTuple2, Sky 3-tuple → SkyTuple3, ≥4-tuple → SkyTupleN) is performed by `Sky.Generate.Go.Type.typeToGo` and is invisible at the source level.

## Variadic

`func F(x ...T) R` → Sky-side takes a `List T` argument; wrapper spreads with `...` and returns `Result Error R`:

```go
func Go_Pkg_F(arg0 []T) (out SkyResult[any, R]) {
    defer SkyFfiRecoverT(&out)()
    out = Ok[any, R](pkg.F(arg0...))
    return
}
```

Like every other call shape, the return is wrapped in `SkyResult` so Go panics are caught by `SkyFfiRecoverT` and surfaced as `Err(ErrFfi(...))`.

## Build-time dead-code elimination

`Sky.Build.Compile.dceFfiWrappers`:

1. Walks `sky-out/main.go` and every non-rt `.go` file under `sky-out/`.
2. Collects every `rt.Go_<name>(` reference.
3. Rewrites each `sky-out/rt/*_bindings.go` keeping only the reachable wrapper bodies.
4. Preserves imports + header comments (Go is happy with unused imports as long as a blank `_` import retains them, which every bindings file already has).

Reduction ratios in the sweep:

| Package | Before | After |
|---------|--------|-------|
| Stripe | 81,697 lines | 119 lines |
| Firestore | ~30k lines | ~200 lines |
| Fyne | ~10k lines | ~500 lines |

## When the inspector can't classify

Some packages resist the inspector:

- Build-tag-gated types (e.g. platform-specific APIs).
- Internal-only types (`<path>/internal.*` or `<path>/vendor.*`) — `shouldSkipFn` filters them out.
- Generics with constraints like `~string` or `string | FieldPath` — the inspector doesn't surface constraint info, so `ReflectGeneric` stubs are emitted. Users can write hand-rolled instantiations in `runtime-go/rt/` if they need those functions.

## Hand-written supplements

Not recommended but supported: files under `runtime-go/rt/` (the source-of-truth runtime) are embedded into every project. Adding hand-written `Go_MyPkg_myFunc` here is permanent and will be visible to every Sky project — use only for truly stable, widely-shared runtime additions, not per-project extensions.
