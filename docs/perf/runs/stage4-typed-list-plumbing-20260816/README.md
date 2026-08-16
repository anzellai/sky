# Stage 4 — a provable `++`, and a provable length read, stop widening their lists

Stage 2 specialised the **kernel** list HOFs (`rt.List_mapT` over
`rt.List_mapAny`). Stage 3 specialised two **pure-Sky** list defs
(`rt.List_foldlElemFirstT`). This stage specialises an **operator**: a `++`
whose two operands carry the same statically-known Go slice type.

Like both predecessors, the typed runtime twin it re-points to —
`rt.List_appendT` (`rt.go:3681`) — had already been compiled into every Sky
binary and was **unreachable**: no caller anywhere in the repo.

Origin: doc 14 **R5** (§4.4, the `any`-based runtime-helper return, explicitly
NOT floor), surfacing as an R1 fall-through at the enclosing slot. Lever: doc 14
**§5.3**, whose text — *"Generalising this to kernel returns is the R5 lever"* —
was a proposal until this stage and is now two shipped instances. Floor check
(§1): both operand shapes are statically known, so §1 answers "typed entry
point", and nothing here touches R3/R4 (Go FFI), §4.2 (wire decode), §4.3 (TEA
dispatch) or §4.5 (stdlib ADT representation). **Not floor-touching.**

## The change, in one emitted line

`sky-stdlib/Std/Ui.sky:1937`, reached once per rendered element, emitted this
before:

```go
allAttrs_5 := /* FFI return */ rt.AsListT[Std_Ui_Attribute](
                  rt.Concat(any(v_2), any(v_3)))
```

and emits this after:

```go
allAttrs_5 := rt.List_appendT[Std_Ui_Attribute](v_2, v_3)
```

Per evaluation on lists of n and m, the first: boxes two slice headers, misses
`rt.Concat`'s `[]any` fast path because both operands are typed, calls
`rt.AsList` on **each** — a reflect walk boxing every element into a fresh
`[]any` — concatenates those into a third slice, and then reflect-narrows all
n+m elements **back** with `rt.AsListT[T]`. Five slices and ~2(n+m) element
boxes. The second is one `append` into one fresh slice.

`renderNodeAs` alone carried **five** such `++`
(`Std/Ui.sky:1937`, `:2015`, and the three-way chain at `:2018`).

Compiler site: `Ctx::lower_binop`'s `"++"` arm, `rust/crates/lower/src/lower.rs`.

## A runtime bug the change had to fix first

`rt.List_appendT` was one line — `return append(a, b...)` — and that is **not**
what `rt.Concat` does. `append` reuses `a`'s backing array whenever
`cap(a) > len(a)`, writing through into memory another Sky value may still hold;
`rt.Concat` has always returned a fresh slice. Sky lists are immutable values, so
the aliasing form would have made `ys ++ zs` mutate a list nobody appended to.

This is not hypothetical. One of the seven sites this change re-points in
`19-skyforum` is, in `Update_update`:

```go
rt.List_appendT[State_Comment_R](v_1.Comments, []State_Comment_R{…})
```

— the left operand is a **live Sky.Live model field**. Shipping the aliasing
form would have let posting a comment corrupt the previous model, a wrong-answer
bug visible only when that field happened to carry spare capacity, and one that
passes every corpus gate.

`runtime-go/rt/list_append_typed_test.go` pins it: the failing test was written
first, `TestListAppendT_doesNotAliasItsLeftOperand` reproduced the aliasing, and
`List_appendT` now always allocates.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Branch | `perf/stage4-rendernode-lists`, off `feat/embedded-postgres` @ `4234ca05` |
| App | `forumbench` — `examples/19-skyforum` plus the `init`-only view-size lever, byte-identical to `../stage3-generic-defs-20260816/`'s |
| Arms | the same app source, compiled by two `sky` binaries differing by exactly this change (`sky-before` md5 `a10851ab…`, `sky-after` md5 `02951418…`) |
| Interaction | signed-in upvote toggle — 2 patches, every press |
| Session store | `memory` |
| Load | `tools/skyliveload`, loopback, 25 sessions, closed loop (`-think 0`), 45 s window |
| `GOMAXPROCS` | **1** on the app |
| Run order | before, after × 3, **alternating** within each view size |
| Repeats | 3 everywhere; ranges reported |

