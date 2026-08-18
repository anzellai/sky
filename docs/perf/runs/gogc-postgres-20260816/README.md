# Is `GOGC` a shippable default, or a trap?

**A trap, as a bare setting — and the answer is `GOMEMLIMIT`, with `GOGC` as
the pacer underneath it.**

The prompting result: `GOGC=800` bought **+33%** throughput on
`docs/perf/runs/gomaxprocs-scaling-20260816/`, level-independent at GOMAXPROCS
8/4/1, justified with "RSS is only 107 MB loaded". That reading was taken with
`SKY_LIVE_STORE=memory` at n=100. **This run repeats it on the PostgreSQL store
across n ∈ {100, 300, 500}, which is where the memory cost actually lives**, and
three things change.

34 arms, **zero rejected**.

## Verdict

| | |
|---|---|
| Does the +33% transfer to postgres? | **No.** Ceiling is **+24%**, and only at n=500. At n=100, `GOGC=800` is *slower* than `GOGC=400`. |
| Is there a setting that keeps most of it inside an e2-small at n=500? | **Yes — `GOGC=400` + `GOMEMLIMIT` ≈ 750 MiB: +19% at 759 MB, bounded.** |
| Does `GOMEMLIMIT` beat `GOGC`? | **Yes, on the axis that matters.** Not throughput-per-byte — it *bounds*, and `GOGC` cannot. |
| What should ship? | `GOMEMLIMIT` derived from detected machine memory at startup, `GOGC=400`. Sketch in [What should ship](#what-should-ship). |

**Do not ship a bare `GOGC` default.** `GOGC=800` at n=500 measured **1,827 MB**
— more than an e2-small *has*.

## The grid

PostgreSQL store, two counterbalanced blocks (block B is block A reversed, so no
cell sits at the same sequence position twice). Both runs shown; throughput is
tight at `GOGC ≤ 400` and the spread at 800 is itself a finding.

| n | GOGC | int/s (2 runs) | peak RSS MB (2 runs) |
|---|---|---|---|
| 100 | 100 | 2,791 – 2,799 | 136 – 140 |
| 100 | 200 | 3,108 – 3,137 | 204 – 215 |
| 100 | 400 | 3,375 – 3,375 | 312 – 342 |
| 100 | 800 | 3,060 – 3,521 | 550 – 773 |
| 300 | 100 | 2,696 – 2,827 | 268 – 268 |
| 300 | 200 | 3,183 – 3,188 | 412 – 415 |
| 300 | 400 | 3,409 – 3,423 | 650 – 708 |
| 300 | 800 | 3,201 – 3,530 | **1,148 – 1,926** |
| 500 | 100 | 2,793 – 2,838 | 400 – 403 |
| 500 | 200 | 3,182 – 3,191 | 626 – 654 |
| 500 | 400 | 3,217 – 3,410 | 1,016 – 1,170 |
| 500 | 800 | 3,457 – 3,535 | **1,674 – 1,979** |

### 1. The +33% does not survive the postgres store

Best case here is **+24%** (`GOGC=800`, n=500). At n=100 the curve *turns over*:
`GOGC=400` reads 3,375 and `GOGC=800` reads 3,060–3,521 — no better, and at four
times the memory. The published +33% is a property of the memory-store workload,
not of the collector, and should not be quoted for a postgres app.

### 2. `GOGC` multiplies the per-session slope — this is the trap

The marginal cost of a session, measured n=100 → n=500 (the corpus's OLS-free
method, so the fixed load-time growth is not charged to one level's sessions):

| GOGC | kB per session |
|---|---|
| 100 | **660** |
| 200 | 1,075 |
| 400 | 1,915 |
| 800 | **2,913** |

`GOGC` is a multiplier on live heap, so it does not raise the baseline and leave
the slope alone — **it scales the slope with it**. At `GOGC=800` every session
costs 4.4× what it costs at the default, which divides any capacity table by the
same factor. A sizing guide that adopts `GOGC=800` and keeps its
sessions-per-instance column is wrong by 4.4×.

### 3. At `GOGC=800` the memory is not merely high, it is unpredictable

Two identical n=300 runs read **1,148 MB and 1,926 MB** — a 68% spread, against
≤6% for every arm at `GOGC ≤ 400`. Peak RSS at a relaxed pacer depends on where
the heap happened to be when the window sampled it. **An operator cannot
provision against that**, which disqualifies it as a default independently of
the mean.

## `GOMEMLIMIT`, and why it wins

| n=500 config | int/s | vs default | peak RSS |
|---|---|---|---|
| `GOGC=100` (default) | 2,816 | — | 402 MB |
| `GOGC=200` | 3,187 | +13% | 640 MB |
| `GOGC=400` | 3,314 | +18% | 1,093 MB |
| `GOGC=800` | 3,496 | +24% | 1,827 MB |
| `GOGC=off` + limit 750MiB | 3,074 | +9% | 766 MB |
| `GOGC=off` + limit 1500MiB | 3,406 | +21% | 1,518 MB |
| `GOGC=100` + limit 750MiB | 2,840 | +1% | 404 MB |
| **`GOGC=400` + limit 750MiB** | **3,345** | **+19%** | **759 MB** |

Three results, in order of importance:

- **The bound holds, and it costs nothing.** Adding a 750 MiB limit to
  `GOGC=400` moved throughput 3,314 → 3,345 (inside noise) while cutting peak
  RSS **31%**, from 1,093 MB to 759 MB. The limit is not a tax; it is the part
  of the heap `GOGC=400` was using without benefit.
- **`GOGC` alone is not competitive at equal memory once you need a guarantee.**
  `GOGC=400` reads 1,016–1,170 MB at n=500 and has no ceiling at n=1000. The
  combined arm reads 759 MB at n=500 *and* 381 MB at n=100 — it takes what it
  needs and stops.
- **`GOGC=off` + limit is the wrong shape.** With no multiplier the collector
  runs only when the limit approaches, so the process spends its whole budget
  regardless of load: **758 MB at n=100**, where the combined arm used 381 MB.
  Same bound, half the memory, and *more* throughput at n=500 (3,345 vs 3,074).
- **`GOGC=100` + limit is a pure backstop.** 2,840 int/s / 404 MB is
  indistinguishable from `GOGC=100` alone: the multiplier collects long before
  the limit binds, so the limit never fires. That is the correct behaviour for a
  safety net and confirms the limit costs nothing when it is not needed.

## Does it fit the instance?

RSS here is macOS/arm64. The capacity run measured the same workload on both and
found **x86 uses 15–19% LESS** (16 kB pages against 4 kB) — so these figures are
divided by 1.17 for a Linux estimate. That factor is **borrowed, not
re-measured here**.

An e2-small is 1.93 GiB. Budget after the OS (~250 MB) and PostgreSQL
(~106–140 MB): **~1,590 MB for the app.**

| n=500 config | est. Linux RSS | e2-small (1,590 MB budget) | e2-medium |
|---|---|---|---|
| `GOGC=100` | ~344 MB | 22% — fits | fits |
| **`GOGC=400` + 750MiB** | **~633 MB** | **40% — fits, bounded** | fits |
| `GOGC=400` alone | ~934 MB | 59% — fits, **unbounded** | fits |
| `GOGC=800` | ~1,520 MB | **96% — no headroom, ±18%** | fits |

**`GOGC=800` does not fit an e2-small.** 96% of budget with an 18% run-to-run
spread is an OOM waiting for a traffic peak, and the mandate's own rule applies:
an app that dies at n=500 on an e2-small is a worse product than one that is
33% slower.

## What should ship

**`GOMEMLIMIT` derived from detected machine memory at startup, with
`GOGC=400`.** Not a `sky.toml` knob as the primary mechanism, and not
documentation only — the failure it prevents is an OOM kill, which an operator
cannot debug from the outside and will not pre-empt by reading a doc.

The machinery already exists and is already correct. `runtime-go/rt/pg_embed_conf.go`
sizes PostgreSQL's `shared_buffers` from `detectRAMBytes()`, which consults
**cgroup v2 → cgroup v1 → `/proc/meminfo` → macOS `sysctl`**, in that order, and
its comment explains why the order matters: `/proc/meminfo` is not namespaced,
so a 512 MB container on a 64 GB node reads 64 GB. A GC limit derived from
`/proc/meminfo` would inherit exactly that bug.

```go
// at startup, before the first allocation-heavy work
if ram := detectRAMBytes(); ram > 0 {
    // The app is not alone: PostgreSQL and the OS want their share, and
    // pg_embed_conf already claims 15% of RAM for shared_buffers.
    debug.SetMemoryLimit(int64(float64(ram) * 0.55))
}
```

Constraints that must hold, each of which this run supports:

- **Only when unset.** An explicit `GOMEMLIMIT`/`GOGC` in the environment always
  wins; an operator who has sized it knows more than the heuristic does.
- **`GOGC` stays a multiplier**, at 400 rather than `off`. The `GOGC=off` arms
  show why: without it the process spends the entire budget at any load.
- **The fraction is the open question.** 0.55 fits the measured n=500 point on
  an e2-small with headroom, but this run did not sweep it.

### What would falsify this

- **A workload whose live heap legitimately exceeds the derived limit.** Every
  arm here stayed under its bound, so the thrash case — collector running
  continuously against a limit it cannot satisfy — is **untested**. It is the
  main risk of shipping a limit, and it is why `GOGC` must remain a multiplier.
- **A machine where `detectRAMBytes()` returns a figure the app does not own** —
  several apps on one host, each sizing to 55% of the same RAM.
- **`GOGC=400` not being the right multiplier.** 200 and 800 were measured;
  300 and 500 were not, and the optimum may not be a power of two.

## Conditions

| | |
|---|---|
| Commit | `628c08c5` on `feat/embedded-postgres`, worktree branch `perf/gogc-locks` |
| Host | **M1 Mac, 8 physical cores (no SMT), 16 GB, macOS 25.5** — generator co-resident over loopback |
| App | `forumbench` — `examples/19-skyforum` + the `init`-only view-size lever, `FORUM_POSTS=5`, **94 `sky-id` elements counted from the served HTML in every arm** |
| Store | **postgres**, the app's own embedded cluster (`--embed`), PostgreSQL **14.21** Homebrew, **`fsync=on`** — matching `gcp-x86-capacity-20260816`, not the `fsync=off` throwaway of the M1 rebaseline |
| Load | closed loop, `-think 0`, 20 s ramp, 8 s warmup, 45 s window, 100/300/500 sessions |
| Design | 12-cell grid × 2 blocks, **block B the reverse of block A**; GOMEMLIMIT and combined arms single |
| Generator | `tools/skyliveload` at this commit; `generator_possibly_saturated` **false in every arm** (4.1–6% of machine) |

### Method guards

Every arm **refuses rather than reports**: the port is free before start and the
listening pid is the pid launched; the store is read from the app's own banner
(`session store: postgres`) because Sky.Live's dev fallback silently degrades to
memory on an unreachable store; **`GOGC`/`GOMEMLIMIT` are read back from the
live process environment** via `ps -E`, never trusted from the launch line; the
view is 94 elements; and a patching self-check runs as a precondition before the
window opens. An RSS watchdog aborts an arm above 5 GB so a relaxed pacer cannot
take a 16 GB host down — **it never fired**.

`-max-error-rate 1.0` follows the corpus convention so a transient blip does not
discard an otherwise good arm; **the error rate is asserted at analysis instead**
(`analyse.sh`), and every arm read `error_rate 0`, `patch_rate 1.0`, 2 patches
per interaction, sessions established = requested.

**Cross-validation against the existing corpus**, which is what licenses
comparing these numbers to it: idle RSS **35.2 MB** against the capacity run's
M1 **34.8 MB**; per-session slope at `GOGC=100` **685 kB** against the published
M1 **615** and x86 **625–650**.

### Two harness defects, both caught

- **The port guard failed 4 of 6 arms of a confirmatory A/B** by refusing to
  start while the *previous* arm's app was still releasing the socket — a
  teardown race reported as a conflict. It now waits up to 60 s, then fails.
- **A sibling agent's cleanup SIGKILLed the wait-loop's `sleep`.** `CLAUDE.md`
  §2's `$3 == "sleep" && $2 != 1` predicate matches any agent's polling sleep,
  not only its own. Recorded here because it corrupts *other* agents' runs.

## Not measured — named, not assumed

- **x86/Linux.** Every arm is arm64/macOS. The 15–19% adjustment is borrowed
  from `gcp-x86-capacity-20260816`, not re-measured.
- **The memory store.** Deliberately — `gomaxprocs-scaling-20260816` covers it,
  and the point here was the store it did not measure.
- **`GOMEMLIMIT` under a live heap that exceeds the bound.** The thrash case.
  Untested, and the main risk of the recommendation.
- **n > 500**, and multi-replica. The `GOGC` slope predicts `GOGC=400` alone
  crosses an e2-small's budget somewhere past n≈800; not verified.
- **`GOGC` between 200 and 800** — 300 and 500 unswept, so "400 is optimal" is
  not established, only that it beats 200 and 800 on the memory/throughput
  trade at these session counts.
- **Absolute throughput against x86.** This is an M1; the corpus's ~2.96×
  M1 : x86 factor is unchallenged here. **The ratios are all within-box and do
  not depend on it.**

## Layout

```
results.tsv     one row per arm, with the analysis-time validity verdict
runs/<tag>/     per-arm load.json, acct.txt, 1 Hz rss.tsv
harness/        every script, including the mutation prover
```
