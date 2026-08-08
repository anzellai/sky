# v0.13 — Gap 4 Lambda Lowering Blocker

Status: design-validated, multi-day implementation pending.

## What works on `perf/v0.13`

* **Phase A1–A3**: solver instance capture, mangling, pipeline wiring.
* **Phase A5+/A5++**: call-site coercion at the typed boundary —
  `Sky_Core_Maybe_withDefault(rt.CoerceInt(0), rt.MaybeCoerce[int](m))`
  with concrete Go types at every arg position.
* **Phase A6 partial**: `_Any` fallback via `normaliseUnresolved`
  for partial-resolution call sites.
* **Phase A7 foundation**: `Mono.reachableInstances` walks transitively
  from `main`, computing the reachable instance set.  98 instances
  captured on `18-job-queue` including value references (used by
  Sky.Live's runtime), Msg constructors, ctor refs.
* **Phase A4 MVP**: per-instance specialised Go functions emitted
  ALONGSIDE the generic versions.  `Sky_Core_Maybe_withDefault__Int(
  def int, m rt.SkyMaybe[int]) int { ... }` etc. — fully concrete
  param sigs.  Currently dead code — call sites still reference the
  generic versions.
* **Phase B1–B4**: `Sky.Core.Maybe`, `Sky.Core.Result`,
  `Sky.Core.Basics`, `Sky.Core.List(non-HOF)` shipping as Sky source.

22/24 examples build clean (16-skychess + 13-skyshop pre-existing).

## What blocks "no any in emitted Go"

The user's principle requires every emitted function to have
concrete Go types — no `[T any]` generics, no `func(any) any`
lambdas.  Switching call sites to use the specialised mangled
names ALMOST works but trips two coordinated issues:

### Issue 1: Lambda lowering produces `func(any) any`

Sky lambdas (`\x -> body`) always lower to Go `func(_x any) any
{ return body }` regardless of HM-inferred input/output types.
The current `curryLambdaPat` helper doesn't take HM types as an
argument.

When the call site of a specialised function passes a lambda:

    Sky_Core_Maybe_withDefault__String("default", rt.MaybeCoerce[string](m))

The first arg expects `string`.  A Sky-lambda `\x -> x ++ "!"`
emitted as `func(_x any) any { ... }` fails Go type-check —
`cannot use func(_x any) any as string value`.

Same class as v0.12 Limitation #18 / Gap 4.

**Fix**: rewrite `curryLambdaPat` to accept (or derive from
context) the lambda's HM-inferred input + output types and emit
typed Go func literals.  Multi-day refactor that touches every
`Can.Lambda` emission site.

### Issue 2: Return-position TVars defaulted to `rt.SkyValue` before spec emission

`splitInferredSigWithReg` calls `defaultErrorTVars` BEFORE
computing `numbered` (the TVar → "T1" mapping).  For
`Maybe.andThen : (a → Maybe b) → Maybe a → Maybe b`, `b` is in
return position → defaulted to `rt.SkyValue` → never gets a `T2`
in the generic sig.

Post-process spec emission only substitutes `T1` (input position).
Result: `Sky_Core_Maybe_andThen__String_Int` emits with
`rt.SkyMaybe[rt.SkyValue]` return instead of `rt.SkyMaybe[int]`.

**Fix**: emit specs from the ORIGINAL (pre-default) annotation,
walking `Can.Def`'s body with σ applied at every type lookup.
This requires a new codegen pass that takes σ as context —
duplicates much of `exprToGo`'s logic.

### Issue 3: Recursive calls within specialised bodies

When `Sky_Core_List_foldl__Int_Int` recurses, the call should go
to itself (same mangled name).  Post-process substitution on the
generic GoFuncDecl handles SHALLOW substitution (T1 → int) but
the BODY's recursive call still references the generic name.

`Mono.specialiseFuncDecl` has scaffolding for this
(`originalName` argument rewires `GoGenericCall` to the mangled
name) but it's not yet wired into the emission path.

## Estimated scope of "full v0.13 no-any"

Multi-week.  The lambda-lowering rewrite alone touches:

* `curryLambdaPat` — accept HM-inferred types.
* `curryLambdaPatTyped` — already exists; thread through every
  `Can.Lambda` emission site.
* `exprToGo` / `exprToGoTyped` — pass expected-type context down
  through every recursive call.
* `solvedTypeToGo` — augment with σ substitution for current
  instance context.
* Spec emission — switch from post-process substitution to direct
  Can.Def walk with σ.
* Runtime kernel — add typed-T variants of every helper that
  currently takes `any`.
* Drop generic emission entirely; ensure all call sites use
  mangled names; ensure DCE pruning is sound.

Realistic timing: 2-3 weeks of focused work for a competent
compiler engineer.

## Recommendation

Ship v0.13 with the current state:

* Sky.Core.Maybe / Result / Basics / List(non-HOF) as Sky source.
* Per-instance specialisation infrastructure in place (dead-emit
  for now, ready to enable when Gap 4 lands).
* Reachability walker in place.
* Clear documentation of remaining scope (this file).

Schedule v0.14 as the "no-any-anywhere" release, with Gap 4
lambda lowering as the headline work.
