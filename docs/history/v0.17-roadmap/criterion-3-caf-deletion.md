# v0.17 close criterion #3 — `getCgEnvFromScope` CAF deletion roadmap

**Status:** MULTI-ITER. Filed iter 16 (2026-06-24) after surface analysis.
**Branch:** `feat/v0.17-fully-typed-codegen` @ `543252c2`.
**Related:** supersedes the now-stale
`docs/v0.17-roadmap/getcgenv-migration.md` (filed 2026-06-20 when
`getCgEnv` + `globalCgEnv` were the surviving impurity; iter 44 has
since deleted both — see below).

---

## TL;DR

Criterion #3's verbatim text says:

> 2 surviving module-level IORefs (`globalCgEnv` + `globalGoSigMap`)
> actually DELETED, not documented as "load-bearing-but-pure". The
> `getCgEnv` CAF must be gone. All ~20 call sites must thread
> `LowerCtx` explicitly.

Audit at iter 16 against `src/Sky/Build/Compile.hs` HEAD `543252c2`:

| Item | Iter-16 status | Evidence |
|---|---|---|
| `globalCgEnv` IORef | DELETED ✓ | iter 44 commit; no `globalCgEnv` token anywhere in `src/` |
| `globalGoSigMap` IORef | DELETED ✓ | iter 44; finalGoSigMap is a pure `let` binding now |
| `getCgEnv` CAF | DELETED ✓ | iter 44; renamed/replaced |
| `getCgEnvFromScope` CAF | **SURVIVES** ✗ | `Compile.hs:860`, 57 reader sites |
| `scopeStateRef` IORef | SURVIVES ✗ | `Compile.hs:508-510`, the underlying CodegenEnv channel |

So the goal's IORef-targeted half ("`globalCgEnv` + `globalGoSigMap`
actually DELETED") is **done**. The CAF-targeted half ("`getCgEnv`
CAF must be gone. All ~20 call sites must thread `LowerCtx`
explicitly") is **partially done**: the original `getCgEnv` CAF is
gone, but it was succeeded by `getCgEnvFromScope` which reads
`scopeStateRef` instead. From the goal text's spirit (no
unsafePerformIO CAF in the codegen reader path), this DOES count as
the same impurity class — it just routes through a different IORef.

Honest verdict: **criterion #3 is partially achieved — the IORef
half is closed, the CAF half is still open via the renamed sibling.**

---

## Surface (verified `grep -n` on `feat/v0.17-fully-typed-codegen` tip)

- **CAF site:** `Compile.hs:860` — `getCgEnvFromScope :: Rec.CodegenEnv = unsafePerformIO $ readIORef scopeStateRef`
- **Total mentions:** 59 across `Compile.hs` (only file).
- **Reader sites (real consumers):** 57 — categorised by enclosing-function `LowerCtx` availability:

| Class | Sites | Enclosing function shape | Threading effort |
|---|---|---|---|
| **A** — `ctx :: LC.LowerCtx` already in scope | ~17 | `wrapTypedReturn` / `goExprGoType` / `exprToGoExpectGo` / `exprToGo` / `genWrappedFunc` / `coerceCallArgsAt` (etc.) | LOW — single-line swap, no signature change |
| **B** — Pure helpers callable from A-sites | ~25 | `goZeroValue` / `isParametricAliasInstantiation` / `solvedTypeToGoViaPipelineFlat` / `padBareParametricAliasArity` / `safeReturnTypeFullViaPipeline` / `safeReturnTypeFullBounded` / `isSealedIfaceReturningCall` | MEDIUM — add `LowerCtx` parameter; thread through transitive callers (≤3 levels up) |
| **C** — Deep dep-emission paths | ~15 | `generateDeclsForDep` / `generateGoMulti` / `generateDef` / inside `imports = unsafePerformIO $ do` block | HIGH — entry-point signature changes; touches the typed-emission entry contract |

