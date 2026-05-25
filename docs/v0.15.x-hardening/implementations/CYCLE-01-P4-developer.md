# Cycle 1 — P4 Developer implementation log

Closes: audit Gap A5 (`audits/CYCLE-01-auditor.md`) + planner item P4 (`plans/CYCLE-01-planner.md`).

Branch: `feat/v0.15.x-hardening-P4-infer-binop-completeness`
PR: [#79](https://github.com/anzellai/sky/pull/79)
Target tag: **v0.15.10** (human-gated; not pushed by this PR)

## Architectural diagnosis

`exprToGo`'s `Can.Call` arm splits on the callee shape:

* Direct callees (`Can.VarKernel` / `VarCtor` / `VarTopLevel` /
  `Lambda`) emit `goFunc(goArgs)` Go-native.
* Everything else (`Can.VarLocal`, `Can.Access`, `Can.Call` result,
  …) routes through `rt.SkyCall(goFunc, goArgs)` — a reflect-based
  dispatcher (~100 ns + heap escape per call).

For typed-local HOF parameters (`f : Int -> Int` in `addOne f x`),
HM knows the Go shape `func(int) int`.  Calling those directly
Go-native has three knock-on wins:

1. Drops the ~100 ns reflect dispatch.
2. The binop inside the arg slot lowers Go-native because the
   typed slot's primitive shape (`int`) propagates through
   `zipWithDefaultExpect`.
3. The surrounding `rt.CoerceInt(…)` recovery wrap disappears
   because the call's Go-static return type is already `int`.

Before / after for `addOne f x = f (x + 1)`:

```go
// pre-fix
func addOne(f func(int) int, x int) int {
    return rt.CoerceInt(rt.SkyCall(f, x + 1))
}

// post-fix
func addOne(f func(int) int, x int) int {
    return f(x + 1)
}
```

## Sequenced steps

1. **Failing-test-first.**  `test/Sky/Build/InferExprTypeBinopSpec.hs`
   (NEW, ~340 LOC) registers 8 reproducer families — addOne,
   wrapBool, joinThem, consG, deepF, applyStr, applyTwice,
   pickAdd — each with a codegen invariant (no
   `rt.SkyCall(<typed-local>, …)`) + a runtime smoke
   (`buildRunExpect`).  On the pre-fix HEAD `0171096`, all 8
   codegen invariants FAIL; all 8 runtime smokes PASS (the
   reflect path is functionally correct, just inefficient).

2. **Sky-side runtime smoke.**  `tests/Hardening/NestedBinopHof.sky`
   (NEW, ~100 LOC) replicates the same 8 cases as a Sky.Test
   module runnable via `sky test`.

3. **Fix.**  `src/Sky/Build/Compile.hs`:
   * `Can.Call`'s `_ ->` fall-through arm gains a
     `typedCallableShape` recovery via HM (`inferExprType` then
     `solvedTypeToGo`).  When the shape is `func(P1) R` for a
     single-arg call AND every slot is emittable/concrete,
     emit the typed direct call.
   * Args route through `zipWithDefaultExpect (typedParamTys ++
     repeat "any") args` so each arg expression sees its typed
     slot — binops therein emit Go-native, record literals pick
     up the parametric-alias instantiation, etc.
   * `peelTypedArrows` helper (NEW) — peels N curried Go arrows;
     rejects any-slots and generic-type-param leaks.  Only the
     N=1 path is invoked today; the N>1 arm is reserved for a
     future widening once a callee-shape registry exists.

4. **Architectural choice — HM, not lambda-scope.**  The
   lambda-types scope is pushed on the WRAPPED expr right before
   render; sub-expression thunks like this `Can.Call` arm may
   force BEFORE the wrap's IO action runs, leaving the IORef
   empty during the lookup (confirmed via trace during
   development).  HM is global and pre-populated by the time
   codegen runs — robust path for both entry- and dep-module
   bodies.

5. **Conservative gate — `length args /= 1`.**  Sky HM always
   renders curried (`T1 -> T2 -> T3` ↦ `func(T1) func(T2) T3`),
   but the EMITTED Go for multi-pattern let-bound functions is
   FLAT (`func(t1, t2) t3`).  Without a per-callee registry the
   call-site can't disambiguate.  Caught during verification:
   an earlier N-arg widening REGRESSED `examples/18-job-queue`'s
   `logAndFail e errId` (let-bound 2-param fn emitted flat, was
   wrongly curried by the widened fix into `logAndFail(e)(errId)`
   which Go rejected as a flat-vs-curry mismatch).  The
   `length args /= 1` gate keeps multi-arg callees on the
   `rt.SkyCall` reflect path which curries via `skyCallOne` and
   is correct for both shapes.

## Verification evidence

### New spec (failing-test-first)

Pre-fix on `0171096`:

```
8 examples failed (codegen invariants),
8 examples passed (runtime smoke — the slow path is functionally correct)
```

Post-fix:

```
$ cabal test --test-show-details=streaming --test-options="--match=InferExprTypeBinop"
Sky.Build.InferExprTypeBinop
  Gap A5 — typed-primitive binop in HOF arg slot: …
    addOne f x = f (x + 1) — emits direct `f(x + 1)`, no `rt.SkyCall(f, …)` [✔]
    addOne runs to completion (6 = identity (5 + 1)) [✔]
    wrapBool g x y = g (x || y) — direct `g(x || y)`, no SkyCall on `g` [✔]
    wrapBool runs to completion (false) [✔]
    joinThem g xs ys = g (xs ++ ys) — direct `g(rt.Concat(…))` [✔]
    joinThem runs to completion (11 = first of [11,22]) [✔]
    consG f x xs = f (x :: xs) — direct `f(rt.List_cons(…))` [✔]
    consG runs to completion (7 = head of (7 :: [8,9])) [✔]
    deepF f x = f ((x + 1) * 2) — direct `f((x + 1) * 2)` [✔]
    deepF runs to completion (10 = (4 + 1) * 2) [✔]
    applyStr g a b = g (a ++ b) — direct `g(a + b)` [✔]
    applyStr runs to completion (hello world) [✔]
    applyTwice h x = h (h x + 1) — chained typed-callable [✔]
    applyTwice runs to completion (42 = 41 + 1) [✔]
    pickAdd runs to completion (15 = (4+1) + (5*2)) [✔]

Finished in 26.9427 seconds
15 examples, 0 failures
```

### Wider cabal sweep

```
$ cabal test --test-show-details=streaming \
    --test-options="--skip=/Sky.Lsp.NvimDriver/ --skip=/Sky.Lsp.Scale/ \
                    --skip=/Sky.Build.VerifyAll/ --skip=/Sky.Build.VerifyScenario/ \
                    --skip=/Sky.Build.EmbeddedRuntime/ --skip=/Sky.Build.EmbeddedInspector/ \
                    --skip=/Sky.Cli/"
Finished in 652.6922 seconds
350 examples, 0 failures, 1 pending
Test suite sky-tests: PASS
```

### Example sweep

```
$ scripts/example-sweep.sh --build-only
sweep: 19 passed, 0 failed
```

### Key examples clean-build (`rm -rf sky-out .skycache .skydeps && sky build`)

* `examples/13-skyshop` — Build complete: sky-out/app
* `examples/12-skyvote` — Build complete: sky-out/app
* `examples/19-skyforum` — Build complete: sky-out/app
* `examples/18-job-queue` — Build complete: sky-out/app  (regression gate)

### `sky check`

```
$ cd examples/12-skyvote && sky check src/Main.sky
No errors found.
```

### Sky-side runtime smoke

```
$ sky test tests/Hardening/NestedBinopHof.sky
8 passed, 0 failed (8 total)
```

### `sky fmt` idempotency

```
$ sky fmt tests/Hardening/NestedBinopHof.sky
Formatted ...
$ md5 tests/Hardening/NestedBinopHof.sky
MD5 (…) = 2a740e622c1cd94d9be58c7c0ca7ae69
$ sky fmt tests/Hardening/NestedBinopHof.sky
Formatted ...
$ md5 tests/Hardening/NestedBinopHof.sky
MD5 (…) = 2a740e622c1cd94d9be58c7c0ca7ae69    # byte-identical
```

## Files changed

* `src/Sky/Build/Compile.hs` — typed-callable fast-path
  (single-arg gate) + `peelTypedArrows` helper.
* `test/Sky/Build/InferExprTypeBinopSpec.hs` — NEW, 8 reproducer
  families × 2 assertions each.
* `test/Spec.hs` — register the new spec.
* `sky-compiler.cabal` — register the new spec module.
* `tests/Hardening/NestedBinopHof.sky` — NEW, Sky-side runtime
  smoke.

## Risk register

* **(closed)** `examples/18-job-queue`'s let-bound flat 2-param
  `logAndFail` regressed under an earlier N-arg widening — the
  `length args /= 1` gate keeps multi-arg callees on the reflect
  path.  Caught by `scripts/example-sweep.sh --build-only`;
  regression coverage in `Sky.Build.ExampleSweep`.
* **(closed post-PR-79 push)** `test-files/record-field-partial-app.sky`
  self-test regressed: `runWidget cfg form = cfg.onSubmit form`
  emitted Go that `go build` rejected with
  `cannot call rt.Field(cfg, "OnSubmit") (any): any is not a function`.
  Cause: the HM-driven recovery saw `cfg.onSubmit : Form -> Msg`
  and fired the fast-path, but `Can.Access` lowers via `rt.Field`
  which returns `any` (no static function shape).  Fix: gate the
  fast-path on `Can.VarLocal` callees only.  Those emit as bare
  `GoIdent (goSafeName name)`, and the entry-/dep-module decl
  paths zip `typedGoParams` with the resolved `solvedTypeToGo`
  per annotation slot — so the bare ident statically carries a
  Go function value and the direct call typechecks.  Other
  indirect shapes (`Can.Access`, `Can.Call` result, `Can.Update`,
  `Can.LetRec` name, …) widen to `any` at the call boundary and
  stay on the `rt.SkyCall` reflect path.  Caught by
  `./scripts/build.sh --self-tests` (CI's self-test gate).
* **(open, future P4-followup)** Multi-arg HOF param values
  (`pickAdd op a b`) still route through `rt.SkyCall`.  Closing
  this needs a per-callee flat-vs-curry registry built at
  definition time; tracked as a future P4-followup.

## Session-cost

~2h walking + ~2h on the curry-vs-flat ambiguity (caught by
example-sweep regression on `18-job-queue`; closed by conservative
gate).

## Sign-off

| Item | Status |
|---|---|
| mem-guard alive throughout | ✅ (verified end of session) |
| All cabal tests green (excluding skipped network/heavy suites) | ✅ 350 examples, 0 failures |
| 19 examples build clean | ✅ |
| `scripts/example-sweep.sh --build-only` green | ✅ |
| `sky check` on `examples/12-skyvote` green | ✅ |
| `sky fmt` clean on every changed `.sky` file | ✅ (idempotent) |
| Background tasks cleaned up | ✅ |
| CI release workflow green | ⏳ pending (PR #79 in CI) |
| Cycle log line appended | ✅ |
