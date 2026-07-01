# v0.17 Session 4 Commit 3 — Adversarial grill findings (BLOCKING)

> Per AUTONOMOUS_GOAL.md workflow: Architect → ≥2 grillers in parallel
> BEFORE code touched.  Both grillers returned **BLOCKING**.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `cd8f4ebf`.
> Commit 1+2 scaffolding remains shipped; no caller migration occurred.

## Empirical context (the failure that triggered this grill)

A prior naive Commit 3 attempt threaded `lookupCgEnvFromCtx phaseACtx`
into all three direct callers of `substituteTVarsToGo` (sites 7255,
8941, 13763) and regressed the example sweep from 26/26 → 16/26.
Visible failure mode:

```
./main.go:3605:38: cannot use rt.MaybeCoerce[any](rt.Nothing[any]())
    (value of struct type rt.SkyMaybe[any]) as rt.SkyMaybe[Std_Ui_Color]
    value in struct literal
```

Reverted (working tree clean post-revert; no commit shipped from
that attempt).

## Architecture-Consult agent verdict: REVISE

Proposed plan:

1. Sites 7255 + 8941 (DECL emit): minimal cgEnv carrying only
   `_cg_recordAliases` + `_cg_aliases`.
2. Site 13763 (USE record literal): full `lookupCgEnvFromCtx
   phaseACtx`.

Diagnosis: the naive plan conflated two cgEnv responsibilities (alias
registry vs caller-side TVar instantiations).

## Griller #1 (false-negative lens): BLOCKING

Convergent findings:

- **Site 13763 full-phaseACtx still leaks** the Maybe[any] regression.
  `phaseACtx` is shared across entry and dep emission paths
  (Compile.hs:6623, 7723, 8008). When a TVar in `pairs` isn't in
  `tvarSubst`, the TVar arm at line 22680 falls through with
  non-empty cgEnv → may substitute via `_cg_funcSkyToGoTVars` →
  same regression class.
- **Minimal cgEnv assumption unverified**: pipeline renderer reads
  multiple cgEnv fields, not just the 2 the arch-consult cited.
  Each is a leak vector.
- **Alias-name collision risk** (MED): when entry+dep both declare
  the same alias name, `Map.union` is left-biased → entry shape
  wins → dep emits entry's struct shape.
- **Risk 2 unconfirmed**: the 10-example regression was NEVER
  empirically bisected.  The arch-consult plan ships 3 migrations
  relying on an unverified hypothesis.
- **Dominant Problem A emit site unverified**: `Store rt.SkyStore`
  at console_app/main.go:4611 could come from 7255, 8941, an
  inferred-sig path, or sibling fallback.  No empirical attribution.

Required mitigation:

1. Pre-commit: SHA-256 every example's `sky-out/main.go` and gate
   post-migration deltas exactly to bundled-console + declared.
2. New spec `Sky.Build.MinimalCgEnvFieldGateSpec` proving the FLAT
   pipeline only reads 2 cgEnv fields under a tracing CodegenEnv.
3. Empirical bisection log committed alongside any migration.

## Griller #2 (type-soundness lens): BLOCKING

Convergent findings:

- **Sites 7255 + 8941 are NOT in the leak class.** Their
  `substituteTVarsToGo` operates on the alias's own `tvarMap` over
  the alias body.  Every nominal reference inside the body is
  either (a) in tvarMap (alias's own TVars) or (b) a fully-qualified
  TType from canonicalisation carrying `home`.  Adding cgEnv at
  DECL sites is at best no-op, at worst opens an
  unbound-TVar-fallthrough hijack (an alias body TVar miss now
  resolves through `aliasRecovery` instead of falling to GoAny →
  `T1` could emit as a struct ref instead of a Go type-parameter
  name).
- **Site 13763 with FULL phaseACtx has cross-alias structural
  hijack risk** (MED): `mapRecordType`'s anonymous-record arm
  consults `mcRecordAliases`; if two structurally-equal aliases
  exist in the entry registry, a nested anon record could
  nominalise to the wrong alias.
- **CRITERION #3 IORef contract VIOLATION** (HIGH): `lookupAliasDecl`
  reader sites (Compile.hs:22737, 22752, 22755) read from the IORef
  in the TType/TAlias parametric-alias arms ABOVE the fallthrough.
  Migrating only the fallthrough at 22758 to cgEnv SPLITS the
  alias-resolution channel — parametric paths read IORef, non-
  parametric paths read cgEnv.  This violates criterion #3's locked
  wording: "any residual IORef in Compile.hs carries a
  machine-verified single-writer / single-reader monotonic
  contract."  Two readers, same map, potentially-different
  mutation windows → contract broken.

Corrected minimal plan:

- Drop sites 7255 + 8941 from Commit 3.
- For site 13763, build a SCOPED cgEnv carrying only aliases
  REACHABLE from the targetTy's alias-decl body (single
  transitive walk over `_cg_aliases`).
- Add `Sky.Build.AliasDeclTVarFallthroughSpec` asserting DECL TVars
  always emit as Go type-param names regardless of cgEnv shape.
- Either migrate ALL THREE `lookupAliasDecl` sites to consume cgEnv
  in a SINGLE shared reader OR migrate NONE — split readers violate
  criterion #3.

## Synthesis: Commit 3 corrected scope

Both grillers converge on:

1. **No migration at sites 7255 and 8941.**  DECL emit does not
   need cgEnv visibility; adding it opens hijack vectors with no
   benefit.
2. **Site 13763 needs a SCOPED cgEnv**, not full phaseACtx.
   Compute via single transitive walk of `_cg_aliases` keyed by
   the target alias body's referenced names.
3. **Criterion #3 IORef contract** is at risk regardless of cgEnv
   threading — either migrate ALL three `lookupAliasDecl` readers
   together or none.  Split readers are forbidden.
4. **Empirical bisection MUST precede any migration.**  Add a
   `SKY_PROBLEMA_TRACE` printf at each emit site, observe which
   one stamps `Store rt.SkyStore` in the bundled-console rebuild,
   then migrate the dominant site in isolation.

## Status

- Commit 1+2 scaffolding shipped at `cd8f4ebf` — unchanged.
- **Commit 3 BLOCKED pending empirical bisection.**
- Working tree clean.

## Next iteration sequence

1. Add `SKY_PROBLEMA_TRACE=1` gated `Debug.Trace.traceIO` at the
   three candidate emit sites in Compile.hs.
2. Rebuild compiler; run `scripts/regenerate-console.sh` with the
   env var set.
3. Identify the site that stamps `Store rt.SkyStore`.
4. If the dominant site is 13763 (USE position): construct a
   scoped cgEnv (transitive reach over `_cg_aliases` from
   targetTy's alias body), thread at 13763 ONLY, verify sweep
   stays 26/26 + console regenerate closes.
5. If the dominant site is 7255 or 8941 (DECL position): the
   arch-consult REVISE plan was misdiagnosed; re-spawn
   Architecture-Consult with the empirical evidence.
6. If the criterion #3 contract requires `lookupAliasDecl`
   migration as a precondition: file as a Commit 3 prerequisite
   under criterion #3 work.

## Risks if we skip the bisection

- Repeat the 10-example regression cycle.
- Ship a fix that closes the console regenerate but introduces a
  subtler downstream leak (cross-alias structural hijack).
- Violate criterion #3 IORef contract silently.
- N-strikes counter advances: this would be attempt #2 on the
  same architectural lever; the 3rd attempt triggers mandatory
  re-classification per CLAUDE.md §0.3 rule 3.

## Discipline lesson

The user pushed back when I noticed I had been working solo
without spawning agents — exactly when the discipline matters.
This grill caught two real risks (cross-alias hijack + criterion
#3 violation) that inline-reasoning per-commit-grill would NOT
have caught.  Agents + grilling is a load-bearing protective
process for compiler-level changes; bypassing it is forbidden by
CLAUDE.md §0.4 and the AUTONOMOUS_GOAL workflow.
