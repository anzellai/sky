# v0.17 Stabilization Postmortem + Plan

> **Status**: Branch `feat/v0.17-pure-sound-codegen` @ `fc9c53b7` (post-revert)
> **Reality check**: v0.17 is less stable than v0.16.x — confirmed.
> **Author intent**: Honest assessment + minimum-risk stabilization path.

---

## TL;DR

**v0.17 is a 432-commit architectural refactor masquerading as a release.** v0.16.x ran patch
releases of 1-5 commits each; v0.17 has accumulated nearly 100× that into a single tag. The
codegen architecture has been re-plumbed mid-flight (PR-α through PR-22 / iter 17-42), but the
refactor is **incomplete**: surfaces designed to be unified (one renderer, one env channel,
one dispatch arm per call shape) still have 6 parallel renderers, multiple env channels, and
overlapping dispatch arms with gaps.

The user-visible damage:
- 3 examples (`05-mux-server`, `11-fyne-stopwatch`, `13-skyshop`) that built on v0.16.x now
  fail with `undefined: rt.Go_<Pkg>_<method>` — typed-FFI dispatch routes through a wrong
  arm.
- Bundled console regenerate-script CI gate fails because `solvedTypeToGoViaPipelineFlat` is
  called with `emptyCgEnv`, losing the alias resolution context.
- Any attempt to env-thread the pipeline regression-cascades into 10 cabal-test specs
  (`CrossModuleLambdaCollisionC`, `DepCurrentModuleHint`) that **encode the bug-compatible
  env-free behavior as their expected output**.

This document is the plan to close all three without continuing the pattern of "fix here,
break there".

---

## What v0.17 actually shipped (commit categorisation)

432 commits since `46b7eaf7` (origin/main HEAD = v0.16.29 baseline). Breakdown:

| Category | Count | % | Examples (top SHAs) |
|---|---:|---:|---|
| Renderer/codegen refactor | 127 | 29.4% | `8b2bf86e` (pipeline helper), `a2d519c2` (mapSkyTypeToGo) |
| Test infra + spec gates | 86 | 19.9% | `cc946d09` (ScopeStateRefAuditSpec), `a807e069` (PanicClassGateSpec) |
| IORef defusing | 78 | 18.1% | `070d170c` (FfiTypedWrapper to LowerCtx), `e1826064` (3 IORefs) |
| Stdlib + surface API | 68 | 15.7% | `9ac77542` (Task.parallel cancel), CPS rewrites |
| Soundness fixes | 46 | 10.6% | `4571da08` (typed-emit), `7d09b428` (snapshotCallerCtx threading) |
| Docs + cleanup | 22 | 5.1% | `bc17c381` (CLAUDE.md state) |
| CLI polish | 5 | 1.2% | `662` (sky build repo-root guard) |

**Contrast with v0.16.x patch cadence**:
- v0.16.22 → v0.16.29 = **20 commits across 8 tags**. Average 2.5 commits per patch release.
- v0.17 unreleased = **432 commits in one branch**. The intent is a single tag.

This is structurally a `v0.18` or `v1.0-rc1` — not a v0.X.0 patch.

---

## The three linked problems (what's actually broken)

### Problem A: bundled-console drift check fails

**Symptom**: `scripts/regenerate-console.sh` rebuilds `runtime-go/rt/console_app/main.go` and
the resulting Go has `Store rt.SkyStore` instead of `Store State_Store_R`. `go build` rejects
all field-of-record-of-closures access:

```
./main.go:4611: model.Store.ReadTraces undefined (type rt.SkyStore has no field or method ReadTraces)
```

**Root cause** (`src/Sky/Build/Compile.hs:22717`):
```haskell
solvedTypeToGoViaPipelineFlat ty =
    solvedTypeToGoViaPipelineFlatCtx emptyCgEnv ty
```

