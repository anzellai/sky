# v0.17 Session 4 — N-strikes reclassification

> Per CLAUDE.md §0.3 rule 3 and §0.4 N-strikes circuit-breaker:
> 3 consecutive iterations failed to close Problem A on the same
> architectural lever (cgEnv threading for alias-name resolution).
> A 4th attempt is FORBIDDEN without re-classification.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `4718e37a`.  Working
> tree clean.

## The three attempts

### Attempt 1 — Naive full-phaseACtx at all 3 substituteTVarsToGo callers

Threaded `lookupCgEnvFromCtx phaseACtx` (FULL cgEnv) into sites 7255,
8941, 13763.  Result: **sweep regressed 26 → 16**, `rt.SkyMaybe[any]`
vs concrete-instantiation Maybe mismatches.

Root cause: full cgEnv carries `_cg_funcSkyToGoTVars` which substituted
caller-side TVar instantiations into alias DECL bodies.

Reverted.  See `docs/v0.17/session-4-commit-3-grill-findings.md`.

### Attempt 2 — Griller-3-shape: sites 7255+8941 minimal cgEnv

Griller-recommended `{ _cg_aliases = ... }` at DECL sites.  Re-grilled
by Griller #3 who identified this would be a **no-op**: `mapNamedType`
reads `_cg_recordAliases` (Set String), NOT `_cg_aliases` (Map).

Never shipped (caught at grill).  See
`docs/v0.17/session-4-commit-3-bisection-result.md`.

### Attempt 3 — sibCgEnv (`_cg_recordAliases` + `_cg_aliases`) at all 3 sites

Threaded SCOPED cgEnv (just the alias registries, no TVar
instantiation channels) at sites 7255, 8941, 13763.  Result: **sweep
25/26**.

- ✅ **Console regenerate closes for `Store`** (no `model.Store.ReadTraces undefined`).
- ❌ **19-skyforum still leaks** `rt.SkySession`:
  ```
  cannot use rt.MaybeCoerce[rt.SkySession](rt.Nothing[any]())
      (value of struct type rt.SkyMaybe[rt.SkySession])
      as rt.SkyMaybe[State_Session_R] value in struct literal
  ```

Reverted.  Working tree clean.

## The leak class is wider than the 3 substituteTVarsToGo callers

`rt.MaybeCoerce[T]` value construction is emitted at **multiple
sites** (grep confirms ≥6):

* `Compile.hs:9667` — `"rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "](" ++ src ++ ")"`
* `Compile.hs:10651` — uses `resolveWrapParamsCtx phaseACtxC ctx mSrc "Maybe" inner`
* `Compile.hs:15153` — `"rt.MaybeCoerce[" ++ eraseScoped inner ++ "]")`
* `Compile.hs:15899` — `"rt.MaybeCoerce[" ++ inner ++ "]"`
* `Compile.hs:17185` — `"rt.MaybeCoerce[" ++ ...`
* `Compile.hs:19143` — `"rt.MaybeCoerce[" ++ inner ++ "]"`

Each of these constructs a Maybe coercion's `T` parameter via a
DIFFERENT erasure / resolve chain (`eraseTypeParams`, `eraseScoped`,
`resolveWrapParamsCtx`, etc.).  These chains all consult various
combinations of the legacy IORef + scopeStateRef + LowerCtx — but
NOT the `_cg_recordAliases` Set that fixes the alias-vs-kernel
collision.

The `Store` leak happened to be visible at site 7255 because that's
where the bundled-console regenerate's `Model.Store` field type is
rendered.  `Session` (19-skyforum) leaks via the value-construction
chain at one of the 10651/15899/17185/19143 sites — bisection on
the `Session` symbol would be needed to attribute it precisely.

## Why the same lever can't close this

Each `rt.MaybeCoerce[T]` emit site has its own context.  Threading
the alias registry to ONE site closes ONE example.  19-skyforum
exposes a different site than the bundled console.  The next example
(perhaps `13-skyshop` with a `Conn` collision, or `17-skymon` with a
`Request` collision) will expose yet another.  Per Griller #3's
HIGH-severity finding: there are 17 kernel-name collisions latent in
`runtimeTypedMap`.

The correct architectural close is NOT "site-by-site cgEnv thread"
but one of:

**Option α — Renderer priority swap**:  Change `mapNamedType`'s
resolution priority so user record-aliases ALWAYS win over kernel
names when the home module matches.  Single-source change in
`Type.hs:1418-1534` rather than N call-site migrations.  Risk: must
prove no example relies on a user-alias shadowing a kernel name
unintentionally.

**Option β — Coherent cgEnv reader migration (criterion #3 work)**:
Migrate every alias-resolution reader site (substituteTVarsToGo
fallthrough + eraseTypeParams + eraseScoped + resolveWrapParamsCtx
+ lookupAliasDecl IORef readers at 22737/22752) to a single shared
reader source.  This is the locked Phase A `globalCgEnv` reshape
work — multi-session, multi-PR.

**Option γ — Kernel-name registry split**:  Split `runtimeTypedMap`
into "kernel-only types" (no user shadowing) and "stdlib aliases"
(can be shadowed by user redeclarations in dep modules).  Then
the collision class disappears at its source.

## Floor membership

Per CLAUDE.md §0.3 rule 4 (floor-touching tactics need user
authorisation): Options α and γ touch `mapNamedType` in Type.hs +
the kernel-name registry in RuntimeMaps.hs — both are §8 irreducible
floor territory.  Option β stays compiler-internal.

**The 2026-06-23 floor authorization in AUTONOMOUS_GOAL.md** (lines
988-1029) DOES authorise floor-touching tactics for v0.17 close.  So
options α and γ are permitted; only the SHAPE choice needs user
direction.

## What was achieved this session

* **Commit 1+2 scaffolding** shipped at `cd8f4ebf` —
  `substituteTVarsToGoCtx` + `substituteTVarsToGoBoundedCtx` exist
  as additive Ctx variants, byte-identical to legacy via emptyCgEnv
  delegation.  No caller migrated.  Working tree gates clean.
* **Empirical bisection** shipped at `4718e37a` — sites 8941+13763
  produce ZERO `rt.SkyStore` hits; site 7255 is the dominant emit
  channel for the Store-specific leak in bundled-console regenerate.
* **3 fresh-context grills run** — converged on identifying:
  (a) `_cg_funcSkyToGoTVars` is the regression channel (avoid full
      phaseACtx),
  (b) `_cg_aliases` is the wrong field (it's `_cg_recordAliases`),
  (c) the leak class spans 17 kernel-name collisions latent across
      multiple emit sites.
* **N-strikes invocation** — 3 attempts on the same lever DID close
  the bundled-console regenerate symptom (Attempt 3), but exposed
  that the leak class is wider than the 3 substituteTVarsToGo
  callers.  This document is the mandatory re-classification.

## What needs user direction

Pick ONE of:

| Option | Scope | Risk | Wall-clock |
|---|---|---|---|
| **α** Renderer priority swap in mapNamedType | 1 file, 1 commit | MED — affects every alias-vs-kernel resolution | 1 session if it works |
| **β** Coherent cgEnv-reader migration (Phase A continuation) | Multi-PR, criterion #3 | HIGH — large surface, but locked plan | 6-10 weeks per locked Phase A timeline |
| **γ** Kernel-name registry split | RuntimeMaps + classifiers | LOW for kernel side, but requires user-alias surface review | 1-2 sessions |
| **Defer** Treat Problem A as known-issue for v0.17, close in v0.17.1 | Documentation only | ZERO code | Hours |

My recommendation: **Option α (renderer priority swap)** first as a
narrow gate trial.  If the 26-example sweep stays green AND
bundled-console regenerate closes AND 19-skyforum closes, ship.  If
ANY regression, fall back to Option γ (registry split) which is more
surgical.  Defer Option β to the locked Phase A multi-session arc
unless α and γ both fail.

## What the loop should NOT do next

Per CLAUDE.md §0.3 rule 3 + N-strikes circuit-breaker: a 4th
substituteTVarsToGo-callsite-migration attempt is **FORBIDDEN**.
The lever is exhausted.  Any further work on this leak class must
re-classify per §0.4 (this document) and pick a different lever
(α, β, or γ above) — OR escalate to user-explicit "defer".

## Status

* Working tree clean at `4718e37a`.
* All scaffolding committed.
* Commits 1+2 (Ctx variants) remain unused but available.
* Bisection result + grill findings + this reclassification doc
  serve as the durable artifact for whoever picks this up next
  (this session continuation or a future session).