Verified before measuring, by `grep` on the built artefact rather than by
assumption: the two `main.go` differ (`d25f392b…` vs `50827981…`), the two
`app-probe` binaries differ, and the after arm emits **7** typed-twin calls
against the before arm's **0**, with `rt.Concat` falling 8 → 1 and
`rt.AsListT[…]` 45 → 40 in the emitted Go.

All runs read `"patch_rate": 1` and `"valid": true`, and both arms render
**byte-identical page lengths** (109,615 at 94 elements; 288,697 at 974), which
is the cheapest available check that the change altered no output.

## The denominator is `prof_cpu_delta_s`, and Stage 2/3's was wrong

Stage 2 and Stage 3 derived objects-per-interaction as
`window objects / (throughput × prof_wall_s)`. `prof_wall_s` is `$(date +%s)`
differenced — **integer seconds**. On a 25 s window it lands on 25 or 26
depending only on where the two calls fell inside their seconds, and it did both
*within this run set*: `p5-before-r1` and `-r2` read 26, every other 94-element
run read 25. That is a **4% swing in the denominator applied per-run**, and it
manufactured an apparent outlier (`p5-before-r3` read 11,390 objs/interaction on
the wall-clock denominator and 11,309 on the CPU one, against siblings at
11,032–11,056).

`prof_cpu_delta_s` is the app's own CPU time over the same window, read to
0.01 s, and with `GOMAXPROCS=1` against a saturating closed-loop generator it
measures how much app work the window actually covers. It reads **25.17–25.21 s
across every run in this set** — which is also the cross-check that the two arms
were profiled over equal *work* rather than equal wall clock. Both denominators
are printed in `ab.tsv` so the artefact stays visible.

**Stage 2's and Stage 3's published per-interaction figures carry the same
artefact, and it is checkable in their committed data.** In each of their
94-element sets exactly one run — `p5-after-r2` in both — recorded `wall=26`
against its five siblings' `25`, which *deflated* that run's objects/interaction
by 4% and so flattered the after arm. Stage 3's 974-element set has it in the
other direction: `p60-after-r1` recorded 25 against the before arm's uniform 26,
*inflating* it. Every one of those runs recorded `prof_cpu_delta_s` between
24.51 and 26.34, so the work actually done was near-identical throughout.

This does not overturn either result — both headline effects were 18–24% against
a 4% artefact — but their quoted between-run spreads are inflated by it, and
per-run comparisons across arms should not be read closer than ±4%. It also is
**not** the explanation for Stage 3's flagged `p60-after-r3` outlier: that run
recorded `wall=26` / `cpu=25.39`, so moving to the CPU denominator makes its
objects/interaction *higher*, not lower. That outlier is real.

## The numbers

Per-run rows are in `ab.tsv`; every run's directory holds its own
`allocs-{pre,post}.pprof`, `load.json`, `memstats-*`, `env.txt` and
`profwindow.txt`.

### 94 elements

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 11,032 – 11,309 | **8,993 – 9,113** | **−18.7%** |
| kB / interaction | 502.0 – 512.8 | **437.9 – 440.8** | **−13.3%** |
| interactions / sec | 1028.8 – 1050.1 | **1129.2 – 1150.3** | **1.09×** |

No range overlaps its counterpart on any row.

### 974 elements

| | before (3 reps) | after (3 reps) | Δ |
|---|---|---|---|
| objects / interaction | 115,005 – 115,594 | **93,196 – 94,807** | **−18.4%** |
| kB / interaction | 5,304.9 – 5,370.2 | **4,650.2 – 4,732.8** | **−12.1%** |
| interactions / sec | 115.3 – 120.9 | **121.9 – 132.9** | **1.08×** |

No range overlaps its counterpart on any row, though throughput is close
(before max 120.9 against after min 121.9) and should be read as "at least
1.01×, most likely ~1.08×".

**The allocation effect is flat across a 10× change in view size — −18.7% at 94
elements and −18.4% at 974 — which is what says it is structural** rather than a
property of one page. That is the same test Stage 3 applied to its own result,
and the same test this run applied to Stage 3's shares before committing to the
work.

## Where it went — targeted, replacement, two negative controls, host control

Self-allocation in **absolute objects per interaction**, three reps per arm, at
BOTH view sizes. A share is the wrong unit for a control: an untouched pass's
share RISES when something else stops allocating.

### 974 elements

