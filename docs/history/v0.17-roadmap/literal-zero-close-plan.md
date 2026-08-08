# v0.17 literal-zero close plan

**Status:** AUTHORIZED 2026-06-23 (user-explicit floor-touching).
**Branch:** `feat/v0.17-fully-typed-codegen`.
**Tip at plan creation:** `57d77597` (runtime shim shipped).
**Methodology:** CLAUDE.md §0.3 hard rules LIVE. Every workflow
under this plan begins with `phase('Architecture-Consult')` and
cites mechanism per the architecture references.

## Canonical references (read FIRST, every session)

- `docs/architecture/sky-compiler-architecture.md` (26 KB) —
  compiler pipeline + rt.Coerce origin catalog + levers + floor
- `docs/architecture/sky-stdlib-correctness.md` (63 KB) —
  Sky.Core laws + Std.Ui invariants + TEA + Std.Db/Auth security
  + cross-backend parity + 5 critical gaps

Both docs are mermaid-illustrated and source-line-cited. They
are durable across sessions, agents, and workflows.

## Plan rationale

The user has read the architecture references and committed to
the MAXIMALLY-AMBITIOUS path: literal zero `rt.Coerce` in
emitted Go via runtime contract rewrite, not the
PRAGMATIC-V0.17-CLOSE reframe. The two breaking changes
(Sky.Live wire format + FFI codegen-emitted shims) are
accepted. Estimated 4-8 weeks of focused work, ~15-25 sessions,
~8-12k LOC.

## Definition of done (v0.17 final)

Judge agent returns "100% ACHIEVED" against the verbatim goal
(`.claude/AUTONOMOUS_GOAL.md` lines 8-13) AND the 9 derived
criteria. Specifically:

1. **Zero `rt.Coerce` calls** in emitted Go for every example
   in the sweep (sealed-iface ADT emission + AsListT elimination +
   per-FFI-symbol typed shims + per-Msg dispatch codegen).
2. `eraseUndeclaredTVarsInGoSource` DELETED — already shipped.
3. `globalCgEnv` + `globalGoSigMap` + `scopeStateRef` + ~14
   residual IORefs DELETED (`CompileCtx` record threaded).
