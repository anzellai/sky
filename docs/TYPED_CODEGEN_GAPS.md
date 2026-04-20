# Typed-codegen gap audit — 2026-04-20

Documents the residual "compiles but doesn't work" gaps on
`feat/typed-codegen` after the v0.9 runtime + compiler fixes. Each
section: what the class is, which file:line exhibits it, and which
P0/P1/P2 bucket the fix belongs to.

Written as an actionable plan for the next session. Do NOT treat as
exhaustive — the hunt was systematic but the Sky codebase is large.

---

## Why "if it compiles it works" still leaks

The v0.9 typed-codegen pass added concrete Go signatures end-to-end
(`func f(name string, age int) rt.SkyResult[Error, Profile]`). Runtime
kernels, however, were all written for the any-heavy v0.7 path and
assume `[]any` / `map[string]any` / `SkyResult[any, any]` as the
universal runtime shape. The result: typed-codegen emits strongly-typed
Go that calls any-kernels that type-assert `v.([]any)` unconditionally,
and panic when the caller passed `[]T`.

The 10 bugs sky-uptime + sky-chat + 08-notes-app hit this session are
all instances of this impedance mismatch. This document catalogues the
un-hit instances.

---

## Part 1 — Classes of already-hit bugs

| Class | Representative panic | Where the fix landed |
|---|---|---|
| Runtime helper with narrow type assumption | `rt.Concat: expected []X, got string` | runtime-go/rt/rt.go `Concat` now handles typed slices |
| HM accepting unsound program | `interface {} is int, not struct {}` — `Result Error ()` vs body returning `Result Error Int` | Not fixed — dep SolveError still swallowed (Compile.hs:413) |
| Codegen emits raw slice assert | `[]rt.SkyADT not []interface{}` in cons pattern | Fixed in PCons/PList; tuple pattern still at risk |
| Stale import / short-name collision | `rt.HttpResponse not rt.SkyResponse` | Fixed for Response via qualifiedRuntimeTypedMap; bare runtimeTypedMap still latent |
| Kernel expected wrong arg shape | `rt.AsBool: expected bool, got struct {}` on `Attr.required ()` | Fixed via `boolAttrPresent` |
| Nil-handlers map in render path | `assignment to entry in nil map` | Fixed in `Html_render` |
| Curried lambda not wrapped recursively | `reflect.Value.Call: call of nil function` | Fixed in `adaptFuncValue` |
| Silent-drop in reflect helpers | `RecordUpdate` dropped `[]any → []T_R` | Fixed via `narrowReflectValue` fallback |
| Unbounded recursion in reflect walker | `stack overflow in walkGob` | Fixed with type-set + depth cap |
| Canonical-home mismatch | `Maybe Db.Db` vs kernel `Db` (empty home) | Fixed via `unifyStructure` home relaxation (but see P0-2 below — the laxity is now a new problem) |

---

## Part 2 — Un-hit instances (prioritised)

### P0 — Class fixes (one change removes N bugs)

**P0-1. Drop `runtimeTypedMap` bare-name lookups.**

`src/Sky/Build/Compile.hs` calls `lookup name runtimeTypedMap` in 9
places (2375, 2408, 2479, 2488, 2630, 2651, 3049, 3075, 3176, 5076).
Every entry is ambiguous on user-module name collision. Any user-
defined `type Session = { ... }`, `type Route = ...`, `type Decoder a`,
`type Store`, `type Stmt`, or `type Conn` silently maps to the kernel
Go type.

Fix: expand `qualifiedRuntimeTypedMap` to cover all entries (home +
name), and remove the bare lookup.

**P0-2. Kernel types need real canonical homes; retire the empty-home
relaxation.**

`src/Sky/Type/Unify.hs:91–109` currently treats `Canonical ""` as
matching any home. That was added to fix `Maybe Db.Db` unification
(ep06) but silently lets unrelated same-named types unify. E.g. any
user `type Response = { ... }` unifies with kernel Response; user
`type Db = Int` unifies with kernel Db.

