# What a Sky.Live interaction actually costs

Sizing guidance quoted **"a complex Sky.Live view costs 2–10 ms per
interaction"**. That was an inference, never a measurement, and every
capacity number downstream of it inherited the guess. This document
records the measurement that replaces it, the harness that produced it,
and — just as importantly — what the measurement does not cover.

> **Rule for quoting anything here:** no number in this document may be
> repeated without the conditions attached to it. Every run writes an
> `env.txt` recording host, commit SHA, load average and container
> flags, precisely so a figure cannot travel without them.

## How to reproduce

```bash
# Phase 1 — the microbenchmark (no container, no network, no database)
scripts/skylive-bench.sh                       # 5 runs, refuses on a busy machine
COUNT=15 BENCHTIME=400ms scripts/skylive-bench.sh

# Phase 2 — the load harness, speaking the real protocol
scripts/skylive-load.sh --app examples/26-ui-showcase
CONCURRENCY="100 500 1000" DURATION=60s scripts/skylive-load.sh

# Phase 3 — constrained runs (read the caveats in the script header)
scripts/skylive-load-constrained.sh --profiles "1x2g 2x2g 1x1g"

# Phase 4 — a real x86 GCP target (see skylive-remote-validation.md)
scripts/skylive-observe-remote.sh --project <id> --instance sky-lang-org
scripts/skylive-load-remote.sh --url http://<bench-ip>:8000   # preflight

# Phase 5 — embedded PostgreSQL on that target. The app is ONE binary run in
# three configurations; the scripts that drove it are archived beside the data.
#   A: ./app                                     memory sessions, no database
#   B: ./app --embed --data-dir /var/lib/<app>   memory sessions, PG idle
#   C: B + SKY_LIVE_STORE=postgres               sessions written to PG
# with SKY_POSTGRES_BIN=/usr/lib/postgresql/15/bin on a Debian target.
ls docs/perf/runs/gcp-embed-postgres-20260815/{sweep,counterbalance}.sh

# Phase 6 — attribution: WHERE the CPU and the memory go. Needs only Go;
# the generated sky-out/ tree builds standalone, so no Rust compiler and
# no change to runtime-go/rt/. Harness + instrumentation are archived
# beside the data.
ls docs/perf/runs/attribution-20260815/harness/
```

Phase 4 needs `gcloud` authenticated against the target's project, which
is always passed explicitly — never taken from `gcloud`'s active config.
Phase 1 needs only Go. Phase 2's browser observer needs the repo's
existing Playwright (`npm install` at the repo root, as
`scripts/verify-examples.sh` expects); run `scripts/skylive-load.sh
--no-observer` to skip it. Phase 3 needs Apple's `container` CLI.

## Every measurement in this work, in one table

This is the sizing answer. Each row is a machine someone might actually
buy, with the conditions that make its numbers mean something; the rest of
this document and
[`skylive-remote-validation.md`](skylive-remote-validation.md) are the
derivations.

| # | Machine · arch | Database | Per session | Knee | Peak (burst) | Sustained | Idle floor | Ops Agent | Commit |
|---|---|---|---|---|---|---|---|---|---|
| 1 | **local container**, 1 CPU quota · **ARM64** | none | 1,047 kB | 100–500 | 88–92/s | 88/s | 34.2 MB app | n/a | `85ded8ef` |
| 2 | **e2-micro** (970 MB, 2 shared vCPU, 0.25 base) · x86 | SQLite | **1,379 kB** | **25–50** | ~18/s | ~9.5/s | 22.7 MB app | absent | `ba3c3b1d` |
| 3 | **e2-small** (1.98 GB, 2 shared vCPU, 0.5 base) · x86 | SQLite | **1,450 kB** | **50–100** | 35–42/s | ~26/s | 22.0 MB app | absent | `ba3c3b1d` |
| 4 | **e2-small** · x86 — *control for rows 5–6* | SQLite | **1,338 kB** | **25–50** | 41.0/s | ~21/s | 21.2 MB app | absent | `8e166eaf` |
| 5 | **e2-small** · x86 | **embedded PostgreSQL**, `memory` sessions | **1,395 kB** | **25–50** | not isolated | ~21/s | 21.2 + **21.9 MB** | absent | `8e166eaf` |
| 6 | **e2-small** · x86 | **embedded PostgreSQL**, `postgres` sessions | **1,764 kB** | **25–50** | 39.9/s | ~19/s | 21.2 + **28.4 MB** | absent | `8e166eaf` |
| 7 | **e2-micro**, sky-lang.org · x86 — *production reference, idle* | SQLite | not measurable | — | — | — | **56.1 MB** app | **present, 86 MB** | live |
| 8 | **e2-small** · x86 — *`19-skyforum`, 94 elements* | **embedded PostgreSQL**, `postgres` sessions | **625–650 kB** (marginal slope, n=100→500) | **100–300** (failure knee) | 183.5/s (rested first run) | **64.3/s** at n=300 | — | absent | `3ed83c08` |
| 9 | **e2-medium** · x86 — *`19-skyforum`, 94 elements* | **embedded PostgreSQL**, `postgres` sessions | — | >500 | — | **261.5/s** at n=300 | — | absent | `3ed83c08` |

> **Rows 1–7 are `examples/26-ui-showcase` (384 elements); rows 8–9 are
> `examples/19-skyforum` (94 elements), several optimisation stages later**
> (`runs/gcp-x86-capacity-20260816/`). Figures do not transfer across view
> sizes or commits — and rows 8–9's "Per session" is a **marginal slope
> across levels**, not RSS ÷ n, which is why it is far below rows 2–6 (see
> the note below).