The pipeline renderer needs `cgEnv._cg_recordAliases` populated to resolve `type alias Store
= { ... }` (user record alias) before runtimeTypedMap's bare-name lookup picks up `Store →
rt.SkyStore` (kernel mapping). With `emptyCgEnv`, the alias resolution can't run and the
kernel mapping wins.

**Why this exists**: PR-22 S7 collapsed `solvedTypeToGo` to the pipeline path
(`solvedTypeToGoViaPipelineFlat`) but didn't migrate the env-aware variant. The legacy
`getCgEnv` (deleted in iter 44 S5) used to read the live env from `globalCgEnv` IORef; the
pipeline replacement loses that channel.

### Problem B: typed-FFI dispatch routes to wrong arm

**Symptom**: examples/13-skyshop emits
```go
rt.Go_Firestore_queryDocuments(q, Lib_Db_ctx())
```
but the FFI generator only emits the `T`-suffix typed wrapper:
```go
func Go_Firestore_queryDocumentsT(arg0 *pkg.Query, arg1 context.Context) (out SkyResult[any, *pkg.DocumentIterator])
```
There is no `Go_Firestore_queryDocuments` (bare) — `go build` rejects.

**Where dispatch arms LIVE** (`exprToGo`, lines 14068-14196):
- 14068: `kernelTypedCall` — stdlib only (List.map, etc.)
- 14100: partial-application gate
- 14116: single Unit arg → emit `<typedName>` ✓ (works for `Uuid.newString ()`)
- 14134: N non-Unit arg → emit `<typedName>` with coerced args
- 14157: literal-arg primitives
- 14175: typedKernelArgCoerce registry
- 14196: kernelType + generic params

**The hidden bug**: I instrumented arm 14134 with `Debug.Trace.trace` — it **never fires**
during the 13-skyshop build. `kernelToGo` (line 14866, the bare-name emitter) **also never
fires**. The bare `rt.Go_Firestore_queryDocuments` is emitted from a **third code path** I
have not located.

Candidates I haven't fully traced:
- `exprToGoTyped` (line 20556) — separate typed-call dispatch with its own VarKernel arms
- `exprToGoExpectGo` (line 13469) — typed-slot expectation path
- `coerceCallArgsAt` family — coercion-aware emission
- Dep-module emission (`generateDeclsForDep`) — runs separately from entry-module emission

The arm-coverage matrix between these 4+ entry points has gaps. v0.17's PR-22 S6 bulk-
migration was supposed to unify them; the migration is **incomplete**.

### Problem C: spec-vs-fix incompatibility

**Symptom**: my attempted Store fix (`1d67143b`, now reverted) changed
`solvedTypeToGoViaPipelineFlat` from `emptyCgEnv` to `scopeStateRef._lc_cgEnv` (matching the
legacy `getCgEnv` semantics). This closed Problem A but broke 10 cabal-tests:

```
9) Sky.Build.DepCurrentModuleHint, ... two deps with same-position `let f x = x`
   emit distinct σ
   expected: ExitSuccess
        but got: ExitFailure 1
```

The failing specs encode the OLD (env-free, buggy) behavior as their expected output. They're
**bug-compatible** — they pass not because the renderer is correct but because the bug is
consistent.

This is the worst kind of spec gate: it locks the bug in. Any architectural fix that closes
Problem A breaks these specs by definition.

---

## Why these three are linked

All three failures stem from one architectural debt: **v0.17 set out to unify 6 parallel
renderers into one (`solvedTypeToGoViaPipeline`), but the unification is incomplete**:

```
                         ┌─────────────────────────────┐
                         │ solvedTypeToGo (legacy CAF) │  ← deleted in PR-22 S7
                         └─────────────────────────────┘
                                       │
                  ┌────────────────────┴────────────────────┐
                  ▼                                         ▼
   solvedTypeToGoViaPipelineFlat                 solvedTypeToGoViaPipelineFlatCtx
   (emptyCgEnv, used by                          (takes env, used by some sites)
   substituteTVarsToGo + 8 others)
                  │
                  └── 10 cabal-test specs encode this behavior
                  └── bundled console field-types broken
```

```
                  ┌──────────────────────────────────────┐
                  │   FFI call dispatch                  │
                  └──────────────────────────────────────┘
                                  │
       ┌──────────────────────────┼──────────────────────┐
       ▼                          ▼                      ▼
   exprToGo                   exprToGoTyped         exprToGoExpectGo
   (Compile.hs:13967)         (Compile.hs:20556)    (Compile.hs:13469)
   6 typed-FFI arms           VarKernel → kernelToGo + ???
   (work for stdlib)          (no typed-FFI arms)
                                  │
                                  └── 3 examples emit bare Go_X_y → fail