| frame | before (3 reps) | after (3 reps) | Δ of means | own spread (b / a) |
|---|---|---|---|---|
| `rt.Concat` *(targeted)* | 7673.0 / 7780.0 / 7842.2 | **256.2 / 272.2 / 275.2** | **−96.6%** | 2.2% / 7.4% |
| `rt.AsList` *(targeted)* | 3070.7 / 3070.9 / 3170.8 | **744.9 / 907.0 / 1025.9** | **−71.2%** | 3.3% / 37.7% |
| `rt.AsListT[SkyADT]` *(targeted)* | 4239.2 / 4433.4 / 4434.1 | **2096.9 / 2151.8 / 2153.7** | **−51.2%** | 4.6% / 2.7% |
| `main.Std_Ui_renderNodeAs.func1` *(targeted)* | 8518.7 / 8626.4 / 8690.3 | **1675.9 / 1877.5 / 1947.0** | **−78.7%** | 2.0% / 16.2% |
| `reflect.unsafe_New` *(headline)* | 18270.5 / 18275.3 / 18477.3 | **12921.4 / 13199.1 / 13296.9** | **−28.4%** | 1.1% / 2.9% |
| `rt.List_appendT[SkyADT]` *(replacement)* | — | **2739.9 / 2766.1 / 2819.5** | one fresh slice per call | — / 2.9% |
| `rt.List_cons` *(negative control — routing)* | 2912.7 / 3151.1 / 3199.3 | 3046.1 / 3130.5 / 3253.5 | **+1.8%** | 9.8% / 6.8% |
| `main.Std_Ui_button.func1` *(negative control — the refusal)* | 1024.9 / 1087.3 / 1142.8 | 1072.4 / 1089.0 / 1266.6 | **+5.3%** | 11.5% / 18.1% |
| `rt.(*VNode).setAttr` *(host control)* | 1880.6 / 1967.7 / 2015.5 | 1909.5 / 1935.2 / 1971.8 | **−0.8%** | 7.2% / 3.3% |
| `rt.HtmlToVNode` *(witness)* | 1437.4 / 1461.3 / 1488.6 | 1471.9 / 1513.5 / 1515.2 | **+2.6%** | 3.6% / 2.9% |

**Every targeted frame agrees between the two view sizes to within 2.5 pp**
(`Concat` −97.2 / −96.6, `AsList` −71.7 / −71.2, `AsListT` −50.8 / −51.2,
`renderNodeAs.func1` −81.0 / −78.7, `unsafe_New` −29.0 / −28.4), and every
control stays inside its own between-run spread at both. Two independent view
sizes reproducing the same per-frame decomposition is stronger evidence than
either set alone.

### 94 elements

