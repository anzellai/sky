# CYCLE-01 — Developer record (Plan Item P2)

**Closes:** Audit Gap A2 (`goExprGoType` returns Nothing for
polymorphic-call results) + `fragility-audit-v0.15.3.md` item #7
residual (coerceArg branch gating on `Just srcTy <-
goExprGoType e` losing precision when the by-shape classifier
can't see the source type).

**Branch:** `feat/v0.15.x-hardening-P2-goexpr-type-poly-call`

**Target patch tag:** v0.15.8 (push is the human's gate — branch +
PR + green CI is the developer's finish line).

## Architectural diagnosis

`goExprGoType :: GoIr.GoExpr -> Maybe String` (pre-fix at
`Compile.hs:4281`) pattern-matched on the lowered Go IR
constructors. For `GoCall (GoIdent name) args` with non-zero
args (e.g. `Sky_Core_Result_andThen_...(fn, r)` lowered from a
`Result.andThen` chain), no arm matched: the `GoCall (GoIdent
name) []` arm at line 4341 required EMPTY args (zero-arg
top-level call), the `rt.*` arm at 4297 requires `rt.` prefix,
and there was no general "look up a user-named function's
return type" path.  Falls to `_ -> Nothing`.

Downstream coercions then gate on `Just srcTy <- goExprGoType
e`.  Pre-fix, this gate fails for the audit's reproducer
(`report(pipeline(5))` where `pipeline : Int -> Result Error
String`) — `coerceArg`'s `goExprGoType e == Just ty`
short-circuit at line 8550 doesn't fire, falls through to
`stripParametric "rt.SkyResult"` which emits the wasteful
`rt.ResultCoerce[Sky_Core_Error_Error, string](pipeline(5))`
wrap.  At runtime this works (the helper reconstructs the
value), but it's reflect-backed cost + extra Go code.

The HM solver KNOWS the type — it's `Result Error String` for
the pipeline call.  The structural fallback recovers this via
`inferExprType solvedTypes srcCanExpr` →
`solvedTypeToGo` → sanity-filter chain.

### Mechanics

1. Signature change:
   `goExprGoType :: Maybe Can.Expr -> GoIr.GoExpr -> Maybe String`.
   The new first parameter is the source `Can.Expr` that
   produced the `GoExpr` (when the caller has it in scope).

2. New `structuralFallback src` arm fires when the by-shape
   `shapeClassified` returns Nothing AND the source is `Just`.
   Runs `inferExprType solved src`, sanity-filters the result,
   and returns the recovered Go type.

3. **Three-tier sanity filter**:

   - `structurallySafeForFallback e`: only `GoCall (GoIdent
     name) _` where `name` doesn't start with `rt.` or `__`.
     User functions and Sky-emitted mono'd kernels are
     stable-typed return paths; runtime helpers and
     synthesised TCO/destruct identifiers aren't.
   - HM type post-substitution must NOT contain any
     unresolved TVar — the `hasUnresolvedTVar` walk chases
     TVar substitutions through `solved`.  A polymorphic
     `List a` (a unresolved) would collapse via
     `solvedTypeToGo` to `[]any` and accidentally match a
     target slot's `[]any` shape, eliding a `rt.AsListAny`
     wrap that's NEEDED at runtime (the actual value is `[]T`,
     not `[]any`).  This was the Sky.Test summarise
     regression.
   - Rendered Go type must be non-`any`, non-empty, not a
     bare generic param, not contain a generic param,
     `isEmittableGoType`.

4. **σ-recovery does NOT consume the fallback**.  The
   recovery sites at `coerceCallArgs` / `coerceCallArgsAt`'s
   VarTopLevel + VarKernel branches still pass `Nothing` to
   `goExprGoType`.  Reason: over-pinning a callee TVar from a
   typed-call arg can conflict with a sibling arg's Go-side
   inference, causing `does not match inferred type` errors at
   `go build` (`List.map` with a typed `[]Tup2` arg + an
   `any`-typed lambda — pre-P2 the σ left T1 bare and Go
   inferred `T1=any` from the lambda; P2's recovery would pin
   T1=Tup2 and Go would then reject the lambda).  The fallback
   fires at the COERCION sites (`coerceArg`'s short-circuit
   gates) where it elides wraps without affecting σ pinning.

5. **Generic-param target arm in `coerceArg` also rejects
   the fallback**.  Same over-pinning rationale: claiming a
   typed source pins `T1 = []Tup2` then forces Go's call-site
   inference into a conflict with sibling lambdas.

6. **Diagnostic mode**: `SKY_DEBUG_INFER=1` env var causes
   every Nothing-with-Just-source path to log a one-liner to
   stderr with the source region + the reject reason
   (`<shape-unsafe>`, `<infer-Nothing>`, `<unresolved-tvar>`,
   `<generic-or-any>`).  Hits log the recovered Go type.
   Wired into `scripts/verify-cli.sh` as a smoke test against
   `examples/19-skyforum` — Nothing=352 hits=62 on that
   surface.

## Sequenced steps

1. **Failing-test-first.** `test/Sky/Build/GoExprTypeInferenceSpec.hs`
   (NEW, ~90 LOC).  Builds `test/fixtures/goexpr-type-inference/`
   (a `Result.andThen` pipeline whose typed result flows into
   a `Result Error String`-typed consumer) and asserts:
   - the build succeeds + the runtime output is `ok:final`,
   - the emitted `main.go` contains NO `rt.ResultCoerce[...]`
     or `rt.Coerce[rt.SkyResult[...]]` wrap on the
     `pipeline(5)` call site,
   - the raw `report(pipeline(5))` call shape IS present.

   Confirmed both assertions FAIL on starting worktree (pre-
   fix shape: `rt.ResultCoerce[Sky_Core_Error_Error,
   string](pipeline(5))`).

2. **Stress fixture.**
   `test-files/v0.15-stress/src/Widget/PipelineResult.sky`
   mirrors the reproducer as a Widget dep module; Main.sky
   exercises it via `PR.report (PR.pipeline 5)` (positive
   path → `ok:final`) AND `PR.report (PR.pipeline (-1))`
   (negative path → `err:InvalidInput: neg`).  Stress sweep
   now reports `ALL 25 PASS` (was 23 pre-this-cycle).

3. **Signature extension.**
   `goExprGoType :: Maybe Can.Expr -> GoIr.GoExpr -> Maybe String`
   in `src/Sky/Build/Compile.hs`.  Callers updated to pass
   `Maybe Can.Expr`:
   - Strategic sites (pass `Just`): `coerceCallArgsAt`'s
     `coerceOne` + `coerceFallback`, `kernelCoerceArg`'s
     catch-all, `lowerArgExpect`.
   - Conservative sites (pass `Nothing`): `wrapTypedReturn`,
     `coerceToFieldType`, all σ-recovery sites, `coerceArg`'s
     generic-param-target branch, internal binop recursive
     walks, `isParametricCompatibleSource`, the TCO-tmp
     coercion at `tcoJump`.

4. **`coerceArg` signature extension.**
   `coerceArg :: Maybe Can.Expr -> GoIr.GoExpr -> String -> GoIr.GoExpr`.
   Threaded through 8 call sites; `__pN` / `__pp` synthesised
   identifiers pass `Nothing`.

5. **Sanity filter triple-gate** in
   `goExprGoType.structuralFallback`:
   - `structurallySafeForFallback ge`,
   - `hasUnresolvedTVar solved ty`,
   - `solvedTypeToGo ty` is non-`any`, non-empty, non-generic,
     non-generic-bearing, emittable.

6. **`SKY_DEBUG_INFER` instrumentation** with `unsafePerformIO
   + lookupEnv` gating.  `scripts/verify-cli.sh` adds a
   smoke step that builds skyforum under the flag.

## Files touched

- `src/Sky/Build/Compile.hs` — signature change + structural
  fallback + diagnostic instrumentation + 30+ caller updates.
- `test/Sky/Build/GoExprTypeInferenceSpec.hs` (NEW).
- `test/fixtures/goexpr-type-inference/sky.toml` (NEW).
- `test/fixtures/goexpr-type-inference/src/Main.sky` (NEW).
- `test/Spec.hs` — register new spec.
- `sky-compiler.cabal` — register new spec module.
- `test-files/v0.15-stress/src/Widget/PipelineResult.sky` (NEW).
- `test-files/v0.15-stress/src/Main.sky` — import + 2 assertions.
- `scripts/verify-cli.sh` — SKY_DEBUG_INFER smoke step.
- `docs/v0.15.x-hardening/implementations/CYCLE-01-P2-developer.md` (THIS).

## Verification evidence

- `cabal test --match=GoExprTypeInference` — **2/2 PASS** (was
  2/2 FAIL pre-fix).
- Full `cabal test` (excluding LSP-driver / VerifyAll /
  EmbeddedRuntime / EmbeddedInspector / Cli per release-skip
  pattern) — **304 examples, 0 failures, 1 pending**.
- 19-example sweep — **all green** (`scripts/example-sweep.sh
  --build-only`).
- 3 representative examples (12-skyvote, 13-skyshop,
  19-skyforum) — clean build.
- `scripts/verify-cli.sh` — **14 pass / 0 fail / 1 skip**
  (Fyne GUI skipped).  SKY_DEBUG_INFER smoke reports
  Nothing=352 hits=62 on skyforum.
- v0.15-stress runtime — **ALL 25 PASS** (added 2 new
  assertions for P2 reproducer).
- `sky check examples/12-skyvote` — **No errors found.**
- `sky fmt` on every new `.sky` file — two-pass byte-identical.

## Risk register

- **Sanity filter completeness**.  The structural fallback's
  three gates collectively reject every shape that would
  over-pin or lose precision, but the audit's
  `<shape-unsafe>` set is hand-curated.  Future Sky shapes
  (e.g. a new lowering for record-update at a polymorphic
  slot) MAY need a new gate entry.  Mitigation: the
  conservative-by-default mode (Nothing returns no precision,
  causing keep-the-wrap) keeps soundness intact even on
  uncovered shapes; new shapes show up as "missing precision"
  not "miscompile".

- **σ-recovery non-consumption is load-bearing**.  Passing
  `Just` to the σ-recovery sites caused the
  Sky.Test.summarise regression (`List.map` over a typed
  filter result with an `any`-typed lambda).  The
  `Nothing`-only restriction at those sites is documented at
  every call line.  Any future "let's enable structural
  fallback there" change MUST add a unifyGoTypes-aware
  alternative.

## Cycle log line (appended on PR open)

```
CYCLE-01-P2 | <ts> | Gap A2 + prior #7 residual CLOSED | PR #<id> green | branch ready for tag v0.15.8
```
