# v0.17 Session 11 — Phase A iters 15-16 shipped + iter 17a learning

**Date**: 2026-06-30
**Branch**: `feat/v0.17-pure-sound-codegen`
**HEAD**: `08fa0a56`
**Continues**: `session-10-phase-A-iters-12-to-14.md`

## Session summary

| Iter | SHA | Sites drained | Notes |
|---|---|---|---|
| 15 | `cc0304b0` | 1 drain (isPlainIdentForTypedRouting) + 1 deletion (isParametricCompatibleSource — dead) | Grilled by Architecture-Consult agent; spec fixture threads `LC.emptyLowerCtx` |
| 16 | `08fa0a56` | 2 drains (operandIsStaticallyTyped lookupLambdaType + lookupLambdaGoType) + 2 deletions (exprToMainStmts, exprToGoTypedWithRet — dead) | Architecture-Consult agent verdict REVISE → narrowed plan; per-commit grill protocol honoured |

**Total this session**: 5 closures (3 drains + 2 dead-helper deletions) +
2 additional drains (deleting the dead helpers eliminated 3 `ctxFromIORef`
hops on the deleted-line surface).
**Cumulative iter 7-16**: 133+ phaseAFallback IORef hops eliminated.
**N-strikes audit on the "drain-caller" lever**: 10 consecutive iters,
zero reverts.

## Iter 17a learning — emptyLowerCtx+withCgEnv is NOT a drop-in for ctxFromIORef

Iter 17a attempted to drain site `:20659` (the
`exprToMainStmtsTyped (ctxFromIORef ()) solvedTypes body` call inside
`generateMainFunc`) by:

1. Widening `generateMainFunc` to take `EmitCompileCtx`
2. Building `mainCtx = LC.withCgEnv (lookupCgEnvFromCtx phaseACtx)
   (LC.emptyLowerCtx home)` and passing to `exprToMainStmtsTyped`

This BUILT green (compiler typechecks + bins out) and PhaseA + AnonRec
gates STAYED at floor, but the 26-example sweep failed 4/26:

```
sweep: 22 passed, 4 failed
  - 02-go-stdlib: undefined: rt.Go_Uuid_newString
  - 03-tea-external: undefined: rt.Go_App_new
  - 05-mux-server: undefined: rt.Go_Mux_newRouter
  - 11-fyne-stopwatch: undefined: rt.Go_...
```

All 4 failures are FFI-heavy examples missing `rt.Go_*` symbols.

**Root cause**: `ctxFromIORef ()` reads the FULL `scopeStateRef`
snapshot, which carries 17+ LowerCtx fields including the FFI-typed
wrapper registry (`_lc_ffiTypedWrapperNames` +
`_lc_ffiTypedWrapperParams`). The agent's siteCitations note for
:20659 said:

> "exprToMainStmtsTyped at :20767 immediately does
> 'let ctx = ctx0 { LC._lc_solved = types }' so its body is correct
> regardless — only the entry edge needs the threaded ctx."

This was correct about `_lc_solved` but missed the OTHER fields
threaded through `defToStmts` / `exprToGoMain` downstream. The
`exprToGoMain` chain consults `_lc_ffiTypedWrapperParams` to emit
typed FFI wrappers; without it, raw `Go_*` symbols are emitted and
the Go compiler rejects them.

**Reverted at sweep-fail observation** per CLAUDE.md §0.3 rule 3
N-strikes audit. This is strike 1 on the "construct LowerCtx from
EmitCompileCtx in generateMainFunc" lever. Two more attempts at the
same lever would FORBID a 4th attempt without re-classification.

## Path to criterion #3 architectural close — REVISED iter 17 plan

Per the iter-17a learning, the correct closure for site :20659 needs
ALL the fields the legacy ctxFromIORef path carries, not just cgEnv.
Two approaches:

### Option (a) — Full LowerCtx constructor from EmitCompileCtx

Add a helper `LC.buildFromEmitCompileCtx :: EmitCompileCtx ->
ModuleName.Canonical -> LowerCtx` that populates:
- `_lc_module` = home
- `_lc_solved` = lookupSolvedTypesFromCtx phaseACtx
- `_lc_aliases` = lookupAliasesFromCtx phaseACtx
- `_lc_fieldIdx` = lookupFieldIdxFromCtx phaseACtx
- `_lc_unionNames` = via unionDetails
- `_lc_unionDetails` = lookupUnionDetailsFromCtx phaseACtx
- `_lc_cgEnv` = Just (lookupCgEnvFromCtx phaseACtx)
- `_lc_ffiTypedWrapperNames` = lookupFfiTypedWrapperNamesFromCtx
- `_lc_ffiTypedWrapperParams` = lookupFfiTypedWrapperParamsFromCtx
- _lc_lambdaTypes, _lc_lambdaGoStr, _lc_lambdaGoTypes — empty
  (main body is at module-toplevel, no enclosing lambda scope)
