# Sky.Live capacity on x86 GCE — the mandate's number, measured

Every millisecond in this performance programme before today was measured on an
Apple M1. The e2-small guidance in `AGENTS.md` carried an **inferred** ~30%
memory adjustment and **no CPU adjustment whatsoever**, while the interaction
had become several times cheaper than the figure that guidance rested on. This
run settles the target on the hardware the target names.

The acceptance criterion, from `.claude/AUTONOMOUS_GOAL.md`:

> to be able to serve 3-500 concurrent users with 1k+ interactions per second
> in a small instance -- or very close to this

with **one embedded PostgreSQL carrying sessions, application data, analytics
and metrics**.

## Verdict

**Not met, and not close — short by ~15× on the literal target.**

| | measured (median, sustained) | target | shortfall |
|---|---|---|---|
| **e2-small**, 300 sessions, embedded PostgreSQL | **64.3 int/s** (55.5–69.0) | 1,000+ | **15.6×** |
| **e2-small**, 500 sessions, embedded PostgreSQL | **65.7 int/s** (63.5–66.0) | 1,000+ | **15.2×** |
| e2-medium, 300 sessions, embedded PostgreSQL | 261.5 int/s (255.1–269.1) | 1,000+ | 3.8× |
| e2-medium, 500 sessions, embedded PostgreSQL | 245.2 int/s (222.9–248.0) | 1,000+ | 4.1× |

An architecture consult had judged **e2-medium the cheapest configuration that
meets the literal number**. That judgement is **falsified**: e2-medium serves
~250 int/s at the mandated concurrency, not 1,000. The next rung buys **3.8×**
over e2-small, which is a large and real gain — but it lands a quarter of the
way to the target, not at it.

The binder is **CPU inside the Sky application process**, and it is not close:
PostgreSQL consumes **0.08–0.12 cores** while the app consumes **1.30–1.90**.

## What binds, in order

1. **Application CPU.** `app_cores` is flat at 1.62–1.65 (e2-medium, postgres)
   across n = 100, 300 and 500 while throughput falls and latency rises
   linearly — the signature of a saturated server with a growing queue. On
   e2-medium that is 81–83% of the whole 2-vCPU machine.
2. **Shared-core scheduling on e2-small**, which is a *second, separate*
   ceiling below the first — see "The e2-small accounting anomaly" below.
3. **PostgreSQL: not a binder at any rate reached here.** 0.08–0.12 cores,
   6–7% of what the app burns. `xact_per_s` tracks throughput ~1:1 (285 int/s →
   282 commits/s), which confirms the documented one-commit-per-interaction
   session write is happening and is simply not expensive at 250/s.
4. **The connection pool: not a binder, and invariantly so.** `backends_max` is
   **7 (occasionally 8) in every single postgres run** — at 100, 300 and 500
   concurrent sessions alike, against `max_connections = 56`. The pool never
   grows because the app never has more than ~7 session writes in flight; it is
   downstream of the CPU ceiling, not a cause of it.
5. **Memory: not a binder.** At n = 500 on e2-medium the app holds 364–374 MB
   with `MemAvailable` still above 3.0 GB.

### Where interactions start FAILING rather than slowing

| | n=100 | n=300 | n=500 |
|---|---|---|---|
| e2-small, postgres | 0% | **0.08 – 0.65%** | **1.68 – 2.09%** |
| e2-small, memory | 0% | 0% | 0 – 0.16% |
| e2-medium, postgres | 0% | 0% | **0%** |
| e2-medium, memory | 0% | 0% | 0% |

**e2-small's failure knee with the full topology sits between 100 and 300
sessions**, and is decisive by 500. **e2-medium's knee is above 500** — it
degrades to 1.4 s p50 without dropping anything.

## The x86-vs-M1 factor — every prior M1 figure is now translatable

Measured on the same app, same generator, same handler, same 94 elements, same
memory store, `GOMAXPROCS=1` on both, generator on the same host over loopback
on both:

| host | interactions/sec | CPU-ms per interaction |
|---|---|---|
| Apple M1 (arm64) | **884.5** (858.9 – 897.7) | 1.131 |
| GCE dedicated x86 core, Xeon @ 2.20 GHz | **299.3** (288.9 – 299.7) | 3.347 |

> **M1 : x86 per-core factor = 2.96×.** Divide any M1 interaction rate in this
> corpus by ~3 to get a GCE-core rate; multiply any M1 millisecond by ~3.

The x86 arm ran on `skyperf-core`, an **e2-standard-4 with four dedicated
vCPUs** — deliberately not a shared-core instance, so the per-core figure
carries no burst or contention confound. Its app pinned 1.0036 cores in all
three repeats, so this is a clean saturated-core cost.

**Memory travels the other way, and the published adjustment has the wrong
sign.** Same workload, 25 sessions: M1 idle RSS 34,752 kB / loaded 63,328 kB;
x86 idle 28,272 kB / loaded 53,956 kB. **x86 uses 15–19% LESS**, not ~30% more.
macOS's 16 kB pages against Linux's 4 kB is the likely mechanism. `MEASURED`.

### Per-session memory slope, x86, under load

OLS-free slope across n = 100 → 500 (not any single level's ratio, which
charges the fixed load-time growth to that level's sessions):

| store | kB per session |
|---|---|
| memory | **451 – 531** |
| postgres | **625 – 650** |

The programme's prior marginal input was 585 kB/session on postgres, measured
on M1; x86 reads 7–11% higher. Both are far from the 1.35–1.42 MB RSS-derived
figure that `AGENTS.md` still carries, and from the 336 kB idle-live-heap one.

## Conditions

| | |
|---|---|
| Commit | `3ed83c08` on `feat/embedded-postgres` (Stage 2 typed-HOF merge) |
| Targets | `skyperf-small` (**e2-small**, 2 shared vCPU / 0.5 baseline, 1.93 GiB) · `skyperf-medium` (**e2-medium**, 2 shared vCPU / 1.0 baseline, 3.83 GiB) |
| Per-core reference | `skyperf-core` (**e2-standard-4**, 4 **dedicated** vCPU) |
| Generator | `skyperf-gen` (**e2-standard-4**), **same zone**, internal IP — a ~0.8–1.5 ms hop, not the ~111 ms UK→us-central1 RTT every earlier remote run in this corpus carries in its latency columns |
| All | `us-central1-a`, project `settleby`, Debian 12, kernel 6.1.0-52, Intel Xeon @ 2.20 GHz, **no cgroup CPU quota** (`/sys/fs/cgroup/cpu.max` absent — real-hardware numbers, not the container-quota kind an earlier run found optimistic by 2.5–5×) |
| App | `forumbench` — `examples/19-skyforum` + the `init`-only view-size lever, **byte-identical to `../forum-rebaseline-20260816/`'s**, cross-compiled `GOOS=linux GOARCH=amd64` |
| View size | **94 `sky-id` elements**, counted from the HTML the app served during each run, never from an expectation |
| Interaction | signed-in upvote toggle — 2 patches, every press |
| PostgreSQL | the app's **own embedded cluster** (`./app --embed --data-dir …`), PostgreSQL 15.19 binaries, **`fsync = on`, `synchronous_commit = on`** — the embedded config generator deliberately leaves durability alone (`pg_embed_conf.go:13`) |
| Load | closed loop, `-think 0`, 25 s ramp, 10 s warmup, **90 s window** |
| Design | **3 blocks, back to back, no idle gaps**, store order alternating (mem→pg, pg→mem, mem→pg) so store is not confounded with position or burst state |
| Repeats | 3 per cell; **median and full range reported**, never a single run |

### The burst effect is real and it is large

`small / memory / n=100` read **226.6** in block 1 and **108.6 / 120.2** in
blocks 2 and 3 — the block-1 run was the first load this instance had ever
seen, and it reads **2× high**. This is exactly the trap the counterbalancing
exists for. **Every figure quoted as sustained above is the median of all three
blocks**, and the rested-instance number is never the answer.

A **seven-run soak** then measured the decay directly. e2-small, embedded
PostgreSQL, n = 300, seven runs back to back with no idle gap, the first of
them after the instance had sat idle for **3 h 17 min** and had therefore
recharged completely:

| run | 1 | 2 | 3 | 4 | 5 | 6 | 7 |
|---|---|---|---|---|---|---|---|
| int/s | **183.5** | 67.0 | 69.0 | 58.1 | 64.6 | 70.5 | 70.2 |
| errors | 0.01% | 0.12% | 0.14% | 0.25% | 0.12% | 0.13% | 0.08% |
| commits/s | 100.4 | 59.2 | 58.4 | 58.6 | 57.1 | 58.6 | 57.5 |

**The rested run reads 2.7× the sustained figure and is gone by the second
run.** Runs 2–7 hold flat at 58–71 int/s (median 69.0), which independently
reproduces the matrix's sustained 55.5–69.0 from a different day-part and a
different starting credit state. `commits/s` collapses in lockstep — further
confirmation that PostgreSQL is following the app, not leading it.

Anyone quoting a first-run e2-small number is quoting something the instance
cannot do for two minutes together.

## Method guards — and the two defects they caught

Both halves of the harness the forum re-baseline hardened are carried here:
`-hid-context` names the handler by rendered text, `-setup` scripts the sign-in
the vote handler needs, `-min-patch-rate 0.9` refuses a run that produced no
patches, a four-interaction `-self-check` runs as a **precondition** before the
measurement window opens, and the store the app *actually opened* is asserted
from its own startup banner. **All 47 recorded runs report `valid: true` and
the store they were asked for.**

`patch_rate` is 1.000 on every e2-medium run and on every unsaturated e2-small
run. Where it dips (0.979–0.999) it is exactly `1 − error_rate`, on the
overloaded e2-small rows only: **the missing patches ARE the failed
interactions**, not a handler that stopped patching. All stay above the
generator's `-min-patch-rate 0.9` floor, so the runs are data rather than
rejects — the failures are the finding.

Two defects were found and fixed *during* this run, both of the class this
branch keeps meeting:

**1. `pgrep -f … | head -1` was measuring the `sudo` wrapper, not the app.**
The app is started `sudo -u skybench env … /opt/skybench/app`, so `-f` matches
two processes and `head -1` takes the lower pid — the wrapper. Measured on the
box: wrapper `comm=sudo rss=11956 kB`, **1 jiffy for a whole run**; app
`comm=app rss=23400 kB`. Every RSS would have been a constant ~12 MB and every
CPU delta ~0. Fixed to `pgrep -x app`, cross-checked against the `:8000`
listener pid from `ss`. The first smoke run had already recorded the wrapper's
numbers before this was caught.

**2. The CPU denominator was `/proc/stat`, which does not tick at
`nproc × 100 Hz` on these instances.** See below. The inline analysis produced
"the app used 158.9% of a machine that was 58.4% busy". The archived 1 Hz
samples let this be re-derived offline (`harness/analyse.sh`) against **wall
time**, in **cores**, with the window taken as the loaded plateau
(`conn ≥ 0.8 × max`) rather than a fixed tail that included post-load idle.

A third, in the tooling around the run: `cargo build --release -p sky` piped
into `tail` reported **exit 0 having built nothing** (wrong cwd, no
`Cargo.toml`) — the precise failure mode `scripts/lib/with-timeout.sh` was
written about. Caught by checking for the binary rather than the status.

### The e2-small accounting anomaly — read `app_cores` on small with care

`stat_tick_hz` is the rate `/proc/stat`'s aggregate accumulated jiffies:

| | stat_tick_hz under load | should be |
|---|---|---|
| e2-medium | 159.5 – 200.7 | 200 |
| **e2-small** | **64.9 – 94.1** | 200 |

The e2-small guest loses **more than half its clock ticks under load** — the
hypervisor is descheduling it hard enough that tick-based accounting stops
being trustworthy, and its per-process and system-wide counters disagree
(`app_cores` 1.30–1.74 against `machine_busy_cores` 0.60–0.86, which cannot
both be right). **CPU attribution on e2-small is therefore indicative only.**
Its throughput, latency and error columns are generator-side and unaffected;
they are what the e2-small verdict rests on. e2-medium's accounting is sound
and is where the CPU attribution is quoted from.

This is itself a finding: an operator cannot diagnose CPU saturation on an
e2-small from inside the guest.

## Known limits — what this does NOT settle

1. **The single-DB topology is only partly exercised.** `19-skyforum` uses no
   `Std.Db`: its posts live in the TEA model, so the application data *does*
   round-trip through the session gob into PostgreSQL on every interaction (the
   heaviest reading of "sessions + application data"), but **analytics and
   metrics are not separately written**. The mandate names four workloads on
   one cluster; three are represented. The measured figures are therefore an
   **upper bound** for the full four-workload topology, not a like-for-like.
2. **The `fsync` contrast is UNMEASURED, and was REFUSED rather than faked.**
   A `pg` vs `pgnofsync` arm ran on e2-medium; the `pg` half landed three clean
   repeats (294.0 / 295.8 / 297.4 int/s at n=100, `fsync=on` asserted). The
   `pgnofsync` half **flipped `fsync` and `synchronous_commit` off via
   `ALTER SYSTEM` + `pg_reload_conf()`, read the settings back, found them
   still `on`, and rejected all three runs** —
   `json/medium-pgnofsync-n100-bfsr{1,2,3}.REJECTED`. The reload did not take
   on the embedded cluster; the cause is not diagnosed here. **What PostgreSQL's
   durability costs is therefore not known**, and the honest record is three
   rejection markers rather than three rows measuring a cluster that was still
   fsyncing.

   What *is* measured: PostgreSQL's total CPU is 0.08–0.12 cores against the
   app's 1.6, and the whole-store cost (postgres vs memory, same instance,
   counterbalanced) is 12–16% on e2-medium.

   Note the M1 corpus cannot answer it either: `forum-rebaseline`'s bench
   cluster ran `fsync = off` **and** `synchronous_commit = off`
   (`harness/pg-up.sh`), so **no M1 postgres figure in this corpus contains a
   WAL fsync at all**. Every postgres number in THIS run does.
