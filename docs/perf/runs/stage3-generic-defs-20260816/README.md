# Stage 3 — a provable `List.foldl` / `List.any` call stops widening its list

Stage 2 specialised the **kernel** list helpers: the call site selects
`rt.List_mapT` over `rt.List_mapAny` and Go's generics instantiate. `List.foldl`
and `List.any` are not kernels — they are pure Sky
(`sky-stdlib/Sky/Core/List.sky`), auto-TCO'd to a Go `for` loop, so
`kernel_call` never sees them and `ListHof::of` never fires. Their fully-typed
runtime twins have therefore been compiled into every Sky binary and
**unreachable**.

This run measures re-pointing a provable call site at them.

## The change, in one emitted line

`sky-stdlib/Std/Ui.sky:2528`, reached twice per rendered element, emitted this
before:

```go
func Std_Ui_markerFlags(v_0 []Std_Ui_Attribute) Std_Ui_MarkerFlags_R {
	return /* FFI return */ rt.Coerce[Std_Ui_MarkerFlags_R](Sky_Core_List_foldl(
		func(_e0 any, _e1 any) any {
			return Std_Ui_markerFlagStep(/* generic erase */ rt.Coerce[Std_Ui_Attribute](_e0),
			                             /* generic erase */ rt.Coerce[Std_Ui_MarkerFlags_R](_e1)) },
		Std_Ui_noMarkerFlags(), /* primitive join */ rt.AsListT[any](v_0)))
}
```

and emits this after:

```go
func Std_Ui_markerFlags(v_0 []Std_Ui_Attribute) Std_Ui_MarkerFlags_R {
	return rt.List_foldlElemFirstT[Std_Ui_Attribute, Std_Ui_MarkerFlags_R](
		Std_Ui_markerFlagStep, Std_Ui_noMarkerFlags(), v_0)
}
```

Per call on n attributes the first costs one `rt.AsListT[any]` boxing **every**
element, one closure, 2n `rt.Coerce`, and one result coerce. The second passes
the callback **by name**.

Compiler site: `Ctx::sky_list_hof_plan` in `rust/crates/lower/src/lower.rs`,
reached from the general-call path. Architecture: doc 14 **R12** (the origin,
added by this work — the class had no row) closing via the new **§5.5** lever,
and explicitly not §5.6 monomorphisation: the Sky def is **not changed**.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Branch | `perf/stage3-generic-list-defs`, off `feat/embedded-postgres` @ `573ae3e2` |
| App | `forumbench` — `examples/19-skyforum` plus the `init`-only view-size lever, byte-identical to `../forum-rebaseline-20260816/`'s |
| Arms | the same app source, compiled by two `sky` binaries differing by exactly this change |
| Interaction | signed-in upvote toggle — 2 patches, every press |
| Session store | `memory` |
| Load | `tools/skyliveload`, loopback, 25 sessions, closed loop (`-think 0`), 45 s window |
| `GOMAXPROCS` | **1** on the app |
| Run order | before, after × 3, **alternating** within each view size |
| Repeats | 3 everywhere; ranges reported |

Verified before measuring, by `grep` on the built artefact rather than by
assumption: the two `main.go` differ (`9323972b…` vs `1acabf16…`), the two
`app-probe` binaries differ, the after arm emits **10** typed-twin calls and the
before arm **0**, and `rt.AsListT[any]` falls 22 → 12 in the emitted Go.

All 12 runs read `"patch_rate": 1`, `"patches_naming_absent_ids": 0`,
`"valid": true`.

## The numbers

Per-run rows are in `ab.tsv`; every run's directory holds its own
`allocs-{pre,post}.pprof`, `load.json`, `memstats-*`, `env.txt` and
`profwindow.txt`.

### 94 elements

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 16,423 – 16,469 | **12,637 – 12,671** | **−23.0%** |
| kB / interaction | 659.7 – 661.6 | **538.8 – 540.4** | **−18.4%** |
| interactions / sec | 851.0 – 890.7 | **1022.4 – 1057.5** | **1.19×** |

### 974 elements

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 170,637 – 170,852 | **128,922 – 131,046** | **−24.2%** |
| kB / interaction | 7,077.9 – 7,087.3 | **5,726.8 – 5,825.1** | **−18.7%** |
| interactions / sec | 98.6 – 100.1 | **103.3 – 121.7** | **1.16×** |

No range overlaps its counterpart on any row. The allocation gain is flat across
a 10× change in view size, which is what says it is structural.

