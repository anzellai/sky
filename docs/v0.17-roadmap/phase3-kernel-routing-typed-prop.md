# Phase 3 — kernel-call routing: typed-elem propagation lever

**Status**: Stage 1 (architectural survey) — NO IMPLEMENTATION

**Authoring iter**: v0.17 iter 7

**Branch tip**: e18260643 (iter 6 shipped 3 IORefs deleted)

**Probe baseline**: 26-ui-showcase main.go — 193 `rt.AsListT[...]` wraps,
183 `rt.Coerce[...]` calls.

---

## 1. Problem statement

After v0.17 Phases 1+2 closed type-directed lowering + Go generics on
parametric records, the residual `rt.AsListT[T]` wrap count on the
canonical Std.Ui stress example (`examples/26-ui-showcase`) is **193**.
The wraps DO NOT cause runtime panics — `AsListT[T]` is lossless
because `rt.AsList(src)` reflects on the value and rebuilds a typed
slice. But every wrap is:

  - **Runtime overhead** — reflect.ValueOf + per-element typed
    conversion + allocation of a new `[]T` slice
  - **Codegen noise** — humans + AI reading emitted Go see redundant
    coercion noise that obscures intent
  - **A v0.17 close-criterion #3 ratchet** — "rt.Coerce + rt.AsListT
    monotone-decreasing per iter"; this report unblocks the next big
    decrement (193 → ~70-100)

## 2. Inner-value classification (193 sites)

Profiling the 193 wraps by their innermost argument shape:

| Class | Pattern | Count | Inner-value source |
|-------|---------|-------|---------------------|
| **A** | `rt.AsListT[T](varName)` | **122** | Plain identifier — typically a function param or let-bound var |
| **B** | `rt.AsListT[T]([]any{...})` | **71**  | Slice literal — built inline at the call site |
| **C** | `rt.AsListT[T](__tco_t<N>)` | **32**  | TCO loop accumulator var |
| **D** | `rt.AsListT[T](rt.X(...))` | **14**  | `rt.*` kernel/helper-call result |
| **E** | `rt.AsListT[T](Sky_Core_*(...))` | **13** | Recursive Sky-source helper call |
| **F** | `rt.AsListT[T](func() any {...})` | **7**  | Inline anonymous `func` body |

Sum overlaps because the same wrap matches multiple classes (e.g. a
TCO acc that is also a var ref). Top-line breakdown:

```
122 var-refs   = 63% of all sites  ←  highest-value class
 71 slice-lits = 37% of all sites
```

Top 10 wraps by exact form:

```
81 rt.AsListT[rt.SkyAttribute](attrs)
42 rt.AsListT[Std_Html_Html](children)
39 rt.AsListT[Std_Html_Html](func( …
13 rt.AsListT[Std_Ui_Element](children)
11 rt.AsListT[rt.SkyAttribute](rt.List_cons( …
 9 rt.AsListT[Std_Ui_Chart_Series_R](seriesList)
 8 rt.AsListT[T1](acc)
 7 rt.AsListT[T1](__tco_t1)
 6 rt.AsListT[rt.SkyAttribute](propagatedAttrs)
 6 rt.AsListT[rt.SkyAttribute](__tco_t0)
```

## 3. THE redundancy class — paradigmatic example

The dominant pattern is `Std.Html.*` dep functions emitted as:

```go
func Std_Html_div(attrs []rt.SkyAttribute, children []Std_Html_Html) Std_Html_Html {
    return Std_Html_Html_HElement("div",
        rt.AsListT[rt.SkyAttribute](attrs),       // REDUNDANT
        rt.AsListT[Std_Html_Html](children))      // REDUNDANT
}
```

Confirmed reading at main.go:671-704. The ctor `Std_Html_Html_HElement`
is declared at line 642:

```go
func Std_Html_Html_HElement(v0 string,
                             v1 []rt.SkyAttribute,
                             v2 []Std_Html_Html) Std_Html_Html_HElement_V
```

The wrapper's param `attrs` ALREADY has Go static type
`[]rt.SkyAttribute`. The ctor's param `v1` ALSO has Go static type
`[]rt.SkyAttribute`. Go's type system accepts `attrs` directly. The
wrap is **provably redundant by static-type identity**.

