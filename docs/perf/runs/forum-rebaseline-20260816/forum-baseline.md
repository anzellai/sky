# What an interaction costs on an application

Conditions, method and the corrected harness are in
[`README.md`](README.md). Every figure below is MEASURED unless the line says
INFERRED. Every configuration is three runs; ranges are given, not means
alone.

---

## 1. The headline: the fixed term is ~0.1 ms, not 2–3 ms

The single most consequential number in the programme was the fixed term. On
`26-ui-showcase` it was extrapolated from two points as **≈2.5 ms**, and a
2.5 ms floor against a 9–11 ms interaction caps any element-count lever at
about 4×. One of those two points was an invalid run.

Measured on skyforum at HEAD, GOMAXPROCS=1, seven view sizes from 30 to 1614
elements, three runs each (`g1.tsv`, `harness/fit.sh`):

```
ALL POINTS (n=21)          cost_ms = -0.147 + 0.01971 x elements   R2 = 0.9983
                           fixed term  -0.147 ms   95%CI -0.418 .. +0.123
                           per element  19.71 us

3 SMALLEST SIZES (30-94)   cost_ms = +0.124 + 0.01827 x elements   R2 = 0.9899
                           fixed term  +0.124 ms   95%CI +0.032 .. +0.216
                           per element  18.27 us

3 LARGEST SIZES (382-1614) cost_ms = -0.953 + 0.02035 x elements   R2 = 0.9971
```

**The fixed term is 0.12 ms and its confidence interval is 0.03–0.22 ms.**
That is the figure from the three *smallest* sizes, where the intercept is
interpolated rather than extrapolated, and it is the one to use. Across the
whole range the intercept is indistinguishable from zero.

The measurement that makes this hard to argue with is direct rather than
fitted: **a 30-element view costs 0.643–0.675 ms per interaction and serves
1,493–1,510 interactions/sec on one core.** Whatever is fixed about an
interaction has to fit inside 0.67 ms, and 0.55 ms of that is
element-proportional.

| elements | ms/interaction (3 runs) | interactions/sec, 1 core |
|---:|---|---|
| 30 | 0.643 / 0.668 / 0.675 | 1493 / 1508 / 1510 |
| 62 | 1.217 / 1.301 / 1.314 | 771 / 779 / 799 |
| **94** (stock skyforum) | **1.784 / 1.786 / 1.924** | **527 / 545 / 545** |
| 206 | 3.776 / 3.817 / 4.051 | 253 / 259 / 260 |
| 382 | 6.995 / 7.149 / 7.189 | 138 / 142 / 143 |
| 974 | 17.767 / 18.479 / 18.683 | 54 / 55 / 55 |
| 1614 | 31.702 / 31.866 / 32.913 | 31 / 31 / 32 |

No two adjacent size ranges overlap.

**The relation is mildly superlinear, and that is why the old extrapolation
failed.** Per-element cost rises from 18.3 µs over 30–94 elements to 20.4 µs
over 382–1614. Fit a straight line across the whole range and the curvature is
absorbed by the constant, which goes *negative*. Extrapolating a constant from
two points 94 and 384 elements apart — as the showcase figure did — puts the
intercept 94 elements outside the data, where exactly this curvature lives.

INFERRED, mechanism: the superlinearity is GC. Each of the 50 concurrent
sessions retains a `prevTree` proportional to the view, so live heap scales
with sessions × elements while allocation rate scales with elements; mark cost
per interaction therefore grows slightly faster than linearly.

### What this does to the ceiling

The ceiling a fixed term imposes is `total / fixed`. On the stock 94-element
skyforum that is **1.855 / 0.124 ≈ 15×**; at 382 elements ≈ 58×; at 974
≈ 150×. On showcase's published numbers it was ~4×.

**So: the ~4–6× ceiling does not transfer. It was an artefact of a fixed term
that is not there.** The floor on this app is the transport plus HTTP
plumbing, and that is under 0.2 ms — under 2% of the cost of rendering a
showcase-sized view.

This does *not* say a 25–50× improvement is available. It says the *fixed
term* does not prevent one. What prevents it is the 18–20 µs per element, and
§3 says where that goes.

---

## 2. Allocation is the cost, and it is purely per-element

Objects and bytes per interaction, from `MemStats` deltas across the load
window (`memstats-idle.json` → `memstats-loaded.json`), same 21 runs:

| elements | objects / interaction | kB / interaction |
|---:|---:|---:|
| 30 | 7,384 – 7,417 | 319 – 321 |
| 62 | 15,330 – 15,361 | 708 – 709 |
| **94** | **23,267 – 23,307** | **1,109 – 1,111** |
| 206 | 51,350 – 51,501 | 2,543 – 2,551 |
| 382 | 95,972 – 96,646 | 4,816 – 4,850 |
| 974 | 255,349 – 255,879 | 13,170 – 13,201 |
| 1614 | 440,768 – 442,125 | 20,926 – 22,952 |

Repeatability is 0.2% — an order of magnitude better than anything in the CPU
profile, which is why the allocation attribution carries the weight in §3.

Fitted the same way:

```
objects  = -34 + 248 x elements       (30-94 el)   R2 = 0.99999
         = -3858 + 273 x elements     (all)        R2 = 0.99937
bytes    = -51 kB + 12.3 kB x element (30-94 el)   R2 = 0.99991
```

**~250 allocations per rendered element, and a fixed allocation term
indistinguishable from zero** (−34 objects, CI −64 .. −3). Every object
allocated in an interaction is allocated on behalf of an element.

For scale: the archived minimal-Go control server allocates ~50 objects for a
whole interaction. Stock skyforum allocates 23,300.

---

## 3. Where the cost sits — the erased list-helper round trip

### The emitted shape

`src/View/Posts.sky:17` — the line 90.6% of the page hangs off — lowers to
(`sky-out/main.go:802`, reformatted):

```go
rt.AsListT[Std_Ui_Element](rt.List_indexedMap(
    any(func(_p0 any, _p1 any) Std_Ui_Element {
        return View_Posts_postRow(v_0, rt.AsInt(_p0), rt.Coerce[State_Post_R](_p1))
    }),
    any(v_1)))                       // v_1 is []State_Post_R — a TYPED slice
```

`rt.List_indexedMap` (`runtime-go/rt/rt.go:8629`) then does, per element:

```go
items := asList(list)                // []State_Post_R -> reflect arm ->
                                     // fresh []any, every element boxed
result := make([]any, len(items))
for i, item := range items {
    step := SkyCall(fn, i)           // arity 2, one arg -> PARTIAL application:
                                     // a fresh curried closure per element
    result[i] = SkyCall(step, item)  // reflect.Value.Call
}
return result                        // then AsListT walks []any back to
                                     // []Std_Ui_Element, asserting per element
```

`rt.SkyCall` (`rt.go:10565`) takes `reflect.ValueOf(f)`, and `skyCallDirect`
allocates a `[]reflect.Value` plus a `reflect.ValueOf` per argument before
`rv.Call`. The partial-application step is an *extra* per-element closure the
"~7n+2 allocations" sketch does not include.

The emitted Go for forumbench carries 19 erased list-helper calls
(11 `List_mapAny`, 5 `List_filterMap`, 2 `List_filterAny`,
1 `List_indexedMap`) and 71 `rt.AsListT` sites.

### Its share of allocation — MEASURED, and flat across a 10× view size

Self-allocation (each site's own objects, so the column sums without
double-counting), from the alloc profiles bracketing the CPU window:

| site | 94 elements | 974 elements |
|---|---:|---:|
| `reflect.Value.call` | 8.59% | 8.56% |
| `rt.asList` | 3.52% | 3.61% |
| `rt.AsListT[any]` | 2.90% | 2.68% |
| `rt.AsListT[SkyADT]` | 2.43% | 2.43% |
| `rt.List_mapAny` | 1.47% | 1.32% |
| `rt.List_cons` | 1.45% | 1.45% |
| `rt.List_filterMap` | 0.30% | 0.21% |
| `skyCallOne` (partial application) | 0.037% | 0.021% |
| `skyCallDirect` + `List_indexedMap` + other `AsListT[T]` | 0.09% | 0.05% |
| **erasure round trip, total** | **20.8%** | **20.3%** |

And cumulatively — the share of all allocation that happens *underneath* the
reflective dispatch:

| | 94 el | 974 el |
|---|---:|---:|
| under `reflect.Value.call` | 89.2% | 91.1% |
| under `rt.SkyCall` | 87.0% | 90.9% |
| under `rt.List_mapAny` | 78.2% | 81.1% |
| under `Std_Ui_layout` | 79.5% | 81.1% |

**One fifth of every object allocated is the erasure round trip's own
bookkeeping, and nine tenths of all allocation happens inside a
`reflect.Value.Call`.** The share is constant across a 10× change in view
size, so it is structural, not a small-view artefact.

### Its share of time — MEASURED

