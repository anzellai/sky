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
```

Phase 1 needs only Go. Phase 2's browser observer needs the repo's
existing Playwright (`npm install` at the repo root, as
`scripts/verify-examples.sh` expects); run `scripts/skylive-load.sh
--no-observer` to skip it. Phase 3 needs Apple's `container` CLI.

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

The consequence is the actionable part. The guidance implies that a
*more complex view* costs more per interaction, so capacity planning
should scale with view size. **It essentially does not.** Going from
`19-skyforum` (94 elements) to `26-ui-showcase` (384 elements) — a 4×
heavier view — adds 65 µs to an 11 ms interaction, or **0.6%**.

For sizing, treat per-interaction cost as **roughly constant in view
size** and driven by the per-request machinery instead: HTTP handling,
session locking, SSE bookkeeping, the app's own `update`/`view`, and GC.
A team worried that their view is "too complex to scale" is worrying
about the wrong variable.

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

## What is measured, and what is not

The server-side per-interaction path, as `dispatch` runs it
(`runtime-go/rt/live.go:5022`) and as the `/_sky/event` handler replies
(`live.go:4702`):

| Step | Where | Measured here? |
|---|---|---|
| `update(msg, model)` | compiled Sky — user code | **No** |
| `view(model) -> Html` | compiled Sky — user code | **No** |
| `HtmlToVNode` | `live.go:108` | not yet |
| `assignSkyIDs` | `live.go:573` | **Yes** |
| `diffTrees` | `live.go:1295` | **Yes** |
| JSON encode of the reply | `live.go:4715` | **Yes** |

The two Sky-compiled steps cannot be driven from a Go benchmark — they
are the user's own functions, and their cost is app-specific. This is a
real limit on the result and is stated rather than folded in: the
measured figure is the **runtime's** share of an interaction, and a
pathological `view` can dominate it.

That said, the seam is clean. `VNode` (`live.go:51`) is a plain exported
Go struct, `diffTrees` takes two of them, and the whole path below the
Sky boundary is ordinary Go. No app needs to be stood up to benchmark
it — which is why Phase 1 needs no container, network or database.

## The finding that matters most: mutation class, not node count

`diffNodes` (`live.go:1301`) is a non-keyed positional walk with two
very different cost regimes:

- **Text or attribute change** — walk the tree, emit a small patch.
  Cost is proportional to node count, with a small constant.
- **Any change to a child count** — the parent's entire child list is
  re-serialised through `renderVNode` into one HTML patch
  (`live.go:1419-1461`). Cost is proportional to the *subtree*, not to
  the size of the change, and allocates heavily.

So "the cost of an interaction" is not one number. Adding a row to a
list costs far more than editing a label in that same list, at the same
node count. Any sizing figure that does not say which regime it is in
is unusable — which is the specific defect in "2–10 ms".

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
4. **Postgres backend counts were not collected** — the reference app
   uses no database, so `pg_backends` reads `n/a` throughout. Point
   `PGURL` at a real cluster and use a Postgres-backed app to populate
   that column.
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
