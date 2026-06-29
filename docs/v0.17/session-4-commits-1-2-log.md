# v0.17 Session 4 — Commits 1+2 log

> Architectural close path per Architecture-Consult agent verdict
> (`session-4-architecture-consult.md`).  Goal: thread `cgEnv`
> through every `solvedTypeToGoViaPipelineFlat` call site so dep-
> module record-alias lookups resolve via the right env (Problem A
> in `stabilization-postmortem.md`; bundled-console regenerate CI
> gate).
>
> Branch: `feat/v0.17-pure-sound-codegen`.

## Commit 1 — verification of pre-Session-4 env-thread sites

PROCEED.  No code changes — the two declared env-threaded sites
already use the @Ctx@ variant of the pipeline renderer:

| Site (line drift since plan) | Caller | Env source |
|---|---|---|
| `Compile.hs:7876` | `generateDeclsForDep` ADT field rendering | per-dep `env` param |
| `Compile.hs:8922` | Entry-module type-alias declaration emission | `lookupCgEnvFromCtx phaseACtx` |

The plan's "8-9 sites direct + ~25 indirect" inventory refers to the
`solvedTypeToGo` legacy alias callers (~30 line hits in
`grep solvedTypeToGo\b` minus comments/definitions).  These are the
migration surface for Commits 3+4.

Empirical gate:

* `examples/01-hello-world` clean-builds.
* `examples/26-ui-showcase` clean-builds; `grep -c rt.Coerce
  sky-out/main.go` = **177** (already well below the round-5
  baseline 287; reflects this session's prior fixes at `c57001df`
  cross-fn TVar collision + `a7be0857` head-alias both-positions
  + `cdb75770` baseline ratchets).
* Bundled-console regenerate still fails (CI on `befbc4c6` proves
  the `Store.ReadTraces undefined` Problem-A symptom is the active
  gap).

## Commit 2 — `substituteTVarsToGo` @Ctx@ scaffolding

ADDITIVE refactor of the TVar-substituting renderer.  No caller has
been migrated yet (those land in Commits 3+4 per the architecture
plan).

### Change shape

`substituteTVarsToGo` + `substituteTVarsToGoBounded` were
re-implemented as thin wrappers over new @Ctx@ variants threading a
`Rec.CodegenEnv`.  The legacy entry points delegate with
`emptyCgEnv`, which is byte-identical to the pre-refactor fallthrough
(since `solvedTypeToGo = solvedTypeToGoViaPipelineFlat = ...
emptyCgEnv`).

Both fallthrough sites inside the recursive body (TVar miss at the
former line 22642 + final wildcard at the former line 22718) now call
`solvedTypeToGoViaPipelineFlatCtx cgEnv ty` so the cgEnv arrives at
the nominal-name lookup point.

### Per-commit grill (G1-G5)

* **G1 (false negatives)**: Are there callers of
  `substituteTVarsToGoBounded` I might miss?  No — it's a
  package-private helper called only by `substituteTVarsToGo` and
  itself.  `grep substituteTVarsToGoBounded` returns 6 hits, all
  internal.
* **G2 (bounded-variant break risk)**: Could threading the env arg
  break the fuel/seen recursion contract?  No — cgEnv is orthogonal
  to fuel/seen.  Both recursive call sites (`rec` and `recCycle`)
  thread the same cgEnv through; recursion remains depth-bounded at
  64.
* **G3 (Session 0 regression delta)**: cgEnv comes from the CALL
  site, not `scopeStateRef`, so the inter-module mutation window
  that broke Session 0's earlier attempt cannot leak here.  This
  commit ships no caller migration yet, so even the cgEnv-from-
  mutating-source risk is absent.
* **G4 (layering)**: cgEnv composes cleanly with `tvarMap` (TVar
  name map), `fuel` (Int), `seen` (Set String) — no overlap on
  any axis.
* **G5 (Problem A progress)**: This commit scaffolds the param
  channel but flips zero callers; bundled-console regenerate still
  fails (as expected).  Commit 3 + Commit 4 land caller migrations.

### Byte-identical determinism gate

`examples/26-ui-showcase/sky-out/main.go` SHA-256 pre-edit vs
post-edit:

| Stage | sha256 prefix |
|---|---|
| Pre-edit (`befbc4c6`) | `d92896acd7620b6b…` |
| Post-edit (Commit 2 in working tree) | `d92896acd7620b6b…` |

Identical.  `rt.Coerce` count unchanged at 177 (per-cluster ratchet
spec stays green).

### Migration surface for Commit 3+4 (deferred to next iter)

Per the Architecture-Consult plan's caller table:

* Entry-module sites (cgEnv = `lookupCgEnvFromCtx phaseACtx`):
  `Compile.hs` ~lines 8694, 9000, 9003, 14368, 14649, 14710, 15717,
  16165, 16203, 16819-16820, 20696.
* Inference-fallback sites (cgEnv = `phaseACtx` OR empty depending
  on phase): ~lines 18461, 18524, 18709, 18851, 18861, 18883,
  19042, 19138.
* `substituteTVarsToGo` direct callers (cgEnv = scope-appropriate):
  `Compile.hs:7255` (poly-ADT field), `Compile.hs:8941`
  (generateStruct), `Compile.hs:13763` (tvarSubst record-field map).

Each migration must:

1. Flip the call site from `solvedTypeToGo` / `substituteTVarsToGo`
   to the @Ctx@ variant with the correct cgEnv source.
2. Verify the byte-identical gate on `26-ui-showcase` (or accept a
   measured delta with a ratchet bump in
   `Sky.Build.RtCoerceBudgetSpec`).
3. Retarget any spec that locks bug-compatible (env-free) output.

## Status

* Commit 1 — VERIFIED (no code change).
* Commit 2 — SCAFFOLDING SHIPPED (this commit).
* Commit 3+4 — pending (caller migrations + spec retargets).
