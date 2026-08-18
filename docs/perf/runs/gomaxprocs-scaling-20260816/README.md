# Does Sky.Live's throughput scale with cores?

**Yes — very nearly linearly with *physical* cores.** The observation that
prompted this run (1.0 dedicated core → 299 int/s, e2-medium's 1.85 app-cores →
345 int/s: ~1.85× the CPU for ~1.15× the throughput) does **not** survive a
same-box test. It was a cross-instance-family artefact, and the agent that
flagged it as indicative rather than conclusive was right to.

On one machine, one binary, one generator, one session count, arms
counterbalanced across three blocks:

| GOMAXPROCS | median int/s | range (3 runs) | speedup vs 1 | app_cores | CPU µs/interaction |
|---|---|---|---|---|---|
| **1** | **484.1** | 475.2 – 485.3 | 1.00 | 0.91 | 1,876 |
| **2** | **865.5** | 853.2 – 881.3 | **1.79** | 1.80 | 2,083 |
| **4** | **1,538.8** | 1,534.0 – 1,553.5 | **3.18** | 3.51 | 2,282 |
| **8** | **1,796.2** | 1,792.8 – 1,808.6 | **3.71** | 6.83 | 3,803 |

Ranges are **≤ 3.3%** of the median at every level and the three blocks agree
to within 2% arm-for-arm, so the curve is not a noise artefact.

## Verdict

**Roughly linear to 4 threads; the 4 → 8 flattening is hyperthreading, not the
application.** Confidence: **high**, for the reason below — the flattening was
reproduced under CPU pinning, where the number of threads is held constant and
only the *physical* cores they sit on changes.

`e2-standard-8` does not have 8 cores. `lscpu` reports **4 cores per socket,
2 threads per core**; `thread_siblings_list` pairs cpu0/4, 1/5, 2/6, 3/7. So
GOMAXPROCS 1 → 4 walks up four distinct physical cores, and 4 → 8 adds their
SMT siblings.

Same GOMAXPROCS = 4, same load, same binary, only the affinity mask changed:

| pinned to | int/s (3 runs) | |
|---|---|---|
| `0,1,2,3` — four **distinct physical cores** | 1,556.5 / 1,568.5 / 1,582.5 | median **1,568.5** |
| `0,4,1,5` — **two** physical cores, both threads each | 1,084.7 / 1,102.0 / 1,097.0 | median **1,097.0** |

Four threads on two physical cores deliver **70%** of four threads on four
physical cores. Against the unpinned GOMAXPROCS=2 figure (865.5, which Linux
spreads across two distinct cores), the second SMT thread on each core is worth
**1.27×** — and the measured 4 → 8 step is **1.17×**. That is what SMT does; it
is not a scaling defect.

**Per physical core, throughput scales at 79–80% efficiency per doubling**
(1.00 → 1.79 → 3.18).

### What this means for the target

**A larger instance is a legitimate route, and the sizing guidance should count
physical cores, not vCPUs.** Any capacity number derived from a GCE vCPU count
overstates the machine by roughly the SMT factor. `AGENTS.md`'s instance table
should say so.

Per-interaction cost is **not** the only lever — but it remains the *best* one,
because it compounds with cores and because two cheap levers were measured here
that do not require a bigger machine at all (below).

## If it is sub-linear, why — the named causes, with evidence

Three separate instruments were run. They disagree with each other in a way
that is itself the finding, so all three are reported.

### 1. A mutex profile at GOMAXPROCS=8 names four locks — and they are real

`runtime.SetMutexProfileFraction(1)`, sampled as a **delta across the
measurement window** (both profiles are cumulative from process start, so the
start reading is subtracted; what is reported is contention *during* the load,
not contention plus startup). Full output in `profiles/g8.txt`.

| accumulated delay | share | site |
|---|---|---|
| **226.34 s** | **39.6%** | `rt.(*memoryStore).Set` → `sync.(*RWMutex).Unlock` — `memMu`, `live_store.go:579`. One process-wide RWMutex, write-locked on **every** interaction's session write. |
| **131.58 s** | **23.0%** | `rt.(*sessionLocker).Lock` (110.91 s) + `.Unlock` (20.67 s) — `live.go:1942`. The per-session entry `e.mu` is correctly per-session, but the **map guard `s.mu` is process-wide and is taken twice per interaction**. |
| **151.14 s** | **26.5%** | `rt.setGoroutineLiveSession` → `sync.Map.Store` (92.64 s) + `rt.clearGoroutineLiveSession` → `sync.Map.Delete` (58.50 s). A process-wide `sync.Map` keyed by goroutine id, written **and** deleted once per interaction. |
| **34.50 s** | **6.0%** | `rt.WithMsgSpanTraced` → `telemetry.Tracer` → otel `(*TracerProvider).Tracer` — a lock taken per span. |

