# v0.17 — Strict-HM arity gate (Limitation #7 negative-arm close)

**Status:** DESIGN SPEC — awaiting implementation across multiple iters.
**Branch:** `feat/v0.17-fully-typed-codegen`
**Banked:** 2026-06-20 at iter 28 entry per per-commit grill verdict.
**Goal:** Close the 4 NEGATIVE arms of `Sky.Type.StrictHmArityGateSpec`
(k-a, k-b, u-a, u-b) — currently `pendingWith` because the gate
implementation requires new `CArityMismatch` constraint + solver
arm + error pipeline render path.

The 4 POSITIVE arms (h-a, p-a, wp-a, wa-a) are LIVE as of iter 27
commit `9f8a22da` and serve as permanent regression guards.

---

## Why this is multi-PR (not a one-shot inline change)

The iter 28 pre-implementation grill identified 3 blockers and 1
verification task that turn the naive inline-at-`constrainCall`
proposal into a multi-PR refactor:

### BLOCKER — `CBadType` does not exist

`Sky.Type.Type.Constraint` has constructors `CEqual`, `CTrue`,
`CAnd`, `CLet`, `CForeign`, `CApp`. There is no clean way to emit
a typed-arity-mismatch error from `constrainCall` today. Two
options surfaced:

* **(a) Abuse `CEqual` to force a generic mismatch** — fast but
  gives users a generic type error (`expected X got Y`) instead
  of an actionable diagnostic ("cannot call : T with ()").
* **(b) Add new `CArityMismatch !Region !String !Int !Int`
  constructor** to `Sky.Type.Type` + matching arm in
  `Sky.Type.Solve.solveHelp` + render path in
  `Sky.Type.Error` / report pipeline. Right architecture.

Per CLAUDE.md non-negotiable #4 ("no-deferral", "the architecturally
correct approach"), option (b) is the only acceptable path.

### BLOCKER — `Instantiate.fromAnnotation` API mismatch

`fromAnnotation :: Int -> T.Annotation -> IO (T.Variable, [T.Variable])`
returns a UF Variable, not a `T.Type`. The grill's proposed
"instantiate then peel TLambda chain" mis-reads the API. The peel
must walk the `T.Type` body of `T.Forall _ ty` directly:

```haskell
declaredArity :: T.Annotation -> Int
declaredArity (T.Forall _ ty) = go 0 ty
  where
    go !n (T.TLambda _ to) = go (n+1) to
    go !n _ = n
```

Pure structural walk. No fresh UF vars needed for the gate
purpose. Drop `fromAnnotation` from the gate design.

### REVISION — Scope reduction

Drop `Can.VarLocal` from the gate's surfaces. The (1)(c) comment
at `src/Sky/Type/Constrain/Expression.hs:230` lists VarLocal as a
surface but that scope is too aggressive: local arity mismatches
already surface via standard `CEqual` unit ↔ non-unit unification.
Only gate on `Can.VarKernel` + `Can.VarTopLevel`.

### VERIFICATION — Cross-module HeadAlias safety

The h-a positive (Sky-side spec) is SAME-MODULE — defines
`myHandler : Handler` in Main and calls it from Main's `main`. The
gate-fire path on CROSS-module HeadAlias (dep module defines
`myHandler : Handler`, entry imports + calls it) is NOT covered
by the existing positive arm.

Risk: if `globalExternals` annotations bypass `unfoldHeadAlias`
(canonicalisation at `src/Sky/Canonicalise/Module.hs:1757`), the
gate sees `TType "Handler"` instead of the unfolded `TLambda
Request (Task Error Response)`. Step (3) peel returns D=0, and a
user calling the handler as `myHandler req` (S=1, req != Unit)
does NOT trigger the unit-arg condition — so h-a same-mod survives.
But a user TYPO `myHandler ()` (S=1, IS unit) would be REJECTED
with a misleading diagnostic ("declared arity 0") — that's
arguably correct behaviour (the typo IS a type error), but the
diagnostic must point at "declared as Handler (alias for Request
-> Task Error Response)" not "declared as 0-arity value".

Verification task: trace `Compile.hs:7662` (the externals
collection) to confirm `T.Annotation` value comes from
canonicalised `Can.TypedDef.retTy` not from a raw Src parse.
Either confirm safe + ship the gate, or add an unfold-pre-gate
step to the externals lookup path.

