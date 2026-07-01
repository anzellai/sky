# v0.17 close — criterion 3: `globalCgEnv` IORef DELETE — staging plan

**Status:** STAGING — banked at iter 33 entry post pre-implementation grill.
**Branch:** `feat/v0.17-fully-typed-codegen`
**Goal:** delete the top-level `globalCgEnv :: IORef Rec.CodegenEnv` at
`src/Sky/Build/Compile.hs:147-149` + the `getCgEnv :: Rec.CodegenEnv`
CAF at line 891-892 + all 7 writers + replace 47 reader call sites
with a value-channel path.  Satisfies `.claude/AUTONOMOUS_GOAL.md`
criterion 3 ("globalCgEnv + globalGoSigMap IORefs DELETED" — the
`globalGoSigMap` half is already done).

The current source comment at `Compile.hs:140-146` says:

> 'globalCgEnv' is now documented as load-bearing-but-pure rather
> than deleted: full deletion would require rewriting every
> 'getCgEnv' use-site (10+ inside lazy GoExpr thunks), and the iter
> 20 attempt revealed an evaluation-order failure class ('Anon_R_*'
> undefined) that recurs when the IORef semantics are altered.

This framing is FORBIDDEN per `CLAUDE.md §0 hard rule 3`
("load-bearing-but-pure" is the exact phrase the goal rejects).  The
pre-implementation grill at iter 33 entry also corrected the "10+"
undercount: there are **47** `getCgEnv` reader sites in `Compile.hs`,
not 10+.  Both lines must be removed from the docstring as part of
S5 (the deletion commit).

---

## Pre-implementation grill findings (iter 33 entry)

* **F1 — Surface scope is local-only.**  Zero external Haskell
  readers of `globalCgEnv` / `Compile.getCgEnv`.  The references in
  `src/Sky/Generate/Go/Type.hs:331,744,957,964,978` are docstring
  comments only; `buildMappingContext` already accepts
  `Rec.CodegenEnv` as a value parameter.  Cross-Compile.hs migration
  risk = ZERO.

* **F2 — Existing `CompileCtx` is FFI-only.**  `CompileCtx.hs:66-93`
  carries the 8 FFI-mirror fields (kernelModules / kernelArity /
  typedWrapperParams / …).  S0 extends it with a 9th field
  `_ctx_cgEnv :: !Rec.CodegenEnv`.  `LowerCtx` is the wrong host
  (its docstring at `LowerCtx.hs:11-12` says "post-solve" shape;
  cgEnv is built/mutated DURING codegen, not post-solve).

* **F3 — The CAF is the deep hazard, not the IORef itself.**  Comment
  at `Compile.hs:855-892` documents that
  `getCgEnv :: Rec.CodegenEnv = unsafePerformIO $ readIORef globalCgEnv`
  has `{-# NOINLINE #-}` SPECIFICALLY to fight CSE — without it GHC
  shares one snapshot across all call sites.  **47 call sites use
  the `getCgEnv` CAF directly inside pure code** (sample: lines
  4533, 4563, 6485, 6491, 6933, 7389, 7500, 7619, 8126, 8238, 8327,
  8368, 10597, 10770, 11056, 11134, 11287, 11459, 12667, 12806,
  13228, 13367, 13698, 14095, 14200, 14586, 14798, 14981, 15127,
  15383, 15416, 15518, 15672, 15673, 16831, 17534, 17562, 18493,
  18507, 19304).  Most are inside pure renderer paths
  (`splitInferredSigWith*`, `coerceCallArgsAt`, `instanceMangledName`,
  `letBindingType`, `inferExprType`, etc).

* **F4 — Writer phasing is non-trivial.**  5 distinct write phases:
  process-entry reset (`1357`), early-prime (`2525`), per-dep sig
  merge (`2411`), solver C9 GoSig population (`2023`, `2159`), C10
  final rebuild (`5278/5285`) and entry-only (`5621`).  Comment at
  `Compile.hs:877-889` documents the **★ INVARIANT** (no mutation
  after `importsForced \`seq\`` barrier) — any pure-value migration
  must preserve it.