The same profile at **GOMAXPROCS=1 totals 39.97 microseconds** — a 14-million-fold
difference, i.e. the contention is entirely a parallelism effect, as expected.

**`Std.Ui.Lazy`'s LRU does not appear at all**, at any level. Checked before
blaming it, as instructed: `examples/19-skyforum` contains no `lazy` call, so
the 1024-cap LRU behind `lazyCacheMutex` (`lazy.go:48`) is **not on this app's
path**. It may still matter for an app that uses `Ui.lazy`; this run says
nothing about that.

**Contention on a single session is ruled out.** Throughput is nearly flat in
session count — at GOMAXPROCS=8: n=25 → 1,704.9, n=100 → 1,796.2, n=400 →
1,898.0. The sweep drove 100 distinct sessions and was never serialising on one
`sess.mu`.

### 2. …but lock *waiting* is not where the CPU goes

Contended locks show up in CPU as `lock2` / `futex` / `procyield`. Summed:

| | GOMAXPROCS=1 | 4 | 8 |
|---|---|---|---|
| lock2 + futex + procyield | **absent from the profile** | 1.8% of CPU | **4.7% of CPU** |

Meanwhile the CPU cost of an interaction rises from **1,876 µs to 3,803 µs**
(2.03×) between 1 and 8 threads, and it rises **across every path at once** —
`reflect.Value.call` 1,185 → 2,134 µs/int, `mallocgc` 624 → 1,091,
`systemstack` 547 → 1,361, `gcBgMarkWorker` 245 → 473. That uniform inflation
is a memory-system and collector signature, not one lock.

### 3. GC is the largest single lever measured, and it is level-independent

`GOGC` sweep, same everything else:

| | GOGC=100 (default) | GOGC=400 | GOGC=800 | gain |
|---|---|---|---|---|
| GOMAXPROCS=8, n=100 | 1,805.1 | 2,280.1 | **2,404.0** | **+33%** |
| GOMAXPROCS=4, n=100 | 1,561.8 | — | **2,143.1** | **+37%** |
| GOMAXPROCS=1, n=100 | 467.5 | — | **621.6** | **+33%** |

`app_cores` is unchanged across the GOGC arms at GOMAXPROCS=8 (6.79 / 6.74 /
6.81) — **the same CPU does a third more work.**

The Go runtime's own `gctrace` reports GC at **8% of CPU at both GOMAXPROCS=1
and 8**, so the 33% is not GC's direct CPU. It is the collector's *duty cycle*:
at GOMAXPROCS=8 the app runs **2,092 GC cycles in 92 s (23/s)** against 750 at
GOMAXPROCS=1, with a mark phase of 11–12 ms per ~26 ms cycle — the write
barrier is on for roughly **45% of wall time**. Relaxing the pacer removes
barrier duty, not collector CPU.

**The gain is the same 1.33–1.37× at 1, 4 and 8 threads.** GC is therefore a
*per-interaction cost* lever, not a parallel-scaling limiter. It does not
explain the shape of the curve; it shifts the whole curve up.

### 4. Sharding into processes buys 33% — and GOGC buys the same 33%

The direct test of whether the ceiling is inside one address space. Same box,
same 8 hyperthreads, same 100 total sessions, same binary; only the number of
processes changes, so every process-wide lock, the Go heap and the GC pacer are
duplicated rather than shared.

| topology | int/s (3 runs) | median | vs 1×8 |
|---|---|---|---|
| **1 process × 8 threads**, 100 sessions | 1,796.2 / 1,785.1 / 1,819.7 | **1,796** | 1.00 |
| **2 processes × 4 threads**, 50 sessions each | 2,093.1 / 2,077.0 / 2,092.1 | **2,092** | **1.16** |
| **4 processes × 2 threads**, 25 sessions each | 2,250.7 / 2,198.3 / 2,270.7 | **2,251** | **1.25** |
| **8 processes × 1 thread**, 12 sessions each | 2,415.3 / 2,384.0 / 2,383.2 | **2,384** | **1.33** |

Eight independent single-threaded processes reach **4.92×** the single
single-threaded process — against a hardware ceiling of about 5× (4 physical
cores × the measured 1.27 SMT factor). **Shared-nothing extracts essentially
all of the hardware.**

But note the two numbers side by side:

> **8 processes × 1 thread = 2,384 int/s.
> One process × 8 threads with `GOGC=800` = 2,404 int/s.**

One environment variable buys what the whole sharded topology buys. The
intra-process penalty is dominated by the **shared heap and its collector**, not
by the four locks the mutex profile names — which is why removing the locks
(sharding) and removing the barrier duty (GOGC) land on the same number.

## For the shared-nothing proposal specifically

The coordinator framed this as "if a lock dominates, fix the lock; if it is GC,
sharding cannot help". The measurement does not split that way, so here it is
plainly:

- **Locks are genuinely contended** — `memMu`, `sessionLocker.mu` and the
  goroutine→session `sync.Map` are process-wide and are each hit on every
  interaction. That is a real defect and worth fixing on its own merits.
- **They are not what caps throughput.** Lock spin is 4.7% of CPU at 8 threads;
  fixing all four cannot plausibly return 33%.
- **Sharding does help — by 33%** — which contradicts "if it is GC, sharding
  cannot help". The mechanism is that separate processes have separate heaps and
  separate pacers, not that they dodge the locks.
- **The same 33% is available for one env var,** without an architecture. So the
  shared-nothing rebuild is **not justified by this data** as the route to that
  33%. Reducing allocation per interaction earns the same thing permanently and
  compounds with cores.

**On the DB-pool half: this run cannot speak to it, and does not contradict the
x86 finding.** Every arm here ran `SKY_LIVE_STORE=memory`, deliberately, to
isolate application CPU. There is **no PostgreSQL in this corpus at all**, so
`backends_max` 7–8 against `max_connections=56` stands unchallenged. Do not read
this run as support for or against per-core DB pools.

## Was the generator the bottleneck? No — shown, not asserted

Established three ways, at every point:

1. **The generator ran on its own 8-vCPU box** (`skygmp-gen`, same zone,
   internal IP, ~0.2 ms hop), so it never competed for the app's cores — which
   matters precisely at GOMAXPROCS=8, where sharing a host would have
   manufactured the sub-linear result under test.
2. **`skyliveload`'s own `getrusage` accounting**: 2.2% of its machine at
   GOMAXPROCS=1 rising to only **6.1% at GOMAXPROCS=8**; its
   `generator_possibly_saturated` flag (trips at 70%) is **false in all 47
   recorded runs**.
3. **The generator box's `/proc/stat` busy fraction**, measured independently
   across exactly the load window: **1.9% → 6.9%**, peaking at 9.9% in the
   8-process arm.

The generator had **>90% headroom at every point on the curve.**

## Method guards, and the two defects they caught

Carried from the capacity harness and extended:

- `-hid-context '>▲<'` names the handler by rendered text; `-setup` scripts the
  sign-in the vote handler needs; `-min-patch-rate 0.9` refuses a run that
  produced no patches; a four-interaction `-self-check` runs as a
  **precondition** before every measurement window. **All runs report
  `valid: true`, `patch_rate 1.0`, 2 patches per interaction, error rate 0.**
- The app's **session store is asserted from its own startup banner**, and
  **`GOMAXPROCS` is read back from `/proc/<pid>/environ`** — an arm that
  silently ran at the wrong level would have produced exactly the flat curve
  this run exists to test for.
- The app pid comes from **`pgrep -x app` cross-checked against the `:8000`
  listener pid**, per the prior run's `pgrep -f` defect.
- **`/proc/stat` tick rate was measured, not owed**: 800 jiffies/s against
  8 × 100 owed, so CPU attribution from inside this guest is sound — unlike the
  65–94 Hz seen on the small shared instances of the capacity run.

**Defect 1 — a leftover binary served an arm.** The first two-process attempt
asserted `expected 2 app processes, found 1` and was discarded. Cause:
`pkill -x app` does not match `app-prof`, so the **instrumented GOMAXPROCS=1
binary left over from the profiling arm still held :8000**, and that arm's two
ports read 415 and 1,305 int/s. Without the pid-count assertion this would have
been recorded as a valid, patch-bearing, plausible 1,721 int/s. Fixed by killing
both executable names; the discarded attempt is not in `results.tsv`.

**Defect 2 — zsh `noclobber` served me a stale file for three cycles.** Two
profiling batches appeared to run and report *identical* throughput to 13
decimal places. `>` had silently refused to overwrite an existing
`prof.log` (`file exists:`) so the block never ran and I was re-reading the
previous batch. The first batch had also produced no profiles at all, because
`remote_prof.sh` was written after provisioning and never uploaded. Both fixed;
harness scripts use `>|` throughout.

