# P3.4c design: pattern-match type-switch lowering — DECOMPOSED

**STATUS**: Iter 52 dual-grill caught a load-bearing blocker. Both
grillers returned needs-revision. Real architectural gap surfaced
BEFORE any Compile.hs codegen touch — methodology working as
designed.

## Dual-grill findings (iter 52)

**Griller 1 (subject inference)** — verdict: needs-revision.
THREE blocking issues:

* **B1**: `Canonicalise._dep_unions :: [(String, [String], [Can.Ctor])]`
  (Module.hs:32) drops `Can.CtorOpts`. For dep ADTs, the lowerer
  cannot distinguish Can.Enum from Can.Normal — and the carve-out
  rule 1 (`opts == Can.Enum`) cannot be evaluated. The design
  assumed metadata that doesn't flow.
* **B2**: `inferredSubjectGoType` returns identical strings
  (`"Mod_Color"`) whether the ADT is a legacy `type X = rt.SkyADT`
  alias OR a sealed-iface interface. The gate cannot distinguish
  by Go name — decision MUST come from the source-of-truth
  Can.Union, not the Go string.
* **B3**: `shouldEmitSealedIface` takes `ModuleName.Canonical` to
  build `Sky.Core.Error.Error`-shaped qualified names. The Go
  name `"Sky_Core_Error_Error"` is not invertible (underscore
  is ambiguous between module segments + Mod_Type boundary).
  Recovery requires the same metadata B1 demands.

**Refinements required (R1-R3)**:
* R1: Add `_cg_unionDetails :: Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])`
  to CodegenEnv keyed by Go-mangled qualified name. Mirror on
  LowerCtx. Populate at both entry + per-dep code points.
  Widen `_dep_unions` to include `CtorOpts`.
* R2: Subject inference walks `T.Type` (via `lookupSolvedRegion`),
  peels `TAlias`, expects `T.TType home name args`. Use `(home,
  name)` directly as the lookup key. Never round-trip via
  `solvedTypeToGo`.
* R3: Explicit gate predicates — anonymous-record / tuple / unit /
  bare TVar / parametric kernel containers → `Nothing`.

**Griller 2 (IR/emission)** — verdict: needs-revision (doc fixes
only, no architectural blockers):

* GoTypeSwitch IR node ALREADY EXISTS (Ir.hs:52, Builder.hs:170-175).
  Design wrongly said it doesn't.
* Expr/Stmt bridge via `GoBlock [stmts] (GoRaw "nil")` IIFE pattern
  is established. caseToGo's existing legacy path (Compile.hs:16093)
  uses it.
* Pattern binding via `GoShortDecl "r" (GoSelector __subject "V0")`
  is straightforward — same flow as legacy bindCtorArg's typed-let
  injection.
* `rt.Unreachable` returns `any` and panics inside (rt.go:5411) —
  use `_ = rt.Unreachable("case/Mod_Color")` not `panic(rt.Unreachable(...))`.
* Spec strategy: hand-built Can.Expr + populated SolvedTypes works
  via real Sky source snippet through compileSource (matches
  UnannotatedParametricCfgViewSpec pattern). Plain unit-spec without
  SolvedTypes falls through to legacy.

## Decomposition (iter 52+)

P3.4c was designed as a single commit but the grill found it
requires metadata threading that doesn't currently exist. Splitting
into focused, individually grillable commits:

### P3.4c.0 — CodegenEnv + LowerCtx metadata widening (NEXT)

Purely additive, no behavior change. Adds the metadata the gate
needs without exposing any new emission path.

* Add `_cg_unionDetails :: Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])`
  to `Rec.CodegenEnv`. Key = Go-mangled qualified name
  (`Sky_Core_Error_Error`).
* Mirror to `LC.LowerCtx` so the scopeStateRef cascade path reads
  the same map.
* Populate from `Can._unions canMod` at `buildCodegenEnv`
  (entry module — full opts available).
* Populate from dep-module canonical modules at the per-dep
  emission entry. May require widening `Canonicalise._dep_unions`
  to carry `CtorOpts` — investigate during implementation.
* Spec: assert the map contains correct entries for a fixture
  ADT — both entry-module and a synthetic dep-module ADT.

Estimated scope: ~100 LOC across `Generate/Go/Record.hs` +
`Build/LowerCtx.hs` + `Canonicalise/Module.hs` (if dep_unions
widening needed) + the population call sites in `Build/Compile.hs`.

### P3.4c.1 — subjectIsSealedIface predicate

Pure function. Takes `LowerCtx + Can.Expr + Solve.SolvedTypes`,
returns `Maybe (ModuleName.Canonical, String, [String], Can.CtorOpts, [Can.Ctor])`
by walking the subject's HM type. Calls `shouldEmitSealedIface`
under the hood for the carve-out decision. Spec-testable in
isolation.

### P3.4c.2 — caseToGoSealedIface helper

Pure helper. Emits `GoBlock [GoTypeSwitch ...] (GoRaw "nil")` per
the design's shape. Returns GoExpr.

### P3.4c.3 — caseToGo dispatch gate

Wire P3.4c.1 + P3.4c.2 into the existing caseToGo top-level. Same
gate (shouldEmitSealedIface still False everywhere → byte-identical
output verified via 26-ui-showcase diff).

### P3.4d / P3.4e — flip carve-out (unchanged from prior plan)

---

# Original P3.4c design (PRESERVED for grill-checklist reference)

## Problem