---

## Migration plan (multi-iter)

### PR-A — `CArityMismatch` constraint scaffolding (iter 29)

1. Add `CArityMismatch !Region !String !Int !Int` to
   `Sky.Type.Type.Constraint`. (`String` = binding name for
   diagnostic; first `Int` = declared arity D; second `Int` =
   supplied arity S.)
2. Add matching arm in `Sky.Type.Solve.solveHelp` —
   `CArityMismatch r name d s` emits a typed-error record into the
   solver's error accumulator, does NOT halt solving (so later
   constraints still report).
3. Add render path in `Sky.Type.Error` (or wherever solver errors
   render): "Arity mismatch — `{name}` declared as {d}-arg, called
   with {s} args. Hint: if you meant the bare value, drop the
   `()`; if you meant a 0-arg call, check the binding's signature."
4. Empty constraint at first — no callers wire it up yet. Existing
   cabal-tests stay green (no behaviour change).

### PR-B — `declaredArity` helper + cross-module externals verification (iter 30) — SHIPPED

Status: **SHIPPED 2026-06-21**. Three artifacts landed:

1. **Pure helper.** `Sky.Type.Constrain.Expression.declaredArity ::
   T.Annotation -> Int` — exported (added to module export list).
   Locked by `Sky.Type.DeclaredArityHelperSpec` (9 cases):
   - bare-value shapes return 0
   - 1-arg / 2-arg / 3-arg arrows return the right count
   - polymorphic Forall returns the same arity as monomorphic
     (helper is structural; caller does the wildcard-`any` gate)
   - wildcard-only Forall (`view : Model -> any`) returns 1
   - unfolded HeadAlias shape (`TLambda Request (Task Error
     Response)`) returns 1 — the load-bearing PR #123 anchor

2. **Externals trace verified safe.** The path from a dep
   `myHandler : Handler` declaration to the entry module's
   `globalExternals` lookup is:

       Canonicalise/Module.hs
         canonicaliseTypedDef  -- includes unfoldHeadAlias via
                               -- arrowResultN + arrowArgs
       → Can.TypedDef name freeVars patterns body retTy
         (retTy is now TLambda-shaped, head TAlias unfolded)

       Sky.Type.Solve  (solving the dep module)
         emits T.Type per region, including the dep's TypedDefs
         carrying the unfolded TLambda body

       Compile.hs:1962 (continueCompile entry-module branch)
         depSolved : [(String, Map.Map String T.Type)]
                     ← Solve.solveWithInstancesAndRegionsImpls

       Compile.hs:7866 buildCrossModuleExternalsWithMods validDeps depSolved
         maps T.Type → T.Annotation via:
           generaliseToAnnotation . fixupHomes
         The fixupHomes pass only rewrites empty-home TType nodes
         to their real defining module — it does NOT re-fold
         TLambda chains or re-introduce TAlias heads.

       Sky.Type.Constrain.Expression:globalExternals
         IORef stores the cross-module Map keyed by (modName,
         name).  declaredArity walks the unfolded TLambda body
         → returns 1 for cross-module `myHandler : Handler`.

   Same-module path uses `globalSameModAnnots` (collected by
   `collectSameModAnnots` over the entry module's own decls) —
   same canonicalisation pipeline, same head-alias unfold.

   No pre-gate unfold step needed.  PR-C can consult
   `declaredArity` against either `globalExternals` or
   `globalSameModAnnots` and trust the result.

3. **Cross-module regression.** New `h-a-cross` positive in
   `Sky.Type.StrictHmArityGateSpec`: defines `Handler`
   (re-exported from `Sky.Http.Server`) + `myHandler : Handler`
   in dep module `Lib.Handlers`; entry imports + references
   `Handlers.myHandler`.  Compiles clean — locks the externals
   trace above so a future refactor breaking the unfold
   pipeline fails fast at this gate before reaching PR-C/PR-D.

PR-B is purely additive — no caller wires the helper yet.
Existing cabal-tests stay green (no behaviour change).  The
gate-fire wiring lands in PR-C.

### PR-C — Wire gate at `constrainCall` for k-a + u-a (iter 31)

1. At `constrainCall` (line 870), after building funcType/argTypes
   but BEFORE the existing constraint chain, peek the func head.
2. If `func` is `Can.VarKernel modName funcName` or
   `Can.VarTopLevel home funcName`:
   * Look up annotation via `lookupKernelType` / `ffiKernelTypeRef`
     (kernel) or `sameModAnnots` / `globalExternals` (top-level).
   * Wildcard-`any` check: `any (/= "any") freeVars` per the
     existing pattern at line 355. Skip gate if real polymorphism.
   * Compute D = `declaredArity annot`.
   * Compute S = `length args`.
   * If `D == 0 && S == 1 && (case head args of Can.Unit -> True;
     _ -> False)`: emit
     `CArityMismatch region (modName ++ "." ++ funcName) 0 1`.
3. The existing constraint chain still runs alongside — the new
   constraint is added to the `CAnd` list. The solver accumulates
   both the legitimate `CEqual` mismatch AND our targeted
   `CArityMismatch` diagnostic; the latter is more informative.
4. Flip `k-a` + `u-a` pendingWith arms in
   `Sky.Type.StrictHmArityGateSpec` to live `CompileErr`
   assertions.
5. Full cabal-test + example sweep — confirm no regressions.

### PR-D — Wire gate for k-b + u-b value-slot case (iter 32+)

The value-slot case (`bare reference to f : () -> X in X slot`)
is structurally different from the call-site case. It needs to
fire NOT at `constrainCall` (no call) but at the `Var*` arms
themselves. Approach: at each `Can.VarKernel` / `Can.VarTopLevel`
arm, after looking up annotation, compute D. If D >= 1 AND the
expected slot's type is the result of peeling D TLambdas (i.e.
"D-arg function called with 0 args"), emit `CArityMismatch
region name d 0`.