| frame | before (3 reps) | after (3 reps) | Δ of means | own spread (b / a) |
|---|---|---|---|---|
| `rt.Concat` *(targeted)* | 716.5 / 729.0 / 730.2 | **16.9 / 19.5 / 25.0** | **−97.2%** | 1.9% / 47.9% |
| `rt.AsList` *(targeted — `Concat`'s reflect widen)* | 290.3 / 301.3 / 325.9 | **77.6 / 90.1 / 91.8** | **−71.7%** | 12.3% / 18.3% |
| `rt.AsListT[SkyADT]` *(targeted — the narrow back)* | 395.9 / 404.6 / 407.9 | **185.7 / 198.2 / 210.6** | **−50.8%** | 3.0% / 13.4% |
| `main.Std_Ui_renderNodeAs.func1` *(targeted — the inlined `any()` widens)* | 814.6 / 815.4 / 838.3 | **145.9 / 149.3 / 174.5** | **−81.0%** | 2.9% / 19.6% |
| `reflect.unsafe_New` *(the headline frame)* | 1701.4 / 1709.8 / 1727.9 | **1193.1 / 1221.8 / 1235.1** | **−29.0%** | 1.6% / 3.5% |
| `rt.List_appendT[SkyADT]` *(the replacement)* | — | **255.6 / 261.2 / 267.5** | one fresh slice per call | — / 4.7% |
| `rt.List_cons` *(negative control — routing)* | 276.6 / 280.2 / 299.7 | 281.8 / 301.8 / 303.6 | **+3.6%** | 8.4% / 7.7% |
| `main.Std_Ui_button.func1` *(negative control — the refusal)* | 80.5 / 95.3 / 95.6 | 84.5 / 91.7 / 105.6 | **+3.8%** | 18.8% / 25.0% |
| `rt.(*VNode).setAttr` *(host control)* | 181.6 / 183.8 / 187.6 | 179.2 / 182.6 / 183.2 | **−1.4%** | 3.3% / 2.2% |
| `rt.HtmlToVNode` *(witness)* | 138.2 / 141.6 / 142.1 | 137.3 / 137.8 / 138.7 | **−1.9%** | 2.8% / 1.0% |

Every control's drift is smaller than its own between-run spread. Every targeted
frame's fall is an order of magnitude larger than either. `rt.Concat`'s 47.9%
after-arm spread is not noise growing — it is three reps of a frame that now
allocates only 17–25 objects per interaction, where one absolute object of
sampling jitter is several percent.

**The replacement is not free, and Stage 3's was.** `rt.List_appendT` allocates
**261 objects per interaction** where `rt.List_foldlElemFirstT` allocated 0.0,
and it must: Sky lists are immutable, so `++` has to produce a new list. The win
is one slice per evaluation instead of five plus 2(n+m) element boxes.

**Stage 3's best control could not be reproduced, and this is why.** Stage 3's
strongest line was a *different instantiation* of the helper it targeted —
`rt.AsListT[rt.SkyADT]` had to hold still while `rt.AsListT[interface{}]` fell
98.9%. Here every list the change touches has element type `Std_Ui_Attribute` /
`Std_Html_Attributes_Attribute` / `Std_Html_Html` / `Std_Ui_Element`, and **all
four are `= rt.SkyADT`** in the emitted Go (`main.go:187`, `:211`, `:227`,
`:339`). One Go type, one stencil, no sibling instantiation left to hold still.

**Two negative controls replace it, and the second appeared rather than being
constructed.**

* `rt.List_cons` is `++`'s structural sibling: `::` emits the same
  `rt.X(any(a), any(b))` widen pair, returns the same `any`, is re-narrowed by
  the same `rt.AsListT[T]`, and runs in the **same function** at the **same
  per-element frequency** (`attrList_24`). It is deliberately not re-pointed. It
  moves **+3.6%** against a between-run spread of 8.4% (before) and 7.7%
  (after) — inside noise, and in the opposite direction to the change. Had the
  predicate keyed on operator shape rather than on proven operand types,
  `List_cons` would have fallen with `Concat`.
* **`rt.Concat` does not go to zero — it goes to 20.** The survivor is
  `main.Std_Ui_button.func1` (`main.go:707`), whose right operand is an
  `rt.List_cons(…)` typed `any`, so `provable()` refuses it. Its caller frame
  moves **+3.8%** against an 18.8% spread. A change that zeroed `rt.Concat`
  outright would mean the predicate had stopped checking its operands.

`setAttr` drifts **−1.4%** against between-run spreads of 3.3% / 2.2%;
`HtmlToVNode` **−1.9%** against 2.8% / 1.0%. `HtmlToVNode` is reported as a
*witness*, never a control — it is 51–54% of the callers of `rt.asList` (see the
falsifier below) and a control cannot sit inside the mechanism under test.

## The second change: unary list kernels that only read a length

`kernel_call` emits `rt.List_isEmptyT[T](xs)` / `rt.List_lengthT[T](xs)` over a
proven `[]T`, in place of `rt.AsBool(rt.List_isEmpty(any(xs)))` /
`rt.AsInt(rt.List_length(any(xs)))`. Same origin (R5), same lever (§5.3), same
"already compiled in and unreachable" condition.

**The Stage 4 brief's premise for this change was stale, and correcting it is
most of the result.** The brief described three `rt.AsListT[any]` wrappers around
`List.isEmpty` in `renderNodeAs`, taken from a `console_app/main.go` that was one
stdlib generation old. Those do not exist in the current emission. What is there
is `rt.AsBool(rt.List_isEmpty(any(nearbyChildren_10)))`, and `rt.List_isEmpty`'s
body calls the **unexported** `asList` — so the real mechanism is worse in
principle than the brief described (a full reflect walk over the list, boxing
every element, in order to read `len(items) == 0`) and much smaller in practice.

It is smaller because **`rt.List_isEmpty` does not appear in the Stage 3 profiles
at all.** Listing every node at `-nodefraction=0` (385 nodes) finds no
`rt.List_isEmpty` and puts `rt.List_length` at **0.011%**. The three call sites
in `renderNodeAs` run on `nearbyChildren`, `pseudoEntries` and
`animationEntries`, all of which are empty on almost every element, so the
reflect walk never has elements to box and the remaining cost is one slice-header
box per call — attributed to the *caller*, where pprof cannot isolate it.

### Why this one needed a benchmark as well as an A/B

Three `List.isEmpty` per rendered element against ~96 objects per element after
the `++` change puts the ceiling near the harness's own between-run spread on
objects/interaction. **A null from the A/B alone could not distinguish "no
effect" from "effect below resolution."** So the per-call cost is measured
exactly instead (`runtime-go/rt/list_twin_alloc_bench_test.go`, `-benchmem`,
2,000,000 iterations, typed `[]struct{K string; V int}` inputs because that is
what a well-typed Sky program hands these helpers):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| `List_isEmpty(any(xs))`, empty | 23.80 | 24 | **1** |
| `List_isEmptyT(xs)`, empty | **0.32** | **0** | **0** |
| `List_isEmpty(any(xs))`, n=8 | 249.70 | 344 | **10** |
| `List_isEmptyT(xs)`, n=8 | **0.32** | **0** | **0** |
| `List_length(any(xs))`, n=8 | 246.40 | 344 | **10** |
| `List_lengthT(xs)`, n=8 | **0.32** | **0** | **0** |

The same file measures the `++` pair, which reconciles the first change against
its call frequency: `AsListT[T](Concat(any(a), any(b)))` on 8+8 costs **23
allocs / 683 ns** against `List_appendT`'s **1 alloc / 91 ns**, and on the
empty-left-operand shape that three of `renderNodeAs`'s five `++` actually run,
**10 allocs / 253 ns** against **1 alloc / 34 ns**.

**One caveat the benchmark cannot settle, and the A/B can.** The benchmark
assigns to a package-level sink, which forces the boxed argument to escape. At
the real call site Go inlines `rt.List_isEmpty` — pprof labels it
`rt.List_length (inline)` — and an inlined callee with a statically known
concrete type may let escape analysis elide the `any(xs)` box entirely. If it
does, the erased form already costs 0 there and the whole-app effect is nil. The
benchmark bounds the per-call cost; only the A/B says what the app pays.

### Emission

10/10 sites convert in `19-skyforum` — 4 `isEmpty`, 6 `length` — and the 4
`rt.AsBool` and 6 `rt.AsInt` wrappers go with them. The full emission diff
against the `++`-only arm is 14 lines, all of them the intended transform.

### Whole-app effect — it resolves, and it is bigger than the profile suggested

Measured against the `++`-only arm (so this is B2 alone, not the pair), 3 reps
each, all 12 runs on a uniformly quiet host (`load1_at_start` 1.76–2.55):

| | `++` only (3 reps) | `++` + unary (3 reps) | Δ |
|---|---|---|---|
| **94 elements** | | | |
| objects / interaction | 8,936 – 9,085 | **8,710 – 8,817** | **−3.0%** |
| kB / interaction | 438.8 – 444.2 | **433.1 – 438.9** | −1.2% |
| interactions / sec | 1136.2 – 1147.5 | **1162.2 – 1169.3** | **1.02×** |
| **974 elements** | | | |
| objects / interaction | 92,515 – 94,620 | **90,039 – 90,164** | **−3.6%** |
| kB / interaction | 4644.0 – 4731.1 | **4563.6 – 4588.6** | **−2.3%** |
| interactions / sec | 132.6 – 134.5 | **137.1 – 137.5** | **1.03×** |

Objects and throughput are non-overlapping at both sizes. **The prediction from
the profile was wrong in the useful direction**: `rt.List_isEmpty` is invisible
to pprof, so the expectation was ~1%, and the measured effect is three to four
times that.

**Where it went, and it is exact.** Differencing every frame between the two
arms at 974 elements finds three that vanish completely:

| frame | `++` only | `++` + unary |
|---|---|---|
| `main.Std_Ui_renderNodeAs.func1.1` | 888.1 | **0** |
| `main.Std_Ui_renderNodeAs.func1.2` | 898.7 | **0** |
| `main.Std_Ui_renderNodeAs.func1.4` | 898.7 | **0** |
| `main.Std_Ui_renderElement.func1.…renderNodeAs.2.{1,2,4}` | 367.2 | **0** |
| `main.State_postScore` (a `List.length` site) | 136.6 | **0** |

Those first three **are** the three `List.isEmpty` sites — the anonymous IIFEs
`Std/Ui.sky:1952`, `:1973` and `:2005` lower to. They sum with the rest to
**3,189 objects/interaction against a measured whole-app drop of 3,333**, so
~96% of the effect is accounted for by name.

They go to *zero*, not down, and that is the mechanism: the erased body forced a
heap-boxed `any` argument, so Go kept each closure as a real function. Replacing
it with `len(xs) == 0` makes the closure trivially inlinable and the frame stops
existing. `main.Std_Ui_renderNodeAs.func1` correspondingly rises **+137.7**,
absorbing them.

Controls hold: `rt.List_cons` +67.7 on ~3,010, `rt.asList` −104.8 on ~3,087,
`rt.(*VNode).setAttr` unmoved — all inside their between-run spreads.

**So the benchmark's escape-analysis caveat resolved against the benchmark.** Go
did *not* elide the boxing at the real call sites; the erased form was paying it,
and the A/B is what proved it. The benchmark bounded the per-call cost correctly
and could not have told us the app paid it.

### Both changes together

| | baseline | `++` + unary | Δ |
|---|---|---|---|
| objects / interaction, 94 el | 11,032 – 11,309 | **8,710 – 8,817** | **−21.3%** |
| objects / interaction, 974 el | 115,005 – 115,594 | **90,039 – 90,164** | **−21.9%** |
| interactions / sec, 94 el | 1028.8 – 1050.1 | **1162.2 – 1169.3** | **1.12×** |
| interactions / sec, 974 el | 115.3 – 120.9 | **137.1 – 137.5** | **1.16×** |

## Binary size — MEASURED, and it is +1,952 bytes for the pair

Stage 2 and Stage 3 both instantiated new Go generics and both left binary size
**UNMEASURED**, with doc 14 §5.5 warning not to claim "no binary growth". Here it
is measured:

| | before | after | Δ |
|---|---|---|---|
| `sky-out/app` | 101,386,962 | 101,387,586 | **+624 B (+0.0006%)** |
| `sky-out/app-probe` | 100,974,866 | 100,975,538 | +672 B |
| `sky-out/main.go` | 138,269 | 138,168 | **−101 B** |

Go stencils by GC shape, and every element type in this app is `rt.SkyADT`, so
**one** `List_appendT` stencil serves all seven sites. That is the reason the
number is this small, and it is a property of this app's types, not a general
guarantee: an app concatenating lists of several distinct GC shapes would pay
one stencil each.

## The falsifier this run was required to settle first

The Stage 4 brief named a falsifier that had to be answered before any code was
written: the **4.10%** figure for the `rt.HtmlToVNode → rt.asList` edge assumes
that edge is dominated by decoding the **children** list rather than the
**attribute** list. pprof cannot separate them — one node, two source call sites
(`live.go:152` and `:155`).

Two exact counters settle it (`arm-counters`, an instrumented build discarded
afterwards; the instrumentation is not committed). At 974 elements, over 5.3 M
`HElement` decodes:

| | lists decoded | reflect arm | elements boxed | share of boxed |
|---|---|---|---|---|
| attributes (`live.go:152`) | 5,301,542 | **100%** | 7,277,508 | **46.8%** |
| children (`live.go:155`) | 5,301,542 | **100%** | 8,267,845 | **53.2%** |

**It splits near-evenly: children do NOT dominate.** The share held at 53.2%
across all ten 5-second samples, varying by 0.1 pp — this is a structural
constant, not a measurement. So the children half alone is ~2.2% of all objects,
roughly half what the estimate assumed, and the already-rejected "remove the
`Std.Html` intermediate" tactic is worth correspondingly less than its stated
~7.4% ceiling.

Two further facts fall out, both durable:

* **The `[]any` fast path in `asList` fires 0% of the time on this edge.** Every
  one of the 10.6 M decodes takes the reflect arm, because `HElement`'s fields
  hold typed slices boxed into `Fields []any`.
* Cross-calibration: the instrumented build reproduces the edge at **4.30%** of
  objects against Stage 3's 4.10% — so pprof's sampled `alloc_objects` and an
  exact counter agree to within run-to-run variation on this frame.

## Flatness of the Stage 3 shares — checked, and they hold

Also required before writing code: the Stage 4 brief's shares were quoted from
`p60-after-r1` alone. Recomputed across all six committed Stage 3 "after"
profiles:

| frame | 974 elements (r1/r2/r3) | 94 elements (r1/r2/r3) |
|---|---|---|
| `reflect.unsafe_New` flat | 15.85 / 15.83 / 15.92% | 15.66 / 15.30 / 15.29% |
| `rt.Concat` flat | 6.46 / 6.50 / 6.46% | 6.41 / 6.41 / 6.87% |
| `rt.Concat` cum | 12.94 / 12.96 / 13.05% | 12.90 / 13.07 / 13.46% |
| `rt.Concat` ← `renderNodeAs.func1` | 78.20 / — / 77.06% | 77.55% |
| `rt.AsList` ← `rt.Concat` | 67.27 / — / 67.16% | 68.08% |

Flat across three repeats **and** across a 10× change in view size. The target
was structural, which is what licensed the work.

## Verification

Sequential, each leg bounded through `scripts/lib/with-timeout.sh`, nothing
piped through `tail`.

| leg | verdict | what it asserted |
|---|---|---|
| `cargo test --workspace` | **PASS** 313 s | |
| `go test -race ./rt/...` (`CGO_ENABLED=1`) | **PASS** 97 s | |
| `xtask roundtrip` | **PASS** | 173/173 byte-exact, 0 error nodes |
| `xtask infer` | **PASS** | 55/55 examples clean, 0 type errors |
| `xtask resolve` | **PASS** | 0 class-(a) unresolved names |
| `xtask repro` | **PASS** 148 s | |
| `xtask build-run --golden --all` | **PASS** 344 s | **24/24** goldens matched |
| `scripts/example-sweep.sh` | **PASS** 185 s | 29 passed, 0 failed |
| `scripts/doc-examples.sh` | **PASS** | 14/14 doc examples compile |
| `19-skyforum` Playwright e2e | **PASS** | loads, clicks upvote, no console errors |
| `xtask coerce-floor` | **RED** | see below — **pre-existing** |

**Two of these were initially wrong, and the fault was mine rather than the
code's.** `example-sweep.sh` needs `sky-out/sky` and said so (exit 2 in 0 s —
correct gate hygiene). And `xtask build-run --golden` **selects a subset without
`--all`**: it reported `GOLDEN GATE: PASS (8 CLI example(s) matched)` while
noting that **16 committed goldens had no emitting example this run**
("env-tolerant, not gated"). A third of the corpus, passing. With `--all` it is
24/24. Anyone verifying a codegen change against the goldens should pass
`--all`; the default is not the gate you think you ran.