When P3.4b's True branch fires (P3.4d/e flips carve-out), an ADT
emits as:
```go
type Mod_Color interface { SkyVariantTag() int; SkyVariantName() string }
type Mod_Color_Red_V struct { SkyVariant_ uint8 }
type Mod_Color_RGB_V struct { V0 int; V1 int; V2 int }
```

The user's `case c of Red -> ... | RGB r g b -> ...` currently
emits via `caseToGo` as:
```go
__subject := c.(Mod_Color)  // = rt.SkyADT alias
switch __subject.Tag {
case 0: ...
case 2:
    r := __subject.Fields[0].(int)
    g := __subject.Fields[1].(int)
    b := __subject.Fields[2].(int)
    ...
}
```

But under sealed-iface, `Mod_Color` is an interface — has NO `.Tag`
field. The above code fails to compile. Worse: even if it compiled,
it would type-assert from interface to struct via concrete instance
which is the wrong dispatch direction.

## New shape (when subject's ADT shouldEmitSealedIface=True)

```go
switch __subject := c.(type) {
case Mod_Color_Red_V:
    _ = __subject  // unused in nullary arm
    ...
case Mod_Color_RGB_V:
    r := __subject.V0
    g := __subject.V1
    b := __subject.V2
    ...
default:
    panic(rt.Unreachable("case/Mod_Color"))
}
```

Go's type switch on an interface dispatches by concrete-type match.
Each case binds `__subject` to that concrete type — fields
accessed via `.V0`/`.V1`/... typed.

## Design

Add a new helper `caseToGoSealedIface :: LC.LowerCtx -> Maybe String
-> Can.Expr -> [Can.CaseBranch] -> GoIr.GoExpr` that emits this
shape. Wire it into the existing `caseToGo` via a top-level branch
BEFORE the legacy logic, gated on:

```haskell
caseToGo ctx mExpectedGo subject branches
    | Just (modName, typeName, ctors, opts, vars) <- subjectIsSealedIface ctx subject
    , shouldEmitSealedIface modName typeName vars opts
    = caseToGoSealedIface ctx mExpectedGo subject ctors branches
    | otherwise = <existing caseToGo body>
```

`subjectIsSealedIface` inspects the subject's HM type:
- If subject's `inferredSubjectGoType` resolves to a known
  user-defined ADT typename in `scopeStateRef`'s solvedTypes
- AND that ADT's Can.Union is monomorphic non-Enum non-carve-out
- Returns the ADT details for `shouldEmitSealedIface` decision

If can't determine the subject's ADT cleanly (Result/Maybe/Cmd/etc.
parametric types, anonymous records, polymorphic vars), falls
through to legacy path. Safe default.

## caseToGoSealedIface emission

```haskell
caseToGoSealedIface ctx _mExpectedGo subject _ctors branches =
    let goSubject = exprToGo ctx subject
        armDecls = map (renderArm ctx) branches
    in GoIr.GoTypeSwitch goSubject "__subject" armDecls
       -- with default arm appended that panics rt.Unreachable
```

For each branch:
- Pattern `Ctor` (nullary): emit `case Mod_X_Ctor_V:` arm with body
- Pattern `Ctor arg1 arg2 ...`: emit `case Mod_X_Ctor_V:` arm with
  let-binding `arg1 := __subject.V0; arg2 := __subject.V1; ...`
- Wildcard `_`: emit `default:` arm

## Key risks (for the grillers)

1. **GoTypeSwitch IR node doesn't exist yet.** Today's case-of
   lowers to `GoIIFE` or `GoSwitch`. Need to add `GoTypeSwitch` to
   GoIr OR emit via `GoDeclRaw` string. Prefer adding the
   structured IR.
2. **Subject-type inference**: how does caseToGo know the subject's
   ADT? `inferredSubjectGoType` returns a Go type string like
   `"Mod_Color"`. Need to map back to ModuleName + typeName + vars
   + opts. The CodegenEnv has `_cg_unionNames :: Set String` (line
   46 of Record.hs) — but no per-ADT details. Need a richer lookup.
3. **Multi-arm wildcard fallback**: Sky's exhaustiveness checker
   guarantees all ctors are covered, but `case x of _ -> default`
   forms exist. Handle both styles.
4. **Nested patterns**: `case Just (RGB r g b) -> ...` — outer
   pattern is Maybe, inner pattern is monomorphic Color. P3.4c only
   handles top-level dispatch on the SUBJECT's ADT. Nested ADTs
   need a follow-up case-of via if-cascade or nested switch.
5. **Co-existence with legacy `coerceSubject` (line 15933)**:
   ResultCoerce / MaybeCoerce paths must keep firing for
   parametric ADTs. The gate only activates for monomorphic
   non-Enum non-carve-out — exactly the carve-out check from P3.3.

## Implementation scope

P3.4c THIS commit:
- Add `caseToGoSealedIface` helper (~80 LOC)
- Add `subjectIsSealedIface` predicate (~30 LOC)
- Wire into `caseToGo` top-level (~10 LOC)
- Add GoTypeSwitch IR node OR emit via GoDeclRaw within a GoIIFE
  wrapper (since caseToGo returns GoExpr)
- Spec: hand-built fixture ADT + Can.Expr + branches → assert
  emitted Go shape

Because `shouldEmitSealedIface` returns False everywhere in P3.3,
the new path is UNREACHABLE on production. Spec calls it directly
with synthetic input to verify emission shape. Risk to existing
build: zero.

P3.4d/e will activate the gate per-ADT and validate on real apps.