4. `SKY_GOSIG_DIFF=1` zero `Anon_R_*` errors on full sweep.
5. `GoTypeAdt` + `GoTypeRoundTrip` parity 72/72 — shipped.
6. CLAUDE.md Limitations #4-10 closed — shipped.
7. Cycle 6 umbrella (#383) CLOSED — verified by Judge.
8. Property fuzzer ≥10,000 iters clean — shipped iter 86 (harness);
   re-run after each phase boundary.
9. All in-flight v0.17 umbrellas (#383 #644 #654 #660 #664 #672
   #677) CLOSED.
10. 5 stdlib gaps from `sky-stdlib-correctness.md` CLOSED.

## Phased breakdown

### Phase 1 — Stdlib correctness gaps (~3-5 days, parallel-safe)

Architecture cite: `sky-stdlib-correctness.md` §8 (actionable gap list).

Workflow Phase-0 cites these gaps; tactical phases close them in
parallel because they touch independent modules.

- **G1 Task.parallel docstring/runtime alignment.**
  Site: `runtime-go/rt/rt.go:5591-5619` + `sky-stdlib/Sky/Core/Task.sky:124-128`.
  Lever: fix runtime to use result channel + context cancel for early
  termination on first Err. ~1 session.
- **G2 Sky.Tui silent-drop diagnostic.**
  Site: `runtime-go/rt/tui_ui.go` (the parallel renderer).
  Lever: emit deduped `tuiWarn` for pseudo-class / transition /
  animation / @media markers. ~1 session.
- **G3 `Math.isNaN` export.**
  Site: `sky-stdlib/Sky/Core/Math.sky:6-19` exposing list.
  Lever: add `isNaN` to exposed surface. Trivial.
- **G4 `Db.migrate` tenant-gate documentation.**
  Site: `sky-stdlib/Std/Db.sky` migrate header + `runtime-go/rt/db_*.go`.
  Lever: module-header annotation. Trivial.
- **G5 Functor/Applicative/Monad law specs.**
  Site: new `test/Sky/Stdlib/MaybeLawsSpec.hs`,
  `ResultLawsSpec.hs`, `TaskLawsSpec.hs`.
  Lever: Hspec property tests via `QuickCheck`. ~1-2 sessions.

Phase 1 exit criterion: 5 gaps closed + specs green + sweep clean
+ no behavior regression on existing examples.

### Phase 2 — Sealed-iface migration on remaining ADTs (~5-8 days)

Architecture cite: compiler ref §6 cat ADT_CONS + §7 sealed-iface
lever; stdlib ref §6 parity table (Sky.Tui renderer fork).

Pre-req: Phase 1 G2 (Sky.Tui tuiWarn) shipped — eliminates the
silent-divergence risk of flipping Element which `tui_ui.go`
consumes.

Pre-req: `runtime-go/rt/adt_shape.go::unwrapADTShape` shipped at
iter 89 commit `57d77597` — accepts both legacy SkyADT and
sealed-iface variant.

Phase 2.1 — Codegen `_T[T any]` type-declaration emission for
parametric ADTs in dep modules (the iter 89b blocker root cause).
Site: `src/Sky/Build/Compile.hs` near `emitSealedIfaceUnion` —
when ADT is parametric, ALSO emit `type Foo_T[T1 any] = Foo`
typed sibling alias adjacent to the sealed iface declaration.
This closes the `undefined: Std_Html_Html_T` failure mode.
~1-2 sessions.

Phase 2.2 — Populate `sealedIfaceFlipParametricAllowList` (one
ADT per iter, build + spec + 13-example sweep verified per flip):
- `Std.Html.Html` — proves the parametric flip end-to-end.
- `Std.Html.Attributes.Attribute`.
- `Std.Ui.Element` — recursive (Node carries `List (Element msg)`).
- Remaining 15 ADTs in batch order documented in iter 88 comment.

Per-flip cite: compiler ref §6 cat ADT_CONS + §7 lever name +
`Compile.hs:912` gate + `Compile.hs:2168` `emitSealedIfaceUnion`
+ `runtime-go/rt/adt_shape.go::unwrapADTShape` consumer
contract.

Phase 2 exit: all 18 parametric/monomorphic ADTs flipped;
26-ui-showcase rt.Coerce floor measured ≥30 below baseline.

### Phase 3 — AsListT elimination (~3-5 days)

Architecture cite: compiler ref §6 cat AsListT (currently 190
sites on 26-ui-showcase) + §7 lever "codegen specialisation of
element-narrow at list-typed slots".

Site: `src/Sky/Build/Compile.hs` list-literal lowering site (locate
via `grep -n "rt.AsListT"`). When the element type is fully
known via `LowerCtx`, emit typed slice literal `[]<T>{...}`
directly instead of `rt.AsListT[<T>]([]any{...})`.

Phase 3 exit: AsListT call count on 26-ui-showcase ≤10.

### Phase 4 — Per-Msg dispatch codegen (~5-8 days)

Architecture cite: compiler ref §8 floor cat "TEA Msg trampoline
at reflect.MakeFunc" — user-authorised floor-touching tactic.

Replace `rt.SkyCall` via `reflect.MakeFunc` with codegen-emitted
per-Msg-constructor dispatch table. Eliminates `func(any) any`
reflective indirection AND the per-call MakeFunc allocation
(~100 ns per HOF call site per CLAUDE.md current state).

Site: `runtime-go/rt/rt.go` `SkyCall` family + `src/Sky/Build/Compile.hs`
HOF call lowering. New: per-module Msg-dispatch table emitted at
init().

Phase 4 exit: zero `reflect.MakeFunc` calls reachable from
emitted code; no behavior regression on TEA loop.

### Phase 5 — Per-FFI-symbol typed shim emission (~5-8 days)

Architecture cite: compiler ref §8 floor cat "Go-FFI return
boundary" — user-authorised floor-touching tactic.

For every imported Go function whose return type is concrete
(not `any` / `interface{}`), emit a typed narrowing shim at
codegen time. Eliminates the `rt.Coerce[T]` wrap at FFI return
sites.

Site: `tools/sky-ffi-inspect/` introspector + `src/Sky/Build/Ffi.hs`
emit. The introspector already records concrete return types
per v0.15.1; the gap is at the emit side.

Phase 5 exit: zero `rt.Coerce` wraps around FFI calls returning
typed Go values.

Caveat: `.skycache/ffi/*.skyi` rebuilds entirely on upgrade
(~15 min per app on first build). Document in CLAUDE.md.

### Phase 6 — Sky.Live wire format rewrite (~5-8 days)

Architecture cite: compiler ref §8 floor cat "Sky.Live wire-decode
entry" + stdlib ref §4 (TEA architecture) — user-authorised
floor-touching tactic + Path C session-break accepted 2026-06-21.

Replace `encoding/gob` with variant-tagged custom binary protocol
+ per-ADT `MarshalBinary` / `UnmarshalBinary` codegen. Eliminates
`gob.Register(SkyMaybe[any]{})` family at runtime-go/rt/live_store.go:119
and removes the `any` wire-cross.

Site: `runtime-go/rt/live_store.go` + `runtime-go/rt/live_session.go`
+ codegen emits per-ADT marshallers from `Compile.hs`.

Phase 6 exit: zero `gob.Register` calls; wire format documented;
session-store migration tool ships (or accepts break on upgrade).

### Phase 7 — IORef defusing close (~3-5 days)

Architecture cite: compiler ref §5 (IORef impurity surface).

Delete `scopeStateRef` literally — thread `CompileCtx` record
through every emit site (~20 → ~60 call sites). Extend Option A
Stage 3+4 from #672. Move `globalAnonRecords` to Reader-style ctx.
Eliminate remaining ~14 residual IORefs catalogued in #654.

Phase 7 exit: zero module-level `IORef`s in `Compile.hs` outside
the build-cache surfaces (which are intentional).

### Phase 8 — Final close (~2-3 days)

- Run property fuzzer ≥10k iters clean on `feat/v0.17-fully-typed-codegen`
- Full sweep (cabal-test + example-sweep + verify-cli + verify-all-web)
- Re-spawn Judge agent against verbatim goal — must return PASS
  with NO "but/except/however/caveat/mostly/essentially" framings
- User approves tag — push `v0.17.0`

## Per-phase verification gates

Every phase MUST measure (per CLAUDE.md §0.3 RULE-4):

```bash
# BEFORE
rg -c "rt\.Coerce" examples/26-ui-showcase/sky-out/main.go
rg -c "rt\.Coerce" examples/00-standard-libs/sky-out/main.go
rg -c "rt\.AsListT" examples/26-ui-showcase/sky-out/main.go

# AFTER (after build)
# Must show non-zero delta in the targeted direction
```

Per phase, the Judge agent verifies:
- Architecture references consulted (§6 category + §7 lever named)
- Measurement delta documented (before/after counts cited)
- No behavior regression (13 clean examples + Sky.Test 131/131 +
  locked specs)
- No silent floor erosion (other criteria not degraded)

## 3-strikes circuit-breaker (CLAUDE.md §0.3 RULE-3)

If any phase fails 3 iters in a row with zero delta on the same
lever, that phase's workflow MUST halt and re-classify. The next
workflow is forensic — re-read architecture references, identify
whether the phase target is in §8 floor that needs scope
adjustment, and escalate to user.

This prevents the iter 60-89 pattern (22 sealed-iface flips on
easy targets while the hard parametric class never closed).

## Forbidden under this plan

- Scaffolding-only commits (RULE-A consequence)
- "Option A/B/C" framings introducing a deferral path
  (RULE-F goal-freeze)
- "Closed-in-fact" tautological progress claims
- "Banking-state milestone" iters that move zero metric
- Continuing to retry a lever past 3-strikes (RULE-C)

## Expected outcome

By end of Phase 8, the Sky compiler reaches the verbatim goal:
- 100% fully typed e2e (no `any` in emitted Go for well-typed
  Sky code)
- No runtime panics (property fuzzer 10k iters clean +
  synchronous-panic gate covers all reachable surfaces)
- "If it compiles, it works" — verified
- Rock-solid + future-proof — sealed-iface foundation +
  IORef-free + codegen-driven dispatch leaves zero technical
  debt that v0.18+ must work around
- 100% soundness for v0.17 — Judge passes

Sky goes big from a true architectural floor, not a documented
exception inventory.
