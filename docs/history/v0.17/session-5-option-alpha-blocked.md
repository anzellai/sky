# v0.17 Session 5 — Option α BLOCKED by adversarial grills

> User selected Option α (renderer priority swap / predicate widening
> in `mapNamedType`) for autonomous execution.  Architecture-Consult
> + 2 adversarial grillers run BEFORE code change per CLAUDE.md §0.4
> discipline.  Both grillers returned BLOCK with convergent serious
> findings.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `fe45bbf7`.  Working
> tree clean.

## Architecture-Consult REVISE (not the priority swap)

The Architect's investigation REFRAMED the fix: priority order is
already correct (record-alias check at branch 5 precedes kernel
fallback at branch 6).  The actual bug is a **registry-key shape
mismatch** at `src/Sky/Type/Type.hs:1427`:

* `_cg_recordAliases` stores BARE names: entry-mod `Store`,
  dep `State_Store` (Record.hs:391, Compile.hs:4501).
* `isRecordAlias` predicate looks for `<base>_R` / `<name>_R` —
  neither key exists in the registry.
* Silent miss → falls through to branch 6 → kernel `rt.SkyStore` wins.

Proposed two-line widening:

```haskell
isRecordAlias =
       Set.member aliasName        (mcRecordAliases ctx)
    || Set.member (name ++ "_R")   (mcRecordAliases ctx)
    || (not (null prefix) && Set.member base (mcRecordAliases ctx))
    || (null prefix       && Set.member name (mcRecordAliases ctx))
```

## Griller A: BLOCK — Server stdlib regression

**Critical finding**: stdlib `Sky.Http.Server.Request` and `Response`
aliases ARE TRecord records.  They live in BOTH:

* `_cg_recordAliases` (added via `collectRecordAliases` at
  Record.hs:391 — TRecord filter passes).
* `runtimeTypedMap` (mapped to `rt.SkyRequest` / `rt.SkyResponse`).

Today's branch-6-wins behavior emits `rt.SkyRequest` for any
`T.TType _ "Request" []` — which is **load-bearing** for every
Server-using example:

* `examples/15-http-server`
* `examples/36-composite-server`
* (likely others using `Sky.Http.Server`)

Post-fix branch 5 wins → user struct `Sky_Http_Server_Request_R`
wins → runtime cast mismatch when Server handlers receive incoming
requests typed by the runtime as `rt.SkyRequest` but the emitted Go
expects `Sky_Http_Server_Request_R`.

Griller A's Handler-was-mis-cited finding: the Architect's
"`Handler` switches identifier but is structurally equivalent" claim
was wrong — `Handler` is a FUNCTION alias, filtered out of
`_cg_recordAliases` by `collectRecordAliases`'s TRecord filter at
Record.hs:396.  So the widening doesn't affect Handler at all
(neither pre- nor post-fix).  The Architect's headline justification
was the wrong example.

## Griller B: BLOCK — Token-level erasure bypasses the fix + runtime semantics change

**Critical finding 1 — Type.hs:1427 only affects 1 of 6 emit
sites.**  Of the 6+ `rt.MaybeCoerce[T]` emit sites listed in the
Session 4 N-strikes reclassification:

| Site | Routes through mapNamedType? | Path |
|---|---|---|
| Compile.hs:9626 | NO | token-level `eraseTypeParams inner` on pre-rendered Go string |
| Compile.hs:10610 | YES | `resolveWrapParamsCtx → mapSkyTypeToGo → mapNamedType` |
| Compile.hs:15098 | NO | `eraseScopedCtx ctx inner` on pre-rendered Go string |
| Compile.hs:15844 | NO | literal `inner` String from `stripSkyMaybe goType` |
| Compile.hs:17130 | PARTIAL | uses `resolveWrapParams` (legacy non-Ctx variant) |
| Compile.hs:19088, 19090 | NO | `inferred` String from `stripParametric` |

Fixing only the `mapNamedType` predicate closes 1/6 sites.  The
other 5 consume pre-rendered Go strings where the kernel-vs-alias
decision was already baked in by an upstream renderer.  Closing 1
site is the EXACT N-strikes site-by-site pattern Session 4 already
exhausted.

**Critical finding 2 — Runtime semantics change.**  In
`runtime-go/rt/rt.go`:

```go
type SkyStore = any                            // line 3538
type SkySession = any                          // line 3537
type SkyMaybe[A any] struct { Tag int; JustValue A }    // line 87
```