The e2e is thin — one page load, one upvote click, one 551-byte body — so it
confirms the app is not broken, not that it is correct in detail. The
`--golden --all` run is the load-bearing behavioural gate here.

### `coerce-floor` is red, and it was red before this work

The gate fails on its **exact-match `adapter` class**: `35-composite-generics`
6 → 2 and `apps/fieldbook` 4 → 1. The gate's own message calls this "a WIN"
that fails because an unrecorded win leaves slack a regression could hide in.

**Checked rather than assumed:** a worktree at the base commit `4234ca05`,
built and run through `coerce-floor`, produces the **identical failure** with the
identical two projects and numbers. This work did not cause it; Stage 3 left it
deliberately unblessed and it is still unblessed.

**The brief's warning about blessing is now out of date, and worth correcting.**
Five projects still do not emit here — `03-tea-external`, `05-mux-server`,
`08-notes-app`, `11-fyne-stopwatch` (no generated Go FFI surface) and
`13-skyshop` (unfetched Sky dependency). But `bless_golden` no longer drops
their rows: it **carries each forward at its last measured floor**, with a source
comment stating that dropping them "would silently retire their locked floor on
whichever machine happened to run the bless". The fake-improvement hazard is
closed in the gate itself. **Not blessing anyway** — the adapter win belongs to
whoever earned it, and rolling it into this stage's ratchet would mis-attribute
it.