(The 60th mention — `Compile.hs:861` — is the CAF's own definition.)

### Why "~20" undercount in the goal text

The verbatim "All ~20 call sites must thread `LowerCtx` explicitly"
was written in iter 33 (#671) when the iter-44 cascade hadn't yet
shrunk the original `getCgEnv` surface (then ~75 sites). After
iter 44 deleted `globalCgEnv` and renamed/inherited 57 readers
into `getCgEnvFromScope`, the real number is ~57. The goal's
INTENT — "no CAF, every reader sees `LowerCtx` explicitly" —
extends 1:1 to the renamed surface.

---

## Why this is multi-iter

Single-iter close would require:

1. Class-A swap (17 sites): trivial — `getCgEnvFromScope` →
   `case LC.lookupCgEnv ctx of Just e -> e; Nothing -> error "..."`.
   ~1 hour.
2. Class-B refactor (25 sites): each requires propagating a new
   `LowerCtx` parameter through 1-3 transitive callers. Some
   transitive callers are themselves Class-C (no ctx in scope).
   ~4-6 hours including diff inspection + spec audit.
3. Class-C refactor (15 sites): these live INSIDE the `imports`
   `unsafePerformIO` block of `generateGoMulti` / inside
   `generateDeclsForDep`'s pure `[GoDecl]` return. The Class-C
   re-shape requires either:
   - (a) Computing the cgEnv eagerly outside the unsafePerformIO
     block and passing it as a `let cgEnv = ... in ...` shadowed
     binding, replacing `getCgEnvFromScope` with that captured
     value. Sound when the cgEnv is **fully constructed before
     the IO action fires** — which is exactly what iter 44's
     `importsForced \`seq\` ...` barrier already enforces. So
     this is the architectural fix: capture-then-shadow.
   - (b) Threading `LowerCtx` through `generateDeclsForDep` / its
     callers up to `generateGo` / `generateGoMulti`. Larger
     surface change but cleaner separation.
4. Verification: 13-example sweep + 410-spec cabal + cold rebuild
   per iter for non-regression. ~25 min wall per iter.

Total honest budget: 3-5 iters for the close, depending on
whether Class-B reuses Class-A site additions and whether
Class-C goes Option (a) or (b).

---

## Iter plan (proposed)

### Iter 17 — Class A swap (17 sites)

- Find each `getCgEnvFromScope` reader inside a function whose
  signature already has `LC.LowerCtx`.
- Replace `let env = getCgEnvFromScope` with
  `let env = case LC.lookupCgEnv ctx of Just e -> e; Nothing -> emptyCgEnv`
  (or `error` with a contract message at sites where the ctx is
  guaranteed-installed by upstream barrier).
- Per the iter-47 audit at `Compile.hs:865-879`, the production
  contract is "`resetCompileState` always installs `initialCgEnv`
  before emission begins". So `Nothing` defaulting to `emptyCgEnv`
  is safe for spec-only invocations (`IsPlainIdent`) and is the
  identity-floor on the production path.
- Gate: 26-ui-showcase rt.Coerce count unchanged (`172`), cabal-test
  `--match Sky.Build` green, `examples/00-standard-libs` clean
  build.
- Commit: "v0.17 iter 17: criterion #3 Class A — 17 getCgEnvFromScope → LowerCtx swap".

### Iter 18 — Class B refactor (25 sites)

- For each helper without `LowerCtx`, add `LC.LowerCtx ->` to the
  signature.
- Propagate through transitive callers (each helper is called
  from ~1-3 sites; the closure traceback is bounded by the iter-15
  static reachability map).
- Class-B helpers include the renderer pipeline entries
  (`solvedTypeToGoViaPipelineFlat` / `padBareParametricAliasArity`
  / `safeReturnTypeFullBounded`) — these all consume `cgEnv`
  EXACTLY to build a `MappingContext` via `buildMappingContext`.
  Sound strategy: replace `getCgEnvFromScope` AT EACH SITE with
  `LC.lookupCgEnv ctx` → `Maybe CodegenEnv`; on `Nothing`, fall
  back to `Rec.emptyCgEnv` (renderer treats empty context as
  "no aliases known" → emits `any` for unknown types, which is
  the existing fallback semantics).
- Gate: SKY_RENDERER_DIFF=1 byte-identical to baseline on the
  13-example sweep.
- Commit: "v0.17 iter 18: criterion #3 Class B — pipeline helpers thread LowerCtx".

### Iter 19 — Class C: imports-block capture-then-shadow (15 sites)

- Inside `generateGoMulti`'s `imports = unsafePerformIO $ do ...`
  block: read `scopeStateRef` ONCE at the top, bind `cgEnvFinal`,
  pass it down via let-shadowing into each helper that previously
  called `getCgEnvFromScope`.
- The `importsForced \`seq\`` barrier already ensures the cgEnv
  is fully constructed before any downstream emission reads it,
  so the capture is correct.
- Inside `generateDeclsForDep` (Class-C top-level): add a
  `LC.LowerCtx` parameter, propagate from the single call site
  in `generateGoMulti` (which now has `cgEnvFinal` in scope).
- Gate: dep-emission specs (`DepSolvedTypesWiringSpec`,
  `T1LeakStandardLibsSpec`) green.
- Commit: "v0.17 iter 19: criterion #3 Class C — generateDeclsForDep + imports capture-shadow".

### Iter 20 — CAF deletion + final verification

- Delete `getCgEnvFromScope` from `Compile.hs:859-879`.
- Delete its haddock comment block at `Compile.hs:840-858`.
- Update the header comment block at `Compile.hs:82-95` to mark
  the migration complete.
- Full 13-example sweep + cabal-test + verify-cli + verify-all-web.
- Update CLAUDE.md "Closed in v0.17" entry.
- Commit: "v0.17 iter 20: criterion #3 — getCgEnvFromScope CAF DELETED".

---

## Stop conditions

- If at iter 17 the Class-A swap regresses any of the 13 examples
  → revert + investigate `Nothing`-defaulting semantics; the
  swap is supposed to be byte-identical because the iter-47
  audit asserts `Nothing` is unreachable on the production path.
- If at iter 18 a Class-B helper is called from a Class-C site
  whose ctx isn't yet threaded → bundle that Class-C site into
  iter 18 OR widen iter-19's scope.
- If at iter 19 the `imports`-block capture causes a `<<loop>>`
  (lazy-thunk cycle around `cgEnvFinal`'s construction) → revert
  to the IO-barrier `readIORef scopeStateRef` shape but with the
  result bound to a let so each helper sees the SAME captured
  snapshot (no fresh IORef read). Same end-state, slightly
  different threading shape.
- If at iter 20 a runtime panic surfaces under any well-typed
  Sky program → revert + file as a CAF-deletion side effect.

---

## Why this is the right close

1. **Criterion #3 INTENT.** The user's goal text targets impurity
   in codegen readers. The renamed `getCgEnvFromScope` is the same
   impurity class as the deleted `getCgEnv` — the rename was
   architectural cosmetics. Goal-fidelity (CLAUDE.md §0 hard rule 1)
   requires closing the impurity, not its name.
2. **The scopeStateRef channel STAYS.** The IORef is the
   writer-side bridge from `resetCompileState` / `continueCompile`
   into the codegen pass. That's load-bearing IO — it's not the
   CAF impurity that criterion #3 calls out. The fix is for the
   READER side to see the value as a captured `LowerCtx` field,
   not via `unsafePerformIO`.
3. **No new architectural debt.** Existing `LowerCtx._lc_cgEnv`
   field installed by iter 36's S2 writer is the proper home for
   the captured value. The CAF was always a transitional helper
   while the writers were being audited.

---

## What this iter (iter 16) ships

- This doc (the roadmap).
- Phase 4 Stage 2 was already shipped at branch tip `543252c2`
  (iter 15 close).
- No code changes in iter 16 — pure architecture work
  documenting the multi-iter close path.

Iter 17 begins the Class-A swap.

---

## Iter 17 — empirical block: Class-A swap is UNSOUND under
## current writer model

**Status:** Iter 17 attempted the Class-A swap (30 sites — wider
than the roadmap's "~17" because every `LC.LowerCtx`-receiving
function with `ctx`/`outerCtx` in scope is structurally a Class-A
candidate, not just the 5 named in the roadmap surface).

**Outcome:** REVERTED. Cold build of `examples/26-ui-showcase`
fails Sky-lowering→go-build with:

```
./main.go:2092: __subject_tAdt.Tag undefined (type Std_Ui_Breakpoint has no field or method Tag)
./main.go:2092: __subject_tAdt.Fields undefined
./main.go:2196: __subject_tAdt.Tag undefined (type Std_Ui_Element)
./main.go:2219: __subject_tAdt.Tag undefined
... [too many errors]
```

Also: `rt.Coerce` count on 26 jumps `172 → 253` (+81 — exactly the
class of regressions the iter-30 TRACK-2 work fought to close).

**Root cause:** The audit comment at `Compile.hs:850-854` is
load-bearing:

> Distinction from iter-37's failed attempt: iter-37 used
> `LC.lookupCgEnv ctx` where `ctx` was a CAPTURED parameter
> value. Captured ctx carries the snapshot from CAPTURE TIME
> (often post-C9, pre-C10), so `lookupCgEnv` returned Just
> (stale env). This helper reads `scopeStateRef` AT FORCE TIME
> — same trick S3 (finalGoSigMap migration) used. No
> captured-stale-ctx risk.

A Class-A swap substitutes a force-time IORef read with a
capture-time field read on `ctx`. For Class-A functions whose
`ctx` is constructed BEFORE the C10 cgEnv finalisation (this
includes `goExprGoType`, `wrapTypedReturn`, `exprToGoExpectGo`,
`exprToGo`, and every typed-codegen helper they invoke), the
capture-time `_lc_cgEnv` is either `Nothing` or a stale empty/
partial snapshot. The swap then routes through `emptyCgEnv` (or
worse, a stale partial) and:

- Sealed-iface dispatch in `caseToGo` / `caseToGoLegacy` /
  `caseBranchToStmts` doesn't see `_cg_sealedIfaceNames` →
  emits the legacy SkyADT shape `__subject_tAdt.Tag/Fields`
  where the user-side type is now sealed-iface → Go rejects.
- `exprToGoExpectGo`'s `finalRet` fallback can't read fresh
  `_cg_solvedTypes` → fails to elide some `rt.Coerce` wraps
  the iter-30/iter-15 work depended on.
- `wrapTypedReturn` skips sealed-iface ctor-body elision (the
  `_cg_sealedIfaceNames` membership check returns False on
  empty env) → emits identity `rt.Coerce` wraps that the
  helper exists specifically to elide.

**Architectural implication:** The naive Class-A swap is
fundamentally a non-starter while `scopeStateRef` is the
source-of-truth for cgEnv mutations across the C9→C10 phase
boundary. The IORef writer model means the cgEnv value visible
to a renderer-time GoExpr thunk is "whatever scopeStateRef
holds when the thunk forces" — NOT "whatever the captured
LowerCtx carries". Iter 38 introduced `getCgEnvFromScope`
specifically as the read-side counterpart of that contract.

**Correct close path (revised):** Criterion #3's INTENT cannot
be satisfied by point-substitution at the read sites. It
requires reshaping the writer side so the cgEnv is FULLY
CONSTRUCTED before any reader-thunk capture-time:

- **Option α — capture-then-shadow at imports block.** Move
  the `imports = unsafePerformIO $ do ...` block (which
  finalises scopeStateRef._lc_cgEnv at C10) to compute
  `cgEnvFinal :: CodegenEnv` purely, then thread it via a
  shadowed binding through every renderer entry. Removes the
  staleness gap by ensuring no reader thunk is constructed
  before cgEnv is final. ~Large surface change at
  `generateGoMulti`'s entry.
- **Option β — strict eager construction.** Force scopeStateRef
  to install C10's final cgEnv BEFORE `generateDeclsForDep` /
  `generateGoMulti`'s body fires. Then capture-time `ctx`
  carries the final value. Requires reordering
  resetCompileState's writer plan. Untried.
- **Option γ — accept the IORef CAF as architectural.** Mark
  `getCgEnvFromScope` as the load-bearing-pure-effect that
  fits the criterion-#3 SPIRIT (the IORef channel IS the
  reader bridge, not a CAF hiding mutation) and document the
  CAF + scopeStateRef pair as the legitimate close-state.
  This violates the goal's verbatim text ("`getCgEnv` CAF
  must be gone"); CLAUDE.md §0 hard rule 1 forbids interpreting
  the goal narrower than the verbatim wording.

**Iter 17 outcome:** Block filed. Code reverted. Documented for
next-iter architect agent. No commit beyond this doc update.

**Next iter (iter 18) must:** Architect Option α — capture-then-
shadow at the `imports`/`generateGoMulti` entry — and prove
soundness with the 26-ui-showcase regression class as the gate
(rt.Coerce ≤ 172 + go build clean).

**Sites attempted in iter 17 (for the next architect's record):**
30 sites across 12 enclosing functions —
- goExprGoType ×3 (L9450, L9579, L9698)
- wrapTypedReturn ×2 (L9785, L9804)
- exprToGoExpectGo ×1 (L12686)
- exprToGo ×7 (L13167, L13229, L13454, L13532, L13632, L13685, L13857)
- coerceCallArgs ×1, coerceCallArgsAt ×2 (incl. inferGoType arm at L15646)
- coerceArg ×1 (L16116)
- kernelCoerceArg ×1 (inferGoType arm at L16618)
- emitPartialUserCall ×1, binopToGo ×1, letToGo ×1, loweredDiscard ×1
- defToStmts ×2, caseToGo ×2, caseToGoLegacy ×3, caseBranchToStmts ×1

All 30 sites passed Haskell compile but produced ill-typed Go on
26-ui-showcase. The proximate failures all routed through
sealed-iface name set lookups (`_cg_sealedIfaceNames`) reading
empty/stale env at the destructure site.

---

## Reader who cares

When iter 20 closes, append a one-line entry to CLAUDE.md
"Closed in v0.17 (kept here for grep)":

> ~`getCgEnvFromScope` CAF (succeeded `getCgEnv` after iter-44
> globalCgEnv deletion) — DELETED iter 20. All 57 codegen reader
> sites now consult `LowerCtx._lc_cgEnv` (Maybe-defaulted to
> `emptyCgEnv` at unreachable spec-only sites). Closes criterion
> #3 of the v0.17 architectural close goal.