At 974 elements, where the profiler is stable on this host (§4), cumulative
CPU over three runs:

| frame | range over 3 runs |
|---|---|
| `reflect.Value.call` | **64.9 – 66.5%** |
| `rt.SkyCall` | 64.8 – 66.3% |
| `rt.List_mapAny` | 61.8 – 63.8% |
| `main.Std_Ui_layout` | 42.9 – 44.1% |
| `main.Main_view` | 42.6 – 44.0% |
| `liveApp.handleEvent` | 38.6 – 40.4% |
| `liveApp.safeViewCall` | 36.1 – 37.5% |
| `rt.List_filterMap` | 34.7 – 35.5% |
| `runtime.mallocgc` | 28.6 – 29.3% |
| `runtime.gcBgMarkWorker` | 10.1 – 11.1% |
| `Std_Ui_buildStyleStringWith` | 6.2 – 6.4% |
| `rt.asList` | 6.0 – 6.3% |
| `rt.HtmlToVNode` | 5.3 – 5.8% |
| `rt.renderVNode` | 5.3 – 5.7% |
| `rt.AsListT[any]` | 4.8 – 6.5% |
| `syscall.write` | 2.9 – 3.5% |
| `rt.List_indexedMap` | 2.4 – 2.9% |
| `rt.diffTrees` | 1.1 – 1.2% |
| `rt.applyStyleInjections` | ~0.9% |

These rows OVERLAP by construction — `handleEvent` contains `view`, `view`
contains `List_mapAny`, `List_mapAny` contains `SkyCall`. Read them as "this
share of samples had that frame on the stack", never as a partition.

**Two thirds of CPU samples have a reflective higher-order call on the
stack.** That is the single largest structural fact in this profile.

### The passes after `view`, and the one that is thrown away

| pass | CPU | allocations |
|---|---|---|
| `HtmlToVNode` — the `Element` → `Html` → `VNode` second tree build | 5.3 – 5.8% | 4.1 – 4.2% |
| `renderVNode` — the full-page HTML string | 5.3 – 5.7% | 2.9 – 3.1% |
| `applyStyleInjections` — the style walks | ~0.9% | below profile resolution |
| **`diffTrees` — the only output the interaction needs** | **1.1 – 1.2%** | **0.09 – 0.13%** |

The reply on the wire is 411–413 bytes and two patches. To produce it the
server rebuilds the whole `Element` tree through reflective dispatch, converts
it to `Html`, converts that to `VNode`, renders the entire page to a string,
and then diffs — and the diff is **1% of the cost**. The full-page HTML string
is built on every interaction and is not what is sent.

---

## 4. A measurement defect: CPU self-time attribution is unreliable here

Found by having three runs rather than one. It changes what may be claimed
from a profile on this host.

At 94 elements — 527–546 interactions/sec, so a very high syscall rate — the
three repeats put `syscall.rawsyscalln` at **42.7%, 88.7% and 87.0%** of
self-time, while the `runtime` GC bucket moves the opposite way (27.8%, 4.4%,
4.7%). Throughput across those same three runs agrees to 3.4%, allocations to
0.2%, total process CPU to 0.6%. **The work is identical; only its attribution
moves.** At 30 elements all three runs read 93.5–94.5% syscall, which cannot
be reconciled with a measured fixed term of 0.12 ms.

The instability tracks interaction rate. At 974 and 1614 elements — 31–55
interactions/sec — the syscall bucket is 2.6–5.1% and every bucket repeats to
within a percentage point:

| bucket (disjoint self-time, 974 elements) | r1 | r2 | r3 |
|---|---:|---:|---:|
| GC + allocator | 49.6% | 49.4% | 49.0% |
| reflect machinery | 23.3% | 23.9% | 24.5% |
| Sky runtime (`sky-app/rt`) | 6.1% | 7.0% | 6.6% |
| write syscall | 4.7% | 4.7% | 5.1% |
| scheduler + other | 3.8% | 4.0% | 4.0% |
| compiled Sky logic (`main.`) | 4.1% | 3.9% | 2.7% |
| map + hash | 3.4% | 3.2% | 3.8% |
| memmove | 3.7% | 3.3% | 3.6% |
| netpoll | 1.3% | 0.7% | 0.7% |

**Half the machine is the garbage collector and its allocator. A quarter is
reflection. Under 4% is the user's compiled Sky.**

Consequences, and they are binding:

* **No single-run self-time attribution on this host is quotable.** The
  archived showcase decomposition is one run per configuration, in the
  syscall-heavy regime.