### The narrow census moved, and it still does not predict the win

`narrow` fell in **every** project and rose in none. Per project, base → HEAD:

```
apps/fieldbook          -45   sky-bundled/console   -26   16-skychess     -23
26-ui-showcase          -21   37-composite-live-shop -21  38-composite-ui -21
17-skymon               -17   00-standard-libs      -15   apps/dispatch   -14
19-skyforum             -12   25-sky-console        -10   apps/ledger      -8
52-blog-analytics        -7   24-tui-kitchen-sink    -6   31-webview       -6
18-job-queue             -5   35-composite-generics  -5   12-skyvote       -4
55-store-partial-update  -4   07-todo-cli            -3   36-composite     -2
27-multi-session-chat    -1
```

**−276 across the 22 projects present in both runs** (−289 including five that
newly moved). Against a corpus `narrow` total near 6,600 that is roughly
**−4.4% of sites**, and it produced a **−21.9% object count**. Stage 3 saw −5.3%
sites → −23% objects and drew the lesson; this is a sharper instance of it.
**Do not size a perf change from `coerce-floor`.**

## Falsification — which mutations make which gate red

Each mutation asserts the pre-state literal is present, that the mutation landed,
and that it is gone after revert. `harness/mutate.sh`.

| # | mutation | gate | result |
|---|---|---|---|
| **S1** | `List_appendT` aliases its left operand again (`return append(a, b...)`) | `go test TestListAppendT_…` | **RED** — this is how the bug was found: the test was written first and failed against the original one-liner |
| **S4** | the twin is called with its operands **swapped** | `xtask build-run --golden --all` | **RED** — `55-store-partial-update` MISMATCH, *"compiles but computes a different answer"* |
| **S7** | `List_isEmptyT` returns `len(xs) != 0` | `go test TestListIsEmptyT_… / TestListUnaryT_…` | **RED** — 3 tests, including the nil-typed-slice case |
| **S3** | the `++` predicate stops requiring `provable(le)` | `xtask build-run --all` | **GREEN — and that is a finding, not a pass** |