**One run is an outlier and is reported rather than filtered.** `p60-after-r3`
returned 103.3 interactions/s against its siblings' 121.7 and 119.4, and its
objects/interaction is correspondingly the highest of its arm (131,046 vs
128,922 / 129,496). It still sits outside the before arm's range (max 100.1), so
the verdict does not rest on it — but the 974-element throughput ratio should be
read as "at least 1.04×, most likely ~1.20×", not as a tight 1.16×. The
allocation columns, which are the primary signal, are unaffected: that arm's
objects/interaction spread is 1.6% against an effect of 24%.

## Where it went — control, witness, and a negative control

Self-allocation in **absolute objects per interaction**, 974 elements, three
reps per arm. A share is the wrong unit for a control: an untouched pass's share
RISES when something else stops allocating.

| frame | before (3 reps) | after (3 reps) | Δ of means |
|---|---|---|---|
| `rt.AsListT[interface{}]` *(targeted)* | 5561.9 / 5761.5 / 6169.3 | **53.9 / 66.9 / 67.1** | **−98.9%** |
| `main.Std_Ui_markerFlags` *(targeted)* | 2782.9 / 2842.0 / 3143.9 | **absent** | **−100%** |
| `main.Std_Ui_markerFlags.func1` *(the closure)* | 2966.2 / 3161.0 / 3364.8 | **absent** | **−100%** |
| `rt.List_foldlElemFirstT` / `rt.List_anyT` *(the replacements)* | — | **0.0** | allocate nothing |
| `rt.AsListT[rt.SkyADT]` *(negative control)* | 4230.9 / 4253.1 / 4267.3 | 4266.4 / 4272.6 / 4289.0 | **+0.6%** |
| `rt.(*VNode).setAttr` *(control)* | 1934.4 / 1936.9 / 1956.5 | 1829.5 / 1976.6 / 2083.5 | **+1.1%** |
| `rt.HtmlToVNode` *(witness)* | 1478.7 / 1481.5 / 1506.5 | 1426.8 / 1519.8 / 1538.3 | **+0.4%** |

**The control is `rt.(*VNode).setAttr`, and it is deliberately NOT the frame
Stage 1 and Stage 2 used.** `rt.HtmlToVNode` is contaminated: on
`../stage2-typed-hof-20260816/p60-after-r1` it is 52.48% of the callers of
`rt.asList`, and `asList` sends 67.96% of its cumulative into
`reflect.Value.Interface` — the exact frame this stage's headline is defined on.
A control cannot sit inside the mechanism under test. It is kept above as a
*witness* and moves +0.4%.

`setAttr` drifts **+1.1%**, against a between-run spread of 1.1% in the before
arm and 13.9% in the after arm — comfortably inside the noise, and in the
opposite direction to the change.

**The negative control is the useful one, and it appeared rather than being
constructed.** `rt.AsListT[rt.SkyADT]` is a *different instantiation* of the very
helper the change targets: it must NOT fall, because those sites were never
provable. It moves +0.6% while its `[interface{}]` sibling falls 98.9%. That is
the strongest single line in the table — it separates "the routing predicate
fired where it should" from "the predicate fired everywhere", which no
host-level control can distinguish.

## The token census does NOT predict this, and that is the lesson

`xtask coerce-floor` counts **sites**, and the honest corpus-wide movement is
**narrow 6,679 → 6,324, −355, −5.3%** across the 22 projects that changed (none
rose). A −5.3% site count converted to a **−23% to −24% object count**.

The two are different units and the ratio is not a constant. `markerFlags` runs
**twice per rendered element**; one removed `rt.AsListT[any]` site removes n
boxes per element per render. Nothing in the census weights by execution
frequency — the gate's own header says so — so a site count is a lower bound on
nothing in particular. **Do not size a perf change from `coerce-floor`.**

The raw gate totals appear to say −23% narrow (9,024 → 6,925). **They do not.**
The golden carries **61** project lines and only **56** emitted in that run; the
five that did not (`03-tea-external`, `05-mux-server`, `08-notes-app`,
`11-fyne-stopwatch` — no generated FFI surface; `13-skyshop` — unfetched Sky
dependency) contribute their golden counts to the "before" total and nothing to
the "after". The −355 above is summed over projects present in **both**.