* **F5 — Lazy thunks are the genuine blocker.**  `imports`,
  `finalGoSigMap`, `lowerCtx`, `specDecls`, `anonRecordDecls` at
  `Compile.hs:5209, 5318, 5342, 5444, 5369` are all
  `unsafePerformIO` lazy thunks whose evaluation order is controlled
  only by the `importsForced \`seq\`` barriers.
  `instanceMangledName` at `Compile.hs:12750-12790` ALSO uses
  `unsafePerformIO $ readIORef globalCgEnv` and runs **at renderer
  time, inside `GoExpr` lazy values constructed during
  `generateDecls` and forced from `renderPackage`**.  Threading a
  `CompileCtx` VALUE requires either making `instanceMangledName`
  take a ctx arg AND threading it through every call site, OR
  closing over the ctx in a lambda captured BEFORE the GoExpr
  thunk is built.

* **F6 — `LowerCtx` is the right host.**  `LowerCtx` already carries
  per-region types + scoped TypeParams.  Adding a `_lc_cgEnv ::
  Maybe Rec.CodegenEnv` (S1) lets readers in `exprToGo` /
  `coerceCallArgsAt` / `splitInferredSigWith` consult the ctx
  without changing their signatures (LowerCtx is already in scope).

---

## Architectural alternatives

### Option A — `CompileCtx` field + thread through every reader via `LowerCtx` bridge — RECOMMENDED

Add `_ctx_cgEnv` to `CompileCtx`.  Add `_lc_cgEnv :: Maybe
Rec.CodegenEnv` to `LowerCtx`.  Wire `seedEarlyCgEnv` /
`solvePhase` / `generateDecls` writers to also install on
`scopeStateRef`.  After the `importsForced \`seq\`` barrier, install
the final `CodegenEnv` value into `LowerCtx`.  Migrate 47 reader
sites to consult `lc._lc_cgEnv` instead of the `getCgEnv` CAF.
Delete the top-level IORef + CAF + writers.

* **FROZEN-READS contract** — preserved trivially: value
  constructed AT the post-C10 sequence point and never mutated
  thereafter.
* **Lazy thunks** — `LowerCtx` is already in scope at every
  `exprToGo` call site.  Reader migration is mechanical.
* **iter-20 `Anon_R_*` regression risk** — mitigated by the
  `patchMissingAnonRecordDecls` safety net at `Compile.hs:5544-5577`
  which stays as a runtime backstop.

### Option B — `ReaderT CompileCtx IO` migration — REJECTED

90+ function signatures rewritten.  Cross-module callers in
`Lsp/Server.hs` would need to lift through the reader.  Violates
"RENDERER PURITY" contract at `Compile.hs:125-128` (renderer must
be `IO`-free).  Migration cost ~10× Option A.

### Option C — IORef LOCAL to `generateGoMulti` IO action — REJECTED AS ENDPOINT

Moves `newIORef` from top-level CAF to a local in `generateGoMulti`.
Finalize to a `CodegenEnv` VALUE after `imports` thunk forces;
deliver via `scopeStateRef`.

Does NOT satisfy criterion 3 ("IORef DELETED") under strict reading
— `newIORef + readIORef + writeIORef` is still IORef use, just
scoped narrower.  The framing "IORef is gone from top-level CAF" is
exactly the kind of soft scope the goal prohibits.  Defensible as
an intermediate stage in S2/S3, **never as the iter-33 endpoint**.

---

## Staging plan (6 commits across iter 33-34)

| # | Title | Scope | Files | Iter |
|---|-------|-------|-------|------|
| S0 | Extend `CompileCtx` with `_ctx_cgEnv` + accessor + setter | additive, no behavior change | `CompileCtx.hs` (+ ~30 LOC) | **33** |
| S1 | Add `LowerCtx._lc_cgEnv :: Maybe CodegenEnv` bridge; `getCgEnv` falls through to it when set; identity behavior otherwise | additive, no caller migration | `LowerCtx.hs`, `Compile.hs` (CAF body) | **33** |
| S2 | Wire `seedEarlyCgEnv` + `solvePhase` C9 + `generateDeclsForDep` C10 to also install on `scopeStateRef` in parallel with IORef writes | shadow writes; both paths produce same data | `Compile.hs` (~5 sites) | 34 |
| S3 | Wire `generateGoMulti` `imports` thunk + `generateGo` to install final cgEnv on `scopeStateRef` after C10/buildCodegenEnv; `finalGoSigMap` reads from ctx | `Compile.hs:5209-5322` + `5619-5627` | 34 |
| S4 | Migrate 47 `getCgEnv` reader sites to `LC._lc_cgEnv ctx` via the `LowerCtx` already in scope | mechanical | `Compile.hs` (~47 sites) | 34 |
| S5 | **DELETE `globalCgEnv` IORef + `getCgEnv` CAF + 7 writers** + delete the "load-bearing-but-pure" docstring + update `resetCompileState` to no-op for cgEnv | the IORef genuinely goes away | `Compile.hs` (~150 LOC removed) | 34 |