3. **Closed loop (`-think 0`) measures the ceiling.** This is the *generous*
   read: it is the most a box can do. A think time only lowers offered load, so
   "target missed by 15×" holds a fortiori.
4. **One interaction shape**, one view size (94 elements), one app.
5. **e2 shared-core instances vary with host contention**, and three blocks
   over ~65 minutes is what bounds that here, not eliminates it.

## Layout

```
results.tsv        one row per run, 36 rows, generator-side + idle-side columns
system.tsv         re-derived per-run system metrics (harness/analyse.sh)
samples/<tag>.tsv  the 1 Hz trace behind every run
json/<tag>.json    the generator's own result record for every run
json/<tag>.idle.txt the asserted store + idle RSS/PSS banner for every run
m1/                the M1 side of the per-core factor (m1.tsv + 3 runs)
core/              the x86 dedicated-core side (results.tsv + 3 runs + scripts)
harness/           every script that ran, including the two remote-side ones
```

`results.tsv` columns are documented in `harness/runone.sh`; `system.tsv`'s in
`harness/analyse.sh`. **`app_cpu_pct` / `pg_cpu_pct` / `cpu_busy_pct` in
`results.tsv` are the DEFECTIVE inline versions** described above and are kept
only so the correction is auditable — use `system.tsv`'s `app_cores` /
`pg_cores` instead.

## Teardown

All four instances carried `--max-run-duration` with
`--instance-termination-action=DELETE` from creation, so an abandoned run could
not bill indefinitely. See `teardown.txt` for the verification, verbatim.