**S3 is unfalsifiable on this corpus and the null is recorded.** Dropping
`provable(le)` changes the emitted Go **not at all**: `26-ui-showcase` emits
exactly 14 `rt.List_appendT[…]` with and without the guard, and the whole corpus
builds 55/55. The guard defends against `GoTy::TyVar` leaking into an
instantiation and against the D2 `lower_lambda` anonymous-struct re-pinning trap
— neither of which any project in the corpus exercises **in `++` position**. It
stays, because the failure it prevents is a miscompile and the cost of keeping it
is zero, but nothing in the repo tests it and this document should not pretend
otherwise.

Two more mutations (`S2` unequal element types, `S6` the unary `provable` guard)
are scripted and **were not run** for time. By S3's result they are likely to be
unfalsifiable in the same way; that is a prediction, not a measurement.

## What the gates would NOT have caught

* **`xtask coerce-floor` counts sites, not executions.** It cannot see the
  reflect walk *inside* `rt.Concat` at all — that costs ~2(n+m) allocations per
  evaluation and emits no token. It also cannot weight by frequency: the five
  `++` in `renderNodeAs` run once per rendered element. Do not size this change,
  or any perf change, from the census.
* **The aliasing bug is caught by exactly one thing, and it is the new test.**
  `append(a, b...)` returns the right *value*; it corrupts a *different* value,
  and only when the left operand carries spare capacity. Nothing in `infer` /
  `roundtrip` / `golden` / `example-sweep` constructs that condition.