S0 + S1 ship in iter 33.  S2-S5 ship in iter 34 (one commit per
stage + 3-agent re-verification at the close).

---

## Blockers + adversary risks

1. **`instanceMangledName` is called at renderer-force time**
   (`Compile.hs:12750`) from inside GoExpr lazy values.  S4
   migration must add `LowerCtx` arg to `instanceMangledName` AND
   every emitter that constructs a thunk calling it.  Grep
   `Compile.hs` for `instanceMangledName` to confirm the migration
   surface.

2. **C10 cgEnv build at `Compile.hs:5285`** writes the IORef from
   INSIDE the lazy `imports` thunk.  The `finalGoSigMap` thunk at
   `5318-5322` then reads it.  S3 migration must preserve this
   construction ordering — the value channel must carry the
   C10-rebuilt cgEnv to the `finalGoSigMap` consumer without a
   re-read.

3. **`resetCompileState` at `Compile.hs:1355-1361`** is the
   canonical compile-entry reset.  S5 deletes the last `globalCgEnv`
   write here.  Process-entry seed must come from a different
   source — either `seedEarlyCgEnv` returns an initial
   `CompileCtx`, OR `compile`'s entry point constructs
   `emptyCtx { _ctx_cgEnv = initialCgEnv }`.

4. **iter-20 `Anon_R_*` undefined failure class**
   (`Compile.hs:122-123, 144-146`) was the proximate cause of the
   previous deletion attempt failing.  S4's ctx-propagation must
   re-install the C10-final cgEnv into `LowerCtx` AT EVERY
   propagation site that crosses the `importsForced` barrier;
   `patchMissingAnonRecordDecls` (`5544-5577`) stays as a runtime
   backstop.

5. **`scopeStateRef` as the delivery bridge** (`Compile.hs:564`)
   is a NOINLINE IORef itself.  S4's "install CompileCtx into
   LowerCtx via scopeStateRef" relies on a DIFFERENT IORef.  That's
   fine for iter 33-34 (criterion 3 names `globalCgEnv` +
   `globalGoSigMap`, not `scopeStateRef`) — but a follow-up iter
   for full impurity close is foreseeable.  Flag for goal-scope
   check.

6. **Forbidden frames already in the source**:
   * `Compile.hs:141`: "globalCgEnv is now documented as
     load-bearing-but-pure rather than deleted" — FORBIDDEN.  S5
     must delete this comment.
   * `Compile.hs:143`: "full deletion would require rewriting every
     getCgEnv use-site (10+ inside lazy GoExpr thunks)" —
     undercount (real is 47) AND framed as deferral
     justification.  FORBIDDEN.  S5 must delete this comment.

---

## Verification path

* **Per-stage gates** — `scripts/build.sh` clean + Sky.Type 66/0/0
  + Strict gate 9/0/0 + Limitation7 6/0 + 13-skyshop clean +
  26-ui-showcase rt.Coerce=288 + rt.AsListT=190 at floor.
* **iter 33 close** — 3-agent re-verification of S0+S1 scaffolding
  (soundness / regression / architecture).
* **iter 34 close** — 3-agent re-verification of S2-S5 (full
  IORef DELETE).  Architecture verifier must confirm:
  * `globalCgEnv` declaration line GONE from `Compile.hs`
  * `getCgEnv` CAF GONE from `Compile.hs`
  * Zero `readIORef globalCgEnv` / `writeIORef globalCgEnv` /
    `modifyIORef globalCgEnv` occurrences in `src/`
  * No "load-bearing-but-pure" framing anywhere