- _lc_aliasMap, _lc_annotMap, _lc_enclosingTypeParams — empty
- _lc_currentDepModule — Nothing (entry-module emission)
- _lc_reachableSet, _lc_reachableProgram — empty? or read from
  somewhere? Need audit.
- _lc_kernelAlias — empty? Or read from phaseACtx? Need audit.

The audit step is non-trivial. Risk: missing one field at the
emptyLowerCtx baseline leaves the same "rt.Go_* undefined" class of
failure surface across some other example.

### Option (b) — pass-through via threaded ctx from C9/C10 phase

The proper close: do NOT build a fresh LowerCtx at generateMainFunc.
Instead, have `continueCompile` build a `lowerCtx` ONCE during the
C10 emit-phase (where scopeStateRef is fully populated), thread that
through into generateGo / generateGoMulti's local scope, and pass
through into generateMainFunc as a parameter. The cgEnv finalisation
at C9 happens BEFORE this, so the threaded ctx is post-finalisation.

This is the cleanest architectural close but requires audit of the
`continueCompile` → `generateGo` / `generateGoMulti` call chain to
see where the ctx can be safely threaded.

### Option (c) — keep site :20659 as the LAST ctxFromIORef call

Accept that site :20659 is the architecturally-cleanest LAST IORef
read because it's the OUTERMOST CALL of exprToMainStmtsTyped from
the main rendering pipeline. Document it as the irreducible site
inside generateMainFunc's docstring. Combined with site :14023
(walkAuthCalls — security audit walker without ctx), these are the
TWO surviving sites; iter 17b then closes :14023 via walkAuthCalls
thread refactor + writes the criterion #3 spec gate for the two
documented surviving call sites + the scopeStateRef bracket-scope
writers.

This is the HONEST close that matches the v0.17 reframe ("rock solid
+ documented surface" — the reframe explicitly contemplates a
documented residual surface).

## Recommendation for next session

**Spawn an Architecture-Consult agent for iter 17** with the full
context above. The agent should:

1. Audit the full caller chain from `continueCompile` → `generateGo`
   / `generateGoMulti` → `generateMainFunc` to see what's available.
2. Verify whether Option (b)'s "thread ctx from continueCompile"
   is mechanically safe (no laziness gotcha from the renderer
   forcing ctx mid-emit).
3. Compare to Option (c) the "document residual surface" path —
   per the v0.17 reframe both are acceptable closures.
4. Identify the 17+ LowerCtx fields that need to populate for
   Option (a) and whether ALL of them have EmitCompileCtx readers.

Iter 17a's failure mode is empirically grounded — the surface is
LARGER than "just cgEnv". The agent's prior PROCEED verdict for
:20659 missed the FFI wrapper registry consequence.

## Pushed commits this session

```
08fa0a56 feat(v0.17 Phase A iter 16): drain operandIsStaticallyTyped + delete 2 dead legacy wrappers
cc0304b0 feat(v0.17 Phase A iter 15): drain isPlainIdentForTypedRouting + delete dead isParametricCompatibleSource
```

Iters 15+16 pushed to remote at `1eafa077..08fa0a56`. CI
should match the same shape as session 10 (4 of the 5 pre-existing
failures unrelated to this work; DictSource Linux-only flake).

## Remaining surface — exactly 3 sites in Compile.hs

After iter 16 (HEAD = `08fa0a56`):

```
:13391-13392  ctxFromIORef () = unsafePerformIO (readIORef scopeStateRef)
:14023        LC.lookupKernelAlias (ctxFromIORef ()) home name
:20659        exprToMainStmtsTyped (ctxFromIORef ()) solvedTypes body
```

Plus 2 surviving `scopeStateRef` bracket-scope readers:
- `phaseAFallback` (Compile.hs:13422) — the IORef-read fallback used
  when no LowerCtx is threaded
- `phaseAFallbackFromCtx` (Compile.hs:13479) — the threaded-ctx
  variant used by all post-iter-7 drained call sites

Per criterion #3 locked wording, these need either DELETION or a
"machine-verified single-writer / single-reader monotonic contract"
(docstring + spec gate). Iter 17 ships one of those two outcomes.

## Resume protocol for next session

1. Read this checkpoint + `session-10-phase-A-iters-12-to-14.md`.
2. Confirm working tree clean + HEAD = `08fa0a56`.
3. Spawn Architecture-Consult agent for iter 17 with the prompt:
   "Audit the continueCompile → generateGo[Multi] → generateMainFunc
    call chain. Choose between Option (a) full LowerCtx constructor /
    Option (b) ctx threading from C10 phase / Option (c) document
    residual surface. Iter 17a empirically failed Option (a)-minimal
    (just cgEnv install) — 4/26 sweep regressed on FFI wrapper
    registry loss. Plan accordingly."
4. Per CLAUDE.md §0.3 rule 3 N-strikes — site :20659 is now at
   strike 1. Two more attempts on the same lever forbid a 4th
   without re-classification.
5. Iter 17 ships criterion #3 close OR the documented-surface
   variant per the v0.17 reframe.
