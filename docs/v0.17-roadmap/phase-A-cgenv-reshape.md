# v0.17 Phase A — `cgEnv` reshape + CAF/IORef deletion (architectural close)

**Status:** DESIGN BANKED 2026-06-24 at branch tip `f4848aba`;
v3 plan locked 2026-06-24 (see `.claude/AUTONOMOUS_GOAL.md` §
"Architectural close plan v3").
**Phase A of 4-phase v0.17 close** (Phase 0 audits → Phase A
cgEnv reshape → Phase B sealed-iface → Phase D Stage 7+ →
Phase E sweep+fuzzer+tag). Phase C (runtime kernel mono)
DROPPED — Maybe/Result/Task already typed end-to-end.
**Scope:** Criterion #3 of the autonomous goal — IORef impurity
in codegen reader path DELETED **AND** any residual IORef carries
a machine-verified single-writer / single-reader monotonic
contract (locked wording — see CLAUDE.md §0.3 rule 1).
**Estimated:** 6-10 weeks wall clock @ 12+ iters (honest band
per `.claude/AUTONOMOUS_GOAL.md` 2026-06-24 decision lock;
supersedes the earlier "3-4 weeks @ 8-12 iters" placeholder
which underbudgeted the 23k-LOC mechanical surface and the
4-prior-attempt revert cost).

> **Read first:**
> - `.claude/AUTONOMOUS_GOAL.md` — the verbatim user goal.
> - `docs/v0.17-roadmap/criterion-3-caf-deletion.md` — surface audit (iter 16).
> - `docs/v0.17-roadmap/globalCgEnv-close-plan.md` — S4/S5 attempt log + rejected options.
> - `docs/v0.17-roadmap/getcgenv-migration.md` — Option A/B/C trade-offs.
> - `docs/architecture/sky-compiler-architecture.md` §2.6 + §4.1.
> - **CLAUDE.md §0 hard rule 3**: "load-bearing-but-pure" framing is FORBIDDEN.

---

## TL;DR

The current state has 43 `globalCgEnv`-related artefacts, 59
`getCgEnvFromScope` mentions (CAF + 57 readers), 144 `scopeStateRef`
mentions, 58 `unsafePerformIO`, and 393 `IORef` mentions across
Compile.hs (23k LOC). Three prior single-iter attempts (iter 17 / 37
/ 42) regressed because point-substitution at the read site fights
the writer model: cgEnv is mutated across the C9→C10 phase boundary,
and the readers force inside lazy GoExpr thunks AFTER the writer has
moved past their capture point. A reader that consults a captured
`LowerCtx._lc_cgEnv` gets a stale snapshot; a reader that consults a
CAF gets the freshest IORef value at force time.

Phase A's verified architectural lever: **make the cgEnv a VALUE
constructed once at a deterministic sequence point AFTER all writes
have settled, then threaded explicitly as a ReaderT field through
the emission pass**. The writer→reader race goes away because there
is no writer once the reader exists. The IORef channel is then
deleted because nothing reads it.

This is Option A from `globalCgEnv-close-plan.md` widened with the
PR-α Stage 3+4 phase extraction from `getcgenv-migration.md`. The
phase extraction is the missing precondition: until `emitPhase` is a
distinct phase running AFTER `solvePhase` returns its `SolveOutputs`,
the writer-then-read ordering cannot be enforced. The two plans were
filed independently in iter 16 + iter 33 because both authors saw the
local problem; merging them is Phase A's contribution.

---

## Invariants

### Invariants BEFORE (verified at branch tip `f4848aba`)

- **I1.** `scopeStateRef :: IORef LC.LowerCtx` holds the
  cumulative LowerCtx after the C9→C10 cgEnv install at
  `Compile.hs:5285` (writer side) and is read by every codegen
  helper that consults cgEnv (reader side).
- **I2.** `getCgEnvFromScope :: Rec.CodegenEnv = unsafePerformIO $
  readIORef scopeStateRef >>= return . LC._lc_cgEnv` is a NOINLINE
  CAF (`Compile.hs:860`) consumed at 57 reader sites. The NOINLINE
  pragma is load-bearing — without it GHC CSE shares one snapshot.