Fix: give kernel types a synthetic home (`ModuleName.kernel "Db"`
etc.), update the canonicaliser's fallback in `Canonicalise/Type.hs:94`
to produce the same canonical, and drop the empty-home special case.

Affected kernel sigs in `src/Sky/Type/Constrain/Expression.hs`:
Value (1235, 1239), Route (1452, 1462), Db (1562, 1568, 1571, 1579,
1589), VNode/Attribute (1644, 1648), Cmd/Sub/Decoder (1654, 1655,
1662, 1663, 1669).

**P0-3. Route every any-variant list kernel through `rt.AsList`.**

`runtime-go/rt/rt.go:1670–1733`: `List_map`, `List_filter`,
`List_foldl`, `List_foldr`, `List_length`, `List_head`, `List_take`,
`List_drop`, `List_reverse`, `List_append` all do
`list.([]any)` unconditionally. Typed codegen passing `[]T_R`
panics.

Fix: one-line change per helper — replace `list.([]any)` with
`AsList(list)`. `AsList` already widens typed slices to `[]any` via
reflect. Same class as the `Concat` fix already applied.

**P0-4. Route Set any-variants through typed storage.**

`runtime-go/rt/stdlib_extra.go:48–120`: every `Set_*` any-variant does
`.(SkySet)`. Typed codegen's `Set.fromList [1,2,3]` produces a `[]int`
(through `Set_fromListT`) — subsequent `Set.insert 4 s` dispatches to
the any-variant, which panics.

Fix: pick ONE storage format (typed slice or SkySet). If SkySet is
kept for back-compat, the any-variants need a typeswitch to handle
both source shapes.

**P0-5. Promote dep `SolveError` to fatal.**

`src/Sky/Build/Compile.hs:413, 429–432` catches dep-module HM errors
and returns an empty parameter-types map. A user `Lib.Db.initSchema :
Db -> Result Error ()` over a body returning `Result Error Int`
compiles silently; the Go emitter then writes `rt.SkyResult[Error,
struct{}]` with an `int` source and it all blows up at runtime.

Fix: either propagate dep `SolveError` to the top-level error stream
(preferred), or at minimum log it at default verbosity so users know
their dep failed.

**P0-6. Typed-tuple pattern emits wrong assertion.**

`src/Sky/Build/Compile.hs:4696` emits `any(subject).(rt.SkyTuple2)`
when destructuring a 2-tuple. `rt.SkyTuple2` is a type alias for
`T2[any, any]`. Typed codegen can produce `T2[int, string]` (concrete
parametrised instantiation) at `solvedTypeToGo` line 5104. These are
DIFFERENT Go types; the assertion panics.

Fix: emit `rt.TupleV0(subject)`/`rt.TupleV1(subject)` reflect-based
accessors (analogous to `ResultOk`/`AdtField`), or propagate the
element types so we emit `subject.(rt.T2[int, string]).V0`.

**P0-7. Task combinators still do `.(SkyResult[any, any])`.**

`runtime-go/rt/rt.go:3379, 3404, 3424`: `Task_sequence`, `Task_parallel`,
`Task_map` assume any-parameterised `SkyResult`. Typed codegen can emit
`SkyResult[string, Foo]` which is a distinct generic instantiation.

Fix: wrap these asserts in `anyResultView` / `ResultCoerce` (the same
pattern we use for `ResultCoerce` on OkValue/ErrValue access).

### P1 — Individual targeted fixes

- **Compile.hs:4996** — `GoTypeAssert concatExpr "string"` panics if
  Concat returns a slice (new possibility after the typed-slice fix).
  Replace with `rt.AsString(concatExpr)`.
- **Compile.hs:4036** — `coerceArg`'s final fallback
  `any(e).(erasedTy)` panics on any non-primitive target type that
  isn't a named runtime type. Route through `rt.Coerce[erasedTy]`
  for struct/alias targets.
- **Unify.hs:135–166** — `unifyRecords` creates a fresh extension var
  when only one side has extra fields, accepting `{ name, age }` against
  a `{ name }`-annotated expectation. This is row-polymorphism by
  accident; the annotation should be strict. Decide: keep laxity +
  document, or tighten so `sky check` surfaces the extra field.
