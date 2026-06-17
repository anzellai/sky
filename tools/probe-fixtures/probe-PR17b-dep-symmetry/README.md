# probe-PR17b-dep-symmetry

v0.17 PR-17b regression probe — closes the T1 leak class
when a dep module mixes ONE generic decl (typed param T1) with
ONE non-generic decl that uses Dict operations.

## Pre-fix bug

The bodyExpr arm in `Compile.hs` had `if null depTypeParams` as an
early-out, and `withScopedEnclosingTypeParamsStmts` had `| null tps
= stmts` as a mirror. Both deferred eager body materialization to
lazy thunks. Those thunks captured the in-flight scope-state IORef
— so when the bodyExpr lazy thunk for the non-generic decl
(`snapshotToDict`) finally resolved, the IORef still held T1
(pushed by the sibling generic decl's emission).

Result: `Lib_snapshotToDict` body emits
`rt.AsMapT[T1](rawData)` where T1 doesn't exist at module scope.
Sky reports "Compilation successful"; `go build` rejects with
`undefined: T1`.

## Post-fix invariant

Both early-outs removed → every dep body materializes eagerly
to `GoRaw` BEFORE the scope-state push for the next sibling.
The T1 binding never survives across the bodyExpr arm boundary.

## Verification

`test/Sky/Build/Pr17bDepSymmetrySpec.hs` builds this fixture
clean + asserts no `rt.As{Map,List}T[Tn]` reference at module
scope (i.e. outside an enclosing generic-function header).

Real-world repro: `examples/13-skyshop` `Lib_Db_snapshotToDict`,
discussed in CLAUDE.md memory `v017_pr17b_dep_module_asymmetry.md`.