> **Read the "Per session" column with
> ["The attribution"](#the-attribution--what-the-11-ms-and-the-14-mb-actually-are)
> beside it.** Every figure in that column is an RSS slope measured under
> load. RSS is a high-water mark, and the slope is dominated by allocator
> headroom that does not belong to any session: on the same build it reads
> 2,118 kB/session at N=25 and 1,367 kB/session at N=100. The **retained**
> cost, measured idle after a forced GC, is **336 kB/session and flat** —
> but that is idle retention, **not** the marginal cost of a loaded session
> either. The sizing input is the **marginal slope under load across
> levels** (rows 8–9's method): 625–650 kB/session on the PostgreSQL store,
> 451–531 kB on the memory store (`runs/gcp-x86-capacity-20260816/`).
> RSS ÷ n charges the fixed base to the sessions; idle post-GC live heap
> omits what a loaded session pins. The RSS columns remain the right input
> for "how much RSS will this instance show at this concurrency".

**Row 1 is kept only as evidence for why on-target measurement is
required.** Apple's `container` rejects fractional `--cpus`, so its 1-CPU
profile is *twice* an e2-small's entitlement and has no burst-credit model
at all. It was **optimistic by ~2.5× on e2-small and ~5× on e2-micro**, and
it put the knee 4–10× too late. Its per-session figure is the only part
that came close, and it was still 30% light. Do not size from row 1.

**Rows 3 and 4 are the same machine type and the same application at two
commits**, and they agree to 8% on per-session memory (1,450 vs 1,338 kB)
and to within measurement noise on unsaturated throughput (21.5 vs
21.4/s at n=25, p50 142 vs 141 ms). That agreement is what licenses
comparing rows 5 and 6 — measured months apart from row 3 — against
the SQLite baseline at all.

### Which resource binds, and where

| Instance | Memory ceiling | **Usable** ceiling | Binds on |
|---|---|---|---|
| e2-micro, SQLite | **~450 sessions — reached, not derived**: asked for 500, established **447** (`runs/gcp-x86-20260815/micro-noagent.tsv`, n=500 row) with `MemAvailable` down to **~43 MB** (`micro-rss-n500-r1-memexhaustion.txt`, final samples) | **~25–50** | **CPU — ≈9–18× before memory** (450 ÷ 25–50) |
| e2-small, SQLite | **never reached** — **500 of 500** established in all three repeats (`runs/gcp-x86-20260815/small-noagent.tsv`), memory nowhere near binding | **~50** | **CPU** |
| e2-small, embedded PostgreSQL | **never reached** — see below | **~50** | **CPU** |

> **Two memory ceilings were deleted from this table rather than
> recalculated: "~1,300 sessions (1.98 GB, ~1.4 MB each)" for e2-small on
> SQLite, and "~1,100 sessions" for e2-small on embedded PostgreSQL.** Both
> were `available RAM ÷ per-session slope`, and the slope was the
> 1.35–1.42 MB RSS/n figure this document itself retracts below ("Where the
> 1.4 MB goes — and why that figure is not a per-session cost"). They cannot
> be re-derived from the replacement slope either: 625–650 kB was measured
> on `19-skyforum` at a 94-element view (`runs/gcp-x86-capacity-20260816/`)
> and these rows are `26-ui-showcase` at 384, so dividing by it would swap
> one app's cost into another app's budget — the exact error the retraction
> is about. A number with no run behind it is deleted, not adjusted. What
> would establish these rows is what established the e2-micro one: run the
> instance out of memory and record where it stops.

On every x86 instance measured, **CPU binds an order of magnitude before
memory does.** On the e2-micro — the only machine whose memory ceiling was
actually reached — sizing from RAM overstates capacity by **≈9–18×** (its
~450-session ceiling against a 25–50-session CPU knee; a ratio of two
observations, not a directly measured quantity). Memory sets the hard
ceiling; latency sets the useful one, and the
useful one arrives first by a wide margin. (This table derives from rows
2–6's commits; the memory-ceiling figures use those runs' RSS slopes. The
later `19-skyforum` measurement — rows 8–9 — reproduces the conclusion with
a sustained 64.3 int/s at 300 sessions on e2-small while memory was nowhere
near binding; `runs/gcp-x86-capacity-20260816/`.)

### What embedded PostgreSQL costs

| | At the floor (idle) | Per session | At 100 sessions, system-wide |
|---|---|---|---|
| SQLite / no database (row 4) | — | 1,338 kB | 158 MB consumed |
| **+ embedded PostgreSQL, `memory` sessions** (row 5) | **+21.9 MB** | +57 kB (1,395) | 161 MB — **+4 MB** |
| **+ embedded PostgreSQL, `postgres` sessions** (row 6) | **+28.4 MB** | +426 kB (1,764) | 244 MB — **+86 MB** |

So embedding the database costs **~22 MB and essentially nothing per
session** while the session store stays in memory, and **~28 MB plus
~426 kB/session** once sessions are actually written through it. On a
2 GB instance whose usable ceiling is ~50 sessions, both are affordable:
the worst case is ~28 MB of floor plus ~21 MB of session overhead at the
knee, against 1.5 GB free.

**PostgreSQL's own memory does not grow with sessions.** Regressed against
established sessions, the postgres process tree's RSS slope is −10 kB
(config B) and +22 kB (config C) per session — zero within noise, because
the pool holds a flat **6 connections** no matter how many sessions exist
(`dbSharedAuxPoolSizeFor(2) = 6`). `pg_backends_max` reads **7** — the pool
plus the 1-Hz sampler's own psql — in all config-C rows at 50 and 100
sessions of `runs/gcp-embed-postgres-20260815/sweep.tsv`; at n=25 one row
reads 7 and two read 0 (a mid-sweep sampler bug, README:78-82).
`runs/gcp-x86-capacity-20260816/README.md:49-53` reads "7 (occasionally 8)"
at 100 / 300 / 500. (**This said 6** — correct for the pool, but it misquoted
`pg_backends_max` as 6 when the column reads 7.) Embedded
PostgreSQL is a **fixed block**, not a per-session tax; the +426 kB/session
in row 6 is paid in the *app*, not the database.

## The answer

**The runtime's share of a Sky.Live interaction costs about 128 ns per
VNode**, for the ordinary case of a text or attribute change:

```
cost ≈ 0.4 µs + 128 ns × nodes          (text / attribute change)
cost ≈          370 ns × nodes          (child-count change: subtree re-render)
```

Applied to the two reference apps:

| View | VNodes | Text/attr change | Row added to a list |
|---|---|---|---|
| `19-skyforum` (94 elements) | 159 | **21 µs** | 59 µs |
| `26-ui-showcase` (384 elements) | 670 | **86 µs** | 244 µs |

**The "2–10 ms per interaction" figure was pessimistic by roughly 25–100×
for the path it named.** The heaviest view in the repo costs 0.086 ms of
diff, render-id assignment and JSON encoding — not 2–10 ms.

Measured as the minimum of 15 repetitions (see *Conditions* below);
linearity holds to within 2% across a 370× range of node counts, from 19
to 7,012 nodes.

Where that time goes, at 670 nodes:

| Component | Cost | Share |
|---|---|---|
| `diffTrees` | 66.7 µs | 78% |
| `assignSkyIDs` | 21.6 µs | 25% |
| JSON encode (1 patch) | negligible | — |

### But end-to-end interactions really are milliseconds

This is the part that matters for sizing, and it is why the original
figure was not simply wrong. Measured against the same app with the
Phase 2 harness:

| Measurement | 1 session | 100 sessions |
|---|---|---|
| Server-side diff path (Phase 1) | 0.086 ms | 0.086 ms |
| POST round-trip, Go client (Phase 2) | ~12 ms | 8.1 ms |
| Click-to-DOM-updated, real browser | 27 ms | ~24 ms |

So an interaction *does* cost milliseconds end to end — but **the
render/diff path is about 1% of it**. The remaining ~99% is HTTP
handling, session locking, SSE bookkeeping, the user's compiled-Sky
`update`/`view`, the network, and client-side patch application.

The practical consequence: **sizing CPU capacity on the diff cost
over-provisions massively, and optimising the differ would buy almost
nothing.** At the saturation throughput measured below (~430
interactions/sec), the entire diff path accounts for under 4% of one
core.

### So was "2–10 ms" right or wrong? Both, and the distinction matters

Reconciling Phase 1 against the saturation throughput measured in Phase
3 settles this.

One CPU saturates at **88–92 interactions/sec** (1-CPU container, 500
sessions). That is **~11 ms of server CPU per interaction** — squarely
in the 2–10 ms range the guidance quoted, if slightly above it.

So:

- **As a figure for total per-interaction server cost, "2–10 ms" is
  about right** — very slightly optimistic. Capacity numbers derived
  from it are broadly sound.
- **As a claim about the view render and diff, it is wrong by ~100×.**
  That path costs 0.086 ms on the heaviest view in the repo: under 1%
  of the 11 ms.

> **The paragraph that stood here was wrong, and the correction is the
> most useful thing on this page.** It read: per-interaction cost is
> "roughly constant in view size"; a 4× heavier view "adds 65 µs to an
> 11 ms interaction, or 0.6%"; a team worried their view is too complex
> to scale "is worrying about the wrong variable."
>
> That generalised a measurement of the **diff** to the whole
> interaction. The diff is indeed near-constant. The interaction is
> **dominated by re-running `view(model)`**, which is proportional to
> the view. View complexity is very much the right variable to worry
> about. See "The attribution" below for why.
>
> **The two-point table that stood here was itself invalid, and is
> withdrawn.** It read `19-skyforum` at 94 elements / 4.41–4.77 ms /
> 249–267 per sec against `26-ui-showcase` at 384 / 10.68–10.93 ms /
> 108–110, and concluded "4.1× the elements costs 2.4× the interaction".
> All three forum runs behind it are flagged
> `"valid": false — no interaction produced a single patch`
> ([`runs/attribution-20260815/viewsize/`](runs/attribution-20260815/viewsize/)):
> the load generator had picked the site-title link, whose `Navigate
> HomePage` is a no-op on the home page, so that arm never ran the
> render/diff path at all.
>
> Replaced by a seven-point regression on runs that provably produced
> patches — [`runs/forum-rebaseline-20260816/`](runs/forum-rebaseline-20260816/).
> The direction was right and the magnitude was not:
>
> ```
> cost_ms = 0.124 + 0.0183 x elements      (30-94 elements, R2 = 0.99)
> ```
>
> **The fixed term is 0.12 ms, not the ~2.5 ms this table's two points
> extrapolate to** — a 30-element view costs 0.64–0.68 ms and serves
> ~1,500 interactions/sec on one core. Cost tracks element count almost
> exactly, with a floor near zero.

## The attribution — what the 11 ms and the 1.4 MB actually are

The sections above measured *how much*. Neither said *what*. This
section names the functions and the retentions, against a control that
makes the numbers mean something. Raw data + harness:
[`runs/attribution-20260815/`](runs/attribution-20260815/).

Conditions: Apple M1 (**arm64**), 8 cores, Go 1.26.1, commit `4f3da18e`,
`examples/26-ui-showcase`, `memory` session store, `GOMAXPROCS=1` to
model one core, closed-loop load at 50 sessions, load average 3.1–5.6 on
a shared machine. Three repeats everywhere; ranges are shown, not means
alone. Profiling cost **2.3% of throughput** (106.5/s profiled vs
109.0/s unprofiled), so the profiled breakdown describes very nearly the
unprofiled system.

### The control: what this costs in Go with none of the machinery

A minimal Go SSE server — holds a connection, keeps a per-session model,
answers a POST with a small JSON patch — speaking enough of the wire
protocol that **the same load generator drives it unmodified**. It is a
floor, not a fair-featured rival: no VDOM, no diff, no reflective
dispatch, no session store.

| | Sky.Live | control | ratio |
|---|---|---|---|
| **Server CPU per interaction** | **9.15 ms** (8.69 / 9.28 / 9.47) | **0.021 ms** (0.018 / 0.019 / 0.024) | **~450×** (bounds 360–530) |
| **Live heap retained per session** | **336 kB** (336.2–336.8) | **26.2 kB** | **12.8×** |
| Goroutine stacks per session | 35.5 kB (4 goroutines) | 21.4 kB (3) | 1.7× |
| **Allocated per interaction** | **5,658 kB** | **3.13 kB** | **~1,800×** |
| **Allocations per interaction** | **133,628** | (not recorded) | — |

The control's *throughput* is not quoted as a server capability: driving
it consumed 28–53% of the 8-core host, so those runs are
generator-bound. Server-side CPU per interaction is unaffected by that
and is the honest comparison.

### Throughput scales with cores — there is no global lock

The first thing to rule out, because it would decouple per-interaction
cost from capacity:

| GOMAXPROCS | Throughput (3 repeats) | CPU per interaction |
|---|---|---|
| 1 | 108.6 / 108.1 / 110.4 /s | 10.93 / 10.68 / 10.81 ms |
| 2 | 204.1 / 210.7 / 214.9 /s | 11.09 / 10.96 / 10.79 ms |
| 4 | 389.6 / 374.7 / 379.6 /s | 11.71 / 12.10 / 11.98 ms |
| 8 | 445.7 / 433.7 / 468.0 /s | 17.04 / 16.97 / 16.07 ms |

Throughput scales cleanly to 4 cores at flat per-interaction cost. **No
lock serialises interactions.** The ceiling is the work itself. (The
per-interaction figures in this table divide whole-run CPU — including
ramp and startup — by measurement-window interactions, so they run ~15%
high; the 9.15 ms above is the steady-state-window measurement. Both
bracket the original ~11 ms estimate, which was sound.)

### Where the 9.15 ms goes

Shares are of total server CPU, mean of three runs, converted at
9.15 ms/interaction. `handleEvent` is the interaction handler.

| Component | Share of CPU | ms/interaction | |
|---|---|---|---|
| **`handleEvent`** — the whole interaction | **61.7%** | **5.65** | |
| ↳ **`view(model)` re-render** | **51.9%** | **4.75** | **84% of the handler** |
| ↳ `Std.Ui.buildStyleString` | 11.9% | 1.08 | |
| ↳ `Std.Ui.layoutContextFor` | 7.5% | 0.68 | |
| ↳ *(`Std.Ui.hasMarker`, inside both)* | *13.5%* | *1.24* | |
| ↳ `HtmlToVNode` | 5.8% | 0.53 | |
| ↳ `renderVNode` | 4.8% | 0.44 | |
| ↳ `applyStyleInjections` | 2.4% | 0.22 | |
| ↳ **`diffTrees`** | **1.3%** | **0.12** | |
| Everything else (SSE, HTTP, netpoll, GC bg) | 38.3% | 3.50 | |
| *of which GC (background + assist)* | *12.8%* | *1.17* | *assist runs inside the handler, so it overlaps the rows above* |

**`diffTrees` costs 0.12 ms** — independently consistent with the 86 µs
the Phase 1 microbenchmark measured for the same path, from a completely
different method. That agreement is the strongest validity signal in
this document, and it confirms the earlier conclusion that optimising
the differ buys nothing.

Every other hypothesis on the table was **refuted by the profile**, and
they are listed because each was plausible and each was wrong:

| Suspect | Measured share | Verdict |
|---|---|---|
| gob-encoding the Model per interaction | **absent** (0%) | Not on the `memory`-store path at all. **The "~10%" this row used to quote is withdrawn** — see ["The ~10% store bound is withdrawn"](#the-10-store-bound-is-withdrawn) |
| `hashAny` — two full reflect+SHA-256 Model walks per dispatch, to decide whether to log | **absent** (0%) | Real code, cheap here because this app's Model is small |
| Session store `Set` | **absent** (0%) | |
| JSON encode/decode of the wire envelope | **absent** (0%) | |
| `msgDisplayName` reflection | **absent** (0%) | |
| OTel Msg span per interaction | 0.75% | Real, negligible |
| Session locking | — | Refuted by core scaling |

Self-time by layer tells the same story from another angle: **Go runtime
and GC 42–46%**, reflection machinery 11–12%, Sky runtime 3–4%, and the
compiled Sky logic itself **~2%**. Almost nothing is computing; the
machine is allocating.

### Why: 133,628 allocations to produce an 86-byte patch

That is the number that explains everything else. Per interaction the
app allocates **5.66 MB across 133,628 objects** — roughly **200
allocations per rendered element** — and the reply on the wire is 86
bytes.

The mechanism is visible in the source. `Std.Ui.layoutContextFor`
([`sky-stdlib/Std/Ui.sky:2461`](../../sky-stdlib/Std/Ui.sky)) asks up to
four independent questions of every element:

```elm
layoutContextFor attrs =
    if hasMarker "__row" attrs then AsRow
    else if hasMarker "__col" attrs then AsColumn
    else if hasMarker "__paragraph" attrs then AsParagraph
    else if hasMarker "__textcolumn" attrs then AsTextColumn
    else AsEl

hasMarker name attrs =
    List.any (\a -> isMarker name a) attrs
```

`buildStyleString` asks two more (`__grid`, `__wrap`). So **six full
`List.any` scans of the attribute list, per element, per render** — and
every predicate call goes through `reflect.Value.Call` via the
higher-order-call adapter, allocating as it goes. `hasMarker` alone is
**13.5% of all server CPU**.

The runtime added its own share of the churn. **Both runtime items below
have since been fixed; the text is kept in the past tense because the
allocation figures in this section were measured before them, and
["What items 1–3 actually bought"](#what-items-13-actually-bought)
re-measures against them.**

- `HtmlToVNode` **allocated** a `map[string]string` *and* a
  `map[string]any` for every element whether or not it had any attributes
  or events. **Fixed:** `applyHtmlAttr` (`live.go:182`) now writes through
  `setAttr` (`live.go:166`) and `setEvent` (`live.go:174`), each of which
  creates its map on first use.
- `applyStyleInjections` made **four full-tree passes, each reallocating
  every node's children slice**. **Half fixed:** the walk is now
  copy-on-write — `walkChildrenWithVoidSiblingHoist` leaves `out` nil
  until a hoist forces a rebuild — and the four `styleMarkerSpec` values
  were hoisted to package level (`live.go:662`, `:719`, `:1050`, `:1104`),
  which is where the cost actually was.

  **Still four unguarded walks.** `applyStyleInjections`
  (`live.go:1015-1020`) calls the four `injectStyleMarker` passes
  unconditionally, and `injectStyleMarker` (`live.go:766`) recurses the
  whole tree with no whole-tree "does any marker exist" early-out. An app
  using no `@media` / `:hover` / transition / animation attribute still
  pays four complete traversals per render.

### Every pass over the tree, per interaction

**No document stated this count before, and the working assumption was six.
It is thirteen.** That gap is why the two runtime items above were the ones
anyone thought to look at: a wrong mental model of the pipeline had nothing
to check itself against. Every entry below is a complete traversal of a
whole tree, per `/_sky/event` interaction, for a `Std.Ui` app — the pinned
default.

| # | Pass | Where | Builds a tree? | Conditional? |
|---|---|---|---|---|
| 1 | `view(model)` builds the **`Element`** ADT | compiled Sky — user code | **yes — tree 1** | no |
| 2 | `Ui.layout` → `renderElement` walks `Element` → **`Html`** | `sky-stdlib/Std/Ui.sky:1696` / `:1776` | **yes — tree 2** | `Std.Ui` apps only |
| 3 | `HtmlToVNode` walks `Html` → **`VNode`** | `runtime-go/rt/live.go:108`, via `safeViewCall` `:5231`, called at `:5116` | **yes — tree 3** | no |
| 4 | `assignSkyIDs` stamps every element | `live.go:602`, called at `:5117` | no | no |
| 5 | `injectMediaQueryStyles` | `live.go:1016` → `injectStyleMarker` `:766` | rebuilds children on hoist | **no guard** |
| 6 | `injectPseudoClassStyles` | `live.go:1017` | rebuilds children on hoist | **no guard** |
| 7 | `injectTransitionStyles` | `live.go:1018` | rebuilds children on hoist | **no guard** |
| 8 | `injectAnimationStyles` | `live.go:1019` | rebuilds children on hoist | **no guard** |
| 9 | `renderVNode` builds the **whole-page HTML string** | `live.go:337`, called at `live.go:5119` | no | no — **and discarded on the patch path** |
| 10 | `diffTrees` — the reply patch | `live.go:1370`, called at `live.go:4795` | no | no |
| 11 | `ackInputsForPrevTree` walks `prevTree` for live `sky-id`s | `live.go:2611`, called at `live.go:4741` | no | **yes** — only when the client sent dirty inputs (`len(s.inputSeqs) != 0`) |
| 12 | **Second `diffTrees`** for the multi-tab fan-out | `live.go:4762` | no | **yes** — only when a sibling tab holds an SSE connection (`hasSSEConnOtherThan`) |
| 13 | Dev determinism check: a **second full `view`** plus two `vnodeShapeSig` walks | `live.go:5239-5245` | **yes — a 4th tree** | **yes** — dev only, `viewDeterminismCheckEnabled()` |

Four things in that table appear in no breakdown anywhere else in this
document, and each is a distinct finding:

1. **The `Element` → `Html` → `VNode` chain builds two complete ADT trees
   before the diff sees anything** (rows 2 and 3), on top of the `Element`
   tree the user's `view` built. `Std.Ui` is the pinned default view layer,
   so this is the ordinary path, not an exotic one. The profile attributes
   rows 1 and 2 together as "`view(model)` — 84% of the handler", which is
   correct but hides that a third of that name is a stdlib tree conversion
   rather than user code.
2. **The four style-injection walks are unguarded** (rows 5–8). An app that
   uses no `@media`, `:hover`, transition or animation attribute anywhere
   still pays four full traversals. A single whole-tree marker check would
   skip all four.
3. **The full-page HTML string is built every interaction and thrown away
   on the patch path** (row 9). `dispatch` always calls `renderVNode`
   (`live.go:5119`); `handleEvent` then ships `patches` and never sends
   `body2` (`live.go:4794-4800`). The string is retained only to seed
   `lastComputedBody` / `lastShippedBody` for no-op suppression — which is
   also the 80 kB/session of "Rendered HTML bodies" in the retention table
   below.
4. **Rows 11 and 12 are real per-interaction work that no cost model
   included.** Row 12 in particular is a *second complete diff* of the same
   two trees, paid by any user with two tabs open.

Rows 1–10 are unconditional; 11–13 are gated as marked. The count is
therefore **10 always, 13 at the ceiling** — against a mental model of 6.

Line numbers above are the `/_sky/event` dispatch path
(`dispatch`, `live.go:4983-5162`, whose render is `live.go:5116-5119`). The
same four-call render sequence — `safeViewCall` → `assignSkyIDs` →
`applyStyleInjections` → `renderVNode` — is **duplicated verbatim at nine
sites** in `live.go` (`:4317`, `:4624`, `:4694`, `:4854`, `:5116`, `:5175`,
`:6381`, `:6994`, and `:280` in the initial-mount helper). Any change to the
pass list has to be made in all nine.

### Where the 1.4 MB goes — and why that figure is not a per-session cost

**The published ~1.4 MB reproduces exactly, and it is the wrong
measure.** RSS is a high-water mark; Go returns memory to the OS lazily.
Measuring sessions **idle** (established, then quiescent, after two
forced GCs) separates retention from allocator headroom:

| N sessions | RSS / session | **live heap / session** | `HeapIdle` / session | stacks / session |
|---|---|---|---|---|
| 25 | **2,118 kB** | **336.8 kB** | 1,240 kB | 34.6 kB |
| 50 | **1,601 kB** | **336.7 kB** | 791 kB | 35.2 kB |
| 100 (r1/r2/r3) | **1,367 / 1,436 / 1,398 kB** | **336.4 / 336.3 / 336.2 kB** | 588 / 664 / 616 kB | 35.5 kB |

**RSS per session falls as sessions rise — 2,118 → 1,367 kB — while live
heap holds at 336 kB to within 0.2%.** A genuine per-session cost cannot
depend on how many sessions you divide by. The RSS regression was
measuring a largely fixed allocator pool, sized by peak allocation churn,
and charging it to sessions. At N=100 it reports 1.4 MB; at N=25 the same
method reports 2.1 MB.

Decomposing one session's 1,367 kB of RSS at N=100:

| Term | kB/session | Share |
|---|---|---|
| **Live heap (`HeapAlloc`) — the real retention** | **336** | **25%** |
| `HeapIdle` — free spans the runtime kept, not returned | 602 | 44% |
| Span fragmentation (`HeapInuse` − `HeapAlloc`) | 275 | 20% |
| Goroutine stacks (4 per session) | 36 | 3% |
| GC metadata + `mspan` | 41 | 3% |

The 64% that is headroom and fragmentation is a **consequence of the
5.66 MB-per-interaction churn**, not a property of a session. It is also
why RSS never came back: after load stopped and the heap was collected
(248 → 140 MB of `HeapInuse`), RSS stayed at 365 MB.

And the 336 kB that *is* retained, attributed by heap profile (3 repeats;
sampled totals agree with `MemStats` to 6%):

| Retention | kB/session | Share | Where |
|---|---|---|---|
| **`prevTree` — the previous VNode tree, held for diffing** | **132** | **40%** | `HtmlToVNode` |
| ↳ *of which the then-eager per-element `Attrs`+`Events` maps* | *98* | *30%* | `applyHtmlAttr`, `live.go:182` — since made lazy (`setAttr` `:166` / `setEvent` `:174`); re-measured below |
| **Rendered HTML bodies** (`lastComputedBody` + `lastShippedBody`) | **80** | **24%** | `strings.Builder` |
| **Style-injection tree copies** | **57** | **17%** | `walkChildrenWithVoidSiblingHoist` |
| `net/http` per-connection read+write buffers | 18 | 6% | `bufio` |
| `assignSkyIDs` path strings | 10 | 3% | |

**The Model does not appear.** ~84% of what a session retains is the
previous VDOM tree plus the rendered HTML kept to diff and to suppress
no-op frames. That is a design decision — server-held previous state is
what makes the diff protocol work — and it is now costed.

### Is any of it avoidable?

**Items 1–3 have since been implemented and re-measured; the results are
in ["What items 1–3 actually bought"](#what-items-13-actually-bought)
below, and two of the three estimates in this table were wrong.** The
table is left as written so the estimates can be compared against what
happened.

Each of these is a separate reviewed change and the estimates are what
they are worth arguing about:

| # | Change | Buys | Cost / risk |
|---|---|---|---|
| 1 | **Fold `Std.Ui`'s six marker scans into one pass** over the attribute list, producing a flags record | up to **1.24 ms/interaction (~13% of CPU)** and a large share of the 133k allocations | Contained to `layoutContextFor` + `buildStyleString` in `Std/Ui.sky`. Behaviour-preserving; needs `26-ui-showcase` + `19-skyforum` render goldens |
| 2 | **Allocate `VNode.Attrs`/`Events` lazily** instead of unconditionally per element | **~98 kB/session (30% of retention)** | `live.go:182` plus every reader. Go reads nil maps safely; only writes need a guard. Small but touches a hot struct — **DONE** (`setAttr` `:166`, `setEvent` `:174`) |
| 3 | **Fuse `applyStyleInjections`' four tree passes into one**, and skip entirely when no style markers exist | **~57 kB/session (17%)** + 4 tree-copies of churn per render | `live.go:1015-1020`. **PARTLY DONE** — the specs were hoisted and the walk made copy-on-write, which was the real cost; the four passes were NOT fused and there is still no marker guard (`injectStyleMarker`, `live.go:766`) |
| 4 | **Stop retaining two HTML bodies** where they are byte-identical | up to **80 kB/session (24%)** | They usually alias already; the win is only in the diverged case. Needs care — the split exists to keep a suppression invariant honest (`live.go:2185-2216`) |
| 5 | **Direct dispatch instead of `reflect.Value.Call`** for Sky higher-order calls | 11–12% of CPU is reflection self-time; more is the allocation it forces | Codegen-level, large, and the highest-leverage item here |
| 6 | Tune `GOGC` / `SetMemoryLimit` | trades RSS headroom against GC CPU — moves the 64%, not the 336 kB | Config only; a sizing lever, not a fix |

Items 1–3 are the cheap ones and together address roughly **13% of the
per-interaction CPU and 47% of per-session retention**. None of them
touches the architecture.

The architectural item is item 5 plus the shape it serves: **Sky
re-runs the entire `view` function on every interaction, through
reflective dispatch, to produce a diff that is 1.3% of the cost.** That
is why per-interaction cost tracks view size, and it is the difference
between this and a LiveView-style design that compiles a template into
static and dynamic segments and re-evaluates only the dynamic ones.
Whether Sky should do the same is a design question this measurement
does not answer — but it is now the question, and it is no longer
guesswork which part of the pipeline it is about.

### What items 1–3 actually bought

Implemented and re-measured with the same harness, same app, same
config (N=50 closed loop, `GOMAXPROCS=1`, three runs each), on a
machine shared with other work — so the allocation figures, which are
load-independent, carry the argument and the CPU figures are quoted
with the load average they were taken at.

| | allocations / interaction | bytes / interaction | live heap / session |
|---|---|---|---|
| Baseline | 133,084 (±0.3%) | 5,634 kB | 336.5 kB |
| **+ item 1** — one marker pass | **111,525** (−16.2%) | 5,126 kB (−9.0%) | — |
| + item 2 — lazy `Attrs`/`Events` | 111,672 (+0.1%) | 4,950 kB (−3.4%) | — |
| **+ item 3** — style-pass allocation | **108,080** (−3.2%) | 4,836 kB (−2.3%) | 337.4 kB |
| **Total** | **−18.8%** | **−14.2%** | **+0.3%** |

**Item 1 delivered, and by a different mechanism than the table above
predicted.** The estimate said "up to 1.24 ms/interaction (~13% of
CPU)"; measured, CPU per interaction fell 9.51 → 8.88 ms, **−6.7%**,
about half the estimate. The saving is not the six scans' loop
overhead: it is that each `hasMarker` call widened the typed attribute
list to `[]any` (`rt.AsListT`), boxing every element, and one fold does
that once instead of six times.

**Item 2 did not move the headline number, and the table's ~98 kB/session
estimate did not survive contact.** `Std.Ui` gives nearly every element an
inline `style` attribute, so the `Attrs` map is allocated regardless; only
the `Events` map on event-less elements is saved. It is retained because the
eager pair was indefensible, not because it paid.

**Item 3's real cost was somewhere else entirely.** The estimate blamed
the four tree-copies. The four tree-copies were 0.015 allocations per
element. What actually cost 11.03 per element was that each pass built
its `styleMarkerSpec` — a slice and a closure — *inside* the function
the walk recursed through, so every node rebuilt it, four times per
render. Hoisting the four specs to package level took one
`applyStyleInjections` over a 389-element tree from **4,290
allocations to 6**. The four passes were NOT fused: once that was
fixed, what remained of the extra three walks was 0.015 allocations
per element, and fusing them would have had to reproduce the
`[anim][transition][pseudo][mq]` prepend ordering by construction for
no measurable gain.

**Retention did not improve; it rose 0.9 kB/session (+0.3%).** The
estimate had items 2+3 removing 47% of it. Two reasons it did not.
Item 2's maps are mostly populated, per above. And item 3's
copy-on-write walk removed an *accidental* compaction: the old code
rebuilt each children slice at exactly `len(children)`, discarding the
spare capacity `append` had grown while the tree was built, and the
new code keeps the original slice and its slack. That is a real trade —
less churn per interaction, slightly more held per session — and it is
recorded here rather than presented as a clean win.

`runtime-go/rt/live_alloc_gate_test.go` ratchets items 2 and 3 so they
cannot silently come back. It cannot see item 1, which is compiled Sky
above the boundary its fixtures start at; that hole is stated in the
file.

### The ~10% store bound is withdrawn

Three places in this work quoted "the earlier memory-vs-postgres gap
(~21/s vs ~19/s) bounds the gob/store path at ~10%". **That bound does not
hold, for two independent reasons, and it is retracted rather than
restated.**

**One: its own source retracts the gap.** The ~21/s vs ~19/s difference is
the same spread that ["The load curve — and a spread that was not what it
looked like"](#5-the-load-curve--and-a-spread-that-was-not-what-it-looked-like)
shows to be an artefact of **run order**, not configuration. Counterbalancing
the sweep, whichever configuration runs first gets ~40/s and whichever runs
third gets ~21/s — including config C, which writes every session through
PostgreSQL. That section's conclusion is that embedded PostgreSQL has **no
measurable throughput cost on this hardware**. A 2/s difference read off a
run order cannot bound anything.

**Two: it bounds nothing above ~20 interactions/sec, which is all it ever
saw.** Every number in that comparison was taken at a sustained ~17–22
interactions/sec, at and past the knee of a 2-vCPU instance. `handleEvent`
writes the session once per interaction (`app.store.Set`,
`runtime-go/rt/live.go:4745`), and on a durable store each of those is its
own transaction and therefore its own fsync. So the measurement applied:

```
  ~20 interactions/s  ->  ~20 session writes/s  ->  ~20 fsync/s
```

The arithmetic does not stay flat when the rate does not. At 1,000
interactions/sec the *same per-interaction write* is:

```
  1,000 interactions/s -> 1,000 session writes/s -> 1,000 fsync/s
```

— **50× the write rate and 50× the fsync rate the measurement ever
applied**, and the byte rate scales by the same factor. That is the regime
where a store cost stops being a percentage and starts being the ceiling:
this repo's own note on the identical mechanism, one transaction per row on
a durable store, puts the fsync-bound ceiling at **order 5–10k/s** and
records that it is a property of the disk rather than of the code
(`runtime-go/rt/analytics_writer.go:9-14`). A "~10%" figure taken at 20/s
says nothing about proximity to that ceiling at 1,000/s.

**What can honestly be said:** the gob/store path is absent from these
profiles by construction, and **it is unmeasured**. Sizing a durable session
store needs a run that actually drives one above the knee. Until that run
exists, no percentage should be quoted for it.

### What this does not cover

1. **`arm64` only.** The GCE work found x86 differs ~30% on memory.
   Ratios should travel; milliseconds should not.
2. **`memory` session store only**, so the gob path is absent from these
   profiles by construction, and **this document no longer offers a bound
   on it** — see ["The ~10% store bound is
   withdrawn"](#the-10-store-bound-is-withdrawn).
3. **Two apps.** The CPU breakdown is `26-ui-showcase`'s; `19-skyforum`
   supplies only the second point on view size. An app with a heavy
   `update` rather than a heavy `view` would profile differently — the
   method here is the transferable part.
4. **The control is a floor, not a target.** It does no diffing and
   holds no VDOM; some of the 440× is work Sky.Live genuinely does.
5. **No same-region or cross-machine client** — everything is loopback,
   so no network term is included.

## Capacity, measured

Bare host, 8-core M1, heavy 384-node view, 1 s think time:

| Concurrent sessions | Throughput | p50 | p95 | p99 |
|---|---|---|---|---|
| 100 | 93/s | 8.1 ms | 12.3 ms | 14.8 ms |
| 500 | 404/s | 70 ms | 637 ms | 1,233 ms |
| 1,000 | 433/s | 659 ms | 3,927 ms | 6,364 ms |

**The knee is between 100 and 500 concurrent sessions.** At 100 the
server keeps up with demand (93/s against a 100/s offered load) and
latency is flat. By 1,000 the throughput has plateaued at ~430/s while
p50 latency has grown 80× — the queue, not the work, dominates.

The load generator used 0.25–1.6% of the machine throughout, so none of
this describes the generator.

**Every throughput figure in this document is for a specific view
size, and does not transfer to a different one.** The rows above are a
384-element view. Capacity guidance quoted without the view size it was
measured at is not usable; scale it by element count, not by session
count alone. The measured relation — seven sizes from 30 to 1614
elements, three runs each, every run patch-bearing — is
`cost_ms ≈ 0.12 + 0.018 × elements` at one core
([`runs/forum-rebaseline-20260816/`](runs/forum-rebaseline-20260816/));
the "2.4× the throughput for 4.1× fewer elements" comparison that stood
here rested on three runs the generator had flagged invalid, and is
withdrawn.

### Memory is the constraint that binds first

Three independent measurements, each the RSS delta from adding that many
live sessions:

| Sessions added | RSS delta | Per session |
|---|---|---|
| 100 | 132.5 MB | 1,357 KB |
| 500 | 511.1 MB | 1,047 KB |
| 1,000 | 1,259.2 MB | 1,289 KB |

**About 1.1 MB of server RSS per live session** holding a 384-node view.
On a 2 GB instance that is roughly 1,700 sessions before memory alone
exhausts — but the throughput knee arrives far earlier, at a few
hundred. Memory sets the hard ceiling; latency sets the useful one.

> **Superseded on x86: the figure is ~1.4 MB, not ~1.1 MB.** This 1.1 MB
> was measured on ARM64 under Apple's `container`. It has since been
> measured on real GCE hardware — the *same* application, under load,
> with RSS regressed against established session count over 1–500
> sessions — and the slope is **1,379 kB/session on an e2-micro and
> 1,450 kB/session on an e2-small**, roughly **30% higher**. See
> [`skylive-remote-validation.md`](skylive-remote-validation.md), "The
> active result".
>
> Size with **~1.4 MB/session** on x86; ~1 GB of session budget carries
> **~730** sessions, not ~950. The ARM figure erred in the conservative
> direction, and the correction this table exists to make — that the
> docs' original 10–100 KB guess was wrong by one to two orders of
> magnitude — is unaffected and now stronger.
>
> The throughput knee moved much further. On GCE it arrives at **25–50
> sessions (e2-micro)** and **50–100 (e2-small)**, not the few hundred
> this ARM run suggested, and an e2-micro's *usable* ceiling is CPU-bound
> roughly 10× below its memory ceiling.
>
> **Superseded again, and more fundamentally: RSS per session is not a
> per-session cost.** Every figure in this section — 1.1 MB, 1.4 MB,
> 1.76 MB — is an RSS slope taken while interactions were in flight, so
> it charges a largely fixed allocator pool to sessions. Measured with
> sessions **idle** and the heap collected, the retention is **336 kB per
> session and constant**, while the RSS slope moves from 2,118 kB/session
> at N=25 to 1,367 kB/session at N=100 on the same build. See
> ["The attribution"](#the-attribution--what-the-11-ms-and-the-14-mb-actually-are).
>
> The practical effect is that **memory capacity was understated**: the
> hard ceiling is set by ~336 kB of retention plus an allocator pool that
> is amortised across sessions, not by ~1.4 MB each. The CPU-binds-first
> conclusion is unaffected and is strengthened.
>
> **Third pass (2026-08-16) — the number to size with is the marginal
> slope, and it is measured.** Neither RSS ÷ n (charges the fixed base to
> sessions) nor idle post-GC live heap (336 kB — omits what a loaded
> session pins) is the sizing input. Measured as an OLS-free slope across
> n = 100 → 500 under load on x86 (`19-skyforum`, 94 elements, commit
> `3ed83c08`): **625–650 kB/session on the PostgreSQL session store,
> 451–531 kB on the memory store** (`runs/gcp-x86-capacity-20260816/`).
> Note `GOGC` multiplies this slope — 2.9× across 100 → 400, measured on M1
> (`runs/gogc-postgres-20260816/`); the x86 slope at the shipped `GOGC=400`
> default is unmeasured.

## What is measured, and what is not

The server-side per-interaction path, as `dispatch` runs it
(`runtime-go/rt/live.go:4983`) and as the `/_sky/event` handler replies
(`handleEvent`, `live.go:4516`):

| Step | Where | In Phase 1 (microbenchmark)? | In Phase 6 (profile)? |
|---|---|---|---|
| `update(msg, model)` | compiled Sky — user code | **No** | **Yes** — negligible for this app |
| `view(model) -> Html` | compiled Sky — user code | **No** | **Yes — 4.75 ms, 84% of the handler** |
| `HtmlToVNode` | `live.go:108` | no | **Yes** — 0.53 ms |
| `assignSkyIDs` | `live.go:602` | **Yes** | **Yes** — 0.05 ms |
| `diffTrees` | `live.go:1370` | **Yes** | **Yes** — 0.12 ms |
| JSON encode of the reply | `writeEventJSON`, `live.go:4808` | **Yes** | negligible |

**This table is a subset of the real path, not the whole of it.** It lists
the steps the two benchmark phases could see. The full per-interaction
pass count — thirteen, not the six above — is
["Every pass over the tree"](#every-pass-over-the-tree-per-interaction)
below.

The two Sky-compiled steps cannot be driven from a Go benchmark — they
are the user's own functions, and their cost is app-specific. Phase 1
therefore measured only the **runtime's** share, and said so, warning
that "a pathological `view` can dominate it."

**Phase 6 profiled a running app and found that it does — and that no
pathology is required.** `view(model)` is 84% of an ordinary interaction
on a stock example app. The caveat above was correct and load-bearing:
the runtime's share, which is what Phase 1 measured, is a few percent of
the whole. See ["The attribution"](#the-attribution--what-the-11-ms-and-the-14-mb-actually-are).

That said, the seam is clean. `VNode` (`live.go:51`) is a plain exported
Go struct, `diffTrees` takes two of them, and the whole path below the
Sky boundary is ordinary Go. No app needs to be stood up to benchmark
it — which is why Phase 1 needs no container, network or database.

## Mutation class decides what the DIFF costs

> **This section was published as "the finding that matters most:
> mutation class, not node count". That framing is retired.** It was
> true of `diffTrees` and was generalised to the interaction — the same
> error, from the same measurement, as the "roughly constant in view
> size" paragraph corrected above. Attribution settled it: mutation
> class decides the cost of `diffTrees`, which is **1.3% of an
> interaction**; node count decides the cost of `view(model)`, which is
> **84% of the handler**. Both regimes below are real and still worth
> knowing — but the variable that sets a server's capacity is node
> count, not mutation class.

`diffNodes` (`live.go:1376`) is a non-keyed positional walk with two
very different cost regimes:

- **Text or attribute change** — walk the tree, emit a small patch.
  Cost is proportional to node count, with a small constant.
- **Any change to a child count** — the parent's entire child list is
  re-serialised through `renderVNode` into one HTML patch
  (`live.go:1493-1504`; the mixed-children variant at `:1508-1520`).
  Cost is proportional to the *subtree*, not to the size of the change,
  and allocates heavily.

So "the cost of a diff" is not one number. Adding a row to a list costs
far more than editing a label in that same list, at the same node
count. Any sizing figure that does not say which regime it is in is
unusable — which is the specific defect in "2–10 ms".

## Reference view sizes (measured, not assumed)

Counted from the HTML the apps actually serve at this commit:

| App | Addressable elements (`sky-id`) | Total tags |
|---|---|---|
| `examples/19-skyforum` — canonical Sky.Live form flow | 94 | 135 |
| `examples/26-ui-showcase` — every `Std.Ui` primitive | 384 | 422 |

`TestBenchTreeSizesMatchReferenceApps` pins the benchmark fixtures to
these counts and fails if they drift more than 10%. It caught a 36%
drift the first time it ran.

## Known weaknesses in this measurement

Stated so they are not discovered later as surprises:

1. **The Phase 1 tree is a calibrated model, not a captured one.** The
   fixture is pinned to the reference apps' *element counts*, and its
   shape (2–3 attributes per element, one event per row, shallow
   nesting) is modelled on what `Std.Ui` emits — but it is not the
   actual rendered tree of either app. Attribute density and nesting
   depth both affect per-node cost. Capturing a real `VNode` tree and
   replaying it would close this; the seam exists (`HtmlToVNode`) but
   is not yet wired to a fixture.
2. **`HtmlToVNode` is not in the measured path.** The Sky `Html` ADT to
   `VNode` lowering runs on every render and is not counted in the
   128 ns/node figure.
3. **The analytics on/off comparison was not run.** Analytics is driven
   by app code calling `Std.Analytics`, not a global switch, so it needs
   `examples/52-blog-analytics` rather than the showcase app used
   throughout here. The harness supports it (`--app`, `--label`); the
   run was not performed.
4. ~~**Postgres backend counts were not collected**~~ — **closed.** They
   were collected on an e2-small running embedded PostgreSQL: a **6-connection
   pool** at 25, 50 and 100 concurrent sessions (`dbSharedAuxPoolSizeFor(2)`),
   with `pg_backends_max` reading **7** — the pool plus the 1-Hz sampler — in
   the config-C rows at 50 and 100 sessions
   (`runs/gcp-embed-postgres-20260815/sweep.tsv`; at n=25 one row read 7 and
   two read 0, README:78-82; this bullet said 6, which is the pool), against a
   derived `max_connections` of 36. See "Embedded PostgreSQL, measured" below. The
   *local* harness still reads `n/a`, because the reference app uses no
   database; the measurement was taken by switching the app's session
   store to `postgres` rather than by pointing `PGURL` anywhere.
5. **Phase 2 and 3 numbers were taken on a contended host** and are
   floors rather than ceilings. Phase 1 is protected against this by the
   min-of-15 estimator; queueing measurements cannot be.

## Guarding against measuring nothing

A benchmark can be wrong quietly, reporting a confident `ns/op` for work
it never did. The guards:

- **`TestBenchFixturesAreNonVacuous`** asserts each mutation class still
  produces the patch shape it is named for. If `list_append` stopped
  emitting an HTML patch, the benchmark would become a no-op walk and
  this fails instead.
- **`skyliveload -self-check`** drives one session end-to-end and prints
  the exchange — cookies, CSRF, scraped handler id, SSE frames, reply
  size — so the client is shown to speak the protocol before any
  throughput is believed.
- **Run validity gates** (`tools/skyliveload/main.go`) mark a run invalid
  rather than reporting it when no session established, no interaction
  produced a patch, no SSE frame arrived, the error rate exceeded its
  limit, or the generator used more than 70% of host CPU.
- **A load gate** in `scripts/skylive-bench.sh` refuses to summarise on a
  busy machine. This is not hypothetical: the first benchmark run in
  this work was taken at load average 15.8 on 8 cores and reported
  `noop` (which produces zero patches) as *slower* than `text_one`, and
  2000 nodes as faster than 1000. Both impossible; both would have read
  as plausible numbers in isolation.

### Constrained runs

App inside an Apple `container` VM, generator on the host, 384-node
view, 1 s think time. **Read the four caveats at the end of this
document before quoting any of this** — in particular, `--cpus 1` is an
*optimistic* stand-in for an e2-small baseline, not a match for it.

| Profile | Sessions | Throughput | p50 | p95 |
|---|---|---|---|---|
| 1 CPU / 2 GB | 100 | 92 /s | 14–18 ms | 75–95 ms |
| 1 CPU / 2 GB | 500 | 88 /s | **4,100–4,200 ms** | 8,700–9,500 ms |
| 2 CPU / 2 GB | 100 | 94 /s | 8.3–8.5 ms | 23–25 ms |
| 2 CPU / 2 GB | 500 | 188–194 /s | 1,390–1,430 ms | 2,400–3,100 ms |

Three things read straight off this:

- **One core serves ~100 concurrent sessions comfortably** at 1 s think
  time — it meets the full 100/s offered load at 14 ms p50.
- **500 sessions is far past the knee on both profiles.** On 1 CPU
  throughput *falls* (92 → 88/s) while latency reaches 4.2 s: the
  server is not just saturated, it is queueing. Since 1 CPU is twice
  the e2-small baseline entitlement, the real baseline would be worse.
- **Throughput scales with cores at saturation** (88 → 188/s from 1 to
  2 CPUs), which confirms the ceiling is CPU-bound rather than lock- or
  IO-bound at that point.

RSS inside the container reached 1.26 GB against the 2 GB limit during
the 500-session runs, so memory and CPU exhaust at roughly the same
concurrency on a 2 GB machine.

## Conditions

Every figure above was taken on:

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 (arm64) |
| Commit | `85ded8ef` on `perf/skylive-benchmark` |
| Go | 1.26.1 |
| App | `examples/26-ui-showcase` (384 elements) unless stated |
| Container | none — bare host |

**The machine was contended.** This repo is routinely worked by several
agents at once, and the 1-minute load average sat between 6 and 18 on 8
cores throughout. That is why Phase 1 is reported as the **minimum of 15
repetitions** rather than a mean: contention only ever *adds* time, so
the minimum is the best available estimator of the uncontended cost and
is an upper bound on the truth. The estimator's validity is visible in
the data — run-to-run spread is 1.0–1.1× for most points, and the
`noop`/`text_one`/`attr_one` classes agree to within 2% as they must,
since they do nearly identical work.

For contrast, the *first* attempt at this measurement, taken as a single
run at load average 15.8, reported `noop` (which produces zero patches)
as **slower** than `text_one`, and 2,000 nodes as **faster** than 1,000.
Both impossible. Neither would have looked wrong quoted on its own.
`scripts/skylive-bench.sh` now refuses to summarise above a load
threshold for this reason.

**The Phase 2 numbers are more affected by contention than Phase 1**,
because they include queueing. Treat the knee's *location* and the
per-session memory as sound, and the absolute throughput ceiling
(~430/s) as a floor — a quiet machine would do better.

## Limits of the constrained runs

Stated at the top of `scripts/skylive-load-constrained.sh` and repeated
here because it is the easiest thing in this document to misuse:

1. **Not a GCP e2-small.** Apple's `container` runs an **ARM64 Linux VM
   on Apple silicon**; e2-small is a shared-core **x86** instance. These
   numbers are good for *relative* comparisons — before/after, where the
   knee is, memory per session — and **must not** be published as "you
   will serve N users on an e2-small". Only a run on real GCP settles
   that.
2. **Not GCP's burstable credit model.** A fixed vCPU allocation has no
   credit accrual or drain. Real e2 behaviour moves between the floor
   and the ceiling over time in a way this cannot reproduce.
3. **The fractional quotas could not be run.** Apple's `container`
   v1.0.0 accepts only **integer** `--cpus` — it allocates whole vCPUs
   to a VM rather than applying a CFS quota. `--cpus 0.5` and
   `--cpus 0.25` are rejected outright, so the e2-small baseline (0.5)
   and e2-micro baseline (0.25) cannot be reproduced with this tool.
   The 1-CPU run is an **optimistic** stand-in for the e2-small
   baseline — twice its entitlement. Reproducing the real floor needs
   Docker (`--cpus 0.5`), a Linux host with cgroup v2 `cpu.max`, or a
   real VM.
4. **The VM's virtual NIC adds a latency floor**, so constrained runs
   compare with each other, not with bare-host runs.

## Where the GCP validation got to

Caveat 1 above says only a run on real GCP settles the hardware
question. That work is recorded in
[`skylive-remote-validation.md`](skylive-remote-validation.md), against
the live e2-micro serving sky-lang.org. In short:

- **Base RSS on x86 is measured** — a real Sky.Live app idles at ~56 MB
  on an e2-micro, well inside its 768 MB unit cap.
- **Per-session memory is not**, because that instance has no concurrent
  sessions to divide by. The harness detects this and refuses to print a
  slope rather than dividing by an absent divisor.
- **A blocking discovery**: the runtime exposes **no session-count and no
  memory metric at all**. `sky_live_sessions_active` is declared in the
  Prometheus help table and never recorded, and `SessionStore` has no
  `Count()`. Any future capacity instrumentation has to add these first.
- Remote load is now supported, defaults to passive, and structurally
  refuses production targets.

## Embedded PostgreSQL, measured

Every PostgreSQL figure in `docs/skydb/embedded-postgres.md` was derived and
none had been observed on target hardware. This section is the observation.
Raw data: [`runs/gcp-embed-postgres-20260815/`](runs/gcp-embed-postgres-20260815/).

### Conditions, attached to every number below

| | |
|---|---|
| Target | `sky-bench-embed`, **e2-small**, `us-central1-a`, project `settleby` |
| | Debian 12, `Linux 6.1.0-52-cloud-amd64` **x86_64**, 2 shared-core vCPU, 20 GB |
| MemTotal | **2,023,888 kB (1.98 GB)** — identical to the `sky-bench-small` of the SQLite run |
| Application | **`examples/26-ui-showcase`**, cross-compiled `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, Go 1.26.1 |
| Commit | **`8e166eaf`** (`feat/embedded-postgres`) — *not* the `ba3c3b1d` of the SQLite run; row 4 of the table above is the control that bridges them |
| PostgreSQL | **15.19** (Debian `15.19-0+deb12u1`) via `SKY_POSTGRES_BIN=/usr/lib/postgresql/15/bin` |
| systemd | unit `skybench`, user `skybench`, **no `MemoryMax`**, `TasksMax=4096`, `LimitNOFILE=65535` |
| Ops Agent | **ABSENT** |
| Generator | `tools/skyliveload`, macOS arm64, 8 cores, **in the UK** — off-box, across the public internet |
| ICMP RTT | **109.8–112.0 ms, mean 111.3, stddev 0.66, 0% loss** (n=20) |
| Think time | 1 s, jitter 0.3 · ramp 20 s · hold 60 s · warmup 5 s |
| Runs | **27** main (3 levels × 3 configs × 3 repeats) + 4 counterbalance; **all 27 `valid=true`** |
| Generator load | **max 0.309%** of the 8-core generator, mean 0.180% — never the bottleneck |

Each level **restarts the app first**, because the memory store holds a
session for the full 30-minute TTL after its SSE closes and a "drain" sleep
drains nothing. The divisor is `sessions_established`, never the number
requested.

### The bundle delivery path is UNTESTED

`SKY_POSTGRES_BIN` was pointed at Debian's `postgresql-15`. This exercises
everything downstream of "PostgreSQL binaries exist" — supervisor,
`initdb`, the tuned conf, pool sizing, the `max_connections` derivation,
lifecycle — because `discoverPgBins` ranks the override *first*
(`runtime-go/rt/pg_embed_bundle.go:114`), above the `go:embed`ed bundle.

What it does **not** test is the bundle itself: `sky build --embed`,
`scripts/skydb/build-postgres-bundle.sh`, bundle extraction into
`<dataRoot>/runtime`, and the version pinning that a bundle carries. A
linux-amd64 bundle cannot be built on Apple silicon and no
`postgres-bundle-v*` release is cut, so that path remains unexercised on
real hardware. **No number here should be read as validating bundle
delivery.**

It also means the cluster is **PostgreSQL 15**, where `postgresVersion`
defaults to `18.6`. Nothing in the runtime objected, which is itself worth
knowing.

### 1. Idle footprint — the 36 MB claim survives, its derivation does not

`embedded-postgres.md` bills **"PostgreSQL base — postmaster + 6
auxiliaries at `shared_buffers = 32MB` — 36 MB (measured)"**. Two separate
methods, over 9 cold restarts per configuration:

| | config A (no PG) | config B (PG, `memory` sessions) | config C (PG, `postgres` sessions) |
|---|---|---|---|
| App RSS, idle | **21.24 MB** | 22.89 MB | 24.17 MB |
| PostgreSQL tree, **RSS sum** | 0 | 76.29 MB | 90.08 MB |
| PostgreSQL tree, **PSS sum** | 0 | **29.46 MB** | **32.24 MB** |
| `MemAvailable` | 1,594.7 MB | 1,572.8 MB | 1,566.3 MB |
| **MemAvailable cost of PG** | — | **21.9 MB** | **28.4 MB** |
| postgres processes | 0 | 6 | 7 |

**The number is approximately right and slightly conservative.** Measured
29.5 MB of PSS, or 21.9 MB of `MemAvailable`, against a claimed 36 MB, for
the postmaster plus its auxiliaries — the same six processes the doc names.

**The stated derivation is falsified.** The `--embed` path does not run at
`shared_buffers = 32MB`. Tuning is derived from the host at every boot
(`runtime-go/rt/pg_embed_conf.go`), and what actually landed on this
2 GB instance was:

```
shared_buffers        = 296MB   # 15% of RAM
effective_cache_size  = 790MB
work_mem              = 13MB
maintenance_work_mem  = 98MB
max_connections       = 36
listen_addresses      = ''
```

`shared_buffers` is **296 MB, not 32 MB — 9× the assumed value.** The
32 MB figure belongs to the *development* cluster profile (`sky db start`),
which uses fixed small constants; the sizing table quotes it for the
embedded profile, which does not.

The footprint is nevertheless ~30 MB because a shared-memory mapping costs
what is *touched*, not what is reserved: `/proc/meminfo` `Shmem` read
**20.8 MB** against the 296 MB segment. So **36 MB is an idle floor, not a
ceiling**, and the headroom above it is an order of magnitude larger than
the row implies. A working set that exercises the buffer pool can pull
resident memory toward 296 MB, and nothing in the sizing table says so.

**RSS is the wrong metric here and by a known factor.** Summing RSS across
the tree counts `shared_buffers` once per process and reads 76–90 MB — an
overstatement of **2.6×**, the same trap the Ops Agent measurement hit at
2.2×.

### 2. The derived `max_connections` holds up

The conf is re-rendered every boot. On this 2-vCPU box it landed at **36**,
and `SHOW max_connections` on the running cluster agreed. That is exactly
what the source derives:

```
app_pool_size(2)              = clamp(2×4, 4, 32)        =  8
aux_pool_size(2)              = clamp(8/4, 2, 8)         =  2
process_connection_demand(2)  = 8 + 2×3 aux consumers    = 14
embeddedMaxConnections(2)     = 14×2 + 3 reserved + 5 headroom = 36
```

(`rust/crates/sky/src/db_pool_sizing.rs:101-127`,
`runtime-go/rt/pg_embed_conf.go:277-284`.)

Checked against demand on the deployed cluster:

| | |
|---|---|
| `max_connections` | **36** |
| `superuser_reserved_connections` | **3** |
| Usable by the app | **33** |
| Worst-case demand, one process (`process_connection_demand`) | 14 |
| Demand with restart overlap (2 processes) | 28 ≤ 33 ✔ |
| **Peak `client backend` rows observed, 100 concurrent sessions** | **7** (`pg_backends_max`) — the 6-connection pool plus the 1-Hz sampler |

The property gate passes, and it is not vacuous: its own falsification
witness — `TestTheHistoricalSizingViolatesTheProperty`, which asserts the
old flat `50` *fails* the property — passes too.

```
runtime-go$ go test ./rt/ -run 'GrantsEveryPool|PoolDemandCounts|HistoricalSizingViolates'
--- PASS: TestEmbeddedClusterGrantsEveryPoolThisProcessOpens
--- PASS: TestPoolDemandCountsEveryPoolNotJustTheApps
--- PASS: TestTheHistoricalSizingViolatesTheProperty
```

**Deployed reality matches the property.** 36 covers 14 of demand twice
over plus the 3 reserved slots, and the app never came within 5× of it.

### 3. Backends under load — the ceiling holds; sharing is not decidable here

`pg_stat_activity`, sampled at 1 Hz, while 100 concurrent Sky.Live sessions
were driving the app with its session store in PostgreSQL:

```
 backend_type                 | state  | count
------------------------------+--------+-------
 client backend               | idle   |     6      <- the app's pool
 client backend               | active |     1      <- the psql doing the counting
 checkpointer / bgwriter / walwriter / autovacuum / logical repl |  1 each
```

**A 6-connection pool for 100 sessions.** The pool does not open one
connection per session; it stays at `dbSharedAuxPoolSizeFor(2) = 6`.
`pg_backends_max` reads a flat **7** across every run at 50 and 100 sessions
(and one of the three n=25 runs; the other two read 0 — the mid-sweep sampler
bug documented in `runs/gcp-embed-postgres-20260815/README.md:78-82`) — the 6
pool backends plus the sampler's own psql, visible as the `active` row above.
The pool's 6 against 33 usable connections is **18% utilisation** — 5.5×
headroom at a concurrency already past the machine's knee.

Six is exactly `dbSharedAuxPoolSizeFor(2) = aux(2) + analyticsShare(2) +
telemetryShare(2)` (`runtime-go/rt/db_pool.go:293`).

**But this instance cannot prove the pools share.** The sharing claim is
that the process opens `aux + 4` backends instead of `3 × aux`. At `aux = 2`
those are **both 6** — the comment at `db_pool.go:290` claims a "strict
improvement at every core count", and at the floor it is an equality, not a
strict improvement. So the observed 6 is *consistent* with sharing and
equally consistent with three unshared pools of 2. Discriminating them
needs `aux ≥ 3`, i.e. ≥ 3 vCPU. **The "12 backends → 4" result stands on
`TestLiveSameDsnConsumersShareOneConnectionSet`, not on this run**, and
this run should not be cited for it.

Sessions really were persisted: `sky_sessions` held **500** rows, the
cumulative total across runs — itself a demonstration of the 30-minute TTL
that makes per-level restarts mandatory.

### 4. Per-session cost with the database

OLS slope of app RSS against *established* sessions, 9 points per
configuration spanning 25–100:

| config | slope | intercept | measured idle |
|---|---|---|---|
| **A** — SQLite/no DB (control) | **1,338 kB/session** | 29.62 MB | 21.24 MB |
| **B** — embedded PG, `memory` sessions | **1,395 kB/session** | 24.72 MB | 22.89 MB |
| **C** — embedded PG, `postgres` sessions | **1,764 kB/session** | 19.65 MB | 24.17 MB |

Against the e2-small SQLite figure of **1,450 kB/session**, the control
here reads 1,338 kB — 8% lower, at a different commit, which is the
honest size of the run-to-run uncertainty on this measurement.

- **Embedded PostgreSQL with a memory session store adds ~57 kB/session**
  (1,338 → 1,395), which is inside that uncertainty. Call it **free per
  session**; its cost is the fixed ~22 MB floor.
- **A PostgreSQL session store adds ~426 kB/session** (1,338 → 1,764), a
  real **+32%**. This is paid in the app — codec buffers and pool state —
  not in the database, whose own footprint is flat in session count.

**The intercept check is weaker here than in the SQLite run and is not
quoted as corroboration.** That run fitted 6 levels and recovered idle RSS
to within 1.7 MB. This one fits 3, and the intercepts land 8.4 MB high
(A), 1.8 MB high (B) and 4.5 MB low (C). Only B's is a clean recovery.

**One caveat that bounds row 6.** The `postgres` store logs
`idleEvict=5m0s`, and every measurement window here is 60 s. No session was
ever evicted, so sessions were resident in the app's cache *and* in
PostgreSQL simultaneously. **1,764 kB/session is therefore the un-evicted
worst case**; a steady state longer than the eviction interval should be
cheaper in the app, and this run cannot say by how much.

System-wide, from `MemAvailable` consumed at 100 sessions (median of 3):

| config | consumed | per session, system-wide |
|---|---|---|
| A | 157.8 MB | 1,578 kB |
| B | 161.4 MB | 1,614 kB |
| C | 243.6 MB | 2,436 kB |

### 5. The load curve — and a spread that was not what it looked like

Sustained throughput, interactions/sec, all three repeats shown because the
spread is a trend rather than an interval:

| sessions | A (SQLite) | B (PG, mem) | C (PG, pg) |
|---|---|---|---|
| 25 | 21.4 · 21.4 · 21.5 | 21.5 · 21.4 · 21.4 | 21.3 · 21.4 · 21.2 |
| 50 | **41.0** · 21.5 · 21.9 | 26.6 · 20.0 · 21.9 | 19.3 · 19.3 · 19.2 |
| 100 | 18.3 · 18.3 · 17.0 | 18.2 · 18.5 · 16.5 | 18.0 · 18.2 · 17.2 |

p50 latency (includes the 111 ms wire): 140–145 ms at n=25 for all three;
176–328 ms at n=50; **1,440–2,256 ms at n=100**.

**The knee is between 25 and 50 sessions**, on all three configurations. At
25 the server meets the full offered load (21.4/s against 25/s demanded)
with flat latency; at 50 it delivers ~20/s against 50/s demanded and p95
crosses 5 s. That is *earlier* than the 50–100 the SQLite e2-small run
reported, and the reason is visible in the first row: 41.0/s is a
first-run-on-a-rested-instance number, and the earlier run's 35–42/s peak
was the same kind of number.

**The apparent config ranking at n=50 is an artifact of run order, and the
counterbalance proves it.** In `sweep.tsv` the configs always run A, B, C
within a level, so A always spends the freshest burst credits. Re-running
n=50 with the order **reversed**:

| order | first run | second | third |
|---|---|---|---|
| forward (A,B,C) | **A 41.0/s** | B 26.6/s | C 19.3/s |
| reversed (C,B,A) | **C 39.9/s** | B 24.8/s | A 21.4/s |

Whichever configuration runs first gets ~40/s; whichever runs third gets
~21/s. **The spread is position, not configuration.** Config C — embedded
PostgreSQL with every session written through it — reaches the same 40/s
burst as bare SQLite when it is given the same credit state.

So the honest statement is: **embedded PostgreSQL has no measurable
throughput cost on this hardware.** Sustained capacity is ~17–22/s for all
three configurations at and past the knee, and any single benchmark run
against a rested e2 instance overstates it by ~2×.

### What could not be measured

1. **Bundle delivery is untested** — see above. The measurements are valid
   for everything downstream of "PostgreSQL binaries exist".
2. **Aux-pool sharing is not decidable at 2 vCPU**, because `aux + 4` and
   `3 × aux` are both 6 there. Needs ≥ 3 vCPU.
3. **The `postgres` session store's steady state past `idleEvict=5m`** —
   every window was 60 s, so row 6's per-session figure is the un-evicted
   worst case.
4. **PostgreSQL 15, not 18.6.** The distro's version was used;
   `postgresVersion` defaults to 18.6 and no bundle exists to test it.
5. **No PSS sample under load.** PSS was taken at idle only, so the
   postgres tree's true resident cost *under* load is bounded (RSS sum
   168 MB, `Shmem` 20.8 MB, so most of the gap is double-counting) but not
   measured. `MemAvailable` covers the system-level question instead.
6. **Sessions above 100 were not run.** The machine is well past its knee
   at 100; 250 and 500 would describe a failing server, as they did on the
   SQLite run.
7. **No same-region client**, so sub-knee latencies remain UK-specific;
   subtract ~111 ms.