- **live.go:1602, 1671, 1795, 1819** — `sky_call(app.view, model).(VNode)`
  is unchecked. Add a typeswitch with fallback so debug paths don't
  panic.

### P2 — Defensive hardening

- **Grep gate on emitted `main.go`.** The non-regression rule is
  already documented in `CLAUDE.md`: "sky-out/main.go files contain no
  `any(body).(T)` patterns outside the `Coerce*` helpers". Turn that
  into a `sky verify` step that fails the build on finding one.
- **Short-name collision fixtures.** Add a `test-files/` entry per
  kernel type (`Db`, `Route`, `Session`, `Response`, `Request`,
  `Decoder`) with a user type of the same name. Verify that both
  compile with the right `runtimeTypedMap` resolution.
- **Extend `walkGobSeen` pattern to all reflect walkers.** Currently
  only session gob walking has the seen-set + depth cap. `Coerce`,
  `narrowReflectValue`, `coerceMapValue`, `coerceSliceValue`,
  `unwrapAny`, `deepEq` all recurse without bound; a Sky alias
  producing a cyclic Ok chain (e.g. `type Wrapped = Ok Wrapped`) will
  loop forever.
- **`rt.MustAssert[T](v, site string)` helper.** Replace every raw
  `.(T)` in runtime-go with a helper that panics with a specific site
  string and a suggested fix (e.g. "expected `[]T` at List_map;
  pass via `rt.AsListT[T]`"). Converts Go's opaque `interface
  conversion` errors into actionable Sky-level diagnostics.
- **Document both bool-attr conventions in kernel sig.** Currently
  `Attr.required` accepts bool OR unit — tighten via the HM
  constraint to `Bool -> Attribute` so `sky check` flags the unit-
  arg form. (Breaks back-compat; guarded rollout.)

---

## Part 3 — Why these are "class" bugs

Every P0 item removes MULTIPLE future bug reports:

- **P0-1 + P0-2 together** eliminate the short-name collision class.
  That's Response, Request, Session, Db, Route, Decoder, Stmt, Row,
  Conn, Store — any user-defined type with a kernel's short name.
- **P0-3** removes the typed-slice panic in every list operation —
  10+ kernel helpers that will otherwise break as users annotate more
  functions.
- **P0-4** does the same for sets.
- **P0-5** unblocks a whole category of user-visible annotation bugs
  where the compiler says OK but runtime disagrees.
- **P0-6** eliminates the typed-tuple panic class entirely.
- **P0-7** same for Task combinators.

Remediating P0 together would take the branch from "mostly works"
to "is it compiles it works holds with high confidence" across every
example we have.

---

## Where we are

Current state of runtime narrowing primitives (after v0.9-dev):

- `rt.Coerce[T]`                ✓ typed + recursive
- `rt.ResultCoerce[E,A]`        ✓ typed + recursive
- `rt.MaybeCoerce[A]`           ✓ typed
- `rt.AsListT[T]`               ✓ typed
- `rt.AsMapT[V]`                ✓ typed + stringify fallback
- `rt.AsDict`                   ✓ reflect-based
- `rt.AsList`                   ✓ reflect-based (typed-slice-safe)
- `rt.Concat`                   ✓ typed-slice-safe (after v0.9-dev)
- `rt.RecordUpdate`             ✓ with narrowReflectValue fallback
- `rt.adaptFuncValue`           ✓ recursive
- `rt.walkGob`                  ✓ depth-bounded + cyclic-safe
- `rt.narrowReflectValue`       ✓ recursive
- `rt.coerceMapValue`           ✓
- `rt.coerceSliceValue`         ✓
- `Attr_disabled/checked/...`   ✓ bool-or-unit via `boolAttrPresent`

Still raw-asserting (P0-3 through P0-7):

- Every any-variant list kernel
- Every any-variant set kernel
- Task_sequence/parallel/map
- Typed-tuple destructuring in codegen
- `coerceArg` fallback for non-primitive targets
- Concat-result string assert at Compile.hs:4996
