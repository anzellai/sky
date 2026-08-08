# gap-3 honest rediagnose log

**Date:** 2026-06-20
**Step:** v0.17 close — step-1-gap3-honest-rediagnose
**Branch:** feat/v0.17-fully-typed-codegen (worktree)

## Verbatim repro

Fixture: `/tmp/sky-iter18-debug` (canonical iter18 reproducer
preserved at `test/fixtures/anon-record-gosigdiff/` for the
regression spec).

```bash
cp -rf /tmp/sky-iter18-debug /tmp/r1
cd /tmp/r1 && rm -rf sky-out .skycache .skydeps
SKY_GOSIG_DIFF=1 ${SKY_BIN} build src/Main.sky
```

### Pre-fix outcome

```
./main.go:866:120: undefined: Anon_R_rootAttrs_wrapperAttrs__5n085ahc

This is a Sky compiler bug.
```

### Default-env outcome (SKY_GOSIG_DIFF unset)

```
Compilation successful
Build complete: sky-out/app
```

## Root cause (post-fix understanding)

Pre-fix, `generateGoMulti`'s `imports` lazy thunk issued a
defensive backup wipe at Compile.hs ~line 4978:

```haskell
imports = unsafePerformIO $ do
    atomicWriteIORef globalAnonRecords Map.empty  -- ← defensive, redundant
    ...
```

`globalAnonRecords` is the shared registry that
`synthAnonRecordName` writes to (via `atomicModifyIORef'`) every
time codegen needs an anonymous-record Go-side name. The decls
thunk renders to a String; rendering forces
`synthAnonRecordName` calls.

`generateAnonRecordDecls` reads the registry at module-end via
the `anonRecordDecls` thunk in `_pkg_decls`.

`resetCompileState` at Compile.hs:1287 ALREADY wipes the registry
at `continueCompile` entry — strictly before any
`synthAnonRecordName` call can fire on this compile.

The in-thunk reset at line 4978 was therefore redundant — and
under `SKY_GOSIG_DIFF=1` it became destructive. The diff probe
in `solvePhase` (lines 2036-2056) forces additional evaluation
chains. Under specific laziness conditions, that forcing can
cause `decls` rendering to fire BEFORE the `imports` thunk
fires. Sequence:

  1. `decls` render → `synthAnonRecordName` registers
     `Anon_R_rootAttrs_wrapperAttrs__5n085ahc` shape via
     `atomicModifyIORef'`.
  2. `imports` thunk fires late → `atomicWriteIORef
     globalAnonRecords Map.empty` WIPES the just-registered
     shape.
  3. `anonRecordDecls = unsafePerformIO generateAnonRecordDecls`
     fires → reads an EMPTY registry.
  4. Emitted Go: cast token at line 866 uses
     `Anon_R_rootAttrs_wrapperAttrs__5n085ahc`, but NO
     `type Anon_R_rootAttrs_wrapperAttrs__5n085ahc = struct{...}`
     declaration exists → `go build` rejects with
     `undefined: Anon_R_…`.

## Fix

`src/Sky/Build/Compile.hs:4969-…` — removed the in-thunk
`atomicWriteIORef globalAnonRecords Map.empty`. The single
authoritative reset is now `resetCompileState` at line 1287,
which fires strictly BEFORE any `synthAnonRecordName`
registration can occur. Registrations from decl-rendering
therefore survive to module-end read, regardless of decl/imports
thunk-force ordering.

## Verification

Both `SKY_GOSIG_DIFF=0` (default env) and `SKY_GOSIG_DIFF=1`
builds of the iter18-debug fixture now:

* Pass `Compilation successful` end-to-end (Sky lowering + go
  build).
* Emit identical anon-record decl counts (proof the differential
  gate is non-destructive).
* Have every `Anon_R_<hash>` cast token matched by a
  `type Anon_R_<hash> = struct{...}` declaration.

## Regression locks

`test/Sky/Build/AnonRecordEmissionGuaranteeSpec.hs` covers three
modes:

1. **Mode-(i)** — `SKY_GOSIG_DIFF=1` build over the fixture
   succeeds (was failing pre-fix).
2. **Mode-(ii)** — default-env build over the same fixture
   succeeds AND emits the same anon-record decl count as
   `SKY_GOSIG_DIFF=1` (non-destructive differential gate).
3. **Mode-(iii)** — render-order invariant: every `Anon_R_<hash>`
   cast token in emitted main.go has a matching `type Anon_R_<hash>
   = struct{...}` declaration. Catches future regressions where
   someone adds a lazy reset back into the imports thunk OR
   introduces a hand-built `Anon_R_` string that bypasses
   `synthAnonRecordName`.

Fixture at `test/fixtures/anon-record-gosigdiff/` is the
canonical reproducer (Cfg-shape parametric record alias
flowing through cross-module Std.Ui.layoutWith call).

## Notes on the original step plan

The step description outlined three candidate bypass sites in
priority order:

* (i) the GOSIG_DIFF probe at Compile.hs (~3 match sites)
  building a sig-comparison string that hand-builds an
  `Anon_R_` name.
* (ii) the `safeReturnType*` / `typeStrWithAliasesReg*` family
  that lowers TRecord shapes WITHOUT routing through
  `synthAnonRecordName`.
* (iii) the lazy-thunk render-order hazard documented in
  `memory v017_pr17b_emission_time_vs_render_time.md`.

The actual root cause turned out to be variant (iii) — the
render-order hazard — BUT not at the GoExpr emission level. It
was at the `globalAnonRecords` IORef registry level, where a
DEFENSIVE wipe in the `imports` lazy thunk could fire AFTER
decl-render registrations under specific laziness conditions
that `SKY_GOSIG_DIFF=1`'s probe forced.

No hand-built `Anon_R_` name was found anywhere in src/ (only
`synthAnonRecordName` constructs them). Variant (ii) was ruled
out by direct grep. The GOSIG_DIFF probe (variant (i)) did NOT
hand-build any `Anon_R_` name — it only reads the goSigMap and
emits stderr warnings.

The fix preserves the `synthAnonRecordName` → atomicModifyIORef
→ `generateAnonRecordDecls` flow exactly as designed. It just
ensures no concurrent OR lazy WIPE can wipe the registry between
registration and read.