The `expected :: T.Expected T.Type` parameter the Var arms
already carry gives the slot's expected type — no
`globalCallHeadFlag` IORef needed (the grill's mention of that
was an alternative architecture; the cleaner path is to unify
through `expected` which is already plumbed). Verify and ship in
PR-D.

---

## Out-of-scope (Limitation #7 closure proper)

* The full closure also requires the `Sky.Type.Limitation7CurrentLooseAcceptanceSpec`
  6 red-then-green cases to flip — those are spec contracts for
  the CURRENT loose-acceptance shapes that should fail post-gate.
* Codegen-side correctness audit at `Compile.hs:5629` + `4736`
  (the `arity == 0` ADT-ctor emission paths) must stay green
  post-gate.
* CLAUDE.md "Limitation #7" entry should flip to
  `### Closed in v0.17 (kept here for grep)` only after PR-D
  ships + the spec is 8/8 live.

---

## Diagnostic UX targets

The error messages users see when the gate fires:

```
TYPE ERROR — src/Main.sky:7:5 [E2010]

Cannot call `Uuid.v4` with `()` — `Uuid.v4` is declared as `String`,
which is a value, not a function.

If you meant the value, drop the `()`:
    println Uuid.v4

If you meant a 0-arg function, check `Sky.Core.Pure.uuidV4` which
ships the canonical `() -> Task Error String` companion shape:
    println (Pure.uuidV4 () |> Task.run |> Result.withDefault "")
```

```
TYPE ERROR — src/Main.sky:11:5 [E2010]

Cannot use `Time.now` as `Task Error Int` — `Time.now` is declared
`() -> Task Error Int`, you must call it with `()`:
    doNow = Time.now ()

Or use `Sky.Core.Pure.timeNow` which has the same shape under the
canonical Pure.* surface:
    doNow = Pure.timeNow ()
```

Each error must mention:
1. The binding name + declared signature.
2. The specific shape mismatch.
3. The actionable fix (drop `()`, add `()`, or use `Pure.*`).

---

## Why this is the right multi-iter scope

Per CLAUDE.md non-negotiable #4 ("default response to a hard
problem: analyse root cause → research the architecturally
correct approach → execute, even when it requires multiple
sessions"), the strict-HM gate's full close is a multi-PR
architectural refactor — adding a new Constraint constructor +
solver arm + error render path is the kind of change that
SHOULD ship in dedicated PRs with adversarial review, not
hacked inline.

The iter 28 grill caught this BEFORE any code was written. That's
the per-commit-grill pattern working as designed
(feedback_v017_per_commit_grill). The iter 27 partial close
(positives locked) + this design spec is honest progress; the
PR-A through PR-D sequence is the architecturally correct close
path.