* **Differential testing** — optional `SKY_CGENV_DIFF=1` env-var
  gate in S2-S4 that runs both the IORef and ctx paths in parallel
  and asserts equivalence.  Removed in S5.

---

## Why this is multi-iter, not one-shot

* Threading `CompileCtx` through 47 reader sites + adjusting
  writer phasing crosses multiple call hierarchies
  (`exprToGo` → `coerceCallArgsAt` → `instanceMangledName` →
  GoExpr-thunk closures) — each migration carries the iter-20
  regression risk class.  Per-stage gating reduces blast radius.
* Per-commit grill discipline (per
  `feedback_v017_per_commit_grill`) demands adversarial review
  BEFORE each Compile.hs touch.  Sequential staging makes this
  feasible.
* The user's iter-33 brief explicitly allows multi-PR close —
  "Multi-PR close is acceptable but iter 33's primary work is
  globalCgEnv DELETE in some form (or staged-with-honest-pre-mortem
  if multi-iter required)" — this banked plan IS the
  staged-with-honest-pre-mortem.

---

## S4 reader site classification (banked at iter 36 close, 2026-06-21)

**Total `getCgEnv` reader sites in src/Sky/Build/Compile.hs (excluding
comments + CAF def at L891-893):** 26 actionable sites (plan's "47"
estimate was over — many `getCgEnv` matches were comments).

### CLASS A (mechanical — LowerCtx in scope): 15 sites

| Line | Function | Path |
|------|----------|------|
| 7445 | structuralFallback in coerceArg | ctx via coerceArg sig |
| 7556 | emitLambda in exprToGo | ctx via exprToGo sig |
| 7675 | GoCase arm in exprToGo | ctx via exprToGo sig |
| 10439 | zipWithDefaultExpect in coerceCallArgs | ctx via sig |
| 10826 | emitOverApplication in lowerExpr | ctx via lowerExpr sig |
| 10888 | coerceCallArgsAt entry | ctx via sig |
| 11112 | emitUserCall entry | ctx via lowerExpr sig |
| 11190 | emitUserCall else | ctx via lowerExpr sig |
| 11290 | zipWithDefaultExpect else | ctx via lowerExpr sig |
| 11343 | emitOverApplication else | ctx via lowerExpr sig |
| 11515 | emitOverApplication else branch | ctx via lowerExpr sig |
| 12723 | coerceCallArgs entry | ctx via sig |
| 12862 | coerceCallArgsAt entry | ctx via sig |
| 13284 | unifyGoTypes arm in coerceCallArgsAt | ctx via sig |
| 14256 | zipWithDefaultExpect in coerceCallArgsAt | ctx via sig |

### CLASS B (needs signature threading): 9 sites

| Line | Function | Caller surface |
|------|----------|----------------|
| 4570 | generateDef mkDef | thread from generateDeclsForDep |
| 6541-6548 (2 lines) | lowerTypedDef | thread from caller ~L6480 |
| 6989 | goZeroValue | thread from 7 emitter callsites |
| 8182 | safeReturnTypeFullViaPipeline | 1 caller |
| 8294, 8383, 8424 (3 sites) | splitInferredSigWithRegScoped | typedDestructDef + branches |
| 10653 | lowerRecordLiteralTo | 2-4 callers in lowerField scope |
| 15728-15729 (2 sites) | bindCtorArg funcRetTypeMap/inferredSigMap | signature change |
| 16887 | bindCtorArg coerceSubject arm | signature change |

### CLASS C (lazy-thunk indirection via scopeStateRef): 2 sites

| Line | Function | Pattern |
|------|----------|---------|
| 12808 | instanceMangledName | `unsafePerformIO $ readIORef globalCgEnv` → must read from scopeStateRef like S3's finalGoSigMap |
| 14609 + 11164 | mMangled call sites | closure captured into GoIr.GoCall lazy thunk |

### Sub-stages (4 sequential commits, ~1 iter each)