* **`provable` on `++` is untested** — see S3 above. A guard against a miscompile
  class that the corpus does not contain is a guard no gate can defend.
* **Byte-identical page length is a weak check.** It would not catch a reordering
  that preserved length, which is why the `golden` gate (whole-program stdout)
  and the `19-skyforum` e2e are the load-bearing correctness gates here.
* **Nothing measures binary size in CI**, so the +624 B above is a one-off
  observation on one app, not a ratchet.

## What this run did NOT measure

1. **arm64, one host, one commit, one app.** Ratios should travel; absolute
   milliseconds should not.
2. **One interaction shape** — an upvote toggle that re-renders the whole page.
3. **The `memory` session store**, so the gob path is absent by construction.
4. **No network term.** Loopback only.
5. **CPU self-time attribution**, which the rebaseline found unrepeatable on this
   host (±37%). Every number above is an allocation count or a closed-loop
   throughput.
6. **Corpus-wide `++` conversion rate.** Quoted for `19-skyforum` only (7/8
   sites); the corpus figure needs the sweep.

## A note on host contention

The 974-element arm of this run was started while a **concurrent sibling
benchmark** on the same host went from idle to 446–590% CPU mid-window. Four
runs were affected (`load1_at_start` read 7.86 on the worst against 2.7–3.1 on
the clean ones); they are quarantined in `runs-void/` rather than deleted, and
the 974-element set was re-run on a quiet host. The 94-element set completed
before the contention began (`load1_at_start` 2.81–4.53) and is unaffected.

Recorded because it is the failure mode this host makes easy and it is invisible
in the result: a contaminated throughput number looks exactly like a real one.

## Reproducing

`harness/` holds `buildarm.sh` (the arm build — note that `cp app app-probe`
silently produces a binary with no pprof listener, which cost one run),
`ab.sh`, `control.sh` + `attrib.sh` (the attribution, with the control choice
argued in `control.sh`'s header), `summarise.sh` (the denominator, argued in its
header) and `mutate.sh` (the falsification matrix).
