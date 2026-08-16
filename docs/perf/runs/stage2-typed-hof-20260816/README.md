# Stage 2 — the erased list-helper round trip, removed where it is provable

`../forum-rebaseline-20260816/` measured what an interaction costs on an
application and found the single largest structural fact in the profile:
**89–91% of all allocation happens inside a `reflect.Value.Call`**, two thirds
of CPU samples have a reflective higher-order call on the stack, and **one
fifth of every object allocated is the erased list-helper round trip's own
bookkeeping**. Allocation was ~250 objects per rendered element with a fixed
term indistinguishable from zero: every object is allocated on behalf of an
element.

This run measures what removing that round trip — where the compiler can prove
it safe — is worth, on the same app, with the same harness, at the same two
view sizes.

## The change, in one emitted line

`src/View/Posts.sky:17`, the line 90.6% of the forum's home page hangs off,
emitted this before:

```go
rt.AsListT[Std_Ui_Element](rt.List_indexedMap(
    any(func(_p0 any, _p1 any) Std_Ui_Element { … }), any(v_1)))
```

and emits this after:

```go
rt.List_indexedMapT[State_Post_R, Std_Ui_Element](
    func(_e2 int, _e3 State_Post_R) Std_Ui_Element { … }, v_1)
```

Per call on a list of n, the first costs one box for the slice header, an
`asList` reflect walk that boxes **every element**, a `reflect.Value.Call` per
element (twice here — `indexedMap` applies the index first, so each element also
builds a curried closure), a `[]any` result, and an `AsListT` walk back. The
second is a Go `for` over a typed slice.

