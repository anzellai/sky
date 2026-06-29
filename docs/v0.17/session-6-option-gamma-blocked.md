# v0.17 Session 6 — Option γ BLOCKED by adversarial grills

> User selected Option γ (kernel-name registry split discriminated by
> home module).  Per discipline: Architecture-Consult + 2 grillers
> run BEFORE code change.  Both grillers found serious factual errors
> + unproven claims in the proposal.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `ee53d708`.  Working
> tree clean.

## Architecture-Consult PROCEED — proposal details

Add `skyStdlibHomePrefixes :: [String] = ["Sky.Core","Sky.Http","Std","Sky.Live"]`
to `RuntimeMaps.hs`.  Thread `mcStdlibHomePrefixes` into MappingContext.
New helper `resolveKernelName :: MappingContext -> ModuleName -> String
-> Maybe String` with priority:

```
isKernelOnly name        → kernel form wins
isStdlibHome ctx home    → kernel form wins
hasUserShadow ctx name   → defer to record-alias (user_R wins)
otherwise                → kernel form wins
```

Replace bare `lookup name runtimeTypedMap` in `mapNamedType` (Type.hs:1426)
+ migrate ~10 other Compile.hs callers.

## Griller C verdict: REVISE

Convergent findings:

1. **`qualifiedRuntimeTypedMap` ALREADY PARTIALLY does this.**  Today it
   carries 4 entries: `(Sky.Core.Http, Response) → rt.HttpResponse`,
   `(Http, Response) → rt.HttpResponse`, `(Sky.Http.Server, Response) →
   rt.SkyResponse`, `(Server, Response) → rt.SkyResponse`.  The OTHER 17
   names in `runtimeTypedMap` LACK qualified entries.  The registry is
   empirically incomplete — γ is reinventing what a fully-populated
   qualified registry would do.

2. **`mapNamedType` arm ordering**: `mcQualRuntimeTyped` consults BEFORE
   the bare `runtimeHit` arm; `isRecordAlias` consults BEFORE bare
   `runtimeHit` too.  For stdlib types that ARE TRecord aliases (e.g.
   stdlib `Sky.Http.Server.Request`), `isRecordAlias` evaluates TRUE
   because `Sky_Http_Server_Request_R` lands in `_cg_recordAliases` —
   the bare `runtimeHit` is BYPASSED today.  This contradicts the
   premise of α and γ both: stdlib Request may already emit as
   `Sky_Http_Server_Request_R` today, not `rt.SkyRequest`.  Empirical
   verification required.

3. **Hardcoded prefix list is fragile**.  User can legally declare
   `module Sky.Core.MyApp` → `isStdlibHome` matches but it's user code.
   Architect cited Sky.Sky.ModuleName having no reserved-prefix check.

4. **Empty-home regression**: γ's `| otherwise → kernel form wins`
   branch is BACKWARDS for empty-home Sky ADTs that should route
   through `unionRecovery` / `aliasRecovery`.

5. **Architect's site list inaccurate**: actual sites per grep are 8
   (paired qualHit + runtimeHit fallback at 11335/37, 11419/21,
   11748/50, 11772/74, 12821/23, 12913/15, 13179/80), not 10.

## Griller D verdict: BLOCK

Convergent findings (independent + more critical):

1. **Architect's line numbers are WRONG**: 9952 / 10303-10308 / 10773 are
   NOT `runtimeTypedMap` lookup sites.  They're `isTupleGoTypeStr` /
   `stripParametric` / `stripParametric` definition.  Architect
   apparently mis-cited.

2. **The "γ closes 5/6 sites" claim is UNPROVEN.**  Backtrace of the 5
   token-level erasure sites:
   - 9626: `goTy` is a String parameter; backtrace requires per-caller
     audit (NOT done by Architect).
   - 15098: `classifyCoerceTarget` over String `targetTy` supplied by
     caller; does NOT route through `mapNamedType`.
   - 15844: `stripSkyMaybe` over caller-supplied `goType` String; does
     NOT route through `mapNamedType`.
   - 19088/19090: PARTIAL — some paths via `mapSkyTypeToGo`, some via
     `inferredSubjectGoType` (legacy renderer with own logic).

3. **`eraseTypeParams` mis-characterised**.  It only rewrites
   `T<digit>` tokens (T1/T2/...), NOT name tokens like `rt.SkySession`
   / `App_Session_R`.  Erasure PRESERVES discrimination.  The actual
   bug: upstream renderer chooses the wrong name, then erasure
   faithfully preserves the wrong choice.

4. **Missed site: 11774** (sibling to 11750, identical shape).
   Architect's migration list is incomplete.

5. **Type.hs:1184 + 1426 second consumer**: Architect addressed only
   one; second mcRuntimeTypedMap consumer at 1426 missed.