This same shape ships ~81+42+13+9+6+6 = **157+ wraps** in the top-10
list alone — all from Std.Html / Std.Ui dep functions handing typed-
slice params straight to typed-slice ctors / kernel calls.

## 4. Why the wrap is emitted today

`coerceArg` / `coerceVia` (Compile.hs:14458) emits `rt.AsListT[T](e)`
whenever:

  - the call target's expected Go type is `[]T` (slice with non-`any`
    elem)
  - AND a regression-safe blanket policy is applied: "any time we want
    a `[]T` arg, wrap the source — runtime AsListT is lossless on
    already-typed inputs"

The wrap is correctness-by-belt-and-braces but ignores the obvious
optimisation: **if the source Go expression's STATIC type already
matches the target Go type, skip the wrap**.

The legacy regression dispatched at `Stage D — coerceToFieldType`
(v0.15) already does this for `rt.Coerce[T]` (the GENERIC bare-coerce
helper) when the source's Go type matches. But the SHORT-CIRCUIT
**does not yet cover the AsListT / AsMapT / MaybeCoerce /
ResultCoerce variants**. That asymmetry is the bug.

## 5. THE LEVER — static-Go-type elision in `coerceVia`

**Single architectural change**: extend `coerceVia` (Compile.hs:14458)
with a static-Go-type guard, identical in shape to the existing Stage-D
coerceToFieldType / coerceArg short-circuit, but extended to cover the
4 lossless-reconstructor variants:

```haskell
coerceVia ctx mSrc goType goArg
    -- New: elision short-circuit (Class A — var ref / static-typed expression)
    | Just srcGoTy <- goExprGoType ctx goArg
    , srcGoTy == goType
    = goArg                                                       -- WRAP ELIDED
    | otherwise = ...existing code...
```

`goExprGoType` already exists in Compile.hs and tracks each `GoExpr`'s
static Go type via:

  - **lambda params** → `lookupLambdaGoType ctx name`
  - **let bindings** → `letBindingType` (per-region SolvedRegion lookup)
  - **field access on typed-record** → field's Go type from the alias
    declaration
  - **kernel calls** → return type from `Rec._cg_funcRetType env`

The short-circuit fires when ALL of these are true:

  1. `goExprGoType ctx goArg` returns `Just srcGoTy` (not `Nothing` —
     means "Go static type known")
  2. `srcGoTy == goType` exact string-match (post-pipeline normalised)
  3. `goType` is not a generic-bare T-param (already handled by
     `isGenericTypeParam`)

**Correctness gate**: Go's type system already enforces this. If the
source's static type is `[]rt.SkyAttribute` AND we pass it to a slot
expecting `[]rt.SkyAttribute`, Go accepts it. No runtime check needed;
the elision is a pure compile-time pass-through.

## 6. Expected impact on close-criterion #3

| Class | Site count | Eligible for elision | Why eligible / not eligible |
|-------|-----------|----------------------|------------------------------|
| A var-refs | 122 | **~110** (estimate) | Most are function params w/ typed slice declared in the Go sig. Few will be `any`-typed param refs |
| B slice-lits | 71 | **~50** (estimate) | When the slice's static type is `[]any` AND target is `[]T`, the wrap IS load-bearing (it converts elements). Elision applies only when both are `[]any{}` empty OR both are typed |
| C __tco_t* | 32 | **~25** (estimate) | TCO assigns into typed locals; if `__tco_t<n>` declared with typed shape, elision applies |
| D rt.* call | 14 | **~5** (estimate) | Only when kernel's `_cg_funcRetType` registers typed return |
| E Sky_Core_*  | 13 | **~10** (estimate) | Sky-source helpers have explicit Go sigs — return type lookup works |
| F func() lit | 7 | **~2** (estimate) | Inline `func() any { ... }` returns `any`; rarely elidable |

**Conservative estimate**: elision closes **~100-150 AsListT wraps**
in 26-ui-showcase (193 → ~50-90), with similar percentages elsewhere.

Same lever ALSO covers:
  - `rt.AsMapT[V]` (analogue for `Dict`-typed slots) — same code-path
  - `rt.MaybeCoerce[T]` (analogue for `Maybe`-typed slots)
  - `rt.ResultCoerce[E, A]` (analogue for `Result`-typed slots)
  - Plain `rt.Coerce[T]` for the same static-type-match cases

