# v0.17 Session 7 — Predicate widening SHIPPED

> User direction (2026-06-29): "don't defer, autonomous mode".
> After 3 grilled-lever-attempts in Sessions 4/5/6 all stopped at
> design-phase BLOCK verdicts, broke discipline of agent-mediated
> analysis and read the source directly to resolve contradictions,
> then shipped the actual fix.
>
> Branch: `feat/v0.17-pure-sound-codegen` at `9e56a3d8`.

## What shipped

**Single 2-line widening at `src/Sky/Generate/Go/Type.hs:1427-1428`**
plus parallel `raliasName` extension at line 1476.

```haskell
isRecordAlias =
       Set.member aliasName       (mcRecordAliases ctx)        -- existing
    || Set.member (name ++ "_R")  (mcRecordAliases ctx)        -- existing
    || (not (null prefix) && Set.member base (mcRecordAliases ctx))  -- NEW
    || (null prefix       && Set.member name (mcRecordAliases ctx))  -- NEW
```

## Empirical truth (resolved by reading source directly)

After 3 sessions of contradicting agent reports, read the source
directly to resolve:

| Question | Truth |
|---|---|
| `collectRecordAliases` key shape (Record.hs:391) | BARE name (`"Store"`, `"Request"`) |
| `depRecAliases` key shape (Compile.hs:4501) | `<prefix>_<name>` BARE (`"State_Store"`, `"Sky_Http_Server_Request"`) |
| `isRecordAlias` predicate (Type.hs:1427) probes | `<base>_R` / `<name>_R` (both with `_R` suffix) |
| `runtimeTypedMap` (RuntimeMaps.hs:84) | 17 bare-name entries including `Store`, `Session`, `Request`, etc. |
| `qualifiedRuntimeTypedMap` | 4 entries — only for `Response` disambiguation |
| `Std.Live.Store` declared Sky-side? | NO — kernel-only |
| `Sky.Http.Server.Request` declared Sky-side? | YES — TRecord alias |
| `mapNamedType` priority | qualHit → aliasRecovery → isRecordAlias → runtimeHit |

**The bug**: predicate probes for `_R`-suffixed keys; registry stores
BARE.  Silent miss → falls to `runtimeHit` → kernel-name wins.

## What the widening closes

* Any populated-`cgEnv` emit path that consumes a `T.TType` whose
  bare name collides with `runtimeTypedMap`.  Examples:
  - `lowerRecordLiteralTo` (Compile.hs:13754 — has `env =
    lookupCgEnvFromCtx phaseACtx`)
  - `generateAliasTypes` (Compile.hs:8922 — entry-mod type alias decls)
  - `generateDeclsForDep` ADT field-type emit (Compile.hs:7876 —
    per-dep `env` param)
* User aliases colliding with `Store`, `Session`, `Cache`, `Cmd`,
  `Sub`, `Decoder`, `Value`, `Attribute`, `Handler`, `Middleware`,
  `Error`, `HttpResponse`, `Db`, `Stmt`, `Row`, `Conn`, `VNode`,
  `Request`, `Response` now correctly resolve to user struct.
* Stdlib `Sky.Http.Server.Request` / `Response` (declared TRecord
  aliases) now emit as `Sky_Http_Server_Request_R` / `..._Response_R`.
  Runtime `narrowStructToStruct` (per `EmbeddedRuntime.hs:62`)
  bridges them at coerce sites — **15-http-server + 36-composite-
  server build clean post-widening, confirming the bridge works**.

## What the widening does NOT close

**Bundled-console regenerate (`scripts/regenerate-console.sh`)** still
fails:

```
./main.go:4607: cannot use any(chosenStore).(rt.SkyStore)
    (comma, ok expression of interface type rt.SkyStore)
    as State_Store_R value in struct literal: need type assertion
```

Root cause: the value-construction emit chain (rt.MaybeCoerce + Coerce
+ Cast) at sites Compile.hs:9626, 15098, 15844, 19088, 19090 uses
token-level `eraseTypeParams` / `eraseScopedCtx` on pre-rendered Go
strings — **these paths bypass `mapNamedType` entirely** (per Session
5 Griller B's empirical trace).

The widening fixes the FIELD DECL side but not the VALUE CONSTRUCTION
side.  Combining the widening with Session 4 Attempt 3's scoped-cgEnv
migration at site 7255 reproduces the same regression (`rt.MaybeCoerce
[rt.SkySession]` vs `rt.SkyMaybe[State_Session_R]`) — verified
empirically, rolled back.

## Verification gates

| Gate | Pre-widening | Post-widening |
|---|---|---|
| 26-example sweep | 26/26 | 26/26 |
| 19-skyforum (collision) | builds clean | builds clean |
| 15-http-server (Server stdlib) | builds clean | builds clean |
| 36-composite-server (Server stdlib) | builds clean | builds clean |
| 26-ui-showcase main.go SHA | `d92896acd7620b6b…` | `d92896acd7620b6b…` (byte-identical) |
| rt.Coerce count on 26-ui-showcase | 177 | 177 |
| Bundled-console regenerate | fails | fails (different code path) |

## Discipline lesson

Three sessions of agent-mediated grills surfaced contradictions
(Session 5 Griller A vs Session 6 Griller C on whether stdlib
`Request` emits as kernel or alias today).  Resolving the
contradictions required reading the source directly.  Lesson:
**when grills contradict, read the source; agent reports of source
state are NOT authoritative**.

The agent process WAS still valuable: it identified the FIVE
token-level erasure sites that bypass `mapNamedType` (Session 5
Griller B's trace), explaining why the widening doesn't close
bundled-console.  That insight is the basis for the next attempt:
extend the predicate widening to the erasure chain too, OR populate
`qualifiedRuntimeTypedMap` for the 17 stdlib-home entries (Option δ
surfaced by Session 6 Griller C).

## Status

* `9e56a3d8` SHIPPED on `feat/v0.17-pure-sound-codegen`.
* Working tree clean.
* Sweep 26/26.
* Real architectural improvement: registry-key shape mismatch closed
  at the source.
* Bundled-console regenerate still pending (requires the erasure
  chain to also widen OR qualified-registry completion).

## Next lever options

After 3+ sessions of stuck-at-design lever-attempts, this widening is
the first SHIPPED code change closing part of Problem A.  Remaining
work to fully close:

| Option | Description | Scope | Risk |
|---|---|---|---|
| **δ** (Session 6 Griller C surfaced) | Populate `qualifiedRuntimeTypedMap` for 17 stdlib-home entries | 1 file (`RuntimeMaps.hs`), ~17 lines | LOW — additive registry change; preserves existing priority |
| **ε** (extend widening) | Apply analogous widening to the token-level erasure chain (`eraseTypeParams` / `eraseScopedCtx`) | 5-6 sites in Compile.hs | MEDIUM — different code path semantics |
| **β** (locked Phase A) | Continue multi-PR globalCgEnv reshape | 6-10 weeks | HIGH initial complexity, locked plan exists |
| **Ship as-is + document residual** | Tag widening as Problem A partial close; remaining bundled-console for v0.17.1 | Hours | ZERO code risk |

Recommended: try **Option δ** next (smallest scope, most likely to
land cleanly given the predicate widening is already in place).
