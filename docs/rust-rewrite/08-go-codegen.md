# 08 — Go Codegen

The `codegen` crate turns the **typed Go-IR** from [`07`](07-lowering-and-ir.md)
into **Go source text**. It does exactly one job: render. It never derives a
type, never resolves a name, never inserts a coercion — those decisions are
finished in `lower`. Codegen's contracts are **determinism** (L4), **fidelity to
the runtime ABI** (L10), and the **panic-safety floor** (the "if it compiles, it
works" invariant, 00 §4).

> **Implementation status.** `rust/crates/codegen`
> is built and renders the `lower` IR deterministically — the determinism gate
> (byte-stable emission across seeds/platforms) holds, and the corpus builds+runs.
> But codegen renders the **interim** representation described in
> [`07` status](07-lowering-and-ir.md), so some shapes below are target, not
> current output: user ADTs emit as `type X = rt.SkyADT` bags (`emit_type`), not
> the sealed-`interface` / iota-`alias` forms in §3; tuples emit `rt.T2[any,any]`
> (`render_tuple_ty`), not `rt.T2[A,B]`; and a `Widen` node renders `any(x)`
> (`render_expr`). The `emit_coerce` elision (`from == to` → no wrap), the
> structural `emit_ty`, and the panic-safety floor (§6) reflect the target design;
> treat §2/§3's sealed-iface/structural-generic emission as the destination.

## The scar this fixes

The Haskell renderer (`src/Sky/Generate/Go/Builder.hs`) is already close to the
right shape — a pure `GoExpr -> String` fold. Two things it does that the rewrite
tightens:

1. **String concatenation is quadratic.** `renderPackage` builds via `unlines`
   over lists and a hand-rolled recursive `intercalate` (`Builder.hs:14`,
   `Builder.hs:349-352`) that re-allocates on every `++`. On a 76k-symbol
   emission (`examples/13-skyshop`) this is real time. The rewrite writes into a
   single reused `String` buffer (§4).
2. **Order determinism is by-convention, not by-construction.** The Haskell
   backend sorts record fields by `_fieldIndex` *at each emission site*
   (`Go/Record.hs:461`) and relies on discipline elsewhere. The rewrite makes
   ordered iteration the *only* option: the IR arrives in interned-id /
   `BTreeMap` order and codegen has no `HashMap` to iterate (L4, enforced by lint
   + the reproducibility gate in [`11`](11-testing-and-verification.md)).

Codegen is a `#![forbid(unsafe_code)]`, `HashMap`-free crate.

## 1. Deterministic emission (L4)

Every renderer appends to a shared buffer and every collection it walks is
already ordered. There is no place where hashmap iteration can leak into output.

```rust
pub fn emit_module(items: &[GoItem]) -> String {
    let mut w = Writer::new();               // wraps String + current indent
    for it in items {                        // items arrive in DCE-filtered, id order
        emit_item(&mut w, it);
    }
    w.finish()
}

/// Thin buffer; O(n) total, never O(n²). No trait objects, no per-node String
/// allocation. `push`/`line` write directly into one growing String.
struct Writer { buf: String, indent: u32 }
impl Writer {
    fn line(&mut self, s: &str) { for _ in 0..self.indent { self.buf.push('\t'); }
                                  self.buf.push_str(s); self.buf.push('\n'); }
    fn raw(&mut self, s: &str)  { self.buf.push_str(s); }
    fn block<F: FnOnce(&mut Self)>(&mut self, f: F) { self.indent += 1; f(self); self.indent -= 1; }
}
```

Fresh names (temporaries introduced by TCO reassignment, IIFE locals) were
already drawn deterministically in `lower` from a counter seeded by a pre-order
traversal (L4, 07 §4) — codegen receives them baked into the IR and never mints
one. This is the precise fix for the historical record-field nondeterminism
(`f6e3ecdd`): there is no "sort keys before iterating" step to get wrong because
there is no unordered map to sort.

### Ordering rules (invariants, tested)

| Emitted thing | Order source |
|---|---|
| Top-level items in a module | DCE-reachable set walked in `DefId` allocation order |
| Struct / record fields | `_fieldIndex` — baked into `GoTy::Struct`/`GoTypeDef::Struct` at lowering (`Go/Record.hs:461`) |
| ADT constructor tags | declaration order (§3) — tag `int` assigned once, stable |
| ~~Monomorphisation instances~~ | **N/A — there is no monomorphiser** (07 §5.1). One emit per definition; nothing to order. |
| Imports | sorted by import path |
| Map literals | keys already `BTreeMap`/`IndexMap` in the IR |

## 2. Rendering the IR

A flat match per node — a direct port of `Builder.renderExpr`/`renderStmt`
(`Builder.hs:135/227`) but reading structural `GoTy` where the Haskell code read
a `String`.

```rust
fn emit_expr(w: &mut Writer, e: &GoExpr) {
    match &e.kind {
        GoExprKind::Ident(n)          => w.raw(n.text()),
        GoExprKind::IntLit(n)         => w.raw(&n.to_string()),
        GoExprKind::StrLit(s)         => { w.raw("\""); w.raw(&escape_go(s)); w.raw("\""); }
        GoExprKind::SliceLit(elem, xs) => {
            w.raw("[]"); emit_ty(w, elem); w.raw("{");
            emit_sep(w, xs, ", ", emit_expr); w.raw("}");
        }
        GoExprKind::GenericCall(f, targs, args) => {
            w.raw(f.text()); w.raw("[");
            emit_sep(w, targs, ", ", emit_ty);      // type args are GoTy → emit_ty
            w.raw("](");
            emit_sep(w, args, ", ", emit_expr); w.raw(")");
        }
        GoExprKind::Coerce { inner, from, to, reason } => emit_coerce(w, inner, from, to, *reason),
        /* … */
    }
}

/// The single place a GoTy becomes text. Port of `renderGoType`
/// (Go/Type.hs:384) — total, no `Raw` arm to leak strings.
fn emit_ty(w: &mut Writer, t: &GoTy) {
    match t {
        GoTy::Bare(p)      => w.raw(p.go_name()),        // int/string/bool/float64/rune/[]byte
        GoTy::Unit         => w.raw("struct{}"),
        GoTy::Any          => w.raw("any"),
        GoTy::Named(n, []) => w.raw(n.text()),
        GoTy::Named(n, a)  => { w.raw(n.text()); w.raw("["); emit_sep(w, a, ", ", emit_ty); w.raw("]"); }
        GoTy::Func(ps, r)  => { w.raw("func("); emit_sep(w, ps, ", ", emit_ty); w.raw(") "); emit_ty(w, r); }
        GoTy::Tuple(xs)    => emit_tuple_ty(w, xs),       // rt.T2[…] / rt.T3[…] / rt.SkyTupleN
        GoTy::TyVar(id)    => w.raw(go_tyvar_name(*id)),  // T1, T2, …
        GoTy::Struct(fs)   => emit_anon_struct(w, fs),
    }
}
```

`emit_coerce` is where 07's justified `Coerce` node becomes the right runtime
call — and, crucially, **emits nothing when `from == to`** (the elision that
keeps the surface at the floor):

```rust
fn emit_coerce(w: &mut Writer, inner: &GoExpr, from: &GoTy, to: &GoTy, reason: CoerceReason) {
    if from == to { return emit_expr(w, inner); }        // no-op elision
    w.raw(&format!("/* {} */ ", reason.comment()));      // typed reason → the /* PROOF */ comment
    match (to, reason) {
        (GoTy::Named(n, [t]), _) if n.is("rt.SkyMaybe")  => call1(w, "rt.MaybeCoerce",  [t], inner),
        (GoTy::Named(n, [e, a]), _) if n.is("rt.SkyResult") => call1(w, "rt.ResultCoerce", [e, a], inner),
        (GoTy::Named(n, [t]), _) if n.is("[]")           => call1(w, "rt.AsListT",      [t], inner),
        (GoTy::Bare(Prim::Str), _)                       => call0(w, "rt.CoerceString", inner),
        (t, _)                                           => call1(w, "rt.Coerce",       [t], inner),
    }
}
```

## 3. The runtime ABI (L10)

Emitted Go links against the existing `runtime-go/rt` package unchanged — keeping
goroutine-Tasks, the deploy story, and the SkyDeploy moat (L10). Codegen must
target its shapes exactly. This is the ABI contract, with the runtime source of
truth:

### Effects & dispatch

| Emitted call | Runtime (`runtime-go/rt/rt.go`) | Meaning |
|---|---|---|
| `rt.AnyTaskRun(t)` | `rt.go:5718` | force a `Task` at an entry boundary (program `main`, `let _ =`, `Cmd.perform`); guarantees a `SkyResult`-shaped result |
| `rt.SkyCall(f, args…)` | `rt.go:9450` | `reflect.MakeFunc`-backed HOF/partial-application dispatch when the arity is dynamic |
| `rt.Coerce[T](v)` | `rt.go:5032` | typed assertion fast-path + reflect map→struct narrowing |
| `rt.AsListT[T](v)` | `rt.go:2136` | narrow `[]any` → `[]T` (per-element coerce) |
| `rt.MaybeCoerce[A]` / `rt.ResultCoerce[E,A]` / `rt.TaskCoerceT[E,A]` | `rt.go:359` / … | container payload narrowing |

The **program entry** wraps the Task-typed `main` unconditionally — the CLAUDE.md
"runtime auto-forces a Task-typed `main`" rule: `func main()` emits
`rt.AnyTaskRun(<entry-expr>)` with no trailing `Task.run` (that call is a no-op at
entry). Module-level `Task.run` bindings still emit their explicit force.

### Value representations codegen must match

| Sky value | Go shape | Source | Codegen rule |
|---|---|---|---|
| `Result e a` | `SkyResult[E,A]{ Tag int; OkValue A; ErrValue E }` | `rt.go:69` | `Ok`→`Tag:0`, `Err`→`Tag:1` — **tags stable, never reordered** |
| `Maybe a` | `SkyMaybe[A]{ Tag int; JustValue A }` | `rt.go:87` | `Just`→`Tag:0`, `Nothing`→`Tag:1` |
| `Task e a` | `SkyTask[E,A] = func() SkyResult[E,A]` | `rt.go:1084` | a thunk; forced by `AnyTaskRun` |
| `(a, b)` / `(a,b,c)` | `rt.T2[A,B]` / `rt.T3[…]`; arity ≥ 4 → `rt.SkyTupleN{ Vs []any }` | `rt.go:3439/3522` | `GoTy::Tuple` chooses parametric vs slice-backed by arity |
| user ADT | `T{ Tag int; SkyName string; Fields []any }` | e.g. `examples/01-hello-world/sky-out/main.go:18` | tags in declaration order; `SkyName` for diagnostics |
| `Dict`/`Set`/list | `map[K]V` / `[]T` directly | — | no wrapper type |
| `SkyValue` / `SkyAttribute` | `= any` aliases | `rt.go:3533` | the erased slots |

**Sealed-interface ADTs** (07 §3/§6) emit a Go `interface` with each variant a
struct that embeds it; the zero value is `nil`, not `T{}` (the
`_cg_sealedIfaceNames`→`goZeroValue` rule, `Go/Record.hs:142`). **iota enums**
(nullary ADTs) emit `type X = int` + `const ( … = iota )` — the *alias* form, not
a distinct type, so values flowing through `any` and asserted back to `X` succeed
(`Builder.hs:113-123`). Both of these ABI quirks are load-bearing for round-trip
soundness and are reproduced exactly (00 "compat first").

### `_fieldIndex` ordering — the ABI's positional contract

A record alias generates a positional constructor `Foo : T1 -> T2 -> Foo`; the Go
struct field order **is** that constructor's calling convention. Fields must emit
sorted by `_fieldIndex` (declaration order), never by map-key/alphabetical order,
or `Piece King White` passes args into the wrong slots and panics
(`Go/Record.hs:453-470`). In the rewrite the order is baked into
`GoTy::Struct`/`GoTypeDef::Struct` at lowering, so codegen simply iterates the
`Vec` — the ordering can't be gotten wrong at the emit site because there is no
choice to make.

## 4. String-building discipline

- **One buffer, append-only.** `Writer` owns a single `String`; every renderer
  `push_str`s into it. No `format!`-per-node into throwaway `String`s, no
  `Vec<String>` + `join` at inner nodes (the `Builder.hs:349` recursive
  `intercalate` shape is what we avoid).
- **`emit_sep`** writes separators inline rather than building a list then
  joining — O(n) total across the whole module.
- **`escape_go`** is a direct port of `Builder.escapeGo` (`Builder.hs:330`): Go
  strings are UTF-8, printable Unicode passes through, C0 controls hex-escape.
- **Reserve once.** `String::with_capacity` seeded from the item count keeps the
  buffer from re-growing on large emissions (skyshop-scale).

## 5. Reserved-Go-name rewriting

Sky's identifier rules are stricter than Go's, but Go *tolerates* shadowing
predeclared names — a footgun. Every Sky identifier that collides with a Go
keyword / predeclared name gets a trailing `_` (the CLAUDE.md "Go reserved-name
rewriting" rule; the list is `reservedGoNames`, `src/Sky/Build/Compile.hs:9766`).

Crucially, in the rewrite this happens **once, at `GoName` interning time** (07,
`GoName(Name)`), not at each emit site — so no renderer re-mangles and the same
Sky name always interns to the same Go identifier (L3 + L4).

```rust
/// Applied when interning a Go identifier, before it ever reaches the IR.
fn go_ident(sky: &str, kind: NameKind) -> String {
    match kind {
        NameKind::EntryMain        => "main".into(),           // program entry — never main_
        NameKind::TopLevel { module } => format!("{}_{}", module.go_prefix(), sky), // Mod_name
        NameKind::Local | NameKind::Param =>
            if RESERVED_GO.contains(sky) { format!("{sky}_") } else { sky.into() },
        NameKind::Field            => capitalize(sky),         // Go-exported field
    }
}

// RESERVED_GO = the four tiers of `reservedGoNames` (Compile.hs:9766):
//   init | predeclared funcs (make/len/append/panic/recover/min/max/…)
//   | 23 keywords (for/func/type/range/…) | predeclared types+consts
//   (string/error/any/int*/uint*/float*/true/false/iota/nil)
```

The **module-prefix safety net** means the reserved list only bites locals and
params: every top-level binding is `<Mod>_<name>` (`Main_view`,
`Std_Ui_layout`), which can never collide with a bare Go keyword. `init` and
`main` are the two special cases — `init = …` (TEA convention) rewrites to
`init_`; the module-`Main` binding `main` becomes the program entry `func
main()`.

## 6. The panic-safety floor

The "if it compiles, it works" invariant (00 §4) has a runtime backstop that
codegen is responsible for wiring. Every emitted `func main()` opens with:

```go
func main() {
    defer rt.LogPanicAndExit()   // panic_recover.go:46
    rt.AnyTaskRun(<entry>)
}
```

`rt.LogPanicAndExit` (`runtime-go/rt/panic_recover.go:46`) `recover()`s whatever
escaped the synchronous Sky path, runs `classifyPanic` to bucket it
(`DivisionByZero`, `TypeMismatch`, `CoerceFailure`, `ComparisonMismatch`,
`IndexOutOfRange`, `NilDereference`, `CompilerBug`, `Unexpected`), emits a
structured `Error` log line with a 4-byte errId (honouring
`SKY_LOG_FORMAT=json`), and exits 1 — instead of dumping a Go stack trace. The
reachable-from-Sky panic sites are exactly the §5 primitive-coerce floor from
[`07`](07-lowering-and-ir.md) (`rt.IntDiv`, `rt.AsInt/AsFloat/AsBool`, `rt.cmp`,
`rt.Coerce`, index-out-of-range, nil-deref).

Codegen emits the matching floors per app shape (mirroring the Haskell backend):

| App shape | Emitted floor |
|---|---|
| Sky.Cli / Sky.Tui / batch (`main = Task.run …`) | `defer rt.LogPanicAndExit()` in `func main` |
| Sky.Http.Server / Sky.Live handlers | per-request `defer`/`recover` → 500 (runtime `rt.go`), not a crash |
| `Cmd.perform` goroutines | wrapped in `rt.SafeGo` |

This is the emission-time leg of the three-leg soundness stool (CLAUDE.md §0.4):
codegen must never emit a raw panic-prone op *without* the recover net in scope.
The `lower` crate guarantees no *unjustified* coercion reaches this point (07
§6); codegen guarantees the *justified* few are still caught. Together: no
runtime panic from well-typed Sky escapes as an unstructured crash.

## What codegen guarantees to the build driver

A single deterministic Go source string per module (byte-identical across
platforms and runs), targeting the unchanged `runtime-go/rt` ABI, with reserved
names rewritten, tags/fields in stable order, and the panic floor wired. The
`project` crate ([`09`](09-runtime-and-ffi.md)) writes it to `sky-out/` and runs
`go build`. The reproducibility gate ([`11`](11-testing-and-verification.md))
compiles the corpus N× across seeds + platforms and byte-diffs the output — the
mechanised proof that L4 holds end to end.