**A window-selection defect was caught before it mattered.** The sampler
outlives the load by ~45 s so a slow teardown cannot truncate it, which made a
`tail -N` window average the app's *idle tail* into its CPU — the trial read
`app_cores 0.747` at GOMAXPROCS=1 where the raw jiffy slope over the load was
1.05. The window is now selected from the trace itself (rows at the run's
plateau connection count), and `app_cores` is recomputed at analysis time
rather than trusted from the driver.

## Conditions

| | |
|---|---|
| Commit | `573ae3e2` on `feat/embedded-postgres`, worktree branch `perf/gomaxprocs-sweep` |
| App box | `skygmp-app` — **e2-standard-8**, us-central1-a, project `settleby` |
| Generator box | `skygmp-gen` — **e2-standard-8**, same zone, internal IP |
| Both | created with `--max-run-duration=4h --instance-termination-action=DELETE` **at creation**, verified by `describe` (`maxRunDuration: 14400s`), and **both confirmed deleted** at teardown |
| CPU | **AMD EPYC 7B12** — 4 cores / 8 threads, 1 NUMA node, `cpu.max` **absent** (no cgroup quota) |
| App | `forumbench` — `examples/19-skyforum` + the `init`-only view-size lever. **sha256 `168f4d5f9968c1f4efb230ab4a1ca655fd7f6337c1044094d2daebae809ce782` — byte-identical to the binary the x86 capacity run measured**, and reproduced bit-for-bit from its source tree before use |
| Instrumented variant | `app-prof`, sha256 `1a1ff304…` — the same emitted package plus `harness/zz_gmpprobe.go` (mutex/block rates + a pprof port, all off unless `SKY_PROBE_ADDR` is set). Its throughput matches the plain binary to **−1.4% / +0.2% / −3.0%** at 8 / 4 / 1, so the instrument's cost is measured rather than assumed |
| Generator | `tools/skyliveload` at this commit, cross-compiled linux/amd64 |
| View size | **94 `sky-id` elements**, counted from the HTML the app served in each run |
| Interaction | signed-in upvote toggle — **2 patches, every press** |
| Store | `memory` (deliberately — isolates application CPU; **no PostgreSQL in this corpus**) |
| Load | closed loop, `-think 0`, 100 sessions, 15 s ramp, 8 s warmup, 45 s window |
| Design | 4 levels × 3 blocks, **arm order permuted between blocks** (1,2,4,8 / 8,4,2,1 / 2,8,1,4) so no level sits at the same sequence position twice |
| Burst credits | **not applicable** — e2-standard vCPUs are dedicated, not burstable. This is why the sweep was not run on e2-small/medium |

## Not measured — named, not assumed

- **PostgreSQL.** Every arm used the memory store. Nothing here confirms or
  contradicts the capacity run's `backends_max` 7–8 / 0.08–0.12 pg-cores.
- **Whether fixing the four named locks would change the curve.** The mandate
  was to measure, not implement. Their 4.7%-of-CPU spin cost bounds the
  available gain from below, but a counterfactual was not run.
- **More than 4 physical cores.** e2-standard-8 is 4 cores; whether the 79–80%
  per-doubling efficiency holds at 8 and 16 *physical* cores is untested. It is
  the obvious next sweep and needs an `n2`/`c3` instance, not an `e2`.
- **`Std.Ui.Lazy`'s LRU under an app that uses it.** Absent from this app's
  path, so unexercised.
- **Any arm on the M1.** Everything here is x86; the existing corpus's
  M1 : x86 factor of ~2.96× is unchallenged and unre-measured.
- **The absolute numbers against the capacity run's Xeon.** This box is an AMD
  EPYC 7B12; its single dedicated *thread* reads 484 int/s where the capacity
  run's Xeon read 299. That is a CPU difference and is not a correction to the
  Xeon figure. **The scaling ratios, which are all within-box, do not depend on
  it.**

## Layout

```
results.tsv         the 12-run counterbalanced sweep, one row per run
sweep.log           its driver output
pin.log             the SMT affinity experiment
shard2.log          2 processes x 4 threads
shardk.log          4x2, 8x1, and the 1x8 control
diag.log            GOGC sweep at 8, session sensitivity, gctrace arms
gogclvl.log         GOGC=100 vs 800 at GOMAXPROCS 1 and 4
prof.log            the profiling arms
profiles/g{1,4,8}.txt   mutex delta, block delta, CPU flat + cum, rendered
                        against the instrumented binary (98 MB, not committed)
runs/<tag>/         per-run load.json, idle assertion, 1 Hz sample, app.log
harness/            every script, including zz_gmpprobe.go
```
