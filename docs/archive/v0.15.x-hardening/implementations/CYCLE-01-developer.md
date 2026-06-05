# CYCLE-01 — Developer record (Plan Item P1)

**Closes:** Audit Gap A1 + fragility-audit-v0.15.3.md item #3.

**Branch:** `feat/v0.15.x-hardening-P1-coerce-parametric-alias-gate`

**Target patch tag:** v0.15.7 (push is the human's gate — branch +
PR is the developer's finish line).

## Architectural diagnosis

`coerceArg`'s parametric-alias short-circuit (Compile.hs ~8499 pre-
fix) was gated on `Just srcTy <- goExprGoType e`.  For expression
shapes whose Go-static type isn't tracked in the lambda-types
registry, that returned Nothing and the arm didn't fire.  Codegen
then fell through to the wrap path, which emits
`any(arg).(Foo_R[any])` — a nominal type assertion across Go
generic instantiations.  When the source's actual runtime type was
`Foo_R[int]` / `Foo_R[Msg]`, the assertion panicked at runtime with
`interface conversion: Foo_R[int], not Foo_R[any]`.

The lambda-types registry doesn't cover three shapes that exercise
this bug:

1. Let-bindings whose RHS is a polymorphic-call result
   (`let cfg1 = forwardCfg cfg0`).  `forwardCfg : Cfg msg -> Cfg msg`
   doesn't register `cfg1` because `letBindingType`'s `canRouteTyped`
   gate rejects `Can.Call` body shapes.
2. VarLocal references that pre-date the current scope's
   `withScopedLambdaTypes` push.
3. Field selectors whose underlying record's Go-static type isn't
   tracked.

For all three the HM solver still has the type — the structural-
fallback arm reads it via `inferExprType` against
`Solve.SolvedTypes`, recognises the `T.TAlias _ aliasName _ _`
shape, and short-circuits when the alias's emitted struct name
(`aliasName ++ "_R"`) matches the target's parametric base.

## Sequenced steps

1. **Failing-test-first.** `test/Sky/Build/CoerceArgParametricSpec.hs`
   builds a project-style fixture (let-bound concrete alias through
   polymorphic forwarder) and asserts:
   - generated Go contains no `.(Cfg_R[any])` nominal assertion,
   - emitted binary runs to completion without panicking.
   Confirmed both assertions FAIL on the starting worktree.
2. **Stress fixture.**
   `test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky` mirrors
   the reproducer as an importable dep module; Main.sky exercises
   it via `XCfg.roundTripInt 42`.
3. **Signature extension.** `coerceArg :: Maybe Can.Expr -> GoIr.GoExpr -> String -> GoIr.GoExpr`.
   Callers updated:
   - `coerceCallArgsAt` (two `coerceOne` / `coerceFallback` arms) —
     pass `Just e`.
   - `kernelCoerceArg` — pass `Just e`.
   - `lowerArgExpect` — pass `Just e`.
   - over-application call-site (`extraIdents` zipWith) — pass
     `Nothing` (synthesised `__p<i>` identifiers).
   - TCO jump (`tcoJump` in entry/dep function bodies) — thread the
     original `Can.Expr` per-position via `zip4tco` so the structural
     fallback can recognise let-bound cross-instantiation in tail
     calls.
4. **Structural-fallback arm.** New `coerceArg` clause:

   ```haskell
   | Just targetBase <- parametricAliasBase ty
   , Just src <- mSrc
   , Just aliasName <- aliasBaseFromCanExpr src
   , aliasName ++ "_R" == targetBase
       = e
   ```

   Helper `aliasBaseFromCanExpr` routes through `inferExprType` +
   the env's `_cg_recordAliases` set.  Critical detail: it accepts
   BOTH the unqualified alias name (entry-module compile path) AND
   the module-prefixed name (dep-module path) so a let-binding in a
   dep function resolves cleanly when the dep module is compiled
   under its own `_cg_aliases` namespace.

5. **Updated the comment block.** Compile.hs ~8485 now describes
   the two-path soundness: (a) lambda-types registry, (b)
   structural fallback via `inferExprType`.  References to the new
   regression spec + the new stress fixture inserted.

## Test rationale

- **Spec is project-style** (vs single-file), mirroring
  `TaskResultBridgesSpec` and `HofTypedMsgSpec` shape, so the build
  exercises the full Sky.Stdlib + module-graph + monomorphisation
  flow.
- **Two assertions** (codegen-shape + runtime) catch both the
  pre-fix wrap-shape regressions AND the codegen-emits-different-
  thing-but-still-panics class.

## Verification evidence

- `cabal test --match=CoerceArgParametric` — 2/2 PASS post-fix
  (was 2/2 FAIL pre-fix).
- Full cabal-test (excluding `Sky.Build.EmbeddedRuntime`'s pre-
  existing tree-mismatch failure unrelated to this work) — 312
  examples, 0 failures, 1 pending.
- v0.15-stress runtime — `ALL 24 PASS` (24 sections including the
  new `XCfg` round-trip).
- Clean-build sweep (29 example dirs, 27 numbered + 2 fixtures) —
  100% green.
- `scripts/verify-cli.sh` — 13/13 pass, 1 GUI skip.
- `scripts/verify-all-web.sh` — 10/10 pass + console e2e green.
- `sky check` on examples/12-skyvote — `No errors found.`
- `sky fmt` on the new .sky fixture — two-pass byte-identical.