- **I3.** Dep emission constructs `[GoDecl]` thunks INSIDE the
  `imports = unsafePerformIO $ do …` block at `Compile.hs:5209`
  BEFORE the `importsForced \`seq\` …` barrier at `5353` runs. The
  thunks force LATER from the renderer (`generateGoMulti`'s return)
  by which time C10 has installed the final cgEnv onto scopeStateRef.
  Reader-thunk capture time vs force time is the load-bearing
  asymmetry; iter 17/37 broke it by capturing a stale ctx.
- **I4.** `globalAnonRecords` (in `Sky.Generate.Go.AnonRecords`)
  mutates during emit. Verified at iter 19 (#644). Its writer pattern
  is "register-on-first-mention from inside a thunk"; readers in
  `renderPackage` consume the final state after `importsForced`
  forces every dep thunk. The IORef is local to the registry module,
  not Compile.hs's CAF chain, but it shares the same hazard class.
- **I5.** `continueCompile` is a 2,800-LOC monolithic IO action that
  interleaves parse / canonicalise / solve / mono / lower / emit
  with intermediate IORef writes. Stage 1 (parse+reset, #657) and
  Stage 2 (canonicalise, #658) of the phase extraction have shipped;
  Stage 3 (solvePhase, #659) is in_progress.

### Invariants AFTER (target)

- **I1'.** `continueCompile` splits into `solvePhase :: IO
  SolveOutputs` (Stages 1-3 already shipped or in_progress) and
  `emitPhase :: ReaderT CompileCtx IO [GoDecl]`. The boundary is a
  pure value — `SolveOutputs` carries every solver-side artefact
  that emit consumes.
- **I2'.** Zero `unsafePerformIO` + `IORef` pairs in `Compile.hs`
  outside intentional, explicitly-gated build-cache (e.g.
  monomorphisation memo for cross-call σ reuse, IF such a cache is
  proven necessary by a benchmark; otherwise also gone). The CAF
  `getCgEnvFromScope` is DELETED. `scopeStateRef` is DELETED.
- **I3'.** Dep emission runs INSIDE `emitPhase`'s `ReaderT`,
  threading `cgEnv` as a Reader field. Every typed lowering helper
  has the cgEnv via `asks _ctx_cgEnv` (or a destructured ctx in
  scope). No reader-thunk capture-vs-force race because the cgEnv
  is constructed before emitPhase enters and never mutated after.
- **I4'.** `globalAnonRecords` either (a) flows as a `StateT` layer
  on top of the `ReaderT` for emitPhase's mutable registries, or
  (b) is replaced by an explicit "two-pass" pattern: pass 1 walks
  the GoIR to collect anonymous shapes; pass 2 renders with the
  finalised registry. Decision deferred to Phase A iter 11 after
  benchmarking the StateT overhead.
- **I5'.** `continueCompile` is a thin orchestrator that calls
  `parsePhase >>= canonPhase >>= solvePhase >>= emitPhase` with no
  IORef writes between stages.

### Off-the-table for Phase A

- **Phase B+ scope.** Std.Ui.Element sealed-iface migration,
  runtime kernel monomorphisation, Phase 4 Stage 7+ — these target
  criterion #1 (rt.Coerce reduction). Phase A does NOT change
  rt.Coerce counts. The 26-ui-showcase floor (172 Coerce / 190
  AsListT) is the regression gate, not the deletion target.
- **`SKY_*` env-var IORefs.** Configuration channel; not in
  criterion #3's scope. Documented separately if a follow-up
  surfaces them.
- **`globalReachableProgram`** (defused via SolveOutputs at iter 25)
  — already closed; mentioned here only to clarify it is NOT a
  Phase A surface.

---

## Methodology refinement — what this plan does differently

The 4 prior failed attempts (iter 17 / 37 / 42 / various Class-A
swap iters) share a pattern:

1. Architect grills a substitution at the reader sites.
2. Adversary agent passes the design as locally-sound.
3. Executor swaps `getCgEnv*` for ctx-field-read at N sites.
4. Cold-build regression: rt.Coerce floor moves OR Go-source
   compile error (`Anon_R_*` undefined / `__subject_tAdt.Tag`
   undefined on sealed-iface).
5. Revert + post-mortem identifies "writer phase boundary not
   threaded".

The repeating root cause: **the substitution is sound IFF the cgEnv
visible at force time matches the cgEnv visible to the CAF at force
time**. With per-site ctx-captures, this requires every emitter that
constructs a thunk to capture the LATEST ctx, AND every writer that
mutates cgEnv to also update every captured ctx — a quadratic
update problem that the writer side cannot solve.

**Refined methodology for Phase A**:

1. **Eliminate the writer-then-read race at the architectural
   level, not the call-site level.** This means the cgEnv must be
   constructed ONCE, AT A KNOWN SEQUENCE POINT, AFTER all writers
   have run, BEFORE any reader forces. The only way to enforce
   this without IORef is to make construction and consumption live
   in distinct phases (solvePhase → emitPhase).
2. **Per-iter gate is empirical, not symbolic.** Each iter MUST:
   (a) cold-build 26-ui-showcase (largest Std.Ui surface — most
   sensitive to cgEnv staleness); (b) cold-build 13-skyshop (76k
   FFI symbols — most sensitive to inspector contract); (c) check
   rt.Coerce count unchanged within ±0 on 26-ui-showcase floor;
   (d) run locked cabal-test specs (DepSolvedTypesWiring,
   T1LeakStandardLibs, AnonRecordWriter, RendererParity,
   StrictHmArityGate).
3. **Per-commit adversarial grill.** Per CLAUDE.md
   `feedback_v017_per_commit_grill`: every Compile.hs edit gets a
   pre-implementation grill agent that adversarially examines:
   (a) writer-then-read ordering preservation; (b) lazy-thunk
   capture timing; (c) wildcard-`any` soundness gate not breached;
   (d) FFI interface satisfaction axiom not breached. PASS verdict
   required before executor runs.
4. **Drift detection per CLAUDE.md §0 hard rule 3.** Each iter
   entry quotes the verbatim goal text. Forbidden framings in any
   "complete" claim: "load-bearing-but-pure", "documented as X",
   "shipped for the scope of \[my chosen subtask\]". The IORef must
   be DELETED — there is no acceptable "documented" outcome.
5. **Continuous-Judge loop at Phase A close.** At iter 12, spawn
   an independent adversarial Judge agent with the verbatim
   criterion #3 text + read access to the post-Phase-A branch. If
   the verdict carries any "but / except / however / caveat /
   mostly / essentially", Phase A is NOT closed and we plan
   closure of the cited gaps.
6. **Hold the line on Phase A scope.** It is tempting under
   pressure to expand to Phase B (Std.Ui.Element) when adversarial
   review surfaces a sealed-iface adjacency. Phase A does NOT
   migrate sealed-iface. If a finding genuinely blocks Phase A,
   document and STOP per CLAUDE.md §0 hard rule 4 — solicit user
   direction. Do not silently widen scope.

---

## Pre-iter-0 audits (Phase 0 deliverable — THIS workflow)

Before Phase A iter 1 lands a single line of refactor, three
audit artifacts MUST exist on the branch. They are the surface
inputs that downstream iters consume; without them the per-iter
adversarial grill cannot evaluate whether a substitution is
sound.

### Audit 1 — Bracketed-writer audit

**Artifact:** `docs/v0.17-roadmap/phase-A-iter-0-bracketed-writers.md`
(exists at branch tip `f4848aba`).

**Purpose.** Enumerate every writer site to `scopeStateRef` and
every dependent CAF read, classify each as (a) intra-emit
intra-dep, (b) intra-emit inter-dep, (c) post-emit renderer-time,
or (d) ambient pre-emit. The classification drives iter 4-5's
threading decisions (intra-dep folds into per-dep StateT;
inter-dep moves into SolveOutputs; renderer-time stays inside
EmitM; ambient pre-emit becomes CompileCtx construction
input).

**Gate.** Audit doc lists at least every writer surfaced by
`grep -n "modifyIORef scopeStateRef\|writeIORef scopeStateRef"
src/Sky/Build/Compile.hs` + every reader surfaced by
`grep -n "getCgEnvFromScope" src/Sky/Build/Compile.hs`.
Classification column populated for each entry.

### Audit 2 — `globalAnonRecords` contract

**Artifact:** `docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md`
(authored at Phase 0 close, this workflow).

**Purpose.** Document the locked Option (c) decision per
2026-06-24: `globalAnonRecords` remains as a bounded-monotonic
IORef WITH an explicit documented contract +
machine-verified spec gate. The contract has two parts:

1. **Source contract (docstring on the IORef definition).**
   Names the writer site (`registerAnonRecordShape` in
   `Sky.Generate.Go.AnonRecords`), names the reader sites
   (`renderPackage`'s anon-record decl walker), names the
   monotonic invariant ("register-on-first-mention; subsequent
   mentions of the same canonical shape are no-ops; values
   only grow; end-of-module strict-eval barrier forces the
   final state before any reader fires").

2. **Spec gate.** `Sky.Build.AnonRecordWriterAuditSpec`
   (extant at iter 19 per #644) extended to assert:
   - Every writer call site is one of the documented sites.
   - No write OVERWRITES a previous shape registration; if a
     shape is mentioned twice, the second mention is a no-op.
   - The reader (renderer-time decl walker) ONLY fires AFTER
     the strict-eval barrier `importsForced \`seq\``.
   - A failing write OR a stale read fails the spec, NOT
     "documented as load-bearing-but-pure".

**Why Option (c) is not a §0 hard rule 3 violation.** Rule 3
forbids "load-bearing-but-pure" as a FRAMING that lets impurity
survive without a substantive guarantee. Option (c)'s contract
+ spec gate IS the substantive guarantee — the IORef can only
mutate in the bounded-monotonic shape, and a violation is a
spec failure not a documentation argument. The user reviewed
this trade-off at 2026-06-24 and authorised Option (c)
explicitly, with the contract+spec as the load-bearing
artifacts (not the docstring text alone).

**Gate.** Contract doc exists; the spec extension lands as a
separate commit BEFORE Phase A iter 1 ships any refactor.

### Audit 3 — Baseline spec (rt.Coerce + IORef snapshot)

**Artifact.** A new `Sky.Build.PhaseAEntrySnapshotSpec` (or
extension to `Sky.Build.RtCoerceBudgetSpec`) that records the
Phase A entry state:

- `rt.Coerce` count on `26-ui-showcase` (current floor 172 OR
  whatever the floor is at Phase A iter 1 entry; the gate just
  pins the entry value).
- `rt.AsListT` count (current floor 190).
- IORef enumeration of `Compile.hs`: every `IORef` definition,
  every `unsafePerformIO`, every `modifyIORef` / `writeIORef`,
  every CAF reader (`getCgEnvFromScope` etc.).

**Purpose.** Phase A's gate is "rt.Coerce floor UNCHANGED" + "named
IORefs DELETED". Without an entry-state baseline pinned in a
spec, drift in either direction during iters 1-11 is invisible
until iter 12's Judge runs. The baseline spec makes drift
visible at every per-iter gate run.

**Gate.** Spec exists, runs in `cabal-test`, records the entry
state as code (not as a markdown table). Subsequent iters
ratchet the IORef counts DOWN as they delete; the rt.Coerce
counts MUST stay equal (Phase A doesn't move them).

### Phase 0 close gate

All three audits exist + their respective specs are GREEN +
the contract doc explicitly names the locked Option (c)
decision. Only then does Phase A iter 1 begin.

---

## Iter breakdown (12+ iters; honest band per 2026-06-24 lock)

Each iter ships ONE coherent commit. Per-iter gates are run at the
end of each iter. The "Stop conditions" block per iter documents the
adversarial-grill question that, if it fails, halts the iter for
re-architecture.

**Pre-iter-0 audits MUST exist before iter 1 begins** — see
§ "Pre-iter-0 audits" above.

### Iter 1 — Extract `solvePhase` (in_progress; #659 wraps up)

**Scope.** Move solver invocation (`constrainExpr` → `solveAll` →
`SolvedTypes` construction) out of `continueCompile` into a pure
`solvePhase :: ParsedSources -> CanonResults -> IO SolveOutputs`
that returns a `SolveOutputs` record carrying everything emit needs:
`_so_solved :: SolvedTypes`, `_so_goSigMap`, `_so_csiByCallee`,
`_so_anonRecords` (the v2 channel — populated by solver
record-discovery, distinct from the current `globalAnonRecords`
which fires during emit).

**Behaviour change.** ZERO. `continueCompile` calls the extracted
function with the same inputs and gets the same outputs. The IORef
writes still happen at the SAME points; we are only relocating the
read sites that consume those values.

**Gate.** scripts/build.sh clean. Sky.Type 66/0/0. 26-ui-showcase
rt.Coerce==172, rt.AsListT==190. 13-skyshop clean. cabal-test
DepSolvedTypesWiring + T1LeakStandardLibs green.

**Stop condition.** If `SolveOutputs` cannot be constructed without
re-reading `globalCgEnv` from inside the solve callbacks → the
solver itself is impure → file a sub-blocker and STOP. (Adversarial
grill must catch this BEFORE executor runs.)

**Commit.** `v0.17 phase-A iter 1: extract solvePhase from continueCompile (pure refactor)`.

### Iter 2 — Extract `emitPhase` signature (still IORef-backed, but typed)

**Scope.** Wrap `generateGoMulti` + `generateGo` + `generateDecls`
into `emitPhase :: SolveOutputs -> IO [GoDecl]`. The body is
mechanically identical to today; only the entry signature changes.
The IORef writers/readers continue to work because `scopeStateRef`
is still in scope module-globally.

**Behaviour change.** ZERO. Same diff hygiene as iter 1.

**Gate.** Same as iter 1.

**Stop condition.** If `generateGoMulti` references a stateful
value that is NOT in `SolveOutputs` (e.g. an env-var IORef bound at
process entry) → trace back to the source and either fold into
`SolveOutputs` OR thread as a separate Reader field for iter 3.

**Commit.** `v0.17 phase-A iter 2: extract emitPhase signature (still IORef-backed)`.

### Iter 3 — Introduce `CompileCtx` record + `ReaderT` scaffold (mtl)

**Scope.** Define
```haskell
data CompileCtx = CompileCtx
    { _ctx_cgEnv :: !Rec.CodegenEnv         -- the deletion target
    , _ctx_solvedTypes :: !SolvedTypes      -- already on LowerCtx; promoted
    , _ctx_goSigMap :: !GoSigMap            -- the C10 final sig map
    , _ctx_anonRecords :: !AnonRegistry     -- if Option-a in iter 11
    , _ctx_inspector :: !Inspector          -- already threaded; consolidated
    , _ctx_module :: !ModuleName.Canonical  -- current module being emitted
    }

type EmitM = ReaderT CompileCtx IO
```
Add `runEmitT` helpers. **At this iter the `EmitM` monad is NOT YET
USED — the type infrastructure ships first; behaviour migration is
iter 4+.**

**Behaviour change.** ZERO. Pure additive: `EmitM` is defined but
no existing function uses it. Existing IORef code untouched.

**Gate.** Same as iter 1. Plus: `EmitM` round-trips via a unit
test (`return ()` in EmitM produces `()` via `runEmitT mkCtx`).

**Stop condition.** If `CompileCtx` accumulates >10 fields, that's
a sign the phase boundary is in the wrong place — re-check
`SolveOutputs` should hold static data and `CompileCtx` should hold
the emit-only data. Adversarial grill must confirm the field
partition before executor runs.

**Commit.** `v0.17 phase-A iter 3: CompileCtx record + ReaderT scaffold (additive)`.

### Iter 4 — Thread `CompileCtx` through entry-module emission

**Scope.** The entry module's emission path (`generateGoMulti`'s
"the user's `main` module" branch) runs INSIDE `EmitM`. Helpers
called only from this path widen their signature to take `EmitM`.
Helpers called from BOTH entry and dep paths take a `LowerCtx`
argument that carries the cgEnv as `_lc_cgEnv` (which is already
the iter-36 S2 shape).

**Behaviour change.** Per-site: each helper reads cgEnv from `ctx`
instead of `getCgEnvFromScope`. The IORef writer side STILL fires
(this is the dual-write phase). The reader side is now
EmitM-routed for entry module.

**Adversarial grill must verify.** (a) Every helper called from
entry path no longer reads `getCgEnvFromScope` UNLESS it's also
called from dep path AND the dep path still routes via IORef.
(b) No helper closes over a stale ctx — captures happen INSIDE
`EmitM`'s `ask` boundary.

**Gate.** 26-ui-showcase rt.Coerce==172. 13-skyshop clean. NEW
gate: `SKY_CGENV_DIFF=1` env-var instruments BOTH paths in parallel
(EmitM ctx read AND `getCgEnvFromScope` read) and asserts the two
values are structurally equal at every reader site that the iter
migrated. Spec: `Phase4CgEnvDiffSpec`.

**Stop condition.** If `SKY_CGENV_DIFF=1` reports any divergence,
the writer model is not yet writer-then-read — the writer is
firing AFTER the EmitM ctx was constructed. Revert and trace the
late-writer; fix it in the writer-side iter (iter 5 may need to
absorb).

**Commit.** `v0.17 phase-A iter 4: thread CompileCtx through entry-module emission`.

### Iter 5 — Thread `CompileCtx` through dep emission (the hardest)

**Scope.** `generateDeclsForDep` becomes
`generateDeclsForDepEmitM :: ModuleName -> EmitM [GoDecl]`. The
`imports = unsafePerformIO $ do …` block at `Compile.hs:5209`
becomes a `ReaderT.local (\ctx -> ctx { _ctx_module = depMod }) $
generateDeclsForDepEmitM depMod` call. The C10 cgEnv installation
moves OUT of the `imports` thunk into a deterministic
`importsM :: EmitM [GoDecl]` that runs strictly BEFORE the dep
emission ReaderT call.

**Behaviour change.** This is the iter where the writer-then-read
race actually closes. After iter 5, `_ctx_cgEnv` carries the
post-C10 final cgEnv for every emit reader. Dual-write to IORef
still happens; iter 9 deletes it.

**Adversarial grill must verify.** (a) The C10 cgEnv construction
that previously fired from INSIDE the `imports` thunk now fires
BEFORE the EmitM ctx is constructed — i.e. it's been hoisted OUT
of the lazy thunk and into the strict `EmitM` setup. (b) The
`patchMissingAnonRecordDecls` safety net at `Compile.hs:5544-5577`
is not bypassed (it stays as the runtime backstop for any anon-
record registered after the C10 freeze). (c) `instanceMangledName`
(the hardest reader, runs at renderer-force time) now reads from
EmitM's ctx — and the renderer is itself inside EmitM (no
`renderPackage` lazy escape).

**Gate.** Same as iter 4. Plus the locked
`AnonRecordWriterAuditSpec`. Plus a NEW spec
`Phase4DepEmissionOrderSpec` that constructs a 2-dep program and
asserts the deps' cgEnv-reads see the merged-cgEnv at every site.

**Stop condition.** If the EmitM ctx for dep N reads a cgEnv that
matches an EARLIER state than dep N's writers expected to see
their own writes in — i.e. the dep's own writers fire AFTER its
own readers — the dep-internal write contract is intra-dep, not
inter-dep. Either fold the intra-dep writes into a per-dep StateT
layer OR document why this is sound (e.g. all intra-dep writers
are anon-record registration which has the runtime backstop).

**Commit.** `v0.17 phase-A iter 5: thread CompileCtx through dep emission (closes writer-then-read race)`.

### Iter 6 — Migrate Class A 17 reader sites to CompileCtx

**Scope.** The 17 Class A sites from
`criterion-3-caf-deletion.md` § "Surface" — sites where `ctx ::
LC.LowerCtx` is already in scope at the call site. Mechanical
swap: `getCgEnvFromScope` → `asks (LC._lc_cgEnv . _ctx_lowerCtx)`
or equivalent. Migration is now sound because iters 4+5 guarantee
`_ctx_cgEnv` carries the post-C10 final value.

**Behaviour change.** ZERO observable. Same value visible at each
reader site, just routed through EmitM.

**Gate.** Same as iter 4. Plus `SKY_CGENV_DIFF=1` must pass on the
13-example sweep.

**Stop condition.** If any reader site shows a divergence under
`SKY_CGENV_DIFF=1`, that site needs a per-thunk capture; document
and route via `EmitM`'s `ask` AT FORCE TIME, not at thunk
construction.

**Commit.** `v0.17 phase-A iter 6: migrate Class A 17 reader sites to CompileCtx`.

### Iter 7 — Migrate Class B 25 reader sites

**Scope.** The 25 Class B sites — pure helpers that need a
`LowerCtx` parameter added to the signature. Threading depth ≤3
transitive callers. Closes the helpers
`solvedTypeToGoViaPipelineFlat`, `padBareParametricAliasArity`,
`safeReturnTypeFullViaPipeline`, `safeReturnTypeFullBounded`,
`isSealedIfaceReturningCall`, `goZeroValue`,
`isParametricAliasInstantiation`.

**Adversarial grill.** Each helper's threading change must be
audited for: (a) does any caller still rely on the CAF being CURRENT
at force time, distinct from the captured-ctx? If yes — that's the
dep-emission race re-surfacing; document and defer to iter 5
re-work; do NOT widen iter 7. (b) Renderer-time helpers
(`instanceMangledName`) need a special route — they consume an
EmitM ctx via a closure captured at GoExpr-construction time;
verify the closure captures `ask`-result, not a `LowerCtx`-local
snapshot.

**Gate.** Same as iter 4.

**Stop condition.** Same as iter 6.

**Commit.** `v0.17 phase-A iter 7: migrate Class B 25 reader sites (pipeline helpers)`.

### Iter 8 — Migrate Class C 15 reader sites

**Scope.** The 15 Class C sites — sites inside the `imports`
unsafePerformIO block of `generateGoMulti` and inside
`generateDeclsForDep`. After iter 5 the imports block is no longer
unsafePerformIO-wrapped (it's `EmitM`), so most Class C sites
auto-resolve. The residual ones are deep-inside-thunk closures
that need explicit `ReaderT.local` wrapping.

**Adversarial grill.** Verify the 2 sites listed as
"`unsafePerformIO $ readIORef globalCgEnv`" at `Compile.hs:12808`
(instanceMangledName) are now reading via EmitM ctx. These are
the renderer-time sites; the close requires the renderer itself
to be inside EmitM.

**Gate.** Same as iter 4. Plus `SKY_CGENV_DIFF=1` passes on EVERY
example in the 13-example sweep.

**Stop condition.** Same as iter 6.

**Commit.** `v0.17 phase-A iter 8: migrate Class C 15 reader sites (renderer-time)`.

### Iter 9 — DELETE `getCgEnvFromScope` CAF

**Scope.** With all 57 reader sites migrated (iters 6+7+8), the
CAF has zero callers. Delete the definition at `Compile.hs:860`
and the docstring at `Compile.hs:840-858`. Also delete the
header-comment "load-bearing-but-pure" framing at any line that
still carries it (per the iter-33 plan's "forbidden frames"
list).

**Gate.** `grep -c "getCgEnvFromScope" src/Sky/Build/Compile.hs ==
0`. `grep -c "getCgEnv " src/Sky/Build/Compile.hs == 0`. Full
13-example sweep clean. 26-ui-showcase rt.Coerce==172.

**Stop condition.** If grep finds any residual mention (in a
comment block, in a docstring), bundle the cleanup into iter 9 —
do not punt to iter 10.

**Commit.** `v0.17 phase-A iter 9: DELETE getCgEnvFromScope CAF`.

### Iter 10 — DELETE `scopeStateRef` IORef + dual-write writer audit

**Scope.** `scopeStateRef` survived as the writer-side bridge in
iters 1-9. Now its writers have nothing left to bridge — all
readers are EmitM. Delete:
- The IORef definition at `Compile.hs:508-510`.
- The 7 writer sites that did `modifyIORef scopeStateRef
  (LC.withCgEnv …)` / `(LC.withAliases …)` / etc.
- The `seedEarlyCgEnv` shadow-install added in iter 36 S2.

The writers' DATA still flows into `SolveOutputs` (via iter 1) or
into `CompileCtx` construction (iter 3+). The IORef channel is
just a stale bridge.

**Gate.** `grep -c "scopeStateRef" src/Sky/Build/Compile.hs == 0`.
`grep -c "globalCgEnv" src/Sky/Build/Compile.hs == 0`. Sweep clean.
`grep -c "IORef" src/Sky/Build/Compile.hs <= 10` (carve-out for
anonRecords + any explicitly-gated build cache).

**Adversarial grill must verify.** No writer that fed
`scopeStateRef._lc_cgEnv` is silently dropped — each writer's
DATA must reach `_ctx_cgEnv` via the SolveOutputs/CompileCtx path.
Trace every deleted writer.

**Stop condition.** If a writer is genuinely live (some emit code
mutates cgEnv mid-emit), Phase A's architecture is incomplete —
the writer must move into SolveOutputs OR a StateT layer (iter
11 decides). Stop and re-architect.

**Commit.** `v0.17 phase-A iter 10: DELETE scopeStateRef IORef + 7 writer sites`.

### Iter 11 — `globalAnonRecords` decision (Reader vs accepted carve-out)

**Scope.** Decide and implement the anon-record channel close.
Three options, benchmarked:

- **Option (a) — StateT layer in EmitM.** `EmitM` becomes
  `ReaderT CompileCtx (StateT AnonRegistry IO)`. Registration
  inside thunks becomes `modify (registerAnon …)`. Reads at
  render time become `get`. Pros: principled, no IORef. Cons:
  every helper that registers an anon shape widens to `EmitM`
  (already done via iter 3+) and the StateT overhead may be
  measurable; the `patchMissingAnonRecordDecls` safety net at
  `Compile.hs:5544-5577` becomes a no-op (registry is monotonic
  by construction).
- **Option (b) — Explicit two-pass.** Pass 1 walks the
  finalised GoIR to collect anon shapes into a pure `Map`. Pass
  2 renders with the map. Pros: zero stateful effect during
  emit; pure Map lookup. Cons: requires the GoIR to be fully
  constructed before any anon shape is needed; this may
  conflict with thunk-driven incremental emission.
- **Option (c) — Document `globalAnonRecords` as scoped, with
  the strict end-of-module barrier as the contract.** Verified
  monotonic single-writer single-reader at iter 19 (#644).
  Pros: zero refactor. Cons: VIOLATES the goal text per CLAUDE.md
  §0 hard rule 3 — "load-bearing-but-pure" is the forbidden
  framing. Listed for completeness; will be rejected by adversarial
  grill.

**Recommended.** Option (a). Benchmark the StateT overhead on
13-skyshop (worst-case anon shapes); if >5% wall-clock regression,
fall back to Option (b).

**Adversarial grill must verify.** Whichever option ships, NO
documentation entry of `globalAnonRecords` survives in the
"acceptable load-bearing impurity" category. The IORef is either
deleted (a/b) or it stays but EVERY mention in source code is
gone (the registry module survives but its public API is the
pure registry, not the IORef).

**Gate.** Same as iter 10. Plus
`grep -c "globalAnonRecords" src/ == 0` if (a) or (b) ships.

**Stop condition.** If both (a) and (b) regress beyond a wall-
clock budget (>10% on 13-skyshop OR Sky.Build.* test time >2×) →
STOP and solicit user direction. Do not silently accept Option
(c).

**Commit.** `v0.17 phase-A iter 11: globalAnonRecords reshape (StateT/two-pass — chosen via bench)`.

### Iter 12 — Phase A close — adversarial Judge against I1'-I4'

**Scope.** No code changes. Spawn an INDEPENDENT adversarial Judge
agent with the verbatim text of criterion #3 from
`.claude/AUTONOMOUS_GOAL.md`:

> 2 surviving module-level IORefs (`globalCgEnv` + `globalGoSigMap`)
> actually DELETED, not documented as "load-bearing-but-pure". The
> `getCgEnv` CAF must be gone. All ~20 call sites must thread
> `LowerCtx` explicitly.

Plus pre-supplied evidence:
- `grep` counts above (zero for the named tokens).
- `SKY_CGENV_DIFF=1` zero divergences on the sweep.
- 26-ui-showcase rt.Coerce floor unchanged.
- 13-example sweep clean.
- Sky.Test 131/131.
- All locked specs green (DepSolvedTypesWiring,
  T1LeakStandardLibs, AnonRecordWriterAudit, RendererParity,
  StrictHmArityGate, Phase4CgEnvDiff, Phase4DepEmissionOrder).

The Judge must verify the LITERAL claim, not a narrower lens. The
verdict is binary: "100% ACHIEVED + VERIFIED" or "NOT ACHIEVED —
\<N\> gaps". Forbidden in PASS: "but", "except", "however",
"caveat", "mostly", "essentially", "for the scope of", "modulo".

**If NOT ACHIEVED.** Plan closure of the cited gaps in 1-2 more
iters; re-run the Judge.

**If 100% ACHIEVED.** Phase A closes. Update CLAUDE.md "Closed in
v0.17" with the one-line entry from `criterion-3-caf-deletion.md`
§ "Reader who cares". File the Phase B + C + D + E iter plans as
their own design docs (Phase A does NOT include Phase B's design).

**Commit.** None (verification iter). The Judge's verdict is the
artefact, banked into this doc as an appendix.

---

## Verification gate (at Phase A close, iter 12)

Hard pass criteria — ALL must hold per the LOCKED criterion #3
wording (CLAUDE.md §0.3 rule 1, 2026-06-24):

> Criterion #3 = `{globalCgEnv, globalGoSigMap, scopeStateRef,
> env-CAFs}` DELETED **AND** any residual IORef in `Compile.hs`
> carries a machine-verified single-writer / single-reader
> monotonic contract.

1. `grep -c "globalCgEnv\|globalGoSigMap\|getCgEnvFromScope\|scopeStateRef" src/Sky/Build/Compile.hs == 0`.
2. `grep -c "unsafePerformIO" src/Sky/Build/Compile.hs == 0` OR
   each surviving site has an EXPLICIT, REVIEWED carve-out
   comment naming what it caches AND a spec gate proving the
   monotonic-only invariant (per the `globalAnonRecords`
   precedent at
   `docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md`).
   Carve-outs accumulate to ≤3 sites; anything more is
   unacceptable.
3. `grep -c "IORef" src/Sky/Build/Compile.hs <= 10`. Carve-out
   detail per (2) — EVERY surviving IORef must have BOTH a
   source-level docstring naming the writer + reader sites AND
   a `Sky.Build.*` spec asserting the bounded-monotonic
   invariant. No "documented as load-bearing-but-pure"
   reframe is acceptable (§0 hard rule 3); the spec gate IS
   the substantive guarantee.
4. 26-ui-showcase: rt.Coerce count UNCHANGED vs Phase A entry
   floor (172). NB: Phase A does NOT target rt.Coerce reduction
   — that's Phase B+. Any reduction at Phase A is a bonus, but
   the floor MUST hold.
5. 26-ui-showcase: rt.AsListT count UNCHANGED vs entry floor (190).
6. 13-example sweep: every example builds clean from a wiped
   slate (`rm -rf sky-out .skycache .skydeps && sky build`).
7. Sky.Test: 131/131 assertions pass.
8. cabal-test locked specs green:
   - `Sky.Build.DepSolvedTypesWiringSpec`
   - `Sky.Build.T1LeakStandardLibsSpec`
   - `Sky.Build.AnonRecordWriterAuditSpec`
   - `Sky.Build.RendererParitySpec`
   - `Sky.Type.StrictHmArityGateSpec` (9/0/0)
   - `Sky.Type.Limitation7CurrentLooseAcceptanceSpec` (6/0)
   - NEW: `Sky.Build.Phase4CgEnvDiffSpec`
   - NEW: `Sky.Build.Phase4DepEmissionOrderSpec`
9. `SKY_CGENV_DIFF=1` reports zero divergences on the 13-example
   sweep. (Then is REMOVED in the close commit per
   `globalCgEnv-close-plan.md` § "Verification path".)
10. Independent adversarial Judge agent returns
    "VERDICT: 100% ACHIEVED + VERIFIED" with no caveats.

---

## Risks + mitigations

### R1 — ReaderT threading through 23k LOC of Compile.hs is mechanically large

**Threat.** Even with mtl support, mechanically widening every
helper signature is a multi-thousand-line diff. Risk of
introducing typos / wrong-monad call-site errors that cabal-build
catches but local-test passes.

**Mitigation.** Per-iter (iters 6-8) sub-batches of ≤25 sites.
Each sub-batch's adversarial-grill validates the helper-to-caller
threading depth. Each sub-batch is a SEPARATE commit, easy to
revert. Bench `scripts/cabal-test.sh` after each sub-batch.

### R2 — Breaks the lazy-evaluation contract some current code relies on

**Threat.** Several emitters construct `GoExpr` thunks that force
LATER (e.g. inside `renderPackage`'s `[GoDecl]` walk). The
laziness is load-bearing: it lets the renderer observe the FINAL
cgEnv even though the thunk was constructed before C10's writer
fired. EmitM threading collapses lazy → strict at the
`ReaderT.local` boundary; thunks captured from outside EmitM see
a frozen ctx.

**Mitigation.** Iter 5 (the dep emission threading) is where this
breaks. The architectural fix: hoist the C10 cgEnv construction
to BEFORE the EmitM ctx is built, so by the time the first thunk
is constructed, the cgEnv is already final. The `importsForced
\`seq\`` barrier already enforces "C10 fires before any thunk
forces"; we widen this to "C10 fires before any thunk is
constructed". The change is verified by `SKY_CGENV_DIFF=1` at
every reader site.

**Stop condition (iter 5).** If hoisting C10 to pre-EmitM is
impossible because some C10 inputs are only available
post-canonicalisation-of-a-late-dep, then the writer-then-read
ordering has a structural cycle and Phase A's architecture is
wrong. STOP and solicit user direction. Acceptable resolution:
StateT layer for cgEnv mutations (Option iter 11 (a) widened to
cgEnv too).

### R3 — Dep emission inside Reader changes timing of `scopeStateRef` writer firing

**Threat.** Some writer fires during dep N's emission and the
write was load-bearing for dep N+1's emission (cross-dep cgEnv
contamination). The IORef channel happened to bridge this
intra-emit cross-dep coupling.

**Mitigation.** Adversarial grill at iter 5: audit every writer
that fires inside `generateDeclsForDep` and confirm whether its
output is consumed (a) intra-dep (folds into per-dep StateT or
the dep's emit-result), or (b) inter-dep (must move into
SolveOutputs or pre-dep-loop cgEnv construction). Intra-dep
writers are sound to localise; inter-dep writers indicate the
PHASE BOUNDARY is in the wrong place — the dep emission depends
on the previous dep's emit result, which is OK because dep order
is determined by the import graph.

**Empirical check.** Per iter 5 spec
`Phase4DepEmissionOrderSpec` — construct a 2-dep program where
dep A's emit registers a record alias and dep B's emit consumes
it. Verify B sees A's registration. If not, the cross-dep
channel is broken.

### R4 — Iter 4-5's dual-write phase doubles the writer surface

**Threat.** During iters 4-5, the IORef and the EmitM ctx both
fire on every cgEnv mutation. If they get out of sync (one
writer forgets to update the other), reader divergence emerges.

**Mitigation.** `SKY_CGENV_DIFF=1` is the in-spec divergence
detector. Every iter 4-9 runs the diff check on the sweep before
declaring done.

### R5 — Performance regression from StateT overhead (iter 11 Option a)

**Threat.** `ReaderT CompileCtx (StateT AnonRegistry IO)` adds a
monadic layer; the `modify`/`get` calls inside hot anon-record
registration paths may regress wall-clock.

**Mitigation.** Iter 11 benchmarks BOTH (a) and (b) on
13-skyshop. If (a) regresses >5%, ship (b). If both regress
>10%, STOP and solicit user direction.

### R6 — Adversarial Judge returns NOT ACHIEVED with a finding outside Phase A scope

**Threat.** Judge agent reads the verbatim goal text and flags
e.g. "`globalGoSigMap` IORef SURVIVES" — but that IORef is
actually deleted (iter 44 closed it). Or Judge flags a
`scopeStateRef` mention in a deleted-since comment block. False-
positive findings extend Phase A indefinitely.

**Mitigation.** Iter 12 Judge prompt includes EVIDENCE LINKS:
the precise commit hashes that closed each predecessor IORef +
the grep-count outputs. Judge's read access is to the current
branch tip, not to historical state. A finding is only
load-bearing if it's reproducible on current tip.

### R7 — Phase A widens into Phase B (Std.Ui.Element sealed-iface)

**Threat.** Iter 5 adversarial grill surfaces a sealed-iface
adjacency: the cgEnv-routing change exposes a latent rt.Coerce
emission that only fires in Std.Ui.Element-touching emit
paths. Tempting to fix in Phase A; doing so breaks the phase
discipline.

**Mitigation.** CLAUDE.md `feedback_v017_no_midway_stop` allows
multi-week mandates; CLAUDE.md `feedback_never_stop_midway`
distinguishes "scope drift" from "genuine blocker". Sealed-iface
work is scoped to Phase B by user direction. If iter 5 surfaces
a sealed-iface issue, document it in `phase-B-sealed-iface.md`
(to be authored at Phase A close) and CONTINUE Phase A. The
rt.Coerce floor at iter 5 may shift if sealed-iface emit
interacts with cgEnv routing; the regression gate at iter 12
accepts the new floor IFF Judge agrees the shift is consistent
with Phase A's stated invariant (rt.Coerce floor unchanged
EXCEPT where a writer-then-read race correction reveals a
sealed-iface that was already mis-routed; reductions OK,
regressions NOT).

### R8 — User direction needed mid-phase (genuine implementation blocker)

**Threat.** Iter 5's adversarial grill exposes a structural cycle
in the writer model (R2). Iter 11's benchmarks show both Option
(a) and (b) regress >10%. The cgEnv mutation pattern in some
canonicaliser-side hook is not visible from Phase A's surface
analysis.

**Mitigation.** Per CLAUDE.md §0 hard rule 4: STOP at the
blocker, PushNotification user with a concrete description of
what direction is needed. Do NOT silently widen scope; do NOT
revert to "load-bearing-but-pure" framing.

---

## Confidence verdict (this workflow's input to the user's
"high confidence → proceed; if not, let's talk" decision)

**My honest verdict: MEDIUM-HIGH confidence in this plan.**

What boosts confidence:

- The 3 prior failed attempts (iter 17 / 37 / 42) all failed at
  the SAME architectural seam (writer-then-read race across the
  C9→C10 boundary inside lazy thunks). The seam is now identified
  and Phase A targets it directly with the phase-extraction
  architecture (solvePhase → emitPhase). This is a sufficient
  fix, not just a tactical move.
- The per-iter empirical gate (`SKY_CGENV_DIFF=1` differential)
  catches regressions at the earliest possible iter, not at
  Phase A close.
- The per-commit adversarial grill enforces the discipline that
  caught the iter-37 root cause AFTER the regression. Front-
  loading the grill catches the same class earlier.
- PR-α Stages 1-2 already shipped; Stage 3 is in_progress. The
  phase extraction is not greenfield work — there's existing
  momentum.
- The two prior plan docs (`criterion-3-caf-deletion.md` +
  `globalCgEnv-close-plan.md`) identified the architectural fix
  paths (Option α capture-then-shadow / Option A ReaderT
  threading) but each was scoped narrower than the actual
  problem. Phase A is the merged, full-scope close.

What tempers confidence:

- The iter 11 anonRecords decision is genuinely open. Option (c)
  is the only PERMANENTLY ZERO-RISK choice for shipping, but
  CLAUDE.md §0 hard rule 3 forbids it. Options (a) and (b) both
  carry residual schedule risk if the benchmarks don't fit.
- 23k LOC of Compile.hs is enough surface that some untested
  call path may fire only under a rare example combination —
  the 13-example sweep is the broadest gate, but it doesn't
  cover user-provided 4th-party code in skydeploy tenants etc.
  Phase A's gate is "Sky compiler is internally consistent";
  Phase B/C/D/E gates expand the surface.
- The wall-clock budget is 3-4 weeks @ 8-12 iters. The 4 prior
  attempts averaged 1 iter each to ship + 1 iter to revert; if
  Phase A's iters need >3 iter cycles to ship (each attempt
  reverts twice before succeeding), the budget doubles. The
  per-commit grill mitigates but cannot eliminate this.
- The 5-phase plan as a whole (Phase A-E) is 17-24 weeks. If
  the user's stars-and-credibility-window shrinks (e.g. a v0.18
  feature pressure builds), Phase A closure may need to ship
  alongside an in-flight Phase B start, breaking phase
  discipline.

**Net.** Proceed with Phase A as designed. The methodology
refinements (per-iter `SKY_CGENV_DIFF=1`, per-commit adversarial
grill, deterministic phase extraction first) genuinely address
the root cause of the prior failures. Phase B-E plans should be
authored at Phase A close, not before — they depend on Phase A's
outcome (e.g. iter 11's anonRecords decision shapes whether
Phase B can rely on a StateT layer).

**Talk first IF:** the user expects Phase A to close in <2 weeks,
or expects the rt.Coerce floor to MOVE (it won't — Phase A is
infra). Both are off-the-table for Phase A's scope.

---

## Appendix A — iter 12 Judge agent prompt template

> You are an INDEPENDENT adversarial Judge verifying whether Phase
> A of the v0.17 architectural close has 100% achieved its target
> on the Sky compiler at `/Users/anzel/works/playground/sky`,
> branch `feat/v0.17-fully-typed-codegen` @ `<SHA>`.
>
> The target is criterion #3 of `.claude/AUTONOMOUS_GOAL.md`,
> verbatim:
> ```
> 2 surviving module-level IORefs (globalCgEnv + globalGoSigMap)
> actually DELETED, not documented as "load-bearing-but-pure". The
> getCgEnv CAF must be gone. All ~20 call sites must thread
> LowerCtx explicitly.
> ```
>
> Phase A scope (broader than the verbatim text, per the merged
> plan at `docs/v0.17-roadmap/phase-A-cgenv-reshape.md`):
> - DELETE `globalCgEnv` IORef and successor CAF
>   `getCgEnvFromScope`. (verbatim half)
> - DELETE `scopeStateRef` IORef (the bridge under the successor
>   CAF). (architectural close half)
> - Migrate all 57 reader sites to `CompileCtx` via `ReaderT`.
>   (verbatim "~20" updated to ~57 per iter-16 audit)
> - `globalAnonRecords` reshape (StateT / two-pass / accepted
>   carve-out — iter 11 decision).
>
> Pre-supplied evidence:
> - `grep -c "globalCgEnv\|globalGoSigMap\|getCgEnvFromScope\|scopeStateRef"
>   src/Sky/Build/Compile.hs` should be 0.
> - 26-ui-showcase rt.Coerce==172, rt.AsListT==190.
> - 13-example sweep clean from wiped slate.
> - Locked specs green per the verification gate above.
>
> VERIFY the LITERAL claim, not a narrower interpretation. Examples
> of disqualifying findings:
> - Any `unsafePerformIO`-backed CAF reading codegen state in
>   `Compile.hs` outside an EXPLICITLY-REVIEWED carve-out (≤3
>   carve-outs accumulated; each must have a comment naming the
>   reviewed cache).
> - Any "load-bearing-but-pure" framing in source or docs.
> - Any reader site that consults a captured-stale snapshot of
>   cgEnv (verify by inspecting closures vs `ask`-time reads in
>   EmitM helpers).
> - Any rt.Coerce floor regression on 26-ui-showcase.
> - Any locked spec failure.
>
> Map every disqualifying finding to a concrete file:line +
> reproduction. List in priority order.
>
> Final verdict — EXACTLY one of:
> - `VERDICT: 100% ACHIEVED + VERIFIED — <one-line proof>`
> - `VERDICT: NOT ACHIEVED — <N> gaps; highest priority: <gap>`
>
> Forbidden in PASS verdict: "but", "except", "however", "caveat",
> "mostly", "essentially", "for the scope of", "modulo".