So the **single change** elides 4+ wrap families simultaneously. Net
impact on `rt.Coerce` count is harder to predict (some are TVar
bridges, can't elide), but at minimum the AsListT family carries the
biggest share.

## 7. Risk register

| Risk | Severity | Mitigation |
|------|----------|------------|
| `goExprGoType` returns stale/wrong type after PR-α scope changes | High | Validate via 26-ui-showcase clean-build + cabal test sweep; gate behind `SKY_ELIDE_TYPED_WRAPS=1` env var for opt-in rollout |
| Cross-module case: dep's `goExprGoType` doesn't see entry-module typed sigs | Medium | Already mitigated by v0.17 scope cascade (PR-17b). Verify via cross-mod regression spec |
| Generic-T params (`T1`, `T2`) that LOOK typed but are erased at link time | Medium | Add `isGenericTypeParam` early-exit (already exists in compile.hs) |
| Cron-emitted typed slice via `[]any{e0, e1, ...}` where elements need typed-conversion at wrap | High | DON'T elide if source is `[]any` slice-literal — element conversion is REAL work. Gate elision strictly on exact static-type match (not just slice-shape match) |
| TCO acc shape regression — `__tco_t<n>` re-assigned across iterations | Medium | Validate TCO loops in main.go still build clean; the typed-loop assignment is `__tco_t = expr_of_typed_T`, which still triggers elision when elem types align |
| AsListT used as a NARROWING coercion (`[]any` → `[]T`) where source is genuinely heterogeneous | High | DON'T elide on `srcGoTy = []any`. Strictly require source Go type to match target Go type — this is a SUFFICIENCY condition, not a necessity |

## 8. Stage plan

### Stage 1 — Survey (THIS DOC, complete)

Cost: 0.5 iter. Output: this design doc. Deliverable: classification +
proposed lever + risk register.

### Stage 2 — Prototype on 1 wrap class (Class A var-refs, 1 dep module)

Cost: 1-2 iters.
1. Extend `coerceVia` with the static-type elision short-circuit
2. Gate behind `SKY_ELIDE_TYPED_WRAPS=1` env var
3. Validate on `Std.Html` dep module only — assert ~120 wraps disappear
4. Run cabal test, 26-ui-showcase build, runtime smoke
5. Decision gate: green? proceed to Stage 3. Red? root-cause within
   Stage 2 (don't proceed broken).

### Stage 3 — Generalise to all 4 reconstructor variants

Cost: 1-2 iters.
1. Apply same short-circuit to `rt.AsMapT`, `rt.MaybeCoerce`,
   `rt.ResultCoerce`, and the generic `rt.Coerce` path
2. Re-measure on 26-ui-showcase + 13-skyshop (Stripe SDK stress)
3. Audit any new failures (Class F func-lit may need source-derivation)

### Stage 4 — Remove the env gate + ship as default

Cost: 0.5 iter.
1. Flip default ON
2. Verify all 26 examples + cabal test
3. Commit + close ratchet

**Total estimated cost**: 3-5 iters (versus the original "multi-iter
architectural work" estimate). The classification revealed that the
dominant inner-value class is var-ref — and `goExprGoType` ALREADY
tracks var Go types via v0.15+ LowerCtx. The lever is sitting in the
existing infrastructure; we just need to fire it.

## 9. Architectural notes

**Why this is NOT a "new typed-kernel-result registry" problem.** The
original iter-7 hypothesis was that closing AsListT required propagating
typed elem info through HOF routing chains — i.e. a new registry that
tracks "kernel X returns []T". The survey reveals that **the
information is already there** — `Rec._cg_funcRetType` carries the
return type, and `goExprGoType` already reads it. The wrap is emitted
unconditionally simply because `coerceVia` short-circuits ONLY for the
generic `rt.Coerce[T]` arm, not for the AsListT / AsMapT / etc. arms.
**The lever is a 5-line guard at the top of `coerceVia`**, not a new
infrastructure.

**Why we didn't catch this sooner.** Phases 1+2 of v0.17 focused on
type-directed lowering (passing the EXPECTED type down to the lowerer)
and Go generics on records. The static-type AVAILABLE-FROM-SOURCE check
(matching emitted-Go type against expected slot type) is a separate
optimisation that didn't fall out naturally from those passes.

**Why this composes with the v0.17 PR-α reader-threading work.**
`goExprGoType` reads from `LowerCtx` (per-region SolvedRegion +
lambda-types + cg env). PR-α completed the threading; without it,
`goExprGoType` would have read stale per-module data and elided wraps
incorrectly. The lever is unblocked NOW because PR-α already shipped.

## 10. Surveyed wrap-site inventory

Compile.hs wrap-emit sites that route to `wrapAsList` or `coerceVia`'s
slice arm:

| Line | Site | Class |
|------|------|-------|
| 9474 | `exprToGoExpectGo` typed-slice arm | Stage-D channel |
| 13820 | `coerceToFieldType` slice arm | Stage-D channel |
| 14470 | `coerceVia` slice arm | **PRIMARY TARGET — extend with elision guard** |
| 15749 | `coerceArg` parametric-alias slice arm | Same as 14470 |
| 18827 | `wrapTypedReturn` slice arm | Stage-D channel |
| 20528-20749 | `kernelTypedCall` wrapAsList (11 hot sites: List.map, filter, foldl, length, head, reverse, take, drop, append, member, indexedMap, find) | HOF kernel routing — already typed at the SOURCE; wraps redundant against typed param |
| 21035 | `coerceVia` inner-slice variant | Same as 14470 |
| 21518 | `wrapElemType` rebuild path | Edge case |

The lever at `coerceVia:14470` short-circuits BEFORE the
`stripSlice goType` test fires, so all 8 wrap sites that route through
`coerceVia` get the elision for free. The standalone `kernelTypedCall
wrapAsList` sites (11 hot HOF routing arms in Compile.hs:20528-20749)
DON'T flow through `coerceVia` — they emit `wrapAsList` directly. Need
to extract the static-type guard into a shared helper:

```haskell
elideOrWrap :: LC.LowerCtx -> String -> GoIr.GoExpr -> GoIr.GoExpr
elideOrWrap ctx targetGoTy e
    | Just srcGoTy <- goExprGoType ctx e
    , srcGoTy == targetGoTy
    = e                                                           -- elide
    | otherwise = wrapAsListGo targetGoTy e                       -- existing wrap

-- Then replace each `wrapAsList elemGo goList` call site with
-- `elideOrWrap ctx ("[]" ++ elemGo) goList`.
```

This applies the same elision in both `coerceVia` AND `kernelTypedCall`
paths.

## 11. Iter budget estimate

Total: **3-5 iters** for full Phase 3 close to ~50-90 AsListT residual.

- iter 8: Stage 2 prototype (Std.Html dep, behind env gate) → ~120
  wraps closed
- iter 9: Stage 3 generalisation (Std.Ui, recursive helpers) → ~30
  more wraps closed
- iter 10: Stage 4 ship as default, env gate removed
- iter 11 (buffer): residual cleanup / regression

Net `rt.Coerce` impact will be smaller (most Coerce wraps are TVar
bridges, not static-type-match candidates), but ~30-50 Coerce sites
should close via the unified `coerceVia` elision.

**Final position**: 26-ui-showcase from 193 AsListT → ~50-70, from 183
Coerce → ~130-150. Significant ratchet on close-criterion #3.

## 12. Open questions for the architect review

1. **Should the lever fire even when source is `[]any` and target is
   `[]any` (both un-typed)?** Yes — wrap is pure noise; elision is
   correct. The wrap fires today because `coerceVia` doesn't check
   whether source AND target are both `[]any`.

2. **Does `goExprGoType` correctly handle `__tco_t<n>` accumulators?**
   Needs verification — TCO emit assigns to typed locals declared via
   `var __tco_t<n> T`, but the typed-LowerCtx tracking may not register
   them. If not, Class C (32 sites) doesn't elide. Workaround: register
   TCO acc vars in LowerCtx during emit. Estimated +0.5 iter.

3. **Can we close Class F (7 func() lit sites) in this lever?**
   Probably not — `goExprGoType` on a `GoFuncLit` returns its return
   type. Elision works iff the func returns `[]T` matching target. The
   ~7 sites likely return `any` (mixed-shape inline builders). Skip
   Class F for Phase 3; cover in a follow-up if needed.

4. **Risk of breaking Issue #521 family?** That family was about
   PER-INSTANTIATION typeParam scope. The elision is local to each
   call site — uses the THIS-FRAME goExprGoType, doesn't touch
   typeParam substitution. Confidence: low risk.