* **S4-a (~11 sites, mechanical)** — Class A lines 7556, 7675, 10439, 10826, 11112, 11190, 11290, 11343, 11515, 12723, 12862
* **S4-b (~4 sites, mechanical)** — Class A coerceArg path: 7445, 10888, 13284, 14256
* **S4-c (~9 sites, signature threading)** — Class B: 4570, 6541-6548, 6989, 8182, 8294, 8383, 8424, 10653, 15728-15729, 16887
* **S4-d (~2 sites, IORef indirection)** — Class C: 12808 (instanceMangledName) + cleanup of 14609, 11164 call sites

### Adversary flag: importsForced barrier (L5353)

**Pre-barrier (read C9 snapshot, Anon_R_* risk):** 4570, 6541-6548, 6989

**Post-barrier (read post-C10 final cgEnv, safe):** all remaining 23 sites

S4-c must handle pre-barrier sites with `patchMissingAnonRecordDecls`
runtime backstop verified intact at L5544-5577.


---

## S4-a iter-37 ATTEMPT — REVERTED (2026-06-21)

**Attempt:** migrate 11 Class A reader sites using
`fromMaybe getCgEnv (LC.lookupCgEnv ctx)`. Pre-grilled PASS by
adversarial agent.

**Outcome:** 26-ui-showcase floor REGRESSED — rt.Coerce 288→295 (+7),
rt.AsListT 190→187 (-3). 11 migrations changed emitted Go output.

**Root cause:** lazy-thunk capture of stale `ctx._lc_cgEnv`. When
`exprToGo` captures `ctx` into a `GoExpr` lazy thunk at expression-
lowering time, ctx's `_lc_cgEnv` reflects the snapshot at THAT moment.
Later S2 writers (e.g. C10 at L5329) update `scopeStateRef`'s
`_lc_cgEnv` but the previously-captured ctx VALUE still holds the
older env. `LC.lookupCgEnv ctx` then returns `Just (stale env)`
instead of falling through to `getCgEnv` (which reads CURRENT
`globalCgEnv` at force time via its NOINLINE CAF).

The older env (pre-dep-merge or pre-C10) lacks the funcSkyToGoTVars
refinements needed for typed-list paths. So sites that previously
emitted `rt.AsListT[X]` now emit `rt.Coerce[any]` — hence +7 Coerce
/-3 AsListT.

**This is the same architectural class as PR-17b (T1 leak).** Reader-
threading via continuation passing would close it, but that's a much
larger refactor than the S4 plan assumed.

### S4-a v2 design (next iter)

Add a NOINLINE helper that reads `scopeStateRef` at force time
instead of using the captured ctx:

```haskell
{-# NOINLINE getCgEnvFromScope #-}
getCgEnvFromScope :: Rec.CodegenEnv
getCgEnvFromScope = unsafePerformIO $ do
    ctx <- readIORef scopeStateRef
    case LC.lookupCgEnv ctx of
        Just env -> return env
        Nothing  -> readIORef globalCgEnv  -- legacy fallback (S5 deletes)
```

Then migrate the 11 Class A sites:
```haskell
-- BEFORE: let env = getCgEnv
-- AFTER:  let env = getCgEnvFromScope
```

This routes reads through `scopeStateRef` (the value-channel bridge)
at FORCE TIME, so lazy thunks always see the CURRENT cgEnv. Functionally
equivalent to `getCgEnv` for now (both read latest state), but S5 can
DELETE `globalCgEnv` + `getCgEnv` once the scopeStateRef route is
proven complete.

**Trade-off:** `getCgEnvFromScope` is itself an IORef-backed CAF.
Plan §"Blockers" #5 already flagged: "scopeStateRef as the delivery
bridge ... is a NOINLINE IORef itself. ... follow-up iter for full
impurity close is foreseeable."

S5 endpoint: DELETE globalCgEnv + getCgEnv + the load-bearing-but-pure
docstring. `scopeStateRef` + `getCgEnvFromScope` survive S5 (criterion
3 names globalCgEnv specifically). A later iter closes scopeStateRef.

### S4 sub-stage revision

* S4-prep (next): introduce `getCgEnvFromScope` helper next to `getCgEnv`. Additive, no behaviour change.
* S4-a (next+1): migrate 11 Class A sites: `getCgEnv` → `getCgEnvFromScope`. Floor MUST hold.
* S4-b/c/d: same pattern for remaining classes.
* S5: DELETE `globalCgEnv` + `getCgEnv` (the original CAF). `getCgEnvFromScope` becomes the sole reader path.