`rt.SkyStore` and `rt.SkySession` are `any` aliases.  Today's
`rt.MaybeCoerce[rt.SkySession](src)`:

* T = `any` (alias resolution)
* `src.(SkyMaybe[any])` cast succeeds for any source
* `coerceInner[any]` is identity
* Net: no-op identity widening, **no reflection**

Post-fix `rt.MaybeCoerce[App_Session_R](src)`:

* T = concrete struct
* `src.(SkyMaybe[any])` still matches
* `coerceInner[App_Session_R](v)` runs the reflect-backed map→struct
  narrowing
* Behavior change: identity → reflect coerce
* New runtime panic class for FFI-returned `map[string]any`,
  gob-decoded session state, and any path where Sky-side typing
  asserts `Session` but the runtime carries an untyped map

This is a load-bearing runtime invariant the Architect didn't
analyse.  Even if the renderer fix were complete (it isn't — see
Critical finding 1), it would introduce a new runtime panic
surface.

## Verdict: BLOCK

Option α as defined cannot close Problem A safely.  The required
scope is much larger:

1. **Renderer fix** at Type.hs:1427 is necessary but covers ≤ 1/6
   emit sites.
2. **5 token-level erasure call sites** (Compile.hs:9626, 15098,
   15844, 19088, 19090) each carry their own
   `eraseTypeParams`/`eraseScopedCtx` chain that emits kernel names
   directly.  Each needs its own audit + migration.
3. **Runtime contract change**: either keep `rt.SkyStore` /
   `rt.SkySession` as `= any` aliases (no behavior change but
   weakens the typing claim) OR make them concrete types
   (validates the typing but breaks every existing call site
   passing `any` values).
4. **Server stdlib aliases** (Request/Response) need a special-case
   exemption from the user-alias-wins logic OR a runtime-side
   adapter making `rt.SkyRequest` and `Sky_Http_Server_Request_R`
   interchangeable.

## What the next lever should be

Reading the grill outputs back against the Session 4 options:

* **Option α (renderer priority swap)** — DISQUALIFIED by Griller A
  (stdlib collision) and Griller B (5/6 bypass + runtime semantics).
* **Option β (coherent multi-PR Phase A reshape)** — still on the
  table; locked plan in
  `docs/v0.17-roadmap/phase-A-cgenv-reshape.md`; 6-10 week
  wall-clock per AUTONOMOUS_GOAL.md lines 1110-1115.
* **Option γ (kernel-name registry split)** — promoted to the
  leading candidate.  Specifically: split `runtimeTypedMap` into:
  - **kernel-only** types (`SkyMaybe`, `SkyResult`, `SkyTask`,
    `SkyList`, `SkyDict`, `SkySet`, `SkyCmd`, `SkySub`) that
    should ALWAYS win — these are the parametric core where
    no user redeclaration makes sense.
  - **shadowable** stdlib types (`Store`, `Session`, `Cache`,
    `Request`, `Response`, `Handler`, `Middleware`, …) where
    a USER alias in a USER module should beat the kernel form,
    BUT the stdlib's own module (`Sky.Http.Server.Request`) keeps
    emitting the kernel form for runtime interop.
* **Defer** — close v0.17 with Problem A documented as known-issue,
  close in v0.17.1 or 0.18.

## Recommendation

Spawn a fresh Architecture-Consult for **Option γ (kernel-name
registry split)** with the empirical Server-collision evidence
from Griller A.  If γ also has hidden gotchas, fall back to:

1. **Defer Problem A for v0.17** and ship a release that closes
   every CLOSED criterion (per
   `docs/v0.17-roadmap/iter87-closure.md`) with Problem A
   explicitly documented as a known v0.17.1 close.
2. **Option β multi-PR Phase A** as the long-arc fix.

## Status

* Working tree clean at `fe45bbf7`.
* All Session 4 artifacts intact + this Session 5 finding.
* No code shipped in Session 5.  The grill-before-code discipline
  caught the regression before it broke 26-example sweep.
* N-strikes counter for THIS lever (renderer priority/predicate
  swap): 1 attempt prevented; lever requires re-classification
  before re-attempting.

Per CLAUDE.md §0 hard rule 4: this is a genuine implementation
blocker requiring user direction.  Options remaining: γ, β, or
defer.  Not autonomous-resolvable without picking the next lever.