Compiler site: `Ctx::list_hof_typed` in `rust/crates/lower/src/lower.rs`,
reached from `kernel_call`. Architecture: doc 08 §6 category 6 ("polymorphic
kernel-fn arg") closing via the §7.4 lever, and explicitly **not** §8.3 floor —
§8.3 as rescoped at `50c8dcee` names these five helpers and says so.

18 of `forumbench`'s 19 erased list-HOF call sites are proven and specialised;
the 19th keeps the erased call because its result element type is `any`.
`rt.AsListT` sites fall 71 → 55.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Branch | `perf/stage2-typed-hof-loop`, off `feat/embedded-postgres` @ `40402294` |
| Go | 1.26.1 |
| App | `forumbench` — `examples/19-skyforum` plus the `init`-only view-size lever, byte-identical to `../forum-rebaseline-20260816/`'s |
| Arms | the **same app source**, compiled by two `sky` binaries differing by exactly one thing: `Ctx::list_hof_typed` and the two runtime helpers it needs |
| Interaction | signed-in upvote toggle — 2 patches, every press |
| Session store | `memory` |
| Load | `tools/skyliveload`, loopback, 25 sessions, closed loop (`-think 0`), 45 s window, 3 s ramp, 3 s warmup |
| `GOMAXPROCS` | **1** on the app |
| Run order | before, after × 3, **alternating** within each view size |
| Repeats | 3 everywhere; ranges reported |

Verified before measuring: the two app binaries differ (`cmp`), the after arm's
emitted Go carries 18 typed dispatch sites and the before arm carries 0, and the
after arm's `sky-out/rt/rt.go` contains the shipped helper bodies — by `grep`,
not assumption. An aliased `cp -i` silently declined to overwrite the compiler
binary once during this run and the only thing that caught it was grepping the
built artefact.

Every run asserts patch production as a **precondition** (a four-press
self-check, each press required to produce a patch naming a `sky-id` the client
holds) and again over the window. All 12 runs read `"patch_rate": 1`,
`"patches_naming_absent_ids": 0`, `"valid": true`.

## The numbers

Per-run rows are in `ab.tsv`; each run's directory holds its own `load.json`,
`memstats-*`, `env.txt` and `selfcheck.txt`.

### 94 elements (the stock `19-skyforum` home page)

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 22,474 – 22,488 | **16,356 – 16,436** | **−27.0%** |
| kB / interaction | 881.9 – 882.5 | **657.0 – 660.2** | **−25.3%** |
| interactions / sec | 568.6 – 579.1 | **877.0 – 898.4** | **1.55×** |
| CPU ms / interaction | 1.743 – 1.778 | **1.106 – 1.130** | **−36.1%** |

### 974 elements

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 238,971 – 239,595 | **170,307 – 171,141** | **−28.7%** |
| kB / interaction | 9,621 – 9,648 | **7,064 – 7,100** | **−26.5%** |
| interactions / sec | 56.9 – 59.1 | **96.9 – 100.3** | **1.71×** |
| CPU ms / interaction | 16.69 – 17.61 | **9.85 – 10.19** | **−42.2%** |

No range overlaps its counterpart on any row. The gain is larger at the larger
view size, which is what says it is structural rather than a small-view
artefact.

**Allocation is the primary signal and wall-clock is corroboration**, because
that is what this host supports: the archived rebaseline found CPU self-time
attribution unrepeatable here while allocation agreed to 0.2%. The
objects/interaction column reproduces to 0.06% within an arm; throughput
reproduces to 2.4%.

The measurement is also load-insensitive at `GOMAXPROCS=1`, which the run shows
rather than assumes: the machine's 1-minute load average fell from 11.51 to 3.57
across the 94-element sweep, and the three before-arm throughputs over that
range were 568.6, 579.1, 577.9 — a 1.8% spread across a 3× change in host load.

## Where it went — and the control

Self-allocation attributed by pprof over the profile window, in **absolute
objects per interaction** rather than as a share of a total the change moves. A
share is the wrong unit for a control: an untouched pass's *share* rises when
something else stops allocating, which reads as a regression it did not have.

| frame | 94 el before | 94 el after | Δ | 974 el before | 974 el after | Δ |
|---|---:|---:|---:|---:|---:|---:|
| `reflect.Value.call` | 1,728 | **303** | **−82.4%** | 18,262 | **3,434** | **−81.2%** |
| `rt.asList` | 792 | **263** | **−66.8%** | 8,053 | **2,809** | **−65.1%** |
| `rt.HtmlToVNode` *(control)* | 148.4 | 143.0 | −3.6% | 1,515 | 1,476 | −2.6% |

**`rt.HtmlToVNode` is the control**: the `Element` → `Html` → `VNode` tree
build, below the Sky boundary, which a change to how a list is traversed cannot
touch. It moves −3.6% and −2.6% — and its own spread between *identical* runs is
6.4% and 6.6%, so both figures sit inside the noise. That is what says the two
rows above it are the code and not the machine. (It is the same control Stage 1
used, where it moved −0.6%.)

`rt.asList` does not go to zero because the erased helpers are still reached
from the sites this cannot prove, and from `List.isEmpty` / `List.length`, whose
kernels take `x any` and are untouched here.

Two small frames drifted upward and are reported rather than filtered:
`rt.renderVNodeInto` 31.0 → 37.3 and `rt.assignSkyIDs` 96.6 → 94.9 objects per
interaction. Both are small-count sites where the sampling profiler's spread
between identical runs is 9% and 50% respectively; `assignSkyIDs` moved *more*
between two identical before-runs (74.5 → 112.0) than it did across the change.
Nothing is claimed about either.

## What this run did NOT measure

1. **arm64, one host, one commit, one app.** Ratios should travel; absolute
   milliseconds should not — `../../skylive-remote-validation.md` found x86
   differs by ~30% on the memory figure.
2. **One interaction shape.** An upvote toggle re-runs `update` over the whole
   post list and re-renders the whole page. An app whose cost is in `update`
   rather than `view` profiles differently.
3. **The `memory` session store**, so the gob/encode path is absent from these
   profiles by construction.
4. **Binary size.** The specialisation is Go's own generic instantiation of one
   runtime function, so the expected growth is Go's stenciling and nothing else
   — but it was not measured.
5. **No network term.** Loopback only.

## Reproducing

`harness/` holds all of it: `buildbench.sh` (build one arm from a named compiler
binary, and stamp what it built), `ab.sh` (the alternating sweep), `control.sh`
(the per-interaction attribution above), `mutate.sh` (the gate mutation matrix).
`forumrun.sh` is `../forum-rebaseline-20260816/harness/forumrun.sh` unchanged.
