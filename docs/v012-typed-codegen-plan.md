# Typed Codegen — Closing Gap 3 + Gap 4 Properly

**Status**: planning artefact for the v0.12.x typed-codegen overhaul
(2026-05-10). Gaps 3 and 4 from the v0.12 grilling session — both
genuinely multi-week, both deeply coupled.

## Executive summary

The user authorised closing both gaps on `exp/tea-core` after the
v0.12 LSP / Tui / verify work landed. Initial assessment underestimated
the coupling: closing Gap 3 (typed kernel routing) alone gives
limited benefit because Sky lambdas still lower to `func(any) any`
(Gap 4's territory). The reflect.MakeFunc cost just relocates from
inside `List_mapAny`'s body to the call-site adapter.

**Therefore**: Gaps 3 and 4 ship together, in a single coordinated
overhaul, across multiple commits within a dedicated workstream.
Estimated scope: 3-5 working days of focused effort.

## Why they couple

| Layer | Currently | Goal |
|---|---|---|
| Sky surface | `List.map (\x -> x + 1) xs` | unchanged |
| Sky-stdlib sig | `List.map : (a -> b) -> List a -> List b` | unchanged |
| Lowerer call site | `rt.List_mapAny(λ, xs)` | `rt.List_mapT[A, B](λ', xs')` |
| Lambda emit | `func(x any) any { ... }` | `func(x A) B { ... }` |
| Slice on disk | `[]any` (from runtime List_map output) | `[]B` (typed) |

Migrating ONLY the call site (Gap 3) without ALSO migrating the
lambda emission (Gap 4) means the typed call site has to coerce a
`func(any) any` to `func(A) B` via reflect.MakeFunc — same cost as
the per-element SkyCall in `List_mapAny`'s body, just relocated.
Migrating ONLY the lambda (Gap 4) without the call site (Gap 3)
means the typed lambda gets coerced back to `any` at the call site
boundary. **Both must move together for the win to materialise.**

## Pre-existing infrastructure (already landed)

* Typed runtime variants exist for the hottest containers:
  `List_mapT`, `List_filterT`, `List_foldlT`, `List_lengthT`,
  `List_headT`, `List_dropAnyT`, `Dict_emptyT`, `Dict_insertT`,
  `Dict_getT`, `Dict_removeT`, `Dict_memberT`, `Dict_keysT`,
  `Dict_valuesT`, `Dict_mapT`. All in `runtime-go/rt/rt.go`.
* `solvedTypeToGo` converts most HM types to Go types (handles
  primitives, List, Dict, Maybe, Result, Task, named records).
* `splitFuncType` extracts arg/return types from a function type.
* `_cg_funcParamTypes` / `_cg_funcReturnTypes` track per-binding
  type info during typed codegen.
* `genericParams` already emits type parameters for kernels marked
  `_ki_typed=True` — but currently substitutes `[any, any]` instead
  of real types.

## Phased execution

### Phase 1: type-inference plumbing (~1 day)

**Goal**: every `Can.Expr` walked by the typed-codegen path knows
its inferred Go type at emission time.

* Add `inferGoType :: Solve.SolvedTypes -> Can.Expr -> String`
  that returns the Go type for an expression, defaulting to
  `"any"` when the type is not concrete or not derivable.
* Cover: `VarLocal` (lookup in types), `VarTopLevel` (lookup in
  funcReturnTypes), `Int`/`Float`/`Str`/`Bool` literals (trivial),
  `Lambda` (compute from body's inferred type), `Call` (compute
  from callee's return type), `If`/`Case` (compute from arm types).
* Add unit tests: 6-8 cases covering each constructor.

### Phase 2: lambda lowering — Gap 4 (~1-2 days)

**Goal**: Sky lambdas lower to typed Go functions when their HM
types are concrete.

* `curryLambdaPat` gains an optional typed-Go-signature parameter:
  if provided, emit `func(x A) B { ... }` instead of
  `func(x any) any { ... }`.
* The lambda body's identifier references coerce `any -> A` at
  param boundaries via `rt.Coerce[A]` (existing helper).
* The body's return value coerces back if needed.
* Audit every callback shape:
    * onClick/onSubmit handlers (~50 sites)
    * view fragments returning `Element msg` (~80 sites)
    * Cmd.perform / Task.andThen continuations (~30 sites)
* Each shape gets a regression test asserting the emitted Go
  signature matches the expected typed shape.

**Risk**: half-typed callbacks — a typed-emitted helper accepts a
typed callback, but a USER-defined helper accepts the legacy
`any`-typed shape. The fix is the typed surface forwards to the
any surface via reflect.MakeFunc only at the boundary between
typed and untyped HOFs. This is the "reverse" of the current
adapter direction; net adapter count stays at 11 or drops, never
climbs.

### Phase 3: kernel routing — Gap 3 (~2-3 days)

**Goal**: every `Can.VarKernel` call emits a typed routing call
when both args' types are concrete.

* New helper `kernelTypedCall :: SolvedTypes -> [Can.Expr] -> String -> String -> Maybe GoExpr`
  that returns `Just typed-call` when:
    * The kernel has a typed `*T` runtime variant.
    * All relevant type args can be derived from the call site
      (via `inferGoType` from Phase 1).
* The call site emits `rt.List_mapT[int, int](typedλ, typedXs)`
  instead of `rt.List_mapAny(λ, xs)`.
* Per-kernel migration list (15 hottest, ~1 hour each):

  | Kernel | Existing typed variant | Notes |
  |---|---|---|
  | List.map | List_mapT[A, B] | needs typed λ from Phase 2 |
  | List.filter | List_filterT[A] | needs `func(A) bool` λ |
  | List.foldl | List_foldlT[A, B] | 2-arg λ |
  | List.foldr | (NEW) List_foldrT[A, B] | mirror of foldl |
  | List.length | List_lengthT[A] | trivial |
  | List.head | List_headT[A] | returns SkyMaybe[A] |
  | List.reverse | List_reverseT[A] | trivial |
  | List.take | List_takeT[A] | trivial |
  | List.drop | List_dropT[A] | trivial |
  | List.append | List_appendT[A] | trivial |
  | Dict.get | Dict_getT[V] | returns SkyMaybe[V] |
  | Dict.insert | Dict_insertT[V] | trivial |
  | Dict.member | Dict_memberT[V] | returns bool |
  | Maybe.map | (NEW) Maybe_mapT[A, B] | needs typed λ |
  | Result.map | (NEW) Result_mapT[E, A, B] | needs typed λ |

  Each kernel gets:
    1. Typed runtime variant (often exists; new for `foldr`,
       `Maybe_mapT`, `Result_mapT`).
    2. Kernel registry entry flipped to `_ki_typed=True`.
    3. `kernelTypedCall` case for the kernel.
    4. Regression test verifying the emitted Go calls the typed
       variant when types are concrete.

### Phase 4: measurement + sweep (~half a day)

* Run benchmark on the existing example sweep with `time` /
  `pprof`. Compare against baseline (current any-routing).
* Acceptance: ≥ 30% perf improvement on hot list-processing
  examples (`19-skyforum`'s feed render, `13-skyshop`'s product
  grid). If less, identify the remaining bottleneck (likely a
  kernel still routed through any).
* Update the **measured** residual `any`-routed-kernel count in
  CLAUDE.md after each commit.

### Phase 5: documentation + release (~half a day)

* `docs/typed-codegen.md` — user-facing description of the typed
  routing contract.
* CLAUDE.md "Typed-codegen TODO" section either DELETED (gap fully
  closed) or DOWNGRADED to "long-tail kernels still on the
  any-route" with the residual list.
* CHANGELOG entry describing the v0.12.x perf improvement.
* Tag and push.

## Honest staging — what each session looks like

This work is genuinely multi-day. To prevent half-merged state, each
phase commits as a single push:

| Session | Output |
|---|---|
| 1 | Phase 1 (`inferGoType` + unit tests). Pushed. |
| 2 | Phase 2 (typed lambdas + audit + first regression). Pushed. |
| 3 | Phase 2 follow-on (callback-shape sweep + remaining tests). Pushed. |
| 4 | Phase 3 batch 1 (List.map / filter / foldl). Pushed. |
| 5 | Phase 3 batch 2 (List.* tail). Pushed. |
| 6 | Phase 3 batch 3 (Dict.* + Maybe.* + Result.*). Pushed. |
| 7 | Phase 4 (benchmarks). Pushed. |
| 8 | Phase 5 (docs + release tag). Pushed. |

Each session ends with green: `cabal test`, `scripts/lsp-test-nvim.sh`,
example sweep. mem-guard runs throughout.

## Risk register

* **Lambda audit miss**: a callback shape we don't update means a
  typed → untyped boundary that accidentally bridges via
  reflect.MakeFunc. Mitigation: regression test per callback shape;
  end-of-phase grep `reflect.MakeFunc` count must equal pre-phase
  count or drop, never climb.
* **HM type-info gaps**: some Can.Expr nodes don't propagate their
  inferred type to the lowerer's environment. Mitigation: Phase 1
  unit tests cover every node; gap forces a no-op fallback to
  current any-routing (safe).
* **Circular dependency on docs**: `docs/skytui` and other v0.12
  surfaces reference the kernel routing. Need to re-check after
  Phase 3 lands.
* **mem-guard kill**: deep-recursive type inference might generate
  very large Constraint trees on some examples. Mitigation: the
  v0.12 structural solver budget catches pathological cases at the
  HM layer; codegen has no equivalent. If we hit a wall, scope back
  to a per-module type-inference cache.

## Decision needed before starting

1. Is the multi-session pace acceptable, or should we revisit and
   accept the current "5% CPU + 11 reflect adapters" state as
   v0.12-shipping behaviour and target Gap 3+4 for v0.13?
2. If we proceed: dedicated workstream branch
   `feat/typed-codegen-overhaul` off `exp/tea-core`, or commit
   directly to `exp/tea-core`?

The work is real and worthwhile. It needs scheduled time + clear
exit criteria, not a one-off "let's see how far we get" session.
