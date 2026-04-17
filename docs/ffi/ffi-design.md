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

| Go signature | Sky return |
|--------------|------------|
| `func F() T` | `T` |
| `func F() error` | `Result Error ()` |
| `func F() (T, error)` | `Result Error T` |
| `func F() (T, U)` | `SkyTuple2[T, U]` |
| `func F() (T, U, error)` | `Result Error (SkyTuple2[T, U])` |
| `func F() (T, U, V)` | `SkyTuple3[T, U, V]` |
| `func F() (T, U, V, error)` | `Result Error (SkyTuple3[T, U, V])` |

## Variadic

`func F(x ...T)` → Sky-side takes a `[]T`, spread with `...` in the wrapper body:

```go
func Go_Pkg_F(arg0 []T) (out SkyResult[any, R]) {
    defer SkyFfiRecoverT(&out)()
    out = Ok[any, R](pkg.F(arg0...))
    return
}
```

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
