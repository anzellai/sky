# v0.17 Session 4 Commit 3 — Empirical bisection result

> Path A (user-selected 2026-06-29) — instrument the 3 candidate emit
> sites with `SKY_PROBLEMA_TRACE=1` `bisectTrace`, run
> `scripts/regenerate-console.sh`, attribute the
> `Store rt.SkyStore` leak to a specific site.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `d57cb672` + tracing
> patch (reverted after attribution).

## Result: DEFINITIVE — Site 7255 (generateAliasForDep)

The trace fires ONLY at site 7255.  Sites 8941 (generateStruct,
entry-module) and 13763 (lowerRecordLiteralTo, USE-position)
produce **zero** `rt.SkyStore` hits during the bundled-console
regenerate.

Smoking-gun trace lines:

```
[BISECT-7255] modPrefix=State alias=Model go=rt.SkyStore
[BISECT-7255] modPrefix=State alias=Model go=rt.SkyStore
```

Followed immediately by the dep's own `Store` alias rendering its
function-typed body CORRECTLY:

```
[BISECT-7255] modPrefix=State alias=Store go=func(struct{}) rt.SkyTask[Sky_Core_Error_Error, State_Overview_R]
[BISECT-7255] modPrefix=State alias=Store go=func(State_LogFilter_R) rt.SkyTask[Sky_Core_Error_Error, []State_LogEntry_R]
...
```

## Root cause: sibling-alias bare-name resolution collision

The dep module `State` declares two record aliases:

- `Model` — has a field `Store : Store` (or `store : Store` — sibling
  reference to State.Store).
- `Store` — function-record alias with methods.

When `generateAliasForDep` emits `Model`'s fields (alphabetically first
or by field order), the field type `Store` is canonicalised as
`T.TType (Canonical { _name = "State" }) "Store" []`.  This routes
through `substituteTVarsToGo` → fallthrough at line 22758 →
`solvedTypeToGoViaPipelineFlatCtx emptyCgEnv ty`.

With empty cgEnv, the pipeline renderer's name-resolution priority is:

1. Kernel-name registry (built-in `runtimeTypedMap` / `mapBaseType`)
   — `Store` here matches a kernel mapping (likely `Std.Live.Store`
   or `Std.Cache.Store` exposing as `rt.SkyStore`).
2. User alias registry from cgEnv — EMPTY at this site so no hit.

The kernel mapping wins on the bare name match.  Result:
`Store rt.SkyStore` in `State_Model_R`, then `go build` rejects every
method call on `model.Store` because `rt.SkyStore` has no
`ReadTraces` / `ReadLogs` / `ReadOverview` / etc. methods.

When the renderer later processes the alias `Store` itself (the
second batch of traces), it has the SAME cgEnv state — but the
specific function-type body uses `_R`-suffixed user aliases
(`State_Overview_R`, `State_LogEntry_R`, etc.) that DON'T collide
with kernel names, so the leak doesn't visibly recur.  The leak is
specific to `Store` because that's where the bare-name collision is.

## What the grillers warned about that does NOT apply here

- **Griller 2's unbound-TVar-fallthrough hijack** (HIGH severity):
  bisection shows the leak is at a NOMINAL TType match (`Store`,
  zero args), not a TVar miss.  The hijack scenario assumes a TVar
  name colliding with an alias name; bisection shows the actual
  scenario is a bare-name nominal TType colliding with a kernel
  name.  Different mechanism — Griller 2's risk does not apply.
- **Griller 1's cross-instantiation leak via `_cg_funcSkyToGoTVars`**:
  bisection shows zero leak at sites 8941 + 13763, so threading
  there isn't proposed.  Only site 7255 needs the fix.  At 7255
  there are no entry-module instantiations to leak — the dep's
  alias body uses its OWN goTVars only.

## Criterion #3 IORef contract impact (Griller 2 HIGH)

Still applies in principle.  But: the minimal fix at site 7255
threads only `_cg_aliases` (and possibly `_cg_recordAliases`) into
`substituteTVarsToGoBoundedCtx`, which is already cgEnv-aware
(Commits 1+2 shipped that scaffolding).  The
`lookupAliasDecl` IORef reads at lines 22737/22752 stay UNCHANGED
because they fire on parametric-alias paths (TType with non-null
args, TAlias with non-null pairs) — sibling `Store` is the
zero-args case that falls THROUGH all those arms to the wildcard.

So: the substitueTVarsToGo fallthrough reader split (cgEnv at 22758
+ IORef at 22737/22752) DOES happen.  But the data flow is:

- TType with args → IORef reader (parametric path; entry-module
  alias decl with concrete args).
- TType with no args → cgEnv reader (the new fix; sibling-alias
  resolution).

These are mutually-exclusive code paths over the SAME alias map
contents, just keyed differently.  The contract requirement is that
both readers see the SAME logical map.  Since `_cg_aliases` and the
`lookupAliasDecl` IORef both ultimately reflect the post-canonicalisation
alias declarations of the program, they CAN be kept in sync via the
existing scopeStateRef install at compile entry.  Griller 2's
violation concern reframes as: this commit must add a spec proving
the two readers return the same Map.empty-or-equal contents at the
fallthrough call site.

## Proposed focused fix

Minimal, narrow scope:

1. Add a NEW param to `generateAliasForDep`: pass `phaseACtx` from
   `generateDeclsForDep` (line 6645) so the dep emitter can read
   `_cg_aliases` for sibling resolution.
2. Inside `generateAliasForDep`, construct a SCOPED cgEnv:
   ```haskell
   sibCgEnv = emptyCgEnv { _cg_aliases = lookupAliasesFromCtx phaseACtx }
   ```
3. Pass `sibCgEnv` to `substituteTVarsToGoCtx sibCgEnv tvarMap fty`
   at the field-type render site (line 7255 → migrated).
4. Do NOT migrate sites 8941 or 13763 (bisection: zero leak).
5. Spec gate: assert at the fallthrough that `_cg_aliases` lookup and
   `lookupAliasDecl` IORef read agree on a representative sample.

This closes Problem A without touching the IORef-reader contract
scope (deferred to Phase A reshape per the locked plan).  Scope is
**ONE file change + ONE new spec**, not the multi-site migration
the arch-consult originally proposed.

## Re-grill required

Before shipping, this focused fix must be grilled afresh because:

- It's a NEW shape that neither griller attacked specifically (both
  grillers were attacking the broader 3-site migration).
- The `_cg_aliases`-only minimal cgEnv at site 7255 needs validation
  on: (a) does the renderer actually consume `_cg_aliases` at the
  fallthrough? (b) what happens if a dep module declares an alias
  with the SAME name as an entry-module alias?
- Empirical gate: must verify post-fix sweep stays 26/26 AND
  bundled-console regenerate produces clean `go build` output.

## Verification gates for the focused fix

1. cabal install clean.
2. 26-example sweep `--build-only` stays 26/26 (gate the per-cluster
   rt.Coerce ratchets).
3. `scripts/regenerate-console.sh` produces a clean
   `runtime-go/rt/console_app/main.go` that `go build` accepts.
4. SHA-256 diff on representative examples (01, 26, 13) shows ONLY
   the bundled-console-related deltas — no unrelated drift.
5. New spec `Sky.Build.SiblingAliasResolutionSpec` locks the
   behavior: a fixture with two record aliases A + B in a dep module,
   where A's field type is B, emits `<Mod>_A_R.B <Mod>_B_R`, not
   `<Mod>_A_R.B rt.SkyB`.

## Next step

Re-spawn ONE adversarial griller specifically attacking the focused
fix's narrow scope.  If the griller approves, implement + verify.