* Everything in §3 comes from the 974-element runs, where three repeats agree,
  or from allocation profiles, which agree to 0.2% at every size.
* `harness/bucket.sh` is the classification rule; it reproduces the archived
  showcase r2 profile exactly (22.07 s total, GC 46.9%, compiled Sky 2.9%).

### Profiler overhead: not measurable

Unprofiled control (`PROFILE=0`, the plain `app` binary, `noprof-g1/`) against
the profiled runs, same sizes, three each:

| elements | unprofiled | profiled |
|---:|---|---|
| 94 | 525.9 / 541.4 / 542.5 /s | 527.3 / 544.7 / 545.5 /s |
| 974 | 53.6 / 54.2 / 54.3 /s | 53.8 / 54.5 / 54.6 /s |

Ranges overlap completely; the profiled arm is 0.5% *faster* at both sizes.
**No profiler overhead is claimed** — it is below the ±1.6% run-to-run spread.
(The archived showcase figure was 2.3%.)

---

## 5. GOMAXPROCS 1 and 8

Both are reported because a 1-core profile makes GC appear inline and distorts
the shape. Same sizes, same three repeats (`g8.tsv`):

| elements | int/sec, 1 core | int/sec, 8 cores | scale | ms/int, 1 core | ms/int, 8 cores |
|---:|---|---|---:|---|---|
| 30 | 1493 – 1510 | 4021 – 4264 | 2.7× | 0.64 – 0.68 | 1.18 – 1.20 |
| 62 | 771 – 799 | 2622 – 2753 | 3.4× | 1.22 – 1.31 | 1.99 – 2.17 |
| **94** | **527 – 545** | **1515 – 1998** | **3.3×** | 1.78 – 1.92 | 2.48 – 3.13 |
| 206 | 253 – 260 | 965 – 989 | 3.8× | 3.78 – 4.05 | 6.25 – 6.42 |
| 382 | 138 – 143 | 536 – 555 | 3.9× | 7.00 – 7.19 | 11.76 – 12.09 |
| 974 | 54 – 55 | 212 – 215 | 3.9× | 17.8 – 18.7 | 29.7 – 30.2 |
| 1614 | 31 – 32 | 130 – 132 | 4.1× | 31.7 – 32.9 | 49.7 – 52.5 |

Eight cores buy 2.7–4.1×, and per-interaction CPU rises 1.6–1.8× — the usual
multicore GC and scheduling tax, consistent with the archived showcase scaling
sweep (1.6× from 1 to 8 cores). The fixed term at GOMAXPROCS=8 is 0.43 ms
(95%CI 0.10–0.76) from the three smallest sizes: larger than the 1-core
figure, still far under 1 ms.

The one outlier in the whole matrix is `cpu-g8/p5-r3`, 1515/s against 1956 and
1998. It is left in.

---

## 6. Comparison with `26-ui-showcase` at the same commit

`showcase-g1/`, `showcase-g8/` — same harness, same validity gates, same
commit, so this is the first like-for-like comparison of the two apps.
Figures are in the run directories and summarised at the end of this file.

---

## 7. Defects found, not fixed

Reported per the brief; no fix attempted.

1. **`SKY_LIVE_STORE_PATH` silently ignores a `postgres://` URL.**
   `docs/skylive/overview.md:123` documents
   `[live] store = "postgres", storePath = "postgres://..."`. With
   `SKY_LIVE_STORE_PATH=postgres://skyperf@127.0.0.1:55433/skylive?sslmode=disable`
   the app logs five connect attempts against `user=anzel database=` on
   `/private/tmp/.s.PGSQL.5432` — pgx's rendering of an **empty** connection
   string — then falls back to memory. The same cluster reached with the libpq
   keyword form, `host=127.0.0.1 port=55433 user=skyperf dbname=skylive
   sslmode=disable`, connects on the first attempt and logs
   `session store: postgres`. `DATABASE_URL` in URL form is dropped the same
   way.

2. **The dev fallback is easy to measure straight past.** The URL failure
   above ends in `DEV fallback → in-memory sessions` and the app then serves
   normally — so a load run against it yields a complete set of valid,
   patch-bearing, entirely mislabelled results. It is documented behaviour
   (`ENV` set makes it a hard failure) and right for a developer; it is a trap
   for a benchmark, which is why the memory runs record the store banner from
   `app.log`.

3. **`skyliveload`'s handler choice was the corpus defect**, described in
   `README.md`. Recorded here so the defects live in one list.