6. **Runtime panic class shift IS REAL for user-declared Session/Store
   aliases**.  Today `rt.MaybeCoerce[rt.SkySession]` = identity (T=any).
   Post-γ for user-home: `rt.MaybeCoerce[App_Session_R]` = reflect-coerce
   field-by-field.  CLASS A regression risk for any user with
   (a) `type alias Session = {...}` AND (b) gob-loaded sessions
   across version skew.

7. **Non-TRecord aliases (tuple/scalar Session) STILL kernel-aliased
   post-γ**.  γ predicate gates on `_cg_recordAliases` only.  User
   `type alias Session = (String, Int)` doesn't close.

## Convergent verdict: BLOCK γ as defined

Both grillers concur:
- Architect's site list contains factual errors (Griller D)
- "γ closes 5/6 sites" is unverified (Griller D)
- Hardcoded prefix list is fragile (Griller C)
- Existing `qualifiedRuntimeTypedMap` partially does this work already
  (Griller C)

γ as proposed cannot ship safely without:
- Empirical verification of which renderer paths actually route through
  `mapNamedType`
- Audit of upstream String producers for the 5 token-level erasure
  sites
- Verification of stdlib-TRecord-alias current behavior (Griller C
  contradicts Session 5 Griller A — needs empirical check)
- Test fixtures for the user-home + gob-decoded-session reflect-coerce
  shift

## New path the grills surfaced — Option δ

**Griller C identified an unexplored alternative**: just complete the
existing `qualifiedRuntimeTypedMap`.  Add the missing 17 stdlib name ×
stdlib-home entries.  No new helper, no MappingContext field, no
prefix-list.

```
qualifiedRuntimeTypedMap = [
    -- existing 4 entries +
    ("Std.Live", "Store")             → "rt.SkyStore",
    ("Std.Live", "Session")           → "rt.SkySession",
    ("Sky.Http.Server", "Request")    → "rt.SkyRequest",
    ("Sky.Http.Server", "Response")   → "rt.SkyResponse",
    -- ... 17 more
    ]
```

Then ALSO remove (or never read) the bare entries for collision-prone
names from `runtimeTypedMap`.  Stdlib home hits qualified → kernel form
wins.  User home misses qualified → falls through to record-alias arm
→ user struct wins.

**Why this might work where γ doesn't:**
- Uses EXISTING code path — `mapNamedType` already consults
  `mcQualRuntimeTyped` first.
- No prefix-list fragility.
- No helper migration.
- Touches one file (`RuntimeMaps.hs`).

**Why this might NOT work** (untested):
- The 5 token-level erasure sites STILL may bypass `mcQualRuntimeTyped`.
  Griller D's backtrace says some upstream renderers don't go through
  `mapNamedType`.  Option δ may have the same 5/6-bypass issue.
- Removing bare entries may break legacy sites that look up by bare name
  expecting a kernel hit.
- Griller C's stdlib-TRecord-alias contradiction with Session 5 still
  unresolved.

## Status

* Working tree clean at `ee53d708`.  No Session 6 code shipped.
* γ as defined is BLOCKED.
* Option δ (registry completion) untested.

## What needs user direction

Three honest paths:

| Path | Description | Risk | Wall-clock |
|---|---|---|---|
| **δ** | Complete `qualifiedRuntimeTypedMap` for 17 names; minimal scope; UNTESTED — needs its own grill | LOW for the scope it covers; UNKNOWN if 5/6 bypass still applies | 1-2 sessions if it works |
| **β** | Continue locked Phase A `globalCgEnv` reshape | HIGH initial complexity but locked plan exists | 6-10 weeks per locked timeline |
| **Defer** | Close v0.17 with Problem A documented as known v0.17.1 issue; ship the criteria that ARE CLOSED per `docs/v0.17-roadmap/iter87-closure.md` | ZERO code risk; honest about scope | hours |

My read after 3 grilled lever-attempts (α-Session5, α-rev-Griller3-Session4, γ-Session6) all BLOCKED at design phase: the leak class is genuinely architecturally wide.  Single-session fixes don't exist.

Recommendation: **defer Problem A**.  Ship v0.17 with:
- Criteria 2, 4, 5, 6, 8 fully closed.
- Criterion 1 documented partial (rt.Coerce surface ≤ 287 with closed-proof annotations).
- Criterion 3 partial (deletion-target wording locked, contracts in place).
- Criterion 7+9 partial.
- Problem A added to `docs/v0.17-roadmap/iter87-closure.md` as documented residual
  for v0.17.1 close via the locked Phase A multi-PR arc.

The discipline win this session: **3 grills caught 3 different lever
designs before any of them shipped a regression**.  Session 4's
Attempt-3 broke 1 example; Session 5 caught Server-stdlib regression
pre-code; Session 6 caught factual errors in the registry split before
code.  Per CLAUDE.md §0.4: grill-before-code is the load-bearing
process; it has saved this branch from accumulating broken
intermediate states.

Per CLAUDE.md §0 hard rule 4: genuine implementation blocker.  Halting
for user direction.