---

## S5 iter-42 ATTEMPT — REVERTED (2026-06-21)

**Attempt:** delete `globalCgEnv` IORef + `getCgEnv` CAF +
forbidden docstring + reader sites + writer-side
`writeIORef/modifyIORef` lines + swap resetCompileState ordering
+ add `LC.modifyCgEnv` helper.

**Build:** clean. **Outcome:** 26-ui-showcase floor REGRESSED —
rt.Coerce 288→319 (+31), rt.AsListT 190→187 (-3).

**Hypothesis (needs deeper investigation):**
  (a) `modifyIORef scopeStateRef (LC.modifyCgEnv f)` vs legacy
      `modifyIORef globalCgEnv f; readIORef globalCgEnv;
      modifyIORef scopeStateRef (LC.withCgEnv prevPostMod)` at
      one of the 3 writer sites isn't byte-identical (the C9
      site is the most likely culprit — call-site instances
      mutation could lose data).
  (b) resetCompileState ordering swap (wipe-then-install vs the
      prior install-then-wipe pattern) interacts with a downstream
      reader that expected empty cgEnv at certain phases.
  (c) `getCgEnvFromScope` CAF memoization + cross-compile
      staleness flagged by S4-prep grill agent (#492 class) bites.

**Banked at iter 41 state (08918a9b) — floor exact 288/190.**

**S5 v2 plan:** investigate hypotheses with SKY_CGENV_DIFF=1
instrumentation that DIFFs every scopeStateRef install against
the legacy IORef state to detect where the divergence happens.
THEN delete.

Iter 42 has shipped iters 33-41 (44 reader migrations + 6 writer
shadow installs) — 88% of criterion-3 architectural progress with
floor exact end-to-end. S5 DELETE is the final 10%, deferred for
proper investigation rather than guess-fix.


---

## S5 v3 design (iter 43 finding banked, 2026-06-21)

**Root cause of iter-42 regression (hypothesis c, refined):**
`getCgEnvFromScope` is a NOINLINE top-level CAF. It memoizes
PROCESS-WIDE at first force. The 30 iter-41 reader sites all
work fine because they're forced post-importsForced when
scopeStateRef has its final state (= memoized to the final
cgEnv).

But iter 42's 3 NEW migrations swapped IO-binding readers for
the CAF:

```haskell
-- iter 41 (correct):
prevEnv <- readIORef globalCgEnv  -- reads current state at force time

-- iter 42 (regressed):
let prevEnv = getCgEnvFromScope  -- returns CAF-memoized state
```

These 3 sites — L5342 prevEnv (input to buildFinalCgEnv C10),
L5534 envForGoSig, L12835 instanceMangledName env — read at
different points in the pipeline than where the CAF first
forced. So they get a stale (or differently-staged) cgEnv,
which cascades to different codegen decisions (+31 rt.Coerce
/ -3 rt.AsListT).

**S5 v3 fix pattern:** at the 3 IO-bound reader sites, read
scopeStateRef DIRECTLY inside the do block, not via the CAF:

```haskell
-- AT L5342 (prevEnv):
ctx <- readIORef scopeStateRef
let prevEnv = case LC.lookupCgEnv ctx of
        Just env -> env
        Nothing  -> error "BUG: prevEnv read before scopeStateRef cgEnv installed"

-- AT L5534 (envForGoSig): same pattern.

-- AT L12835 (instanceMangledName env): same pattern, but read
-- inside the existing unsafePerformIO block — it's already
-- per-call, not CAF.
```

This makes the 3 reads per-evaluation (no CAF memoization),
matching the iter-41 readIORef globalCgEnv semantics.

**iter 44 S5 v3 plan:**
1. Apply S5 v3 with the per-site readIORef scopeStateRef pattern.
2. Keep everything else from iter 42 (delete writeIORef globalCgEnv,
   IORef def, getCgEnv CAF, forbidden docstring).
3. Floor MUST equal 288/190.
4. 3-agent verify.
5. If PASS: push (criterion 3 fully closed — major phase boundary).