```

The renderer collapse and the dispatch fragmentation are the same architectural problem:
**v0.17 picked a unification target without auditing which entry points reach it**.

---

## What v0.16.x did right (and v0.17 didn't)

| Practice | v0.16.x | v0.17 |
|---|---|---|
| Patch cadence | 1-5 commits per tag | 432 commits, no tag |
| Refactor in patches | No (deferred to majors) | Yes (major refactor mid-stream) |
| Spec gates | Lock new behavior | Lock existing behavior (incl. bugs) |
| Example sweep gate | Pass before tag | Pass before each commit at risk |
| Documentation | After-the-fact | In-the-middle of refactor (CLAUDE.md churns) |
| Honest scope | "Bug fix" or "stdlib addition" | "100% fully typed e2e" (aspirational) |

v0.16's restraint produced a stable patch series. v0.17's ambition produced an entangled
refactor where each fix surfaces another break.

The user's intuition is correct: **rolling v0.17 back to v0.16-discipline would be more
shippable than the current state**.

---

## The stabilization plan

Three sessions of focused work. Each session ends green-everywhere before the next starts.

### Session 1 — Locate the third FFI emission path (Problem B)

**Goal**: identify the actual code path that emits bare `Go_Firestore_queryDocuments`. Stop
guessing. Add a `Debug.Trace.trace` at every site that builds a `"Go_" ++ modName ++ "_" ++
funcName` string. Run examples/13-skyshop. The trace that fires is the bug site.

Hypothesis ranking:
1. **`exprToGoTyped`'s VarKernel arm** (line 20570) — `kernelToGo modName funcName`. Highest
   probability based on the agent's analysis.
2. **`exprToGoExpectGo`'s Call dispatch** — typed-slot expectation may have its own short-
   circuit.
3. **Dep-module emission** (`generateDeclsForDep` → `generateAliasForDep` family) —
   substituteTVarsToGo may emit FFI-bare in dep contexts.

Once found, add the same typed-FFI dispatch arms that `exprToGo` has (lines 14116, 14134).

**Gate**:
- examples/05-mux-server, 11-fyne-stopwatch, 13-skyshop clean-build
- emitted main.go has zero `rt.Go_<Pkg>_<method>(` (bare) calls; all are `rt.Go_<Pkg>_<method>T(`
- Cabal-test suite stays green (no spec regression)
- Regression spec added: `Sky.Build.TypedFfiDispatchSpec` with multi-module fixture for each
  call shape (0-arg Unit, 1-arg, 2-arg, 3+arg)

**Risk**: low. The fix is additive (new arm at the missing emission site). Doesn't change
existing behavior.

### Session 2 — Migrate `solvedTypeToGoViaPipelineFlat` callers + retarget specs (Problems A + C)

**Goal**: thread cgEnv through the 8-9 callers of `solvedTypeToGoViaPipelineFlat`. Update the
10 failing specs to assert the env-aware (correct) output. Delete `emptyCgEnv` fallback once
no callers remain.

Specific call sites to migrate (per agent's locate):
- `substituteTVarsToGo` fall-through at line 22588 (drives field-type rendering in
  `generateAliasForDep`)
- Anywhere `solvedTypeToGo` is consumed inside an env-context

For each spec to retarget:
1. Determine what output the spec asserts (bug-compatible behavior)
2. Build the correct env-aware output by hand
3. Update the assertion + add a comment explaining the migration rationale

**Gate**:
- `scripts/regenerate-console.sh` produces a clean go-build (Store field correctly resolves
  to `State_Store_R`)
- All 10 specs pass with new assertions
- Cabal-test full sweep + example sweep both green
- Spec migration documented in `docs/v0.17/spec-retarget-log.md` (which spec changed, from
  what to what, why the new is correct)

**Risk**: medium. Spec-retargeting is delicate — if the new assertion is wrong, the spec
becomes a no-op gate. Mitigation: each spec migration is its own commit + reviewed before
merge.

### Session 3 — Tag v0.17.0-rc1 + plan v0.17.x patch cadence

**Goal**: release rc1 with the two fixes above. Reset to v0.16-style patch cadence for the
remaining v0.17.x cleanup.

What stays deferred to v0.17.x patches (NOT in v0.17.0):
- `globalCgEnv` IORef deletion (criterion #3) — per Option A user decision, deferred to
  Phase A Stage 3+4
- Sealed-iface ADT emission (#677) — large architectural change, multi-session
- 5 of 6 parallel renderer collapse — risky without full arm-coverage audit
- Examples that need rewriting to use the unified codegen surface (none currently)

What v0.17.0 ships:
- Everything currently on the branch (432 commits)
- Session 1 + Session 2 fixes
- Honest CHANGELOG explaining: "v0.17.0 is the first release on the unified codegen
  pipeline. Three examples + bundled console regenerate were broken pre-fix; this release
  closes them. Full architectural close (rt.Coerce → 0, globalCgEnv deletion, sealed-iface
  ADT emission) is the v0.17.x → v1.0 roadmap."

**Gate for tag**:
- All three cycle gates green: cabal-test (clean), example sweep (39/39 + 4 fyne/firestore
  pre-installed), bundled-console drift check
- Two-week soak on `feat/v0.17-pure-sound-codegen` before tag (no new commits, just user
  testing)
- `sky-lang.org` + `skydeploy` rebuild against the rc1 binary; user verifies their dogfood

**Risk**: tagging without architectural completion. Mitigation: the CHANGELOG narrates the
honest scope. v0.17.0 is "v0.17 begins" not "v0.17 done".

---

## What I am NOT recommending

These are tempting moves that would make things worse:

- **Don't keep stacking partial fixes onto v0.17.** Each fix risks a fresh regression
  because the codegen surface isn't fully understood. The pattern this session showed —
  fix → 10 cabal-tests break → revert — is the failure mode to avoid.
- **Don't try to land all of #677 (sealed-iface ADT) before v0.17.0.** It's multi-session
  work per CLAUDE.md §0.2 N-strikes (3 prior swap attempts produced regressions). Defer.
- **Don't rebase v0.17 onto main and lose the 432 commits.** The architectural work is
  real; it just needs to be sequenced + bounded.
- **Don't pivot to "rewrite v0.17 from scratch".** The current branch has the right ideas;
  the execution gaps are localised to 3 problems above.

---

## Open questions for the user

1. **Is the v0.17.0 tag scoped to "the refactor + the three fixes" — i.e. honest "v0.17
   begins"?** Or do you want the full architectural close before tagging? The former is 3
   sessions; the latter is 8-12 sessions.

2. **For the 10 cabal-test specs that encode env-free behavior** — do you want me to update
   them in-place (treating them as wrong), or freeze them and add new env-aware specs
   alongside?

3. **For the typed-FFI dispatch gap** — should I delegate the third-path location to a
   dedicated debugging agent (more thorough but slower), or instrument every Go-name builder
   site (faster but messier)?

4. **`19-skyforum` and `26-ui-showcase` pass currently.** Are there OTHER user-visible
   regressions you've seen in v0.17 that I should also be solving in Sessions 1-2? Better to
   surface them now than find them after rc1.

---

## Appendix: file:line citations

| Problem | File:line | Function | What's there |
|---|---|---|---|
| A | `Compile.hs:22717` | `solvedTypeToGoViaPipelineFlat` | Calls `Ctx emptyCgEnv ty` |
| A | `Compile.hs:22588` | `substituteTVarsToGoBounded` | Falls through to env-free `solvedTypeToGo` |
| B | `Compile.hs:14068-14196` | `exprToGo` Can.Call arms | 5 typed-FFI dispatch arms |
| B | `Compile.hs:14866` | `kernelToGo` | Bare-name fallback emitter |
| B | `Compile.hs:20570` | `exprToGoTyped` VarKernel | Direct `kernelToGo` call (no typed-FFI check) |
| B | `Compile.hs:13469` | `exprToGoExpectGo` | Typed-slot expectation entry |
| C | `test/Sky/Build/CrossModuleLambdaCollisionC_Spec.hs` | spec | Encodes env-free shape |
| C | `test/Sky/Build/DepCurrentModuleHintSpec.hs` | spec | Encodes env-free shape |
| arch | `Compile.hs:90-150` | module docstring | LowerCtx + scopeStateRef contract |
| arch | `Compile.hs:480-600` | exprToGoExpectGo preamble | Typed-slot threading rationale |
| arch | `.claude/AUTONOMOUS_GOAL.md` | line 347 | Option A defer decision for globalCgEnv |
| arch | `CLAUDE.md` §0.2 | N-strikes circuit-breaker | 3-failure reclassification rule |

## Appendix: relevant commit SHAs

| SHA | Subject | Why it matters |
|---|---|---|
| `46b7eaf7` | origin/main (= v0.16.29 baseline) | Branch base |
| `8b2bf86e` | Pipeline helper introduced | PR-22 S0 |
| `a2d519c2` | mapSkyTypeToGo + MappingContext | Pipeline core |
| `441e8a0a` | PR-22 planning + adversary review | Refactor plan |
| `cc946d09` | ScopeStateRefAuditSpec | Locked contract |
| `4571da08` | typed-emit gate (resolveWrapParams) | Iter 30+ |
| `1d67143b` | (reverted) Store fix attempt | What broke 10 specs |
| `fc9c53b7` | Revert of 1d67143b | Current branch HEAD |

---

*Written 2026-06-28 after the session where my Store fix (`1d67143b`) closed Problem A but
introduced 10 cabal-test regressions, and I reverted it (`fc9c53b7`) to keep the branch
clean. The user's reaction — "v0.16.x seems more stable, that doesn't make sense" — is
literally correct. This document is my honest debt + plan.*