**`coerce_floor.golden` is deliberately NOT re-blessed by this work**, even
though the gate fails only because `adapter` is exact-match and this change
LOWERED it (35 → 17 raw; −7 across the two projects the gate names,
`35-composite-generics` 6→2 and `apps/fieldbook` 4→1). Blessing while five
projects cannot emit would write a 56-project golden over a 61-project one and
silently drop their coverage — the failure mode `docs/coverage/`'s ledger
discipline exists to prevent.

To produce an honest corpus-wide census, a session needs those five emitting
first. Four are missing a **generated Go FFI surface**
(`Github.Com.Google.Uuid` for `03-tea-external` and `08-notes-app`,
`Github.Com.Gorilla.Mux` for `05-mux-server`, `Fyne.Io.Fyne.V2.App` for
`11-fyne-stopwatch`) — AGENTS.md records that the large surfaces are
`.gitignore`d and regenerated on demand, so `sky install` in each project
directory regenerates them (needs a working Go toolchain, and network for the
module fetch). `13-skyshop` additionally needs the Sky dependency
`github.com/anzellai/sky-tailwind` fetched, which is the same `sky install`.
Then `xtask coerce-floor` should report 61/61 counted, and only then is
`--bless` safe. Expect the blessed `narrow` total to land near 8,669
(9,024 − 355) rather than 6,925.

> **Settled, 2026-08-16.** The five surfaces were restored and the gate
> reported **61/61 counted**, so the bless above was taken:
> `adapter` **35 → 28**, `narrow` **9,024 → 8,325**, total **9,059 → 8,353**,
> rows 61 → 61. The `adapter` −7 is exactly the two projects named above.
>
> The `narrow` prediction of ~8,669 came in at **8,325** because Stage 4
> landed between this run and the bless; 8,669 was the Stage-3-only estimate
> and there is no Stage-3-only number to compare it against any more.
>
> The refusal to bless at 56/61 was correct, and the gate has since been
> taught to enforce it rather than rely on a reader noticing: a golden row
> that cannot be measured is now a hard failure naming what to install
> (`SKY_LIVE_TESTS=skip` to opt out, loudly), `--bless` refuses under a
> shortfall, and both verdict lines carry the denominator. The five rows
> this document had to argue about by hand are now argued about by the gate.

## Scope: the fallback is real and is exercised

Corpus-wide, **60 of 70** provable-shaped call sites route to a twin (`foldl`
16/18, `any` 44/52). The erased defs are still genuinely CALLED in ten projects
— `07-todo-cli`, `08-notes-app`, `12-skyvote`, `13-skyshop`, `16-skychess`,
`17-skymon`, `18-job-queue`, `27-multi-session-chat`, `52-blog-analytics`,
`53-record-update-map`. Both refusal causes are `provable()` doing its documented
job: an anonymous-struct accumulator (the #166 class `lower_lambda` may still
re-pin) and an erased tuple element `rt.T2[any, any]`.

`19-skyforum` alone routes 10/10, which is why an app-level reading overstates
the corpus rate. Quote 60/70, not 10/10.

## What this run did NOT measure

1. **arm64, one host, one commit, one app.** Ratios should travel; absolute
   milliseconds should not — `../../skylive-remote-validation.md` found x86
   differs by ~30% on the memory figure.
2. **One interaction shape.** An upvote toggle re-runs `update` over the whole
   post list and re-renders the whole page.
3. **The `memory` session store**, so the gob/encode path is absent from these
   profiles by construction.
4. **Binary size. UNMEASURED.** Go stencils `List_foldlElemFirstT` /
   `List_anyT` per GC shape. Stage 2 left the same quantity unmeasured for a
   smaller change; this one instantiates over more shapes.
5. **No network term.** Loopback only.
6. **CPU self-time attribution**, which the rebaseline found unrepeatable on this
   host (±37%). Every number above is an allocation count or a closed-loop
   throughput, never a CPU share.

## Reproducing

`harness/` holds `control.sh` (the attribution above, with the control choice
argued in its header) and `mutate.sh` (the S1–S4 mutation matrix). `ab.sh` and
`forumrun.sh` are `../stage2-typed-hof-20260816/`'s and
`../forum-rebaseline-20260816/`'s unchanged.

**All three `allocs-*.pprof` per arm are committed here.** Stage 2's attribution
carried only `r1`, so its per-frame column had n = 1 and its quoted 6.4% / 6.6%
"spread between identical runs" cannot be recomputed from what it shipped. Every
frame column above is three runs, and the ranges are printed rather than the
means alone.
