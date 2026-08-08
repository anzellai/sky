# v0.17 Session 4 — Architecture-Consult agent report

> Captures the Architecture-Consult agent's analysis of the env-thread
> `solvedTypeToGoViaPipelineFlat` close (roadmap "Session 3" / postmortem
> Problem A).
>
> Branch: `feat/v0.17-pure-sound-codegen` at `1b76d54c`.
> Report generated: Session 4 prep iteration.

## Verdict

**PROCEED** — tactic is sound, callers correctly identified, low regression risk.

The 10 specs that encode "env-free" behavior are NOT renderer bugs — they correctly lock the per-module alias isolation contract. Retargeting them is straightforward (build correct output via `safeReturnTypeFullBounded` or run the env-threaded path on the same input).

## Caller inventory (8-9 sites direct + ~25 indirect)

### Direct callers

1. **Compile.hs:22667** — `solvedTypeToGo = solvedTypeToGoViaPipelineFlat` (legacy redirect; ~50+ downstream sites use this alias)
2. **Compile.hs:7890** — `generateDeclsForDep` ADT field-type rendering. **Already env-threaded via `env` param.**
3. **Compile.hs:8936** — Type alias declaration emission. **Already env-threaded via `phaseACtx`.**

### Indirect callers (via `solvedTypeToGo`)

- **Compile.hs:8694** — Record constructor param types
- **Compile.hs:9013-9017** — Sealed-interface method signatures
- **Compile.hs:14382, 14663, 14724** — Field-type extraction in pattern-match
- **Compile.hs:16806-16807** — Curry lambda type params + returns
- **Compile.hs:18456, 18641, 18783, 18793** — Inference fallback
- **Compile.hs:20692** — `wrapTypedReturn`

### Via `substituteTVarsToGo`

- **Compile.hs:7269** — ADT polymorphic field substitution
- **Compile.hs:8955** — Record-alias parametric field substitution
- **Compile.hs:22635** — `substituteTVarsToGoBounded` fallback

## Per-caller cgEnv assignment

| Caller | Context | Correct cgEnv source |
|---|---|---|
| 7890 | Dep-module emit | `env` param (already wired) |
| 8936 | Entry-module type alias | `lookupCgEnvFromCtx phaseACtx` (already wired) |
| 8694 | Record ctor params | LowerCtx (entry- or dep-aware) |
| 9013-9017 | Sealed-iface methods | `phaseACtx` (entry) |
| 14382, 14663, 14724 | Field extraction | `phaseACtx` via LowerCtx |
| 16806-16807 | Lambda σ recovery | `phaseACtx` (entry) |
| 18456-18793 | Inference fallback | `phaseACtx` (entry) or empty (cross-mod) |
| 20692 | wrapTypedReturn | `phaseACtx` via LowerCtx |
| 7269, 8955 | substituteTVarsToGo | Calling context's cgEnv |

## 10 bug-encoding specs (per roadmap)

1. **CrossModuleLambdaCollisionC_Spec** — 3 modules with `let encodeOne x = …` expected to emit distinct σ
2. **DepCurrentModuleHintSpec** — Two deps with `let f x = x` expected to emit distinct σ
3-10. **8 others** — referenced in roadmap as "+8 others"; names deferred to implementation phase

Each retarget:
- New assertion identifies CORRECT env-aware output
- Build via `safeReturnTypeFullBounded` (legacy truth reference) OR manually via `buildMappingContext`
- Log entry in `docs/v0.17/spec-retarget-log.md`

## Adversarial grill (G1-G5)

**G1 (false negatives)**: Could unmigrated callers leak T1 from `emptyCgEnv`? — **YES**. Every `solvedTypeToGo` site in dep-module context trying to resolve a user record alias emits the kernel name (`rt.SkyStore`) instead of `State_Store_R`. The bundled-console regenerate failure is live proof.

**G2 (false positives)**: Could explicit env-thread expose wrong env? — **RISK at inference sites** (18456+) which are PRE-emit. Mitigation: thread from the call site's phase context, NOT `scopeStateRef`.

**G3 (Session 0 regression delta)**: Session 0 threaded through `scopeStateRef._lc_cgEnv` — but `scopeStateRef` MUTATES during inter-module emission, exposing wrong envs to wrong specs. Explicit threading at call site avoids the mutation window. **Critical**: don't repeat Session 0's mistake.

**G4 (layering)**: Composes cleanly with PR-17b's ctx-replace + Session 3c's params-channel fix. The cgEnv field in LowerCtx is the right vehicle — already threaded through wrap sites. Sites that don't yet read from LowerCtx (inference, some dep-emit) need explicit cgEnv param.

**G5 (Compile.hs:22635 fallback)**: `substituteTVarsToGoBounded` falls through to bare `solvedTypeToGo ty`. The fallthrough MUST ALSO thread cgEnv — otherwise parametric record fields still emit bare kernel names.

## Recommended commit sequence (~4 sessions)

### Commit 1: Per-dep env threading verification (no code changes)

Verify Compile.hs:7890 + 8936 work correctly with current env args. No code changes (already from iter 6 / scopeStateRef install).

Gate: cabal test green, no spec changes.

### Commit 2: substituteTVarsToGo param threading

- Add `cgEnv :: CodegenEnv` param to `substituteTVarsToGo`
- Migrate 2 direct callers (7269, 8955) + fallthrough at 22635
- Retarget poly-field specs (2 of 10)

Gate: cabal test green, 2 specs updated in spec-retarget-log.md.

### Commit 3: Entry-module direct callers

- Thread cgEnv through 25+ `solvedTypeToGo` sites in entry-module context
- Source cgEnv from `phaseACtx`
- Sites: 8694, 9013-9017, 14382, 14663, 14724, 16806-16807, 20692
- Retarget 6 specs

Gate: cabal test + bundled-console-regenerate green (closes Problem A).

### Commit 4: Inference + fallback sites

- Thread cgEnv into inference-site contexts (18456, 18641, 18783, 18793)
- Use entry-module cgEnv or empty depending on phase
- Retarget final 2 specs

Gate: full sweep + example sweep + verify-cli green.

## Estimated cost

~4 sessions. Payoff: criterion 12 (bundled-console regenerate) + 10 specs correct + architectural closure on the pipeline's env-dependence.

## Next iteration

Start with **Commit 1**: verify 7890 + 8936 are clean (no-op pass). If that's a no-op pass, proceed to Commit 2 (substituteTVarsToGo param threading).

Per CLAUDE.md §0.4 — per-commit adversarial grill before each commit. Per CLAUDE.md §0.1 — push at milestone (after Commit 3 closes bundled console regenerate).
