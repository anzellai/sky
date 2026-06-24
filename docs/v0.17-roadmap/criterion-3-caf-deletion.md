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

## Reader who cares

When iter 20 closes, append a one-line entry to CLAUDE.md
"Closed in v0.17 (kept here for grep)":

> ~`getCgEnvFromScope` CAF (succeeded `getCgEnv` after iter-44
> globalCgEnv deletion) — DELETED iter 20. All 57 codegen reader
> sites now consult `LowerCtx._lc_cgEnv` (Maybe-defaulted to
> `emptyCgEnv` at unreachable spec-only sites). Closes criterion
> #3 of the v0.17 architectural close goal.
